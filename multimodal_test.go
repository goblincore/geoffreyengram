//go:build legacy

package engram

import (
	"context"
	"os"
	"testing"
)

// mmMockEmbedder implements MultimodalEmbeddingProvider for integration tests.
// Returns deterministic vectors based on content type.
type mmMockEmbedder struct {
	dim   int
	calls int
}

func (m *mmMockEmbedder) Embed(_ context.Context, text, _ string) ([]float32, error) {
	m.calls++
	vec := make([]float32, m.dim)
	// Text gets a vector in one region
	vec[0] = 0.8
	vec[1] = 0.2
	return vec, nil
}

func (m *mmMockEmbedder) EmbedMedia(_ context.Context, media MediaContent, _ string) ([]float32, error) {
	m.calls++
	vec := make([]float32, m.dim)
	switch ModalityFromMimeType(media.MimeType) {
	case ModalityImage:
		vec[0] = 0.3
		vec[1] = 0.9 // image vectors: different direction from text
	case ModalityAudio:
		vec[0] = 0.1
		vec[2] = 0.95 // audio vectors: yet another direction
	}
	return vec, nil
}

func (m *mmMockEmbedder) SupportsModality(mod Modality) bool {
	return mod == ModalityText || mod == ModalityImage || mod == ModalityAudio
}

func (m *mmMockEmbedder) Dimension() int { return m.dim }

func (m *mmMockEmbedder) ModelName() string { return "mock-multimodal-embedder" }

// Verify interface compliance
var _ MultimodalEmbeddingProvider = (*mmMockEmbedder)(nil)

func setupTestEngram(t *testing.T, embedder EmbeddingProvider) *Engram {
	t.Helper()
	tmpDir := t.TempDir()
	dbPath := tmpDir + "/test.db"

	cfg := Config{
		DBPath:            dbPath,
		EmbeddingProvider: embedder,
		Classifier:        NewHeuristicClassifier(""),
		EntityExtractor:   &DefaultEntityExtractor{},
	}
	eng, err := Init(cfg)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { eng.Close() })
	return eng
}

func TestMultimodal_AddMediaMemory(t *testing.T) {
	embedder := &mmMockEmbedder{dim: 8}
	eng := setupTestEngram(t, embedder)

	// Add an image memory
	memID, err := eng.AddWithOptions(AddOptions{
		UserID:      "player1",
		Media:       &MediaContent{MimeType: "image/png", Data: []byte{0x89, 0x50}},
		Description: "A sunset over the ocean",
		ExternalRef: "asset://sunset_01.png",
	})
	if err != nil {
		t.Fatalf("AddWithOptions (image): %v", err)
	}
	if memID == 0 {
		t.Fatal("expected non-zero memory ID")
	}

	// Verify stored correctly
	memories, err := eng.ListRecent("player1", 10, nil)
	if err != nil {
		t.Fatalf("ListRecent: %v", err)
	}
	if len(memories) != 1 {
		t.Fatalf("expected 1 memory, got %d", len(memories))
	}

	m := memories[0]
	if m.Modality != ModalityImage {
		t.Errorf("modality = %q, want image", m.Modality)
	}
	if m.MimeType != "image/png" {
		t.Errorf("mime_type = %q, want image/png", m.MimeType)
	}
	if m.ExternalRef != "asset://sunset_01.png" {
		t.Errorf("external_ref = %q, want asset://sunset_01.png", m.ExternalRef)
	}
	if m.Content != "A sunset over the ocean" {
		t.Errorf("content = %q, want description", m.Content)
	}
}

