package dualmem

import (
	"math"
	"sort"
	"testing"
)

func TestBasisVector_Deterministic(t *testing.T) {
	enc := NewHDCEncoder()
	v1 := enc.BasisVector("engine")
	v2 := enc.BasisVector("engine")

	if len(v1) != HDCDimensions {
		t.Fatalf("expected %d dimensions, got %d", HDCDimensions, len(v1))
	}
	for i := range v1 {
		if v1[i] != v2[i] {
			t.Fatalf("non-deterministic at index %d: %f != %f", i, v1[i], v2[i])
		}
	}
}

func TestBasisVector_BinaryValues(t *testing.T) {
	enc := NewHDCEncoder()
	v := enc.BasisVector("test")
	for i, val := range v {
		if val != 1.0 && val != -1.0 {
			t.Fatalf("expected +1/-1 at index %d, got %f", i, val)
		}
	}
}

func TestBasisVector_Orthogonal(t *testing.T) {
	enc := NewHDCEncoder()
	v1 := enc.BasisVector("engine")
	v2 := enc.BasisVector("database")

	sim := CosineSimilarity(v1, v2)
	// Random binary vectors in high dimensions should have near-zero cosine similarity
	if math.Abs(float64(sim)) > 0.1 {
		t.Errorf("expected near-orthogonal vectors, got cosine similarity %f", sim)
	}
}

func TestRotate_Preserves(t *testing.T) {
	enc := NewHDCEncoder()
	v := enc.BasisVector("test")
	rotated := enc.Rotate(v, 7)

	if len(rotated) != len(v) {
		t.Fatalf("rotation changed dimension: %d → %d", len(v), len(rotated))
	}

	// Norm should be preserved (both are ±1 vectors)
	var norm1, norm2 float64
	for i := range v {
		norm1 += float64(v[i]) * float64(v[i])
		norm2 += float64(rotated[i]) * float64(rotated[i])
	}
	if math.Abs(norm1-norm2) > 1e-6 {
		t.Errorf("rotation changed norm: %f → %f", norm1, norm2)
	}
}

func TestRotate_Zero(t *testing.T) {
	enc := NewHDCEncoder()
	v := enc.BasisVector("test")
	rotated := enc.Rotate(v, 0)
	for i := range v {
		if v[i] != rotated[i] {
			t.Fatal("zero rotation changed vector")
		}
	}
}

func TestEncodeModule_Normalized(t *testing.T) {
	enc := NewHDCEncoder()
	m := ModuleMap{
		Path:        "dualmem/",
		Language:    "go",
		Summary:     "Go package dualmem",
		KeyTypes:    []string{"struct Engine", "struct Config"},
		EntryPoints: []string{"Add()", "DualSearch()"},
		FileCount:   10,
		Imports:     []string{"context", "database/sql", "sync"},
		Identifiers: []string{"runQuery", "parseResult"},
	}
	v := enc.EncodeModule(m)

	if len(v) != HDCDimensions {
		t.Fatalf("expected %d dimensions, got %d", HDCDimensions, len(v))
	}

	// Check L2 norm is ~1.0
	var norm float64
	for _, val := range v {
		norm += float64(val) * float64(val)
	}
	norm = math.Sqrt(norm)
	if math.Abs(norm-1.0) > 1e-5 {
		t.Errorf("expected unit norm, got %f", norm)
	}
}

func TestEncodeQuery_Normalized(t *testing.T) {
	enc := NewHDCEncoder()
	v := enc.EncodeQuery("fix the auth bug in engine")

	if len(v) != HDCDimensions {
		t.Fatalf("expected %d dimensions, got %d", HDCDimensions, len(v))
	}

	var norm float64
	for _, val := range v {
		norm += float64(val) * float64(val)
	}
	norm = math.Sqrt(norm)
	if math.Abs(norm-1.0) > 1e-5 {
		t.Errorf("expected unit norm, got %f", norm)
	}
}

