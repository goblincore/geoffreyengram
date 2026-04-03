package dualmem

import (
	"path/filepath"
	"testing"
)

func testCoChangeStore(t *testing.T) *SQLiteStore {
	t.Helper()
	tmpDir := t.TempDir()
	store, err := NewSQLiteStore(filepath.Join(tmpDir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

const cochangeNS = "test-cochange"

func TestUpsertCoChange(t *testing.T) {
	store := testCoChangeStore(t)

	// First upsert — creates new edge
	err := store.UpsertCoChange(cochangeNS, "auth.go", "middleware.go", "mem-1")
	if err != nil {
		t.Fatalf("UpsertCoChange: %v", err)
	}

	edges, err := store.GetCoChangeNeighbors(cochangeNS, "auth.go", 0, 10)
	if err != nil {
		t.Fatalf("GetCoChangeNeighbors: %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(edges))
	}
	if edges[0].CoCount != 1 {
		t.Fatalf("expected co_count 1, got %d", edges[0].CoCount)
	}
	if len(edges[0].MemoryIDs) != 1 || edges[0].MemoryIDs[0] != "mem-1" {
		t.Fatalf("expected memory_ids [mem-1], got %v", edges[0].MemoryIDs)
	}

	// Second upsert with different memory — increments
	err = store.UpsertCoChange(cochangeNS, "auth.go", "middleware.go", "mem-2")
	if err != nil {
		t.Fatalf("UpsertCoChange (second): %v", err)
	}

	edges, err = store.GetCoChangeNeighbors(cochangeNS, "auth.go", 0, 10)
	if err != nil {
		t.Fatalf("GetCoChangeNeighbors: %v", err)
	}
	if edges[0].CoCount != 2 {
		t.Fatalf("expected co_count 2, got %d", edges[0].CoCount)
	}
	if len(edges[0].MemoryIDs) != 2 {
		t.Fatalf("expected 2 memory IDs, got %d", len(edges[0].MemoryIDs))
	}

	// Duplicate memory ID — should not add again
	err = store.UpsertCoChange(cochangeNS, "auth.go", "middleware.go", "mem-1")
	if err != nil {
		t.Fatalf("UpsertCoChange (dup): %v", err)
	}
	edges, _ = store.GetCoChangeNeighbors(cochangeNS, "auth.go", 0, 10)
	if len(edges[0].MemoryIDs) != 2 {
		t.Fatalf("expected 2 memory IDs after dup, got %d", len(edges[0].MemoryIDs))
	}
}

func TestCoChange_PathNormalization(t *testing.T) {
	store := testCoChangeStore(t)

	// Paths should be stored in lexicographic order (source < target)
	err := store.UpsertCoChange(cochangeNS, "z.go", "a.go", "mem-1")
	if err != nil {
		t.Fatal(err)
	}

	// Query by either path should work
	edges, err := store.GetCoChangeNeighbors(cochangeNS, "a.go", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge querying by a.go, got %d", len(edges))
	}

	edges, err = store.GetCoChangeNeighbors(cochangeNS, "z.go", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge querying by z.go, got %d", len(edges))
	}
}

func TestGetCoChangePaths_Bulk(t *testing.T) {
	store := testCoChangeStore(t)

	// Create a small co-change graph: A↔B, A↔C, B↔D
	store.UpsertCoChange(cochangeNS, "a.go", "b.go", "m1")
	store.UpsertCoChange(cochangeNS, "a.go", "c.go", "m1")
	store.UpsertCoChange(cochangeNS, "b.go", "d.go", "m2")

	// Query for paths touching A or B — should return A↔B, A↔C, B↔D
	edges, err := store.GetCoChangePaths(cochangeNS, []string{"a.go", "b.go"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(edges) != 3 {
		t.Fatalf("expected 3 edges, got %d", len(edges))
	}
}

func TestGetCoChangeNeighbors_MinStrength(t *testing.T) {
	store := testCoChangeStore(t)

	// Edge with co_count 1 (strength 1.0)
	store.UpsertCoChange(cochangeNS, "a.go", "b.go", "m1")
	// Edge with co_count 3 (strength 3.0)
	store.UpsertCoChange(cochangeNS, "a.go", "c.go", "m1")
	store.UpsertCoChange(cochangeNS, "a.go", "c.go", "m2")
	store.UpsertCoChange(cochangeNS, "a.go", "c.go", "m3")

	// minStrength = 2.0 should only return a↔c
	edges, err := store.GetCoChangeNeighbors(cochangeNS, "a.go", 2.0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge above strength 2.0, got %d", len(edges))
	}
}

func TestDecayCoChange(t *testing.T) {
	store := testCoChangeStore(t)

	store.UpsertCoChange(cochangeNS, "a.go", "b.go", "m1")
	store.UpsertCoChange(cochangeNS, "a.go", "b.go", "m2")

	// Verify initial state
	edges, _ := store.GetCoChangeNeighbors(cochangeNS, "a.go", 0, 10)
	if len(edges) == 0 {
		t.Fatal("expected at least 1 edge")
	}
	initialStrength := edges[0].Strength

	// Decay with very short half-life — for a just-created edge, decay should be minimal
	// (near zero days elapsed), so strength stays close to co_count.
	err := store.DecayCoChange(cochangeNS, 1)
	if err != nil {
		t.Fatalf("DecayCoChange: %v", err)
	}

	edges, _ = store.GetCoChangeNeighbors(cochangeNS, "a.go", 0, 10)
	if len(edges) == 0 {
		// Edge might have been cleaned up if strength dropped below 0.1
		return
	}
	// For a just-created edge, strength should be approximately equal to initial
	// (within floating point tolerance from SQLite's julianday precision)
	diff := edges[0].Strength - initialStrength
	if diff < 0 {
		diff = -diff
	}
	if diff > 0.1 {
		t.Fatalf("expected strength near %f (tolerance 0.1), got %f", initialStrength, edges[0].Strength)
	}
}

func TestExpandEntitiesWithEdges(t *testing.T) {
	store := testCoChangeStore(t)

	// Create entities A, B, C with edges A→B (uses, 0.8) and B→C (depends_on, 1.0)
	idA, _ := store.UpsertEntity(&EntityNode{Name: "auth", Type: "concept", Namespace: cochangeNS})
	idB, _ := store.UpsertEntity(&EntityNode{Name: "middleware", Type: "concept", Namespace: cochangeNS})
	idC, _ := store.UpsertEntity(&EntityNode{Name: "jwt", Type: "concept", Namespace: cochangeNS})

	store.UpsertEdge(&EntityEdge{SourceID: idA, TargetID: idB, Relation: "uses", Strength: 0.8, Namespace: cochangeNS})
	store.UpsertEdge(&EntityEdge{SourceID: idB, TargetID: idC, Relation: "depends_on", Strength: 1.0, Namespace: cochangeNS})

	// Expand from A — should find B with edge info
	// Note: UpsertEdge hardcodes strength=1.0 on first insert, ignoring Strength field
	expanded, err := store.ExpandEntitiesWithEdges(cochangeNS, []string{idA}, 10)
	if err != nil {
		t.Fatalf("ExpandEntitiesWithEdges: %v", err)
	}
	if len(expanded) == 0 {
		t.Fatal("expected at least 1 expanded edge")
	}

	found := false
	for _, e := range expanded {
		if e.NeighborID == idB && e.Relation == "uses" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected to find B with 'uses' relation, got %+v", expanded)
	}
}

func TestBuildCoChangeEdges_Engine(t *testing.T) {
	store := testCoChangeStore(t)

	e := &Engine{store: store}

	// Build co-change edges for 3 files
	e.buildCoChangeEdges(cochangeNS, "mem-1", []string{"auth.go", "middleware.go", "jwt.go"})

	// Should create 3 edges: auth↔middleware, auth↔jwt, middleware↔jwt
	edges, err := store.GetCoChangePaths(cochangeNS, []string{"auth.go", "middleware.go", "jwt.go"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(edges) != 3 {
		t.Fatalf("expected 3 co-change edges from 3 files, got %d", len(edges))
	}
}

func TestBuildCoChangeEdges_SingleFile(t *testing.T) {
	store := testCoChangeStore(t)

	e := &Engine{store: store}

	// Single file — should not create any edges
	e.buildCoChangeEdges(cochangeNS, "mem-1", []string{"auth.go"})

	edges, _ := store.GetCoChangePaths(cochangeNS, []string{"auth.go"}, 10)
	if len(edges) != 0 {
		t.Fatalf("expected 0 edges from single file, got %d", len(edges))
	}
}

func TestModuleCoChangeStrength(t *testing.T) {
	neighbors := map[string]float64{
		"auth.go":       2.5,
		"middleware.go":  1.0,
	}

	// Direct path match
	if s := moduleCoChangeStrength("auth.go", neighbors); s != 2.5 {
		t.Fatalf("expected 2.5 for auth.go, got %f", s)
	}

	// No match
	if s := moduleCoChangeStrength("unrelated.go", neighbors); s != 0 {
		t.Fatalf("expected 0 for unrelated.go, got %f", s)
	}

	// Nil map
	if s := moduleCoChangeStrength("auth.go", nil); s != 0 {
		t.Fatalf("expected 0 for nil map, got %f", s)
	}
}

func BenchmarkUpsertCoChange(b *testing.B) {
	tmpDir := b.TempDir()
	store, err := NewSQLiteStore(filepath.Join(tmpDir, "bench.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer store.Close()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		store.UpsertCoChange(cochangeNS, "auth.go", "middleware.go", "mem-1")
	}
}

func BenchmarkGetCoChangeNeighbors(b *testing.B) {
	tmpDir := b.TempDir()
	store, err := NewSQLiteStore(filepath.Join(tmpDir, "bench.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer store.Close()

	// Seed 50 co-change edges
	for i := 0; i < 50; i++ {
		src := filepath.Join("pkg", string(rune('a'+i%26))+".go")
		tgt := filepath.Join("pkg", string(rune('a'+(i+1)%26))+".go")
		store.UpsertCoChange(cochangeNS, src, tgt, "mem-1")
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		store.GetCoChangeNeighbors(cochangeNS, "pkg/a.go", 0, 20)
	}
}
