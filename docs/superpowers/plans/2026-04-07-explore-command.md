# Explore Command Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add code-reading evidence pipeline that ranks files, extracts relevant snippets within a token budget, and feeds real code to the LLM — closing the hallucination gap in `consult`.

**Architecture:** One core engine method `ReadCodeEvidence` reads actual source files ranked by HDC+BM25. Three entry points: upgraded `consult` synthesis, new `explore` CLI command, and a pre-hook script. Block extraction uses hybrid strategy: whole-file for small files, targeted symbol extraction for large files.

**Tech Stack:** Go, existing dualmem engine (HDC, BM25, codemap, co-change graph), GLM 5.1 via AnthropicSummarizer for synthesis.

**Spec:** `docs/superpowers/specs/2026-04-07-explore-command-design.md`

**Execution:** All tasks dispatched via dispatcher UI, GLM 5.1 model, pi harness.

---

### Task 1: Block Extraction Helpers (`dualmem/extract.go`)

New file with pure functions for reading source files and extracting code blocks around identifiers. No engine dependency — just file I/O and text processing.

**Files:**
- Create: `dualmem/extract.go`
- Create: `dualmem/extract_test.go`

- [ ] **Step 1: Write failing tests for `countLines`**

Create `dualmem/extract_test.go`:

```go
package dualmem

import "testing"

func TestCountLines(t *testing.T) {
	tests := []struct {
		name string
		s    string
		want int
	}{
		{"empty", "", 0},
		{"one line", "hello", 1},
		{"two lines", "hello\nworld", 2},
		{"trailing newline", "hello\nworld\n", 2},
		{"many lines", "a\nb\nc\nd\ne", 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := countLines(tt.s)
			if got != tt.want {
				t.Errorf("countLines(%q)=%d, want %d", tt.s, got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./dualmem/ -run TestCountLines -v`
Expected: FAIL — `countLines` undefined

- [ ] **Step 3: Implement `countLines` in `dualmem/extract.go`**

Create `dualmem/extract.go`:

```go
package dualmem

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// countLines returns the number of lines in a string.
func countLines(s string) int {
	if s == "" {
		return 0
	}
	n := strings.Count(s, "\n")
	if !strings.HasSuffix(s, "\n") {
		n++
	}
	return n
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./dualmem/ -run TestCountLines -v`
Expected: PASS

- [ ] **Step 5: Write failing tests for `readFileLines`**

Append to `dualmem/extract_test.go`:

```go
func TestReadFileLines(t *testing.T) {
	dir := t.TempDir()

	// Write a 10-line file
	content := "line1\nline2\nline3\nline4\nline5\nline6\nline7\nline8\nline9\nline10\n"
	path := filepath.Join(dir, "test.go")
	os.WriteFile(path, []byte(content), 0644)

	t.Run("read all", func(t *testing.T) {
		lines, err := readFileLines(path)
		if err != nil {
			t.Fatal(err)
		}
		if len(lines) != 10 {
			t.Errorf("got %d lines, want 10", len(lines))
		}
		if lines[0] != "line1" {
			t.Errorf("first line = %q, want %q", lines[0], "line1")
		}
	})

	t.Run("missing file", func(t *testing.T) {
		_, err := readFileLines(filepath.Join(dir, "nope.go"))
		if err == nil {
			t.Error("expected error for missing file")
		}
	})
}
```

- [ ] **Step 6: Implement `readFileLines`**

Add to `dualmem/extract.go`:

```go
// readFileLines reads a file and returns its lines.
func readFileLines(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	return lines, scanner.Err()
}
```

- [ ] **Step 7: Run tests**

Run: `go test ./dualmem/ -run TestReadFileLines -v`
Expected: PASS

- [ ] **Step 8: Write failing tests for `extractBlock`**

Append to `dualmem/extract_test.go`:

```go
func TestExtractBlock_Go(t *testing.T) {
	lines := []string{
		"package main",                // 0
		"",                            // 1
		"func hello() {",              // 2
		"	fmt.Println(\"hello\")",    // 3
		"}",                           // 4
		"",                            // 5
		"func goodbye(name string) {", // 6
		"	if name == \"\" {",         // 7
		"		return",                // 8
		"	}",                         // 9
		"	fmt.Println(name)",         // 10
		"}",                           // 11
	}

	t.Run("find hello func", func(t *testing.T) {
		start, end := extractBlock(lines, "hello", "go")
		if start != 2 || end != 4 {
			t.Errorf("got [%d,%d], want [2,4]", start, end)
		}
	})

	t.Run("find goodbye func", func(t *testing.T) {
		start, end := extractBlock(lines, "goodbye", "go")
		if start != 6 || end != 11 {
			t.Errorf("got [%d,%d], want [6,11]", start, end)
		}
	})

	t.Run("not found", func(t *testing.T) {
		start, end := extractBlock(lines, "missing", "go")
		if start != -1 || end != -1 {
			t.Errorf("got [%d,%d], want [-1,-1]", start, end)
		}
	})
}

func TestExtractBlock_Python(t *testing.T) {
	lines := []string{
		"import os",                 // 0
		"",                          // 1
		"def greet(name):",          // 2
		"    print(f'hi {name}')",   // 3
		"    return True",           // 4
		"",                          // 5
		"def farewell():",           // 6
		"    print('bye')",          // 7
	}

	t.Run("find greet", func(t *testing.T) {
		start, end := extractBlock(lines, "greet", "python")
		if start != 2 || end != 4 {
			t.Errorf("got [%d,%d], want [2,4]", start, end)
		}
	})

	t.Run("find farewell", func(t *testing.T) {
		start, end := extractBlock(lines, "farewell", "python")
		if start != 6 || end != 7 {
			t.Errorf("got [%d,%d], want [6,7]", start, end)
		}
	})
}

func TestExtractBlock_TypeScript(t *testing.T) {
	lines := []string{
		"import React from 'react';",              // 0
		"",                                         // 1
		"export function App() {",                  // 2
		"  return <div>Hello</div>;",               // 3
		"}",                                        // 4
		"",                                         // 5
		"export const helper = (x: number) => {",   // 6
		"  return x * 2;",                          // 7
		"};",                                       // 8
	}

	t.Run("find App", func(t *testing.T) {
		start, end := extractBlock(lines, "App", "typescript")
		if start != 2 || end != 4 {
			t.Errorf("got [%d,%d], want [2,4]", start, end)
		}
	})

	t.Run("find helper", func(t *testing.T) {
		start, end := extractBlock(lines, "helper", "typescript")
		if start != 6 || end != 8 {
			t.Errorf("got [%d,%d], want [6,8]", start, end)
		}
	})
}

func TestExtractBlock_Truncation(t *testing.T) {
	// Build a function with 80 lines (exceeds 60-line max)
	lines := make([]string, 82)
	lines[0] = "func bigFunc() {"
	for i := 1; i <= 80; i++ {
		lines[i] = "	doWork()"
	}
	lines[81] = "}"

	start, end := extractBlock(lines, "bigFunc", "go")
	if start != 0 {
		t.Errorf("start=%d, want 0", start)
	}
	// Should truncate to 60 lines from start
	if end-start+1 > maxBlockLines {
		t.Errorf("block size=%d, want <=%d", end-start+1, maxBlockLines)
	}
}
```

