package dualmem

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	ts "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

// Thresholds for promoting a file to its own module entry.
const (
	significantExportThreshold = 5   // ≥5 exported symbols (types + entry points)
	significantLineThreshold   = 300 // ≥300 lines of code
)

// fileSignificance holds the classification result for a single source file.
type fileSignificance struct {
	path          string // absolute path
	baseName      string // e.g. "boosts.ts"
	exportCount   int    // types + entry points
	lineCount     int
	isSignificant bool
}

// langConfig holds tree-sitter queries and extraction logic per language.
type langConfig struct {
	lang        *ts.Language
	langName    string
	typeQuery   string // captures @type
	entryQuery  string // captures @entry
	importQuery string // captures @import
	identQuery  string // captures @ident
	// isExported decides if a node is public. If nil, all are public.
	isExported func(nodeText string, src []byte, node *ts.Node) bool
}

// Tree-sitter language configs. Each defines queries for symbol extraction.
var (
	tsLangConfig = langConfig{
		langName: "typescript",
		typeQuery: strings.Join([]string{
			`(export_statement declaration: (class_declaration name: (type_identifier) @type))`,
			`(export_statement declaration: (interface_declaration name: (type_identifier) @type))`,
			`(export_statement declaration: (type_alias_declaration name: (type_identifier) @type))`,
			`(export_statement declaration: (enum_declaration name: (identifier) @type))`,
		}, "\n"),
		entryQuery: strings.Join([]string{
			`(export_statement declaration: (function_declaration name: (identifier) @entry))`,
			`(export_statement declaration: (lexical_declaration (variable_declarator name: (identifier) @entry)))`,
		}, "\n"),
		importQuery: `(import_statement source: (string (string_fragment) @import))`,
		identQuery: strings.Join([]string{
			`(function_declaration name: (identifier) @ident)`,
			`(lexical_declaration (variable_declarator name: (identifier) @ident))`,
		}, "\n"),
		// isExported is nil for TypeScript because:
		// - entryQuery already constrains to export_statement (pre-filtered)
		// - identQuery dedup relies on entrySeen check in parseWithTreeSitter
		isExported: nil,
	}

	pyConfig = langConfig{
		langName: "python",
		typeQuery: `(class_definition name: (identifier) @type)`,
		// Entry query matches all functions — filtering happens in isExported
		entryQuery: `(function_definition name: (identifier) @entry)`,
		importQuery: strings.Join([]string{
			`(import_from_statement module_name: (dotted_name) @import)`,
			`(import_statement name: (dotted_name) @import)`,
		}, "\n"),
		identQuery: `(function_definition name: (identifier) @ident)`,
		isExported: func(nodeText string, src []byte, node *ts.Node) bool {
			// Python: non-underscore top-level functions are public (entries)
			// Underscore-prefixed are private (identifiers)
			return !strings.HasPrefix(nodeText, "_")
		},
	}

	rsConfig = langConfig{
		langName: "rust",
		typeQuery: strings.Join([]string{
			`(struct_item name: (type_identifier) @type)`,
			`(enum_item name: (type_identifier) @type)`,
			`(trait_item name: (type_identifier) @type)`,
		}, "\n"),
		entryQuery: `(function_item name: (identifier) @entry)`,
		importQuery: `(use_declaration argument: (_) @import)`,
		identQuery:  `(function_item name: (identifier) @ident)`,
		isExported: func(nodeText string, src []byte, node *ts.Node) bool {
			// Rust: pub keyword before item. The identifier is a child of function_item.
			// Walk up to function_item and check if its text starts with "pub ".
			parent := node.Parent()
			if parent != nil {
				parentText := string(src[parent.StartByte():parent.EndByte()])
				return strings.HasPrefix(parentText, "pub ")
			}
			return false
		},
	}
)

// lazily initialized tree-sitter language pointers.
var langsInitialized bool

func ensureLangs() {
	if langsInitialized {
		return
	}
	tsLangConfig.lang = grammars.TypescriptLanguage()
	pyConfig.lang = grammars.PythonLanguage()
	rsConfig.lang = grammars.RustLanguage()
	langsInitialized = true
}

