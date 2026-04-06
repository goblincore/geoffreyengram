# Dispatch UI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a Go HTTP server + vanilla JS frontend that replaces `dispatch.sh` + cron with a web-based dispatch dashboard supporting task creation, live log streaming, and task management.

**Architecture:** Single Go binary in `cmd/dispatch-ui/` that embeds static HTML/JS/CSS via `embed.FS`. Reads the existing `~/.claude/dispatch/` directory (plans, reports, .env, dispatch.conf). Spawns `claude --bare` as child processes for task execution. Uses Server-Sent Events (SSE) for live log streaming (no extra dependencies needed vs WebSocket).

**Tech Stack:** Go stdlib (`net/http`, `embed`, `os/exec`, `encoding/json`), vanilla HTML/JS/CSS. No external Go dependencies beyond what's in go.mod. No npm/node build step.

**Spec:** `docs/superpowers/specs/2026-04-06-dispatch-ui-design.md`

---

### Task 1: Frontmatter Parser

Port the YAML frontmatter parsing from dispatch.sh to Go. This is the foundation — every other component reads/writes plan files.

**Files:**
- Create: `cmd/dispatch-ui/frontmatter.go`
- Create: `cmd/dispatch-ui/frontmatter_test.go`

- [ ] **Step 1: Write the failing test for parsing frontmatter**

```go
// cmd/dispatch-ui/frontmatter_test.go
package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParsePlanFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.md")
	content := `---
title: "Test plan"
status: pending
project: /tmp/myproject
model: glm-5.1
api_key_env: ZAI_API_KEY
base_url: https://api.z.ai/api/anthropic
branch: dispatch/test
priority: 2
max_runtime: 10m
created: 2026-04-06
depends_on: []
allowed_tools: Edit,Write,Bash,Read,Glob,Grep
---

## Context

This is the plan body.

## Instructions

Do the thing.
`
	os.WriteFile(path, []byte(content), 0644)

	plan, err := parsePlanFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Title != "Test plan" {
		t.Errorf("title = %q, want %q", plan.Title, "Test plan")
	}
	if plan.Status != "pending" {
		t.Errorf("status = %q, want %q", plan.Status, "pending")
	}
	if plan.Project != "/tmp/myproject" {
		t.Errorf("project = %q", plan.Project)
	}
	if plan.Model != "glm-5.1" {
		t.Errorf("model = %q", plan.Model)
	}
	if plan.Priority != 2 {
		t.Errorf("priority = %d", plan.Priority)
	}
	if plan.MaxRuntime != "10m" {
		t.Errorf("max_runtime = %q", plan.MaxRuntime)
	}
	if plan.Body == "" || plan.Body[:2] != "\n#" {
		t.Errorf("body should start with plan content, got %q", plan.Body[:20])
	}
}

func TestSetFrontmatterField(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.md")
	content := `---
title: "Test"
status: pending
priority: 3
---

Body here.
`
	os.WriteFile(path, []byte(content), 0644)

	err := setFrontmatterField(path, "status", "running")
	if err != nil {
		t.Fatal(err)
	}

	plan, _ := parsePlanFile(path)
	if plan.Status != "running" {
		t.Errorf("status = %q, want running", plan.Status)
	}

	// Verify body is preserved
	if plan.Body == "" {
		t.Error("body should be preserved after field update")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/donny/Projects/2026/geoffreyengram && go test ./cmd/dispatch-ui/ -run TestParse -v`
Expected: FAIL — `parsePlanFile` not defined

- [ ] **Step 3: Implement frontmatter parser**

```go
// cmd/dispatch-ui/frontmatter.go
package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Plan represents a parsed dispatch plan file.
type Plan struct {
	Name     string // filename without .md extension
	Path     string // full filesystem path
	Title    string
	Status   string // pending, running, done, failed, cancelled
	Project  string
	Model    string
	APIKeyEnv   string
	BaseURL     string
	Branch      string
	Priority    int
	MaxRuntime  string
	Created     string
	DependsOn   []string
	AllowedTools string
	StartedAt   string
	FinishedAt  string
	ExitCode    int
	Report      string
	CreatedBy   string
	PlanningModel string
	Body        string // everything after the second ---
}

// parsePlanFile reads a markdown file with YAML frontmatter and returns a Plan.
func parsePlanFile(path string) (*Plan, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	text := string(data)

	// Split on --- delimiters
	parts := strings.SplitN(text, "---", 3)
	if len(parts) < 3 {
		return nil, fmt.Errorf("invalid frontmatter in %s", path)
	}

	fm := parts[1] // YAML frontmatter
	body := parts[2] // plan body

	p := &Plan{
		Name:     strings.TrimSuffix(filepath.Base(path), ".md"),
		Path:     path,
		Body:     body,
		Priority: 5, // default
	}

	for _, line := range strings.Split(fm, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line == "---" {
			continue
		}
		key, val := splitKV(line)
		switch key {
		case "title":
			p.Title = val
		case "status":
			p.Status = val
		case "project":
			p.Project = val
		case "model":
			p.Model = val
		case "api_key_env":
			p.APIKeyEnv = val
		case "base_url":
			p.BaseURL = val
		case "branch":
			p.Branch = val
		case "priority":
			if n, err := strconv.Atoi(val); err == nil {
				p.Priority = n
			}
		case "max_runtime":
			p.MaxRuntime = val
		case "created":
			p.Created = val
		case "depends_on":
			p.DependsOn = parseDependsList(val)
		case "allowed_tools":
			p.AllowedTools = val
		case "started_at":
			p.StartedAt = val
		case "finished_at":
			p.FinishedAt = val
		case "exit_code":
			if n, err := strconv.Atoi(val); err == nil {
				p.ExitCode = n
			}
		case "report":
			p.Report = val
		case "created_by":
			p.CreatedBy = val
		case "planning_model":
			p.PlanningModel = val
		}
	}

	return p, nil
}

// splitKV splits "key: value" and strips quotes from value.
func splitKV(line string) (string, string) {
	idx := strings.Index(line, ":")
	if idx < 0 {
		return line, ""
	}
	key := strings.TrimSpace(line[:idx])
	val := strings.TrimSpace(line[idx+1:])
	// Strip inline comments
	if ci := strings.Index(val, " #"); ci >= 0 {
		val = strings.TrimSpace(val[:ci])
	}
	// Strip quotes
	val = strings.Trim(val, `"'`)
	return key, val
}

// parseDependsList parses "[]" or "[a, b]" or "a,b" into a slice.
func parseDependsList(s string) []string {
	s = strings.Trim(s, "[]")
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	var out []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// setFrontmatterField updates a single field in a plan file's frontmatter in-place.
func setFrontmatterField(path, key, value string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	text := string(data)
	parts := strings.SplitN(text, "---", 3)
	if len(parts) < 3 {
		return fmt.Errorf("invalid frontmatter in %s", path)
	}

	lines := strings.Split(parts[1], "\n")
	found := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, key+":") {
			lines[i] = key + ": " + value
			found = true
			break
		}
	}
	if !found {
		// Insert before the last empty line
		lines = append(lines[:len(lines)-1], key+": "+value, "")
	}

	result := "---" + strings.Join(lines, "\n") + "---" + parts[2]
	return os.WriteFile(path, []byte(result), 0644)
}

