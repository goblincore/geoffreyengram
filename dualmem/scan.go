package dualmem

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ScanResult holds the output of a full repo scan: ranked files, all extracted
// tags, the dependency graph, and summary counts.
type ScanResult struct {
	Files     []RankedFile // Files sorted by personalized PageRank score
	AllTags   []Tag        // Every tag extracted across the repo
	Graph     *FileGraph   // The file-level dependency graph
	TagCount  int          // Total number of tags extracted
	FileCount int          // Total number of source files scanned
}

// RepoScanner orchestrates tag extraction, caching, graph construction,
// personalization, and PageRank ranking for a single repository.
type RepoScanner struct {
	RootDir   string       // Absolute path to the repository root
	Namespace string       // Namespace for cache isolation
	Store     *SQLiteStore // SQLite store for tag cache persistence
}

// fileEntry represents a discovered source file with its detected language.
type fileEntry struct {
	absPath string // Absolute filesystem path
	relPath string // Path relative to RootDir
	lang    string // Detected language key (e.g. "go", "javascript")
}

// ScanAndRank performs a full scan pipeline:
//  1. Discover source files
//  2. Extract tags (using cache, re-parsing only changed files)
//  3. Build file-level dependency graph
//  4. Build personalization vector from query + overrides
//  5. Run personalized PageRank and return ranked results
func (s *RepoScanner) ScanAndRank(query string, personalOverrides map[string]float64) (*ScanResult, error) {
	// Step 1: Discover files
	files, err := s.discoverFiles()
	if err != nil {
		return nil, fmt.Errorf("discover files: %w", err)
	}
	if len(files) == 0 {
		return &ScanResult{}, nil
	}

	// Step 2: Extract tags (cached)
	allTags, err := s.extractTagsCached(files)
	if err != nil {
		return nil, fmt.Errorf("extract tags: %w", err)
	}

	// Step 3: Build dependency graph
	graph := BuildFileGraph(allTags)

	// Step 4: Build personalization
	personalization := s.buildPersonalization(query, allTags, personalOverrides)

	// Step 5: Rank files via personalized PageRank
	ranked := graph.RankFiles(allTags, personalization)

	// Convert FileRank → RankedFile
	rankedFiles := make([]RankedFile, 0, len(ranked))
	for _, fr := range ranked {
		// Build a one-line description from definitions
		desc := buildFileDesc(fr.Definitions)
		rankedFiles = append(rankedFiles, RankedFile{
			Path:   fr.Path,
			Score:  fr.Score,
			Source: "pagerank",
			Desc:   desc,
		})
	}

	return &ScanResult{
		Files:     rankedFiles,
		AllTags:   allTags,
		Graph:     graph,
		TagCount:  len(allTags),
		FileCount: len(files),
	}, nil
}

// discoverFiles walks the directory tree under RootDir, skipping common
// non-source directories, and returns file entries with detected languages.
func (s *RepoScanner) discoverFiles() ([]fileEntry, error) {
	var files []fileEntry

	err := filepath.WalkDir(s.RootDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		// Get relative path
		rel, err := filepath.Rel(s.RootDir, path)
		if err != nil {
			return nil
		}

		if d.IsDir() {
			base := filepath.Base(rel)
			if skipDirs[base] {
				return filepath.SkipDir
			}
			if skipExactNames[base] {
				return filepath.SkipDir
			}
			return nil
		}

		// Only process files with a recognized language
		lang := DetectLanguage(path)
		if lang == "" {
			return nil
		}

		// Skip large files (>100KB) — likely bundled/generated
		if info, err := d.Info(); err == nil && info.Size() > 100*1024 {
			return nil
		}

		// Skip known generated file patterns
		base := filepath.Base(path)
		if isGeneratedFileName(base) {
			return nil
		}

		// Skip test/spec files — they add noise to codemap ranking
		if isTestFile(base) {
			return nil
		}

		files = append(files, fileEntry{
			absPath: path,
			relPath: filepath.ToSlash(rel),
			lang:    lang,
		})
		return nil
	})

	return files, err
}

