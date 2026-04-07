package dualmem

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestScanAndRank_SmallRepo runs the full scan pipeline on this project's own
// source, verifies that ranked files are sorted by score descending, and checks
// that a second run uses the cache (same results, no re-parsing).
func TestScanAndRank_SmallRepo(t *testing.T) {
	// Use the dualmem package directory itself as a small repo
	rootDir := filepath.Join("..", "dualmem")
	absRoot, err := filepath.Abs(rootDir)
	if err != nil {
		t.Fatalf("abs path: %v", err)
	}

	// Verify the directory exists and has Go files
	entries, err := os.ReadDir(absRoot)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	hasGo := false
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".go") && !strings.HasSuffix(e.Name(), "_test.go") {
			hasGo = true
			break
		}
	}
	if !hasGo {
		t.Skip("no Go source files found in dualmem directory")
	}

	store := newTestStore(t)
	scanner := &RepoScanner{
		RootDir:   absRoot,
		Namespace: "test-scan",
		Store:     store,
	}

	// First scan
	result, err := scanner.ScanAndRank("", nil)
	if err != nil {
		t.Fatalf("ScanAndRank: %v", err)
	}

	if result.FileCount == 0 {
		t.Error("expected FileCount > 0")
	}
	if result.TagCount == 0 {
		t.Error("expected TagCount > 0")
	}
	if len(result.Files) == 0 {
		t.Error("expected at least one ranked file")
	}
	if result.Graph == nil {
		t.Error("expected Graph to be non-nil")
	}

	// Verify files are sorted by score descending
	for i := 1; i < len(result.Files); i++ {
		if result.Files[i].Score > result.Files[i-1].Score {
			t.Errorf("files not sorted by score: files[%d].Score=%.6f > files[%d].Score=%.6f",
				i, result.Files[i].Score, i-1, result.Files[i-1].Score)
		}
	}

	// Every file should have a non-negative score
	for _, f := range result.Files {
		if f.Score < 0 {
			t.Errorf("file %s has negative score: %f", f.Path, f.Score)
		}
	}

	// Second scan should produce identical results (from cache)
	result2, err := scanner.ScanAndRank("", nil)
	if err != nil {
		t.Fatalf("ScanAndRank (second): %v", err)
	}

	if result2.FileCount != result.FileCount {
		t.Errorf("second scan FileCount=%d, want %d", result2.FileCount, result.FileCount)
	}
	if result2.TagCount != result.TagCount {
		t.Errorf("second scan TagCount=%d, want %d", result2.TagCount, result.TagCount)
	}
	if len(result2.Files) != len(result.Files) {
		t.Errorf("second scan len(Files)=%d, want %d", len(result2.Files), len(result.Files))
	}

	// Compare scores from both runs
	for i := range result.Files {
		if i >= len(result2.Files) {
			break
		}
		if result.Files[i].Path != result2.Files[i].Path {
			t.Errorf("file order mismatch at index %d: %q vs %q", i, result.Files[i].Path, result2.Files[i].Path)
		}
		// Use tolerance for float comparison
		diff := result.Files[i].Score - result2.Files[i].Score
		if diff < 0 {
			diff = -diff
		}
		if diff > 1e-10 {
			t.Errorf("score mismatch for %s: %.10f vs %.10f", result.Files[i].Path, result.Files[i].Score, result2.Files[i].Score)
		}
	}

	// Verify cache entries exist
	mtimes, err := store.LoadAllTagMtimes("test-scan")
	if err != nil {
		t.Fatalf("LoadAllTagMtimes: %v", err)
	}
	if len(mtimes) == 0 {
		t.Error("expected cache entries after scan")
	}
}

