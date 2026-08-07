package dualmem

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestGeminiSummarizerUsesHeaderAuthentication(t *testing.T) {
	const secret = "fixture-secret-never-real"
	summarizer := NewGeminiSummarizer(secret, "gemini-2.5-flash-lite")
	summarizer.client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if got := req.URL.Query().Get("key"); got != "" {
			t.Fatalf("credential appeared in URL query: %q", got)
		}
		if got := req.Header.Get("x-goog-api-key"); got != secret {
			t.Fatalf("x-goog-api-key = %q, want sentinel", got)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"candidates":[{"content":{"parts":[{"text":"summary"}]}}]}`)),
			Header:     make(http.Header),
		}, nil
	})}

	got, err := summarizer.GenerateText(context.Background(), "example", 10)
	if err != nil {
		t.Fatal(err)
	}
	if got != "summary" {
		t.Fatalf("summary = %q, want %q", got, "summary")
	}
}

func TestGeminiSummarizerTransportErrorDoesNotExposeCredential(t *testing.T) {
	const secret = "fixture-secret-never-real"
	summarizer := NewGeminiSummarizer(secret, "gemini-2.5-flash-lite")
	summarizer.client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("provider reflected %s", secret)
	})}

	_, err := summarizer.GenerateText(context.Background(), "example", 10)
	if err == nil {
		t.Fatal("GenerateText() error = nil, want transport error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("transport error exposed credential: %q", err)
	}
	if !strings.Contains(err.Error(), "network access is required; retry with network permission") {
		t.Fatalf("transport error missing network guidance: %q", err)
	}
}

func TestGeminiSummarizerHTTPErrorBoundsAndRedactsCredential(t *testing.T) {
	const secret = "fixture-secret-never-real"
	body := strings.Repeat("x", 195) + secret + strings.Repeat("y", 1000)
	reader := strings.NewReader(body)
	summarizer := NewGeminiSummarizer(secret, "gemini-2.5-flash-lite")
	summarizer.client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusInternalServerError,
			Body:       io.NopCloser(reader),
			Header:     make(http.Header),
		}, nil
	})}

	_, err := summarizer.GenerateText(context.Background(), "example", 10)
	if err == nil {
		t.Fatal("GenerateText() error = nil, want HTTP error")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), secret[:5]) {
		t.Fatalf("HTTP error exposed credential: %q", err)
	}
	if want := len(body) - 200; reader.Len() != want {
		t.Fatalf("provider error body remaining = %d, want %d", reader.Len(), want)
	}
}
