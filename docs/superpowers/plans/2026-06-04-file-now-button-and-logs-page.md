# File-Now Button + Logs Page Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a "File now" button to dashboard rows (advances `collecting` items to proposal) and a `/logs` page rendering the events table.

**Architecture:** Server-rendered HTML on the existing health mux (`health.go`/`dashboard.go`). New `POST /file-now` endpoint mirrors the Slack `cmdFileNow` path (slack.go:232). New `GET /logs` handler backed by a new `Store.ListRecentEvents` query (events LEFT JOIN items). No JS, no auth — same trust level as the existing dashboard.

**Tech Stack:** Go stdlib `net/http`, `html/template`-free Fprintf rendering (existing pattern), SQLite via `modernc.org/sqlite`, stdlib `testing` with `httptest` + `newTestStore`.

**Spec:** `docs/superpowers/specs/2026-06-04-file-now-button-and-logs-page-design.md`

**Conventions:** TDD red-green per repo rule. Run tests with `go test ./...`. Format with `gofumpt -w <files>` before each commit.

---

### Task 1: `Store.ListRecentEvents`

**Files:**
- Modify: `store.go` (add `EventRow` type + `ListRecentEvents` after `ListEventsForItem`, ~line 352)
- Test: `store_test.go`

- [ ] **Step 1: Write the failing test**

Append to `store_test.go`:

```go
func TestListRecentEvents(t *testing.T) {
	s := newTestStore(t)
	it1 := &Item{SlackChannel: "C1", SlackTS: "1", AuthorSlackID: "U1", Text: "first item", Status: "collecting", ApprovalThreshold: 3}
	s.InsertItem(it1)
	it2 := &Item{SlackChannel: "C1", SlackTS: "2", AuthorSlackID: "U1", Text: "second item", Status: "collecting", ApprovalThreshold: 3}
	s.InsertItem(it2)
	s.LogEvent(&it1.ID, "created", "{}")
	s.LogEvent(&it2.ID, "created", "{}")
	s.LogEvent(&it1.ID, "vote", `{"user":"U2"}`)
	s.LogEvent(nil, "system", "{}")

	all, err := s.ListRecentEvents(200, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 4 {
		t.Fatalf("expected 4 events, got %d", len(all))
	}
	// Newest first.
	if all[0].Kind != "system" {
		t.Fatalf("expected newest event first, got %s", all[0].Kind)
	}
	if all[0].ItemID != nil {
		t.Fatal("system event should have nil ItemID")
	}
	if all[1].Kind != "vote" || all[1].ItemText != "first item" {
		t.Fatalf("expected vote on 'first item', got %s / %q", all[1].Kind, all[1].ItemText)
	}

	only1, err := s.ListRecentEvents(200, &it1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(only1) != 2 {
		t.Fatalf("expected 2 events for item 1, got %d", len(only1))
	}
	for _, e := range only1 {
		if e.ItemID == nil || *e.ItemID != it1.ID {
			t.Fatalf("filter leaked event for other item: %+v", e)
		}
	}

	lim, err := s.ListRecentEvents(1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(lim) != 1 || lim[0].Kind != "system" {
		t.Fatalf("limit not respected: %+v", lim)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run TestListRecentEvents ./...`
Expected: FAIL — compile error `s.ListRecentEvents undefined`

- [ ] **Step 3: Write implementation**

Add to `store.go` directly after `ListEventsForItem` (~line 352):

