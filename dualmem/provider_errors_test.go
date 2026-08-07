package dualmem

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestProviderUnavailableErrorIsSafeAndUnwraps(t *testing.T) {
	cause := errors.New("dial tcp: lookup provider.invalid: no such host")
	err := newProviderUnavailableError("gemini", "embed", cause)
	if !errors.Is(err, cause) {
		t.Fatal("provider error must preserve its cause")
	}
	const want = "dualmem: gemini embed unavailable: network access is required; retry with network permission"
	if got := err.Error(); got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
}

func TestRedactCredential(t *testing.T) {
	const secret = "fixture-secret-never-real"
	got := redactCredential(fmt.Sprintf("provider reflected %s twice: %s", secret, secret), secret)
	if strings.Contains(got, secret) || strings.Count(got, "[REDACTED]") != 2 {
		t.Fatalf("redaction failed: %q", got)
	}
}

func TestProviderErrorBodyIsBoundedAndRedactsTrailingCredentialPrefix(t *testing.T) {
	const secret = "fixture-secret-never-real"
	body := strings.Repeat("x", 195) + secret + strings.Repeat("y", 1000)
	reader := strings.NewReader(body)

	got := providerErrorBody(reader, secret)

	if strings.Contains(got, secret) || strings.Contains(got, secret[:5]) {
		t.Fatalf("provider error body exposed credential: %q", got)
	}
	if !strings.Contains(got, "[REDACTED]") {
		t.Fatalf("provider error body did not redact trailing credential prefix: %q", got)
	}
	if want := len(body) - 200; reader.Len() != want {
		t.Fatalf("provider error body remaining = %d, want %d", reader.Len(), want)
	}
}
