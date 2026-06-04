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
	if !strings.Contains(body, ">created<") || !strings.Contains(body, ">vote<") {
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
	if !strings.Contains(body, ">created<") {
		t.Fatal("missing item 1 event")
	}
	if strings.Contains(body, ">cancel<") {
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
