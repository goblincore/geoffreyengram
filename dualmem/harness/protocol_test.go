package harness

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestDecodeEventV1(t *testing.T) {
	raw := `{"schema_version":"1.0","kind":"session_start","harness":"pi","cwd":"/repo","project":{"root":"","namespace":""},"session_id":"s1","files":[],"metadata":{"model":"test"}}`
	event, err := DecodeEvent(strings.NewReader(raw), 64<<10)
	if err != nil {
		t.Fatal(err)
	}
	if event.Kind != EventSessionStart || event.Harness != "pi" || event.SessionID != "s1" {
		t.Fatalf("unexpected event: %#v", event)
	}
}

func TestDecodeEventRejectsUnsupportedMajor(t *testing.T) {
	raw := `{"schema_version":"2.0","kind":"session_start","harness":"future","cwd":"/repo"}`
	_, err := DecodeEvent(strings.NewReader(raw), 64<<10)
	if !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("error = %v, want ErrUnsupportedVersion", err)
	}
}

func TestDecodeEventRejectsMalformedNumericVersion(t *testing.T) {
	raw := `{"schema_version":"1.bogus","kind":"session_start","harness":"future","cwd":"/repo"}`
	if _, err := DecodeEvent(strings.NewReader(raw), 64<<10); err == nil {
		t.Fatal("DecodeEvent accepted a non-numeric minor version")
	}
}

func TestDecodeEventAcceptsUnknownFieldsAndKinds(t *testing.T) {
	raw := `{"schema_version":"1.7","kind":"future_event","harness":"future","cwd":"/repo","future_option":{"enabled":true}}`
	event, err := DecodeEvent(strings.NewReader(raw), 64<<10)
	if err != nil {
		t.Fatal(err)
	}
	if event.Kind != EventKind("future_event") {
		t.Fatalf("kind = %q, want future_event", event.Kind)
	}
}

func TestDecodeEventRejectsMissingRequiredFields(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{name: "schema version", raw: `{"kind":"session_start","harness":"pi","cwd":"/repo"}`},
		{name: "harness", raw: `{"schema_version":"1.0","kind":"session_start","cwd":"/repo"}`},
		{name: "cwd", raw: `{"schema_version":"1.0","kind":"session_start","harness":"pi"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := DecodeEvent(strings.NewReader(tt.raw), 64<<10); err == nil {
				t.Fatal("DecodeEvent succeeded, want validation error")
			}
		})
	}
}

func TestDecodeEventEnforcesInputLimit(t *testing.T) {
	raw := `{"schema_version":"1.0","kind":"prompt","harness":"pi","cwd":"/repo"}`
	if _, err := DecodeEvent(strings.NewReader(raw), int64(len(raw))); err != nil {
		t.Fatalf("exact-limit event: %v", err)
	}
	if _, err := DecodeEvent(strings.NewReader(raw), int64(len(raw)-1)); err == nil {
		t.Fatal("oversized event succeeded")
	}
}

func TestDecodeEventEnforcesMetadataLimits(t *testing.T) {
	t.Run("entry count", func(t *testing.T) {
		metadata := make(map[string]string, 33)
		for i := 0; i < 33; i++ {
			metadata[string(rune('A'+i))] = "value"
		}
		raw, err := json.Marshal(Event{SchemaVersion: "1.0", Kind: EventPrompt, Harness: "pi", CWD: "/repo", Metadata: metadata})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := DecodeEvent(bytes.NewReader(raw), 64<<10); err == nil {
			t.Fatal("event with 33 metadata entries succeeded")
		}
	})

	t.Run("value bytes", func(t *testing.T) {
		raw, err := json.Marshal(Event{SchemaVersion: "1.0", Kind: EventPrompt, Harness: "pi", CWD: "/repo", Metadata: map[string]string{"note": strings.Repeat("x", 1025)}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := DecodeEvent(bytes.NewReader(raw), 64<<10); err == nil {
			t.Fatal("event with oversized metadata value succeeded")
		}
	})
}

func TestDecodeEventRetainsArtifactOnlyForSessionEnd(t *testing.T) {
	decode := func(t *testing.T, kind EventKind) Event {
		t.Helper()
		raw, err := json.Marshal(Event{SchemaVersion: "1.0", Kind: kind, Harness: "pi", CWD: "/repo", ArtifactRef: "/tmp/session.jsonl"})
		if err != nil {
			t.Fatal(err)
		}
		event, err := DecodeEvent(bytes.NewReader(raw), 64<<10)
		if err != nil {
			t.Fatal(err)
		}
		return event
	}

	if got := decode(t, EventPrompt).ArtifactRef; got != "" {
		t.Fatalf("prompt artifact_ref = %q, want empty", got)
	}
	if got := decode(t, EventSessionEnd).ArtifactRef; got != "/tmp/session.jsonl" {
		t.Fatalf("session_end artifact_ref = %q", got)
	}
}

func TestProtocolJSONNames(t *testing.T) {
	response := Response{
		SchemaVersion: "1.0",
		Action:        ActionInjectContext,
		Context:       "context",
		Diagnostics:   []Diagnostic{{Code: "notice", Message: "message"}},
	}
	var encoded bytes.Buffer
	if err := EncodeResponse(&encoded, response); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(encoded.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"schema_version", "action", "context", "diagnostics"} {
		if _, ok := got[key]; !ok {
			t.Fatalf("encoded response missing %q: %s", key, encoded.String())
		}
	}
	if _, ok := got["SchemaVersion"]; ok {
		t.Fatalf("encoded response used Go field name: %s", encoded.String())
	}
}

func TestNormalizePathsCleansDeduplicatesAndResolvesRelativePaths(t *testing.T) {
	got := NormalizePaths("/repo/subdir", []string{"../a.go", "/repo/a.go", "./b.go", "", "./b.go"})
	want := []string{"/repo/a.go", "/repo/subdir/b.go"}
	if len(got) != len(want) {
		t.Fatalf("NormalizePaths() = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("NormalizePaths() = %#v, want %#v", got, want)
		}
	}
}
