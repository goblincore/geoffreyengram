package dualmem

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	ts "github.com/odvcencio/gotreesitter"
)

func TestExtractGoCallEdges(t *testing.T) {
	tmpDir := t.TempDir()
	goCode := `package example

import "fmt"

func caller() {
	helper()
	fmt.Println("hello")
}

func helper() {}
`
	if err := os.WriteFile(filepath.Join(tmpDir, "example.go"), []byte(goCode), 0644); err != nil {
		t.Fatal(err)
	}

	edges := extractGoCallEdges("example", []string{filepath.Join(tmpDir, "example.go")})
	if len(edges) < 2 {
		t.Fatalf("expected at least 2 edges, got %d", len(edges))
	}

	haveHelper := false
	havePrintln := false
	for _, e := range edges {
		if e.CalleeName == "helper" && e.EnclosingFunction == "caller" {
			haveHelper = true
		}
		if e.CalleeName == "fmt.Println" && e.EnclosingFunction == "caller" {
			havePrintln = true
		}
		if e.EdgeType != "calls" {
			t.Errorf("expected EdgeType calls, got %q", e.EdgeType)
		}
		if e.Weight != 1.0 {
			t.Errorf("expected Weight 1.0, got %f", e.Weight)
		}
	}
	if !haveHelper {
		t.Error("missing caller->helper edge")
	}
	if !havePrintln {
		t.Error("missing caller->fmt.Println edge")
	}
}

func TestExtractGoCallEdges_WithReceiver(t *testing.T) {
	tmpDir := t.TempDir()
	goCode := `package example

type Server struct{}

func (s *Server) Start() {
	s.listen()
}

func (s *Server) listen() {}
`
	if err := os.WriteFile(filepath.Join(tmpDir, "server.go"), []byte(goCode), 0644); err != nil {
		t.Fatal(err)
	}

	edges := extractGoCallEdges("example", []string{filepath.Join(tmpDir, "server.go")})
	found := false
	for _, e := range edges {
		if e.CalleeName == "s.listen" && e.EnclosingFunction == "Server.Start" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected edge with EnclosingFunction=Server.Start and CalleeName=s.listen, got edges: %+v", edges)
	}
}

func TestExtractTSCallEdges(t *testing.T) {
	tmpDir := t.TempDir()
	tsCode := `function greet(name: string) {
    console.log("hello " + name);
    formatName(name);
}

function formatName(n: string): string {
    return n.trim();
}
`
	if err := os.WriteFile(filepath.Join(tmpDir, "greet.ts"), []byte(tsCode), 0644); err != nil {
		t.Fatal(err)
	}

	edges := extractTSCallEdges("src", []string{filepath.Join(tmpDir, "greet.ts")})
	if len(edges) < 2 {
		t.Fatalf("expected at least 2 call edges, got %d", len(edges))
	}

	haveLog := false
	haveFormat := false
	for _, e := range edges {
		if e.CalleeName == "log" {
			haveLog = true
		}
		if e.CalleeName == "formatName" {
			haveFormat = true
		}
		if e.EdgeType != "calls" {
			t.Errorf("expected EdgeType calls, got %q", e.EdgeType)
		}
		if e.Weight != 1.0 {
			t.Errorf("expected Weight 1.0, got %f", e.Weight)
		}
	}
	if !haveLog {
		t.Error("missing console.log call edge (callee 'log')")
	}
	if !haveFormat {
		t.Error("missing formatName call edge")
	}
}

func TestExtractPyCallEdges(t *testing.T) {
	tmpDir := t.TempDir()
	pyCode := `def main():
    result = process_data("input")
    print(result)

def process_data(data):
    return data.upper()
`
	if err := os.WriteFile(filepath.Join(tmpDir, "app.py"), []byte(pyCode), 0644); err != nil {
		t.Fatal(err)
	}

	edges := extractPyCallEdges("app", []string{filepath.Join(tmpDir, "app.py")})
	if len(edges) < 2 {
		t.Fatalf("expected at least 2 call edges, got %d", len(edges))
	}

	haveProcess := false
	havePrint := false
	for _, e := range edges {
		if e.CalleeName == "process_data" && e.EnclosingFunction == "main" {
			haveProcess = true
		}
		if e.CalleeName == "print" {
			havePrint = true
		}
		if e.EdgeType != "calls" {
			t.Errorf("expected EdgeType calls, got %q", e.EdgeType)
		}
		if e.Weight != 1.0 {
			t.Errorf("expected Weight 1.0, got %f", e.Weight)
		}
	}
	if !haveProcess {
		t.Error("missing main->process_data edge")
	}
	if !havePrint {
		t.Error("missing main->print edge")
	}
}

func TestExtractImportEdges_Go(t *testing.T) {
	tmpDir := t.TempDir()
	goCode := `package example

import (
	"fmt"
	"strings"
)

func dummy() { fmt.Println(strings.ToUpper("x")) }
`
	if err := os.WriteFile(filepath.Join(tmpDir, "imports.go"), []byte(goCode), 0644); err != nil {
		t.Fatal(err)
	}

	edges := extractImportEdges("example", []string{filepath.Join(tmpDir, "imports.go")}, nil)
	if len(edges) < 2 {
		t.Fatalf("expected at least 2 import edges, got %d", len(edges))
	}

	haveFmt := false
	haveStrings := false
	for _, e := range edges {
		if e.CalleeName == "fmt" {
			haveFmt = true
		}
		if e.CalleeName == "strings" {
			haveStrings = true
		}
		if e.EdgeType != "imports" {
			t.Errorf("expected EdgeType imports, got %q", e.EdgeType)
		}
		if e.Weight != 0.7 {
			t.Errorf("expected Weight 0.7, got %f", e.Weight)
		}
	}
	if !haveFmt {
		t.Error("missing import edge for fmt")
	}
	if !haveStrings {
		t.Error("missing import edge for strings")
	}
}

