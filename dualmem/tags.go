package dualmem

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	ts "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

//go:embed tags_scm/*.scm
var tagsScmFS embed.FS

// langEntry holds a tree-sitter language pointer and the compiled query.
type langEntry struct {
	lang  *ts.Language
	query string
}

var (
	langRegistryOnce sync.Once
	langRegistry     map[string]*langEntry
)

// stripUnsupportedPredicates removes predicate lines and doc-comment anchors
// that cause tag extraction to miss symbols in files without doc comments.
//
// The tags.scm format uses patterns like:
//
//	(comment)* @doc
//	.
//	(function_declaration ...) @definition.function
//	(#strip! @doc "...")
//	(#select-adjacent! @doc @definition.function)
//
// The `.` anchor requires the comment to be immediately adjacent to the
// definition. When there's no comment, the match is rejected. Since we don't
// need doc text for tag extraction, we strip these doc-related lines entirely.
func stripUnsupportedPredicates(query string) string {
	lines := strings.Split(query, "\n")
	filtered := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if shouldStripTagsLine(trimmed) {
			continue
		}
		filtered = append(filtered, line)
	}
	return strings.Join(filtered, "\n")
}

// shouldStripTagsLine returns true for lines that should be removed from tags
// queries to make them work with gotreesitter v0.11.2 without doc comment anchoring.
func shouldStripTagsLine(trimmed string) bool {
	switch {
	case trimmed == "(comment)* @doc",
		trimmed == "(comment)+ @doc",
		trimmed == ".",                                     // adjacency anchor
		strings.HasPrefix(trimmed, "(#strip!"),            // doc text stripping
		strings.HasPrefix(trimmed, "(#set-adjacent!"),     // unsupported predicate
		strings.HasPrefix(trimmed, "(#select-adjacent!"): // doc adjacency filter
		return true
	}
	return false
}

// initLangRegistry lazily loads the language registry.
func initLangRegistry() map[string]*langEntry {
	langRegistryOnce.Do(func() {
		loadQuery := func(name string) string {
			data, err := tagsScmFS.ReadFile(fmt.Sprintf("tags_scm/%s-tags.scm", name))
			if err != nil {
				return ""
			}
			return stripUnsupportedPredicates(string(data))
		}

		langRegistry = map[string]*langEntry{
			"go": {
				lang:  grammars.GoLanguage(),
				query: loadQuery("go"),
			},
			"javascript": {
				lang:  grammars.TypescriptLanguage(),
				query: loadQuery("javascript"),
			},
			"python": {
				lang:  grammars.PythonLanguage(),
				query: loadQuery("python"),
			},
			"rust": {
				lang:  grammars.RustLanguage(),
				query: loadQuery("rust"),
			},
		}
	})
	return langRegistry
}

// DetectLanguage maps a file path's extension to a language key.
// Returns "" for unrecognised extensions.
func DetectLanguage(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go":
		return "go"
	case ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs":
		return "javascript"
	case ".py", ".pyi":
		return "python"
	case ".rs":
		return "rust"
	default:
		return ""
	}
}

// ExtractTags parses filePath with tree-sitter and returns definition/reference
// tags extracted using the language's tags.scm query. Returns nil, nil when
// lang is unknown or the file cannot be read.
func ExtractTags(filePath, lang string) ([]Tag, error) {
	registry := initLangRegistry()
	entry, ok := registry[lang]
	if !ok || entry == nil || entry.query == "" {
		return nil, nil
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("ExtractTags: read %s: %w", filePath, err)
	}

	parser := ts.NewParser(entry.lang)
	tree, err := parser.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("ExtractTags: parse %s: %w", filePath, err)
	}
	defer tree.Release()

	q, err := ts.NewQuery(entry.query, entry.lang)
	if err != nil {
		return nil, fmt.Errorf("ExtractTags: compile query for %s: %w", lang, err)
	}

	root := tree.RootNode()
	cursor := q.Exec(root, entry.lang, data)

	var tags []Tag
	for {
		match, ok := cursor.NextMatch()
		if !ok {
			break
		}

		// Each match may have captures like:
		//   @name.definition.function → the symbol name
		//   @definition.function      → the span of the whole definition
		//   @doc                      → documentation comment (ignored)
		// We only care about captures whose name starts with "name.".

		var nameText string
		var nameNode *ts.Node
		var kindStr string // "def" or "ref"
		var subKind string // e.g. "function", "call", "class"

		for i := range match.Captures {
			cap := &match.Captures[i]
			capName := cap.Name // e.g. "name.definition.function"

			if !strings.HasPrefix(capName, "name.") {
				continue
			}

			// Parse suffix after "name."
			// Format: "name.definition.<subkind>" or "name.reference.<subkind>"
			rest := capName[len("name."):] // "definition.function" or "reference.call"

			var kind string
			var sub string
			if strings.HasPrefix(rest, "definition.") {
				kind = "def"
				sub = rest[len("definition."):]
			} else if strings.HasPrefix(rest, "reference.") {
				kind = "ref"
				sub = rest[len("reference."):]
			} else {
				// Unrecognised pattern — skip
				continue
			}

			nameText = string(data[cap.Node.StartByte():cap.Node.EndByte()])
			nameNode = cap.Node
			kindStr = kind
			subKind = sub
		}

		if nameText == "" || kindStr == "" {
			continue
		}

		line := 1
		if nameNode != nil {
			line = 1 + countNewlines(data, nameNode.StartByte())
		}

		tags = append(tags, Tag{
			File:    filePath,
			Name:    nameText,
			Kind:    kindStr,
			SubKind: subKind,
			Line:    line,
		})
	}

	return tags, nil
}
