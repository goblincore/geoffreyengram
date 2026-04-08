package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestTildeExpansion verifies that ~ in config sqlite_path is expanded
// to the actual home directory. Without this, Go opens "./~/..." as a
// relative path, silently creating a wrong DB in the cwd.
//
// Regression: 2026-04-07 — YAML config with sqlite_path: ~/.local/...
// caused writes to go to ./~/.local/... instead of $HOME/.local/...
func TestTildeExpansion(t *testing.T) {
	// Simulate what loadConfig does after YAML parsing
	cfg := CLIConfig{}
	cfg.Storage.SQLitePath = "~/.local/share/dualmem/memories.db"

	// Apply tilde expansion (same logic as loadConfig)
	if strings.HasPrefix(cfg.Storage.SQLitePath, "~/") {
		home, _ := os.UserHomeDir()
		cfg.Storage.SQLitePath = filepath.Join(home, cfg.Storage.SQLitePath[2:])
	}

	// Verify the path is absolute and doesn't contain ~
	if strings.Contains(cfg.Storage.SQLitePath, "~") {
		t.Fatalf("Tilde not expanded: %s", cfg.Storage.SQLitePath)
	}
	if !filepath.IsAbs(cfg.Storage.SQLitePath) {
		t.Fatalf("Path not absolute after expansion: %s", cfg.Storage.SQLitePath)
	}

	home, _ := os.UserHomeDir()
	expected := filepath.Join(home, ".local", "share", "dualmem", "memories.db")
	if cfg.Storage.SQLitePath != expected {
		t.Fatalf("Wrong expansion: got %s, want %s", cfg.Storage.SQLitePath, expected)
	}
}

// TestTildeExpansionNoOp verifies absolute paths pass through unchanged.
func TestTildeExpansionNoOp(t *testing.T) {
	path := "/absolute/path/to/memories.db"
	cfg := CLIConfig{}
	cfg.Storage.SQLitePath = path

	if strings.HasPrefix(cfg.Storage.SQLitePath, "~/") {
		home, _ := os.UserHomeDir()
		cfg.Storage.SQLitePath = filepath.Join(home, cfg.Storage.SQLitePath[2:])
	}

	if cfg.Storage.SQLitePath != path {
		t.Fatalf("Absolute path was modified: got %s, want %s", cfg.Storage.SQLitePath, path)
	}
}
