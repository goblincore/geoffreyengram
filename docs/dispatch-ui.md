# Dispatch UI

A web dashboard for managing headless Claude Code agent tasks. Replaces the `dispatch.sh` + cron system with a Go-native orchestrator.

## Quick start

```bash
go build -o ~/go/bin/dispatch-ui ./cmd/dispatch-ui/
dispatch-ui
# Open http://localhost:8090
```

The dashboard reads your existing `~/.claude/dispatch/plans/` directory. Existing plan files appear automatically.

## Features

- **Task queue** — see pending, running, done, and failed tasks at a glance
- **Live log streaming** — watch agent output in real-time via SSE
- **Task creation** — write a short description, Opus generates the full plan, review and dispatch
- **Task management** — cancel running tasks, retry failed ones, adjust priority, delete
- **Multi-harness** — dispatch to Claude CLI, Pi, or OpenCode (set `harness:` in plan frontmatter)
- **Dualmem integration** — Pi tasks get project memory injected + CLI instructions for saving memories

## Creating a task from the UI

1. Click **"+ New Task"**
2. Fill in: task name, short description, project path, executor model, priority, max runtime
3. Click **"Generate Plan"** — the planning model (default: Opus via your subscription) expands the description into a full plan with context, instructions, and acceptance criteria
4. Review/edit the generated plan
5. Click **"Dispatch"** — the task enters the queue and runs on the next poll cycle (5s)

## Creating a task manually

Drop a `.md` file into `~/.claude/dispatch/plans/` with this format:

```yaml
---
title: "My task title"
status: pending
project: /path/to/your/project
model: glm-5.1
harness: claude          # "claude" (default), "pi", or "opencode"
priority: 2
max_runtime: 25m
allowed_tools: Edit,Write,Bash,Read,Glob,Grep
setup: "pnpm install --frozen-lockfile --prefer-offline"  # optional: provisioning run in the worktree before the agent; "none"/"skip" to opt out; falls back to DEFAULT_SETUP_CMD
setup_timeout: 5m        # optional: max duration for the setup command (default 5m); falls back to DEFAULT_SETUP_TIMEOUT
---

## Context
What this task is about.

## Instructions
Step-by-step instructions for the agent.

## Acceptance criteria
- Tests pass
- Code compiles
```

## Configuration

The server reads `~/.claude/dispatch/dispatch.conf`:

```bash
PLANS_DIR="$HOME/.claude/dispatch/plans"
REPORTS_DIR="$HOME/.claude/dispatch/reports"
CLAUDE_BIN="$HOME/.local/bin/claude"
DEFAULT_MODEL="glm-5.1"
LISTEN_ADDR="127.0.0.1:8090"
PLANNING_MODEL="claude-opus-4-6"
DEFAULT_SETUP_CMD="pnpm install --frozen-lockfile --prefer-offline"  # run in each fresh worktree before the agent
DEFAULT_SETUP_TIMEOUT="5m"                                           # bound for the setup command
```

### Worktree setup hook

Each task runs in a fresh `git worktree`, which starts without gitignored
build artifacts like `node_modules`. Set `DEFAULT_SETUP_CMD` (project-wide) or a
per-task `setup:` field to provision the worktree once, before the agent starts —
so the agent doesn't waste a turn discovering a bare checkout and reinstalling on
every task. The command runs via `sh -c` in the worktree root with the task's
environment; its output streams to the task log prefixed `[dispatch][setup]`. A
non-zero exit (or a timeout) fails the task fast — before any agent tokens are
spent — and preserves the worktree for inspection. Set `setup: none` (or `skip`)
on a task to opt out of an inherited default.

API keys go in `~/.claude/dispatch/.env`:

```bash
export ZAI_API_KEY="your-key"
export ANTHROPIC_API_KEY="your-key"
export GEMINI_API_KEY="your-key"
```

## Harness comparison (GLM 5.1)

| Harness | Time | Streaming | Edit tool | Notes |
|---------|------|-----------|-----------|-------|
| **Pi** | 1m 50s | Real-time | Native works | Recommended. Minimal, fast, no Edit issues |
| **Claude CLI** | 4m 37s | Real-time | Needs Python workaround | Auto-injected preamble tells GLM to use Python for edits |
| **OpenCode** | 15m+ timeout | Buffers until exit | N/A | Not recommended — stdout buffering makes it unusable |
