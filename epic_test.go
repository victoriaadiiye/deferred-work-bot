package main

import (
	"context"
	"encoding/json"
	"testing"
)

func epicIssue(key, summary string) JiraIssue {
	var iss JiraIssue
	iss.Key = key
	iss.Fields.Summary = summary
	return iss
}

func TestClassifyEpic_PicksCandidate(t *testing.T) {
	fc := &fakeClaude{resp: `{"epic":"QORK-440"}`}
	epics := []JiraIssue{epicIssue("QORK-440", "MissionQ Feature Parity"), epicIssue("QORK-217", "Observability")}
	got, err := classifyEpic(context.Background(), fc, "MQ parity dispatcher cleanup", epics)
	if err != nil {
		t.Fatal(err)
	}
	if got != "QORK-440" {
		t.Fatalf("epic = %q, want QORK-440", got)
	}
}

func TestClassifyEpic_RejectsHallucinatedKey(t *testing.T) {
	// Model returns a key that is not among the candidates — must be dropped.
	fc := &fakeClaude{resp: `{"epic":"QORK-9999"}`}
	epics := []JiraIssue{epicIssue("QORK-440", "MissionQ Feature Parity")}
	got, _ := classifyEpic(context.Background(), fc, "x", epics)
	if got != "" {
		t.Fatalf("expected hallucinated key to be rejected, got %q", got)
	}
}

func TestClassifyEpic_NoEpicsNoCall(t *testing.T) {
	fc := &fakeClaude{resp: `{"epic":"QORK-1"}`}
	got, _ := classifyEpic(context.Background(), fc, "x", nil)
	if got != "" {
		t.Fatalf("got %q", got)
	}
	if len(fc.got) != 0 {
		t.Fatal("should not call claude when there are no epic candidates")
	}
}

func TestClassifyEpic_EmptyWhenNoFit(t *testing.T) {
	fc := &fakeClaude{resp: `{"epic":""}`}
	epics := []JiraIssue{epicIssue("QORK-440", "MissionQ Feature Parity")}
	got, _ := classifyEpic(context.Background(), fc, "unrelated work", epics)
	if got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestFileProposal_SetsParentEpic(t *testing.T) {
	j := &fakeJira{}
	d := &Draft{Summary: "x", Description: "y", IssueType: "Task", Labels: []string{"deferred-work"}, Priority: "Medium", EpicKey: "QORK-440"}
	if _, err := FileProposal(j, FileInput{Branch: "new", ProjectKey: "QORK", Draft: d}); err != nil {
		t.Fatal(err)
	}
	if j.lastCreate.ParentEpicKey != "QORK-440" {
		t.Fatalf("parent epic = %q, want QORK-440", j.lastCreate.ParentEpicKey)
	}
}

func latestEpicOverride(t *testing.T, store *Store, itemID int64) string {
	t.Helper()
	v, _ := store.LatestOverride(itemID, "epic_override")
	return v
}

func TestCmdEpic_Override(t *testing.T) {
	cases := []struct {
		name, input, want string
	}{
		{"valid key uppercased", "<@UBOT> epic: qork-440", "QORK-440"},
		{"none clears", "<@UBOT> epic: none", "none"},
		{"garbage ignored", "<@UBOT> epic: not a key", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newTestStore(t)
			fake := newFakeSlack("UBOT")
			r := &Router{Store: store, Slack: fake, BotUserID: "UBOT", WatchedChannels: map[string]bool{"C1": true}, Signals: &SignalsConfig{}, ApprovalThreshold: 3}
			seedItem(t, r, MessageEvent{Channel: "C1", TS: "1700.1", User: "U1", Text: "x"})
			it, _ := store.GetItemByTS("C1", "1700.1")
			r.HandleMessage(MessageEvent{Channel: "C1", TS: "1700.2", ThreadTS: "1700.1", User: "U2", Text: tc.input})
			if got := latestEpicOverride(t, store, it.ID); got != tc.want {
				t.Fatalf("override = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDraft_EpicKeyRoundTripsThroughJSON(t *testing.T) {
	d := Draft{Summary: "s", EpicKey: "QORK-440", EpicSummary: "MissionQ Feature Parity"}
	b, _ := json.Marshal(d)
	var back Draft
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.EpicKey != "QORK-440" || back.EpicSummary != "MissionQ Feature Parity" {
		t.Fatalf("epic fields lost: %+v", back)
	}
}
