package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/slack-go/slack"
)

type claudeAPI interface {
	Run(ctx context.Context, prompt string) (string, error)
}

func detectSubprojectByKeyword(cfg *ProjectsConfig, text string) string {
	low := strings.ToLower(text)
	for _, sub := range cfg.Subprojects {
		if strings.Contains(low, strings.ToLower(sub)) {
			return sub
		}
	}
	return ""
}

func detectSubproject(ctx context.Context, cfg *ProjectsConfig, c claudeAPI, text string) (string, error) {
	if v := detectSubprojectByKeyword(cfg, text); v != "" {
		return v, nil
	}
	prompt := fmt.Sprintf(`You are categorizing a piece of work into one of these sub-projects.

Sub-projects: %s

Text: %q

Return JSON: {"subproject": "<one of the sub-projects or empty string>"}
Only return the JSON, no other text.`, strings.Join(cfg.Subprojects, ", "), text)
	out, err := c.Run(ctx, prompt)
	if err != nil {
		return "", err
	}
	jsonStr, err := ExtractJSON(out)
	if err != nil {
		return "", nil // fail soft — treat as none
	}
	var parsed struct {
		Subproject string `json:"subproject"`
	}
	if err := json.Unmarshal([]byte(jsonStr), &parsed); err != nil {
		return "", nil
	}
	if !containsLower(cfg.Subprojects, parsed.Subproject) {
		return "", nil
	}
	return parsed.Subproject, nil
}

func containsLower(list []string, v string) bool {
	v = strings.ToLower(v)
	for _, x := range list {
		if strings.ToLower(x) == v {
			return true
		}
	}
	return false
}

type RelatedTicket struct {
	Key     string `json:"key"`
	Summary string `json:"summary,omitempty"`
	Verdict string `json:"verdict"` // encompassed|referenced|unrelated
}

func classifyRelatedTickets(ctx context.Context, c claudeAPI, workText string, issues []JiraIssue) ([]RelatedTicket, error) {
	if len(issues) == 0 {
		return nil, nil
	}
	summaries := make([]map[string]any, len(issues))
	for i, iss := range issues {
		summaries[i] = map[string]any{"key": iss.Key, "summary": iss.Fields.Summary}
	}
	payload, _ := json.Marshal(summaries)
	prompt := fmt.Sprintf(`Classify each Jira ticket relative to this deferred-work item.

WORK ITEM:
%s

TICKETS:
%s

For each ticket, return a JSON array of objects: {"key": "...", "verdict": "encompassed"|"referenced"|"unrelated"}
- "encompassed": this ticket already covers the same scope of work; filing a new ticket would duplicate.
- "referenced": this ticket touches related code/concepts but is not the same work.
- "unrelated": no meaningful overlap.
Only return the JSON array, no other text.`, workText, string(payload))
	out, err := c.Run(ctx, prompt)
	if err != nil {
		return nil, err
	}
	jsonStr, err := ExtractJSON(out)
	if err != nil {
		return nil, err
	}
	var res []RelatedTicket
	if err := json.Unmarshal([]byte(jsonStr), &res); err != nil {
		return nil, err
	}
	return res, nil
}

type DraftInput struct {
	Text         string
	Thread       []string
	Subproject   string
	PriorityOver string
	Permalink    string
}

type Draft struct {
	Summary     string   `json:"summary"`
	Description string   `json:"description"`
	IssueType   string   `json:"issue_type"`
	Labels      []string `json:"labels"`
	Priority    string   `json:"priority"`
}

func DraftTicket(ctx context.Context, c claudeAPI, in DraftInput) (*Draft, error) {
	prompt := fmt.Sprintf(`You are drafting a Jira ticket from a Slack deferred-work item.

Sub-project label: %q
Original message:
%s

Thread comments:
%s

Slack permalink: %s

Return JSON:
{
  "summary": "<one-line, imperative voice, <=120 chars>",
  "description": "<multi-paragraph description, include original message verbatim, then synthesized context from comments, then a final line with the Slack permalink>",
  "labels": ["deferred-work"%s],
  "priority": "Low|Medium|High"
}
Only return the JSON, no other text.`,
		in.Subproject,
		in.Text,
		strings.Join(in.Thread, "\n---\n"),
		in.Permalink,
		labelHint(in.Subproject),
	)
	out, err := c.Run(ctx, prompt)
	if err != nil {
		return nil, err
	}
	js, err := ExtractJSON(out)
	if err != nil {
		return nil, err
	}
	var d Draft
	if err := json.Unmarshal([]byte(js), &d); err != nil {
		return nil, err
	}
	d.IssueType = "Task"
	if in.PriorityOver != "" {
		d.Priority = in.PriorityOver
	}
	if d.Priority == "" {
		d.Priority = "Medium"
	}
	// Ensure deferred-work + subproject labels are present.
	d.Labels = ensureLabels(d.Labels, "deferred-work", in.Subproject)
	return &d, nil
}

