package dualmem

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
)

// mockEmbedder returns deterministic embeddings for testing.
type mockEmbedder struct {
	dim int
}

func (m *mockEmbedder) Embed(_ context.Context, text string, _ string) ([]float32, error) {
	vec := make([]float32, m.dim)
	// Simple hash-based embedding: distribute text bytes across dimensions
	for i, b := range []byte(text) {
		vec[i%m.dim] += float32(b) / 256.0
	}
	// Normalize
	var norm float32
	for _, v := range vec {
		norm += v * v
	}
	if norm > 0 {
		for i := range vec {
			vec[i] /= norm
		}
	}
	return vec, nil
}

func (m *mockEmbedder) Dimension() int { return m.dim }

func (m *mockEmbedder) ModelName() string { return "mock-embedder" }

// mockClassifier returns a fixed sector for testing.
type mockClassifier struct{}

func (m *mockClassifier) Classify(content string) string { return "decision" }

// mockExtractor returns simple entities for testing.
type mockExtractor struct{}

func (m *mockExtractor) Extract(content string) []Entity {
	return []Entity{{Text: "test", Type: "topic"}}
}

func newTestEngine(t *testing.T) *Engine {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	engine, err := New(Config{
		SQLitePath:        dbPath,
		EmbeddingProvider: &mockEmbedder{dim: 768},
		Classifier:        &mockClassifier{},
		EntityExtractor:   &mockExtractor{},
		MaxDetailPerUser:  10,
		ImportanceTheta:   0.65,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { engine.Close() })
	return engine
}

func TestEngineAddAndSearch(t *testing.T) {
	engine := newTestEngine(t)
	ctx := context.Background()

	// Add memories with decision sector hint + high salience so they route to Detail
	for _, msg := range []string{
		"John prefers dark roast coffee",
		"Alice's favorite band is Aphex Twin",
		"Bob works at TechCorp on Mondays",
	} {
		engine.AddWithOptions(ctx, MemoryInput{
			UserMessage: msg,
			SectorHint:  "decision",
			Salience:    0.9,
		}, "user1")
	}

	// Search
	results := engine.Search("coffee preferences", "user1", 5, nil)
	if len(results) == 0 {
		t.Fatal("Search returned no results")
	}
	t.Logf("Search returned %d results", len(results))
	for _, r := range results {
		t.Logf("  [%.3f] %s", r.Similarity, r.Text[:min(60, len(r.Text))])
	}
}

func TestEngineAddWithOptions(t *testing.T) {
	engine := newTestEngine(t)
	ctx := context.Background()

	// Add with high salience + decision hint → should route to Detail
	err := engine.AddWithOptions(ctx, MemoryInput{
		UserMessage:      "John Smith lives at 42 Oak Street",
		AssistantMessage: "Noted!",
		SectorHint:       "decision",
		Salience:         0.9,
		SessionID:        "sess-1",
	}, "user1")
	if err != nil {
		t.Fatalf("AddWithOptions: %v", err)
	}

	// Verify it ended up in detail
	details, err := engine.store.GetDetailMemories("user1")
	if err != nil {
		t.Fatalf("GetDetailMemories: %v", err)
	}
	if len(details) == 0 {
		t.Error("expected memory in detail path, got none")
	}
}

func TestEngineDualSearch(t *testing.T) {
	engine := newTestEngine(t)
	ctx := context.Background()

	// Add memories
	engine.Add("I prefer Earl Grey tea", "Classic!", "user1")
	engine.Add("We went hiking last weekend", "How was it?", "user1")

	result, err := engine.DualSearch(ctx, "user1", "tea preferences", SearchOpts{
		Limit:         5,
		IncludeSketch: true,
	})
	if err != nil {
		t.Fatalf("DualSearch: %v", err)
	}
	if result == nil {
		t.Fatal("DualSearch returned nil")
	}
	t.Logf("DualSearch: %d detail, %d episodes, %d arcs, profile=%v",
		len(result.DetailMemories), len(result.Episodes), len(result.Arcs), result.Profile != nil)
}

func TestEngineAssembleContext(t *testing.T) {
	engine := newTestEngine(t)
	ctx := context.Background()

	// Add memories with decision sector so they route to Detail
	for _, msg := range []string{
		"Alice works at TechCorp as an engineer",
		"Bob's favorite programming language is Go",
		"Charlie visited Tokyo last summer on July 15",
		"Diana has a cat named Whiskers",
		"Eve enjoys playing guitar on weekends",
	} {
		engine.AddWithOptions(ctx, MemoryInput{
			UserMessage: msg,
			SectorHint:  "decision",
			Salience:    0.9,
		}, "user1")
	}

	block, err := engine.AssembleContext(ctx, "user1", "tell me about this user", 2000)
	if err != nil {
		t.Fatalf("AssembleContext: %v", err)
	}
	if block == nil {
		t.Fatal("AssembleContext returned nil")
	}
	if block.TokenCount > 2000 {
		t.Errorf("token count %d exceeds budget 2000", block.TokenCount)
	}
	if len(block.Sources) == 0 {
		t.Error("expected sources, got none")
	}
	t.Logf("AssembleContext: %d tokens, %d sources\n%s", block.TokenCount, len(block.Sources), block.Text[:min(200, len(block.Text))])
}

func TestEngineDetailCapacity(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	engine, err := New(Config{
		SQLitePath:        dbPath,
		EmbeddingProvider: &mockEmbedder{dim: 768},
		Classifier:        &mockClassifier{},
		MaxDetailPerUser:  3, // Very small cap for testing
		ImportanceTheta:   0.30, // Low threshold so most things go to detail
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer engine.Close()
	ctx := context.Background()

	// Add more than capacity
	for i := 0; i < 5; i++ {
		err := engine.AddWithOptions(ctx, MemoryInput{
			UserMessage: "Important fact number " + string(rune('A'+i)),
			SectorHint:  "decision",
			Salience:    0.9,
		}, "user1")
		if err != nil {
			t.Fatalf("Add %d: %v", i, err)
		}
	}

	// Detail count should not exceed cap
	count, err := engine.store.GetDetailCount("user1")
	if err != nil {
		t.Fatalf("GetDetailCount: %v", err)
	}
	if count > 3 {
		t.Errorf("detail count = %d, exceeds cap of 3", count)
	}
	t.Logf("Detail count: %d (cap: 3)", count)
}

func TestEngineConcurrency(t *testing.T) {
	engine := newTestEngine(t)
	ctx := context.Background()

	// 10 goroutines adding simultaneously
	var wg sync.WaitGroup
	errs := make(chan error, 10)

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			err := engine.AddWithOptions(ctx, MemoryInput{
				UserMessage: "Concurrent message from goroutine",
				Salience:    0.5,
			}, "user1")
			if err != nil {
				errs <- err
			}
		}(i)
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("concurrent Add error: %v", err)
	}

	// Verify no panics and data is consistent
	count, _ := engine.store.GetDetailCount("user1")
	t.Logf("After concurrent adds: %d detail memories", count)
	if count > 10 {
		t.Errorf("detail count %d exceeds max 10", count)
	}
}

func TestEngineDemoteFromDetail(t *testing.T) {
	engine := newTestEngine(t)
	ctx := context.Background()

	// Add a high-importance memory
	err := engine.AddWithOptions(ctx, MemoryInput{
		UserMessage: "Critical fact: John prefers dark roast",
		SectorHint:  "decision",
		Salience:    0.95,
	}, "user1")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	details, _ := engine.store.GetDetailMemories("user1")
	if len(details) == 0 {
		t.Skip("memory routed to sketch, not detail — test not applicable")
	}

	memID := details[0].ID
	err = engine.DemoteFromDetail(ctx, "user1", memID)
	if err != nil {
		t.Fatalf("DemoteFromDetail: %v", err)
	}

	// Should be gone from detail
	count, _ := engine.store.GetDetailCount("user1")
	if count != 0 {
		t.Errorf("detail count after demote = %d, want 0", count)
	}
}

func TestEngineDropInCompatibility(t *testing.T) {
	engine := newTestEngine(t)

	// These should match Engram's exact signatures
	engine.Add("user message", "assistant message", "user1")
	results := engine.Search("query", "user1", 5, nil)

	// Results type should be []DetailMemory
	_ = results
	t.Log("Drop-in compatibility: Add and Search work with Engram-compatible signatures")
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