```go
// EventRow is an event joined with its item's text for display.
type EventRow struct {
	Event
	ItemText string
}

// ListRecentEvents returns events newest first, optionally filtered to one
// item, with the owning item's text joined in (empty for itemless events).
func (s *Store) ListRecentEvents(limit int, itemID *int64) ([]EventRow, error) {
	if limit == 0 {
		limit = 200
	}
	q := `SELECT e.id, e.item_id, e.kind, e.payload_json, e.created_at, COALESCE(i.text, '')
		FROM events e LEFT JOIN items i ON i.id = e.item_id`
	args := []any{}
	if itemID != nil {
		q += ` WHERE e.item_id = ?`
		args = append(args, *itemID)
	}
	q += ` ORDER BY e.id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EventRow
	for rows.Next() {
		var e EventRow
		var iid sql.NullInt64
		if err := rows.Scan(&e.ID, &iid, &e.Kind, &e.Payload, &e.CreatedAt, &e.ItemText); err != nil {
			return nil, err
		}
		if iid.Valid {
			v := iid.Int64
			e.ItemID = &v
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -run TestListRecentEvents ./...`
Expected: PASS

- [ ] **Step 5: Full suite + format + commit**

```bash
go test ./... && gofumpt -w store.go store_test.go
git add store.go store_test.go
git commit -m "feat(store): ListRecentEvents with item text join"
```

---

### Task 2: `POST /file-now` endpoint

**Files:**
- Modify: `health.go` (register route in `handler()` ~line 24; add `fileNow` handler after `trigger`)
- Test: `health_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `health_test.go`:

```go
func TestFileNow_AdvancesCollectingItem(t *testing.T) {
	srv, store, _, w := newTestHealthServer(t)
	it := &Item{SlackChannel: "C1", SlackTS: "1700.1", AuthorSlackID: "U1", Text: "x", Status: "collecting", ApprovalThreshold: 3}
	store.InsertItem(it)

	req := httptest.NewRequest("POST", "/file-now", strings.NewReader("item_id=1"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	srv.handler().ServeHTTP(rec, req)

	if rec.Code != 303 {
		t.Fatalf("code: %d, body: %s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/" {
		t.Fatalf("expected redirect to /, got %q", loc)
	}
	got, _ := store.GetItemByID(it.ID)
	if got.Status != "proposing" {
		t.Fatalf("expected proposing, got %s", got.Status)
	}
	select {
	case j := <-w.queue:
		if pj, ok := j.(ProposeJob); !ok || pj.ItemID != it.ID {
			t.Fatalf("expected ProposeJob for item %d, got %+v", it.ID, j)
		}
	default:
		t.Fatal("expected ProposeJob enqueued")
	}
	events, _ := store.ListEventsForItem(it.ID)
	found := false
	for _, ev := range events {
		if ev.Kind == "advanced" && strings.Contains(ev.Payload, `"reason":"file_now"`) && strings.Contains(ev.Payload, `"via":"dashboard"`) {
			found = true
		}
	}
	if !found {
		t.Fatal("expected advanced event with reason=file_now via=dashboard")
	}
}

func TestFileNow_NonCollectingIsNoop(t *testing.T) {
	srv, store, _, w := newTestHealthServer(t)
	it := &Item{SlackChannel: "C1", SlackTS: "1700.1", AuthorSlackID: "U1", Text: "x", Status: "ticketed", ApprovalThreshold: 3}
	store.InsertItem(it)

	req := httptest.NewRequest("POST", "/file-now", strings.NewReader("item_id=1"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	srv.handler().ServeHTTP(rec, req)

	if rec.Code != 303 {
		t.Fatalf("code: %d", rec.Code)
	}
	got, _ := store.GetItemByID(it.ID)
	if got.Status != "ticketed" {
		t.Fatalf("status should not change, got %s", got.Status)
	}
	select {
	case <-w.queue:
		t.Fatal("no job should be enqueued")
	default:
	}
}

func TestFileNow_Errors(t *testing.T) {
	srv, _, _, _ := newTestHealthServer(t)

	rec := httptest.NewRecorder()
	srv.handler().ServeHTTP(rec, httptest.NewRequest("GET", "/file-now", nil))
	if rec.Code != 405 {
		t.Fatalf("GET should be 405, got %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/file-now", strings.NewReader("item_id=abc"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	srv.handler().ServeHTTP(rec, req)
	if rec.Code != 400 {
		t.Fatalf("bad item_id should be 400, got %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest("POST", "/file-now", strings.NewReader("item_id=999"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	srv.handler().ServeHTTP(rec, req)
	if rec.Code != 404 {
		t.Fatalf("unknown item should be 404, got %d", rec.Code)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -run TestFileNow ./...`
Expected: FAIL — 404 from mux (route not registered), wrong status codes

- [ ] **Step 3: Write implementation**

In `health.go`, register the route inside `handler()`:

```go
	mux.HandleFunc("/file-now", h.fileNow)
```

Add the handler after `trigger`:

```go
// fileNow advances a collecting item straight to proposal, mirroring the
// Slack "@bot file now" command. Form POST from the dashboard; no auth,
// same trust level as the dashboard itself.
func (h *HealthServer) fileNow(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "method", 405)
		return
	}
	itemID, err := strconv.ParseInt(r.FormValue("item_id"), 10, 64)
	if err != nil {
		http.Error(w, "bad item_id", 400)
		return
	}
	it, err := h.deps.Store.GetItemByID(itemID)
	if err != nil {
		http.Error(w, "item not found", 404)
		return
	}
	if it.Status == "collecting" {
		h.deps.Store.UpdateItemStatus(it.ID, "proposing")
		h.deps.Store.LogEvent(&it.ID, "advanced", `{"reason":"file_now","via":"dashboard"}`)
		h.deps.Worker.Submit(ProposeJob{ItemID: it.ID})
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -run TestFileNow ./...`
Expected: PASS

- [ ] **Step 5: Full suite + format + commit**

```bash
go test ./... && gofumpt -w health.go health_test.go
git add health.go health_test.go
git commit -m "feat(dashboard): POST /file-now endpoint advancing collecting items"
```

---

### Task 3: Dashboard Actions column + nav links

**Files:**
- Modify: `dashboard.go` (table header ~line 83, row render ~line 120, `pageHead` CSS + nav, h1 handling)
- Test: `health_test.go` (dashboard tests live there)

- [ ] **Step 1: Write the failing test**

Append to `health_test.go`:

```go
func TestDashboard_FileNowButtonOnlyOnCollecting(t *testing.T) {
	store := newTestStore(t)
	store.InsertItem(&Item{SlackChannel: "C1", SlackTS: "1", AuthorSlackID: "U1", Text: "still collecting", Status: "collecting", ApprovalThreshold: 3})
	store.InsertItem(&Item{SlackChannel: "C1", SlackTS: "2", AuthorSlackID: "U1", Text: "already ticketed", Status: "ticketed", ApprovalThreshold: 3})
	w := &Worker{queue: make(chan job, 1)}
	srv := NewHealthServer(HealthDeps{Store: store, Worker: w})
	rec := httptest.NewRecorder()
	srv.handler().ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	body := rec.Body.String()

	if !strings.Contains(body, `action="/file-now"`) {
		t.Fatal("expected file-now form on collecting row")
	}
	if !strings.Contains(body, `name="item_id" value="1"`) {
		t.Fatal("expected hidden item_id=1 input")
	}
	if strings.Count(body, `action="/file-now"`) != 1 {
		t.Fatal("file-now form should only appear on collecting rows")
	}
	if !strings.Contains(body, `href="/logs"`) {
		t.Fatal("expected nav link to /logs")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run TestDashboard_FileNowButtonOnlyOnCollecting ./...`
Expected: FAIL — "expected file-now form on collecting row"

- [ ] **Step 3: Implement dashboard changes**

In `dashboard.go`:

3a. Table header (line 83) — add Actions column:

```go
	fmt.Fprint(w, `<th>Status</th><th>Text</th><th>Subproject</th><th>Jira</th><th>Epic</th><th>Age</th><th>Actions</th>`)
```

3b. Row render — before the `fmt.Fprintf(w, `<tr>...` call (line 120), build the actions cell:

```go
		actionsCell := "-"
		if row.Status == "collecting" {
			actionsCell = fmt.Sprintf(`<form method="post" action="/file-now"><input type="hidden" name="item_id" value="%d"><button class="file-now-btn" type="submit">File now</button></form>`, row.ItemID)
		}
```

and extend the row Fprintf with one more `<td>%s</td>` and the `actionsCell` argument:

```go
		fmt.Fprintf(w, `<tr><td><span class="badge %s">%s</span></td><td class="text-cell" title="%s">%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td></tr>`,
			statusClass,
			html.EscapeString(row.Status),
			html.EscapeString(row.Text),
			html.EscapeString(textPreview),
			html.EscapeString(subproject),
			jiraCell,
			epicCell,
			ageStr,
			actionsCell,
		)
```

3c. `pageHead` — replace the trailing h1 with a nav bar so the logs page can reuse the head with its own h1. Change the end of `pageHead` from:

```html
<body>
<h1>Deferred Work Dashboard</h1>
```

to:

```html
<body>
<div class="nav"><a href="/">Dashboard</a><a href="/logs">Logs</a></div>
```

and add CSS inside the `<style>` block (next to `.stat-card` rules):

```css
  .nav { display: flex; gap: 1rem; margin-bottom: 1rem; }
  .nav a { color: #8b949e; font-size: 0.9rem; }
  .nav a:hover { color: #58a6ff; text-decoration: none; }
  .file-now-btn {
    background: #238636; color: #fff; border: none; border-radius: 6px;
    padding: 4px 10px; font-size: 0.78rem; cursor: pointer; font-weight: 500;
  }
  .file-now-btn:hover { background: #2ea043; }
```

3d. In the `dashboard` handler, print the h1 right after `pageHead` (line 66):

```go
	fmt.Fprint(w, pageHead)
	fmt.Fprint(w, `<h1>Deferred Work Dashboard</h1>`)
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -run 'TestDashboard' ./...`
Expected: PASS (including the pre-existing `TestDashboard_RendersHTML`, `TestDashboard_Empty`, `TestDashboard_WithJiraLinks` — the h1 move must not break them)

- [ ] **Step 5: Full suite + format + commit**

```bash
go test ./... && gofumpt -w dashboard.go health_test.go
git add dashboard.go health_test.go
git commit -m "feat(dashboard): file-now button on collecting rows, nav bar"
```

---

### Task 4: Logs page

**Files:**
- Create: `logs.go`
- Create: `logs_test.go`
- Modify: `health.go` (register `/logs` route in `handler()`)

- [ ] **Step 1: Write the failing tests**

Create `logs_test.go`:

```go
package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLogs_RendersEvents(t *testing.T) {
	srv, store, _, _ := newTestHealthServer(t)
	it := &Item{SlackChannel: "C1", SlackTS: "1", AuthorSlackID: "U1", Text: "fix flaky test", Status: "collecting", ApprovalThreshold: 3}
	store.InsertItem(it)
	store.LogEvent(&it.ID, "created", "{}")
	store.LogEvent(&it.ID, "vote", `{"user":"U2","source":"reaction"}`)

	rec := httptest.NewRecorder()
	srv.handler().ServeHTTP(rec, httptest.NewRequest("GET", "/logs", nil))
	if rec.Code != 200 {
		t.Fatalf("code: %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Event Log") {
		t.Fatal("missing title")
	}
	if !strings.Contains(body, "created") || !strings.Contains(body, "vote") {
		t.Fatal("missing event kinds")
	}
	if !strings.Contains(body, "fix flaky test") {
		t.Fatal("missing item text preview")
	}
	if !strings.Contains(body, `href="/logs?item_id=1"`) {
		t.Fatal("missing per-item filter link")
	}
	if !strings.Contains(body, `&#34;user&#34;:&#34;U2&#34;`) && !strings.Contains(body, `"user":"U2"`) {
		t.Fatal("missing payload")
	}
}

func TestLogs_FilterByItem(t *testing.T) {
	srv, store, _, _ := newTestHealthServer(t)
	it1 := &Item{SlackChannel: "C1", SlackTS: "1", AuthorSlackID: "U1", Text: "item one", Status: "collecting", ApprovalThreshold: 3}
	store.InsertItem(it1)
	it2 := &Item{SlackChannel: "C1", SlackTS: "2", AuthorSlackID: "U1", Text: "item two", Status: "collecting", ApprovalThreshold: 3}
	store.InsertItem(it2)
	store.LogEvent(&it1.ID, "created", "{}")
	store.LogEvent(&it2.ID, "cancel", `{"by":"U9"}`)

	rec := httptest.NewRecorder()
	srv.handler().ServeHTTP(rec, httptest.NewRequest("GET", "/logs?item_id=1", nil))
	body := rec.Body.String()
	if !strings.Contains(body, "created") {
		t.Fatal("missing item 1 event")
	}
	if strings.Contains(body, "cancel") {
		t.Fatal("item 2 event should be filtered out")
	}
	if !strings.Contains(body, "Show all") {
		t.Fatal("missing show-all link when filtered")
	}
}

func TestLogs_BadItemID(t *testing.T) {
	srv, _, _, _ := newTestHealthServer(t)
	rec := httptest.NewRecorder()
	srv.handler().ServeHTTP(rec, httptest.NewRequest("GET", "/logs?item_id=abc", nil))
	if rec.Code != 400 {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestLogs_Empty(t *testing.T) {
	srv, _, _, _ := newTestHealthServer(t)
	rec := httptest.NewRecorder()
	srv.handler().ServeHTTP(rec, httptest.NewRequest("GET", "/logs", nil))
	if rec.Code != 200 {
		t.Fatalf("code: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "No events yet") {
		t.Fatal("missing empty state")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -run TestLogs ./...`
Expected: FAIL — 404 (route not registered)

- [ ] **Step 3: Write implementation**

In `health.go` `handler()`:

```go
	mux.HandleFunc("/logs", h.logsPage)
```

Create `logs.go`:

```go
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

	fmt.Fprint(w, `<table><thead><tr><th>Time</th><th>Item</th><th>Kind</th><th>Payload</th></tr></thead><tbody>`)
	for _, e := range events {
		timeCell := fmt.Sprintf("%s <span class=\"age\">(%s ago)</span>", e.CreatedAt.Format("2006-01-02 15:04"), formatAge(time.Since(e.CreatedAt)))
		itemCell := "-"
		if e.ItemID != nil {
			preview := e.ItemText
			if len(preview) > 60 {
				preview = preview[:60] + "..."
			}
			itemCell = fmt.Sprintf(`<a href="/logs?item_id=%d">#%d</a> %s`, *e.ItemID, *e.ItemID, html.EscapeString(preview))
		}
		fmt.Fprintf(w, `<tr><td>%s</td><td class="text-cell">%s</td><td><span class="badge kind-%s">%s</span></td><td class="payload">%s</td></tr>`,
			timeCell,
			itemCell,
			html.EscapeString(e.Kind),
			html.EscapeString(e.Kind),
			html.EscapeString(e.Payload),
		)
	}
	fmt.Fprint(w, `</tbody></table>`)
	if len(events) == 0 {
		fmt.Fprint(w, `<p class="empty">No events yet.</p>`)
	}
	fmt.Fprint(w, pageFooter)
}
```

Add CSS to `pageHead` in `dashboard.go` (next to the existing `.badge.*` rules):

```css
  .badge { background: #8b949e22; color: #8b949e; }
  .badge.kind-created, .badge.kind-intake { background: #58a6ff22; color: #58a6ff; }
  .badge.kind-vote, .badge.kind-advanced, .badge.kind-regen { background: #d2a8ff22; color: #d2a8ff; }
  .badge.kind-proposal, .badge.kind-resolution { background: #3fb95022; color: #3fb950; }
  .badge.kind-cancel, .badge.kind-attachment_failed { background: #f8514922; color: #f85149; }
  .badge.kind-reminder, .badge.kind-warning, .badge.kind-archive { background: #f0883e22; color: #f0883e; }
  .payload {
    font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
    font-size: 0.78rem; color: #8b949e;
    max-width: 320px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  }
  .age { color: #8b949e; font-size: 0.8rem; }
```

Note: the bare `.badge` default must come BEFORE the existing `.badge.collecting` etc. rules in the style block (lower specificity, but keep order tidy). The two-class rules override it on the dashboard.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -run TestLogs ./...`
Expected: PASS

- [ ] **Step 5: Full suite + format + commit**

```bash
go test ./... && gofumpt -w logs.go logs_test.go health.go dashboard.go
git add logs.go logs_test.go health.go dashboard.go
git commit -m "feat(dashboard): /logs page with global event feed and per-item filter"
```

---

### Task 5: Manual smoke check

**Files:** none (verification only)

- [ ] **Step 1: Build and run locally**

```bash
go build -o deferred-work-bot . && echo BUILD OK
```

Expected: `BUILD OK`. (Full app needs Slack tokens; build + unit tests are the verification gate. If a local `.env` with valid tokens exists, optionally run the bot and open `http://localhost:<HealthPort>/` and `/logs`.)

- [ ] **Step 2: Final full suite**

Run: `go test ./...`
Expected: all PASS, no skips introduced.