// parseTSModule parses TypeScript/JavaScript files using tree-sitter.
func parseTSModule(relPath string, files []string) *ModuleMap {
	ensureLangs()
	mod := parseWithTreeSitter(relPath, files, &tsLangConfig)
	if mod != nil {
		mod.Summary = fmt.Sprintf("TypeScript module (%d files)", len(files))
	}
	return mod
}

// classifyFileSignificance checks whether a single file is significant enough
// to warrant its own module entry (rather than being grouped with the directory).
func classifyFileSignificance(filePath string, cfg *langConfig) fileSignificance {
	sig := fileSignificance{
		path:     filePath,
		baseName: filepath.Base(filePath),
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		return sig
	}
	sig.lineCount = bytes.Count(data, []byte("\n")) + 1

	if cfg.lang == nil {
		sig.isSignificant = sig.lineCount >= significantLineThreshold
		return sig
	}

	parser := ts.NewParser(cfg.lang)
	tree, err := parser.Parse(data)
	if err != nil {
		sig.isSignificant = sig.lineCount >= significantLineThreshold
		return sig
	}
	defer tree.Release()
	root := tree.RootNode()

	// Count exported types
	if cfg.typeQuery != "" {
		extractCaptures(root, cfg.lang, data, cfg.typeQuery, func(text string, node *ts.Node) {
			sig.exportCount++
		})
	}
	// Count exported entry points
	if cfg.entryQuery != "" {
		extractCaptures(root, cfg.lang, data, cfg.entryQuery, func(text string, node *ts.Node) {
			// For TS, entryQuery only matches exports, so no isExported check needed.
			// For other langs, apply the filter.
			if cfg.isExported != nil && !cfg.isExported(text, data, node) {
				return
			}
			sig.exportCount++
		})
	}

	sig.isSignificant = sig.exportCount >= significantExportThreshold || sig.lineCount >= significantLineThreshold
	return sig
}

// parseTSModuleSplit parses TypeScript files, splitting significant files into
// their own modules. Files below the significance threshold are grouped into
// a directory-level module as before.
func parseTSModuleSplit(relPath string, files []string) []ModuleMap {
	ensureLangs()
	cfg := &tsLangConfig

	// Single file — no splitting needed
	if len(files) <= 1 {
		mod := parseWithTreeSitter(relPath, files, cfg)
		if mod != nil {
			mod.Summary = fmt.Sprintf("TypeScript module (%d files)", len(files))
			return []ModuleMap{*mod}
		}
		return nil
	}

	// Classify each file
	var significant []fileSignificance
	var restFiles []string
	for _, f := range files {
		sig := classifyFileSignificance(f, cfg)
		if sig.isSignificant {
			significant = append(significant, sig)
		} else {
			restFiles = append(restFiles, f)
		}
	}

	// If none are significant, fall back to directory-level grouping
	if len(significant) == 0 {
		mod := parseWithTreeSitter(relPath, files, cfg)
		if mod != nil {
			mod.Summary = fmt.Sprintf("TypeScript module (%d files)", len(files))
			return []ModuleMap{*mod}
		}
		return nil
	}

	var modules []ModuleMap

	// Each significant file gets its own module
	for _, sig := range significant {
		ext := filepath.Ext(sig.baseName)
		nameNoExt := strings.TrimSuffix(sig.baseName, ext)
		filePath := relPath + "/" + nameNoExt
		mod := parseWithTreeSitter(filePath, []string{sig.path}, cfg)
		if mod != nil {
			mod.Summary = fmt.Sprintf("TypeScript file (%d lines, %d exports)", sig.lineCount, sig.exportCount)
			mod.FileCount = 1
			modules = append(modules, *mod)
		}
	}

	// Remaining files form the "rest" directory module
	if len(restFiles) > 0 {
		mod := parseWithTreeSitter(relPath, restFiles, cfg)
		if mod != nil {
			mod.Summary = fmt.Sprintf("TypeScript module (%d files)", len(restFiles))
			modules = append(modules, *mod)
		}
	}

	return modules
}

// parsePythonModule parses Python files using tree-sitter.
func parsePythonModule(relPath string, files []string) *ModuleMap {
	ensureLangs()
	mod := parseWithTreeSitter(relPath, files, &pyConfig)
	if mod != nil {
		mod.Summary = fmt.Sprintf("Python module (%d files)", len(files))
	}
	return mod
}

