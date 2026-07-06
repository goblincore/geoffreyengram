package dualmem

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// setupTestGitRepo creates a temp git repo with 3 commits touching auth.go, jwt.go, middleware.go.
// Commit 1: auth.go + jwt.go
// Commit 2: auth.go + jwt.go + middleware.go
// Commit 3: middleware.go + handler.go
func setupTestGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	// Init repo
	runGitForCochange(t, dir, "init")
	runGitForCochange(t, dir, "config", "user.email", "test@test.com")
	runGitForCochange(t, dir, "config", "user.name", "Test")

	// Commit dates are relative to now so the fixture always falls inside
	// ParseGitLog's --since window (fixed dates rotted once the wall clock
	// moved 12+ months past them).
	commitDate := time.Now().UTC().AddDate(0, -3, 0)
	dateEnv := "GIT_AUTHOR_DATE=" + commitDate.Format(time.RFC3339)
	committerEnv := "GIT_COMMITTER_DATE=" + commitDate.Format(time.RFC3339)

	// Commit 1: auth.go + jwt.go
	writeFileForCochange(t, dir, "auth.go", "package auth\n")
	writeFileForCochange(t, dir, "jwt.go", "package jwt\n")
	runGitWithEnvForCochange(t, dir, []string{dateEnv, committerEnv}, "add", "auth.go", "jwt.go")
	runGitWithEnvForCochange(t, dir, []string{dateEnv, committerEnv}, "commit", "-m", "add auth and jwt")

	// Commit 2 (a month later): auth.go + jwt.go + middleware.go
	commitDate2 := time.Now().UTC().AddDate(0, -2, 0)
	dateEnv2 := "GIT_AUTHOR_DATE=" + commitDate2.Format(time.RFC3339)
	committerEnv2 := "GIT_COMMITTER_DATE=" + commitDate2.Format(time.RFC3339)

	writeFileForCochange(t, dir, "auth.go", "package auth\n// updated")
	writeFileForCochange(t, dir, "jwt.go", "package jwt\n// updated")
	writeFileForCochange(t, dir, "middleware.go", "package middleware\n")
	runGitWithEnvForCochange(t, dir, []string{dateEnv2, committerEnv2}, "add", "auth.go", "jwt.go", "middleware.go")
	runGitWithEnvForCochange(t, dir, []string{dateEnv2, committerEnv2}, "commit", "-m", "update auth and jwt, add middleware")

	// Commit 3 (a month after that): middleware.go + handler.go
	commitDate3 := time.Now().UTC().AddDate(0, -1, 0)
	dateEnv3 := "GIT_AUTHOR_DATE=" + commitDate3.Format(time.RFC3339)
	committerEnv3 := "GIT_COMMITTER_DATE=" + commitDate3.Format(time.RFC3339)

	writeFileForCochange(t, dir, "middleware.go", "package middleware\n// updated")
	writeFileForCochange(t, dir, "handler.go", "package handler\n")
	runGitWithEnvForCochange(t, dir, []string{dateEnv3, committerEnv3}, "add", "middleware.go", "handler.go")
	runGitWithEnvForCochange(t, dir, []string{dateEnv3, committerEnv3}, "commit", "-m", "update middleware, add handler")

	return dir
}

func runGitForCochange(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %s: %v", args, string(out), err)
	}
}

func runGitWithEnvForCochange(t *testing.T, dir string, env []string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %s: %v", args, string(out), err)
	}
}

