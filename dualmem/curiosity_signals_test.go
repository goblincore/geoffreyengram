package dualmem

import (
	"context"
	"testing"
)

// newSignalTestEngine creates a minimal Engine with a real SQLiteStore for signal gathering tests.
func newSignalTestEngine(t *testing.T) *Engine {
	t.Helper()
	store := newTestStore(t)
	return &Engine{
		store: store,
		cfg:   &Config{},
	}
}

func TestGatherSignals_EmptyRepo(t *testing.T) {
	engine := newSignalTestEngine(t)

	cm := &CodeMap{
		Namespace: "test",
		Zoom2: []ModuleMap{
			{
				Path:     "pkg/foo",
				Language: "go",
				Files:    []FileInfo{{RelPath: "pkg/foo/foo.go"}},
			},
		},
	}

	signals, err := engine.GatherCuriositySignals(context.Background(), "test", cm, nil, "")
	if err != nil {
		t.Fatalf("GatherCuriositySignals error: %v", err)
	}
	if signals == nil {
		t.Fatal("expected non-nil signals")
	}
	// No memories yet — count should be 0
	if count := signals.MemoryCounts["pkg/foo"]; count != 0 {
		t.Errorf("expected 0 memory count for pkg/foo, got %d", count)
	}
	// No edges — edge count should be 0
	if count := signals.EdgeCounts["pkg/foo"]; count != 0 {
		t.Errorf("expected 0 edge count, got %d", count)
	}
}

func TestGatherSignals_WithMemories(t *testing.T) {
	engine := newSignalTestEngine(t)
	ns := "test"

	// Insert a memory associated with pkg/bar files
	store := engine.store.(*SQLiteStore)
	dm := &DetailMemory{
		ID:              "mem1",
		Text:            "some decision about pkg/bar",
		ImportanceScore: 0.8,
		Sector:          "semantic",
		Salience:        0.7,
		Type:            "decision",
		Files:           []string{"pkg/bar/bar.go"},
	}
	if err := store.InsertDetail(dm, make([]float32, 768), ns); err != nil {
		t.Fatalf("InsertDetail: %v", err)
	}

	cm := &CodeMap{
		Namespace: ns,
		Zoom2: []ModuleMap{
			{
				Path:     "pkg/bar",
				Language: "go",
				Files:    []FileInfo{{RelPath: "pkg/bar/bar.go"}},
			},
			{
				Path:     "pkg/baz",
				Language: "go",
				Files:    []FileInfo{{RelPath: "pkg/baz/baz.go"}},
			},
		},
	}

	edges := []StructuralEdge{
		{SourcePath: "pkg/bar/bar.go", TargetPath: "pkg/baz/baz.go", EdgeType: "calls", Weight: 1.0},
		{SourcePath: "pkg/baz/baz.go", TargetPath: "pkg/bar/bar.go", EdgeType: "imports", Weight: 0.7},
	}

	signals, err := engine.GatherCuriositySignals(context.Background(), ns, cm, edges, "")
	if err != nil {
		t.Fatalf("GatherCuriositySignals error: %v", err)
	}

	// pkg/bar has 1 memory
	if signals.MemoryCounts["pkg/bar"] < 1 {
		t.Errorf("expected at least 1 memory for pkg/bar, got %d", signals.MemoryCounts["pkg/bar"])
	}
	// pkg/baz has 0 memories
	if signals.MemoryCounts["pkg/baz"] != 0 {
		t.Errorf("expected 0 memories for pkg/baz, got %d", signals.MemoryCounts["pkg/baz"])
	}
	// Both modules should have edges (each file appears as source/target)
	if signals.EdgeCounts["pkg/bar"] == 0 {
		t.Errorf("expected edge count > 0 for pkg/bar")
	}
	if signals.EdgeCounts["pkg/baz"] == 0 {
		t.Errorf("expected edge count > 0 for pkg/baz")
	}

	// Verify signals is usable by RankModules
	targets := RankModules(cm, signals)
	if len(targets) != 2 {
		t.Fatalf("expected 2 targets, got %d", len(targets))
	}
	// pkg/baz should rank higher (no memories = higher curiosity gap)
	if targets[0].ModulePath != "pkg/baz" {
		t.Errorf("expected pkg/baz to rank first (no memories), got %s", targets[0].ModulePath)
	}
}
