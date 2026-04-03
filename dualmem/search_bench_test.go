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

// TestCodemapRelevance_CoChangeBoost demonstrates that co-change boosting
// surfaces structurally related files that HDC similarity alone would miss.
//
// Scenario: A developer working on "auth.go" has historically also changed
// "middleware.go" and "jwt.go" alongside it (evidenced by memories with
// multiple --files). A new module "rate_limiter.go" was recently added to
// the auth flow but has very different code structure (no shared types/imports).
// Without co-change: rate_limiter.go ranks low (different HDC signature).
// With co-change: rate_limiter.go is boosted because it co-changes with auth.go.
func TestCodemapRelevance_CoChangeBoost(t *testing.T) {
	enc := NewHDCEncoder()
	modules := []ModuleMap{
		// Auth-related cluster (should all appear)
		{Path: "src/auth/", Language: "go", Summary: "Go module",
			KeyTypes: []string{"AuthService", "TokenValidator"}, FileCount: 3,
			Identifiers: []string{"validateJWT", "refreshToken"}, Imports: []string{"crypto/rsa"}},
		{Path: "src/middleware/", Language: "go", Summary: "Go module",
			KeyTypes: []string{"AuthMiddleware", "RateLimiterMiddleware"}, FileCount: 2,
			Identifiers: []string{"requireAuth", "extractBearer"}, Imports: []string{"net/http"}},
		// Rate limiter — structurally different from auth (no shared types/imports)
		// but co-changes with auth.go in practice
		{Path: "src/ratelimit/", Language: "go", Summary: "Go module",
			KeyTypes: []string{"SlidingWindow", "TokenBucket"}, FileCount: 2,
			Identifiers: []string{"allowRequest", "resetBucket"}, Imports: []string{"sync", "time"}},
		// Unrelated modules
		{Path: "src/storage/", Language: "go", Summary: "Go module",
			KeyTypes: []string{"SQLiteStore", "PostgresStore"}, FileCount: 5,
			Identifiers: []string{"openDB", "migrate"}, Imports: []string{"database/sql"}},
		{Path: "src/ui/dashboard/", Language: "typescript", Summary: "TypeScript module",
			KeyTypes: []string{"DashboardView"}, FileCount: 4},
		{Path: "src/ui/settings/", Language: "typescript", Summary: "TypeScript module",
			KeyTypes: []string{"SettingsForm"}, FileCount: 3},
		{Path: "src/analytics/", Language: "go", Summary: "Go module",
			KeyTypes: []string{"EventTracker"}, FileCount: 2,
			Identifiers: []string{"trackEvent"}, Imports: []string{"encoding/json"}},
		{Path: "src/notifications/", Language: "go", Summary: "Go module",
			KeyTypes: []string{"NotificationService"}, FileCount: 3,
			Identifiers: []string{"sendPush", "sendEmail"}, Imports: []string{"net/smtp"}},
	}

	cm := &CodeMap{
		Zoom1: "Go + TypeScript project.",
		Zoom2: modules,
	}

	moduleEmbs := make(map[string][]float32)
	for _, m := range modules {
		moduleEmbs[m.Path] = enc.EncodeModule(m)
	}

	// Query about auth — should find auth and middleware via HDC,
	// but rate_limiter has very different code structure
	authQuery := enc.EncodeQuery("authentication token validation")
	boostPaths := []string{"auth.go"}

	// Co-change neighbors: auth.go historically co-changes with ratelimit/ and middleware/
	cochangeNeighbors := map[string]float64{
		"src/ratelimit/": 3.0, // strong co-change (3 co-occurrences)
		"src/middleware/": 2.0, // moderate co-change
	}

	t.Run("without_cochange", func(t *testing.T) {
		out := cm.RenderAtBudget(400, authQuery, moduleEmbs, boostPaths)
		t.Log("Without co-change:\n" + out)

		rateLimitIdx := strings.Index(out, "src/ratelimit/")
		authIdx := strings.Index(out, "src/auth/")

		if rateLimitIdx < 0 {
			t.Log("CONFIRMED: ratelimit not in output without co-change (HDC signal too weak)")
		} else if authIdx >= 0 && rateLimitIdx > authIdx {
			t.Log("ratelimit present but ranked below auth")
		}
	})

	t.Run("with_cochange", func(t *testing.T) {
		out := cm.RenderAtBudgetWithCoChange(400, authQuery, moduleEmbs, boostPaths, cochangeNeighbors)
		t.Log("With co-change:\n" + out)

		// Rate limiter should now appear because it co-changes with auth
		if !strings.Contains(out, "src/ratelimit/") {
			t.Error("expected src/ratelimit/ in co-change-boosted output — co-change should surface structurally dissimilar but historically related modules")
		}
		if !strings.Contains(out, "src/auth/") {
			t.Error("expected src/auth/ in output")
		}

		// Unrelated modules should still be ranked low
		authIdx := strings.Index(out, "src/auth/")
		for _, irrelevant := range []string{"src/ui/dashboard/", "src/analytics/", "src/notifications/"} {
			irIdx := strings.Index(out, irrelevant)
			if irIdx >= 0 && authIdx >= 0 && irIdx < authIdx {
				t.Errorf("expected src/auth/ before %s (auth is directly relevant)", irrelevant)
			}
		}
	})

	t.Run("value_delta", func(t *testing.T) {
		// Quantify the improvement: count how many relevant modules appear
		// in top results with and without co-change
		withoutOut := cm.RenderAtBudget(400, authQuery, moduleEmbs, boostPaths)
		withOut := cm.RenderAtBudgetWithCoChange(400, authQuery, moduleEmbs, boostPaths, cochangeNeighbors)

		relevant := []string{"src/auth/", "src/middleware/", "src/ratelimit/"}
		countWithout := 0
		countWith := 0
		for _, r := range relevant {
			if strings.Contains(withoutOut, r) {
				countWithout++
			}
			if strings.Contains(withOut, r) {
				countWith++
			}
		}

		t.Logf("Relevant modules surfaced: without co-change=%d/%d, with co-change=%d/%d",
			countWithout, len(relevant), countWith, len(relevant))
		if countWith > countWithout {
			t.Logf("CO-CHANGE VALUE: +%d additional relevant modules surfaced", countWith-countWithout)
		}
	})
}