// parseRustModule parses Rust files using tree-sitter.
func parseRustModule(relPath string, files []string) *ModuleMap {
	ensureLangs()
	mod := parseWithTreeSitter(relPath, files, &rsConfig)
	if mod != nil {
		mod.Summary = fmt.Sprintf("Rust module (%d files)", len(files))
	}
	return mod
}

// parseWithTreeSitter is the generic tree-sitter extraction pipeline.
func parseWithTreeSitter(relPath string, files []string, cfg *langConfig) *ModuleMap {
	if len(files) == 0 || cfg.lang == nil {
		return nil
	}

	parser := ts.NewParser(cfg.lang)

	typeSeen := make(map[string]bool)
	entrySeen := make(map[string]bool)
	importSeen := make(map[string]bool)
	identSeen := make(map[string]bool)

	var types, entries, imports, idents []string

	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}

		tree, err := parser.Parse(data)
		if err != nil {
			continue
		}
		root := tree.RootNode()

		// Extract types
		if cfg.typeQuery != "" {
			extractCaptures(root, cfg.lang, data, cfg.typeQuery, func(text string, node *ts.Node) {
				label := fmt.Sprintf("class %s", text)
				if cfg.langName == "rust" {
					parent := node.Parent()
					if parent != nil {
						switch parent.Type(cfg.lang) {
						case "struct_item":
							label = "struct " + text
						case "enum_item":
							label = "enum " + text
						case "trait_item":
							label = "trait " + text
						}
					}
				}
				if !typeSeen[label] {
					typeSeen[label] = true
					types = append(types, label)
				}
			})
		}

		// Extract entry points (exported/public functions only)
		if cfg.entryQuery != "" {
			extractCaptures(root, cfg.lang, data, cfg.entryQuery, func(text string, node *ts.Node) {
				// For languages with export conventions, filter to public only
				if cfg.isExported != nil && !cfg.isExported(text, data, node) {
					return
				}
				entry := text + "()"
				if !entrySeen[entry] {
					entrySeen[entry] = true
					entries = append(entries, entry)
				}
			})
		}

		// Extract imports
		if cfg.importQuery != "" {
			extractCaptures(root, cfg.lang, data, cfg.importQuery, func(text string, node *ts.Node) {
				if !importSeen[text] {
					importSeen[text] = true
					imports = append(imports, text)
				}
			})
		}

		// Extract private identifiers
		if cfg.identQuery != "" {
			extractCaptures(root, cfg.lang, data, cfg.identQuery, func(text string, node *ts.Node) {
				if entrySeen[text+"()"] {
					return
				}
				if cfg.isExported != nil && cfg.isExported(text, data, node) {
					return
				}
				if !identSeen[text] {
					identSeen[text] = true
					idents = append(idents, text)
				}
			})
		}

		tree.Release()
	}

	// Apply caps
	if len(types) > 8 {
		types = types[:8]
	}
	if len(entries) > 6 {
		entries = entries[:6]
	}
	if len(imports) > 15 {
		imports = imports[:15]
	}
	if len(idents) > 15 {
		idents = idents[:15]
	}

	displayPath := relPath + "/"
	if relPath == "." {
		displayPath = "./"
	}

	return &ModuleMap{
		Path:        displayPath,
		Language:    cfg.langName,
		KeyTypes:    types,
		EntryPoints: entries,
		FileCount:   len(files),
		Imports:     imports,
		Identifiers: idents,
	}
}

// extractCaptures runs a tree-sitter query and calls fn for each capture.
func extractCaptures(root *ts.Node, lang *ts.Language, src []byte, queryStr string, fn func(text string, node *ts.Node)) {
	q, err := ts.NewQuery(queryStr, lang)
	if err != nil {
		return
	}
	cursor := q.Exec(root, lang, src)
	for {
		match, ok := cursor.NextMatch()
		if !ok {
			break
		}
		for _, cap := range match.Captures {
			text := string(src[cap.Node.StartByte():cap.Node.EndByte()])
			fn(text, cap.Node)
		}
	}
}