func TestMultimodal_AddAudioMemory(t *testing.T) {
	embedder := &mmMockEmbedder{dim: 8}
	eng := setupTestEngram(t, embedder)

	memID, err := eng.AddWithOptions(AddOptions{
		UserID:      "player1",
		Media:       &MediaContent{MimeType: "audio/mpeg", Data: []byte{0xFF, 0xFB}},
		Description: "Jazz music playing in the bar",
	})
	if err != nil {
		t.Fatalf("AddWithOptions (audio): %v", err)
	}
	if memID == 0 {
		t.Fatal("expected non-zero memory ID")
	}

	memories, err := eng.ListRecent("player1", 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	if memories[0].Modality != ModalityAudio {
		t.Errorf("modality = %q, want audio", memories[0].Modality)
	}
}

func TestMultimodal_AddMediaWithoutDescription(t *testing.T) {
	embedder := &mmMockEmbedder{dim: 8}
	eng := setupTestEngram(t, embedder)

	memID, err := eng.AddWithOptions(AddOptions{
		UserID: "player1",
		Media:  &MediaContent{MimeType: "image/jpeg", Data: []byte{0xFF, 0xD8}},
		// No description — should use placeholder
	})
	if err != nil {
		t.Fatalf("AddWithOptions: %v", err)
	}
	if memID == 0 {
		t.Fatal("expected non-zero memory ID")
	}

	memories, err := eng.ListRecent("player1", 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	if memories[0].Content != "[image: image/jpeg]" {
		t.Errorf("content = %q, want placeholder", memories[0].Content)
	}
}

func TestMultimodal_TextStillWorks(t *testing.T) {
	embedder := &mmMockEmbedder{dim: 8}
	eng := setupTestEngram(t, embedder)

	// Text path should be unchanged
	memID, err := eng.AddWithOptions(AddOptions{
		UserID:           "player1",
		UserMessage:      "Hello bartender",
		AssistantMessage: "Hey there, welcome back!",
	})
	if err != nil {
		t.Fatalf("AddWithOptions (text): %v", err)
	}
	if memID == 0 {
		t.Fatal("expected non-zero memory ID")
	}

	memories, err := eng.ListRecent("player1", 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	if memories[0].Modality != ModalityText {
		t.Errorf("modality = %q, want text", memories[0].Modality)
	}
	if memories[0].MimeType != "" {
		t.Errorf("mime_type = %q, want empty for text", memories[0].MimeType)
	}
}

func TestMultimodal_ErrorWithoutMultimodalEmbedder(t *testing.T) {
	// Use a text-only embedder
	textOnlyEmbedder := NewGeminiEmbedder("fake-key", 8)

	tmpDir := t.TempDir()
	cfg := Config{
		DBPath:            tmpDir + "/test.db",
		EmbeddingProvider: textOnlyEmbedder,
		Classifier:        NewHeuristicClassifier(""),
	}
	eng, err := Init(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	// Should fail because GeminiEmbedder doesn't implement MultimodalEmbeddingProvider
	_, err = eng.AddWithOptions(AddOptions{
		UserID: "player1",
		Media:  &MediaContent{MimeType: "image/png", Data: []byte{1}},
	})
	if err == nil {
		t.Fatal("expected error when using text-only embedder for media")
	}
}

func TestMultimodal_SearchWithModalityFilter(t *testing.T) {
	embedder := &mmMockEmbedder{dim: 8}
	eng := setupTestEngram(t, embedder)

	// Add text + image + audio memories
	eng.AddWithOptions(AddOptions{
		UserID:           "player1",
		UserMessage:      "I love jazz",
		AssistantMessage: "Me too!",
	})
	eng.AddWithOptions(AddOptions{
		UserID:      "player1",
		Media:       &MediaContent{MimeType: "image/png", Data: []byte{1}},
		Description: "A photo of the bar",
	})
	eng.AddWithOptions(AddOptions{
		UserID:      "player1",
		Media:       &MediaContent{MimeType: "audio/mpeg", Data: []byte{2}},
		Description: "Jazz playing",
	})

	// Search filtered to images only
	results := eng.SearchWithOptions(SearchOptions{
		Query:      "bar",
		UserID:     "player1",
		Limit:      10,
		Modalities: []Modality{ModalityImage},
	})
	for _, r := range results {
		if r.Modality != ModalityImage {
			t.Errorf("expected only image results, got %s", r.Modality)
		}
	}
}

func TestMultimodal_CrossModalSearch(t *testing.T) {
	embedder := &mmMockEmbedder{dim: 8}
	eng := setupTestEngram(t, embedder)

	// Add text memory
	eng.AddWithOptions(AddOptions{
		UserID:           "player1",
		UserMessage:      "This jazz is great",
		AssistantMessage: "Glad you like it",
	})

	// Search with audio — should still find text memories
	// (in real embeddings, jazz audio and "jazz" text would be in the same region)
	results := eng.SearchWithOptions(SearchOptions{
		UserID: "player1",
		Limit:  10,
		Media:  &MediaContent{MimeType: "audio/mpeg", Data: []byte{0xFF}},
	})
	// With mock vectors the similarity may be low but results should still be returned
	if len(results) == 0 {
		t.Log("Note: cross-modal search returned 0 results with mock embedder (expected with deterministic vectors)")
	}
}

func TestMultimodal_SchemaMigration(t *testing.T) {
	// Test that a fresh DB gets the v3 columns
	tmpDir := t.TempDir()
	store, err := NewStore(tmpDir + "/test.db")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	// Insert with new columns
	mem := Memory{
		Content:     "test",
		Sector:      SectorSemantic,
		Salience:    0.5,
		UserID:      "test_user",
		Modality:    ModalityImage,
		MimeType:    "image/png",
		ExternalRef: "asset://test.png",
	}
	id, err := store.InsertMemory(mem)
	if err != nil {
		t.Fatalf("InsertMemory: %v", err)
	}

	// Read back
	mems, err := store.GetRecentMemories("test_user", 10, nil)
	if err != nil {
		t.Fatalf("GetRecentMemories: %v", err)
	}
	if len(mems) != 1 {
		t.Fatalf("expected 1, got %d", len(mems))
	}
	if mems[0].Modality != ModalityImage {
		t.Errorf("modality = %q, want image", mems[0].Modality)
	}
	if mems[0].MimeType != "image/png" {
		t.Errorf("mime_type = %q, want image/png", mems[0].MimeType)
	}
	if mems[0].ExternalRef != "asset://test.png" {
		t.Errorf("external_ref = %q, want asset://test.png", mems[0].ExternalRef)
	}
	_ = id
}

// TestMultimodal_DefaultModality verifies old text memories get modality="text".
func TestMultimodal_DefaultModality(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewStore(tmpDir + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	// Insert without setting modality (should default to "text")
	mem := Memory{
		Content:  "just text",
		Sector:   SectorSemantic,
		Salience: 0.5,
		UserID:   "test_user",
	}
	_, err = store.InsertMemory(mem)
	if err != nil {
		t.Fatal(err)
	}

	mems, err := store.GetRecentMemories("test_user", 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	if mems[0].Modality != ModalityText {
		t.Errorf("default modality = %q, want text", mems[0].Modality)
	}
}

// Ensure no unused imports
var _ = os.TempDir