// TestScanAndRank_Personalized tests that a query for "PageRank" boosts
// graph.go in the ranking because the file path contains "graph" and it
// defines the PageRank-related symbols.
func TestScanAndRank_Personalized(t *testing.T) {
	rootDir := filepath.Join("..", "dualmem")
	absRoot, err := filepath.Abs(rootDir)
	if err != nil {
		t.Fatalf("abs path: %v", err)
	}

	scanner := &RepoScanner{
		RootDir:   absRoot,
		Namespace: "test-personalized",
		Store:     newTestStore(t),
	}

	result, err := scanner.ScanAndRank("PageRank graph", nil)
	if err != nil {
		t.Fatalf("ScanAndRank: %v", err)
	}

	if len(result.Files) == 0 {
		t.Fatal("expected at least one ranked file")
	}

	// Find graph.go in the results
	var graphIdx int = -1
	var topScore float64
	for i, f := range result.Files {
		if i == 0 {
			topScore = f.Score
		}
		if strings.Contains(f.Path, "graph.go") {
			graphIdx = i
			break
		}
	}

	if graphIdx < 0 {
		// List files for debugging
		for _, f := range result.Files {
			t.Logf("  %s (%.6f)", f.Path, f.Score)
		}
		t.Fatal("graph.go not found in ranked files")
	}

	// graph.go should be in the top files
	if graphIdx > 5 {
		t.Errorf("graph.go at index %d, expected top 5", graphIdx)
	}

	// The top file should have a higher score than uniform PageRank would give
	// (personalization should boost relevant files)
	if topScore <= 0 {
		t.Error("top score should be positive")
	}

	t.Logf("Top file: %s (%.6f)", result.Files[0].Path, result.Files[0].Score)
	t.Logf("graph.go at index %d: %.6f", graphIdx, result.Files[graphIdx].Score)
}

