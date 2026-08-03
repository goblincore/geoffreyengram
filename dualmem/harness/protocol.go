// Package harness defines the harness-neutral lifecycle protocol used by
// DualMem integrations.
package harness

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

type EventKind string

const (
	EventSessionStart EventKind = "session_start"
	EventPrompt       EventKind = "prompt"
	EventFileRead     EventKind = "file_read"
	EventFileWrite    EventKind = "file_write"
	EventSessionEnd   EventKind = "session_end"
)

type ProjectRef struct {
	Root      string `json:"root,omitempty"`
	Namespace string `json:"namespace,omitempty"`
}

type ToolRef struct {
	Name  string `json:"name,omitempty"`
	Phase string `json:"phase,omitempty"`
}

type Event struct {
	SchemaVersion string            `json:"schema_version"`
	Kind          EventKind         `json:"kind"`
	Harness       string            `json:"harness"`
	CWD           string            `json:"cwd"`
	Project       ProjectRef        `json:"project,omitempty"`
	SessionID     string            `json:"session_id,omitempty"`
	Prompt        string            `json:"prompt,omitempty"`
	Files         []string          `json:"files,omitempty"`
	ArtifactRef   string            `json:"artifact_ref,omitempty"`
	Tool          ToolRef           `json:"tool,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"`
}

type Action string

const (
	ActionInjectContext Action = "inject_context"
	ActionRecorded      Action = "recorded"
	ActionNone          Action = "none"
)

type Diagnostic struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type Response struct {
	SchemaVersion string       `json:"schema_version"`
	Action        Action       `json:"action"`
	Context       string       `json:"context,omitempty"`
	Diagnostics   []Diagnostic `json:"diagnostics,omitempty"`
}

var ErrUnsupportedVersion = errors.New("unsupported event schema version")

// DecodeEvent reads and validates one DualMem Event envelope. Unknown JSON
// fields and event kinds are deliberately accepted for forward compatibility.
func DecodeEvent(r io.Reader, maxBytes int64) (Event, error) {
	var event Event
	if maxBytes < 0 {
		return event, fmt.Errorf("event size limit must not be negative")
	}

	raw, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if err != nil {
		return event, fmt.Errorf("read event: %w", err)
	}
	if int64(len(raw)) > maxBytes {
		return event, fmt.Errorf("event exceeds %d-byte limit", maxBytes)
	}
	if err := json.Unmarshal(raw, &event); err != nil {
		return event, fmt.Errorf("decode event: %w", err)
	}
	if event.SchemaVersion == "" {
		return event, fmt.Errorf("event schema_version is required")
	}
	major, minor, found := strings.Cut(event.SchemaVersion, ".")
	if !found || !decimalVersionComponent(major) || !decimalVersionComponent(minor) {
		return event, fmt.Errorf("event schema_version must use numeric MAJOR.MINOR form")
	}
	if major != "1" {
		return event, fmt.Errorf("%w: %q", ErrUnsupportedVersion, event.SchemaVersion)
	}
	if event.Harness == "" {
		return event, fmt.Errorf("event harness is required")
	}
	if event.CWD == "" {
		return event, fmt.Errorf("event cwd is required")
	}
	if len(event.Metadata) > 32 {
		return event, fmt.Errorf("event metadata exceeds 32 entries")
	}
	for key, value := range event.Metadata {
		if len(value) > 1024 {
			return event, fmt.Errorf("event metadata value for %q exceeds 1024 bytes", key)
		}
	}
	if event.Kind != EventSessionEnd {
		event.ArtifactRef = ""
	}

	return event, nil
}

func decimalVersionComponent(component string) bool {
	if component == "" {
		return false
	}
	for _, character := range component {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func EncodeResponse(w io.Writer, response Response) error {
	return json.NewEncoder(w).Encode(response)
}

// NormalizePaths cleans paths, resolves relative paths against cwd, removes
// empty entries, and deduplicates while preserving first-seen order.
func NormalizePaths(cwd string, paths []string) []string {
	normalized := make([]string, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		if path == "" {
			continue
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(cwd, path)
		}
		path = filepath.Clean(path)
		if _, exists := seen[path]; exists {
			continue
		}
		seen[path] = struct{}{}
		normalized = append(normalized, path)
	}
	return normalized
}
