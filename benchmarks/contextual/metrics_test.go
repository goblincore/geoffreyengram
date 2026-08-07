package main

import (
	"math"
	"testing"
)

func TestModuleAwarePrecisionAtK(t *testing.T) {
	gt := GroundTruth{Files: []string{"auth/jwt.go"}, Modules: []string{"auth/"}}
	if !isRelevantMatch("auth/middleware.go", gt) {
		t.Fatal("file in a ground-truth module should be relevant")
	}
	if isRelevantMatch("ui/button.go", gt) {
		t.Fatal("unrelated module should not be relevant")
	}
}

func TestSiblingAwareRecallAtK(t *testing.T) {
	results := []SearchResult{{Path: "auth/jwt.go", Score: 0.9}}
	gt := GroundTruth{Files: []string{"auth/jwt.go", "auth/oauth.go"}, Modules: []string{"auth/"}}
	if got := recallAtK(results, gt, 5); got != 1.0 {
		t.Fatalf("Recall@5 = %.3f, want 1.0", got)
	}
}

func TestPrecisionAtK(t *testing.T) {
	results := []SearchResult{
		{Path: "auth/jwt.go", Score: 0.9},
		{Path: "auth/middleware.go", Score: 0.8},
		{Path: "ui/button.go", Score: 0.7},
		{Path: "auth/oauth.go", Score: 0.6},
		{Path: "db/store.go", Score: 0.5},
	}
	gt := GroundTruth{
		Files:   []string{"auth/jwt.go", "auth/middleware.go", "auth/oauth.go"},
		Modules: []string{"auth/"},
	}

	p3 := precisionAtK(results, gt, 3)
	if math.Abs(p3-2.0/3.0) > 0.01 {
		t.Errorf("Precision@3: got %.3f, want %.3f", p3, 2.0/3.0)
	}

	p5 := precisionAtK(results, gt, 5)
	if math.Abs(p5-3.0/5.0) > 0.01 {
		t.Errorf("Precision@5: got %.3f, want %.3f", p5, 3.0/5.0)
	}
}

func TestRecallAtK(t *testing.T) {
	results := []SearchResult{
		{Path: "auth/jwt.go", Score: 0.9},
		{Path: "ui/button.go", Score: 0.8},
		{Path: "auth/middleware.go", Score: 0.7},
	}
	gt := GroundTruth{
		Files:   []string{"auth/jwt.go", "auth/middleware.go", "auth/oauth.go"},
		Modules: []string{"auth/"},
	}

	r5 := recallAtK(results, gt, 5)
	if math.Abs(r5-1.0) > 0.01 {
		t.Errorf("Recall@5: got %.3f, want 1.0", r5)
	}
}

func TestNDCGAt10(t *testing.T) {
	// Perfect ordering: all relevant results first
	perfect := []SearchResult{
		{Path: "auth/jwt.go", Score: 0.9},
		{Path: "auth/middleware.go", Score: 0.8},
		{Path: "auth/oauth.go", Score: 0.7},
	}
	gt := GroundTruth{
		Files:   []string{"auth/jwt.go", "auth/middleware.go", "auth/oauth.go"},
		Modules: []string{"auth/"},
	}

	ndcg := ndcgAtK(perfect, gt, 10)
	if math.Abs(ndcg-1.0) > 0.01 {
		t.Errorf("NDCG@10 perfect: got %.3f, want 1.0", ndcg)
	}

	// Imperfect: relevant result at position 3 instead of 1
	imperfect := []SearchResult{
		{Path: "ui/button.go", Score: 0.9},
		{Path: "db/store.go", Score: 0.8},
		{Path: "auth/jwt.go", Score: 0.7},
	}
	gt2 := GroundTruth{Files: []string{"auth/jwt.go"}, Modules: []string{"auth/"}}
	ndcg2 := ndcgAtK(imperfect, gt2, 10)
	if ndcg2 >= 1.0 || ndcg2 <= 0.0 {
		t.Errorf("NDCG@10 imperfect: got %.3f, want between 0 and 1", ndcg2)
	}
}

func TestModuleMatch(t *testing.T) {
	results := []SearchResult{
		{Path: "dualmem/hdc.go", Score: 0.9},
	}
	gt := GroundTruth{
		Files:   []string{"dualmem/codemap.go"},
		Modules: []string{"dualmem/"}, // module hit
	}

	// Module-aware precision should count files in the ground-truth module.
	p := precisionAtK(results, gt, 3)
	if p != 1.0 {
		t.Errorf("Module-aware precision should be 1 for a file in the right module, got %.3f", p)
	}

	// Module-aware NDCG should give partial credit
	ndcg := ndcgAtK(results, gt, 10)
	if ndcg <= 0.0 {
		t.Errorf("Module-aware NDCG should give partial credit, got %.3f", ndcg)
	}
}

func TestComputeIRMetrics(t *testing.T) {
	out := AdapterOutput{
		Results: []SearchResult{
			{Path: "auth/jwt.go", Score: 0.9},
			{Path: "auth/middleware.go", Score: 0.8},
			{Path: "ui/button.go", Score: 0.7},
		},
		LatencyMs: 42,
		APICalls:  0,
	}
	gt := GroundTruth{
		Files:   []string{"auth/jwt.go", "auth/middleware.go"},
		Modules: []string{"auth/"},
	}

	m := computeIRMetrics(out, gt)
	if m.LatencyMs != 42 {
		t.Errorf("latency: got %d, want 42", m.LatencyMs)
	}
	if m.PrecisionAt3 < 0.5 {
		t.Errorf("P@3 too low: %.3f", m.PrecisionAt3)
	}
	if m.RecallAt5 < 0.9 {
		t.Errorf("R@5 too low: %.3f", m.RecallAt5)
	}
}
