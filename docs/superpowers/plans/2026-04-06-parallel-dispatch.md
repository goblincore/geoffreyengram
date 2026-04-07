# Parallel Task Dispatch Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Enable the dispatch engine to run multiple tasks concurrently (up to a configurable limit) and auto-clean worktrees on success.

**Architecture:** Replace the single `running *RunningTask` pointer with a `map[string]*RunningTask`. `pollQueue` dispatches up to `MaxConcurrent - len(running)` eligible tasks per cycle. `ExecutePlan` self-registers/deregisters via defer. Worktrees are removed on success, preserved on failure.

**Tech Stack:** Go, git worktrees, existing dispatch-ui codebase.

**Spec:** `docs/superpowers/specs/2026-04-06-parallel-dispatch-design.md`

---

### Task 1: Add config fields for concurrency and worktree cleanup

**Files:**
- Modify: `cmd/dispatch-ui/config.go:10-28` (DispatchConfig struct)
- Modify: `cmd/dispatch-ui/config.go:41-76` (parseShellFile switch)

- [ ] **Step 1: Add fields to DispatchConfig**

In `cmd/dispatch-ui/config.go`, add two fields to the `DispatchConfig` struct (after `PlanningBaseURL`):

```go
type DispatchConfig struct {
	PlansDir            string
	ReportsDir          string
	LogFile             string
	DefaultModel        string
	DefaultAuthTokenEnv string
	DefaultBaseURL      string
	DefaultMaxRuntime   string
	ClaudeBin           string
	OpenCodeBin         string
	PiBin               string
	ExtraPath           string
	Preamble            string
	ListenAddr          string
	PlanningModel       string
	PlanningAPIKeyEnv   string
	PlanningBaseURL     string
	MaxConcurrent       int    // max parallel tasks (default 2)
	AutoCleanupWorktrees bool  // remove worktrees on success (default true)
	EnvVars             map[string]string
}
```

- [ ] **Step 2: Set defaults in loadDispatchConfig**

In `loadDispatchConfig`, add defaults alongside the existing ones (around line 33):

```go
cfg := &DispatchConfig{
	// Defaults
	ListenAddr:           "127.0.0.1:8090",
	PlanningModel:        "claude-opus-4-6",
	PlanningAPIKeyEnv:    "ANTHROPIC_API_KEY",
	MaxConcurrent:        2,
	AutoCleanupWorktrees: true,
	EnvVars:              make(map[string]string),
}
```

- [ ] **Step 3: Parse new config keys**

In the `parseShellFile` callback switch (around line 41-76), add cases:

```go
case "MAX_CONCURRENT":
	if n, err := strconv.Atoi(v); err == nil && n > 0 {
		cfg.MaxConcurrent = n
	}
case "AUTO_CLEANUP_WORKTREES":
	cfg.AutoCleanupWorktrees = v == "true" || v == "1" || v == "yes"
```

Add `"strconv"` to the imports if not already present. Check — `config.go` currently only imports `"bufio"`, `"os"`, `"strings"`. Add `"strconv"`.

- [ ] **Step 4: Verify build**

Run: `cd /Users/donny/Projects/2026/geoffreyengram && go build ./cmd/dispatch-ui/`
Expected: Build succeeds.

- [ ] **Step 5: Commit**

```bash
git add cmd/dispatch-ui/config.go
git commit -m "feat(dispatch): add MaxConcurrent and AutoCleanupWorktrees config"
```

---

### Task 2: Pluralize Engine.running to a map

**Files:**
- Modify: `cmd/dispatch-ui/engine.go:12-39` (Engine struct, RunningTask, NewEngine)

- [ ] **Step 1: Change `running` field to a map**

In `cmd/dispatch-ui/engine.go`, replace the Engine struct and NewEngine:

