package dualmem

import (
	"fmt"
	"testing"
)

// newTestEngineForStructural creates a minimal Engine with a real SQLiteStore,
// allowing tests to call GetStructuralNeighborPaths.
func newTestEngineForStructural(t *testing.T) *Engine {
	t.Helper()
	store := newTestStore(t)
	return &Engine{store: store}
}

func TestGetStructuralNeighborPaths_Basic(t *testing.T) {
	eng := newTestEngineForStructural(t)

	// Insert structural edges: A -> B (calls, 1.0), B -> C (calls, 1.0), A -> D (imports, 0.7)
	edges := []StructuralEdge{
		{SourcePath: "A", TargetPath: "B", EdgeType: "calls", Weight: 1.0, Namespace: "test-ns"},
		{SourcePath: "B", TargetPath: "C", EdgeType: "calls", Weight: 1.0, Namespace: "test-ns"},
		{SourcePath: "A", TargetPath: "D", EdgeType: "imports", Weight: 0.7, Namespace: "test-ns"},
	}
	if err := eng.store.InsertStructuralEdges("test-ns", edges); err != nil {
		t.Fatalf("InsertStructuralEdges: %v", err)
	}

	result := eng.GetStructuralNeighborPaths("test-ns", []string{"A"}, 2)

	// B should be reachable (1-hop via calls from A)
	if _, ok := result["B"]; !ok {
		t.Error("expected B in result (1-hop via calls)")
	}

	// C should be reachable (2-hop via calls: A->B->C)
	if _, ok := result["C"]; !ok {
		t.Error("expected C in result (2-hop via calls)")
	}

	// D should be reachable (1-hop via imports from A)
	if _, ok := result["D"]; !ok {
		t.Error("expected D in result (1-hop via imports)")
	}

	// A should NOT be in result (seed paths excluded)
	if _, ok := result["A"]; ok {
		t.Error("seed path A should not be in result")
	}
}

func TestGetStructuralNeighborPaths_ScoreDecay(t *testing.T) {
	eng := newTestEngineForStructural(t)

	// Insert: A -> B (imports, 0.7), B -> C (imports, 0.7)
	edges := []StructuralEdge{
		{SourcePath: "A", TargetPath: "B", EdgeType: "imports", Weight: 0.7, Namespace: "test-ns"},
		{SourcePath: "B", TargetPath: "C", EdgeType: "imports", Weight: 0.7, Namespace: "test-ns"},
	}
	if err := eng.store.InsertStructuralEdges("test-ns", edges); err != nil {
		t.Fatalf("InsertStructuralEdges: %v", err)
	}

	result := eng.GetStructuralNeighborPaths("test-ns", []string{"A"}, 2)

	// Expected scores:
	// B_score = 1.0 * 0.7 + 0.7 * 0.3 = 0.91
	// C_score = 0.91 * 0.7 + 0.7 * 0.3 = 0.847
	bScore, bOk := result["B"]
	if !bOk {
		t.Fatal("expected B in result")
	}
	cScore, cOk := result["C"]
	if !cOk {
		t.Fatal("expected C in result")
	}

	// B_score > C_score (scores decay across hops)
	if bScore <= cScore {
		t.Errorf("expected B score > C score, got B=%.4f C=%.4f", bScore, cScore)
	}

	// Verify approximate expected values
	const eps = 0.01
	if diff := bScore - 0.91; diff > eps || diff < -eps {
		t.Errorf("B score: got %.4f, want ~0.91", bScore)
	}
	if diff := cScore - 0.847; diff > eps || diff < -eps {
		t.Errorf("C score: got %.4f, want ~0.847", cScore)
	}
}

func TestGetStructuralNeighborPaths_BeamPrune(t *testing.T) {
	eng := newTestEngineForStructural(t)

	// Insert 10 edges: A -> B0..B9 (all calls, weight 1.0)
	// Insert 10 edges: B0 -> C0..B9 -> C9 (all calls, weight 1.0)
	var edges []StructuralEdge
	for i := 0; i < 10; i++ {
		bPath := fmt.Sprintf("B%d", i)
		cPath := fmt.Sprintf("C%d", i)
		edges = append(edges, StructuralEdge{
			SourcePath: "A", TargetPath: bPath, EdgeType: "calls", Weight: 1.0, Namespace: "test-ns",
		})
		edges = append(edges, StructuralEdge{
			SourcePath: bPath, TargetPath: cPath, EdgeType: "calls", Weight: 1.0, Namespace: "test-ns",
		})
	}
	if err := eng.store.InsertStructuralEdges("test-ns", edges); err != nil {
		t.Fatalf("InsertStructuralEdges: %v", err)
	}

	result := eng.GetStructuralNeighborPaths("test-ns", []string{"A"}, 2)

	// Beam search keeps top 5 per hop, so total non-seed paths should be <= 10
	if len(result) > 10 {
		t.Errorf("expected at most 10 non-seed paths (beam width 5), got %d", len(result))
	}

	// Should have at least some paths
	if len(result) == 0 {
		t.Error("expected some paths in result")
	}

	// A should not be in result (seed)
	if _, ok := result["A"]; ok {
		t.Error("seed path A should not be in result")
	}
}

func TestGetStructuralNeighborPaths_EmptySeeds(t *testing.T) {
	eng := newTestEngineForStructural(t)

	result := eng.GetStructuralNeighborPaths("test-ns", nil, 2)
	if result != nil {
		t.Errorf("expected nil for nil seeds, got %v", result)
	}

	result = eng.GetStructuralNeighborPaths("test-ns", []string{}, 2)
	if result != nil {
		t.Errorf("expected nil for empty seeds, got %v", result)
	}
}

func TestGetStructuralNeighborPaths_NoEdges(t *testing.T) {
	eng := newTestEngineForStructural(t)

	// No edges inserted, just query
	result := eng.GetStructuralNeighborPaths("test-ns", []string{"A"}, 2)

	// Should return empty map (no edges to expand), not nil
	if result == nil {
		t.Error("expected non-nil empty map for no-edges case")
	}
	if len(result) != 0 {
		t.Errorf("expected 0 paths, got %d", len(result))
	}
}