- [ ] **Step 9: Implement `extractBlock`**

Add to `dualmem/extract.go`:

```go
const maxBlockLines = 60

// extractBlock finds the code block enclosing the named identifier.
// Returns (startLine, endLine) as 0-based indices into lines.
// Returns (-1, -1) if identifier not found.
func extractBlock(lines []string, identifier, lang string) (int, int) {
	defLine := -1
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if containsDefinition(trimmed, identifier, lang) {
			defLine = i
			break
		}
	}
	if defLine == -1 {
		return -1, -1
	}

	switch lang {
	case "python":
		return extractPythonBlock(lines, defLine)
	default:
		return extractBraceBlock(lines, defLine)
	}
}

// containsDefinition checks if a line contains a definition of the identifier.
func containsDefinition(line, identifier, lang string) bool {
	switch lang {
	case "go":
		return strings.Contains(line, "func "+identifier) ||
			(strings.Contains(line, "func (") && strings.Contains(line, ") "+identifier)) ||
			strings.Contains(line, "type "+identifier)
	case "python":
		return strings.HasPrefix(line, "def "+identifier) ||
			strings.HasPrefix(line, "class "+identifier) ||
			strings.HasPrefix(line, "async def "+identifier)
	case "typescript", "javascript", "tsx", "jsx":
		return strings.Contains(line, "function "+identifier) ||
			strings.Contains(line, "const "+identifier) ||
			strings.Contains(line, "let "+identifier) ||
			strings.Contains(line, "class "+identifier) ||
			strings.Contains(line, "export function "+identifier) ||
			strings.Contains(line, "export const "+identifier) ||
			strings.Contains(line, "export class "+identifier)
	case "rust":
		return strings.Contains(line, "fn "+identifier) ||
			strings.Contains(line, "struct "+identifier) ||
			strings.Contains(line, "enum "+identifier) ||
			strings.Contains(line, "impl "+identifier)
	default:
		return strings.Contains(line, identifier)
	}
}

// extractBraceBlock finds the end of a brace-delimited block.
func extractBraceBlock(lines []string, startLine int) (int, int) {
	depth := 0
	opened := false
	for i := startLine; i < len(lines); i++ {
		for _, ch := range lines[i] {
			if ch == '{' {
				depth++
				opened = true
			} else if ch == '}' {
				depth--
			}
		}
		if opened && depth <= 0 {
			end := i
			if end-startLine+1 > maxBlockLines {
				end = startLine + maxBlockLines - 1
			}
			return startLine, end
		}
	}
	end := startLine + maxBlockLines - 1
	if end >= len(lines) {
		end = len(lines) - 1
	}
	return startLine, end
}

// extractPythonBlock finds the end of an indentation-delimited block.
func extractPythonBlock(lines []string, startLine int) (int, int) {
	if startLine >= len(lines) {
		return startLine, startLine
	}
	baseIndent := indentLevel(lines[startLine])
	end := startLine

	for i := startLine + 1; i < len(lines); i++ {
		line := lines[i]
		if strings.TrimSpace(line) == "" {
			continue
		}
		if indentLevel(line) <= baseIndent {
			break
		}
		end = i
	}

	if end-startLine+1 > maxBlockLines {
		end = startLine + maxBlockLines - 1
	}
	return startLine, end
}

// indentLevel returns the number of leading spaces (tabs count as 4).
func indentLevel(s string) int {
	n := 0
	for _, ch := range s {
		if ch == ' ' {
			n++
		} else if ch == '\t' {
			n += 4
		} else {
			break
		}
	}
	return n
}
```

- [ ] **Step 10: Run all extract tests**

Run: `go test ./dualmem/ -run "TestExtractBlock|TestCountLines|TestReadFileLines" -v`
Expected: All PASS

- [ ] **Step 11: Write failing tests for `matchIdentifiers`**

Append to `dualmem/extract_test.go`:

```go
func TestMatchIdentifiers(t *testing.T) {
	identifiers := []string{"EncodeModule", "EncodeQuery", "HDCDim", "splitCamelCase", "hdcTokenize"}

	t.Run("exact match", func(t *testing.T) {
		matches := matchIdentifiers(identifiers, "encode module")
		if len(matches) == 0 {
			t.Fatal("expected matches")
		}
		if matches[0] != "EncodeModule" {
			t.Errorf("top match = %q, want EncodeModule", matches[0])
		}
	})

	t.Run("partial match", func(t *testing.T) {
		matches := matchIdentifiers(identifiers, "tokenize")
		found := false
		for _, m := range matches {
			if m == "hdcTokenize" {
				found = true
			}
		}
		if !found {
			t.Error("expected hdcTokenize in matches")
		}
	})

	t.Run("no match", func(t *testing.T) {
		matches := matchIdentifiers(identifiers, "database connection pool")
		if len(matches) != 0 {
			t.Errorf("expected no matches, got %v", matches)
		}
	})
}
```

- [ ] **Step 12: Implement `matchIdentifiers`**

Add to `dualmem/extract.go`:

```go
// matchIdentifiers scores identifiers against query tokens and returns
// those with overlap > 0, sorted by score descending.
func matchIdentifiers(identifiers []string, query string) []string {
	queryTokens := make(map[string]bool)
	for _, tok := range hdcTokenize(query) {
		queryTokens[tok] = true
	}

	type scored struct {
		name  string
		score int
	}
	var hits []scored

	for _, id := range identifiers {
		idTokens := hdcTokenizeSymbol(id)
		overlap := 0
		for _, tok := range idTokens {
			if queryTokens[tok] {
				overlap++
			}
		}
		if overlap > 0 {
			hits = append(hits, scored{id, overlap})
		}
	}

	for i := 0; i < len(hits); i++ {
		for j := i + 1; j < len(hits); j++ {
			if hits[j].score > hits[i].score {
				hits[i], hits[j] = hits[j], hits[i]
			}
		}
	}

	result := make([]string, len(hits))
	for i, h := range hits {
		result[i] = h.name
	}
	return result
}
```

- [ ] **Step 13: Run all tests and commit**

Run: `go test ./dualmem/ -run "TestCountLines|TestReadFileLines|TestExtractBlock|TestMatchIdentifiers" -v`
Expected: All PASS

```bash
git add dualmem/extract.go dualmem/extract_test.go
git commit -m "feat(explore): block extraction helpers — readFileLines, extractBlock, matchIdentifiers"
```

---

### Task 2: `ReadCodeEvidence` Engine Method

Core method that uses the codemap file ranking, reads source files, extracts snippets within a token budget. Lives in `dualmem/dualmem.go` alongside `Consult`.

**Files:**
- Modify: `dualmem/dualmem.go` (add `ReadCodeEvidence` method + helpers)
- Modify: `dualmem/types.go` (add `ReadCodeOpts`, `CodeEvidence`, `CodeSnippet` types)
- Create: `dualmem/explore_test.go`

- [ ] **Step 1: Add types to `dualmem/types.go`**

Add after the `RankedFile` type (around line 444):

```go
// ReadCodeOpts controls code evidence extraction.
type ReadCodeOpts struct {
	MaxTokens int      // token budget for snippets (default 4000)
	MaxFiles  int      // max files to read (default 8)
	SeedPaths []string // optional pre-ranked paths to prioritize
}

// CodeEvidence holds extracted code snippets from ranked source files.
type CodeEvidence struct {
	Snippets    []CodeSnippet
	TotalTokens int
}

// CodeSnippet is a code excerpt from a source file with location metadata.
type CodeSnippet struct {
	FilePath   string // relative to repo root
	StartLine  int    // 1-based
	EndLine    int    // 1-based
	Content    string
	Relevance  string // source description (e.g. module summary)
	TokenCount int
}
```

- [ ] **Step 2: Write failing test for `ReadCodeEvidence`**

Create `dualmem/explore_test.go`:

```go
package dualmem

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ctx is a test helper returning a background context.
func ctx(t *testing.T) context.Context {
	t.Helper()
	return context.Background()
}

func TestReadCodeEvidence_SmallFile(t *testing.T) {
	dir := t.TempDir()
	srcDir := filepath.Join(dir, "pkg")
	os.MkdirAll(srcDir, 0755)

	smallFile := "package pkg\n\nfunc Hello() string {\n\treturn \"hello\"\n}\n\nfunc Goodbye() string {\n\treturn \"goodbye\"\n}\n"
	os.WriteFile(filepath.Join(srcDir, "greet.go"), []byte(smallFile), 0644)

	cm := &CodeMap{
		RootDir: dir,
		Zoom2: []ModuleMap{
			{
				Path:     "pkg/",
				Language: "go",
				Summary:  "greeting functions",
				KeyTypes: []string{"Hello", "Goodbye"},
				Files: []FileInfo{
					{RelPath: "pkg/greet.go", Identifiers: []string{"Hello", "Goodbye"}},
				},
			},
		},
	}
	idx := BuildCodeIndex(cm)

	engine, _ := newTestEngineWithGen(t)
	evidence, err := engine.ReadCodeEvidence(ctx(t), "test-ns", "hello greeting", ReadCodeOpts{
		MaxTokens: 4000,
		MaxFiles:  8,
	}, cm, idx)
	if err != nil {
		t.Fatalf("ReadCodeEvidence: %v", err)
	}
	if len(evidence.Snippets) == 0 {
		t.Fatal("expected at least one snippet")
	}
	if evidence.Snippets[0].StartLine != 1 {
		t.Errorf("StartLine=%d, want 1 (whole file)", evidence.Snippets[0].StartLine)
	}
	if evidence.Snippets[0].Content == "" {
		t.Error("expected non-empty content")
	}
	if evidence.TotalTokens <= 0 {
		t.Error("expected positive token count")
	}
}

func TestReadCodeEvidence_LargeFile_TargetedExtraction(t *testing.T) {
	dir := t.TempDir()
	srcDir := filepath.Join(dir, "pkg")
	os.MkdirAll(srcDir, 0755)

	var lines []string
	lines = append(lines, "package pkg", "")
	lines = append(lines, "func Irrelevant() {", "\treturn", "}", "")
	lines = append(lines, "func EncodeModule(data string) []byte {")
	for i := 0; i < 20; i++ {
		lines = append(lines, "\t// processing line")
	}
	lines = append(lines, "\treturn nil", "}")
	for len(lines) < 200 {
		lines = append(lines, "// padding")
	}

	content := strings.Join(lines, "\n") + "\n"
	os.WriteFile(filepath.Join(srcDir, "encoder.go"), []byte(content), 0644)

	cm := &CodeMap{
		RootDir: dir,
		Zoom2: []ModuleMap{
			{
				Path:     "pkg/",
				Language: "go",
				Summary:  "encoder module",
				KeyTypes: []string{"EncodeModule", "Irrelevant"},
				Files: []FileInfo{
					{RelPath: "pkg/encoder.go", Identifiers: []string{"EncodeModule", "Irrelevant"}},
				},
			},
		},
	}
	idx := BuildCodeIndex(cm)

	engine, _ := newTestEngineWithGen(t)
	evidence, err := engine.ReadCodeEvidence(ctx(t), "test-ns", "encode module", ReadCodeOpts{
		MaxTokens: 4000,
		MaxFiles:  8,
	}, cm, idx)
	if err != nil {
		t.Fatal(err)
	}
	if len(evidence.Snippets) == 0 {
		t.Fatal("expected snippets")
	}
	snippet := evidence.Snippets[0]
	if snippet.StartLine == 1 {
		t.Error("expected targeted extraction, not whole file (file is 200 lines)")
	}
	if snippet.Content == "" {
		t.Error("expected non-empty content")
	}
}

func TestReadCodeEvidence_TokenBudget(t *testing.T) {
	dir := t.TempDir()
	srcDir := filepath.Join(dir, "pkg")
	os.MkdirAll(srcDir, 0755)

	for _, name := range []string{"a.go", "b.go", "c.go"} {
		content := "package pkg\n\n"
		for i := 0; i < 50; i++ {
			content += "// This is line number filling space for tokens\n"
		}
		os.WriteFile(filepath.Join(srcDir, name), []byte(content), 0644)
	}

	cm := &CodeMap{
		RootDir: dir,
		Zoom2: []ModuleMap{
			{
				Path:     "pkg/",
				Language: "go",
				Summary:  "test module",
				Files: []FileInfo{
					{RelPath: "pkg/a.go", Identifiers: []string{"A"}},
					{RelPath: "pkg/b.go", Identifiers: []string{"B"}},
					{RelPath: "pkg/c.go", Identifiers: []string{"C"}},
				},
			},
		},
	}
	idx := BuildCodeIndex(cm)

	engine, _ := newTestEngineWithGen(t)
	evidence, err := engine.ReadCodeEvidence(ctx(t), "test-ns", "test", ReadCodeOpts{
		MaxTokens: 200,
		MaxFiles:  8,
	}, cm, idx)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.TotalTokens > 250 {
		t.Errorf("exceeded budget: %d tokens", evidence.TotalTokens)
	}
}

func TestReadCodeEvidence_MissingFile(t *testing.T) {
	dir := t.TempDir()

	cm := &CodeMap{
		RootDir: dir,
		Zoom2: []ModuleMap{
			{
				Path:     "pkg/",
				Language: "go",
				Summary:  "ghost module",
				Files: []FileInfo{
					{RelPath: "pkg/ghost.go", Identifiers: []string{"Ghost"}},
				},
			},
		},
	}
	idx := BuildCodeIndex(cm)

	engine, _ := newTestEngineWithGen(t)
	evidence, err := engine.ReadCodeEvidence(ctx(t), "test-ns", "ghost", ReadCodeOpts{
		MaxTokens: 4000,
		MaxFiles:  8,
	}, cm, idx)
	if err != nil {
		t.Fatal(err)
	}
	if len(evidence.Snippets) != 0 {
		t.Errorf("expected 0 snippets for missing file, got %d", len(evidence.Snippets))
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./dualmem/ -run "TestReadCodeEvidence" -v`
Expected: FAIL — `ReadCodeEvidence` undefined

