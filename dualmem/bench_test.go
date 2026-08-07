package dualmem

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// benchResult holds metrics for a single scenario × query × mode evaluation.
type benchResult struct {
	Scenario     string
	Query        string
	Mode         string  // "none", "flat", "dualmem"
	Precision    float64 // relevant_detail_sources / total_detail_sources
	Recall       float64 // distinct_expected_found / total_expected
	NDCG         float64 // normalized discounted cumulative gain
	TokensUsed   int
	TokenBudget  int
	TokenUtil    float64 // tokens_used / token_budget
	BudgetWaste  float64 // irrelevant_tokens / tokens_used
	WarningFirst bool    // was the first detail memory a warning?
	OrderCorrect bool    // did ordered tags match?
}

// newBenchEngine creates a test engine with a sectorClassifier that respects sector hints.
func newBenchEngine(t *testing.T) *Engine {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "bench.db")
	engine, err := New(Config{
		SQLitePath:        dbPath,
		EmbeddingProvider: &mockEmbedder{dim: 768},
		Classifier:        &sectorPassthrough{},
		EntityExtractor:   &mockExtractor{},
		MaxDetailPerUser:  200, // generous for bench scenarios
		ImportanceTheta:   0.65,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { engine.Close() })
	return engine
}

// sectorPassthrough returns the sector hint as-is (bench scenarios provide explicit sectors).
type sectorPassthrough struct{}

func (s *sectorPassthrough) Classify(content string) string { return "decision" }

// idTagMap maps detail memory IDs to benchmark tags.
type idTagMap struct {
	idToTag   map[string]string // detail ID → tag
	tagToID   map[string]string // tag → detail ID
	tagToText map[string]string // tag → original text
}

// seedMemories adds all benchmark memories to the engine and builds ID↔tag mappings.
func seedMemories(t *testing.T, engine *Engine, memories []benchMemory, userID string) *idTagMap {
	t.Helper()
	ctx := context.Background()

	for _, m := range memories {
		salience := m.Salience
		if salience == 0 {
			salience = 0.5
		}
		err := engine.AddWithOptions(ctx, MemoryInput{
			UserMessage: m.Text,
			SectorHint:  m.Sector,
			Salience:    salience,
			Type:        m.Type,
			Files:       m.Files,
		}, userID)
		if err != nil {
			t.Fatalf("AddWithOptions(%s): %v", m.Tag, err)
		}
	}

	// Build ID→tag map by retrieving stored details and matching text
	details, err := engine.store.GetDetailMemories(userID)
	if err != nil {
		t.Fatalf("GetDetailMemories: %v", err)
	}

	m := &idTagMap{
		idToTag:   make(map[string]string),
		tagToID:   make(map[string]string),
		tagToText: make(map[string]string),
	}
	for _, d := range details {
		for _, bm := range memories {
			if bm.Tag != "" && d.Text == bm.Text {
				m.idToTag[d.ID] = bm.Tag
				m.tagToID[bm.Tag] = d.ID
				m.tagToText[bm.Tag] = bm.Text
			}
		}
	}
	return m
}

// --- Mode runners ---

// runNone returns an empty result (baseline: no memory at all).
func runNone(query benchQuery) benchResult {
	return benchResult{
		Mode:        "none",
		Query:       query.Query,
		TokenBudget: query.TokenBudget,
	}
}

// runFlat does raw cosine-similarity search on Detail memories only — no routing, priority, or budget.
func runFlat(t *testing.T, engine *Engine, userID string, query benchQuery, m *idTagMap) benchResult {
	t.Helper()
	ctx := context.Background()

	queryEmb, err := engine.embedder.Embed(ctx, query.Query, "RETRIEVAL_QUERY")
	if err != nil {
		t.Fatalf("embed query: %v", err)
	}

	details, err := engine.store.GetDetailMemories(userID)
	if err != nil {
		t.Fatalf("GetDetailMemories: %v", err)
	}

	type scored struct {
		detailWithVector
		similarity float64
	}
	var results []scored
	for _, d := range details {
		sim := CosineSimilarity(queryEmb, d.Vector)
		results = append(results, scored{d, sim})
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].similarity > results[j].similarity
	})

	// Take top 10 with budget constraint
	limit := 10
	if len(results) < limit {
		limit = len(results)
	}

	var surfacedIDs []string
	tokensUsed := 0
	for i := 0; i < limit; i++ {
		tokens := estimateTokens(results[i].Text)
		if tokensUsed+tokens > query.TokenBudget {
			break
		}
		surfacedIDs = append(surfacedIDs, results[i].ID)
		tokensUsed += tokens
	}

	return evaluateByIDs("flat", query, surfacedIDs, tokensUsed, m)
}