// writePlanFile creates a new plan file with frontmatter and body.
func writePlanFile(p *Plan) error {
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString(fmt.Sprintf("title: %q\n", p.Title))
	b.WriteString(fmt.Sprintf("status: %s\n", p.Status))
	b.WriteString(fmt.Sprintf("project: %s\n", p.Project))
	b.WriteString(fmt.Sprintf("model: %s\n", p.Model))
	b.WriteString(fmt.Sprintf("api_key_env: %s\n", p.APIKeyEnv))
	if p.BaseURL != "" {
		b.WriteString(fmt.Sprintf("base_url: %s\n", p.BaseURL))
	} else {
		b.WriteString("base_url:\n")
	}
	b.WriteString(fmt.Sprintf("branch: dispatch/%s\n", p.Name))
	b.WriteString(fmt.Sprintf("priority: %d\n", p.Priority))
	b.WriteString(fmt.Sprintf("max_runtime: %s\n", p.MaxRuntime))
	b.WriteString(fmt.Sprintf("created: %s\n", p.Created))
	b.WriteString("depends_on: []\n")
	b.WriteString(fmt.Sprintf("allowed_tools: %s\n", p.AllowedTools))
	if p.CreatedBy != "" {
		b.WriteString(fmt.Sprintf("created_by: %s\n", p.CreatedBy))
	}
	if p.PlanningModel != "" {
		b.WriteString(fmt.Sprintf("planning_model: %s\n", p.PlanningModel))
	}
	b.WriteString("---\n")
	b.WriteString(p.Body)

	return os.WriteFile(p.Path, []byte(b.String()), 0644)
}
```

Note: Add `"path/filepath"` to the imports in frontmatter.go.

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /Users/donny/Projects/2026/geoffreyengram && go test ./cmd/dispatch-ui/ -run TestParse -v && go test ./cmd/dispatch-ui/ -run TestSet -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add cmd/dispatch-ui/frontmatter.go cmd/dispatch-ui/frontmatter_test.go
git commit -m "feat(dispatch-ui): add frontmatter parser for plan files"
```

---

### Task 2: Config Loader and Dispatch Engine Types

Load `dispatch.conf` and `.env`, define the execution engine types and state management.

**Files:**
- Create: `cmd/dispatch-ui/config.go`
- Create: `cmd/dispatch-ui/engine.go`
- Create: `cmd/dispatch-ui/engine_test.go`

- [ ] **Step 1: Write failing test for config loading**

```go
// cmd/dispatch-ui/engine_test.go
package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDispatchConfig(t *testing.T) {
	dir := t.TempDir()
	confPath := filepath.Join(dir, "dispatch.conf")
	os.WriteFile(confPath, []byte(`PLANS_DIR="`+dir+`/plans"
REPORTS_DIR="`+dir+`/reports"
LOG_FILE="`+dir+`/dispatch.log"
LOCK_FILE="`+dir+`/.lock"
DEFAULT_MODEL="glm-5.1"
DEFAULT_AUTH_TOKEN_ENV="ZAI_API_KEY"
DEFAULT_BASE_URL="https://api.z.ai/api/anthropic"
DEFAULT_MAX_RUNTIME="1800"
CLAUDE_BIN="/usr/local/bin/claude"
`), 0644)

	envPath := filepath.Join(dir, ".env")
	os.WriteFile(envPath, []byte(`export ZAI_API_KEY="test-key-123"
export ANTHROPIC_API_KEY="test-ant-key"
`), 0644)

	cfg, err := loadDispatchConfig(confPath, envPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PlansDir != dir+"/plans" {
		t.Errorf("PlansDir = %q", cfg.PlansDir)
	}
	if cfg.DefaultModel != "glm-5.1" {
		t.Errorf("DefaultModel = %q", cfg.DefaultModel)
	}
	if cfg.EnvVars["ZAI_API_KEY"] != "test-key-123" {
		t.Errorf("ZAI_API_KEY = %q", cfg.EnvVars["ZAI_API_KEY"])
	}
}

func TestListPlans(t *testing.T) {
	dir := t.TempDir()
	plansDir := filepath.Join(dir, "plans")
	os.MkdirAll(plansDir, 0755)

	// Create two plan files
	os.WriteFile(filepath.Join(plansDir, "task-a.md"), []byte(`---
title: "Task A"
status: pending
project: /tmp
model: glm-5.1
priority: 2
max_runtime: 10m
---

Body A
`), 0644)
	os.WriteFile(filepath.Join(plansDir, "task-b.md"), []byte(`---
title: "Task B"
status: done
project: /tmp
model: sonnet-4.6
priority: 1
max_runtime: 5m
exit_code: 0
---

Body B
`), 0644)

	eng := &Engine{Config: &DispatchConfig{PlansDir: plansDir}}
	plans, err := eng.ListPlans()
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 2 {
		t.Fatalf("expected 2 plans, got %d", len(plans))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/dispatch-ui/ -run TestLoad -v`
Expected: FAIL — types not defined

- [ ] **Step 3: Implement config loader**

