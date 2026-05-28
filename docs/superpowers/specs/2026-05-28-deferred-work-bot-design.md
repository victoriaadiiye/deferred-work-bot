# deferred-work-bot — Design

**Date:** 2026-05-28
**Status:** Approved
**Owner:** @vuifhaolain

## Problem

Deferred work surfaced during PR reviews, design discussions, and meetings often gets lost. There is no lightweight way to (a) park the idea where the team can see it, (b) get social proof that it's worth tracking, and (c) push it into Jira without manual rewriting. The bot closes that loop by turning a Slack message into a Jira ticket when the team agrees the work matters.

## Goals

- Lightweight capture in a dedicated Slack channel (and invite-only operation elsewhere).
- Social-proof gate: 3 unique-user approvals before any ticket is drafted.
- Automated ticket drafting from the original message plus thread comments.
- Detect overlap with existing Jira work before filing a duplicate.
- Reviewable proposal step — bot posts the draft back to the thread before filing.
- Operate as a single, dockerized Go binary, mirroring `pr-review-bot`'s deployment shape.

## Non-goals

- Replacing a full project-management tool.
- Cross-team or cross-org routing (QORK teamspace only for v1).
- Persistent threaded conversation with the bot beyond the documented `@bot` commands.
- Web UI dashboard beyond the `/health` and `/metrics` endpoints.

## Stack

- Go 1.22+, `slack-go/slack` (Socket Mode).
- SQLite (single-file, persisted via Docker volume).
- Shells out to local `claude` CLI for LLM work — no `ANTHROPIC_API_KEY` required.
- Jira Cloud REST API v3.
- Docker + docker-compose; `Taskfile.yaml` for ops.

## Architecture

```
                ┌──── Slack Events (Socket Mode) ──────┐
                │                                       │
   message/reaction/                              ┌────────────┐
   reply event ──┼──► event handler ──── enqueue ►│ job queue  │
                │                                  └─────┬──────┘
                │                                        ▼
                │                                  ┌────────────┐
                │                                  │worker pool │── shells ──► `claude` CLI
                │                                  │  (N=WORKERS)│── HTTP ───► Jira REST
                │                                  └─────┬──────┘
                │                                        │
                │            ┌──── SQLite ────┐          │
                │            │ items, votes,  │◄─────────┘
                │            │ proposals,     │
                │            │ tickets,events │
                │            └────────────────┘
                │
                └──── ticker (5min): TTL reminders, warnings, archive
```

Single binary. Event router is thin and writes to the queue; workers do all I/O-heavy work (Claude shells, Jira calls).

## State model (SQLite)

```sql
items
  id INTEGER PK
  slack_channel TEXT
  slack_ts TEXT UNIQUE          -- original message ts
  author_slack_id TEXT
  text TEXT
  subproject TEXT               -- qompass|qatalyst|... or NULL
  status TEXT                   -- collecting|proposing|proposed|ticketed|cancelled|archived
  approval_threshold INT        -- default 3
  last_reminder_at TIMESTAMP
  warning_posted_at TIMESTAMP
  created_at, updated_at TIMESTAMP

votes
  item_id FK
  user_slack_id TEXT
  source TEXT                   -- reaction|reply
  signal TEXT                   -- white_check_mark|claude-it|+1|approve|lgtm|...
  created_at TIMESTAMP
  UNIQUE(item_id, user_slack_id)

proposals
  id INTEGER PK
  item_id FK
  slack_ts TEXT                 -- bot's proposal message ts
  draft_json TEXT               -- {project, type, summary, description, labels, priority}
  related_tickets_json TEXT     -- [{key, score, verdict: encompassed|referenced|unrelated}]
  branch TEXT                   -- new|comment_on_existing|awaiting_resolution
  existing_ticket_key TEXT      -- when branch=comment_on_existing
  status TEXT                   -- draft|awaiting_resolution|approved|filed|rejected
  created_at, updated_at TIMESTAMP

tickets
  proposal_id FK
  jira_key TEXT
  jira_url TEXT
  action TEXT                   -- created|commented_on_existing
  existing_ticket_key TEXT
  created_at TIMESTAMP

events
  id INTEGER PK
  item_id FK NULL
  kind TEXT                     -- vote|reminder|warning|cancel|archive|proposal|ticket|error
  payload_json TEXT
  created_at TIMESTAMP
```

`events` is the audit log — full timeline reconstructable for any item.

## State transitions