// runDualMem uses the full AssembleContext pipeline.
func runDualMem(t *testing.T, engine *Engine, userID string, query benchQuery, m *idTagMap) benchResult {
	t.Helper()
	ctx := context.Background()

	block, err := engine.AssembleContext(ctx, userID, query.Query, query.TokenBudget)
	if err != nil {
		t.Fatalf("AssembleContext: %v", err)
	}

	// Extract detail source IDs from ContextBlock.Sources
	var surfacedIDs []string
	for _, src := range block.Sources {
		if src.Type == "detail" {
			surfacedIDs = append(surfacedIDs, src.ID)
		}
	}

	return evaluateByIDs("dualmem", query, surfacedIDs, block.TokenCount, m)
}

// evaluateByIDs computes precision, recall, NDCG using ID-based matching.
// Only counts detail memories — structural sections (codemap, diff, profile) are excluded.
func evaluateByIDs(mode string, query benchQuery, surfacedIDs []string, tokensUsed int, m *idTagMap) benchResult {
	res := benchResult{
		Mode:        mode,
		Query:       query.Query,
		TokensUsed:  tokensUsed,
		TokenBudget: query.TokenBudget,
	}

	if query.TokenBudget > 0 {
		res.TokenUtil = float64(tokensUsed) / float64(query.TokenBudget)
	}

	expectedSet := toSet(query.ExpectedTags)
	forbidSet := toSet(query.ForbidTags)

	// Map surfaced IDs to tags
	var surfacedTags []string
	relevantCount := 0
	totalDetailSurfaced := 0

	for _, id := range surfacedIDs {
		tag, ok := m.idToTag[id]
		if !ok {
			// Detail memory exists but has no tag (e.g., "filler" memories)
			totalDetailSurfaced++
			continue
		}
		totalDetailSurfaced++
		surfacedTags = append(surfacedTags, tag)
		if expectedSet[tag] {
			relevantCount++
		}
	}

	// Precision: of detail memories surfaced, how many were expected?
	if totalDetailSurfaced > 0 {
		res.Precision = float64(relevantCount) / float64(totalDetailSurfaced)
	}

	// Recall: of expected tags, how many were surfaced?
	if len(query.ExpectedTags) > 0 {
		res.Recall = float64(relevantCount) / float64(len(query.ExpectedTags))
	}

	// Budget waste: estimate tokens spent on non-expected memories
	if tokensUsed > 0 {
		irrelevantCount := totalDetailSurfaced - relevantCount
		if totalDetailSurfaced > 0 {
			res.BudgetWaste = float64(irrelevantCount) / float64(totalDetailSurfaced)
		}
	}

	// NDCG: measures ordering quality
	res.NDCG = computeNDCG(surfacedTags, query.ExpectedTags)

	// Warning first?
	if len(surfacedTags) > 0 {
		res.WarningFirst = strings.Contains(surfacedTags[0], "warning")
	}

	// Order correct?
	if len(query.OrderedTags) > 0 {
		res.OrderCorrect = checkOrder(surfacedTags, query.OrderedTags)
	} else {
		res.OrderCorrect = true
	}

	// Penalize for forbidden tags
	for _, tag := range surfacedTags {
		if forbidSet[tag] {
			res.Precision *= 0.5
		}
	}

	return res
}

// --- Metrics helpers ---

