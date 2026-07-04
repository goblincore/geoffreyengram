package dualmem

// Regression tests for non-blocking context assembly (stale-while-revalidate).
//
// Invariant under test: once a code map has been stored for a namespace,
// GetCodeMap / AssembleContext must NEVER rescan the codebase inline — a
// stale map is served immediately and marked Stale/CommitsBehind. Only a
// true first run (no stored map) may scan inline. Rescans happen via
// RefreshCodeMap (CLI `map`, background workers).

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func runGitSWR(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@test.com",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// newSWRRepo creates a git repo containing one Go file with a single commit.
func newSWRRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"),
		[]byte("package main\n\n// Entry point.\nfunc main() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGitSWR(t, dir, "init")
	runGitSWR(t, dir, "add", ".")
	runGitSWR(t, dir, "commit", "-m", "init")
	return dir
}

func newSWREngine(t *testing.T, rootDir string) *Engine {
	t.Helper()
	engine, err := New(Config{
		SQLitePath:        filepath.Join(t.TempDir(), "test.db"),
		EmbeddingProvider: &mockEmbedder{dim: 768},
		Classifier:        &mockClassifier{},
		EntityExtractor:   &mockExtractor{},
		RootDir:           rootDir,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { engine.Close() })
	return engine
}

// advanceHEAD commits a new file so HEAD moves past the stored map's commit.
func advanceHEAD(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "extra.go"),
		[]byte("package main\n\nfunc extra() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGitSWR(t, dir, "add", ".")
	runGitSWR(t, dir, "commit", "-m", "advance")
}

func TestGetCodeMap_FirstRunScansInline(t *testing.T) {
	dir := newSWRRepo(t)
	engine := newSWREngine(t, dir)

	cm, idx := engine.GetCodeMap(context.Background(), "swr-ns")
	if cm == nil || idx == nil {
		t.Fatal("first run should generate a map inline")
	}
	if cm.Stale {
		t.Error("freshly generated map must not be marked stale")
	}
	_, commit := GetGitState(dir)
	if cm.GitCommit != commit {
		t.Errorf("GitCommit = %q, want current HEAD %q", cm.GitCommit, commit)
	}

	stored, err := engine.store.GetCodeMap("swr-ns")
	if err != nil || stored == nil {
		t.Fatalf("first run must persist the map: stored=%v err=%v", stored, err)
	}
}

func TestGetCodeMap_StaleServedWithoutRescan(t *testing.T) {
	dir := newSWRRepo(t)
	engine := newSWREngine(t, dir)
	ctx := context.Background()

	// Seed stored map at commit 1.
	if _, _, err := engine.RefreshCodeMap(ctx, "swr-ns"); err != nil {
		t.Fatalf("RefreshCodeMap: %v", err)
	}
	_, commit1 := GetGitState(dir)

	advanceHEAD(t, dir)
	_, commit2 := GetGitState(dir)
	if commit1 == commit2 {
		t.Fatal("expected HEAD to advance")
	}

	var scanEvents int
	engine.OnScanProgress = func(p ScanProgress) { scanEvents++ }

	cm, idx := engine.GetCodeMap(ctx, "swr-ns")
	if cm == nil || idx == nil {
		t.Fatal("stored map must be served even when stale")
	}
	if !cm.Stale {
		t.Error("map behind HEAD must be marked Stale")
	}
	if cm.CommitsBehind < 1 {
		t.Errorf("CommitsBehind = %d, want >= 1", cm.CommitsBehind)
	}
	if cm.GitCommit != commit1 {
		t.Errorf("served map GitCommit = %q, want stored commit %q (no inline rescan)", cm.GitCommit, commit1)
	}
	if scanEvents != 0 {
		t.Errorf("GetCodeMap emitted %d scan progress events; must never scan when a map is stored", scanEvents)
	}

	// The store must be untouched: a rescan would have re-stamped commit2.
	stored, _ := engine.store.GetCodeMap("swr-ns")
	if stored == nil || stored.GitCommit != commit1 {
		t.Errorf("stored map was modified by GetCodeMap: got commit %q, want %q", stored.GitCommit, commit1)
	}
}

func TestGetCodeMap_FreshMapNotStale(t *testing.T) {
	dir := newSWRRepo(t)
	engine := newSWREngine(t, dir)
	ctx := context.Background()

	if _, _, err := engine.RefreshCodeMap(ctx, "swr-ns"); err != nil {
		t.Fatalf("RefreshCodeMap: %v", err)
	}

	cm, _ := engine.GetCodeMap(ctx, "swr-ns")
	if cm == nil {
		t.Fatal("expected stored map")
	}
	if cm.Stale || cm.CommitsBehind != 0 {
		t.Errorf("map at HEAD marked stale (Stale=%v CommitsBehind=%d)", cm.Stale, cm.CommitsBehind)
	}
}

func TestAssembleContext_ServesStaleMapWithoutRescan(t *testing.T) {
	dir := newSWRRepo(t)
	engine := newSWREngine(t, dir)
	ctx := context.Background()

	// AssembleContext uses the userID as the codemap namespace.
	if _, _, err := engine.RefreshCodeMap(ctx, "testuser"); err != nil {
		t.Fatalf("RefreshCodeMap: %v", err)
	}
	_, commit1 := GetGitState(dir)

	advanceHEAD(t, dir)

	var scanEvents int
	engine.OnScanProgress = func(p ScanProgress) { scanEvents++ }

	block, err := engine.AssembleContext(ctx, "testuser", "main entry point", 3000)
	if err != nil {
		t.Fatalf("AssembleContext: %v", err)
	}
	// Stale maps render with an annotated header, e.g.
	// "[Codebase Map — 1 commits behind, run `dualmem index` to refresh]".
	if !strings.Contains(block.Text, "[Codebase Map") {
		t.Error("stale map should still be rendered into assembled context")
	}
	if !strings.Contains(block.Text, "commits behind") {
		t.Error("stale map header should carry the commits-behind annotation")
	}
	if scanEvents != 0 {
		t.Errorf("AssembleContext emitted %d scan progress events; assembly must never block on a rescan", scanEvents)
	}

	stored, _ := engine.store.GetCodeMap("testuser")
	if stored == nil || stored.GitCommit != commit1 {
		t.Error("AssembleContext must not re-stamp or regenerate the stored map")
	}
}

func TestRefreshCodeMap_EmptyDiffRestampsWithoutScan(t *testing.T) {
	dir := newSWRRepo(t)
	engine := newSWREngine(t, dir)
	ctx := context.Background()

	cm1, _, err := engine.RefreshCodeMap(ctx, "swr-ns")
	if err != nil {
		t.Fatalf("RefreshCodeMap: %v", err)
	}

	// Move HEAD without changing any files.
	runGitSWR(t, dir, "commit", "--allow-empty", "-m", "empty")
	_, commit2 := GetGitState(dir)

	var scanEvents int
	engine.OnScanProgress = func(p ScanProgress) { scanEvents++ }

	cm2, idx, err := engine.RefreshCodeMap(ctx, "swr-ns")
	if err != nil {
		t.Fatalf("RefreshCodeMap: %v", err)
	}
	if idx == nil {
		t.Fatal("expected code index")
	}
	if cm2.GitCommit != commit2 {
		t.Errorf("re-stamp GitCommit = %q, want %q", cm2.GitCommit, commit2)
	}
	if len(cm2.Zoom2) != len(cm1.Zoom2) {
		t.Errorf("re-stamp changed module count: %d -> %d", len(cm1.Zoom2), len(cm2.Zoom2))
	}
	if scanEvents != 0 {
		t.Errorf("empty diff triggered %d scan progress events; expected commit re-stamp only", scanEvents)
	}
}