// extractTagsCached performs bulk mtime checks against the cache, only
// re-parsing files that have changed. It also cleans up cache entries for
// files that no longer exist.
func (s *RepoScanner) extractTagsCached(files []fileEntry) ([]Tag, error) {
	var allTags []Tag

	// If no store, extract without caching
	if s.Store == nil {
		for _, f := range files {
			tags, err := ExtractTags(f.absPath, f.lang)
			if err != nil {
				continue // skip unparseable files
			}
			// Fix file paths to be relative
			for i := range tags {
				tags[i].File = f.relPath
			}
			allTags = append(allTags, tags...)
		}
		return allTags, nil
	}

	// Bulk load cached mtimes
	cachedMtimes, err := s.Store.LoadAllTagMtimes(s.Namespace)
	if err != nil {
		// Fall back to uncached extraction
		for _, f := range files {
			tags, err := ExtractTags(f.absPath, f.lang)
			if err != nil {
				continue
			}
			for i := range tags {
				tags[i].File = f.relPath
			}
			allTags = append(allTags, tags...)
		}
		return allTags, nil
	}

	// Track which cached files are still present
	activeFiles := make(map[string]bool, len(files))

	for _, f := range files {
		activeFiles[f.relPath] = true

		// Get current mtime
		info, err := os.Stat(f.absPath)
		if err != nil {
			continue
		}
		currentMtime := info.ModTime().Unix()

		cachedMtime, isCached := cachedMtimes[f.relPath]

		if isCached && cachedMtime == currentMtime {
			// Cache hit: load from store
			tags, _, err := s.Store.LoadTags(s.Namespace, f.relPath)
			if err != nil || tags == nil {
				// Cache corrupt, re-parse
				tags, err = ExtractTags(f.absPath, f.lang)
				if err != nil {
					continue
				}
				for i := range tags {
					tags[i].File = f.relPath
				}
				_ = s.Store.StoreTags(s.Namespace, f.relPath, f.lang, tags, currentMtime)
			}
			allTags = append(allTags, tags...)
		} else {
			// Cache miss or stale: extract fresh
			tags, err := ExtractTags(f.absPath, f.lang)
			if err != nil {
				continue
			}
			for i := range tags {
				tags[i].File = f.relPath
			}
			_ = s.Store.StoreTags(s.Namespace, f.relPath, f.lang, tags, currentMtime)
			allTags = append(allTags, tags...)
		}
	}

	// Cleanup: delete cache entries for files that no longer exist
	for cachedPath := range cachedMtimes {
		if !activeFiles[cachedPath] {
			_ = s.Store.DeleteTags(s.Namespace, cachedPath)
		}
	}

	return allTags, nil
}

// buildPersonalization creates a personalization vector for PageRank based on
// the query string and user overrides. Query tokens are matched against tag
// names and file paths. File path matches get a 100x weight boost because
// matching the file name is a much stronger signal than a symbol reference.
func (s *RepoScanner) buildPersonalization(query string, tags []Tag, overrides map[string]float64) map[string]float64 {
	personalization := make(map[string]float64)

	// Apply user overrides first
	for path, weight := range overrides {
		personalization[path] = weight
	}

	if query == "" {
		return personalization
	}

	// Tokenize query: split on non-alphanumeric, lowercase
	tokens := tokenizeQuery(query)
	if len(tokens) == 0 {
		return personalization
	}

	// Collect all file paths from tags
	fileSet := make(map[string]bool)
	for _, t := range tags {
		fileSet[t.File] = true
	}

	for file := range fileSet {
		score := 0.0
		fileLower := strings.ToLower(file)

		// Check file path matches (strong signal)
		for _, token := range tokens {
			if strings.Contains(fileLower, token) {
				score += 100.0
			}
		}

		// Check tag name matches in this file
		for _, t := range tags {
			if t.File != file {
				continue
			}
			nameLower := strings.ToLower(t.Name)
			for _, token := range tokens {
				if nameLower == token || strings.Contains(nameLower, token) {
					score += 1.0
					break // one match per tag is enough
				}
			}
		}

		if score > 0 {
			// Add to any existing override
			personalization[file] += score
		}
	}

	return personalization
}

// tokenizeQuery splits a query string into lowercase alphanumeric tokens.
func tokenizeQuery(query string) []string {
	var tokens []string
	for _, part := range strings.Fields(query) {
		// Split on non-alphanumeric/underscore boundaries
		for _, chunk := range strings.FieldsFunc(strings.ToLower(part), func(r rune) bool {
			return !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_')
		}) {
			if chunk != "" {
				tokens = append(tokens, chunk)
			}
		}
	}
	return tokens
}

// buildFileDesc creates a one-line description from a file's definition tags,
// grouping by sub-kind (types, entry points, etc.).
func buildFileDesc(defs []Tag) string {
	if len(defs) == 0 {
		return ""
	}

	// Group definitions by sub-kind
	groups := make(map[string][]string) // subkind → names
	var order []string
	for _, d := range defs {
		sk := d.SubKind
		if sk == "" {
			sk = "other"
		}
		if _, exists := groups[sk]; !exists {
			order = append(order, sk)
		}
		groups[sk] = append(groups[sk], d.Name)
	}

	var parts []string
	for _, sk := range order {
		names := groups[sk]
		// Limit to first 3 names per group
		if len(names) > 3 {
			names = append(names[:3], "...")
		}
		parts = append(parts, fmt.Sprintf("%s: %s", sk, strings.Join(names, ", ")))
	}

	// Limit total length
	desc := strings.Join(parts, "; ")
	if len(desc) > 200 {
		desc = desc[:197] + "..."
	}
	return desc
}

