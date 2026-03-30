package dualmem

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"math"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// CodeMap is a multi-resolution structural summary of a codebase.
type CodeMap struct {
	Namespace   string      `json:"namespace"`
	RootDir     string      `json:"root_dir"`
	Zoom1       string      `json:"zoom1"`       // System-level (~50 tokens)
	Zoom2       []ModuleMap `json:"zoom2"`        // Per-module (~50-100 tokens each)
	GeneratedAt time.Time   `json:"generated_at"`
	GitCommit   string      `json:"git_commit"`
}

// ModuleMap is a zoom-2 entry: one package or meaningful directory.
type ModuleMap struct {
	Path        string   `json:"path"`                    // Relative path from root
	Language    string   `json:"language"`                 // "go", "typescript", "other"
	Summary     string   `json:"summary"`                  // Human-readable description
	KeyTypes    []string `json:"key_types"`                // Important exported types/interfaces
	EntryPoints []string `json:"entry_points"`             // main funcs, handler registrations
	FileCount   int      `json:"file_count"`
	Imports     []string `json:"imports,omitempty"`        // External package imports
	Identifiers []string `json:"identifiers,omitempty"`   // Unexported/private identifiers (content vocabulary)
}

// Skip patterns for directory scanning.
var skipDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true, "__pycache__": true,
	".next": true, "dist": true, "build": true, ".claude": true, ".vscode": true,
	".idea": true, "coverage": true, ".nyc_output": true, "target": true,
	".nx-cache": true, ".github": true, ".changeset": true, ".superpowers": true,
	".turbo": true, ".cache": true, ".parcel-cache": true, ".output": true,
	".nuxt": true, ".svelte-kit": true, "tmp": true, "temp": true, "logs": true,
	".vexp": true,
}

// skipExactNames filters out non-code content directories by exact base name.
var skipExactNames = map[string]bool{
	"assets": true, "images": true, "icons": true, "fonts": true,
	"sass": true, "scss": true, "styles": true, "css": true,
	"fixtures": true, "snapshots": true, "__snapshots__": true, "__mocks__": true,
}

// ScanCodebase walks a directory tree and builds a multi-resolution code map.
// Uses go/ast for Go, regex heuristics for TypeScript, file counts for everything else.
func ScanCodebase(rootDir string) (*CodeMap, error) {
	rootDir, err := filepath.Abs(rootDir)
	if err != nil {
		return nil, fmt.Errorf("codemap: abs path: %w", err)
	}

	// Collect source directories
	type dirInfo struct {
		relPath  string
		goFiles  []string
		tsFiles  []string
		pyFiles  []string
		rsFiles  []string
		allFiles []string
	}
	dirs := make(map[string]*dirInfo)
	maxDepth := 5
	maxDirs := 200

	filepath.Walk(rootDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip errors
		}
		if info.IsDir() {
			if skipDirs[info.Name()] || skipExactNames[info.Name()] {
				return filepath.SkipDir
			}
			rel, _ := filepath.Rel(rootDir, path)
			depth := strings.Count(rel, string(os.PathSeparator))
			if depth > maxDepth {
				return filepath.SkipDir
			}
			if len(dirs) >= maxDirs {
				return filepath.SkipDir
			}
			return nil
		}

		dir := filepath.Dir(path)
		relDir, _ := filepath.Rel(rootDir, dir)
		if relDir == "" {
			relDir = "."
		}

		d, ok := dirs[relDir]
		if !ok {
			d = &dirInfo{relPath: relDir}
			dirs[relDir] = d
		}

		ext := filepath.Ext(info.Name())
		d.allFiles = append(d.allFiles, info.Name())
		switch ext {
		case ".go":
			d.goFiles = append(d.goFiles, filepath.Join(dir, info.Name()))
		case ".ts", ".tsx":
			d.tsFiles = append(d.tsFiles, filepath.Join(dir, info.Name()))
		case ".py":
			d.pyFiles = append(d.pyFiles, filepath.Join(dir, info.Name()))
		case ".rs":
			d.rsFiles = append(d.rsFiles, filepath.Join(dir, info.Name()))
		}
		return nil
	})

	// Build module maps for directories with source code
	var modules []ModuleMap
	for _, d := range dirs {
		var mod *ModuleMap
		switch {
		case len(d.goFiles) > 0:
			mod = parseGoPackage(d.relPath, d.goFiles)
		case len(d.tsFiles) > 0:
			// TypeScript: split significant files into own modules
			mods := parseTSModuleSplit(d.relPath, d.tsFiles)
			modules = append(modules, mods...)
			continue
		case len(d.pyFiles) > 0:
			mod = parsePythonModule(d.relPath, d.pyFiles)
		case len(d.rsFiles) > 0:
			mod = parseRustModule(d.relPath, d.rsFiles)
		default:
			if isInterestingDir(d.relPath) {
				mod = &ModuleMap{
					Path:      d.relPath + "/",
					Language:  detectLanguage(d.allFiles),
					FileCount: len(d.allFiles),
					Summary:   fmt.Sprintf("%d files", len(d.allFiles)),
				}
			}
		}
		if mod != nil {
			modules = append(modules, *mod)
		}
	}

	// Sort by path for stable output
	sort.Slice(modules, func(i, j int) bool {
		return modules[i].Path < modules[j].Path
	})

	zoom1 := synthesizeZoom1(modules, rootDir)

	return &CodeMap{
		RootDir:     rootDir,
		Zoom1:       zoom1,
		Zoom2:       modules,
		GeneratedAt: time.Now(),
	}, nil
}

