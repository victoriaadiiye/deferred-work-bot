package main

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/slack-go/slack"
)

type SlackAPI interface {
	PostMessage(channelID string, options ...slack.MsgOption) (channel string, ts string, err error)
	AddReaction(name string, item slack.ItemRef) error
	RemoveReaction(name string, item slack.ItemRef) error
	GetConversationReplies(params *slack.GetConversationRepliesParameters) (msgs []slack.Message, hasMore bool, nextCursor string, err error)
	GetPermalink(params *slack.PermalinkParameters) (string, error)
	GetUserInfo(user string) (*slack.User, error)
	AuthTest() (*slack.AuthTestResponse, error)
}


type Router struct {
	Store             *Store
	Slack             SlackAPI
	BotUserID         string
	WatchedChannels   map[string]bool
	ApprovalThreshold int
	AuthorCanApprove  bool
	Signals           *SignalsConfig
	Projects          *ProjectsConfig
	Worker            *Worker
	Config            *Config
	Claude            claudeAPI
	ReminderInterval  time.Duration
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
	r.seedVoteReactions(e.Channel, e.TS)
	r.Store.LogEvent(&it.ID, "created", "{}")
	if r.Worker != nil {
		r.Worker.Submit(IntakeJob{ItemID: it.ID})
	}
}

// seedVoteReactions adds the approve/cancel reactions to a tracked message so
// members can vote with a single click instead of hunting for an emoji.
func (r *Router) seedVoteReactions(channel, ts string) {
	r.Slack.AddReaction("white_check_mark", slackItem(channel, ts))
	r.Slack.AddReaction("x", slackItem(channel, ts))
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
		if e.User == parent.AuthorSlackID && !r.AuthorCanApprove {
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
	case strings.HasPrefix(cmd, "epic:"):
		r.cmdEpic(it, e, strings.TrimSpace(strings.TrimPrefix(cmd, "epic:")))
	default:
		r.cmdFreeform(it, e, cmd)
	}
}

// normalizeCommand strips the bot mention and lowercases the remainder.
func normalizeCommand(text, botID string) string {
	t := strings.ReplaceAll(text, "<@"+botID+">", "")
	return strings.ToLower(strings.TrimSpace(t))
}

func (r *Router) cmdStatus(it *Item, e MessageEvent) {
	n, _ := r.Store.CountVotes(it.ID)
	age := time.Since(it.CreatedAt).Hours() / 24

	var lines []string
	lines = append(lines, fmt.Sprintf("*Status:* `%s` — *%d/%d* approvals, idle *%.1fd*", it.Status, n, it.ApprovalThreshold, age))

	// Voters list.
	voters, _ := r.Store.ListVoters(it.ID)
	if len(voters) > 0 {
		mentions := make([]string, len(voters))
		for i, u := range voters {
			mentions[i] = "<@" + u + ">"
		}
		lines = append(lines, "*Voters:* "+strings.Join(mentions, ", "))
	} else {
		lines = append(lines, "*Voters:* none yet")
	}

	// Next-reminder ETA (only meaningful while still collecting).
	if it.Status == "collecting" && r.ReminderInterval > 0 {
		var nextReminder time.Time
		if it.LastReminderAt != nil {
			nextReminder = it.LastReminderAt.Add(r.ReminderInterval)
		} else {
			nextReminder = it.CreatedAt.Add(r.ReminderInterval)
		}
		eta := time.Until(nextReminder)
		var etaStr string
		if eta <= 0 {
			etaStr = "due now"
		} else {
			etaStr = fmt.Sprintf("in %.1fd", eta.Hours()/24)
		}
		lines = append(lines, "*Next reminder:* "+etaStr)
	}

	msg := strings.Join(lines, "\n")
	r.Slack.PostMessage(e.Channel, slack.MsgOptionText(msg, false), slack.MsgOptionTS(it.SlackTS))
}

func (r *Router) cmdHelp(it *Item, e MessageEvent) {
	r.Slack.PostMessage(e.Channel, slack.MsgOptionText(helpText, false), slack.MsgOptionTS(it.SlackTS))
}

const helpText = "*deferred-work-bot commands:*\n" +
	"• `@bot status` — show votes + idle time\n" +
	"• `@bot cancel` — withdraw item\n" +
	"• `@bot regen` — re-draft proposal with latest thread context\n" +
	"• `@bot project: <name>` — override sub-project label\n" +
	"• `@bot priority: <low|medium|high>` — override priority\n" +
	"• `@bot epic: <KEY|none>` — set/clear the parent epic\n" +
	"• `@bot file now` — skip approval gate; propose immediately\n" +
	"• `@bot search` — re-run related-ticket search\n" +
	"• `@bot help` — this message\n" +
	"• `@bot <question>` — free-form question about this item"

func (r *Router) cmdCancel(it *Item, e MessageEvent) {
	if err := r.Store.UpdateItemStatus(it.ID, "cancelled"); err != nil {
		return
	}
	r.Store.LogEvent(&it.ID, "cancel", `{"by":"`+e.User+`","via":"@bot cancel"}`)
	r.Slack.AddReaction("wastebasket", slackItem(it.SlackChannel, it.SlackTS))
}

func (r *Router) cmdFileNow(it *Item, e MessageEvent) {
	if it.Status != "collecting" {
		return
	}
	r.Store.UpdateItemStatus(it.ID, "proposing")
	r.Store.LogEvent(&it.ID, "advanced", `{"reason":"file_now","by":"`+e.User+`"}`)
	if r.Worker != nil {
		r.Worker.Submit(ProposeJob{ItemID: it.ID})
	}
}

