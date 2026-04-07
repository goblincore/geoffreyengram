package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Engine manages plan execution and log streaming.
type Engine struct {
	Config  *DispatchConfig
	mu      sync.Mutex
	running map[string]*RunningTask // keyed by plan name
	logs    map[string]*LogBuffer
}

// RunningTask holds the currently executing plan and its cancel function.
type RunningTask struct {
	Plan   *Plan
	Cancel func()
}

// LogBuffer is a thread-safe append-only log with pub/sub support.
type LogBuffer struct {
	mu    sync.Mutex
	lines []string
	subs  []chan string
}

// NewEngine creates a new Engine with the given config.
func NewEngine(cfg *DispatchConfig) *Engine {
	return &Engine{
		Config:  cfg,
		running: make(map[string]*RunningTask),
		logs:    make(map[string]*LogBuffer),
	}
}

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

// ListPlans reads all .md files from PlansDir and returns them sorted:
// running first, then pending by descending priority, then done/failed by recency.
func (e *Engine) ListPlans() ([]*Plan, error) {
	entries, err := os.ReadDir(e.Config.PlansDir)
	if err != nil {
		return nil, fmt.Errorf("read plans dir: %w", err)
	}

	var plans []*Plan
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		path := filepath.Join(e.Config.PlansDir, entry.Name())
		plan, err := parsePlanFile(path)
		if err != nil {
			// Skip unparseable files
			continue
		}
		plans = append(plans, plan)
	}

	sort.Slice(plans, func(i, j int) bool {
		return planSortKey(plans[i]) < planSortKey(plans[j])
	})

	return plans, nil
}

// planSortKey returns a sort key string for ordering plans:
// "0" prefix → running (highest priority)
// "1" prefix → pending, sorted by descending priority (padded 9-priority)
// "2" prefix → done/failed/other, sorted by descending recency (FinishedAt desc)
func planSortKey(p *Plan) string {
	switch p.Status {
	case "running":
		return "0:" + p.Name
	case "pending":
		// Higher priority number = lower sort key = earlier in list.
		// Priority 9 → "0", priority 0 → "9". Pad to 3 digits for correct lex order.
		inverted := 999 - p.Priority
		return fmt.Sprintf("1:%04d:%s", inverted, p.Name)
	default:
		// done, failed, etc. — sort by FinishedAt descending (negate by using inverted string).
		// Use "~" minus FinishedAt for descending lex order (tilde > all printable).
		// If no FinishedAt, fall back to name.
		if p.FinishedAt != "" {
			// For descending sort, we flip the string lexicographically.
			// Simple approach: use a constant prefix and sort by name for stability.
			return "2:" + invertString(p.FinishedAt) + ":" + p.Name
		}
		return "2:~:" + p.Name
	}
}

// invertString inverts each byte to reverse lexicographic ordering.
func invertString(s string) string {
	b := make([]byte, len(s))
	for i := range s {
		b[i] = 0xFF - s[i]
	}
	return string(b)
}

// dependenciesMet checks that all plans in plan.DependsOn have status "done".
func (e *Engine) dependenciesMet(plan *Plan) bool {
	for _, dep := range plan.DependsOn {
		depPath := filepath.Join(e.Config.PlansDir, dep+".md")
		depPlan, err := parsePlanFile(depPath)
		if err != nil || depPlan.Status != "done" {
			return false
		}
	}
	return true
}

// Append adds a line to the buffer and notifies all subscribers.
func (lb *LogBuffer) Append(line string) {
	lb.mu.Lock()
	lb.lines = append(lb.lines, line)
	subs := lb.subs
	lb.mu.Unlock()

	for _, ch := range subs {
		select {
		case ch <- line:
		default:
			// Drop if subscriber is not keeping up
		}
	}
}

// Subscribe returns a channel that receives all existing and future log lines,
// plus an unsubscribe function to clean up.
func (lb *LogBuffer) Subscribe() (chan string, func()) {
	ch := make(chan string, 64)

	lb.mu.Lock()
	existing := make([]string, len(lb.lines))
	copy(existing, lb.lines)
	lb.subs = append(lb.subs, ch)
	lb.mu.Unlock()

	// Send existing lines
	go func() {
		for _, line := range existing {
			ch <- line
		}
	}()

	unsub := func() {
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

	return ch, unsub
}
