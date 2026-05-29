package main

import (
	"fmt"
	"strings"

	"github.com/slack-go/slack"
)

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
	parent, err := r.Store.GetItemByTS(e.Channel, e.ThreadTS)
	if err != nil {
		return
	}
	if isTerminal(parent.Status) {
		return
	}
	text := strings.ToLower(e.Text)

	if ReplyHasCancel(r.Signals, text) || r.botMentioned(e.Text) && strings.Contains(text, "cancel") {
		r.Store.UpdateItemStatus(parent.ID, "cancelled")
		r.Store.LogEvent(&parent.ID, "cancel", `{"by":"`+e.User+`","via":"reply"}`)
		r.Slack.AddReaction("wastebasket", slackItem(parent.SlackChannel, parent.SlackTS))
		return
	}

	if r.botMentioned(e.Text) {
		r.dispatchCommand(parent, e)
		return
	}

	// Resolution keywords only apply when latest proposal is awaiting resolution.
	if p, err := r.Store.GetLatestProposal(parent.ID); err == nil && p.Status == "awaiting_resolution" {
		if kw := ResolutionKeyword(text); kw != "" {
			r.handleResolution(parent, p, kw, e)
			return
		}
	}

	if ReplyHasApprove(r.Signals, text) {
		if e.User == parent.AuthorSlackID {
			return
		}
		r.Store.UpsertVote(parent.ID, e.User, "reply", "keyword")
		r.Store.LogEvent(&parent.ID, "vote", `{"user":"`+e.User+`","source":"reply"}`)
		// If this is a proposal-thread vote on the proposal message itself,
		// handle that separately. Otherwise it's a vote on the item.
		r.maybeAdvanceToProposing(parent)
	}
}

func (r *Router) botMentioned(text string) bool {
	return strings.Contains(text, "<@"+r.BotUserID+">")
}

func (r *Router) dispatchCommand(it *Item, e MessageEvent) {
	cmd := normalizeCommand(e.Text, r.BotUserID)
	switch {
	case cmd == "status":
		r.cmdStatus(it, e)
	case cmd == "help":
		r.cmdHelp(it, e)
	case cmd == "cancel":
		r.cmdCancel(it, e)
	case cmd == "file now":
		r.cmdFileNow(it, e)
	case cmd == "regen":
		r.cmdRegen(it, e)
	case cmd == "search":
		r.cmdSearch(it, e)
	case strings.HasPrefix(cmd, "project:"):
		r.cmdProject(it, e, strings.TrimSpace(strings.TrimPrefix(cmd, "project:")))
	case strings.HasPrefix(cmd, "priority:"):
		r.cmdPriority(it, e, strings.TrimSpace(strings.TrimPrefix(cmd, "priority:")))
	default:
		r.cmdFreeform(it, e, cmd)
	}
}

// normalizeCommand strips the bot mention and lowercases the remainder.
func normalizeCommand(text, botID string) string {
	t := strings.ReplaceAll(text, "<@"+botID+">", "")
	return strings.ToLower(strings.TrimSpace(t))
}

// Stubs — real implementations land in later tasks.
func (r *Router) cmdStatus(it *Item, e MessageEvent) {
	n, _ := r.Store.CountVotes(it.ID)
	msg := fmt.Sprintf("Status: *%s* — %d/%d approvals", it.Status, n, it.ApprovalThreshold)
	r.Slack.PostMessage(e.Channel, slack.MsgOptionText(msg, false), slack.MsgOptionTS(it.SlackTS))
}
func (r *Router) cmdHelp(it *Item, e MessageEvent)               { r.postHelp(e) }
func (r *Router) cmdCancel(it *Item, e MessageEvent)             { r.Store.UpdateItemStatus(it.ID, "cancelled"); r.Slack.AddReaction("wastebasket", slackItem(it.SlackChannel, it.SlackTS)) }
func (r *Router) cmdFileNow(it *Item, e MessageEvent)            { /* Task 23 */ }
func (r *Router) cmdRegen(it *Item, e MessageEvent)              { /* Task 24 */ }
func (r *Router) cmdSearch(it *Item, e MessageEvent)             { /* Task 24 */ }
func (r *Router) cmdProject(it *Item, e MessageEvent, v string)  { r.Store.UpdateItemSubproject(it.ID, v); r.Slack.AddReaction("white_check_mark", slackItem(e.Channel, e.TS)) }
func (r *Router) cmdPriority(it *Item, e MessageEvent, v string) { /* Task 24 */ }
func (r *Router) cmdFreeform(it *Item, e MessageEvent, q string) { /* Task 25 */ }

func (r *Router) postHelp(e MessageEvent) {
	help := "*Commands:* `status`, `cancel`, `regen`, `project: <name>`, `priority: <low|med|high>`, `file now`, `search`, `help`, or any free-form question."
	r.Slack.PostMessage(e.Channel, slack.MsgOptionText(help, false), slack.MsgOptionTS(e.ThreadTS))
}

func (r *Router) handleResolution(it *Item, p *Proposal, keyword string, e MessageEvent) {
	// Implemented in Task 21.
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

func (r *Router) HandleMessageDeleted(e MessageEvent) {
	it, err := r.Store.GetItemByTS(e.Channel, e.TS)
	if err != nil {
		return
	}
	if isTerminal(it.Status) {
		return
	}
	r.Store.UpdateItemStatus(it.ID, "cancelled")
	r.Store.LogEvent(&it.ID, "cancel", `{"reason":"message_deleted"}`)
}

func (r *Router) HandleMessageChanged(e MessageEvent) {
	it, err := r.Store.GetItemByTS(e.Channel, e.TS)
	if err != nil {
		return
	}
	if isTerminal(it.Status) {
		return
	}
	r.Store.UpdateItemText(it.ID, e.Text)
	r.Store.LogEvent(&it.ID, "edited", "{}")
}

func (r *Router) HandleAppMention(e MessageEvent) {
	if e.User == r.BotUserID || e.User == "" {
		return
	}
	if e.ThreadTS != "" && e.ThreadTS != e.TS {
		// route as thread reply
		r.handleThreadReply(e)
		return
	}
	// Top-level @mention in a non-watched channel — create item.
	if _, err := r.Store.GetItemByTS(e.Channel, e.TS); err == nil {
		return // already tracked
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
	r.Store.LogEvent(&it.ID, "created", `{"via":"app_mention"}`)
}
