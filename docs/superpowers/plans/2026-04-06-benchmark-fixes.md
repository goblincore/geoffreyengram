# Phase 4 Benchmark Fixes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix three benchmark issues — codemap cache for warm queries, two-stage file-level search, and markdown doc indexing — to improve search precision and latency.

**Architecture:** Wire CLI `search-code` through the Engine's existing SQLite-backed codemap cache. Add per-file metadata collection during scanning. Build a two-stage search (module→file) with BM25 file scoring. Treat markdown docs as pseudo-modules in the existing pipeline.

**Tech Stack:** Go, SQLite (existing store), go/ast, tree-sitter, regex-based markdown parsing.

**Spec:** `docs/superpowers/specs/2026-04-06-benchmark-fixes-design.md`

---

### Task 1: Add `"benchmarks"` to `skipDirs` (quick fix)

**Files:**
- Modify: `dualmem/codemap.go:42` (skipDirs map)
- Test: `dualmem/codemap_test.go`

- [ ] **Step 1: Write a test that benchmarks/ directories are skipped**

In `dualmem/codemap_test.go`, add:

```go
func TestScanCodebase_SkipsBenchmarks(t *testing.T) {
	tmpDir := t.TempDir()

	// Real source file
	writeFile(t, tmpDir, "main.go", `package main
func main() {}
`)

	// Benchmarks dir should be skipped
	benchDir := filepath.Join(tmpDir, "benchmarks")
	os.MkdirAll(benchDir, 0755)
	writeFile(t, benchDir, "bench.go", `package benchmarks
func BenchStuff() {}
`)

	cm, err := ScanCodebase(tmpDir)
	if err != nil {
		t.Fatalf("ScanCodebase: %v", err)
	}

	for _, m := range cm.Zoom2 {
		if strings.Contains(m.Path, "benchmarks") {
			t.Errorf("benchmarks/ should be excluded from scan, found module: %s", m.Path)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/donny/Projects/2026/geoffreyengram && go test ./dualmem/ -run TestScanCodebase_SkipsBenchmarks -v`
Expected: FAIL — benchmarks module is found.

- [ ] **Step 3: Add `"benchmarks"` to skipDirs**

In `dualmem/codemap.go`, line 42, add to the `skipDirs` map:

```go
var skipDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true, "__pycache__": true,
	".next": true, "dist": true, "build": true, ".claude": true, ".vscode": true,
	".idea": true, "coverage": true, ".nyc_output": true, "target": true,
	".nx-cache": true, ".github": true, ".changeset": true, ".superpowers": true,
	".turbo": true, ".cache": true, ".parcel-cache": true, ".output": true,
	".nuxt": true, ".svelte-kit": true, "tmp": true, "temp": true, "logs": true,
	".vexp": true, "benchmarks": true,
	// Python virtual environments & tool caches
	".venv": true, "venv": true, ".virtualenv": true, "env": true, ".env": true,
	".tox": true, ".mypy_cache": true, ".pytest_cache": true, ".ruff_cache": true,
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /Users/donny/Projects/2026/geoffreyengram && go test ./dualmem/ -run TestScanCodebase_SkipsBenchmarks -v`
Expected: PASS

- [ ] **Step 5: Run full test suite**

Run: `cd /Users/donny/Projects/2026/geoffreyengram && go test ./dualmem/... -count=1`
Expected: All tests pass.

- [ ] **Step 6: Commit**

```bash
git add dualmem/codemap.go dualmem/codemap_test.go
git commit -m "fix(codemap): exclude benchmarks/ from codebase scan

Prevents benchmark corpus from affecting BM25 IDF values,
stabilizing NDCG scores across runs."
```

---

### Task 2: Wire `search-code` CLI through Engine (codemap cache)

**Files:**
- Modify: `cmd/dualmem/main.go:1094-1138` (cmdSearchCode function)
- Test: manual — run `dualmem search-code` twice, verify second is fast

- [ ] **Step 1: Modify `cmdSearchCode()` to use Engine**

Replace the current `cmdSearchCode` function in `cmd/dualmem/main.go` (lines 1094-1138) with:

```go
func cmdSearchCode(cfg CLIConfig) {
	fs := flag.NewFlagSet("search-code", flag.ExitOnError)
	root := fs.String("root", "", "Project root (default: cwd)")
	limit := fs.Int("limit", 10, "Max results")
	jsonOut := fs.Bool("json", false, "JSON output")
	moduleOnly := fs.Bool("module-only", false, "Module-level results (skip file ranking)")
	fs.Parse(os.Args[2:])

	query := strings.Join(fs.Args(), " ")
	if query == "" {
		fmt.Fprintln(os.Stderr, "usage: dualmem search-code <query>")
		os.Exit(1)
	}

	rootDir := *root
	if rootDir == "" {
		rootDir, _ = os.Getwd()
	}

	// Open store for caching
	store, err := dualmem.NewSQLiteStore(cfg.Storage.SQLitePath)
	if err != nil {
		// Fallback: no cache, scan directly
		fmt.Fprintf(os.Stderr, "warning: could not open store for cache: %v\n", err)
		cm, scanErr := dualmem.ScanCodebase(rootDir)
		if scanErr != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", scanErr)
			os.Exit(1)
		}
		idx := dualmem.BuildCodeIndex(cm)
		printSearchResults(cm, idx, query, *limit, *jsonOut, *moduleOnly)
		return
	}
	defer store.Close()

	ns := "claude:" + filepath.Base(rootDir)
	engine, engErr := dualmem.NewEngine(dualmem.EngineConfig{
		RootDir:   rootDir,
		Namespace: ns,
	}, store, nil) // nil embedder — HDC only, no API calls
	if engErr != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", engErr)
		os.Exit(1)
	}

	ctx := context.Background()
	cm, idx := engine.GetCodeMap(ctx, ns)
	if cm == nil {
		fmt.Fprintln(os.Stderr, "error: could not generate code map")
		os.Exit(1)
	}

	printSearchResults(cm, idx, query, *limit, *jsonOut, *moduleOnly)
}

func printSearchResults(cm *dualmem.CodeMap, idx *dualmem.CodeIndex, query string, limit int, jsonOut bool, moduleOnly bool) {
	if !moduleOnly {
		fileResults := dualmem.SearchCodeMapFiles(cm, idx, query, limit)
		if jsonOut {
			json.NewEncoder(os.Stdout).Encode(fileResults)
			return
		}
		for i, r := range fileResults {
			fmt.Printf("  %d. %-35s score=%.4f (module: %s %.2f, file: %.2f)\n",
				i+1, r.FilePath, r.CombinedScore, r.ModulePath, r.ModuleScore, r.FileScore)
		}
		return
	}

	results := dualmem.SearchCodeMap(cm, idx, query, limit)
	if jsonOut {
		json.NewEncoder(os.Stdout).Encode(results)
		return
	}
	for i, r := range results {
		fmt.Printf("  %d. %-30s score=%.4f (hdc=%.2f kw=%.2f)  %s\n",
			i+1, r.Path, r.HybridScore, r.Similarity, r.KeywordScore, r.Summary)
		if len(r.KeyTypes) > 0 {
			fmt.Printf("     Types: %s\n", strings.Join(r.KeyTypes, ", "))
		}
		if len(r.EntryPoints) > 0 {
			fmt.Printf("     Entry: %s\n", strings.Join(r.EntryPoints, ", "))
		}
		if len(r.Imports) > 0 && len(r.Imports) <= 8 {
			fmt.Printf("     Imports: %s\n", strings.Join(r.Imports, ", "))
		}
	}
}
```

**Dependency note:** This task references `dualmem.SearchCodeMapFiles` which is implemented in Task 4. **Do not attempt to build until Task 4 is complete.** Implement Tasks 1, 3, 4 first, then Task 2.

Also check before implementing:
- `NewEngine` accepts nil embedder (used in tests). If it requires non-nil, add a guard.
- `SQLiteStore` has a `Close()` method. If not, add `func (s *SQLiteStore) Close() error { return s.db.Close() }` to `dualmem/store_sqlite.go`.

- [ ] **Step 2: Verify the code compiles**

Run: `cd /Users/donny/Projects/2026/geoffreyengram && go build ./cmd/dualmem/`
Expected: Build succeeds (after Task 4 implements `SearchCodeMapFiles`). If building at this point, stub `SearchCodeMapFiles` first — see Task 4 Step 1.

- [ ] **Step 3: Test cache behavior manually**

```bash
cd /Users/donny/Projects/2026/geoffreyengram
time ~/go/bin/dualmem search-code "HDC encoding"     # cold — should scan
time ~/go/bin/dualmem search-code "HDC encoding"     # warm — should be fast
```

Expected: Second run significantly faster (<1s vs several seconds on this repo).

- [ ] **Step 4: Run full test suite**

Run: `cd /Users/donny/Projects/2026/geoffreyengram && go test ./... -count=1`
Expected: All tests pass.

- [ ] **Step 5: Commit**

```bash
git add cmd/dualmem/main.go dualmem/store_sqlite.go
git commit -m "feat(search-code): wire CLI through Engine for codemap caching

search-code now uses the SQLite-backed codemap cache via Engine.
Warm queries skip ScanCodebase entirely. Cache invalidates on new
git commits. Adds --module-only flag for backward compat."
```