- [ ] **Step 4: Implement `ReadCodeEvidence`**

Add to `dualmem/dualmem.go` after the `Consult` method (after line ~1600):

```go
// ReadCodeEvidence ranks files against a query and extracts relevant code snippets
// within a token budget. This is the core evidence pipeline used by Consult and Explore.
func (e *Engine) ReadCodeEvidence(ctx context.Context, namespace, query string, opts ReadCodeOpts, cm *CodeMap, idx *CodeIndex) (*CodeEvidence, error) {
	if opts.MaxTokens <= 0 {
		opts.MaxTokens = 4000
	}
	if opts.MaxFiles <= 0 {
		opts.MaxFiles = 8
	}

	fileResults := SearchCodeMapFiles(cm, idx, query, opts.MaxFiles*2)

	if len(opts.SeedPaths) > 0 {
		seedSet := make(map[string]bool)
		for _, sp := range opts.SeedPaths {
			seedSet[sp] = true
		}
		var boosted, rest []FileResult
		for _, fr := range fileResults {
			if seedSet[fr.FilePath] {
				boosted = append(boosted, fr)
			} else {
				rest = append(rest, fr)
			}
		}
		fileResults = append(boosted, rest...)
	}

	if len(fileResults) > opts.MaxFiles {
		fileResults = fileResults[:opts.MaxFiles]
	}

	evidence := &CodeEvidence{}
	rootDir := cm.RootDir

	for _, fr := range fileResults {
		if evidence.TotalTokens >= opts.MaxTokens {
			break
		}

		fullPath := filepath.Join(rootDir, fr.FilePath)
		lines, err := readFileLines(fullPath)
		if err != nil {
			continue
		}

		numLines := len(lines)
		lang := detectLangFromPath(fr.FilePath)
		remaining := opts.MaxTokens - evidence.TotalTokens

		if numLines < 150 {
			content := strings.Join(lines, "\n")
			tokens := estimateTokens(content)
			if tokens > remaining {
				continue
			}
			evidence.Snippets = append(evidence.Snippets, CodeSnippet{
				FilePath:   fr.FilePath,
				StartLine:  1,
				EndLine:    numLines,
				Content:    content,
				Relevance:  fr.Summary,
				TokenCount: tokens,
			})
			evidence.TotalTokens += tokens
		} else {
			fileIdentifiers := findFileIdentifiers(cm, fr.FilePath)
			matches := matchIdentifiers(fileIdentifiers, query)

			if len(matches) == 0 {
				end := 80
				if end > numLines {
					end = numLines
				}
				content := strings.Join(lines[:end], "\n")
				tokens := estimateTokens(content)
				if tokens > remaining {
					continue
				}
				evidence.Snippets = append(evidence.Snippets, CodeSnippet{
					FilePath:   fr.FilePath,
					StartLine:  1,
					EndLine:    end,
					Content:    content,
					Relevance:  fr.Summary,
					TokenCount: tokens,
				})
				evidence.TotalTokens += tokens
			} else {
				for _, ident := range matches {
					if evidence.TotalTokens >= opts.MaxTokens {
						break
					}
					startIdx, endIdx := extractBlock(lines, ident, lang)
					if startIdx < 0 {
						continue
					}
					content := strings.Join(lines[startIdx:endIdx+1], "\n")
					tokens := estimateTokens(content)
					if tokens > opts.MaxTokens-evidence.TotalTokens {
						continue
					}
					evidence.Snippets = append(evidence.Snippets, CodeSnippet{
						FilePath:   fr.FilePath,
						StartLine:  startIdx + 1,
						EndLine:    endIdx + 1,
						Content:    content,
						Relevance:  fr.Summary,
						TokenCount: tokens,
					})
					evidence.TotalTokens += tokens
				}
			}
		}
	}

	return evidence, nil
}

// findFileIdentifiers looks up identifiers for a file path in the codemap.
func findFileIdentifiers(cm *CodeMap, filePath string) []string {
	for _, mod := range cm.Zoom2 {
		for _, fi := range mod.Files {
			if fi.RelPath == filePath {
				return fi.Identifiers
			}
		}
	}
	return nil
}

// detectLangFromPath infers language from file extension.
func detectLangFromPath(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".go":
		return "go"
	case ".py":
		return "python"
	case ".ts", ".tsx":
		return "typescript"
	case ".js", ".jsx":
		return "javascript"
	case ".rs":
		return "rust"
	default:
		return "other"
	}
}
```

