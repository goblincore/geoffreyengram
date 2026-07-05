package dualmem

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// TestArchiveV1_RoundTrip builds a temp store with detail memories (regular and
// checkpoint-typed) across two namespaces, archives it, and asserts the JSON
// parses and row counts match what was written.
//
// Rows are inserted directly via the SQLiteStore so the test exercises the
// archive (not engine routing/dedup) deterministically.
func TestArchiveV1_RoundTrip(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "archive_src.db")
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	nsA := "claude:project-a"
	nsB := "claude:project-b"

	// Namespace A: 3 regular memories + 1 checkpoint (Type="checkpoint").
	memA := []struct {
		text string
		typ  string
	}{
		{"auth rewrite is compliance-driven", "decision"},
		{"rate limiter uses token bucket", "decision"},
		{"cache layer is write-through", "decision"},
		{"auth refactor checkpoint", "checkpoint"},
	}
	for i, m := range memA {
		if err := store.InsertDetail(&DetailMemory{
			ID:        aid("A", i),
			Text:      m.text,
			Sector:    "decision",
			Salience:  0.9,
			Type:      m.typ,
			CreatedAt: time.Now(),
		}, []float32{float32(i), 0, 0, 0}, nsA); err != nil {
			t.Fatalf("InsertDetail nsA %d: %v", i, err)
		}
	}

	// Namespace B: 2 regular memories.
	memB := []string{"uses postgres with pgbouncer", "deploys via argo cd"}
	for i, m := range memB {
		if err := store.InsertDetail(&DetailMemory{
			ID:        aid("B", i),
			Text:      m,
			Sector:    "map",
			Salience:  0.8,
			CreatedAt: time.Now(),
		}, []float32{0, float32(i), 0, 0}, nsB); err != nil {
			t.Fatalf("InsertDetail nsB %d: %v", i, err)
		}
	}

	outDir := filepath.Join(t.TempDir(), "archive")
	res, err := ArchiveV1(dbPath, ArchiveV1Options{OutDir: outDir})
	if err != nil {
		t.Fatalf("ArchiveV1: %v", err)
	}

	// Both namespaces should be discovered and archived.
	if len(res.Namespaces) != 2 {
		t.Fatalf("expected 2 namespaces, got %d: %v", len(res.Namespaces), res.Namespaces)
	}
	if got := res.PerNSCounts[nsA]["detail_memories"]; got != 4 {
		t.Errorf("nsA detail_memories: expected 4 (3 regular + 1 checkpoint), got %d", got)
	}
	if got := res.PerNSCounts[nsB]["detail_memories"]; got != 2 {
		t.Errorf("nsB detail_memories: expected 2, got %d", got)
	}
	if res.TotalRows <= 0 {
		t.Errorf("expected total rows > 0, got %d", res.TotalRows)
	}

	// Verify the on-disk files exist and parse, with correct per-table counts.
	assertNSFile(t, outDir, nsA, "detail_memories", 4)
	assertNSFile(t, outDir, nsB, "detail_memories", 2)

	// BLOB columns (embedding) must be base64-encoded strings in the JSON.
	assertEmbeddingBase64(t, outDir, nsA)

	// The checkpoint row must round-trip its type.
	assertCheckpointType(t, outDir, nsA)
}

// TestArchiveV1_ForceOverwrite asserts that re-archiving into an existing
// non-empty dir fails without --force and succeeds with --force.
func TestArchiveV1_ForceOverwrite(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "src.db")
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()
	if err := store.InsertDetail(&DetailMemory{
		ID:        "mem-force-1",
		Text:      "hello",
		Sector:    "decision",
		Salience:  0.9,
		CreatedAt: time.Now(),
	}, []float32{1, 0, 0, 0}, "claude:test-ns"); err != nil {
		t.Fatalf("InsertDetail: %v", err)
	}

	outDir := filepath.Join(t.TempDir(), "archive")
	if _, err := ArchiveV1(dbPath, ArchiveV1Options{OutDir: outDir}); err != nil {
		t.Fatalf("first archive: %v", err)
	}

	// Second run without force must fail.
	if _, err := ArchiveV1(dbPath, ArchiveV1Options{OutDir: outDir}); err == nil {
		t.Fatal("expected error archiving into non-empty dir without --force, got nil")
	}

	// With force it must succeed.
	if _, err := ArchiveV1(dbPath, ArchiveV1Options{OutDir: outDir, Force: true}); err != nil {
		t.Fatalf("archive with --force: %v", err)
	}
}

