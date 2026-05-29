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
