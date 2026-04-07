package dualmem

import (
	"math"
	"testing"
)

func TestBuildFileGraph(t *testing.T) {
	tags := []Tag{
		// Definitions in dualmem/store.go
		{File: "dualmem/store.go", Name: "Store", Kind: "def", SubKind: "type"},
		{File: "dualmem/store.go", Name: "NewStore", Kind: "def", SubKind: "function"},
		{File: "dualmem/store.go", Name: "Save", Kind: "def", SubKind: "method"},
		// Definitions in dualmem/engine.go
		{File: "dualmem/engine.go", Name: "Engine", Kind: "def", SubKind: "type"},
		{File: "dualmem/engine.go", Name: "NewEngine", Kind: "def", SubKind: "function"},
		// Definitions in dualmem/embed.go
		{File: "dualmem/embed.go", Name: "Embed", Kind: "def", SubKind: "function"},
		// References from engine.go to store.go and embed.go
		{File: "dualmem/engine.go", Name: "NewStore", Kind: "ref", Line: 10},
		{File: "dualmem/engine.go", Name: "Save", Kind: "ref", Line: 25},
		{File: "dualmem/engine.go", Name: "Embed", Kind: "ref", Line: 30},
		// References from store.go to embed.go
		{File: "dualmem/store.go", Name: "Embed", Kind: "ref", Line: 15},
	}

	g := BuildFileGraph(tags)

	// Check nodes
	if len(g.Nodes) != 3 {
		t.Fatalf("expected 3 nodes, got %d", len(g.Nodes))
	}
	for _, f := range []string{"dualmem/store.go", "dualmem/engine.go", "dualmem/embed.go"} {
		if !g.Nodes[f] {
			t.Errorf("missing node %q", f)
		}
	}

	// Check edges: engine.go → store.go (2 refs: NewStore, Save)
	if g.Edges["dualmem/engine.go"]["dualmem/store.go"] != 2 {
		t.Errorf("expected engine→store edge weight 2, got %d", g.Edges["dualmem/engine.go"]["dualmem/store.go"])
	}

	// Check edges: engine.go → embed.go (1 ref: Embed)
	if g.Edges["dualmem/engine.go"]["dualmem/embed.go"] != 1 {
		t.Errorf("expected engine→embed edge weight 1, got %d", g.Edges["dualmem/engine.go"]["dualmem/embed.go"])
	}

	// Check edges: store.go → embed.go (1 ref: Embed)
	if g.Edges["dualmem/store.go"]["dualmem/embed.go"] != 1 {
		t.Errorf("expected store→embed edge weight 1, got %d", g.Edges["dualmem/store.go"]["dualmem/embed.go"])
	}

	// No self-edges
	if g.Edges["dualmem/store.go"] != nil && g.Edges["dualmem/store.go"]["dualmem/store.go"] != 0 {
		t.Error("self-edge should not exist for store.go")
	}
	if g.Edges["dualmem/embed.go"] != nil {
		t.Error("embed.go should have no outgoing edges")
	}
}

func TestBuildFileGraph_SelfReferences(t *testing.T) {
	tags := []Tag{
		{File: "a.go", Name: "Foo", Kind: "def"},
		{File: "a.go", Name: "Bar", Kind: "def"},
		{File: "a.go", Name: "Bar", Kind: "ref"}, // self-reference, should be ignored
		{File: "b.go", Name: "Foo", Kind: "ref"}, // cross-file reference
	}

	g := BuildFileGraph(tags)

	// a.go should have no outgoing edges (self-ref skipped)
	if len(g.Edges["a.go"]) != 0 {
		t.Errorf("a.go should have no outgoing edges, got %d", len(g.Edges["a.go"]))
	}

	// b.go → a.go should exist
	if g.Edges["b.go"]["a.go"] != 1 {
		t.Errorf("expected b→a edge weight 1, got %d", g.Edges["b.go"]["a.go"])
	}
}

func TestBuildFileGraph_UnknownReference(t *testing.T) {
	tags := []Tag{
		{File: "a.go", Name: "Foo", Kind: "def"},
		{File: "b.go", Name: "Missing", Kind: "ref"}, // no def for this
	}

	g := BuildFileGraph(tags)

	// No edges should be created for unknown references
	if len(g.Edges["b.go"]) != 0 {
		t.Errorf("b.go should have no edges, got %d", len(g.Edges["b.go"]))
	}
}

func TestBuildFileGraph_Empty(t *testing.T) {
	g := BuildFileGraph(nil)
	if len(g.Nodes) != 0 {
		t.Errorf("expected 0 nodes, got %d", len(g.Nodes))
	}
	if len(g.Edges) != 0 {
		t.Errorf("expected 0 edges, got %d", len(g.Edges))
	}
}

