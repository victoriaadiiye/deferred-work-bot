package main

import (
	"fmt"
	"testing"
)

func TestParsePRRefs(t *testing.T) {
	cases := []struct {
		name  string
		text  string
		count int
		want  PRRef
	}{
		{
			"standard PR URL",
			"see https://github.com/qumulo/nexus-be/pull/42 for context",
			1,
			PRRef{Owner: "qumulo", Repo: "nexus-be", Number: "42"},
		},
		{
			"multiple PRs deduped",
			"https://github.com/a/b/pull/1 and https://github.com/a/b/pull/1 again",
			1,
			PRRef{Owner: "a", Repo: "b", Number: "1"},
		},
		{
			"no PR",
			"no link here",
			0,
			PRRef{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParsePRRefs(tc.text)
			if len(got) != tc.count {
				t.Fatalf("count = %d, want %d", len(got), tc.count)
			}
			if tc.count > 0 && got[0] != tc.want {
				t.Fatalf("ref = %+v, want %+v", got[0], tc.want)
			}
		})
	}
}

func TestExtractJiraKeys(t *testing.T) {
	keys := ExtractJiraKeys("QORK-441: fix the thing", "branch: feat/qork-200-something")
	if len(keys) != 2 {
		t.Fatalf("keys = %v", keys)
	}
	if keys[0] != "QORK-441" || keys[1] != "QORK-200" {
		t.Fatalf("keys = %v", keys)
	}
}

func TestExtractJiraKeys_Dedup(t *testing.T) {
	keys := ExtractJiraKeys("QORK-1 and QORK-1")
	if len(keys) != 1 {
		t.Fatalf("expected dedup, got %v", keys)
	}
}

func TestExtractJiraKeys_None(t *testing.T) {
	keys := ExtractJiraKeys("no jira keys here")
	if len(keys) != 0 {
		t.Fatalf("expected none, got %v", keys)
	}
}

type fakeGitHub struct {
	prs map[string]*GitHubPR // keyed by "owner/repo/number"
}

func (f *fakeGitHub) FetchPR(ref PRRef) (*GitHubPR, error) {
	key := ref.Owner + "/" + ref.Repo + "/" + ref.Number
	if pr, ok := f.prs[key]; ok {
		return pr, nil
	}
	return nil, fmt.Errorf("not found")
}

func TestEpicFromPR_FollowsChain(t *testing.T) {
	gh := &fakeGitHub{prs: map[string]*GitHubPR{
		"qumulo/nexus-be/42": {
			Title: "QORK-200: fix auth",
			Body:  "closes QORK-200",
		},
	}}
	// QORK-200 has parent epic QORK-440
	detail := &JiraIssueDetail{}
	detail.Key = "QORK-200"
	detail.Fields.Summary = "fix auth"
	detail.Fields.Parent = &struct {
		Key    string `json:"key"`
		Fields struct {
			Summary   string `json:"summary"`
			IssueType struct {
				Name string `json:"name"`
			} `json:"issuetype"`
		} `json:"fields"`
	}{
		Key: "QORK-440",
	}
	detail.Fields.Parent.Fields.Summary = "MissionQ Feature Parity"
	detail.Fields.Parent.Fields.IssueType.Name = "Epic"

	j := &fakeJira{issues: map[string]*JiraIssueDetail{"QORK-200": detail}}
	exec := &JobExecutor{GitHub: gh, Jira: j}

	key, summary := exec.epicFromPR("check https://github.com/qumulo/nexus-be/pull/42")
	if key != "QORK-440" {
		t.Fatalf("epic key = %q, want QORK-440", key)
	}
	if summary != "MissionQ Feature Parity" {
		t.Fatalf("summary = %q", summary)
	}
}

func TestEpicFromPR_NoPRInText(t *testing.T) {
	gh := &fakeGitHub{prs: map[string]*GitHubPR{}}
	exec := &JobExecutor{GitHub: gh, Jira: &fakeJira{}}
	key, _ := exec.epicFromPR("no pr here")
	if key != "" {
		t.Fatalf("expected empty, got %q", key)
	}
}

func TestEpicFromPR_NoParent(t *testing.T) {
	gh := &fakeGitHub{prs: map[string]*GitHubPR{
		"a/b/1": {Title: "QORK-100: stuff"},
	}}
	detail := &JiraIssueDetail{}
	detail.Key = "QORK-100"
	detail.Fields.Summary = "stuff"
	j := &fakeJira{issues: map[string]*JiraIssueDetail{"QORK-100": detail}}
	exec := &JobExecutor{GitHub: gh, Jira: j}

	key, _ := exec.epicFromPR("https://github.com/a/b/pull/1")
	if key != "" {
		t.Fatalf("expected empty when no parent, got %q", key)
	}
}

func TestEpicFromPR_NilGitHub(t *testing.T) {
	exec := &JobExecutor{GitHub: nil, Jira: &fakeJira{}}
	key, _ := exec.epicFromPR("https://github.com/a/b/pull/1")
	if key != "" {
		t.Fatalf("expected empty when no github client, got %q", key)
	}
}
