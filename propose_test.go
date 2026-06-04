package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

type fakeClaude struct {
	resp string
	err  error
	got  []string
}

func (f *fakeClaude) Run(ctx context.Context, prompt string) (string, error) {
	f.got = append(f.got, prompt)
	return f.resp, f.err
}

func TestDetectSubproject_Keyword(t *testing.T) {
	cfg := &ProjectsConfig{Subprojects: []string{"qompass", "qatalyst"}}
	got := detectSubprojectByKeyword(cfg, "we should defer this qompass thing")
	if got != "qompass" {
		t.Fatalf("got %q", got)
	}
}

func TestDetectSubproject_KeywordCaseInsensitive(t *testing.T) {
	cfg := &ProjectsConfig{Subprojects: []string{"qompass", "qatalyst"}}
	got := detectSubprojectByKeyword(cfg, "Qatalyst rolls up")
	if got != "qatalyst" {
		t.Fatalf("got %q", got)
	}
}

func TestDetectSubproject_NoneFound(t *testing.T) {
	cfg := &ProjectsConfig{Subprojects: []string{"qompass", "qatalyst"}}
	if detectSubprojectByKeyword(cfg, "no project here") != "" {
		t.Fatal("expected empty")
	}
}

func TestDetectSubproject_FallbackToClaude(t *testing.T) {
	cfg := &ProjectsConfig{Subprojects: []string{"qompass", "qatalyst"}}
	fc := &fakeClaude{resp: `{"subproject":"qatalyst"}`}
	got, err := detectSubproject(context.Background(), cfg, fc, "vague text without keyword")
	if err != nil {
		t.Fatal(err)
	}
	if got != "qatalyst" {
		t.Fatalf("got %q", got)
	}
	if len(fc.got) != 1 || !strings.Contains(fc.got[0], "vague text") {
		t.Fatalf("claude not called with text: %+v", fc.got)
	}
}

func TestDetectSubproject_ClaudeReturnsNone(t *testing.T) {
	cfg := &ProjectsConfig{Subprojects: []string{"qompass"}}
	fc := &fakeClaude{resp: `{"subproject":""}`}
	got, _ := detectSubproject(context.Background(), cfg, fc, "vague")
	if got != "" {
		t.Fatalf("expected empty fallback, got %q", got)
	}
}

func TestClassifyRelatedTickets(t *testing.T) {
	issues := []JiraIssue{
		{Key: "QORK-1"}, {Key: "QORK-2"}, {Key: "QORK-3"},
	}
	fc := &fakeClaude{resp: `[
		{"key":"QORK-1","verdict":"encompassed"},
		{"key":"QORK-2","verdict":"referenced"},
		{"key":"QORK-3","verdict":"unrelated"}
	]`}
	res, err := classifyRelatedTickets(context.Background(), fc, "work text", issues)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 3 {
		t.Fatalf("len: %d", len(res))
	}
	if res[0].Verdict != "encompassed" || res[2].Verdict != "unrelated" {
		t.Fatalf("verdicts: %+v", res)
	}
}

