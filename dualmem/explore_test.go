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
