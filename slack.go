package main

import "github.com/slack-go/slack"

type SlackAPI interface {
	PostMessage(channelID string, options ...slack.MsgOption) (channel string, ts string, err error)
	AddReaction(name string, item slack.ItemRef) error
	RemoveReaction(name string, item slack.ItemRef) error
	GetConversationReplies(params *slack.GetConversationRepliesParameters) (msgs []slack.Message, hasMore bool, nextCursor string, err error)
	GetPermalink(params *slack.PermalinkParameters) (string, error)
	AuthTest() (*slack.AuthTestResponse, error)
}

// Worker is a stub; Task 26 replaces this with the real implementation.
type Worker struct{ queue chan job }

type job interface{ kind() string }

func (w *Worker) Submit(j job) {
	if w == nil || w.queue == nil {
		return
	}
	select {
	case w.queue <- j:
	default:
	}
}

// ProposeJob is a stub; Task 26 will replace it.
type ProposeJob struct{ ItemID int64 }

func (ProposeJob) kind() string { return "propose" }

type Router struct {
	Store             *Store
	Slack             SlackAPI
	BotUserID         string
	WatchedChannels   map[string]bool
	ApprovalThreshold int
	Signals           *SignalsConfig
	Projects          *ProjectsConfig
	Worker            *Worker
	Config            *Config
}

type MessageEvent struct {
	Channel  string
	TS       string
	ThreadTS string
	User     string
	Text     string
	Edited   bool
	Deleted  bool
}

func (r *Router) HandleMessage(e MessageEvent) {
	if e.User == r.BotUserID || e.User == "" {
		return
	}
	if e.ThreadTS != "" && e.ThreadTS != e.TS {
		r.handleThreadReply(e)
		return
	}
	if !r.WatchedChannels[e.Channel] {
		return
	}
	if e.Deleted {
		// future: mark cancelled
		return
	}
	it := &Item{
		SlackChannel:      e.Channel,
		SlackTS:           e.TS,
		AuthorSlackID:     e.User,
		Text:              e.Text,
		Status:            "collecting",
		ApprovalThreshold: r.ApprovalThreshold,
	}
	if err := r.Store.InsertItem(it); err != nil {
		return
	}
	r.Slack.AddReaction("eyes", slackItem(e.Channel, e.TS))
	r.Store.LogEvent(&it.ID, "created", "{}")
}

func (r *Router) handleThreadReply(e MessageEvent) {
	// Implemented in later task.
}

type ReactionEvent struct {
	User    string
	Channel string
	TS      string
	Name    string
}

func (r *Router) HandleReactionAdded(e ReactionEvent) {
	if e.User == r.BotUserID {
		return
	}
	it, err := r.Store.GetItemByTS(e.Channel, e.TS)
	if err != nil {
		// could be a proposal reaction — handled in proposal-approval task
		r.handleProposalReaction(e, /*add=*/ true)
		return
	}
	if isTerminal(it.Status) {
		return
	}
	if IsCancelReaction(r.Signals, e.Name) {
		r.Store.UpdateItemStatus(it.ID, "cancelled")
		r.Store.LogEvent(&it.ID, "cancel", `{"by":"`+e.User+`","via":"reaction"}`)
		r.Slack.AddReaction("wastebasket", slackItem(e.Channel, e.TS))
		return
	}
	if !IsApproveReaction(r.Signals, e.Name) {
		return
	}
	if e.User == it.AuthorSlackID {
		return
	}
	r.Store.UpsertVote(it.ID, e.User, "reaction", e.Name)
	r.Store.LogEvent(&it.ID, "vote", `{"user":"`+e.User+`","source":"reaction","signal":"`+e.Name+`"}`)
	r.maybeAdvanceToProposing(it)
}

func (r *Router) HandleReactionRemoved(e ReactionEvent) {
	if e.User == r.BotUserID {
		return
	}
	it, err := r.Store.GetItemByTS(e.Channel, e.TS)
	if err != nil {
		return
	}
	if isTerminal(it.Status) {
		return
	}
	if !IsApproveReaction(r.Signals, e.Name) {
		return
	}
	r.Store.RemoveVote(it.ID, e.User)
	r.Store.LogEvent(&it.ID, "vote_removed", `{"user":"`+e.User+`"}`)
}

func (r *Router) maybeAdvanceToProposing(it *Item) {
	n, _ := r.Store.CountVotes(it.ID)
	if n < it.ApprovalThreshold {
		return
	}
	if it.Status != "collecting" {
		return
	}
	r.Store.UpdateItemStatus(it.ID, "proposing")
	r.Store.LogEvent(&it.ID, "advanced", `{"reason":"threshold"}`)
	if r.Worker != nil {
		r.Worker.Submit(ProposeJob{ItemID: it.ID})
	}
}

func (r *Router) handleProposalReaction(e ReactionEvent, add bool) {
	// Implemented in proposal-approval task.
}

func isTerminal(status string) bool {
	switch status {
	case "ticketed", "commented_on_existing", "cancelled", "archived":
		return true
	}
	return false
}

func slackItem(channel, ts string) slack.ItemRef {
	return slack.ItemRef{Channel: channel, Timestamp: ts}
}