```go
// Engine manages plan execution and log streaming.
type Engine struct {
	Config  *DispatchConfig
	mu      sync.Mutex
	running map[string]*RunningTask // keyed by plan name
	logs    map[string]*LogBuffer
}

// RunningTask holds a currently executing plan and its cancel function.
type RunningTask struct {
	Plan   *Plan
	Cancel func()
}

// NewEngine creates a new Engine with the given config.
func NewEngine(cfg *DispatchConfig) *Engine {
	return &Engine{
		Config:  cfg,
		running: make(map[string]*RunningTask),
		logs:    make(map[string]*LogBuffer),
	}
}
```

- [ ] **Step 2: Add helper methods**

After `NewEngine`, add:

```go
// IsAtCapacity returns true if the engine is running the maximum number of concurrent tasks.
func (e *Engine) IsAtCapacity() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.running) >= e.Config.MaxConcurrent
}

// IsRunning returns true if the named plan is currently executing.
func (e *Engine) IsRunning(name string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, ok := e.running[name]
	return ok
}

// RunningCount returns the number of currently executing tasks.
func (e *Engine) RunningCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.running)
}

// CancelTask cancels a specific running task by name.
// Returns true if the task was found and cancelled.
func (e *Engine) CancelTask(name string) bool {
	e.mu.Lock()
	task, ok := e.running[name]
	e.mu.Unlock()
	if !ok {
		return false
	}
	task.Cancel()
	return true
}
```

- [ ] **Step 3: Remove old CancelRunning method**

Delete the old `CancelRunning` function (lines 626-635 of engine.go):

```go
// DELETE THIS:
// func (e *Engine) CancelRunning() bool {
//     ...
// }
```

- [ ] **Step 4: Verify build**

Run: `cd /Users/donny/Projects/2026/geoffreyengram && go build ./cmd/dispatch-ui/`
Expected: FAIL — `exec.go` and `server.go` still reference old patterns. That's expected; we fix them in Tasks 3 and 4.

- [ ] **Step 5: Commit (even with build errors — logically complete unit)**

```bash
git add cmd/dispatch-ui/engine.go
git commit -m "feat(dispatch): pluralize Engine.running to map for parallel execution

Adds IsAtCapacity, IsRunning, RunningCount, CancelTask methods.
Removes single-task CancelRunning. Build will break until exec.go
and server.go are updated in next commits."
```

---

### Task 3: Update ExecutePlan for map-based registration and worktree cleanup

**Files:**
- Modify: `cmd/dispatch-ui/exec.go:382-385` (running registration)
- Modify: `cmd/dispatch-ui/exec.go:612-621` (running cleanup + worktree)

- [ ] **Step 1: Update self-registration (line 382-385)**

Replace:
```go
// 6. Store cancel func for UI cancellation
e.mu.Lock()
e.running = &RunningTask{Plan: plan, Cancel: cancel}
e.mu.Unlock()
```

With:
```go
// 6. Register in running map; deregister on completion
e.mu.Lock()
e.running[plan.Name] = &RunningTask{Plan: plan, Cancel: cancel}
e.mu.Unlock()

defer func() {
	e.mu.Lock()
	delete(e.running, plan.Name)
	e.mu.Unlock()
}()
```

- [ ] **Step 2: Remove the old cleanup block (lines 618-620)**

Delete the old "Clear running" block near the end of `ExecutePlan`:
```go
// DELETE THIS:
// // 13. Clear running
// e.mu.Lock()
// e.running = nil
// e.mu.Unlock()
```

The `defer` from Step 1 handles this now.

- [ ] **Step 3: Add worktree auto-cleanup**

Find the block that logs worktree preservation (around line 613-615):
```go
// Log worktree location for inspection
if workDir != plan.Project {
	lb.Append(fmt.Sprintf("[dispatch] worktree preserved at %s for inspection", workDir))
}
```

