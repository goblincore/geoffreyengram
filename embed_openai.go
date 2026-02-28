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

// OpenAIEmbedder generates vector embeddings via the OpenAI API.
// Implements EmbeddingProvider.
type OpenAIEmbedder struct {
	apiKey    string
	model     string
	dimension int
	baseURL   string
	client    *http.Client
}

// OpenAIOption configures an OpenAIEmbedder.
type OpenAIOption func(*OpenAIEmbedder)

// WithOpenAIModel sets the embedding model (default: text-embedding-3-small).
func WithOpenAIModel(model string) OpenAIOption {
	return func(e *OpenAIEmbedder) { e.model = model }
}

// WithOpenAIDimension sets the output embedding dimension (default: 1536).
func WithOpenAIDimension(dim int) OpenAIOption {
	return func(e *OpenAIEmbedder) { e.dimension = dim }
}

// WithOpenAIBaseURL sets the API base URL (default: https://api.openai.com).
// Useful for Azure OpenAI, proxies, or compatible APIs.
func WithOpenAIBaseURL(url string) OpenAIOption {
	return func(e *OpenAIEmbedder) { e.baseURL = url }
}

// NewOpenAIEmbedder creates an embedding provider for OpenAI's embedding models.
func NewOpenAIEmbedder(apiKey string, opts ...OpenAIOption) *OpenAIEmbedder {
	e := &OpenAIEmbedder{
		apiKey:    apiKey,
		model:     "text-embedding-3-small",
		dimension: 1536,
		baseURL:   "https://api.openai.com",
		client:    &http.Client{Timeout: 15 * time.Second},
	}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// Embed generates a vector for the given text.
// The taskType parameter is accepted for interface compatibility but ignored
// (OpenAI embeddings do not have task-specific modes).
func (e *OpenAIEmbedder) Embed(ctx context.Context, text, taskType string) ([]float32, error) {
	if e.apiKey == "" {
		return nil, fmt.Errorf("no API key")
	}

	url := e.baseURL + "/v1/embeddings"

	reqBody := openAIEmbedRequest{
		Input:      text,
		Model:      e.model,
		Dimensions: e.dimension,
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
	req.Header.Set("Authorization", "Bearer "+e.apiKey)

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("openai embed %d: %s", resp.StatusCode, string(body[:min(len(body), 200)]))
	}

	var oaiResp openAIEmbedResponse
	if err := json.NewDecoder(resp.Body).Decode(&oaiResp); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}

	if len(oaiResp.Data) == 0 || len(oaiResp.Data[0].Embedding) == 0 {
		return nil, fmt.Errorf("empty embedding returned")
	}

	// Convert float64 response to float32 for compact storage
	vec := make([]float32, len(oaiResp.Data[0].Embedding))
	for i, v := range oaiResp.Data[0].Embedding {
		vec[i] = float32(v)
	}
	return vec, nil
}

// Dimension returns the configured embedding dimension.
func (e *OpenAIEmbedder) Dimension() int {
	return e.dimension
}

// --- OpenAI Embed API types ---

type openAIEmbedRequest struct {
	Input      string `json:"input"`
	Model      string `json:"model"`
	Dimensions int    `json:"dimensions"`
}

type openAIEmbedResponse struct {
	Data []openAIEmbedData `json:"data"`
}

type openAIEmbedData struct {
	Embedding []float64 `json:"embedding"`
}
