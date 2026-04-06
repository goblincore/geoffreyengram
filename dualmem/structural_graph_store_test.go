package dualmem

import (
	"testing"
)

func TestInsertStructuralEdges(t *testing.T) {
	s := newTestStore(t)
	edges := []StructuralEdge{
		{SourcePath: "pkg/foo.go", TargetPath: "pkg/bar.go", EdgeType: "calls", Weight: 1.0, LineNumber: 42, EnclosingFunction: "main", CalleeName: "Run", Namespace: "test-ns"},
		{SourcePath: "pkg/foo.go", TargetPath: "pkg/util.go", EdgeType: "imports", Weight: 0.7, LineNumber: 5, EnclosingFunction: "", CalleeName: "Helper", Namespace: "test-ns"},
		{SourcePath: "pkg/foo.go", TargetPath: "pkg/types.go", EdgeType: "contains", Weight: 0.5, LineNumber: 0, EnclosingFunction: "", CalleeName: "Config", Namespace: "test-ns"},
	}
	if err := s.InsertStructuralEdges("test-ns", edges); err != nil {
		t.Fatalf("InsertStructuralEdges: %v", err)
	}

	got, err := s.GetStructuralEdgesForPath("test-ns", "pkg/foo.go", 10)
	if err != nil {
		t.Fatalf("GetStructuralEdgesForPath: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 edges, got %d", len(got))
	}

	want := map[string]StructuralEdge{
		"calls":    {SourcePath: "pkg/foo.go", TargetPath: "pkg/bar.go", EdgeType: "calls", Weight: 1.0, LineNumber: 42, EnclosingFunction: "main", CalleeName: "Run"},
		"imports":  {SourcePath: "pkg/foo.go", TargetPath: "pkg/util.go", EdgeType: "imports", Weight: 0.7, LineNumber: 5, EnclosingFunction: "", CalleeName: "Helper"},
		"contains": {SourcePath: "pkg/foo.go", TargetPath: "pkg/types.go", EdgeType: "contains", Weight: 0.5, LineNumber: 0, EnclosingFunction: "", CalleeName: "Config"},
	}
	for _, e := range got {
		w, ok := want[e.EdgeType]
		if !ok {
			t.Errorf("unexpected edge type %q", e.EdgeType)
			continue
		}
		if e.SourcePath != w.SourcePath {
			t.Errorf("SourcePath: got %q, want %q", e.SourcePath, w.SourcePath)
		}
		if e.TargetPath != w.TargetPath {
			t.Errorf("TargetPath: got %q, want %q", e.TargetPath, w.TargetPath)
		}
		if e.Weight != w.Weight {
			t.Errorf("Weight: got %f, want %f", e.Weight, w.Weight)
		}
		if e.LineNumber != w.LineNumber {
			t.Errorf("LineNumber: got %d, want %d", e.LineNumber, w.LineNumber)
		}
		if e.EnclosingFunction != w.EnclosingFunction {
			t.Errorf("EnclosingFunction: got %q, want %q", e.EnclosingFunction, w.EnclosingFunction)
		}
		if e.CalleeName != w.CalleeName {
			t.Errorf("CalleeName: got %q, want %q", e.CalleeName, w.CalleeName)
		}
	}
}

func TestInsertStructuralEdges_Replace(t *testing.T) {
	s := newTestStore(t)

	// First insert: 2 edges
	edges1 := []StructuralEdge{
		{SourcePath: "A", TargetPath: "B", EdgeType: "calls", Weight: 1.0, LineNumber: 1, Namespace: "test-ns"},
		{SourcePath: "A", TargetPath: "C", EdgeType: "imports", Weight: 0.7, LineNumber: 2, Namespace: "test-ns"},
	}
	if err := s.InsertStructuralEdges("test-ns", edges1); err != nil {
		t.Fatalf("first insert: %v", err)
	}

	// Second insert: 1 different edge — full rebuild should replace old edges
	edges2 := []StructuralEdge{
		{SourcePath: "X", TargetPath: "Y", EdgeType: "contains", Weight: 0.5, LineNumber: 10, Namespace: "test-ns"},
	}
	if err := s.InsertStructuralEdges("test-ns", edges2); err != nil {
		t.Fatalf("second insert: %v", err)
	}

	got, err := s.GetStructuralEdgesForPath("test-ns", "X", 10)
	if err != nil {
		t.Fatalf("GetStructuralEdgesForPath: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 edge after rebuild, got %d", len(got))
	}
	if got[0].TargetPath != "Y" || got[0].EdgeType != "contains" {
		t.Errorf("got %+v, want X->Y contains", got[0])
	}

	// Verify old edges are gone
	oldEdges, _ := s.GetStructuralEdgesForPath("test-ns", "A", 10)
	if len(oldEdges) != 0 {
		t.Errorf("old edges should be gone, got %d", len(oldEdges))
	}
}