Replace with:
```go
// Worktree cleanup: remove directory on success, preserve on failure
if workDir != plan.Project {
	if status == "done" && e.Config.AutoCleanupWorktrees {
		rmOut, rmErr := exec.Command("git", "-C", plan.Project, "worktree", "remove", workDir).CombinedOutput()
		if rmErr != nil {
			lb.Append(fmt.Sprintf("[dispatch] worktree cleanup failed: %s", strings.TrimSpace(string(rmOut))))
		} else {
			lb.Append(fmt.Sprintf("[dispatch] worktree cleaned up (branch %s preserved for merge)", branch))
		}
	} else {
		lb.Append(fmt.Sprintf("[dispatch] worktree preserved at %s for inspection", workDir))
	}
}
```

- [ ] **Step 4: Verify build**

Run: `cd /Users/donny/Projects/2026/geoffreyengram && go build ./cmd/dispatch-ui/`
Expected: FAIL — `server.go` still references `CancelRunning()`. Fixed in Task 4.

- [ ] **Step 5: Commit**

```bash
git add cmd/dispatch-ui/exec.go
git commit -m "feat(dispatch): ExecutePlan uses running map, auto-cleans worktrees

Self-registers/deregisters via defer. Worktrees removed on success
(branch preserved for merge), preserved on failure for debugging."
```

---

### Task 4: Update server cancel handler

**Files:**
- Modify: `cmd/dispatch-ui/server.go:302-316` (handleCancelTask)
- Modify: `cmd/dispatch-ui/server.go:19-35` (APITask struct)

- [ ] **Step 1: Update handleCancelTask**

Replace the `handleCancelTask` method (lines 302-316):

```go
// handleCancelTask cancels a specific running task by name.
func (s *Server) handleCancelTask(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	if !s.eng.CancelTask(name) {
		writeError(w, http.StatusConflict, "task is not currently running")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "cancelled"})
}
```

- [ ] **Step 2: Add Harness to APITask and planToAPI**

In the `APITask` struct (around line 19), add after `AllowedTools`:

```go
type APITask struct {
	Name         string   `json:"name"`
	Title        string   `json:"title"`
	Status       string   `json:"status"`
	Project      string   `json:"project"`
	Model        string   `json:"model"`
	Harness      string   `json:"harness,omitempty"`
	Priority     int      `json:"priority"`
	MaxRuntime   string   `json:"max_runtime"`
	Branch       string   `json:"branch"`
	Created      string   `json:"created"`
	StartedAt    string   `json:"started_at,omitempty"`
	FinishedAt   string   `json:"finished_at,omitempty"`
	ExitCode     int      `json:"exit_code,omitempty"`
	DependsOn    []string `json:"depends_on,omitempty"`
	AllowedTools string   `json:"allowed_tools"`
	CreatedBy    string   `json:"created_by,omitempty"`
}
```

In `planToAPI` (around line 81), add `Harness: p.Harness`:

```go
func planToAPI(p *Plan) APITask {
	return APITask{
		Name:         p.Name,
		Title:        p.Title,
		Status:       p.Status,
		Project:      p.Project,
		Model:        p.Model,
		Harness:      p.Harness,
		Priority:     p.Priority,
		MaxRuntime:   p.MaxRuntime,
		Branch:       p.Branch,
		Created:      p.Created,
		StartedAt:    p.StartedAt,
		FinishedAt:   p.FinishedAt,
		ExitCode:     p.ExitCode,
		DependsOn:    p.DependsOn,
		AllowedTools: p.AllowedTools,
		CreatedBy:    p.CreatedBy,
	}
}
```

- [ ] **Step 3: Verify build**

Run: `cd /Users/donny/Projects/2026/geoffreyengram && go build ./cmd/dispatch-ui/`
Expected: Build succeeds.

- [ ] **Step 4: Commit**

```bash
git add cmd/dispatch-ui/server.go
git commit -m "feat(dispatch): update cancel handler for per-task cancellation

handleCancelTask uses CancelTask(name) instead of CancelRunning().
Adds Harness field to APITask for UI visibility."
```

---

### Task 5: Update pollQueue for multi-dispatch