- [ ] **Step 5: Run tests**

Run: `go test ./dualmem/ -run "TestReadCodeEvidence" -v`
Expected: All PASS

- [ ] **Step 6: Commit**

```bash
git add dualmem/dualmem.go dualmem/types.go dualmem/explore_test.go
git commit -m "feat(explore): ReadCodeEvidence engine method — hybrid file extraction with token budget"
```

---

### Task 3: `Explore` Engine Method + `ExploreResult` Type

The `Explore` method calls `ReadCodeEvidence` then synthesizes a short summary from the actual snippets. Returns `ExploreResult` with snippets-first formatting.

**Files:**
- Modify: `dualmem/types.go` (add `ExploreResult` type + `FormatSnippetsFirst`)
- Modify: `dualmem/dualmem.go` (add `Explore` method)
- Modify: `dualmem/explore_test.go` (add tests)

- [ ] **Step 1: Add `ExploreResult` type and formatter to `dualmem/types.go`**

Add after the `CodeSnippet` type. Note: `types.go` already imports `"fmt"` and `"strings"` (used by `FormatText`).

```go
// ExploreResult holds the output of an Explore query — code snippets + short summary.
type ExploreResult struct {
	Query       string
	Evidence    *CodeEvidence
	Summary     string // 50-100 word grounded summary
	TotalTokens int
}

// FormatSnippetsFirst renders the result in snippets-first format for context injection.
func (r *ExploreResult) FormatSnippetsFirst() string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("[Explore: %s]\n", r.Query))
	sb.WriteString(fmt.Sprintf("%d files, %d tokens\n\n", len(r.Evidence.Snippets), r.TotalTokens))

	for _, s := range r.Evidence.Snippets {
		sb.WriteString(fmt.Sprintf("--- %s:%d-%d (%s) ---\n", s.FilePath, s.StartLine, s.EndLine, s.Relevance))
		sb.WriteString(s.Content)
		sb.WriteString("\n\n")
	}

	if r.Summary != "" {
		sb.WriteString("[Summary]\n")
		sb.WriteString(r.Summary)
		sb.WriteString("\n")
	}

	return sb.String()
}
```

- [ ] **Step 2: Write failing tests for `Explore` and `FormatSnippetsFirst`**

Append to `dualmem/explore_test.go`:

```go
func TestExplore_ProducesSummary(t *testing.T) {
	dir := t.TempDir()
	srcDir := filepath.Join(dir, "pkg")
	os.MkdirAll(srcDir, 0755)

	os.WriteFile(filepath.Join(srcDir, "main.go"), []byte("package pkg\n\nfunc Run() {\n\tfmt.Println(\"running\")\n}\n"), 0644)

	engine, gen := newTestEngineWithGen(t)
	engine.cfg.RootDir = dir

	result, err := engine.Explore(ctx(t), "test-ns", "how does run work", 4000)
	if err != nil {
		t.Fatal(err)
	}
	if result.Summary == "" {
		t.Error("expected non-empty summary")
	}
	if gen.generateCalls == 0 {
		t.Error("expected GenerateText to be called for summary")
	}
}

func TestExploreResult_FormatSnippetsFirst(t *testing.T) {
	result := &ExploreResult{
		Query: "how does encoding work",
		Evidence: &CodeEvidence{
			Snippets: []CodeSnippet{
				{
					FilePath:   "hdc.go",
					StartLine:  15,
					EndLine:    42,
					Content:    "func EncodeModule() {\n\t// encode\n}",
					Relevance:  "hdc",
					TokenCount: 10,
				},
			},
			TotalTokens: 10,
		},
		Summary:     "Encoding uses HDC vectors with 2048 dimensions.",
		TotalTokens: 20,
	}

	text := result.FormatSnippetsFirst()

	if !strings.Contains(text, "[Explore: how does encoding work]") {
		t.Error("missing header")
	}
	if !strings.Contains(text, "--- hdc.go:15-42 (hdc) ---") {
		t.Error("missing snippet header")
	}
	if !strings.Contains(text, "func EncodeModule()") {
		t.Error("missing snippet content")
	}
	if !strings.Contains(text, "[Summary]") {
		t.Error("missing summary section")
	}
	if !strings.Contains(text, "Encoding uses HDC vectors") {
		t.Error("missing summary content")
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./dualmem/ -run "TestExplore" -v`
Expected: FAIL — `Explore` method undefined

- [ ] **Step 4: Implement `Explore` engine method**

Add to `dualmem/dualmem.go` after `ReadCodeEvidence`:

```go
// Explore reads actual code from ranked files and produces a grounded summary.
// Unlike Consult, it returns a snippets-first format (code blocks + short summary)
// optimized for context injection. Does not cache to knowledge docs.
func (e *Engine) Explore(ctx context.Context, namespace, query string, budget int) (*ExploreResult, error) {
	if budget <= 0 {
		budget = 4000
	}

	cm, idx := e.getOrGenerateCodeMap(ctx, namespace)

	snippetBudget := budget * 80 / 100

	evidence, err := e.ReadCodeEvidence(ctx, namespace, query, ReadCodeOpts{
		MaxTokens: snippetBudget,
		MaxFiles:  8,
	}, cm, idx)
	if err != nil {
		return nil, fmt.Errorf("explore: read code evidence: %w", err)
	}

	var summary string
	gen, genErr := e.getSynthesisGenerator()
	if genErr == nil && len(evidence.Snippets) > 0 {
		var snippetText strings.Builder
		for _, s := range evidence.Snippets {
			snippetText.WriteString(fmt.Sprintf("--- %s:%d-%d ---\n", s.FilePath, s.StartLine, s.EndLine))
			snippetText.WriteString(s.Content)
			snippetText.WriteString("\n\n")
		}

		prompt := fmt.Sprintf(
			"Given the following code snippets related to %q, write a brief summary (50-100 words) "+
				"explaining what this code does and how the pieces connect. Be specific — reference "+
				"actual function names and files.\n\n%s", query, snippetText.String())

		text, err := gen.GenerateText(ctx, prompt, 200)
		if err == nil {
			summary = strings.TrimSpace(text)
		}
	}

	totalTokens := evidence.TotalTokens + estimateTokens(summary)

	return &ExploreResult{
		Query:       query,
		Evidence:    evidence,
		Summary:     summary,
		TotalTokens: totalTokens,
	}, nil
}
```

- [ ] **Step 5: Run tests**

Run: `go test ./dualmem/ -run "TestExplore" -v`
Expected: All PASS

- [ ] **Step 6: Commit**

```bash
git add dualmem/dualmem.go dualmem/types.go dualmem/explore_test.go
git commit -m "feat(explore): Explore engine method with snippets-first summary"
```

---

### Task 4: Upgrade `Consult` Synthesis with Code Evidence

Modify the existing `Consult` method to read actual code before synthesizing. The synthesis prompt now includes real code snippets.

**Files:**
- Modify: `dualmem/dualmem.go` (update `Consult` method)
- Modify: `dualmem/knowledge_test.go` (add `lastPrompt` to mock)
- Modify: `dualmem/consult_test.go` (add code evidence test)

- [ ] **Step 1: Add `lastPrompt` field to mock in `knowledge_test.go`**

In `dualmem/knowledge_test.go`, find the `mockTextGenSummarizer` struct (around line 12-30). Add `lastPrompt string` field and update `GenerateText` to capture it:

The struct currently looks like:
```go
type mockTextGenSummarizer struct {
	generateCalls int
	// ... possibly other fields
}
```

Change to:
```go
type mockTextGenSummarizer struct {
	generateCalls int
	lastPrompt    string
}
```

And update the `GenerateText` method:
```go
func (m *mockTextGenSummarizer) GenerateText(_ context.Context, prompt string, _ int) (string, error) {
	m.generateCalls++
	m.lastPrompt = prompt
	return "Mock synthesized explanation of the subsystem.", nil
}
```

- [ ] **Step 2: Write failing test for consult with code evidence**

Append to `dualmem/consult_test.go`. Add `"os"` and `"path/filepath"` to imports:

```go
func TestConsult_SynthesisPromptIncludesCode(t *testing.T) {
	dir := t.TempDir()
	srcDir := filepath.Join(dir, "pkg")
	os.MkdirAll(srcDir, 0755)
	os.WriteFile(filepath.Join(srcDir, "auth.go"), []byte("package pkg\n\nfunc ValidateToken(token string) bool {\n\treturn token != \"\"\n}\n"), 0644)

	engine, gen := newTestEngineWithGen(t)
	engine.cfg.RootDir = dir

	engine.AddWithOptions(ctx(t), MemoryInput{
		UserMessage: "Auth validates JWT tokens",
		Salience:    0.9,
		Files:       []string{"pkg/auth.go"},
	}, "test-ns")

	_, err := engine.Consult(ctx(t), "test-ns", "how does auth validation work", 2000)
	if err != nil {
		t.Fatal(err)
	}

	if gen.generateCalls == 0 {
		t.Fatal("expected synthesis call")
	}
	if !strings.Contains(gen.lastPrompt, "[Code Snippets]") {
		t.Error("synthesis prompt missing [Code Snippets] section")
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./dualmem/ -run TestConsult_SynthesisPromptIncludesCode -v`
Expected: FAIL — prompt does not contain `[Code Snippets]`

- [ ] **Step 4: Update `Consult` to include code evidence**

In `dualmem/dualmem.go`, inside the `Consult` method:

**4a.** After the structural evidence gathering (after the `evidenceText := formatCallGraph(...)` line around line 1417) and before the cache check (`if docMatchScore >= 0.75`), add:

```go
	// 3.5 Read code evidence for grounded synthesis
	var codeEvidence *CodeEvidence
	if codeMap != nil {
		codeEvidence, _ = e.ReadCodeEvidence(ctx, namespace, query, ReadCodeOpts{
			MaxTokens: budget / 2,
			MaxFiles:  6,
			SeedPaths: seedPaths,
		}, codeMap, codeIdx)
	}
```

Note: `codeMap` and `codeIdx` are the existing variables — but in the current code they're named `codeMap` at line 1367 and the function returns `(codeMap, codeIdx)`. Check the actual variable names:
- Line 1366: `codeMap, codeIdx := e.getOrGenerateCodeMap(ctx, namespace)` — but wait, the current code uses `codeMap` for the `*CodeMap` and then refers to it as `codeMap`. Actually looking at line 1366-1367:
```go
codeMap, codeIdx := e.getOrGenerateCodeMap(ctx, namespace)
```
Wait, the actual variable name at line 1366 is not `codeMap` — let me re-read. The current code at line 1366 says:
```go
codeMap, codeIdx := e.getOrGenerateCodeMap(ctx, namespace)
```
But actually looking at the read output, line 1366 says:
```go
	codeMap, codeIdx := e.getOrGenerateCodeMap(ctx, namespace)
```
No — the actual code uses `codeMap` at line 1367 as `if codeMap != nil`. Let me check the original naming in the Consult function. The var is referred to as `codeMap` at line 1367. Update: actually the lines say:

