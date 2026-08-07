package dualmem

import (
	"fmt"
	"io"
	"strings"
)

const maxProviderErrorBodyBytes = 200

type providerUnavailableError struct {
	provider  string
	operation string
	cause     error
}

func (e *providerUnavailableError) Error() string {
	return fmt.Sprintf("dualmem: %s %s unavailable: network access is required; retry with network permission", e.provider, e.operation)
}

func (e *providerUnavailableError) Unwrap() error { return e.cause }

func newProviderUnavailableError(provider, operation string, cause error) error {
	return &providerUnavailableError{provider: provider, operation: operation, cause: cause}
}

func redactCredential(text, credential string) string {
	if credential == "" {
		return text
	}
	return strings.ReplaceAll(text, credential, "[REDACTED]")
}

func providerErrorBody(body io.Reader, credential string) string {
	data, _ := io.ReadAll(io.LimitReader(body, maxProviderErrorBodyBytes))
	text := redactCredential(string(data), credential)
	if credential == "" {
		return text
	}

	maxPrefixLen := len(credential) - 1
	if len(text) < maxPrefixLen {
		maxPrefixLen = len(text)
	}
	for prefixLen := maxPrefixLen; prefixLen > 0; prefixLen-- {
		prefix := credential[:prefixLen]
		if strings.HasSuffix(text, prefix) {
			return strings.TrimSuffix(text, prefix) + "[REDACTED]"
		}
	}
	return text
}