```go
// cmd/dispatch-ui/config.go
package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// DispatchConfig holds parsed dispatch.conf + .env values.
type DispatchConfig struct {
	PlansDir           string
	ReportsDir         string
	LogFile            string
	DefaultModel       string
	DefaultAuthTokenEnv string
	DefaultBaseURL     string
	DefaultMaxRuntime  string
	ClaudeBin          string
	ListenAddr         string
	PlanningModel      string
	PlanningAPIKeyEnv  string
	PlanningBaseURL    string
	EnvVars            map[string]string // from .env
}

func loadDispatchConfig(confPath, envPath string) (*DispatchConfig, error) {
	cfg := &DispatchConfig{
		ListenAddr:        "127.0.0.1:8090",
		PlanningModel:     "claude-opus-4-6",
		PlanningAPIKeyEnv: "ANTHROPIC_API_KEY",
		EnvVars:           make(map[string]string),
	}

	// Parse dispatch.conf (KEY="value" format, bash-style)
	if err := parseShellFile(confPath, func(k, v string) {
		switch k {
		case "PLANS_DIR":
			cfg.PlansDir = v
		case "REPORTS_DIR":
			cfg.ReportsDir = v
		case "LOG_FILE":
			cfg.LogFile = v
		case "DEFAULT_MODEL":
			cfg.DefaultModel = v
		case "DEFAULT_AUTH_TOKEN_ENV":
			cfg.DefaultAuthTokenEnv = v
		case "DEFAULT_BASE_URL":
			cfg.DefaultBaseURL = v
		case "DEFAULT_MAX_RUNTIME":
			cfg.DefaultMaxRuntime = v
		case "CLAUDE_BIN":
			cfg.ClaudeBin = v
		case "LISTEN_ADDR":
			cfg.ListenAddr = v
		case "PLANNING_MODEL":
			cfg.PlanningModel = v
		case "PLANNING_API_KEY_ENV":
			cfg.PlanningAPIKeyEnv = v
		case "PLANNING_BASE_URL":
			cfg.PlanningBaseURL = v
		}
	}); err != nil {
		return nil, fmt.Errorf("reading dispatch.conf: %w", err)
	}

	// Parse .env (export KEY="value" format)
	if envPath != "" {
		parseShellFile(envPath, func(k, v string) {
			cfg.EnvVars[k] = v
		})
	}

	return cfg, nil
}

// parseShellFile reads KEY=VALUE or export KEY=VALUE lines.
func parseShellFile(path string, fn func(k, v string)) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Strip "export " prefix
		line = strings.TrimPrefix(line, "export ")
		k, v := splitKV(line)
		if k != "" {
			// splitKV uses ": " but conf uses "=", handle both
			if idx := strings.Index(line, "="); idx > 0 {
				k = strings.TrimSpace(line[:idx])
				v = strings.TrimSpace(line[idx+1:])
				v = strings.Trim(v, `"'`)
			}
			fn(k, v)
		}
	}
	return scanner.Err()
}
```

- [ ] **Step 4: Implement Engine with ListPlans**

```go
// cmd/dispatch-ui/engine.go
package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Engine manages the dispatch queue and task execution.
type Engine struct {
	Config  *DispatchConfig
	mu      sync.Mutex
	running *RunningTask // currently executing task (nil if idle)
	logs    map[string]*LogBuffer // task name -> log buffer
}

// RunningTask tracks a currently executing task.
type RunningTask struct {
	Plan    *Plan
	Cancel  func() // call to cancel the process
}

// LogBuffer holds log lines for streaming.
type LogBuffer struct {
	mu    sync.Mutex
	lines []string
	subs  []chan string // SSE subscribers
}

func NewEngine(cfg *DispatchConfig) *Engine {
	return &Engine{
		Config: cfg,
		logs:   make(map[string]*LogBuffer),
	}
}

// ListPlans reads all .md files from PlansDir and returns parsed plans.
// Sorted: running first, then pending by priority, then done/failed by recency.
func (e *Engine) ListPlans() ([]*Plan, error) {
	entries, err := os.ReadDir(e.Config.PlansDir)
	if err != nil {
		return nil, err
	}

	var plans []*Plan
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		path := filepath.Join(e.Config.PlansDir, entry.Name())
		plan, err := parsePlanFile(path)
		if err != nil {
			continue // skip unparseable files
		}
		plans = append(plans, plan)
	}

	sort.Slice(plans, func(i, j int) bool {
		return planSortKey(plans[i]) < planSortKey(plans[j])
	})

	return plans, nil
}

func planSortKey(p *Plan) string {
	// running=0, pending=1, done=2, failed=3, cancelled=4
	statusOrder := map[string]string{
		"running": "0", "pending": "1", "done": "2", "failed": "3", "cancelled": "4",
	}
	s := statusOrder[p.Status]
	if s == "" {
		s = "9"
	}
	// For pending, sort by priority (lower = first)
	if p.Status == "pending" {
		return s + fmt.Sprintf("%d", p.Priority)
	}
	// For done/failed, sort by finished_at descending (newer first)
	if p.FinishedAt != "" {
		// Invert timestamp for descending sort — crude but effective
		return s + "0" + p.FinishedAt
	}
	return s + "9"
}
```

Note: Add `"fmt"` to engine.go imports.

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./cmd/dispatch-ui/ -run "TestLoad|TestList" -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add cmd/dispatch-ui/config.go cmd/dispatch-ui/engine.go cmd/dispatch-ui/engine_test.go
git commit -m "feat(dispatch-ui): config loader and engine with plan listing"
```

---

### Task 3: Task Execution Engine

The core: spawn `claude --bare` as a child process, capture output, handle timeouts and cancellation.

**Files:**
- Modify: `cmd/dispatch-ui/engine.go`
- Create: `cmd/dispatch-ui/exec.go`
- Create: `cmd/dispatch-ui/exec_test.go`

- [ ] **Step 1: Write failing test for execution**

```go
// cmd/dispatch-ui/exec_test.go
package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestExecutePlan(t *testing.T) {
	dir := t.TempDir()
	plansDir := filepath.Join(dir, "plans")
	reportsDir := filepath.Join(dir, "reports")
	os.MkdirAll(plansDir, 0755)
	os.MkdirAll(reportsDir, 0755)

	// Create a mock "claude" script that just echoes
	mockClaude := filepath.Join(dir, "mock-claude")
	os.WriteFile(mockClaude, []byte(`#!/bin/bash
echo "Reading files..."
echo "Implementing feature..."
echo "Tests passing!"
`), 0755)

	planPath := filepath.Join(plansDir, "test-exec.md")
	os.WriteFile(planPath, []byte(`---
title: "Test exec"
status: pending
project: `+dir+`
model: test-model
api_key_env: TEST_KEY
base_url:
branch: dispatch/test-exec
priority: 1
max_runtime: 10s
allowed_tools: Bash,Read
---

Just echo some stuff.
`), 0644)

	cfg := &DispatchConfig{
		PlansDir:           plansDir,
		ReportsDir:         reportsDir,
		ClaudeBin:          mockClaude,
		DefaultAuthTokenEnv: "TEST_KEY",
		EnvVars:            map[string]string{"TEST_KEY": "fake-key"},
	}
	eng := NewEngine(cfg)

	plan, _ := parsePlanFile(planPath)
	err := eng.ExecutePlan(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}

	// Re-read plan to check status was updated
	plan, _ = parsePlanFile(planPath)
	if plan.Status != "done" {
		t.Errorf("status = %q, want done", plan.Status)
	}
	if plan.ExitCode != 0 {
		t.Errorf("exit_code = %d, want 0", plan.ExitCode)
	}

	// Check report was written
	reportPath := filepath.Join(reportsDir, "test-exec.md")
	if _, err := os.Stat(reportPath); os.IsNotExist(err) {
		t.Error("report file not created")
	}
}

func TestExecutePlanTimeout(t *testing.T) {
	dir := t.TempDir()
	plansDir := filepath.Join(dir, "plans")
	reportsDir := filepath.Join(dir, "reports")
	os.MkdirAll(plansDir, 0755)
	os.MkdirAll(reportsDir, 0755)

	// Mock claude that sleeps forever
	mockClaude := filepath.Join(dir, "mock-claude-slow")
	os.WriteFile(mockClaude, []byte(`#!/bin/bash
echo "Starting..."
sleep 60
`), 0755)

	planPath := filepath.Join(plansDir, "test-timeout.md")
	os.WriteFile(planPath, []byte(`---