func TestEncodeQuery_Empty(t *testing.T) {
	enc := NewHDCEncoder()
	v := enc.EncodeQuery("")

	if len(v) != HDCDimensions {
		t.Fatalf("expected %d dimensions, got %d", HDCDimensions, len(v))
	}
	// Should be a zero vector
	for i, val := range v {
		if val != 0 {
			t.Fatalf("expected zero vector for empty query, got %f at index %d", val, i)
		}
	}
}

func TestHDC_RelevantModuleRanksHigher(t *testing.T) {
	enc := NewHDCEncoder()

	engineModule := ModuleMap{
		Path:        "dualmem/",
		Language:    "go",
		KeyTypes:    []string{"struct Engine", "struct Config"},
		EntryPoints: []string{"Add()", "DualSearch()"},
		Imports:     []string{"context", "sync"},
		Identifiers: []string{"assembleContext", "embedQuery"},
	}
	storeModule := ModuleMap{
		Path:        "store/",
		Language:    "go",
		KeyTypes:    []string{"struct SQLiteStore"},
		EntryPoints: []string{"Open()"},
		Imports:     []string{"database/sql"},
		Identifiers: []string{"execSQL"},
	}

	query := enc.EncodeQuery("Engine struct config")
	engineVec := enc.EncodeModule(engineModule)
	storeVec := enc.EncodeModule(storeModule)

	simEngine := CosineSimilarity(query, engineVec)
	simStore := CosineSimilarity(query, storeVec)

	if simEngine <= simStore {
		t.Errorf("expected Engine module to rank higher for 'Engine struct config' query, got engine=%f store=%f", simEngine, simStore)
	}
}

func TestHDC_PathTokensContribute(t *testing.T) {
	enc := NewHDCEncoder()

	storeModule := ModuleMap{
		Path:     "store/sqlite/",
		Language: "go",
	}
	cmdModule := ModuleMap{
		Path:     "cmd/server/",
		Language: "go",
	}

	query := enc.EncodeQuery("store sqlite database")
	storeSim := CosineSimilarity(query, enc.EncodeModule(storeModule))
	cmdSim := CosineSimilarity(query, enc.EncodeModule(cmdModule))

	if storeSim <= cmdSim {
		t.Errorf("expected store module to rank higher for 'store sqlite' query, got store=%f cmd=%f", storeSim, cmdSim)
	}
}

func TestHDC_LanguageContributes(t *testing.T) {
	enc := NewHDCEncoder()

	goModule := ModuleMap{Path: "pkg/", Language: "go"}
	tsModule := ModuleMap{Path: "pkg/", Language: "typescript"}

	query := enc.EncodeQuery("go package")
	goSim := CosineSimilarity(query, enc.EncodeModule(goModule))
	tsSim := CosineSimilarity(query, enc.EncodeModule(tsModule))

	if goSim <= tsSim {
		t.Errorf("expected Go module to rank higher for 'go package' query, got go=%f ts=%f", goSim, tsSim)
	}
}

func TestTokenize(t *testing.T) {
	tests := []struct {
		input    string
		expected []string
	}{
		{"dualmem/store/", []string{"dualmem", "store"}},
		{"HTMLParser", []string{"html", "parser"}},     // lowercased by hdcTokenize
		{"getIPv4", []string{"get", "i", "pv", "4"}},  // splits digits from letters
		{"simple", []string{"simple"}},
		{"a_b-c.d", []string{"b", "c", "d"}},     // 'a' is a stop word
		{"struct Engine", []string{"engine"}},      // 'struct' is stripped
		{"", nil},
	}

	for _, tt := range tests {
		got := hdcTokenize(tt.input)
		if len(got) != len(tt.expected) {
			t.Errorf("hdcTokenize(%q) = %v (len %d), want %v (len %d)", tt.input, got, len(got), tt.expected, len(tt.expected))
			continue
		}
		for i := range got {
			if got[i] != tt.expected[i] {
				t.Errorf("hdcTokenize(%q)[%d] = %q, want %q", tt.input, i, got[i], tt.expected[i])
			}
		}
	}
}

