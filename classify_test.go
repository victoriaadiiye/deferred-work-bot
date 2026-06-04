package main

import (
	"context"
	"testing"
)

func TestLooksLikeProposal(t *testing.T) {
	cases := []struct {
		name string
		text string
		want bool
	}{
		{"empty", "", false},
		{"whitespace", "   \n ", false},
		{"too short", "fix this", false},
		{"bare url", "https://example.com/a/b", false},
		{"bare url angle-wrapped", "<https://example.com/a/b>", false},
		{"only mentions", "<@U1> <@U2>", false},
		{"emoji", ":tada:", false},
		{"real proposal", "we should refactor the ingest retry path soon", true},
		{"todo with mention", "<@U1> can you file a ticket to clean up the worker pool", true},
		{"exactly four words", "track flaky integration tests", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := looksLikeProposal(tc.text, 0); got != tc.want {
				t.Fatalf("looksLikeProposal(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}

func TestLooksLikeProposal_RespectsMinWords(t *testing.T) {
	if looksLikeProposal("park this later", 4) {
		t.Fatal("3 words should fail a min of 4")
	}
	if !looksLikeProposal("park this later", 3) {
		t.Fatal("3 words should pass a min of 3")
	}
}

func TestClassifyIsProposal(t *testing.T) {
	cases := []struct {
		name string
		resp string
		want bool
	}{
		{"yes", `{"is_proposal": true}`, true},
		{"no", `{"is_proposal": false}`, false},
		{"fenced", "```json\n{\"is_proposal\": true}\n```", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fc := &fakeClaude{resp: tc.resp}
			got, err := classifyIsProposal(context.Background(), fc, "some message")
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestExecuteClassify_CreatesItemWhenProposal(t *testing.T) {
	store := newTestStore(t)
	fake := newFakeSlack("UBOT")
	w := &Worker{queue: make(chan job, 4)}
	fc := &fakeClaude{resp: `{"is_proposal": true}`}
	ex := &JobExecutor{Store: store, Slack: fake, Claude: fc, Worker: w, BotUserID: "UBOT"}

	err := ex.Execute(context.Background(), ClassifyJob{
		Channel: "C1", TS: "1700.1", User: "U1", Text: "we should fix the flaky tests", ApprovalThreshold: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	it, err := store.GetItemByTS("C1", "1700.1")
	if err != nil {
		t.Fatalf("expected item created: %v", err)
	}
	if it.Status != "collecting" || it.ApprovalThreshold != 3 {
		t.Fatalf("bad item: %+v", it)
	}
	if len(fake.reactions) != 2 || fake.reactions[0].Name != "white_check_mark" || fake.reactions[1].Name != "x" {
		t.Fatalf("expected seeded vote reactions, got %+v", fake.reactions)
	}
	if _, ok := nextJobOfType[IntakeJob](w); !ok {
		t.Fatal("expected IntakeJob enqueued")
	}
}

func TestExecuteClassify_SkipsWhenNotProposal(t *testing.T) {
	store := newTestStore(t)
	fake := newFakeSlack("UBOT")
	fc := &fakeClaude{resp: `{"is_proposal": false}`}
	ex := &JobExecutor{Store: store, Slack: fake, Claude: fc, BotUserID: "UBOT"}

	err := ex.Execute(context.Background(), ClassifyJob{
		Channel: "C1", TS: "1700.1", User: "U1", Text: "good morning everyone", ApprovalThreshold: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetItemByTS("C1", "1700.1"); err != ErrNotFound {
		t.Fatal("non-proposal should not be tracked")
	}
	if len(fake.reactions) != 0 {
		t.Fatalf("expected no reactions, got %+v", fake.reactions)
	}
}

func TestExecuteClassify_FailsOpenOnJudgeError(t *testing.T) {
	store := newTestStore(t)
	fake := newFakeSlack("UBOT")
	fc := &fakeClaude{resp: "", err: context.DeadlineExceeded}
	ex := &JobExecutor{Store: store, Slack: fake, Claude: fc, BotUserID: "UBOT"}

	err := ex.Execute(context.Background(), ClassifyJob{
		Channel: "C1", TS: "1700.1", User: "U1", Text: "we should fix the flaky tests", ApprovalThreshold: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetItemByTS("C1", "1700.1"); err != nil {
		t.Fatalf("judge error should fail open and still track: %v", err)
	}
}