func TestPageRank_BasicRanking(t *testing.T) {
	// Build a simple graph:
	//   a.go → b.go (1 ref)
	//   a.go → c.go (1 ref)
	//   b.go → c.go (2 refs)
	// c.go is most referenced, should rank highest
	tags := []Tag{
		{File: "a.go", Name: "X", Kind: "def"},
		{File: "b.go", Name: "Y", Kind: "def"},
		{File: "c.go", Name: "Z", Kind: "def"},
		{File: "a.go", Name: "Y", Kind: "ref"},
		{File: "a.go", Name: "Z", Kind: "ref"},
		{File: "b.go", Name: "Z", Kind: "ref"},
		{File: "b.go", Name: "Z", Kind: "ref"}, // duplicate ref → stronger edge
	}

	g := BuildFileGraph(tags)
	scores := g.PageRank(nil, 0.85, 30)

	// c.go should have the highest score (most referenced)
	if scores["c.go"] <= scores["a.go"] {
		t.Errorf("c.go (%.6f) should rank higher than a.go (%.6f)", scores["c.go"], scores["a.go"])
	}
	if scores["c.go"] <= scores["b.go"] {
		t.Errorf("c.go (%.6f) should rank higher than b.go (%.6f)", scores["c.go"], scores["b.go"])
	}

	// b.go should rank higher than a.go (b is referenced by a, a is a dangling node)
	if scores["b.go"] <= scores["a.go"] {
		t.Errorf("b.go (%.6f) should rank higher than a.go (%.6f)", scores["b.go"], scores["a.go"])
	}

	// Scores should sum to ~1
	sum := 0.0
	for _, s := range scores {
		sum += s
	}
	if math.Abs(sum-1.0) > 0.01 {
		t.Errorf("scores should sum to 1.0, got %.6f", sum)
	}
}

func TestPageRank_Personalized(t *testing.T) {
	// Graph: a→b, a→c, b→c
	tags := []Tag{
		{File: "a.go", Name: "X", Kind: "def"},
		{File: "b.go", Name: "Y", Kind: "def"},
		{File: "c.go", Name: "Z", Kind: "def"},
		{File: "a.go", Name: "Y", Kind: "ref"},
		{File: "a.go", Name: "Z", Kind: "ref"},
		{File: "b.go", Name: "Z", Kind: "ref"},
	}

	g := BuildFileGraph(tags)

	// Personalize toward a.go
	personalized := g.PageRank(map[string]float64{"a.go": 1.0}, 0.85, 30)
	uniform := g.PageRank(nil, 0.85, 30)

	// With personalization on a.go, a.go should score higher than uniform
	if personalized["a.go"] <= uniform["a.go"] {
		t.Errorf("personalized a.go (%.6f) should be > uniform a.go (%.6f)",
			personalized["a.go"], uniform["a.go"])
	}
}

func TestPageRank_DanglingNodes(t *testing.T) {
	// Graph with a dangling node (no outgoing edges)
	tags := []Tag{
		{File: "hub.go", Name: "Hub", Kind: "def"},
		{File: "leaf.go", Name: "Leaf", Kind: "def"},
		{File: "dead.go", Name: "Dead", Kind: "def"},
		{File: "leaf.go", Name: "Hub", Kind: "ref"},
		{File: "dead.go", Name: "Hub", Kind: "ref"},
	}

	g := BuildFileGraph(tags)
	scores := g.PageRank(nil, 0.85, 50)

	// hub.go is referenced by both leaf and dead, should rank highest
	if scores["hub.go"] <= scores["leaf.go"] || scores["hub.go"] <= scores["dead.go"] {
		t.Errorf("hub.go (%.6f) should rank highest; leaf.go=%.6f dead.go=%.6f",
			scores["hub.go"], scores["leaf.go"], scores["dead.go"])
	}

	// All scores should be positive
	for _, s := range scores {
		if s <= 0 {
			t.Errorf("all scores should be positive, got %.6f", s)
		}
	}
}

func TestPageRank_EmptyGraph(t *testing.T) {
	g := &FileGraph{Nodes: make(map[string]bool), Edges: make(map[string]map[string]int)}
	scores := g.PageRank(nil, 0.85, 30)
	if len(scores) != 0 {
		t.Errorf("empty graph should return empty scores, got %d entries", len(scores))
	}
}

func TestPageRank_SingleNode(t *testing.T) {
	g := &FileGraph{
		Nodes: map[string]bool{"solo.go": true},
		Edges: make(map[string]map[string]int),
	}
	scores := g.PageRank(nil, 0.85, 30)
	if len(scores) != 1 {
		t.Fatalf("expected 1 score, got %d", len(scores))
	}
	if math.Abs(scores["solo.go"]-1.0) > 0.01 {
		t.Errorf("single node should have score 1.0, got %.6f", scores["solo.go"])
	}
}

func TestPageRank_ScoresSumToOne(t *testing.T) {
	tags := []Tag{
		{File: "a.go", Name: "X", Kind: "def"},
		{File: "b.go", Name: "Y", Kind: "def"},
		{File: "c.go", Name: "Z", Kind: "def"},
		{File: "d.go", Name: "W", Kind: "def"},
		{File: "a.go", Name: "Y", Kind: "ref"},
		{File: "a.go", Name: "Z", Kind: "ref"},
		{File: "b.go", Name: "Z", Kind: "ref"},
		{File: "b.go", Name: "W", Kind: "ref"},
		{File: "c.go", Name: "W", Kind: "ref"},
		{File: "d.go", Name: "X", Kind: "ref"},
	}

	g := BuildFileGraph(tags)
	scores := g.PageRank(nil, 0.85, 50)

	sum := 0.0
	for _, s := range scores {
		sum += s
	}
	if math.Abs(sum-1.0) > 0.001 {
		t.Errorf("scores should sum to 1.0, got %.6f", sum)
	}
}

