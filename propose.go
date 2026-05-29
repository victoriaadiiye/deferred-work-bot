package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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
