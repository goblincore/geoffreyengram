package engram

import (
	"context"
	"path/filepath"
	"testing"
)

// mockReflector implements ReflectionProvider for testing.
type mockReflector struct {
	reflections []Reflection
	err         error
	calledWith  []Memory // records what memories were passed
}

func (m *mockReflector) Reflect(ctx context.Context, memories []Memory, charCtx string) ([]Reflection, error) {
	m.calledWith = memories
	return m.reflections, m.err
}

// mockEmbedder implements EmbeddingProvider for testing.
type mockEmbedder struct {
	vec []float32
	dim int
}

func (m *mockEmbedder) Embed(ctx context.Context, text, taskType string) ([]float32, error) {
	return m.vec, nil
}

func (m *mockEmbedder) Dimension() int { return m.dim }

func testEngram(t *testing.T, reflector ReflectionProvider, embedder EmbeddingProvider) *Engram {
	t.Helper()
	dir := t.TempDir()
	cfg := Config{
		DBPath:             filepath.Join(dir, "test.db"),
		ReflectionProvider: reflector,
		EmbeddingProvider:  embedder,
		DecayInterval:      999999 * 1e9, // effectively disable decay worker for tests
	}
	cm, err := Init(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cm.Close() })
	return cm
}

func TestReflectNoProvider(t *testing.T) {
	cm := testEngram(t, nil, nil)
	_, err := cm.Reflect(context.Background(), ReflectOptions{UserID: "u1"})
	if err == nil {
		t.Error("expected error when no ReflectionProvider configured")
	}
}

func TestReflectMinMemories(t *testing.T) {
	mock := &mockReflector{
		reflections: []Reflection{{Content: "pattern!", Salience: 0.8}},
	}
	cm := testEngram(t, mock, nil)

	// Add only 2 memories (below default MinMemories of 5)
	cm.store.InsertMemory(Memory{Content: "a", Sector: SectorEpisodic, Salience: 0.5, UserID: "u1", Summary: "a"})
	cm.store.InsertMemory(Memory{Content: "b", Sector: SectorSemantic, Salience: 0.5, UserID: "u1", Summary: "b"})

	results, err := cm.Reflect(context.Background(), ReflectOptions{UserID: "u1"})
	if err != nil {
		t.Fatal(err)
	}
	if results != nil {
		t.Error("expected nil results when below MinMemories")
	}
	if mock.calledWith != nil {
		t.Error("provider should not have been called")
	}
}

func TestReflectStoresMemories(t *testing.T) {
	mock := &mockReflector{
		reflections: []Reflection{
			{Content: "They always mention music when stressed", Salience: 0.8, Entities: []Entity{{Text: "music", Type: "topic"}}},
			{Content: "They seem nostalgic about Japan", Salience: 0.7},
		},
	}
	cm := testEngram(t, mock, nil)

	// Add enough memories
	for i := 0; i < 6; i++ {
		cm.store.InsertMemory(Memory{Content: "memory", Sector: SectorEpisodic, Salience: 0.5, UserID: "u1", Summary: "m"})
	}

	results, err := cm.Reflect(context.Background(), ReflectOptions{UserID: "u1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 reflections stored, got %d", len(results))
	}

	// Verify stored as reflective sector
	for _, r := range results {
		if r.Sector != SectorReflective {
			t.Errorf("expected sector reflective, got %s", r.Sector)
		}
		if r.ID <= 0 {
			t.Error("expected positive ID from storage")
		}
	}

	// Verify in database
	mems, _ := cm.store.GetRecentMemories("u1", 100, []Sector{SectorReflective})
	if len(mems) != 2 {
		t.Errorf("expected 2 reflective memories in DB, got %d", len(mems))
	}
}