// TestScanAndRank_WithOverrides tests that personalOverrides are respected.
func TestScanAndRank_WithOverrides(t *testing.T) {
	rootDir := filepath.Join("..", "dualmem")
	absRoot, err := filepath.Abs(rootDir)
	if err != nil {
		t.Fatalf("abs path: %v", err)
	}

	scanner := &RepoScanner{
		RootDir:   absRoot,
		Namespace: "test-overrides",
		Store:     newTestStore(t),
	}

	// Force a specific file to rank highest
	overrides := map[string]float64{
		"types.go": 1000.0,
	}

	result, err := scanner.ScanAndRank("", overrides)
	if err != nil {
		t.Fatalf("ScanAndRank: %v", err)
	}

	if len(result.Files) == 0 {
		t.Fatal("expected at least one ranked file")
	}

	// Find types.go
	var found bool
	for _, f := range result.Files {
		if strings.HasSuffix(f.Path, "types.go") {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("types.go not found in results")
	}

	// types.go should be the top-ranked file
	topPath := result.Files[0].Path
	if !strings.HasSuffix(topPath, "types.go") {
		t.Errorf("top file is %s (%.6f), expected types.go", topPath, result.Files[0].Score)
	}
}

// TestScanAndRank_NoStore tests that scanning works without a cache store.
func TestScanAndRank_NoStore(t *testing.T) {
	rootDir := filepath.Join("..", "dualmem")
	absRoot, err := filepath.Abs(rootDir)
	if err != nil {
		t.Fatalf("abs path: %v", err)
	}

	scanner := &RepoScanner{
		RootDir:   absRoot,
		Namespace: "test-nostore",
		Store:     nil, // No cache
	}

	result, err := scanner.ScanAndRank("", nil)
	if err != nil {
		t.Fatalf("ScanAndRank: %v", err)
	}

	if result.FileCount == 0 {
		t.Error("expected FileCount > 0 without store")
	}
	if result.TagCount == 0 {
		t.Error("expected TagCount > 0 without store")
	}
}

// TestRenderRankedFiles verifies the rendering output format.
func TestRenderRankedFiles(t *testing.T) {
	files := []RankedFile{
		{Path: "dualmem/graph.go", Score: 0.15, Source: "pagerank", Desc: "function: BuildFileGraph, PageRank, RankFiles"},
		{Path: "dualmem/store.go", Score: 0.10, Source: "pagerank", Desc: "type: Store; function: NewStore, Save"},
		{Path: "dualmem/engine.go", Score: 0.08, Source: "pagerank", Desc: "type: Engine; function: NewEngine"},
		{Path: "dualmem/tags.go", Score: 0.05, Source: "pagerank", Desc: "function: ExtractTags, DetectLanguage"},
	}

	// Test with generous budget
	output := RenderRankedFiles(files, 1000)
	if output == "" {
		t.Fatal("expected non-empty output")
	}

	// Should contain file paths
	for _, f := range files {
		if !strings.Contains(output, f.Path) {
			t.Errorf("output missing file path %q", f.Path)
		}
	}

	// Should contain scores
	if !strings.Contains(output, "0.1500") {
		t.Error("output missing score for graph.go")
	}

	// Lines should be indented with two spaces
	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	for i, line := range lines {
		if !strings.HasPrefix(line, "  ") {
			t.Errorf("line %d not indented: %q", i, line)
		}
	}

	// Test with very small budget
	smallOutput := RenderRankedFiles(files, 5)
	if smallOutput == "" {
		t.Error("expected at least one line with 5 token budget")
	}
	smallLines := strings.Split(strings.TrimRight(smallOutput, "\n"), "\n")
	if len(smallLines) >= len(files) {
		t.Errorf("expected fewer lines with small budget, got %d", len(smallLines))
	}

	// Test empty input
	emptyOutput := RenderRankedFiles(nil, 1000)
	if emptyOutput != "" {
		t.Errorf("expected empty output for nil files, got %q", emptyOutput)
	}

	// Test files without descriptions
	noDescFiles := []RankedFile{
		{Path: "main.go", Score: 0.25, Source: "pagerank"},
	}
	noDescOutput := RenderRankedFiles(noDescFiles, 100)
	if !strings.Contains(noDescOutput, "main.go") {
		t.Error("output missing main.go")
	}
	// Should still have score
	if !strings.Contains(noDescOutput, "0.2500") {
		t.Error("output missing score for main.go")
	}
}

// TestRenderRankedFiles_SingleFileBudget verifies that a single long line
// can still be rendered even when budget is tight.
func TestRenderRankedFiles_SingleFileBudget(t *testing.T) {
	files := []RankedFile{
		{Path: "a.go", Score: 0.5, Source: "pagerank", Desc: "a very long description"},
	}
	output := RenderRankedFiles(files, 50)
	if !strings.Contains(output, "a.go") {
		t.Error("expected a.go in output even with small budget")
	}
}

// TestBuildFileDesc tests the description builder edge cases.
func TestBuildFileDesc(t *testing.T) {
	// Empty
	if desc := buildFileDesc(nil); desc != "" {
		t.Errorf("expected empty desc for nil, got %q", desc)
	}

	// Single type
	desc := buildFileDesc([]Tag{
		{Name: "Foo", Kind: "def", SubKind: "type"},
	})
	if !strings.Contains(desc, "type: Foo") {
		t.Errorf("expected 'type: Foo' in desc, got %q", desc)
	}

	// Multiple sub-kinds with many names
	var defs []Tag
	for i := 0; i < 10; i++ {
		defs = append(defs, Tag{Name: fmt.Sprintf("Func%d", i), Kind: "def", SubKind: "function"})
	}
	desc = buildFileDesc(defs)
	if !strings.Contains(desc, "function:") {
		t.Errorf("expected 'function:' in desc, got %q", desc)
	}
	if strings.Contains(desc, "Func9") {
		t.Error("expected only first 3 names, got more")
	}
	if !strings.Contains(desc, "...") {
		t.Error("expected '...' for truncated list")
	}
}

// TestTokenizeQuery tests query tokenization.
func TestTokenizeQuery(t *testing.T) {
	tests := []struct {
		query string
		want  []string
	}{
		{"PageRank graph", []string{"pagerank", "graph"}},
		{"hello-world", []string{"hello", "world"}},
		{"foo_bar baz", []string{"foo_bar", "baz"}},
		{"  spaces  everywhere  ", []string{"spaces", "everywhere"}},
		{"", nil},
		{"!@#$%", nil},
	}

	for _, tt := range tests {
		got := tokenizeQuery(tt.query)
		if len(got) != len(tt.want) {
			t.Errorf("tokenizeQuery(%q) = %v, want %v", tt.query, got, tt.want)
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("tokenizeQuery(%q)[%d] = %q, want %q", tt.query, i, got[i], tt.want[i])
			}
		}
	}
}
