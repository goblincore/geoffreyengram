package engram

import (
	"math"
	"testing"
	"time"
)

func TestCompositeScoreDefaults(t *testing.T) {
	w := DefaultScoringWeights()
	// Perfect similarity, full salience, just accessed, full link weight, neutral sector
	score := CompositeScore(1.0, 1.0, 0, 1.0, 1.0, w)
	// recency at 0 days = exp(0) = 1.0
	// raw = 0.6*1 + 0.2*1 + 0.1*1 + 0.1*1 = 1.0
	expected := 1.0
	if math.Abs(score-expected) > 0.001 {
		t.Errorf("expected %.3f, got %.3f", expected, score)
	}
}

func TestCompositeScoreZeroSimilarity(t *testing.T) {
	w := DefaultScoringWeights()
	score := CompositeScore(0, 0.8, 0, 0, 1.0, w)
	// raw = 0.6*0 + 0.2*0.8 + 0.1*1.0 + 0.1*0 = 0.26
	expected := 0.26
	if math.Abs(score-expected) > 0.001 {
		t.Errorf("expected %.3f, got %.3f", expected, score)
	}
}

func TestCompositeScoreSectorMultiplier(t *testing.T) {
	w := DefaultScoringWeights()
	base := CompositeScore(0.5, 0.5, 0, 0.5, 1.0, w)
	boosted := CompositeScore(0.5, 0.5, 0, 0.5, 2.0, w)
	if math.Abs(boosted-base*2) > 0.001 {
		t.Errorf("sector weight 2.0 should double score: base=%.3f, boosted=%.3f", base, boosted)
	}
}

func TestCompositeScoreCustomWeights(t *testing.T) {
	// Salience-heavy weights
	w := ScoringWeights{Similarity: 0.2, Salience: 0.6, Recency: 0.1, LinkWeight: 0.1}
	score := CompositeScore(0.0, 1.0, 0, 0.0, 1.0, w)
	// raw = 0.2*0 + 0.6*1 + 0.1*1 + 0.1*0 = 0.7
	expected := 0.7
	if math.Abs(score-expected) > 0.001 {
		t.Errorf("expected %.3f, got %.3f", expected, score)
	}
}

func TestCompositeScoreRecencyDecay(t *testing.T) {
	w := DefaultScoringWeights()
	recent := CompositeScore(0.5, 0.5, 0, 0, 1.0, w)
	old := CompositeScore(0.5, 0.5, 100, 0, 1.0, w)
	if old >= recent {
		t.Errorf("old memories should score lower: recent=%.3f, old=%.3f", recent, old)
	}
}

func TestCosineSimilarityIdentical(t *testing.T) {
	v := []float32{1, 2, 3}
	sim := CosineSimilarity(v, v)
	if math.Abs(sim-1.0) > 0.001 {
		t.Errorf("identical vectors should have similarity 1.0, got %.3f", sim)
	}
}

func TestCosineSimilarityOrthogonal(t *testing.T) {
	a := []float32{1, 0}
	b := []float32{0, 1}
	sim := CosineSimilarity(a, b)
	if math.Abs(sim) > 0.001 {
		t.Errorf("orthogonal vectors should have similarity 0.0, got %.3f", sim)
	}
}

func TestCosineSimilarityOpposite(t *testing.T) {
	a := []float32{1, 0}
	b := []float32{-1, 0}
	sim := CosineSimilarity(a, b)
	if math.Abs(sim-(-1.0)) > 0.001 {
		t.Errorf("opposite vectors should have similarity -1.0, got %.3f", sim)
	}
}

func TestCosineSimilarityDifferentLengths(t *testing.T) {
	a := []float32{1, 2, 3}
	b := []float32{1, 2}
	sim := CosineSimilarity(a, b)
	if sim != 0 {
		t.Errorf("different length vectors should return 0, got %.3f", sim)
	}
}

func TestCosineSimilarityEmpty(t *testing.T) {
	sim := CosineSimilarity(nil, nil)
	if sim != 0 {
		t.Errorf("nil vectors should return 0, got %.3f", sim)
	}
}

func TestCosineSimilarityZeroVector(t *testing.T) {
	a := []float32{0, 0, 0}
	b := []float32{1, 2, 3}
	sim := CosineSimilarity(a, b)
	if sim != 0 {
		t.Errorf("zero vector should return 0, got %.3f", sim)
	}
}

func TestDecayFactorZeroDays(t *testing.T) {
	d := DecayFactor(0.02, 0, 0.5)
	if math.Abs(d-1.0) > 0.001 {
		t.Errorf("zero days should give decay factor ~1.0, got %.3f", d)
	}
}

func TestDecayFactorHighSalienceDampens(t *testing.T) {
	lowSalience := DecayFactor(0.02, 30, 0.1)
	highSalience := DecayFactor(0.02, 30, 0.9)
	if highSalience <= lowSalience {
		t.Errorf("high salience should decay slower: low=%.3f, high=%.3f", lowSalience, highSalience)
	}
}

func TestDaysSince(t *testing.T) {
	past := time.Now().Add(-48 * time.Hour)
	days := DaysSince(past)
	if math.Abs(days-2.0) > 0.01 {
		t.Errorf("expected ~2.0 days, got %.3f", days)
	}
}