---

### Task 3: Add `FileInfo` type and per-file metadata collection

**Files:**
- Modify: `dualmem/codemap.go` (FileInfo type, parseGoPackage)
- Modify: `dualmem/parse_treesitter.go` (parseWithTreeSitter, parseTSModuleSplit)
- Test: `dualmem/codemap_test.go`

- [ ] **Step 1: Write test for per-file metadata in Go packages**

In `dualmem/codemap_test.go`, add:

```go
func TestScanCodebase_GoPerFileInfo(t *testing.T) {
	tmpDir := t.TempDir()

	// Two Go files in same package
	pkgDir := filepath.Join(tmpDir, "engine")
	os.MkdirAll(pkgDir, 0755)

	writeFile(t, pkgDir, "search.go", `package engine

import "strings"

// Search finds results.
func Search(query string) []string {
	return strings.Split(query, " ")
}

func tokenize(s string) []string {
	return nil
}
`)

	writeFile(t, pkgDir, "index.go", `package engine

// Index builds a search index.
type Index struct {
	Name string
}

func BuildIndex(docs []string) *Index {
	return &Index{}
}

func normalize(s string) string {
	return s
}
`)

	cm, err := ScanCodebase(tmpDir)
	if err != nil {
		t.Fatalf("ScanCodebase: %v", err)
	}

	var engineMod *ModuleMap
	for i := range cm.Zoom2 {
		if strings.Contains(cm.Zoom2[i].Path, "engine") {
			engineMod = &cm.Zoom2[i]
			break
		}
	}
	if engineMod == nil {
		t.Fatal("expected engine/ module")
	}

	if len(engineMod.Files) != 2 {
		t.Fatalf("expected 2 FileInfo entries, got %d", len(engineMod.Files))
	}

	// Check that each file has its own identifiers
	filesByName := map[string]*FileInfo{}
	for i := range engineMod.Files {
		base := filepath.Base(engineMod.Files[i].RelPath)
		filesByName[base] = &engineMod.Files[i]
	}

	searchFile := filesByName["search.go"]
	if searchFile == nil {
		t.Fatal("expected FileInfo for search.go")
	}
	// Should contain "tokenize" as identifier (unexported func)
	found := false
	for _, id := range searchFile.Identifiers {
		if id == "tokenize" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("search.go should contain identifier 'tokenize', got: %v", searchFile.Identifiers)
	}

	indexFile := filesByName["index.go"]
	if indexFile == nil {
		t.Fatal("expected FileInfo for index.go")
	}
	found = false
	for _, id := range indexFile.Identifiers {
		if id == "normalize" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("index.go should contain identifier 'normalize', got: %v", indexFile.Identifiers)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/donny/Projects/2026/geoffreyengram && go test ./dualmem/ -run TestScanCodebase_GoPerFileInfo -v`
Expected: FAIL — `FileInfo` type doesn't exist yet, `Files` field missing from `ModuleMap`.

- [ ] **Step 3: Add `FileInfo` type and `Files` field to `ModuleMap`**

In `dualmem/codemap.go`, after the `ModuleMap` struct definition (around line 38), add the `FileInfo` type and the `Files` field:

```go
// FileInfo holds per-file metadata collected during scanning.
// Used by stage-2 file search to rank individual files within a module.
type FileInfo struct {
	RelPath     string   `json:"rel_path"`    // relative to repo root, e.g. "dualmem/hdc.go"
	Identifiers []string `json:"identifiers"` // all symbols (exported + unexported) in this file
	Imports     []string `json:"imports"`      // imports in this file
}
```

Add to `ModuleMap`:

```go
type ModuleMap struct {
	Path        string     `json:"path"`
	Language    string     `json:"language"`
	Summary     string     `json:"summary"`
	KeyTypes    []string   `json:"key_types"`
	EntryPoints []string   `json:"entry_points"`
	FileCount   int        `json:"file_count"`
	Imports     []string   `json:"imports,omitempty"`
	Identifiers []string   `json:"identifiers,omitempty"`
	Files       []FileInfo `json:"files,omitempty"`
}
```

- [ ] **Step 4: Modify `parseGoPackage` to collect per-file metadata**

In `dualmem/codemap.go`, modify `parseGoPackage()` to collect identifiers per-file. The key change is inside the `for _, file := range pkg.Files` loop — track per-file identifiers alongside the existing module-level aggregation.

After the existing loop over `pkg.Files` (which collects module-level types/entryPoints/imports/identifiers), add a second pass that collects per-file data:

```go
// Per-file metadata for stage-2 ranking
var fileInfos []FileInfo
for filePath, file := range pkg.Files {
	var fileIdents []string
	fileIdentSeen := make(map[string]bool)

	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				if ts, ok := spec.(*ast.TypeSpec); ok {
					name := ts.Name.Name
					if !fileIdentSeen[name] {
						fileIdentSeen[name] = true
						fileIdents = append(fileIdents, name)
					}
				}
			}
		case *ast.FuncDecl:
			name := d.Name.Name
			if d.Recv == nil && !fileIdentSeen[name] {
				fileIdentSeen[name] = true
				fileIdents = append(fileIdents, name)
			}
		}
	}

	var fileImports []string
	for _, imp := range file.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		fileImports = append(fileImports, path)
	}

	// Use relative path from root dir
	rel := filePath
	if idx := strings.Index(filePath, relPath); idx >= 0 {
		rel = filePath[idx:]
	}
	// Trim leading slash or absolute prefix — we want "engine/search.go" style
	rel = filepath.Base(filePath)
	if relPath != "." {
		rel = relPath + "/" + rel
	}

	if len(fileIdents) > 20 {
		fileIdents = fileIdents[:20]
	}

	fileInfos = append(fileInfos, FileInfo{
		RelPath:     rel,
		Identifiers: fileIdents,
		Imports:     fileImports,
	})
}
```

Then include `Files: fileInfos` in the returned `ModuleMap`.

Note: `pkg.Files` is a `map[string]*ast.File` where the key is the absolute file path. The relative path construction must handle this. Test carefully.

- [ ] **Step 5: Run test to verify it passes**

Run: `cd /Users/donny/Projects/2026/geoffreyengram && go test ./dualmem/ -run TestScanCodebase_GoPerFileInfo -v`
Expected: PASS

- [ ] **Step 6: Add per-file metadata to tree-sitter parsers**

Modify `parseWithTreeSitter()` in `dualmem/parse_treesitter.go` to collect per-file identifiers. The function already iterates `files` one at a time (line 280). For each file, collect identifiers into a `FileInfo` and append to a slice.

Add a `fileInfos []FileInfo` accumulator before the `for _, f := range files` loop. Inside the loop, after extracting types/entries/imports/idents for the module, also build per-file data:

```go
// Per-file metadata (collected alongside module-level)
var perFileIdents []string
perFileIdentSeen := make(map[string]bool)

// After each extractCaptures call for types/entries/idents,
// also collect into perFileIdents using the same callback pattern:
```

The simplest approach: after the existing extraction for each file `f`, do a second pass over the same parse tree collecting ALL identifiers (types + entries + idents) into `perFileIdents`. Then create:

```go
rel := filepath.Base(f)
if relPath != "." {
	rel = relPath + "/" + rel
}
fileInfos = append(fileInfos, FileInfo{
	RelPath:     rel,
	Identifiers: perFileIdents,
	Imports:     perFileImports,
})
```

Add `Files: fileInfos` to the returned `ModuleMap`.

- [ ] **Step 7: Write test for TS per-file metadata**

In `dualmem/codemap_test.go`, add a test that creates a temp dir with two TS files (both below significance threshold), scans, and verifies each has its own `FileInfo` entry with correct identifiers.

- [ ] **Step 8: Run all tests**

Run: `cd /Users/donny/Projects/2026/geoffreyengram && go test ./dualmem/... -count=1`
Expected: All pass. Verify existing codemap tests still pass — the `Files` field is `omitempty` so JSON serialization is backward compatible.

- [ ] **Step 9: Commit**

```bash
git add dualmem/codemap.go dualmem/codemap_test.go dualmem/parse_treesitter.go
git commit -m "feat(codemap): collect per-file metadata during scanning

Adds FileInfo type with per-file identifiers and imports.
parseGoPackage and parseWithTreeSitter now populate Files
on each ModuleMap. Used by stage-2 file search."
```

---

### Task 4: Implement `SearchCodeMapFiles()` (two-stage file search)

**Files:**
- Modify: `dualmem/codemap.go` (FileResult type, SearchCodeMapFiles function)
- Test: `dualmem/codemap_test.go`

- [ ] **Step 1: Write test for file-level search**

In `dualmem/codemap_test.go`, add:

```go
func TestSearchCodeMapFiles_RanksCorrectFile(t *testing.T) {
	// Build a CodeMap with one module containing multiple files
	cm := &CodeMap{
		Zoom2: []ModuleMap{
			{
				Path:        "engine/",
				Language:    "go",
				Summary:     "Go package engine",
				KeyTypes:    []string{"struct Index", "struct Query"},
				EntryPoints: []string{"Search()", "BuildIndex()"},
				Identifiers: []string{"tokenize", "normalize", "rank"},
				FileCount:   3,
				Files: []FileInfo{
					{
						RelPath:     "engine/search.go",
						Identifiers: []string{"Search", "tokenize", "rank"},
					},
					{
						RelPath:     "engine/index.go",
						Identifiers: []string{"Index", "BuildIndex", "normalize"},
					},
					{
						RelPath:     "engine/utils.go",
						Identifiers: []string{"formatOutput", "logDebug"},
					},
				},
			},
			{
				Path:        "cmd/",
				Language:    "go",
				Summary:     "Go binary (package main)",
				KeyTypes:    []string{},
				EntryPoints: []string{"main()"},
				Identifiers: []string{"run", "parseFlags"},
				FileCount:   1,
				Files: []FileInfo{
					{
						RelPath:     "cmd/main.go",
						Identifiers: []string{"main", "run", "parseFlags"},
					},
				},
			},
		},
	}

	idx := BuildCodeIndex(cm)
	results := SearchCodeMapFiles(cm, idx, "search tokenize rank", 5)

	if len(results) == 0 {
		t.Fatal("expected file results")
	}

	// engine/search.go should rank first — it has all three query terms
	if results[0].FilePath != "engine/search.go" {
		t.Errorf("expected engine/search.go as top result, got %s", results[0].FilePath)
	}

	// All results should have non-zero combined scores
	for _, r := range results {
		if r.CombinedScore <= 0 {
			t.Errorf("result %s has zero combined score", r.FilePath)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/donny/Projects/2026/geoffreyengram && go test ./dualmem/ -run TestSearchCodeMapFiles -v`
Expected: FAIL — `FileResult` and `SearchCodeMapFiles` don't exist yet.

- [ ] **Step 3: Implement `FileResult` type and `SearchCodeMapFiles()`**

In `dualmem/codemap.go`, after `SearchCodeMapCompat` (around line 815), add:

```go
// FileResult is a file-level search result from two-stage search.
type FileResult struct {
	ModulePath    string  `json:"module_path"`
	FilePath      string  `json:"file_path"`
	FileScore     float64 `json:"file_score"`     // stage-2 BM25 score
	ModuleScore   float64 `json:"module_score"`    // stage-1 hybrid score
	CombinedScore float64 `json:"combined_score"`  // weighted blend
	Summary       string  `json:"summary"`
}

// SearchCodeMapFiles performs two-stage search: module-level (stage 1) then
// file-level BM25 ranking within top modules (stage 2).
func SearchCodeMapFiles(cm *CodeMap, idx *CodeIndex, query string, limit int) []FileResult {
	if cm == nil || idx == nil {
		return nil
	}

	// Stage 1: module-level search
	topK := 5
	if topK > len(cm.Zoom2) {
		topK = len(cm.Zoom2)
	}
	moduleResults := SearchCodeMap(cm, idx, query, topK)

	queryTokens := hdcTokenize(query)

	// Stage 2: rank files within each top module
	var fileResults []FileResult

	for _, mr := range moduleResults {
		if len(mr.Files) == 0 {
			// Module has no per-file info — emit as single file result
			fileResults = append(fileResults, FileResult{
				ModulePath:    mr.Path,
				FilePath:      mr.Path,
				FileScore:     0,
				ModuleScore:   mr.HybridScore,
				CombinedScore: mr.HybridScore,
				Summary:       mr.Summary,
			})
			continue
		}

		for _, fi := range mr.Files {
			fileScore := fileBM25Score(fi, queryTokens)
			combined := 0.6*mr.HybridScore + 0.4*fileScore
			fileResults = append(fileResults, FileResult{
				ModulePath:    mr.Path,
				FilePath:      fi.RelPath,
				FileScore:     fileScore,
				ModuleScore:   mr.HybridScore,
				CombinedScore: combined,
				Summary:       mr.Summary,
			})
		}
	}

	// Sort by combined score descending
	sort.Slice(fileResults, func(i, j int) bool {
		return fileResults[i].CombinedScore > fileResults[j].CombinedScore
	})

	if limit > 0 && len(fileResults) > limit {
		fileResults = fileResults[:limit]
	}
	return fileResults
}

// fileBM25Score computes a simple BM25-like score for a file against query tokens.
// Uses the file's identifiers + filename tokens as the document.
func fileBM25Score(fi FileInfo, queryTokens []string) float64 {
	// Build term frequency from identifiers + filename
	tf := make(map[string]int)
	for _, ident := range fi.Identifiers {
		for _, tok := range hdcTokenize(ident) {
			tf[tok]++
		}
	}
	for _, tok := range hdcTokenize(fi.RelPath) {
		tf[tok]++
	}

	docLen := 0
	for _, count := range tf {
		docLen += count
	}
	if docLen == 0 {
		return 0
	}

	avgDocLen := 10.0 // reasonable default for file-level docs

	var score float64
	for _, qt := range queryTokens {
		termFreq := float64(tf[qt])
		if termFreq == 0 {
			continue
		}
		// Simplified BM25 — no IDF (we don't have corpus-level file stats)
		// Use term frequency saturation only
		num := termFreq * (bm25K1 + 1)
		denom := termFreq + bm25K1*(1-bm25B+bm25B*float64(docLen)/avgDocLen)
		score += num / denom
	}

	// Normalize to [0, 1] range — cap at a reasonable max
	maxPossible := float64(len(queryTokens)) * (bm25K1 + 1) / (1 + bm25K1*(1-bm25B))
	if maxPossible > 0 {
		score = score / maxPossible
	}
	return score
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /Users/donny/Projects/2026/geoffreyengram && go test ./dualmem/ -run TestSearchCodeMapFiles -v`
Expected: PASS — `engine/search.go` ranks first.

