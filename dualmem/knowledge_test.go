package dualmem

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// mockTextGenSummarizer implements both SummarizerProvider and TextGenerator.
type mockTextGenSummarizer struct {
	generateCalls int
	lastPrompt    string
}

func (m *mockTextGenSummarizer) SummarizeEpisode(_ context.Context, messages []string) (string, error) {
	return "Episode: " + messages[0], nil
}

func (m *mockTextGenSummarizer) SummarizeArc(_ context.Context, episodes []string) (string, error) {
	return "Arc: " + episodes[0], nil
}

func (m *mockTextGenSummarizer) UpdateProfile(_ context.Context, current *ProfileSketch, newArcs []string) (*ProfileSketch, error) {
	return &ProfileSketch{Interests: []string{"test"}}, nil
}

func (m *mockTextGenSummarizer) GenerateText(_ context.Context, prompt string, _ int) (string, error) {
	m.generateCalls++
	m.lastPrompt = prompt
	return fmt.Sprintf("Synthesized knowledge document based on %d source memories.", strings.Count(prompt, ". ")+1), nil
}

func newTestEngineWithGen(t *testing.T) (*Engine, *mockTextGenSummarizer) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	gen := &mockTextGenSummarizer{}
	engine, err := New(Config{
		SQLitePath:        dbPath,
		EmbeddingProvider: &mockEmbedder{dim: 768},
		Classifier:        &mockClassifier{},
		EntityExtractor:   &mockExtractor{},
		Summarizer:        gen,
		MaxDetailPerUser:  50,
		ImportanceTheta:   0.65,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { engine.Close() })
	return engine, gen
}

// --- Clustering tests ---

func TestClusterByFileOverlap(t *testing.T) {
	memories := []detailWithVector{
		{DetailMemory: DetailMemory{ID: "1", Text: "auth uses JWT", Files: []string{"auth.go", "middleware.go"}}, UserID: "u1"},
		{DetailMemory: DetailMemory{ID: "2", Text: "refresh tokens in Redis", Files: []string{"auth.go", "middleware.go", "redis.go"}}, UserID: "u1"},
		{DetailMemory: DetailMemory{ID: "3", Text: "rate limiter cleanup", Files: []string{"middleware.go", "auth.go"}}, UserID: "u1"},
		{DetailMemory: DetailMemory{ID: "4", Text: "HDC encoder uses 2048 dims", Files: []string{"hdc.go", "codemap.go"}}, UserID: "u1"},
		{DetailMemory: DetailMemory{ID: "5", Text: "content layer extracts identifiers", Files: []string{"hdc.go", "codemap.go"}}, UserID: "u1"},
	}

	clusters := clusterByFileOverlap(memories, 2)
	if len(clusters) != 2 {
		t.Fatalf("expected 2 clusters, got %d", len(clusters))
	}

	// Check cluster sizes (order may vary)
	sizes := make(map[int]bool)
	for _, c := range clusters {
		sizes[len(c.members)] = true
	}
	if !sizes[3] || !sizes[2] {
		t.Errorf("expected clusters of size 3 and 2, got %v", sizes)
	}
}

func TestClusterByFileOverlap_NoOverlap(t *testing.T) {
	memories := []detailWithVector{
		{DetailMemory: DetailMemory{ID: "1", Text: "auth", Files: []string{"auth.go"}}, UserID: "u1"},
		{DetailMemory: DetailMemory{ID: "2", Text: "hdc", Files: []string{"hdc.go"}}, UserID: "u1"},
	}

	clusters := clusterByFileOverlap(memories, 2)
	// No overlap ≥ 2, so each memory is its own cluster
	if len(clusters) != 2 {
		t.Fatalf("expected 2 singleton clusters, got %d", len(clusters))
	}
}

