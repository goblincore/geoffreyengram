package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParsePlanFileSetupFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.md")
	content := `---
title: "Setup plan"
status: pending
harness: pi
setup: "pnpm install --frozen-lockfile --prefer-offline"
setup_timeout: 8m
---

Body.
`
	os.WriteFile(path, []byte(content), 0644)

	plan, err := parsePlanFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Setup != "pnpm install --frozen-lockfile --prefer-offline" {
		t.Errorf("setup = %q", plan.Setup)
	}
	if plan.SetupTimeout != "8m" {
		t.Errorf("setup_timeout = %q", plan.SetupTimeout)
	}
}

func TestResolveSetupCommand(t *testing.T) {
	cases := []struct {
		name      string
		planSetup string
		def       string
		want      string
	}{
		{"task overrides default", "pnpm i", "yarn", "pnpm i"},
		{"falls back to default", "", "pnpm install", "pnpm install"},
		{"nothing configured", "", "", ""},
		{"task opts out with none", "none", "pnpm install", ""},
		{"task opts out with skip (case-insensitive)", "SKIP", "pnpm install", ""},
		{"trims whitespace", "  pnpm i  ", "", "pnpm i"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := &Plan{Setup: tc.planSetup}
			cfg := &DispatchConfig{DefaultSetupCmd: tc.def}
			if got := resolveSetupCommand(plan, cfg); got != tc.want {
				t.Errorf("resolveSetupCommand() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestResolveSetupTimeout(t *testing.T) {
	cases := []struct {
		name   string
		planTO string
		defTO  string
		want   time.Duration
	}{
		{"task override wins", "2m", "10m", 2 * time.Minute},
		{"default when task empty", "", "10m", 10 * time.Minute},
		{"builtin fallback when both empty", "", "", defaultSetupTimeout},
		{"builtin fallback when both invalid", "garbage", "", defaultSetupTimeout},
		{"bare integer seconds", "90", "", 90 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := &Plan{SetupTimeout: tc.planTO}
			cfg := &DispatchConfig{DefaultSetupTimeout: tc.defTO}
			if got := resolveSetupTimeout(plan, cfg); got != tc.want {
				t.Errorf("resolveSetupTimeout() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRunSetupSuccess(t *testing.T) {
	e := &Engine{}
	lb := &LogBuffer{}
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	code, err := e.runSetup(context.Background(), "echo provisioning && touch ran", dir, os.Environ(), 30*time.Second, lb, nil)
	if err != nil {
		t.Fatalf("runSetup error: %v (code %d)", err, code)
	}
	if code != 0 {
		t.Errorf("code = %d, want 0", code)
	}
	if _, statErr := os.Stat(marker); statErr != nil {
		t.Errorf("setup command did not run in workDir: %v", statErr)
	}
}

func TestRunSetupFailure(t *testing.T) {
	e := &Engine{}
	lb := &LogBuffer{}
	code, err := e.runSetup(context.Background(), "exit 7", t.TempDir(), os.Environ(), 30*time.Second, lb, nil)
	if err == nil {
		t.Fatal("expected error on non-zero exit")
	}
	if code != 7 {
		t.Errorf("code = %d, want 7", code)
	}
}

func TestRunSetupTimeout(t *testing.T) {
	e := &Engine{}
	lb := &LogBuffer{}
	code, err := e.runSetup(context.Background(), "sleep 5", t.TempDir(), os.Environ(), 100*time.Millisecond, lb, nil)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if code != 124 {
		t.Errorf("code = %d, want 124 (timeout)", code)
	}
}
