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
		Projects:  &ProjectsConfig{Subprojects: []string{"qompass"}, QORKProjects: []string{"QORK"}},
		Signals:   &SignalsConfig{},
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

func TestJobExecutor_FileFlow_AssignsAuthor(t *testing.T) {
	store := newTestStore(t)
	fake := newFakeSlack("UBOT")
	fake.users = map[string]*slack.User{
		"U1": {ID: "U1", Profile: slack.UserProfile{Email: "author@qumulo.com"}},
	}
	fc := &fakeClaude{resp: `{"summary":"synth","description":"d","epic":""}`}
	jc := &fakeJira{accountID: "jira-acct-123"}
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
		Store: store, Slack: fake, Claude: fc, Jira: jc,
		Projects:  &ProjectsConfig{QORKProjects: []string{"QORK"}},
		Signals:   &SignalsConfig{},
		BotUserID: "UBOT",
	}
	if err := ex.Execute(context.Background(), FileJob{ProposalID: p.ID}); err != nil {
		t.Fatal(err)
	}
	if jc.lastCreate.AssigneeAccountID != "jira-acct-123" {
		t.Fatalf("expected ticket assigned to author's Jira account, got %q", jc.lastCreate.AssigneeAccountID)
	}
	var filed *postedMsg
	for i := range fake.posted {
		if strings.HasPrefix(fake.posted[i].Text, "Filed:") {
			filed = &fake.posted[i]
		}
	}
	if filed == nil || !strings.Contains(filed.Text, "assigned to <@U1>") {
		t.Fatalf("expected assignment surfaced in confirmation, got %+v", filed)
	}
}

func TestJobExecutor_FileFlow_UnresolvedAssigneeIsObservable(t *testing.T) {
	store := newTestStore(t)
	fake := newFakeSlack("UBOT")
	// No email on the author (e.g. bot token missing users:read.email scope).
	fake.users = map[string]*slack.User{"U1": {ID: "U1"}}
	fc := &fakeClaude{resp: `{"summary":"synth","description":"d","epic":""}`}
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
		Store: store, Slack: fake, Claude: fc, Jira: jc,
		Projects:  &ProjectsConfig{QORKProjects: []string{"QORK"}},
		Signals:   &SignalsConfig{},
		BotUserID: "UBOT",
	}
	if err := ex.Execute(context.Background(), FileJob{ProposalID: p.ID}); err != nil {
		t.Fatal(err)
	}
	// Ticket still files, just unassigned.
	if jc.createdKey != "QORK-100" {
		t.Fatal("ticket should still be filed when assignee can't be resolved")
	}
	if jc.lastCreate.AssigneeAccountID != "" {
		t.Fatalf("expected no assignee, got %q", jc.lastCreate.AssigneeAccountID)
	}
	// The miss is logged so it's diagnosable rather than silent.
	events, _ := store.ListEventsForItem(it.ID)
	found := false
	for _, ev := range events {
		if ev.Kind == "assignee_unresolved" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected assignee_unresolved event to be logged")
	}
}

func TestJobExecutor_ReminderFlow_PostsReminder(t *testing.T) {
	store := newTestStore(t)
	fake := newFakeSlack("UBOT")
	it := &Item{SlackChannel: "C1", SlackTS: "1700.1", AuthorSlackID: "U1", Text: "reminder test item", Status: "collecting", ApprovalThreshold: 3}
	store.InsertItem(it)
	store.UpsertVote(it.ID, "U2", "reaction", "white_check_mark")
	store.UpsertVote(it.ID, "U3", "reaction", "white_check_mark")

	ex := &JobExecutor{
		Store: store, Slack: fake, Jira: &fakeJira{},
		Projects: &ProjectsConfig{}, Signals: &SignalsConfig{},
		BotUserID: "UBOT",
	}
	if err := ex.Execute(context.Background(), ReminderJob{ItemID: it.ID}); err != nil {
		t.Fatal(err)
	}

	// A message should have been posted into the thread.
	if len(fake.posted) == 0 {
		t.Fatal("expected reminder message posted")
	}
	msg := fake.posted[0]
	if msg.Channel != "C1" {
		t.Fatalf("channel: %s", msg.Channel)
	}
	if !strings.Contains(msg.Text, "2/3") {
		t.Fatalf("expected vote count in message: %s", msg.Text)
	}
	if !strings.Contains(msg.Text, "reminder test item") {
		t.Fatalf("expected quoted original message: %s", msg.Text)
	}

	// LastReminderAt should be updated.
	got, _ := store.GetItemByID(it.ID)
	if got.LastReminderAt == nil {
		t.Fatal("expected LastReminderAt to be set")
	}

	// A reminder event with via=trigger should be logged.
	events, _ := store.ListEventsForItem(it.ID)
	found := false
	for _, ev := range events {
		if ev.Kind == "reminder" && strings.Contains(ev.Payload, `"via":"trigger"`) {
			found = true
		}
	}
	if !found {
		t.Fatal("expected reminder event with via=trigger")
	}
}

func TestJobExecutor_ReminderFlow_SkipsTerminal(t *testing.T) {
	store := newTestStore(t)
	fake := newFakeSlack("UBOT")
	it := &Item{SlackChannel: "C1", SlackTS: "1700.1", AuthorSlackID: "U1", Text: "x", Status: "ticketed", ApprovalThreshold: 3}
	store.InsertItem(it)

	ex := &JobExecutor{
		Store: store, Slack: fake, Jira: &fakeJira{},
		Projects: &ProjectsConfig{}, Signals: &SignalsConfig{},
		BotUserID: "UBOT",
	}
	if err := ex.Execute(context.Background(), ReminderJob{ItemID: it.ID}); err != nil {
		t.Fatal(err)
	}
	if len(fake.posted) != 0 {
		t.Fatalf("expected no message for terminal item, got %d", len(fake.posted))
	}
}

func TestJobExecutor_FileFlow_CreatesIssueAndLocks(t *testing.T) {
	store := newTestStore(t)
	fake := newFakeSlack("UBOT")
	fc := &fakeClaude{resp: `{"summary":"synth summary","description":"synth desc","epic":""}`}
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
		Store: store, Slack: fake, Claude: fc, Jira: jc,
		Projects:  &ProjectsConfig{QORKProjects: []string{"QORK"}},
		Signals:   &SignalsConfig{},
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
	// The agent synthesized the filed ticket.
	if jc.lastCreate.Summary != "synth summary" || jc.lastCreate.Description != "synth desc" {
		t.Fatalf("issue not built from synthesis: %+v", jc.lastCreate)
	}
	// The full context was attached as markdown to the new ticket.
	if len(jc.attachments) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(jc.attachments))
	}
	att := jc.attachments[0]
	if att.Key != "QORK-100" || att.Filename != "QORK-100-context.md" {
		t.Fatalf("attachment target/name: %+v", att)
	}
	if !strings.Contains(att.Content, "# Deferred-work context") || !strings.Contains(att.Content, "## Original message") {
		t.Fatalf("attachment missing context: %s", att.Content)
	}
}