func TestClusterMemories_WithExistingDocs(t *testing.T) {
	memories := []detailWithVector{
		{DetailMemory: DetailMemory{ID: "1", Text: "new auth fact", Files: []string{"auth.go", "middleware.go"}}, UserID: "u1"},
		{DetailMemory: DetailMemory{ID: "2", Text: "another auth fact", Files: []string{"auth.go", "middleware.go"}}, UserID: "u1"},
		{DetailMemory: DetailMemory{ID: "3", Text: "third auth thing", Files: []string{"auth.go", "middleware.go"}}, UserID: "u1"},
	}

	existingDocs := []KnowledgeDoc{
		{Topic: "auth-system", Files: []string{"auth.go", "middleware.go"}, SourceIDs: []string{"old1"}},
	}

	clusters, orphaned := clusterMemories(memories, existingDocs, 3)

	// All 3 share ≥2 files with the existing doc, so they should cluster under "auth-system"
	if len(clusters) != 1 {
		t.Fatalf("expected 1 cluster, got %d (orphaned: %d)", len(clusters), orphaned)
	}
	if clusters[0].topic != "auth-system" {
		t.Errorf("expected topic 'auth-system', got %q", clusters[0].topic)
	}
}

func TestAutoTopicName(t *testing.T) {
	tests := []struct {
		files    []string
		expected string
	}{
		{[]string{"auth.go", "auth_test.go"}, "auth"},
		{[]string{"pkg/hdc/encoder.go", "pkg/hdc/vector.go"}, "pkg-hdc"},
		{[]string{}, "unnamed"},
	}

	for _, tt := range tests {
		got := autoTopicName(tt.files, nil)
		if got != tt.expected {
			t.Errorf("autoTopicName(%v) = %q, want %q", tt.files, got, tt.expected)
		}
	}
}

