package dualmem

// Tests for the v2 pinned context block (assemble_v2.go).
//
// Coverage goals (from the task acceptance list):
//   - token cap enforced (hard cap respected even when items overflow)
//   - ordering: handoff > preferences > changed-file facts > codemap status
//   - changed-file fact selection (only facts whose Files intersect the diff)
//   - empty store degrades to a codemap line only (no scan)
//
// The codemap no-rescan contract is additionally re-asserted here for the v2
// path, mirroring the v1 regression in codemap_swr_test.go.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// newV2Repo creates a git repo with one committed file and returns its path.
func newV2Repo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"),
		[]byte("package main\n\nfunc main() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGitV2(t, dir, "init")
	runGitV2(t, dir, "add", ".")
	runGitV2(t, dir, "commit", "-m", "init")
	return dir
}

func runGitV2(t *testing.T, dir string, args ...string) {
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

func newV2Engine(t *testing.T, rootDir string) *Engine {
	t.Helper()
	engine, err := New(Config{
		SQLitePath:        filepath.Join(t.TempDir(), "v2.db"),
		EmbeddingProvider: &mockEmbedder{dim: 64},
		RootDir:           rootDir,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { engine.Close() })
	return engine
}

// commitChange writes/updates a file and commits it, advancing HEAD.
func commitChange(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	runGitV2(t, dir, "add", ".")
	runGitV2(t, dir, "commit", "-m", "update "+name)
}

// TestPinnedBlock_EmptyStoreDegradesToCodemapLine verifies that with no facts,
// no checkpoints, and no stored codemap, the v2 block still returns successfully
// and triggers no filesystem walk (the no-scan invariant).
func TestPinnedBlock_EmptyStoreDegradesToCodemapLine(t *testing.T) {
	dir := newV2Repo(t)
	engine := newV2Engine(t, dir)
	ctx := context.Background()

	var scanEvents int
	engine.OnScanProgress = func(p ScanProgress) { scanEvents++ }

	block, err := engine.Assemble(ctx, "ns-empty", "session start", 0, AssembleOptions{})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if block == nil {
		t.Fatal("nil block")
	}
	// No codemap stored yet → no codemap line; block is empty but valid.
	if block.Text != "" {
		t.Errorf("empty store should yield empty text, got %q", block.Text)
	}
	if scanEvents != 0 {
		t.Errorf("v2 pinned block scanned the fs %d times; must never scan", scanEvents)
	}
	if len(block.ServedFactIDs) != 0 {
		t.Errorf("empty store should serve no facts, got %v", block.ServedFactIDs)
	}
}

// TestPinnedBlock_CodemapStatusNoRescan asserts the v2 path reads the STORED
// codemap without rescanning, even when HEAD has advanced past it.
func TestPinnedBlock_CodemapStatusNoRescan(t *testing.T) {
	dir := newV2Repo(t)
	engine := newV2Engine(t, dir)
	ctx := context.Background()

	// Seed a stored codemap, then advance HEAD so it goes stale.
	if _, _, err := engine.RefreshCodeMap(ctx, "ns-cm"); err != nil {
		t.Fatalf("RefreshCodeMap: %v", err)
	}
	commitChange(t, dir, "extra.go", "package main\nfunc extra() {}\n")

	var scanEvents int
	engine.OnScanProgress = func(p ScanProgress) { scanEvents++ }

	block, err := engine.Assemble(ctx, "ns-cm", "session start", 0, AssembleOptions{})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if !strings.Contains(block.Text, "[Codebase Map") {
		t.Errorf("expected codemap status line, got %q", block.Text)
	}
	if !strings.Contains(block.Text, "commits behind") {
		t.Errorf("stale codemap should report commits behind, got %q", block.Text)
	}
	if scanEvents != 0 {
		t.Errorf("v2 pinned block scanned %d times; must read stored map only", scanEvents)
	}
}

// TestPinnedBlock_Ordering verifies the four sections appear in the fixed
// order: handoff, preferences, changed-file facts, codemap status.
func TestPinnedBlock_Ordering(t *testing.T) {
	dir := newV2Repo(t)
	engine := newV2Engine(t, dir)
	ctx := context.Background()

	// Seed a stored codemap so the status line is present.
	if _, _, err := engine.RefreshCodeMap(ctx, "ns-order"); err != nil {
		t.Fatalf("RefreshCodeMap: %v", err)
	}

	// Preference (user-global).
	if _, err := engine.AddFact(ctx, Fact{
		Namespace: "",
		Kind:      FactKindPreference,
		Source:    FactSourceVerified,
		Text:      "Prefer table-driven tests.",
	}); err != nil {
		t.Fatalf("AddFact pref: %v", err)
	}

	// Changed-file fact (repo-scoped to the namespace).
	if _, err := engine.AddFact(ctx, Fact{
		Namespace: "ns-order",
		Kind:      FactKindDecision,
		Source:    FactSourceVerified,
		Text:      "Use git diff for incremental refresh.",
		Files:     []string{"main.go"},
	}); err != nil {
		t.Fatalf("AddFact decision: %v", err)
	}

	// A checkpoint (handoff).
	cp := &Checkpoint{Task: "v2 ordering", Status: "in_progress"}
	if err := engine.SaveCheckpoint(ctx, "ns-order", cp); err != nil {
		t.Fatalf("SaveCheckpoint: %v", err)
	}

	// Record a session marker so the next diff has a baseline, then advance
	// HEAD so main.go counts as changed since the marker.
	engine.recordSessionMarker("ns-order")
	commitChange(t, dir, "main.go", "package main\n\nfunc main() { _ = extra }\n")

	block, err := engine.Assemble(ctx, "ns-order", "session start", 0, AssembleOptions{})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	idxHandoff := strings.Index(block.Text, "[Checkpoint: v2 ordering]")
	idxPref := strings.Index(block.Text, "Prefer table-driven tests.")
	idxChanged := strings.Index(block.Text, "Use git diff for incremental refresh.")
	idxCM := strings.Index(block.Text, "[Codebase Map")

	if idxHandoff < 0 || idxPref < 0 || idxChanged < 0 || idxCM < 0 {
		t.Fatalf("missing sections in block:\n%s\nh=%d p=%d c=%d cm=%d",
			block.Text, idxHandoff, idxPref, idxChanged, idxCM)
	}
	if !(idxHandoff < idxPref && idxPref < idxChanged && idxChanged < idxCM) {
		t.Errorf("ordering wrong: handoff=%d pref=%d changed=%d codemap=%d",
			idxHandoff, idxPref, idxChanged, idxCM)
	}

	// Both facts should be recorded as served.
	if len(block.ServedFactIDs) != 2 {
		t.Errorf("expected 2 served fact IDs (preference + changed-file), got %d: %v", len(block.ServedFactIDs), block.ServedFactIDs)
	}
}

// TestPinnedBlock_TokenCapEnforced checks the hard cap holds. With a tiny
// budget the block never exceeds it once at least one item has landed.
func TestPinnedBlock_TokenCapEnforced(t *testing.T) {
	dir := newV2Repo(t)
	engine := newV2Engine(t, dir)
	ctx := context.Background()

	// Seed codemap so the status line competes for the tiny budget.
	if _, _, err := engine.RefreshCodeMap(ctx, "ns-cap"); err != nil {
		t.Fatalf("RefreshCodeMap: %v", err)
	}

	// Many preferences to overflow a small budget.
	for i := 0; i < 20; i++ {
		if _, err := engine.AddFact(ctx, Fact{
			Namespace: "",
			Kind:      FactKindPreference,
			Source:    FactSourceVerified,
			Text:      "Preference number " + string(rune('A'+i)) + " with some extra words to add token weight.",
		}); err != nil {
			t.Fatalf("AddFact %d: %v", i, err)
		}
	}

	const cap = 60 // deliberately tiny
	block, err := engine.Assemble(ctx, "ns-cap", "session start", 0, AssembleOptions{PinnedBudget: cap})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if block.TokenCount > cap {
		t.Errorf("token count %d exceeds hard cap %d", block.TokenCount, cap)
	}
}

// TestPinnedBlock_ChangedFileFactSelection verifies only facts whose Files
// intersect the diff are emitted, and facts touching untouched files are not.
func TestPinnedBlock_ChangedFileFactSelection(t *testing.T) {
	dir := newV2Repo(t)
	engine := newV2Engine(t, dir)
	ctx := context.Background()

	// Seed codemap to create a session marker baseline via the stored map's
	// commit. We then record a marker at that commit and advance.
	if _, _, err := engine.RefreshCodeMap(ctx, "ns-sel"); err != nil {
		t.Fatalf("RefreshCodeMap: %v", err)
	}
	engine.recordSessionMarker("ns-sel") // marker at current HEAD

	// Fact touching a file we WILL change.
	if _, err := engine.AddFact(ctx, Fact{
		Namespace: "ns-sel",
		Kind:      FactKindGotcha,
		Source:    FactSourceVerified,
		Text:      "Never call Close twice on the store.",
		Files:     []string{"main.go"},
	}); err != nil {
		t.Fatalf("AddFact hit: %v", err)
	}
	// Fact touching a file we will NOT change.
	if _, err := engine.AddFact(ctx, Fact{
		Namespace: "ns-sel",
		Kind:      FactKindDecision,
		Source:    FactSourceVerified,
		Text:      "Docs live under docs/architecture.md.",
		Files:     []string{"docs/architecture.md"},
	}); err != nil {
		t.Fatalf("AddFact miss: %v", err)
	}

	// Change only main.go (with genuinely new content so it commits).
	commitChange(t, dir, "main.go", "package main\n\nfunc main() { x := 1; _ = x }\n")

	block, err := engine.Assemble(ctx, "ns-sel", "session start", 0, AssembleOptions{PinnedBudget: 400})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if !strings.Contains(block.Text, "Never call Close twice") {
		t.Errorf("changed-file fact (main.go) should be present:\n%s", block.Text)
	}
	if strings.Contains(block.Text, "docs/architecture.md") {
		t.Errorf("unchanged-file fact should NOT be present:\n%s", block.Text)
	}
	// Exactly one repo-scoped fact served (the preference namespace is "" so
	// the docs fact is repo-scoped too and must be excluded).
}

// TestPinnedBlock_LegacyEscapeHatch confirms AssembleOptions{Legacy:true}
// delegates to the v1 path (which renders detail memories and full codemap).
func TestPinnedBlock_LegacyEscapeHatch(t *testing.T) {
	dir := newV2Repo(t)
	engine := newV2Engine(t, dir)
	ctx := context.Background()

	if _, _, err := engine.RefreshCodeMap(ctx, "ns-leg"); err != nil {
		t.Fatalf("RefreshCodeMap: %v", err)
	}
	// Add a detail memory (v1 primitive). v2 would NOT surface it; legacy must.
	if err := engine.AddWithOptions(ctx, MemoryInput{
		UserMessage: "Legacy detail memory surfaced only by v1.",
		SectorHint:  "decision",
		Salience:    0.9,
	}, "ns-leg"); err != nil {
		t.Fatalf("AddWithOptions: %v", err)
	}

	// v2 default: detail memory absent.
	v2, err := engine.Assemble(ctx, "ns-leg", "session start", 0, AssembleOptions{})
	if err != nil {
		t.Fatalf("Assemble v2: %v", err)
	}
	if strings.Contains(v2.Text, "Legacy detail memory surfaced only by v1") {
		t.Errorf("v2 pinned block must not surface detail memories:\n%s", v2.Text)
	}

	// Legacy: detail memory present.
	leg, err := engine.Assemble(ctx, "ns-leg", "session start", 3000, AssembleOptions{Legacy: true})
	if err != nil {
		t.Fatalf("Assemble legacy: %v", err)
	}
	if !strings.Contains(leg.Text, "Legacy detail memory surfaced only by v1") {
		t.Errorf("legacy path must surface detail memories:\n%s", leg.Text)
	}
}

// TestPinnedBlock_PreferencesUserGlobal verifies preferences (namespace "") are
// surfaced regardless of the queried namespace.
func TestPinnedBlock_PreferencesUserGlobal(t *testing.T) {
	dir := newV2Repo(t)
	engine := newV2Engine(t, dir)
	ctx := context.Background()

	if _, _, err := engine.RefreshCodeMap(ctx, "ns-a"); err != nil {
		t.Fatalf("RefreshCodeMap: %v", err)
	}
	if _, err := engine.AddFact(ctx, Fact{
		Namespace: "",
		Kind:      FactKindPreference,
		Source:    FactSourceVerified,
		Text:      "Always write concise commit messages.",
	}); err != nil {
		t.Fatalf("AddFact: %v", err)
	}

	// Query a DIFFERENT namespace; the user-global pref should still appear.
	block, err := engine.Assemble(ctx, "ns-b", "session start", 0, AssembleOptions{})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if !strings.Contains(block.Text, "Always write concise commit messages") {
		t.Errorf("user-global preference should appear in any namespace:\n%s", block.Text)
	}
}

