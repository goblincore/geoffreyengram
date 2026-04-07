package dualmem

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// setupTestGitRepo creates a temporary git repo with 3 commits.
// Returns (repoDir, firstCommit, latestCommit).
func setupStalenessTestGitRepo(t *testing.T) (string, string, string) {
	t.Helper()
	repoDir := t.TempDir()

	run := func(name string, args ...string) {
		cmd := exec.Command(name, args...)
		cmd.Dir = repoDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s %v: %s: %v", name, args, string(out), err)
		}
	}

	// Init repo
	run("git", "init")
	run("git", "config", "user.email", "test@test.com")
	run("git", "config", "user.name", "Test")

	// Commit 1: initial files
	os.WriteFile(filepath.Join(repoDir, "auth.go"), []byte("package auth\nfunc ValidateToken() {}\n"), 0644)
	os.WriteFile(filepath.Join(repoDir, "middleware.go"), []byte("package mw\n"), 0644)
	run("git", "add", ".")
	run("git", "commit", "-m", "initial")

	firstCommit := gitCurrentCommit(repoDir)

	// Commit 2: modify auth.go and middleware.go
	os.WriteFile(filepath.Join(repoDir, "auth.go"), []byte("package auth\nfunc ValidateToken() {}\nfunc RefreshToken() {}\n"), 0644)
	os.WriteFile(filepath.Join(repoDir, "middleware.go"), []byte("package mw\nfunc Chain() {}\n"), 0644)
	run("git", "add", ".")
	run("git", "commit", "-m", "update auth and middleware")

	// Commit 3: add new file (doesn't affect tested files)
	os.WriteFile(filepath.Join(repoDir, "handler.go"), []byte("package handler\n"), 0644)
	run("git", "add", ".")
	run("git", "commit", "-m", "add handler")

	latestCommit := gitCurrentCommit(repoDir)

	return repoDir, firstCommit, latestCommit
}

func TestCheckFileStaleness(t *testing.T) {
	repoDir, firstCommit, _ := setupStalenessTestGitRepo(t)

	// Memory references 3 files; 2 of 3 changed since firstCommit (66% > 50%)
	result := CheckFileStaleness(repoDir, firstCommit, []string{"auth.go", "middleware.go", "handler.go"})
	if !result.IsStale {
		t.Error("expected stale: 2/3 files changed since firstCommit")
	}
	if result.ChangedRatio == 0 {
		t.Error("expected non-zero changed ratio")
	}
	t.Logf("stale result: isStale=%v ratio=%.2f reason=%q", result.IsStale, result.ChangedRatio, result.Reason)
}

func TestCheckFileStaleness_NoChange(t *testing.T) {
	repoDir, _, latestCommit := setupStalenessTestGitRepo(t)

	// Memory from latest commit — nothing changed since then
	result := CheckFileStaleness(repoDir, latestCommit, []string{"auth.go", "middleware.go"})
	if result.IsStale {
		t.Error("expected not stale: no files changed since latest commit")
	}
	if result.ChangedRatio != 0 {
		t.Errorf("expected 0 changed ratio, got %.2f", result.ChangedRatio)
	}
}

func TestCheckFileStaleness_EmptyFiles(t *testing.T) {
	repoDir, firstCommit, _ := setupStalenessTestGitRepo(t)

	result := CheckFileStaleness(repoDir, firstCommit, nil)
	if result.IsStale {
		t.Error("expected not stale with no files")
	}
}

func TestCheckFileStaleness_EmptyCommit(t *testing.T) {
	repoDir, _, _ := setupStalenessTestGitRepo(t)

	result := CheckFileStaleness(repoDir, "", []string{"auth.go"})
	if result.IsStale {
		t.Error("expected not stale with empty sinceCommit")
	}
}

func TestCheckSymbolStaleness(t *testing.T) {
	// "ValidateJWT" not in current tags → stale
	result := CheckSymbolStaleness("auth.go", "Uses ValidateJWT for auth", []Tag{
		{Name: "ValidateToken", Kind: "function", File: "auth.go"},
	})
	if !result.IsStale {
		t.Error("expected stale: ValidateJWT not in tags")
	}
	t.Logf("symbol stale: %q", result.Reason)
}

func TestCheckSymbolStaleness_NotStale(t *testing.T) {
	// "ValidateToken" IS in current tags → not stale (all CamelCase words matched)
	result := CheckSymbolStaleness("auth.go", "ValidateToken is used for auth", []Tag{
		{Name: "ValidateToken", Kind: "function", File: "auth.go"},
	})
	if result.IsStale {
		t.Error("expected not stale: ValidateToken exists in tags")
	}
}

func TestCheckSymbolStaleness_NoSymbols(t *testing.T) {
	// Text with no CamelCase symbols → not stale (nothing to check)
	result := CheckSymbolStaleness("auth.go", "uses simple auth logic", []Tag{})
	if result.IsStale {
		t.Error("expected not stale when no symbols in text")
	}
}

func TestCheckAgeStaleness(t *testing.T) {
	// 45 days old → stale
	createdAt := time.Now().Add(-45 * 24 * time.Hour)
	result := CheckAgeStaleness(createdAt, 30, false)
	if !result.IsStale {
		t.Error("expected stale: 45 days > 30 day max")
	}
	t.Logf("age stale: %q", result.Reason)
}

func TestCheckAgeStaleness_Recent(t *testing.T) {
	// 5 days old → not stale
	createdAt := time.Now().Add(-5 * 24 * time.Hour)
	result := CheckAgeStaleness(createdAt, 30, false)
	if result.IsStale {
		t.Error("expected not stale: 5 days < 30 day max")
	}
}

func TestCheckAgeStaleness_Reinforced(t *testing.T) {
	// 100 days old but reinforced → not stale
	createdAt := time.Now().Add(-100 * 24 * time.Hour)
	result := CheckAgeStaleness(createdAt, 30, true)
	if result.IsStale {
		t.Error("expected not stale: reinforced memories are never stale")
	}
}
