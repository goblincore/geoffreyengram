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
// Implements dualmem.EmbeddingProvider.
type GeminiEmbedder struct {
	apiKey    string
	dimension int
	client    *http.Client
}

// NewGeminiEmbedder creates an embedding provider for gemini-embedding-001.
func NewGeminiEmbedder(apiKey string, dimension int) *GeminiEmbedder {
	if dimension == 0 {
		dimension = 768
	}
	return &GeminiEmbedder{
		apiKey:    apiKey,
		dimension: dimension,
		client:    &http.Client{Timeout: 10 * time.Second},
	}
}

func (e *GeminiEmbedder) Embed(ctx context.Context, text, taskType string) ([]float32, error) {
	if e.apiKey == "" {
		return nil, fmt.Errorf("dualmem: no Gemini API key")
	}

	url := "https://generativelanguage.googleapis.com/v1beta/models/gemini-embedding-001:embedContent?key=" + e.apiKey

	type part struct {
		Text string `json:"text"`
	}
	type content struct {
		Parts []part `json:"parts"`
	}
	reqBody := struct {
		Content              content `json:"content"`
		TaskType             string  `json:"taskType"`
		OutputDimensionality int     `json:"outputDimensionality"`
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

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("dualmem: gemini embed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("dualmem: gemini embed %d: %s", resp.StatusCode, string(body[:min(len(body), 200)]))
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
