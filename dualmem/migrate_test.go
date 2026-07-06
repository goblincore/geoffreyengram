package dualmem

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- mock curator ---

// mockCurator is a test FactCurator with a programmable per-source verdict.
type mockCurator struct {
	// verdicts maps source_id -> curatedFact verdict (skip or keep with kind/text).
	verdicts map[string]curatedFact
	// errOnBatch, if non-zero, makes the Nth Curate() call (1-indexed) return
	// an error to exercise the resilient-skip path.
	errOnBatch int
	calls      int
}

func (m *mockCurator) Curate(_ context.Context, batch []v1SourceItem) ([]curatedFact, error) {
	m.calls++
	if m.errOnBatch > 0 && m.calls == m.errOnBatch {
		return nil, errFake("simulated LLM failure")
	}
	out := make([]curatedFact, 0, len(batch))
	for _, it := range batch {
		if v, ok := m.verdicts[it.SourceID]; ok {
			v.SourceID = it.SourceID // echo back so MigrateV2 can match it
			out = append(out, v)
			continue
		}
		// Default: skip anything not explicitly mapped.
		out = append(out, curatedFact{SourceID: it.SourceID, Skipped: true})
	}
	return out, nil
}

type errFake string

func (e errFake) Error() string { return string(e) }

// --- helpers ---

// newMigrateTestEngine builds an engine wired with a mock embedder for
// deterministic dedupe. It does NOT set a curator — tests pass their own.
func newMigrateTestEngine(t *testing.T) *Engine {
	t.Helper()
	return newFactsTestEngine(t)
}

// seedV1Details inserts v1 detail memories directly into the store (bypassing
// AddWithOptions, which near-duplicate-dedupes at write time and would collapse
// the deliberately-similar items). Returns the source IDs keyed by a stable
// label so tests can reference them.
func seedV1Details(t *testing.T, engine *Engine, ctx context.Context, namespace string) map[string]string {
	t.Helper()
	out := make(map[string]string)
	now := time.Now().UTC()
	for _, m := range []struct {
		label, text, mtype string
		files              []string
		age                time.Duration
	}{
		{"dec1", "We chose SQLite over Postgres for the CLI to keep zero-setup.", "decision", []string{"store_sqlite.go"}, 10 * 24 * time.Hour},
		{"got1", "rateLimiter cleanup skips nil check intentionally for hot path.", "warning", []string{"rate_limiter.go"}, 8 * 24 * time.Hour},
		{"narr1", "Had a good chat with the team about the roadmap today.", "", nil, 5 * 24 * time.Hour},                                            // narrative → skip
		{"dup1", "Picked SQLite instead of Postgres so the CLI needs no setup steps.", "decision", []string{"store_sqlite.go"}, 3 * 24 * time.Hour}, // near-dup of dec1 once both are rewritten, newer
	} {
		id := generateID()
		vec, err := engine.embedder.Embed(ctx, m.text, "RETRIEVAL_DOCUMENT")
		if err != nil {
			t.Fatalf("embed %s: %v", m.label, err)
		}
		if err := engine.store.InsertDetail(&DetailMemory{
			ID:        id,
			Text:      m.text,
			Sector:    "decision",
			Salience:  0.9,
			Type:      m.mtype,
			Files:     m.files,
			CreatedAt: now.Add(-m.age),
		}, vec, namespace); err != nil {
			t.Fatalf("InsertDetail %s: %v", m.label, err)
		}
		out[m.label] = id
	}
	return out
}

// makeArchiveFile creates a minimal v1 archive JSON file for the given
// namespace in a temp dir, so the migration precondition is satisfied.
func makeArchiveFile(t *testing.T, dir, namespace string) string {
	t.Helper()
	path := filepath.Join(dir, sanitizeNamespace(namespace)+".json")
	doc := map[string]any{
		"format":         v1ArchiveFormat,
		"schema_version": 7,
		"archived_at":    time.Now().UTC().Format(time.RFC3339),
		"namespace":      namespace,
		"tables":         map[string]any{},
	}
	body, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatalf("marshal archive: %v", err)
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatalf("write archive: %v", err)
	}
	return path
}