// computeNDCG computes Normalized Discounted Cumulative Gain.
func computeNDCG(surfacedTags []string, expectedTags []string) float64 {
	if len(expectedTags) == 0 || len(surfacedTags) == 0 {
		return 0
	}

	expectedSet := toSet(expectedTags)

	dcg := 0.0
	for i, tag := range surfacedTags {
		if expectedSet[tag] {
			dcg += 1.0 / math.Log2(float64(i+2))
		}
	}

	idcg := 0.0
	n := len(expectedTags)
	if n > len(surfacedTags) {
		n = len(surfacedTags)
	}
	for i := 0; i < n; i++ {
		idcg += 1.0 / math.Log2(float64(i+2))
	}

	if idcg == 0 {
		return 0
	}
	return dcg / idcg
}

func checkOrder(surfaced []string, expected []string) bool {
	idx := 0
	for _, tag := range surfaced {
		if idx < len(expected) && tag == expected[idx] {
			idx++
		}
	}
	return idx == len(expected)
}

func toSet(tags []string) map[string]bool {
	s := make(map[string]bool, len(tags))
	for _, t := range tags {
		s[t] = true
	}
	return s
}

// --- Main benchmark test ---

func TestBench(t *testing.T) {
	scenarios := benchScenarios()
	var allResults []benchResult

	for _, scenario := range scenarios {
		t.Run(scenario.Name, func(t *testing.T) {
			engine := newBenchEngine(t)
			userID := "bench-user"
			m := seedMemories(t, engine, scenario.Memories, userID)

			for _, query := range scenario.Queries {
				noneResult := runNone(query)
				noneResult.Scenario = scenario.Name

				flatResult := runFlat(t, engine, userID, query, m)
				flatResult.Scenario = scenario.Name

				dualResult := runDualMem(t, engine, userID, query, m)
				dualResult.Scenario = scenario.Name

				allResults = append(allResults, noneResult, flatResult, dualResult)

				t.Logf("\n  Query: %q", query.Query)
				t.Logf("  %-8s | Prec: %.2f | Recall: %.2f | NDCG: %.2f | Tokens: %4d/%d | Waste: %.2f | Order: %v",
					"none", noneResult.Precision, noneResult.Recall, noneResult.NDCG, noneResult.TokensUsed, noneResult.TokenBudget, noneResult.BudgetWaste, noneResult.OrderCorrect)
				t.Logf("  %-8s | Prec: %.2f | Recall: %.2f | NDCG: %.2f | Tokens: %4d/%d | Waste: %.2f | Order: %v",
					"flat", flatResult.Precision, flatResult.Recall, flatResult.NDCG, flatResult.TokensUsed, flatResult.TokenBudget, flatResult.BudgetWaste, flatResult.OrderCorrect)
				t.Logf("  %-8s | Prec: %.2f | Recall: %.2f | NDCG: %.2f | Tokens: %4d/%d | Waste: %.2f | Order: %v",
					"dualmem", dualResult.Precision, dualResult.Recall, dualResult.NDCG, dualResult.TokensUsed, dualResult.TokenBudget, dualResult.BudgetWaste, dualResult.OrderCorrect)
			}
		})
	}

	t.Log("\n" + formatSummaryTable(allResults))

	// Regression guards
	dualAvgRecall := avgMetric(allResults, "dualmem", func(r benchResult) float64 { return r.Recall })
	flatAvgRecall := avgMetric(allResults, "flat", func(r benchResult) float64 { return r.Recall })

	if dualAvgRecall < flatAvgRecall {
		t.Errorf("DualMem avg recall (%.2f) should be >= flat avg recall (%.2f)", dualAvgRecall, flatAvgRecall)
	}
}