func (r *Router) cmdRegen(it *Item, e MessageEvent) {
	if p, err := r.Store.GetLatestProposal(it.ID); err == nil {
		r.Store.UpdateProposalStatus(p.ID, "rejected")
	}
	if it.Status != "proposed" && it.Status != "proposing" {
		r.Store.UpdateItemStatus(it.ID, "proposing")
	}
	r.Store.LogEvent(&it.ID, "regen", `{"by":"`+e.User+`"}`)
	if r.Worker != nil {
		r.Worker.Submit(ProposeJob{ItemID: it.ID})
	}
}

func (r *Router) cmdSearch(it *Item, e MessageEvent) {
	// Same as regen but only re-runs Jira search portion; for v1 we just submit
	// a ProposeJob — the worker will redo search as part of the flow.
	r.cmdRegen(it, e)
}

func (r *Router) cmdProject(it *Item, e MessageEvent, v string) {
	value := strings.ToLower(strings.TrimSpace(v))
	if value == "" {
		return
	}
	r.Store.UpdateItemSubproject(it.ID, value)
	r.Store.LogEvent(&it.ID, "project_override", `{"value":"`+value+`","by":"`+e.User+`"}`)
	r.Slack.AddReaction("white_check_mark", slackItem(e.Channel, e.TS))
	if it.Status == "proposed" {
		r.cmdRegen(it, e)
	}
}

func (r *Router) cmdPriority(it *Item, e MessageEvent, v string) {
	value := strings.ToLower(strings.TrimSpace(v))
	switch value {
	case "low", "medium", "med", "high":
	default:
		return
	}
	if value == "med" {
		value = "medium"
	}
	r.Store.LogEvent(&it.ID, "priority_override", `{"value":"`+value+`","by":"`+e.User+`"}`)
	r.Slack.AddReaction("white_check_mark", slackItem(e.Channel, e.TS))
	if it.Status == "proposed" {
		r.cmdRegen(it, e)
	}
}

// issueKeyRe matches a Jira issue key like QORK-441 (uppercased before test).
var issueKeyRe = regexp.MustCompile(`^[A-Z][A-Z0-9]+-[0-9]+$`)

// cmdEpic records a human override of the parent epic. `none` forces no epic;
// otherwise the value must look like an issue key. The override is re-applied
// on the next proposal, so a proposed item is regenerated immediately.
func (r *Router) cmdEpic(it *Item, e MessageEvent, v string) {
	value := strings.ToUpper(strings.TrimSpace(v))
	switch {
	case value == "":
		return
	case value == "NONE":
		value = "none"
	case !issueKeyRe.MatchString(value):
		return
	}
	r.Store.LogEvent(&it.ID, "epic_override", `{"value":"`+value+`","by":"`+e.User+`"}`)
	r.Slack.AddReaction("white_check_mark", slackItem(e.Channel, e.TS))
	if it.Status == "proposed" {
		r.cmdRegen(it, e)
	}
}

func (r *Router) cmdFreeform(it *Item, e MessageEvent, q string) {
	if r.Claude == nil {
		return
	}
	thread, _, _, _ := r.Slack.GetConversationReplies(&slack.GetConversationRepliesParameters{ChannelID: it.SlackChannel, Timestamp: it.SlackTS})
	var ctx []string
	for _, m := range thread {
		ctx = append(ctx, m.Text)
	}
	prompt := fmt.Sprintf(`Answer this question about a deferred-work item.

ITEM:
%s

THREAD:
%s

QUESTION:
%s

Be concise (under 100 words). Reply with plain text only.`, it.Text, strings.Join(ctx, "\n---\n"), q)
	out, err := r.Claude.Run(context.Background(), prompt)
	if err != nil {
		r.Slack.PostMessage(e.Channel, slack.MsgOptionText("(claude error)", false), slack.MsgOptionTS(it.SlackTS))
		return
	}
	r.Slack.PostMessage(e.Channel, slack.MsgOptionText(strings.TrimSpace(out), false), slack.MsgOptionTS(it.SlackTS))
}

func (r *Router) handleResolution(it *Item, p *Proposal, keyword string, e MessageEvent) {
	var branch string
	switch keyword {
	case "new":
		branch = "new"
	case "comment":
		branch = "comment_on_existing"
	case "both":
		branch = "both"
	default:
		return
	}
	r.Store.UpdateProposalBranch(p.ID, branch, "draft")
	r.Store.LogEvent(&it.ID, "resolution", `{"branch":"`+branch+`"}`)
	r.Slack.PostMessage(e.Channel,
		slack.MsgOptionText(fmt.Sprintf("Resolution: *%s*. React with any approve signal to file.", branch), false),
		slack.MsgOptionTS(it.SlackTS))
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
	if e.User == it.AuthorSlackID && !r.AuthorCanApprove {
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
	if !add {
		return
	}
	p, err := r.Store.GetProposalBySlackTS(e.TS)
	if err != nil {
		return
	}
	if p.Status != "draft" {
		return
	}
	if !IsApproveReaction(r.Signals, e.Name) {
		return
	}
	it, _ := r.Store.GetItemByID(p.ItemID)
	if it == nil || isTerminal(it.Status) {
		return
	}
	if r.Worker == nil {
		return
	}
	r.Worker.Submit(FileJob{ProposalID: p.ID})
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
	r.seedVoteReactions(e.Channel, e.TS)
	r.Store.LogEvent(&it.ID, "created", `{"via":"app_mention"}`)
	if r.Worker != nil {
		r.Worker.Submit(IntakeJob{ItemID: it.ID})
	}
}
