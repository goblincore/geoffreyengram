package dualmem

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// GeminiEmbedder generates vector embeddings via the Gemini API.
// Supports gemini-embedding-001 and gemini-embedding-2 (text-embedding-005, etc).
// Implements dualmem.EmbeddingProvider.
type GeminiEmbedder struct {
	apiKey    string
	model     string
	dimension int
	client    *http.Client
}

// GeminiEmbedderOption configures a GeminiEmbedder.
type GeminiEmbedderOption func(*GeminiEmbedder)

// WithGeminiModel sets the embedding model name (default: "gemini-embedding-001").
// Use "gemini-embedding-2" for the latest model.
func WithGeminiModel(model string) GeminiEmbedderOption {
	return func(e *GeminiEmbedder) { e.model = model }
}

// NewGeminiEmbedder creates an embedding provider for Gemini embedding models.
// Default model is gemini-embedding-001; use WithGeminiModel to change.
func NewGeminiEmbedder(apiKey string, dimension int, opts ...GeminiEmbedderOption) *GeminiEmbedder {
	if dimension == 0 {
		dimension = 768
	}
	e := &GeminiEmbedder{
		apiKey:    apiKey,
		model:     "gemini-embedding-001",
		dimension: dimension,
		client:    &http.Client{Timeout: 15 * time.Second},
	}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

func (e *GeminiEmbedder) Embed(ctx context.Context, text, taskType string) ([]float32, error) {
	if e.apiKey == "" {
		return nil, fmt.Errorf("dualmem: no Gemini API key")
	}

	url := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:embedContent", e.model)

	type part struct {
		Text string `json:"text"`
	}
	type content struct {
		Parts []part `json:"parts"`
	}
	reqBody := struct {
		Content              content `json:"content"`
		TaskType             string  `json:"taskType,omitempty"`
		OutputDimensionality int     `json:"outputDimensionality,omitempty"`
	}{
		Content:              content{Parts: []part{{Text: text}}},
		TaskType:             taskType,
		OutputDimensionality: e.dimension,
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", e.apiKey)

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, newProviderUnavailableError("gemini", "embed", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		bodyText := redactCredential(string(body), e.apiKey)
		return nil, fmt.Errorf("dualmem: gemini embed %d: %s", resp.StatusCode, bodyText[:min(len(bodyText), 200)])
	}

	var geminiResp struct {
		Embedding struct {
			Values []float64 `json:"values"`
		} `json:"embedding"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&geminiResp); err != nil {
		return nil, fmt.Errorf("dualmem: decode: %w", err)
	}
	if len(geminiResp.Embedding.Values) == 0 {
		return nil, fmt.Errorf("dualmem: empty embedding")
	}

	vec := make([]float32, len(geminiResp.Embedding.Values))
	for i, v := range geminiResp.Embedding.Values {
		vec[i] = float32(v)
	}
	return vec, nil
}

func (e *GeminiEmbedder) Dimension() int { return e.dimension }

func (e *GeminiEmbedder) ModelName() string { return e.model }
