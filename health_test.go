package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestHealthServer(t *testing.T) (*HealthServer, *Store, *fakeSlack, *Worker) {
	t.Helper()
	store := newTestStore(t)
	fake := newFakeSlack("UBOT")
	w := &Worker{queue: make(chan job, 4)}
	srv := NewHealthServer(HealthDeps{Store: store, Worker: w, TriggerToken: "tok", Slack: fake})
	return srv, store, fake, w
}

func TestHealth_OK(t *testing.T) {
	store := newTestStore(t)
	w := &Worker{queue: make(chan job, 1)}
	srv := NewHealthServer(HealthDeps{Store: store, Worker: w, TriggerToken: ""})
	rec := httptest.NewRecorder()
	srv.handler().ServeHTTP(rec, httptest.NewRequest("GET", "/health", nil))
	if rec.Code != 200 {
		t.Fatalf("code: %d", rec.Code)
	}
}

func TestMetrics_ReportsQueueAndItemCounts(t *testing.T) {
	store := newTestStore(t)
	store.InsertItem(&Item{SlackChannel: "C1", SlackTS: "1", AuthorSlackID: "U1", Text: "x", Status: "collecting", ApprovalThreshold: 3})
	store.InsertItem(&Item{SlackChannel: "C1", SlackTS: "2", AuthorSlackID: "U1", Text: "x", Status: "ticketed", ApprovalThreshold: 3})
	w := &Worker{queue: make(chan job, 4)}
	w.queue <- ProposeJob{}
	srv := NewHealthServer(HealthDeps{Store: store, Worker: w, TriggerToken: ""})
	rec := httptest.NewRecorder()
	srv.handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body := rec.Body.String()
	if !strings.Contains(body, "queue_depth 1") {
		t.Fatalf("queue_depth missing: %s", body)
	}
	if !strings.Contains(body, `items_by_status{status="collecting"} 1`) {
		t.Fatalf("collecting count missing: %s", body)
	}
}

func TestTrigger_RequiresToken(t *testing.T) {
	store := newTestStore(t)
	w := &Worker{queue: make(chan job, 4)}
	srv := NewHealthServer(HealthDeps{Store: store, Worker: w, TriggerToken: "sekret"})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/trigger?item_id=1&action=propose", nil)
	srv.handler().ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatalf("code: %d", rec.Code)
	}
}

func TestTrigger_ArchiveAction(t *testing.T) {
	srv, store, fake, _ := newTestHealthServer(t)
	it := &Item{SlackChannel: "C1", SlackTS: "1700.1", AuthorSlackID: "U1", Text: "archive me", Status: "collecting", ApprovalThreshold: 3}
	store.InsertItem(it)

	req := httptest.NewRequest("POST", "/trigger?item_id=1&action=archive", nil)
	req.Header.Set("Authorization", "Bearer tok")
	rec := httptest.NewRecorder()
	srv.handler().ServeHTTP(rec, req)

	if rec.Code != 202 {
		t.Fatalf("code: %d, body: %s", rec.Code, rec.Body.String())
	}
	got, _ := store.GetItemByID(it.ID)
	if got.Status != "archived" {
		t.Fatalf("expected archived, got %s", got.Status)
	}

	// :wastebasket: reaction should have been posted.
	hasWastebasket := false
	for _, r := range fake.reactions {
		if r.Name == "wastebasket" && r.Action == "add" {
			hasWastebasket = true
		}
	}
	if !hasWastebasket {
		t.Fatal("expected :wastebasket: reaction on original message")
	}

	// Archive event should be logged.
	events, _ := store.ListEventsForItem(it.ID)
	found := false
	for _, ev := range events {
		if ev.Kind == "archive" && strings.Contains(ev.Payload, `"via":"trigger"`) {
			found = true
		}
	}
	if !found {
		t.Fatal("expected archive event with via=trigger")
	}
}

