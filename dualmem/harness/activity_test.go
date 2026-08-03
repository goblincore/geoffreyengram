package harness

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestJSONLActivitySinkAppendsMetadataOnlyWithPrivateMode(t *testing.T) {
	root := t.TempDir()
	sink := &JSONLActivitySink{Root: root}
	activities := []Activity{
		{
			Timestamp: time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC),
			Harness:   "future-harness",
			Namespace: "claude:repo",
			SessionID: "../../session id",
			Kind:      EventFileWrite,
			Files:     []string{"/repo/runtime.go"},
		},
		{
			Timestamp: time.Date(2026, 7, 31, 12, 1, 0, 0, time.UTC),
			Harness:   "future-harness",
			Namespace: "claude:repo",
			SessionID: "../../session id",
			Kind:      EventSessionEnd,
			Artifact:  "/cache/session.jsonl",
		},
	}
	for _, activity := range activities {
		if err := sink.Record(context.Background(), activity); err != nil {
			t.Fatal(err)
		}
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].IsDir() {
		t.Fatalf("activity directory entries = %#v, want one file", entries)
	}
	if strings.Contains(entries[0].Name(), "..") || strings.ContainsAny(entries[0].Name(), `/\\: `) {
		t.Fatalf("unsafe activity filename %q", entries[0].Name())
	}
	path := filepath.Join(root, entries[0].Name())
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("activity mode = %04o, want 0600", got)
	}

	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for i, want := range activities {
		if !scanner.Scan() {
			t.Fatalf("missing JSONL record %d: %v", i, scanner.Err())
		}
		var got Activity
		if err := json.Unmarshal(scanner.Bytes(), &got); err != nil {
			t.Fatalf("decode record %d: %v", i, err)
		}
		if got.Harness != want.Harness || got.Namespace != want.Namespace || got.SessionID != want.SessionID || got.Kind != want.Kind {
			t.Fatalf("record %d = %#v, want metadata from %#v", i, got, want)
		}
	}
	if scanner.Scan() {
		t.Fatalf("unexpected extra JSONL record %q", scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"prompt", "patch", "command", "environment", "secret"} {
		if strings.Contains(strings.ToLower(string(raw)), forbidden) {
			t.Fatalf("activity log contains forbidden payload category %q: %s", forbidden, raw)
		}
	}
}