// RenderRankedFiles renders a ranked file list as a structured text block
// suitable for LLM context injection. It stops adding files when the
// estimated token budget is exhausted (chars/4 heuristic).
//
// Output format:
//
//	/path/to/file.go — types: Foo, Bar; function: NewFoo (score: 0.123)
func RenderRankedFiles(files []RankedFile, maxTokens int) string {
	if len(files) == 0 {
		return ""
	}

	maxChars := maxTokens * 4
	var lines []string
	totalChars := 0

	for _, f := range files {
		line := fmt.Sprintf("  %s", f.Path)
		if f.Desc != "" {
			line += fmt.Sprintf(" — %s", f.Desc)
		}
		line += fmt.Sprintf(" (%.4f)", f.Score)

		lineLen := len(line) + 1 // +1 for newline
		if totalChars+lineLen > maxChars && totalChars > 0 {
			break
		}
		lines = append(lines, line)
		totalChars += lineLen
	}

	if len(lines) == 0 {
		return ""
	}

	return strings.Join(lines, "\n") + "\n"
}

// TopRanked returns the top N files from a ScanResult.
func (sr *ScanResult) TopRanked(n int) []RankedFile {
	if n > len(sr.Files) {
		n = len(sr.Files)
	}
	return sr.Files[:n]
}

// FilesByPrefix returns all ranked files whose path starts with the given prefix.
func (sr *ScanResult) FilesByPrefix(prefix string) []RankedFile {
	var matches []RankedFile
	for _, f := range sr.Files {
		if strings.HasPrefix(f.Path, prefix) {
			matches = append(matches, f)
		}
	}
	return matches
}

// SortFilesByPath re-sorts the files by path (alphabetical) instead of score.
func (sr *ScanResult) SortFilesByPath() {
	sort.Slice(sr.Files, func(i, j int) bool {
		return sr.Files[i].Path < sr.Files[j].Path
	})
}

// RenderForContext produces a codemap text block from graph-ranked files,
// suitable for injection into AssembleContext output. It includes a zoom1
// summary header followed by top-ranked files within the token budget.
func (sr *ScanResult) RenderForContext(maxTokens int) string {
	if sr == nil || len(sr.Files) == 0 {
		return ""
	}

	var sb strings.Builder

	// Zoom1-style header: language composition + file count
	header := sr.synthesizeHeader()
	sb.WriteString(header)
	headerTokens := len(header) / 4

	remaining := maxTokens - headerTokens
	if remaining < 50 {
		return sb.String()
	}

	sb.WriteString("\n")
	body := RenderRankedFiles(sr.Files, remaining)
	sb.WriteString(body)

	return sb.String()
}

// synthesizeHeader generates a one-line project summary from scan results,
// similar to the old CodeMap.Zoom1 but derived from tags rather than AST modules.
func (sr *ScanResult) synthesizeHeader() string {
	langCounts := make(map[string]int)
	for _, t := range sr.AllTags {
		if t.Kind == "def" {
			lang := detectLangFromPath(t.File)
			if lang != "" {
				langCounts[lang]++
			}
		}
	}

	var langs []string
	for _, l := range []string{"Go", "TypeScript", "Python", "Rust"} {
		key := strings.ToLower(l)
		if langCounts[key] > 0 {
			langs = append(langs, l)
		}
	}

	prefix := "Project"
	if len(langs) > 0 {
		prefix = strings.Join(langs, "+") + " project"
	}

	return fmt.Sprintf("%s. %d files scanned, %d symbols extracted.\n", prefix, sr.FileCount, sr.TagCount)
}

// isGeneratedFileName returns true for filenames that typically indicate
// bundled, minified, or generated code.
func isGeneratedFileName(name string) bool {
	lower := strings.ToLower(name)
	for _, pattern := range []string{
		".min.js", ".bundle.js", ".chunk.js",
		"swagger-ui", ".generated.", ".pb.go",
		"_generated.go", ".d.ts",
	} {
		if strings.Contains(lower, pattern) {
			return true
		}
	}
	return false
}

// isTestFile returns true for filenames that are test/spec files.
func isTestFile(name string) bool {
	lower := strings.ToLower(name)
	for _, suffix := range []string{
		".test.ts", ".test.tsx", ".test.js", ".test.jsx",
		".spec.ts", ".spec.tsx", ".spec.js", ".spec.jsx",
		"_test.go", "_test.py",
	} {
		if strings.HasSuffix(lower, suffix) {
			return true
		}
	}
	return false
}

// detectLangFromPath returns the language name from a file extension.
func detectLangFromPath(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".go":
		return "go"
	case ".ts", ".tsx", ".js", ".jsx", ".mjs":
		return "typescript"
	case ".py":
		return "python"
	case ".rs":
		return "rust"
	default:
		return ""
	}
}
