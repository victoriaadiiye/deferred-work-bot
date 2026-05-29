package main

import (
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenStore(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestStore_InsertGetItem(t *testing.T) {
	s := newTestStore(t)
	it := &Item{
		SlackChannel:      "C1",
		SlackTS:           "1700000000.000100",
		AuthorSlackID:     "U1",
		Text:              "deferred work blob",
		Status:            "collecting",
		ApprovalThreshold: 3,
	}
	if err := s.InsertItem(it); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if it.ID == 0 {
		t.Fatal("ID not set after insert")
	}
	got, err := s.GetItemByTS("C1", "1700000000.000100")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Text != "deferred work blob" || got.Status != "collecting" {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}
	if got.CreatedAt.IsZero() {
		t.Fatal("created_at not set")
	}
	_ = time.Now()
}

func TestStore_UniqueTS(t *testing.T) {
	s := newTestStore(t)
	it := &Item{SlackChannel: "C1", SlackTS: "1", AuthorSlackID: "U1", Text: "x", Status: "collecting", ApprovalThreshold: 3}
	if err := s.InsertItem(it); err != nil {
		t.Fatal(err)
	}
	dup := *it
	dup.ID = 0
	if err := s.InsertItem(&dup); err == nil {
		t.Fatal("expected unique constraint violation")
	}
}

func TestStore_UpdateStatus(t *testing.T) {
	s := newTestStore(t)
	it := &Item{SlackChannel: "C1", SlackTS: "1", AuthorSlackID: "U1", Text: "x", Status: "collecting", ApprovalThreshold: 3}
	s.InsertItem(it)
	if err := s.UpdateItemStatus(it.ID, "proposing"); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, _ := s.GetItemByID(it.ID)
	if got.Status != "proposing" {
		t.Fatalf("status not updated: %s", got.Status)
	}
}

func TestStore_ListByStatus(t *testing.T) {
	s := newTestStore(t)
	for i, st := range []string{"collecting", "collecting", "ticketed"} {
		s.InsertItem(&Item{SlackChannel: "C1", SlackTS: string(rune('a' + i)), AuthorSlackID: "U1", Text: "x", Status: st, ApprovalThreshold: 3})
	}
	items, err := s.ListItemsByStatus("collecting")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2, got %d", len(items))
	}
}
