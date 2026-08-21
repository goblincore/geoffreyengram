package dualmem

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestExtractIdentifiers(t *testing.T) {
	tests := []struct {
		query    string
		wantAny  []string // at least one of these must appear in result
		wantNone bool     // if true, result must be empty
	}{
		{
			query:   "LC-1663",
			wantAny: []string{"LC-1663"},
		},
		{
			query:   "status of LC-1663 task",
			wantAny: []string{"LC-1663"},
		},
		{
			query:   "fix PR-42 bug",
			wantAny: []string{"PR-42"},
		},
		{
			query:    "auth system refactor",
			wantNone: true,
		},
		{
			query:    "what next",
			wantNone: true,
		},
		{
			query:   "v2.1 migration",
			wantAny: []string{"v2.1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			got := extractIdentifiers(tt.query)
			if tt.wantNone {
				if len(got) != 0 {
					t.Errorf("extractIdentifiers(%q) = %v, want empty", tt.query, got)
				}
				return
			}
			for _, want := range tt.wantAny {
				found := false
				for _, g := range got {
					if strings.EqualFold(g, want) {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("extractIdentifiers(%q) = %v, want %q in result", tt.query, got, want)
				}
			}
		})
	}
}

func TestKeywordMatchScore(t *testing.T) {
	tests := []struct {
		name      string
		query     string
		text      string
		wantAbove float64 // score must be above this threshold
		wantBelow float64 // score must be below this threshold (0 = no upper bound)
	}{
		{
			name:      "exact identifier match",
			query:     "LC-1663",
			text:      "LC-1663: Inline finalization is fire-and-forget during createProfile",
			wantAbove: 1.0, // identifier bonus kicks in
		},
		{
			name:      "no match",
			query:     "LC-1663",
			text:      "auth middleware rewrite driven by legal compliance",
			wantAbove: -1,
			wantBelow: 0.1,
		},
		{
			name:      "empty query",
			query:     "",
			text:      "some memory text",
			wantAbove: -1,
			wantBelow: 0.01,
		},
		{
			name:      "token overlap without identifier",
			query:     "auth system",
			text:      "auth middleware rewrite for the system",
			wantAbove: 0.3, // partial token match
			wantBelow: 1.5, // no identifier bonus
		},
		{
			name:      "partial token overlap",
			query:     "auth system refactor",
			text:      "auth middleware",
			wantAbove: 0.1,
			wantBelow: 1.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := keywordMatchScore(tt.query, tt.text)
			if tt.wantAbove >= 0 && got <= tt.wantAbove {
				t.Errorf("keywordMatchScore(%q, %q) = %.3f, want > %.3f", tt.query, tt.text, got, tt.wantAbove)
			}
			if tt.wantBelow > 0 && got >= tt.wantBelow {
				t.Errorf("keywordMatchScore(%q, %q) = %.3f, want < %.3f", tt.query, tt.text, got, tt.wantBelow)
			}
		})
	}
}

func TestDetailSearch_HybridRanking(t *testing.T) {
	store := newTestStore(t)
	embedder := &mockEmbedder{dim: 64}

	dp := NewDetailPath(store, embedder, 0.5, 100, nil)
	ctx := context.Background()
	userID := "test-user-hybrid"

	// Insert: one memory with "LC-1663" in text, several unrelated ones
	memories := []string{
		"auth middleware rewrite driven by legal compliance requirements",
		"LC-1663: Inline finalization is fire-and-forget during createProfile. VCs get signed server-side.",
		"button component uses React.memo for performance",
		"database migration uses SQLite for zero-setup",
		"LC-1637 ConsentFlow guide wizard steps completed",
	}

	for i, text := range memories {
		emb, _ := embedder.Embed(ctx, text, "")
		dm := &DetailMemory{
			ID:              fmt.Sprintf("mem-%d", i),
			Text:            text,
			ImportanceScore: 0.75,
			Salience:        0.7,
			Sector:          "semantic",
			Type:            "decision",
		}
		dp.Insert(ctx, dm, emb, userID)
	}

	// Search for "LC-1663" — the specific memory should rank first
	queryEmb, _ := embedder.Embed(ctx, "LC-1663", "RETRIEVAL_QUERY")
	results, err := dp.Search(ctx, queryEmb, userID, 5, "LC-1663", nil)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	if len(results) == 0 {
		t.Fatal("Search returned no results")
	}

	if !strings.Contains(results[0].Text, "LC-1663") {
		t.Errorf("Expected LC-1663 memory to rank first, got: %q", results[0].Text)
	}
}