func TestSplitCamelCase(t *testing.T) {
	tests := []struct {
		input    string
		expected []string
	}{
		{"FooBar", []string{"Foo", "Bar"}},
		{"HTMLParser", []string{"HTML", "Parser"}},
		{"simple", []string{"simple"}},
		{"ABC", []string{"ABC"}},
		{"getURL", []string{"get", "URL"}},
	}

	for _, tt := range tests {
		got := splitCamelCase(tt.input)
		if len(got) != len(tt.expected) {
			t.Errorf("splitCamelCase(%q) = %v, want %v", tt.input, got, tt.expected)
			continue
		}
		for i := range got {
			if got[i] != tt.expected[i] {
				t.Errorf("splitCamelCase(%q)[%d] = %q, want %q", tt.input, i, got[i], tt.expected[i])
			}
		}
	}
}

func TestHDCEncodeCodeMap(t *testing.T) {
	cm := &CodeMap{
		Zoom2: []ModuleMap{
			{Path: "pkg/", Language: "go", KeyTypes: []string{"struct Foo"}, Imports: []string{"fmt"}, Identifiers: []string{"helper"}},
			{Path: "cmd/", Language: "go", EntryPoints: []string{"main()"}, Imports: []string{"os"}},
		},
	}

	result := HDCEncodeCodeMap(cm)
	if len(result) != 2 {
		t.Fatalf("expected 2 module embeddings, got %d", len(result))
	}
	if _, ok := result["pkg/"]; !ok {
		t.Error("missing embedding for pkg/")
	}
	if _, ok := result["cmd/"]; !ok {
		t.Error("missing embedding for cmd/")
	}
}

func TestHDCEncodeCodeMap_Empty(t *testing.T) {
	cm := &CodeMap{Zoom2: nil}
	result := HDCEncodeCodeMap(cm)
	if result != nil {
		t.Error("expected nil for empty code map")
	}
}

// --- Content layer ranking tests ---