func TestDraftTicket(t *testing.T) {
	fc := &fakeClaude{resp: `{
		"summary": "Fix flaky test in qompass ingest",
		"description": "Long form description...",
		"labels": ["deferred-work", "qompass"],
		"priority": "Medium"
	}`}
	d, err := DraftTicket(context.Background(), fc, DraftInput{
		Text:         "test is flaky in qompass",
		Thread:       []string{"+1 from me"},
		Subproject:   "qompass",
		PriorityOver: "",
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Summary != "Fix flaky test in qompass ingest" {
		t.Fatalf("summary: %s", d.Summary)
	}
	if d.IssueType != "Task" {
		t.Fatalf("type: %s", d.IssueType)
	}
	if len(d.Labels) != 2 || d.Labels[0] != "deferred-work" {
		t.Fatalf("labels: %+v", d.Labels)
	}
}

func TestDraftTicket_PriorityOverride(t *testing.T) {
	fc := &fakeClaude{resp: `{"summary":"s","description":"d","labels":["deferred-work"],"priority":"Low"}`}
	d, _ := DraftTicket(context.Background(), fc, DraftInput{Text: "x", PriorityOver: "High"})
	if d.Priority != "High" {
		t.Fatalf("expected override to High, got %s", d.Priority)
	}
}

func TestSynthesizeTicket_PicksFromCandidates(t *testing.T) {
	fc := &fakeClaude{resp: `{"summary":"Fix ingest retry","description":"short synthesized desc","epic":"QORK-7"}`}
	cands := []JiraIssue{{Key: "QORK-7"}}
	cands[0].Fields.Summary = "Ingest reliability"
	syn, err := SynthesizeTicket(context.Background(), fc, SynthesizeInput{
		Text:           "ingest retries are flaky",
		Thread:         []string{"agreed"},
		Related:        []RelatedTicket{{Key: "QORK-3", Verdict: "referenced"}},
		EpicCandidates: cands,
	})
	if err != nil {
		t.Fatal(err)
	}
	if syn.Summary != "Fix ingest retry" || syn.Epic != "QORK-7" {
		t.Fatalf("parsed: %+v", syn)
	}
	// The prompt must offer the candidate epics and the related tickets.
	if len(fc.got) != 1 || !strings.Contains(fc.got[0], "QORK-7") || !strings.Contains(fc.got[0], "QORK-3") {
		t.Fatalf("prompt missing context: %v", fc.got)
	}
}

func TestSynthesizeTicket_LockedEpic(t *testing.T) {
	fc := &fakeClaude{resp: `{"summary":"s","description":"d","epic":"QORK-9"}`}
	_, err := SynthesizeTicket(context.Background(), fc, SynthesizeInput{
		Text:       "work",
		EpicLocked: true,
		EpicKey:    "QORK-9",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fc.got[0], "already decided: QORK-9") {
		t.Fatalf("prompt should pin the locked epic: %s", fc.got[0])
	}
}

func TestValidateEpicChoice(t *testing.T) {
	cands := []JiraIssue{{Key: "QORK-7"}, {Key: "QORK-8"}}
	cands[0].Fields.Summary = "Reliability"
	if k, s := validateEpicChoice("QORK-7", cands); k != "QORK-7" || s != "Reliability" {
		t.Fatalf("valid choice: %s %s", k, s)
	}
	if k, _ := validateEpicChoice("QORK-999", cands); k != "" {
		t.Fatalf("hallucinated key should be rejected, got %s", k)
	}
	if k, _ := validateEpicChoice("", cands); k != "" {
		t.Fatalf("empty key should stay empty, got %s", k)
	}
}

func TestBuildContextMarkdown(t *testing.T) {
	it := &Item{SlackChannel: "C1", SlackTS: "1700.1", Subproject: "qompass", Text: "the original message"}
	md := buildContextMarkdown(it, []string{"reply one", "reply two"},
		[]RelatedTicket{{Key: "QORK-3", Verdict: "referenced", Summary: "near thing"}},
		"QORK-7", "Reliability", "https://slack/x")
	for _, want := range []string{"the original message", "reply one", "reply two", "QORK-3", "QORK-7", "Reliability", "https://slack/x", "qompass"} {
		if !strings.Contains(md, want) {
			t.Fatalf("markdown missing %q:\n%s", want, md)
		}
	}
}

func TestRenderProposalMessage_NewBranch(t *testing.T) {
	d := &Draft{
		Summary:     "Fix flaky test",
		Description: "long...",
		IssueType:   "Task",
		Labels:      []string{"deferred-work", "qompass"},
		Priority:    "Medium",
	}
	out := RenderProposalMessage(d, []RelatedTicket{}, "new", "", false, "https://qumulo.atlassian.net")
	if !strings.Contains(out, "Fix flaky test") || !strings.Contains(out, "Task") || !strings.Contains(out, "Medium") {
		t.Fatalf("missing fields:\n%s", out)
	}
	if !strings.Contains(out, "approve signal to file") {
		t.Fatalf("missing footer:\n%s", out)
	}
}

func TestRenderProposalMessage_EncompassedBranch(t *testing.T) {
	out := RenderProposalMessage(nil, []RelatedTicket{{Key: "QORK-5", Verdict: "encompassed"}}, "awaiting_resolution", "QORK-5", false, "https://qumulo.atlassian.net")
	if !strings.Contains(out, "QORK-5") || !strings.Contains(out, "encompassed") {
		t.Fatalf("missing encompassed banner: %s", out)
	}
	if !strings.Contains(out, "comment") || !strings.Contains(out, "new") || !strings.Contains(out, "both") {
		t.Fatalf("missing resolution options: %s", out)
	}
}

func TestRenderProposalMessage_TTLBanner(t *testing.T) {
	d := &Draft{Summary: "x", IssueType: "Task", Priority: "Low"}
	out := RenderProposalMessage(d, nil, "new", "", true, "https://qumulo.atlassian.net")
	if !strings.Contains(out, "no response") {
		t.Fatalf("missing TTL banner: %s", out)
	}
}

type fakeJira struct {
	createdKey  string
	createdURL  string
	comments    []struct{ Key, Text string }
	labels      []struct{ Key, Label string }
	failSearch  bool
	accountID   string
	epics       []JiraIssue
	issues      map[string]*JiraIssueDetail
	lastCreate  CreateIssueInput
	attachments []struct{ Key, Filename, Content string }
}

func (f *fakeJira) Search(in JiraSearchInput) ([]JiraIssue, error) {
	if f.failSearch {
		return nil, fmt.Errorf("jira search error")
	}
	return nil, nil
}
func (f *fakeJira) SearchEpics(projects []string, limit int) ([]JiraIssue, error) {
	return f.epics, nil
}
func (f *fakeJira) GetIssue(key string) (*JiraIssueDetail, error) {
	if f.issues != nil {
		if d, ok := f.issues[key]; ok {
			return d, nil
		}
	}
	return nil, fmt.Errorf("issue %s not found", key)
}
func (f *fakeJira) CreateIssue(in CreateIssueInput) (*CreatedIssue, error) {
	f.lastCreate = in
	f.createdKey = "QORK-100"
	f.createdURL = "https://x/browse/QORK-100"
	return &CreatedIssue{Key: f.createdKey, URL: f.createdURL}, nil
}
func (f *fakeJira) AddComment(key, text string) error {
	f.comments = append(f.comments, struct{ Key, Text string }{key, text})
	return nil
}
func (f *fakeJira) AddLabel(key, label string) error {
	f.labels = append(f.labels, struct{ Key, Label string }{key, label})
	return nil
}
func (f *fakeJira) AddAttachment(key, filename string, content []byte) error {
	f.attachments = append(f.attachments, struct{ Key, Filename, Content string }{key, filename, string(content)})
	return nil
}
func (f *fakeJira) FindAccountID(email string) (string, error) {
	return f.accountID, nil
}

func TestFileProposal_NewBranchCreatesIssue(t *testing.T) {
	j := &fakeJira{}
	d := &Draft{Summary: "x", Description: "y", IssueType: "Task", Labels: []string{"deferred-work"}, Priority: "Medium"}
	res, err := FileProposal(j, FileInput{
		Branch:     "new",
		ProjectKey: "QORK",
		Draft:      d,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.NewKey != "QORK-100" {
		t.Fatalf("key: %s", res.NewKey)
	}
}

func TestFileProposal_CommentBranchAddsCommentAndLabel(t *testing.T) {
	j := &fakeJira{}
	res, err := FileProposal(j, FileInput{
		Branch:            "comment_on_existing",
		ExistingTicketKey: "QORK-5",
		CommentText:       "deferred-work follow-up: ...",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.CommentedOn != "QORK-5" {
		t.Fatalf("commented: %s", res.CommentedOn)
	}
	if len(j.comments) != 1 || j.comments[0].Key != "QORK-5" {
		t.Fatalf("comment not made: %+v", j.comments)
	}
	if len(j.labels) != 1 || j.labels[0].Label != "deferred-work-followup" {
		t.Fatalf("label not added: %+v", j.labels)
	}
}

func TestFileProposal_BothBranchDoesBoth(t *testing.T) {
	j := &fakeJira{}
	d := &Draft{Summary: "x", Description: "y", IssueType: "Task", Labels: []string{"deferred-work"}, Priority: "Medium"}
	res, err := FileProposal(j, FileInput{
		Branch:            "both",
		ProjectKey:        "QORK",
		ExistingTicketKey: "QORK-5",
		CommentText:       "context",
		Draft:             d,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.NewKey == "" || res.CommentedOn == "" {
		t.Fatalf("expected both, got %+v", res)
	}
}

func TestDecideBranch(t *testing.T) {
	cases := []struct {
		name     string
		verdicts []string
		want     string
		existing string
	}{
		{"all unrelated", []string{"unrelated", "unrelated"}, "new", ""},
		{"only referenced", []string{"referenced", "unrelated"}, "new", ""},
		{"encompassed wins", []string{"encompassed", "referenced"}, "awaiting_resolution", "QORK-1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rels := make([]RelatedTicket, len(tc.verdicts))
			for i, v := range tc.verdicts {
				rels[i] = RelatedTicket{Key: fmt.Sprintf("QORK-%d", i+1), Verdict: v}
			}
			b, k := DecideBranch(rels)
			if b != tc.want {
				t.Fatalf("branch: got %s want %s", b, tc.want)
			}
			if b == "awaiting_resolution" && k != tc.existing {
				t.Fatalf("existing: got %s want %s", k, tc.existing)
			}
		})
	}
}
