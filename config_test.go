package main

import (
	"os"
	"testing"
)

func TestLoadConfig_AllRequiredSet(t *testing.T) {
	env := map[string]string{
		"SLACK_APP_TOKEN":        "xapp-1",
		"SLACK_BOT_TOKEN":        "xoxb-1",
		"WATCHED_CHANNELS":       "C123,C456",
		"JIRA_BASE_URL":          "https://example.atlassian.net",
		"JIRA_EMAIL":             "me@example.com",
		"JIRA_API_TOKEN":         "tok",
		"JIRA_QORK_PROJECTS":     "QORK",
		"APPROVAL_THRESHOLD":     "3",
		"REMINDER_INTERVAL_DAYS": "3",
		"WARNING_AT_DAYS":        "10",
		"ARCHIVE_GRACE_DAYS":     "3",
		"WORKERS":                "2",
		"QUEUE_SIZE":             "64",
		"SQLITE_PATH":            "/tmp/test.db",
		"HEALTH_PORT":            "8080",
	}
	for k, v := range env {
		t.Setenv(k, v)
	}
	c, err := LoadConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.SlackAppToken != "xapp-1" || c.SlackBotToken != "xoxb-1" {
		t.Fatalf("tokens not parsed: %+v", c)
	}
	if len(c.WatchedChannels) != 2 || c.WatchedChannels[0] != "C123" {
		t.Fatalf("channels not parsed: %+v", c.WatchedChannels)
	}
	if c.ApprovalThreshold != 3 || c.Workers != 2 {
		t.Fatalf("ints not parsed: %+v", c)
	}
}

func TestLoadConfig_MissingRequired(t *testing.T) {
	os.Clearenv()
	_, err := LoadConfig()
	if err == nil {
		t.Fatal("expected error for missing required env vars")
	}
}

func TestLoadConfig_Defaults(t *testing.T) {
	os.Clearenv()
	t.Setenv("SLACK_APP_TOKEN", "x")
	t.Setenv("SLACK_BOT_TOKEN", "x")
	t.Setenv("WATCHED_CHANNELS", "C1")
	t.Setenv("JIRA_BASE_URL", "x")
	t.Setenv("JIRA_EMAIL", "x")
	t.Setenv("JIRA_API_TOKEN", "x")
	t.Setenv("JIRA_QORK_PROJECTS", "QORK")
	c, err := LoadConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.ApprovalThreshold != 3 {
		t.Fatalf("default threshold wrong: %d", c.ApprovalThreshold)
	}
	if c.ReminderIntervalDays != 3 || c.WarningAtDays != 10 || c.ArchiveGraceDays != 3 {
		t.Fatalf("default lifecycle wrong: %+v", c)
	}
	if c.Workers != 2 || c.QueueSize != 64 || c.HealthPort != 8080 {
		t.Fatalf("default pool/server wrong: %+v", c)
	}
}

func TestLoadProjects(t *testing.T) {
	tmp := t.TempDir() + "/projects.yaml"
	os.WriteFile(tmp, []byte(`subprojects:
  - qompass
  - qatalyst
qork_projects:
  - QORK
`), 0o644)
	p, err := LoadProjects(tmp)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(p.Subprojects) != 2 || p.Subprojects[0] != "qompass" {
		t.Fatalf("subprojects wrong: %+v", p)
	}
}

func TestLoadSignals(t *testing.T) {
	tmp := t.TempDir() + "/signals.yaml"
	os.WriteFile(tmp, []byte(`approve_reactions:
  - white_check_mark
  - claude-it
approve_replies:
  - approve
  - lgtm
cancel_reactions:
  - x
cancel_replies:
  - cancel
`), 0o644)
	s, err := LoadSignals(tmp)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(s.ApproveReactions) != 2 || s.ApproveReactions[1] != "claude-it" {
		t.Fatalf("approve reactions wrong: %+v", s)
	}
	if len(s.CancelReplies) != 1 || s.CancelReplies[0] != "cancel" {
		t.Fatalf("cancel replies wrong: %+v", s)
	}
}