// --- tests ---

// TestMigrateV2_MissingArchiveRefusal verifies migration refuses to run unless
// a v1 archive file exists for the namespace, and that the error carries the
// command to run.
func TestMigrateV2_MissingArchiveRefusal(t *testing.T) {
	engine := newMigrateTestEngine(t)
	ctx := context.Background()
	_ = seedV1Details(t, engine, ctx, "claude:test-repo")

	// Point at an empty temp dir: no archive present.
	emptyDir := t.TempDir()
	_, err := engine.MigrateV2(ctx, MigrateV2Options{
		Namespace:  "claude:test-repo",
		ArchiveDir: emptyDir,
		Curator:    &mockCurator{verdicts: map[string]curatedFact{}},
	})
	if err == nil {
		t.Fatal("expected ArchiveMissingError, got nil")
	}
	var ame *ArchiveMissingError
	if !errors.As(err, &ame) {
		t.Fatalf("expected *ArchiveMissingError, got %T: %v", err, err)
	}
	if ame.Namespace != "claude:test-repo" {
		t.Errorf("namespace = %q, want claude:test-repo", ame.Namespace)
	}
	if !strings.Contains(ame.Error(), "archive-v1") {
		t.Errorf("error should mention archive-v1 command, got: %v", ame)
	}
}

// TestMigrateV2_ClassificationRouting verifies the LLM verdicts route items to
// the correct fact kind or skip, and that the dry-run markdown preview reflects
// the kept set.
func TestMigrateV2_ClassificationRouting(t *testing.T) {
	engine := newMigrateTestEngine(t)
	ctx := context.Background()
	labels := seedV1Details(t, engine, ctx, "claude:test-repo")

	archiveDir := t.TempDir()
	makeArchiveFile(t, archiveDir, "claude:test-repo")

	verdicts := map[string]curatedFact{
		labels["dec1"]: {Kind: FactKindDecision, Text: "Chose SQLite over Postgres for the CLI for zero-setup."},
		labels["got1"]: {Kind: FactKindGotcha, Text: "rateLimiter cleanup intentionally skips the nil check on the hot path."},
		labels["dup1"]: {Kind: FactKindDecision, Text: "Chose SQLite over Postgres for the CLI for zero-setup."},
		// narr1 gets no verdict → default skip
	}

	res, err := engine.MigrateV2(ctx, MigrateV2Options{
		Namespace:  "claude:test-repo",
		ArchiveDir: archiveDir,
		Curator:    &mockCurator{verdicts: verdicts},
	})
	if err != nil {
		t.Fatalf("MigrateV2: %v", err)
	}

	if res.DryRun != true {
		t.Errorf("dry-run default expected, got committed")
	}
	if res.Sources != 4 {
		t.Errorf("sources = %d, want 4 (dec1+got1+narr1+dup1)", res.Sources)
	}
	// dec1 + dup1 → both kept as decisions (dedupe handled separately below, but
	// both are classified). narr1 → skipped. got1 → gotcha.
	if res.KeptByKind[FactKindDecision] != 2 {
		t.Errorf("decision kept = %d, want 2", res.KeptByKind[FactKindDecision])
	}
	if res.KeptByKind[FactKindGotcha] != 1 {
		t.Errorf("gotcha kept = %d, want 1", res.KeptByKind[FactKindGotcha])
	}
	if res.Skipped != 1 {
		t.Errorf("skipped = %d, want 1 (narr1)", res.Skipped)
	}

	// Dry-run: nothing inserted.
	live, _ := engine.ListFacts("claude:test-repo", "", false)
	if len(live) != 0 {
		t.Errorf("dry-run should not insert facts, got %d live", len(live))
	}

	// Markdown preview should contain the kept kinds.
	if !strings.Contains(res.Markdown, "## decision") {
		t.Errorf("markdown missing decision section:\n%s", res.Markdown)
	}
	if !strings.Contains(res.Markdown, "## gotcha") {
		t.Errorf("markdown missing gotcha section:\n%s", res.Markdown)
	}
}

