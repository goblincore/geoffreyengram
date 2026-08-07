package dualmem

import (
	"fmt"
	"strings"
)

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
