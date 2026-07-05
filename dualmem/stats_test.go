package dualmem

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// newStatsTestEngine builds an Engine backed by a temp SQLite DB with the
// shared mock embedder, sized for the v2 instrumentation tests.
func newStatsTestEngine(t *testing.T) *Engine {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "stats.db")
	engine, err := New(Config{
		SQLitePath:        dbPath,
		EmbeddingProvider: &mockEmbedder{dim: 64},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { engine.Close() })
	return engine
}

// TestServeTouchHitCreditsOnce is the core instrumentation loop: a fact is
// served, the session touches one of its cited files, and distill credits
// exactly one hit — and never a second one for the same (session, fact).
func TestServeTouchHitCreditsOnce(t *testing.T) {
	engine := newStatsTestEngine(t)
	ctx := context.Background()

	f, err := engine.AddFact(ctx, Fact{
		Namespace: "repo",
		Kind:      FactKindDecision,
		Source:    FactSourceVerified,
		Text:      "Route auth through middleware.",
		Files:     []string{"auth/middleware.go"},
	})
	if err != nil {
		t.Fatalf("AddFact: %v", err)
	}

	const session = "sess-1"
	engine.RecordServed(session, []string{f.ID}, FactSurfacePinned)

	// Before any touch: zero hits.
	if got := mustFactHits(t, engine, f.ID); got != 0 {
		t.Fatalf("hits before touch = %d, want 0", got)
	}

	// Touch the cited file.
	credits, err := engine.RecordFileTouches(session, []string{"auth/middleware.go"})
	if err != nil {
		t.Fatalf("RecordFileTouches: %v", err)
	}
	if credits != 1 {
		t.Fatalf("first touch credits = %d, want 1", credits)
	}
	if got := mustFactHits(t, engine, f.ID); got != 1 {
		t.Fatalf("hits after touch = %d, want 1", got)
	}

	// Second call must be idempotent — no new credit.
	credits, err = engine.RecordFileTouches(session, []string{"auth/middleware.go"})
	if err != nil {
		t.Fatalf("RecordFileTouches (2nd): %v", err)
	}
	if credits != 0 {
		t.Fatalf("second touch credits = %d, want 0 (idempotent)", credits)
	}
	if got := mustFactHits(t, engine, f.ID); got != 1 {
		t.Fatalf("hits after second touch = %d, want 1", got)
	}

	// Serving the same fact again in the same session is also a no-op.
	engine.RecordServed(session, []string{f.ID}, FactSurfaceRecall)
	credits, err = engine.RecordFileTouches(session, []string{"auth/middleware.go"})
	if err != nil {
		t.Fatalf("RecordFileTouches (3rd): %v", err)
	}
	if credits != 0 {
		t.Fatalf("post-reserve touch credits = %d, want 0", credits)
	}
}

// TestServeTouchNoIntersectionIsNoOp confirms a fact whose files the session
// never touched earns no hit, even after multiple serves.
func TestServeTouchNoIntersectionIsNoOp(t *testing.T) {
	engine := newStatsTestEngine(t)
	ctx := context.Background()

	f, err := engine.AddFact(ctx, Fact{
		Namespace: "repo",
		Kind:      FactKindGotcha,
		Source:    FactSourceVerified,
		Text:      "Never call X before Y is initialized.",
		Files:     []string{"pkg/x/y.go"},
	})
	if err != nil {
		t.Fatalf("AddFact: %v", err)
	}

	const session = "sess-2"
	engine.RecordServed(session, []string{f.ID}, FactSurfaceFileContext)
	engine.RecordServed(session, []string{f.ID}, FactSurfaceRecall)

	credits, err := engine.RecordFileTouches(session, []string{"completely/unrelated.go"})
	if err != nil {
		t.Fatalf("RecordFileTouches: %v", err)
	}
	if credits != 0 {
		t.Fatalf("non-intersecting credits = %d, want 0", credits)
	}
	if got := mustFactHits(t, engine, f.ID); got != 0 {
		t.Fatalf("hits after non-intersecting touch = %d, want 0", got)
	}
}