// --- Go AST parser ---

func parseGoPackage(relPath string, goFiles []string) *ModuleMap {
	if len(goFiles) == 0 {
		return nil
	}

	fset := token.NewFileSet()
	dir := filepath.Dir(goFiles[0])

	pkgs, err := parser.ParseDir(fset, dir, func(fi os.FileInfo) bool {
		// Skip test files for the map
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, parser.ParseComments)
	if err != nil {
		return &ModuleMap{
			Path:      relPath + "/",
			Language:  "go",
			FileCount: len(goFiles),
			Summary:   fmt.Sprintf("Go package (%d files, parse error)", len(goFiles)),
		}
	}

	var types []string
	var entryPoints []string
	var imports []string
	var identifiers []string
	var pkgName string
	isMain := false
	importSeen := make(map[string]bool)
	identSeen := make(map[string]bool)

	for name, pkg := range pkgs {
		pkgName = name
		if name == "main" {
			isMain = true
		}

		for _, file := range pkg.Files {
			for _, decl := range file.Decls {
				switch d := decl.(type) {
				case *ast.GenDecl:
					if d.Tok == token.IMPORT {
						for _, spec := range d.Specs {
							if imp, ok := spec.(*ast.ImportSpec); ok {
								path := strings.Trim(imp.Path.Value, `"`)
								if !importSeen[path] {
									importSeen[path] = true
									imports = append(imports, path)
								}
							}
						}
					}
					for _, spec := range d.Specs {
						if ts, ok := spec.(*ast.TypeSpec); ok {
							if ts.Name.IsExported() {
								kind := "type"
								switch ts.Type.(type) {
								case *ast.InterfaceType:
									kind = "interface"
								case *ast.StructType:
									kind = "struct"
								}
								types = append(types, fmt.Sprintf("%s %s", kind, ts.Name.Name))
							} else if !identSeen[ts.Name.Name] {
								// Unexported type — content vocabulary
								identSeen[ts.Name.Name] = true
								identifiers = append(identifiers, ts.Name.Name)
							}
						}
					}
				case *ast.FuncDecl:
					if d.Name.Name == "main" {
						entryPoints = append(entryPoints, "main()")
					} else if d.Name.IsExported() && d.Recv == nil {
						// Top-level exported functions (not methods)
						entryPoints = append(entryPoints, d.Name.Name+"()")
					} else if !d.Name.IsExported() && d.Recv == nil && !identSeen[d.Name.Name] {
						// Unexported top-level function — content vocabulary
						identSeen[d.Name.Name] = true
						identifiers = append(identifiers, d.Name.Name)
					}
				}
			}
		}
	}

	// Limit to most important items
	if len(types) > 8 {
		types = types[:8]
	}
	if len(entryPoints) > 6 {
		entryPoints = entryPoints[:6]
	}
	if len(imports) > 15 {
		imports = imports[:15]
	}
	if len(identifiers) > 15 {
		identifiers = identifiers[:15]
	}

	displayPath := relPath + "/"
	if relPath == "." {
		displayPath = "./"
	}

	summary := fmt.Sprintf("Go package %s", pkgName)
	if isMain {
		summary = fmt.Sprintf("Go binary (package main)")
	}

	return &ModuleMap{
		Path:        displayPath,
		Language:    "go",
		Summary:     summary,
		KeyTypes:    types,
		EntryPoints: entryPoints,
		FileCount:   len(goFiles),
		Imports:     imports,
		Identifiers: identifiers,
	}
}

// --- Zoom-1 synthesizer ---

func synthesizeZoom1(modules []ModuleMap, rootDir string) string {
	goCount, tsCount, pyCount, rsCount, otherCount := 0, 0, 0, 0, 0
	var clis []string
	var packages []string

	for _, m := range modules {
		switch m.Language {
		case "go":
			goCount++
			if strings.Contains(m.Summary, "binary") || strings.Contains(m.Summary, "main") {
				clis = append(clis, strings.TrimSuffix(m.Path, "/"))
			} else {
				packages = append(packages, strings.TrimSuffix(m.Path, "/"))
			}
		case "typescript":
			tsCount++
		case "python":
			pyCount++
		case "rust":
			rsCount++
		default:
			otherCount++
		}
	}

	var parts []string

	// Language composition
	var langs []string
	if goCount > 0 {
		langs = append(langs, "Go")
	}
	if tsCount > 0 {
		langs = append(langs, "TypeScript")
	}
	if pyCount > 0 {
		langs = append(langs, "Python")
	}
	if rsCount > 0 {
		langs = append(langs, "Rust")
	}
	if len(langs) > 0 {
		parts = append(parts, strings.Join(langs, "+")+" project")
	} else {
		parts = append(parts, "Project")
	}

	// Packages
	if len(packages) > 0 {
		if len(packages) <= 4 {
			parts = append(parts, fmt.Sprintf("packages: %s", strings.Join(packages, ", ")))
		} else {
			parts = append(parts, fmt.Sprintf("%d packages", len(packages)))
		}
	}

	// CLIs/binaries
	if len(clis) > 0 {
		if len(clis) <= 3 {
			parts = append(parts, fmt.Sprintf("binaries: %s", strings.Join(clis, ", ")))
		} else {
			parts = append(parts, fmt.Sprintf("%d binaries", len(clis)))
		}
	}

	return strings.Join(parts, ". ") + "."
}

// --- Render ---

// RenderAtBudget formats the code map within a token budget.
// When queryEmbedding and moduleEmbeddings are provided, sorts by similarity.
// boostPaths optionally boost modules whose paths contain any of the given strings.
// Otherwise falls back to file count (largest first).
func (cm *CodeMap) RenderAtBudget(maxTokens int, queryEmbedding []float32, moduleEmbeddings map[string][]float32, boostPaths []string) string {
	var sb strings.Builder

	sb.WriteString(cm.Zoom1)
	tokensUsed := estimateTokensStr(cm.Zoom1)

	if maxTokens <= tokensUsed+20 {
		return sb.String()
	}

	sorted := make([]ModuleMap, len(cm.Zoom2))
	copy(sorted, cm.Zoom2)

	// Build a set of normalized boost path segments for matching
	boostSet := make(map[string]bool, len(boostPaths))
	for _, bp := range boostPaths {
		// Normalize: extract base filename without extension, lowercase
		base := strings.TrimSuffix(filepath.Base(bp), filepath.Ext(bp))
		if base != "" && base != "." {
			boostSet[strings.ToLower(base)] = true
		}
		// Also keep the directory path for prefix matching
		dir := filepath.Dir(bp)
		if dir != "" && dir != "." {
			boostSet[strings.ToLower(dir)] = true
		}
	}

	const (
		boostFactor       float64 = 0.3
		minSimilarityThreshold    = 0.05
	)

	if queryEmbedding != nil && moduleEmbeddings != nil {
		// Compute effective scores with boost
		type scored struct {
			mod   ModuleMap
			score float64
		}
		scoredMods := make([]scored, len(sorted))
		for i, m := range sorted {
			sim := float64(CosineSimilarity(queryEmbedding, moduleEmbeddings[m.Path]))
			if len(boostSet) > 0 && moduleMatchesBoost(m.Path, boostSet) {
				sim += boostFactor
			}
			scoredMods[i] = scored{mod: m, score: sim}
		}
		sort.Slice(scoredMods, func(i, j int) bool {
			return scoredMods[i].score > scoredMods[j].score
		})

		sb.WriteString("\n")
		for _, sm := range scoredMods {
			if sm.score < minSimilarityThreshold {
				break // skip low-relevance modules
			}
			line := formatModuleMapLine(sm.mod)
			lineTokens := estimateTokensStr(line)
			if tokensUsed+lineTokens > maxTokens {
				break
			}
			sb.WriteString("\n")
			sb.WriteString(line)
			tokensUsed += lineTokens
		}
	} else {
		sort.Slice(sorted, func(i, j int) bool {
			return sorted[i].FileCount > sorted[j].FileCount
		})

		sb.WriteString("\n")
		for _, m := range sorted {
			line := formatModuleMapLine(m)
			lineTokens := estimateTokensStr(line)
			if tokensUsed+lineTokens > maxTokens {
				break
			}
			sb.WriteString("\n")
			sb.WriteString(line)
			tokensUsed += lineTokens
		}
	}

	return sb.String()
}

// moduleMatchesBoost checks if a module path contains any of the boost path segments.
func moduleMatchesBoost(modulePath string, boostSet map[string]bool) bool {
	lowerPath := strings.ToLower(modulePath)
	for bp := range boostSet {
		if strings.Contains(lowerPath, bp) {
			return true
		}
	}
	return false
}

func formatModuleMapLine(m ModuleMap) string {
	parts := []string{fmt.Sprintf("  %s — %s", m.Path, m.Summary)}
	if len(m.KeyTypes) > 0 {
		parts = append(parts, fmt.Sprintf("    Types: %s", strings.Join(m.KeyTypes, ", ")))
	}
	if len(m.EntryPoints) > 0 {
		parts = append(parts, fmt.Sprintf("    Entry: %s", strings.Join(m.EntryPoints, ", ")))
	}
	return strings.Join(parts, "\n")
}

func estimateTokensStr(s string) int {
	return len(s) / 4
}

// --- Helpers ---

func isInterestingDir(relPath string) bool {
	interesting := []string{"cmd", "scripts", "examples", "docs", "config", "migrations"}
	base := filepath.Base(relPath)
	for _, name := range interesting {
		if base == name {
			return true
		}
	}
	return false
}

func detectLanguage(files []string) string {
	counts := map[string]int{}
	for _, f := range files {
		ext := filepath.Ext(f)
		switch ext {
		case ".go":
			counts["go"]++
		case ".ts", ".tsx":
			counts["typescript"]++
		case ".py":
			counts["python"]++
		case ".js", ".jsx":
			counts["javascript"]++
		case ".rs":
			counts["rust"]++
		}
	}
	best := "other"
	bestCount := 0
	for lang, c := range counts {
		if c > bestCount {
			best = lang
			bestCount = c
		}
	}
	return best
}

// MarshalZoom2 serializes the zoom-2 modules to JSON.
func (cm *CodeMap) MarshalZoom2() string {
	b, _ := json.Marshal(cm.Zoom2)
	return string(b)
}

// UnmarshalZoom2 parses zoom-2 modules from JSON.
func UnmarshalZoom2(data string) []ModuleMap {
	var modules []ModuleMap
	json.Unmarshal([]byte(data), &modules)
	return modules
}

// moduleToSummaryText builds a text representation of a module for embedding.
func moduleToSummaryText(m ModuleMap) string {
	var sb strings.Builder
	sb.WriteString(m.Path)
	sb.WriteString(" — ")
	sb.WriteString(m.Summary)
	if len(m.KeyTypes) > 0 {
		sb.WriteString(". Types: ")
		sb.WriteString(strings.Join(m.KeyTypes, ", "))
	}
	if len(m.EntryPoints) > 0 {
		sb.WriteString(". Entry: ")
		sb.WriteString(strings.Join(m.EntryPoints, ", "))
	}
	return sb.String()
}

// HDCEncodeCodeMap produces HDC embeddings for all modules in a CodeMap.
// Deterministic, no API calls — instant local encoding.
// Returns nil if the code map has no modules.
func HDCEncodeCodeMap(cm *CodeMap) map[string][]float32 {
	if len(cm.Zoom2) == 0 {
		return nil
	}
	enc := NewHDCEncoder()
	result := make(map[string][]float32, len(cm.Zoom2))
	for _, m := range cm.Zoom2 {
		result[m.Path] = enc.EncodeModule(m)
	}
	return result
}

// HDCEncodeQuery produces an HDC vector for a query string.
func HDCEncodeQuery(query string) []float32 {
	enc := NewHDCEncoder()
	return enc.EncodeQuery(query)
}

// ModuleResult is a ModuleMap with similarity scores from hybrid search.
type ModuleResult struct {
	ModuleMap
	Similarity   float64 `json:"similarity"`    // HDC cosine score
	KeywordScore float64 `json:"keyword_score"`  // BM25 score
	HybridScore  float64 `json:"hybrid_score"`   // blended final score
}

// BuildCodeIndex produces a CodeIndex with both HDC vectors and BM25 token data.
// This is the primary precomputation entry point for hybrid search.
func BuildCodeIndex(cm *CodeMap) *CodeIndex {
	if cm == nil || len(cm.Zoom2) == 0 {
		return &CodeIndex{}
	}

	idx := &CodeIndex{
		HDCVectors: HDCEncodeCodeMap(cm),
		TokenFreqs: make(map[string]map[string]int, len(cm.Zoom2)),
		DocLens:    make(map[string]int, len(cm.Zoom2)),
		NumDocs:    len(cm.Zoom2),
	}

	// Build per-module token frequencies
	df := make(map[string]int) // document frequency per token
	totalLen := 0
	for _, m := range cm.Zoom2 {
		tf := make(map[string]int)
		// Tokenize all module fields using the same tokenizer as HDC
		for _, tok := range hdcTokenize(m.Path) {
			tf[tok]++
		}
		for _, kt := range m.KeyTypes {
			for _, tok := range hdcTokenizeSymbol(kt) {
				tf[tok]++
			}
		}
		for _, ep := range m.EntryPoints {
			for _, tok := range hdcTokenizeSymbol(ep) {
				tf[tok]++
			}
		}
		for _, ident := range m.Identifiers {
			for _, tok := range hdcTokenize(ident) {
				tf[tok]++
			}
		}
		for _, imp := range m.Imports {
			for _, tok := range hdcTokenize(imp) {
				tf[tok]++
			}
		}

		docLen := 0
		for tok, count := range tf {
			docLen += count
			_ = count // used above
			df[tok]++
		}
		idx.TokenFreqs[m.Path] = tf
		idx.DocLens[m.Path] = docLen
		totalLen += docLen
	}

	// Compute IDF: log(1 + (N - df + 0.5) / (df + 0.5))
	idx.IDF = make(map[string]float64, len(df))
	n := float64(idx.NumDocs)
	for tok, freq := range df {
		idx.IDF[tok] = math.Log(1 + (n-float64(freq)+0.5)/(float64(freq)+0.5))
	}

	if idx.NumDocs > 0 {
		idx.AvgDocLen = float64(totalLen) / float64(idx.NumDocs)
	}

	return idx
}

// SearchCodeMap ranks modules by hybrid HDC+BM25 similarity to a query string.
// Returns up to limit results sorted by descending hybrid score.
func SearchCodeMap(cm *CodeMap, idx *CodeIndex, query string, limit int) []ModuleResult {
	if cm == nil || len(cm.Zoom2) == 0 || idx == nil {
		return nil
	}

	queryVec := HDCEncodeQuery(query)
	queryTokens := hdcTokenize(query)

	// Score all modules
	type scored struct {
		module ModuleMap
		hdc    float64
		bm25   float64
	}
	items := make([]scored, 0, len(cm.Zoom2))
	hdcScores := make([]float64, 0, len(cm.Zoom2))
	maxBM25 := 0.0

	for _, m := range cm.Zoom2 {
		hdc := CosineSimilarity(queryVec, idx.HDCVectors[m.Path])
		bm25 := BM25Score(idx, m.Path, queryTokens)
		items = append(items, scored{module: m, hdc: hdc, bm25: bm25})
		hdcScores = append(hdcScores, hdc)
		if bm25 > maxBM25 {
			maxBM25 = bm25
		}
	}

	// Sort HDC scores descending for adaptive alpha
	sortedHDC := make([]float64, len(hdcScores))
	copy(sortedHDC, hdcScores)
	sort.Float64s(sortedHDC)
	// Reverse to descending
	for i, j := 0, len(sortedHDC)-1; i < j; i, j = i+1, j-1 {
		sortedHDC[i], sortedHDC[j] = sortedHDC[j], sortedHDC[i]
	}

	alpha := AdaptiveAlpha(sortedHDC)

	// If no BM25 signal, use pure HDC
	if maxBM25 == 0 {
		alpha = 1.0
	}

	// Blend and build results
	results := make([]ModuleResult, 0, len(items))
	for _, s := range items {
		hdcNorm := (s.hdc + 1) / 2 // [-1,1] → [0,1]
		bm25Norm := 0.0
		if maxBM25 > 0 {
			bm25Norm = s.bm25 / maxBM25
		}
		hybrid := alpha*hdcNorm + (1-alpha)*bm25Norm

		results = append(results, ModuleResult{
			ModuleMap:    s.module,
			Similarity:   s.hdc,
			KeywordScore: s.bm25,
			HybridScore:  hybrid,
		})
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].HybridScore > results[j].HybridScore
	})
	if limit > 0 && len(results) > limit {
		results = results[:limit]
	}
	return results
}

