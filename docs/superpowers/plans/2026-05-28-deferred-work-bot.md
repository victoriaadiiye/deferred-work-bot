# deferred-work-bot Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a Slack bot that tracks deferred work, gates it behind a 3-approval social-proof vote, drafts a Jira ticket via the local `claude` CLI (with related-ticket detection), and files it on a final 1-approval proposal vote.

**Architecture:** Single Go binary. Socket Mode Slack events → in-process event router → bounded worker pool. SQLite for state + audit. Shells to local `claude` CLI for LLM work (no Anthropic API key). Jira Cloud REST for tickets. Docker + compose deployment with `/health`, `/metrics`, `POST /trigger`. Mirrors `pr-review-bot` shape.

**Tech Stack:** Go 1.22+, `github.com/slack-go/slack`, `modernc.org/sqlite` (CGo-free), `gopkg.in/yaml.v3`, `net/http`, stdlib only otherwise.

---

## File Map

| File | Responsibility |
|------|----------------|
| `main.go` | Entry point. Wire config, store, slack client, worker pool, ticker, health server. Signal handling. |
| `config.go` | `Config` struct, env loading, projects.yaml + signals.yaml loading. |
| `store.go` | SQLite schema migrations, all CRUD against items/votes/proposals/tickets/events. |
| `signals.go` | Pure helpers for matching approve/cancel signals from reactions and reply text. |
| `slack.go` | `SlackAPI` interface, event router, command dispatch, message builders. |
| `claude.go` | Shell wrapper for the `claude` CLI; JSON parsing helpers. |
| `jira.go` | Jira REST client: JQL search, create issue, add comment, add label. |
| `propose.go` | Drafting + Jira flow: subproject detect, relevance, branch decision, draft, post. |
| `worker.go` | Bounded job queue + worker pool + graceful drain. |
| `ticker.go` | 5-min loop: reminders at 3d cadence, warning at 10d, archive at 13d. |
| `health.go` | `/health`, `/metrics`, `POST /trigger` HTTP server. |
| `projects.yaml` | Sub-project list (`qompass`, `qatalyst`, …) and QORK project keys. |
| `signals.yaml` | Approve reactions, approve reply tokens, cancel signals. |
| `slack-manifest.yaml` | Slack app scopes + event subscriptions. |
| `Dockerfile` | Multi-stage: Go build + `claude` CLI install. |
| `docker-compose.yml` | Service definition, mounts, env_file, restart policy. |
| `Taskfile.yaml` | `deploy`, `redeploy`, `kill`, `logs`, `status`, `test`. |
| `README.md` | Setup, usage, config reference. |
| `*_test.go` | Per-file tests with fakes. |

---

## Task 1: Module init + scaffold

**Files:**
- Create: `/Users/vuifhaolain/projects/deferred-work-bot/go.mod`
- Create: `/Users/vuifhaolain/projects/deferred-work-bot/.gitignore`
- Create: `/Users/vuifhaolain/projects/deferred-work-bot/Taskfile.yaml`
- Create: `/Users/vuifhaolain/projects/deferred-work-bot/main.go`

- [ ] **Step 1: Init module**

```bash
cd /Users/vuifhaolain/projects/deferred-work-bot
go mod init github.com/vuifhaolain/deferred-work-bot
go get github.com/slack-go/slack@latest
go get modernc.org/sqlite@latest
go get gopkg.in/yaml.v3@latest
```

- [ ] **Step 2: Write `.gitignore`**

```
.env
data/
*.db
*.db-*
deferred-work-bot
private-key.pem
```

- [ ] **Step 3: Write minimal `main.go`**

```go
package main

import "fmt"

func main() {
	fmt.Println("deferred-work-bot")
}
```

- [ ] **Step 4: Write `Taskfile.yaml`**

```yaml
version: '3'
tasks:
  build:
    cmds:
      - go build -o deferred-work-bot ./...
  test:
    cmds:
      - go test ./... -count=1 -race
  fmt:
    cmds:
      - gofumpt -w .
  lint:
    cmds:
      - golangci-lint run
  deploy:
    cmds:
      - docker compose up -d --build
  redeploy:
    cmds:
      - docker compose up -d --build --force-recreate
  kill:
    cmds:
      - docker compose down
  logs:
    cmds:
      - docker compose logs -f
  status:
    cmds:
      - docker compose ps
```

- [ ] **Step 5: Verify build and commit**

Run: `go build ./...`
Expected: succeeds, produces `deferred-work-bot` binary.

```bash
git add -A
git commit -m "chore: scaffold module, taskfile, gitignore"
```

---

## Task 2: Config + env loading

**Files:**
- Create: `config.go`
- Create: `config_test.go`
- Create: `.env.example`

- [ ] **Step 1: Write failing test for `LoadConfig`**

`config_test.go`:

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run TestLoadConfig ./...`
Expected: FAIL — `LoadConfig` undefined.

- [ ] **Step 3: Implement `config.go`**

```go
package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
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
	ReminderIntervalDays int
	WarningAtDays        int
	ArchiveGraceDays     int
	Workers              int
	QueueSize            int
	SQLitePath           string
	HealthPort           int
	TriggerToken         string // optional shared token for POST /trigger
}

