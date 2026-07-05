package main

import (
	"context"
	"math"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goblincore/geoffreyengram/dualmem"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// mcpMockEmbedder is a deterministic, API-free embedder for handler tests. It
// mirrors dualmem's internal mockEmbedder (unexported, so redefined here):
// hash bytes across dimensions and normalize. Same text → same vector.
type mcpMockEmbedder struct{ dim int }

func (m *mcpMockEmbedder) Embed(_ context.Context, text string, _ string) ([]float32, error) {
	vec := make([]float32, m.dim)
	for i, b := range []byte(text) {
		vec[i%m.dim] += float32(b) / 256.0
	}
	var norm float64
	for _, v := range vec {
		norm += float64(v) * float64(v)
	}
	if norm > 0 {
		inv := float32(1.0 / math.Sqrt(norm))
		for i := range vec {
			vec[i] *= inv
		}
	}
	return vec, nil
}

func (m *mcpMockEmbedder) Dimension() int   { return m.dim }
func (m *mcpMockEmbedder) ModelName() string { return "mock" }

// newTestEngine builds an Engine on a temp SQLite DB with the mock embedder.
func newTestEngine(t *testing.T) *dualmem.Engine {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "mcp.db")
	engine, err := dualmem.New(dualmem.Config{
		SQLitePath:        dbPath,
		EmbeddingProvider: &mcpMockEmbedder{dim: 64},
	})
	if err != nil {
		t.Fatalf("dualmem.New: %v", err)
	}
	t.Cleanup(func() { engine.Close() })
	return engine
}

func seedFact(t *testing.T, engine *dualmem.Engine, ns, kind, source, text string, files ...string) *dualmem.Fact {
	t.Helper()
	f, err := engine.AddFact(context.Background(), dualmem.Fact{
		Namespace: ns,
		Kind:      kind,
		Source:    source,
		Text:      text,
		Files:     files,
	})
	if err != nil {
		t.Fatalf("AddFact: %v", err)
	}
	return f
}

func resultText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if res == nil {
		t.Fatal("nil CallToolResult")
	}
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String()
}

// --- recall ---

func TestRecallHandler_HappyPath(t *testing.T) {
	engine := newTestEngine(t)
	ns := "repo-x"
	seedFact(t, engine, ns, dualmem.FactKindDecision, dualmem.FactSourceVerified,
		"Chose SQLite over Postgres for the CLI to keep zero-setup.", "store_sqlite.go")

	h := recallHandler(engine, ns, "test-session")
	res, _, err := h(context.Background(), &mcp.CallToolRequest{}, recallInput{
		Query: "database storage choice",
		Limit: 5,
	})
	if err != nil {
		t.Fatalf("recall handler: %v", err)
	}
	text := resultText(t, res)
	if !strings.Contains(text, "Recalled 1 fact") {
		t.Fatalf("expected recall count, got: %s", text)
	}
	if !strings.Contains(text, "[decision]") {
		t.Fatalf("expected kind label, got: %s", text)
	}
	if !strings.Contains(text, "SQLite") {
		t.Fatalf("expected fact text, got: %s", text)
	}
	if !strings.Contains(text, "source=verified") {
		t.Fatalf("expected provenance source, got: %s", text)
	}
	if !strings.Contains(text, "Files: store_sqlite.go") {
		t.Fatalf("expected file list, got: %s", text)
	}
}

func TestRecallHandler_EmptyResults(t *testing.T) {
	engine := newTestEngine(t)
	h := recallHandler(engine, "empty-ns", "test-session")
	res, _, err := h(context.Background(), &mcp.CallToolRequest{}, recallInput{
		Query: "anything at all",
	})
	if err != nil {
		t.Fatalf("recall handler: %v", err)
	}
	text := resultText(t, res)
	if !strings.Contains(text, "No matching facts found") {
		t.Fatalf("expected empty message, got: %s", text)
	}
}

func TestRecallHandler_MissingQuery(t *testing.T) {
	engine := newTestEngine(t)
	h := recallHandler(engine, "repo-x", "test-session")
	if _, _, err := h(context.Background(), &mcp.CallToolRequest{}, recallInput{}); err == nil {
		t.Fatal("expected error for missing query")
	}
}

func TestRecallHandler_KindFilter(t *testing.T) {
	engine := newTestEngine(t)
	ns := "repo-y"
	seedFact(t, engine, ns, dualmem.FactKindDecision, dualmem.FactSourceVerified, "Decision one.")
	seedFact(t, engine, ns, dualmem.FactKindGotcha, dualmem.FactSourceVerified, "Gotcha one.")

	h := recallHandler(engine, ns, "test-session")
	res, _, err := h(context.Background(), &mcp.CallToolRequest{}, recallInput{
		Query: "one",
		Kind:  dualmem.FactKindGotcha,
	})
	if err != nil {
		t.Fatalf("recall handler: %v", err)
	}
	text := resultText(t, res)
	if strings.Contains(text, "[decision]") {
		t.Fatalf("kind=gotcha filter should exclude decisions, got: %s", text)
	}
	if !strings.Contains(text, "[gotcha]") {
		t.Fatalf("expected gotcha result, got: %s", text)
	}
}

