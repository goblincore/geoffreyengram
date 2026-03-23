package dualmem

import (
	"context"
	"fmt"
)

// EmbeddingClassifier determines sector by cosine similarity against pre-embedded
// anchor descriptions. Zero extra API calls when used with ClassifyFromEmbedding
// (reuses the already-computed memory embedding).
type EmbeddingClassifier struct {
	anchors        map[string][]float32
	embedder       EmbeddingProvider
	defaultSector  string
}

// NewEmbeddingClassifier creates a classifier by embedding the sector anchor
// descriptions from the provided SectorConfig. Requires len(anchors) embedding
// API calls at init time (cached permanently).
func NewEmbeddingClassifier(ctx context.Context, embedder EmbeddingProvider, sectors *SectorConfig) (*EmbeddingClassifier, error) {
	anchors := make(map[string][]float32, len(sectors.Anchors))
	for sector, desc := range sectors.Anchors {
		vec, err := embedder.Embed(ctx, desc, "RETRIEVAL_DOCUMENT")
		if err != nil {
			return nil, fmt.Errorf("dualmem: embed anchor %s: %w", sector, err)
		}
		anchors[sector] = vec
	}
	defaultSector := sectors.Default
	if defaultSector == "" {
		defaultSector = "semantic"
	}
	return &EmbeddingClassifier{anchors: anchors, embedder: embedder, defaultSector: defaultSector}, nil
}

// ClassifyFromEmbedding determines the sector for an already-computed embedding.
// Zero API calls — just cosine similarity against the cached anchor vectors.
func (c *EmbeddingClassifier) ClassifyFromEmbedding(vec []float32) string {
	bestSector := c.defaultSector
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
		return c.defaultSector
	}
	return c.ClassifyFromEmbedding(vec)
}