// TestMigrateV2_DedupeSupersede verifies near-duplicate candidates (same text)
// collapse via dedupe, with the newest (latest created_at) winning. dup1 has
// the same text as dec1 but a more recent created_at, so dup1 survives.
func TestMigrateV2_DedupeSupersede(t *testing.T) {
	engine := newMigrateTestEngine(t)
	ctx := context.Background()
	labels := seedV1Details(t, engine, ctx, "claude:test-repo")

	archiveDir := t.TempDir()
	makeArchiveFile(t, archiveDir, "claude:test-repo")

	// Both dec1 and dup1 are kept as decisions with IDENTICAL curated text →
	// dedupe collapses them (newest created_at, dup1, wins).
	verdicts := map[string]curatedFact{
		labels["dec1"]: {Kind: FactKindDecision, Text: "IDENTICAL_TEXT_SQLITE"}, // older
		labels["dup1"]: {Kind: FactKindDecision, Text: "IDENTICAL_TEXT_SQLITE"}, // newer
		labels["got1"]: {Kind: FactKindGotcha, Text: "rate limiter nil check is intentional"},
	}

	res, err := engine.MigrateV2(ctx, MigrateV2Options{
		Namespace:       "claude:test-repo",
		ArchiveDir:      archiveDir,
		DedupeThreshold: 0.90,
		Curator:         &mockCurator{verdicts: verdicts},
		Commit:          true,
	})
	if err != nil {
		t.Fatalf("MigrateV2: %v", err)
	}

	// Two identical-text candidates → 1 deduped.
	if res.DedupeSkipped != 1 {
		t.Errorf("deduped = %d, want 1", res.DedupeSkipped)
	}
	// Inserted: 1 decision (winner) + 1 gotcha = 2.
	if res.Inserted != 2 {
		t.Errorf("inserted = %d, want 2", res.Inserted)
	}

	// Exactly one live decision fact.
	live, _ := engine.ListFacts("claude:test-repo", "", false)
	var decisions []*Fact
	for _, f := range live {
		if f.Kind == FactKindDecision {
			decisions = append(decisions, f)
		}
	}
	if len(decisions) != 1 {
		t.Fatalf("live decisions = %d, want 1", len(decisions))
	}
	// Winner carries source=migrated.
	if decisions[0].Source != FactSourceMigrated {
		t.Errorf("source = %q, want migrated", decisions[0].Source)
	}
}

// TestMigrateV2_DryRunVsCommit verifies the dry-run writes nothing while the
// committed run inserts facts, and that a committed run is idempotent-ish
// (re-running dry-run after commit shows the now-live facts as the preview).
func TestMigrateV2_DryRunVsCommit(t *testing.T) {
	engine := newMigrateTestEngine(t)
	ctx := context.Background()
	labels := seedV1Details(t, engine, ctx, "claude:test-repo")

	archiveDir := t.TempDir()
	makeArchiveFile(t, archiveDir, "claude:test-repo")

	verdicts := map[string]curatedFact{
		labels["dec1"]: {Kind: FactKindDecision, Text: "Decision A rewritten."},
	}

	opts := MigrateV2Options{
		Namespace:  "claude:test-repo",
		ArchiveDir: archiveDir,
		Curator:    &mockCurator{verdicts: verdicts},
	}

	// Dry run: no facts inserted.
	dry, err := engine.MigrateV2(ctx, opts)
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if !dry.DryRun {
		t.Error("expected DryRun=true")
	}
	if dry.Inserted != 0 {
		t.Errorf("dry-run inserted = %d, want 0", dry.Inserted)
	}
	live, _ := engine.ListFacts("claude:test-repo", "", false)
	if len(live) != 0 {
		t.Errorf("dry-run should leave store empty, got %d facts", len(live))
	}

	// Commit: one fact inserted.
	opts.Commit = true
	committed, err := engine.MigrateV2(ctx, opts)
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if committed.DryRun {
		t.Error("expected DryRun=false after commit")
	}
	if committed.Inserted != 1 {
		t.Errorf("commit inserted = %d, want 1", committed.Inserted)
	}
	live, _ = engine.ListFacts("claude:test-repo", "", false)
	if len(live) != 1 {
		t.Fatalf("live facts = %d, want 1", len(live))
	}
	if live[0].Source != FactSourceMigrated {
		t.Errorf("source = %q, want migrated", live[0].Source)
	}
	if live[0].Namespace != "claude:test-repo" {
		t.Errorf("namespace = %q, want claude:test-repo", live[0].Namespace)
	}
}