// SearchCodeMapCompat is a backward-compatible shim that accepts raw HDC vectors.
// Uses HDC-only scoring (no BM25 blend).
func SearchCodeMapCompat(cm *CodeMap, moduleEmbs map[string][]float32, query string, limit int) []ModuleResult {
	if cm == nil || len(cm.Zoom2) == 0 {
		return nil
	}
	queryVec := HDCEncodeQuery(query)
	var results []ModuleResult
	for _, m := range cm.Zoom2 {
		sim := CosineSimilarity(queryVec, moduleEmbs[m.Path])
		results = append(results, ModuleResult{ModuleMap: m, Similarity: sim, HybridScore: sim})
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].HybridScore > results[j].HybridScore
	})
	if limit > 0 && len(results) > limit {
		results = results[:limit]
	}
	return results
}

// EmbedCodeMap embeds all module summaries in a code map.
// Returns nil if embedder is nil (graceful degradation for standalone CLI usage).
func EmbedCodeMap(ctx context.Context, cm *CodeMap, embedder EmbeddingProvider) (map[string]ModuleEmbedding, error) {
	if embedder == nil || len(cm.Zoom2) == 0 {
		return nil, nil
	}

	result := make(map[string]ModuleEmbedding, len(cm.Zoom2))
	for _, m := range cm.Zoom2 {
		summary := moduleToSummaryText(m)
		vec, err := embedder.Embed(ctx, summary, "RETRIEVAL_DOCUMENT")
		if err != nil {
			return nil, fmt.Errorf("codemap: embed module %s: %w", m.Path, err)
		}
		result[m.Path] = ModuleEmbedding{
			Summary:   summary,
			Embedding: vec,
		}
	}
	return result, nil
}