// TestArchiveV1_EmptyStore asserts archiving a store with no namespaced rows
// succeeds and writes no namespace files (but still creates the dir).
func TestArchiveV1_EmptyStore(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "empty.db")
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	store.Close()

	outDir := filepath.Join(t.TempDir(), "archive")
	res, err := ArchiveV1(dbPath, ArchiveV1Options{OutDir: outDir})
	if err != nil {
		t.Fatalf("ArchiveV1 empty store: %v", err)
	}
	if len(res.Namespaces) != 0 {
		t.Errorf("expected 0 namespaces, got %d: %v", len(res.Namespaces), res.Namespaces)
	}
	// Global tables still reported (schema version rows exist post-migrate).
	if res.GlobalCounts["dualmem_schema_version"] < 1 {
		t.Errorf("expected dualmem_schema_version rows >= 1, got %d", res.GlobalCounts["dualmem_schema_version"])
	}
}

// TestArchiveV1_SanitizeNamespace ensures namespace values with special chars
// produce safe, distinct filenames.
func TestArchiveV1_SanitizeNamespace(t *testing.T) {
	cases := map[string]string{
		"claude:geoffreyengram": "claude_geoffreyengram",
		"claude:infra":          "claude_infra",
		"claude/a/b":            "claude_a_b",
		"":                      "unnamed",
		"../etc/passwd":         "etc_passwd",
		"ns with spaces":        "ns_with_spaces",
	}
	for in, want := range cases {
		if got := sanitizeNamespace(in); got != want {
			t.Errorf("sanitizeNamespace(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsBlobType(t *testing.T) {
	if !isBlobType("BLOB") {
		t.Error("isBlobType(BLOB) = false")
	}
	if !isBlobType("blob") {
		t.Error("isBlobType(blob) = false")
	}
	if isBlobType("TEXT") {
		t.Error("isBlobType(TEXT) = true")
	}
	if isBlobType("INTEGER") {
		t.Error("isBlobType(INTEGER) = true")
	}
}

// --- helpers ---

func aid(prefix string, i int) string { return prefix + "-" + strconv.Itoa(i) }

func assertNSFile(t *testing.T, outDir, ns, table string, want int) {
	t.Helper()
	fname := sanitizeNamespace(ns) + ".json"
	path := filepath.Join(outDir, fname)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var doc archiveDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("unmarshal %s: %v", path, err)
	}
	if doc.Format != v1ArchiveFormat {
		t.Errorf("%s: format = %q, want %q", path, doc.Format, v1ArchiveFormat)
	}
	if doc.Namespace != ns {
		t.Errorf("%s: namespace = %q, want %q", path, doc.Namespace, ns)
	}
	if doc.SchemaVersion < 1 {
		t.Errorf("%s: schema_version = %d, want >= 1", path, doc.SchemaVersion)
	}
	if doc.ArchivedAt == "" {
		t.Errorf("%s: archived_at empty", path)
	}
	if doc.SourceDB == "" {
		t.Errorf("%s: source_db empty", path)
	}
	rows, ok := doc.Tables[table]
	if !ok {
		t.Fatalf("%s: tables missing %q (have: %v)", path, table, tableNames(doc.Tables))
	}
	if len(rows) != want {
		t.Errorf("%s: %s rows = %d, want %d", path, table, len(rows), want)
	}
	// Global tables must be present in every namespace file (self-contained).
	for _, g := range []string{"dualmem_schema_version"} {
		if _, ok := doc.Tables[g]; !ok {
			t.Errorf("%s: global table %q missing", path, g)
		}
	}
}

func assertEmbeddingBase64(t *testing.T, outDir, ns string) {
	t.Helper()
	fname := sanitizeNamespace(ns) + ".json"
	path := filepath.Join(outDir, fname)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var doc archiveDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	rows := doc.Tables["detail_memories"]
	if len(rows) == 0 {
		t.Fatal("no detail_memories rows to check embedding")
	}
	for _, row := range rows {
		emb, ok := row["embedding"]
		if !ok {
			t.Fatalf("detail_memories row missing embedding column: %v", row)
		}
		s, ok := emb.(string)
		if !ok {
			t.Fatalf("embedding not a string (not base64-encoded): %T", emb)
		}
		if s == "" {
			t.Errorf("embedding is empty string; expected base64")
		}
		// Must be valid base64 (decode without error) and non-empty bytes.
		dec, err := base64.StdEncoding.DecodeString(s)
		if err != nil || len(dec) == 0 {
			t.Errorf("embedding is not valid base64: %q (err=%v)", trunc(s, 20), err)
		}
	}
}

func assertCheckpointType(t *testing.T, outDir, ns string) {
	t.Helper()
	path := filepath.Join(outDir, sanitizeNamespace(ns)+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var doc archiveDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var sawCheckpoint bool
	for _, row := range doc.Tables["detail_memories"] {
		if v, ok := row["type"].(string); ok && v == "checkpoint" {
			sawCheckpoint = true
			break
		}
	}
	if !sawCheckpoint {
		t.Errorf("expected at least one detail memory with type=%q", "checkpoint")
	}
}

func tableNames(m map[string][]map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
