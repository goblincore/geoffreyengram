package dualmem

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// mockDistillGenerator is a TextGenerator that returns a canned response. It is
// used as cfg.SynthesisGenerator so the v2 distill path can be exercised
// without a live LLM. Calls counts invocations so tests can assert routing.
type mockDistillGenerator struct {
	response string
	err      error
	calls    int
	prompts  []string
}

func (m *mockDistillGenerator) GenerateText(_ context.Context, prompt string, _ int) (string, error) {
	m.calls++
	m.prompts = append(m.prompts, prompt)
	return m.response, m.err
}

// boolPtr returns a pointer to b, for Config.LegacyDistill.
func boolPtr(b bool) *bool { return &b }

// newDistillTestEngine builds an Engine with the mock embedder and an injectable
// v2 generator (cfg.SynthesisGenerator). Legacy distill is DISABLED so the v2
// fact-candidate path is exercised in isolation.
func newDistillTestEngine(t *testing.T, gen TextGenerator) *Engine {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "distill.db")
	engine, err := New(Config{
		SQLitePath:         dbPath,
		EmbeddingProvider:  &mockEmbedder{dim: 64},
		SynthesisGenerator: gen,
		LegacyDistill:      boolPtr(false),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { engine.Close() })
	return engine
}

// --- parser / prompt unit tests ---

func TestParseFactCandidateResponse_Valid(t *testing.T) {
	input := `{
		"candidates": [
			{"kind": "decision", "text": "Chose SQLite over Postgres for the CLI to keep zero-setup.", "files": ["store_sqlite.go"]},
			{"kind": "deadend", "text": "Storing embeddings as JSON text was too slow on large stores; abandoned."},
			{"kind": "preference", "text": "Donny prefers concise commit messages."}
		]
	}`
	got, err := parseFactCandidateResponse(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 candidates, got %d", len(got))
	}
	if got[0].Kind != FactKindDecision || len(got[0].Files) != 1 {
		t.Fatalf("first candidate mismatch: %+v", got[0])
	}
	if got[1].Kind != FactKindDeadEnd {
		t.Fatalf("second candidate kind mismatch: %+v", got[1])
	}
}