title: "Test timeout"
status: pending
project: `+dir+`
model: test-model
api_key_env: TEST_KEY
max_runtime: 2s
---

This will timeout.
`), 0644)

	cfg := &DispatchConfig{
		PlansDir:   plansDir,
		ReportsDir: reportsDir,
		ClaudeBin:  mockClaude,
		EnvVars:    map[string]string{"TEST_KEY": "fake-key"},
	}
	eng := NewEngine(cfg)

	plan, _ := parsePlanFile(planPath)
	start := time.Now()
	eng.ExecutePlan(context.Background(), plan)
	elapsed := time.Since(start)

	if elapsed > 10*time.Second {
		t.Errorf("execution took %s, should have timed out at ~2s", elapsed)
	}

	plan, _ = parsePlanFile(planPath)
	if plan.Status != "failed" {
		t.Errorf("status = %q, want failed", plan.Status)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/dispatch-ui/ -run TestExecute -v`
Expected: FAIL — `ExecutePlan` not defined

- [ ] **Step 3: Implement execution engine**

```go
// cmd/dispatch-ui/exec.go
package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// parseMaxRuntime converts "10m", "1h", "1800" to time.Duration.
func parseMaxRuntime(s string) time.Duration {
	if strings.HasSuffix(s, "m") {
		n, _ := strconv.Atoi(strings.TrimSuffix(s, "m"))
		return time.Duration(n) * time.Minute
	}
	if strings.HasSuffix(s, "h") {
		n, _ := strconv.Atoi(strings.TrimSuffix(s, "h"))
		return time.Duration(n) * time.Hour
	}
	n, _ := strconv.Atoi(s)
	if n > 0 {
		return time.Duration(n) * time.Second
	}
	return 30 * time.Minute // default
}

// ExecutePlan runs a plan by spawning the claude CLI as a child process.
func (e *Engine) ExecutePlan(ctx context.Context, plan *Plan) error {
	// Claim the plan
	setFrontmatterField(plan.Path, "status", "running")
	setFrontmatterField(plan.Path, "started_at", time.Now().UTC().Format(time.RFC3339))

	e.mu.Lock()
	lb := &LogBuffer{}
	e.logs[plan.Name] = lb
	e.mu.Unlock()

	// Resolve auth
	apiKeyEnv := plan.APIKeyEnv
	if apiKeyEnv == "" {
		apiKeyEnv = e.Config.DefaultAuthTokenEnv
	}
	apiKey := e.Config.EnvVars[apiKeyEnv]
	if apiKey == "" {
		apiKey = os.Getenv(apiKeyEnv)
	}

	baseURL := plan.BaseURL
	// If base_url field was not in frontmatter at all, use default
	// (We store empty string if key was present but empty — meaning direct Anthropic)

	// Build environment
	env := os.Environ()
	if baseURL != "" {
		env = append(env, "ANTHROPIC_AUTH_TOKEN="+apiKey)
		env = append(env, "ANTHROPIC_BASE_URL="+baseURL)
	} else {
		env = append(env, "ANTHROPIC_API_KEY="+apiKey)
	}
	env = append(env, "ANTHROPIC_DEFAULT_SONNET_MODEL="+plan.Model)

	// Build command
	timeout := parseMaxRuntime(plan.MaxRuntime)
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Store cancel func for UI cancellation
	e.mu.Lock()
	e.running = &RunningTask{Plan: plan, Cancel: cancel}
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		e.running = nil
		e.mu.Unlock()
	}()

	prompt := strings.TrimSpace(plan.Body)
	allowedTools := plan.AllowedTools
	if allowedTools == "" {
		allowedTools = "Edit,Write,Bash,Read,Glob,Grep"
	}

	cmd := exec.CommandContext(execCtx, e.Config.ClaudeBin,
		"--bare", "-p", prompt,
		"--allowed-tools", allowedTools,
		"--output-format", "text",
		"--no-session-persistence",
	)
	cmd.Dir = plan.Project
	cmd.Env = env

	// Capture output
	stdout, _ := cmd.StdoutPipe()
	cmd.Stderr = cmd.Stdout // merge stderr into stdout

	// Prepare report file
	reportPath := filepath.Join(e.Config.ReportsDir, plan.Name+".md")
	reportFile, err := os.Create(reportPath)
	if err != nil {
		return fmt.Errorf("creating report: %w", err)
	}
	defer reportFile.Close()

	// Write report header
	fmt.Fprintf(reportFile, "# Report: %s\n\n", plan.Name)
	fmt.Fprintf(reportFile, "**Plan:** %s.md\n", plan.Name)
	fmt.Fprintf(reportFile, "**Model:** %s\n", plan.Model)
	fmt.Fprintf(reportFile, "**Started:** %s\n", time.Now().UTC().Format(time.RFC3339))
	fmt.Fprintf(reportFile, "**Project:** %s\n\n---\n\n", plan.Project)

	// Start process
	if err := cmd.Start(); err != nil {
		setFrontmatterField(plan.Path, "status", "failed")
		setFrontmatterField(plan.Path, "finished_at", time.Now().UTC().Format(time.RFC3339))
		setFrontmatterField(plan.Path, "exit_code", "1")
		return fmt.Errorf("starting claude: %w", err)
	}

	// Stream output line by line
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024) // 1MB line buffer
	for scanner.Scan() {
		line := scanner.Text()
		reportFile.WriteString(line + "\n")
		lb.Append(line)
	}

	// Wait for process
	err = cmd.Wait()
	exitCode := 0
	if err != nil {
		if execCtx.Err() == context.DeadlineExceeded {
			exitCode = 124
		} else if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = 1
		}
	}

	// Write report footer
	fmt.Fprintf(reportFile, "\n---\n\n")
	if exitCode == 124 {
		fmt.Fprintf(reportFile, "**Status:** TIMEOUT\n")
	} else if exitCode == 0 {
		fmt.Fprintf(reportFile, "**Status:** SUCCESS\n")
	} else {
		fmt.Fprintf(reportFile, "**Status:** FAILED (exit code: %d)\n", exitCode)
	}
	fmt.Fprintf(reportFile, "**Finished:** %s\n", time.Now().UTC().Format(time.RFC3339))

	// Update plan frontmatter
	status := "done"
	if exitCode != 0 {
		status = "failed"
	}
	setFrontmatterField(plan.Path, "status", status)
	setFrontmatterField(plan.Path, "finished_at", time.Now().UTC().Format(time.RFC3339))
	setFrontmatterField(plan.Path, "exit_code", strconv.Itoa(exitCode))
	setFrontmatterField(plan.Path, "report", "reports/"+plan.Name+".md")

	return nil
}

// CancelRunning cancels the currently running task.
func (e *Engine) CancelRunning() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.running != nil {
		e.running.Cancel()
		return true
	}
	return false
}

// Append adds a log line and notifies subscribers.
func (lb *LogBuffer) Append(line string) {
	lb.mu.Lock()
	defer lb.mu.Unlock()
	lb.lines = append(lb.lines, line)
	for _, ch := range lb.subs {
		select {
		case ch <- line:
		default: // drop if subscriber is slow
		}
	}
}

// Subscribe returns a channel that receives new log lines.
func (lb *LogBuffer) Subscribe() (chan string, func()) {
	ch := make(chan string, 100)
	lb.mu.Lock()
	lb.subs = append(lb.subs, ch)
	// Send existing lines
	for _, line := range lb.lines {
		ch <- line
	}
	lb.mu.Unlock()
	return ch, func() {
		lb.mu.Lock()
		defer lb.mu.Unlock()
		for i, s := range lb.subs {
			if s == ch {
				lb.subs = append(lb.subs[:i], lb.subs[i+1:]...)
				break
			}
		}
		close(ch)
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/dispatch-ui/ -run TestExecute -v -timeout 30s`
Expected: PASS (TestExecutePlan succeeds, TestExecutePlanTimeout completes in ~2-3s)

