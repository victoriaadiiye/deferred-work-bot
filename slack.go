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

func slackItem(channel, ts string) slack.ItemRef {
	return slack.ItemRef{Channel: channel, Timestamp: ts}
}