```go
	codeMap, codeIdx := e.getOrGenerateCodeMap(ctx, namespace)
	if codeMap != nil {
```

Wait — that's not what the actual code says. Looking at the Read output again for lines 1366-1368:
```
1366		codeMap, codeIdx := e.getOrGenerateCodeMap(ctx, namespace)
1367		if codeMap != nil {
```

Hmm, but the actual variable was `cm` not `codeMap`. Let me look again at the real code... In the `Consult` method the code says:
```go
	codeMap, codeIdx := e.getOrGenerateCodeMap(ctx, namespace)
```
No, it actually says at line 1366-1367:
```go
	codeMap, codeIdx := e.getOrGenerateCodeMap(ctx, namespace)
	if codeMap != nil {
```

Since the agent implementing this will read the actual code, tell them: locate the `getOrGenerateCodeMap` call in `Consult` and use the same local variable names for the CodeMap and CodeIndex. Add the `ReadCodeEvidence` call right after the co-change/structural evidence gathering block.

**4b.** Replace the synthesis prompt construction (the block starting with `promptParts = append(promptParts, fmt.Sprintf(` around line 1465) with:

```go
		var promptParts []string
		promptParts = append(promptParts, fmt.Sprintf(
			"Given the following code and structural evidence about %q in this codebase, "+
				"write a concise explanation (200-400 words) of how this subsystem works.\n\n"+
				"Include: key files and their roles, data/control flow between components, "+
				"important design decisions or constraints. Reference specific code where helpful.", query))

		if codeEvidence != nil && len(codeEvidence.Snippets) > 0 {
			var snippetText strings.Builder
			for _, s := range codeEvidence.Snippets {
				snippetText.WriteString(fmt.Sprintf("--- %s:%d-%d ---\n", s.FilePath, s.StartLine, s.EndLine))
				snippetText.WriteString(s.Content)
				snippetText.WriteString("\n\n")
			}
			promptParts = append(promptParts, "[Code Snippets]\n"+snippetText.String())
		}

		if evidenceText != "" {
			promptParts = append(promptParts, "[Structural Evidence]\n"+evidenceText)
		}
```

Remove the old line that says `"Do NOT include code snippets. Focus on conceptual understanding"` — it's been replaced by the new prompt above.

- [ ] **Step 5: Run all consult tests**

Run: `go test ./dualmem/ -run "TestConsult" -v`
Expected: All PASS (existing tests + new test)

- [ ] **Step 6: Commit**

```bash
git add dualmem/dualmem.go dualmem/consult_test.go dualmem/knowledge_test.go
git commit -m "feat(explore): upgrade consult synthesis with real code evidence"
```

---

### Task 5: `explore` CLI Subcommand

Add the `explore` subcommand to the CLI, following the same patterns as `consult`.

**Files:**
- Modify: `cmd/dualmem/main.go` (add `explore` case + `cmdExplore` function + update `printUsage`)

- [ ] **Step 1: Add `explore` to the command switch**

In `cmd/dualmem/main.go`, find the command switch (around line 265). Add before `case "consult"`:

```go
	case "explore":
		cmdExplore(cfg)
```

- [ ] **Step 2: Update `printUsage`**

In the `printUsage()` function (around line 278), add after the `consult` line:

```go
  explore     Read ranked code files and produce grounded briefing
```

- [ ] **Step 3: Implement `cmdExplore`**

Add after `cmdConsult` function (around line 2272):

```go
func cmdExplore(cfg CLIConfig) {
	fs := flag.NewFlagSet("explore", flag.ExitOnError)
	ns := fs.String("ns", "", "Namespace")
	budget := fs.Int("budget", 4000, "Token budget for snippets")
	fs.Parse(filterFlags(os.Args[2:]))

	var queryParts []string
	for _, arg := range os.Args[2:] {
		if !strings.HasPrefix(arg, "-") {
			queryParts = append(queryParts, arg)
		}
	}
	query := strings.Join(queryParts, " ")
	if query == "" {
		fmt.Fprintf(os.Stderr, "usage: dualmem explore \"query\" [--budget N] [--ns namespace]\n")
		os.Exit(1)
	}

	namespace := resolveNamespace(*ns, cfg)

	engine, err := newEngine(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer engine.Close()

	ctx := context.Background()
	result, err := engine.Explore(ctx, namespace, query, *budget)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	fmt.Print(result.FormatSnippetsFirst())
}
```

- [ ] **Step 4: Build and smoke test**

```bash
cd /Users/donny/Projects/2026/geoffreyengram && go build ./cmd/dualmem/
./dualmem explore "how does HDC encoding work" --budget 3000
```
Expected: Output with `[Explore: ...]` header, code snippets, and a short summary.

- [ ] **Step 5: Commit**

```bash
git add cmd/dualmem/main.go
git commit -m "feat(explore): add explore CLI subcommand"
```

---

### Task 6: Pre-Hook Script

Simple shell script that extracts a topic from the user's prompt and runs `dualmem explore`.

**Files:**
- Create: `hooks/dualmem-explore-prehook.sh`

- [ ] **Step 1: Create the hook script**

Create `hooks/dualmem-explore-prehook.sh`:

```bash
#!/bin/bash
# Pre-hook: run dualmem explore on session start to inject grounded code context.
# Extracts a topic from the user's prompt and feeds it to explore.
# Exits silently if no clear topic is found.

PROMPT=""
if [ -t 0 ]; then
    PROMPT="$1"
else
    PROMPT=$(cat)
fi

if [ -z "$PROMPT" ]; then
    exit 0
fi

# Simple heuristic: take first line, strip conversational prefixes
TOPIC=$(echo "$PROMPT" | head -1 | sed -E "s/^(hey |hi |hello |please |can you |help me |I need to |lets |let's )//i" | cut -c1-120)

# Skip if topic is too short or generic
if [ ${#TOPIC} -lt 10 ]; then
    exit 0
fi

~/go/bin/dualmem explore "$TOPIC" --budget 3000 2>/dev/null
```

