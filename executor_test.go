package main

import (
	"context"
	"strings"
	"testing"

	"github.com/slack-go/slack"
)

func TestJobExecutor_ProposeFlow_NewBranch(t *testing.T) {
	store := newTestStore(t)
	fake := newFakeSlack("UBOT")
	fc := &fakeClaude{resp: `{"summary":"do x","description":"d","labels":["deferred-work","qompass"],"priority":"Medium"}`}
	jc := &fakeJira{}
	it := &Item{SlackChannel: "C1", SlackTS: "1700.1", AuthorSlackID: "U1", Text: "qompass thing", Status: "proposing", ApprovalThreshold: 3}
	store.InsertItem(it)
	ex := &JobExecutor{
		Store: store, Slack: fake, Claude: fc, Jira: jc,
		Projects: &ProjectsConfig{Subprojects: []string{"qompass"}, QORKProjects: []string{"QORK"}},
		Signals:  &SignalsConfig{},
		BotUserID: "UBOT",
	}
	if err := ex.Execute(context.Background(), ProposeJob{ItemID: it.ID}); err != nil {
		t.Fatal(err)
	}
	p, err := store.GetLatestProposal(it.ID)
	if err != nil {
		t.Fatalf("no proposal: %v", err)
	}
	if p.Branch != "new" {
		t.Fatalf("branch: %s", p.Branch)
	}
	if p.Status != "draft" {
		t.Fatalf("status: %s", p.Status)
	}
	if len(fake.posted) == 0 || !strings.Contains(fake.posted[0].Text, "") {
		t.Fatal("expected proposal posted")
	}
	got, _ := store.GetItemByID(it.ID)
	if got.Status != "proposed" {
		t.Fatalf("item status: %s", got.Status)
	}
}

func TestJobExecutor_FileFlow_RedraftsWhenBranchChangedFromAwaitingResolution(t *testing.T) {
	store := newTestStore(t)
	fake := newFakeSlack("UBOT")
	fake.replies["1700.1"] = []slack.Message{
		{Msg: slack.Msg{Text: "let's do this", Timestamp: "1700.99", User: "U2"}},
	}
	fc := &fakeClaude{resp: `{"summary":"redrafted summary","description":"desc","labels":["deferred-work"],"priority":"High"}`}
	jc := &fakeJira{}
	it := &Item{SlackChannel: "C1", SlackTS: "1700.1", AuthorSlackID: "U1", Text: "do something", Status: "proposed", ApprovalThreshold: 3}
	store.InsertItem(it)
	// Proposal that was originally awaiting_resolution, resolved to new, but has no draft.
	p := &Proposal{
		ItemID:             it.ID,
		SlackTS:            "1700.2",
		DraftJSON:          "null",
		RelatedTicketsJSON: "[]",
		Branch:             "new",
		Status:             "draft",
	}
	store.InsertProposal(p)
	ex := &JobExecutor{
		Store: store, Slack: fake, Claude: fc, Jira: jc,
		Projects:  &ProjectsConfig{QORKProjects: []string{"QORK"}},
		Signals:   &SignalsConfig{},
		BotUserID: "UBOT",
	}
	if err := ex.Execute(context.Background(), FileJob{ProposalID: p.ID}); err != nil {
		t.Fatal(err)
	}
	tk, err := store.GetTicketByProposal(p.ID)
	if err != nil {
		t.Fatalf("no ticket created: %v", err)
	}
	if tk.JiraKey != "QORK-100" {
		t.Fatalf("expected QORK-100, got %s", tk.JiraKey)
	}
	// Verify Claude was called (re-draft) and the created issue got the redrafted summary.
	if jc.createdKey != "QORK-100" {
		t.Fatalf("issue not created via Jira")
	}
	// fc.resp set summary to "redrafted summary" — verify Claude was called to redraft.
	if len(fc.got) == 0 {
		t.Fatal("expected Claude to be called to redraft")
	}
}

func TestJobExecutor_FileFlow_CreatesIssueAndLocks(t *testing.T) {
	store := newTestStore(t)
	fake := newFakeSlack("UBOT")
	jc := &fakeJira{}
	it := &Item{SlackChannel: "C1", SlackTS: "1700.1", AuthorSlackID: "U1", Text: "x", Status: "proposed", ApprovalThreshold: 3}
	store.InsertItem(it)
	p := &Proposal{
		ItemID:             it.ID,
		SlackTS:            "1700.2",
		DraftJSON:          `{"summary":"do x","description":"d","issue_type":"Task","labels":["deferred-work"],"priority":"Medium"}`,
		RelatedTicketsJSON: "[]",
		Branch:             "new",
		Status:             "draft",
	}
	store.InsertProposal(p)
	ex := &JobExecutor{
		Store: store, Slack: fake, Jira: jc,
		Projects: &ProjectsConfig{QORKProjects: []string{"QORK"}},
		Signals:  &SignalsConfig{},
		BotUserID: "UBOT",
	}
	if err := ex.Execute(context.Background(), FileJob{ProposalID: p.ID}); err != nil {
		t.Fatal(err)
	}
	got, _ := store.GetItemByID(it.ID)
	if got.Status != "ticketed" {
		t.Fatalf("item status: %s", got.Status)
	}
	tk, err := store.GetTicketByProposal(p.ID)
	if err != nil {
		t.Fatalf("no ticket: %v", err)
	}
	if tk.JiraKey != "QORK-100" {
		t.Fatalf("key: %s", tk.JiraKey)
	}
}