- [ ] **Step 5: Add edge case test — modules without FileInfo**

```go
func TestSearchCodeMapFiles_ModulesWithoutFiles(t *testing.T) {
	cm := &CodeMap{
		Zoom2: []ModuleMap{
			{
				Path:        "docs/",
				Language:    "other",
				Summary:     "4 files",
				FileCount:   4,
				// No Files field — should still appear in results
			},
		},
	}
	idx := BuildCodeIndex(cm)
	results := SearchCodeMapFiles(cm, idx, "documentation", 5)

	if len(results) == 0 {
		t.Fatal("expected at least one result even without FileInfo")
	}
	if results[0].FilePath != "docs/" {
		t.Errorf("expected docs/ as fallback, got %s", results[0].FilePath)
	}
}
```

- [ ] **Step 6: Run all tests**

Run: `cd /Users/donny/Projects/2026/geoffreyengram && go test ./dualmem/... -count=1`
Expected: All pass.

- [ ] **Step 7: Commit**

```bash
git add dualmem/codemap.go dualmem/codemap_test.go
git commit -m "feat(codemap): two-stage file-level search

SearchCodeMapFiles ranks individual files within top-K modules
using BM25 against per-file identifiers. Combined score blends
module-level (60%) and file-level (40%) signals."
```

---

### Task 5: Markdown doc indexing

**Files:**
- Modify: `dualmem/codemap.go` (parseMarkdownFile, ScanCodebase md collection)
- Test: `dualmem/codemap_test.go`

- [ ] **Step 1: Write test for `parseMarkdownFile`**

In `dualmem/codemap_test.go`, add:

```go
func TestParseMarkdownFile(t *testing.T) {
	tmpDir := t.TempDir()

	content := `# Getting Started

This guide explains how to set up the **consent flow** for new users.

## Authentication

Use the ` + "`" + `AuthProvider` + "`" + ` component to handle OAuth.

## Configuration

Set **API_KEY** in your ` + "`" + `.env` + "`" + ` file.

### Advanced Options

Enable ` + "`" + `DEBUG_MODE` + "`" + ` for verbose logging.
`

	writeFile(t, tmpDir, "setup.md", content)

	mod := parseMarkdownFile("docs/setup.md", filepath.Join(tmpDir, "setup.md"))
	if mod == nil {
		t.Fatal("expected non-nil ModuleMap")
	}

	if mod.Language != "markdown" {
		t.Errorf("language = %q, want markdown", mod.Language)
	}
	if mod.Path != "docs/setup.md" {
		t.Errorf("path = %q, want docs/setup.md", mod.Path)
	}
	if mod.FileCount != 1 {
		t.Errorf("file_count = %d, want 1", mod.FileCount)
	}

	// Should have identifiers from headings, bold, and backticks
	identSet := make(map[string]bool)
	for _, id := range mod.Identifiers {
		identSet[id] = true
	}

	// Headings
	if !identSet["Getting Started"] {
		t.Error("expected heading 'Getting Started' in identifiers")
	}
	if !identSet["Authentication"] {
		t.Error("expected heading 'Authentication' in identifiers")
	}

	// Bold terms
	if !identSet["consent flow"] {
		t.Error("expected bold term 'consent flow' in identifiers")
	}
	if !identSet["API_KEY"] {
		t.Error("expected bold term 'API_KEY' in identifiers")
	}

	// Backtick code refs
	if !identSet["AuthProvider"] {
		t.Error("expected code ref 'AuthProvider' in identifiers")
	}
	if !identSet["DEBUG_MODE"] {
		t.Error("expected code ref 'DEBUG_MODE' in identifiers")
	}

	// Summary should be first non-empty paragraph
	if !strings.Contains(mod.Summary, "consent flow") {
		t.Errorf("summary should contain first paragraph content, got: %s", mod.Summary)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/donny/Projects/2026/geoffreyengram && go test ./dualmem/ -run TestParseMarkdownFile -v`