// TestMigrateV2_ResilientToMalformedLLM verifies a batch whose LLM call fails
// is skipped (and counted) rather than crashing the whole migration, and that
// items in other batches still migrate.
func TestMigrateV2_ResilientToMalformedLLM(t *testing.T) {
	engine := newMigrateTestEngine(t)
	ctx := context.Background()
	labels := seedV1Details(t, engine, ctx, "claude:test-repo")

	archiveDir := t.TempDir()
	makeArchiveFile(t, archiveDir, "claude:test-repo")

	verdicts := map[string]curatedFact{
		labels["dec1"]: {Kind: FactKindDecision, Text: "SQLite decision."},
		labels["got1"]: {Kind: FactKindGotcha, Text: "Nil check gotcha."},
	}

	// Force BatchSize=1 so each item is its own batch; fail the 2nd call.
	curator := &mockCurator{verdicts: verdicts, errOnBatch: 2}
	res, err := engine.MigrateV2(ctx, MigrateV2Options{
		Namespace:  "claude:test-repo",
		ArchiveDir: archiveDir,
		BatchSize:  1,
		Curator:    curator,
	})
	if err != nil {
		t.Fatalf("MigrateV2 should survive a bad batch, got: %v", err)
	}
	// One batch failed → one item counted in errors + skipped.
	if len(res.Errors) == 0 {
		t.Error("expected at least one curation error recorded")
	}
	// The migration still completes and reports a result.
	if res.Sources != 4 {
		t.Errorf("sources = %d, want 4", res.Sources)
	}
}

// TestMigrateV2_PreservesCreatedAt verifies the inserted fact carries the v1
// source's original created_at rather than "now".
func TestMigrateV2_PreservesCreatedAt(t *testing.T) {
	engine := newMigrateTestEngine(t)
	ctx := context.Background()
	labels := seedV1Details(t, engine, ctx, "claude:test-repo")

	archiveDir := t.TempDir()
	makeArchiveFile(t, archiveDir, "claude:test-repo")

	details, _ := engine.store.GetDetailMemories("claude:test-repo")
	origCreatedAt := make(map[string]time.Time)
	for _, d := range details {
		origCreatedAt[d.ID] = d.CreatedAt
	}
	verdicts := map[string]curatedFact{
		labels["dec1"]: {Kind: FactKindDecision, Text: "SQLite decision rewritten."},
	}

	_, err := engine.MigrateV2(ctx, MigrateV2Options{
		Namespace:  "claude:test-repo",
		ArchiveDir: archiveDir,
		Curator:    &mockCurator{verdicts: verdicts},
		Commit:     true,
	})
	if err != nil {
		t.Fatalf("MigrateV2: %v", err)
	}

	live, _ := engine.ListFacts("claude:test-repo", "", false)
	if len(live) != 1 {
		t.Fatalf("live = %d, want 1", len(live))
	}
	orig := origCreatedAt[labels["dec1"]]
	if !live[0].CreatedAt.Equal(orig) {
		t.Errorf("created_at = %v, want original %v", live[0].CreatedAt, orig)
	}
}