- [ ] **Step 5: Commit**

```bash
git add cmd/dispatch-ui/exec.go cmd/dispatch-ui/exec_test.go
git commit -m "feat(dispatch-ui): task execution engine with timeout and log capture"
```

---

### Task 4: HTTP API and SSE Log Streaming

The web server: REST API for task CRUD, SSE endpoint for live log streaming.

**Files:**
- Create: `cmd/dispatch-ui/server.go`
- Create: `cmd/dispatch-ui/main.go`

- [ ] **Step 1: Write the HTTP server with API routes**

```go
// cmd/dispatch-ui/server.go
package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

//go:embed static
var staticFiles embed.FS

// APITask is the JSON representation of a plan for the API.
type APITask struct {
	Name         string   `json:"name"`
	Title        string   `json:"title"`
	Status       string   `json:"status"`
	Project      string   `json:"project"`
	Model        string   `json:"model"`
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

func planToAPI(p *Plan) APITask {
	return APITask{
		Name: p.Name, Title: p.Title, Status: p.Status,
		Project: p.Project, Model: p.Model, Priority: p.Priority,
		MaxRuntime: p.MaxRuntime, Branch: p.Branch, Created: p.Created,
		StartedAt: p.StartedAt, FinishedAt: p.FinishedAt,
		ExitCode: p.ExitCode, DependsOn: p.DependsOn,
		AllowedTools: p.AllowedTools, CreatedBy: p.CreatedBy,
	}
}

type Server struct {
	engine *Engine
	mux    *http.ServeMux
}

func NewServer(eng *Engine) *Server {
	s := &Server{engine: eng, mux: http.NewServeMux()}
	s.routes()
	return s
}

func (s *Server) routes() {
	// API routes
	s.mux.HandleFunc("GET /api/tasks", s.handleListTasks)
	s.mux.HandleFunc("GET /api/tasks/{name}", s.handleGetTask)
	s.mux.HandleFunc("POST /api/tasks", s.handleCreateTask)
	s.mux.HandleFunc("PUT /api/tasks/{name}", s.handleUpdateTask)
	s.mux.HandleFunc("DELETE /api/tasks/{name}", s.handleDeleteTask)
	s.mux.HandleFunc("POST /api/tasks/{name}/cancel", s.handleCancelTask)
	s.mux.HandleFunc("POST /api/tasks/{name}/retry", s.handleRetryTask)
	s.mux.HandleFunc("GET /api/tasks/{name}/report", s.handleGetReport)
	s.mux.HandleFunc("GET /api/tasks/{name}/plan", s.handleGetPlan)
	s.mux.HandleFunc("GET /api/tasks/{name}/logs", s.handleSSELogs)
	s.mux.HandleFunc("GET /api/config", s.handleGetConfig)

	// Static files (frontend)
	staticFS, _ := fs.Sub(staticFiles, "static")
	s.mux.Handle("GET /", http.FileServer(http.FS(staticFS)))
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func (s *Server) handleListTasks(w http.ResponseWriter, r *http.Request) {
	plans, err := s.engine.ListPlans()
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	tasks := make([]APITask, len(plans))
	for i, p := range plans {
		tasks[i] = planToAPI(p)
	}
	writeJSON(w, tasks)
}

func (s *Server) handleGetTask(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	path := filepath.Join(s.engine.Config.PlansDir, name+".md")
	plan, err := parsePlanFile(path)
	if err != nil {
		writeError(w, 404, "task not found")
		return
	}
	writeJSON(w, planToAPI(plan))
}

func (s *Server) handleCreateTask(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name         string `json:"name"`
		Title        string `json:"title"`
		Project      string `json:"project"`
		Model        string `json:"model"`
		APIKeyEnv    string `json:"api_key_env"`
		BaseURL      string `json:"base_url"`
		Priority     int    `json:"priority"`
		MaxRuntime   string `json:"max_runtime"`
		AllowedTools string `json:"allowed_tools"`
		Body         string `json:"body"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid JSON")
		return
	}
	if req.Name == "" || req.Body == "" {
		writeError(w, 400, "name and body are required")
		return
	}
	if req.Priority == 0 {
		req.Priority = 3
	}
	if req.MaxRuntime == "" {
		req.MaxRuntime = "25m"
	}
	if req.AllowedTools == "" {
		req.AllowedTools = "Edit,Write,Bash,Read,Glob,Grep"
	}

	plan := &Plan{
		Name:         req.Name,
		Path:         filepath.Join(s.engine.Config.PlansDir, req.Name+".md"),
		Title:        req.Title,
		Status:       "pending",
		Project:      req.Project,
		Model:        req.Model,
		APIKeyEnv:    req.APIKeyEnv,
		BaseURL:      req.BaseURL,
		Priority:     req.Priority,
		MaxRuntime:   req.MaxRuntime,
		Created:      time.Now().Format("2006-01-02"),
		AllowedTools: req.AllowedTools,
		CreatedBy:    "ui",
		Body:         "\n" + req.Body + "\n",
	}
	if err := writePlanFile(plan); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	w.WriteHeader(201)
	writeJSON(w, planToAPI(plan))
}

func (s *Server) handleUpdateTask(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	path := filepath.Join(s.engine.Config.PlansDir, name+".md")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		writeError(w, 404, "task not found")
		return
	}
	var req map[string]any
	json.NewDecoder(r.Body).Decode(&req)
	for k, v := range req {
		setFrontmatterField(path, k, fmt.Sprintf("%v", v))
	}
	plan, _ := parsePlanFile(path)
	writeJSON(w, planToAPI(plan))
}

func (s *Server) handleDeleteTask(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	path := filepath.Join(s.engine.Config.PlansDir, name+".md")
	if err := os.Remove(path); err != nil {
		writeError(w, 404, "task not found")
		return
	}
	w.WriteHeader(204)
}