```
                           ┌─────────────┐
   new top-level msg ────►│ collecting  │
   (or @mention in        └─────┬───────┘
    invited channel)            │
                                │ vote count ≥ 3
                                │   OR @bot file now
                                ▼
                          ┌─────────────┐
                          │  proposing  │ ── worker: draft + Jira search
                          └─────┬───────┘
                                │ proposal posted
                                ▼
                          ┌─────────────┐
                          │  proposed   │
                          └──┬───┬───┬──┘
                             │   │   │ proposal approval (1 vote)
                             │   │   │ + branch=new
                             │   │   ▼
                             │   │  ┌─────────────┐
                             │   │  │  ticketed   │
                             │   │  └─────────────┘
                             │   │
                             │   │ @bot regen, edits in thread, override commands
                             │   └──► back to `proposing`
                             │
                             ▼ branch=comment_on_existing path
                          ┌─────────────────┐
                          │ commented_on_   │  ─► Jira: post comment + label
                          │ existing        │     `deferred-work-followup`
                          └─────────────────┘

   From `collecting` any time:
     :x: reaction OR @bot cancel ──► cancelled
     no movement: warning at 10d idle, archive at 13d idle (10d + 3d grace)
```

Reminder ticker (every 5min) repost-on-channel: for items in `collecting`, repost original at `last_reminder_at + 3d` (first repost at 3d from creation). At 10d total idle, post warning with red/warning emojis. At 13d idle, mark `archived`.

If approvals arrive after warning but before archive deadline, normal flow resumes — proposal triggers as soon as threshold met.

Terminal states (`ticketed`, `commented_on_existing`, `cancelled`, `archived`) ignore all subsequent thread events.

## Slack event dispatch

| Event | Handler |
|-------|---------|
| `message` (top-level, watched channel, no thread_ts) | Insert `items` row, status=`collecting`. React `:eyes:`. |
| `message` (thread reply, parent is tracked item) | Approve keywords → upsert vote. `@bot <cmd>` → dispatch command. Resolution keywords (`comment`/`new`/`both`) if proposal awaiting resolution. |
| `app_mention` (non-watched channel, top-level) | Treat as new item; author = mentioner. Status=`collecting`. |
| `app_mention` (in thread of tracked item) | Same path as thread reply command dispatch. |
| `reaction_added` | If on tracked item or proposal: route by signal. Approve set → vote. `:x:` → cancel. |
| `reaction_removed` | Decrement vote if removed reaction was an approval. |
| `message_changed` | Re-parse text on tracked item; regen will pick up new content. |
| `message_deleted` | Mark item `cancelled`. |

## Signals (configurable, default in `signals.yaml`)

- **Approve reactions:** `:white_check_mark:`, `:claude-it:`, `:+1:`, `:thumbsup:`
- **Approve reply tokens** (case-insensitive, word-bounded): `approve`, `approved`, `+1`, `lgtm`, `:white_check_mark:`, `:claude-it:`
- **Cancel:** `:x:` reaction OR reply containing `@bot cancel`

Voting rules:
- Bot reactions never count.
- Author self-vote excluded.
- Dedup per user across reaction + reply (one vote per user per item).
- Removing a reaction decrements the count.

## `@bot` thread commands

| Command | Behavior |
|---------|----------|
| `@bot status` | Reply with vote count, voters, days idle, current status, next reminder time. |
| `@bot cancel` | Mark `cancelled`. React `:wastebasket:` on item. |
| `@bot regen` | If proposal exists: re-draft using updated thread context. Old proposal marked `rejected`, new posted. |
| `@bot project: <name>` | Override `subproject`. Regen if proposal exists. |
| `@bot priority: <low\|medium\|high>` | Override priority. Regen if proposed. |
| `@bot file now` | Bypass vote gate. Transition `collecting`→`proposing` immediately. |
| `@bot search` | Re-run Jira related-ticket search; post updated relevance summary. |
| `@bot help` | List all commands. |
| `@bot <freeform>` | Shell to `claude` with item + thread context + question; post reply. |

## Drafting + Jira flow

Triggered on entry to `proposing`. One worker job per transition.

1. **Load context:** item + full thread (Slack `conversations.replies`) + accumulated `@bot` overrides.
2. **Detect sub-project** (fallback chain):
   - (a) Keyword scan in msg + thread against `projects.yaml` list.
   - (b) If none: shell to `claude` — "infer subproject from: <text>".
   - (c) If still none: search QORK unlabeled only.
3. **Jira related-ticket search:**
   - Extract keywords (Claude or stopword filter).
   - JQL:
     ```
     project in (<JIRA_QORK_PROJECTS>)
     AND (statusCategory != Done OR resolved > -90d)
     AND (labels = "<subproject>" OR labels is EMPTY)
     AND text ~ "<kw1> OR <kw2> ..."
     ORDER BY updated DESC
     LIMIT 20
     ```
   - Shell to `claude` with item text + top 20 ticket `{key, summary, description}`. Get JSON: each classified `encompassed | referenced | unrelated`.
4. **Branch decision:**
   - Any `encompassed` → post resolution question, set proposal `branch=awaiting_resolution`. Reply keywords `comment` / `new` / `both` resolve. (`both` = comment on existing AND file new.)
   - Only `referenced` → include as related links in proposal, branch=`new`.
   - All `unrelated` → branch=`new`.
5. **Draft ticket (branch=new or both):**
   - Shell to `claude` with item text + thread + sub-project + overrides.
   - Output JSON: `{summary, description, issue_type: "Task", labels: ["deferred-work", subproject], priority}`.
