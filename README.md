# deferred-work-bot

Slack bot that tracks deferred work, gates it behind a 3-approval vote, drafts Jira tickets via the local `claude` CLI (with related-ticket detection), and files them on a final 1-approval vote.

## How it works

1. Post a deferred-work item in the dedicated channel (or `@deferred-work-bot <text>` in any invited channel).
2. The bot decides whether the message actually proposes trackable work: a cheap length/content prefilter (`PROPOSAL_MIN_WORDS`, default 4) drops obvious chatter, then Claude judges the rest. Only accepted messages are tracked — an `@mention` always bypasses the gate. (If Claude is unavailable the gate fails open, so real work is never silently dropped.)
3. Once tracked, the bot seeds the approve/cancel vote reactions.
4. Three unique-user approvals (reactions or reply keywords) trigger a proposal draft.
5. The bot posts the draft + related Jira tickets back to the thread.
6. One approval reaction on the proposal files the ticket (or comments on an existing one).

The dashboard (`/` on the health port) lists every item and has a **Cancel** button per in-flight item; cancelling also drops a `:wastebasket:` on the original Slack message.

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