// TestGraphBoost_StructureAware verifies that structure-aware graph boosting
// produces differentiated scores based on edge types, unlike the old flat +0.15.
func TestGraphBoost_StructureAware(t *testing.T) {
	store := testEntityStore(t)

	// Create entities with different edge types
	idAuth, _ := store.UpsertEntity(&EntityNode{Name: "auth", Type: "concept", Namespace: testNS})
	idJWT, _ := store.UpsertEntity(&EntityNode{Name: "jwt", Type: "concept", Namespace: testNS})
	idLogging, _ := store.UpsertEntity(&EntityNode{Name: "logging", Type: "concept", Namespace: testNS})

	// auth → depends_on → jwt (strong structural relationship)
	store.UpsertEdge(&EntityEdge{SourceID: idAuth, TargetID: idJWT, Relation: "depends_on", Namespace: testNS})
	// auth → relates → logging (weak association)
	store.UpsertEdge(&EntityEdge{SourceID: idAuth, TargetID: idLogging, Relation: "relates", Namespace: testNS})

	// Create memories linked to each entity
	store.InsertDetail(&DetailMemory{ID: "mem-jwt", Text: "JWT validation logic"}, []float32{0.1, 0.2}, testNS)
	store.InsertDetail(&DetailMemory{ID: "mem-log", Text: "Added request logging"}, []float32{0.3, 0.4}, testNS)
	store.LinkMemoryToEntity("mem-jwt", idJWT, testNS)
	store.LinkMemoryToEntity("mem-log", idLogging, testNS)

	// Compute graph boost — should differentiate by edge type
	e := &Engine{store: store}
	boostMap := e.computeGraphBoost(&GraphBoostConfig{
		Namespace: testNS,
		MaxHops:   1,
	}, "auth system")

	if boostMap == nil {
		t.Fatal("expected non-nil boost map")
	}

	// Both memories should be boosted, but JWT should get higher boost
	// because depends_on (1.0) > relates (0.5)
	jwtBoost := boostMap["mem-jwt"]
	logBoost := boostMap["mem-log"]

	t.Logf("JWT boost: %.3f, Logging boost: %.3f", jwtBoost, logBoost)

	if jwtBoost <= 0 {
		t.Error("expected positive boost for JWT memory (linked via depends_on)")
	}
	if logBoost <= 0 {
		t.Error("expected positive boost for logging memory (linked via relates)")
	}

	// The key assertion: structure-aware boost should produce different values
	// (old flat boost would give both 0.15)
	if jwtBoost == logBoost {
		t.Error("expected differentiated boost scores (depends_on should boost more than relates)")
	}
}

// Suppress unused import warning
var _ = os.DevNull