func TestDetailSearch_PureCosineFallback(t *testing.T) {
	store := newTestStore(t)
	embedder := &mockEmbedder{dim: 64}

	dp := NewDetailPath(store, embedder, 0.5, 100, nil)
	ctx := context.Background()
	userID := "test-user-cosine"

	memories := []string{
		"auth middleware rewrite driven by legal compliance",
		"button component uses React.memo for performance",
	}

	for i, text := range memories {
		emb, _ := embedder.Embed(ctx, text, "")
		dm := &DetailMemory{
			ID:              fmt.Sprintf("mem-%d", i),
			Text:            text,
			ImportanceScore: 0.75,
			Salience:        0.7,
			Sector:          "semantic",
			Type:            "decision",
		}
		dp.Insert(ctx, dm, emb, userID)
	}

	// Empty queryText = pure cosine, should not panic
	queryEmb, _ := embedder.Embed(ctx, "auth", "RETRIEVAL_QUERY")
	results, err := dp.Search(ctx, queryEmb, userID, 5, "", nil)
	if err != nil {
		t.Fatalf("Search with empty queryText failed: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("Search returned no results")
	}
}

func TestDetailSearch_IdentifierPreFilter(t *testing.T) {
	// Verifies that identifier-matched memories are guaranteed in results even
	// when they would normally be cut off by the limit on cosine ranking alone.
	store := newTestStore(t)
	embedder := &mockEmbedder{dim: 64}

	dp := NewDetailPath(store, embedder, 0.5, 100, nil)
	ctx := context.Background()
	userID := "test-user-prefilter"

	// Insert 4 generic memories + 1 LC-1663 specific one.
	// With limit=3, pure cosine might cut the LC-1663 memory.
	memories := []string{
		"auth middleware rewrite driven by legal compliance requirements",
		"button component uses React.memo for performance optimization",
		"database migration uses SQLite for zero-setup deployments",
		"navigation uses React Router with lazy loading for code splitting",
		"LC-1663: Inline finalization is fire-and-forget during createProfile",
	}
	for i, text := range memories {
		emb, _ := embedder.Embed(ctx, text, "")
		dm := &DetailMemory{
			ID:              fmt.Sprintf("prefilter-mem-%d", i),
			Text:            text,
			ImportanceScore: 0.75,
			Salience:        0.7,
			Sector:          "semantic",
			Type:            "decision",
		}
		dp.Insert(ctx, dm, emb, userID)
	}

	// limit=3, but LC-1663 memory must still appear
	queryEmb, _ := embedder.Embed(ctx, "LC-1663", "RETRIEVAL_QUERY")
	results, err := dp.Search(ctx, queryEmb, userID, 3, "LC-1663", nil)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	found := false
	for _, r := range results {
		if strings.Contains(r.Text, "LC-1663") {
			found = true
			break
		}
	}
	if !found {
		texts := make([]string, len(results))
		for i, r := range results {
			l := len(r.Text)
		if l > 50 {
			l = 50
		}
		texts[i] = r.Text[:l]
		}
		t.Errorf("LC-1663 memory not found in results (limit=3, got %d): %v", len(results), texts)
	}
}

func TestDetailSearch_GraphBoost(t *testing.T) {
	store := newTestStore(t)
	embedder := &mockEmbedder{dim: 64}

	dp := NewDetailPath(store, embedder, 0.5, 100, nil)
	ctx := context.Background()
	userID := "test-user-graphboost"

	// Insert several memories — the "auth middleware" memory will get graph-boosted
	memories := []struct {
		id   string
		text string
	}{
		{"gb-mem-0", "button component uses React.memo for performance optimization"},
		{"gb-mem-1", "auth middleware rewrite driven by legal compliance requirements"},
		{"gb-mem-2", "database migration uses SQLite for zero-setup deployments"},
		{"gb-mem-3", "navigation uses React Router with lazy loading for code splitting"},
	}

	for _, m := range memories {
		emb, _ := embedder.Embed(ctx, m.text, "")
		dm := &DetailMemory{
			ID:              m.id,
			Text:            m.text,
			ImportanceScore: 0.75,
			Salience:        0.5,
			Sector:          "semantic",
			Type:            "decision",
		}
		dp.Insert(ctx, dm, emb, userID)
	}

	// Query for "database" — without boost, "database migration" would rank
	// by cosine/keyword. With a boost on "auth middleware" (gb-mem-1),
	// the auth memory should be promoted above its natural ranking.
	queryEmb, _ := embedder.Embed(ctx, "database migration", "RETRIEVAL_QUERY")

	// First: search WITHOUT graph boost — database should be first
	resultsNoBoost, err := dp.Search(ctx, queryEmb, userID, 4, "database migration", nil)
	if err != nil {
		t.Fatalf("Search without boost failed: %v", err)
	}
	if len(resultsNoBoost) == 0 {
		t.Fatal("No results returned")
	}

	// Find auth memory's position without boost
	authPosNoBoost := -1
	for i, r := range resultsNoBoost {
		if r.ID == "gb-mem-1" {
			authPosNoBoost = i
			break
		}
	}

	// Now search WITH graph boost on the auth memory
	graphBoost := map[string]float64{
		"gb-mem-1": 0.5, // large boost to ensure it moves up
	}
	resultsWithBoost, err := dp.Search(ctx, queryEmb, userID, 4, "database migration", graphBoost)
	if err != nil {
		t.Fatalf("Search with boost failed: %v", err)
	}

	authPosWithBoost := -1
	for i, r := range resultsWithBoost {
		if r.ID == "gb-mem-1" {
			authPosWithBoost = i
			break
		}
	}

	if authPosWithBoost < 0 {
		t.Fatal("Auth memory not found in boosted results")
	}

	// The boosted auth memory should rank higher (lower index) than without boost
	if authPosNoBoost >= 0 && authPosWithBoost >= authPosNoBoost {
		t.Errorf("Graph boost did not improve auth memory ranking: pos %d (no boost) vs %d (with boost)",
			authPosNoBoost, authPosWithBoost)
	}

	// With a 0.5 boost, auth should be ranked first
	if resultsWithBoost[0].ID != "gb-mem-1" {
		t.Errorf("Expected auth memory (gb-mem-1) to rank first with large boost, got %q", resultsWithBoost[0].ID)
	}
}

func TestDetailSearch_GraphBoostNilNoEffect(t *testing.T) {
	// Verify that passing nil graphBoostedIDs doesn't change behavior
	store := newTestStore(t)
	embedder := &mockEmbedder{dim: 64}

	dp := NewDetailPath(store, embedder, 0.5, 100, nil)
	ctx := context.Background()
	userID := "test-user-nilboost"

	emb, _ := embedder.Embed(ctx, "test memory", "")
	dm := &DetailMemory{
		ID:              "nil-mem-0",
		Text:            "test memory about auth",
		ImportanceScore: 0.75,
		Salience:        0.5,
	}
	dp.Insert(ctx, dm, emb, userID)

	queryEmb, _ := embedder.Embed(ctx, "auth", "RETRIEVAL_QUERY")

	// Both nil and empty map should produce the same results
	r1, err := dp.Search(ctx, queryEmb, userID, 5, "auth", nil)
	if err != nil {
		t.Fatalf("Search with nil boost failed: %v", err)
	}
	r2, err := dp.Search(ctx, queryEmb, userID, 5, "auth", map[string]float64{})
	if err != nil {
		t.Fatalf("Search with empty boost failed: %v", err)
	}

	if len(r1) != len(r2) {
		t.Errorf("Result count mismatch: nil=%d empty=%d", len(r1), len(r2))
	}
}


// At capacity, a new memory whose importance ties the lowest existing one used
// to be dropped: Insert required a strict lowest < new, so it returned the new
// text for sketch routing and reported no error. The type floor pins large
// numbers of memories at exactly Theta+0.05, so ties are the common case and
// full namespaces silently stopped accepting anything new.
func TestDetailInsert_TieAtCapacityEvictsLeastRecentlyAccessed(t *testing.T) {
	store := newTestStore(t)
	embedder := &mockEmbedder{dim: 64}
	ctx := context.Background()
	userID := "test-user-capacity"

	const capacity = 3
	const tiedScore = 0.70
	dp := NewDetailPath(store, embedder, 0.65, capacity, nil)

	// Fill to capacity with memories that all share the same importance score.
	// "oldest" is the least recently accessed, so it is the one that should go.
	fill := []struct {
		id           string
		lastAccessed time.Time
	}{
		{"keep-b", time.Now().Add(-1 * time.Hour)},
		{"oldest", time.Now().Add(-72 * time.Hour)},
		{"keep-a", time.Now().Add(-2 * time.Hour)},
	}
	for _, f := range fill {
		emb, _ := embedder.Embed(ctx, f.id, "")
		dm := &DetailMemory{
			ID:              f.id,
			Text:            "tied memory " + f.id,
			Sector:          "decision",
			Salience:        0.7,
			ImportanceScore: tiedScore,
			Type:            "general",
			CreatedAt:       f.lastAccessed,
			LastAccessedAt:  f.lastAccessed,
		}
		if err := store.InsertDetail(dm, emb, userID); err != nil {
			t.Fatalf("seeding %s: %v", f.id, err)
		}
		// InsertDetail does not persist CreatedAt/LastAccessedAt — the columns
		// default to datetime('now') at one-second resolution. Set them directly
		// so the rows have the distinct timestamps that real data has.
		stamp := f.lastAccessed.UTC().Format("2006-01-02 15:04:05")
		if _, err := store.db.Exec(
			`UPDATE detail_memories SET created_at = ?, last_accessed_at = ? WHERE id = ?`,
			stamp, stamp, f.id); err != nil {
			t.Fatalf("stamping %s: %v", f.id, err)
		}
	}

	// A new memory with the identical score must still get in.
	emb, _ := embedder.Embed(ctx, "newcomer", "")
	newcomer := &DetailMemory{
		ID:              "newcomer",
		Text:            "tied memory newcomer",
		Sector:          "decision",
		Salience:        0.7,
		ImportanceScore: tiedScore,
		Type:            "general",
		CreatedAt:       time.Now(),
		LastAccessedAt:  time.Now(),
	}
	demoted, err := dp.Insert(ctx, newcomer, emb, userID)
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}

	details, err := store.GetDetailMemories(userID)
	if err != nil {
		t.Fatalf("GetDetailMemories: %v", err)
	}
	present := map[string]bool{}
	for _, d := range details {
		present[d.ID] = true
	}

	if !present["newcomer"] {
		t.Errorf("newcomer was not inserted; Insert returned demoted=%q (it was routed to sketch instead)", demoted)
	}
	if len(details) != capacity {
		t.Errorf("detail count = %d, want %d (capacity must be respected)", len(details), capacity)
	}
	if present["oldest"] {
		t.Errorf("evicted the wrong row: least-recently-accessed 'oldest' should have been demoted, got %v", present)
	}
	if !present["keep-a"] || !present["keep-b"] {
		t.Errorf("evicted a more-recently-accessed row; present = %v", present)
	}
	if demoted != "tied memory oldest" {
		t.Errorf("demoted = %q, want the evicted oldest memory's text", demoted)
	}
}
