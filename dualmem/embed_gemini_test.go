package dualmem

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestGeminiEmbedderUsesHeaderAuthentication(t *testing.T) {
	const secret = "fixture-secret-never-real"
	embedder := NewGeminiEmbedder(secret, 2)
	embedder.client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if got := req.URL.Query().Get("key"); got != "" {
			t.Fatalf("credential appeared in URL query: %q", got)
		}
		if got := req.Header.Get("x-goog-api-key"); got != secret {
			t.Fatalf("x-goog-api-key = %q, want sentinel", got)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"embedding":{"values":[0.1,0.2]}}`)),
			Header:     make(http.Header),
		}, nil
	})}

	got, err := embedder.Embed(context.Background(), "example", "RETRIEVAL_DOCUMENT")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != 0.1 || got[1] != 0.2 {
		t.Fatalf("embedding = %v, want [0.1 0.2]", got)
	}
}

func TestGeminiEmbedderTransportErrorDoesNotExposeCredential(t *testing.T) {
	const secret = "fixture-secret-never-real"
	embedder := NewGeminiEmbedder(secret, 2)
	embedder.client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("failed request to %s", req.URL.String())
	})}

	_, err := embedder.Embed(context.Background(), "example", "RETRIEVAL_DOCUMENT")
	if err == nil {
		t.Fatal("Embed() error = nil, want transport error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("transport error exposed credential: %q", err)
	}
	if !strings.Contains(err.Error(), "network access is required; retry with network permission") {
		t.Fatalf("transport error missing network guidance: %q", err)
	}
}

func TestGeminiEmbedderHTTPErrorRedactsReflectedCredential(t *testing.T) {
	const secret = "fixture-secret-never-real"
	embedder := NewGeminiEmbedder(secret, 2)
	embedder.client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusInternalServerError,
			Body:       io.NopCloser(strings.NewReader("provider reflected " + secret)),
			Header:     make(http.Header),
		}, nil
	})}

	_, err := embedder.Embed(context.Background(), "example", "RETRIEVAL_DOCUMENT")
	if err == nil {
		t.Fatal("Embed() error = nil, want HTTP error")
	}
	if strings.Contains(err.Error(), secret) || !strings.Contains(err.Error(), "[REDACTED]") {
		t.Fatalf("HTTP error did not redact credential: %q", err)
	}
}

func TestGeminiEmbedderHTTPErrorRedactsCredentialBeforeTruncation(t *testing.T) {
	const secret = "fixture-secret-never-real"
	body := strings.Repeat("x", 195) + secret + strings.Repeat("y", 1000)
	reader := strings.NewReader(body)
	embedder := NewGeminiEmbedder(secret, 2)
	embedder.client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusInternalServerError,
			Body:       io.NopCloser(reader),
			Header:     make(http.Header),
		}, nil
	})}

	_, err := embedder.Embed(context.Background(), "example", "RETRIEVAL_DOCUMENT")
	if err == nil {
		t.Fatal("Embed() error = nil, want HTTP error")
	}
	if strings.Contains(err.Error(), secret[:5]) {
		t.Fatalf("HTTP error exposed credential prefix: %q", err)
	}
	if want := len(body) - 200; reader.Len() != want {
		t.Fatalf("provider error body remaining = %d, want %d", reader.Len(), want)
	}
}