// TestMinSimilarity verifies that the MinSimilarity filter actually removes low-similarity results.
func TestMinSimilarity(t *testing.T) {
	engine := newBenchEngine(t)
	ctx := context.Background()
	userID := "minsim-user"

	// Seed: 1 relevant + 5 unrelated
	memories := []benchMemory{
		{Text: "JWT authentication uses RS256 signing with key rotation", Type: "decision", Sector: "decision", Salience: 0.8},
		{Text: "Docker compose config for local PostgreSQL development", Sector: "map", Salience: 0.4},
		{Text: "Frontend uses React Router v6 with lazy loading", Sector: "map", Salience: 0.4},
		{Text: "CSS theme variables in globals.css root scope", Sector: "map", Salience: 0.3},
		{Text: "Kubernetes manifests in deploy directory with kustomize", Sector: "map", Salience: 0.3},
		{Text: "README badges for code coverage and Go version", Sector: "map", Salience: 0.2},
	}
	for _, m := range memories {
		salience := m.Salience
		if salience == 0 {
			salience = 0.5
		}
		engine.AddWithOptions(ctx, MemoryInput{
			UserMessage: m.Text,
			SectorHint:  m.Sector,
			Salience:    salience,
			Type:        m.Type,
		}, userID)
	}

	// Search without threshold
	noThreshold, err := engine.DualSearch(ctx, userID, "authentication JWT", SearchOpts{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}

	// Search with threshold
	withThreshold, err := engine.DualSearch(ctx, userID, "authentication JWT", SearchOpts{Limit: 10, MinSimilarity: 0.01})
	if err != nil {
		t.Fatal(err)
	}

	t.Logf("Without threshold: %d results", len(noThreshold.DetailMemories))
	for _, dm := range noThreshold.DetailMemories {
		t.Logf("  sim=%.4f %s", dm.Similarity, dm.Text[:min(60, len(dm.Text))])
	}

	t.Logf("With threshold (0.01): %d results", len(withThreshold.DetailMemories))
	for _, dm := range withThreshold.DetailMemories {
		t.Logf("  sim=%.4f %s", dm.Similarity, dm.Text[:min(60, len(dm.Text))])
	}

	// With threshold should have fewer or equal results
	if len(withThreshold.DetailMemories) > len(noThreshold.DetailMemories) {
		t.Errorf("Threshold should reduce results: got %d > %d",
			len(withThreshold.DetailMemories), len(noThreshold.DetailMemories))
	}

	// All results with threshold should meet the minimum
	for _, dm := range withThreshold.DetailMemories {
		if dm.Similarity < 0.01 {
			t.Errorf("Result below threshold: sim=%.4f text=%s", dm.Similarity, dm.Text[:40])
		}
	}
}

// --- Aggregate helpers ---

func avgMetric(results []benchResult, mode string, metric func(benchResult) float64) float64 {
	var sum float64
	var count int
	for _, r := range results {
		if r.Mode == mode {
			sum += metric(r)
			count++
		}
	}
	if count == 0 {
		return 0
	}
	return sum / float64(count)
}

func formatSummaryTable(results []benchResult) string {
	var sb strings.Builder
	sb.WriteString("=== DualMem Benchmark Summary ===\n\n")
	sb.WriteString(fmt.Sprintf("%-25s | %-8s | %5s | %6s | %5s | %6s | %6s\n",
		"Scenario", "Mode", "Prec", "Recall", "NDCG", "Tokens", "Waste"))
	sb.WriteString(strings.Repeat("-", 85) + "\n")

	for _, r := range results {
		sb.WriteString(fmt.Sprintf("%-25s | %-8s | %5.2f | %6.2f | %5.2f | %6d | %5.2f\n",
			truncate(r.Scenario, 25), r.Mode, r.Precision, r.Recall, r.NDCG, r.TokensUsed, r.BudgetWaste))
	}

	sb.WriteString(strings.Repeat("-", 85) + "\n")
	for _, mode := range []string{"none", "flat", "dualmem"} {
		avgPrec := avgMetric(results, mode, func(r benchResult) float64 { return r.Precision })
		avgRec := avgMetric(results, mode, func(r benchResult) float64 { return r.Recall })
		avgNDCG := avgMetric(results, mode, func(r benchResult) float64 { return r.NDCG })
		avgWaste := avgMetric(results, mode, func(r benchResult) float64 { return r.BudgetWaste })
		avgTokens := avgMetric(results, mode, func(r benchResult) float64 { return float64(r.TokensUsed) })
		sb.WriteString(fmt.Sprintf("%-25s | %-8s | %5.2f | %6.2f | %5.2f | %6.0f | %5.2f\n",
			"AVERAGE", mode, avgPrec, avgRec, avgNDCG, avgTokens, avgWaste))
	}

	return sb.String()
}

func truncate(s string, max int) string {
	if len(s) > max {
		return s[:max-3] + "..."
	}
	return s
}

// TestBenchSupersede validates the supersede-by-type mechanism end-to-end.
// It seeds memories sequentially (old then new versions), checks that each
// supersede-eligible type has at most 1 detail memory, that sketch preserves
// the demoted versions, and that retrieval returns only the current version.
func TestBenchSupersede(t *testing.T) {
	engine := newBenchEngine(t)
	userID := "bench-supersede"
	ctx := context.Background()

	scenario := scenarioSupersede()

	// Add memories sequentially (order matters for supersession)
	for i, m := range scenario.Memories {
		salience := m.Salience
		if salience == 0 {
			salience = 0.5
		}
		err := engine.AddWithOptions(ctx, MemoryInput{
			UserMessage: m.Text,
			SectorHint:  m.Sector,
			Salience:    salience,
			Type:        m.Type,
			Files:       m.Files,
		}, userID)
		if err != nil {
			t.Fatalf("AddWithOptions step %d (%s): %v", i+1, m.Tag, err)
		}
	}

	// Check memory counts per type — each supersede-eligible type should have at most 1
	for _, memType := range []string{"decision", "continuity", "warning", "architecture"} {
		memories, _ := engine.store.GetDetailsByType(userID, memType)
		t.Logf("Type %q: %d detail memories", memType, len(memories))
		if len(memories) > 1 {
			texts := make([]string, len(memories))
			for i, m := range memories {
				texts[i] = m.Text[:min(60, len(m.Text))]
			}
			t.Errorf("expected at most 1 %s memory after supersede, got %d: %v", memType, len(memories), texts)
		}
	}

	// Check sketch preservation — old memories should have been demoted, not lost
	sketchCount, _ := engine.store.GetSketchRawCount(userID)
	t.Logf("Sketch raw count: %d (expect >= 4 from superseded memories)", sketchCount)
	if sketchCount < 4 {
		t.Errorf("expected at least 4 sketch entries from superseded memories, got %d", sketchCount)
	}

	// Run retrieval quality checks via normal bench infrastructure
	m := &idTagMap{
		idToTag:   make(map[string]string),
		tagToID:   make(map[string]string),
		tagToText: make(map[string]string),
	}
	details, _ := engine.store.GetDetailMemories(userID)
	for _, d := range details {
		for _, bm := range scenario.Memories {
			if d.Text == bm.Text && bm.Tag != "" {
				m.idToTag[d.ID] = bm.Tag
				m.tagToID[bm.Tag] = d.ID
				m.tagToText[bm.Tag] = bm.Text
			}
		}
	}

	for _, query := range scenario.Queries {
		// Get assembled context directly to check for forbidden tags
		block, err := engine.AssembleContext(ctx, userID, query.Query, query.TokenBudget)
		if err != nil {
			t.Fatalf("AssembleContext: %v", err)
		}

		// Extract surfaced detail IDs and map to tags
		var surfacedTags []string
		for _, src := range block.Sources {
			if src.Type == "detail" {
				if tag, ok := m.idToTag[src.ID]; ok {
					surfacedTags = append(surfacedTags, tag)
				}
			}
		}

		result := runDualMem(t, engine, userID, query, m)
		t.Logf("\n  Query: %q", query.Query)
		t.Logf("  Prec: %.2f | Recall: %.2f | NDCG: %.2f | Tokens: %d/%d",
			result.Precision, result.Recall, result.NDCG, result.TokensUsed, result.TokenBudget)
		t.Logf("  Surfaced tags: %v", surfacedTags)

		// Check that forbidden tags (old superseded memories) are NOT in results
		for _, tag := range query.ForbidTags {
			for _, surfaced := range surfacedTags {
				if surfaced == tag {
					t.Errorf("forbidden tag %q appeared in results for query %q", tag, query.Query)
				}
			}
		}
	}
}

// --- Live benchmark with real Gemini Embedding 2 ---

// newLiveBenchEngine creates a test engine with real Gemini Embedding 2.
// Requires DUALMEM_LIVE_TESTS=1 and GEMINI_API_KEY.
func newLiveBenchEngine(t *testing.T) *Engine {
	t.Helper()
	if os.Getenv("DUALMEM_LIVE_TESTS") != "1" {
		t.Skip("set DUALMEM_LIVE_TESTS=1 to run provider-backed tests")
	}
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		t.Skip("GEMINI_API_KEY not set — skipping live benchmark")
	}

	dbPath := filepath.Join(t.TempDir(), "bench-live.db")
	engine, err := New(Config{
		SQLitePath:        dbPath,
		EmbeddingProvider: NewGeminiEmbedder(apiKey, 768, WithGeminiModel("gemini-embedding-2-preview")),
		Classifier:        &sectorPassthrough{},
		EntityExtractor:   &mockExtractor{},
		MaxDetailPerUser:  200,
		ImportanceTheta:   0.65,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { engine.Close() })
	return engine
}

// TestBenchLive runs all benchmark scenarios with real Gemini Embedding 2 embeddings.
// This test requires DUALMEM_LIVE_TESTS=1 and GEMINI_API_KEY and makes real API calls.
// Run with: DUALMEM_LIVE_TESTS=1 go test ./dualmem/ -v -run TestBenchLive -count=1
func TestBenchLive(t *testing.T) {
	if os.Getenv("DUALMEM_LIVE_TESTS") != "1" {
		t.Skip("set DUALMEM_LIVE_TESTS=1 to run provider-backed tests")
	}
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		t.Skip("GEMINI_API_KEY not set — skipping live benchmark")
	}

	scenarios := benchScenarios()
	var allResults []benchResult

	for _, scenario := range scenarios {
		t.Run(scenario.Name, func(t *testing.T) {
			// Fresh engine per scenario (isolated DB)
			engine := newLiveBenchEngine(t)
			userID := "bench-live"

			// Seed with rate limiting to avoid API throttling
			m := seedMemoriesLive(t, engine, scenario.Memories, userID)

			for _, query := range scenario.Queries {
				// Small delay between queries
				time.Sleep(200 * time.Millisecond)

				noneResult := runNone(query)
				noneResult.Scenario = scenario.Name

				flatResult := runFlat(t, engine, userID, query, m)
				flatResult.Scenario = scenario.Name

				dualResult := runDualMem(t, engine, userID, query, m)
				dualResult.Scenario = scenario.Name

				allResults = append(allResults, noneResult, flatResult, dualResult)

				t.Logf("\n  Query: %q", query.Query)
				logResult(t, "none", noneResult)
				logResult(t, "flat", flatResult)
				logResult(t, "dualmem", dualResult)

				// Also log similarity scores for debugging
				logSimilarities(t, engine, userID, query.Query, m)
			}
		})
	}

	t.Log("\n" + formatSummaryTable(allResults))

	// Log improvement over flat
	dualPrec := avgMetric(allResults, "dualmem", func(r benchResult) float64 { return r.Precision })
	flatPrec := avgMetric(allResults, "flat", func(r benchResult) float64 { return r.Precision })
	dualNDCG := avgMetric(allResults, "dualmem", func(r benchResult) float64 { return r.NDCG })
	flatNDCG := avgMetric(allResults, "flat", func(r benchResult) float64 { return r.NDCG })

	t.Logf("\n=== DualMem vs Flat (Live Embeddings) ===")
	t.Logf("Precision: dualmem=%.3f flat=%.3f (delta=%.3f)", dualPrec, flatPrec, dualPrec-flatPrec)
	t.Logf("NDCG:      dualmem=%.3f flat=%.3f (delta=%.3f)", dualNDCG, flatNDCG, dualNDCG-flatNDCG)
}