func TestHDC_ContentLayerImproves(t *testing.T) {
	enc := NewHDCEncoder()

	// Module pool — realistic modules with content fields
	modules := []ModuleMap{
		{
			Path: "db/", Language: "go",
			KeyTypes:    []string{"struct Pool"},
			EntryPoints: []string{"Open()"},
			Imports:     []string{"database/sql", "sync"},
			Identifiers: []string{"pool", "connManager", "acquireConn"},
		},
		{
			Path: "auth/", Language: "go",
			KeyTypes:    []string{"struct AuthService"},
			EntryPoints: []string{"Login()"},
			Imports:     []string{"crypto/bcrypt", "net/http", "encoding/json"},
			Identifiers: []string{"validateToken", "hashPassword", "checkPermission"},
		},
		{
			Path: "api/", Language: "go",
			KeyTypes:    []string{"struct Router"},
			EntryPoints: []string{"ServeHTTP()"},
			Imports:     []string{"net/http", "encoding/json"},
			Identifiers: []string{"handleRequest", "parsePayload", "validateSchema"},
		},
		{
			Path: "config/", Language: "go",
			KeyTypes:    []string{"struct Config"},
			EntryPoints: []string{"Load()"},
			Imports:     []string{"os", "encoding/json"},
			Identifiers: []string{"readFile", "mergeDefaults"},
		},
		{
			Path: "cmd/server/", Language: "go",
			EntryPoints: []string{"main()"},
			Imports:     []string{"os", "flag"},
			Identifiers: []string{"run", "parseFlags"},
		},
	}

	type rankCase struct {
		Name     string
		Query    string
		Expected string // path that should rank first
	}

	cases := []rankCase{
		{"database pooling", "database connection pooling", "db/"},
		{"auth token validation", "validate authentication token", "auth/"},
		{"JSON parsing", "parse JSON payload and validate schema", "api/"},
		{"password hashing", "hash password bcrypt", "auth/"},
		{"config loading", "load configuration from file", "config/"},
	}

	// With content layer (current weights)
	t.Run("WithContent", func(t *testing.T) {
		passed := 0
		for _, tc := range cases {
			queryVec := enc.EncodeQuery(tc.Query)
			best := ""
			bestSim := float64(-1)
			for _, m := range modules {
				sim := CosineSimilarity(queryVec, enc.EncodeModule(m))
				if sim > bestSim {
					bestSim = sim
					best = m.Path
				}
			}
			if best == tc.Expected {
				passed++
				t.Logf("  PASS %s: %s ranked first (sim=%.4f)", tc.Name, best, bestSim)
			} else {
				t.Logf("  MISS %s: got %s, want %s (sim=%.4f)", tc.Name, best, tc.Expected, bestSim)
			}
		}
		t.Logf("Content layer accuracy: %d/%d (%.0f%%)", passed, len(cases), float64(passed)/float64(len(cases))*100)
	})

	// Without content layer (strip Imports/Identifiers)
	t.Run("WithoutContent", func(t *testing.T) {
		stripped := make([]ModuleMap, len(modules))
		for i, m := range modules {
			stripped[i] = ModuleMap{
				Path:        m.Path,
				Language:    m.Language,
				KeyTypes:    m.KeyTypes,
				EntryPoints: m.EntryPoints,
				FileCount:   m.FileCount,
				// No Imports, no Identifiers
			}
		}

		passed := 0
		for _, tc := range cases {
			queryVec := enc.EncodeQuery(tc.Query)
			best := ""
			bestSim := float64(-1)
			for _, m := range stripped {
				sim := CosineSimilarity(queryVec, enc.EncodeModule(m))
				if sim > bestSim {
					bestSim = sim
					best = m.Path
				}
			}
			if best == tc.Expected {
				passed++
				t.Logf("  PASS %s: %s ranked first (sim=%.4f)", tc.Name, best, bestSim)
			} else {
				t.Logf("  MISS %s: got %s, want %s (sim=%.4f)", tc.Name, best, tc.Expected, bestSim)
			}
		}
		t.Logf("No-content accuracy: %d/%d (%.0f%%)", passed, len(cases), float64(passed)/float64(len(cases))*100)
	})
}