func TestGetStructuralNeighbors(t *testing.T) {
	s := newTestStore(t)
	edges := []StructuralEdge{
		{SourcePath: "A", TargetPath: "B", EdgeType: "calls", Weight: 1.0, LineNumber: 1, Namespace: "test-ns"},
		{SourcePath: "A", TargetPath: "C", EdgeType: "imports", Weight: 0.7, LineNumber: 2, Namespace: "test-ns"},
		{SourcePath: "D", TargetPath: "A", EdgeType: "calls", Weight: 1.0, LineNumber: 3, Namespace: "test-ns"},
		{SourcePath: "E", TargetPath: "F", EdgeType: "calls", Weight: 1.0, LineNumber: 4, Namespace: "test-ns"},
	}
	if err := s.InsertStructuralEdges("test-ns", edges); err != nil {
		t.Fatalf("InsertStructuralEdges: %v", err)
	}

	got, err := s.GetStructuralNeighbors("test-ns", "A", nil, 50)
	if err != nil {
		t.Fatalf("GetStructuralNeighbors: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 neighbors for A, got %d", len(got))
	}

	// Verify E->F is NOT returned
	for _, e := range got {
		if e.SourcePath == "E" && e.TargetPath == "F" {
			t.Error("E->F should not be in results")
		}
	}

	// Verify expected edges are present
	seen := map[string]bool{}
	for _, e := range got {
		key := e.SourcePath + "->" + e.TargetPath
		seen[key] = true
	}
	for _, want := range []string{"A->B", "A->C", "D->A"} {
		if !seen[want] {
			t.Errorf("missing edge %s", want)
		}
	}
}

func TestGetStructuralNeighbors_EdgeTypeFilter(t *testing.T) {
	s := newTestStore(t)
	edges := []StructuralEdge{
		{SourcePath: "A", TargetPath: "B", EdgeType: "calls", Weight: 1.0, LineNumber: 1, Namespace: "test-ns"},
		{SourcePath: "A", TargetPath: "C", EdgeType: "imports", Weight: 0.7, LineNumber: 2, Namespace: "test-ns"},
		{SourcePath: "A", TargetPath: "D", EdgeType: "contains", Weight: 0.5, LineNumber: 3, Namespace: "test-ns"},
	}
	if err := s.InsertStructuralEdges("test-ns", edges); err != nil {
		t.Fatalf("InsertStructuralEdges: %v", err)
	}

	// Filter to "calls" only
	got, err := s.GetStructuralNeighbors("test-ns", "A", []string{"calls"}, 50)
	if err != nil {
		t.Fatalf("GetStructuralNeighbors: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 calls edge, got %d", len(got))
	}
	if got[0].EdgeType != "calls" || got[0].TargetPath != "B" {
		t.Errorf("got %+v, want A->B calls", got[0])
	}

	// Filter to "imports" and "contains"
	got2, err := s.GetStructuralNeighbors("test-ns", "A", []string{"imports", "contains"}, 50)
	if err != nil {
		t.Fatalf("GetStructuralNeighbors: %v", err)
	}
	if len(got2) != 2 {
		t.Fatalf("expected 2 edges, got %d", len(got2))
	}
	for _, e := range got2 {
		if e.EdgeType != "imports" && e.EdgeType != "contains" {
			t.Errorf("unexpected edge type %q", e.EdgeType)
		}
	}
}

func TestGetStructuralEdgesForPath(t *testing.T) {
	s := newTestStore(t)
	edges := []StructuralEdge{
		{SourcePath: "A", TargetPath: "B", EdgeType: "calls", Weight: 1.0, LineNumber: 1, Namespace: "test-ns"},
		{SourcePath: "A", TargetPath: "C", EdgeType: "imports", Weight: 0.7, LineNumber: 2, Namespace: "test-ns"},
		{SourcePath: "D", TargetPath: "A", EdgeType: "calls", Weight: 1.0, LineNumber: 3, Namespace: "test-ns"},
	}
	if err := s.InsertStructuralEdges("test-ns", edges); err != nil {
		t.Fatalf("InsertStructuralEdges: %v", err)
	}

	got, err := s.GetStructuralEdgesForPath("test-ns", "A", 10)
	if err != nil {
		t.Fatalf("GetStructuralEdgesForPath: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 outbound edges from A, got %d", len(got))
	}

	// Verify D->A (inbound to A) is NOT returned
	for _, e := range got {
		if e.SourcePath == "D" && e.TargetPath == "A" {
			t.Error("D->A should not be returned (inbound, not outbound)")
		}
	}

	// Verify outbound edges
	seen := map[string]bool{}
	for _, e := range got {
		if e.SourcePath != "A" {
			t.Errorf("all edges should have source A, got %q", e.SourcePath)
		}
		seen[e.TargetPath] = true
	}
	if !seen["B"] || !seen["C"] {
		t.Errorf("expected outbound edges A->B and A->C, seen: %v", seen)
	}
}
