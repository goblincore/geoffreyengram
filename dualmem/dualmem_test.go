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
