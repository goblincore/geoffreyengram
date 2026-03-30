package dualmem

import (
	"context"
	"fmt"
	"strings"
	"testing"
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
	results, err := dp.Search(ctx, queryEmb, userID, 5, "LC-1663")
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
	results, err := dp.Search(ctx, queryEmb, userID, 5, "")
	if err != nil {
		t.Fatalf("Search with empty queryText failed: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("Search returned no results")
	}
}
