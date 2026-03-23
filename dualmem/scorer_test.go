package dualmem

import "testing"

func TestSectorBonusScore(t *testing.T) {
	// Test with CodingSectors defaults
	coding := CodingSectors()
	biasSet := make(map[string]bool)
	for _, s := range coding.DetailBias {
		biasSet[s] = true
	}
	scorer := &ImportanceScorer{Theta: 0.65, DetailBias: biasSet}

	tests := []struct {
		sector string
		want   float64
	}{
		{"decision", 1.0},
		{"warning", 1.0},
		{"map", 0.0},
		{"continuity", 0.0},
		{"", 0.0},
	}
	for _, tt := range tests {
		got := scorer.sectorBonusScore(tt.sector)
		if got != tt.want {
			t.Errorf("sectorBonusScore(%q) = %v, want %v", tt.sector, got, tt.want)
		}
	}

	// Test with NPCSectors
	npc := NPCSectors()
	npcBias := make(map[string]bool)
	for _, s := range npc.DetailBias {
		npcBias[s] = true
	}
	npcScorer := &ImportanceScorer{Theta: 0.65, DetailBias: npcBias}

	npcTests := []struct {
		sector string
		want   float64
	}{
		{"semantic", 1.0},
		{"procedural", 1.0},
		{"episodic", 0.0},
		{"emotional", 0.0},
	}
	for _, tt := range npcTests {
		got := npcScorer.sectorBonusScore(tt.sector)
		if got != tt.want {
			t.Errorf("NPC sectorBonusScore(%q) = %v, want %v", tt.sector, got, tt.want)
		}
	}
}

func TestSpecificityScore(t *testing.T) {
	// Content with proper nouns, numbers, dates, and quotes → high specificity
	rich := `John Smith said on March 15 that the total was 42.5% and quoted "never again".`
	score := specificityScore(rich)
	if score < 0.8 {
		t.Errorf("specificityScore with rich content = %v, want >= 0.8", score)
	}

	// Plain content → low specificity
	plain := "the user talked about feelings and emotions today"
	score = specificityScore(plain)
	if score > 0.3 {
		t.Errorf("specificityScore with plain content = %v, want <= 0.3", score)
	}
}

func TestNoveltyScore(t *testing.T) {
	// No similar memory → fully novel
	if got := noveltyScore(0.0); got != 1.0 {
		t.Errorf("noveltyScore(0.0) = %v, want 1.0", got)
	}

	// Below threshold → still novel
	if got := noveltyScore(0.85); got != 1.0 {
		t.Errorf("noveltyScore(0.85) = %v, want 1.0", got)
	}

	// Exact duplicate → zero novelty
	if got := noveltyScore(1.0); got != 0.0 {
		t.Errorf("noveltyScore(1.0) = %v, want 0.0", got)
	}

	// Mid-range → proportional
	mid := noveltyScore(0.925) // halfway between 0.85 and 1.0
	if mid < 0.45 || mid > 0.55 {
		t.Errorf("noveltyScore(0.925) = %v, want ~0.5", mid)
	}
}

func TestImportanceScorer(t *testing.T) {
	coding := CodingSectors()
	biasSet := make(map[string]bool)
	for _, s := range coding.DetailBias {
		biasSet[s] = true
	}
	scorer := &ImportanceScorer{Theta: 0.65, DetailBias: biasSet}

	// Decision sector + high salience + specific content + novel = high importance
	highScore := scorer.Score("decision", 0.9, `Chose SQLite over Postgres for "zero-setup" (85% of users)`, 0.0, "")
	if !scorer.IsDetail(highScore) {
		t.Errorf("high-importance memory scored %v, expected >= 0.65 (detail)", highScore)
	}

	// Continuity sector + default salience + vague content + duplicate = low importance
	lowScore := scorer.Score("continuity", 0.3, "we talked about stuff", 0.95, "")
	if scorer.IsDetail(lowScore) {
		t.Errorf("low-importance memory scored %v, expected < 0.65 (sketch)", lowScore)
	}

	// Typed memories always route to Detail regardless of raw score
	typedScore := scorer.Score("continuity", 0.5, "we talked about stuff", 0.0, "continuity")
	if !scorer.IsDetail(typedScore) {
		t.Errorf("typed continuity memory scored %v, expected >= 0.65 (detail)", typedScore)
	}
}

func TestSpecificityScoreCapped(t *testing.T) {
	// Even with all signals, score should never exceed 1.0
	extreme := `Dr. Jane Smith on 2024-01-15 at 99.9% certainty "absolutely critical" numbers 123 456`
	score := specificityScore(extreme)
	if score > 1.0 {
		t.Errorf("specificityScore should be capped at 1.0, got %v", score)
	}
}
