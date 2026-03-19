package engram

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// GeminiEmbedder2 generates vector embeddings via the Gemini Embedding 2 API.
// Supports text, images, and audio in a single shared embedding space.
// Implements both EmbeddingProvider and MultimodalEmbeddingProvider.
type GeminiEmbedder2 struct {
	apiKey    string
	dimension int
	baseURL   string // overridable for tests
	client    *http.Client
}

// NewGeminiEmbedder2 creates an embedding provider for gemini-embedding-2-preview.
// Default dimension is 768 if 0 is passed.
func NewGeminiEmbedder2(apiKey string, dimension int) *GeminiEmbedder2 {
	if dimension == 0 {
		dimension = 768
	}
	return &GeminiEmbedder2{
		apiKey:    apiKey,
		dimension: dimension,
		baseURL:   "https://generativelanguage.googleapis.com/v1beta/models/gemini-embedding-2-preview:embedContent",
		client:    &http.Client{Timeout: 30 * time.Second},
	}
}

// Embed generates a vector for text content.
// taskType should be "RETRIEVAL_QUERY" for search queries or "RETRIEVAL_DOCUMENT" for stored memories.
func (e *GeminiEmbedder2) Embed(ctx context.Context, text, taskType string) ([]float32, error) {
	parts := []gemini2Part{{Text: text}}
	return e.embed(ctx, parts, taskType)
}

// EmbedMedia generates a vector for image or audio content.
// The media bytes are base64-encoded and sent as inline_data.
func (e *GeminiEmbedder2) EmbedMedia(ctx context.Context, media MediaContent, taskType string) ([]float32, error) {
	if len(media.Data) == 0 {
		return nil, fmt.Errorf("engram: empty media data")
	}
	if media.MimeType == "" {
		return nil, fmt.Errorf("engram: empty mime type")
	}

	encoded := base64.StdEncoding.EncodeToString(media.Data)
	parts := []gemini2Part{
		{InlineData: &gemini2InlineData{MimeType: media.MimeType, Data: encoded}},
	}
	return e.embed(ctx, parts, taskType)
}

// SupportsModality reports whether this embedder can handle the given modality.
func (e *GeminiEmbedder2) SupportsModality(m Modality) bool {
	switch m {
	case ModalityText, ModalityImage, ModalityAudio:
		return true
	default:
		return false
	}
}

// Dimension returns the configured embedding dimension.
func (e *GeminiEmbedder2) Dimension() int { return e.dimension }

// embed is the shared implementation for text and media embedding.
func (e *GeminiEmbedder2) embed(ctx context.Context, parts []gemini2Part, taskType string) ([]float32, error) {
	if e.apiKey == "" {
		return nil, fmt.Errorf("engram: no Gemini API key")
	}

	url := e.baseURL + "?key=" + e.apiKey

	reqBody := gemini2EmbedRequest{
		Content:              gemini2Content{Parts: parts},
		TaskType:             taskType,
		OutputDimensionality: e.dimension,
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("engram: marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("engram: new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("engram: gemini embed2: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("engram: gemini embed2 %d: %s", resp.StatusCode, string(body[:min(len(body), 200)]))
	}

	var geminiResp geminiEmbedResponse // reuse existing response type — same shape
	if err := json.NewDecoder(resp.Body).Decode(&geminiResp); err != nil {
		return nil, fmt.Errorf("engram: decode: %w", err)
	}
	if len(geminiResp.Embedding.Values) == 0 {
		return nil, fmt.Errorf("engram: empty embedding returned")
	}

	vec := make([]float32, len(geminiResp.Embedding.Values))
	for i, v := range geminiResp.Embedding.Values {
		vec[i] = float32(v)
	}
	return vec, nil
}

// ModalityFromMimeType derives the Modality from a MIME type string.
func ModalityFromMimeType(mimeType string) Modality {
	switch {
	case strings.HasPrefix(mimeType, "image/"):
		return ModalityImage
	case strings.HasPrefix(mimeType, "audio/"):
		return ModalityAudio
	default:
		return ModalityText
	}
}

// --- Gemini Embedding 2 API types ---

type gemini2EmbedRequest struct {
	Content              gemini2Content `json:"content"`
	TaskType             string         `json:"taskType,omitempty"`
	OutputDimensionality int            `json:"outputDimensionality,omitempty"`
}

type gemini2Content struct {
	Parts []gemini2Part `json:"parts"`
}

type gemini2Part struct {
	Text       string             `json:"text,omitempty"`
	InlineData *gemini2InlineData `json:"inline_data,omitempty"`
}

type gemini2InlineData struct {
	MimeType string `json:"mime_type"`
	Data     string `json:"data"` // base64-encoded
}
