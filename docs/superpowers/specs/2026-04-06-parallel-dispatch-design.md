# Parallel Task Dispatch & Worktree Cleanup

**Date:** 2026-04-06
**Status:** Draft
**Context:** The dispatch engine currently runs one task at a time. Since each task already runs in its own git worktree, there's no technical reason for sequential execution. Tasks with independent dependency graphs should run in parallel.

## Problem

1. **Sequential execution wastes time.** Tasks 1, 3, and 5 of the benchmark fixes have no dependencies on each other, but the engine runs them one at a time because `Engine.running` is a single `*RunningTask`.
2. **Worktrees accumulate.** Completed task worktrees are preserved indefinitely at `~/.claude/dispatch/worktrees/`. Successful tasks don't need their worktrees — only failed tasks benefit from preservation for debugging.

## Design

### 1. Engine: Pluralized Running Map

Replace `running *RunningTask` with `running map[string]*RunningTask` keyed by plan name.

**New fields on `DispatchConfig`:**
- `MaxConcurrent int` — max parallel tasks (default 2). Parsed from `MAX_CONCURRENT` in `dispatch.conf`.
- `AutoCleanupWorktrees bool` — remove worktrees after successful tasks (default true). Parsed from `AUTO_CLEANUP_WORKTREES`.

**New methods on `Engine`:**
- `IsAtCapacity() bool` — `len(e.running) >= e.Config.MaxConcurrent`
- `IsRunning(name string) bool` — checks if a specific plan is in the running map
- `CancelTask(name string) bool` — cancels a specific running task by name (replaces `CancelRunning()`)

**`NewEngine` change:** Initialize `running` as `make(map[string]*RunningTask)`.

### 2. Poll Queue: Multi-Dispatch

Current `pollQueue` skips if anything is running. New behavior:

```
every 5 seconds:
    lock, snapshot running count, unlock
    if at capacity → skip

    slots = maxConcurrent - runningCount
    list all pending plans with deps met AND not already running
    sort by priority (existing sort)
    take first `slots` plans
    launch each in its own goroutine
```

Dependency checking (`dependenciesMet`) is unchanged — it already requires dep plans to have `status: "done"`. A task whose dependency is still running naturally won't be eligible.

The 5-second poll interval means worst case we slightly over-launch by 1 task if two poll cycles overlap. This is acceptable — the over-launch resolves on the next cycle and the extra task still runs in its own worktree safely.

### 3. ExecutePlan: Self-Registration and Worktree Cleanup

**Self-registration:** `ExecutePlan` adds itself to the running map at start and removes itself via `defer`:

```go
e.mu.Lock()
e.running[plan.Name] = &RunningTask{Plan: plan, Cancel: cancel}
e.mu.Unlock()

defer func() {
    e.mu.Lock()
    delete(e.running, plan.Name)
    e.mu.Unlock()
}()
```

**Worktree cleanup:** After updating frontmatter with final status, if the task succeeded and `AutoCleanupWorktrees` is true:

```go
if status == "done" && e.Config.AutoCleanupWorktrees && workDir != plan.Project {
    exec.Command("git", "-C", plan.Project, "worktree", "remove", workDir).Run()
    lb.Append(fmt.Sprintf("[dispatch] worktree cleaned up (branch %s preserved for merge)", branch))
} else if workDir != plan.Project {
    lb.Append(fmt.Sprintf("[dispatch] worktree preserved at %s for inspection", workDir))
}
```

Rule: **success = remove worktree directory, preserve branch** (branch has the task's commits and needs to be merged manually or via a later integration task). **Failure = preserve both** worktree and branch for debugging.

### 4. Server/API Changes

**`handleCancelTask`:** Look up the specific task in the running map instead of checking the single running pointer:

```go
s.eng.mu.Lock()
task, exists := s.eng.running[name]
s.eng.mu.Unlock()
if !exists {
    writeError(w, http.StatusConflict, "task is not currently running")
    return
}
task.Cancel()
```

**`APITask`:** Add `Harness string` field so the UI shows which harness each task uses.

**No other API or UI changes needed.** The list endpoint already handles multiple running tasks. SSE logs are already keyed by plan name. The UI already renders status indicators per-task.

## Files Modified

| File | Changes |
|---|---|
| `cmd/dispatch-ui/config.go` | Add `MaxConcurrent`, `AutoCleanupWorktrees` to `DispatchConfig`; parse from conf |
| `cmd/dispatch-ui/engine.go` | `running` becomes `map[string]*RunningTask`; add `IsAtCapacity()`, `IsRunning()`, `CancelTask()`; update `NewEngine` |
| `cmd/dispatch-ui/exec.go` | Self-register/deregister in map; worktree auto-cleanup on success |
| `cmd/dispatch-ui/main.go` | `pollQueue` dispatches multiple tasks per cycle |
| `cmd/dispatch-ui/server.go` | `handleCancelTask` uses map lookup; add `Harness` to `APITask` |

## Config Additions

```
MAX_CONCURRENT="2"
AUTO_CLEANUP_WORKTREES="true"
```

## Testing

- **Manual test:** Create 3 independent pending plans, start dispatch, verify 2 run simultaneously and the 3rd starts when one finishes.
- **Dependency test:** Create plan A (no deps) and plan B (depends on A). Verify B doesn't start until A is done.
- **Cleanup test:** Run a task that succeeds, verify worktree is removed. Run one that fails, verify worktree is preserved.
- **Cancel test:** Start 2 tasks, cancel one by name, verify the other continues.
- **Build test:** `go build ./cmd/dispatch-ui/` succeeds.

## Success Criteria

1. Two tasks run simultaneously when both are eligible (no dependency conflicts)
2. `MAX_CONCURRENT` config is respected
3. Successful task worktrees are auto-removed
4. Failed task worktrees are preserved
5. Individual task cancellation works
6. Existing single-task behavior is unchanged when `MAX_CONCURRENT=1`