// TestServeTouchPathNormalization confirms that basename/slash-variant paths
// still intersect (the correlator normalizes both sides).
func TestServeTouchPathNormalization(t *testing.T) {
	engine := newStatsTestEngine(t)
	ctx := context.Background()

	f, err := engine.AddFact(ctx, Fact{
		Namespace: "repo",
		Kind:      FactKindDeadEnd,
		Source:    FactSourceVerified,
		Text:      "Tried inlining the walker; blew the token budget.",
		Files:     []string{"dualmem/codemap.go"},
	})
	if err != nil {
		t.Fatalf("AddFact: %v", err)
	}

	const session = "sess-3"
	engine.RecordServed(session, []string{f.ID}, FactSurfacePinned)

	// Touched path with a leading "./" and backslashes must still match.
	credits, err := engine.RecordFileTouches(session, []string{"./dualmem/codemap.go"})
	if err != nil {
		t.Fatalf("RecordFileTouches: %v", err)
	}
	if credits != 1 {
		t.Fatalf("normalized-path credits = %d, want 1", credits)
	}
}

// TestFactStatsScorecard seeds a store with a mix of facts (hit, served-but-
// not-hit, never-served, dead) and asserts the scorecard aggregates correctly.
func TestFactStatsScorecard(t *testing.T) {
	engine := newStatsTestEngine(t)
	ctx := context.Background()
	ns := "repo-stats"

	// Fact A — served once, touched → hit.
	a, err := engine.AddFact(ctx, Fact{
		Namespace: ns, Kind: FactKindDecision, Source: FactSourceVerified,
		Text: "Use SQLite for the CLI.", Files: []string{"store_sqlite.go"},
	})
	if err != nil {
		t.Fatalf("AddFact A: %v", err)
	}
	// Fact B — served many times, never touched → dead.
	b, err := engine.AddFact(ctx, Fact{
		Namespace: ns, Kind: FactKindGotcha, Source: FactSourceVerified,
		Text: "Don't reorder migrations.", Files: []string{"migrations/"},
	})
	if err != nil {
		t.Fatalf("AddFact B: %v", err)
	}
	// Fact C — never served.
	_, err = engine.AddFact(ctx, Fact{
		Namespace: ns, Kind: FactKindReference, Source: FactSourceDoc,
		Text: "See docs/architecture.md for module map.",
	})
	if err != nil {
		t.Fatalf("AddFact C: %v", err)
	}

	// Serve A once and credit a hit.
	engine.RecordServed("sess-a", []string{a.ID}, FactSurfacePinned)
	if _, err := engine.RecordFileTouches("sess-a", []string{"store_sqlite.go"}); err != nil {
		t.Fatalf("RecordFileTouches A: %v", err)
	}

	// Serve B six times across six sessions, never touch its file.
	for i := 0; i < 6; i++ {
		sess := "sess-b-" + string(rune('0'+i))
		engine.RecordServed(sess, []string{b.ID}, FactSurfaceRecall)
	}

	scorecard, err := engine.FactStats(FactStatsOpts{Namespaces: []string{ns}})
	if err != nil {
		t.Fatalf("FactStats: %v", err)
	}

	// Overall roll-up: 3 facts, 2 served (A, B), 1 hit (A).
	if scorecard.Overall.FactsCount != 3 {
		t.Errorf("overall facts = %d, want 3", scorecard.Overall.FactsCount)
	}
	if scorecard.Overall.ServedCount != 2 {
		t.Errorf("overall served = %d, want 2", scorecard.Overall.ServedCount)
	}
	if scorecard.Overall.HitCount != 1 {
		t.Errorf("overall hits = %d, want 1", scorecard.Overall.HitCount)
	}

	// B should appear in dead facts (served 6 >= default 5, zero hits).
	var deadIDs []string
	for _, d := range scorecard.Dead {
		deadIDs = append(deadIDs, d.ID)
	}
	if !containsString(deadIDs, b.ID) {
		t.Errorf("dead facts = %v, want to include B (%s)", deadIDs, b.ID)
	}
	for _, d := range scorecard.Dead {
		if d.ID == b.ID && d.Serves != 6 {
			t.Errorf("dead B serves = %d, want 6", d.Serves)
		}
	}

	// A must NOT be in dead facts (it has a hit).
	if containsString(deadIDs, a.ID) {
		t.Errorf("A (%s) should not be dead — it has a hit", a.ID)
	}
}

