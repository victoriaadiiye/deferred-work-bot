package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestTicker_PostsReminderAfter3Days(t *testing.T) {
	store := newTestStore(t)
	fake := newFakeSlack("UBOT")
	now := time.Now()
	older := now.Add(-4 * 24 * time.Hour)
	it := &Item{SlackChannel: "C1", SlackTS: "1700.1", AuthorSlackID: "U1", Text: "x", Status: "collecting", ApprovalThreshold: 3}
	store.InsertItem(it)
	store.db.Exec(`UPDATE items SET created_at = ? WHERE id = ?`, older, it.ID)

	tk := &Ticker{
		Store: store, Slack: fake,
		ReminderEvery: 3 * 24 * time.Hour,
		WarnAt:        10 * 24 * time.Hour,
		ArchiveAt:     13 * 24 * time.Hour,
		Now:           func() time.Time { return now },
	}
	tk.Tick(context.Background())
	if len(fake.posted) == 0 || !strings.Contains(fake.posted[0].Text, "") {
		t.Fatal("expected reminder posted")
	}
	got, _ := store.GetItemByID(it.ID)
	if got.LastReminderAt == nil {
		t.Fatal("expected last_reminder_at set")
	}
}

func TestTicker_PostsWarningAt10Days(t *testing.T) {
	store := newTestStore(t)
	fake := newFakeSlack("UBOT")
	now := time.Now()
	older := now.Add(-11 * 24 * time.Hour)
	it := &Item{SlackChannel: "C1", SlackTS: "1700.1", AuthorSlackID: "U1", Text: "x", Status: "collecting", ApprovalThreshold: 3}
	store.InsertItem(it)
	store.db.Exec(`UPDATE items SET created_at = ?, last_reminder_at = ? WHERE id = ?`, older, older, it.ID)

	tk := &Ticker{Store: store, Slack: fake, ReminderEvery: 3 * 24 * time.Hour, WarnAt: 10 * 24 * time.Hour, ArchiveAt: 13 * 24 * time.Hour, Now: func() time.Time { return now }}
	tk.Tick(context.Background())
	got, _ := store.GetItemByID(it.ID)
	if got.WarningPostedAt == nil {
		t.Fatal("expected warning posted")
	}
	if !strings.Contains(fake.posted[0].Text, ":rotating_light:") {
		t.Fatalf("expected warning emojis: %s", fake.posted[0].Text)
	}
}

func TestTicker_ArchivesAt13Days(t *testing.T) {
	store := newTestStore(t)
	fake := newFakeSlack("UBOT")
	now := time.Now()
	older := now.Add(-14 * 24 * time.Hour)
	warned := now.Add(-4 * 24 * time.Hour)
	it := &Item{SlackChannel: "C1", SlackTS: "1700.1", AuthorSlackID: "U1", Text: "x", Status: "collecting", ApprovalThreshold: 3}
	store.InsertItem(it)
	store.db.Exec(`UPDATE items SET created_at = ?, warning_posted_at = ? WHERE id = ?`, older, warned, it.ID)

	tk := &Ticker{Store: store, Slack: fake, ReminderEvery: 3 * 24 * time.Hour, WarnAt: 10 * 24 * time.Hour, ArchiveAt: 13 * 24 * time.Hour, Now: func() time.Time { return now }}
	tk.Tick(context.Background())
	got, _ := store.GetItemByID(it.ID)
	if got.Status != "archived" {
		t.Fatalf("status: %s", got.Status)
	}
}

func TestTicker_RetriesStuckProposing(t *testing.T) {
	store := newTestStore(t)
	fake := newFakeSlack("UBOT")
	w := &Worker{queue: make(chan job, 4)}
	now := time.Now()

	// Item stuck in "proposing" for 45 minutes — beyond the 30-minute threshold.
	it := &Item{SlackChannel: "C1", SlackTS: "1700.1", AuthorSlackID: "U1", Text: "x", Status: "proposing", ApprovalThreshold: 3}
	store.InsertItem(it)
	stuckAt := now.Add(-45 * time.Minute)
	store.db.Exec(`UPDATE items SET updated_at = ? WHERE id = ?`, stuckAt, it.ID)

	tk := &Ticker{
		Store: store, Slack: fake, Worker: w,
		ReminderEvery: 3 * 24 * time.Hour,
		WarnAt:        10 * 24 * time.Hour,
		ArchiveAt:     13 * 24 * time.Hour,
		Now:           func() time.Time { return now },
	}
	tk.Tick(context.Background())

	// A ProposeJob should have been enqueued.
	select {
	case j := <-w.queue:
		pj, ok := j.(ProposeJob)
		if !ok {
			t.Fatalf("expected ProposeJob, got %T", j)
		}
		if pj.ItemID != it.ID {
			t.Fatalf("expected itemID %d, got %d", it.ID, pj.ItemID)
		}
	default:
		t.Fatal("expected ProposeJob to be re-enqueued for stuck item")
	}
}

func TestTicker_DoesNotRetryRecentlyProposing(t *testing.T) {
	store := newTestStore(t)
	fake := newFakeSlack("UBOT")
	w := &Worker{queue: make(chan job, 4)}
	now := time.Now()

	// Item in "proposing" for only 5 minutes — below the threshold.
	it := &Item{SlackChannel: "C1", SlackTS: "1700.1", AuthorSlackID: "U1", Text: "x", Status: "proposing", ApprovalThreshold: 3}
	store.InsertItem(it)
	recentAt := now.Add(-5 * time.Minute)
	store.db.Exec(`UPDATE items SET updated_at = ? WHERE id = ?`, recentAt, it.ID)

	tk := &Ticker{
		Store: store, Slack: fake, Worker: w,
		ReminderEvery: 3 * 24 * time.Hour,
		WarnAt:        10 * 24 * time.Hour,
		ArchiveAt:     13 * 24 * time.Hour,
		Now:           func() time.Time { return now },
	}
	tk.Tick(context.Background())

	select {
	case <-w.queue:
		t.Fatal("should not have re-enqueued recently-proposing item")
	default:
		// expected: no job
	}
}

func TestTicker_NewVotesAfterWarningRevertsLifecycle(t *testing.T) {
	// If vote count >= threshold, the propose advancement happens elsewhere (router).
	// Ticker's contract: do not archive if proposal/ticket has been moved past 'collecting'.
	store := newTestStore(t)
	fake := newFakeSlack("UBOT")
	now := time.Now()
	older := now.Add(-14 * 24 * time.Hour)
	it := &Item{SlackChannel: "C1", SlackTS: "1700.1", AuthorSlackID: "U1", Text: "x", Status: "proposed", ApprovalThreshold: 3}
	store.InsertItem(it)
	store.db.Exec(`UPDATE items SET created_at = ?, warning_posted_at = ? WHERE id = ?`, older, now.Add(-4*24*time.Hour), it.ID)

	tk := &Ticker{Store: store, Slack: fake, ReminderEvery: 3 * 24 * time.Hour, WarnAt: 10 * 24 * time.Hour, ArchiveAt: 13 * 24 * time.Hour, Now: func() time.Time { return now }}
	tk.Tick(context.Background())
	got, _ := store.GetItemByID(it.ID)
	if got.Status == "archived" {
		t.Fatal("non-collecting items should not be archived by ticker")
	}
}
