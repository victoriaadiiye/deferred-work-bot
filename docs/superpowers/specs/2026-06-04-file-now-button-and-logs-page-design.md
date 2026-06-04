# File-Now Button + Logs Page — Design

Date: 2026-06-04
Status: Approved

## Goal

Two dashboard additions:

1. **File-now button** — advance a `collecting` item to proposal directly from the web dashboard, mirroring the existing Slack `@bot file now` path (slack.go:237).
2. **Logs page** — render the existing `events` table as a browsable global feed with per-item filtering.

## Context

- Dashboard is server-rendered HTML (`dashboard.go`), no auth, internal network.
- `POST /trigger?item_id=X&action=propose` exists (health.go:60) but requires bearer token and returns bare 202 — unsuitable for browser forms.
- `events` table exists with kinds: created, intake, vote, vote_removed, cancel, advanced, proposal, reminder, warning, archive, edited, regen, project_override, priority_override, epic_override, resolution, attachment_failed.
- Store only has per-item event query (`ListEventsForItem`); no global listing.

## Design

### File-now button

**Endpoint:** `POST /file-now` (form-encoded `item_id`). No auth — same trust level as the dashboard itself.

Handler logic:
1. Method must be POST, else 405.
2. Parse `item_id`, else 400.
3. Load item via `GetItemByID`, else 404.
4. If status != `collecting`: no-op, redirect (idempotent — double-click safe).
5. Else: `UpdateItemStatus(id, "proposing")`, `LogEvent(id, "advanced", {"reason":"file_now","via":"dashboard"})`, `Worker.Submit(ProposeJob{ItemID: id})`.
6. Redirect `303 See Other` back to `/`.

**Dashboard change:** new `Actions` column. Rows with status `collecting` render an inline form:

```html
<form method="post" action="/file-now"><input type="hidden" name="item_id" value="N"><button class="file-now-btn">File now</button></form>
```

Other rows render `-`.

**Rejected alternative:** JS fetch to `/trigger` with embedded token — leaks token into HTML, adds JS to a no-JS page.

### Logs page

**Store:** `ListRecentEvents(limit int, itemID *int64) ([]EventRow, error)`

```sql
SELECT e.id, e.item_id, e.kind, e.payload_json, e.created_at, COALESCE(i.text, '')
FROM events e LEFT JOIN items i ON i.id = e.item_id
[WHERE e.item_id = ?]
ORDER BY e.id DESC LIMIT ?
```

`EventRow` = Event fields + `ItemText string`.

**Handler:** `GET /logs` on the health mux. Optional `?item_id=N` filter (invalid value → 400). Limit 200.

Table columns:
- **Time** — `created_at`, absolute + age via existing `formatAge`.
- **Item** — `#ID` linking to `/logs?item_id=N`, plus text preview (60 chars). `-` for nil item_id.
- **Kind** — colored badge, reuse badge CSS pattern; new kind-specific colors where sensible, neutral fallback.
- **Payload** — raw JSON in small monospace, escaped.

Filter note + "Show all" link when filtered, same pattern as dashboard status filter. Empty state message.

**Nav:** shared header links `Dashboard | Logs` added to `pageHead` (both pages use the same head constant; title stays "Deferred Work").

## Files touched

| File | Change |
|---|---|
| `health.go` | register `/file-now`, `/logs` routes; `fileNow` handler |
| `dashboard.go` | Actions column, button CSS, nav links in `pageHead` |
| `logs.go` (new) | `logsPage` handler |
| `store.go` | `EventRow`, `ListRecentEvents` |
| `health_test.go` / `logs_test.go` / `store_test.go` / `dashboard_test.go` (as fits existing layout) | tests below |

## Testing (TDD, repo convention)

File-now handler:
- GET → 405
- bad/missing item_id → 400
- unknown item → 404
- collecting item → status becomes `proposing`, `advanced` event logged with via=dashboard, ProposeJob submitted, 303 redirect to `/`
- non-collecting item → no status change, no job, still redirects

Store:
- `ListRecentEvents` returns newest first, respects limit, joins item text, nil filter vs item filter

Logs page:
- renders events with kind badges and item links
- `?item_id` filters
- invalid `item_id` → 400
- empty state

Dashboard:
- collecting row contains file-now form; ticketed row does not

## Out of scope

- Auth for dashboard/file-now (dashboard already unauthed by design)
- Pagination beyond limit 200
- Event payload pretty-printing/expansion
