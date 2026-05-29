package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

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
