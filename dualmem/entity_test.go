package dualmem

import (
	"path/filepath"
	"testing"
)

// testEntityStore creates a temporary SQLiteStore for entity graph tests.
func testEntityStore(t *testing.T) *SQLiteStore {
	t.Helper()
	tmpDir := t.TempDir()
	store, err := NewSQLiteStore(filepath.Join(tmpDir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

const testNS = "test-ns"

func TestUpsertEntity(t *testing.T) {
	store := testEntityStore(t)

	node := &EntityNode{
		Name:      "auth.go",
		Type:      "file",
		Namespace: testNS,
	}

	// First insert — should create new entity
	id1, err := store.UpsertEntity(node)
	if err != nil {
		t.Fatalf("UpsertEntity (first): %v", err)
	}
	if id1 == "" {
		t.Fatal("expected non-empty ID from first upsert")
	}

	// Second insert with same name/type/namespace — should return same ID
	id2, err := store.UpsertEntity(node)
	if err != nil {
		t.Fatalf("UpsertEntity (second): %v", err)
	}
	if id2 != id1 {
		t.Fatalf("expected same ID on duplicate upsert, got %s vs %s", id1, id2)
	}

	// Verify mention_count incremented
	entities, err := store.GetEntitiesByName(testNS, "auth.go", 10)
	if err != nil {
		t.Fatalf("GetEntitiesByName: %v", err)
	}
	if len(entities) != 1 {
		t.Fatalf("expected 1 entity, got %d", len(entities))
	}
	if entities[0].MentionCount != 2 {
		t.Fatalf("expected mention_count=2 after two upserts, got %d", entities[0].MentionCount)
	}
}

func TestUpsertEdge(t *testing.T) {
	store := testEntityStore(t)

	// Create two entities
	idA, _ := store.UpsertEntity(&EntityNode{Name: "auth.go", Type: "file", Namespace: testNS})
	idB, _ := store.UpsertEntity(&EntityNode{Name: "middleware.go", Type: "file", Namespace: testNS})

	edge := &EntityEdge{
		SourceID:  idA,
		TargetID:  idB,
		Relation:  "imports",
		Namespace: testNS,
	}

	// First insert
	if err := store.UpsertEdge(edge); err != nil {
		t.Fatalf("UpsertEdge (first): %v", err)
	}

	// Verify edge exists
	edges, err := store.GetEntityEdges(idA)
	if err != nil {
		t.Fatalf("GetEntityEdges: %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(edges))
	}
	if edges[0].Mentions != 1 {
		t.Fatalf("expected mentions=1, got %d", edges[0].Mentions)
	}
	if edges[0].Strength != 1.0 {
		t.Fatalf("expected strength=1.0, got %f", edges[0].Strength)
	}

	// Second insert — same edge, should increment mentions and update strength
	if err := store.UpsertEdge(edge); err != nil {
		t.Fatalf("UpsertEdge (second): %v", err)
	}

	edges, err = store.GetEntityEdges(idA)
	if err != nil {
		t.Fatalf("GetEntityEdges after second upsert: %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge after duplicate upsert, got %d", len(edges))
	}
	if edges[0].Mentions != 2 {
		t.Fatalf("expected mentions=2, got %d", edges[0].Mentions)
	}
	// Running average: (1.0*1 + 1.0) / 2 = 1.0
	if edges[0].Strength != 1.0 {
		t.Fatalf("expected strength=1.0 after second upsert, got %f", edges[0].Strength)
	}
}

func TestLinkMemoryToEntity(t *testing.T) {
	store := testEntityStore(t)

	entityID, _ := store.UpsertEntity(&EntityNode{Name: "auth.go", Type: "file", Namespace: testNS})
	memID := generateID()

	// First link
	if err := store.LinkMemoryToEntity(memID, entityID, testNS); err != nil {
		t.Fatalf("LinkMemoryToEntity: %v", err)
	}

	// Duplicate link — should be idempotent (INSERT OR IGNORE)
	if err := store.LinkMemoryToEntity(memID, entityID, testNS); err != nil {
		t.Fatalf("LinkMemoryToEntity (duplicate): %v", err)
	}

	// Verify only one link exists
	ids, err := store.GetMemoryIDsByEntities([]string{entityID})
	if err != nil {
		t.Fatalf("GetMemoryIDsByEntities: %v", err)
	}
	if len(ids) != 1 {
		t.Fatalf("expected 1 memory ID, got %d", len(ids))
	}
	if ids[0] != memID {
		t.Fatalf("expected memory ID %s, got %s", memID, ids[0])
	}
}

func TestExpandEntities(t *testing.T) {
	store := testEntityStore(t)

	// Create entities A, B, C
	idA, _ := store.UpsertEntity(&EntityNode{Name: "A", Type: "concept", Namespace: testNS})
	idB, _ := store.UpsertEntity(&EntityNode{Name: "B", Type: "concept", Namespace: testNS})
	idC, _ := store.UpsertEntity(&EntityNode{Name: "C", Type: "concept", Namespace: testNS})

	// Edges: A -> B, B -> C
	store.UpsertEdge(&EntityEdge{SourceID: idA, TargetID: idB, Relation: "relates", Namespace: testNS})
	store.UpsertEdge(&EntityEdge{SourceID: idB, TargetID: idC, Relation: "relates", Namespace: testNS})

	// Expand from A — should find B (not C, since A is not directly connected to C)
	neighborsA, err := store.ExpandEntities(testNS, []string{idA}, 10)
	if err != nil {
		t.Fatalf("ExpandEntities from A: %v", err)
	}
	if !containsID(neighborsA, idB) {
		t.Fatalf("expanding from A should find B, got %v", neighborsA)
	}
	if containsID(neighborsA, idA) {
		t.Fatal("expanding from A should not include A itself")
	}

	// Expand from B — should find A and C
	neighborsB, err := store.ExpandEntities(testNS, []string{idB}, 10)
	if err != nil {
		t.Fatalf("ExpandEntities from B: %v", err)
	}
	if !containsID(neighborsB, idA) || !containsID(neighborsB, idC) {
		t.Fatalf("expanding from B should find A and C, got %v", neighborsB)
	}

	// Expand from [A, C] — should find B
	neighborsAC, err := store.ExpandEntities(testNS, []string{idA, idC}, 10)
	if err != nil {
		t.Fatalf("ExpandEntities from [A,C]: %v", err)
	}
	if !containsID(neighborsAC, idB) {
		t.Fatalf("expanding from [A,C] should find B, got %v", neighborsAC)
	}
	if containsID(neighborsAC, idA) || containsID(neighborsAC, idC) {
		t.Fatalf("expanding from [A,C] should not include A or C, got %v", neighborsAC)
	}

	// Empty input
	empty, err := store.ExpandEntities(testNS, nil, 10)
	if err != nil {
		t.Fatalf("ExpandEntities with nil: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("expected empty result for nil input, got %v", empty)
	}
}

func TestGetMemoryIDsByEntities(t *testing.T) {
	store := testEntityStore(t)

	idA, _ := store.UpsertEntity(&EntityNode{Name: "A", Type: "concept", Namespace: testNS})
	idB, _ := store.UpsertEntity(&EntityNode{Name: "B", Type: "concept", Namespace: testNS})

	mem1 := "mem-001"
	mem2 := "mem-002"
	mem3 := "mem-003"

	// Link memories to entities
	store.LinkMemoryToEntity(mem1, idA, testNS)
	store.LinkMemoryToEntity(mem2, idA, testNS)
	store.LinkMemoryToEntity(mem3, idB, testNS)

	// Query by entity A — should get mem1 and mem2
	idsA, err := store.GetMemoryIDsByEntities([]string{idA})
	if err != nil {
		t.Fatalf("GetMemoryIDsByEntities(A): %v", err)
	}
	if len(idsA) != 2 {
		t.Fatalf("expected 2 memory IDs for entity A, got %d", len(idsA))
	}
	if !containsID(idsA, mem1) || !containsID(idsA, mem2) {
		t.Fatalf("expected mem1 and mem2, got %v", idsA)
	}

	// Query by both entities — should get all 3
	idsAll, err := store.GetMemoryIDsByEntities([]string{idA, idB})
	if err != nil {
		t.Fatalf("GetMemoryIDsByEntities(A,B): %v", err)
	}
	if len(idsAll) != 3 {
		t.Fatalf("expected 3 memory IDs for entities A+B, got %d", len(idsAll))
	}

	// Empty input
	idsNone, err := store.GetMemoryIDsByEntities(nil)
	if err != nil {
		t.Fatalf("GetMemoryIDsByEntities(nil): %v", err)
	}
	if len(idsNone) != 0 {
		t.Fatalf("expected 0 memory IDs for nil input, got %d", len(idsNone))
	}
}

func TestGetEntitiesByName(t *testing.T) {
	store := testEntityStore(t)

	store.UpsertEntity(&EntityNode{Name: "auth_handler.go", Type: "file", Namespace: testNS})
	store.UpsertEntity(&EntityNode{Name: "auth_middleware.go", Type: "file", Namespace: testNS})
	store.UpsertEntity(&EntityNode{Name: "database.go", Type: "file", Namespace: testNS})

	// Search for "auth" — should match 2
	results, err := store.GetEntitiesByName(testNS, "auth", 10)
	if err != nil {
		t.Fatalf("GetEntitiesByName: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 entities matching 'auth', got %d", len(results))
	}

	// Search for "database" — should match 1
	results, err = store.GetEntitiesByName(testNS, "database", 10)
	if err != nil {
		t.Fatalf("GetEntitiesByName: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 entity matching 'database', got %d", len(results))
	}

	// Search for something nonexistent
	results, err = store.GetEntitiesByName(testNS, "nonexistent", 10)
	if err != nil {
		t.Fatalf("GetEntitiesByName: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected 0 entities matching 'nonexistent', got %d", len(results))
	}

	// Search with limit
	results, err = store.GetEntitiesByName(testNS, "auth", 1)
	if err != nil {
		t.Fatalf("GetEntitiesByName with limit: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 entity with limit=1, got %d", len(results))
	}
}

func TestGetEntityStats(t *testing.T) {
	store := testEntityStore(t)

	// Empty namespace
	nodes, edges, links, err := store.GetEntityStats(testNS)
	if err != nil {
		t.Fatalf("GetEntityStats (empty): %v", err)
	}
	if nodes != 0 || edges != 0 || links != 0 {
		t.Fatalf("expected all zeros for empty ns, got nodes=%d edges=%d links=%d", nodes, edges, links)
	}

	// Add some data
	idA, _ := store.UpsertEntity(&EntityNode{Name: "A", Type: "concept", Namespace: testNS})
	idB, _ := store.UpsertEntity(&EntityNode{Name: "B", Type: "concept", Namespace: testNS})
	store.UpsertEdge(&EntityEdge{SourceID: idA, TargetID: idB, Relation: "relates", Namespace: testNS})
	store.LinkMemoryToEntity(generateID(), idA, testNS)
	store.LinkMemoryToEntity(generateID(), idB, testNS)

	nodes, edges, links, err = store.GetEntityStats(testNS)
	if err != nil {
		t.Fatalf("GetEntityStats: %v", err)
	}
	if nodes != 2 {
		t.Fatalf("expected 2 nodes, got %d", nodes)
	}
	if edges != 1 {
		t.Fatalf("expected 1 edge, got %d", edges)
	}
	if links != 2 {
		t.Fatalf("expected 2 links, got %d", links)
	}
}

func TestGetTopEntities(t *testing.T) {
	store := testEntityStore(t)

	// Create entities with different mention counts
	store.UpsertEntity(&EntityNode{Name: "rare", Type: "concept", Namespace: testNS})
	// "common" gets 3 upserts => mention_count = 3
	store.UpsertEntity(&EntityNode{Name: "common", Type: "concept", Namespace: testNS})
	store.UpsertEntity(&EntityNode{Name: "common", Type: "concept", Namespace: testNS})
	store.UpsertEntity(&EntityNode{Name: "common", Type: "concept", Namespace: testNS})
	// "medium" gets 2 upserts => mention_count = 2
	store.UpsertEntity(&EntityNode{Name: "medium", Type: "concept", Namespace: testNS})
	store.UpsertEntity(&EntityNode{Name: "medium", Type: "concept", Namespace: testNS})

	top, err := store.GetTopEntities(testNS, 10)
	if err != nil {
		t.Fatalf("GetTopEntities: %v", err)
	}
	if len(top) != 3 {
		t.Fatalf("expected 3 entities, got %d", len(top))
	}
	// Should be ordered by mention_count DESC
	if top[0].Name != "common" {
		t.Fatalf("expected 'common' first (highest mentions), got %q", top[0].Name)
	}
	if top[0].MentionCount != 3 {
		t.Fatalf("expected mention_count=3 for 'common', got %d", top[0].MentionCount)
	}
	if top[1].Name != "medium" {
		t.Fatalf("expected 'medium' second, got %q", top[1].Name)
	}
	if top[2].Name != "rare" {
		t.Fatalf("expected 'rare' third, got %q", top[2].Name)
	}

	// Test with limit
	topLimited, err := store.GetTopEntities(testNS, 1)
	if err != nil {
		t.Fatalf("GetTopEntities with limit: %v", err)
	}
	if len(topLimited) != 1 {
		t.Fatalf("expected 1 entity with limit=1, got %d", len(topLimited))
	}
}

func TestGetEntityEdges(t *testing.T) {
	store := testEntityStore(t)

	idA, _ := store.UpsertEntity(&EntityNode{Name: "A", Type: "concept", Namespace: testNS})
	idB, _ := store.UpsertEntity(&EntityNode{Name: "B", Type: "concept", Namespace: testNS})
	idC, _ := store.UpsertEntity(&EntityNode{Name: "C", Type: "concept", Namespace: testNS})

	// A -> B (imports), A -> C (uses)
	store.UpsertEdge(&EntityEdge{SourceID: idA, TargetID: idB, Relation: "imports", Namespace: testNS})
	store.UpsertEdge(&EntityEdge{SourceID: idA, TargetID: idC, Relation: "uses", Namespace: testNS})

	// Query edges for A (as source) — should return 2
	edgesA, err := store.GetEntityEdges(idA)
	if err != nil {
		t.Fatalf("GetEntityEdges(A): %v", err)
	}
	if len(edgesA) != 2 {
		t.Fatalf("expected 2 edges for A, got %d", len(edgesA))
	}

	// Query edges for B (as target) — should return 1 (A->B)
	edgesB, err := store.GetEntityEdges(idB)
	if err != nil {
		t.Fatalf("GetEntityEdges(B): %v", err)
	}
	if len(edgesB) != 1 {
		t.Fatalf("expected 1 edge for B, got %d", len(edgesB))
	}
	if edgesB[0].SourceID != idA || edgesB[0].TargetID != idB {
		t.Fatalf("expected edge A->B, got %s->%s", edgesB[0].SourceID, edgesB[0].TargetID)
	}
	if edgesB[0].Relation != "imports" {
		t.Fatalf("expected relation 'imports', got %q", edgesB[0].Relation)
	}

	// Query edges for C (as target) — should return 1 (A->C)
	edgesC, err := store.GetEntityEdges(idC)
	if err != nil {
		t.Fatalf("GetEntityEdges(C): %v", err)
	}
	if len(edgesC) != 1 {
		t.Fatalf("expected 1 edge for C, got %d", len(edgesC))
	}

	// Entity with no edges
	idD, _ := store.UpsertEntity(&EntityNode{Name: "D", Type: "concept", Namespace: testNS})
	edgesD, err := store.GetEntityEdges(idD)
	if err != nil {
		t.Fatalf("GetEntityEdges(D): %v", err)
	}
	if len(edgesD) != 0 {
		t.Fatalf("expected 0 edges for isolated D, got %d", len(edgesD))
	}
}

// containsID checks if a string slice contains a given ID.
func containsID(ids []string, target string) bool {
	for _, id := range ids {
		if id == target {
			return true
		}
	}
	return false
}