// --- Import Graph & Clustering ---

// BuildImportGraph constructs an undirected import graph from codemap modules.
// Only internal edges (imports between modules in the same codebase) are included.
func BuildImportGraph(modules []ModuleMap) *ImportGraph {
	g := &ImportGraph{
		Nodes: make(map[string]*ModuleMap, len(modules)),
		Adj:   make(map[string][]string, len(modules)),
	}

	// Index modules by path
	for i := range modules {
		g.Nodes[modules[i].Path] = &modules[i]
		g.Adj[modules[i].Path] = nil // ensure entry exists
	}

	// Build edges by matching imports to module paths
	for _, m := range modules {
		for _, imp := range m.Imports {
			target := resolveImportToModule(imp, g.Nodes)
			if target != "" && target != m.Path {
				g.Edges = append(g.Edges, ImportEdge{From: m.Path, To: target})
				// Undirected adjacency
				if !containsStr(g.Adj[m.Path], target) {
					g.Adj[m.Path] = append(g.Adj[m.Path], target)
				}
				if !containsStr(g.Adj[target], m.Path) {
					g.Adj[target] = append(g.Adj[target], m.Path)
				}
			}
		}
	}

	return g
}

// resolveImportToModule matches an import string to a module path.
// Tries exact match first, then suffix match after stripping prefixes.
func resolveImportToModule(imp string, nodes map[string]*ModuleMap) string {
	// Normalize: strip trailing slash for comparison
	normalized := strings.TrimSuffix(imp, "/")

	// Exact match (with or without trailing slash)
	if _, ok := nodes[normalized]; ok {
		return normalized
	}
	if _, ok := nodes[normalized+"/"]; ok {
		return normalized + "/"
	}

	// Strip common prefixes: ./, ../, @scope/pkg/
	cleaned := normalized
	for strings.HasPrefix(cleaned, "./") || strings.HasPrefix(cleaned, "../") {
		if strings.HasPrefix(cleaned, "./") {
			cleaned = cleaned[2:]
		} else {
			cleaned = cleaned[3:]
		}
	}
	// Strip @scope/package/ prefix (e.g., @learncard/auth/provider → auth/provider)
	if strings.HasPrefix(cleaned, "@") {
		parts := strings.SplitN(cleaned, "/", 3)
		if len(parts) >= 3 {
			cleaned = parts[2]
		}
	}

	// Try suffix matching against all module paths
	for path := range nodes {
		pathNorm := strings.TrimSuffix(path, "/")
		if pathNorm == cleaned {
			return path
		}
		// Check if module path ends with the cleaned import
		if strings.HasSuffix(pathNorm, "/"+cleaned) || strings.HasSuffix(pathNorm, cleaned) {
			return path
		}
	}

	// Go module imports: full paths like "github.com/foo/bar/pkg"
	// Match if any module path is a suffix of the import path
	for path := range nodes {
		pathNorm := strings.TrimSuffix(path, "/")
		if pathNorm == "." {
			continue
		}
		if strings.HasSuffix(normalized, "/"+pathNorm) {
			return path
		}
	}

	return "" // external import, no match
}

