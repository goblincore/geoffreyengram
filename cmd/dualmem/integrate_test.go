package main

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIntegrateDryRunPrintsMetadataOnlyAndMakesNoWrites(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")
	var out, errOut bytes.Buffer

	status := runIntegrate([]string{"--harness", "claude", "--home", home, "--dry-run"}, &out, &errOut)
	if status != 0 {
		t.Fatalf("runIntegrate status = %d; stderr=%q", status, errOut.String())
	}
	for _, field := range []string{"harness=shared", "harness=claude", "path=", "action=", "mode=", "backup="} {
		if !strings.Contains(out.String(), field) {
			t.Fatalf("dry-run output missing %q: %q", field, out.String())
		}
	}
	for _, body := range []string{"#!/bin/sh", "DualMem provides", "hook --adapter"} {
		if strings.Contains(out.String()+errOut.String(), body) {
			t.Fatalf("dry-run output exposed file body %q", body)
		}
	}
	for _, path := range []string{
		filepath.Join(home, ".config", "dualmem", "env"),
		filepath.Join(home, ".config", "dualmem", "bin", "dualmem-run"),
		filepath.Join(home, ".claude", "settings.json"),
		filepath.Join(home, ".claude", "CLAUDE.md"),
	} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("dry-run wrote %s: %v", path, err)
		}
	}
}

func TestIntegrateAppliesAndUninstallsOwnedPiFilesWithoutProvider(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".pi", "agent"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")
	var installOut, installErr bytes.Buffer
	if status := runIntegrate([]string{"--harness", "pi", "--home", home}, &installOut, &installErr); status != 0 {
		t.Fatalf("install status = %d; stderr=%q", status, installErr.String())
	}
	assertIntegrateMode(t, filepath.Join(home, ".config", "dualmem", "env"), 0o600)
	assertIntegrateMode(t, filepath.Join(home, ".config", "dualmem", "bin", "dualmem-run"), 0o700)
	assertIntegrateMode(t, filepath.Join(home, ".pi", "agent", "extensions", "dualmem.ts"), 0o700)

	var uninstallOut, uninstallErr bytes.Buffer
	if status := runIntegrate([]string{"--harness", "pi", "--home", home, "--uninstall"}, &uninstallOut, &uninstallErr); status != 0 {
		t.Fatalf("uninstall status = %d; stderr=%q", status, uninstallErr.String())
	}
	for _, path := range []string{
		filepath.Join(home, ".config", "dualmem", "env"),
		filepath.Join(home, ".config", "dualmem", "bin", "dualmem-run"),
		filepath.Join(home, ".pi", "agent", "extensions", "dualmem.ts"),
		filepath.Join(home, ".pi", "agent", "AGENTS.md"),
	} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("uninstall left owned path %s: %v", path, err)
		}
	}
}

func TestIntegrateRejectsInvalidArgumentsBeforeWrites(t *testing.T) {
	home := t.TempDir()
	tests := [][]string{
		{"--home", home},
		{"--harness", "unknown", "--home", home},
		{"--harness", "claude", "--home", home, "unexpected"},
	}
	for _, args := range tests {
		var out, errOut bytes.Buffer
		if status := runIntegrate(args, &out, &errOut); status != 2 {
			t.Fatalf("runIntegrate(%v) status = %d, want 2; stderr=%q", args, status, errOut.String())
		}
	}
	if _, err := os.Lstat(filepath.Join(home, ".config")); !os.IsNotExist(err) {
		t.Fatalf("invalid arguments wrote to home: %v", err)
	}
}

func assertIntegrateMode(t *testing.T, path string, want fs.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want.Perm() {
		t.Fatalf("%s mode = %04o, want %04o", path, got, want.Perm())
	}
}