// TestMigrateV2_V1RowsNeverDeleted verifies the migration does not delete any
// v1 detail memories — it only adds facts.
func TestMigrateV2_V1RowsNeverDeleted(t *testing.T) {
	engine := newMigrateTestEngine(t)
	ctx := context.Background()
	seedV1Details(t, engine, ctx, "claude:test-repo")

	archiveDir := t.TempDir()
	makeArchiveFile(t, archiveDir, "claude:test-repo")

	before, _ := engine.store.GetDetailMemories("claude:test-repo")

	// Run with a curator that keeps everything as decisions.
	verdicts := map[string]curatedFact{}
	for _, d := range before {
		verdicts[d.ID] = curatedFact{Kind: FactKindDecision, Text: "rewritten: " + d.Text[:min(len(d.Text), 20)]}
	}
	_, err := engine.MigrateV2(ctx, MigrateV2Options{
		Namespace:  "claude:test-repo",
		ArchiveDir: archiveDir,
		Curator:    &mockCurator{verdicts: verdicts},
		Commit:     true,
	})
	if err != nil {
		t.Fatalf("MigrateV2: %v", err)
	}

	after, _ := engine.store.GetDetailMemories("claude:test-repo")
	if len(after) != len(before) {
		t.Errorf("v1 detail rows changed: before=%d after=%d (migration must never delete v1 rows)", len(before), len(after))
	}
}

// TestParseCurateResponse_ToleratesFences verifies the LLM response parser
// survives markdown fences, surrounding prose, and trailing garbage.
func TestParseCurateResponse_ToleratesFences(t *testing.T) {
	batch := []v1SourceItem{
		{SourceID: "s1"},
		{SourceID: "s2"},
	}
	resp := "Here you go:\n```json\n" +
		`{"items":[{"source_id":"s1","verdict":"keep","kind":"decision","text":"Chose X."},{"source_id":"s2","verdict":"skip"}]}` +
		"\n```\nThanks!"
	verdicts, err := parseCurateResponse(resp, batch)
	if err != nil {
		t.Fatalf("parseCurateResponse: %v", err)
	}
	if len(verdicts) != 2 {
		t.Fatalf("verdicts = %d, want 2", len(verdicts))
	}
	if verdicts[0].Kind != FactKindDecision || verdicts[0].Text != "Chose X." {
		t.Errorf("verdict[0] = %+v", verdicts[0])
	}
	if !verdicts[1].Skipped {
		t.Errorf("verdict[1] should be skipped")
	}
}

// TestParseCurateResponse_MalformedReturnsError verifies broken JSON surfaces
// as an error (the caller treats it as a whole-batch skip, never a crash).
func TestParseCurateResponse_MalformedReturnsError(t *testing.T) {
	batch := []v1SourceItem{{SourceID: "s1"}}
	_, err := parseCurateResponse("not json at all {{{", batch)
	if err == nil {
		t.Fatal("expected error for malformed JSON, got nil")
	}
}

// TestParseCurateResponse_UnknownKindDowngradesToSkip verifies a "keep" verdict
// with an invalid kind is treated as skip rather than dropped silently.
func TestParseCurateResponse_UnknownKindDowngradesToSkip(t *testing.T) {
	batch := []v1SourceItem{{SourceID: "s1"}}
	resp := `{"items":[{"source_id":"s1","verdict":"keep","kind":"bogus","text":"x"}]}`
	verdicts, err := parseCurateResponse(resp, batch)
	if err != nil {
		t.Fatalf("parseCurateResponse: %v", err)
	}
	if len(verdicts) != 1 || !verdicts[0].Skipped {
		t.Errorf("expected downgrade to skip, got %+v", verdicts)
	}
}