func labelHint(sub string) string {
	if sub == "" {
		return ""
	}
	return `, "` + sub + `"`
}

func ensureLabels(labels []string, required ...string) []string {
	seen := map[string]bool{}
	for _, l := range labels {
		seen[l] = true
	}
	out := labels
	for _, r := range required {
		if r == "" || seen[r] {
			continue
		}
		out = append(out, r)
		seen[r] = true
	}
	return out
}

// RenderProposalMessage builds the Slack message body for a deferred-work proposal.
func RenderProposalMessage(d *Draft, rels []RelatedTicket, branch, existingKey string, ttlTriggered bool) string {
	var b strings.Builder
	if ttlTriggered {
		b.WriteString(":warning: *no response from team in 3 days — proposing anyway*\n\n")
	}
	if branch == "awaiting_resolution" {
		fmt.Fprintf(&b, "*Existing ticket may cover this:* <%s|%s> (encompassed).\n\n", existingKey, existingKey)
		b.WriteString("Reply `comment` to add a follow-up to the existing ticket, `new` to file a fresh one, or `both` for both.\n")
		return b.String()
	}
	if d != nil {
		fmt.Fprintf(&b, "*Proposed ticket — %s (%s)*\n", d.IssueType, d.Priority)
		fmt.Fprintf(&b, "*Summary:* %s\n", d.Summary)
		if d.Description != "" {
			desc := d.Description
			if len(desc) > 600 {
				desc = desc[:600] + "…"
			}
			fmt.Fprintf(&b, "*Description preview:*\n```\n%s\n```\n", desc)
		}
		fmt.Fprintf(&b, "*Labels:* %s\n", strings.Join(d.Labels, ", "))
	}
	if len(rels) > 0 {
		b.WriteString("\n*Related tickets:*\n")
		for _, r := range rels {
			if r.Verdict == "unrelated" {
				continue
			}
			fmt.Fprintf(&b, "• <%s|%s> — %s\n", r.Key, r.Key, r.Verdict)
		}
	}
	b.WriteString("\n_React with any approve signal to file. `@bot regen` to revise._")
	return b.String()
}

type jiraAPI interface {
	Search(in JiraSearchInput) ([]JiraIssue, error)
	CreateIssue(in CreateIssueInput) (*CreatedIssue, error)
	AddComment(key, text string) error
	AddLabel(key, label string) error
}

type FileInput struct {
	Branch            string // new|comment_on_existing|both
	ProjectKey        string
	ExistingTicketKey string
	CommentText       string // synthesized context for the existing-ticket branch
	Draft             *Draft
}

type FileResult struct {
	NewKey      string
	NewURL      string
	CommentedOn string
}

func FileProposal(j jiraAPI, in FileInput) (*FileResult, error) {
	res := &FileResult{}
	if in.Branch == "comment_on_existing" || in.Branch == "both" {
		if in.ExistingTicketKey == "" {
			return nil, errors.New("existing ticket key required for comment branch")
		}
		if err := j.AddComment(in.ExistingTicketKey, in.CommentText); err != nil {
			return nil, err
		}
		if err := j.AddLabel(in.ExistingTicketKey, "deferred-work-followup"); err != nil {
			return nil, err
		}
		res.CommentedOn = in.ExistingTicketKey
	}
	if in.Branch == "new" || in.Branch == "both" {
		if in.Draft == nil {
			return nil, errors.New("draft required for new-ticket branch")
		}
		created, err := j.CreateIssue(CreateIssueInput{
			ProjectKey:  in.ProjectKey,
			Summary:     in.Draft.Summary,
			Description: in.Draft.Description,
			IssueType:   in.Draft.IssueType,
			Labels:      in.Draft.Labels,
			Priority:    in.Draft.Priority,
		})
		if err != nil {
			return nil, err
		}
		res.NewKey = created.Key
		res.NewURL = created.URL
	}
	return res, nil
}

// DecideBranch picks the proposal branch from related-ticket classifications.
// Returns (branch, existingKey). existingKey is set only when branch == "awaiting_resolution".
func DecideBranch(rels []RelatedTicket) (string, string) {
	for _, r := range rels {
		if r.Verdict == "encompassed" {
			return "awaiting_resolution", r.Key
		}
	}
	return "new", ""
}

type JobExecutor struct {
	Store     *Store
	Slack     SlackAPI
	Claude    claudeAPI
	Jira      jiraAPI
	Projects  *ProjectsConfig
	Signals   *SignalsConfig
	BotUserID string
}

