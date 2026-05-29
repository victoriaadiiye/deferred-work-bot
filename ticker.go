package main

import (
	"context"
	"fmt"
	"time"

	"github.com/slack-go/slack"
)

// stuckProposingThreshold is the minimum duration an item must have been in
// status=proposing before the ticker re-enqueues it as a ProposeJob.
const stuckProposingThreshold = 30 * time.Minute

type Ticker struct {
	Store         *Store
	Slack         SlackAPI
	Worker        *Worker
	ReminderEvery time.Duration
	WarnAt        time.Duration
	ArchiveAt     time.Duration
	Now           func() time.Time
}

func (t *Ticker) Tick(ctx context.Context) {
	items, err := t.Store.ListItemsByStatus("collecting")
	if err != nil {
		return
	}
	now := t.now()
	for _, it := range items {
		age := now.Sub(it.CreatedAt)
		if t.shouldArchive(it, now) {
			t.archive(it)
			continue
		}
		if t.shouldWarn(it, age) {
			t.warn(it, now)
			continue
		}
		if t.shouldRemind(it, age, now) {
			t.remind(it, now)
		}
	}

	// Re-enqueue any items stuck in proposing for longer than the threshold.
	cutoff := now.Add(-stuckProposingThreshold)
	stuck, err := t.Store.ListItemsByStatusOlderThan("proposing", cutoff)
	if err != nil {
		return
	}
	for _, it := range stuck {
		if t.Worker != nil {
			t.Worker.Submit(ProposeJob{ItemID: it.ID})
		}
	}
}

func (t *Ticker) now() time.Time {
	if t.Now != nil {
		return t.Now()
	}
	return time.Now()
}

func (t *Ticker) shouldRemind(it *Item, age time.Duration, now time.Time) bool {
	if age < t.ReminderEvery {
		return false
	}
	if it.LastReminderAt == nil {
		return true
	}
	return now.Sub(*it.LastReminderAt) >= t.ReminderEvery
}

func (t *Ticker) shouldWarn(it *Item, age time.Duration) bool {
	return age >= t.WarnAt && it.WarningPostedAt == nil
}

func (t *Ticker) shouldArchive(it *Item, now time.Time) bool {
	if it.WarningPostedAt == nil {
		return false
	}
	return now.Sub(*it.WarningPostedAt) >= (t.ArchiveAt - t.WarnAt)
}

func (t *Ticker) remind(it *Item, now time.Time) {
	n, _ := t.Store.CountVotes(it.ID)
	age := now.Sub(it.CreatedAt).Hours() / 24
	body := fmt.Sprintf("Still pending — *%d/%d* approvals, *%.1fd* idle. Original:\n> %s",
		n, it.ApprovalThreshold, age, truncate(it.Text, 200))
	t.Slack.PostMessage(it.SlackChannel,
		slack.MsgOptionText(body, false),
		slack.MsgOptionTS(it.SlackTS))
	t.Store.UpdateItemReminderTimes(it.ID, &now, it.WarningPostedAt)
	t.Store.LogEvent(&it.ID, "reminder", "{}")
}

func (t *Ticker) warn(it *Item, now time.Time) {
	body := fmt.Sprintf(":rotating_light: :warning: *Deferred-work auto-archive incoming.* "+
		"This item will be archived in 3 days unless it gets activity. :warning: :rotating_light:\n> %s",
		truncate(it.Text, 200))
	t.Slack.PostMessage(it.SlackChannel,
		slack.MsgOptionText(body, false),
		slack.MsgOptionTS(it.SlackTS))
	t.Store.UpdateItemReminderTimes(it.ID, it.LastReminderAt, &now)
	t.Store.LogEvent(&it.ID, "warning", "{}")
}

func (t *Ticker) archive(it *Item) {
	t.Store.UpdateItemStatus(it.ID, "archived")
	t.Store.LogEvent(&it.ID, "archive", "{}")
	t.Slack.AddReaction("wastebasket", slack.ItemRef{Channel: it.SlackChannel, Timestamp: it.SlackTS})
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
