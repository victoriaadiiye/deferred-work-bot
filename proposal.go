package main

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"strconv"
	"strings"
)

// proposalPage renders a single proposal in full — no truncation — so the
// drafted ticket can be reviewed before it is filed. Accepts either ?id=<proposalID>
// (a specific draft) or ?item_id=<itemID> (the item's latest draft). Sibling
// proposals for the same item are linked so superseded drafts stay reachable.
func (h *HealthServer) proposalPage(w http.ResponseWriter, r *http.Request) {
	var p *Proposal
	var err error
	switch {
	case r.URL.Query().Get("id") != "":
		var id int64
		id, err = strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
		if err != nil {
			http.Error(w, "bad id", 400)
			return
		}
		p, err = h.deps.Store.GetLatestProposalByID(id)
	case r.URL.Query().Get("item_id") != "":
		var id int64
		id, err = strconv.ParseInt(r.URL.Query().Get("item_id"), 10, 64)
		if err != nil {
			http.Error(w, "bad item_id", 400)
			return
		}
		p, err = h.deps.Store.GetLatestProposal(id)
	default:
		http.Error(w, "id or item_id required", 400)
		return
	}
	if err != nil || p == nil {
		http.Error(w, "proposal not found", 404)
		return
	}

	it, _ := h.deps.Store.GetItemByID(p.ItemID)
	siblings, _ := h.deps.Store.ListProposalsForItem(p.ItemID)

	jiraBase := h.jiraBaseURL()

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, pageHead)
	fmt.Fprintf(w, `<h1>Proposal #%d</h1>`, p.ID)
	fmt.Fprintf(w, `<p class="filter-note"><a href="/">&larr; Dashboard</a>`)
	if it != nil {
		fmt.Fprintf(w, ` &middot; <a href="/logs?item_id=%d">Item #%d event log</a>`, it.ID, it.ID)
	}
	fmt.Fprint(w, `</p>`)

	// Proposal meta line.
	fmt.Fprintf(w, `<p><span class="badge %s">%s</span>`, html.EscapeString(statusBadgeClass(p.Status)), html.EscapeString(p.Status))
	fmt.Fprintf(w, ` &middot; branch <strong>%s</strong>`, html.EscapeString(p.Branch))
	fmt.Fprintf(w, ` &middot; created %s</p>`, p.CreatedAt.Format("2006-01-02 15:04"))

	// Originating request, in full.
	if it != nil {
		fmt.Fprint(w, `<h2 class="prop-h">Request</h2>`)
		fmt.Fprintf(w, `<pre class="prop-body">%s</pre>`, html.EscapeString(it.Text))
	}

	// The drafted ticket, every field untruncated.
	var d Draft
	if p.DraftJSON != "" {
		json.Unmarshal([]byte(p.DraftJSON), &d)
	}
	if d.Summary != "" || d.Description != "" {
		fmt.Fprint(w, `<h2 class="prop-h">Drafted ticket</h2>`)
		fmt.Fprint(w, `<table class="prop-fields"><tbody>`)
		propRow(w, "Summary", html.EscapeString(d.Summary))
		propRow(w, "Issue type", html.EscapeString(d.IssueType))
		propRow(w, "Priority", html.EscapeString(d.Priority))
		if len(d.Labels) > 0 {
			propRow(w, "Labels", html.EscapeString(strings.Join(d.Labels, ", ")))
		}
		if d.EpicKey != "" {
			epic := html.EscapeString(d.EpicKey)
			if jiraBase != "" {
				epic = fmt.Sprintf(`<a href="%s/browse/%s" target="_blank">%s</a>`, jiraBase, html.EscapeString(d.EpicKey), html.EscapeString(d.EpicKey))
			}
			if d.EpicSummary != "" {
				epic += " — " + html.EscapeString(d.EpicSummary)
			}
			propRow(w, "Epic", epic)
		}
		fmt.Fprint(w, `</tbody></table>`)
		if d.Description != "" {
			fmt.Fprint(w, `<h2 class="prop-h">Description</h2>`)
			fmt.Fprintf(w, `<pre class="prop-body">%s</pre>`, html.EscapeString(d.Description))
		}
	}

	// Related tickets.
	var rels []RelatedTicket
	if p.RelatedTicketsJSON != "" {
		json.Unmarshal([]byte(p.RelatedTicketsJSON), &rels)
	}
	if len(rels) > 0 {
		fmt.Fprint(w, `<h2 class="prop-h">Related tickets</h2>`)
		fmt.Fprint(w, `<table><thead><tr><th>Key</th><th>Verdict</th><th>Summary</th></tr></thead><tbody>`)
		for _, rt := range rels {
			keyCell := html.EscapeString(rt.Key)
			if jiraBase != "" {
				keyCell = fmt.Sprintf(`<a href="%s/browse/%s" target="_blank">%s</a>`, jiraBase, html.EscapeString(rt.Key), html.EscapeString(rt.Key))
			}
			fmt.Fprintf(w, `<tr><td>%s</td><td>%s</td><td class="text-cell">%s</td></tr>`,
				keyCell, html.EscapeString(rt.Verdict), html.EscapeString(rt.Summary))
		}
		fmt.Fprint(w, `</tbody></table>`)
	}

	// Other drafts for the same item.
	if len(siblings) > 1 {
		fmt.Fprint(w, `<h2 class="prop-h">All drafts for this item</h2><ul class="prop-list">`)
		for _, s := range siblings {
			marker := ""
			if s.ID == p.ID {
				marker = " (viewing)"
			}
			fmt.Fprintf(w, `<li><a href="/proposal?id=%d">#%d</a> — %s, %s%s</li>`,
				s.ID, s.ID, html.EscapeString(s.Status), s.CreatedAt.Format("2006-01-02 15:04"), marker)
		}
		fmt.Fprint(w, `</ul>`)
	}

	fmt.Fprint(w, pageFooter)
}

func propRow(w http.ResponseWriter, label, valueHTML string) {
	fmt.Fprintf(w, `<tr><th>%s</th><td>%s</td></tr>`, label, valueHTML)
}

// jiraBaseURL derives the Jira base (scheme+host) from the first ticket URL on
// record, so proposal-page links match the rest of the dashboard.
func (h *HealthServer) jiraBaseURL() string {
	rows, err := h.deps.Store.ListDashboardRows(200)
	if err != nil {
		return ""
	}
	for _, r := range rows {
		if r.JiraURL != "" {
			if idx := strings.Index(r.JiraURL, "/browse/"); idx > 0 {
				return r.JiraURL[:idx]
			}
		}
	}
	return ""
}

// statusBadgeClass maps a proposal status to a dashboard badge class.
func statusBadgeClass(status string) string {
	switch status {
	case "draft":
		return "proposing"
	case "filed":
		return "ticketed"
	case "rejected":
		return "cancelled"
	default:
		return status
	}
}
