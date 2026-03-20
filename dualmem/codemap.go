package dualmem

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
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
	Path        string   `json:"path"`         // Relative path from root
	Language    string   `json:"language"`      // "go", "typescript", "other"
	Summary     string   `json:"summary"`       // Human-readable description
	KeyTypes    []string `json:"key_types"`     // Important exported types/interfaces
	EntryPoints []string `json:"entry_points"`  // main funcs, handler registrations
	FileCount   int      `json:"file_count"`
}

// Skip patterns for directory scanning.
var skipDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true, "__pycache__": true,
	".next": true, "dist": true, "build": true, ".claude": true, ".vscode": true,
	".idea": true, "coverage": true, ".nyc_output": true, "target": true,
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
		relPath string
		goFiles []string
		tsFiles []string
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
			if skipDirs[info.Name()] {
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
			mod = parseTypeScriptModule(d.relPath, d.tsFiles)
		default:
			// Only include directories with 3+ files or meaningful names
			if len(d.allFiles) >= 3 || isInterestingDir(d.relPath) {
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
	var pkgName string
	isMain := false

	for name, pkg := range pkgs {
		pkgName = name
		if name == "main" {
			isMain = true
		}

		for _, file := range pkg.Files {
			for _, decl := range file.Decls {
				switch d := decl.(type) {
				case *ast.GenDecl:
					for _, spec := range d.Specs {
						if ts, ok := spec.(*ast.TypeSpec); ok && ts.Name.IsExported() {
							kind := "type"
							switch ts.Type.(type) {
							case *ast.InterfaceType:
								kind = "interface"
							case *ast.StructType:
								kind = "struct"
							}
							types = append(types, fmt.Sprintf("%s %s", kind, ts.Name.Name))
						}
					}
				case *ast.FuncDecl:
					if d.Name.Name == "main" {
						entryPoints = append(entryPoints, "main()")
					} else if d.Name.IsExported() && d.Recv == nil {
						// Top-level exported functions (not methods)
						entryPoints = append(entryPoints, d.Name.Name+"()")
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
	}
}

// --- TypeScript regex parser ---

var tsExportPattern = regexp.MustCompile(`export\s+(?:default\s+)?(?:async\s+)?(function|class|interface|type|const|enum)\s+(\w+)`)

func parseTypeScriptModule(relPath string, tsFiles []string) *ModuleMap {
	var types []string
	var entryPoints []string
	hasIndex := false

	for _, f := range tsFiles {
		base := filepath.Base(f)
		if base == "index.ts" || base == "index.tsx" {
			hasIndex = true
			entryPoints = append(entryPoints, base)
		}

		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		content := string(data)

		matches := tsExportPattern.FindAllStringSubmatch(content, -1)
		for _, m := range matches {
			if len(m) >= 3 {
				kind := m[1]
				name := m[2]
				switch kind {
				case "class", "interface", "type", "enum":
					types = append(types, fmt.Sprintf("%s %s", kind, name))
				case "function", "const":
					if !hasIndex || base != "index.ts" {
						entryPoints = append(entryPoints, name+"()")
					}
				}
			}
		}
	}

	if len(types) > 8 {
		types = types[:8]
	}
	if len(entryPoints) > 6 {
		entryPoints = entryPoints[:6]
	}

	return &ModuleMap{
		Path:        relPath + "/",
		Language:    "typescript",
		Summary:     fmt.Sprintf("TypeScript module (%d files)", len(tsFiles)),
		KeyTypes:    types,
		EntryPoints: entryPoints,
		FileCount:   len(tsFiles),
	}
}

// --- Zoom-1 synthesizer ---

func synthesizeZoom1(modules []ModuleMap, rootDir string) string {
	goCount, tsCount, otherCount := 0, 0, 0
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
// Always includes zoom-1. Adds zoom-2 modules by file count (largest first)
// until budget is exhausted.
func (cm *CodeMap) RenderAtBudget(maxTokens int) string {
	var sb strings.Builder

	// Always include zoom-1
	sb.WriteString(cm.Zoom1)
	tokensUsed := estimateTokensStr(cm.Zoom1)

	if maxTokens <= tokensUsed+20 {
		return sb.String()
	}

	// Sort modules by file count (most files = most important)
	sorted := make([]ModuleMap, len(cm.Zoom2))
	copy(sorted, cm.Zoom2)
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

	return sb.String()
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
// test
