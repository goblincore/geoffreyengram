package dualmem

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
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

func TestDualSearch_UsesPrecomputedEmbedding(t *testing.T) {
	engine := newTestEngine(t)
	ctx := context.Background()

	// Add a test memory using AddWithOptions (Add has a different signature)
	err := engine.AddWithOptions(ctx, MemoryInput{
		UserMessage: "credential review validation logic",
		SectorHint:  "decision",
		Salience:    0.9,
	}, "testuser")
	if err != nil {
		t.Fatal(err)
	}

	// Pre-compute embedding
	emb, err := engine.Embedder().Embed(ctx, "credential review", "RETRIEVAL_QUERY")
	if err != nil {
		t.Fatal(err)
	}

	// Search with pre-computed embedding
	results, err := engine.DualSearch(ctx, "testuser", "credential review", SearchOpts{
		Limit:          5,
		QueryEmbedding: emb,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results.DetailMemories) == 0 {
		t.Fatal("expected at least one detail memory")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func TestPromoteFromSketchRaw(t *testing.T) {
	engine := newTestEngine(t)
	ctx := context.Background()

	// Add a memory with low salience — should route to sketch
	err := engine.AddWithOptions(ctx, MemoryInput{
		UserMessage: "low importance chat about nothing special",
		Salience:    0.1,
	}, "user1")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	// Verify it's in sketch_raw (not detail)
	detailCount, _ := engine.store.GetDetailCount("user1")
	raws, _ := engine.store.GetAllSketchRaw("user1", 100)
	if len(raws) == 0 {
		t.Skip("memory routed to detail instead of sketch — adjust salience")
	}
	rawID := raws[0].ID
	t.Logf("Before promote: %d detail, %d raw", detailCount, len(raws))

	// Promote
	err = engine.PromoteToDetail(ctx, "user1", rawID, nil)
	if err != nil {
		t.Fatalf("PromoteToDetail: %v", err)
	}

	// Verify: in detail, gone from sketch_raw
	details, _ := engine.store.GetDetailMemories("user1")
	found := false
	for _, d := range details {
		if d.Text == "low importance chat about nothing special" {
			found = true
			break
		}
	}
	if !found {
		t.Error("promoted memory not found in detail path")
	}

	raw, _ := engine.store.GetSketchRawByID(rawID)
	if raw != nil {
		t.Error("raw entry should be deleted after promotion")
	}
}

func TestPromoteFromEpisode(t *testing.T) {
	engine := newTestEngine(t)
	ctx := context.Background()

	// Directly insert an episode via store
	vec := make([]float32, 768)
	vec[0] = 0.7
	ep := &Episode{
		ID:          "ep-promote-1",
		SummaryText: "Summary of recent coffee discussions with multiple friends",
		Entities:    []Entity{{Text: "coffee", Type: "topic"}},
	}
	err := engine.store.InsertEpisode(ep, vec, "user1", "mock-embedder", nil)
	if err != nil {
		t.Fatalf("InsertEpisode: %v", err)
	}

	// Promote the episode
	err = engine.PromoteToDetail(ctx, "user1", "ep-promote-1", nil)
	if err != nil {
		t.Fatalf("PromoteToDetail: %v", err)
	}

	// Verify in detail
	details, _ := engine.store.GetDetailMemories("user1")
	found := false
	for _, d := range details {
		if strings.Contains(d.Text, "coffee discussions") {
			found = true
			break
		}
	}
	if !found {
		t.Error("promoted episode not found in detail path")
	}

	// Episode should be deleted
	epCheck, _ := engine.store.GetEpisodeByID("ep-promote-1")
	if epCheck != nil {
		t.Error("episode should be deleted after promotion")
	}
}

func TestPromoteWithTypeOverride(t *testing.T) {
	engine := newTestEngine(t)
	ctx := context.Background()

	// Insert a raw sketch entry
	engine.store.InsertSketchRaw("user1", "never touch the rate limiter cleanup", "decision", "", nil)
	raws, _ := engine.store.GetAllSketchRaw("user1", 10)
	if len(raws) == 0 {
		t.Fatal("expected raw entry")
	}

	// Promote with type override
	err := engine.PromoteToDetail(ctx, "user1", raws[0].ID, &PromoteOpts{
		Type:     "warning",
		Salience: 0.9,
	})
	if err != nil {
		t.Fatalf("PromoteToDetail: %v", err)
	}

	details, _ := engine.store.GetDetailMemories("user1")
	if len(details) == 0 {
		t.Fatal("expected detail memory")
	}
	if details[0].Type != "warning" {
		t.Errorf("type = %q, want 'warning'", details[0].Type)
	}
	if details[0].Salience != 0.9 {
		t.Errorf("salience = %v, want 0.9", details[0].Salience)
	}
}

func TestPromoteNotFound(t *testing.T) {
	engine := newTestEngine(t)
	ctx := context.Background()

	err := engine.PromoteToDetail(ctx, "user1", "nonexistent-id", nil)
	if err == nil {
		t.Fatal("expected error for nonexistent ID")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %v, want 'not found' message", err)
	}
}

func TestPromoteCapacityManagement(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	engine, err := New(Config{
		SQLitePath:        dbPath,
		EmbeddingProvider: &mockEmbedder{dim: 768},
		Classifier:        &mockClassifier{},
		MaxDetailPerUser:  2,
		ImportanceTheta:   0.30,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer engine.Close()
	ctx := context.Background()

	// Fill detail to capacity
	for _, msg := range []string{"first important fact", "second important fact"} {
		engine.AddWithOptions(ctx, MemoryInput{
			UserMessage: msg,
			SectorHint:  "decision",
			Salience:    0.9,
		}, "user1")
	}
	count, _ := engine.store.GetDetailCount("user1")
	if count != 2 {
		t.Fatalf("expected 2 detail memories, got %d", count)
	}

	// Insert a raw entry directly and promote it
	engine.store.InsertSketchRaw("user1", "third critical warning about fragile code", "warning", "", nil)
	raws, _ := engine.store.GetAllSketchRaw("user1", 10)
	if len(raws) == 0 {
		t.Fatal("expected raw entry")
	}

	err = engine.PromoteToDetail(ctx, "user1", raws[0].ID, &PromoteOpts{Type: "warning", Salience: 0.95})
	if err != nil {
		t.Fatalf("PromoteToDetail: %v", err)
	}

	// Detail should still be at capacity (2), with lowest demoted
	count, _ = engine.store.GetDetailCount("user1")
	if count > 2 {
		t.Errorf("detail count = %d, exceeds cap of 2", count)
	}
}

func TestReEvaluateSketchRaw(t *testing.T) {
	engine := newTestEngine(t)
	ctx := context.Background()

	// Insert raw entries directly (simulating old pre-type-boost memories)
	for _, content := range []string{
		"Rejected Postgres for CLI — chose SQLite for zero-setup",
		"Don't touch rate limiter cleanup — intentional nil check skip",
		"Just chatting about the weather today",
	} {
		engine.store.InsertSketchRaw("user1", content, "decision", "", nil)
	}

	raws, _ := engine.store.GetAllSketchRaw("user1", 100)
	if len(raws) != 3 {
		t.Fatalf("expected 3 raws, got %d", len(raws))
	}

	// Re-evaluate with type=warning — the type boost should promote them
	count, err := engine.ReEvaluateSketchRaw(ctx, "user1", &PromoteOpts{Type: "warning"})
	if err != nil {
		t.Fatalf("ReEvaluateSketchRaw: %v", err)
	}

	t.Logf("Promoted %d out of 3 raw entries", count)
	if count == 0 {
		t.Error("expected at least some promotions with type boost")
	}

	// Promoted entries should be in detail now
	details, _ := engine.store.GetDetailMemories("user1")
	if len(details) != count {
		t.Errorf("detail count = %d, want %d", len(details), count)
	}
}

func TestAssembleContext_QueryAwareCodeMap(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a tiny Go project
	writeFile(t, tmpDir, "main.go", "package main\nfunc main() {}\n")
	subDir := filepath.Join(tmpDir, "auth")
	os.MkdirAll(subDir, 0755)
	writeFile(t, subDir, "auth.go", "package auth\ntype Validator struct {}\nfunc Validate() error { return nil }\n")

	// Init git
	exec.Command("git", "-C", tmpDir, "init").Run()
	exec.Command("git", "-C", tmpDir, "add", ".").Run()
	exec.Command("git", "-C", tmpDir, "-c", "user.email=test@test.com", "-c", "user.name=Test", "commit", "-m", "init").Run()

	dbPath := filepath.Join(t.TempDir(), "test.db")
	engine, err := New(Config{
		SQLitePath:        dbPath,
		EmbeddingProvider: &mockEmbedder{dim: 768},
		Classifier:        &mockClassifier{},
		EntityExtractor:   &mockExtractor{},
		RootDir:           tmpDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()

	ctx := context.Background()
	block, err := engine.AssembleContext(ctx, "testuser", "authentication validation", 3000)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(block.Text, "[Codebase Map]") {
		t.Error("expected codemap in context output")
	}
}

// --- GC and Supersession Tests ---

func TestAccessRecencyFactor(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name     string
		accessed time.Time
		memType  string
		wantMin  float64
		wantMax  float64
	}{
		{"just accessed", now, "", 0.99, 1.01},
		{"30 days ago", now.AddDate(0, 0, -30), "", 0.49, 0.51},
		{"60 days ago", now.AddDate(0, 0, -60), "", 0.49, 0.51}, // clamped at 0.5
		{"90 days ago", now.AddDate(0, 0, -90), "", 0.49, 0.51}, // clamped at 0.5
		{"warning always 1.0", now.AddDate(0, 0, -90), "warning", 0.99, 1.01},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := accessRecencyFactor(tt.accessed, tt.memType, now)
			if got < tt.wantMin || got > tt.wantMax {
				t.Errorf("accessRecencyFactor() = %v, want [%v, %v]", got, tt.wantMin, tt.wantMax)
			}
		})
	}
}

func TestContinuitySupersession(t *testing.T) {
	engine := newTestEngine(t)
	ctx := context.Background()

	// Add first continuity entry
	err := engine.AddWithOptions(ctx, MemoryInput{
		UserMessage: "Project status: done with auth, remaining: payments, dashboard",
		Salience:    0.9,
		Type:        "continuity",
	}, "user1")
	if err != nil {
		t.Fatalf("Add first: %v", err)
	}

	count, _ := engine.store.GetDetailCount("user1")
	if count != 1 {
		t.Fatalf("expected 1 detail, got %d", count)
	}

	// Add a second, similar continuity entry (updated status)
	err = engine.AddWithOptions(ctx, MemoryInput{
		UserMessage: "Project status: done with auth and payments, remaining: dashboard",
		Salience:    0.9,
		Type:        "continuity",
	}, "user1")
	if err != nil {
		t.Fatalf("Add second: %v", err)
	}

	// The old entry should have been superseded (demoted)
	details, _ := engine.store.GetDetailMemories("user1")
	continuityCount := 0
	for _, d := range details {
		if d.Type == "continuity" {
			continuityCount++
		}
	}
	// With mock embedder, similar text should produce similar embeddings
	// so the first should be superseded
	t.Logf("Continuity entries in detail: %d (expect 1 if supersession worked)", continuityCount)
	if continuityCount > 1 {
		t.Errorf("expected at most 1 continuity in detail (supersession should demote old), got %d", continuityCount)
	}
}

func TestContinuitySupersession_DifferentTopics(t *testing.T) {
	engine := newTestEngine(t)
	ctx := context.Background()

	// Add two continuity entries about very different topics
	err := engine.AddWithOptions(ctx, MemoryInput{
		UserMessage: "Auth system rewrite in progress, JWT validation done, refresh tokens remaining",
		Salience:    0.9,
		Type:        "continuity",
	}, "user1")
	if err != nil {
		t.Fatal(err)
	}

	err = engine.AddWithOptions(ctx, MemoryInput{
		UserMessage: "Database migration to PostgreSQL started, schema design complete",
		Salience:    0.9,
		Type:        "continuity",
	}, "user1")
	if err != nil {
		t.Fatal(err)
	}

	// Both should survive — different topics
	details, _ := engine.store.GetDetailMemories("user1")
	continuityCount := 0
	for _, d := range details {
		if d.Type == "continuity" {
			continuityCount++
		}
	}
	t.Logf("Continuity entries: %d (expect 2 for different topics)", continuityCount)
	// Different topics should both survive (cosine similarity < 0.75)
}

func TestGarbageCollect_DryRun(t *testing.T) {
	engine := newTestEngine(t)
	ctx := context.Background()

	// Add some memories
	for _, msg := range []string{"fact one", "fact two", "fact three"} {
		engine.AddWithOptions(ctx, MemoryInput{
			UserMessage: msg,
			SectorHint:  "decision",
			Salience:    0.9,
			Type:        "continuity",
		}, "user1")
	}

	beforeCount, _ := engine.store.GetDetailCount("user1")

	report, err := engine.GarbageCollect(ctx, "user1", GCOptions{DryRun: true, Verbose: true})
	if err != nil {
		t.Fatalf("GarbageCollect: %v", err)
	}

	afterCount, _ := engine.store.GetDetailCount("user1")
	if afterCount != beforeCount {
		t.Errorf("dry run changed detail count: before=%d, after=%d", beforeCount, afterCount)
	}

	t.Logf("GC dry-run report: episodes=%d, arcs=%d, stale=%d, superseded=%d, cold=%d",
		report.ExpiredEpisodes, report.ExpiredArcs, report.StaleDetails,
		report.SupersededContinuity, report.AccessColdDetails)
}

func TestGarbageCollect_ExpiredEpisodes(t *testing.T) {
	engine := newTestEngine(t)
	ctx := context.Background()

	// Insert an episode that's already expired
	vec := make([]float32, 768)
	vec[0] = 0.5
	ep := &Episode{
		ID:          "ep-expired-1",
		SummaryText: "Old conversation about weather from long ago",
	}
	engine.store.InsertEpisode(ep, vec, "user1", "mock-embedder", nil)

	// Manually set expires_at to past
	engine.store.(*SQLiteStore).db.Exec(
		`UPDATE episodes SET expires_at = '2020-01-01 00:00:00' WHERE id = 'ep-expired-1'`)

	report, err := engine.GarbageCollect(ctx, "user1", GCOptions{Verbose: true})
	if err != nil {
		t.Fatalf("GarbageCollect: %v", err)
	}

	if report.ExpiredEpisodes != 1 {
		t.Errorf("expected 1 expired episode, got %d", report.ExpiredEpisodes)
	}

	// Verify deleted
	ep2, _ := engine.store.GetEpisodeByID("ep-expired-1")
	if ep2 != nil {
		t.Error("expired episode should be deleted")
	}
}

func TestGarbageCollect_ExpiredArcs(t *testing.T) {
	engine := newTestEngine(t)
	ctx := context.Background()

	// Insert an arc directly
	vec := make([]float32, 128) // arcs use sketched embeddings
	vec[0] = 0.5
	arc := &Arc{
		ID:          "arc-expired-1",
		SummaryText: "Theme about old project work",
		EpisodeIDs:  []string{"ep1", "ep2"},
	}
	engine.store.InsertArc(arc, vec, "user1", "mock-embedder", 12345)

	// Manually expire it
	engine.store.(*SQLiteStore).db.Exec(
		`UPDATE arcs SET expires_at = '2020-01-01 00:00:00' WHERE id = 'arc-expired-1'`)

	report, err := engine.GarbageCollect(ctx, "user1", GCOptions{})
	if err != nil {
		t.Fatal(err)
	}

	if report.ExpiredArcs != 1 {
		t.Errorf("expected 1 expired arc, got %d", report.ExpiredArcs)
	}
}

func TestGetDetailsByType(t *testing.T) {
	engine := newTestEngine(t)
	ctx := context.Background()

	// Add different types
	for _, tc := range []struct {
		text, typ string
	}{
		{"warning about fragile code", "warning"},
		{"decision to use SQLite", "decision"},
		{"status update on auth work", "continuity"},
		{"another status update", "continuity"},
	} {
		engine.AddWithOptions(ctx, MemoryInput{
			UserMessage: tc.text,
			Salience:    0.9,
			Type:        tc.typ,
		}, "user1")
	}

	// Query only continuity
	continuity, err := engine.store.GetDetailsByType("user1", "continuity")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range continuity {
		if d.Type != "continuity" {
			t.Errorf("expected type=continuity, got %q", d.Type)
		}
	}
	t.Logf("Found %d continuity entries", len(continuity))

	// Query warnings
	warnings, err := engine.store.GetDetailsByType("user1", "warning")
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 1 {
		t.Errorf("expected 1 warning, got %d", len(warnings))
	}
}

func TestDetectIntent(t *testing.T) {
	tests := []struct {
		query  string
		expect Intent
	}{
		// Debug
		{"fix the bug in codemap", IntentDebug},
		{"error in AssembleContext", IntentDebug},
		{"why is the test failing", IntentDebug},
		{"crash on nil pointer", IntentDebug},
		{"debug the search ranking", IntentDebug},

		// Continue
		{"continue yesterday's work", IntentContinue},
		{"resume the auth refactor", IntentContinue},
		{"pick up where we left off", IntentContinue},
		{"session context", IntentContinue},
		{"what next", IntentContinue},
		{"what's next for this project", IntentContinue},
		{"next step", IntentContinue},
		{"what should I work on", IntentContinue},
		{"todo", IntentContinue},
		{"status", IntentContinue},
		{"progress", IntentContinue},

		// Explore
		{"where is the embedding logic", IntentExplore},
		{"how does the pipeline work", IntentExplore},
		{"explain the sketch path", IntentExplore},
		{"architecture overview", IntentExplore},

		// Feature
		{"add a new CLI command", IntentFeature},
		{"implement intent detection", IntentFeature},
		{"create a checkpoint system", IntentFeature},
		{"build a migration tool", IntentFeature},

		// Default (no strong signal)
		{"coffee preferences", IntentDefault},
		{"memory system", IntentDefault},
		{"dualmem", IntentDefault},
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			got := DetectIntent(tt.query)
			if got != tt.expect {
				t.Errorf("DetectIntent(%q) = %q, want %q", tt.query, got, tt.expect)
			}
		})
	}
}

func TestIntentProfileTypeMultiplier(t *testing.T) {
	debug := IntentProfiles[IntentDebug]

	if m := debug.TypeMultiplier("warning"); m != 2.0 {
		t.Errorf("debug.warning = %.1f, want 2.0", m)
	}
	if m := debug.TypeMultiplier("continuity"); m != 0.5 {
		t.Errorf("debug.continuity = %.1f, want 0.5", m)
	}

	cont := IntentProfiles[IntentContinue]
	if m := cont.TypeMultiplier("continuity"); m != 2.0 {
		t.Errorf("continue.continuity = %.1f, want 2.0", m)
	}

	explore := IntentProfiles[IntentExplore]
	if m := explore.TypeMultiplier("map"); m != 2.0 {
		t.Errorf("explore.map = %.1f, want 2.0", m)
	}
}

func TestDetailSortScoreWithIntent(t *testing.T) {
	now := time.Now()
	recent := now.Add(-1 * time.Hour)

	warning := DetailMemory{Type: "warning", LastAccessedAt: recent}
	decision := DetailMemory{Type: "decision", LastAccessedAt: recent}
	continuity := DetailMemory{Type: "continuity", LastAccessedAt: recent}
	general := DetailMemory{Type: "", LastAccessedAt: recent}

	// Without intent: warnings > decisions/continuity > general
	wScore := detailSortScore(warning, nil, now, "")
	dScore := detailSortScore(decision, nil, now, "")
	cScore := detailSortScore(continuity, nil, now, "")
	gScore := detailSortScore(general, nil, now, "")

	if wScore <= dScore {
		t.Errorf("without intent: warning (%.2f) should > decision (%.2f)", wScore, dScore)
	}
	if dScore <= gScore {
		t.Errorf("without intent: decision (%.2f) should > general (%.2f)", dScore, gScore)
	}

	// With debug intent: warnings boosted even more, continuity suppressed
	debugProfile := IntentProfiles[IntentDebug]
	wDebug := detailSortScore(warning, &debugProfile, now, "")
	cDebug := detailSortScore(continuity, &debugProfile, now, "")
	gDebug := detailSortScore(general, &debugProfile, now, "")

	if wDebug <= wScore {
		t.Errorf("debug intent should boost warnings: %.2f vs %.2f", wDebug, wScore)
	}
	if cDebug >= cScore {
		t.Errorf("debug intent should suppress continuity: %.2f vs %.2f", cDebug, cScore)
	}

	// With continue intent: continuity boosted
	contProfile := IntentProfiles[IntentContinue]
	cCont := detailSortScore(continuity, &contProfile, now, "")
	if cCont <= cScore {
		t.Errorf("continue intent should boost continuity: %.2f vs %.2f", cCont, cScore)
	}

	_ = gScore
	_ = gDebug
}

func TestAssembleContextWithIntent(t *testing.T) {
	engine := newTestEngine(t)
	ctx := context.Background()

	// Add memories of different types
	memories := []MemoryInput{
		{UserMessage: "Don't modify the rate limiter cleanup code", SectorHint: "warning", Salience: 0.9, Type: "warning"},
		{UserMessage: "Chose SQLite over Postgres for zero-setup", SectorHint: "decision", Salience: 0.8, Type: "decision"},
		{UserMessage: "In progress: auth refactor, done JWT, remaining refresh", SectorHint: "continuity", Salience: 0.7, Type: "continuity"},
		{UserMessage: "Auth logic in Nakama RPCs, not middleware", SectorHint: "map", Salience: 0.7, Type: ""},
	}
	for _, m := range memories {
		if err := engine.AddWithOptions(ctx, m, "user1"); err != nil {
			t.Fatalf("AddWithOptions: %v", err)
		}
	}

	// Assemble with debug intent
	debugBlock, err := engine.AssembleContextWith(ctx, "user1", "fix the bug", 5000, &ContextOpts{Intent: IntentDebug})
	if err != nil {
		t.Fatalf("AssembleContextWith debug: %v", err)
	}
	if debugBlock.Intent != IntentDebug {
		t.Errorf("expected intent debug, got %q", debugBlock.Intent)
	}

	// Assemble with continue intent
	contBlock, err := engine.AssembleContextWith(ctx, "user1", "resume work", 5000, &ContextOpts{Intent: IntentContinue})
	if err != nil {
		t.Fatalf("AssembleContextWith continue: %v", err)
	}
	if contBlock.Intent != IntentContinue {
		t.Errorf("expected intent continue, got %q", contBlock.Intent)
	}

	// Auto-detect: "session context" should resolve to continue
	autoBlock, err := engine.AssembleContextWith(ctx, "user1", "session context", 5000, nil)
	if err != nil {
		t.Fatalf("AssembleContextWith auto: %v", err)
	}
	if autoBlock.Intent != IntentContinue {
		t.Errorf("expected auto-detected intent continue for 'session context', got %q", autoBlock.Intent)
	}
}

func TestSaveAndGetCheckpoints(t *testing.T) {
	engine := newTestEngine(t)
	ctx := context.Background()

	// Save a checkpoint
	cp := &Checkpoint{
		Task:           "auth refactor",
		Status:         "in_progress",
		FilesActive:    []string{"auth.go", "middleware.go"},
		CompletedSteps: []string{"JWT validation", "middleware setup"},
		RemainingSteps: []string{"refresh tokens", "logout endpoint"},
	}
	if err := engine.SaveCheckpoint(ctx, "user1", cp); err != nil {
		t.Fatalf("SaveCheckpoint: %v", err)
	}

	// Retrieve it
	checkpoints, err := engine.GetCheckpoints(ctx, "user1")
	if err != nil {
		t.Fatalf("GetCheckpoints: %v", err)
	}
	if len(checkpoints) != 1 {
		t.Fatalf("expected 1 checkpoint, got %d", len(checkpoints))
	}
	if checkpoints[0].Task != "auth refactor" {
		t.Errorf("task = %q, want %q", checkpoints[0].Task, "auth refactor")
	}
	if len(checkpoints[0].RemainingSteps) != 2 {
		t.Errorf("remaining steps = %d, want 2", len(checkpoints[0].RemainingSteps))
	}
}

func TestCheckpointSupersession(t *testing.T) {
	engine := newTestEngine(t)
	ctx := context.Background()

	// Save initial checkpoint
	cp1 := &Checkpoint{
		Task:           "auth refactor",
		Status:         "in_progress",
		CompletedSteps: []string{"JWT validation"},
		RemainingSteps: []string{"refresh tokens", "logout"},
	}
	if err := engine.SaveCheckpoint(ctx, "user1", cp1); err != nil {
		t.Fatal(err)
	}

	// Save updated checkpoint with same task — should supersede
	cp2 := &Checkpoint{
		Task:           "auth refactor",
		Status:         "in_progress",
		CompletedSteps: []string{"JWT validation", "refresh tokens"},
		RemainingSteps: []string{"logout"},
	}
	if err := engine.SaveCheckpoint(ctx, "user1", cp2); err != nil {
		t.Fatal(err)
	}

	// Should only have one checkpoint
	checkpoints, err := engine.GetCheckpoints(ctx, "user1")
	if err != nil {
		t.Fatal(err)
	}
	if len(checkpoints) != 1 {
		t.Errorf("expected 1 checkpoint after supersession, got %d", len(checkpoints))
	}
	if len(checkpoints[0].CompletedSteps) != 2 {
		t.Errorf("completed steps = %d, want 2 (should be updated version)", len(checkpoints[0].CompletedSteps))
	}
}

func TestCheckpointFormatForContext(t *testing.T) {
	cp := &Checkpoint{
		Task:            "auth refactor",
		Status:          "blocked",
		FilesActive:     []string{"auth.go:42-80", "middleware.go"},
		DecisionPending: "JWT vs session tokens",
		CompletedSteps:  []string{"JWT validation"},
		RemainingSteps:  []string{"refresh tokens", "logout"},
		BlockedOn:       "waiting for API spec",
	}

	text := cp.FormatForContext()
	if !strings.Contains(text, "[Checkpoint: auth refactor]") {
		t.Error("missing checkpoint header")
	}
	if !strings.Contains(text, "status=blocked") {
		t.Error("missing status")
	}
	if !strings.Contains(text, "auth.go:42-80") {
		t.Error("missing files")
	}
	if !strings.Contains(text, "JWT vs session tokens") {
		t.Error("missing pending decision")
	}
	if !strings.Contains(text, "waiting for API spec") {
		t.Error("missing blocked reason")
	}
}

func TestCheckpointInAssembleContext(t *testing.T) {
	engine := newTestEngine(t)
	ctx := context.Background()

	// Save a checkpoint
	cp := &Checkpoint{
		Task:           "auth refactor",
		Status:         "in_progress",
		RemainingSteps: []string{"logout endpoint"},
	}
	engine.SaveCheckpoint(ctx, "user1", cp)

	// Also add a regular memory
	engine.AddWithOptions(ctx, MemoryInput{
		UserMessage: "Chose SQLite over Postgres",
		SectorHint:  "decision",
		Salience:    0.8,
		Type:        "decision",
	}, "user1")

	// Assemble context
	block, err := engine.AssembleContext(ctx, "user1", "session context", 5000)
	if err != nil {
		t.Fatal(err)
	}

	// Checkpoint should appear in output
	if !strings.Contains(block.Text, "[Checkpoint: auth refactor]") {
		t.Error("checkpoint not found in context output")
	}
	// Regular memory should also appear
	if !strings.Contains(block.Text, "SQLite") {
		t.Error("regular memory not found in context output")
	}
	// Checkpoint source should be tracked
	hasCheckpointSource := false
	for _, s := range block.Sources {
		if s.Type == "checkpoint" {
			hasCheckpointSource = true
		}
	}
	if !hasCheckpointSource {
		t.Error("checkpoint source not tracked")
	}
}

func TestExtractEntityCandidates(t *testing.T) {
	tests := []struct {
		query    string
		wantAny  []string // at least these should appear
		minCount int      // minimum number of candidates
	}{
		{
			query:    "auth middleware",
			wantAny:  []string{"auth", "middleware", "auth middleware"},
			minCount: 3,
		},
		{
			query:    "SQLite database",
			wantAny:  []string{"sqlite", "database", "sqlite database"},
			minCount: 3,
		},
		{
			query:   "a",
			wantAny: nil, // single-char word filtered out
		},
		{
			query:    "HDC encoder performance",
			wantAny:  []string{"hdc", "encoder", "hdc encoder", "encoder performance"},
			minCount: 5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			got := extractEntityCandidates(tt.query)
			if tt.minCount > 0 && len(got) < tt.minCount {
				t.Errorf("extractEntityCandidates(%q) returned %d candidates, want >= %d: %v",
					tt.query, len(got), tt.minCount, got)
			}
			for _, want := range tt.wantAny {
				found := false
				for _, g := range got {
					if g == want {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("extractEntityCandidates(%q) missing %q, got %v", tt.query, want, got)
				}
			}
		})
	}
}

func TestDualSearch_WithGraphBoost(t *testing.T) {
	// End-to-end test: create engine, add entities, and verify graph boost in DualSearch
	engine := newTestEngine(t)
	ctx := context.Background()
	userID := "test-graphboost"
	ns := "test-ns"

	// Insert memories via engine with high salience to ensure detail routing
	memories := []MemoryInput{
		{UserMessage: "button component uses React.memo for performance optimization", Salience: 0.9, Type: "decision"},
		{UserMessage: "auth middleware rewrite driven by legal compliance requirements", Salience: 0.9, Type: "decision"},
		{UserMessage: "database migration uses SQLite for zero-setup deployments", Salience: 0.9, Type: "decision"},
	}
	for _, m := range memories {
		if err := engine.AddWithOptions(ctx, m, userID); err != nil {
			t.Fatalf("AddWithOptions: %v", err)
		}
	}

	// Get the detail memories to find their IDs
	details, _ := engine.store.GetDetailMemories(userID)
	if len(details) < 3 {
		t.Fatalf("Expected >= 3 detail memories, got %d", len(details))
	}

	// Create an entity and link it to the auth memory
	authMemID := ""
	for _, d := range details {
		if strings.Contains(d.Text, "auth middleware") {
			authMemID = d.ID
			break
		}
	}
	if authMemID == "" {
		t.Fatal("Could not find auth middleware memory")
	}

	// Create entity node
	entityID, err := engine.store.UpsertEntity(&EntityNode{
		Name:      "auth",
		Type:      "concept",
		Namespace: ns,
	})
	if err != nil {
		t.Fatalf("UpsertEntity: %v", err)
	}

	// Link memory to entity
	if err := engine.store.LinkMemoryToEntity(authMemID, entityID, ns); err != nil {
		t.Fatalf("LinkMemoryToEntity: %v", err)
	}

	// Search with graph boost — querying for "auth" should boost the auth memory
	result, err := engine.DualSearch(ctx, userID, "auth system", SearchOpts{
		Limit:     10,
		QueryText: "auth system",
		GraphBoost: &GraphBoostConfig{
			Namespace:   ns,
			BoostWeight: 0.3,
		},
	})
	if err != nil {
		t.Fatalf("DualSearch with graph boost: %v", err)
	}

	if len(result.DetailMemories) == 0 {
		t.Fatal("No detail memories returned")
	}

	// The auth memory should appear in results (graph boost helps ensure this)
	found := false
	for _, dm := range result.DetailMemories {
		if strings.Contains(dm.Text, "auth middleware") {
			found = true
			break
		}
	}
	if !found {
		t.Error("Auth middleware memory not found in graph-boosted results")
	}

	// Search WITHOUT graph boost for comparison (should still work)
	resultNoBoost, err := engine.DualSearch(ctx, userID, "auth system", SearchOpts{
		Limit:     10,
		QueryText: "auth system",
	})
	if err != nil {
		t.Fatalf("DualSearch without graph boost: %v", err)
	}
	if len(resultNoBoost.DetailMemories) == 0 {
		t.Fatal("No detail memories returned without boost")
	}
}

func TestAssembleContextWith_GraphBoost(t *testing.T) {
	engine := newTestEngine(t)
	ctx := context.Background()
	userID := "test-ctx-graphboost"
	ns := userID // AssembleContextWith uses userID as namespace for GraphBoost

	// Insert memories with distinct topics
	memories := []MemoryInput{
		{UserMessage: "payment gateway uses Stripe for card processing", Salience: 0.9, Type: "decision"},
		{UserMessage: "caching layer uses Redis for session storage", Salience: 0.9, Type: "decision"},
		{UserMessage: "deployment pipeline runs on GitHub Actions CI/CD", Salience: 0.9, Type: "decision"},
	}
	for _, m := range memories {
		if err := engine.AddWithOptions(ctx, m, userID); err != nil {
			t.Fatalf("AddWithOptions: %v", err)
		}
	}

	// Find the payment memory ID
	details, _ := engine.store.GetDetailMemories(userID)
	if len(details) < 3 {
		t.Fatalf("Expected >= 3 detail memories, got %d", len(details))
	}

	paymentMemID := ""
	for _, d := range details {
		if strings.Contains(d.Text, "payment gateway") {
			paymentMemID = d.ID
			break
		}
	}
	if paymentMemID == "" {
		t.Fatal("Could not find payment gateway memory")
	}

	// Create entity and link to payment memory
	entityID, err := engine.store.UpsertEntity(&EntityNode{
		Name:      "stripe",
		Type:      "service",
		Namespace: ns,
	})
	if err != nil {
		t.Fatalf("UpsertEntity: %v", err)
	}
	if err := engine.store.LinkMemoryToEntity(paymentMemID, entityID, ns); err != nil {
		t.Fatalf("LinkMemoryToEntity: %v", err)
	}

	// AssembleContextWith should auto-enable graph boost
	block, err := engine.AssembleContextWith(ctx, userID, "stripe integration", 5000, nil)
	if err != nil {
		t.Fatalf("AssembleContextWith: %v", err)
	}
	if block.Text == "" {
		t.Fatal("Empty context block")
	}

	// Payment memory should appear in output (graph boost surfaces it)
	if !strings.Contains(block.Text, "payment gateway") && !strings.Contains(block.Text, "Stripe") {
		t.Error("Expected payment/Stripe memory in graph-boosted context output")
	}

	// With DisableGraphBoost, the search should still work (just no boost)
	blockNoBoost, err := engine.AssembleContextWith(ctx, userID, "stripe integration", 5000, &ContextOpts{
		DisableGraphBoost: true,
	})
	if err != nil {
		t.Fatalf("AssembleContextWith (no graph): %v", err)
	}
	// Should still return context (other retrieval paths work)
	if blockNoBoost == nil {
		t.Fatal("Nil context block with DisableGraphBoost")
	}
}

func TestFileContext(t *testing.T) {
	engine := newTestEngine(t)
	ctx := context.Background()

	// Add a warning with files
	if err := engine.AddWithOptions(ctx, MemoryInput{
		UserMessage: "Don't touch rateLimiter cleanup hot path",
		SectorHint:  "warning",
		Salience:    0.9,
		Type:        "warning",
		Files:       []string{"rate_limiter.go"},
	}, "user1"); err != nil {
		t.Fatalf("AddWithOptions warning: %v", err)
	}

	// Add a decision with files
	if err := engine.AddWithOptions(ctx, MemoryInput{
		UserMessage: "Chose SQLite over Postgres for zero-setup",
		SectorHint:  "decision",
		Salience:    0.8,
		Type:        "decision",
		Files:       []string{"store_sqlite.go", "config.go"},
	}, "user1"); err != nil {
		t.Fatalf("AddWithOptions decision: %v", err)
	}

	// Add a map memory with files (should appear in FileContext results)
	if err := engine.AddWithOptions(ctx, MemoryInput{
		UserMessage: "Rate limiter flow: api.go → rate_limiter.go",
		SectorHint:  "procedural",
		Salience:    0.7,
		Type:        "map",
		Files:       []string{"rate_limiter.go"},
	}, "user1"); err != nil {
		t.Fatalf("AddWithOptions map: %v", err)
	}

	// Add a general memory (should NOT appear in FileContext results)
	if err := engine.AddWithOptions(ctx, MemoryInput{
		UserMessage: "Rate limiter handles 10k req/s",
		SectorHint:  "semantic",
		Salience:    0.8,
		Type:        "",
		Files:       []string{"rate_limiter.go"},
	}, "user1"); err != nil {
		t.Fatalf("AddWithOptions general: %v", err)
	}

	// Query for rate_limiter.go — should return warning + map (2 results)
	results, err := engine.FileContext(ctx, "user1", "rate_limiter.go", 0)
	if err != nil {
		t.Fatalf("FileContext: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("Expected 2 results for rate_limiter.go, got %d", len(results))
	}
	typeSet := make(map[string]bool)
	for _, r := range results {
		typeSet[r.Type] = true
	}
	if !typeSet["warning"] {
		t.Errorf("Expected a warning result, got types: %v", typeSet)
	}
	if !typeSet["map"] {
		t.Errorf("Expected a map result, got types: %v", typeSet)
	}

	// Query for nonexistent file — should return 0
	results, err = engine.FileContext(ctx, "user1", "nonexistent.go", 0)
	if err != nil {
		t.Fatalf("FileContext nonexistent: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("Expected 0 results for nonexistent.go, got %d", len(results))
	}
}

func TestFileIndex(t *testing.T) {
	engine := newTestEngine(t)
	ctx := context.Background()

	// Add warning with files
	if err := engine.AddWithOptions(ctx, MemoryInput{
		UserMessage: "Don't modify foo internals",
		SectorHint:  "warning",
		Salience:    0.9,
		Type:        "warning",
		Files:       []string{"foo.go"},
	}, "user1"); err != nil {
		t.Fatalf("AddWithOptions warning: %v", err)
	}

	// Add decision with files
	if err := engine.AddWithOptions(ctx, MemoryInput{
		UserMessage: "Use bar and baz together",
		SectorHint:  "decision",
		Salience:    0.8,
		Type:        "decision",
		Files:       []string{"bar.go", "baz.go"},
	}, "user1"); err != nil {
		t.Fatalf("AddWithOptions decision: %v", err)
	}

	// Add general memory (should NOT appear in FileIndex)
	if err := engine.AddWithOptions(ctx, MemoryInput{
		UserMessage: "Qux handles background jobs",
		SectorHint:  "semantic",
		Salience:    0.8,
		Type:        "",
		Files:       []string{"qux.go"},
	}, "user1"); err != nil {
		t.Fatalf("AddWithOptions general: %v", err)
	}

	files, err := engine.FileIndex(ctx, "user1")
	if err != nil {
		t.Fatalf("FileIndex: %v", err)
	}

	fileSet := make(map[string]bool)
	for _, f := range files {
		fileSet[f] = true
	}

	for _, want := range []string{"foo.go", "bar.go", "baz.go"} {
		if !fileSet[want] {
			t.Errorf("FileIndex missing %s, got %v", want, files)
		}
	}
	if fileSet["qux.go"] {
		t.Errorf("FileIndex should NOT contain qux.go (general type), got %v", files)
	}
}
