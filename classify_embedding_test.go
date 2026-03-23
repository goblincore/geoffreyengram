package engram

import (
	"context"
	"math"
	"testing"
)

// classifyMockEmbedder returns deterministic vectors for testing.
// Each sector anchor gets a distinct unit vector direction (4 sectors, no reflective).
type classifyMockEmbedder struct {
	dimension int
	calls     int
}

func (m *classifyMockEmbedder) Embed(_ context.Context, text, _ string) ([]float32, error) {
	m.calls++
	vec := make([]float32, m.dimension)

	// Generate deterministic vectors based on content.
	// Sector anchors get distinct directions; test inputs get
	// vectors close to specific sectors for predictable classification.
	switch {
	// Anchor descriptions (matched by substring for robustness)
	case contains(text, "Something happened"):
		vec[0] = 1.0 // episodic → dimension 0
	case contains(text, "A fact, preference"):
		vec[1] = 1.0 // semantic → dimension 1
	case contains(text, "How to do something"):
		vec[2] = 1.0 // procedural → dimension 2
	case contains(text, "Someone felt or expressed"):
		vec[3] = 1.0 // emotional → dimension 3

	// Test inputs: biased toward specific sectors
	case contains(text, "visited the cafe"):
		vec[0] = 0.9
		vec[1] = 0.1 // mostly episodic
	case contains(text, "likes jazz music"):
		vec[1] = 0.9
		vec[3] = 0.2 // mostly semantic
	case contains(text, "how to make coffee"):
		vec[2] = 0.85
		vec[1] = 0.15 // mostly procedural
	case contains(text, "felt really happy"):
		vec[3] = 0.95 // mostly emotional

	default:
		// Generic: slight semantic bias
		vec[1] = 0.5
	}

	// Normalize to unit vector
	var norm float64
	for _, v := range vec {
		norm += float64(v) * float64(v)
	}
	if norm > 0 {
		norm = math.Sqrt(norm)
		for i := range vec {
			vec[i] = float32(float64(vec[i]) / norm)
		}
	}

	return vec, nil
}

func (m *classifyMockEmbedder) Dimension() int { return m.dimension }

func (m *classifyMockEmbedder) ModelName() string { return "mock-classify-embedder" }

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestEmbeddingClassifier_Init(t *testing.T) {
	embedder := &classifyMockEmbedder{dimension: 8}
	classifier, err := NewEmbeddingClassifier(context.Background(), embedder)
	if err != nil {
		t.Fatalf("NewEmbeddingClassifier: %v", err)
	}

	// Should have made 4 embed calls (one per sector, no reflective)
	if embedder.calls != 4 {
		t.Fatalf("expected 4 init calls, got %d", embedder.calls)
	}

	// Should have 4 anchors
	if len(classifier.anchors) != 4 {
		t.Fatalf("expected 4 anchors, got %d", len(classifier.anchors))
	}

	// Reflective should NOT be present
	if _, ok := classifier.anchors[SectorReflective]; ok {
		t.Fatal("reflective should not be in anchor set")
	}
}

func TestEmbeddingClassifier_ClassifyFromEmbedding(t *testing.T) {
	embedder := &classifyMockEmbedder{dimension: 8}
	classifier, err := NewEmbeddingClassifier(context.Background(), embedder)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		text string
		want Sector
	}{
		{"episodic", "visited the cafe", SectorEpisodic},
		{"semantic", "likes jazz music", SectorSemantic},
		{"procedural", "how to make coffee", SectorProcedural},
		{"emotional", "felt really happy", SectorEmotional},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vec, err := embedder.Embed(context.Background(), tt.text, "RETRIEVAL_DOCUMENT")
			if err != nil {
				t.Fatal(err)
			}

			got := classifier.ClassifyFromEmbedding(vec)
			if got != tt.want {
				t.Errorf("ClassifyFromEmbedding(%q) = %s, want %s", tt.text, got, tt.want)
			}
		})
	}
}

func TestEmbeddingClassifier_Classify(t *testing.T) {
	embedder := &classifyMockEmbedder{dimension: 8}
	classifier, err := NewEmbeddingClassifier(context.Background(), embedder)
	if err != nil {
		t.Fatal(err)
	}

	callsBefore := embedder.calls
	sector := classifier.Classify("visited the cafe")
	if sector != SectorEpisodic {
		t.Errorf("Classify('visited the cafe') = %s, want episodic", sector)
	}
	if embedder.calls != callsBefore+1 {
		t.Errorf("expected 1 additional embed call, got %d", embedder.calls-callsBefore)
	}
}

func TestEmbeddingClassifier_FallbackOnUnknownInput(t *testing.T) {
	embedder := &classifyMockEmbedder{dimension: 8}
	classifier, err := NewEmbeddingClassifier(context.Background(), embedder)
	if err != nil {
		t.Fatal(err)
	}

	// Unknown input gets a generic vector biased toward semantic
	sector := classifier.Classify("something completely random")
	if sector != SectorSemantic {
		t.Errorf("expected semantic fallback for unknown input, got %s", sector)
	}
}

// Verify interface compliance
var _ SectorClassifier = (*EmbeddingClassifier)(nil)
