package dualmem

import (
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
			Body:       io.NopCloser(strings.NewReader(`{"candidates":[{"content":{"parts":[{"text":"semantic"}]}}]}`)),
			Header:     make(http.Header),
		}, nil
	})}

	if got := classifier.Classify("example"); got != classifier.sectors.Default {
		t.Fatalf("classification = %q, want default %q", got, classifier.sectors.Default)
	}
}