func (e *JobExecutor) Execute(ctx context.Context, j job) error {
	switch v := j.(type) {
	case ProposeJob:
		return e.executePropose(ctx, v.ItemID)
	case FileJob:
		return e.executeFile(ctx, v.ProposalID)
	case ReminderJob:
		return e.executeReminder(ctx, v.ItemID)
	}
	return fmt.Errorf("unknown job: %s", j.kind())
}

func (e *JobExecutor) executePropose(ctx context.Context, itemID int64) error {
	it, err := e.Store.GetItemByID(itemID)
	if err != nil {
		return err
	}
	if it.Status == "cancelled" || it.Status == "archived" {
		return nil
	}

	// 1. Load thread.
	msgs, _, _, _ := e.Slack.GetConversationReplies(&slack.GetConversationRepliesParameters{ChannelID: it.SlackChannel, Timestamp: it.SlackTS})
	var thread []string
	for _, m := range msgs {
		if m.User == e.BotUserID || m.Timestamp == it.SlackTS {
			continue
		}
		thread = append(thread, m.Text)
	}

	// 2. Subproject (use override if present).
	sub := it.Subproject
	if sub == "" {
		v, _ := detectSubproject(ctx, e.Projects, e.Claude, it.Text+"\n"+strings.Join(thread, "\n"))
		sub = v
		if sub != "" {
			e.Store.UpdateItemSubproject(it.ID, sub)
		}
	}

	// 3. Jira search.
	keywords := extractKeywords(it.Text)
	issues, _ := e.Jira.Search(JiraSearchInput{
		Projects:   e.Projects.QORKProjects,
		Subproject: sub,
		Keywords:   keywords,
		Limit:      20,
	})

	// 4. Classify relevance.
	rels, _ := classifyRelatedTickets(ctx, e.Claude, it.Text, issues)
	branch, existing := DecideBranch(rels)

	// 5. Draft (skip for awaiting_resolution path).
	var draft *Draft
	if branch == "new" {
		permalink, _ := e.Slack.GetPermalink(&slack.PermalinkParameters{Channel: it.SlackChannel, Ts: it.SlackTS})
		priority, _ := e.Store.LatestOverride(it.ID, "priority_override")
		d, err := DraftTicket(ctx, e.Claude, DraftInput{
			Text:         it.Text,
			Thread:       thread,
			Subproject:   sub,
			PriorityOver: priority,
			Permalink:    permalink,
		})
		if err != nil {
			return err
		}
		draft = d
	}

	// 6. Post proposal.
	body := RenderProposalMessage(draft, rels, branch, existing, false)
	_, ts, err := e.Slack.PostMessage(it.SlackChannel,
		slack.MsgOptionText(body, false),
		slack.MsgOptionTS(it.SlackTS))
	if err != nil {
		return err
	}

	// 7. Persist proposal row.
	draftJSON, _ := json.Marshal(draft)
	relsJSON, _ := json.Marshal(rels)
	p := &Proposal{
		ItemID:             it.ID,
		SlackTS:            ts,
		DraftJSON:          string(draftJSON),
		RelatedTicketsJSON: string(relsJSON),
		Branch:             branch,
		ExistingTicketKey:  existing,
		Status:             "draft",
	}
	if branch == "awaiting_resolution" {
		p.Status = "awaiting_resolution"
	}
	e.Store.InsertProposal(p)
	e.Store.UpdateItemStatus(it.ID, "proposed")
	e.Store.LogEvent(&it.ID, "proposal", `{"branch":"`+branch+`"}`)
	return nil
}

