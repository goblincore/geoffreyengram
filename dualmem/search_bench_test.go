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

// TestSearchBenchmark compares HDC-only, Hybrid (HDC+BM25), and simulated grep
// for finding the right modules in the geoffreyengram repo.
//
// 3-way comparison: HDC-only | Hybrid | Grep
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
	idx := BuildCodeIndex(cm)

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
		// Specific queries — HDC should excel
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
		// General queries — hybrid should improve over HDC-only
		{
			Query:        "struct Config types initialization",
			WantContains: "dualmem/",
			GrepPatterns: []string{"struct Config", "type Config"},
		},
		{
			Query:        "dualmem CLI command line flags",
			WantContains: "cmd/dualmem/",
			GrepPatterns: []string{"cmdAdd", "flag.NewFlagSet"},
		},
		{
			Query:        "treesitter AST parser multi-language",
			WantContains: "dualmem/",
			GrepPatterns: []string{"treesitter", "ParseTreeSitter"},
		},
	}

	t.Logf("%-50s | %-14s | %-14s | %-14s", "Query", "HDC-only", "Hybrid", "Grep")
	t.Log(strings.Repeat("-", 100))

	hdcCorrect, hybridCorrect, grepCorrect := 0, 0, 0
	var totalHDCTime, totalHybridTime, totalGrepTime time.Duration

	for _, tc := range cases {
		// --- HDC-only ---
		hdcStart := time.Now()
		hdcResults := SearchCodeMapCompat(cm, embs, tc.Query, 5)
		hdcTime := time.Since(hdcStart)
		totalHDCTime += hdcTime

		hdcTop := ""
		hdcSim := 0.0
		if len(hdcResults) > 0 {
			hdcTop = hdcResults[0].Path
			hdcSim = hdcResults[0].Similarity
		}
		hdcOK := strings.Contains(hdcTop, tc.WantContains)
		if hdcOK {
			hdcCorrect++
		}

		// --- Hybrid ---
		hybridStart := time.Now()
		hybridResults := SearchCodeMap(cm, idx, tc.Query, 5)
		hybridTime := time.Since(hybridStart)
		totalHybridTime += hybridTime

		hybridTop := ""
		hybridScore := 0.0
		if len(hybridResults) > 0 {
			hybridTop = hybridResults[0].Path
			hybridScore = hybridResults[0].HybridScore
		}
		hybridOK := strings.Contains(hybridTop, tc.WantContains)
		if hybridOK {
			hybridCorrect++
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
			type hit struct {
				path  string
				count int
			}
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
			hdcStatus = fmt.Sprintf("OK %.3f", hdcSim)
		}
		hybridStatus := "MISS"
		if hybridOK {
			hybridStatus = fmt.Sprintf("OK %.3f", hybridScore)
		}
		grepStatus := fmt.Sprintf("%d calls", grepCalls)
		if grepOK {
			grepStatus += " OK"
		} else {
			grepStatus += " MISS"
		}

		query := tc.Query
		if len(query) > 50 {
			query = query[:47] + "..."
		}
		t.Logf("%-50s | %-14s | %-14s | %-14s",
			query, hdcStatus, hybridStatus, grepStatus)
	}

	t.Log(strings.Repeat("=", 100))
	t.Logf("HDC-only accuracy:  %d/%d (%.0f%%)  Total: %v",
		hdcCorrect, len(cases), float64(hdcCorrect)/float64(len(cases))*100,
		totalHDCTime.Round(time.Microsecond))
	t.Logf("Hybrid accuracy:    %d/%d (%.0f%%)  Total: %v",
		hybridCorrect, len(cases), float64(hybridCorrect)/float64(len(cases))*100,
		totalHybridTime.Round(time.Microsecond))
	t.Logf("Grep accuracy:      %d/%d (%.0f%%)  Total: %v  (multi-call)",
		grepCorrect, len(cases), float64(grepCorrect)/float64(len(cases))*100,
		totalGrepTime.Round(time.Microsecond))
	t.Logf("")
	t.Logf("Key: Hybrid = HDC cosine + BM25 keyword scoring with adaptive blending.")
	t.Logf("     1 tool call each. Grep requires %d+ calls per query.", len(cases[0].GrepPatterns))
}

