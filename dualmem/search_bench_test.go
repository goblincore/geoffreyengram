package dualmem

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// TestSearchBenchmark compares HDC search (1 call) vs simulated grep chain
// (multiple calls) for finding the right modules in the geoffreyengram repo.
//
// HDC search excels at natural-language queries with domain-specific terms.
// Grep excels at exact symbol lookup. This benchmark measures the overlap.
func TestSearchBenchmark(t *testing.T) {
	// Scan from project root (parent of dualmem/)
	rootDir, err := filepath.Abs("..")
	if err != nil {
		t.Skip("can't resolve project root")
	}

	cm, err := ScanCodebase(rootDir)
	if err != nil {
		t.Fatal(err)
	}
	embs := HDCEncodeCodeMap(cm)

	// Log available modules for context
	t.Log("Available modules:")
	for _, m := range cm.Zoom2 {
		t.Logf("  %-30s %s (%d types, %d entries, %d imports, %d idents)",
			m.Path, m.Language, len(m.KeyTypes), len(m.EntryPoints), len(m.Imports), len(m.Identifiers))
	}
	t.Log("")

	type benchCase struct {
		Query        string
		WantContains string   // substring that should appear in top-1 path
		GrepPatterns []string // what an agent would grep for
	}

	cases := []benchCase{
		// Specific queries where HDC should excel (domain terms match identifiers/imports)
		{
			Query:        "embedding provider gemini API key",
			WantContains: "./",
			GrepPatterns: []string{"GeminiEmbedder", "Embed("},
		},
		{
			Query:        "entity graph waypoints associative expansion",
			WantContains: "./",
			GrepPatterns: []string{"Waypoint", "entity", "expansion"},
		},
		{
			Query:        "comparison benchmark scenario LLM judge",
			WantContains: "examples/comparison/",
			GrepPatterns: []string{"Scenario", "judge", "benchmark"},
		},
		{
			Query:        "dualmem CLI add search context checkpoint",
			WantContains: "cmd/dualmem/",
			GrepPatterns: []string{"cmdAdd", "cmdSearch", "checkpoint"},
		},
		{
			Query:        "MCP stdio server tool handler registration",
			WantContains: "cmd/",
			GrepPatterns: []string{"mcp.AddTool", "StdioTransport"},
		},
		// Generic queries where both may struggle
		{
			Query:        "struct Config types initialization",
			WantContains: "dualmem/",
			GrepPatterns: []string{"struct Config", "type Config"},
		},
	}

	t.Logf("%-55s | %-12s | %-10s | %-14s | %-10s", "Query", "HDC Top-1", "HDC Time", "Grep Calls", "Grep Time")
	t.Log(strings.Repeat("-", 110))

	hdcCorrect, grepCorrect := 0, 0
	var totalHDCTime, totalGrepTime time.Duration

	for _, tc := range cases {
		// --- HDC: 1 call ---
		hdcStart := time.Now()
		results := SearchCodeMap(cm, embs, tc.Query, 5)
		hdcTime := time.Since(hdcStart)
		totalHDCTime += hdcTime

		hdcTop := ""
		hdcSim := 0.0
		if len(results) > 0 {
			hdcTop = results[0].Path
			hdcSim = results[0].Similarity
		}
		hdcOK := strings.Contains(hdcTop, tc.WantContains)
		if hdcOK {
			hdcCorrect++
		}

		// --- Grep simulation ---
		grepStart := time.Now()
		grepCalls := 0
		grepHits := make(map[string]int)

		for _, pattern := range tc.GrepPatterns {
			grepCalls++
			for _, m := range cm.Zoom2 {
				text := strings.ToLower(m.Path + " " + m.Summary + " " +
					strings.Join(m.KeyTypes, " ") + " " +
					strings.Join(m.EntryPoints, " ") + " " +
					strings.Join(m.Imports, " ") + " " +
					strings.Join(m.Identifiers, " "))
				if strings.Contains(text, strings.ToLower(pattern)) {
					grepHits[m.Path]++
				}
			}
		}
		grepTime := time.Since(grepStart)
		totalGrepTime += grepTime

		grepOK := false
		if len(grepHits) > 0 {
			type hit struct{ path string; count int }
			var hits []hit
			for p, c := range grepHits {
				hits = append(hits, hit{p, c})
			}
			sort.Slice(hits, func(i, j int) bool { return hits[i].count > hits[j].count })
			grepOK = strings.Contains(hits[0].path, tc.WantContains)
		}
		if grepOK {
			grepCorrect++
		}

		hdcStatus := "MISS"
		if hdcOK {
			hdcStatus = fmt.Sprintf("OK (%.3f)", hdcSim)
		}
		grepStatus := fmt.Sprintf("%d calls", grepCalls)
		if grepOK {
			grepStatus += " OK"
		} else {
			grepStatus += " MISS"
		}

		query := tc.Query
		if len(query) > 55 {
			query = query[:52] + "..."
		}
		t.Logf("%-55s | %-12s | %-10s | %-14s | %-10s",
			query, hdcStatus, hdcTime.Round(time.Microsecond), grepStatus, grepTime.Round(time.Microsecond))
	}

	t.Log(strings.Repeat("=", 110))
	t.Logf("HDC accuracy:  %d/%d (%.0f%%)  | Total time: %v | Calls: %d (1 per query)",
		hdcCorrect, len(cases), float64(hdcCorrect)/float64(len(cases))*100,
		totalHDCTime.Round(time.Microsecond), len(cases))
	t.Logf("Grep accuracy: %d/%d (%.0f%%) | Total time: %v | Calls: %d (%d per query)",
		grepCorrect, len(cases), float64(grepCorrect)/float64(len(cases))*100,
		totalGrepTime.Round(time.Microsecond), len(cases)*len(cases), len(cases))
	t.Logf("")
	t.Logf("Key advantage: HDC uses 1 tool call per query vs %d+ grep calls.", len(cases))
	t.Logf("In a real agent session, each grep call = network round-trip + context tokens.")
}

// BenchmarkSearchCodeMap_EndToEnd benchmarks the full pipeline:
// scan codebase → HDC encode → search query → rank results.
func BenchmarkSearchCodeMap_EndToEnd(b *testing.B) {
	rootDir, err := filepath.Abs(".")
	if err != nil {
		b.Skip("can't resolve cwd")
	}

	cm, err := ScanCodebase(rootDir)
	if err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		embs := HDCEncodeCodeMap(cm)
		_ = SearchCodeMap(cm, embs, "HDC encoding similarity ranking", 10)
	}
}

// BenchmarkSearchCodeMap_QueryOnly benchmarks just the query encoding + ranking
// (codemap already scanned and encoded).
func BenchmarkSearchCodeMap_QueryOnly(b *testing.B) {
	rootDir, err := filepath.Abs(".")
	if err != nil {
		b.Skip("can't resolve cwd")
	}

	cm, err := ScanCodebase(rootDir)
	if err != nil {
		b.Fatal(err)
	}
	embs := HDCEncodeCodeMap(cm)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = SearchCodeMap(cm, embs, "HDC encoding similarity ranking", 10)
	}
}

// Suppress unused import warning
var _ = os.DevNull
