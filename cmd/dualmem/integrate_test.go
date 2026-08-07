package main

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	harnessintegrate "github.com/goblincore/geoffreyengram/dualmem/integrate"
)

func TestIntegrateDoctorJSONExitCodesAndArgumentErrors(t *testing.T) {
	home := t.TempDir()
	project, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if status := runIntegrate([]string{"doctor", "--home", home, "--project", project, "--json"}, &out, &errOut); status != 0 {
		t.Fatalf("clean doctor status = %d; stderr=%q", status, errOut.String())
	}
	var findings []harnessintegrate.Finding
	if err := json.Unmarshal(out.Bytes(), &findings); err != nil {
		t.Fatalf("doctor JSON is not a finding array: %v; output=%q", err, out.String())
	}
	if len(findings) == 0 {
		t.Fatal("doctor returned no project finding")
	}

	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errOut.Reset()
	if status := runIntegrate([]string{"doctor", "--home", home, "--project", project, "--json"}, &out, &errOut); status != 1 {
		t.Fatalf("drifted doctor status = %d, want 1; stderr=%q", status, errOut.String())
	}

	out.Reset()
	errOut.Reset()
	if status := runIntegrate([]string{"doctor", "--unknown"}, &out, &errOut); status != 2 {
		t.Fatalf("invalid doctor status = %d, want 2; stderr=%q", status, errOut.String())
	}

	brokenHome := t.TempDir()
	if err := os.MkdirAll(filepath.Join(brokenHome, ".claude", "settings.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errOut.Reset()
	if status := runIntegrate([]string{"doctor", "--home", brokenHome, "--project", project}, &out, &errOut); status != 2 {
		t.Fatalf("unreadable configuration status = %d, want 2; stderr=%q", status, errOut.String())
	}
}

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

func TestDocumentedIntegrateCommands(t *testing.T) {
	documented := [][]string{
		{"integrate", "--harness", "all", "--dry-run", "--home", "/tmp/home"},
		{"integrate", "--harness", "pi", "--home", "/tmp/home"},
		{"integrate", "doctor", "--home", "/tmp/home", "--project", "/tmp/repo", "--json"},
		{"integrate", "--harness", "codex", "--uninstall", "--dry-run", "--home", "/tmp/home"},
	}

	for _, command := range documented {
		command := append([]string(nil), command...)
		home := t.TempDir()
		project, err := filepath.Abs(filepath.Join("..", ".."))
		if err != nil {
			t.Fatal(err)
		}
		for i, arg := range command {
			switch arg {
			case "/tmp/home":
				command[i] = home
			case "/tmp/repo":
				command[i] = project
			}
		}
		var out, errOut bytes.Buffer
		if status := runIntegrate(command[1:], &out, &errOut); status != 0 {
			t.Fatalf("documented command %q status = %d; stderr=%q", command, status, errOut.String())
		}
	}

	readme, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, claim := range []string{
		"Claude Code, Codex, and pi",
		"dualmem integrate --harness all",
		"DualMem Event v1",
		"Deprecated:",
	} {
		if !strings.Contains(string(readme), claim) {
			t.Errorf("README missing documented integration claim %q", claim)
		}
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
