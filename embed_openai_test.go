package engram

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOpenAIEmbedderSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("wrong auth header: %s", r.Header.Get("Authorization"))
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("wrong content type: %s", r.Header.Get("Content-Type"))
		}

		var req openAIEmbedRequest
		json.NewDecoder(r.Body).Decode(&req)
		if req.Model != "text-embedding-3-small" {
			t.Errorf("expected model text-embedding-3-small, got %s", req.Model)
		}
		if req.Input != "test text" {
			t.Errorf("expected input 'test text', got %s", req.Input)
		}

		json.NewEncoder(w).Encode(openAIEmbedResponse{
			Data: []openAIEmbedData{
				{Embedding: []float64{0.1, 0.2, 0.3}},
			},
		})
	}))
	defer srv.Close()

	e := NewOpenAIEmbedder("test-key", WithOpenAIBaseURL(srv.URL), WithOpenAIDimension(3))
	vec, err := e.Embed(context.Background(), "test text", "RETRIEVAL_QUERY")
	if err != nil {
		t.Fatal(err)
	}
	if len(vec) != 3 {
		t.Fatalf("expected 3-dim vector, got %d", len(vec))
	}
	if vec[0] != float32(0.1) {
		t.Errorf("expected 0.1, got %f", vec[0])
	}
	if vec[2] != float32(0.3) {
		t.Errorf("expected 0.3, got %f", vec[2])
	}
}

func TestOpenAIEmbedderEmptyKey(t *testing.T) {
	e := NewOpenAIEmbedder("")
	_, err := e.Embed(context.Background(), "test", "")
	if err == nil {
		t.Error("expected error for empty API key")
	}
}

func TestOpenAIEmbedderHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"rate limited"}`, http.StatusTooManyRequests)
	}))
	defer srv.Close()

	e := NewOpenAIEmbedder("test-key", WithOpenAIBaseURL(srv.URL))
	_, err := e.Embed(context.Background(), "test", "")
	if err == nil {
		t.Error("expected error for HTTP 429")
	}
}

func TestOpenAIEmbedderEmptyResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(openAIEmbedResponse{Data: []openAIEmbedData{}})
	}))
	defer srv.Close()

	e := NewOpenAIEmbedder("test-key", WithOpenAIBaseURL(srv.URL))
	_, err := e.Embed(context.Background(), "test", "")
	if err == nil {
		t.Error("expected error for empty response")
	}
}

func TestOpenAIEmbedderEmptyEmbedding(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(openAIEmbedResponse{
			Data: []openAIEmbedData{{Embedding: []float64{}}},
		})
	}))
	defer srv.Close()

	e := NewOpenAIEmbedder("test-key", WithOpenAIBaseURL(srv.URL))
	_, err := e.Embed(context.Background(), "test", "")
	if err == nil {
		t.Error("expected error for empty embedding values")
	}
}

func TestOpenAIEmbedderDimension(t *testing.T) {
	e := NewOpenAIEmbedder("key", WithOpenAIDimension(768))
	if e.Dimension() != 768 {
		t.Errorf("expected 768, got %d", e.Dimension())
	}
}

func TestOpenAIEmbedderDefaults(t *testing.T) {
	e := NewOpenAIEmbedder("key")
	if e.model != "text-embedding-3-small" {
		t.Errorf("expected default model text-embedding-3-small, got %s", e.model)
	}
	if e.dimension != 1536 {
		t.Errorf("expected default dimension 1536, got %d", e.dimension)
	}
	if e.baseURL != "https://api.openai.com" {
		t.Errorf("expected default base URL, got %s", e.baseURL)
	}
}

func TestOpenAIEmbedderCustomModel(t *testing.T) {
	e := NewOpenAIEmbedder("key", WithOpenAIModel("text-embedding-3-large"))
	if e.model != "text-embedding-3-large" {
		t.Errorf("expected text-embedding-3-large, got %s", e.model)
	}
}