// TestBenchLiveMinSimilarity tests the effect of MinSimilarity threshold with real embeddings.
func TestBenchLiveMinSimilarity(t *testing.T) {
	if os.Getenv("DUALMEM_LIVE_TESTS") != "1" {
		t.Skip("set DUALMEM_LIVE_TESTS=1 to run provider-backed tests")
	}
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		t.Skip("GEMINI_API_KEY not set — skipping live MinSimilarity test")
	}

	engine := newLiveBenchEngine(t)
	userID := "minsim-live"
	ctx := context.Background()

	// Use the Noise Resistance scenario — 2 relevant + 25 noisy
	scenario := scenarioNoiseResistance()
	_ = seedMemoriesLive(t, engine, scenario.Memories, userID)

	query := "WebSocket connection handling"

	// Test different thresholds
	thresholds := []float64{0, 0.1, 0.2, 0.3, 0.4, 0.5}
	for _, thresh := range thresholds {
		time.Sleep(200 * time.Millisecond)
		results, err := engine.DualSearch(ctx, userID, query, SearchOpts{
			Limit:         10,
			MinSimilarity: thresh,
		})
		if err != nil {
			t.Fatalf("DualSearch(thresh=%.1f): %v", thresh, err)
		}

		t.Logf("MinSimilarity=%.1f → %d results:", thresh, len(results.DetailMemories))
		for _, dm := range results.DetailMemories {
			relevant := strings.Contains(dm.Text, "WebSocket")
			marker := " "
			if relevant {
				marker = "✓"
			}
			t.Logf("  %s sim=%.4f %s", marker, dm.Similarity, dm.Text[:min(70, len(dm.Text))])
		}
	}
}