Expected: FAIL — `parseMarkdownFile` doesn't exist.

- [ ] **Step 3: Implement `parseMarkdownFile`**

In `dualmem/codemap.go`, add before `synthesizeZoom1`:

```go
import "regexp"

// Regex patterns for markdown extraction.
var (
	mdHeadingRe  = regexp.MustCompile(`(?m)^#{1,4}\s+(.+)$`)
	mdBoldRe     = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	mdBacktickRe = regexp.MustCompile("`([^`]+)`")
)

// parseMarkdownFile extracts identifiers from a markdown file for search indexing.
func parseMarkdownFile(relPath string, absPath string) *ModuleMap {
	data, err := os.ReadFile(absPath)
	if err != nil || len(data) < 100 {
		return nil
	}
	content := string(data)

	var identifiers []string
	seen := make(map[string]bool)

	addIdent := func(s string) {
		s = strings.TrimSpace(s)
		if s != "" && !seen[s] {
			seen[s] = true
			identifiers = append(identifiers, s)
		}
	}

	// Extract headings
	for _, match := range mdHeadingRe.FindAllStringSubmatch(content, -1) {
		addIdent(match[1])
	}

	// Extract bold terms
	for _, match := range mdBoldRe.FindAllStringSubmatch(content, -1) {
		addIdent(match[1])
	}

	// Extract backtick code references
	for _, match := range mdBacktickRe.FindAllStringSubmatch(content, -1) {
		addIdent(match[1])
	}

	if len(identifiers) > 30 {
		identifiers = identifiers[:30]
	}

	// Summary: first non-empty, non-heading line
	var summary string
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		summary = line
		break
	}
	if len(summary) > 100 {
		summary = summary[:100]
	}

	return &ModuleMap{
		Path:        relPath,
		Language:    "markdown",
		Summary:     summary,
		FileCount:   1,
		Identifiers: identifiers,
		Files: []FileInfo{
			{
				RelPath:     relPath,
				Identifiers: identifiers,
			},
		},
	}
}
```

Note: Add `"regexp"` to the imports at the top of `codemap.go`.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /Users/donny/Projects/2026/geoffreyengram && go test ./dualmem/ -run TestParseMarkdownFile -v`
Expected: PASS

- [ ] **Step 5: Integrate markdown collection into `ScanCodebase`**

Modify `ScanCodebase()` in `dualmem/codemap.go`. In the `dirInfo` struct, add `mdFiles []string`. In the filepath.Walk switch block (around line 114-124), add:

```go
case ".md":
	d.mdFiles = append(d.mdFiles, filepath.Join(dir, info.Name()))
```

In the module building loop (around line 130-157), after the language-specific switch cases, add a new block to handle directories with only markdown files, and also handle markdown files that exist alongside source code:

```go
// After the existing switch block, process markdown files in all dirs
for _, d := range dirs {
	for _, mdFile := range d.mdFiles {
		relFile := filepath.Base(mdFile)
		relPath := d.relPath
		if relPath == "." {
			relPath = relFile
		} else {
			relPath = relPath + "/" + relFile
		}
		mod := parseMarkdownFile(relPath, mdFile)
		if mod != nil {
			modules = append(modules, *mod)
		}
	}
}
```

This creates a separate loop after the main module-building loop. Markdown files always become their own module entries regardless of what other source files are in the directory.

- [ ] **Step 6: Write integration test for docs in search results**

```go
func TestScanCodebase_MarkdownIndexed(t *testing.T) {
	tmpDir := t.TempDir()

	writeFile(t, tmpDir, "main.go", `package main
func main() {}
`)

	docsDir := filepath.Join(tmpDir, "docs")
	os.MkdirAll(docsDir, 0755)
	writeFile(t, docsDir, "guide.md", `# Setup Guide

This guide explains how to configure the **authentication** system.

## Installation

Use ` + "`" + `npm install` + "`" + ` to get started.

## Configuration

Set the **API_KEY** environment variable before running the server.
This is required for the OAuth flow to work correctly with the identity provider.
`)

	writeFile(t, tmpDir, "README.md", `# My Project

A tool for managing **user accounts** and ` + "`" + `sessions` + "`" + `.