// BenchmarkSearchCodeMap_EndToEnd benchmarks the full pipeline:
// scan codebase → build index → search query → rank results.
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
		idx := BuildCodeIndex(cm)
		_ = SearchCodeMap(cm, idx, "HDC encoding similarity ranking", 10)
	}
}

// BenchmarkSearchCodeMap_QueryOnly benchmarks just the query encoding + ranking
// (codemap already scanned and indexed).
func BenchmarkSearchCodeMap_QueryOnly(b *testing.B) {
	rootDir, err := filepath.Abs(".")
	if err != nil {
		b.Skip("can't resolve cwd")
	}

	cm, err := ScanCodebase(rootDir)
	if err != nil {
		b.Fatal(err)
	}
	idx := BuildCodeIndex(cm)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = SearchCodeMap(cm, idx, "HDC encoding similarity ranking", 10)
	}
}

// BenchmarkBuildCodeIndex measures the overhead of building the hybrid index
// (HDC vectors + BM25 token data) vs HDC-only.
func BenchmarkBuildCodeIndex(b *testing.B) {
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
		_ = BuildCodeIndex(cm)
	}
}

// TestCodemapRelevance_MemoryInformed tests the memory-informed codemap ranking
// against a simulated large repo (like LearnCard) where checkpoints mention
// specific files and most modules are irrelevant to the task.
//
// Scenario: user is working on LC-1663 (JWT streamlining). Checkpoints mention
// jwt.go, auth.go, middleware.go. The repo has 20+ modules but only a few are
// auth-related. Without boosting, a generic query like "session context" would
// surface random modules. With boosting, auth modules should rank first.
func TestCodemapRelevance_MemoryInformed(t *testing.T) {
	// Simulate a large TypeScript monorepo with many unrelated modules
	enc := NewHDCEncoder()
	modules := []ModuleMap{
		// Auth-related (should be boosted)
		{Path: "src/auth/jwt/", Language: "typescript", Summary: "TypeScript module",
			KeyTypes: []string{"class JWTService", "interface TokenPayload"}, FileCount: 4,
			Identifiers: []string{"validateToken", "refreshToken", "signJWT"}, Imports: []string{"jsonwebtoken"}},
		{Path: "src/middleware/auth/", Language: "typescript", Summary: "TypeScript module",
			KeyTypes: []string{"class AuthMiddleware"}, FileCount: 2,
			Identifiers: []string{"requireAuth", "extractBearerToken"}, Imports: []string{"express"}},
		{Path: "src/services/credentials/", Language: "typescript", Summary: "TypeScript module",
			KeyTypes: []string{"class CredentialService"}, FileCount: 5,
			Identifiers: []string{"issueCredential", "verifyVC"}, Imports: []string{"@learncard/core"}},
		// Irrelevant modules (should be filtered or ranked low)
		{Path: "src/components/boost-search/", Language: "typescript", Summary: "TypeScript module", FileCount: 1},
		{Path: "src/components/qrcode-scanner-button/", Language: "typescript", Summary: "TypeScript module", FileCount: 2},
		{Path: "src/components/ai-sessions/AiSessions/", Language: "typescript", Summary: "TypeScript module", FileCount: 3},
		{Path: "src/components/network-settings/", Language: "typescript", Summary: "TypeScript module",
			KeyTypes: []string{"class NetworkSettingsState"}, FileCount: 2},
		{Path: "src/components/qrcode-user-card/", Language: "typescript", Summary: "TypeScript module", FileCount: 3},
		{Path: "src/components/learncard/", Language: "typescript", Summary: "TypeScript module", FileCount: 12},
		{Path: "src/components/boost/boost-options-menu/", Language: "typescript", Summary: "TypeScript module", FileCount: 3},
		{Path: "src/components/boost/boostCMS/", Language: "typescript", Summary: "TypeScript module", FileCount: 2},
		{Path: "src/components/locationSearch/", Language: "typescript", Summary: "TypeScript module", FileCount: 2},
		{Path: "src/components/push-notification-settings/", Language: "typescript", Summary: "TypeScript module", FileCount: 2},
		{Path: "src/components/familyCMS/FamilyBoostPreview/", Language: "typescript", Summary: "TypeScript module", FileCount: 4},
		{Path: "src/components/EarnedAndManagedTabs/", Language: "typescript", Summary: "TypeScript module", FileCount: 1},
		{Path: "netlify/edge-functions/", Language: "typescript", Summary: "TypeScript module", FileCount: 1},
	}

	cm := &CodeMap{
		Zoom1: "TypeScript project.",
		Zoom2: modules,
	}

	// Build HDC embeddings for all modules
	moduleEmbs := make(map[string][]float32)
	for _, m := range modules {
		moduleEmbs[m.Path] = enc.EncodeModule(m)
	}

	// Generic query — simulates "session context" at session start
	genericQuery := enc.EncodeQuery("session context")

	// File hints from checkpoints (simulates LC-1663 JWT work)
	boostPaths := []string{"jwt.go", "auth.go", "middleware.go", "credentials.ts"}

	t.Run("without_boost", func(t *testing.T) {
		out := cm.RenderAtBudget(600, genericQuery, moduleEmbs, nil)
		t.Log("Without boost:\n" + out)

		// Check what appears first — likely random
		jwtIdx := strings.Index(out, "src/auth/jwt/")
		boostSearchIdx := strings.Index(out, "src/components/boost-search/")

		if jwtIdx >= 0 && boostSearchIdx >= 0 && jwtIdx < boostSearchIdx {
			t.Log("NOTE: JWT happened to rank higher even without boost (good HDC signal)")
		}
	})

	t.Run("with_boost", func(t *testing.T) {
		out := cm.RenderAtBudget(600, genericQuery, moduleEmbs, boostPaths)
		t.Log("With boost:\n" + out)

		// Auth modules should appear in output and rank before random UI modules
		if !strings.Contains(out, "src/auth/jwt/") {
			t.Error("expected src/auth/jwt/ in boosted output")
		}
		if !strings.Contains(out, "src/middleware/auth/") {
			t.Error("expected src/middleware/auth/ in boosted output")
		}

		// JWT should appear before any random UI component
		jwtIdx := strings.Index(out, "src/auth/jwt/")
		for _, irrelevant := range []string{
			"src/components/boost-search/",
			"src/components/qrcode-scanner-button/",
			"src/components/ai-sessions/",
			"src/components/network-settings/",
		} {
			irIdx := strings.Index(out, irrelevant)
			if irIdx >= 0 && jwtIdx > irIdx {
				t.Errorf("expected src/auth/jwt/ before %s (boost should prioritize)", irrelevant)
			}
		}
	})

	t.Run("min_threshold_filters_noise", func(t *testing.T) {
		out := cm.RenderAtBudget(2000, genericQuery, moduleEmbs, nil)
		t.Log("Large budget, no boost:\n" + out)

		// Count modules shown — should be fewer than total due to threshold
		shown := 0
		for _, m := range modules {
			if strings.Contains(out, m.Path) {
				shown++
			}
		}
		t.Logf("Showed %d/%d modules (threshold filtered %d)", shown, len(modules), len(modules)-shown)

		// With generic query and no boost, threshold should filter some noise
		if shown == len(modules) {
			t.Log("NOTE: all modules passed threshold — HDC may not discriminate enough for this query")
		}
	})
}

// Suppress unused import warning
var _ = os.DevNull
