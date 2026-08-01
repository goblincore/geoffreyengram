//go:build legacy

package engram

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGeminiEmbedder2_Embed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request shape
		var req gemini2EmbedRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if len(req.Content.Parts) != 1 {
			t.Fatalf("expected 1 part, got %d", len(req.Content.Parts))
		}
		if req.Content.Parts[0].Text == "" {
			t.Fatal("expected text part")
		}
		if req.Content.Parts[0].InlineData != nil {
			t.Fatal("expected no inline_data for text embedding")
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"embedding": map[string]any{
				"values": []float64{0.1, 0.2, 0.3},
			},
		})
	}))
	defer server.Close()

	embedder := NewGeminiEmbedder2("test-key", 3)
	embedder.baseURL = server.URL

	vec, err := embedder.Embed(context.Background(), "hello world", "RETRIEVAL_DOCUMENT")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vec) != 3 {
		t.Fatalf("expected 3-dim vector, got %d", len(vec))
	}
	if vec[0] != 0.1 || vec[1] != 0.2 || vec[2] != 0.3 {
		t.Fatalf("unexpected values: %v", vec)
	}
}

func TestGeminiEmbedder2_EmbedMedia_Image(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req gemini2EmbedRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if len(req.Content.Parts) != 1 {
			t.Fatalf("expected 1 part, got %d", len(req.Content.Parts))
		}
		part := req.Content.Parts[0]
		if part.Text != "" {
			t.Fatal("expected no text for media embedding")
		}
		if part.InlineData == nil {
			t.Fatal("expected inline_data for media embedding")
		}
		if part.InlineData.MimeType != "image/png" {
			t.Fatalf("expected image/png, got %s", part.InlineData.MimeType)
		}
		if part.InlineData.Data == "" {
			t.Fatal("expected non-empty base64 data")
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"embedding": map[string]any{
				"values": []float64{0.4, 0.5, 0.6},
			},
		})
	}))
	defer server.Close()

	embedder := NewGeminiEmbedder2("test-key", 3)
	embedder.baseURL = server.URL

	media := MediaContent{
		MimeType: "image/png",
		Data:     []byte{0x89, 0x50, 0x4E, 0x47}, // PNG magic bytes
	}
	vec, err := embedder.EmbedMedia(context.Background(), media, "RETRIEVAL_DOCUMENT")
	if err != nil {
		t.Fatalf("EmbedMedia: %v", err)
	}
	if len(vec) != 3 {
		t.Fatalf("expected 3-dim vector, got %d", len(vec))
	}
}

func TestGeminiEmbedder2_EmbedMedia_Audio(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req gemini2EmbedRequest
		json.NewDecoder(r.Body).Decode(&req)

		part := req.Content.Parts[0]
		if part.InlineData == nil || part.InlineData.MimeType != "audio/mpeg" {
			t.Fatalf("expected audio/mpeg inline_data")
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"embedding": map[string]any{
				"values": []float64{0.7, 0.8, 0.9},
			},
		})
	}))
	defer server.Close()

	embedder := NewGeminiEmbedder2("test-key", 3)
	embedder.baseURL = server.URL

	media := MediaContent{MimeType: "audio/mpeg", Data: []byte{0xFF, 0xFB}}
	vec, err := embedder.EmbedMedia(context.Background(), media, "RETRIEVAL_DOCUMENT")
	if err != nil {
		t.Fatalf("EmbedMedia: %v", err)
	}
	if len(vec) != 3 {
		t.Fatalf("expected 3-dim, got %d", len(vec))
	}
}

func TestGeminiEmbedder2_EmbedMedia_EmptyData(t *testing.T) {
	embedder := NewGeminiEmbedder2("test-key", 3)
	_, err := embedder.EmbedMedia(context.Background(), MediaContent{MimeType: "image/png", Data: nil}, "RETRIEVAL_DOCUMENT")
	if err == nil {
		t.Fatal("expected error for empty data")
	}
}

func TestGeminiEmbedder2_EmbedMedia_EmptyMimeType(t *testing.T) {
	embedder := NewGeminiEmbedder2("test-key", 3)
	_, err := embedder.EmbedMedia(context.Background(), MediaContent{MimeType: "", Data: []byte{1}}, "RETRIEVAL_DOCUMENT")
	if err == nil {
		t.Fatal("expected error for empty mime type")
	}
}

func TestGeminiEmbedder2_NoAPIKey(t *testing.T) {
	embedder := NewGeminiEmbedder2("", 3)
	_, err := embedder.Embed(context.Background(), "hello", "RETRIEVAL_DOCUMENT")
	if err == nil {
		t.Fatal("expected error for empty API key")
	}
}

func TestGeminiEmbedder2_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error": "bad request"}`))
	}))
	defer server.Close()

	embedder := NewGeminiEmbedder2("test-key", 3)
	embedder.baseURL = server.URL

	_, err := embedder.Embed(context.Background(), "hello", "RETRIEVAL_DOCUMENT")
	if err == nil {
		t.Fatal("expected error for API error")
	}
}

func TestGeminiEmbedder2_SupportsModality(t *testing.T) {
	embedder := NewGeminiEmbedder2("key", 768)

	tests := []struct {
		modality Modality
		want     bool
	}{
		{ModalityText, true},
		{ModalityImage, true},
		{ModalityAudio, true},
		{Modality("video"), false},
		{Modality(""), false},
	}
	for _, tt := range tests {
		if got := embedder.SupportsModality(tt.modality); got != tt.want {
			t.Errorf("SupportsModality(%q) = %v, want %v", tt.modality, got, tt.want)
		}
	}
}

func TestGeminiEmbedder2_DefaultDimension(t *testing.T) {
	embedder := NewGeminiEmbedder2("key", 0)
	if embedder.Dimension() != 768 {
		t.Fatalf("expected default 768, got %d", embedder.Dimension())
	}
}

func TestModalityFromMimeType(t *testing.T) {
	tests := []struct {
		mime string
		want Modality
	}{
		{"image/png", ModalityImage},
		{"image/jpeg", ModalityImage},
		{"audio/mpeg", ModalityAudio},
		{"audio/wav", ModalityAudio},
		{"text/plain", ModalityText},
		{"application/json", ModalityText},
		{"", ModalityText},
	}
	for _, tt := range tests {
		if got := ModalityFromMimeType(tt.mime); got != tt.want {
			t.Errorf("ModalityFromMimeType(%q) = %v, want %v", tt.mime, got, tt.want)
		}
	}
}

// Verify interface compliance at compile time.
var _ EmbeddingProvider = (*GeminiEmbedder2)(nil)
var _ MultimodalEmbeddingProvider = (*GeminiEmbedder2)(nil)
