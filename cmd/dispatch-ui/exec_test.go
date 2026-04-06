package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// makeTempPlan creates a temporary plan file in dir with the given content and returns the path.
func makeTempPlan(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name+".md")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write plan file: %v", err)
	}
	return path
}

// makeScript writes a bash script to dir and returns its path.
func makeScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/bash\n"+body), 0755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	return path
}

func TestParseMaxRuntime(t *testing.T) {
	tests := []struct {
		input string
		want  time.Duration
	}{
		{"10m", 10 * time.Minute},
		{"1h", 1 * time.Hour},
		{"30s", 30 * time.Second},
		{"1800", 1800 * time.Second},
		{"90", 90 * time.Second},
		{"", 0},
		{"invalid", 0},
	}
	for _, tc := range tests {
		got := parseMaxRuntime(tc.input)
		if got != tc.want {
			t.Errorf("parseMaxRuntime(%q) = %v; want %v", tc.input, got, tc.want)
		}
	}
}

func TestExecutePlan(t *testing.T) {
	tmpDir := t.TempDir()
	reportsDir := filepath.Join(tmpDir, "reports")
	if err := os.MkdirAll(reportsDir, 0755); err != nil {
		t.Fatalf("mkdir reports: %v", err)
	}

	// Create mock claude script
	claudePath := makeScript(t, tmpDir, "claude", `echo "Reading files..."
echo "Implementing..."
echo "Tests passing!"
`)

	// Create plan file
	planContent := `---
title: test plan
status: pending
project: ` + tmpDir + `
max_runtime: 30s
allowed_tools: Bash
---
Do some work.
`
	planPath := makeTempPlan(t, tmpDir, "test-plan", planContent)

	cfg := &DispatchConfig{
		PlansDir:   tmpDir,
		ReportsDir: reportsDir,
		ClaudeBin:  claudePath,
		EnvVars:    map[string]string{},
	}
	engine := NewEngine(cfg)

	plan, err := parsePlanFile(planPath)
	if err != nil {
		t.Fatalf("parse plan: %v", err)
	}

	ctx := context.Background()
	if err := engine.ExecutePlan(ctx, plan); err != nil {
		t.Fatalf("ExecutePlan: %v", err)
	}

	// Re-parse plan to check updated frontmatter
	updated, err := parsePlanFile(planPath)
	if err != nil {
		t.Fatalf("re-parse plan: %v", err)
	}

	if updated.Status != "done" {
		t.Errorf("status = %q; want %q", updated.Status, "done")
	}
	if updated.ExitCode != 0 {
		t.Errorf("exit_code = %d; want 0", updated.ExitCode)
	}
	if updated.Report == "" {
		t.Error("report field is empty")
	}

	// Verify report file exists and contains output
	if _, err := os.Stat(updated.Report); err != nil {
		t.Errorf("report file %q does not exist: %v", updated.Report, err)
	} else {
		data, _ := os.ReadFile(updated.Report)
		reportContent := string(data)
		if !strings.Contains(reportContent, "Tests passing!") {
			t.Errorf("report does not contain expected output; got:\n%s", reportContent)
		}
	}

	// Verify log buffer was populated
	lb := engine.logs[plan.Name]
	if lb == nil {
		t.Fatal("log buffer not created for plan")
	}
	lb.mu.Lock()
	logLen := len(lb.lines)
	lb.mu.Unlock()
	if logLen == 0 {
		t.Error("log buffer is empty")
	}
}

func TestExecutePlanTimeout(t *testing.T) {
	tmpDir := t.TempDir()
	reportsDir := filepath.Join(tmpDir, "reports")
	if err := os.MkdirAll(reportsDir, 0755); err != nil {
		t.Fatalf("mkdir reports: %v", err)
	}

	// Create mock claude that sleeps forever
	claudePath := makeScript(t, tmpDir, "claude-slow", `echo "Starting..."
sleep 60
`)

	planContent := `---
title: slow plan
status: pending
project: ` + tmpDir + `
max_runtime: 2s
allowed_tools: Bash
---
Do slow work.
`
	planPath := makeTempPlan(t, tmpDir, "slow-plan", planContent)

	cfg := &DispatchConfig{
		PlansDir:   tmpDir,
		ReportsDir: reportsDir,
		ClaudeBin:  claudePath,
		EnvVars:    map[string]string{},
	}
	engine := NewEngine(cfg)

	plan, err := parsePlanFile(planPath)
	if err != nil {
		t.Fatalf("parse plan: %v", err)
	}

	start := time.Now()
	ctx := context.Background()
	// ExecutePlan should return (possibly with error) after timeout
	_ = engine.ExecutePlan(ctx, plan)
	elapsed := time.Since(start)

	// Should complete in roughly 2-3s, not 60s
	if elapsed > 10*time.Second {
		t.Errorf("execution took %v; expected ~2s (timeout)", elapsed)
	}

	// Re-parse plan
	updated, err := parsePlanFile(planPath)
	if err != nil {
		t.Fatalf("re-parse plan: %v", err)
	}

	if updated.Status != "failed" {
		t.Errorf("status = %q; want %q", updated.Status, "failed")
	}
}

func TestCancelRunning(t *testing.T) {
	// With no running task, CancelRunning should return false
	cfg := &DispatchConfig{
		PlansDir:   t.TempDir(),
		ReportsDir: t.TempDir(),
		EnvVars:    map[string]string{},
	}
	engine := NewEngine(cfg)
	if engine.CancelRunning() {
		t.Error("CancelRunning() = true; want false when nothing running")
	}
}
