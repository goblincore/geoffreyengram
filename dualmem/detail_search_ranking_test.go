package dualmem

// Regression tests for the 2026-04-24 retrieval ranking bug.
//
// Bug: distinctive-phrase continuity/decision/investigation memories lose to
// long checkpoint blobs that share broad domain vocabulary with the query.
// See Claude Notes/Research/2026-04-24-dualmem-retrieval-ranking-bug.md for
// the full repro write-up.

import (
	"context"
	"fmt"
	"math"
	"strings"
	"testing"
)

// makeUnitVec returns a unit vector with the supplied components in the
// leading dimensions, normalized to length 1. Useful for hand-crafting cosine
// similarity relationships in ranking tests.
func makeUnitVec(dim int, components ...float32) []float32 {
	v := make([]float32, dim)
	var sumSq float64
	for i, c := range components {
		v[i] = c
		sumSq += float64(c) * float64(c)
	}
	if sumSq <= 0 {
		return v
	}
	norm := float32(math.Sqrt(sumSq))
	for i := range v {
		v[i] /= norm
	}
	return v
}

func truncateForLog(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ReplaceAll(s[:n], "\n", " ") + "..."
}

// TestDetailSearch_TypedHighSalienceMustRankInTop3 captures the core contract
// the writeup is asserting: when a high-salience typed memory (continuity /
// decision / investigation / warning) is competing with checkpoint memories
// that share the same query vocabulary but have higher raw cosine similarity
// (the dominant production failure mode), the typed memory must still land in
// the top-3 returned by Search.
//
// This is the ranking-bug regression that motivates the fix; see the writeup
// at Claude Notes/Research/2026-04-24-dualmem-retrieval-ranking-bug.md.
func TestDetailSearch_TypedHighSalienceMustRankInTop3(t *testing.T) {
	store := newTestStore(t)
	embedder := &mockEmbedder{dim: 8}
	dp := NewDetailPath(store, embedder, 0.5, 100, nil)
	ctx := context.Background()
	userID := "test-typed-priority"
	dim := 8

	// Query embedding biased toward axis-0 — the same axis the long checkpoint
	// distractors live on. Their raw cosine will dominate the target's.
	queryVec := makeUnitVec(dim, 0.95, 0.31)
	queryText := "dualmem stack architecture status"

	// Target: a high-salience continuity memory whose text contains every query
	// token (kwScore = 1.0) but whose embedding lies far from the query axis
	// (low cosine). This is the "distinctive memory drowned by vocab-overlap
	// checkpoints" pattern from the writeup.
	target := &DetailMemory{
		ID: "target-continuity",
		Text: "TODO: review dualmem stack architecture and status. " +
			"Specific gaps: ranking, type-priority, recency.",
		Sector:          "procedural",
		Salience:        0.85,
		ImportanceScore: 0.85,
		Type:            "continuity",
	}
	if err := store.InsertDetail(target, makeUnitVec(dim, 0.20, 0.98), userID); err != nil {
		t.Fatalf("insert target: %v", err)
	}

	// Five checkpoint distractors. Each text contains every query token too
	// (so kwScore is identical to the target's → kwNorm = 1.0 for everyone),
	// but their embeddings sit on the query axis → high cosine. With the
	// current code, these win on hybrid score and push the target out of the
	// top-N pool; the high-salience guarantee can only inject 2 unseen
	// memories at the *end* of the list, so the target lands at rank 6+.
	distractors := []struct {
		id       string
		text     string
		cosLead  float32
		cosOther float32
	}{
		{"ckpt-1", `{"task":"dualmem stack architecture status sweep","status":"done","notes":"baseline"}`, 0.97, 0.24},
		{"ckpt-2", `{"task":"dualmem stack architecture status patch","status":"done","notes":"benchmark sweep"}`, 0.96, 0.28},
		{"ckpt-3", `{"task":"dualmem stack architecture status verify","status":"done","notes":"comparison"}`, 0.95, 0.31},
		{"ckpt-4", `{"task":"dualmem stack architecture status retry","status":"done","notes":"swap default"}`, 0.94, 0.34},
		{"ckpt-5", `{"task":"dualmem stack architecture status final","status":"done","notes":"verification"}`, 0.93, 0.37},
	}
	for _, d := range distractors {
		dm := &DetailMemory{
			ID:              d.id,
			Text:            d.text,
			Sector:          "procedural",
			Salience:        0.70,
			ImportanceScore: 0.70,
			Type:            "checkpoint",
		}
		if err := store.InsertDetail(dm, makeUnitVec(dim, d.cosLead, d.cosOther), userID); err != nil {
			t.Fatalf("insert distractor %s: %v", d.id, err)
		}
	}

	// limit=5 mirrors the production CLI default (cmdSearch's --limit defaults
	// to 5; AssembleContext passes Limit=10 but the same skew shows up there
	// when there are >10 candidates).
	results, err := dp.Search(ctx, queryVec, userID, 5, queryText, nil)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("Search returned no results")
	}

	t.Logf("ranking for %q (limit=5):", queryText)
	for i, r := range results {
		t.Logf("  #%d  type=%s salience=%.2f sim=%.3f text=%q",
			i+1, r.Type, r.Salience, r.Similarity, truncateForLog(r.Text, 80))
	}

	topN := 3
	if len(results) < topN {
		topN = len(results)
	}
	for i := 0; i < topN; i++ {
		if results[i].ID == "target-continuity" {
			return
		}
	}
	var got []string
	for i := 0; i < topN; i++ {
		got = append(got, results[i].ID)
	}
	t.Fatalf("high-salience continuity target should rank in top-%d (it shares all query tokens "+
		"with distractors but has lower cosine); got top-%d=%v", topN, topN, got)
}

