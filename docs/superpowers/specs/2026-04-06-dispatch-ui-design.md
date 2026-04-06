# Dispatch UI — Design Spec

**Date:** 2026-04-06
**Status:** Draft

## Overview

A local web application for managing the plan dispatch system. Go HTTP server (`cmd/dispatch-ui/`) serves a vanilla HTML/JS/CSS frontend on localhost. Replaces `dispatch.sh` + cron with a Go-native orchestrator that provides a dashboard, live log streaming, and Opus-powered task creation.

## Goals

1. See all tasks at a glance — pending, running, done, failed
2. Create new tasks: write a short description, have a configurable LLM generate the full plan, review/edit, dispatch
3. Watch live agent output as tasks execute
4. Cancel running tasks, retry failed ones, adjust priority of pending ones
5. Keep the same plan file format (`.md` with YAML frontmatter) for backwards compatibility

## Non-Goals

- Git integration (branch management, diffs, PRs) — deferred
- Multi-user / remote access — localhost only for now
- Persistent task history / database — plan files on disk are the source of truth
- Desktop app wrapper (Electron/Tauri) — deferred, but the architecture should not prevent it

## Architecture

### Go Server (replaces dispatch.sh)

A single Go binary in `cmd/dispatch-ui/` that:

1. **Serves the web UI** — static HTML/JS/CSS embedded via `embed.FS`
2. **Manages the task queue** — reads `~/.claude/dispatch/plans/*.md`, watches for changes via fsnotify
3. **Executes tasks** — spawns `claude --bare` as child processes (replacing the cron + bash approach)
4. **Streams logs** — captures child process stdout/stderr and forwards via WebSocket
5. **Generates plans** — calls `claude --bare` with a meta-prompt to expand short descriptions into full plans

### Directory Structure

```
cmd/dispatch-ui/
  main.go          — HTTP server, routes, WebSocket handler
  dispatch.go      — task queue, process management, execution engine
  plangen.go       — plan generation (calls Claude CLI for Opus expansion)
  frontmatter.go   — YAML frontmatter parser/writer (port from dispatch.sh)
  static/          — embedded frontend files
    index.html
    app.js
    style.css
```

### Config

Reads the existing `~/.claude/dispatch/dispatch.conf` for `PLANS_DIR`, `REPORTS_DIR`, etc. Adds:

```
# dispatch.conf additions
LISTEN_ADDR="127.0.0.1:8090"
PLANNING_MODEL="claude-opus-4-6"      # model for generating plans (configurable)
PLANNING_API_KEY_ENV="ANTHROPIC_API_KEY"  # auth for planning model
PLANNING_BASE_URL=""                   # empty = direct Anthropic
```

The `.env` file continues to hold API keys.

### Plan File Format (unchanged)

```yaml
---
title: "Add unit tests for sketch.go"
status: pending          # pending | running | done | failed | cancelled
project: /Users/donny/Projects/2026/geoffreyengram
model: glm-5.1
api_key_env: ZAI_API_KEY
base_url: https://api.z.ai/api/anthropic
branch: dispatch/test-sketch
priority: 2
max_runtime: 10m
created: 2026-04-06
depends_on: []
allowed_tools: Edit,Write,Bash,Read,Glob,Grep
---

## Context
...

## Instructions
...
```

New fields added by the server (backwards compatible — dispatch.sh ignores unknown fields):
- `started_at`, `finished_at`, `exit_code`, `report` — same as dispatch.sh already writes
- `created_by: ui` — indicates the plan was created via the UI (vs hand-authored)
- `planning_model: claude-opus-4-6` — which model generated the plan body

## UI Design

### Layout

Two-panel layout:

- **Left sidebar (340px)** — task queue, ordered by status (running first, then pending by priority, then done/failed by recency). Each entry shows: status dot (color-coded), task name, model, timing info.
- **Right panel (flex)** — detail view for selected task. Contains:
  - Header: task name, status badge, model, branch, timing
  - Action buttons: View Plan, Cancel (if running), Retry (if failed), Edit Priority (if pending)
  - Content area: live streaming log (if running), report content (if done/failed), plan preview (if pending)
- **Top bar** — "Dispatch" title, task count badge, "+ New Task" button

### Status Colors

