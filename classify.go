package main

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// minProposalWords is the default floor below which a watched-channel message is
// treated as chatter and never reaches the Claude proposal judge.
const minProposalWords = 4

var (
	// mentionRe matches a Slack user/channel mention like <@U123> or <#C123|name>.
	mentionRe = regexp.MustCompile(`<[@#][^>]+>`)
	// urlOnlyRe matches a message that is nothing but a single link.
	urlOnlyRe = regexp.MustCompile(`^<?https?://\S+>?$`)
)

// looksLikeProposal is the cheap, synchronous prefilter that runs on the Slack
// event path. It rejects only obvious non-proposals — empty text, a bare link, a
// lone mention/emoji, or anything shorter than minWords — so the expensive
// Claude judge is reserved for plausible candidates. Borderline messages pass;
// Claude makes the real call inside ClassifyJob.
func looksLikeProposal(text string, minWords int) bool {
	if minWords <= 0 {
		minWords = minProposalWords
	}
	t := strings.TrimSpace(text)
	if t == "" {
		return false
	}
	// A message that is only mentions ("<@U1> <@U2>") carries no proposal.
	stripped := strings.TrimSpace(mentionRe.ReplaceAllString(t, " "))
	if stripped == "" {
		return false
	}
	if urlOnlyRe.MatchString(stripped) {
		return false
	}
	return len(strings.Fields(stripped)) >= minWords
}

// classifyIsProposal asks the model whether a message proposes trackable work.
// Errors are surfaced so the caller can decide how to degrade (we fail open).
func classifyIsProposal(ctx context.Context, c claudeAPI, text string) (bool, error) {
	prompt := fmt.Sprintf(`You filter Slack messages for a bot that turns deferred work into Jira tickets.
Decide whether this message proposes a concrete piece of work worth tracking for later — a task, follow-up, bug, tech-debt item, cleanup, or improvement that would warrant a ticket.

Answer false for greetings, questions, status updates, opinions, acknowledgements, links shared without an ask, or general chit-chat.

MESSAGE:
%q

Return JSON: {"is_proposal": true|false}
Only return the JSON, no other text.`, text)
	out, err := c.Run(ctx, prompt)
	if err != nil {
		return false, err
	}
	js, err := ExtractJSON(out)
	if err != nil {
		return false, err
	}
	var parsed struct {
		IsProposal bool `json:"is_proposal"`
	}
	if err := json.Unmarshal([]byte(js), &parsed); err != nil {
		return false, err
	}
	return parsed.IsProposal, nil
}
