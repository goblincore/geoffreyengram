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
			mod = parseTSModule(d.relPath, d.tsFiles)
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
