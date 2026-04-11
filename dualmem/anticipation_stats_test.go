package dualmem

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestAnticipationStats_NoData(t *testing.T) {
	engine := newTestEngine(t)
	stats, err := engine.ComputeAnticipationStats(context.Background(), "testns", 30*time.Minute)
	if err != nil {
		t.Fatalf("ComputeAnticipationStats: %v", err)
	}
	if stats.Total != 0 {
		t.Errorf("expected 0 total, got %d", stats.Total)
	}
	if stats.HitRate != 0 {
		t.Errorf("expected 0 hit rate, got %f", stats.HitRate)
	}
}

func TestAnticipationStats_HitAndMiss(t *testing.T) {
	engine := newTestEngine(t)
	ctx := context.Background()
	ns := "ant-test"

	// Create anticipation memories (simulating what the anticipatory worker does).
	// Anticipation 1: file that will be "hit" — a later memory references the same file.
	engine.AddWithOptions(ctx, MemoryInput{
		UserMessage: "Pre-explored auth.go: JWT validation logic",
		SectorHint:  "cochange:routes.go→auth.go",
		Type:        "anticipation",
		Salience:    0.65,
		Files:       []string{"auth.go"},
	}, ns)

	// Wait >1s to ensure different second-precision timestamps in SQLite.
	time.Sleep(1100 * time.Millisecond)

	// Anticipation 2: file that will be a "miss" — nothing references it later.
	engine.AddWithOptions(ctx, MemoryInput{
		UserMessage: "Pre-explored config.go: environment variable loading",
		SectorHint:  "structural:main.go→config.go",
		Type:        "anticipation",
		Salience:    0.65,
		Files:       []string{"config.go"},
	}, ns)

	// Wait >1s for timestamp separation.
	time.Sleep(1100 * time.Millisecond)

	// Now add a non-anticipation memory that references auth.go (creating a "hit").
	engine.AddWithOptions(ctx, MemoryInput{
		UserMessage: "Fixed JWT expiry validation bug in auth.go",
		Type:        "decision",
		Files:       []string{"auth.go"},
	}, ns)

	stats, err := engine.ComputeAnticipationStats(ctx, ns, 30*time.Minute)
	if err != nil {
		t.Fatalf("ComputeAnticipationStats: %v", err)
	}

	if stats.Total != 2 {
		t.Errorf("expected 2 total anticipations, got %d", stats.Total)
	}
	if stats.Hits != 1 {
		t.Errorf("expected 1 hit, got %d", stats.Hits)
	}
	if stats.Misses != 1 {
		t.Errorf("expected 1 miss, got %d", stats.Misses)
	}

	// Check by-source breakdown.
	if cc, ok := stats.BySource["cochange"]; ok {
		if cc.Hits != 1 {
			t.Errorf("cochange: expected 1 hit, got %d", cc.Hits)
		}
	} else {
		t.Error("missing cochange source")
	}
	if st, ok := stats.BySource["structural"]; ok {
		if st.Misses != 1 {
			t.Errorf("structural: expected 1 miss, got %d", st.Misses)
		}
	} else {
		t.Error("missing structural source")
	}
}

func TestParseSourceType(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"cochange:routes.go,middleware.go→auth.go", "cochange"},
		{"structural:main.go→config.go", "structural"},
		{"", "unknown"},
		{"semantic:something", "unknown"},
	}
	for _, tt := range tests {
		got := parseSourceType(tt.input)
		if got != tt.want {
			t.Errorf("parseSourceType(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestFormatAnticipationStats_Empty(t *testing.T) {
	stats := &AnticipationStats{
		BySource: make(map[string]*HitMiss),
	}
	output := FormatAnticipationStats(stats, "test:ns")
	if output == "" {
		t.Fatal("empty output")
	}
	if !strings.Contains(output, "No anticipation memories found") {
		t.Errorf("expected empty message, got:\n%s", output)
	}
}

func TestFormatAnticipationStats_WithData(t *testing.T) {
	stats := &AnticipationStats{
		Total:   10,
		Hits:    4,
		Misses:  6,
		HitRate: 0.4,
		BySource: map[string]*HitMiss{
			"cochange":   {Hits: 3, Misses: 4},
			"structural": {Hits: 1, Misses: 2},
		},
		TopHits: []FileHitStat{
			{Path: "auth.go", Hits: 3, Total: 4, HitRate: 0.75},
		},
	}
	output := FormatAnticipationStats(stats, "test:ns")
	for _, want := range []string{"Total: 10", "40%", "cochange", "auth.go"} {
		if !strings.Contains(output, want) {
			t.Errorf("output missing %q:\n%s", want, output)
		}
	}
}
