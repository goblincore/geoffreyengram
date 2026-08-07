package dualmem

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestGeminiClassifierUsesHeaderAuthentication(t *testing.T) {
	const secret = "fixture-secret-never-real"
	classifier := NewGeminiClassifier(secret, CodingSectors())
	classifier.client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if got := req.URL.Query().Get("key"); got != "" {
			t.Fatalf("credential appeared in URL query: %q", got)
		}
		if got := req.Header.Get("x-goog-api-key"); got != secret {
			t.Fatalf("x-goog-api-key = %q, want sentinel", got)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"candidates":[{"content":{"parts":[{"text":"decision"}]}}]}`)),
			Header:     make(http.Header),
		}, nil
	})}

	if got := classifier.Classify("example"); got != "decision" {
		t.Fatalf("classification = %q, want %q", got, "decision")
	}
}

func TestGeminiClassifierTransportErrorDoesNotExposeCredential(t *testing.T) {
	const secret = "fixture-secret-never-real"
	classifier := NewGeminiClassifier(secret, CodingSectors())
	classifier.client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("provider reflected %s", secret)
	})}

	_, err := classifier.llmClassify(context.Background(), "example")
	if err == nil {
		t.Fatal("llmClassify() error = nil, want transport error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("transport error exposed credential: %q", err)
	}
	if !strings.Contains(err.Error(), "network access is required; retry with network permission") {
		t.Fatalf("transport error missing network guidance: %q", err)
	}
}

func TestGeminiClassifierHTTPErrorBoundsAndRedactsCredential(t *testing.T) {
	const secret = "fixture-secret-never-real"
	body := strings.Repeat("x", 195) + secret + strings.Repeat("y", 1000)
	reader := strings.NewReader(body)
	classifier := NewGeminiClassifier(secret, CodingSectors())
	classifier.client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusInternalServerError,
			Body:       io.NopCloser(reader),
			Header:     make(http.Header),
		}, nil
	})}

	_, err := classifier.llmClassify(context.Background(), "example")
	if err == nil {
		t.Fatal("llmClassify() error = nil, want HTTP error")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), secret[:5]) {
		t.Fatalf("HTTP error exposed credential: %q", err)
	}
	if want := len(body) - 200; reader.Len() != want {
		t.Fatalf("provider error body remaining = %d, want %d", reader.Len(), want)
	}
}
