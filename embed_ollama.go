package engram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// OllamaEmbedder generates vector embeddings via a local Ollama server.
// Implements EmbeddingProvider. No API key required.
type OllamaEmbedder struct {
	host      string
	model     string
	dimension int
	client    *http.Client
}

// OllamaOption configures an OllamaEmbedder.
type OllamaOption func(*OllamaEmbedder)

// WithOllamaHost sets the Ollama server URL (default: http://localhost:11434).
func WithOllamaHost(host string) OllamaOption {
	return func(e *OllamaEmbedder) { e.host = host }
}

// NewOllamaEmbedder creates an embedding provider for a local Ollama instance.
// The model must be already pulled (e.g., "nomic-embed-text", "all-minilm").
// Dimension should match the model's output dimension.
func NewOllamaEmbedder(model string, dimension int, opts ...OllamaOption) *OllamaEmbedder {
	e := &OllamaEmbedder{
		host:      "http://localhost:11434",
		model:     model,
		dimension: dimension,
		client:    &http.Client{Timeout: 30 * time.Second},
	}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// Embed generates a vector for the given text.
// The taskType parameter is accepted for interface compatibility but ignored
// (Ollama embeddings do not have task-specific modes).
func (e *OllamaEmbedder) Embed(ctx context.Context, text, taskType string) ([]float32, error) {
	url := e.host + "/api/embed"

	reqBody := ollamaEmbedRequest{
		Model: e.model,
		Input: text,
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("ollama embed %d: %s", resp.StatusCode, string(body[:min(len(body), 200)]))
	}

	var ollamaResp ollamaEmbedResponse
	if err := json.NewDecoder(resp.Body).Decode(&ollamaResp); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}

	if len(ollamaResp.Embeddings) == 0 || len(ollamaResp.Embeddings[0]) == 0 {
		return nil, fmt.Errorf("empty embedding returned")
	}

	// Convert float64 response to float32 for compact storage
	vec := make([]float32, len(ollamaResp.Embeddings[0]))
	for i, v := range ollamaResp.Embeddings[0] {
		vec[i] = float32(v)
	}
	return vec, nil
}

// Dimension returns the configured embedding dimension.
func (e *OllamaEmbedder) Dimension() int {
	return e.dimension
}

// --- Ollama Embed API types ---

type ollamaEmbedRequest struct {
	Model string `json:"model"`
	Input string `json:"input"`
}

type ollamaEmbedResponse struct {
	Embeddings [][]float64 `json:"embeddings"`
}
