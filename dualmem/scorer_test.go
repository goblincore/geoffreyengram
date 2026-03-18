package dualmem

import "testing"

func TestSectorBonusScore(t *testing.T) {
	tests := []struct {
		sector string
		want   float64
	}{
		{"semantic", 1.0},
		{"procedural", 1.0},
		{"episodic", 0.0},
		{"emotional", 0.0},
		{"reflective", 0.0},
		{"", 0.0},
	}
	for _, tt := range tests {
		got := sectorBonusScore(tt.sector)
		if got != tt.want {
			t.Errorf("sectorBonusScore(%q) = %v, want %v", tt.sector, got, tt.want)
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
	scorer := &ImportanceScorer{Theta: 0.65}

	// Semantic sector + high salience + specific content + novel = high importance
	highScore := scorer.Score("semantic", 0.9, `John prefers "dark roast" coffee (85% arabica)`, 0.0)
	if !scorer.IsDetail(highScore) {
		t.Errorf("high-importance memory scored %v, expected >= 0.65 (detail)", highScore)
	}

	// Episodic sector + default salience + vague content + duplicate = low importance
	lowScore := scorer.Score("episodic", 0.3, "we talked about stuff", 0.95)
	if scorer.IsDetail(lowScore) {
		t.Errorf("low-importance memory scored %v, expected < 0.65 (sketch)", lowScore)
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