// DetectClusters finds groups of related modules using connected components.
// Large components (>maxSize) are split; tiny ones (<minSize) are merged with neighbors.
func DetectClusters(graph *ImportGraph) []Cluster {
	const maxClusterSize = 8
	const minClusterSize = 2

	// Find connected components via BFS
	visited := make(map[string]bool)
	var components [][]string

	for path := range graph.Nodes {
		if visited[path] {
			continue
		}
		component := bfsComponent(path, graph.Adj, visited)
		components = append(components, component)
	}

	// Split large components
	var refined [][]string
	for _, comp := range components {
		if len(comp) <= maxClusterSize {
			refined = append(refined, comp)
		} else {
			refined = append(refined, splitLargeComponent(comp, graph, maxClusterSize)...)
		}
	}

	// Merge tiny components into nearest neighbor
	var result []Cluster
	var tiny [][]string
	for _, comp := range refined {
		if len(comp) >= minClusterSize {
			result = append(result, buildCluster(comp, graph))
		} else {
			tiny = append(tiny, comp)
		}
	}

	// Try to merge each tiny component with a result cluster that shares imports
	for _, t := range tiny {
		merged := false
		for i := range result {
			if clusterSharesEdge(t, result[i], graph) {
				for _, path := range t {
					if mod, ok := graph.Nodes[path]; ok {
						result[i].Modules = append(result[i].Modules, *mod)
					}
				}
				result[i].Name = generateClusterName(result[i].Modules)
				merged = true
				break
			}
		}
		if !merged && len(t) > 0 {
			// Keep as its own cluster even though it's small
			result = append(result, buildCluster(t, graph))
		}
	}

	// Sort clusters by size (largest first) for stable output
	sort.Slice(result, func(i, j int) bool {
		return len(result[i].Modules) > len(result[j].Modules)
	})

	return result
}

