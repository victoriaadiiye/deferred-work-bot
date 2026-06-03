package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
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
	// EpicKey/EpicSummary record the parent epic the bot matched this work to;
	// EpicKey becomes the new issue's parent at filing time. Both are populated
	// by the bot (not the model), hence omitempty.
	EpicKey     string `json:"epic_key,omitempty"`
	EpicSummary string `json:"epic_summary,omitempty"`
}

// classifyEpic asks the model which open epic the work item best belongs to.
// It returns "" when no epic is a clear fit, and validates the answer against
// the candidate keys so a hallucinated key can never reach Jira.
func classifyEpic(ctx context.Context, c claudeAPI, workText string, epics []JiraIssue) (string, error) {
	if len(epics) == 0 {
		return "", nil
	}
	list := make([]map[string]any, len(epics))
	for i, ep := range epics {
		list[i] = map[string]any{"key": ep.Key, "summary": ep.Fields.Summary}
	}
	payload, _ := json.Marshal(list)
	prompt := fmt.Sprintf(`You are assigning a deferred-work item to the most appropriate epic.

WORK ITEM:
%s

EPICS:
%s

Return JSON: {"epic": "<the epic key that best fits, or an empty string if none is a clear fit>"}
Choose an epic only when the work item clearly belongs to its theme. When in doubt, return "".
Only return the JSON, no other text.`, workText, string(payload))
	out, err := c.Run(ctx, prompt)
	if err != nil {
		return "", err
	}
	js, err := ExtractJSON(out)
	if err != nil {
		return "", nil // fail soft — no epic
	}
	var parsed struct {
		Epic string `json:"epic"`
	}
	if err := json.Unmarshal([]byte(js), &parsed); err != nil {
		return "", nil
	}
	for _, ep := range epics {
		if ep.Key == parsed.Epic {
			return ep.Key, nil
		}
	}
	return "", nil
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

// SynthesizeInput is the full context bundle handed to the file-time agent.
type SynthesizeInput struct {
	Text           string
	Thread         []string
	Subproject     string
	Permalink      string
	Related        []RelatedTicket
	EpicLocked     bool        // true when EpicKey is fixed by override/PR chain
	EpicKey        string      // pre-resolved epic (authoritative when EpicLocked)
	EpicSummary    string      // summary of the pre-resolved epic, if known
	EpicCandidates []JiraIssue // open epics to choose from when not locked
}

// Synthesized is the agent's structured response: a concise ticket plus its
// chosen epic. The epic is validated against the candidate keys by the caller
// so a hallucinated key can never reach Jira.
type Synthesized struct {
	Summary     string `json:"summary"`
	Description string `json:"description"`
	Epic        string `json:"epic"`
}

// SynthesizeTicket hands the whole context bundle to the agent in one call. The
// agent writes a short, synthesized description and either references the epic
// that is already locked or picks the best-fitting one from the candidates.
func SynthesizeTicket(ctx context.Context, c claudeAPI, in SynthesizeInput) (*Synthesized, error) {
	var epicSection string
	switch {
	case in.EpicLocked && in.EpicKey != "":
		epicSection = fmt.Sprintf("The parent epic is already decided: %s%s. Reference it in the description and return this exact key in \"epic\"; do not choose a different one.", in.EpicKey, epicParen(in.EpicSummary))
	case in.EpicLocked:
		epicSection = `No parent epic applies to this work. Return "" for "epic".`
	case len(in.EpicCandidates) > 0:
		list := make([]map[string]any, len(in.EpicCandidates))
		for i, ep := range in.EpicCandidates {
			list[i] = map[string]any{"key": ep.Key, "summary": ep.Fields.Summary}
		}
		payload, _ := json.Marshal(list)
		epicSection = fmt.Sprintf("Pick the parent epic this work best belongs to from these candidates, or \"\" if none is a clear fit:\n%s", string(payload))
	default:
		epicSection = `No epic candidates are available. Return "" for "epic".`
	}

	relPayload := "none"
	if len(in.Related) > 0 {
		b, _ := json.Marshal(in.Related)
		relPayload = string(b)
	}

	prompt := fmt.Sprintf(`You are writing a Jira ticket from a Slack deferred-work item. Synthesize all of the context below into one concise, well-scoped ticket.

ORIGINAL MESSAGE:
%s

THREAD COMMENTS:
%s

SUB-PROJECT: %q
RELATED TICKETS: %s
SLACK PERMALINK: %s

EPIC:
%s

Return JSON only:
{
  "summary": "<one-line, imperative voice, <=120 chars>",
  "description": "<a short synthesized description — a few tight paragraphs that capture the problem and the intended work, NOT a transcript of the thread. If an epic is chosen, mention it. Finish with a line linking the Slack permalink.>",
  "epic": "<the chosen epic key, or an empty string>"
}
Only return the JSON, no other text.`,
		in.Text,
		strings.Join(in.Thread, "\n---\n"),
		in.Subproject,
		relPayload,
		in.Permalink,
		epicSection,
	)
	out, err := c.Run(ctx, prompt)
	if err != nil {
		return nil, err
	}
	js, err := ExtractJSON(out)
	if err != nil {
		return nil, err
	}
	var s Synthesized
	if err := json.Unmarshal([]byte(js), &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// validateEpicChoice returns the candidate matching key (with its summary), or
// ("", "") when key is empty or not among the candidates.
func validateEpicChoice(key string, candidates []JiraIssue) (string, string) {
	if key == "" {
		return "", ""
	}
	for _, ep := range candidates {
		if ep.Key == key {
			return ep.Key, ep.Fields.Summary
		}
	}
	return "", ""
}

func epicParen(summary string) string {
	if summary == "" {
		return ""
	}
	return " (" + summary + ")"
}

// buildContextMarkdown renders the full context bundle handed to the agent as a
// markdown document, attached to the ticket so the synthesized description is
// always backed by the complete, verbatim source material.
func buildContextMarkdown(it *Item, thread []string, rels []RelatedTicket, epicKey, epicSummary, permalink string) string {
	var b strings.Builder
	b.WriteString("# Deferred-work context\n\n")
	fmt.Fprintf(&b, "- Slack channel: %s\n", it.SlackChannel)
	fmt.Fprintf(&b, "- Slack thread ts: %s\n", it.SlackTS)
	if it.Subproject != "" {
		fmt.Fprintf(&b, "- Sub-project: %s\n", it.Subproject)
	}
	if permalink != "" {
		fmt.Fprintf(&b, "- Permalink: %s\n", permalink)
	}
	if epicKey != "" {
		if epicSummary != "" {
			fmt.Fprintf(&b, "- Epic: %s — %s\n", epicKey, epicSummary)
		} else {
			fmt.Fprintf(&b, "- Epic: %s\n", epicKey)
		}
	}
	b.WriteString("\n## Original message\n\n")
	b.WriteString(it.Text)
	b.WriteString("\n\n## Thread discussion\n\n")
	if len(thread) == 0 {
		b.WriteString("_(no replies)_\n")
	} else {
		for _, c := range thread {
			fmt.Fprintf(&b, "- %s\n", strings.ReplaceAll(c, "\n", " "))
		}
	}
	if len(rels) > 0 {
		b.WriteString("\n## Related tickets\n\n")
		for _, r := range rels {
			fmt.Fprintf(&b, "- %s — %s", r.Key, r.Verdict)
			if r.Summary != "" {
				fmt.Fprintf(&b, " — %s", r.Summary)
			}
			b.WriteString("\n")
		}
	}
	return b.String()
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

// RenderIntakeMessage builds the status comment posted right after an item is
// created: it acknowledges the proposal, reports whether the work looks new or
// overlaps an existing ticket, surfaces related tickets, and shows the vote
// count against the approval threshold.
func RenderIntakeMessage(rels []RelatedTicket, branch, existing, epicKey, epicSummary string, votes, threshold int) string {
	var b strings.Builder
	b.WriteString(":wave: *Picked up this proposal* — I'll file it to Jira once it clears the approval gate.\n\n")
	if branch == "awaiting_resolution" && existing != "" {
		fmt.Fprintf(&b, "This may already be covered by <%s|%s>. If it's approved I'll ask whether to comment on that ticket, file a fresh one, or both.\n", existing, existing)
	} else {
		b.WriteString("Looks *new* — I didn't find an existing ticket that already covers this.\n")
	}
	if epicKey != "" {
		if epicSummary != "" {
			fmt.Fprintf(&b, "*Epic:* %s — %s (reply `@bot epic: <KEY>` to change, `@bot epic: none` to skip).\n", epicKey, epicSummary)
		} else {
			fmt.Fprintf(&b, "*Epic:* %s (reply `@bot epic: <KEY>` to change, `@bot epic: none` to skip).\n", epicKey)
		}
	} else {
		b.WriteString("*Epic:* none found — reply `@bot epic: <KEY>` to set one.\n")
	}
	var related []RelatedTicket
	for _, r := range rels {
		if r.Verdict == "referenced" {
			related = append(related, r)
		}
	}
	if len(related) > 0 {
		b.WriteString("\n*Possibly related:*\n")
		for _, r := range related {
			fmt.Fprintf(&b, "• <%s|%s>\n", r.Key, r.Key)
		}
	}
	fmt.Fprintf(&b, "\n*%d/%d approvals.* React :white_check_mark: to approve or :x: to cancel.", votes, threshold)
	return b.String()
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
		if d.EpicKey != "" {
			if d.EpicSummary != "" {
				fmt.Fprintf(&b, "*Epic:* %s — %s\n", d.EpicKey, d.EpicSummary)
			} else {
				fmt.Fprintf(&b, "*Epic:* %s\n", d.EpicKey)
			}
		}
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
	SearchEpics(projects []string, limit int) ([]JiraIssue, error)
	GetIssue(key string) (*JiraIssueDetail, error)
	CreateIssue(in CreateIssueInput) (*CreatedIssue, error)
	AddComment(key, text string) error
	AddLabel(key, label string) error
	AddAttachment(key, filename string, content []byte) error
	FindAccountID(email string) (string, error)
}

type FileInput struct {
	Branch            string // new|comment_on_existing|both
	ProjectKey        string
	ExistingTicketKey string
	CommentText       string // synthesized context for the existing-ticket branch
	Draft             *Draft
	AssigneeAccountID string // Jira account ID of the proposal author, if resolved
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
			ProjectKey:        in.ProjectKey,
			Summary:           in.Draft.Summary,
			Description:       in.Draft.Description,
			IssueType:         in.Draft.IssueType,
			Labels:            in.Draft.Labels,
			Priority:          in.Draft.Priority,
			AssigneeAccountID: in.AssigneeAccountID,
			ParentEpicKey:     in.Draft.EpicKey,
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

type githubAPI interface {
	FetchPR(ref PRRef) (*GitHubPR, error)
}

type JobExecutor struct {
	Store     *Store
	Slack     SlackAPI
	Claude    claudeAPI
	Jira      jiraAPI
	GitHub    githubAPI
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
	case IntakeJob:
		return e.executeIntake(ctx, v.ItemID)
	}
	return fmt.Errorf("unknown job: %s", j.kind())
}

// proposalContext holds the result of the "is this new?" analysis: the thread,
// the detected sub-project, the related-ticket classifications, and the chosen
// branch. It is shared by the intake comment and the full propose flow.
type proposalContext struct {
	Thread      []string
	Sub         string
	Rels        []RelatedTicket
	Branch      string
	Existing    string
	EpicKey     string
	EpicSummary string
}

// detectEpic resolves the parent epic for an item. Priority:
//  1. Human override (`@bot epic: KEY` / `@bot epic: none`)
//  2. PR → ticket → epic chain (if text mentions a GitHub PR)
//  3. Claude classification against open epics
func (e *JobExecutor) detectEpic(ctx context.Context, it *Item) (key, summary string) {
	if k, s, locked := e.resolvedEpicHard(it); locked {
		return k, s
	}

	epics, err := e.Jira.SearchEpics(e.Projects.QORKProjects, 50)
	if err != nil || len(epics) == 0 {
		return "", ""
	}
	k, _ := classifyEpic(ctx, e.Claude, it.Text, epics)
	if k == "" {
		return "", ""
	}
	for _, ep := range epics {
		if ep.Key == k {
			return k, ep.Fields.Summary
		}
	}
	return k, ""
}

// resolvedEpicHard returns the epic fixed by a deterministic source — a human
// `@bot epic:` override or the PR→ticket→epic chain. locked is true whenever one
// of those applies, including the explicit "none" override (which locks to no
// epic). When locked, callers must not let the model pick a different epic.
func (e *JobExecutor) resolvedEpicHard(it *Item) (key, summary string, locked bool) {
	switch ov, _ := e.Store.LatestOverride(it.ID, "epic_override"); ov {
	case "":
		// fall through to the PR chain
	case "none":
		return "", "", true
	default:
		return ov, "", true
	}
	if k, s := e.epicFromPR(it.Text); k != "" {
		return k, s, true
	}
	return "", "", false
}

// loadThread returns the non-bot, non-original-message replies in an item's
// Slack thread.
func (e *JobExecutor) loadThread(it *Item) []string {
	msgs, _, _, _ := e.Slack.GetConversationReplies(&slack.GetConversationRepliesParameters{ChannelID: it.SlackChannel, Timestamp: it.SlackTS})
	var thread []string
	for _, m := range msgs {
		if m.User == e.BotUserID || m.Timestamp == it.SlackTS {
			continue
		}
		thread = append(thread, m.Text)
	}
	return thread
}

// epicFromPR extracts GitHub PR URLs from text, fetches each PR, finds Jira
// keys mentioned in the PR title/body/branch, then looks up each ticket's
// parent. Returns the first epic found.
func (e *JobExecutor) epicFromPR(text string) (key, summary string) {
	if e.GitHub == nil {
		return "", ""
	}
	refs := ParsePRRefs(text)
	for _, ref := range refs {
		pr, err := e.GitHub.FetchPR(ref)
		if err != nil {
			continue
		}
		jiraKeys := ExtractJiraKeys(pr.Title, pr.Body, pr.Head.Ref)
		for _, jk := range jiraKeys {
			issue, err := e.Jira.GetIssue(jk)
			if err != nil {
				continue
			}
			if issue.Fields.Parent != nil && issue.Fields.Parent.Fields.IssueType.Name == "Epic" {
				return issue.Fields.Parent.Key, issue.Fields.Parent.Fields.Summary
			}
		}
	}
	return "", ""
}

// gatherContext loads the thread, detects the sub-project, searches Jira, and
// classifies related tickets. It persists a freshly-detected sub-project so
// later runs reuse it.
func (e *JobExecutor) gatherContext(ctx context.Context, it *Item) proposalContext {
	thread := e.loadThread(it)

	sub := it.Subproject
	if sub == "" {
		v, _ := detectSubproject(ctx, e.Projects, e.Claude, it.Text+"\n"+strings.Join(thread, "\n"))
		sub = v
		if sub != "" {
			e.Store.UpdateItemSubproject(it.ID, sub)
		}
	}

	issues, _ := e.Jira.Search(JiraSearchInput{
		Projects:   e.Projects.QORKProjects,
		Subproject: sub,
		Keywords:   extractKeywords(it.Text),
		Limit:      20,
	})

	// Include tickets the bot previously created so we catch our own duplicates
	// even if the keyword search misses them.
	botKeys, _ := e.Store.ListRecentTicketKeys(time.Now().Add(-90 * 24 * time.Hour))
	issues = mergeIssues(issues, e.lookupMissing(issues, botKeys))

	rels, _ := classifyRelatedTickets(ctx, e.Claude, it.Text, issues)
	branch, existing := DecideBranch(rels)
	epicKey, epicSummary := e.detectEpic(ctx, it)
	return proposalContext{
		Thread: thread, Sub: sub, Rels: rels, Branch: branch, Existing: existing,
		EpicKey: epicKey, EpicSummary: epicSummary,
	}
}

// executeIntake posts the initial status comment as soon as an item is created:
// it acknowledges the proposal, runs the "is this new?" check, surfaces any
// related-ticket context, and reports the current vote count.
func (e *JobExecutor) executeIntake(ctx context.Context, itemID int64) error {
	it, err := e.Store.GetItemByID(itemID)
	if err != nil {
		return err
	}
	if isTerminal(it.Status) {
		return nil
	}
	pc := e.gatherContext(ctx, it)
	n, _ := e.Store.CountVotes(it.ID)
	body := RenderIntakeMessage(pc.Rels, pc.Branch, pc.Existing, pc.EpicKey, pc.EpicSummary, n, it.ApprovalThreshold)
	e.Slack.PostMessage(it.SlackChannel,
		slack.MsgOptionText(body, false),
		slack.MsgOptionTS(it.SlackTS))
	e.Store.LogEvent(&it.ID, "intake", `{}`)
	return nil
}

// resolveAssignee maps a Slack user to a Jira account ID via their email
// address, returning "" when it cannot be resolved (unknown user, missing
// email scope, or no matching Jira account).
func (e *JobExecutor) resolveAssignee(slackUserID string) string {
	if slackUserID == "" {
		return ""
	}
	u, err := e.Slack.GetUserInfo(slackUserID)
	if err != nil || u == nil || u.Profile.Email == "" {
		return ""
	}
	id, _ := e.Jira.FindAccountID(u.Profile.Email)
	return id
}

func (e *JobExecutor) executePropose(ctx context.Context, itemID int64) error {
	it, err := e.Store.GetItemByID(itemID)
	if err != nil {
		return err
	}
	if it.Status == "cancelled" || it.Status == "archived" {
		return nil
	}

	// 1-4. Load thread, detect sub-project, search + classify related tickets.
	pc := e.gatherContext(ctx, it)
	thread, sub, rels, branch, existing := pc.Thread, pc.Sub, pc.Rels, pc.Branch, pc.Existing

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
		d.EpicKey = pc.EpicKey
		d.EpicSummary = pc.EpicSummary
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

	var rels []RelatedTicket
	json.Unmarshal([]byte(p.RelatedTicketsJSON), &rels)

	// For a fresh ticket, hand the whole context bundle to the agent: it writes
	// the synthesized summary/description and picks the parent epic (unless one is
	// already locked by a human override or the PR→epic chain). The same bundle is
	// rendered to markdown and attached to the issue for full traceability.
	var contextMD string
	if p.Branch == "new" || p.Branch == "both" {
		thread := e.loadThread(it)
		permalink, _ := e.Slack.GetPermalink(&slack.PermalinkParameters{Channel: it.SlackChannel, Ts: it.SlackTS})
		priority, _ := e.Store.LatestOverride(it.ID, "priority_override")
		if priority == "" {
			priority = draft.Priority
		}
		if priority == "" {
			priority = "Medium"
		}

		epicKey, epicSummary, locked := e.resolvedEpicHard(it)
		var candidates []JiraIssue
		if !locked {
			candidates, _ = e.Jira.SearchEpics(e.Projects.QORKProjects, 50)
		}

		syn, err := SynthesizeTicket(ctx, e.Claude, SynthesizeInput{
			Text:           it.Text,
			Thread:         thread,
			Subproject:     it.Subproject,
			Permalink:      permalink,
			Related:        rels,
			EpicLocked:     locked,
			EpicKey:        epicKey,
			EpicSummary:    epicSummary,
			EpicCandidates: candidates,
		})
		if err != nil {
			e.Slack.PostMessage(it.SlackChannel,
				slack.MsgOptionText(":warning: Failed to draft ticket: "+err.Error(), false),
				slack.MsgOptionTS(it.SlackTS))
			return err
		}
		if !locked {
			epicKey, epicSummary = validateEpicChoice(syn.Epic, candidates)
		}

		draft.Summary = syn.Summary
		draft.Description = syn.Description
		draft.IssueType = "Task"
		draft.Priority = priority
		draft.Labels = ensureLabels(draft.Labels, "deferred-work", it.Subproject)
		draft.EpicKey = epicKey
		draft.EpicSummary = epicSummary

		contextMD = buildContextMarkdown(it, thread, rels, epicKey, epicSummary, permalink)
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
		AssigneeAccountID: e.resolveAssignee(it.AuthorSlackID),
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

	// Attach the full context as markdown. The ticket already exists, so a failed
	// upload is logged but never fails the filing.
	if res.NewKey != "" && contextMD != "" {
		filename := res.NewKey + "-context.md"
		if aerr := e.Jira.AddAttachment(res.NewKey, filename, []byte(contextMD)); aerr != nil {
			e.Store.LogEvent(&it.ID, "attachment_failed", `{"key":"`+res.NewKey+`","error":`+strconv.Quote(aerr.Error())+`}`)
		}
	}

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

// lookupMissing fetches Jira issues for keys not already present in the
// existing set. Used to pull in bot-created tickets for dedup.
func (e *JobExecutor) lookupMissing(existing []JiraIssue, keys []string) []JiraIssue {
	have := map[string]bool{}
	for _, iss := range existing {
		have[iss.Key] = true
	}
	var out []JiraIssue
	for _, k := range keys {
		if have[k] {
			continue
		}
		detail, err := e.Jira.GetIssue(k)
		if err != nil {
			continue
		}
		var iss JiraIssue
		iss.Key = detail.Key
		iss.Fields.Summary = detail.Fields.Summary
		out = append(out, iss)
	}
	return out
}

func mergeIssues(a, b []JiraIssue) []JiraIssue {
	out := make([]JiraIssue, len(a))
	copy(out, a)
	return append(out, b...)
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