// TestDetailSearch_LiteralPrefixQueryWins reproduces Repro A: a query that is
// a literal opening fragment of a high-salience investigation memory must
// land that memory in the top-3.
func TestDetailSearch_LiteralPrefixQueryWins(t *testing.T) {
	store := newTestStore(t)
	embedder := &mockEmbedder{dim: 8}
	dp := NewDetailPath(store, embedder, 0.5, 100, nil)
	ctx := context.Background()
	userID := "test-literal-prefix"

	dim := 8
	queryVec := makeUnitVec(dim, 0.95, 0.31)

	target := &DetailMemory{
		ID: "target-investigation",
		Text: "CORRECTION to prior 2026-04-24 compaction-bug memory: compaction is NOT the bug. " +
			"Real cause is a content:[] response in the session log; retry harness then chains " +
			"additional empty turns under the same trace.",
		Sector:          "semantic",
		Salience:        0.92,
		ImportanceScore: 0.92,
		Type:            "investigation",
	}
	if err := store.InsertDetail(target, makeUnitVec(dim, 0.20, 0.98), userID); err != nil {
		t.Fatalf("insert target: %v", err)
	}

	// Distractors share the query's common vocab (compaction, bug, memory,
	// prior, 2026) so their kw match score is close to the target's, but they
	// have higher raw cosine on the query axis.
	for i := 0; i < 7; i++ {
		dm := &DetailMemory{
			ID: fmt.Sprintf("ckpt-%d", i),
			Text: fmt.Sprintf(
				`{"task":"prior 2026-04-22 compaction harness retry loop probe %d","memory":"compaction-bug trace, prior memory snapshot","status":"done"}`, i),
			Sector:          "procedural",
			Salience:        0.70,
			ImportanceScore: 0.70,
			Type:            "checkpoint",
		}
		emb := makeUnitVec(dim, 0.95-float32(i)*0.005, 0.31+float32(i)*0.005)
		if err := store.InsertDetail(dm, emb, userID); err != nil {
			t.Fatalf("insert ckpt-%d: %v", i, err)
		}
	}

	queryText := "CORRECTION to prior 2026-04-24 compaction-bug memory"
	results, err := dp.Search(ctx, queryVec, userID, 5, queryText, nil)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("Search returned no results")
	}

	t.Logf("ranking for %q (limit=5):", queryText)
	for i, r := range results {
		t.Logf("  #%d  type=%s salience=%.2f sim=%.3f text=%q",
			i+1, r.Type, r.Salience, r.Similarity, truncateForLog(r.Text, 80))
	}

	topN := 3
	if len(results) < topN {
		topN = len(results)
	}
	for i := 0; i < topN; i++ {
		if results[i].ID == "target-investigation" {
			return
		}
	}
	var got []string
	for i := 0; i < topN; i++ {
		got = append(got, results[i].ID)
	}
	t.Fatalf("target-investigation should rank in top-%d for literal-prefix query; got top-%d=%v", topN, topN, got)
}