// bfsComponent performs BFS from a starting node, returning all reachable nodes.
func bfsComponent(start string, adj map[string][]string, visited map[string]bool) []string {
	queue := []string{start}
	visited[start] = true
	var component []string

	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]
		component = append(component, node)

		for _, neighbor := range adj[node] {
			if !visited[neighbor] {
				visited[neighbor] = true
				queue = append(queue, neighbor)
			}
		}
	}
	return component
}

// splitLargeComponent splits a component into subgroups by iteratively
// removing the node with the most connections (highest degree), breaking
// the component into smaller pieces.
func splitLargeComponent(comp []string, graph *ImportGraph, maxSize int) [][]string {
	// Build a local adjacency set for this component
	inComp := make(map[string]bool, len(comp))
	for _, p := range comp {
		inComp[p] = true
	}

	localAdj := make(map[string][]string)
	for _, p := range comp {
		for _, n := range graph.Adj[p] {
			if inComp[n] {
				localAdj[p] = append(localAdj[p], n)
			}
		}
	}

	// Iteratively remove highest-degree nodes until all components are small enough
	removed := make(map[string]bool)
	for i := 0; i < len(comp); i++ {
		// Find connected components of remaining nodes
		subVisited := make(map[string]bool)
		var subs [][]string
		for _, p := range comp {
			if removed[p] || subVisited[p] {
				continue
			}
			sub := bfsWithFilter(p, localAdj, subVisited, removed)
			subs = append(subs, sub)
		}

		// Check if all are small enough
		allSmall := true
		for _, s := range subs {
			if len(s) > maxSize {
				allSmall = false
				break
			}
		}
		if allSmall {
			// Add back removed nodes to their nearest subcomponent
			for r := range removed {
				bestIdx := 0
				bestEdges := 0
				for idx, s := range subs {
					edges := 0
					for _, n := range localAdj[r] {
						if !removed[n] && containsStr(s, n) {
							edges++
						}
					}
					if edges > bestEdges {
						bestEdges = edges
						bestIdx = idx
					}
				}
				if len(subs) > 0 {
					subs[bestIdx] = append(subs[bestIdx], r)
				}
			}
			return subs
		}

		// Find highest-degree node in largest component
		largestIdx := 0
		for idx, s := range subs {
			if len(s) > len(subs[largestIdx]) {
				largestIdx = idx
			}
		}
		highDeg := ""
		highCount := -1
		for _, p := range subs[largestIdx] {
			deg := 0
			for _, n := range localAdj[p] {
				if !removed[n] {
					deg++
				}
			}
			if deg > highCount {
				highCount = deg
				highDeg = p
			}
		}
		if highDeg != "" {
			removed[highDeg] = true
		}
	}

	// Fallback: return original as single component
	return [][]string{comp}
}