**Files:**
- Modify: `cmd/dispatch-ui/main.go:42-80` (pollQueue function)

- [ ] **Step 1: Rewrite pollQueue**

Replace the `pollQueue` function:

```go
// pollQueue continuously checks for pending plans whose dependencies are met
// and executes up to MaxConcurrent tasks in parallel.
func pollQueue(eng *Engine) {
	for {
		time.Sleep(5 * time.Second)

		// Check available slots
		eng.mu.Lock()
		slots := eng.Config.MaxConcurrent - len(eng.running)
		// Snapshot running names to avoid launching duplicates
		runningNames := make(map[string]bool, len(eng.running))
		for name := range eng.running {
			runningNames[name] = true
		}
		eng.mu.Unlock()

		if slots <= 0 {
			continue
		}

		plans, err := eng.ListPlans()
		if err != nil {
			log.Printf("pollQueue: list plans: %v", err)
			continue
		}

		// Find eligible pending plans (deps met, not already running)
		var eligible []*Plan
		for _, p := range plans {
			if p.Status == "pending" && !runningNames[p.Name] && eng.dependenciesMet(p) {
				eligible = append(eligible, p)
				if len(eligible) >= slots {
					break
				}
			}
		}

		// Launch all eligible plans
		for _, plan := range eligible {
			go func(p *Plan) {
				if err := eng.ExecutePlan(context.Background(), p); err != nil {
					log.Printf("execute plan %s: %v", p.Name, err)
				}
			}(plan)
		}
	}
}
```

- [ ] **Step 2: Verify build**

Run: `cd /Users/donny/Projects/2026/geoffreyengram && go build ./cmd/dispatch-ui/`
Expected: Build succeeds.

- [ ] **Step 3: Commit**

```bash
git add cmd/dispatch-ui/main.go
git commit -m "feat(dispatch): pollQueue dispatches multiple tasks up to MaxConcurrent

Finds all eligible pending plans with dependencies met, launches
up to available slots per poll cycle. Tasks naturally fill slots
as previous tasks complete."
```

---

### Task 6: Update dispatch.conf and install

**Files:**
- Modify: `~/.claude/dispatch/dispatch.conf`

- [ ] **Step 1: Add config entries**

Append to `~/.claude/dispatch/dispatch.conf`:

```
# Parallel execution
MAX_CONCURRENT="2"
AUTO_CLEANUP_WORKTREES="true"
```

- [ ] **Step 2: Build and install**

```bash
cd /Users/donny/Projects/2026/geoffreyengram
go build ./cmd/dispatch-ui/
go install ./cmd/dispatch-ui/
```

Expected: Both succeed.

- [ ] **Step 3: Run full test — verify parallel execution**

Start the dispatch server:
```bash
~/go/bin/dispatch-ui
```

Create 3 independent test plans (no dependencies) via the API or by creating .md files in the plans dir. Verify in the UI that 2 run simultaneously and the 3rd starts when one finishes.

- [ ] **Step 4: Verify worktree cleanup**

After a task succeeds, check:
```bash
ls ~/.claude/dispatch/worktrees/
```
The succeeded task's worktree directory should be gone. The branch should still exist:
```bash
git branch | grep dispatch/
```

After a task fails, the worktree should be preserved:
```bash
ls ~/.claude/dispatch/worktrees/<failed-task-name>
```

- [ ] **Step 5: Verify per-task cancellation**

With 2 tasks running, cancel one via the UI or API:
```bash
curl -X POST http://127.0.0.1:8090/api/tasks/<task-name>/cancel
```
The cancelled task should stop. The other should continue.

- [ ] **Step 6: Final commit**

```bash
git add cmd/dispatch-ui/
git commit -m "feat(dispatch): parallel execution with configurable concurrency

Tasks run in parallel up to MAX_CONCURRENT (default 2). Worktrees
auto-cleaned on success (branch preserved), preserved on failure.
Per-task cancellation via CancelTask(name)."
```
