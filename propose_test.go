package main

import (
	"context"
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