// TestFactStatsStalenessNotAGitRepo confirms staleness is best-effort and never
// flags anything when RootDir isn't a git repo (the test temp dir isn't one).
func TestFactStatsStalenessNotAGitRepo(t *testing.T) {
	engine := newStatsTestEngine(t)
	ctx := context.Background()

	// Add a fact with a plausible (but fictional) git_commit. Without a real
	// git repo at RootDir the scorer can't compute distance, so nothing should
	// be flagged stale.
	_, err := engine.AddFact(ctx, Fact{
		Namespace: "repo", Kind: FactKindReference, Source: FactSourceDoc,
		Text: "Ancient reference.", GitCommit: "0000000000000000000000000000000000000000",
	})
	if err != nil {
		t.Fatalf("AddFact: %v", err)
	}

	scorecard, err := engine.FactStats(FactStatsOpts{Namespaces: []string{"repo"}})
	if err != nil {
		t.Fatalf("FactStats: %v", err)
	}
	if len(scorecard.Stale) != 0 {
		t.Errorf("stale candidates = %d, want 0 (not a git repo)", len(scorecard.Stale))
	}
}

// TestRecordServedIsIdempotentPerSession confirms repeated RecordServed calls
// in one session don't create duplicate rows (PK = session_id, fact_id).
func TestRecordServedIsIdempotentPerSession(t *testing.T) {
	engine := newStatsTestEngine(t)
	ctx := context.Background()

	f, err := engine.AddFact(ctx, Fact{
		Namespace: "repo", Kind: FactKindPreference, Source: FactSourceVerified,
		Text: "Use tabs for Go.",
	})
	if err != nil {
		t.Fatalf("AddFact: %v", err)
	}

	const session = "sess-unique"
	engine.RecordServed(session, []string{f.ID}, FactSurfacePinned)
	engine.RecordServed(session, []string{f.ID}, FactSurfaceRecall)
	engine.RecordServed(session, []string{f.ID}, FactSurfacePrecedent)

	served, err := engine.GetStore().GetServedFactsForSession(session)
	if err != nil {
		t.Fatalf("GetServedFactsForSession: %v", err)
	}
	if len(served) != 1 {
		t.Fatalf("served rows = %d, want 1 (idempotent PK)", len(served))
	}
	// Surface stays the first one written (INSERT OR IGNORE keeps the original).
	if served[0].Surface != FactSurfacePinned {
		t.Errorf("surface = %q, want %q (first write wins)", served[0].Surface, FactSurfacePinned)
	}
}

// mustFactHits reloads a fact and asserts on its hit count.
func mustFactHits(t *testing.T, engine *Engine, id string) int {
	t.Helper()
	f, err := engine.GetFact(id)
	if err != nil {
		t.Fatalf("GetFact %s: %v", id, err)
	}
	return f.Hits
}

// containsString reports whether the slice contains s.
func containsString(slice []string, s string) bool {
	for _, x := range slice {
		if x == s {
			return true
		}
	}
	return false
}

// TestCollectTouchedFilesTranscriptFallback confirms the transcript-scan
// fallback extracts repo-relative path tokens (used when the auto-capture hook
// log is absent).
func TestCollectTouchedFilesTranscriptFallback(t *testing.T) {
	transcript := "User: please update dualmem/facts.go and the README.md\n" +
		"Assistant: edited cmd/dualmem/main.go and ran go vet"

	got := collectTouchedFiles("claude:repo", "", transcript)

	want := map[string]bool{
		"dualmem/facts.go":    false,
		"README.md":           false, // no slash — filtered out
		"cmd/dualmem/main.go": false,
	}
	for _, p := range got {
		if p == "dualmem/facts.go" {
			want["dualmem/facts.go"] = true
		}
		if p == "cmd/dualmem/main.go" {
			want["cmd/dualmem/main.go"] = true
		}
		if p == "README.md" {
			t.Errorf("README.md should be filtered (no slash), got %q", p)
		}
	}
	if !want["dualmem/facts.go"] {
		t.Errorf("expected dualmem/facts.go in touched set, got %v", got)
	}
	if !want["cmd/dualmem/main.go"] {
		t.Errorf("expected cmd/dualmem/main.go in touched set, got %v", got)
	}

	// Sanity: every returned token has a slash.
	for _, p := range got {
		if !strings.Contains(p, "/") {
			t.Errorf("touched token %q has no slash", p)
		}
	}
}
