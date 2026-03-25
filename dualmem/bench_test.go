package dualmem

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// benchResult holds metrics for a single scenario × query × mode evaluation.
type benchResult struct {
	Scenario     string
	Query        string
	Mode         string  // "none", "flat", "dualmem"
	Precision    float64 // relevant_surfaced / total_surfaced
	Recall       float64 // relevant_surfaced / total_relevant
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

// seedMemories adds all benchmark memories to the engine and returns a tag→text map.
func seedMemories(t *testing.T, engine *Engine, memories []benchMemory, userID string) map[string]string {
	t.Helper()
	ctx := context.Background()
	tagToText := make(map[string]string)

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
		if m.Tag != "" {
			tagToText[m.Tag] = m.Text
		}
	}
	return tagToText
}

// --- Mode runners ---

// runNone returns an empty result (baseline: no memory at all).
func runNone(query benchQuery) benchResult {
	return benchResult{
		Mode:        "none",
		Query:       query.Query,
		TokenBudget: query.TokenBudget,
		// Everything else is zero
	}
}

// runFlat does raw cosine-similarity search on Detail memories only — no routing, priority, or budget.
func runFlat(t *testing.T, engine *Engine, userID string, query benchQuery, tagToText map[string]string) benchResult {
	t.Helper()
	ctx := context.Background()

	// Embed query
	queryEmb, err := engine.embedder.Embed(ctx, query.Query, "RETRIEVAL_QUERY")
	if err != nil {
		t.Fatalf("embed query: %v", err)
	}

	// Get all details and score by cosine similarity
	details, err := engine.store.GetDetailMemories(userID)
	if err != nil {
		t.Fatalf("GetDetailMemories: %v", err)
	}

	type scored struct {
		DetailMemory
		similarity float64
	}
	var results []scored
	for _, d := range details {
		sim := CosineSimilarity(queryEmb, d.Vector)
		dm := d.DetailMemory
		dm.Similarity = sim
		results = append(results, scored{dm, sim})
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].similarity > results[j].similarity
	})

	// Take top 10 (same as DualSearch default) and assemble flat text
	limit := 10
	if len(results) < limit {
		limit = len(results)
	}

	var contextTexts []string
	tokensUsed := 0
	for i := 0; i < limit; i++ {
		text := results[i].Text
		tokens := estimateTokens(text)
		if tokensUsed+tokens > query.TokenBudget {
			break
		}
		contextTexts = append(contextTexts, text)
		tokensUsed += tokens
	}

	return evaluateResult("flat", query, contextTexts, tokensUsed, tagToText)
}

// runDualMem uses the full AssembleContext pipeline.
func runDualMem(t *testing.T, engine *Engine, userID string, query benchQuery, tagToText map[string]string) benchResult {
	t.Helper()
	ctx := context.Background()

	block, err := engine.AssembleContext(ctx, userID, query.Query, query.TokenBudget)
	if err != nil {
		t.Fatalf("AssembleContext: %v", err)
	}

	// Extract individual memory texts from the block
	var contextTexts []string
	for _, src := range block.Sources {
		if src.Type == "detail" {
			// Look up the text by finding it in the block output
			// We need to match source IDs back to memory texts
		}
	}

	// Since AssembleContext formats text as joined parts, parse it back.
	// Each memory appears as a labeled block separated by double newlines.
	parts := strings.Split(block.Text, "\n\n")
	for _, part := range parts {
		contextTexts = append(contextTexts, part)
	}

	return evaluateResult("dualmem", query, contextTexts, block.TokenCount, tagToText)
}

