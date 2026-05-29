package main

import (
	"context"
	"strings"
	"testing"
)

func TestClaudeRunner_RunUsesEcho(t *testing.T) {
	r := &ClaudeRunner{Bin: "/bin/echo"}
	out, err := r.Run(context.Background(), "hello")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "hello") {
		t.Fatalf("expected echo to include 'hello', got %q", out)
	}
}

func TestExtractJSON_Object(t *testing.T) {
	raw := "some text\n```json\n{\"k\":1}\n```\ntrailing"
	got, err := ExtractJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got != `{"k":1}` {
		t.Fatalf("mismatch: %q", got)
	}
}

func TestExtractJSON_BareObject(t *testing.T) {
	raw := "noise {\"x\":2}\nmore"
	got, err := ExtractJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got != `{"x":2}` {
		t.Fatalf("mismatch: %q", got)
	}
}

func TestExtractJSON_Array(t *testing.T) {
	raw := "leading\n[1,2,3]\ntrailing"
	got, err := ExtractJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got != `[1,2,3]` {
		t.Fatalf("mismatch: %q", got)
	}
}
