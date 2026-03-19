package engram

import (
	"context"
	"fmt"
)

// Sector anchor descriptions for zero-shot embedding classification.
// These are embedded once at init time and cached permanently.
//
// Note: SectorReflective is NOT included — reflective memories are synthesized
// by Reflect(), not classified from incoming content. The classifier only handles
// the 4 input-facing sectors.
var sectorAnchorDescriptions = map[Sector]string{
	SectorEpisodic:   "Something happened at a specific time. A visit, encounter, meeting, or event that someone experienced or witnessed.",
	SectorSemantic:   "A fact, preference, or stable truth about someone. What they like, who they are, biographical details, knowledge.",
	SectorProcedural: "How to do something. A technique, method, process, recipe, instructions, or step-by-step skill.",
	SectorEmotional:  "Someone felt or expressed an emotion. They seemed happy, sad, angry, nervous, excited, or moved during an interaction.",
}

// EmbeddingClassifier determines sector by cosine similarity against pre-embedded
// anchor descriptions. Zero extra API calls when used with ClassifyFromEmbedding
// (reuses the already-computed memory embedding). Required for multimodal memories
// where keyword heuristics can't work (images, audio).
type EmbeddingClassifier struct {
	anchors  map[Sector][]float32
	embedder EmbeddingProvider
}

// NewEmbeddingClassifier creates a classifier by embedding the 4 sector anchor
// descriptions. Requires 4 embedding API calls at init time (cached permanently).
func NewEmbeddingClassifier(ctx context.Context, embedder EmbeddingProvider) (*EmbeddingClassifier, error) {
	anchors := make(map[Sector][]float32, len(sectorAnchorDescriptions))
	for sector, desc := range sectorAnchorDescriptions {
		vec, err := embedder.Embed(ctx, desc, "RETRIEVAL_DOCUMENT")
		if err != nil {
			return nil, fmt.Errorf("engram: embed anchor %s: %w", sector, err)
		}
		anchors[sector] = vec
	}
	return &EmbeddingClassifier{anchors: anchors, embedder: embedder}, nil
}

// ClassifyFromEmbedding determines the sector for an already-computed embedding.
// Zero API calls — just cosine similarity against the 4 cached anchor vectors.
// This is the primary classification path for multimodal memories.
func (c *EmbeddingClassifier) ClassifyFromEmbedding(vec []float32) Sector {
	bestSector := SectorSemantic
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
// Embeds the text first, then classifies from the embedding.
// Note: this makes an API call — prefer ClassifyFromEmbedding when you
// already have the embedding vector.
func (c *EmbeddingClassifier) Classify(content string) Sector {
	vec, err := c.embedder.Embed(context.Background(), content, "RETRIEVAL_DOCUMENT")
	if err != nil {
		return SectorSemantic // safe fallback
	}
	return c.ClassifyFromEmbedding(vec)
}