// TestHDC_ContentLayerNDCG measures ranking quality with NDCG across a module pool.
func TestHDC_ContentLayerNDCG(t *testing.T) {
	enc := NewHDCEncoder()

	// 8 modules — realistic codebase
	modules := []ModuleMap{
		{Path: "store/sqlite/", Language: "go", KeyTypes: []string{"struct SQLiteStore"}, Imports: []string{"database/sql", "sync"}, Identifiers: []string{"execQuery", "scanRow"}},
		{Path: "store/redis/", Language: "go", KeyTypes: []string{"struct RedisCache"}, Imports: []string{"github.com/redis/go-redis/v9"}, Identifiers: []string{"setKey", "getKey", "expire"}},
		{Path: "auth/", Language: "go", KeyTypes: []string{"struct AuthService"}, Imports: []string{"crypto/bcrypt", "crypto/rand"}, Identifiers: []string{"validateToken", "issueJWT"}},
		{Path: "api/handlers/", Language: "go", KeyTypes: []string{"struct Handler"}, Imports: []string{"net/http", "encoding/json"}, Identifiers: []string{"respondJSON", "parseBody"}},
		{Path: "api/middleware/", Language: "go", Imports: []string{"net/http", "log"}, Identifiers: []string{"logRequest", "recoverPanic", "rateLimit"}},
		{Path: "config/", Language: "go", KeyTypes: []string{"struct Config"}, Imports: []string{"os", "gopkg.in/yaml.v3"}, Identifiers: []string{"loadYAML", "validate"}},
		{Path: "cmd/server/", Language: "go", EntryPoints: []string{"main()"}, Imports: []string{"os", "flag"}, Identifiers: []string{"run"}},
		{Path: "internal/telemetry/", Language: "go", Imports: []string{"go.opentelemetry.io/otel"}, Identifiers: []string{"initTracer", "spanFromCtx"}},
	}

	type ndcgCase struct {
		Query    string
		// Relevance grades: 3=perfect, 2=good, 1=marginal, 0=irrelevant
		Grades map[string]float64
	}

	cases := []ndcgCase{
		{
			Query: "SQLite database query execution",
			Grades: map[string]float64{
				"store/sqlite/": 3, "store/redis/": 1, "config/": 0,
			},
		},
		{
			Query: "authentication token JWT",
			Grades: map[string]float64{
				"auth/": 3, "api/middleware/": 1, "api/handlers/": 1,
			},
		},
		{
			Query: "HTTP request handler JSON response",
			Grades: map[string]float64{
				"api/handlers/": 3, "api/middleware/": 2, "auth/": 0,
			},
		},
		{
			Query: "Redis caching key expiration",
			Grades: map[string]float64{
				"store/redis/": 3, "store/sqlite/": 0,
			},
		},
	}

	totalNDCG := 0.0
	for _, tc := range cases {
		queryVec := enc.EncodeQuery(tc.Query)

		// Rank all modules by similarity
		type ranked struct {
			path string
			sim  float64
		}
		var ranking []ranked
		for _, m := range modules {
			sim := CosineSimilarity(queryVec, enc.EncodeModule(m))
			ranking = append(ranking, ranked{m.Path, sim})
		}
		sort.Slice(ranking, func(i, j int) bool {
			return ranking[i].sim > ranking[j].sim
		})

		// Compute DCG
		dcg := 0.0
		for i, r := range ranking {
			grade := tc.Grades[r.path] // 0 if not in map
			dcg += grade / math.Log2(float64(i+2))
		}

		// Compute ideal DCG
		var grades []float64
		for _, g := range tc.Grades {
			grades = append(grades, g)
		}
		sort.Float64s(grades)
		// Reverse
		for i, j := 0, len(grades)-1; i < j; i, j = i+1, j-1 {
			grades[i], grades[j] = grades[j], grades[i]
		}
		idcg := 0.0
		for i, g := range grades {
			idcg += g / math.Log2(float64(i+2))
		}

		ndcg := 0.0
		if idcg > 0 {
			ndcg = dcg / idcg
		}
		totalNDCG += ndcg

		t.Logf("  %-45s NDCG=%.3f  top3=[%s, %s, %s]",
			tc.Query, ndcg, ranking[0].path, ranking[1].path, ranking[2].path)
	}

	avgNDCG := totalNDCG / float64(len(cases))
	t.Logf("\n  Average NDCG: %.3f", avgNDCG)

	if avgNDCG < 0.5 {
		t.Errorf("average NDCG %.3f is below 0.5 threshold — content layer not helping enough", avgNDCG)
	}
}

// --- Benchmarks ---

func BenchmarkHDCEncodeModule(b *testing.B) {
	enc := NewHDCEncoder()
	m := ModuleMap{
		Path:        "dualmem/store/sqlite/",
		Language:    "go",
		KeyTypes:    []string{"struct SQLiteStore", "interface Store", "struct Config"},
		EntryPoints: []string{"Open()", "Close()", "Add()", "Search()"},
		FileCount:   8,
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		enc.EncodeModule(m)
	}
}

func BenchmarkHDCEncodeModule_WithContent(b *testing.B) {
	enc := NewHDCEncoder()
	m := ModuleMap{
		Path:        "dualmem/store/sqlite/",
		Language:    "go",
		KeyTypes:    []string{"struct SQLiteStore", "interface Store", "struct Config"},
		EntryPoints: []string{"Open()", "Close()", "Add()", "Search()"},
		FileCount:   8,
		Imports:     []string{"database/sql", "context", "sync", "fmt", "encoding/json"},
		Identifiers: []string{"execQuery", "scanRow", "buildWhere", "parseResult", "validateInput"},
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		enc.EncodeModule(m)
	}
}

func BenchmarkHDCEncodeQuery(b *testing.B) {
	enc := NewHDCEncoder()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		enc.EncodeQuery("fix the authentication bug in the engine")
	}
}
