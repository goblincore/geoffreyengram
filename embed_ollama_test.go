package engram

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOllamaEmbedderSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("wrong content type: %s", r.Header.Get("Content-Type"))
		}

		var req ollamaEmbedRequest
		json.NewDecoder(r.Body).Decode(&req)
		if req.Model != "nomic-embed-text" {
			t.Errorf("expected nomic-embed-text, got %s", req.Model)
		}
		if req.Input != "test text" {
			t.Errorf("expected input 'test text', got %s", req.Input)
		}

		json.NewEncoder(w).Encode(ollamaEmbedResponse{
			Embeddings: [][]float64{{0.5, -0.3, 0.8}},
		})
	}))
	defer srv.Close()

	e := NewOllamaEmbedder("nomic-embed-text", 3, WithOllamaHost(srv.URL))
	vec, err := e.Embed(context.Background(), "test text", "RETRIEVAL_DOCUMENT")
	if err != nil {
		t.Fatal(err)
	}
	if len(vec) != 3 {
		t.Fatalf("expected 3-dim vector, got %d", len(vec))
	}
	if vec[0] != float32(0.5) {
		t.Errorf("expected 0.5, got %f", vec[0])
	}
	if vec[1] != float32(-0.3) {
		t.Errorf("expected -0.3, got %f", vec[1])
	}
}

func TestOllamaEmbedderHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "model not found", http.StatusNotFound)
	}))
	defer srv.Close()

	e := NewOllamaEmbedder("nonexistent-model", 768, WithOllamaHost(srv.URL))
	_, err := e.Embed(context.Background(), "test", "")
	if err == nil {
		t.Error("expected error for HTTP 404")
	}
}

func TestOllamaEmbedderEmptyResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(ollamaEmbedResponse{Embeddings: [][]float64{}})
	}))
	defer srv.Close()

	e := NewOllamaEmbedder("model", 768, WithOllamaHost(srv.URL))
	_, err := e.Embed(context.Background(), "test", "")
	if err == nil {
		t.Error("expected error for empty response")
	}
}

func TestOllamaEmbedderEmptyEmbedding(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(ollamaEmbedResponse{
			Embeddings: [][]float64{{}},
		})
	}))
	defer srv.Close()

	e := NewOllamaEmbedder("model", 768, WithOllamaHost(srv.URL))
	_, err := e.Embed(context.Background(), "test", "")
	if err == nil {
		t.Error("expected error for empty embedding values")
	}
}

func TestOllamaEmbedderDimension(t *testing.T) {
	e := NewOllamaEmbedder("nomic-embed-text", 768)
	if e.Dimension() != 768 {
		t.Errorf("expected 768, got %d", e.Dimension())
	}
}

func TestOllamaEmbedderDefaults(t *testing.T) {
	e := NewOllamaEmbedder("all-minilm", 384)
	if e.host != "http://localhost:11434" {
		t.Errorf("expected default host, got %s", e.host)
	}
	if e.model != "all-minilm" {
		t.Errorf("expected model all-minilm, got %s", e.model)
	}
	if e.dimension != 384 {
		t.Errorf("expected dimension 384, got %d", e.dimension)
	}
}

func TestOllamaEmbedderConnectionRefused(t *testing.T) {
	e := NewOllamaEmbedder("model", 768, WithOllamaHost("http://localhost:1"))
	_, err := e.Embed(context.Background(), "test", "")
	if err == nil {
		t.Error("expected connection error")
	}
}