func (s *Server) handleCancelTask(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	s.engine.mu.Lock()
	isRunning := s.engine.running != nil && s.engine.running.Plan.Name == name
	s.engine.mu.Unlock()

	if isRunning {
		s.engine.CancelRunning()
		writeJSON(w, map[string]string{"status": "cancelling"})
	} else {
		path := filepath.Join(s.engine.Config.PlansDir, name+".md")
		setFrontmatterField(path, "status", "cancelled")
		writeJSON(w, map[string]string{"status": "cancelled"})
	}
}

func (s *Server) handleRetryTask(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	path := filepath.Join(s.engine.Config.PlansDir, name+".md")
	setFrontmatterField(path, "status", "pending")
	// Clear runtime fields by setting to empty
	setFrontmatterField(path, "started_at", "")
	setFrontmatterField(path, "finished_at", "")
	setFrontmatterField(path, "exit_code", "0")
	plan, _ := parsePlanFile(path)
	writeJSON(w, planToAPI(plan))
}

func (s *Server) handleGetReport(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	path := filepath.Join(s.engine.Config.ReportsDir, name+".md")
	data, err := os.ReadFile(path)
	if err != nil {
		writeError(w, 404, "report not found")
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	w.Write(data)
}

func (s *Server) handleGetPlan(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	path := filepath.Join(s.engine.Config.PlansDir, name+".md")
	plan, err := parsePlanFile(path)
	if err != nil {
		writeError(w, 404, "plan not found")
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	w.Write([]byte(plan.Body))
}

// handleSSELogs streams log lines via Server-Sent Events.
func (s *Server) handleSSELogs(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, 500, "streaming not supported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher.Flush()

	s.engine.mu.Lock()
	lb := s.engine.logs[name]
	s.engine.mu.Unlock()

	if lb == nil {
		// No live logs — send report content if available
		reportPath := filepath.Join(s.engine.Config.ReportsDir, name+".md")
		if data, err := os.ReadFile(reportPath); err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				fmt.Fprintf(w, "data: %s\n\n", line)
			}
			flusher.Flush()
		}
		fmt.Fprintf(w, "event: done\ndata: complete\n\n")
		flusher.Flush()
		return
	}

	ch, unsub := lb.Subscribe()
	defer unsub()

	for {
		select {
		case line, ok := <-ch:
			if !ok {
				fmt.Fprintf(w, "event: done\ndata: complete\n\n")
				flusher.Flush()
				return
			}
			fmt.Fprintf(w, "data: %s\n\n", line)
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	// Collect known projects from existing plans
	plans, _ := s.engine.ListPlans()
	projects := map[string]bool{}
	for _, p := range plans {
		if p.Project != "" {
			projects[p.Project] = true
		}
	}
	projectList := make([]string, 0, len(projects))
	for p := range projects {
		projectList = append(projectList, p)
	}

	writeJSON(w, map[string]any{
		"default_model":    s.engine.Config.DefaultModel,
		"planning_model":   s.engine.Config.PlanningModel,
		"listen_addr":      s.engine.Config.ListenAddr,
		"projects":         projectList,
		"models":           []string{"glm-5.1", "sonnet-4.6", "claude-opus-4-6"},
	})
}
```

- [ ] **Step 2: Write main.go with queue polling loop**

```go
// cmd/dispatch-ui/main.go
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

func main() {
	home, _ := os.UserHomeDir()
	dispatchDir := filepath.Join(home, ".claude", "dispatch")
	confPath := filepath.Join(dispatchDir, "dispatch.conf")
	envPath := filepath.Join(dispatchDir, ".env")

	cfg, err := loadDispatchConfig(confPath, envPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// Ensure directories exist
	os.MkdirAll(cfg.PlansDir, 0755)
	os.MkdirAll(cfg.ReportsDir, 0755)

	eng := NewEngine(cfg)
	srv := NewServer(eng)

	// Start queue polling in background
	go pollQueue(eng)

	addr := cfg.ListenAddr
	fmt.Printf("Dispatch UI running at http://%s\n", addr)
	log.Fatal(http.ListenAndServe(addr, srv))
}

// pollQueue checks for pending plans every 5 seconds and executes them sequentially.
func pollQueue(eng *Engine) {
	for {
		time.Sleep(5 * time.Second)

		eng.mu.Lock()
		isRunning := eng.running != nil
		eng.mu.Unlock()
		if isRunning {
			continue // already executing a task
		}

		plans, err := eng.ListPlans()
		if err != nil {
			log.Printf("Error listing plans: %v", err)
			continue
		}

		for _, plan := range plans {
			if plan.Status != "pending" {
				continue
			}
			// Check dependencies
			if !eng.dependenciesMet(plan) {
				continue
			}
			log.Printf("Dispatching: %s (model: %s)", plan.Name, plan.Model)
			if err := eng.ExecutePlan(context.Background(), plan); err != nil {
				log.Printf("Execution error for %s: %v", plan.Name, err)
			}
			break // one at a time
		}
	}
}
```

Add `dependenciesMet` method to engine.go:

```go
// dependenciesMet checks if all depends_on plans have status "done".
func (e *Engine) dependenciesMet(plan *Plan) bool {
	if len(plan.DependsOn) == 0 {
		return true
	}
	for _, dep := range plan.DependsOn {
		depPath := filepath.Join(e.Config.PlansDir, dep+".md")
		depPlan, err := parsePlanFile(depPath)
		if err != nil || depPlan.Status != "done" {
			return false
		}
	}
	return true
}
```

- [ ] **Step 3: Verify it compiles**

Run: `cd /Users/donny/Projects/2026/geoffreyengram && go build ./cmd/dispatch-ui/`
Expected: Compiles successfully

- [ ] **Step 4: Commit**

```bash
git add cmd/dispatch-ui/server.go cmd/dispatch-ui/main.go cmd/dispatch-ui/engine.go
git commit -m "feat(dispatch-ui): HTTP server with REST API and SSE log streaming"
```

---

### Task 5: Frontend — Dashboard HTML/JS/CSS

The vanilla frontend: task list, detail panel, live log viewer, task creation modal.

**Files:**
- Create: `cmd/dispatch-ui/static/index.html`
- Create: `cmd/dispatch-ui/static/app.js`
- Create: `cmd/dispatch-ui/static/style.css`

- [ ] **Step 1: Create index.html**

```html
<!-- cmd/dispatch-ui/static/index.html -->
<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>Dispatch</title>
  <link rel="stylesheet" href="style.css">
</head>
<body>
  <div id="app">
    <header id="topbar">
      <div class="topbar-left">
        <h1>Dispatch</h1>
        <span id="task-count" class="badge">0</span>
      </div>
      <button id="btn-new-task" class="btn-primary">+ New Task</button>
    </header>

    <div id="main">
      <aside id="sidebar">
        <div class="sidebar-header">Queue</div>
        <div id="task-list"></div>
      </aside>

      <section id="detail">
        <div id="detail-empty">
          <p>Select a task to view details</p>
        </div>
        <div id="detail-content" hidden>
          <div id="detail-header">
            <div>
              <h2 id="detail-title"></h2>
              <div id="detail-meta"></div>
            </div>
            <div id="detail-actions"></div>
          </div>
          <div id="detail-body"></div>
        </div>
      </section>
    </div>
  </div>

  <!-- New Task Modal -->
  <div id="modal-overlay" class="modal-overlay" hidden>
    <div class="modal">
      <div class="modal-header">
        <h2>New Task</h2>
        <button class="modal-close" onclick="closeModal()">&times;</button>
      </div>
      <div class="modal-body">
        <div id="step-form">
          <label>Task Name<input type="text" id="inp-name" placeholder="e.g. add-auth-tests"></label>
          <label>Description<textarea id="inp-desc" rows="4" placeholder="Short description of what to build..."></textarea></label>
          <div class="form-row">
            <label>Project<select id="inp-project"></select></label>
            <label>Executor Model<select id="inp-model"></select></label>
          </div>
          <div class="form-row">
            <label>Priority<select id="inp-priority">
              <option value="1">1 (Highest)</option>
              <option value="2">2</option>
              <option value="3" selected>3 (Default)</option>
              <option value="4">4</option>
              <option value="5">5 (Lowest)</option>
            </select></label>
            <label>Max Runtime<select id="inp-runtime">
              <option value="10m">10 minutes</option>
              <option value="25m" selected>25 minutes</option>
              <option value="30m">30 minutes</option>
              <option value="1h">1 hour</option>
            </select></label>
          </div>
          <button id="btn-generate" class="btn-primary" onclick="generatePlan()">Generate Plan</button>
        </div>
        <div id="step-review" hidden>
          <div class="review-header">
            <h3>Review Generated Plan</h3>
            <span id="gen-status" class="badge">generating...</span>
          </div>
          <textarea id="inp-plan-body" rows="20"></textarea>
          <div class="review-actions">
            <button class="btn-secondary" onclick="backToForm()">Back</button>
            <button id="btn-dispatch" class="btn-primary" onclick="dispatchTask()">Dispatch</button>
          </div>
        </div>
      </div>
    </div>
  </div>

  <script src="app.js"></script>
</body>
</html>
```

- [ ] **Step 2: Create style.css**

Implement the GitHub-dark themed CSS matching the mockup from the design phase. The CSS should cover:
- Dark theme with `#0d1117` background, `#161b22` sidebar
- Status dot colors (orange running, gray pending, green done, red failed)
- Pulsing animation for running tasks
- Monospace log viewer area
- Modal overlay for new task creation
- Responsive two-panel layout

The file is ~300 lines of CSS. Key selectors: `#topbar`, `#sidebar`, `.task-item`, `.task-dot`, `#detail`, `.log-viewer`, `.modal`, `.btn-primary`, `.badge`, `.status-*`.

- [ ] **Step 3: Create app.js**

Implement the JavaScript for:
- `fetchTasks()` — GET /api/tasks, render task list, auto-refresh every 3s
- `selectTask(name)` — show task detail, connect SSE for live logs if running
- `generatePlan()` — POST /api/generate-plan with description (placeholder for Task 6)
- `dispatchTask()` — POST /api/tasks with form data + plan body
- `cancelTask(name)` — POST /api/tasks/{name}/cancel
- `retryTask(name)` — POST /api/tasks/{name}/retry
- `deleteTask(name)` — DELETE /api/tasks/{name}
- SSE log streaming: `new EventSource('/api/tasks/{name}/logs')` with auto-scroll

Key functions:
- `renderTaskList(tasks)` — builds sidebar HTML with status dots and metadata
- `renderDetail(task)` — builds detail panel with action buttons based on status
- `connectLogs(name)` — creates EventSource, appends lines to `.log-viewer` div
- `openModal()` / `closeModal()` — toggle the new task modal
- `loadConfig()` — GET /api/config to populate project/model dropdowns

- [ ] **Step 4: Verify the full app compiles and serves**

Run: `cd /Users/donny/Projects/2026/geoffreyengram && go build ./cmd/dispatch-ui/ && ./dispatch-ui`
Expected: Server starts, http://localhost:8090 shows the dashboard (may have empty task list if no plans exist)

- [ ] **Step 5: Commit**

```bash
git add cmd/dispatch-ui/static/
git commit -m "feat(dispatch-ui): vanilla JS frontend with dashboard, log viewer, task creation"
```

---

### Task 6: Plan Generation via Claude CLI

Call the planning model to expand short descriptions into full plan markdown.

**Files:**
- Create: `cmd/dispatch-ui/plangen.go`
- Modify: `cmd/dispatch-ui/server.go` (add /api/generate-plan handler)

- [ ] **Step 1: Implement plan generation**

```go
// cmd/dispatch-ui/plangen.go
package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// GeneratePlan calls the planning model to expand a short description into a full plan.
func (e *Engine) GeneratePlan(ctx context.Context, description, project string) (string, error) {
	// Gather project context
	var contextParts []string

	// 1. CLAUDE.md
	claudeMD := filepath.Join(project, ".claude", "CLAUDE.md")
	if data, err := os.ReadFile(claudeMD); err == nil {
		contextParts = append(contextParts, "## Project Instructions (CLAUDE.md)\n\n"+string(data))
	}

	// 2. Recent git log
	gitCmd := exec.CommandContext(ctx, "git", "log", "--oneline", "-20")
	gitCmd.Dir = project
	if out, err := gitCmd.Output(); err == nil {
		contextParts = append(contextParts, "## Recent Commits\n\n```\n"+string(out)+"```")
	}

	// 3. File listing
	var files []string
	filepath.Walk(project, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(project, path)
		// Skip hidden dirs, node_modules, vendor, etc.
		if info.IsDir() {
			if strings.HasPrefix(info.Name(), ".") || info.Name() == "node_modules" || info.Name() == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		// Only include code files
		ext := filepath.Ext(path)
		if ext == ".go" || ext == ".ts" || ext == ".js" || ext == ".py" || ext == ".rs" {
			files = append(files, rel)
		}
		if len(files) >= 50 {
			return filepath.SkipAll
		}
		return nil
	})
	if len(files) > 0 {
		contextParts = append(contextParts, "## Project Files\n\n```\n"+strings.Join(files, "\n")+"\n```")
	}

	// 4. Example plan (most recent done plan for this project)
	plans, _ := e.ListPlans()
	for _, p := range plans {
		if p.Status == "done" && p.Project == project {
			contextParts = append(contextParts, "## Example Plan (for format reference)\n\n```markdown"+p.Body+"```")
			break
		}
	}

	projectContext := strings.Join(contextParts, "\n\n---\n\n")

	metaPrompt := fmt.Sprintf(`You are a plan author for an automated dispatch system. Given a short task description and project context, produce a detailed implementation plan in markdown format.

The plan should follow this structure:
1. ## Context — explain what exists and why this change is needed
2. ## Key files to read first — list the specific files the agent should read before starting (with explanations of what to look for)
3. ## Instructions — step-by-step instructions with code examples where helpful
4. ## Acceptance criteria — how to verify the work is complete (specific test commands, build commands)

Requirements:
- Be specific about file paths
- Include code snippets where they help clarify intent
- Reference existing patterns in the codebase
- End with concrete verification steps (test commands, build commands)
- Do NOT include YAML frontmatter — just the plan body

## Project Context

%s

## Task Description

%s

Write the plan now:`, projectContext, description)

	// Resolve planning model auth
	apiKeyEnv := e.Config.PlanningAPIKeyEnv
	apiKey := e.Config.EnvVars[apiKeyEnv]
	if apiKey == "" {
		apiKey = os.Getenv(apiKeyEnv)
	}

	env := os.Environ()
	if e.Config.PlanningBaseURL != "" {
		env = append(env, "ANTHROPIC_AUTH_TOKEN="+apiKey)
		env = append(env, "ANTHROPIC_BASE_URL="+e.Config.PlanningBaseURL)
	} else {
		env = append(env, "ANTHROPIC_API_KEY="+apiKey)
	}
	env = append(env, "ANTHROPIC_DEFAULT_SONNET_MODEL="+e.Config.PlanningModel)

	genCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(genCtx, e.Config.ClaudeBin,
		"--bare", "-p", metaPrompt,
		"--output-format", "text",
		"--no-session-persistence",
	)
	cmd.Env = env
	cmd.Dir = project

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("plan generation failed: %w\nstderr: %s", err, stderr.String())
	}

	return stdout.String(), nil
}
```

- [ ] **Step 2: Add /api/generate-plan handler to server.go**

Add to `routes()`:
```go
s.mux.HandleFunc("POST /api/generate-plan", s.handleGeneratePlan)
```

Add handler:
```go
func (s *Server) handleGeneratePlan(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Description string `json:"description"`
		Project     string `json:"project"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid JSON")
		return
	}
	if req.Description == "" || req.Project == "" {
		writeError(w, 400, "description and project are required")
		return
	}

	body, err := s.engine.GeneratePlan(r.Context(), req.Description, req.Project)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]string{"body": body})
}
```

- [ ] **Step 3: Verify it compiles**

Run: `go build ./cmd/dispatch-ui/`
Expected: Compiles

- [ ] **Step 4: Commit**

```bash
git add cmd/dispatch-ui/plangen.go cmd/dispatch-ui/server.go
git commit -m "feat(dispatch-ui): plan generation via Claude CLI with project context"
```

---

### Task 7: Integration Test and Polish

End-to-end test with mock claude binary, fix any compilation issues, verify the full flow.

**Files:**
- Create: `cmd/dispatch-ui/integration_test.go`
- Modify: any files with compilation issues

- [ ] **Step 1: Write integration test**

```go
// cmd/dispatch-ui/integration_test.go
package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setupTestServer(t *testing.T) (*Server, string) {
	dir := t.TempDir()
	plansDir := filepath.Join(dir, "plans")
	reportsDir := filepath.Join(dir, "reports")
	os.MkdirAll(plansDir, 0755)
	os.MkdirAll(reportsDir, 0755)

	// Create a mock claude
	mockClaude := filepath.Join(dir, "mock-claude")
	os.WriteFile(mockClaude, []byte("#!/bin/bash\necho \"Done!\"\n"), 0755)

	cfg := &DispatchConfig{
		PlansDir:   plansDir,
		ReportsDir: reportsDir,
		ClaudeBin:  mockClaude,
		EnvVars:    map[string]string{"TEST_KEY": "fake"},
	}
	eng := NewEngine(cfg)
	srv := NewServer(eng)
	return srv, plansDir
}

