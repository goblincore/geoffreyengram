package engram

import "context"

// EmbeddingProvider generates vector embeddings from text.
// Built-in: GeminiEmbedder. Implement this for OpenAI, Ollama, etc.
type EmbeddingProvider interface {
	Embed(ctx context.Context, text string, taskType string) ([]float32, error)
	Dimension() int
}

// SectorClassifier determines which cognitive sector a memory belongs to.
// Built-in: HeuristicClassifier (keyword matching + optional LLM fallback).
type SectorClassifier interface {
	Classify(content string) Sector
}

// EntityExtractor pulls entities from memory content for the waypoint graph.
// Built-in: DefaultEntityExtractor (brackets, quotes, capitalized phrases, known entities).
type EntityExtractor interface {
	Extract(content string) []Entity
}