func TestSanitizeTopic(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"auth/middleware", "auth-middleware"},
		{"Hello World", "hello-world"},
		{"pkg/hdc", "pkg-hdc"},
		{"--leading-dashes--", "leading-dashes"},
	}

	for _, tt := range tests {
		got := sanitizeTopic(tt.input)
		if got != tt.expected {
			t.Errorf("sanitizeTopic(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

// --- Synthesis prompt tests ---

func TestFormatSynthesisPrompt(t *testing.T) {
	memories := []detailWithVector{
		{DetailMemory: DetailMemory{ID: "1", Text: "Auth uses JWT tokens", Type: "decision"}, UserID: "u1"},
		{DetailMemory: DetailMemory{ID: "2", Text: "Rate limiter skips nil check", Type: "warning"}, UserID: "u1"},
	}

	prompt := formatSynthesisPrompt("auth-middleware", []string{"auth.go"}, memories)

	if !strings.Contains(prompt, "auth-middleware") {
		t.Error("prompt missing topic")
	}
	if !strings.Contains(prompt, "auth.go") {
		t.Error("prompt missing files")
	}
	if !strings.Contains(prompt, "[decision]") {
		t.Error("prompt missing memory type annotations")
	}
	if !strings.Contains(prompt, "[warning]") {
		t.Error("prompt missing warning annotation")
	}
}

// --- Staleness tests ---

func TestIsDocStale(t *testing.T) {
	docTime := time.Now().Add(-1 * time.Hour)
	doc := &KnowledgeDoc{
		SourceIDs: []string{"m1", "m2"},
		UpdatedAt: docTime,
	}

	// Memory created before doc → not stale
	memories := []detailWithVector{
		{DetailMemory: DetailMemory{ID: "m1", CreatedAt: docTime.Add(-30 * time.Minute)}},
		{DetailMemory: DetailMemory{ID: "m2", CreatedAt: docTime.Add(-30 * time.Minute)}},
	}
	if isDocStale(doc, memories) {
		t.Error("expected not stale when all source memories older than doc")
	}

	// Memory created after doc → stale
	memories[0].CreatedAt = docTime.Add(10 * time.Minute)
	if !isDocStale(doc, memories) {
		t.Error("expected stale when source memory is newer than doc")
	}
}

// --- Integration tests ---

func TestSynthesize_EndToEnd(t *testing.T) {
	engine, gen := newTestEngineWithGen(t)
	ctx := context.Background()
	ns := "test-ns"

	// Add memories that share files (should cluster)
	for _, mem := range []MemoryInput{
		{UserMessage: "Auth uses JWT for validation", SectorHint: "decision", Salience: 0.9, Files: []string{"auth.go", "middleware.go"}},
		{UserMessage: "Refresh tokens stored in Redis with 7-day TTL", SectorHint: "decision", Salience: 0.9, Files: []string{"auth.go", "middleware.go"}},
		{UserMessage: "Rate limiter nil check skipped intentionally", SectorHint: "warning", Salience: 0.9, Files: []string{"middleware.go", "auth.go"}, Type: "warning"},
	} {
		if err := engine.AddWithOptions(ctx, mem, ns); err != nil {
			t.Fatalf("Add: %v", err)
		}
	}

	// Synthesize
	result, err := engine.Synthesize(ctx, ns, nil)
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}

	if gen.generateCalls == 0 {
		t.Error("expected at least one LLM call")
	}

	if len(result.Created) == 0 {
		t.Fatal("expected at least one knowledge doc created")
	}

	doc := result.Created[0]
	if doc.Content == "" {
		t.Error("knowledge doc has empty content")
	}
	if doc.TokenCount == 0 {
		t.Error("knowledge doc has zero token count")
	}
	if len(doc.SourceIDs) != 3 {
		t.Errorf("expected 3 source IDs, got %d", len(doc.SourceIDs))
	}

	// Verify doc is retrievable
	docs, err := engine.ListKnowledgeDocs(ctx, ns)
	if err != nil {
		t.Fatalf("ListKnowledgeDocs: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("expected 1 doc, got %d", len(docs))
	}
	if docs[0].Topic == "" {
		t.Error("doc has empty topic")
	}

	t.Logf("Created doc: topic=%q tokens=%d sources=%d", doc.Topic, doc.TokenCount, len(doc.SourceIDs))
}

func TestSynthesize_DryRun(t *testing.T) {
	engine, gen := newTestEngineWithGen(t)
	ctx := context.Background()
	ns := "test-ns"

	for _, mem := range []MemoryInput{
		{UserMessage: "fact 1", SectorHint: "decision", Salience: 0.9, Files: []string{"a.go", "b.go"}},
		{UserMessage: "fact 2", SectorHint: "decision", Salience: 0.9, Files: []string{"a.go", "b.go"}},
		{UserMessage: "fact 3", SectorHint: "decision", Salience: 0.9, Files: []string{"a.go", "b.go"}},
	} {
		engine.AddWithOptions(ctx, mem, ns)
	}

	_, err := engine.Synthesize(ctx, ns, &SynthesizeOpts{DryRun: true})
	if err != nil {
		t.Fatalf("Synthesize dry run: %v", err)
	}

	if gen.generateCalls != 0 {
		t.Error("dry run should not call LLM")
	}

	// Verify no docs were actually stored
	docs, _ := engine.ListKnowledgeDocs(ctx, ns)
	if len(docs) != 0 {
		t.Errorf("dry run should not create docs, got %d", len(docs))
	}
}

func TestSynthesize_SkipsFreshDocs(t *testing.T) {
	engine, gen := newTestEngineWithGen(t)
	ctx := context.Background()
	ns := "test-ns"

	for _, mem := range []MemoryInput{
		{UserMessage: "Auth system uses JWT for session validation", SectorHint: "decision", Salience: 0.9, Files: []string{"a.go", "b.go"}},
		{UserMessage: "Refresh tokens stored in Redis with 7-day TTL", SectorHint: "decision", Salience: 0.9, Files: []string{"a.go", "b.go"}},
		{UserMessage: "Rate limiter nil check skipped intentionally for hot path", SectorHint: "decision", Salience: 0.9, Files: []string{"a.go", "b.go"}},
	} {
		engine.AddWithOptions(ctx, mem, ns)
	}

	// First synthesis
	_, err := engine.Synthesize(ctx, ns, nil)
	if err != nil {
		t.Fatalf("First synthesize: %v", err)
	}
	firstCalls := gen.generateCalls

	// Small delay to ensure timestamps differ
	time.Sleep(10 * time.Millisecond)

	// Second synthesis without new memories — should skip
	result, err := engine.Synthesize(ctx, ns, nil)
	if err != nil {
		t.Fatalf("Second synthesize: %v", err)
	}

	if gen.generateCalls != firstCalls {
		t.Errorf("expected no new LLM calls (was %d, now %d)", firstCalls, gen.generateCalls)
	}
	if result.Skipped == 0 {
		t.Error("expected at least one skipped doc")
	}
}

func TestDeleteKnowledgeDoc(t *testing.T) {
	engine, _ := newTestEngineWithGen(t)
	ctx := context.Background()
	ns := "test-ns"

	for _, mem := range []MemoryInput{
		{UserMessage: "Auth uses JWT for validation in middleware", SectorHint: "decision", Salience: 0.9, Files: []string{"a.go", "b.go"}},
		{UserMessage: "Refresh tokens stored in Redis with TTL", SectorHint: "decision", Salience: 0.9, Files: []string{"a.go", "b.go"}},
		{UserMessage: "Rate limiter nil check skipped intentionally", SectorHint: "decision", Salience: 0.9, Files: []string{"a.go", "b.go"}},
	} {
		engine.AddWithOptions(ctx, mem, ns)
	}

	engine.Synthesize(ctx, ns, nil)

	docs, _ := engine.ListKnowledgeDocs(ctx, ns)
	if len(docs) == 0 {
		t.Fatal("expected at least one doc")
	}

	// Delete
	err := engine.DeleteKnowledgeDoc(ctx, ns, docs[0].Topic)
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// Verify deleted
	docs, _ = engine.ListKnowledgeDocs(ctx, ns)
	if len(docs) != 0 {
		t.Errorf("expected 0 docs after delete, got %d", len(docs))
	}
}

func TestKnowledgeDocsInAssembleContext(t *testing.T) {
	engine, _ := newTestEngineWithGen(t)
	ctx := context.Background()
	ns := "test-ns"

	// Add and synthesize
	for _, mem := range []MemoryInput{
		{UserMessage: "Auth uses JWT for validation", SectorHint: "decision", Salience: 0.9, Files: []string{"auth.go", "middleware.go"}},
		{UserMessage: "Refresh tokens stored in Redis", SectorHint: "decision", Salience: 0.9, Files: []string{"auth.go", "middleware.go"}},
		{UserMessage: "Rate limiter nil check skipped", SectorHint: "warning", Salience: 0.9, Files: []string{"middleware.go", "auth.go"}, Type: "warning"},
	} {
		engine.AddWithOptions(ctx, mem, ns)
	}

	engine.Synthesize(ctx, ns, nil)

	// Assemble context
	block, err := engine.AssembleContext(ctx, ns, "how does auth work", 3000)
	if err != nil {
		t.Fatalf("AssembleContext: %v", err)
	}

	// Should contain a [Knowledge: ...] section
	if !strings.Contains(block.Text, "[Knowledge:") {
		t.Error("context output missing [Knowledge:] section")
	}

	// Should have a "knowledge" source type
	hasKnowledgeSource := false
	for _, src := range block.Sources {
		if src.Type == "knowledge" {
			hasKnowledgeSource = true
			break
		}
	}
	if !hasKnowledgeSource {
		t.Error("context sources missing 'knowledge' type")
	}

	t.Logf("Context output (%d tokens):\n%s", block.TokenCount, block.Text)
}

func TestKnowledgeDocFormatForContext(t *testing.T) {
	doc := KnowledgeDoc{
		Topic:   "auth-middleware",
		Content: "The auth system uses JWT tokens.",
		Files:   []string{"auth.go", "middleware.go"},
	}

	formatted := doc.FormatForContext()

	if !strings.Contains(formatted, "[Knowledge: auth-middleware]") {
		t.Error("missing topic header")
	}
	if !strings.Contains(formatted, "The auth system uses JWT tokens.") {
		t.Error("missing content")
	}
	if !strings.Contains(formatted, "auth.go") {
		t.Error("missing file reference")
	}
}

func TestFilesOverlap(t *testing.T) {
	if filesOverlap([]string{"a.go", "b.go"}, []string{"b.go", "c.go"}) != 1 {
		t.Error("expected overlap of 1")
	}
	if filesOverlap([]string{"a.go", "b.go"}, []string{"a.go", "b.go"}) != 2 {
		t.Error("expected overlap of 2")
	}
	if filesOverlap([]string{"a.go"}, []string{"b.go"}) != 0 {
		t.Error("expected overlap of 0")
	}
	if filesOverlap(nil, []string{"a.go"}) != 0 {
		t.Error("expected overlap of 0 for nil")
	}
}

func TestMergeFiles(t *testing.T) {
	merged := mergeFiles([]string{"b.go", "a.go"}, []string{"c.go", "a.go"})
	if len(merged) != 3 {
		t.Errorf("expected 3 unique files, got %d", len(merged))
	}
	// Should be sorted
	if merged[0] != "a.go" {
		t.Errorf("expected sorted, got %v", merged)
	}
}

func TestGetUncoveredMemories(t *testing.T) {
	engine, _ := newTestEngineWithGen(t)
	ctx := context.Background()
	ns := "test-ns"

	// Add memories with very distinct text to avoid novelty dedup
	texts := []string{
		"Authentication system uses JWT tokens for session management",
		"Database migrations run automatically on startup via goose",
		"Rate limiter uses sliding window algorithm for throttling",
		"WebSocket connections managed by gorilla/websocket library",
		"Configuration loaded from YAML with environment variable overrides",
	}
	for _, text := range texts {
		engine.AddWithOptions(ctx, MemoryInput{
			UserMessage: text,
			SectorHint:  "decision",
			Salience:    0.9,
			Files:       []string{"a.go", "b.go"},
		}, ns)
	}

	// Check how many actually routed to detail
	allDetail, _ := engine.store.GetDetailMemories(ns)
	detailCount := 0
	for _, dm := range allDetail {
		if dm.Type != "checkpoint" && dm.Type != "seed" {
			detailCount++
		}
	}
	t.Logf("Detail memories after add: %d", detailCount)

	// Before synthesis, all detail memories should be uncovered
	uncovered, err := engine.store.GetUncoveredMemories(ns)
	if err != nil {
		t.Fatalf("GetUncoveredMemories: %v", err)
	}
	if len(uncovered) != detailCount {
		t.Errorf("expected %d uncovered before synthesis, got %d", detailCount, len(uncovered))
	}

	if detailCount < 3 {
		t.Skipf("only %d memories routed to detail (need ≥3 for a cluster), skipping synthesis check", detailCount)
	}

	// Synthesize
	result, _ := engine.Synthesize(ctx, ns, nil)
	t.Logf("Synthesize: created=%d orphaned=%d", len(result.Created), result.Orphaned)

	// After synthesis, covered memories should be excluded
	uncovered, err = engine.store.GetUncoveredMemories(ns)
	if err != nil {
		t.Fatalf("GetUncoveredMemories after: %v", err)
	}

	// Some should now be covered
	if len(uncovered) >= detailCount {
		t.Errorf("expected fewer uncovered after synthesis, got %d (same as before)", len(uncovered))
	}
}
