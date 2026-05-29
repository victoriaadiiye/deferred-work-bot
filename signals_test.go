package main

import "testing"

func TestIsApproveReaction(t *testing.T) {
	sig := &SignalsConfig{ApproveReactions: []string{"white_check_mark", "claude-it", "+1"}}
	cases := []struct {
		emoji string
		want  bool
	}{
		{"white_check_mark", true},
		{"claude-it", true},
		{"+1", true},
		{"x", false},
		{"thumbsdown", false},
	}
	for _, tc := range cases {
		if got := IsApproveReaction(sig, tc.emoji); got != tc.want {
			t.Errorf("IsApproveReaction(%q)=%v want %v", tc.emoji, got, tc.want)
		}
	}
}

func TestIsCancelReaction(t *testing.T) {
	sig := &SignalsConfig{CancelReactions: []string{"x"}}
	if !IsCancelReaction(sig, "x") {
		t.Fatal("x should be cancel")
	}
	if IsCancelReaction(sig, "white_check_mark") {
		t.Fatal("white_check_mark should not be cancel")
	}
}

func TestReplyHasApprove(t *testing.T) {
	sig := &SignalsConfig{ApproveReplies: []string{"approve", "lgtm", "+1"}}
	cases := []struct {
		text string
		want bool
	}{
		{"lgtm", true},
		{"LGTM!", true},
		{"approve this", true},
		{"approval-pending", false}, // word-bounded
		{"+1 from me", true},
		{"nope", false},
	}
	for _, tc := range cases {
		if got := ReplyHasApprove(sig, tc.text); got != tc.want {
			t.Errorf("ReplyHasApprove(%q)=%v want %v", tc.text, got, tc.want)
		}
	}
}

func TestReplyHasCancel(t *testing.T) {
	sig := &SignalsConfig{CancelReplies: []string{"cancel"}}
	if !ReplyHasCancel(sig, "@bot cancel") {
		t.Fatal("expected match")
	}
	if ReplyHasCancel(sig, "cancellation policy") {
		t.Fatal("word-bound failed")
	}
}

func TestResolutionKeyword(t *testing.T) {
	cases := []struct {
		text string
		want string
	}{
		{"comment please", "comment"},
		{"file as new", "new"},
		{"both", "both"},
		{"unrelated", ""},
	}
	for _, tc := range cases {
		if got := ResolutionKeyword(tc.text); got != tc.want {
			t.Errorf("ResolutionKeyword(%q)=%q want %q", tc.text, got, tc.want)
		}
	}
}