func TestRankFiles(t *testing.T) {
	tags := []Tag{
		{File: "hub.go", Name: "Hub", Kind: "def", SubKind: "type"},
		{File: "hub.go", Name: "NewHub", Kind: "def", SubKind: "function"},
		{File: "user.go", Name: "User", Kind: "def", SubKind: "type"},
		{File: "user.go", Name: "GetUser", Kind: "def", SubKind: "method"},
		{File: "main.go", Name: "main", Kind: "def", SubKind: "function"},
		// main references everything
		{File: "main.go", Name: "NewHub", Kind: "ref"},
		{File: "main.go", Name: "GetUser", Kind: "ref"},
		// user references hub
		{File: "user.go", Name: "Hub", Kind: "ref"},
	}

	g := BuildFileGraph(tags)
	ranked := g.RankFiles(tags, nil)

	// Should return 3 files
	if len(ranked) != 3 {
		t.Fatalf("expected 3 ranked files, got %d", len(ranked))
	}

	// Scores should be in descending order
	for i := 1; i < len(ranked); i++ {
		if ranked[i].Score > ranked[i-1].Score {
			t.Errorf("files not sorted: [%d] %.6f > [%d] %.6f",
				i, ranked[i].Score, i-1, ranked[i-1].Score)
		}
	}

	// All scores positive
	for _, rf := range ranked {
		if rf.Score <= 0 {
			t.Errorf("score for %s should be positive, got %.6f", rf.Path, rf.Score)
		}
	}

	// hub.go should rank first (most referenced)
	if ranked[0].Path != "hub.go" {
		t.Errorf("expected hub.go first, got %s", ranked[0].Path)
	}

	// hub.go should have its definitions attached
	hub := ranked[0]
	if len(hub.Definitions) != 2 {
		t.Fatalf("hub.go should have 2 definitions, got %d", len(hub.Definitions))
	}
	names := make(map[string]bool)
	for _, d := range hub.Definitions {
		names[d.Name] = true
	}
	if !names["Hub"] || !names["NewHub"] {
		t.Errorf("hub.go definitions missing Hub or NewHub: %v", hub.Definitions)
	}

	// main.go should have its definition
	var mainFile *FileRank
	for i := range ranked {
		if ranked[i].Path == "main.go" {
			mainFile = &ranked[i]
			break
		}
	}
	if mainFile == nil {
		t.Fatal("main.go not found in ranked files")
	}
	if len(mainFile.Definitions) != 1 || mainFile.Definitions[0].Name != "main" {
		t.Errorf("main.go should have 1 definition 'main', got %v", mainFile.Definitions)
	}
}

func TestRankFiles_WithPersonalization(t *testing.T) {
	tags := []Tag{
		{File: "a.go", Name: "A", Kind: "def"},
		{File: "b.go", Name: "B", Kind: "def"},
		{File: "c.go", Name: "C", Kind: "def"},
		{File: "a.go", Name: "B", Kind: "ref"},
		{File: "a.go", Name: "C", Kind: "ref"},
		{File: "b.go", Name: "C", Kind: "ref"},
	}

	g := BuildFileGraph(tags)

	// Personalize to c.go — it's already the most referenced, should still rank first
	ranked := g.RankFiles(tags, map[string]float64{"c.go": 1.0})

	if len(ranked) == 0 {
		t.Fatal("expected non-empty results")
	}
	if ranked[0].Path != "c.go" {
		t.Errorf("personalized c.go should rank first, got %s", ranked[0].Path)
	}
}

func TestRankFiles_DefinitionsAttached(t *testing.T) {
	tags := []Tag{
		{File: "types.go", Name: "Config", Kind: "def", SubKind: "type"},
		{File: "types.go", Name: "Options", Kind: "def", SubKind: "type"},
		{File: "types.go", Name: "Defaults", Kind: "def", SubKind: "function"},
		{File: "app.go", Name: "App", Kind: "def", SubKind: "type"},
		{File: "app.go", Name: "Config", Kind: "ref"},
		{File: "app.go", Name: "Defaults", Kind: "ref"},
	}

	g := BuildFileGraph(tags)
	ranked := g.RankFiles(tags, nil)

	// Find types.go
	var typesFile *FileRank
	for i := range ranked {
		if ranked[i].Path == "types.go" {
			typesFile = &ranked[i]
			break
		}
	}
	if typesFile == nil {
		t.Fatal("types.go not found in ranked files")
	}

	if len(typesFile.Definitions) != 3 {
		t.Errorf("types.go should have 3 definitions, got %d", len(typesFile.Definitions))
	}
}