func TestReflectFiltersOutReflections(t *testing.T) {
	mock := &mockReflector{
		reflections: []Reflection{{Content: "observation", Salience: 0.7}},
	}
	cm := testEngram(t, mock, nil)

	// Add 4 non-reflective + 3 reflective memories
	for i := 0; i < 4; i++ {
		cm.store.InsertMemory(Memory{Content: "regular", Sector: SectorEpisodic, Salience: 0.5, UserID: "u1", Summary: "r"})
	}
	for i := 0; i < 3; i++ {
		cm.store.InsertMemory(Memory{Content: "old reflection", Sector: SectorReflective, Salience: 0.7, UserID: "u1", Summary: "ref"})
	}

	// Total: 7 memories, but only 4 are non-reflective
	// With MinMemories=5, this should not trigger (4 < 5)
	results, err := cm.Reflect(context.Background(), ReflectOptions{UserID: "u1", MinMemories: 5})
	if err != nil {
		t.Fatal(err)
	}
	if results != nil {
		t.Error("expected nil — not enough non-reflective memories")
	}

	// With MinMemories=3, it should trigger
	results, err = cm.Reflect(context.Background(), ReflectOptions{UserID: "u1", MinMemories: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 reflection, got %d", len(results))
	}

	// Verify only non-reflective memories were passed to the provider
	for _, m := range mock.calledWith {
		if m.Sector == SectorReflective {
			t.Error("reflective memories should not be passed to the provider")
		}
	}
}

func TestReflectSalienceClamping(t *testing.T) {
	mock := &mockReflector{
		reflections: []Reflection{
			{Content: "zero salience", Salience: 0},     // should become 0.7
			{Content: "over salience", Salience: 1.5},    // should become 1.0
			{Content: "normal salience", Salience: 0.6},  // stays 0.6
		},
	}
	cm := testEngram(t, mock, nil)

	for i := 0; i < 6; i++ {
		cm.store.InsertMemory(Memory{Content: "m", Sector: SectorEpisodic, Salience: 0.5, UserID: "u1", Summary: "m"})
	}

	results, err := cm.Reflect(context.Background(), ReflectOptions{UserID: "u1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3, got %d", len(results))
	}

	// Check in DB
	mems, _ := cm.store.GetRecentMemories("u1", 100, []Sector{SectorReflective})
	saliences := make(map[string]float64)
	for _, m := range mems {
		saliences[m.Content] = m.Salience
	}
	if s := saliences["zero salience"]; s != 0.7 {
		t.Errorf("zero salience should clamp to 0.7, got %.1f", s)
	}
	if s := saliences["over salience"]; s != 1.0 {
		t.Errorf("over salience should clamp to 1.0, got %.1f", s)
	}
	if s := saliences["normal salience"]; s != 0.6 {
		t.Errorf("normal salience should stay 0.6, got %.1f", s)
	}
}

func TestReflectDeduplication(t *testing.T) {
	// Use a mock embedder that returns the same vector for everything
	// This means all reflections will be "duplicates" of existing ones
	embed := &mockEmbedder{vec: []float32{1, 0, 0}, dim: 3}

	mock := &mockReflector{
		reflections: []Reflection{
			{Content: "duplicate observation", Salience: 0.7},
		},
	}
	cm := testEngram(t, mock, embed)

	// Add base memories
	for i := 0; i < 6; i++ {
		cm.store.InsertMemory(Memory{Content: "m", Sector: SectorEpisodic, Salience: 0.5, UserID: "u1", Summary: "m"})
	}

	// First reflection — should succeed (no existing reflections)
	results1, err := cm.Reflect(context.Background(), ReflectOptions{UserID: "u1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results1) != 1 {
		t.Fatalf("first reflect: expected 1, got %d", len(results1))
	}

	// Second reflection — same content, should be deduplicated
	results2, err := cm.Reflect(context.Background(), ReflectOptions{UserID: "u1"})
	if err != nil {
		t.Fatal(err)
	}
	if results2 != nil {
		t.Errorf("second reflect: expected nil (deduplicated), got %d results", len(results2))
	}
}

func TestReflectEmptyResult(t *testing.T) {
	mock := &mockReflector{
		reflections: []Reflection{}, // LLM found no patterns
	}
	cm := testEngram(t, mock, nil)

	for i := 0; i < 6; i++ {
		cm.store.InsertMemory(Memory{Content: "m", Sector: SectorEpisodic, Salience: 0.5, UserID: "u1", Summary: "m"})
	}

	results, err := cm.Reflect(context.Background(), ReflectOptions{UserID: "u1"})
	if err != nil {
		t.Fatal(err)
	}
	if results != nil {
		t.Errorf("expected nil for empty reflections, got %d", len(results))
	}
}

func TestParseReflections(t *testing.T) {
	input := `[{"content":"They mention music often","salience":0.8,"entities":[{"text":"music","type":"topic"}]},{"content":"Empty","salience":0.5,"entities":[]}]`

	refs, err := parseReflections(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 2 {
		t.Fatalf("expected 2 reflections, got %d", len(refs))
	}
	if refs[0].Content != "They mention music often" {
		t.Errorf("unexpected content: %s", refs[0].Content)
	}
	if len(refs[0].Entities) != 1 {
		t.Errorf("expected 1 entity, got %d", len(refs[0].Entities))
	}
}

func TestParseReflectionsEmptyArray(t *testing.T) {
	refs, err := parseReflections("[]")
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 0 {
		t.Errorf("expected 0, got %d", len(refs))
	}
}

func TestParseReflectionsCodeBlock(t *testing.T) {
	input := "```json\n[{\"content\":\"pattern\",\"salience\":0.7,\"entities\":[]}]\n```"
	refs, err := parseReflections(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 {
		t.Fatalf("expected 1, got %d", len(refs))
	}
	if refs[0].Content != "pattern" {
		t.Errorf("unexpected content: %s", refs[0].Content)
	}
}
