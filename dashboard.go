package main

import (
	"fmt"
	"html"
	"net/http"
	"strings"
	"time"
)

func (h *HealthServer) dashboard(w http.ResponseWriter, r *http.Request) {
	rows, err := h.deps.Store.ListDashboardRows(200)
	if err != nil {
		http.Error(w, "db error", 500)
		return
	}

	jiraBase := ""
	if h.deps.Store != nil && len(rows) > 0 {
		for _, r := range rows {
			if r.JiraURL != "" {
				if idx := strings.Index(r.JiraURL, "/browse/"); idx > 0 {
					jiraBase = r.JiraURL[:idx]
				}
				break
			}
		}
	}

	var counts [6]int
	for _, row := range rows {
		switch row.Status {
		case "collecting":
			counts[0]++
		case "proposing", "proposed":
			counts[1]++
		case "ticketed":
			counts[2]++
		case "commented_on_existing":
			counts[3]++
		case "cancelled":
			counts[4]++
		case "archived":
			counts[5]++
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, pageHead)
	fmt.Fprintf(w, `<div class="stats">`)
	statLabels := []string{"Collecting", "Proposing", "Ticketed", "Commented", "Cancelled", "Archived"}
	statClasses := []string{"collecting", "proposing", "ticketed", "commented", "cancelled", "archived"}
	for i, label := range statLabels {
		fmt.Fprintf(w, `<div class="stat-card %s"><div class="stat-num">%d</div><div class="stat-label">%s</div></div>`, statClasses[i], counts[i], label)
	}
	fmt.Fprint(w, `</div>`)

	fmt.Fprint(w, `<table><thead><tr>`)
	fmt.Fprint(w, `<th>Status</th><th>Text</th><th>Subproject</th><th>Jira</th><th>Epic</th><th>Age</th>`)
	fmt.Fprint(w, `</tr></thead><tbody>`)

	for _, row := range rows {
		statusClass := row.Status
		if statusClass == "commented_on_existing" {
			statusClass = "commented"
		}
		age := time.Since(row.CreatedAt)
		ageStr := formatAge(age)

		textPreview := row.Text
		if len(textPreview) > 120 {
			textPreview = textPreview[:120] + "..."
		}

		jiraCell := "-"
		if row.JiraKey != "" && row.JiraURL != "" {
			jiraCell = fmt.Sprintf(`<a href="%s" target="_blank">%s</a>`, html.EscapeString(row.JiraURL), html.EscapeString(row.JiraKey))
		} else if row.JiraKey != "" {
			jiraCell = html.EscapeString(row.JiraKey)
		}

		epicCell := "-"
		if row.EpicKey != "" {
			if jiraBase != "" {
				epicCell = fmt.Sprintf(`<a href="%s/browse/%s" target="_blank">%s</a>`, jiraBase, row.EpicKey, row.EpicKey)
			} else {
				epicCell = html.EscapeString(row.EpicKey)
			}
		}

		subproject := row.Subproject
		if subproject == "" {
			subproject = "-"
		}

		fmt.Fprintf(w, `<tr><td><span class="badge %s">%s</span></td><td class="text-cell" title="%s">%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td></tr>`,
			statusClass,
			html.EscapeString(row.Status),
			html.EscapeString(row.Text),
			html.EscapeString(textPreview),
			html.EscapeString(subproject),
			jiraCell,
			epicCell,
			ageStr,
		)
	}

	fmt.Fprint(w, `</tbody></table>`)
	if len(rows) == 0 {
		fmt.Fprint(w, `<p class="empty">No deferred work items yet.</p>`)
	}
	fmt.Fprint(w, pageFooter)
}

func formatAge(d time.Duration) string {
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%.1fh", d.Hours())
	default:
		return fmt.Sprintf("%.1fd", d.Hours()/24)
	}
}

const pageHead = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Deferred Work</title>
<style>
  *, *::before, *::after { box-sizing: border-box; margin: 0; padding: 0; }
  body {
    font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
    background: #0f1117;
    color: #c9d1d9;
    padding: 2rem;
    line-height: 1.5;
  }
  h1 { color: #e6edf3; margin-bottom: 1.5rem; font-size: 1.5rem; font-weight: 600; }
  .stats {
    display: flex; gap: 1rem; margin-bottom: 2rem; flex-wrap: wrap;
  }
  .stat-card {
    background: #161b22;
    border: 1px solid #30363d;
    border-radius: 8px;
    padding: 1rem 1.5rem;
    min-width: 120px;
    text-align: center;
  }
  .stat-num { font-size: 2rem; font-weight: 700; }
  .stat-label { font-size: 0.8rem; color: #8b949e; text-transform: uppercase; letter-spacing: 0.05em; }
  .stat-card.collecting .stat-num { color: #f0883e; }
  .stat-card.proposing .stat-num { color: #d2a8ff; }
  .stat-card.ticketed .stat-num { color: #3fb950; }
  .stat-card.commented .stat-num { color: #58a6ff; }
  .stat-card.cancelled .stat-num { color: #8b949e; }
  .stat-card.archived .stat-num { color: #484f58; }
  table {
    width: 100%;
    border-collapse: collapse;
    background: #161b22;
    border-radius: 8px;
    overflow: hidden;
    border: 1px solid #30363d;
  }
  thead { background: #1c2128; }
  th {
    text-align: left; padding: 0.75rem 1rem;
    font-size: 0.8rem; color: #8b949e;
    text-transform: uppercase; letter-spacing: 0.05em;
    font-weight: 600; border-bottom: 1px solid #30363d;
  }
  td { padding: 0.75rem 1rem; border-bottom: 1px solid #21262d; font-size: 0.9rem; }
  tr:last-child td { border-bottom: none; }
  tr:hover { background: #1c2128; }
  .text-cell { max-width: 400px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  a { color: #58a6ff; text-decoration: none; }
  a:hover { text-decoration: underline; }
  .badge {
    display: inline-block; padding: 2px 10px; border-radius: 12px;
    font-size: 0.78rem; font-weight: 500;
  }
  .badge.collecting { background: #f0883e22; color: #f0883e; }
  .badge.proposing, .badge.proposed { background: #d2a8ff22; color: #d2a8ff; }
  .badge.ticketed { background: #3fb95022; color: #3fb950; }
  .badge.commented, .badge.commented_on_existing { background: #58a6ff22; color: #58a6ff; }
  .badge.cancelled { background: #8b949e22; color: #8b949e; }
  .badge.archived { background: #484f5822; color: #484f58; }
  .empty { text-align: center; color: #8b949e; padding: 3rem; }
  @media (max-width: 768px) {
    body { padding: 1rem; }
    .stats { gap: 0.5rem; }
    .stat-card { min-width: 80px; padding: 0.75rem; }
    .stat-num { font-size: 1.5rem; }
    .text-cell { max-width: 200px; }
  }
</style>
</head>
<body>
<h1>Deferred Work Dashboard</h1>
`

const pageFooter = `
</body>
</html>
`