// --- precedent ---

func TestPrecedentHandler_HappyPath(t *testing.T) {
	engine := newTestEngine(t)
	ns := "repo-p"
	seedFact(t, engine, ns, dualmem.FactKindDecision, dualmem.FactSourceVerified, "Chose SQLite over Postgres.")
	seedFact(t, engine, ns, dualmem.FactKindDeadEnd, dualmem.FactSourceVerified, "Tried storing embeddings as JSON text; too slow.")
	// A gotcha that must NOT surface under precedent.
	seedFact(t, engine, ns, dualmem.FactKindGotcha, dualmem.FactSourceVerified, "Walker limit interacts with depth.")

	h := precedentHandler(engine, ns, "test-session")
	res, _, err := h(context.Background(), &mcp.CallToolRequest{}, precedentInput{
		Approach: "storage backend",
	})
	if err != nil {
		t.Fatalf("precedent handler: %v", err)
	}
	text := resultText(t, res)
	if strings.Contains(text, "[gotcha]") {
		t.Fatalf("precedent must not return gotchas, got: %s", text)
	}
	if !strings.Contains(text, "[decision]") && !strings.Contains(text, "[deadend]") {
		t.Fatalf("expected decision or deadend, got: %s", text)
	}
}

func TestPrecedentHandler_EmptyResults(t *testing.T) {
	engine := newTestEngine(t)
	h := precedentHandler(engine, "empty-ns", "test-session")
	res, _, err := h(context.Background(), &mcp.CallToolRequest{}, precedentInput{
		Approach: "an untried approach",
	})
	if err != nil {
		t.Fatalf("precedent handler: %v", err)
	}
	text := resultText(t, res)
	if !strings.Contains(text, "No prior decision or dead-end") {
		t.Fatalf("expected empty message, got: %s", text)
	}
}

func TestPrecedentHandler_MissingApproach(t *testing.T) {
	engine := newTestEngine(t)
	h := precedentHandler(engine, "repo-p", "test-session")
	if _, _, err := h(context.Background(), &mcp.CallToolRequest{}, precedentInput{}); err == nil {
		t.Fatal("expected error for missing approach")
	}
}

// --- file_context (reworked) ---

func TestFileContextHandler_FactsCitingPath(t *testing.T) {
	engine := newTestEngine(t)
	ns := "repo-fc"
	seedFact(t, engine, ns, dualmem.FactKindGotcha, dualmem.FactSourceVerified,
		"Walker limit interacts with directory depth here.", "codemap.go")
	rootDir := t.TempDir() // no git → no co-change neighbors, that's fine

	h := fileContextHandler(engine, ns, rootDir, "test-session")
	res, _, err := h(context.Background(), &mcp.CallToolRequest{}, fileContextInput{
		Path: "codemap.go",
	})
	if err != nil {
		t.Fatalf("file_context handler: %v", err)
	}
	text := resultText(t, res)
	if !strings.Contains(text, "Context for codemap.go") {
		t.Fatalf("expected header, got: %s", text)
	}
	if !strings.Contains(text, "Facts:") {
		t.Fatalf("expected Facts section, got: %s", text)
	}
	if !strings.Contains(text, "Walker limit") {
		t.Fatalf("expected fact body, got: %s", text)
	}
}

func TestFileContextHandler_EmptyResults(t *testing.T) {
	engine := newTestEngine(t)
	rootDir := t.TempDir()
	h := fileContextHandler(engine, "empty-ns", rootDir, "test-session")
	res, _, err := h(context.Background(), &mcp.CallToolRequest{}, fileContextInput{
		Path: "nonexistent.go",
	})
	if err != nil {
		t.Fatalf("file_context handler: %v", err)
	}
	text := resultText(t, res)
	if !strings.Contains(text, "No context for") {
		t.Fatalf("expected empty message, got: %s", text)
	}
}

func TestFileContextHandler_MissingPath(t *testing.T) {
	engine := newTestEngine(t)
	h := fileContextHandler(engine, "repo-fc", t.TempDir(), "test-session")
	if _, _, err := h(context.Background(), &mcp.CallToolRequest{}, fileContextInput{}); err == nil {
		t.Fatal("expected error for missing path")
	}
}

func TestFileContextHandler_MatchesBasename(t *testing.T) {
	engine := newTestEngine(t)
	ns := "repo-bn"
	// Fact stores a bare basename; query uses a repo-relative path.
	seedFact(t, engine, ns, dualmem.FactKindDecision, dualmem.FactSourceVerified,
		"Decision citing facts.go by basename.", "facts.go")

	h := fileContextHandler(engine, ns, t.TempDir(), "test-session")
	res, _, err := h(context.Background(), &mcp.CallToolRequest{}, fileContextInput{
		Path: "dualmem/facts.go", // repo-relative; should match via basename
	})
	if err != nil {
		t.Fatalf("file_context handler: %v", err)
	}
	text := resultText(t, res)
	if !strings.Contains(text, "Decision citing facts.go") {
		t.Fatalf("expected basename match, got: %s", text)
	}
}