func writeFileForCochange(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestParseGitLog(t *testing.T) {
	dir := setupTestGitRepo(t)

	commits, err := ParseGitLog(dir, 12)
	if err != nil {
		t.Fatalf("ParseGitLog: %v", err)
	}

	if len(commits) != 3 {
		t.Fatalf("expected 3 commits, got %d", len(commits))
	}

	// First commit should be the most recent (git log is reverse chronological)
	c := commits[0]
	if c.Subject != "update middleware, add handler" {
		t.Errorf("commit[0] subject = %q, want 'update middleware, add handler'", c.Subject)
	}
	if len(c.Files) != 2 {
		t.Errorf("commit[0] files = %d, want 2", len(c.Files))
	}

	// Second commit
	c = commits[1]
	if c.Subject != "update auth and jwt, add middleware" {
		t.Errorf("commit[1] subject = %q", c.Subject)
	}
	if len(c.Files) != 3 {
		t.Errorf("commit[1] files = %d, want 3", len(c.Files))
	}

	// Third commit (oldest)
	c = commits[2]
	if c.Subject != "add auth and jwt" {
		t.Errorf("commit[2] subject = %q", c.Subject)
	}
	if len(c.Files) != 2 {
		t.Errorf("commit[2] files = %d, want 2", len(c.Files))
	}
}

func TestBuildGitCoChangeEdges(t *testing.T) {
	dir := setupTestGitRepo(t)

	commits, err := ParseGitLog(dir, 12)
	if err != nil {
		t.Fatalf("ParseGitLog: %v", err)
	}

	edges := BuildGitCoChangeEdges(commits)

	// Expected pairs:
	// Commit 1: auth.go + jwt.go
	// Commit 2: auth.go + jwt.go, auth.go + middleware.go, jwt.go + middleware.go
	// Commit 3: middleware.go + handler.go
	//
	// So:
	//   auth.go + jwt.go: count=2
	//   auth.go + middleware.go: count=1
	//   jwt.go + middleware.go: count=1
	//   middleware.go + handler.go: count=1

	if len(edges) != 4 {
		t.Fatalf("expected 4 edges, got %d", len(edges))
	}

	// Find the auth.go + jwt.go edge
	var authJwtEdge *GitCoChangeEdge
	for _, e := range edges {
		if (e.FileA == "auth.go" && e.FileB == "jwt.go") ||
			(e.FileA == "jwt.go" && e.FileB == "auth.go") {
			authJwtEdge = e
			break
		}
	}

	if authJwtEdge == nil {
		t.Fatal("auth.go + jwt.go edge not found")
	}
	if authJwtEdge.CoCount != 2 {
		t.Errorf("auth.go + jwt.go co_count = %d, want 2", authJwtEdge.CoCount)
	}
	if authJwtEdge.Weight <= 0 {
		t.Errorf("auth.go + jwt.go weight = %f, want > 0", authJwtEdge.Weight)
	}

	// auth+jwt should have higher weight than single-commit pairs since it appears in 2 commits
	var maxSingleWeight float64
	for _, e := range edges {
		if e != authJwtEdge && e.Weight > maxSingleWeight {
			maxSingleWeight = e.Weight
		}
	}
	if authJwtEdge.Weight <= maxSingleWeight {
		t.Errorf("auth.go + jwt.go weight (%f) should be > max single-commit weight (%f)",
			authJwtEdge.Weight, maxSingleWeight)
	}
}

func TestCoChangeNeighbors(t *testing.T) {
	edges := map[string]*GitCoChangeEdge{
		"a\x00b": {FileA: "a.go", FileB: "b.go", Weight: 2.0},
		"a\x00c": {FileA: "a.go", FileB: "c.go", Weight: 1.5},
		"b\x00c": {FileA: "b.go", FileB: "c.go", Weight: 0.5},
		"d\x00e": {FileA: "d.go", FileB: "e.go", Weight: 3.0}, // disconnected
	}

	neighbors := CoChangeNeighbors(edges, []string{"a.go"}, 5)
	if len(neighbors) != 2 {
		t.Fatalf("expected 2 neighbors, got %d", len(neighbors))
	}
	if neighbors["b.go"] != 2.0 {
		t.Errorf("b.go weight = %f, want 2.0", neighbors["b.go"])
	}
	if neighbors["c.go"] != 1.5 {
		t.Errorf("c.go weight = %f, want 1.5", neighbors["c.go"])
	}

	// Test topN limit
	neighbors = CoChangeNeighbors(edges, []string{"a.go"}, 1)
	if len(neighbors) != 1 {
		t.Fatalf("expected 1 neighbor with topN=1, got %d", len(neighbors))
	}
	if _, ok := neighbors["b.go"]; !ok {
		t.Error("expected b.go as top neighbor")
	}
}

func TestBuildGitCoChangeGraphAdaptive(t *testing.T) {
	dir := setupTestGitRepo(t)

	// Use a narrow depth that might miss commits, with minEdges to trigger expansion
	edges, err := BuildGitCoChangeGraph(dir, 1, 100)
	if err != nil {
		t.Fatalf("BuildGitCoChangeGraph: %v", err)
	}

	// Since we asked for 100 edges but only have 4, the adaptive path should
	// have expanded to full history
	if len(edges) != 4 {
		t.Errorf("expected 4 edges after adaptive expansion, got %d", len(edges))
	}
}

func TestCochangeKey(t *testing.T) {
	k1 := cochangeKey("a.go", "b.go")
	k2 := cochangeKey("b.go", "a.go")
	if k1 != k2 {
		t.Errorf("cochangeKey not canonical: %q != %q", k1, k2)
	}
}
