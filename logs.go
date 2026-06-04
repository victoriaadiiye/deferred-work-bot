package main

import (
	"fmt"
	"html"
	"net/http"
	"strconv"
	"time"
)

func (h *HealthServer) logsPage(w http.ResponseWriter, r *http.Request) {
	var itemID *int64
	if raw := r.URL.Query().Get("item_id"); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			http.Error(w, "bad item_id", 400)
			return
		}
		itemID = &id
	}
	events, err := h.deps.Store.ListRecentEvents(200, itemID)
	if err != nil {
		http.Error(w, "db error", 500)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, pageHead)
	fmt.Fprint(w, `<h1>Event Log</h1>`)
	if itemID != nil {
		fmt.Fprintf(w, `<p class="filter-note">Showing events for item <strong>#%d</strong>. <a href="/logs">Show all</a></p>`, *itemID)
	}

	if len(events) == 0 {
		fmt.Fprint(w, `<p class="empty">No events yet.</p>`)
		fmt.Fprint(w, pageFooter)
		return
	}

	fmt.Fprint(w, `<table><thead><tr><th>Time</th><th>Item</th><th>Kind</th><th>Payload</th></tr></thead><tbody>`)
	for _, e := range events {
		timeCell := fmt.Sprintf("%s <span class=\"age\">(%s ago)</span>", e.CreatedAt.Format("2006-01-02 15:04"), formatAge(time.Since(e.CreatedAt)))
		itemCell := "-"
		if e.ItemID != nil {
			itemCell = fmt.Sprintf(`<a href="/logs?item_id=%d">#%d</a> %s`, *e.ItemID, *e.ItemID, html.EscapeString(truncateRunes(e.ItemText, 60)))
		}
		fmt.Fprintf(
			w, `<tr><td>%s</td><td class="text-cell">%s</td><td><span class="badge kind-%s">%s</span></td><td class="payload">%s</td></tr>`,
			timeCell,
			itemCell,
			html.EscapeString(e.Kind),
			html.EscapeString(e.Kind),
			html.EscapeString(e.Payload),
		)
	}
	fmt.Fprint(w, `</tbody></table>`)
	fmt.Fprint(w, pageFooter)
}
