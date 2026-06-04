package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	yaml "gopkg.in/yaml.v3"
)

type Config struct {
	SlackAppToken        string
	SlackBotToken        string
	WatchedChannels      []string
	JiraBaseURL          string
	JiraEmail            string
	JiraAPIToken         string
	JiraQORKProjects     []string
	ApprovalThreshold    int
	AuthorCanApprove     bool
	ProposalMinWords     int
	ReminderIntervalDays int
	WarningAtDays        int
	ArchiveGraceDays     int
	Workers              int
	QueueSize            int
	SQLitePath           string
	HealthPort           int
	TriggerToken         string // optional shared token for POST /trigger
	PublicBaseURL        string // optional external URL of the dashboard, e.g. https://bot.example.com
}

func LoadConfig() (*Config, error) {
	required := []string{
		"SLACK_APP_TOKEN", "SLACK_BOT_TOKEN", "WATCHED_CHANNELS",
		"JIRA_BASE_URL", "JIRA_EMAIL", "JIRA_API_TOKEN",
	}
	var missing []string
	for _, k := range required {
		if os.Getenv(k) == "" {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing required env vars: %s", strings.Join(missing, ","))
	}
	c := &Config{
		SlackAppToken:        os.Getenv("SLACK_APP_TOKEN"),
		SlackBotToken:        os.Getenv("SLACK_BOT_TOKEN"),
		WatchedChannels:      splitCSV(os.Getenv("WATCHED_CHANNELS")),
		JiraBaseURL:          strings.TrimRight(os.Getenv("JIRA_BASE_URL"), "/"),
		JiraEmail:            os.Getenv("JIRA_EMAIL"),
		JiraAPIToken:         os.Getenv("JIRA_API_TOKEN"),
		JiraQORKProjects:     splitCSV(os.Getenv("JIRA_QORK_PROJECTS")),
		ApprovalThreshold:    intEnv("APPROVAL_THRESHOLD", 3),
		AuthorCanApprove:     boolEnv("AUTHOR_CAN_APPROVE", false),
		ProposalMinWords:     intEnv("PROPOSAL_MIN_WORDS", minProposalWords),
		ReminderIntervalDays: intEnv("REMINDER_INTERVAL_DAYS", 3),
		WarningAtDays:        intEnv("WARNING_AT_DAYS", 10),
		ArchiveGraceDays:     intEnv("ARCHIVE_GRACE_DAYS", 3),
		Workers:              intEnv("WORKERS", 2),
		QueueSize:            intEnv("QUEUE_SIZE", 64),
		SQLitePath:           defaultStr(os.Getenv("SQLITE_PATH"), "/data/state.db"),
		HealthPort:           intEnv("HEALTH_PORT", 8080),
		TriggerToken:         os.Getenv("TRIGGER_TOKEN"),
		PublicBaseURL:        strings.TrimRight(os.Getenv("PUBLIC_BASE_URL"), "/"),
	}
	if len(c.WatchedChannels) == 0 {
		return nil, errors.New("WATCHED_CHANNELS empty after parse")
	}
	return c, nil
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func intEnv(k string, def int) int {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func boolEnv(k string, def bool) bool {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

func defaultStr(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

type ProjectsConfig struct {
	Subprojects  []string `yaml:"subprojects"`
	QORKProjects []string `yaml:"qork_projects"`
}

type SignalsConfig struct {
	ApproveReactions []string `yaml:"approve_reactions"`
	ApproveReplies   []string `yaml:"approve_replies"`
	CancelReactions  []string `yaml:"cancel_reactions"`
	CancelReplies    []string `yaml:"cancel_replies"`
}

func LoadProjects(path string) (*ProjectsConfig, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var p ProjectsConfig
	if err := yaml.Unmarshal(b, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

func LoadSignals(path string) (*SignalsConfig, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s SignalsConfig
	if err := yaml.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	return &s, nil
}