func TestParseFactCandidateResponse_FencedAndEmpty(t *testing.T) {
	// Markdown-fenced JSON with an empty candidate list is a valid answer.
	input := "```json\n" + `{"candidates": []}` + "\n```"
	got, err := parseFactCandidateResponse(input)
	if err != nil {
		t.Fatalf("empty fenced list should parse: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("want 0 candidates, got %d", len(got))
	}

	// Bare fence too.
	input2 := "```\n" + `{"candidates": [{"kind":"gotcha","text":"x is fragile"}]}` + "\n```"
	got2, err := parseFactCandidateResponse(input2)
	if err != nil {
		t.Fatalf("bare fence should parse: %v", err)
	}
	if len(got2) != 1 || got2[0].Kind != FactKindGotcha {
		t.Fatalf("bare fence candidate mismatch: %+v", got2)
	}
}

func TestParseFactCandidateResponse_DropsMalformedEntries(t *testing.T) {
	// Entries with invalid kind or empty text are dropped, not fatal.
	input := `{
		"candidates": [
			{"kind": "decision", "text": "a good fact"},
			{"kind": "bogus", "text": "bad kind"},
			{"kind": "decision", "text": "   "},
			{"kind": "gotcha", "text": "another good one"}
		]
	}`
	got, err := parseFactCandidateResponse(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 valid candidates (drop bad kind + empty text), got %d", len(got))
	}
}

func TestParseFactCandidateResponse_GarbageErrors(t *testing.T) {
	if _, err := parseFactCandidateResponse("totally not json at all"); err == nil {
		t.Fatal("expected error for unparseable body")
	}
}

func TestFormatFactCandidatePrompt(t *testing.T) {
	p := formatFactCandidatePrompt("User: hi", 7)
	if !strings.Contains(p, "User: hi") {
		t.Error("prompt should embed the transcript")
	}
	if !strings.Contains(p, "Maximum 7 candidates") {
		t.Error("prompt should state the max-candidates cap")
	}
	for _, kind := range []string{"decision", "deadend", "gotcha", "preference", "reference"} {
		if !strings.Contains(p, kind) {
			t.Errorf("prompt should enumerate kind %q", kind)
		}
	}
	// The prompt must steer toward FEW facts and permit an empty answer.
	if !strings.Contains(p, "empty") || !strings.Contains(p, "narrative") {
		t.Error("prompt should tell the model to skip narrative and accept an empty list")
	}
}

// --- end-to-end Distill tests (v2 path, legacy disabled) ---

func TestDistillExtractsFactCandidates(t *testing.T) {
	resp := `{"candidates":[
		{"kind":"decision","text":"Chose SQLite over Postgres for the CLI to keep zero-setup.","files":["store_sqlite.go"]},
		{"kind":"deadend","text":"Storing embeddings as JSON text was too slow on large stores."}
	]}`
	gen := &mockDistillGenerator{response: resp}
	engine := newDistillTestEngine(t, gen)
	ctx := context.Background()

	res, err := engine.Distill(ctx, DistillOpts{
		Text:      "User: pick a db\nAssistant: SQLite it is.",
		Namespace: "repo-x",
	}, "user-1")
	if err != nil {
		t.Fatalf("Distill: %v", err)
	}
	if len(res.Candidates) != 2 {
		t.Fatalf("want 2 candidates, got %d", len(res.Candidates))
	}
	if res.FactsWritten != 2 || res.FactsSuperseded != 0 || res.FactsIdentical != 0 {
		t.Fatalf("counts: written=%d superseded=%d identical=%d", res.FactsWritten, res.FactsSuperseded, res.FactsIdentical)
	}
	if gen.calls != 1 {
		t.Fatalf("v2 generator should be called exactly once, got %d", gen.calls)
	}

	// Verify the facts landed with source=inferred, kind, files, session id.
	live, err := engine.ListFacts("repo-x", "", false)
	if err != nil {
		t.Fatalf("ListFacts: %v", err)
	}
	if len(live) != 2 {
		t.Fatalf("want 2 live facts, got %d", len(live))
	}
	var dec *Fact
	for _, f := range live {
		if f.Kind == FactKindDecision {
			dec = f
		}
		if f.Source != FactSourceInferred {
			t.Errorf("fact source = %q, want inferred", f.Source)
		}
	}
	if dec == nil {
		t.Fatal("decision fact not found")
	}
	if len(dec.Files) != 1 || dec.Files[0] != "store_sqlite.go" {
		t.Errorf("decision files = %v, want [store_sqlite.go]", dec.Files)
	}
	// Provenance: git commit stamped (may be "" outside a repo, but SessionID set).
	if dec.SessionID == "" {
		t.Error("fact should carry the session id in provenance")
	}
}

func TestDistillSupersedeOnNearDup(t *testing.T) {
	engine := newDistillTestEngine(t, &mockDistillGenerator{})
	ctx := context.Background()

	// Seed an existing live fact (verified source).
	old, err := engine.AddFact(ctx, Fact{
		Namespace: "repo",
		Kind:      FactKindGotcha,
		Source:    FactSourceVerified,
		Text:      "The rate limiter cleanup skips the nil check intentionally for the hot path.",
		Files:     []string{"rate_limiter.go"},
	})
	if err != nil {
		t.Fatalf("AddFact old: %v", err)
	}

	// Candidate is a near-duplicate (word reorder + suffix): cosine ~0.95 with
	// the mock embedder, above the 0.90 supersede threshold, but NOT identical text.
	nearDup := "The rate limiter cleanup intentionally skips the nil check for hot-path performance."
	// Sanity: the candidate must not be byte-identical (else it'd be a no-op).
	if strings.TrimSpace(nearDup) == strings.TrimSpace(old.Text) {
		t.Fatal("test setup: candidate must differ in text from the seed fact")
	}

	status, err := engine.distillInsertFactCandidate(ctx, FactCandidate{
		Kind:  FactKindGotcha,
		Text:  nearDup,
		Files: []string{"rate_limiter.go"},
	}, "repo", "sess-1")
	if err != nil {
		t.Fatalf("distillInsertFactCandidate: %v", err)
	}
	if status != "superseded" {
		t.Fatalf("status = %q, want superseded", status)
	}

	// Old fact is now superseded; the new one is the live head.
	gotOld, err := engine.GetFact(old.ID)
	if err != nil {
		t.Fatalf("GetFact old: %v", err)
	}
	if gotOld.SupersededBy == "" {
		t.Fatal("old fact should be marked superseded")
	}
	live, err := engine.ListFacts("repo", "", false)
	if err != nil {
		t.Fatalf("ListFacts: %v", err)
	}
	if len(live) != 1 {
		t.Fatalf("want exactly 1 live (head) fact, got %d", len(live))
	}
	if live[0].ID == old.ID {
		t.Fatal("head should be the NEW superseding fact, not the old one")
	}
	if live[0].Source != FactSourceInferred {
		t.Errorf("superseding fact source = %q, want inferred", live[0].Source)
	}
	if live[0].SessionID != "sess-1" {
		t.Errorf("superseding fact session = %q, want sess-1", live[0].SessionID)
	}
}

func TestDistillIdenticalTextNoop(t *testing.T) {
	engine := newDistillTestEngine(t, &mockDistillGenerator{})
	ctx := context.Background()

	text := "Chose SQLite over Postgres for the CLI to keep zero-setup."
	old, err := engine.AddFact(ctx, Fact{
		Namespace: "repo",
		Kind:      FactKindDecision,
		Source:    FactSourceVerified,
		Text:      text,
	})
	if err != nil {
		t.Fatalf("AddFact: %v", err)
	}

	status, err := engine.distillInsertFactCandidate(ctx, FactCandidate{
		Kind: FactKindDecision,
		Text: text, // byte-identical
	}, "repo", "sess-2")
	if err != nil {
		t.Fatalf("distillInsertFactCandidate: %v", err)
	}
	if status != "identical" {
		t.Fatalf("status = %q, want identical", status)
	}

	// Nothing inserted, nothing superseded.
	live, err := engine.ListFacts("repo", "", false)
	if err != nil {
		t.Fatalf("ListFacts: %v", err)
	}
	if len(live) != 1 || live[0].ID != old.ID {
		t.Fatalf("identical candidate must be a no-op; live = %+v", live)
	}
	gotOld, _ := engine.GetFact(old.ID)
	if gotOld.SupersededBy != "" {
		t.Fatal("seed fact must not be superseded by an identical candidate")
	}
}

func TestDistillMalformedOutputResilience(t *testing.T) {
	// Garbage LLM response → v2 extraction errors, but Distill never fails.
	gen := &mockDistillGenerator{response: "<<<not json at all>>>"}
	engine := newDistillTestEngine(t, gen)
	ctx := context.Background()

	res, err := engine.Distill(ctx, DistillOpts{
		Text:      "User: stuff",
		Namespace: "repo",
	}, "user-1")
	if err != nil {
		t.Fatalf("Distill must not fail on malformed LLM output: %v", err)
	}
	if len(res.Candidates) != 0 {
		t.Fatalf("want 0 candidates on garbage, got %d", len(res.Candidates))
	}
	if res.FactsWritten != 0 {
		t.Fatalf("want 0 facts written on garbage, got %d", res.FactsWritten)
	}

	// A response with some valid + some malformed candidates: valid ones land,
	// malformed ones are silently dropped (counted as 0 since they never reach
	// the insert step after parse-time filtering).
	gen2 := &mockDistillGenerator{response: `{"candidates":[
		{"kind":"decision","text":"a real fact"},
		{"kind":"nope","text":"bad kind"},
		{"kind":"decision","text":"   "}
	]}`}
	engine2 := newDistillTestEngine(t, gen2)
	res2, err := engine2.Distill(ctx, DistillOpts{Text: "User: x", Namespace: "repo"}, "u")
	if err != nil {
		t.Fatalf("Distill: %v", err)
	}
	if len(res2.Candidates) != 1 {
		t.Fatalf("want 1 valid candidate after parse filtering, got %d", len(res2.Candidates))
	}
	if res2.FactsWritten != 1 {
		t.Fatalf("want 1 fact written, got %d", res2.FactsWritten)
	}
}

func TestDistillPreferenceIsUserGlobal(t *testing.T) {
	// A preference candidate should be stored with namespace "" (user-global),
	// not the repo namespace, even though distill runs in a repo context.
	resp := `{"candidates":[{"kind":"preference","text":"Prefer concise commit messages with a clear subject line."}]}`
	engine := newDistillTestEngine(t, &mockDistillGenerator{response: resp})
	ctx := context.Background()

	_, err := engine.Distill(ctx, DistillOpts{Text: "User: x", Namespace: "repo-y"}, "u")
	if err != nil {
		t.Fatalf("Distill: %v", err)
	}

	// The preference should appear in the user-global namespace.
	global, err := engine.ListFacts("", FactKindPreference, false)
	if err != nil {
		t.Fatalf("ListFacts global: %v", err)
	}
	if len(global) != 1 {
		t.Fatalf("want 1 user-global preference, got %d", len(global))
	}
	if global[0].Namespace != "" {
		t.Errorf("preference namespace = %q, want empty (user-global)", global[0].Namespace)
	}
	// And NOT in the repo namespace.
	repoFacts, _ := engine.ListFacts("repo-y", "", false)
	if len(repoFacts) != 0 {
		t.Errorf("preference should not be repo-scoped, got %d repo facts", len(repoFacts))
	}
}

func TestDistillLegacySwitchOff(t *testing.T) {
	// With LegacyDistill disabled and NO Summarizer set, the legacy path must
	// never be invoked (it would panic/error on the missing Summarizer). The v2
	// path runs alone via SynthesisGenerator.
	resp := `{"candidates":[{"kind":"decision","text":"a fact"}]}`
	engine := newDistillTestEngine(t, &mockDistillGenerator{response: resp})
	ctx := context.Background()

	res, err := engine.Distill(ctx, DistillOpts{Text: "User: x", Namespace: "repo"}, "u")
	if err != nil {
		t.Fatalf("Distill: %v", err)
	}
	// Legacy fields stay zeroed.
	if len(res.Facts) != 0 || res.Triples != nil || res.Written != 0 {
		t.Errorf("legacy path should not run when disabled: %+v", res)
	}
	if res.FactsWritten != 1 {
		t.Errorf("v2 path should still write: got FactsWritten=%d", res.FactsWritten)
	}
}