- [ ] **Step 2: Make executable and test**

```bash
chmod +x hooks/dualmem-explore-prehook.sh
echo "how does the HDC encoder work in dualmem" | ./hooks/dualmem-explore-prehook.sh
```
Expected: Explore output with code snippets.

```bash
echo "hi" | ./hooks/dualmem-explore-prehook.sh
```
Expected: No output (topic too short).

- [ ] **Step 3: Commit**

```bash
git add hooks/dualmem-explore-prehook.sh
git commit -m "feat(explore): pre-hook script for session-start code exploration"
```

---

### Task 7: `consult --fresh` Flag + CLAUDE.md Docs

Add `--fresh` flag to consult CLI to force re-synthesis with code evidence. Update CLAUDE.md with `explore` documentation.

**Files:**
- Modify: `cmd/dualmem/main.go` (add `--fresh` flag to `cmdConsult`)
- Modify: `dualmem/dualmem.go` (add `DeleteKnowledgeDoc` wrapper if needed)
- Modify: `.claude/CLAUDE.md` (add `explore` reference)

- [ ] **Step 1: Check if `DeleteKnowledgeDoc` exists on store**

Search for `DeleteKnowledgeDoc` in `dualmem/store_sqlite.go`. If it doesn't exist, add to the store:

```go
func (s *SQLiteStore) DeleteKnowledgeDoc(id string) error {
	_, err := s.db.Exec("DELETE FROM knowledge_docs WHERE id = ?", id)
	return err
}
```

And add to the `Store` interface if needed. Then add a thin engine wrapper in `dualmem/dualmem.go`:

```go
// DeleteKnowledgeDoc removes a knowledge doc by ID.
func (e *Engine) DeleteKnowledgeDoc(id string) error {
	return e.store.DeleteKnowledgeDoc(id)
}
```

- [ ] **Step 2: Add `--fresh` flag to `cmdConsult`**

In `cmd/dualmem/main.go`, in `cmdConsult` (around line 2228), add to the flag set:

```go
	fresh := fs.Bool("fresh", false, "Force re-synthesis with code evidence (bypass cache)")
```

Then before calling `engine.Consult`, add cache-busting logic:

```go
	if *fresh {
		ctx := context.Background()
		queryEmb, _ := engine.Embed(ctx, query, "RETRIEVAL_QUERY")
		kdocs, _ := engine.GetKnowledgeDocs(ctx, namespace, query, queryEmb, 3)
		for _, kd := range kdocs {
			engine.DeleteKnowledgeDoc(kd.ID)
		}
	}
```

Note: check if `engine.Embed` and `engine.GetKnowledgeDocs` are public methods. If `Embed` is not directly on Engine, use `engine.embedder.Embed`. If `GetKnowledgeDocs` is not public, the agent should find the right way to call it — it's referenced in the `Consult` method so it should be accessible.

- [ ] **Step 3: Update `.claude/CLAUDE.md`**

In `.claude/CLAUDE.md`, find the code search section that lists dualmem commands. Add `explore` to the command listing:

```
~/go/bin/dualmem explore "query"                         # grounded code briefing (reads actual files)
```

Add a new subsection after the "Deep understanding" section:

```markdown
### Grounded code exploration
```bash
~/go/bin/dualmem explore "how does X work?"  # reads ranked files, extracts snippets, short summary
```

**explore before modifying unfamiliar code.** If you're about to work on code you haven't seen before, run `explore` first. Unlike `consult` (which caches knowledge docs), `explore` is ephemeral — it reads actual code and produces a snippets-first briefing optimized for context injection.
```

- [ ] **Step 4: Build and test**

```bash
go build ./cmd/dualmem/
./dualmem consult "how does HDC work" --fresh
```
Expected: Re-synthesized explanation with code evidence (not cached).

- [ ] **Step 5: Commit**

```bash
git add cmd/dualmem/main.go dualmem/dualmem.go dualmem/store_sqlite.go .claude/CLAUDE.md
git commit -m "feat(explore): consult --fresh flag + CLAUDE.md docs for explore"
```

---

### Task 8: Integration Test — End-to-End

Run `explore` against the actual geoffreyengram repo to verify grounded output.

**Files:**
- Modify: `dualmem/explore_test.go` (add integration test, skipped by default)

- [ ] **Step 1: Write integration test**

Append to `dualmem/explore_test.go`:

```go
func TestExplore_Integration_RealCodebase(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	repoRoot := os.Getenv("DUALMEM_TEST_REPO")
	if repoRoot == "" {
		t.Skip("set DUALMEM_TEST_REPO to run integration tests")
	}

	engine, _ := newTestEngineWithGen(t)
	engine.cfg.RootDir = repoRoot

	result, err := engine.Explore(ctx(t), "claude:geoffreyengram", "how does HDC encoding work", 4000)
	if err != nil {
		t.Fatalf("Explore: %v", err)
	}

	if len(result.Evidence.Snippets) == 0 {
		t.Error("expected code snippets from real codebase")
	}

	foundCode := false
	for _, s := range result.Evidence.Snippets {
		if strings.Contains(s.Content, "func ") || strings.Contains(s.Content, "type ") {
			foundCode = true
			break
		}
	}
	if !foundCode {
		t.Error("snippets don't contain actual Go code")
	}

	if result.Summary == "" {
		t.Error("expected non-empty summary")
	}

	t.Logf("Explore result: %d snippets, %d tokens", len(result.Evidence.Snippets), result.TotalTokens)
	t.Log(result.FormatSnippetsFirst())
}
```

- [ ] **Step 2: Run unit tests (should all pass)**

Run: `go test ./dualmem/ -run "TestExplore|TestReadCodeEvidence|TestExtractBlock|TestConsult" -v -short`
Expected: All PASS

- [ ] **Step 3: Optionally run integration test**

Run: `DUALMEM_TEST_REPO=/Users/donny/Projects/2026/geoffreyengram go test ./dualmem/ -run TestExplore_Integration -v`
Expected: PASS with real code snippets in output.

- [ ] **Step 4: Final commit**

```bash
git add dualmem/explore_test.go
git commit -m "test(explore): integration test for real codebase exploration"
```
