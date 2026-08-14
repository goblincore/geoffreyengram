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

// base_branch was accepted in plan frontmatter for months while nothing parsed
// it, so worktrees silently came off HEAD. These two tests pin both halves of
// that bug: it must survive the parse, and it must survive a write-back.
func TestParsePlanFileBaseBranch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "based.md")
	content := `---
title: "Based plan"
status: pending
project: /tmp/myproject
branch: dispatch/child
base_branch: feature/the-base
---

Body.
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	plan, err := parsePlanFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if plan.BaseBranch != "feature/the-base" {
		t.Errorf("BaseBranch = %q, want %q", plan.BaseBranch, "feature/the-base")
	}
}

func TestPlanRoundTripPreservesBaseBranch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rt.md")
	content := `---
title: "Round trip"
status: pending
project: /tmp/myproject
branch: dispatch/child
base_branch: feature/the-base
---

Body.
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	plan, err := parsePlanFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Status updates rewrite the file; the base must not be dropped en route.
	plan.Status = "running"
	if err := writePlanFile(plan); err != nil {
		t.Fatal(err)
	}
	reread, err := parsePlanFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if reread.BaseBranch != "feature/the-base" {
		t.Errorf("BaseBranch after round trip = %q, want %q", reread.BaseBranch, "feature/the-base")
	}
}
