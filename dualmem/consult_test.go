package dualmem

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestConsult_CacheMiss_Synthesizes(t *testing.T) {
	engine, gen := newTestEngineWithGen(t)
	ctx := context.Background()
	ns := "test-ns"

	// Add some memories so there's context to synthesize from
	engine.AddWithOptions(ctx, MemoryInput{
		UserMessage: "Auth uses JWT for session validation",
		SectorHint:  "decision",
		Salience:    0.9,
		Files:       []string{"auth.go", "middleware.go"},
	}, ns)
	engine.AddWithOptions(ctx, MemoryInput{
		UserMessage: "Refresh tokens stored in Redis with 7-day TTL",
		SectorHint:  "decision",
		Salience:    0.9,
		Files:       []string{"auth.go", "store.go"},
	}, ns)

	report, err := engine.Consult(ctx, ns, "how does auth work", 2000)
	if err != nil {
		t.Fatalf("Consult: %v", err)
	}

	// Should have synthesized (no cached knowledge doc)
	if !report.Synthesized {
		t.Error("expected Synthesized=true on cache miss")
	}
	if report.Explanation == "" {
		t.Error("expected non-empty explanation")
	}
	if report.DocTopic == "" {
		t.Error("expected non-empty DocTopic")
	}
	if gen.generateCalls == 0 {
		t.Error("expected GenerateText to be called")
	}
}

func TestConsult_CacheHit_ServesExisting(t *testing.T) {
	engine, _ := newTestEngineWithGen(t)
	ctx := context.Background()
	ns := "test-ns"

	// Pre-seed a knowledge doc whose embedding matches the query.
	// Use the mock embedder to embed the query text, then save a doc with that embedding.
	query := "how does auth work"
	queryEmb, _ := engine.embedder.Embed(ctx, query, "RETRIEVAL_QUERY")

	now := time.Now()
	existingDoc := &KnowledgeDoc{
		ID:         generateID(),
		Namespace:  ns,
		Topic:      "auth",
		Content:    "Pre-cached explanation of the auth subsystem.",
		Files:      []string{"auth.go", "middleware.go"},
		SourceIDs:  []string{"m1", "m2"},
		Embedding:  queryEmb, // same vector as query → cosine = 1.0
		TokenCount: 20,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := engine.store.UpsertKnowledgeDoc(existingDoc, queryEmb); err != nil {
		t.Fatalf("UpsertKnowledgeDoc: %v", err)
	}

	// Consult should hit cache (cosine = 1.0 ≥ 0.75)
	report, err := engine.Consult(ctx, ns, query, 2000)
	if err != nil {
		t.Fatalf("Consult: %v", err)
	}
	if report.Synthesized {
		t.Error("expected cache hit, got Synthesized=true")
	}
	if report.Explanation != existingDoc.Content {
		t.Errorf("expected cached content, got %q", report.Explanation)
	}
	if report.DocTopic != "auth" {
		t.Errorf("expected topic 'auth', got %q", report.DocTopic)
	}
	if report.Confidence < 0.39 {
		t.Errorf("expected medium+ confidence, got %.2f", report.Confidence)
	}
}

func TestComputeConsultConfidence(t *testing.T) {
	tests := []struct {
		name      string
		docScore  float64
		edges     int
		memories  int
		wantLow   float64
		wantHigh  float64
		wantLabel string
	}{
		{"high confidence", 0.9, 15, 8, 0.7, 1.0, "high"},
		{"medium confidence", 0.5, 5, 2, 0.4, 0.7, "medium"},
		{"low confidence", 0.0, 1, 0, 0.0, 0.4, "low"},
		{"edge-heavy", 0.0, 20, 0, 0.25, 0.35, "low"},
		{"memory-heavy", 0.0, 0, 10, 0.25, 0.35, "low"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			score, label := computeConsultConfidence(tt.docScore, tt.edges, tt.memories)
			if score < tt.wantLow || score > tt.wantHigh {
				t.Errorf("score=%.2f, want [%.2f, %.2f]", score, tt.wantLow, tt.wantHigh)
			}
			if label != tt.wantLabel {
				t.Errorf("label=%q, want %q", label, tt.wantLabel)
			}
		})
	}
}

func TestConsultReport_FormatText(t *testing.T) {
	report := &ConsultReport{
		Confidence: 0.82,
		ConfLabel:  "high",
		Explanation: "Auth starts in middleware.go which validates JWTs.",
		Evidence:    "[Call Graph]\nmiddleware.go → jwt.go (calls: ValidateToken)\n",
		RelevantFiles: []RankedFile{
			{Path: "middleware.go", Score: 0.91, Source: "hdc", Desc: "HTTP middleware"},
			{Path: "jwt.go", Score: 0.87, Source: "hdc", Desc: "JWT validation"},
		},
	}

	text := report.FormatText()

	if !strings.Contains(text, "Confidence: 0.82 (high)") {
		t.Error("missing confidence header")
	}
	if !strings.Contains(text, "=== Explanation ===") {
		t.Error("missing explanation section")
	}
	if !strings.Contains(text, "=== Structural Evidence ===") {
		t.Error("missing evidence section")
	}
	if !strings.Contains(text, "=== Relevant Files ===") {
		t.Error("missing files section")
	}
	if !strings.Contains(text, "middleware.go") {
		t.Error("missing file entry")
	}
}

func TestFileOverlap(t *testing.T) {
	tests := []struct {
		name string
		a, b []string
		want float64
	}{
		{"full overlap", []string{"a.go", "b.go"}, []string{"a.go", "b.go", "c.go"}, 1.0},
		{"half overlap", []string{"a.go", "b.go"}, []string{"a.go", "c.go"}, 0.5},
		{"no overlap", []string{"a.go"}, []string{"b.go"}, 0.0},
		{"empty a", []string{}, []string{"a.go"}, 0.0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := fileOverlap(tt.a, tt.b)
			if got != tt.want {
				t.Errorf("fileOverlap=%v, want %v", got, tt.want)
			}
		})
	}
}

func TestConsult_SynthesisPromptIncludesCode(t *testing.T) {
	dir := t.TempDir()
	srcDir := filepath.Join(dir, "pkg")
	os.MkdirAll(srcDir, 0755)
	os.WriteFile(filepath.Join(srcDir, "auth.go"), []byte("package pkg\n\nfunc ValidateToken(token string) bool {\n\treturn token != \"\"\n}\n"), 0644)

	engine, gen := newTestEngineWithGen(t)
	engine.cfg.RootDir = dir

	engine.AddWithOptions(ctx(t), MemoryInput{
		UserMessage: "Auth validates JWT tokens",
		Salience:    0.9,
		Files:       []string{"pkg/auth.go"},
	}, "test-ns")

	_, err := engine.Consult(ctx(t), "test-ns", "how does auth validation work", 2000)
	if err != nil {
		t.Fatal(err)
	}

	if gen.generateCalls == 0 {
		t.Fatal("expected synthesis call")
	}
	if !strings.Contains(gen.lastPrompt, "[Code Snippets]") {
		t.Error("synthesis prompt missing [Code Snippets] section")
	}
}
