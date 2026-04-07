package dualmem

import (
	"os"
	"path/filepath"
	"testing"
)

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