// evaluateResult computes precision, recall, NDCG, and other metrics.
func evaluateResult(mode string, query benchQuery, contextTexts []string, tokensUsed int, tagToText map[string]string) benchResult {
	res := benchResult{
		Mode:        mode,
		Query:       query.Query,
		TokensUsed:  tokensUsed,
		TokenBudget: query.TokenBudget,
	}

	if query.TokenBudget > 0 {
		res.TokenUtil = float64(tokensUsed) / float64(query.TokenBudget)
	}

	// Build reverse map: memory text → tags (for matching)
	textToTags := make(map[string][]string)
	for tag, text := range tagToText {
		textToTags[text] = append(textToTags[text], tag)
	}

	// Determine which expected/forbidden tags were surfaced
	expectedSet := toSet(query.ExpectedTags)
	forbidSet := toSet(query.ForbidTags)

	surfacedTags := []string{}         // ordered list of tags found in context
	relevantSurfaced := 0              // count of expected tags found
	irrelevantTokens := 0              // tokens spent on non-relevant content
	totalSurfacedWithTags := 0         // total surfaced items that matched any tag

	for _, ct := range contextTexts {
		tags := findMatchingTags(ct, tagToText)
		if len(tags) > 0 {
			totalSurfacedWithTags++
			for _, tag := range tags {
				surfacedTags = append(surfacedTags, tag)
				if expectedSet[tag] {
					relevantSurfaced++
				}
			}
		} else {
			irrelevantTokens += estimateTokens(ct)
		}
	}

	// Precision: of surfaced tagged memories, how many were expected?
	if totalSurfacedWithTags > 0 {
		res.Precision = float64(relevantSurfaced) / float64(totalSurfacedWithTags)
	}

	// Recall: of expected tags, how many were surfaced?
	if len(query.ExpectedTags) > 0 {
		res.Recall = float64(relevantSurfaced) / float64(len(query.ExpectedTags))
	}

	// Budget waste
	if tokensUsed > 0 {
		res.BudgetWaste = float64(irrelevantTokens) / float64(tokensUsed)
	}

	// NDCG: measures ordering quality
	res.NDCG = computeNDCG(surfacedTags, query.ExpectedTags)

	// Warning first?
	if len(surfacedTags) > 0 {
		// Check if first surfaced tag is a warning-type memory
		for _, tag := range surfacedTags {
			text := tagToText[tag]
			if text != "" {
				res.WarningFirst = isWarningText(text, tagToText)
				break
			}
		}
	}

	// Order correct?
	if len(query.OrderedTags) > 0 {
		res.OrderCorrect = checkOrder(surfacedTags, query.OrderedTags)
	} else {
		res.OrderCorrect = true // no order constraint
	}

	// Check forbidden tags
	for _, tag := range surfacedTags {
		if forbidSet[tag] {
			// Penalize precision
			res.Precision *= 0.5
		}
	}

	return res
}

// --- Metrics helpers ---

// computeNDCG computes Normalized Discounted Cumulative Gain.
// relevantSet defines which tags are relevant (binary relevance).
func computeNDCG(surfacedTags []string, expectedTags []string) float64 {
	if len(expectedTags) == 0 || len(surfacedTags) == 0 {
		return 0
	}

	expectedSet := toSet(expectedTags)

	// DCG: sum of 1/log2(i+2) for each relevant result at position i
	dcg := 0.0
	for i, tag := range surfacedTags {
		if expectedSet[tag] {
			dcg += 1.0 / math.Log2(float64(i+2))
		}
	}

	// Ideal DCG: all relevant results at top positions
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

func findMatchingTags(contextText string, tagToText map[string]string) []string {
	var matches []string
	for tag, text := range tagToText {
		if strings.Contains(contextText, text) {
			matches = append(matches, tag)
		}
	}
	return matches
}

func isWarningText(text string, tagToText map[string]string) bool {
	// Check if the tag name contains "warning"
	for tag, t := range tagToText {
		if t == text && strings.Contains(tag, "warning") {
			return true
		}
	}
	return false
}

func checkOrder(surfaced []string, expected []string) bool {
	// Check that expected tags appear in the given order within surfaced
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
			tagToText := seedMemories(t, engine, scenario.Memories, userID)

			for _, query := range scenario.Queries {
				// Mode 1: No memory
				noneResult := runNone(query)
				noneResult.Scenario = scenario.Name

				// Mode 2: Flat cosine search
				flatResult := runFlat(t, engine, userID, query, tagToText)
				flatResult.Scenario = scenario.Name

				// Mode 3: Full DualMem
				dualResult := runDualMem(t, engine, userID, query, tagToText)
				dualResult.Scenario = scenario.Name

				allResults = append(allResults, noneResult, flatResult, dualResult)

				// Log per-query comparison
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

	// Print aggregate summary
	t.Log("\n" + formatSummaryTable(allResults))

	// Regression guards: DualMem should beat flat on average
	dualAvgRecall := avgMetric(allResults, "dualmem", func(r benchResult) float64 { return r.Recall })
	flatAvgRecall := avgMetric(allResults, "flat", func(r benchResult) float64 { return r.Recall })

	if dualAvgRecall < flatAvgRecall {
		t.Errorf("DualMem avg recall (%.2f) should be >= flat avg recall (%.2f)", dualAvgRecall, flatAvgRecall)
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

	// Averages
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
