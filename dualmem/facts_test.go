package dualmem

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// newFactsTestEngine builds an Engine backed by a temp SQLite DB with the
// shared mock embedder. It mirrors newTestEngine (dualmem_test.go) but is kept
// local to the facts tests so they don't depend on unrelated fixture state.
func newFactsTestEngine(t *testing.T) *Engine {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "facts.db")
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

func TestFactsCRUD(t *testing.T) {
	engine := newFactsTestEngine(t)
	ctx := context.Background()

	// Add
	f, err := engine.AddFact(ctx, Fact{
		Namespace: "repo-x",
		Kind:      FactKindDecision,
		Source:    FactSourceVerified,
		Text:      "Chose SQLite over Postgres for the CLI to keep zero-setup.",
		Files:     []string{"store_sqlite.go"},
	})
	if err != nil {
		t.Fatalf("AddFact: %v", err)
	}
	if f.ID == "" {
		t.Fatal("AddFact returned empty ID")
	}
	if f.CreatedAt.IsZero() {
		t.Fatal("AddFact did not stamp CreatedAt")
	}
	if len(f.Files) != 1 || f.Files[0] != "store_sqlite.go" {
		t.Fatalf("Files not preserved: %v", f.Files)
	}

	// Get
	got, err := engine.GetFact(f.ID)
	if err != nil {
		t.Fatalf("GetFact: %v", err)
	}
	if got.Text != f.Text {
		t.Fatalf("GetFact text mismatch: got %q want %q", got.Text, f.Text)
	}
	if got.Kind != FactKindDecision || got.Source != FactSourceVerified {
		t.Fatalf("GetFact kind/source mismatch: %+v", got)
	}
	if got.Namespace != "repo-x" {
		t.Fatalf("GetFact namespace mismatch: %q", got.Namespace)
	}
	if got.SupersededBy != "" {
		t.Fatalf("fresh fact should not be superseded, got %q", got.SupersededBy)
	}
	if len(got.Vector) == 0 {
		t.Fatal("GetFact should populate Vector for search")
	}

	// List sees it
	listed, err := engine.ListFacts("repo-x", "", false)
	if err != nil {
		t.Fatalf("ListFacts: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("ListFacts: want 1 fact, got %d", len(listed))
	}

	// Kind filter narrows correctly
	decisions, _ := engine.ListFacts("repo-x", FactKindDecision, false)
	if len(decisions) != 1 {
		t.Fatalf("kind=decision filter: want 1, got %d", len(decisions))
	}
	gotchas, _ := engine.ListFacts("repo-x", FactKindGotcha, false)
	if len(gotchas) != 0 {
		t.Fatalf("kind=gotcha filter: want 0, got %d", len(gotchas))
	}
}

func TestFactValidation(t *testing.T) {
	engine := newFactsTestEngine(t)
	ctx := context.Background()

	cases := []struct {
		name string
		fact Fact
		want string // substring expected in the error
	}{
		{
			name: "invalid kind",
			fact: Fact{Kind: "bogus", Source: FactSourceVerified, Text: "hi"},
			want: "invalid fact kind",
		},
		{
			name: "empty kind",
			fact: Fact{Kind: "", Source: FactSourceVerified, Text: "hi"},
			want: "invalid fact kind",
		},
		{
			name: "invalid source",
			fact: Fact{Kind: FactKindDecision, Source: "guessed", Text: "hi"},
			want: "invalid fact source",
		},
		{
			name: "empty text",
			fact: Fact{Kind: FactKindDecision, Source: FactSourceVerified, Text: "   "},
			want: "text must be non-empty",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := engine.AddFact(ctx, tc.fact)
			if err == nil {
				t.Fatalf("AddFact: expected error containing %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("AddFact: expected error containing %q, got %q", tc.want, err.Error())
			}
		})
	}

	// ListFacts also validates kind.
	if _, err := engine.ListFacts("repo", "nope", false); err == nil {
		t.Fatal("ListFacts with invalid kind should error")
	}
}

// TestFactSupersedeChain builds A -> B -> C and verifies:
//   - the chain is walkable via SupersededBy pointers;
//   - default ListFacts excludes superseded entries (only C remains);
//   - includeSuperseded=true surfaces the whole chain.
func TestFactSupersedeChain(t *testing.T) {
	engine := newFactsTestEngine(t)
	ctx := context.Background()

	a, err := engine.AddFact(ctx, Fact{
		Namespace: "repo",
		Kind:      FactKindDecision,
		Source:    FactSourceVerified,
		Text:      "Use a single SQLite file at ~/.dualmem/mem.db.",
	})
	if err != nil {
		t.Fatalf("AddFact A: %v", err)
	}

	b, err := engine.SupersedeFact(ctx, a.ID, Fact{
		Kind:   FactKindDecision,
		Source: FactSourceVerified,
		Text:   "Use SQLite but split into per-namespace files.",
	})
	if err != nil {
		t.Fatalf("SupersedeFact A->B: %v", err)
	}
	if b.Namespace != a.Namespace {
		t.Fatalf("superseding fact should inherit namespace: got %q want %q", b.Namespace, a.Namespace)
	}

	c, err := engine.SupersedeFact(ctx, b.ID, Fact{
		Kind:   FactKindDecision,
		Source: FactSourceVerified,
		Text:   "Use a single SQLite file with a namespace column on every table.",
	})
	if err != nil {
		t.Fatalf("SupersedeFact B->C: %v", err)
	}

	// Walk the chain A -> B -> C.
	wantChain := []string{b.ID, c.ID}
	cur := a.ID
	for i, want := range wantChain {
		got, err := engine.GetFact(cur)
		if err != nil {
			t.Fatalf("walk chain step %d: GetFact(%q): %v", i, cur, err)
		}
		if got.SupersededBy != want {
			t.Fatalf("chain step %d: %q.SupersededBy = %q, want %q", i, cur, got.SupersededBy, want)
		}
		cur = got.SupersededBy
	}
	// C is the head: not superseded.
	head, _ := engine.GetFact(c.ID)
	if head.SupersededBy != "" {
		t.Fatalf("head of chain should not be superseded, got %q", head.SupersededBy)
	}

	// Default list: only the live head C.
	live, err := engine.ListFacts("repo", "", false)
	if err != nil {
		t.Fatalf("ListFacts default: %v", err)
	}
	if len(live) != 1 || live[0].ID != c.ID {
		t.Fatalf("ListFacts default: want only [%s], got %+v", c.ID, idsOf(live))
	}

	// includeSuperseded: all three.
	all, err := engine.ListFacts("repo", "", true)
	if err != nil {
		t.Fatalf("ListFacts all: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("ListFacts includeSuperseded: want 3, got %d (%+v)", len(all), idsOf(all))
	}

	// Cannot supersede an already-superseded fact (must target the head).
	if _, err := engine.SupersedeFact(ctx, a.ID, Fact{
		Kind: FactKindDecision, Source: FactSourceVerified, Text: "nope",
	}); err == nil {
		t.Fatal("SupersedeFact on already-superseded A should error; must target the head")
	}
}

// TestFactSearchBlendsGlobalAndRepo verifies that searching a repo namespace
// also surfaces user-global (namespace "") facts, e.g. preferences.
func TestFactSearchBlendsGlobalAndRepo(t *testing.T) {
	engine := newFactsTestEngine(t)
	ctx := context.Background()

	// User-global preference (namespace "").
	if _, err := engine.AddFact(ctx, Fact{
		Namespace: "",
		Kind:      FactKindPreference,
		Source:    FactSourceVerified,
		Text:      "Donny prefers concise commit messages with a clear subject line.",
	}); err != nil {
		t.Fatalf("AddFact preference: %v", err)
	}

	// Repo-scoped decision.
	if _, err := engine.AddFact(ctx, Fact{
		Namespace: "repo",
		Kind:      FactKindDecision,
		Source:    FactSourceVerified,
		Text:      "Donny prefers SQLite for the local memory store on this repo.",
	}); err != nil {
		t.Fatalf("AddFact decision: %v", err)
	}

	// Searching the repo namespace should return BOTH.
	hits, err := engine.SearchFacts(ctx, "what does Donny prefer for storage and commits", "repo", "", 10)
	if err != nil {
		t.Fatalf("SearchFacts: %v", err)
	}
	if len(hits) < 2 {
		t.Fatalf("SearchFacts: expected global+repo blend to return >=2, got %d", len(hits))
	}
	sawGlobal, sawRepo := false, false
	for _, h := range hits {
		if h.Namespace == "" {
			sawGlobal = true
		}
		if h.Namespace == "repo" {
			sawRepo = true
		}
		if h.Similarity == 0 {
			t.Fatal("SearchFacts should set Similarity on results")
		}
	}
	if !sawGlobal {
		t.Error("SearchFacts: user-global preference was not blended into repo search")
	}
	if !sawRepo {
		t.Error("SearchFacts: repo-scoped fact missing from repo search")
	}

	// Searching the empty namespace directly still works and finds the global.
	hitsGlobal, err := engine.SearchFacts(ctx, "commit message preference", "", FactKindPreference, 10)
	if err != nil {
		t.Fatalf("SearchFacts global: %v", err)
	}
	if len(hitsGlobal) == 0 {
		t.Fatal("SearchFacts global: expected at least one preference")
	}
}

func TestSearchFactsMultiWithEmbeddingAvoidsProvider(t *testing.T) {
	engine := newFactsTestEngine(t)
	ctx := context.Background()

	if _, err := engine.AddFact(ctx, Fact{
		Namespace: "repo",
		Kind:      FactKindDecision,
		Source:    FactSourceVerified,
		Text:      "Use SQLite for local context storage.",
	}); err != nil {
		t.Fatalf("AddFact: %v", err)
	}

	queryEmbedding, err := engine.Embedder().Embed(ctx, "local context storage", "RETRIEVAL_QUERY")
	if err != nil {
		t.Fatalf("Embed query fixture: %v", err)
	}
	embedder := &unavailableEmbedder{dim: 64}
	engine.embedder = embedder

	hits, err := engine.searchFactsMultiWithEmbedding(ctx, "local context storage", "repo", nil, 5, queryEmbedding)
	if err != nil {
		t.Fatalf("searchFactsMultiWithEmbedding: %v", err)
	}
	if len(hits) != 1 || hits[0].Text != "Use SQLite for local context storage." {
		t.Errorf("hits = %#v, want the stored fact", hits)
	}
	if embedder.calls != 0 {
		t.Errorf("Embed calls = %d, want 0 when using a precomputed vector", embedder.calls)
	}
}

// TestFactSearchExcludesSuperseded verifies SearchFacts never returns a
// superseded fact, even if it would otherwise rank highly.
func TestFactSearchExcludesSuperseded(t *testing.T) {
	engine := newFactsTestEngine(t)
	ctx := context.Background()

	old, err := engine.AddFact(ctx, Fact{
		Namespace: "repo",
		Kind:      FactKindDeadEnd,
		Source:    FactSourceVerified,
		Text:      "Tried storing embeddings as JSON text; too slow on large stores.",
	})
	if err != nil {
		t.Fatalf("AddFact old: %v", err)
	}
	if _, err := engine.SupersedeFact(ctx, old.ID, Fact{
		Kind:   FactKindDeadEnd,
		Source: FactSourceVerified,
		Text:   "Store embeddings as little-endian float32 BLOBs for fast decode.",
	}); err != nil {
		t.Fatalf("SupersedeFact: %v", err)
	}

	hits, err := engine.SearchFacts(ctx, "embedding storage too slow JSON", "repo", "", 10)
	if err != nil {
		t.Fatalf("SearchFacts: %v", err)
	}
	for _, h := range hits {
		if h.ID == old.ID {
			t.Fatal("SearchFacts returned a superseded fact")
		}
	}
	if len(hits) == 0 {
		t.Fatal("SearchFacts: expected the superseding fact in results")
	}
}

// TestFactSearchFileMatchBoost checks the file-path match component: a fact
// whose file path appears in the query outranks an otherwise-similar fact.
func TestFactSearchFileMatchBoost(t *testing.T) {
	engine := newFactsTestEngine(t)
	ctx := context.Background()

	// Two near-identical decisions; one cites a file, one doesn't.
	if _, err := engine.AddFact(ctx, Fact{
		Namespace: "repo",
		Kind:      FactKindGotcha,
		Source:    FactSourceVerified,
		Text:      "The walker limit interacts with directory depth in this module.",
		Files:     []string{"codemap.go"},
	}); err != nil {
		t.Fatalf("AddFact cited: %v", err)
	}
	if _, err := engine.AddFact(ctx, Fact{
		Namespace: "repo",
		Kind:      FactKindGotcha,
		Source:    FactSourceVerified,
		Text:      "The walker limit interacts with directory depth in this module.",
	}); err != nil {
		t.Fatalf("AddFact uncited: %v", err)
	}

	hits, err := engine.SearchFacts(ctx, "walker limit issue in codemap.go", "repo", "", 10)
	if err != nil {
		t.Fatalf("SearchFacts: %v", err)
	}
	if len(hits) < 2 {
		t.Fatalf("want >=2 hits, got %d", len(hits))
	}
	if hits[0].FileMatch == 0 {
		t.Fatalf("top hit should have a file match, got FileMatch=0 for %q", hits[0].Text)
	}
	// The file-cited fact should rank first (file match boost on top of equal cosine).
	if len(hits[0].Files) == 0 {
		t.Fatal("expected the file-cited fact to rank first")
	}
}

func idsOf(fs []*Fact) []string {
	out := make([]string, len(fs))
	for i, f := range fs {
		out[i] = f.ID
	}
	return out
}