6. **Post proposal to Slack thread:**
   - If TTL-triggered (3-day no-quorum repost is NOT a proposal; proposals are only triggered by vote threshold or `@bot file now`).
   - Body: summary, description preview, labels, priority, related tickets section.
   - Footer: "React with any approve signal to file. `@bot regen` to revise."
   - Save `proposals` row, status=`draft` or `awaiting_resolution`.
7. **On proposal approval (1 vote signal):**
   - `branch=new` → Jira `POST /rest/api/3/issue` with reporter=`JIRA_EMAIL`'s creds, assignee=null.
   - `branch=comment_on_existing` → `POST /rest/api/3/issue/{key}/comment` with synthesized context + add label `deferred-work-followup` via `PUT /rest/api/3/issue/{key}`.
   - `branch=both` → both of the above.
   - Post result link(s) in thread. React `:white_check_mark:` on original item. Lock item.
8. **Failure handling:**
   - Jira/Claude error → log to `events`, post error in thread, item stays `proposed`. Retry via `@bot regen` or new approval reaction.

## Ticket field policy

| Field | Source |
|-------|--------|
| Project | `JIRA_QORK_PROJECTS` (single project `QORK` for v1) |
| Issue type | `Task` |
| Summary | Claude-synthesized |
| Description | Claude-synthesized: original msg + each thread comment + Slack permalink |
| Labels | `deferred-work` + sub-project label (`qompass`, `qatalyst`, …) |
| Priority | Default `Medium`; overridable via `@bot priority:` |
| Assignee | Unassigned (always) |
| Reporter | `JIRA_EMAIL` (your personal creds for v1) |

## Deployment

### Layout

```
deferred-work-bot/
├── main.go                  # entry, event router wiring, ticker
├── slack.go                 # event handlers, command dispatch, message builders
├── store.go                 # SQLite schema, queries, migrations
├── worker.go                # bounded worker pool, job types
├── propose.go               # drafting + Jira flow
├── jira.go                  # JQL search, create-issue, comment
├── claude.go                # shell to `claude` CLI, JSON parsing
├── signals.go               # approve/cancel signal matching
├── health.go                # /health, /metrics, POST /trigger
├── projects.yaml            # subproject → label map; QORK project keys
├── signals.yaml             # configurable approve/cancel signal sets
├── slack-manifest.yaml      # Slack app scopes + events
├── Dockerfile
├── docker-compose.yml
├── Taskfile.yaml
├── *_test.go
└── README.md
```

### Environment (`.env`)

```
SLACK_APP_TOKEN=xapp-...
SLACK_BOT_TOKEN=xoxb-...
WATCHED_CHANNELS=C0123ABCDEF
JIRA_BASE_URL=https://qumulo.atlassian.net
JIRA_EMAIL=vuifhaolain@qumulo.com
JIRA_API_TOKEN=...
JIRA_QORK_PROJECTS=QORK
APPROVAL_THRESHOLD=3
REMINDER_INTERVAL_DAYS=3
WARNING_AT_DAYS=10
ARCHIVE_GRACE_DAYS=3
WORKERS=2
QUEUE_SIZE=64
SQLITE_PATH=/data/state.db
HEALTH_PORT=8080
```

### Docker

Multi-stage build: Go binary + `claude` CLI in final image. Mounts: `~/.claude:/root/.claude:ro`, `./data:/data` (SQLite). `restart: unless-stopped`.

### Endpoints

- `GET /health` — `200` if DB + Slack reachable; `503` otherwise.
- `GET /metrics` — Prometheus-style: queue depth, item counts by status, job latency histograms, Jira error counter.
- `POST /trigger` — auth via shared token; manual nudge for an item (force reminder / force propose / force archive).

### Graceful shutdown

SIGINT/SIGTERM → stop Slack event ingestion → drain worker queue (timeout: 60s) → close DB → exit.

## Testing strategy

- `store_test.go` — schema migrations, vote dedup, transition guards.
- `signals_test.go` — table-driven signal matching (variants of approve/cancel/keywords).
- `slack_test.go` — fake `SlackAPI` interface (same shape as pr-review-bot). Drive events through router; assert state + outbound calls.
- `propose_test.go` — fake `claude` shell + fake Jira client. Assert JQL, branch decisions, payload shape.
- `jira_test.go` — `httptest.Server`, request body assertions for create-issue and add-comment.
- `worker_test.go` — pool drain behavior, queue-full handling, shutdown.

No real Slack/Jira in CI. Manual smoke via `docker-compose up` against a dev Slack workspace and a sandbox Jira project.

## Open items / future

- v2: rotate reporter from personal creds to a service account.
- v2: dashboard UI for archived items / triage backlog.
- v2: cross-team support beyond QORK.
- v2: integration with `defer-work` skill — automatically file deferred-work docs from `secondbrain/<project>/deferred-work/` as items.