This project provides authentication and authorization services for web applications.
It supports OAuth 2.0, SAML, and custom identity providers.
`)

	cm, err := ScanCodebase(tmpDir)
	if err != nil {
		t.Fatalf("ScanCodebase: %v", err)
	}

	// Should find markdown modules
	var foundGuide, foundReadme bool
	for _, m := range cm.Zoom2 {
		if strings.Contains(m.Path, "guide.md") {
			foundGuide = true
			if m.Language != "markdown" {
				t.Errorf("guide.md language = %q, want markdown", m.Language)
			}
		}
		if strings.Contains(m.Path, "README.md") {
			foundReadme = true
		}
	}
	if !foundGuide {
		t.Error("expected docs/guide.md in modules")
	}
	if !foundReadme {
		t.Error("expected README.md in modules")
	}

	// Search should find docs
	idx := BuildCodeIndex(cm)
	results := SearchCodeMap(cm, idx, "authentication API_KEY configuration", 10)

	foundDocInResults := false
	for _, r := range results {
		if strings.HasSuffix(r.Path, ".md") {
			foundDocInResults = true
			break
		}
	}
	if !foundDocInResults {
		t.Error("expected at least one .md file in search results for auth query")
	}
}
```

- [ ] **Step 7: Run all tests**

Run: `cd /Users/donny/Projects/2026/geoffreyengram && go test ./dualmem/... -count=1`
Expected: All pass.

- [ ] **Step 8: Commit**

```bash
git add dualmem/codemap.go dualmem/codemap_test.go
git commit -m "feat(codemap): index markdown docs alongside source code

ScanCodebase now collects .md files and creates ModuleMap entries
with identifiers extracted from headings, bold terms, and backtick
code references. Docs participate in the same HDC+BM25 pipeline."
```

---

### Task 6: Wire CLI output format and rebuild binary

**Files:**
- Modify: `cmd/dualmem/main.go` (ensure printSearchResults compiles)
- Test: manual CLI test

- [ ] **Step 1: Verify full build**

Run: `cd /Users/donny/Projects/2026/geoffreyengram && go build ./cmd/dualmem/`
Expected: Build succeeds. If there are compilation errors, fix them — common issues:
- `store.Close()` missing on SQLiteStore — add it
- `NewEngine` signature mismatch — check what args it needs
- Import paths

- [ ] **Step 2: Install updated binary**

Run: `cd /Users/donny/Projects/2026/geoffreyengram && go install ./cmd/dualmem/`

- [ ] **Step 3: Test on geoffreyengram**

```bash
cd /Users/donny/Projects/2026/geoffreyengram
~/go/bin/dualmem search-code "HDC encoding vectors"
~/go/bin/dualmem search-code "HDC encoding vectors" --module-only
```

Expected: Default output shows file-level results like `dualmem/hdc.go`. `--module-only` shows module-level results like `dualmem/`.

- [ ] **Step 4: Test that markdown docs appear**

```bash
cd /Users/donny/Projects/2026/geoffreyengram
~/go/bin/dualmem search-code "benchmark findings improvements"
```

Expected: `docs/` markdown files appear in results if they match.

- [ ] **Step 5: Test cache (second run should be faster)**

```bash
time ~/go/bin/dualmem search-code "codemap scan cache"
time ~/go/bin/dualmem search-code "codemap scan cache"
```

Expected: Second run noticeably faster.

- [ ] **Step 6: Run full test suite**

Run: `cd /Users/donny/Projects/2026/geoffreyengram && go test ./... -count=1`
Expected: All pass.

- [ ] **Step 7: Commit**

```bash
git add cmd/dualmem/main.go
git commit -m "feat(search-code): file-level output, --module-only flag, cache integration

search-code now defaults to file-level results via SearchCodeMapFiles.
Use --module-only for backward-compatible module output. Queries go
through Engine for SQLite-backed codemap caching."
```

---

### Task 7: Re-run benchmarks and verify improvements

**Files:**
- No code changes — benchmark verification only

- [ ] **Step 1: Run benchmark on geoffreyengram**

```bash
cd /Users/donny/Projects/2026/geoffreyengram
go run ./benchmarks/contextual/ --corpus geoffreyengram --adapter dualmem
```

Check P@3, NDCG@10, latency. Compare against baseline (P@3=0.19, NDCG=0.35, latency=42ms).

- [ ] **Step 2: Run benchmark on LearnCard (warm cache)**

```bash
cd /Users/donny/Work/LearnCard
# First run: cold (will be slow)
~/go/bin/dualmem search-code "consent flow" --root .
# Second run: warm cache — should be <1s
time ~/go/bin/dualmem search-code "consent flow" --root .
```

Verify warm query latency < 1s.

- [ ] **Step 3: Document results**

Update `docs/superpowers/specs/2026-04-06-benchmark-findings.md` with the new numbers in a "Post-Fix Results" section.

- [ ] **Step 4: Final commit**

```bash
git add docs/
git commit -m "docs: add post-fix benchmark results for Phase 4"
```