- Running: orange (#f0883e), pulsing dot
- Pending: gray (#8b949e)
- Done: green (#3fb950)
- Failed: red (#f85149)
- Cancelled: gray, struck-through text

### Task Creation Flow

1. Click "+ New Task" — a modal/slide-over appears with:
   - **Description** (textarea) — short prompt describing the task
   - **Project** — dropdown, auto-populated from projects seen in existing plans
   - **Executor Model** — dropdown: glm-5.1, sonnet-4.6, opus-4.6 (with corresponding auth)
   - **Priority** — 1-5, default 3
   - **Max Runtime** — select: 10m, 25m, 30m, 1h
   - **Allowed Tools** — text input, default "Edit,Write,Bash,Read,Glob,Grep"

2. Click "Generate Plan" — server calls the planning model (configurable, defaults to Opus) via Claude CLI:
   ```
   claude --bare -p "<meta-prompt with description + project context>" \
     --output-format text --no-session-persistence
   ```
   The meta-prompt instructs the model to produce a plan in the established format (context, key files to read first, step-by-step instructions, acceptance criteria). The server gathers project context to include in the meta-prompt:
   - The project's `.claude/CLAUDE.md` (if it exists)
   - Output of `git log --oneline -20` in the project directory
   - Output of `find . -type f -name '*.go' -o -name '*.ts' | head -50` (or similar file listing)
   - An example plan from `PLANS_DIR` (the most recent `done` plan for the same project, if any)

   The meta-prompt template is a hardcoded string in `plangen.go` that wraps this context around the user's description.

3. **Review pane** — the generated plan body appears in a textarea/editor. The user can edit freely. Frontmatter is shown as form fields above (pre-filled from step 1).

4. Click "Dispatch" — server writes the `.md` file to `PLANS_DIR` with `status: pending` and the task enters the queue.

### Live Log Streaming

When a task is running, the right panel shows a terminal-style log viewer:

- WebSocket connection from browser to server
- Server pipes child process stdout/stderr line by line
- Auto-scroll to bottom, with scroll-lock if user scrolls up
- Monospace font, basic ANSI color support (green for success, red for errors, blue for tool calls)
- Log is also written to the report file on disk (same as dispatch.sh does)

### Task Actions

- **Cancel** (running tasks) — sends SIGTERM to the child process, waits 5s, then SIGKILL. Sets status to `cancelled`.
- **Retry** (failed/cancelled tasks) — resets status to `pending`, clears `started_at`/`finished_at`/`exit_code`. Optionally allows editing the plan before retry.
- **Edit Priority** (pending tasks) — inline edit of priority field
- **Delete** (any status) — removes the plan file (with confirmation)
- **View Plan** — shows the full plan markdown in a read-only viewer
- **View Report** — shows the report file content (for done/failed tasks)

## Execution Engine

Ported from dispatch.sh logic:

1. **Poll loop** — every 5 seconds, scan `PLANS_DIR` for `status: pending` plans
2. **Dependency check** — if `depends_on` is set, verify all dependencies are `done`
3. **Claim** — set `status: running`, `started_at` timestamp
4. **Environment** — resolve API key from env var, set `ANTHROPIC_AUTH_TOKEN` or `ANTHROPIC_API_KEY` based on whether `base_url` is set
5. **Branch** — `git checkout -b <branch>` in the project directory (non-fatal if exists)
6. **Execute** — spawn `claude --bare -p "<plan body>" --allowed-tools "<tools>" --output-format text --no-session-persistence` with the constructed environment
7. **Timeout** — context with deadline; kill process on timeout (exit code 124)
8. **Capture** — pipe stdout/stderr to: (a) WebSocket broadcast, (b) report file, (c) in-memory buffer
9. **Finalize** — set `status: done|failed`, `finished_at`, `exit_code`, `report` path

Sequential execution (one task at a time) to match current behavior. Parallel execution is a future enhancement.

## API Endpoints

```
GET  /                        — serves index.html
GET  /api/tasks               — list all tasks (parsed from plan files)
GET  /api/tasks/:name         — single task detail
POST /api/tasks               — create a new task (writes plan file)
PUT  /api/tasks/:name         — update task (priority, status changes)
DELETE /api/tasks/:name       — delete task (removes plan file)
POST /api/tasks/:name/cancel  — cancel running task
POST /api/tasks/:name/retry   — retry failed task
POST /api/generate-plan       — generate plan body from description (calls planning model)
GET  /api/config              — get dispatch config (projects, models, defaults)
WS   /ws/logs/:name           — WebSocket stream of live logs for a task
```

## Testing Strategy

- **Unit tests** for frontmatter parsing, task state management
- **Integration test** with a mock claude binary (shell script that echoes output and exits)
- **Manual testing** via the browser

## Future Enhancements (not in v1)

- Parallel task execution (configurable concurrency)
- Git diff viewer for completed tasks
- Task templates (saved descriptions for common tasks)
- Notification support (macOS notifications via osascript)
- Electron/Tauri wrapper
- Authentication for non-localhost access
