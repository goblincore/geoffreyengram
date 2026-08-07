package dualmem

import (
	"context"
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