func bfsWithFilter(start string, adj map[string][]string, visited, excluded map[string]bool) []string {
	queue := []string{start}
	visited[start] = true
	var result []string

	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]
		result = append(result, node)

		for _, n := range adj[node] {
			if !visited[n] && !excluded[n] {
				visited[n] = true
				queue = append(queue, n)
			}
		}
	}
	return result
}

func buildCluster(paths []string, graph *ImportGraph) Cluster {
	var modules []ModuleMap
	for _, p := range paths {
		if mod, ok := graph.Nodes[p]; ok {
			modules = append(modules, *mod)
		}
	}
	return Cluster{
		Name:    generateClusterName(modules),
		Modules: modules,
	}
}

// generateClusterName creates a descriptive name from the modules in a cluster.
// Uses the common path prefix and dominant types.
func generateClusterName(modules []ModuleMap) string {
	if len(modules) == 0 {
		return "unknown"
	}
	if len(modules) == 1 {
		return strings.TrimSuffix(modules[0].Path, "/")
	}

	// Find common path prefix
	paths := make([]string, len(modules))
	for i, m := range modules {
		paths[i] = strings.TrimSuffix(m.Path, "/")
	}
	prefix := commonPathPrefix(paths)
	if prefix != "" && prefix != "." {
		return strings.TrimSuffix(prefix, "/")
	}

	// No common prefix — use the most central module's last path segment
	// Pick the module with the most types/entry points as "dominant"
	best := modules[0]
	bestScore := 0
	for _, m := range modules {
		score := len(m.KeyTypes) + len(m.EntryPoints)
		if score > bestScore {
			bestScore = score
			best = m
		}
	}
	name := strings.TrimSuffix(best.Path, "/")
	parts := strings.Split(name, "/")
	if len(parts) > 0 {
		return parts[len(parts)-1] + "-system"
	}
	return name
}

