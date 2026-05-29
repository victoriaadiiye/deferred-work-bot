package main

import (
	"context"
	"encoding/json"
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