func TestTrigger_ArchiveAction_SkipsAlreadyTerminal(t *testing.T) {
	srv, store, _, _ := newTestHealthServer(t)
	it := &Item{SlackChannel: "C1", SlackTS: "1700.1", AuthorSlackID: "U1", Text: "x", Status: "ticketed", ApprovalThreshold: 3}
	store.InsertItem(it)

	req := httptest.NewRequest("POST", "/trigger?item_id=1&action=archive", nil)
	req.Header.Set("Authorization", "Bearer tok")
	rec := httptest.NewRecorder()
	srv.handler().ServeHTTP(rec, req)

	if rec.Code != 202 {
		t.Fatalf("code: %d", rec.Code)
	}
	got, _ := store.GetItemByID(it.ID)
	if got.Status != "ticketed" {
		t.Fatalf("terminal item should not change status, got %s", got.Status)
	}
}

func TestDashboard_RendersHTML(t *testing.T) {
	store := newTestStore(t)
	store.InsertItem(&Item{SlackChannel: "C1", SlackTS: "1", AuthorSlackID: "U1", Text: "fix flaky test", Status: "collecting", ApprovalThreshold: 3})
	store.InsertItem(&Item{SlackChannel: "C1", SlackTS: "2", AuthorSlackID: "U1", Text: "add metrics", Status: "ticketed", ApprovalThreshold: 3})
	w := &Worker{queue: make(chan job, 1)}
	srv := NewHealthServer(HealthDeps{Store: store, Worker: w})
	rec := httptest.NewRecorder()
	srv.handler().ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 200 {
		t.Fatalf("code: %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Deferred Work Dashboard") {
		t.Fatal("missing title")
	}
	if !strings.Contains(body, "fix flaky test") {
		t.Fatal("missing item text")
	}
	if !strings.Contains(body, "collecting") || !strings.Contains(body, "ticketed") {
		t.Fatal("missing status badges")
	}
}

func TestDashboard_Empty(t *testing.T) {
	store := newTestStore(t)
	w := &Worker{queue: make(chan job, 1)}
	srv := NewHealthServer(HealthDeps{Store: store, Worker: w})
	rec := httptest.NewRecorder()
	srv.handler().ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 200 {
		t.Fatalf("code: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "No deferred work items yet") {
		t.Fatal("missing empty state message")
	}
}

func TestDashboard_WithJiraLinks(t *testing.T) {
	store := newTestStore(t)
	it := &Item{SlackChannel: "C1", SlackTS: "1", AuthorSlackID: "U1", Text: "do thing", Status: "ticketed", ApprovalThreshold: 3}
	store.InsertItem(it)
	p := &Proposal{ItemID: it.ID, SlackTS: "2", DraftJSON: `{"epic_key":"QORK-440"}`, RelatedTicketsJSON: "[]", Branch: "new", Status: "filed"}
	store.InsertProposal(p)
	store.InsertTicket(&Ticket{ProposalID: p.ID, JiraKey: "QORK-100", JiraURL: "https://jira.example/browse/QORK-100", Action: "created"})

	w := &Worker{queue: make(chan job, 1)}
	srv := NewHealthServer(HealthDeps{Store: store, Worker: w})
	rec := httptest.NewRecorder()
	srv.handler().ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	body := rec.Body.String()
	if !strings.Contains(body, "QORK-100") {
		t.Fatal("missing jira key")
	}
	if !strings.Contains(body, "https://jira.example/browse/QORK-100") {
		t.Fatal("missing jira link")
	}
	if !strings.Contains(body, "QORK-440") {
		t.Fatal("missing epic key")
	}
}

func TestTrigger_EnqueuesProposeJob(t *testing.T) {
	store := newTestStore(t)
	store.InsertItem(&Item{SlackChannel: "C1", SlackTS: "1", AuthorSlackID: "U1", Text: "x", Status: "collecting", ApprovalThreshold: 3})
	w := &Worker{queue: make(chan job, 4)}
	srv := NewHealthServer(HealthDeps{Store: store, Worker: w, TriggerToken: "sekret"})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/trigger?item_id=1&action=propose", nil)
	req.Header.Set("Authorization", "Bearer sekret")
	srv.handler().ServeHTTP(rec, req)
	if rec.Code != 202 {
		t.Fatalf("code: %d, body: %s", rec.Code, rec.Body.String())
	}
	select {
	case <-w.queue:
	default:
		t.Fatal("expected job enqueued")
	}
}

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