func TestAPIListTasksEmpty(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/tasks", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	var tasks []APITask
	json.NewDecoder(w.Body).Decode(&tasks)
	if len(tasks) != 0 {
		t.Errorf("expected empty list, got %d", len(tasks))
	}
}

func TestAPICreateAndListTask(t *testing.T) {
	srv, _ := setupTestServer(t)

	// Create a task
	body := `{"name":"test-task","title":"Test","project":"/tmp","model":"glm-5.1","body":"## Do stuff\n\nDo it."}`
	req := httptest.NewRequest("POST", "/api/tasks", strings.NewReader(body))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != 201 {
		t.Fatalf("create status = %d, body = %s", w.Code, w.Body.String())
	}

	// List tasks
	req = httptest.NewRequest("GET", "/api/tasks", nil)
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	var tasks []APITask
	json.NewDecoder(w.Body).Decode(&tasks)
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
	if tasks[0].Name != "test-task" {
		t.Errorf("name = %q", tasks[0].Name)
	}
	if tasks[0].Status != "pending" {
		t.Errorf("status = %q", tasks[0].Status)
	}
}

func TestAPIDeleteTask(t *testing.T) {
	srv, plansDir := setupTestServer(t)

	// Create plan file directly
	os.WriteFile(filepath.Join(plansDir, "to-delete.md"), []byte("---\ntitle: Delete me\nstatus: done\n---\nBody\n"), 0644)

	req := httptest.NewRequest("DELETE", "/api/tasks/to-delete", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != 204 {
		t.Errorf("delete status = %d", w.Code)
	}

	// Verify gone
	if _, err := os.Stat(filepath.Join(plansDir, "to-delete.md")); !os.IsNotExist(err) {
		t.Error("file should be deleted")
	}
}
```

- [ ] **Step 2: Run all tests**

Run: `go test ./cmd/dispatch-ui/ -v -timeout 60s`
Expected: All tests pass

- [ ] **Step 3: Build and manual smoke test**

Run: `go build -o /tmp/dispatch-ui ./cmd/dispatch-ui/ && /tmp/dispatch-ui`
Open http://localhost:8090 — verify:
- Dashboard loads with existing plans from `~/.claude/dispatch/plans/`
- Clicking a task shows its detail
- SSE log streaming works for completed tasks (shows report)
- New Task modal opens and form fields populate

- [ ] **Step 4: Commit**

```bash
git add cmd/dispatch-ui/integration_test.go
git commit -m "test(dispatch-ui): integration tests for HTTP API"
```

---

## Verification

After all tasks are complete:

1. **Build:** `go build ./cmd/dispatch-ui/`
2. **Unit tests:** `go test ./cmd/dispatch-ui/ -v`
3. **Manual test:** Run the binary, open http://localhost:8090, verify:
   - Existing plans from `~/.claude/dispatch/plans/` appear in the sidebar
   - Clicking a done/failed task shows the report content
   - Creating a new task via the modal writes a `.md` file to plans/
   - The queue poll picks up pending tasks and executes them
4. **No regressions:** `go test ./dualmem/...` (existing tests still pass)