func (e *JobExecutor) executeFile(ctx context.Context, proposalID int64) error {
	p, err := e.Store.GetLatestProposalByID(proposalID)
	if err != nil {
		return err
	}
	if p.Status != "draft" {
		return nil
	}
	it, _ := e.Store.GetItemByID(p.ItemID)
	if it == nil || isTerminal(it.Status) {
		return nil
	}

	var draft Draft
	json.Unmarshal([]byte(p.DraftJSON), &draft)

	// When the item was originally awaiting_resolution and resolved to new/both,
	// the proposal was created without a draft. Re-draft now before filing.
	if (p.Branch == "new" || p.Branch == "both") && draft.Summary == "" {
		msgs, _, _, _ := e.Slack.GetConversationReplies(&slack.GetConversationRepliesParameters{ChannelID: it.SlackChannel, Timestamp: it.SlackTS})
		var thread []string
		for _, m := range msgs {
			if m.User == e.BotUserID || m.Timestamp == it.SlackTS {
				continue
			}
			thread = append(thread, m.Text)
		}
		permalink, _ := e.Slack.GetPermalink(&slack.PermalinkParameters{Channel: it.SlackChannel, Ts: it.SlackTS})
		priority, _ := e.Store.LatestOverride(it.ID, "priority_override")
		d, err := DraftTicket(ctx, e.Claude, DraftInput{
			Text:         it.Text,
			Thread:       thread,
			Subproject:   it.Subproject,
			PriorityOver: priority,
			Permalink:    permalink,
		})
		if err != nil {
			return err
		}
		draft = *d
	}

	commentText := buildExistingTicketComment(it.Text, draft.Description)

	projectKey := ""
	if len(e.Projects.QORKProjects) > 0 {
		projectKey = e.Projects.QORKProjects[0]
	}
	res, err := FileProposal(e.Jira, FileInput{
		Branch:            p.Branch,
		ProjectKey:        projectKey,
		ExistingTicketKey: p.ExistingTicketKey,
		CommentText:       commentText,
		Draft:             &draft,
	})
	if err != nil {
		e.Slack.PostMessage(it.SlackChannel,
			slack.MsgOptionText(":warning: Failed to file ticket: "+err.Error(), false),
			slack.MsgOptionTS(it.SlackTS))
		return err
	}

	e.Store.UpdateProposalStatus(p.ID, "filed")
	action := "created"
	jiraKey := res.NewKey
	jiraURL := res.NewURL
	if p.Branch == "comment_on_existing" {
		action = "commented_on_existing"
		jiraKey = res.CommentedOn
		jiraURL = ""
	}
	e.Store.InsertTicket(&Ticket{
		ProposalID:        p.ID,
		JiraKey:           jiraKey,
		JiraURL:           jiraURL,
		Action:            action,
		ExistingTicketKey: p.ExistingTicketKey,
	})

	switch p.Branch {
	case "new":
		e.Store.UpdateItemStatus(it.ID, "ticketed")
	case "comment_on_existing":
		e.Store.UpdateItemStatus(it.ID, "commented_on_existing")
	case "both":
		e.Store.UpdateItemStatus(it.ID, "ticketed")
	}

	msg := "Filed: "
	if res.NewKey != "" {
		msg += fmt.Sprintf("<%s|%s>", res.NewURL, res.NewKey)
	}
	if res.CommentedOn != "" {
		if res.NewKey != "" {
			msg += " + "
		}
		msg += "commented on " + res.CommentedOn
	}
	e.Slack.PostMessage(it.SlackChannel, slack.MsgOptionText(msg, false), slack.MsgOptionTS(it.SlackTS))
	e.Slack.AddReaction("white_check_mark", slack.ItemRef{Channel: it.SlackChannel, Timestamp: it.SlackTS})
	return nil
}

// postReminder posts a reminder message to the item's thread and updates
// LastReminderAt. It is called both by the ticker (via remind) and by
// executeReminder for manual /trigger?action=reminder requests.
func postReminder(store *Store, slackAPI SlackAPI, it *Item, via string) error {
	n, _ := store.CountVotes(it.ID)
	now := time.Now()
	age := now.Sub(it.CreatedAt).Hours() / 24
	body := fmt.Sprintf("Still pending — *%d/%d* approvals, *%.1fd* idle. Original:\n> %s",
		n, it.ApprovalThreshold, age, truncate(it.Text, 200))
	slackAPI.PostMessage(it.SlackChannel,
		slack.MsgOptionText(body, false),
		slack.MsgOptionTS(it.SlackTS))
	store.UpdateItemReminderTimes(it.ID, &now, it.WarningPostedAt)
	store.LogEvent(&it.ID, "reminder", `{"via":"`+via+`"}`)
	return nil
}

func (e *JobExecutor) executeReminder(ctx context.Context, itemID int64) error {
	it, err := e.Store.GetItemByID(itemID)
	if err != nil {
		return err
	}
	if isTerminal(it.Status) {
		return nil
	}
	return postReminder(e.Store, e.Slack, it, "trigger")
}

func buildExistingTicketComment(original, descPreview string) string {
	return "*Deferred-work follow-up*\n\nOriginal Slack message:\n" + original + "\n\nSynthesized context:\n" + descPreview
}

// extractKeywords is a tiny stopword filter; the worker uses claude inference for tougher cases.
func extractKeywords(text string) []string {
	stop := map[string]bool{
		"the": true, "a": true, "an": true, "is": true, "and": true, "or": true,
		"to": true, "for": true, "of": true, "in": true, "on": true, "we": true,
		"i": true, "this": true, "that": true, "be": true, "it": true, "by": true,
		"with": true, "from": true, "as": true, "at": true, "should": true, "will": true,
	}
	var out []string
	seen := map[string]bool{}
	for _, w := range strings.Fields(strings.ToLower(text)) {
		w = strings.Trim(w, ".,!?:;()[]{}\"'`")
		if len(w) < 3 || stop[w] || seen[w] {
			continue
		}
		out = append(out, w)
		seen[w] = true
		if len(out) >= 8 {
			break
		}
	}
	return out
}