// seedMemoriesLive is like seedMemories but adds a small delay between inserts
// to avoid hitting Gemini API rate limits.
func seedMemoriesLive(t *testing.T, engine *Engine, memories []benchMemory, userID string) *idTagMap {
	t.Helper()
	ctx := context.Background()

	for i, m := range memories {
		salience := m.Salience
		if salience == 0 {
			salience = 0.5
		}
		err := engine.AddWithOptions(ctx, MemoryInput{
			UserMessage: m.Text,
			SectorHint:  m.Sector,
			Salience:    salience,
			Type:        m.Type,
			Files:       m.Files,
		}, userID)
		if err != nil {
			t.Fatalf("AddWithOptions(%s): %v", m.Tag, err)
		}

		// Rate limit: ~10 req/s should be fine for Gemini free tier
		if i > 0 && i%5 == 0 {
			time.Sleep(500 * time.Millisecond)
		}
	}

	// Build ID→tag map
	details, err := engine.store.GetDetailMemories(userID)
	if err != nil {
		t.Fatalf("GetDetailMemories: %v", err)
	}

	m := &idTagMap{
		idToTag:   make(map[string]string),
		tagToID:   make(map[string]string),
		tagToText: make(map[string]string),
	}
	for _, d := range details {
		for _, bm := range memories {
			if bm.Tag != "" && d.Text == bm.Text {
				m.idToTag[d.ID] = bm.Tag
				m.tagToID[bm.Tag] = d.ID
				m.tagToText[bm.Tag] = bm.Text
			}
		}
	}
	return m
}

// logResult logs a benchmark result line.
func logResult(t *testing.T, mode string, r benchResult) {
	t.Helper()
	t.Logf("  %-8s | Prec: %.2f | Recall: %.2f | NDCG: %.2f | Tokens: %4d/%d | Waste: %.2f | Order: %v",
		mode, r.Precision, r.Recall, r.NDCG, r.TokensUsed, r.TokenBudget, r.BudgetWaste, r.OrderCorrect)
}

// logSimilarities logs cosine similarity scores for all detail memories against the query.
// Helps debug what real embeddings look like.
func logSimilarities(t *testing.T, engine *Engine, userID, query string, m *idTagMap) {
	t.Helper()
	ctx := context.Background()

	results, err := engine.DualSearch(ctx, userID, query, SearchOpts{Limit: 20})
	if err != nil {
		t.Logf("  [similarity debug] DualSearch error: %v", err)
		return
	}

	t.Logf("  Similarity scores (query: %q):", query)
	for _, dm := range results.DetailMemories {
		tag := m.idToTag[dm.ID]
		if tag == "" {
			tag = "(untagged)"
		}
		t.Logf("    sim=%.4f [%-20s] %s", dm.Similarity, tag, dm.Text[:min(60, len(dm.Text))])
	}
}
