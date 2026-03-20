package dualmem

import (
	"context"
	"fmt"
)

// Sector anchor descriptions for zero-shot embedding classification.
// Embedded once at init time, cached permanently.
//
// Note: "reflective" is NOT included — reflective memories are synthesized,
// not classified from input. The classifier handles only 4 input-facing sectors.
var sectorAnchorDescriptions = map[string]string{
	"episodic":   "Something happened at a specific time. A visit, encounter, meeting, or event that someone experienced or witnessed.",
	"semantic":   "A fact, preference, or stable truth about someone. What they like, who they are, biographical details, knowledge.",
	"procedural": "How to do something. A technique, method, process, recipe, instructions, or step-by-step skill.",
	"emotional":  "Someone felt or expressed an emotion. They seemed happy, sad, angry, nervous, excited, or moved during an interaction.",
}

// EmbeddingClassifier determines sector by cosine similarity against pre-embedded
// anchor descriptions. Zero extra API calls when used with ClassifyFromEmbedding
// (reuses the already-computed memory embedding).
type EmbeddingClassifier struct {
	anchors  map[string][]float32
	embedder EmbeddingProvider
}

// NewEmbeddingClassifier creates a classifier by embedding the 4 sector anchor
// descriptions. Requires 4 embedding API calls at init time (cached permanently).
func NewEmbeddingClassifier(ctx context.Context, embedder EmbeddingProvider) (*EmbeddingClassifier, error) {
	anchors := make(map[string][]float32, len(sectorAnchorDescriptions))
	for sector, desc := range sectorAnchorDescriptions {
		vec, err := embedder.Embed(ctx, desc, "RETRIEVAL_DOCUMENT")
		if err != nil {
			return nil, fmt.Errorf("dualmem: embed anchor %s: %w", sector, err)
		}
		anchors[sector] = vec
	}
	return &EmbeddingClassifier{anchors: anchors, embedder: embedder}, nil
}

// ClassifyFromEmbedding determines the sector for an already-computed embedding.
// Zero API calls — just cosine similarity against the 4 cached anchor vectors.
func (c *EmbeddingClassifier) ClassifyFromEmbedding(vec []float32) string {
	bestSector := "semantic"
	bestSim := -1.0

	for sector, anchor := range c.anchors {
		sim := CosineSimilarity(vec, anchor)
		if sim > bestSim {
			bestSim = sim
			bestSector = sector
		}
	}
	return bestSector
}

// Classify implements SectorClassifier for backward compatibility.
// Embeds text first, then classifies from the embedding.
// Note: this makes an API call — prefer ClassifyFromEmbedding when you
// already have the embedding vector.
func (c *EmbeddingClassifier) Classify(content string) string {
	vec, err := c.embedder.Embed(context.Background(), content, "RETRIEVAL_DOCUMENT")
	if err != nil {
		return "semantic" // safe fallback
	}
	return c.ClassifyFromEmbedding(vec)
}