func commonPathPrefix(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	parts0 := strings.Split(paths[0], "/")
	var common []string
	for i, part := range parts0 {
		allMatch := true
		for _, p := range paths[1:] {
			pp := strings.Split(p, "/")
			if i >= len(pp) || pp[i] != part {
				allMatch = false
				break
			}
		}
		if allMatch {
			common = append(common, part)
		} else {
			break
		}
	}
	return strings.Join(common, "/")
}

func clusterSharesEdge(paths []string, cluster Cluster, graph *ImportGraph) bool {
	clusterPaths := make(map[string]bool, len(cluster.Modules))
	for _, m := range cluster.Modules {
		clusterPaths[m.Path] = true
	}
	for _, p := range paths {
		for _, n := range graph.Adj[p] {
			if clusterPaths[n] {
				return true
			}
		}
	}
	return false
}

func containsStr(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}

// FormatClusterPrompt creates an LLM prompt for a cluster to generate a semantic description.
func FormatClusterPrompt(cluster Cluster) string {
	var sb strings.Builder
	sb.WriteString("Given these related code modules in a project:\n\n")

	for _, m := range cluster.Modules {
		sb.WriteString(fmt.Sprintf("Module: %s\n", m.Path))
		if len(m.KeyTypes) > 0 {
			sb.WriteString(fmt.Sprintf("  Types: %s\n", strings.Join(m.KeyTypes, ", ")))
		}
		if len(m.EntryPoints) > 0 {
			sb.WriteString(fmt.Sprintf("  Entry points: %s\n", strings.Join(m.EntryPoints, ", ")))
		}
		if len(m.Imports) > 0 {
			// Only show internal imports (ones that reference other modules)
			var internal []string
			for _, imp := range m.Imports {
				for _, other := range cluster.Modules {
					if strings.Contains(imp, strings.TrimSuffix(other.Path, "/")) {
						internal = append(internal, imp)
						break
					}
				}
			}
			if len(internal) > 0 {
				sb.WriteString(fmt.Sprintf("  Internal imports: %s\n", strings.Join(internal, ", ")))
			}
		}
		sb.WriteString("\n")
	}

	sb.WriteString("Based on the types, entry points, and import relationships between these modules, ")
	sb.WriteString("write 2-3 concise sentences describing:\n")
	sb.WriteString("1. What this system/feature does\n")
	sb.WriteString("2. How the components relate to each other\n")
	sb.WriteString("3. The likely data flow between them\n\n")
	sb.WriteString("Be specific about component names. Do not speculate beyond what the code structure shows.")

	return sb.String()
}
