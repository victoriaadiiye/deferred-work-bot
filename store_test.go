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

func TestStore_UpsertVote(t *testing.T) {
	s := newTestStore(t)
	it := &Item{SlackChannel: "C1", SlackTS: "1", AuthorSlackID: "U_AUTHOR", Text: "x", Status: "collecting", ApprovalThreshold: 3}
	s.InsertItem(it)

	if err := s.UpsertVote(it.ID, "U2", "reaction", "white_check_mark"); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertVote(it.ID, "U2", "reply", "lgtm"); err != nil {
		t.Fatal(err) // same user, different source — should dedup, not error
	}
	n, err := s.CountVotes(it.ID)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected 1 vote after dedup, got %d", n)
	}

	s.UpsertVote(it.ID, "U3", "reaction", "+1")
	n, _ = s.CountVotes(it.ID)
	if n != 2 {
		t.Fatalf("expected 2 votes, got %d", n)
	}
}

func TestStore_RemoveVote(t *testing.T) {
	s := newTestStore(t)
	it := &Item{SlackChannel: "C1", SlackTS: "1", AuthorSlackID: "U1", Text: "x", Status: "collecting", ApprovalThreshold: 3}
	s.InsertItem(it)
	s.UpsertVote(it.ID, "U2", "reaction", "white_check_mark")
	if err := s.RemoveVote(it.ID, "U2"); err != nil {
		t.Fatal(err)
	}
	n, _ := s.CountVotes(it.ID)
	if n != 0 {
		t.Fatalf("expected 0 votes after removal, got %d", n)
	}
}

func TestStore_VoteExcludesAuthor(t *testing.T) {
	// Author self-vote enforcement happens at the dispatch layer, but the
	// store offers a HasVoted helper used by the caller to skip inserts.
	s := newTestStore(t)
	it := &Item{SlackChannel: "C1", SlackTS: "1", AuthorSlackID: "U_AUTHOR", Text: "x", Status: "collecting", ApprovalThreshold: 3}
	s.InsertItem(it)
	ok, err := s.HasVoted(it.ID, "U_AUTHOR")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("HasVoted should be false initially")
	}
}

func TestStore_ProposalsRoundtrip(t *testing.T) {
	s := newTestStore(t)
	it := &Item{SlackChannel: "C1", SlackTS: "1", AuthorSlackID: "U1", Text: "x", Status: "proposing", ApprovalThreshold: 3}
	s.InsertItem(it)
	p := &Proposal{
		ItemID:             it.ID,
		SlackTS:            "1700.000200",
		DraftJSON:          `{"summary":"do X"}`,
		RelatedTicketsJSON: `[]`,
		Branch:             "new",
		Status:             "draft",
	}
	if err := s.InsertProposal(p); err != nil {
		t.Fatal(err)
	}
	if p.ID == 0 {
		t.Fatal("proposal ID not set")
	}
	got, err := s.GetLatestProposal(it.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.DraftJSON != p.DraftJSON {
		t.Fatalf("mismatch: %+v", got)
	}
}

func TestStore_RecordTicket(t *testing.T) {
	s := newTestStore(t)
	it := &Item{SlackChannel: "C1", SlackTS: "1", AuthorSlackID: "U1", Text: "x", Status: "proposed", ApprovalThreshold: 3}
	s.InsertItem(it)
	p := &Proposal{ItemID: it.ID, SlackTS: "2", DraftJSON: "{}", RelatedTicketsJSON: "[]", Branch: "new", Status: "approved"}
	s.InsertProposal(p)
	tk := &Ticket{ProposalID: p.ID, JiraKey: "QORK-1", JiraURL: "https://x/QORK-1", Action: "created"}
	if err := s.InsertTicket(tk); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetTicketByProposal(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.JiraKey != "QORK-1" {
		t.Fatalf("mismatch: %+v", got)
	}
}

func TestStore_LogEvent(t *testing.T) {
	s := newTestStore(t)
	it := &Item{SlackChannel: "C1", SlackTS: "1", AuthorSlackID: "U1", Text: "x", Status: "collecting", ApprovalThreshold: 3}
	s.InsertItem(it)
	if err := s.LogEvent(&it.ID, "vote", `{"user":"U2"}`); err != nil {
		t.Fatal(err)
	}
	events, err := s.ListEventsForItem(it.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Kind != "vote" {
		t.Fatalf("event mismatch: %+v", events)
	}
}