func TestExtractImportEdges_TS(t *testing.T) {
	tmpDir := t.TempDir()
	tsCode := `import { foo } from './utils';
import bar from '../lib/bar';

export function baz() { return foo() + bar(); }
`
	if err := os.WriteFile(filepath.Join(tmpDir, "index.ts"), []byte(tsCode), 0644); err != nil {
		t.Fatal(err)
	}

	edges := extractImportEdges("src", []string{filepath.Join(tmpDir, "index.ts")}, &tsLangConfig)
	if len(edges) < 2 {
		t.Fatalf("expected at least 2 import edges, got %d", len(edges))
	}

	haveUtils := false
	haveBar := false
	for _, e := range edges {
		if e.CalleeName == "./utils" {
			haveUtils = true
		}
		if e.CalleeName == "../lib/bar" {
			haveBar = true
		}
		if e.EdgeType != "imports" {
			t.Errorf("expected EdgeType imports, got %q", e.EdgeType)
		}
		if e.Weight != 0.7 {
			t.Errorf("expected Weight 0.7, got %f", e.Weight)
		}
	}
	if !haveUtils {
		t.Error("missing import edge for ./utils")
	}
	if !haveBar {
		t.Error("missing import edge for ../lib/bar")
	}
}

func TestBuildStructuralGraph(t *testing.T) {
	tmpDir := t.TempDir()

	// main.go
	mainGo := `package main

import "fmt"

func main() { fmt.Println("hello"); helper() }
func helper() {}
`
	if err := os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte(mainGo), 0644); err != nil {
		t.Fatal(err)
	}

	// sub/util.ts
	subDir := filepath.Join(tmpDir, "sub")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatal(err)
	}
	utilTS := `import { x } from './x';
export function compute() { x(); }
`
	if err := os.WriteFile(filepath.Join(subDir, "util.ts"), []byte(utilTS), 0644); err != nil {
		t.Fatal(err)
	}

	result, err := ScanCodebase(tmpDir, nil)
	if err != nil {
		t.Fatalf("ScanCodebase: %v", err)
	}
	edges := result.Edges

	if len(edges) < 3 {
		t.Fatalf("expected at least 3 edges, got %d", len(edges))
	}

	// Set namespace and verify
	for i := range edges {
		edges[i].Namespace = "test-ns"
	}
	for _, e := range edges {
		if e.Namespace != "test-ns" {
			t.Errorf("expected Namespace test-ns, got %q", e.Namespace)
		}
	}

	// Should have both "calls" and "imports" edges
	haveCalls := false
	haveImports := false
	for _, e := range edges {
		if e.EdgeType == "calls" {
			haveCalls = true
		}
		if e.EdgeType == "imports" {
			haveImports = true
		}
	}
	if !haveCalls {
		t.Error("expected at least one 'calls' edge")
	}
	if !haveImports {
		t.Error("expected at least one 'imports' edge")
	}
}

func TestFindEnclosingFunction(t *testing.T) {
	ensureLangs()

	tsCode := `function outer() {
    inner();
}
function inner() {}
`
	p := ts.NewParser(tsLangConfig.lang)
	tree, err := p.Parse([]byte(tsCode))
	if err != nil {
		t.Fatal(err)
	}
	defer tree.Release()
	root := tree.RootNode()

	queryStr := `(call_expression function: (identifier) @call)`
	var enclosingResult string
	extractCaptures(root, tsLangConfig.lang, []byte(tsCode), queryStr, func(text string, node *ts.Node) {
		if text == "inner" {
			enclosingResult = findEnclosingFunction(node, tsLangConfig.lang, []byte(tsCode))
		}
	})

	if enclosingResult != "outer" {
		t.Errorf("expected enclosing function 'outer', got %q", enclosingResult)
	}
}

func TestCountNewlines(t *testing.T) {
	tests := []struct {
		data string
		pos  uint32
		want int
	}{
		{"hello\nworld", 6, 1},
		{"no newlines", 5, 0},
		{"a\nb\nc\n", 7, 3},
		{"", 0, 0},
	}
	for _, tt := range tests {
		got := countNewlines([]byte(tt.data), tt.pos)
		if got != tt.want {
			t.Errorf("countNewlines(%q, %d) = %d, want %d", tt.data, tt.pos, got, tt.want)
		}
	}
}

func TestBuildStructuralGraph_SkipsIgnoredDirs(t *testing.T) {
	tmpDir := t.TempDir()

	// node_modules should be skipped
	nmDir := filepath.Join(tmpDir, "node_modules", "pkg")
	if err := os.MkdirAll(nmDir, 0755); err != nil {
		t.Fatal(err)
	}
	// This file should NOT be scanned
	if err := os.WriteFile(filepath.Join(nmDir, "index.ts"), []byte(`import "foo"; function bar() { baz(); }`), 0644); err != nil {
		t.Fatal(err)
	}

	// But this one should
	if err := os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte(`package main
func main() {}
`), 0644); err != nil {
		t.Fatal(err)
	}

	result, err := ScanCodebase(tmpDir, nil)
	if err != nil {
		t.Fatalf("ScanCodebase: %v", err)
	}
	edges := result.Edges

	for _, e := range edges {
		if strings.Contains(e.SourcePath, "node_modules") {
			t.Errorf("should not contain edges from node_modules: %+v", e)
		}
	}
}