func LoadConfig() (*Config, error) {
	required := []string{
		"SLACK_APP_TOKEN", "SLACK_BOT_TOKEN", "WATCHED_CHANNELS",
		"JIRA_BASE_URL", "JIRA_EMAIL", "JIRA_API_TOKEN", "JIRA_QORK_PROJECTS",
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
		ReminderIntervalDays: intEnv("REMINDER_INTERVAL_DAYS", 3),
		WarningAtDays:        intEnv("WARNING_AT_DAYS", 10),
		ArchiveGraceDays:     intEnv("ARCHIVE_GRACE_DAYS", 3),
		Workers:              intEnv("WORKERS", 2),
		QueueSize:            intEnv("QUEUE_SIZE", 64),
		SQLitePath:           defaultStr(os.Getenv("SQLITE_PATH"), "/data/state.db"),
		HealthPort:           intEnv("HEALTH_PORT", 8080),
		TriggerToken:         os.Getenv("TRIGGER_TOKEN"),
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

func defaultStr(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
```

- [ ] **Step 4: Run tests, verify pass**

Run: `go test -run TestLoadConfig ./... -v`
Expected: 3 PASS.

- [ ] **Step 5: Write `.env.example`**

```
SLACK_APP_TOKEN=xapp-...
SLACK_BOT_TOKEN=xoxb-...
WATCHED_CHANNELS=C0123ABCDEF
JIRA_BASE_URL=https://qumulo.atlassian.net
JIRA_EMAIL=vuifhaolain@qumulo.com
JIRA_API_TOKEN=
JIRA_QORK_PROJECTS=QORK
APPROVAL_THRESHOLD=3
REMINDER_INTERVAL_DAYS=3
WARNING_AT_DAYS=10
ARCHIVE_GRACE_DAYS=3
WORKERS=2
QUEUE_SIZE=64
SQLITE_PATH=/data/state.db
HEALTH_PORT=8080
TRIGGER_TOKEN=
```

- [ ] **Step 6: Commit**

```bash
git add -A
git commit -m "feat(config): env loading with defaults and validation"
```

---

## Task 3: projects.yaml + signals.yaml loaders

**Files:**
- Create: `projects.yaml`
- Create: `signals.yaml`
- Modify: `config.go` (add loader funcs)
- Modify: `config_test.go` (add tests)

- [ ] **Step 1: Write failing tests**

Append to `config_test.go`:

```go
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
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./... -v`
Expected: FAIL — `LoadProjects`, `LoadSignals` undefined.

- [ ] **Step 3: Implement loaders**

Append to `config.go`:

```go
import "gopkg.in/yaml.v3"

type ProjectsConfig struct {
	Subprojects   []string `yaml:"subprojects"`
	QORKProjects  []string `yaml:"qork_projects"`
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
```

(Note: merge the new `import` into existing import block.)

- [ ] **Step 4: Write the actual YAML files**

`projects.yaml`:

```yaml
subprojects:
  - qompass
  - qatalyst
  - nexus
  - qfsd
  - qatalyst-loadgen
qork_projects:
  - QORK
```

`signals.yaml`:

```yaml
approve_reactions:
  - white_check_mark
  - claude-it
  - "+1"
  - thumbsup
approve_replies:
  - approve
  - approved
  - "+1"
  - lgtm
cancel_reactions:
  - x
cancel_replies:
  - cancel
```

- [ ] **Step 5: Tests pass + commit**

Run: `go test ./... -v`
Expected: all PASS.

```bash
git add -A
git commit -m "feat(config): projects.yaml + signals.yaml loaders"
```

---

## Task 4: Store — schema + items CRUD

**Files:**
- Create: `store.go`
- Create: `store_test.go`

- [ ] **Step 1: Failing test for store init + item insert/get**

```go
package main

import (
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenStore(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestStore_InsertGetItem(t *testing.T) {
	s := newTestStore(t)
	it := &Item{
		SlackChannel:      "C1",
		SlackTS:           "1700000000.000100",
		AuthorSlackID:     "U1",
		Text:              "deferred work blob",
		Status:            "collecting",
		ApprovalThreshold: 3,
	}
	if err := s.InsertItem(it); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if it.ID == 0 {
		t.Fatal("ID not set after insert")
	}
	got, err := s.GetItemByTS("C1", "1700000000.000100")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Text != "deferred work blob" || got.Status != "collecting" {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}
	if got.CreatedAt.IsZero() {
		t.Fatal("created_at not set")
	}
	_ = time.Now()
}

func TestStore_UniqueTS(t *testing.T) {
	s := newTestStore(t)
	it := &Item{SlackChannel: "C1", SlackTS: "1", AuthorSlackID: "U1", Text: "x", Status: "collecting", ApprovalThreshold: 3}
	if err := s.InsertItem(it); err != nil {
		t.Fatal(err)
	}
	dup := *it
	dup.ID = 0
	if err := s.InsertItem(&dup); err == nil {
		t.Fatal("expected unique constraint violation")
	}
}

func TestStore_UpdateStatus(t *testing.T) {
	s := newTestStore(t)
	it := &Item{SlackChannel: "C1", SlackTS: "1", AuthorSlackID: "U1", Text: "x", Status: "collecting", ApprovalThreshold: 3}
	s.InsertItem(it)
	if err := s.UpdateItemStatus(it.ID, "proposing"); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, _ := s.GetItemByID(it.ID)
	if got.Status != "proposing" {
		t.Fatalf("status not updated: %s", got.Status)
	}
}

func TestStore_ListByStatus(t *testing.T) {
	s := newTestStore(t)
	for i, st := range []string{"collecting", "collecting", "ticketed"} {
		s.InsertItem(&Item{SlackChannel: "C1", SlackTS: string(rune('a' + i)), AuthorSlackID: "U1", Text: "x", Status: st, ApprovalThreshold: 3})
	}
	items, err := s.ListItemsByStatus("collecting")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2, got %d", len(items))
	}
}
```

- [ ] **Step 2: Run, verify failure**

Run: `go test ./... -v`
Expected: FAIL — `Store`, `Item`, etc. undefined.

- [ ] **Step 3: Implement `store.go` (items only for this task)**

```go
package main

import (
	"database/sql"
	"errors"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

type Item struct {
	ID                int64
	SlackChannel      string
	SlackTS           string
	AuthorSlackID     string
	Text              string
	Subproject        string
	Status            string
	ApprovalThreshold int
	LastReminderAt    *time.Time
	WarningPostedAt   *time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

const schemaItems = `
CREATE TABLE IF NOT EXISTS items (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  slack_channel TEXT NOT NULL,
  slack_ts TEXT NOT NULL UNIQUE,
  author_slack_id TEXT NOT NULL,
  text TEXT NOT NULL,
  subproject TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL,
  approval_threshold INTEGER NOT NULL DEFAULT 3,
  last_reminder_at TIMESTAMP,
  warning_posted_at TIMESTAMP,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_items_status ON items(status);
`

func OpenStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schemaItems); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) InsertItem(it *Item) error {
	res, err := s.db.Exec(`INSERT INTO items(slack_channel, slack_ts, author_slack_id, text, subproject, status, approval_threshold) VALUES(?,?,?,?,?,?,?)`,
		it.SlackChannel, it.SlackTS, it.AuthorSlackID, it.Text, it.Subproject, it.Status, it.ApprovalThreshold)
	if err != nil {
		return err
	}
	id, _ := res.LastInsertId()
	it.ID = id
	return s.refreshItem(it)
}

func (s *Store) refreshItem(it *Item) error {
	got, err := s.GetItemByID(it.ID)
	if err != nil {
		return err
	}
	*it = *got
	return nil
}

func (s *Store) GetItemByID(id int64) (*Item, error) {
	row := s.db.QueryRow(`SELECT id, slack_channel, slack_ts, author_slack_id, text, subproject, status, approval_threshold, last_reminder_at, warning_posted_at, created_at, updated_at FROM items WHERE id = ?`, id)
	return scanItem(row)
}

func (s *Store) GetItemByTS(channel, ts string) (*Item, error) {
	row := s.db.QueryRow(`SELECT id, slack_channel, slack_ts, author_slack_id, text, subproject, status, approval_threshold, last_reminder_at, warning_posted_at, created_at, updated_at FROM items WHERE slack_channel = ? AND slack_ts = ?`, channel, ts)
	return scanItem(row)
}

func (s *Store) UpdateItemStatus(id int64, status string) error {
	_, err := s.db.Exec(`UPDATE items SET status = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, status, id)
	return err
}

func (s *Store) ListItemsByStatus(status string) ([]*Item, error) {
	rows, err := s.db.Query(`SELECT id, slack_channel, slack_ts, author_slack_id, text, subproject, status, approval_threshold, last_reminder_at, warning_posted_at, created_at, updated_at FROM items WHERE status = ? ORDER BY created_at`, status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Item
	for rows.Next() {
		it, err := scanItemRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanItem(r rowScanner) (*Item, error) {
	var it Item
	var lastRem, warnAt sql.NullTime
	err := r.Scan(&it.ID, &it.SlackChannel, &it.SlackTS, &it.AuthorSlackID, &it.Text, &it.Subproject, &it.Status, &it.ApprovalThreshold, &lastRem, &warnAt, &it.CreatedAt, &it.UpdatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if lastRem.Valid {
		it.LastReminderAt = &lastRem.Time
	}
	if warnAt.Valid {
		it.WarningPostedAt = &warnAt.Time
	}
	return &it, nil
}

func scanItemRows(r *sql.Rows) (*Item, error) { return scanItem(r) }

var ErrNotFound = errors.New("not found")
```

- [ ] **Step 4: Run tests, verify pass**

Run: `go test ./... -v`
Expected: 4 PASS.

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "feat(store): items table + CRUD with SQLite"
```

---

## Task 5: Store — votes (dedup, source agnostic)

**Files:**
- Modify: `store.go`
- Modify: `store_test.go`

- [ ] **Step 1: Failing test**

Append to `store_test.go`:

```go
func TestStore_UpsertVote(t *testing.T) {
	s := newTestStore(t)
	it := &Item{SlackChannel: "C1", SlackTS: "1", AuthorSlackID: "U_AUTHOR", Text: "x", Status: "collecting", ApprovalThreshold: 3}
	s.InsertItem(it)

	if err := s.UpsertVote(it.ID, "U2", "reaction", "white_check_mark"); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertVote(it.ID, "U2", "reply", "lgtm"); err != nil {
		t.Fatal(err) // same user, different source — should dedup, not error
	}
	n, err := s.CountVotes(it.ID)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected 1 vote after dedup, got %d", n)
	}

	s.UpsertVote(it.ID, "U3", "reaction", "+1")
	n, _ = s.CountVotes(it.ID)
	if n != 2 {
		t.Fatalf("expected 2 votes, got %d", n)
	}
}

func TestStore_RemoveVote(t *testing.T) {
	s := newTestStore(t)
	it := &Item{SlackChannel: "C1", SlackTS: "1", AuthorSlackID: "U1", Text: "x", Status: "collecting", ApprovalThreshold: 3}
	s.InsertItem(it)
	s.UpsertVote(it.ID, "U2", "reaction", "white_check_mark")
	if err := s.RemoveVote(it.ID, "U2"); err != nil {
		t.Fatal(err)
	}
	n, _ := s.CountVotes(it.ID)
	if n != 0 {
		t.Fatalf("expected 0 votes after removal, got %d", n)
	}
}

func TestStore_VoteExcludesAuthor(t *testing.T) {
	// Author self-vote enforcement happens at the dispatch layer, but the
	// store offers a HasVoted helper used by the caller to skip inserts.
	s := newTestStore(t)
	it := &Item{SlackChannel: "C1", SlackTS: "1", AuthorSlackID: "U_AUTHOR", Text: "x", Status: "collecting", ApprovalThreshold: 3}
	s.InsertItem(it)
	ok, err := s.HasVoted(it.ID, "U_AUTHOR")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("HasVoted should be false initially")
	}
}
```

- [ ] **Step 2: Run, verify failure**

Run: `go test ./... -v`
Expected: FAIL — methods undefined.

- [ ] **Step 3: Extend `store.go`**

Add to schema (modify `OpenStore` to execute a multi-statement schema string):

```go
const schemaVotes = `
CREATE TABLE IF NOT EXISTS votes (
  item_id INTEGER NOT NULL REFERENCES items(id) ON DELETE CASCADE,
  user_slack_id TEXT NOT NULL,
  source TEXT NOT NULL,
  signal TEXT NOT NULL,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (item_id, user_slack_id)
);
`
```

Update `OpenStore` to run `schemaItems + schemaVotes` (concatenated).

Add methods:

```go
func (s *Store) UpsertVote(itemID int64, user, source, signal string) error {
	_, err := s.db.Exec(`INSERT INTO votes(item_id, user_slack_id, source, signal) VALUES(?,?,?,?)
		ON CONFLICT(item_id, user_slack_id) DO NOTHING`, itemID, user, source, signal)
	return err
}

func (s *Store) RemoveVote(itemID int64, user string) error {
	_, err := s.db.Exec(`DELETE FROM votes WHERE item_id = ? AND user_slack_id = ?`, itemID, user)
	return err
}

func (s *Store) CountVotes(itemID int64) (int, error) {
	row := s.db.QueryRow(`SELECT COUNT(*) FROM votes WHERE item_id = ?`, itemID)
	var n int
	err := row.Scan(&n)
	return n, err
}

func (s *Store) HasVoted(itemID int64, user string) (bool, error) {
	row := s.db.QueryRow(`SELECT 1 FROM votes WHERE item_id = ? AND user_slack_id = ?`, itemID, user)
	var v int
	err := row.Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}
```

- [ ] **Step 4: Run tests, verify pass**

Run: `go test ./... -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "feat(store): votes table with cross-source dedup"
```

---

## Task 6: Store — proposals, tickets, events

**Files:**
- Modify: `store.go`
- Modify: `store_test.go`

- [ ] **Step 1: Failing tests**

```go
func TestStore_ProposalsRoundtrip(t *testing.T) {
	s := newTestStore(t)
	it := &Item{SlackChannel: "C1", SlackTS: "1", AuthorSlackID: "U1", Text: "x", Status: "proposing", ApprovalThreshold: 3}
	s.InsertItem(it)
	p := &Proposal{
		ItemID:             it.ID,
		SlackTS:            "1700.000200",
		DraftJSON:          `{"summary":"do X"}`,
		RelatedTicketsJSON: `[]`,
		Branch:             "new",
		Status:             "draft",
	}
	if err := s.InsertProposal(p); err != nil {
		t.Fatal(err)
	}
	if p.ID == 0 {
		t.Fatal("proposal ID not set")
	}
	got, err := s.GetLatestProposal(it.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.DraftJSON != p.DraftJSON {
		t.Fatalf("mismatch: %+v", got)
	}
}

func TestStore_RecordTicket(t *testing.T) {
	s := newTestStore(t)
	it := &Item{SlackChannel: "C1", SlackTS: "1", AuthorSlackID: "U1", Text: "x", Status: "proposed", ApprovalThreshold: 3}
	s.InsertItem(it)
	p := &Proposal{ItemID: it.ID, SlackTS: "2", DraftJSON: "{}", RelatedTicketsJSON: "[]", Branch: "new", Status: "approved"}
	s.InsertProposal(p)
	tk := &Ticket{ProposalID: p.ID, JiraKey: "QORK-1", JiraURL: "https://x/QORK-1", Action: "created"}
	if err := s.InsertTicket(tk); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetTicketByProposal(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.JiraKey != "QORK-1" {
		t.Fatalf("mismatch: %+v", got)
	}
}

func TestStore_LogEvent(t *testing.T) {
	s := newTestStore(t)
	it := &Item{SlackChannel: "C1", SlackTS: "1", AuthorSlackID: "U1", Text: "x", Status: "collecting", ApprovalThreshold: 3}
	s.InsertItem(it)
	if err := s.LogEvent(&it.ID, "vote", `{"user":"U2"}`); err != nil {
		t.Fatal(err)
	}
	events, err := s.ListEventsForItem(it.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Kind != "vote" {
		t.Fatalf("event mismatch: %+v", events)
	}
}
```

- [ ] **Step 2: Run, verify failure**

Expected: types + methods undefined.

- [ ] **Step 3: Extend `store.go`**

Add schema:

```go
const schemaProposals = `
CREATE TABLE IF NOT EXISTS proposals (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  item_id INTEGER NOT NULL REFERENCES items(id) ON DELETE CASCADE,
  slack_ts TEXT NOT NULL,
  draft_json TEXT NOT NULL,
  related_tickets_json TEXT NOT NULL,
  branch TEXT NOT NULL,
  existing_ticket_key TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_proposals_item ON proposals(item_id);
`

const schemaTickets = `
CREATE TABLE IF NOT EXISTS tickets (
  proposal_id INTEGER NOT NULL REFERENCES proposals(id) ON DELETE CASCADE,
  jira_key TEXT NOT NULL,
  jira_url TEXT NOT NULL,
  action TEXT NOT NULL,
  existing_ticket_key TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (proposal_id, jira_key)
);
`

const schemaEvents = `
CREATE TABLE IF NOT EXISTS events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  item_id INTEGER REFERENCES items(id) ON DELETE CASCADE,
  kind TEXT NOT NULL,
  payload_json TEXT NOT NULL DEFAULT '{}',
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_events_item ON events(item_id);
`
```

Update `OpenStore` schema concatenation to `schemaItems + schemaVotes + schemaProposals + schemaTickets + schemaEvents`.

Types + methods:

```go
type Proposal struct {
	ID                 int64
	ItemID             int64
	SlackTS            string
	DraftJSON          string
	RelatedTicketsJSON string
	Branch             string
	ExistingTicketKey  string
	Status             string
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

type Ticket struct {
	ProposalID        int64
	JiraKey           string
	JiraURL           string
	Action            string
	ExistingTicketKey string
	CreatedAt         time.Time
}

type Event struct {
	ID        int64
	ItemID    *int64
	Kind      string
	Payload   string
	CreatedAt time.Time
}

func (s *Store) InsertProposal(p *Proposal) error {
	res, err := s.db.Exec(`INSERT INTO proposals(item_id, slack_ts, draft_json, related_tickets_json, branch, existing_ticket_key, status) VALUES(?,?,?,?,?,?,?)`,
		p.ItemID, p.SlackTS, p.DraftJSON, p.RelatedTicketsJSON, p.Branch, p.ExistingTicketKey, p.Status)
	if err != nil {
		return err
	}
	id, _ := res.LastInsertId()
	p.ID = id
	return nil
}

func (s *Store) GetLatestProposal(itemID int64) (*Proposal, error) {
	row := s.db.QueryRow(`SELECT id, item_id, slack_ts, draft_json, related_tickets_json, branch, existing_ticket_key, status, created_at, updated_at FROM proposals WHERE item_id = ? ORDER BY id DESC LIMIT 1`, itemID)
	var p Proposal
	err := row.Scan(&p.ID, &p.ItemID, &p.SlackTS, &p.DraftJSON, &p.RelatedTicketsJSON, &p.Branch, &p.ExistingTicketKey, &p.Status, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &p, err
}

func (s *Store) UpdateProposalStatus(id int64, status string) error {
	_, err := s.db.Exec(`UPDATE proposals SET status = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, status, id)
	return err
}

func (s *Store) GetProposalBySlackTS(ts string) (*Proposal, error) {
	row := s.db.QueryRow(`SELECT id, item_id, slack_ts, draft_json, related_tickets_json, branch, existing_ticket_key, status, created_at, updated_at FROM proposals WHERE slack_ts = ?`, ts)
	var p Proposal
	err := row.Scan(&p.ID, &p.ItemID, &p.SlackTS, &p.DraftJSON, &p.RelatedTicketsJSON, &p.Branch, &p.ExistingTicketKey, &p.Status, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &p, err
}

func (s *Store) InsertTicket(t *Ticket) error {
	_, err := s.db.Exec(`INSERT INTO tickets(proposal_id, jira_key, jira_url, action, existing_ticket_key) VALUES(?,?,?,?,?)`,
		t.ProposalID, t.JiraKey, t.JiraURL, t.Action, t.ExistingTicketKey)
	return err
}

func (s *Store) GetTicketByProposal(proposalID int64) (*Ticket, error) {
	row := s.db.QueryRow(`SELECT proposal_id, jira_key, jira_url, action, existing_ticket_key, created_at FROM tickets WHERE proposal_id = ? LIMIT 1`, proposalID)
	var t Ticket
	err := row.Scan(&t.ProposalID, &t.JiraKey, &t.JiraURL, &t.Action, &t.ExistingTicketKey, &t.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &t, err
}

func (s *Store) LogEvent(itemID *int64, kind, payload string) error {
	_, err := s.db.Exec(`INSERT INTO events(item_id, kind, payload_json) VALUES(?,?,?)`, itemID, kind, payload)
	return err
}

func (s *Store) ListEventsForItem(itemID int64) ([]*Event, error) {
	rows, err := s.db.Query(`SELECT id, item_id, kind, payload_json, created_at FROM events WHERE item_id = ? ORDER BY id`, itemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Event
	for rows.Next() {
		var e Event
		var iid sql.NullInt64
		if err := rows.Scan(&e.ID, &iid, &e.Kind, &e.Payload, &e.CreatedAt); err != nil {
			return nil, err
		}
		if iid.Valid {
			v := iid.Int64
			e.ItemID = &v
		}
		out = append(out, &e)
	}
	return out, rows.Err()
}

func (s *Store) UpdateItemReminderTimes(id int64, lastReminder, warning *time.Time) error {
	_, err := s.db.Exec(`UPDATE items SET last_reminder_at = ?, warning_posted_at = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, lastReminder, warning, id)
	return err
}

func (s *Store) UpdateItemSubproject(id int64, sub string) error {
	_, err := s.db.Exec(`UPDATE items SET subproject = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, sub, id)
	return err
}
```

- [ ] **Step 4: Run tests, verify pass**

Run: `go test ./... -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "feat(store): proposals, tickets, events tables"
```

---

## Task 7: Signal matching helpers

**Files:**
- Create: `signals.go`
- Create: `signals_test.go`

- [ ] **Step 1: Failing tests**

```go
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
```

- [ ] **Step 2: Run, verify failure**

Expected: undefined functions.

- [ ] **Step 3: Implement `signals.go`**

```go
package main

import (
	"regexp"
	"strings"
)

func IsApproveReaction(sig *SignalsConfig, emoji string) bool {
	return contains(sig.ApproveReactions, emoji)
}

func IsCancelReaction(sig *SignalsConfig, emoji string) bool {
	return contains(sig.CancelReactions, emoji)
}

func ReplyHasApprove(sig *SignalsConfig, text string) bool {
	return anyWordMatch(text, sig.ApproveReplies)
}

func ReplyHasCancel(sig *SignalsConfig, text string) bool {
	return anyWordMatch(text, sig.CancelReplies)
}

// ResolutionKeyword scans a reply for the first of: comment, new, both.
// Returns "" if none found.
func ResolutionKeyword(text string) string {
	t := strings.ToLower(text)
	for _, kw := range []string{"both", "comment", "new"} {
		if wordMatch(t, kw) {
			return kw
		}
	}
	return ""
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func anyWordMatch(text string, tokens []string) bool {
	t := strings.ToLower(text)
	for _, tok := range tokens {
		if wordMatch(t, strings.ToLower(tok)) {
			return true
		}
	}
	return false
}

var wordCache = map[string]*regexp.Regexp{}

func wordMatch(text, token string) bool {
	re, ok := wordCache[token]
	if !ok {
		// allow '+', alphanumerics, dashes; word-boundary that treats '+' and '-' as word chars
		re = regexp.MustCompile(`(^|[^\w\-+])` + regexp.QuoteMeta(token) + `($|[^\w\-+])`)
		wordCache[token] = re
	}
	return re.MatchString(text)
}
```

- [ ] **Step 4: Run, verify pass**

Run: `go test ./... -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "feat(signals): approve/cancel/resolution matching"
```

---

## Task 8: Claude shell wrapper

**Files:**
- Create: `claude.go`
- Create: `claude_test.go`

- [ ] **Step 1: Failing tests**

```go
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
```

- [ ] **Step 2: Run, verify failure**

Expected: undefined.

- [ ] **Step 3: Implement `claude.go`**

```go
package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

type ClaudeRunner struct {
	Bin     string        // path to `claude` CLI (default: "claude")
	Timeout time.Duration // default 5min
}

func NewClaudeRunner() *ClaudeRunner {
	return &ClaudeRunner{Bin: "claude", Timeout: 5 * time.Minute}
}

// Run feeds the prompt on stdin to `claude -p --output-format text` and
// returns stdout. Errors include stderr for diagnosis.
func (r *ClaudeRunner) Run(ctx context.Context, prompt string) (string, error) {
	bin := r.Bin
	if bin == "" {
		bin = "claude"
	}
	to := r.Timeout
	if to == 0 {
		to = 5 * time.Minute
	}
	cctx, cancel := context.WithTimeout(ctx, to)
	defer cancel()

	args := []string{}
	if strings.HasSuffix(bin, "claude") {
		args = append(args, "-p", "--output-format", "text")
	}
	cmd := exec.CommandContext(cctx, bin, args...)
	cmd.Stdin = strings.NewReader(prompt)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s: %w (stderr: %s)", bin, err, stderr.String())
	}
	return stdout.String(), nil
}

var (
	reJSONFence  = regexp.MustCompile("(?s)```(?:json)?\\s*(\\{.*?\\}|\\[.*?\\])\\s*```")
	reJSONObject = regexp.MustCompile(`(?s)(\{.*\}|\[.*\])`)
)

// ExtractJSON finds the first JSON object or array in text, stripped of fences.
func ExtractJSON(text string) (string, error) {
	if m := reJSONFence.FindStringSubmatch(text); len(m) == 2 {
		return strings.TrimSpace(m[1]), nil
	}
	if m := reJSONObject.FindString(text); m != "" {
		return strings.TrimSpace(m), nil
	}
	return "", errors.New("no JSON object or array found in claude output")
}
```

- [ ] **Step 4: Run tests, verify pass**

Run: `go test ./... -v`
Expected: PASS. (Tests use `/bin/echo` so they don't need real `claude` CLI.)

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "feat(claude): shell wrapper + JSON extraction"
```

---

## Task 9: Jira client — search

**Files:**
- Create: `jira.go`
- Create: `jira_test.go`

- [ ] **Step 1: Failing tests**

```go
package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestJira_Search(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/3/search" {
			t.Errorf("path: %s", r.URL.Path)
		}
		if r.Method != "POST" {
			t.Errorf("method: %s", r.Method)
		}
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		json.Unmarshal(b, &body)
		jql, _ := body["jql"].(string)
		if !strings.Contains(jql, "project in (QORK)") {
			t.Errorf("jql missing project filter: %s", jql)
		}
		if !strings.Contains(jql, `labels = "qompass"`) || !strings.Contains(jql, "labels is EMPTY") {
			t.Errorf("jql missing label filter: %s", jql)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"issues":[{"key":"QORK-1","fields":{"summary":"foo","description":"bar"}}]}`))
	}))
	defer srv.Close()

	c := &JiraClient{BaseURL: srv.URL, Email: "u", Token: "t"}
	res, err := c.Search(JiraSearchInput{
		Projects:   []string{"QORK"},
		Subproject: "qompass",
		Keywords:   []string{"foo", "bar"},
		Limit:      20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Key != "QORK-1" {
		t.Fatalf("mismatch: %+v", res)
	}
}

func TestJira_Search_NoSubproject_OnlyUnlabeled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		json.Unmarshal(b, &body)
		jql, _ := body["jql"].(string)
		if !strings.Contains(jql, "labels is EMPTY") {
			t.Errorf("expected empty-labels filter, got: %s", jql)
		}
		if strings.Contains(jql, `labels = "`) {
			t.Errorf("should not include label= filter when subproject empty: %s", jql)
		}
		w.Write([]byte(`{"issues":[]}`))
	}))
	defer srv.Close()
	c := &JiraClient{BaseURL: srv.URL, Email: "u", Token: "t"}
	_, err := c.Search(JiraSearchInput{Projects: []string{"QORK"}, Keywords: []string{"x"}, Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
}
```

- [ ] **Step 2: Run, verify failure**

Expected: types undefined.

- [ ] **Step 3: Implement search part of `jira.go`**

```go
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type JiraClient struct {
	BaseURL string
	Email   string
	Token   string
	HTTP    *http.Client
}

func NewJiraClient(baseURL, email, token string) *JiraClient {
	return &JiraClient{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Email:   email,
		Token:   token,
		HTTP:    &http.Client{Timeout: 30 * time.Second},
	}
}

type JiraSearchInput struct {
	Projects   []string
	Subproject string
	Keywords   []string
	Limit      int
}

type JiraIssue struct {
	Key    string `json:"key"`
	Fields struct {
		Summary     string `json:"summary"`
		Description any    `json:"description"`
		Labels      []string `json:"labels"`
		Status      struct {
			Name string `json:"name"`
		} `json:"status"`
	} `json:"fields"`
}

func (c *JiraClient) BuildJQL(in JiraSearchInput) string {
	var parts []string
	parts = append(parts, fmt.Sprintf("project in (%s)", strings.Join(in.Projects, ",")))
	parts = append(parts, "(statusCategory != Done OR resolved > -90d)")
	if in.Subproject != "" {
		parts = append(parts, fmt.Sprintf(`(labels = "%s" OR labels is EMPTY)`, in.Subproject))
	} else {
		parts = append(parts, "labels is EMPTY")
	}
	if len(in.Keywords) > 0 {
		quoted := make([]string, 0, len(in.Keywords))
		for _, k := range in.Keywords {
			k = strings.ReplaceAll(k, `"`, `\"`)
			quoted = append(quoted, fmt.Sprintf(`"%s"`, k))
		}
		parts = append(parts, fmt.Sprintf("text ~ (%s)", strings.Join(quoted, " OR ")))
	}
	return strings.Join(parts, " AND ") + " ORDER BY updated DESC"
}

func (c *JiraClient) Search(in JiraSearchInput) ([]JiraIssue, error) {
	if in.Limit == 0 {
		in.Limit = 20
	}
	body, _ := json.Marshal(map[string]any{
		"jql":        c.BuildJQL(in),
		"maxResults": in.Limit,
		"fields":     []string{"summary", "description", "labels", "status"},
	})
	req, _ := http.NewRequest("POST", c.BaseURL+"/rest/api/3/search", bytes.NewReader(body))
	req.SetBasicAuth(c.Email, c.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("jira search %d: %s", resp.StatusCode, string(b))
	}
	var out struct {
		Issues []JiraIssue `json:"issues"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.Issues, nil
}
```

- [ ] **Step 4: Run tests, verify pass**

Run: `go test ./... -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "feat(jira): search client with QORK/subproject JQL"
```

---

## Task 10: Jira client — create issue, add comment, add label

**Files:**
- Modify: `jira.go`
- Modify: `jira_test.go`

- [ ] **Step 1: Failing tests**

Append to `jira_test.go`:

```go
func TestJira_CreateIssue(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/3/issue" || r.Method != "POST" {
			t.Fatalf("unexpected: %s %s", r.Method, r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		var body struct {
			Fields struct {
				Project struct {
					Key string `json:"key"`
				} `json:"project"`
				Summary   string   `json:"summary"`
				IssueType struct {
					Name string `json:"name"`
				} `json:"issuetype"`
				Labels []string `json:"labels"`
			} `json:"fields"`
		}
		json.Unmarshal(b, &body)
		if body.Fields.Project.Key != "QORK" {
			t.Errorf("project: %s", body.Fields.Project.Key)
		}
		if body.Fields.IssueType.Name != "Task" {
			t.Errorf("type: %s", body.Fields.IssueType.Name)
		}
		if body.Fields.Summary != "do the thing" {
			t.Errorf("summary: %s", body.Fields.Summary)
		}
		w.WriteHeader(201)
		w.Write([]byte(`{"key":"QORK-99","self":"https://example/rest/api/3/issue/QORK-99"}`))
	}))
	defer srv.Close()
	c := &JiraClient{BaseURL: srv.URL, Email: "u", Token: "t", HTTP: http.DefaultClient}
	res, err := c.CreateIssue(CreateIssueInput{
		ProjectKey: "QORK",
		Summary:    "do the thing",
		Description: "details",
		IssueType:  "Task",
		Labels:     []string{"deferred-work", "qompass"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Key != "QORK-99" {
		t.Fatalf("key: %s", res.Key)
	}
	if !strings.Contains(res.URL, "/browse/QORK-99") {
		t.Fatalf("browse url: %s", res.URL)
	}
}

func TestJira_AddComment(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/3/issue/QORK-5/comment" || r.Method != "POST" {
			t.Fatalf("unexpected: %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(201)
		w.Write([]byte(`{"id":"1"}`))
	}))
	defer srv.Close()
	c := &JiraClient{BaseURL: srv.URL, Email: "u", Token: "t", HTTP: http.DefaultClient}
	if err := c.AddComment("QORK-5", "follow-up: stuff"); err != nil {
		t.Fatal(err)
	}
}

func TestJira_AddLabel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/3/issue/QORK-5" || r.Method != "PUT" {
			t.Fatalf("unexpected: %s %s", r.Method, r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(b), `"add":"deferred-work-followup"`) {
			t.Errorf("missing add op: %s", string(b))
		}
		w.WriteHeader(204)
	}))
	defer srv.Close()
	c := &JiraClient{BaseURL: srv.URL, Email: "u", Token: "t", HTTP: http.DefaultClient}
	if err := c.AddLabel("QORK-5", "deferred-work-followup"); err != nil {
		t.Fatal(err)
	}
}
```

- [ ] **Step 2: Run, verify failure**

Expected: methods undefined.

- [ ] **Step 3: Extend `jira.go`**

```go
type CreateIssueInput struct {
	ProjectKey  string
	Summary     string
	Description string
	IssueType   string
	Labels      []string
	Priority    string
}

type CreatedIssue struct {
	Key string
	URL string
}

func (c *JiraClient) CreateIssue(in CreateIssueInput) (*CreatedIssue, error) {
	fields := map[string]any{
		"project":   map[string]any{"key": in.ProjectKey},
		"summary":   in.Summary,
		"issuetype": map[string]any{"name": in.IssueType},
		"labels":    in.Labels,
	}
	if in.Description != "" {
		fields["description"] = adfFromText(in.Description)
	}
	if in.Priority != "" {
		fields["priority"] = map[string]any{"name": in.Priority}
	}
	body, _ := json.Marshal(map[string]any{"fields": fields})
	req, _ := http.NewRequest("POST", c.BaseURL+"/rest/api/3/issue", bytes.NewReader(body))
	req.SetBasicAuth(c.Email, c.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("create issue %d: %s", resp.StatusCode, string(b))
	}
	var out struct {
		Key string `json:"key"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &CreatedIssue{Key: out.Key, URL: c.BaseURL + "/browse/" + out.Key}, nil
}

func (c *JiraClient) AddComment(issueKey, text string) error {
	body, _ := json.Marshal(map[string]any{"body": adfFromText(text)})
	req, _ := http.NewRequest("POST", c.BaseURL+"/rest/api/3/issue/"+issueKey+"/comment", bytes.NewReader(body))
	req.SetBasicAuth(c.Email, c.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("add comment %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

func (c *JiraClient) AddLabel(issueKey, label string) error {
	body, _ := json.Marshal(map[string]any{
		"update": map[string]any{
			"labels": []map[string]any{{"add": label}},
		},
	})
	req, _ := http.NewRequest("PUT", c.BaseURL+"/rest/api/3/issue/"+issueKey, bytes.NewReader(body))
	req.SetBasicAuth(c.Email, c.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("add label %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

// adfFromText wraps a plain-text string in Atlassian Document Format, which
// the Jira Cloud v3 API requires for description and comment bodies.
func adfFromText(s string) map[string]any {
	return map[string]any{
		"type":    "doc",
		"version": 1,
		"content": []map[string]any{
			{
				"type": "paragraph",
				"content": []map[string]any{
					{"type": "text", "text": s},
				},
			},
		},
	}
}
```

- [ ] **Step 4: Run tests, verify pass**

Run: `go test ./... -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "feat(jira): create issue, add comment, add label"
```

---

## Task 11: SlackAPI interface + fake

**Files:**
- Create: `slack.go` (interface only this task)
- Create: `slack_test.go` (fake + helper)

- [ ] **Step 1: Define interface + fake**

`slack.go`:

```go
package main

import "github.com/slack-go/slack"

type SlackAPI interface {
	PostMessage(channelID string, options ...slack.MsgOption) (channel string, ts string, err error)
	AddReaction(name string, item slack.ItemRef) error
	RemoveReaction(name string, item slack.ItemRef) error
	GetConversationReplies(params *slack.GetConversationRepliesParameters) (msgs []slack.Message, hasMore bool, nextCursor string, err error)
	GetPermalink(params *slack.PermalinkParameters) (string, error)
	AuthTest() (*slack.AuthTestResponse, error)
}
```

`slack_test.go`:

```go
package main

import (
	"sync"

	"github.com/slack-go/slack"
)

type fakeSlack struct {
	mu        sync.Mutex
	botID     string
	posted    []postedMsg
	reactions []reactRef
	replies   map[string][]slack.Message // keyed by ts
}

type postedMsg struct {
	Channel string
	TS      string
	Text    string
}

type reactRef struct {
	Action  string // add|remove
	Name    string
	Channel string
	TS      string
}

func newFakeSlack(botID string) *fakeSlack {
	return &fakeSlack{botID: botID, replies: map[string][]slack.Message{}}
}

func (f *fakeSlack) PostMessage(channelID string, options ...slack.MsgOption) (string, string, error) {
	// Compose the message to extract text (best-effort: just record the option count + channel).
	f.mu.Lock()
	defer f.mu.Unlock()
	ts := generateTS(len(f.posted))
	f.posted = append(f.posted, postedMsg{Channel: channelID, TS: ts, Text: optionsText(options)})
	return channelID, ts, nil
}

func (f *fakeSlack) AddReaction(name string, item slack.ItemRef) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reactions = append(f.reactions, reactRef{Action: "add", Name: name, Channel: item.Channel, TS: item.Timestamp})
	return nil
}

func (f *fakeSlack) RemoveReaction(name string, item slack.ItemRef) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reactions = append(f.reactions, reactRef{Action: "remove", Name: name, Channel: item.Channel, TS: item.Timestamp})
	return nil
}

func (f *fakeSlack) GetConversationReplies(params *slack.GetConversationRepliesParameters) ([]slack.Message, bool, string, error) {
	return f.replies[params.Timestamp], false, "", nil
}

func (f *fakeSlack) GetPermalink(p *slack.PermalinkParameters) (string, error) {
	return "https://slack.example/archives/" + p.Channel + "/p" + p.Ts, nil
}

func (f *fakeSlack) AuthTest() (*slack.AuthTestResponse, error) {
	return &slack.AuthTestResponse{UserID: f.botID}, nil
}

func generateTS(n int) string { return "1700000000.00010" + string(rune('0'+n%10)) }
func optionsText(_ []slack.MsgOption) string { return "" }
```

- [ ] **Step 2: Sanity test**

Append to `slack_test.go`:

```go
import "testing"

func TestFakeSlack_PostAndReact(t *testing.T) {
	f := newFakeSlack("UBOT")
	_, ts, _ := f.PostMessage("C1", slack.MsgOptionText("hi", false))
	if ts == "" {
		t.Fatal("expected ts")
	}
	f.AddReaction("eyes", slack.ItemRef{Channel: "C1", Timestamp: ts})
	if len(f.reactions) != 1 || f.reactions[0].Name != "eyes" {
		t.Fatalf("reactions: %+v", f.reactions)
	}
}
```

Run: `go test ./... -v`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add -A
git commit -m "feat(slack): SlackAPI interface + fake for tests"
```

---

## Task 12: Event router skeleton + new-item handler

**Files:**
- Modify: `slack.go` (add `Router`, `HandleMessage`)
- Modify: `slack_test.go` (add tests)

- [ ] **Step 1: Failing tests**

```go
func TestRouter_NewItemInWatchedChannel(t *testing.T) {
	store := newTestStore(t)
	fake := newFakeSlack("UBOT")
	r := &Router{
		Store: store, Slack: fake, BotUserID: "UBOT",
		WatchedChannels: map[string]bool{"C1": true},
		ApprovalThreshold: 3,
	}
	r.HandleMessage(MessageEvent{
		Channel: "C1", TS: "1700.1", User: "U_AUTHOR", Text: "park this for later",
	})
	it, err := store.GetItemByTS("C1", "1700.1")
	if err != nil {
		t.Fatalf("item not stored: %v", err)
	}
	if it.Status != "collecting" {
		t.Fatalf("status: %s", it.Status)
	}
	if len(fake.reactions) != 1 || fake.reactions[0].Name != "eyes" {
		t.Fatalf("expected :eyes: reaction, got %+v", fake.reactions)
	}
}

func TestRouter_IgnoresOwnMessages(t *testing.T) {
	store := newTestStore(t)
	fake := newFakeSlack("UBOT")
	r := &Router{Store: store, Slack: fake, BotUserID: "UBOT", WatchedChannels: map[string]bool{"C1": true}}
	r.HandleMessage(MessageEvent{Channel: "C1", TS: "1700.2", User: "UBOT", Text: "I am the bot"})
	_, err := store.GetItemByTS("C1", "1700.2")
	if err != ErrNotFound {
		t.Fatal("bot messages should not be tracked")
	}
}

func TestRouter_IgnoresThreadReplyAsItem(t *testing.T) {
	store := newTestStore(t)
	fake := newFakeSlack("UBOT")
	r := &Router{Store: store, Slack: fake, BotUserID: "UBOT", WatchedChannels: map[string]bool{"C1": true}}
	r.HandleMessage(MessageEvent{Channel: "C1", TS: "1700.3", ThreadTS: "1700.1", User: "U1", Text: "reply"})
	_, err := store.GetItemByTS("C1", "1700.3")
	if err != ErrNotFound {
		t.Fatal("thread replies are not new items")
	}
}

func TestRouter_NonWatchedChannelIgnored(t *testing.T) {
	store := newTestStore(t)
	fake := newFakeSlack("UBOT")
	r := &Router{Store: store, Slack: fake, BotUserID: "UBOT", WatchedChannels: map[string]bool{"C1": true}}
	r.HandleMessage(MessageEvent{Channel: "C999", TS: "1700.4", User: "U1", Text: "park this"})
	_, err := store.GetItemByTS("C999", "1700.4")
	if err != ErrNotFound {
		t.Fatal("non-watched channels should be ignored for top-level posts")
	}
}
```

- [ ] **Step 2: Run, verify failure**

- [ ] **Step 3: Implement router**

Append to `slack.go`:

```go
type Router struct {
	Store             *Store
	Slack             SlackAPI
	BotUserID         string
	WatchedChannels   map[string]bool
	ApprovalThreshold int
	Signals           *SignalsConfig
	Projects          *ProjectsConfig
	Worker            *Worker
	Config            *Config
}

type MessageEvent struct {
	Channel  string
	TS       string
	ThreadTS string
	User     string
	Text     string
	Edited   bool
	Deleted  bool
}

func (r *Router) HandleMessage(e MessageEvent) {
	if e.User == r.BotUserID || e.User == "" {
		return
	}
	if e.ThreadTS != "" && e.ThreadTS != e.TS {
		r.handleThreadReply(e)
		return
	}
	if !r.WatchedChannels[e.Channel] {
		return
	}
	if e.Deleted {
		// future: mark cancelled
		return
	}
	it := &Item{
		SlackChannel:      e.Channel,
		SlackTS:           e.TS,
		AuthorSlackID:     e.User,
		Text:              e.Text,
		Status:            "collecting",
		ApprovalThreshold: r.ApprovalThreshold,
	}
	if err := r.Store.InsertItem(it); err != nil {
		return
	}
	r.Slack.AddReaction("eyes", slackItem(e.Channel, e.TS))
	r.Store.LogEvent(&it.ID, "created", "{}")
}

func (r *Router) handleThreadReply(e MessageEvent) {
	// Implemented in later task.
}

func slackItem(channel, ts string) slack.ItemRef {
	return slack.ItemRef{Channel: channel, Timestamp: ts}
}
```

- [ ] **Step 4: Tests pass, commit**

Run: `go test ./... -v`
Expected: 4 PASS.

```bash
git add -A
git commit -m "feat(slack): router + new-item handler"
```

---

## Task 13: Reaction add/remove → votes

**Files:**
- Modify: `slack.go`
- Modify: `slack_test.go`

- [ ] **Step 1: Failing tests**

```go
func TestRouter_ReactionAddedCountsAsVote(t *testing.T) {
	store := newTestStore(t)
	fake := newFakeSlack("UBOT")
	sig := &SignalsConfig{ApproveReactions: []string{"white_check_mark"}}
	r := &Router{Store: store, Slack: fake, BotUserID: "UBOT", WatchedChannels: map[string]bool{"C1": true}, Signals: sig, ApprovalThreshold: 3}
	r.HandleMessage(MessageEvent{Channel: "C1", TS: "1700.1", User: "U_AUTHOR", Text: "x"})
	r.HandleReactionAdded(ReactionEvent{User: "U2", Channel: "C1", TS: "1700.1", Name: "white_check_mark"})
	it, _ := store.GetItemByTS("C1", "1700.1")
	n, _ := store.CountVotes(it.ID)
	if n != 1 {
		t.Fatalf("expected 1 vote, got %d", n)
	}
}

func TestRouter_AuthorReactionExcluded(t *testing.T) {
	store := newTestStore(t)
	fake := newFakeSlack("UBOT")
	sig := &SignalsConfig{ApproveReactions: []string{"white_check_mark"}}
	r := &Router{Store: store, Slack: fake, BotUserID: "UBOT", WatchedChannels: map[string]bool{"C1": true}, Signals: sig, ApprovalThreshold: 3}
	r.HandleMessage(MessageEvent{Channel: "C1", TS: "1700.1", User: "U_AUTHOR", Text: "x"})
	r.HandleReactionAdded(ReactionEvent{User: "U_AUTHOR", Channel: "C1", TS: "1700.1", Name: "white_check_mark"})
	it, _ := store.GetItemByTS("C1", "1700.1")
	n, _ := store.CountVotes(it.ID)
	if n != 0 {
		t.Fatalf("expected author vote excluded, got %d", n)
	}
}

func TestRouter_BotReactionExcluded(t *testing.T) {
	store := newTestStore(t)
	fake := newFakeSlack("UBOT")
	sig := &SignalsConfig{ApproveReactions: []string{"white_check_mark"}}
	r := &Router{Store: store, Slack: fake, BotUserID: "UBOT", WatchedChannels: map[string]bool{"C1": true}, Signals: sig, ApprovalThreshold: 3}
	r.HandleMessage(MessageEvent{Channel: "C1", TS: "1700.1", User: "U_AUTHOR", Text: "x"})
	r.HandleReactionAdded(ReactionEvent{User: "UBOT", Channel: "C1", TS: "1700.1", Name: "white_check_mark"})
	it, _ := store.GetItemByTS("C1", "1700.1")
	n, _ := store.CountVotes(it.ID)
	if n != 0 {
		t.Fatalf("expected bot vote excluded, got %d", n)
	}
}

func TestRouter_ReactionRemovedDecrements(t *testing.T) {
	store := newTestStore(t)
	fake := newFakeSlack("UBOT")
	sig := &SignalsConfig{ApproveReactions: []string{"white_check_mark"}}
	r := &Router{Store: store, Slack: fake, BotUserID: "UBOT", WatchedChannels: map[string]bool{"C1": true}, Signals: sig, ApprovalThreshold: 3}
	r.HandleMessage(MessageEvent{Channel: "C1", TS: "1700.1", User: "U_AUTHOR", Text: "x"})
	r.HandleReactionAdded(ReactionEvent{User: "U2", Channel: "C1", TS: "1700.1", Name: "white_check_mark"})
	r.HandleReactionRemoved(ReactionEvent{User: "U2", Channel: "C1", TS: "1700.1", Name: "white_check_mark"})
	it, _ := store.GetItemByTS("C1", "1700.1")
	n, _ := store.CountVotes(it.ID)
	if n != 0 {
		t.Fatalf("expected 0 votes after removal, got %d", n)
	}
}

func TestRouter_CancelReactionMarksCancelled(t *testing.T) {
	store := newTestStore(t)
	fake := newFakeSlack("UBOT")
	sig := &SignalsConfig{CancelReactions: []string{"x"}}
	r := &Router{Store: store, Slack: fake, BotUserID: "UBOT", WatchedChannels: map[string]bool{"C1": true}, Signals: sig, ApprovalThreshold: 3}
	r.HandleMessage(MessageEvent{Channel: "C1", TS: "1700.1", User: "U_AUTHOR", Text: "x"})
	r.HandleReactionAdded(ReactionEvent{User: "U2", Channel: "C1", TS: "1700.1", Name: "x"})
	it, _ := store.GetItemByTS("C1", "1700.1")
	if it.Status != "cancelled" {
		t.Fatalf("expected cancelled, got %s", it.Status)
	}
}
```

- [ ] **Step 2: Run, verify failure**

- [ ] **Step 3: Implement**

```go
type ReactionEvent struct {
	User    string
	Channel string
	TS      string
	Name    string
}

func (r *Router) HandleReactionAdded(e ReactionEvent) {
	if e.User == r.BotUserID {
		return
	}
	it, err := r.Store.GetItemByTS(e.Channel, e.TS)
	if err != nil {
		// could be a proposal reaction — handled in proposal-approval task
		r.handleProposalReaction(e, /*add=*/ true)
		return
	}
	if isTerminal(it.Status) {
		return
	}
	if IsCancelReaction(r.Signals, e.Name) {
		r.Store.UpdateItemStatus(it.ID, "cancelled")
		r.Store.LogEvent(&it.ID, "cancel", `{"by":"`+e.User+`","via":"reaction"}`)
		r.Slack.AddReaction("wastebasket", slackItem(e.Channel, e.TS))
		return
	}
	if !IsApproveReaction(r.Signals, e.Name) {
		return
	}
	if e.User == it.AuthorSlackID {
		return
	}
	r.Store.UpsertVote(it.ID, e.User, "reaction", e.Name)
	r.Store.LogEvent(&it.ID, "vote", `{"user":"`+e.User+`","source":"reaction","signal":"`+e.Name+`"}`)
	r.maybeAdvanceToProposing(it)
}

func (r *Router) HandleReactionRemoved(e ReactionEvent) {
	if e.User == r.BotUserID {
		return
	}
	it, err := r.Store.GetItemByTS(e.Channel, e.TS)
	if err != nil {
		return
	}
	if isTerminal(it.Status) {
		return
	}
	if !IsApproveReaction(r.Signals, e.Name) {
		return
	}
	r.Store.RemoveVote(it.ID, e.User)
	r.Store.LogEvent(&it.ID, "vote_removed", `{"user":"`+e.User+`"}`)
}

func (r *Router) maybeAdvanceToProposing(it *Item) {
	n, _ := r.Store.CountVotes(it.ID)
	if n < it.ApprovalThreshold {
		return
	}
	if it.Status != "collecting" {
		return
	}
	r.Store.UpdateItemStatus(it.ID, "proposing")
	r.Store.LogEvent(&it.ID, "advanced", `{"reason":"threshold"}`)
	if r.Worker != nil {
		r.Worker.Submit(ProposeJob{ItemID: it.ID})
	}
}

func (r *Router) handleProposalReaction(e ReactionEvent, add bool) {
	// Implemented in proposal-approval task.
}

func isTerminal(status string) bool {
	switch status {
	case "ticketed", "commented_on_existing", "cancelled", "archived":
		return true
	}
	return false
}
```

- [ ] **Step 4: Tests pass, commit**

Run: `go test ./... -v`
Expected: PASS.

```bash
git add -A
git commit -m "feat(slack): reaction handlers — vote upsert/remove + cancel"
```

---

## Task 14: Thread reply handler — votes via keywords + command stub

**Files:**
- Modify: `slack.go`
- Modify: `slack_test.go`

- [ ] **Step 1: Failing tests**

```go
func TestRouter_ReplyApproveKeywordCountsVote(t *testing.T) {
	store := newTestStore(t)
	fake := newFakeSlack("UBOT")
	sig := &SignalsConfig{ApproveReplies: []string{"lgtm", "+1", "approve"}}
	r := &Router{Store: store, Slack: fake, BotUserID: "UBOT", WatchedChannels: map[string]bool{"C1": true}, Signals: sig, ApprovalThreshold: 3}
	r.HandleMessage(MessageEvent{Channel: "C1", TS: "1700.1", User: "U_AUTHOR", Text: "x"})
	r.HandleMessage(MessageEvent{Channel: "C1", TS: "1700.2", ThreadTS: "1700.1", User: "U2", Text: "LGTM"})
	it, _ := store.GetItemByTS("C1", "1700.1")
	n, _ := store.CountVotes(it.ID)
	if n != 1 {
		t.Fatalf("expected 1 vote, got %d", n)
	}
}

func TestRouter_ReplyCancelKeyword(t *testing.T) {
	store := newTestStore(t)
	fake := newFakeSlack("UBOT")
	sig := &SignalsConfig{CancelReplies: []string{"cancel"}}
	r := &Router{Store: store, Slack: fake, BotUserID: "UBOT", WatchedChannels: map[string]bool{"C1": true}, Signals: sig, ApprovalThreshold: 3}
	r.HandleMessage(MessageEvent{Channel: "C1", TS: "1700.1", User: "U_AUTHOR", Text: "x"})
	r.HandleMessage(MessageEvent{Channel: "C1", TS: "1700.2", ThreadTS: "1700.1", User: "U2", Text: "<@UBOT> cancel"})
	it, _ := store.GetItemByTS("C1", "1700.1")
	if it.Status != "cancelled" {
		t.Fatalf("expected cancelled, got %s", it.Status)
	}
}

func TestRouter_BotMentionDispatch(t *testing.T) {
	store := newTestStore(t)
	fake := newFakeSlack("UBOT")
	r := &Router{Store: store, Slack: fake, BotUserID: "UBOT", WatchedChannels: map[string]bool{"C1": true}, Signals: &SignalsConfig{}, ApprovalThreshold: 3}
	r.HandleMessage(MessageEvent{Channel: "C1", TS: "1700.1", User: "U_AUTHOR", Text: "x"})
	r.HandleMessage(MessageEvent{Channel: "C1", TS: "1700.2", ThreadTS: "1700.1", User: "U2", Text: "<@UBOT> status"})
	// Bot should have posted at least one reply.
	if len(fake.posted) < 1 {
		t.Fatal("expected bot to post status reply")
	}
}
```

- [ ] **Step 2: Run, verify failure**

- [ ] **Step 3: Implement `handleThreadReply` + command dispatch stub**

Replace the empty `handleThreadReply` in `slack.go`:

```go
func (r *Router) handleThreadReply(e MessageEvent) {
	parent, err := r.Store.GetItemByTS(e.Channel, e.ThreadTS)
	if err != nil {
		return
	}
	if isTerminal(parent.Status) {
		return
	}
	text := strings.ToLower(e.Text)

	if ReplyHasCancel(r.Signals, text) || r.botMentioned(e.Text) && strings.Contains(text, "cancel") {
		r.Store.UpdateItemStatus(parent.ID, "cancelled")
		r.Store.LogEvent(&parent.ID, "cancel", `{"by":"`+e.User+`","via":"reply"}`)
		r.Slack.AddReaction("wastebasket", slackItem(parent.SlackChannel, parent.SlackTS))
		return
	}

	if r.botMentioned(e.Text) {
		r.dispatchCommand(parent, e)
		return
	}

	// Resolution keywords only apply when latest proposal is awaiting resolution.
	if p, err := r.Store.GetLatestProposal(parent.ID); err == nil && p.Status == "awaiting_resolution" {
		if kw := ResolutionKeyword(text); kw != "" {
			r.handleResolution(parent, p, kw, e)
			return
		}
	}

	if ReplyHasApprove(r.Signals, text) {
		if e.User == parent.AuthorSlackID {
			return
		}
		r.Store.UpsertVote(parent.ID, e.User, "reply", "keyword")
		r.Store.LogEvent(&parent.ID, "vote", `{"user":"`+e.User+`","source":"reply"}`)
		// If this is a proposal-thread vote on the proposal message itself,
		// handle that separately. Otherwise it's a vote on the item.
		r.maybeAdvanceToProposing(parent)
	}
}

func (r *Router) botMentioned(text string) bool {
	return strings.Contains(text, "<@"+r.BotUserID+">")
}

func (r *Router) dispatchCommand(it *Item, e MessageEvent) {
	cmd := normalizeCommand(e.Text, r.BotUserID)
	switch {
	case cmd == "status":
		r.cmdStatus(it, e)
	case cmd == "help":
		r.cmdHelp(it, e)
	case cmd == "cancel":
		r.cmdCancel(it, e)
	case cmd == "file now":
		r.cmdFileNow(it, e)
	case cmd == "regen":
		r.cmdRegen(it, e)
	case cmd == "search":
		r.cmdSearch(it, e)
	case strings.HasPrefix(cmd, "project:"):
		r.cmdProject(it, e, strings.TrimSpace(strings.TrimPrefix(cmd, "project:")))
	case strings.HasPrefix(cmd, "priority:"):
		r.cmdPriority(it, e, strings.TrimSpace(strings.TrimPrefix(cmd, "priority:")))
	default:
		r.cmdFreeform(it, e, cmd)
	}
}

// normalizeCommand strips the bot mention and lowercases the remainder.
func normalizeCommand(text, botID string) string {
	t := strings.ReplaceAll(text, "<@"+botID+">", "")
	return strings.ToLower(strings.TrimSpace(t))
}

// Stubs — real implementations land in later tasks.
func (r *Router) cmdStatus(it *Item, e MessageEvent) {
	n, _ := r.Store.CountVotes(it.ID)
	msg := fmt.Sprintf("Status: *%s* — %d/%d approvals", it.Status, n, it.ApprovalThreshold)
	r.Slack.PostMessage(e.Channel, slack.MsgOptionText(msg, false), slack.MsgOptionTS(it.SlackTS))
}
func (r *Router) cmdHelp(it *Item, e MessageEvent)               { r.postHelp(e) }
func (r *Router) cmdCancel(it *Item, e MessageEvent)             { r.Store.UpdateItemStatus(it.ID, "cancelled"); r.Slack.AddReaction("wastebasket", slackItem(it.SlackChannel, it.SlackTS)) }
func (r *Router) cmdFileNow(it *Item, e MessageEvent)            { /* Task 23 */ }
func (r *Router) cmdRegen(it *Item, e MessageEvent)              { /* Task 24 */ }
func (r *Router) cmdSearch(it *Item, e MessageEvent)             { /* Task 24 */ }
func (r *Router) cmdProject(it *Item, e MessageEvent, v string)  { r.Store.UpdateItemSubproject(it.ID, v); r.Slack.AddReaction("white_check_mark", slackItem(e.Channel, e.TS)) }
func (r *Router) cmdPriority(it *Item, e MessageEvent, v string) { /* Task 24 */ }
func (r *Router) cmdFreeform(it *Item, e MessageEvent, q string) { /* Task 25 */ }

func (r *Router) postHelp(e MessageEvent) {
	help := "*Commands:* `status`, `cancel`, `regen`, `project: <name>`, `priority: <low|med|high>`, `file now`, `search`, `help`, or any free-form question."
	r.Slack.PostMessage(e.Channel, slack.MsgOptionText(help, false), slack.MsgOptionTS(e.ThreadTS))
}

func (r *Router) handleResolution(it *Item, p *Proposal, keyword string, e MessageEvent) {
	// Implemented in Task 21.
}
```

Add imports (`fmt`, `strings`, `github.com/slack-go/slack`).

- [ ] **Step 4: Tests pass, commit**

Run: `go test ./... -v`
Expected: PASS.

```bash
git add -A
git commit -m "feat(slack): thread reply handler with vote + command dispatch stub"
```

---

## Task 15: app_mention handler (invited-channel + thread variants)

**Files:**
- Modify: `slack.go`
- Modify: `slack_test.go`

- [ ] **Step 1: Failing tests**

```go
func TestRouter_AppMentionInNonWatchedChannelCreatesItem(t *testing.T) {
	store := newTestStore(t)
	fake := newFakeSlack("UBOT")
	r := &Router{Store: store, Slack: fake, BotUserID: "UBOT", WatchedChannels: map[string]bool{"C1": true}, Signals: &SignalsConfig{}, ApprovalThreshold: 3}
	r.HandleAppMention(MessageEvent{Channel: "C999", TS: "1700.5", User: "U2", Text: "<@UBOT> track this work"})
	if _, err := store.GetItemByTS("C999", "1700.5"); err != nil {
		t.Fatalf("expected item, got: %v", err)
	}
}

func TestRouter_AppMentionInThreadDispatchesCommand(t *testing.T) {
	store := newTestStore(t)
	fake := newFakeSlack("UBOT")
	r := &Router{Store: store, Slack: fake, BotUserID: "UBOT", WatchedChannels: map[string]bool{"C1": true}, Signals: &SignalsConfig{}, ApprovalThreshold: 3}
	r.HandleMessage(MessageEvent{Channel: "C1", TS: "1700.1", User: "U_AUTHOR", Text: "x"})
	r.HandleAppMention(MessageEvent{Channel: "C1", TS: "1700.2", ThreadTS: "1700.1", User: "U2", Text: "<@UBOT> status"})
	if len(fake.posted) == 0 {
		t.Fatal("expected status reply")
	}
}
```

- [ ] **Step 2: Run, verify failure**

- [ ] **Step 3: Implement**

Append to `slack.go`:

```go
func (r *Router) HandleAppMention(e MessageEvent) {
	if e.User == r.BotUserID || e.User == "" {
		return
	}
	if e.ThreadTS != "" && e.ThreadTS != e.TS {
		// route as thread reply
		r.handleThreadReply(e)
		return
	}
	// Top-level @mention in a non-watched channel — create item.
	if _, err := r.Store.GetItemByTS(e.Channel, e.TS); err == nil {
		return // already tracked
	}
	it := &Item{
		SlackChannel:      e.Channel,
		SlackTS:           e.TS,
		AuthorSlackID:     e.User,
		Text:              e.Text,
		Status:            "collecting",
		ApprovalThreshold: r.ApprovalThreshold,
	}
	if err := r.Store.InsertItem(it); err != nil {
		return
	}
	r.Slack.AddReaction("eyes", slackItem(e.Channel, e.TS))
	r.Store.LogEvent(&it.ID, "created", `{"via":"app_mention"}`)
}
```

- [ ] **Step 4: Tests pass, commit**

Run: `go test ./... -v`
Expected: PASS.

```bash
git add -A
git commit -m "feat(slack): app_mention handler for invited channels"
```

---

## Task 16: message_changed / message_deleted handlers

**Files:**
- Modify: `slack.go`
- Modify: `slack_test.go`

- [ ] **Step 1: Failing tests**

```go
func TestRouter_MessageDeletedCancels(t *testing.T) {
	store := newTestStore(t)
	fake := newFakeSlack("UBOT")
	r := &Router{Store: store, Slack: fake, BotUserID: "UBOT", WatchedChannels: map[string]bool{"C1": true}, Signals: &SignalsConfig{}, ApprovalThreshold: 3}
	r.HandleMessage(MessageEvent{Channel: "C1", TS: "1700.1", User: "U_AUTHOR", Text: "x"})
	r.HandleMessageDeleted(MessageEvent{Channel: "C1", TS: "1700.1"})
	it, _ := store.GetItemByTS("C1", "1700.1")
	if it.Status != "cancelled" {
		t.Fatalf("expected cancelled, got %s", it.Status)
	}
}

func TestRouter_MessageEditedUpdatesText(t *testing.T) {
	store := newTestStore(t)
	fake := newFakeSlack("UBOT")
	r := &Router{Store: store, Slack: fake, BotUserID: "UBOT", WatchedChannels: map[string]bool{"C1": true}, Signals: &SignalsConfig{}, ApprovalThreshold: 3}
	r.HandleMessage(MessageEvent{Channel: "C1", TS: "1700.1", User: "U_AUTHOR", Text: "original"})
	r.HandleMessageChanged(MessageEvent{Channel: "C1", TS: "1700.1", Text: "edited text", User: "U_AUTHOR"})
	it, _ := store.GetItemByTS("C1", "1700.1")
	if it.Text != "edited text" {
		t.Fatalf("text not updated: %s", it.Text)
	}
}
```

- [ ] **Step 2: Run, verify failure**

- [ ] **Step 3: Implement**

```go
func (r *Router) HandleMessageDeleted(e MessageEvent) {
	it, err := r.Store.GetItemByTS(e.Channel, e.TS)
	if err != nil {
		return
	}
	if isTerminal(it.Status) {
		return
	}
	r.Store.UpdateItemStatus(it.ID, "cancelled")
	r.Store.LogEvent(&it.ID, "cancel", `{"reason":"message_deleted"}`)
}

func (r *Router) HandleMessageChanged(e MessageEvent) {
	it, err := r.Store.GetItemByTS(e.Channel, e.TS)
	if err != nil {
		return
	}
	if isTerminal(it.Status) {
		return
	}
	_, _ = r.Store.db.Exec(`UPDATE items SET text = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, e.Text, it.ID)
	r.Store.LogEvent(&it.ID, "edited", "{}")
}
```

Note: this leaks `db` access from outside `store.go`. Better — add a method `UpdateItemText` to `store.go`:

```go
func (s *Store) UpdateItemText(id int64, text string) error {
	_, err := s.db.Exec(`UPDATE items SET text = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, text, id)
	return err
}
```

Use `r.Store.UpdateItemText(it.ID, e.Text)` instead.

- [ ] **Step 4: Tests pass, commit**

Run: `go test ./... -v`
Expected: PASS.

```bash
git add -A
git commit -m "feat(slack): message_changed/deleted handlers"
```

---

## Task 17: Subproject detection

**Files:**
- Create: `propose.go`
- Create: `propose_test.go`

- [ ] **Step 1: Failing tests**

```go
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
```

- [ ] **Step 2: Run, verify failure**

- [ ] **Step 3: Implement**

```go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type claudeAPI interface {
	Run(ctx context.Context, prompt string) (string, error)
}

func detectSubprojectByKeyword(cfg *ProjectsConfig, text string) string {
	low := strings.ToLower(text)
	for _, sub := range cfg.Subprojects {
		if strings.Contains(low, strings.ToLower(sub)) {
			return sub
		}
	}
	return ""
}

func detectSubproject(ctx context.Context, cfg *ProjectsConfig, c claudeAPI, text string) (string, error) {
	if v := detectSubprojectByKeyword(cfg, text); v != "" {
		return v, nil
	}
	prompt := fmt.Sprintf(`You are categorizing a piece of work into one of these sub-projects.

Sub-projects: %s

Text: %q

Return JSON: {"subproject": "<one of the sub-projects or empty string>"}
Only return the JSON, no other text.`, strings.Join(cfg.Subprojects, ", "), text)
	out, err := c.Run(ctx, prompt)
	if err != nil {
		return "", err
	}
	jsonStr, err := ExtractJSON(out)
	if err != nil {
		return "", nil // fail soft — treat as none
	}
	var parsed struct {
		Subproject string `json:"subproject"`
	}
	if err := json.Unmarshal([]byte(jsonStr), &parsed); err != nil {
		return "", nil
	}
	if !containsLower(cfg.Subprojects, parsed.Subproject) {
		return "", nil
	}
	return parsed.Subproject, nil
}

func containsLower(list []string, v string) bool {
	v = strings.ToLower(v)
	for _, x := range list {
		if strings.ToLower(x) == v {
			return true
		}
	}
	return false
}
```

- [ ] **Step 4: Tests pass, commit**

Run: `go test ./... -v`
Expected: PASS.

```bash
git add -A
git commit -m "feat(propose): sub-project detection (keyword + claude fallback)"
```

---

## Task 18: Relevance ranking + branch decision

**Files:**
- Modify: `propose.go`
- Modify: `propose_test.go`

- [ ] **Step 1: Failing tests**

```go
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

func TestDecideBranch(t *testing.T) {
	cases := []struct {
		name      string
		verdicts  []string
		want      string
		existing  string
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
```

- [ ] **Step 2: Run, verify failure**

- [ ] **Step 3: Implement**

Append to `propose.go`:

```go
type RelatedTicket struct {
	Key     string `json:"key"`
	Summary string `json:"summary,omitempty"`
	Verdict string `json:"verdict"` // encompassed|referenced|unrelated
}

func classifyRelatedTickets(ctx context.Context, c claudeAPI, workText string, issues []JiraIssue) ([]RelatedTicket, error) {
	if len(issues) == 0 {
		return nil, nil
	}
	summaries := make([]map[string]any, len(issues))
	for i, iss := range issues {
		summaries[i] = map[string]any{"key": iss.Key, "summary": iss.Fields.Summary}
	}
	payload, _ := json.Marshal(summaries)
	prompt := fmt.Sprintf(`Classify each Jira ticket relative to this deferred-work item.

WORK ITEM:
%s

TICKETS:
%s

For each ticket, return a JSON array of objects: {"key": "...", "verdict": "encompassed"|"referenced"|"unrelated"}
- "encompassed": this ticket already covers the same scope of work; filing a new ticket would duplicate.
- "referenced": this ticket touches related code/concepts but is not the same work.
- "unrelated": no meaningful overlap.
Only return the JSON array, no other text.`, workText, string(payload))
	out, err := c.Run(ctx, prompt)
	if err != nil {
		return nil, err
	}
	jsonStr, err := ExtractJSON(out)
	if err != nil {
		return nil, err
	}
	var res []RelatedTicket
	if err := json.Unmarshal([]byte(jsonStr), &res); err != nil {
		return nil, err
	}
	return res, nil
}

// DecideBranch picks the proposal branch from related-ticket classifications.
// Returns (branch, existingKey). existingKey is set only when branch == "awaiting_resolution".
func DecideBranch(rels []RelatedTicket) (string, string) {
	for _, r := range rels {
		if r.Verdict == "encompassed" {
			return "awaiting_resolution", r.Key
		}
	}
	return "new", ""
}
```

- [ ] **Step 4: Tests pass, commit**

Run: `go test ./... -v`
Expected: PASS.

```bash
git add -A
git commit -m "feat(propose): related-ticket classifier + branch decision"
```

---

## Task 19: Ticket drafting

**Files:**
- Modify: `propose.go`
- Modify: `propose_test.go`

- [ ] **Step 1: Failing test**

```go
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
```

- [ ] **Step 2: Run, verify failure**

- [ ] **Step 3: Implement**

```go
type DraftInput struct {
	Text         string
	Thread       []string
	Subproject   string
	PriorityOver string
	Permalink    string
}

type Draft struct {
	Summary     string   `json:"summary"`
	Description string   `json:"description"`
	IssueType   string   `json:"issue_type"`
	Labels      []string `json:"labels"`
	Priority    string   `json:"priority"`
}

func DraftTicket(ctx context.Context, c claudeAPI, in DraftInput) (*Draft, error) {
	prompt := fmt.Sprintf(`You are drafting a Jira ticket from a Slack deferred-work item.

Sub-project label: %q
Original message:
%s

Thread comments:
%s

Slack permalink: %s

Return JSON:
{
  "summary": "<one-line, imperative voice, <=120 chars>",
  "description": "<multi-paragraph description, include original message verbatim, then synthesized context from comments, then a final line with the Slack permalink>",
  "labels": ["deferred-work"%s],
  "priority": "Low|Medium|High"
}
Only return the JSON, no other text.`,
		in.Subproject,
		in.Text,
		strings.Join(in.Thread, "\n---\n"),
		in.Permalink,
		labelHint(in.Subproject),
	)
	out, err := c.Run(ctx, prompt)
	if err != nil {
		return nil, err
	}
	js, err := ExtractJSON(out)
	if err != nil {
		return nil, err
	}
	var d Draft
	if err := json.Unmarshal([]byte(js), &d); err != nil {
		return nil, err
	}
	d.IssueType = "Task"
	if in.PriorityOver != "" {
		d.Priority = in.PriorityOver
	}
	if d.Priority == "" {
		d.Priority = "Medium"
	}
	// Ensure deferred-work + subproject labels are present.
	d.Labels = ensureLabels(d.Labels, "deferred-work", in.Subproject)
	return &d, nil
}

func labelHint(sub string) string {
	if sub == "" {
		return ""
	}
	return `, "` + sub + `"`
}

func ensureLabels(labels []string, required ...string) []string {
	seen := map[string]bool{}
	for _, l := range labels {
		seen[l] = true
	}
	out := labels
	for _, r := range required {
		if r == "" || seen[r] {
			continue
		}
		out = append(out, r)
		seen[r] = true
	}
	return out
}
```

- [ ] **Step 4: Tests pass, commit**

```bash
git add -A
git commit -m "feat(propose): ticket drafting via claude"
```

---

## Task 20: Proposal posting (Slack message builder)

**Files:**
- Modify: `propose.go`
- Modify: `propose_test.go`

- [ ] **Step 1: Failing test**

```go
func TestRenderProposalMessage_NewBranch(t *testing.T) {
	d := &Draft{
		Summary:     "Fix flaky test",
		Description: "long...",
		IssueType:   "Task",
		Labels:      []string{"deferred-work", "qompass"},
		Priority:    "Medium",
	}
	out := RenderProposalMessage(d, []RelatedTicket{}, "new", "", false)
	if !strings.Contains(out, "Fix flaky test") || !strings.Contains(out, "Task") || !strings.Contains(out, "Medium") {
		t.Fatalf("missing fields:\n%s", out)
	}
	if !strings.Contains(out, "approve signal to file") {
		t.Fatalf("missing footer:\n%s", out)
	}
}

func TestRenderProposalMessage_EncompassedBranch(t *testing.T) {
	out := RenderProposalMessage(nil, []RelatedTicket{{Key: "QORK-5", Verdict: "encompassed"}}, "awaiting_resolution", "QORK-5", false)
	if !strings.Contains(out, "QORK-5") || !strings.Contains(out, "encompassed") {
		t.Fatalf("missing encompassed banner: %s", out)
	}
	if !strings.Contains(out, "comment") || !strings.Contains(out, "new") || !strings.Contains(out, "both") {
		t.Fatalf("missing resolution options: %s", out)
	}
}

func TestRenderProposalMessage_TTLBanner(t *testing.T) {
	d := &Draft{Summary: "x", IssueType: "Task", Priority: "Low"}
	out := RenderProposalMessage(d, nil, "new", "", true)
	if !strings.Contains(out, "no response") {
		t.Fatalf("missing TTL banner: %s", out)
	}
}
```

- [ ] **Step 2: Run, verify failure**

- [ ] **Step 3: Implement**

```go
func RenderProposalMessage(d *Draft, rels []RelatedTicket, branch, existingKey string, ttlTriggered bool) string {
	var b strings.Builder
	if ttlTriggered {
		b.WriteString(":warning: *No team response in 3 days — proposing anyway*\n\n")
	}
	if branch == "awaiting_resolution" {
		fmt.Fprintf(&b, "*Existing ticket may cover this:* <%s|%s> (encompassed).\n\n", existingKey, existingKey)
		b.WriteString("Reply `comment` to add a follow-up to the existing ticket, `new` to file a fresh one, or `both` for both.\n")
		return b.String()
	}
	if d != nil {
		fmt.Fprintf(&b, "*Proposed ticket — %s (%s)*\n", d.IssueType, d.Priority)
		fmt.Fprintf(&b, "*Summary:* %s\n", d.Summary)
		if d.Description != "" {
			desc := d.Description
			if len(desc) > 600 {
				desc = desc[:600] + "…"
			}
			fmt.Fprintf(&b, "*Description preview:*\n```\n%s\n```\n", desc)
		}
		fmt.Fprintf(&b, "*Labels:* %s\n", strings.Join(d.Labels, ", "))
	}
	if len(rels) > 0 {
		b.WriteString("\n*Related tickets:*\n")
		for _, r := range rels {
			if r.Verdict == "unrelated" {
				continue
			}
			fmt.Fprintf(&b, "• <%s|%s> — %s\n", r.Key, r.Key, r.Verdict)
		}
	}
	b.WriteString("\n_React with any approve signal to file. `@bot regen` to revise._")
	return b.String()
}
```

- [ ] **Step 4: Tests pass, commit**

```bash
git add -A
git commit -m "feat(propose): proposal message renderer"
```

---

## Task 21: Resolution loop (comment / new / both)

**Files:**
- Modify: `slack.go` (implement `handleResolution`)
- Modify: `slack_test.go`

- [ ] **Step 1: Failing tests**

```go
func TestRouter_ResolutionNewKeyword(t *testing.T) {
	store := newTestStore(t)
	fake := newFakeSlack("UBOT")
	r := &Router{Store: store, Slack: fake, BotUserID: "UBOT", WatchedChannels: map[string]bool{"C1": true}, Signals: &SignalsConfig{}, ApprovalThreshold: 3}
	r.HandleMessage(MessageEvent{Channel: "C1", TS: "1700.1", User: "U1", Text: "x"})
	it, _ := store.GetItemByTS("C1", "1700.1")
	store.UpdateItemStatus(it.ID, "proposed")
	p := &Proposal{ItemID: it.ID, SlackTS: "1700.2", DraftJSON: `{"summary":"s"}`, RelatedTicketsJSON: "[]", Branch: "awaiting_resolution", ExistingTicketKey: "QORK-5", Status: "awaiting_resolution"}
	store.InsertProposal(p)
	r.HandleMessage(MessageEvent{Channel: "C1", TS: "1700.3", ThreadTS: "1700.1", User: "U2", Text: "let's file as new"})
	got, _ := store.GetLatestProposal(it.ID)
	if got.Branch != "new" {
		t.Fatalf("branch: %s", got.Branch)
	}
}

func TestRouter_ResolutionCommentKeyword(t *testing.T) {
	store := newTestStore(t)
	fake := newFakeSlack("UBOT")
	r := &Router{Store: store, Slack: fake, BotUserID: "UBOT", WatchedChannels: map[string]bool{"C1": true}, Signals: &SignalsConfig{}, ApprovalThreshold: 3}
	r.HandleMessage(MessageEvent{Channel: "C1", TS: "1700.1", User: "U1", Text: "x"})
	it, _ := store.GetItemByTS("C1", "1700.1")
	store.UpdateItemStatus(it.ID, "proposed")
	p := &Proposal{ItemID: it.ID, SlackTS: "1700.2", DraftJSON: "{}", RelatedTicketsJSON: "[]", Branch: "awaiting_resolution", ExistingTicketKey: "QORK-5", Status: "awaiting_resolution"}
	store.InsertProposal(p)
	r.HandleMessage(MessageEvent{Channel: "C1", TS: "1700.3", ThreadTS: "1700.1", User: "U2", Text: "comment please"})
	got, _ := store.GetLatestProposal(it.ID)
	if got.Branch != "comment_on_existing" {
		t.Fatalf("branch: %s", got.Branch)
	}
	if got.Status != "draft" {
		t.Fatalf("status: %s", got.Status)
	}
}
```

- [ ] **Step 2: Run, verify failure**

- [ ] **Step 3: Implement**

Replace the empty `handleResolution` in `slack.go`:

```go
func (r *Router) handleResolution(it *Item, p *Proposal, keyword string, e MessageEvent) {
	var branch string
	switch keyword {
	case "new":
		branch = "new"
	case "comment":
		branch = "comment_on_existing"
	case "both":
		branch = "both"
	default:
		return
	}
	_, _ = r.Store.db.Exec(`UPDATE proposals SET branch = ?, status = 'draft', updated_at = CURRENT_TIMESTAMP WHERE id = ?`, branch, p.ID)
	r.Store.LogEvent(&it.ID, "resolution", `{"branch":"`+branch+`"}`)
	r.Slack.PostMessage(e.Channel,
		slack.MsgOptionText(fmt.Sprintf("Resolution: *%s*. React with any approve signal to file.", branch), false),
		slack.MsgOptionTS(it.SlackTS))
}
```

(Note: refactor `db.Exec` access through a new `Store.UpdateProposalBranch(id int64, branch, status string) error` method to keep DB queries centralized.)

- [ ] **Step 4: Add store method**

Append to `store.go`:

```go
func (s *Store) UpdateProposalBranch(id int64, branch, status string) error {
	_, err := s.db.Exec(`UPDATE proposals SET branch = ?, status = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, branch, status, id)
	return err
}
```

Use it: `r.Store.UpdateProposalBranch(p.ID, branch, "draft")`.

- [ ] **Step 5: Tests pass, commit**

```bash
git add -A
git commit -m "feat(slack): resolution-keyword handler (comment/new/both)"
```

---

## Task 22: Filing — approval on proposal → create / comment / both

**Files:**
- Modify: `slack.go` (implement `handleProposalReaction`)
- Modify: `propose.go` (add `FileProposal` orchestrator)
- Modify: `slack_test.go`, `propose_test.go`

- [ ] **Step 1: Failing tests**

In `propose_test.go`:

```go
type fakeJira struct {
	createdKey string
	createdURL string
	comments   []struct{ Key, Text string }
	labels     []struct{ Key, Label string }
}

func (f *fakeJira) Search(in JiraSearchInput) ([]JiraIssue, error) { return nil, nil }
func (f *fakeJira) CreateIssue(in CreateIssueInput) (*CreatedIssue, error) {
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

func TestFileProposal_NewBranchCreatesIssue(t *testing.T) {
	j := &fakeJira{}
	d := &Draft{Summary: "x", Description: "y", IssueType: "Task", Labels: []string{"deferred-work"}, Priority: "Medium"}
	res, err := FileProposal(j, FileInput{
		Branch:      "new",
		ProjectKey:  "QORK",
		Draft:       d,
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
```

- [ ] **Step 2: Run, verify failure**

- [ ] **Step 3: Implement**

Add to `propose.go`:

```go
type jiraAPI interface {
	Search(in JiraSearchInput) ([]JiraIssue, error)
	CreateIssue(in CreateIssueInput) (*CreatedIssue, error)
	AddComment(key, text string) error
	AddLabel(key, label string) error
}

type FileInput struct {
	Branch            string // new|comment_on_existing|both
	ProjectKey        string
	ExistingTicketKey string
	CommentText       string // synthesized context for the existing-ticket branch
	Draft             *Draft
}

type FileResult struct {
	NewKey      string
	NewURL      string
	CommentedOn string
}

func FileProposal(j jiraAPI, in FileInput) (*FileResult, error) {
	res := &FileResult{}
	if in.Branch == "comment_on_existing" || in.Branch == "both" {
		if in.ExistingTicketKey == "" {
			return nil, errors.New("existing ticket key required for comment branch")
		}
		if err := j.AddComment(in.ExistingTicketKey, in.CommentText); err != nil {
			return nil, err
		}
		if err := j.AddLabel(in.ExistingTicketKey, "deferred-work-followup"); err != nil {
			return nil, err
		}
		res.CommentedOn = in.ExistingTicketKey
	}
	if in.Branch == "new" || in.Branch == "both" {
		if in.Draft == nil {
			return nil, errors.New("draft required for new-ticket branch")
		}
		created, err := j.CreateIssue(CreateIssueInput{
			ProjectKey:  in.ProjectKey,
			Summary:     in.Draft.Summary,
			Description: in.Draft.Description,
			IssueType:   in.Draft.IssueType,
			Labels:      in.Draft.Labels,
			Priority:    in.Draft.Priority,
		})
		if err != nil {
			return nil, err
		}
		res.NewKey = created.Key
		res.NewURL = created.URL
	}
	return res, nil
}
```

Add `import "errors"`.

- [ ] **Step 4: Wire proposal-reaction handler in `slack.go`**

Replace `handleProposalReaction`:

```go
func (r *Router) handleProposalReaction(e ReactionEvent, add bool) {
	if !add {
		return
	}
	p, err := r.Store.GetProposalBySlackTS(e.TS)
	if err != nil {
		return
	}
	if p.Status != "draft" {
		return
	}
	if !IsApproveReaction(r.Signals, e.Name) {
		return
	}
	it, _ := r.Store.GetItemByID(p.ItemID)
	if it == nil || isTerminal(it.Status) {
		return
	}
	r.Worker.Submit(FileJob{ProposalID: p.ID})
}
```

(File-job execution wires through the worker; concrete `FileJob.Run` lives with the worker in Task 26.)

- [ ] **Step 5: Tests pass, commit**

```bash
git add -A
git commit -m "feat(propose): FileProposal orchestrator + approval wiring"
```

---

## Task 23: @bot commands — status, help, cancel, file now (real implementations)

**Files:**
- Modify: `slack.go`
- Modify: `slack_test.go`

- [ ] **Step 1: Failing tests**

```go
func TestCmdStatus_ReportsCounts(t *testing.T) {
	store := newTestStore(t)
	fake := newFakeSlack("UBOT")
	r := &Router{Store: store, Slack: fake, BotUserID: "UBOT", WatchedChannels: map[string]bool{"C1": true}, Signals: &SignalsConfig{ApproveReactions: []string{"white_check_mark"}}, ApprovalThreshold: 3}
	r.HandleMessage(MessageEvent{Channel: "C1", TS: "1700.1", User: "U1", Text: "x"})
	r.HandleReactionAdded(ReactionEvent{User: "U2", Channel: "C1", TS: "1700.1", Name: "white_check_mark"})
	r.HandleMessage(MessageEvent{Channel: "C1", TS: "1700.2", ThreadTS: "1700.1", User: "U3", Text: "<@UBOT> status"})
	if len(fake.posted) == 0 {
		t.Fatal("expected status reply")
	}
}

func TestCmdFileNow_TransitionsToProposing(t *testing.T) {
	store := newTestStore(t)
	fake := newFakeSlack("UBOT")
	w := &Worker{queue: make(chan job, 1)}
	r := &Router{Store: store, Slack: fake, BotUserID: "UBOT", WatchedChannels: map[string]bool{"C1": true}, Signals: &SignalsConfig{}, ApprovalThreshold: 3, Worker: w}
	r.HandleMessage(MessageEvent{Channel: "C1", TS: "1700.1", User: "U1", Text: "x"})
	r.HandleMessage(MessageEvent{Channel: "C1", TS: "1700.2", ThreadTS: "1700.1", User: "U2", Text: "<@UBOT> file now"})
	it, _ := store.GetItemByTS("C1", "1700.1")
	if it.Status != "proposing" {
		t.Fatalf("status: %s", it.Status)
	}
	select {
	case <-w.queue:
	default:
		t.Fatal("expected ProposeJob enqueued")
	}
}
```

- [ ] **Step 2: Run, verify failure**

(Worker type stub needed; create minimal `worker.go` for compile, expanded in Task 26.)

`worker.go` (stub):

```go
package main

type job interface{ kind() string }
type Worker struct{ queue chan job }

type ProposeJob struct{ ItemID int64 }
func (ProposeJob) kind() string { return "propose" }

type FileJob struct{ ProposalID int64 }
func (FileJob) kind() string { return "file" }

type ReminderJob struct{ ItemID int64 }
func (ReminderJob) kind() string { return "reminder" }

func (w *Worker) Submit(j job) {
	if w == nil || w.queue == nil {
		return
	}
	select {
	case w.queue <- j:
	default:
	}
}
```

- [ ] **Step 3: Implement real commands**

Replace stubs in `slack.go`:

```go
func (r *Router) cmdStatus(it *Item, e MessageEvent) {
	n, _ := r.Store.CountVotes(it.ID)
	age := time.Since(it.CreatedAt).Hours() / 24
	msg := fmt.Sprintf("*Status:* `%s` — *%d/%d* approvals, idle *%.1fd*.", it.Status, n, it.ApprovalThreshold, age)
	r.Slack.PostMessage(e.Channel, slack.MsgOptionText(msg, false), slack.MsgOptionTS(it.SlackTS))
}

func (r *Router) cmdHelp(it *Item, e MessageEvent) {
	r.Slack.PostMessage(e.Channel, slack.MsgOptionText(helpText, false), slack.MsgOptionTS(it.SlackTS))
}

const helpText = "*deferred-work-bot commands:*\n" +
	"• `@bot status` — show votes + idle time\n" +
	"• `@bot cancel` — withdraw item\n" +
	"• `@bot regen` — re-draft proposal with latest thread context\n" +
	"• `@bot project: <name>` — override sub-project label\n" +
	"• `@bot priority: <low|medium|high>` — override priority\n" +
	"• `@bot file now` — skip approval gate; propose immediately\n" +
	"• `@bot search` — re-run related-ticket search\n" +
	"• `@bot help` — this message\n" +
	"• `@bot <question>` — free-form question about this item"

func (r *Router) cmdCancel(it *Item, e MessageEvent) {
	if err := r.Store.UpdateItemStatus(it.ID, "cancelled"); err != nil {
		return
	}
	r.Store.LogEvent(&it.ID, "cancel", `{"by":"`+e.User+`","via":"@bot cancel"}`)
	r.Slack.AddReaction("wastebasket", slackItem(it.SlackChannel, it.SlackTS))
}

func (r *Router) cmdFileNow(it *Item, e MessageEvent) {
	if it.Status != "collecting" {
		return
	}
	r.Store.UpdateItemStatus(it.ID, "proposing")
	r.Store.LogEvent(&it.ID, "advanced", `{"reason":"file_now","by":"`+e.User+`"}`)
	if r.Worker != nil {
		r.Worker.Submit(ProposeJob{ItemID: it.ID})
	}
}
```

Add `import "time"`.

- [ ] **Step 4: Tests pass, commit**

```bash
git add -A
git commit -m "feat(slack): cmdStatus, cmdHelp, cmdCancel, cmdFileNow"
```

---

## Task 24: @bot regen + project/priority overrides + search

**Files:**
- Modify: `slack.go`
- Modify: `slack_test.go`
- Modify: `store.go` (add `UpdateProposalDraft`)

- [ ] **Step 1: Failing tests**

```go
func TestCmdProject_UpdatesSubproject(t *testing.T) {
	store := newTestStore(t)
	fake := newFakeSlack("UBOT")
	r := &Router{Store: store, Slack: fake, BotUserID: "UBOT", WatchedChannels: map[string]bool{"C1": true}, Signals: &SignalsConfig{}, ApprovalThreshold: 3}
	r.HandleMessage(MessageEvent{Channel: "C1", TS: "1700.1", User: "U1", Text: "x"})
	r.HandleMessage(MessageEvent{Channel: "C1", TS: "1700.2", ThreadTS: "1700.1", User: "U2", Text: "<@UBOT> project: qatalyst"})
	it, _ := store.GetItemByTS("C1", "1700.1")
	if it.Subproject != "qatalyst" {
		t.Fatalf("subproject: %s", it.Subproject)
	}
}

func TestCmdPriority_SavedAsLatestOverride(t *testing.T) {
	store := newTestStore(t)
	fake := newFakeSlack("UBOT")
	r := &Router{Store: store, Slack: fake, BotUserID: "UBOT", WatchedChannels: map[string]bool{"C1": true}, Signals: &SignalsConfig{}, ApprovalThreshold: 3}
	r.HandleMessage(MessageEvent{Channel: "C1", TS: "1700.1", User: "U1", Text: "x"})
	r.HandleMessage(MessageEvent{Channel: "C1", TS: "1700.2", ThreadTS: "1700.1", User: "U2", Text: "<@UBOT> priority: high"})
	// Priority override stored via event log; verify it was logged.
	events, _ := store.ListEventsForItem(1)
	found := false
	for _, ev := range events {
		if ev.Kind == "priority_override" && strings.Contains(ev.Payload, "high") {
			found = true
		}
	}
	if !found {
		t.Fatal("expected priority_override event")
	}
}

func TestCmdRegen_EnqueuesProposeJob(t *testing.T) {
	store := newTestStore(t)
	fake := newFakeSlack("UBOT")
	w := &Worker{queue: make(chan job, 1)}
	r := &Router{Store: store, Slack: fake, BotUserID: "UBOT", WatchedChannels: map[string]bool{"C1": true}, Signals: &SignalsConfig{}, ApprovalThreshold: 3, Worker: w}
	r.HandleMessage(MessageEvent{Channel: "C1", TS: "1700.1", User: "U1", Text: "x"})
	it, _ := store.GetItemByTS("C1", "1700.1")
	store.UpdateItemStatus(it.ID, "proposed")
	store.InsertProposal(&Proposal{ItemID: it.ID, SlackTS: "1700.x", DraftJSON: "{}", RelatedTicketsJSON: "[]", Branch: "new", Status: "draft"})
	r.HandleMessage(MessageEvent{Channel: "C1", TS: "1700.2", ThreadTS: "1700.1", User: "U2", Text: "<@UBOT> regen"})
	select {
	case <-w.queue:
	default:
		t.Fatal("expected ProposeJob enqueued")
	}
	// Old proposal marked rejected.
	p, _ := store.GetLatestProposal(it.ID)
	if p.Status != "rejected" {
		t.Fatalf("old proposal status: %s", p.Status)
	}
}
```

- [ ] **Step 2: Run, verify failure**

- [ ] **Step 3: Implement**

```go
func (r *Router) cmdProject(it *Item, e MessageEvent, value string) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return
	}
	r.Store.UpdateItemSubproject(it.ID, value)
	r.Store.LogEvent(&it.ID, "project_override", `{"value":"`+value+`","by":"`+e.User+`"}`)
	r.Slack.AddReaction("white_check_mark", slackItem(e.Channel, e.TS))
}

func (r *Router) cmdPriority(it *Item, e MessageEvent, value string) {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "low", "medium", "med", "high":
	default:
		return
	}
	if value == "med" {
		value = "medium"
	}
	r.Store.LogEvent(&it.ID, "priority_override", `{"value":"`+value+`","by":"`+e.User+`"}`)
	r.Slack.AddReaction("white_check_mark", slackItem(e.Channel, e.TS))
}

func (r *Router) cmdRegen(it *Item, e MessageEvent) {
	if p, err := r.Store.GetLatestProposal(it.ID); err == nil {
		r.Store.UpdateProposalStatus(p.ID, "rejected")
	}
	if it.Status != "proposed" && it.Status != "proposing" {
		r.Store.UpdateItemStatus(it.ID, "proposing")
	}
	r.Store.LogEvent(&it.ID, "regen", `{"by":"`+e.User+`"}`)
	if r.Worker != nil {
		r.Worker.Submit(ProposeJob{ItemID: it.ID})
	}
}

func (r *Router) cmdSearch(it *Item, e MessageEvent) {
	// Same as regen but only re-runs Jira search portion; for v1 we just submit
	// a ProposeJob — the worker will redo search as part of the flow.
	r.cmdRegen(it, e)
}
```

`store.go` — add helper to read latest priority/project overrides from events (used by worker in Task 26):

```go
func (s *Store) LatestOverride(itemID int64, kind string) (string, error) {
	row := s.db.QueryRow(`SELECT payload_json FROM events WHERE item_id = ? AND kind = ? ORDER BY id DESC LIMIT 1`, itemID, kind)
	var p string
	err := row.Scan(&p)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	// payload is {"value":"...", "by":"..."}
	var parsed struct {
		Value string `json:"value"`
	}
	_ = json.Unmarshal([]byte(p), &parsed)
	return parsed.Value, nil
}
```

Add `"encoding/json"` import to `store.go` if not already present.

- [ ] **Step 4: Tests pass, commit**

```bash
git add -A
git commit -m "feat(slack): cmdRegen, cmdProject, cmdPriority, cmdSearch"
```

---

## Task 25: @bot freeform question handler

**Files:**
- Modify: `slack.go`
- Modify: `slack_test.go`

- [ ] **Step 1: Failing test**

```go
func TestCmdFreeform_AsksClaude(t *testing.T) {
	store := newTestStore(t)
	fake := newFakeSlack("UBOT")
	fc := &fakeClaude{resp: "this item is about flaky tests."}
	r := &Router{
		Store: store, Slack: fake, BotUserID: "UBOT",
		WatchedChannels: map[string]bool{"C1": true},
		Signals:         &SignalsConfig{},
		ApprovalThreshold: 3,
		Claude:          fc,
	}
	r.HandleMessage(MessageEvent{Channel: "C1", TS: "1700.1", User: "U1", Text: "flaky test in qompass"})
	r.HandleMessage(MessageEvent{Channel: "C1", TS: "1700.2", ThreadTS: "1700.1", User: "U2", Text: "<@UBOT> what's this about?"})
	if len(fake.posted) == 0 {
		t.Fatal("expected reply")
	}
}
```

- [ ] **Step 2: Run, verify failure**

- [ ] **Step 3: Implement**

Add field to `Router`:

```go
type Router struct {
	// ... existing fields ...
	Claude claudeAPI
}
```

Replace stub:

```go
func (r *Router) cmdFreeform(it *Item, e MessageEvent, q string) {
	if r.Claude == nil {
		return
	}
	thread, _, _, _ := r.Slack.GetConversationReplies(&slack.GetConversationRepliesParameters{ChannelID: it.SlackChannel, Timestamp: it.SlackTS})
	var ctx []string
	for _, m := range thread {
		ctx = append(ctx, m.Text)
	}
	prompt := fmt.Sprintf(`Answer this question about a deferred-work item.

ITEM:
%s

THREAD:
%s

QUESTION:
%s

Be concise (under 100 words). Reply with plain text only.`, it.Text, strings.Join(ctx, "\n---\n"), q)
	out, err := r.Claude.Run(context.Background(), prompt)
	if err != nil {
		r.Slack.PostMessage(e.Channel, slack.MsgOptionText("(claude error)", false), slack.MsgOptionTS(it.SlackTS))
		return
	}
	r.Slack.PostMessage(e.Channel, slack.MsgOptionText(strings.TrimSpace(out), false), slack.MsgOptionTS(it.SlackTS))
}
```

Add `"context"` to imports.

- [ ] **Step 4: Tests pass, commit**

```bash
git add -A
git commit -m "feat(slack): cmdFreeform answers via claude shell"
```

---

## Task 26: Worker pool + job execution

**Files:**
- Modify: `worker.go`
- Create: `worker_test.go`

- [ ] **Step 1: Failing test**

```go
package main

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestWorker_DrainsQueue(t *testing.T) {
	var processed atomic.Int32
	deps := WorkerDeps{
		Execute: func(ctx context.Context, j job) error {
			processed.Add(1)
			return nil
		},
	}
	w := NewWorker(2, 16, deps)
	w.Start()
	defer w.Stop(2 * time.Second)
	for i := 0; i < 8; i++ {
		w.Submit(ProposeJob{ItemID: int64(i)})
	}
	deadline := time.Now().Add(2 * time.Second)
	for processed.Load() < 8 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if processed.Load() != 8 {
		t.Fatalf("processed: %d", processed.Load())
	}
}

func TestWorker_DropsWhenQueueFull(t *testing.T) {
	var processed atomic.Int32
	deps := WorkerDeps{
		Execute: func(ctx context.Context, j job) error {
			time.Sleep(50 * time.Millisecond)
			processed.Add(1)
			return nil
		},
	}
	w := NewWorker(1, 1, deps) // tiny pool, tiny queue
	w.Start()
	defer w.Stop(time.Second)
	var dropped int
	for i := 0; i < 20; i++ {
		if !w.Submit(ProposeJob{ItemID: int64(i)}) {
			dropped++
		}
	}
	if dropped == 0 {
		t.Fatal("expected drops")
	}
}

func TestWorker_StopDrainsInflight(t *testing.T) {
	var done sync.WaitGroup
	deps := WorkerDeps{
		Execute: func(ctx context.Context, j job) error {
			defer done.Done()
			time.Sleep(50 * time.Millisecond)
			return nil
		},
	}
	w := NewWorker(2, 4, deps)
	w.Start()
	done.Add(2)
	w.Submit(ProposeJob{ItemID: 1})
	w.Submit(ProposeJob{ItemID: 2})
	if err := w.Stop(time.Second); err != nil {
		t.Fatalf("stop: %v", err)
	}
	done.Wait()
}
```

- [ ] **Step 2: Run, verify failure**

- [ ] **Step 3: Implement full `worker.go`**

```go
package main

import (
	"context"
	"errors"
	"sync"
	"time"
)

type job interface{ kind() string }

type ProposeJob struct{ ItemID int64 }
func (ProposeJob) kind() string { return "propose" }

type FileJob struct{ ProposalID int64 }
func (FileJob) kind() string { return "file" }

type ReminderJob struct{ ItemID int64 }
func (ReminderJob) kind() string { return "reminder" }

type WorkerDeps struct {
	Execute func(ctx context.Context, j job) error
	Logger  func(format string, args ...any)
}

type Worker struct {
	workers int
	queue   chan job
	deps    WorkerDeps
	wg      sync.WaitGroup
	ctx     context.Context
	cancel  context.CancelFunc
}

func NewWorker(workers, queueSize int, deps WorkerDeps) *Worker {
	if workers < 1 {
		workers = 1
	}
	if queueSize < 1 {
		queueSize = 1
	}
	ctx, cancel := context.WithCancel(context.Background())
	if deps.Logger == nil {
		deps.Logger = func(string, ...any) {}
	}
	return &Worker{
		workers: workers,
		queue:   make(chan job, queueSize),
		deps:    deps,
		ctx:     ctx,
		cancel:  cancel,
	}
}

func (w *Worker) Start() {
	for i := 0; i < w.workers; i++ {
		w.wg.Add(1)
		go w.loop()
	}
}

func (w *Worker) Submit(j job) bool {
	if w == nil {
		return false
	}
	select {
	case w.queue <- j:
		return true
	default:
		w.deps.Logger("worker queue full, dropping %s", j.kind())
		return false
	}
}

func (w *Worker) Stop(timeout time.Duration) error {
	close(w.queue)
	done := make(chan struct{})
	go func() {
		w.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		w.cancel()
		return nil
	case <-time.After(timeout):
		w.cancel()
		return errors.New("worker stop timeout")
	}
}

func (w *Worker) loop() {
	defer w.wg.Done()
	for j := range w.queue {
		if err := w.deps.Execute(w.ctx, j); err != nil {
			w.deps.Logger("job %s failed: %v", j.kind(), err)
		}
	}
}

func (w *Worker) QueueDepth() int {
	if w == nil {
		return 0
	}
	return len(w.queue)
}
```

- [ ] **Step 4: Tests pass, commit**

```bash
git add -A
git commit -m "feat(worker): bounded worker pool with graceful drain"
```

---

## Task 27: Job executors (propose, file, reminder)

**Files:**
- Modify: `propose.go` (add `JobExecutor`)
- Create: `executor_test.go`

- [ ] **Step 1: Failing test**

```go
package main

import (
	"context"
	"strings"
	"testing"
)

func TestJobExecutor_ProposeFlow_NewBranch(t *testing.T) {
	store := newTestStore(t)
	fake := newFakeSlack("UBOT")
	fc := &fakeClaude{resp: `{"summary":"do x","description":"d","labels":["deferred-work","qompass"],"priority":"Medium"}`}
	jc := &fakeJira{}
	it := &Item{SlackChannel: "C1", SlackTS: "1700.1", AuthorSlackID: "U1", Text: "qompass thing", Status: "proposing", ApprovalThreshold: 3}
	store.InsertItem(it)
	ex := &JobExecutor{
		Store: store, Slack: fake, Claude: fc, Jira: jc,
		Projects: &ProjectsConfig{Subprojects: []string{"qompass"}, QORKProjects: []string{"QORK"}},
		Signals:  &SignalsConfig{},
		BotUserID: "UBOT",
	}
	if err := ex.Execute(context.Background(), ProposeJob{ItemID: it.ID}); err != nil {
		t.Fatal(err)
	}
	p, err := store.GetLatestProposal(it.ID)
	if err != nil {
		t.Fatalf("no proposal: %v", err)
	}
	if p.Branch != "new" {
		t.Fatalf("branch: %s", p.Branch)
	}
	if p.Status != "draft" {
		t.Fatalf("status: %s", p.Status)
	}
	if len(fake.posted) == 0 || !strings.Contains(fake.posted[0].Text, "") {
		t.Fatal("expected proposal posted")
	}
	got, _ := store.GetItemByID(it.ID)
	if got.Status != "proposed" {
		t.Fatalf("item status: %s", got.Status)
	}
}

func TestJobExecutor_FileFlow_CreatesIssueAndLocks(t *testing.T) {
	store := newTestStore(t)
	fake := newFakeSlack("UBOT")
	jc := &fakeJira{}
	it := &Item{SlackChannel: "C1", SlackTS: "1700.1", AuthorSlackID: "U1", Text: "x", Status: "proposed", ApprovalThreshold: 3}
	store.InsertItem(it)
	p := &Proposal{
		ItemID:             it.ID,
		SlackTS:            "1700.2",
		DraftJSON:          `{"summary":"do x","description":"d","issue_type":"Task","labels":["deferred-work"],"priority":"Medium"}`,
		RelatedTicketsJSON: "[]",
		Branch:             "new",
		Status:             "draft",
	}
	store.InsertProposal(p)
	ex := &JobExecutor{
		Store: store, Slack: fake, Jira: jc,
		Projects: &ProjectsConfig{QORKProjects: []string{"QORK"}},
		Signals:  &SignalsConfig{},
		BotUserID: "UBOT",
	}
	if err := ex.Execute(context.Background(), FileJob{ProposalID: p.ID}); err != nil {
		t.Fatal(err)
	}
	got, _ := store.GetItemByID(it.ID)
	if got.Status != "ticketed" {
		t.Fatalf("item status: %s", got.Status)
	}
	tk, err := store.GetTicketByProposal(p.ID)
	if err != nil {
		t.Fatalf("no ticket: %v", err)
	}
	if tk.JiraKey != "QORK-100" {
		t.Fatalf("key: %s", tk.JiraKey)
	}
}
```

- [ ] **Step 2: Run, verify failure**

- [ ] **Step 3: Implement**

Append to `propose.go`:

```go
type JobExecutor struct {
	Store     *Store
	Slack     SlackAPI
	Claude    claudeAPI
	Jira      jiraAPI
	Projects  *ProjectsConfig
	Signals   *SignalsConfig
	BotUserID string
}

func (e *JobExecutor) Execute(ctx context.Context, j job) error {
	switch v := j.(type) {
	case ProposeJob:
		return e.executePropose(ctx, v.ItemID)
	case FileJob:
		return e.executeFile(ctx, v.ProposalID)
	case ReminderJob:
		return e.executeReminder(ctx, v.ItemID)
	}
	return fmt.Errorf("unknown job: %s", j.kind())
}

func (e *JobExecutor) executePropose(ctx context.Context, itemID int64) error {
	it, err := e.Store.GetItemByID(itemID)
	if err != nil {
		return err
	}
	if it.Status == "cancelled" || it.Status == "archived" {
		return nil
	}

	// 1. Load thread.
	msgs, _, _, _ := e.Slack.GetConversationReplies(&slack.GetConversationRepliesParameters{ChannelID: it.SlackChannel, Timestamp: it.SlackTS})
	var thread []string
	for _, m := range msgs {
		if m.User == e.BotUserID || m.Timestamp == it.SlackTS {
			continue
		}
		thread = append(thread, m.Text)
	}

	// 2. Subproject (use override if present).
	sub := it.Subproject
	if sub == "" {
		v, _ := detectSubproject(ctx, e.Projects, e.Claude, it.Text+"\n"+strings.Join(thread, "\n"))
		sub = v
		if sub != "" {
			e.Store.UpdateItemSubproject(it.ID, sub)
		}
	}

	// 3. Jira search.
	keywords := extractKeywords(it.Text)
	issues, _ := e.Jira.Search(JiraSearchInput{
		Projects:   e.Projects.QORKProjects,
		Subproject: sub,
		Keywords:   keywords,
		Limit:      20,
	})

	// 4. Classify relevance.
	rels, _ := classifyRelatedTickets(ctx, e.Claude, it.Text, issues)
	branch, existing := DecideBranch(rels)

	// 5. Draft (skip for awaiting_resolution path).
	var draft *Draft
	if branch == "new" {
		permalink, _ := e.Slack.GetPermalink(&slack.PermalinkParameters{Channel: it.SlackChannel, Ts: it.SlackTS})
		priority, _ := e.Store.LatestOverride(it.ID, "priority_override")
		d, err := DraftTicket(ctx, e.Claude, DraftInput{
			Text:         it.Text,
			Thread:       thread,
			Subproject:   sub,
			PriorityOver: priority,
			Permalink:    permalink,
		})
		if err != nil {
			return err
		}
		draft = d
	}

	// 6. Post proposal.
	body := RenderProposalMessage(draft, rels, branch, existing, false)
	_, ts, err := e.Slack.PostMessage(it.SlackChannel,
		slack.MsgOptionText(body, false),
		slack.MsgOptionTS(it.SlackTS))
	if err != nil {
		return err
	}

	// 7. Persist proposal row.
	draftJSON, _ := json.Marshal(draft)
	relsJSON, _ := json.Marshal(rels)
	p := &Proposal{
		ItemID:             it.ID,
		SlackTS:            ts,
		DraftJSON:          string(draftJSON),
		RelatedTicketsJSON: string(relsJSON),
		Branch:             branch,
		ExistingTicketKey:  existing,
		Status:             "draft",
	}
	if branch == "awaiting_resolution" {
		p.Status = "awaiting_resolution"
	}
	e.Store.InsertProposal(p)
	e.Store.UpdateItemStatus(it.ID, "proposed")
	e.Store.LogEvent(&it.ID, "proposal", `{"branch":"`+branch+`"}`)
	return nil
}

func (e *JobExecutor) executeFile(ctx context.Context, proposalID int64) error {
	p, err := e.Store.GetLatestProposalByID(proposalID)
	if err != nil {
		return err
	}
	if p.Status != "draft" {
		return nil
	}
	it, _ := e.Store.GetItemByID(p.ItemID)
	if it == nil || isTerminal(it.Status) {
		return nil
	}

	var draft Draft
	json.Unmarshal([]byte(p.DraftJSON), &draft)
	commentText := buildExistingTicketComment(it.Text, draft.Description)

	projectKey := ""
	if len(e.Projects.QORKProjects) > 0 {
		projectKey = e.Projects.QORKProjects[0]
	}
	res, err := FileProposal(e.Jira, FileInput{
		Branch:            p.Branch,
		ProjectKey:        projectKey,
		ExistingTicketKey: p.ExistingTicketKey,
		CommentText:       commentText,
		Draft:             &draft,
	})
	if err != nil {
		e.Slack.PostMessage(it.SlackChannel,
			slack.MsgOptionText(":warning: Failed to file ticket: "+err.Error(), false),
			slack.MsgOptionTS(it.SlackTS))
		return err
	}

	e.Store.UpdateProposalStatus(p.ID, "filed")
	action := "created"
	jiraKey := res.NewKey
	jiraURL := res.NewURL
	if p.Branch == "comment_on_existing" {
		action = "commented_on_existing"
		jiraKey = res.CommentedOn
		jiraURL = ""
	}
	e.Store.InsertTicket(&Ticket{
		ProposalID:        p.ID,
		JiraKey:           jiraKey,
		JiraURL:           jiraURL,
		Action:            action,
		ExistingTicketKey: p.ExistingTicketKey,
	})

	switch p.Branch {
	case "new":
		e.Store.UpdateItemStatus(it.ID, "ticketed")
	case "comment_on_existing":
		e.Store.UpdateItemStatus(it.ID, "commented_on_existing")
	case "both":
		e.Store.UpdateItemStatus(it.ID, "ticketed")
	}

	msg := "Filed: "
	if res.NewKey != "" {
		msg += fmt.Sprintf("<%s|%s>", res.NewURL, res.NewKey)
	}
	if res.CommentedOn != "" {
		if res.NewKey != "" {
			msg += " + "
		}
		msg += "commented on " + res.CommentedOn
	}
	e.Slack.PostMessage(it.SlackChannel, slack.MsgOptionText(msg, false), slack.MsgOptionTS(it.SlackTS))
	e.Slack.AddReaction("white_check_mark", slack.ItemRef{Channel: it.SlackChannel, Timestamp: it.SlackTS})
	return nil
}

func (e *JobExecutor) executeReminder(ctx context.Context, itemID int64) error {
	// Reminder posting is driven by the ticker; this is reserved for manual triggers.
	return nil
}

func buildExistingTicketComment(original, descPreview string) string {
	return "*Deferred-work follow-up*\n\nOriginal Slack message:\n" + original + "\n\nSynthesized context:\n" + descPreview
}

// extractKeywords is a tiny stopword filter; the worker uses claude inference for tougher cases.
func extractKeywords(text string) []string {
	stop := map[string]bool{
		"the": true, "a": true, "an": true, "is": true, "and": true, "or": true,
		"to": true, "for": true, "of": true, "in": true, "on": true, "we": true,
		"i": true, "this": true, "that": true, "be": true, "it": true, "by": true,
		"with": true, "from": true, "as": true, "at": true, "should": true, "will": true,
	}
	var out []string
	seen := map[string]bool{}
	for _, w := range strings.Fields(strings.ToLower(text)) {
		w = strings.Trim(w, ".,!?:;()[]{}\"'`")
		if len(w) < 3 || stop[w] || seen[w] {
			continue
		}
		out = append(out, w)
		seen[w] = true
		if len(out) >= 8 {
			break
		}
	}
	return out
}
```

Add to `store.go`:

```go
func (s *Store) GetLatestProposalByID(id int64) (*Proposal, error) {
	row := s.db.QueryRow(`SELECT id, item_id, slack_ts, draft_json, related_tickets_json, branch, existing_ticket_key, status, created_at, updated_at FROM proposals WHERE id = ?`, id)
	var p Proposal
	err := row.Scan(&p.ID, &p.ItemID, &p.SlackTS, &p.DraftJSON, &p.RelatedTicketsJSON, &p.Branch, &p.ExistingTicketKey, &p.Status, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &p, err
}
```

- [ ] **Step 4: Tests pass, commit**

```bash
git add -A
git commit -m "feat(propose): JobExecutor — propose + file orchestrators"
```

---

## Task 28: Ticker — reminders, warnings, archive

**Files:**
- Create: `ticker.go`
- Create: `ticker_test.go`

- [ ] **Step 1: Failing test**

```go
package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestTicker_PostsReminderAfter3Days(t *testing.T) {
	store := newTestStore(t)
	fake := newFakeSlack("UBOT")
	now := time.Now()
	older := now.Add(-4 * 24 * time.Hour)
	it := &Item{SlackChannel: "C1", SlackTS: "1700.1", AuthorSlackID: "U1", Text: "x", Status: "collecting", ApprovalThreshold: 3}
	store.InsertItem(it)
	store.db.Exec(`UPDATE items SET created_at = ? WHERE id = ?`, older, it.ID)

	tk := &Ticker{
		Store: store, Slack: fake,
		ReminderEvery: 3 * 24 * time.Hour,
		WarnAt:        10 * 24 * time.Hour,
		ArchiveAt:     13 * 24 * time.Hour,
		Now:           func() time.Time { return now },
	}
	tk.Tick(context.Background())
	if len(fake.posted) == 0 || !strings.Contains(fake.posted[0].Text, "") {
		t.Fatal("expected reminder posted")
	}
	got, _ := store.GetItemByID(it.ID)
	if got.LastReminderAt == nil {
		t.Fatal("expected last_reminder_at set")
	}
}

func TestTicker_PostsWarningAt10Days(t *testing.T) {
	store := newTestStore(t)
	fake := newFakeSlack("UBOT")
	now := time.Now()
	older := now.Add(-11 * 24 * time.Hour)
	it := &Item{SlackChannel: "C1", SlackTS: "1700.1", AuthorSlackID: "U1", Text: "x", Status: "collecting", ApprovalThreshold: 3}
	store.InsertItem(it)
	store.db.Exec(`UPDATE items SET created_at = ?, last_reminder_at = ? WHERE id = ?`, older, older, it.ID)

	tk := &Ticker{Store: store, Slack: fake, ReminderEvery: 3 * 24 * time.Hour, WarnAt: 10 * 24 * time.Hour, ArchiveAt: 13 * 24 * time.Hour, Now: func() time.Time { return now }}
	tk.Tick(context.Background())
	got, _ := store.GetItemByID(it.ID)
	if got.WarningPostedAt == nil {
		t.Fatal("expected warning posted")
	}
	if !strings.Contains(fake.posted[0].Text, ":rotating_light:") {
		t.Fatalf("expected warning emojis: %s", fake.posted[0].Text)
	}
}

func TestTicker_ArchivesAt13Days(t *testing.T) {
	store := newTestStore(t)
	fake := newFakeSlack("UBOT")
	now := time.Now()
	older := now.Add(-14 * 24 * time.Hour)
	warned := now.Add(-4 * 24 * time.Hour)
	it := &Item{SlackChannel: "C1", SlackTS: "1700.1", AuthorSlackID: "U1", Text: "x", Status: "collecting", ApprovalThreshold: 3}
	store.InsertItem(it)
	store.db.Exec(`UPDATE items SET created_at = ?, warning_posted_at = ? WHERE id = ?`, older, warned, it.ID)

	tk := &Ticker{Store: store, Slack: fake, ReminderEvery: 3 * 24 * time.Hour, WarnAt: 10 * 24 * time.Hour, ArchiveAt: 13 * 24 * time.Hour, Now: func() time.Time { return now }}
	tk.Tick(context.Background())
	got, _ := store.GetItemByID(it.ID)
	if got.Status != "archived" {
		t.Fatalf("status: %s", got.Status)
	}
}

func TestTicker_NewVotesAfterWarningRevertsLifecycle(t *testing.T) {
	// If vote count >= threshold, the propose advancement happens elsewhere (router).
	// Ticker's contract: do not archive if proposal/ticket has been moved past 'collecting'.
	store := newTestStore(t)
	fake := newFakeSlack("UBOT")
	now := time.Now()
	older := now.Add(-14 * 24 * time.Hour)
	it := &Item{SlackChannel: "C1", SlackTS: "1700.1", AuthorSlackID: "U1", Text: "x", Status: "proposed", ApprovalThreshold: 3}
	store.InsertItem(it)
	store.db.Exec(`UPDATE items SET created_at = ?, warning_posted_at = ? WHERE id = ?`, older, now.Add(-4*24*time.Hour), it.ID)

	tk := &Ticker{Store: store, Slack: fake, ReminderEvery: 3 * 24 * time.Hour, WarnAt: 10 * 24 * time.Hour, ArchiveAt: 13 * 24 * time.Hour, Now: func() time.Time { return now }}
	tk.Tick(context.Background())
	got, _ := store.GetItemByID(it.ID)
	if got.Status == "archived" {
		t.Fatal("non-collecting items should not be archived by ticker")
	}
}
```

- [ ] **Step 2: Run, verify failure**

- [ ] **Step 3: Implement `ticker.go`**

```go
package main

import (
	"context"
	"fmt"
	"time"

	"github.com/slack-go/slack"
)

type Ticker struct {
	Store         *Store
	Slack         SlackAPI
	ReminderEvery time.Duration
	WarnAt        time.Duration
	ArchiveAt     time.Duration
	Now           func() time.Time
}

func (t *Ticker) Tick(ctx context.Context) {
	items, err := t.Store.ListItemsByStatus("collecting")
	if err != nil {
		return
	}
	now := t.now()
	for _, it := range items {
		age := now.Sub(it.CreatedAt)
		if t.shouldArchive(it, now) {
			t.archive(it)
			continue
		}
		if t.shouldWarn(it, age) {
			t.warn(it, now)
			continue
		}
		if t.shouldRemind(it, age, now) {
			t.remind(it, now)
		}
	}
}

func (t *Ticker) now() time.Time {
	if t.Now != nil {
		return t.Now()
	}
	return time.Now()
}

func (t *Ticker) shouldRemind(it *Item, age time.Duration, now time.Time) bool {
	if age < t.ReminderEvery {
		return false
	}
	if it.LastReminderAt == nil {
		return true
	}
	return now.Sub(*it.LastReminderAt) >= t.ReminderEvery
}

func (t *Ticker) shouldWarn(it *Item, age time.Duration) bool {
	return age >= t.WarnAt && it.WarningPostedAt == nil
}

func (t *Ticker) shouldArchive(it *Item, now time.Time) bool {
	if it.WarningPostedAt == nil {
		return false
	}
	return now.Sub(*it.WarningPostedAt) >= (t.ArchiveAt - t.WarnAt)
}

func (t *Ticker) remind(it *Item, now time.Time) {
	n, _ := t.Store.CountVotes(it.ID)
	age := now.Sub(it.CreatedAt).Hours() / 24
	body := fmt.Sprintf("Still pending — *%d/%d* approvals, *%.1fd* idle. Original:\n> %s",
		n, it.ApprovalThreshold, age, truncate(it.Text, 200))
	t.Slack.PostMessage(it.SlackChannel,
		slack.MsgOptionText(body, false),
		slack.MsgOptionTS(it.SlackTS))
	t.Store.UpdateItemReminderTimes(it.ID, &now, it.WarningPostedAt)
	t.Store.LogEvent(&it.ID, "reminder", "{}")
}

func (t *Ticker) warn(it *Item, now time.Time) {
	body := fmt.Sprintf(":rotating_light: :warning: *Deferred-work auto-archive incoming.* "+
		"This item will be archived in 3 days unless it gets activity. :warning: :rotating_light:\n> %s",
		truncate(it.Text, 200))
	t.Slack.PostMessage(it.SlackChannel,
		slack.MsgOptionText(body, false),
		slack.MsgOptionTS(it.SlackTS))
	t.Store.UpdateItemReminderTimes(it.ID, it.LastReminderAt, &now)
	t.Store.LogEvent(&it.ID, "warning", "{}")
}

func (t *Ticker) archive(it *Item) {
	t.Store.UpdateItemStatus(it.ID, "archived")
	t.Store.LogEvent(&it.ID, "archive", "{}")
	t.Slack.AddReaction("wastebasket", slack.ItemRef{Channel: it.SlackChannel, Timestamp: it.SlackTS})
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
```

- [ ] **Step 4: Tests pass, commit**

```bash
git add -A
git commit -m "feat(ticker): reminders + warnings + archive"
```

---

## Task 29: Health server — /health, /metrics, POST /trigger

**Files:**
- Create: `health.go`
- Create: `health_test.go`

- [ ] **Step 1: Failing test**

```go
package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealth_OK(t *testing.T) {
	store := newTestStore(t)
	w := &Worker{queue: make(chan job, 1)}
	srv := NewHealthServer(HealthDeps{Store: store, Worker: w, TriggerToken: ""})
	rec := httptest.NewRecorder()
	srv.handler().ServeHTTP(rec, httptest.NewRequest("GET", "/health", nil))
	if rec.Code != 200 {
		t.Fatalf("code: %d", rec.Code)
	}
}

func TestMetrics_ReportsQueueAndItemCounts(t *testing.T) {
	store := newTestStore(t)
	store.InsertItem(&Item{SlackChannel: "C1", SlackTS: "1", AuthorSlackID: "U1", Text: "x", Status: "collecting", ApprovalThreshold: 3})
	store.InsertItem(&Item{SlackChannel: "C1", SlackTS: "2", AuthorSlackID: "U1", Text: "x", Status: "ticketed", ApprovalThreshold: 3})
	w := &Worker{queue: make(chan job, 4)}
	w.queue <- ProposeJob{}
	srv := NewHealthServer(HealthDeps{Store: store, Worker: w, TriggerToken: ""})
	rec := httptest.NewRecorder()
	srv.handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body := rec.Body.String()
	if !strings.Contains(body, "queue_depth 1") {
		t.Fatalf("queue_depth missing: %s", body)
	}
	if !strings.Contains(body, `items_by_status{status="collecting"} 1`) {
		t.Fatalf("collecting count missing: %s", body)
	}
}

func TestTrigger_RequiresToken(t *testing.T) {
	store := newTestStore(t)
	w := &Worker{queue: make(chan job, 4)}
	srv := NewHealthServer(HealthDeps{Store: store, Worker: w, TriggerToken: "sekret"})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/trigger?item_id=1&action=propose", nil)
	srv.handler().ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatalf("code: %d", rec.Code)
	}
}

func TestTrigger_EnqueuesProposeJob(t *testing.T) {
	store := newTestStore(t)
	store.InsertItem(&Item{SlackChannel: "C1", SlackTS: "1", AuthorSlackID: "U1", Text: "x", Status: "collecting", ApprovalThreshold: 3})
	w := &Worker{queue: make(chan job, 4)}
	srv := NewHealthServer(HealthDeps{Store: store, Worker: w, TriggerToken: "sekret"})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/trigger?item_id=1&action=propose", nil)
	req.Header.Set("Authorization", "Bearer sekret")
	srv.handler().ServeHTTP(rec, req)
	if rec.Code != 202 {
		t.Fatalf("code: %d, body: %s", rec.Code, rec.Body.String())
	}
	select {
	case <-w.queue:
	default:
		t.Fatal("expected job enqueued")
	}
}
```

- [ ] **Step 2: Run, verify failure**

- [ ] **Step 3: Implement**

```go
package main

import (
	"fmt"
	"net/http"
	"strconv"
)

type HealthDeps struct {
	Store        *Store
	Worker       *Worker
	TriggerToken string
}

type HealthServer struct{ deps HealthDeps }

func NewHealthServer(d HealthDeps) *HealthServer { return &HealthServer{deps: d} }

func (h *HealthServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", h.health)
	mux.HandleFunc("/metrics", h.metrics)
	mux.HandleFunc("/trigger", h.trigger)
	return mux
}

func (h *HealthServer) ListenAndServe(addr string) error {
	return http.ListenAndServe(addr, h.handler())
}

func (h *HealthServer) health(w http.ResponseWriter, r *http.Request) {
	if h.deps.Store == nil || h.deps.Store.db.Ping() != nil {
		http.Error(w, "db unreachable", 503)
		return
	}
	w.WriteHeader(200)
	w.Write([]byte("ok\n"))
}

func (h *HealthServer) metrics(w http.ResponseWriter, r *http.Request) {
	statuses := []string{"collecting", "proposing", "proposed", "ticketed", "commented_on_existing", "cancelled", "archived"}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	fmt.Fprintf(w, "# HELP queue_depth Worker queue depth\nqueue_depth %d\n", h.deps.Worker.QueueDepth())
	for _, st := range statuses {
		n := 0
		row := h.deps.Store.db.QueryRow(`SELECT COUNT(*) FROM items WHERE status = ?`, st)
		row.Scan(&n)
		fmt.Fprintf(w, `items_by_status{status=%q} %d`+"\n", st, n)
	}
}

func (h *HealthServer) trigger(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "method", 405)
		return
	}
	if h.deps.TriggerToken != "" {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer "+h.deps.TriggerToken {
			http.Error(w, "unauthorized", 401)
			return
		}
	}
	itemID, err := strconv.ParseInt(r.URL.Query().Get("item_id"), 10, 64)
	if err != nil {
		http.Error(w, "bad item_id", 400)
		return
	}
	action := r.URL.Query().Get("action")
	switch action {
	case "propose":
		h.deps.Worker.Submit(ProposeJob{ItemID: itemID})
	case "reminder":
		h.deps.Worker.Submit(ReminderJob{ItemID: itemID})
	default:
		http.Error(w, "bad action", 400)
		return
	}
	w.WriteHeader(202)
}
```

- [ ] **Step 4: Tests pass, commit**

```bash
git add -A
git commit -m "feat(health): /health, /metrics, POST /trigger"
```

---

## Task 30: Main wiring + graceful shutdown

**Files:**
- Modify: `main.go`
- Create: `main_test.go` (smoke compile test only)

- [ ] **Step 1: Replace `main.go` with full wiring**

```go
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/socketmode"
	"github.com/slack-go/slack/slackevents"
)

func main() {
	cfg, err := LoadConfig()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	projects, err := LoadProjects("projects.yaml")
	if err != nil {
		log.Fatalf("projects.yaml: %v", err)
	}
	signals, err := LoadSignals("signals.yaml")
	if err != nil {
		log.Fatalf("signals.yaml: %v", err)
	}
	store, err := OpenStore(cfg.SQLitePath)
	if err != nil {
		log.Fatalf("store: %v", err)
	}
	defer store.Close()

	api := slack.New(cfg.SlackBotToken,
		slack.OptionAppLevelToken(cfg.SlackAppToken))
	auth, err := api.AuthTest()
	if err != nil {
		log.Fatalf("auth: %v", err)
	}
	botID := auth.UserID
	log.Printf("authenticated as %s (%s)", auth.User, botID)

	jira := NewJiraClient(cfg.JiraBaseURL, cfg.JiraEmail, cfg.JiraAPIToken)
	claudeRunner := NewClaudeRunner()

	executor := &JobExecutor{
		Store: store, Slack: api, Claude: claudeRunner, Jira: jira,
		Projects: projects, Signals: signals, BotUserID: botID,
	}
	worker := NewWorker(cfg.Workers, cfg.QueueSize, WorkerDeps{
		Execute: executor.Execute,
		Logger:  log.Printf,
	})
	worker.Start()

	watched := map[string]bool{}
	for _, c := range cfg.WatchedChannels {
		watched[c] = true
	}
	router := &Router{
		Store: store, Slack: api, BotUserID: botID,
		WatchedChannels: watched, ApprovalThreshold: cfg.ApprovalThreshold,
		Signals: signals, Projects: projects, Worker: worker, Config: cfg,
		Claude: claudeRunner,
	}

	tk := &Ticker{
		Store: store, Slack: api,
		ReminderEvery: time.Duration(cfg.ReminderIntervalDays) * 24 * time.Hour,
		WarnAt:        time.Duration(cfg.WarningAtDays) * 24 * time.Hour,
		ArchiveAt:     time.Duration(cfg.WarningAtDays+cfg.ArchiveGraceDays) * 24 * time.Hour,
	}
	ctx, cancel := context.WithCancel(context.Background())
	go runTicker(ctx, tk)

	health := NewHealthServer(HealthDeps{Store: store, Worker: worker, TriggerToken: cfg.TriggerToken})
	go func() {
		addr := fmt.Sprintf(":%d", cfg.HealthPort)
		log.Printf("health listening on %s", addr)
		if err := health.ListenAndServe(addr); err != nil {
			log.Printf("health: %v", err)
		}
	}()

	// Socket Mode event loop.
	sm := socketmode.New(api)
	go runSocketMode(ctx, sm, router)
	go func() { sm.Run() }()

	// Wait for signal.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	log.Print("shutdown signal received")
	cancel()
	worker.Stop(60 * time.Second)
	log.Print("shutdown complete")
}

func runTicker(ctx context.Context, tk *Ticker) {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	tk.Tick(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			tk.Tick(ctx)
		}
	}
}

func runSocketMode(ctx context.Context, sm *socketmode.Client, r *Router) {
	for evt := range sm.Events {
		switch evt.Type {
		case socketmode.EventTypeEventsAPI:
			payload, ok := evt.Data.(slackevents.EventsAPIEvent)
			if !ok {
				continue
			}
			sm.Ack(*evt.Request)
			handleEventsAPI(r, payload)
		}
	}
}

func handleEventsAPI(r *Router, e slackevents.EventsAPIEvent) {
	switch e.Type {
	case slackevents.CallbackEvent:
		switch ev := e.InnerEvent.Data.(type) {
		case *slackevents.MessageEvent:
			r.HandleMessage(MessageEvent{
				Channel:  ev.Channel,
				TS:       ev.TimeStamp,
				ThreadTS: ev.ThreadTimeStamp,
				User:     ev.User,
				Text:     ev.Text,
			})
		case *slackevents.AppMentionEvent:
			r.HandleAppMention(MessageEvent{
				Channel:  ev.Channel,
				TS:       ev.TimeStamp,
				ThreadTS: ev.ThreadTimeStamp,
				User:     ev.User,
				Text:     ev.Text,
			})
		case *slackevents.ReactionAddedEvent:
			r.HandleReactionAdded(ReactionEvent{
				User:    ev.User,
				Channel: ev.Item.Channel,
				TS:      ev.Item.Timestamp,
				Name:    ev.Reaction,
			})
		case *slackevents.ReactionRemovedEvent:
			r.HandleReactionRemoved(ReactionEvent{
				User:    ev.User,
				Channel: ev.Item.Channel,
				TS:      ev.Item.Timestamp,
				Name:    ev.Reaction,
			})
		}
	}
}
```

- [ ] **Step 2: Compile**

Run: `go build ./...`
Expected: succeeds.

- [ ] **Step 3: Smoke test compile**

`main_test.go`:

```go
package main

import "testing"

func TestMainCompiles(t *testing.T) {
	// Compilation success is the test.
}
```

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add -A
git commit -m "feat: main wiring + socket-mode event loop + graceful shutdown"
```

---

## Task 31: Slack manifest + README

**Files:**
- Create: `slack-manifest.yaml`
- Create: `README.md`

- [ ] **Step 1: Write `slack-manifest.yaml`**

```yaml
display_information:
  name: deferred-work-bot
  description: Tracks deferred work, votes, and files Jira tickets.
  background_color: "#2c2d30"
features:
  bot_user:
    display_name: deferred-work-bot
    always_online: true
  app_home:
    home_tab_enabled: false
    messages_tab_enabled: false
oauth_config:
  scopes:
    bot:
      - app_mentions:read
      - channels:history
      - channels:read
      - chat:write
      - groups:history
      - reactions:read
      - reactions:write
      - users:read
settings:
  event_subscriptions:
    bot_events:
      - app_mention
      - message.channels
      - message.groups
      - reaction_added
      - reaction_removed
  interactivity:
    is_enabled: true
  socket_mode_enabled: true
```

- [ ] **Step 2: Write `README.md`**

```markdown
# deferred-work-bot

Slack bot that tracks deferred work, gates it behind a 3-approval vote, drafts Jira tickets via the local `claude` CLI (with related-ticket detection), and files them on a final 1-approval vote.

## How it works

1. Post a deferred-work item in the dedicated channel (or `@deferred-work-bot <text>` in any invited channel).
2. The bot reacts `:eyes:` and tracks the item.
3. Three unique-user approvals (reactions or reply keywords) trigger a proposal draft.
4. The bot posts the draft + related Jira tickets back to the thread.
5. One approval reaction on the proposal files the ticket (or comments on an existing one).

## Commands

`@bot status` · `@bot cancel` · `@bot regen` · `@bot project: <name>` · `@bot priority: <low|medium|high>` · `@bot file now` · `@bot search` · `@bot help` · `@bot <freeform question>`

## Approval signals (configurable in `signals.yaml`)

- Reactions: `:white_check_mark:`, `:claude-it:`, `:+1:`, `:thumbsup:`
- Reply keywords: `approve`, `approved`, `+1`, `lgtm`

## Lifecycle

- Reminder every 3 days while item sits without 3 approvals.
- Warning posted at 10 days idle.
- Archived 3 days after warning (13 days total) if still no movement.
- `:x:` reaction or `@bot cancel` cancels any time.

## Setup

1. Install [Claude CLI](https://docs.anthropic.com/en/docs/claude-code) and authenticate it.
2. Create a Slack app from `slack-manifest.yaml`.
3. Generate a Jira API token (Atlassian → account settings → security → API tokens).
4. Copy `.env.example` → `.env`, fill in tokens.
5. `task deploy`

## Tasks

| Cmd | What |
|------|------|
| `task build` | Build binary |
| `task test` | Run all tests |
| `task deploy` | `docker compose up -d --build` |
| `task redeploy` | Rebuild and recreate |
| `task kill` | Stop bot |
| `task logs` | Tail logs |
| `task status` | Container status |
```

- [ ] **Step 3: Commit**

```bash
git add -A
git commit -m "docs: README + Slack app manifest"
```

---

## Task 32: Dockerfile + docker-compose

**Files:**
- Create: `Dockerfile`
- Create: `docker-compose.yml`
- Create: `entrypoint.sh`

- [ ] **Step 1: Write `Dockerfile`**

```dockerfile
# Build stage
FROM golang:1.25-alpine AS builder
WORKDIR /src
RUN apk add --no-cache git ca-certificates
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/deferred-work-bot ./...

# Runtime stage — needs node for the `claude` CLI
FROM node:22-alpine
RUN apk add --no-cache ca-certificates tini && \
    npm install -g @anthropic-ai/claude-code
WORKDIR /app
COPY --from=builder /out/deferred-work-bot /usr/local/bin/deferred-work-bot
COPY projects.yaml signals.yaml ./
COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh
EXPOSE 8080
ENTRYPOINT ["/sbin/tini", "--", "/entrypoint.sh"]
```

- [ ] **Step 2: Write `entrypoint.sh`**

```sh
#!/bin/sh
set -e
mkdir -p /data
exec /usr/local/bin/deferred-work-bot
```

- [ ] **Step 3: Write `docker-compose.yml`**

```yaml
services:
  bot:
    build: .
    env_file: .env
    volumes:
      - ./data:/data
      - ${HOST_HOME:-/home/vuifhaolain}/.claude:/root/.claude:ro
    ports:
      - "${HEALTH_PORT:-8080}:8080"
    restart: unless-stopped
```

- [ ] **Step 4: Smoke build**

Run: `docker compose build`
Expected: succeeds.

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "feat: Dockerfile + docker-compose for scalable deployment"
```

---

## Verification checklist

After Task 32, run the full suite and a smoke deploy:

- [ ] `task test` — all tests green
- [ ] `task build` — binary builds
- [ ] `docker compose build` — image builds
- [ ] In a sandbox Slack workspace + sandbox Jira project: post a message in the watched channel, get :eyes:, get 3 approvals from different users, observe proposal post, react `:white_check_mark:` on proposal, see Jira ticket created and Slack link posted, item reaches `ticketed`.
- [ ] Repeat with an "encompassed" branch (force by faking a related ticket in Jira) — see `comment` / `new` / `both` flow.
- [ ] Manually mark an item's `created_at` to 11 days ago in SQLite, restart bot, verify warning post.
- [ ] Manually mark to 14 days ago with `warning_posted_at` set, verify archive.
- [ ] `curl localhost:8080/health` returns 200.
- [ ] `curl localhost:8080/metrics` shows queue + status counts.
- [ ] `curl -X POST -H "Authorization: Bearer $TRIGGER_TOKEN" "localhost:8080/trigger?item_id=1&action=propose"` returns 202 and enqueues.

## Open follow-ups (not in scope for v1)

- Replace personal `JIRA_API_TOKEN` with a service-account token.
- Web dashboard reading from SQLite + `events` log.
- Cross-team support beyond QORK.
- Integration with the `defer-work` skill so deferred-work docs in `secondbrain/<project>/deferred-work/` get auto-filed as items.
