package dualmem

import (
	"encoding/json"
	"math"
	"testing"
	"time"
)

func TestNewRerankerFeatures(t *testing.T) {
	tests := []struct {
		name    string
		memType string
		wantW   float64 // TypeIsWarning
		wantD   float64 // TypeIsDecision
		wantC   float64 // TypeIsContinuity
	}{
		{"warning type", "warning", 1, 0, 0},
		{"decision type", "decision", 0, 1, 0},
		{"continuity type", "continuity", 0, 0, 1},
		{"general type", "general", 0, 0, 0},
		{"empty type", "", 0, 0, 0},
		{"unknown type", "bookmark", 0, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := NewRerankerFeatures(0.8, 0.5, 0.7, tt.memType, 2, 3.0, 100, true)
			if f.TypeIsWarning != tt.wantW {
				t.Errorf("TypeIsWarning = %v, want %v", f.TypeIsWarning, tt.wantW)
			}
			if f.TypeIsDecision != tt.wantD {
				t.Errorf("TypeIsDecision = %v, want %v", f.TypeIsDecision, tt.wantD)
			}
			if f.TypeIsContinuity != tt.wantC {
				t.Errorf("TypeIsContinuity = %v, want %v", f.TypeIsContinuity, tt.wantC)
			}
			// Verify scalar fields are passed through.
			if f.CosineSim != 0.8 {
				t.Errorf("CosineSim = %v, want 0.8", f.CosineSim)
			}
			if f.Importance != 0.5 {
				t.Errorf("Importance = %v, want 0.5", f.Importance)
			}
			if f.Salience != 0.7 {
				t.Errorf("Salience = %v, want 0.7", f.Salience)
			}
			if f.FileOverlap != 2.0 {
				t.Errorf("FileOverlap = %v, want 2", f.FileOverlap)
			}
			if f.AgeDays != 3.0 {
				t.Errorf("AgeDays = %v, want 3", f.AgeDays)
			}
			if f.TextLength != 100.0 {
				t.Errorf("TextLength = %v, want 100", f.TextLength)
			}
			if f.SectorMatch != 1.0 {
				t.Errorf("SectorMatch = %v, want 1 (true)", f.SectorMatch)
			}
		})
	}

	// Verify sectorMatch=false maps to 0.
	f := NewRerankerFeatures(0, 0, 0, "", 0, 0, 0, false)
	if f.SectorMatch != 0.0 {
		t.Errorf("SectorMatch for false = %v, want 0", f.SectorMatch)
	}
}

func TestRerankerFeaturesVector(t *testing.T) {
	t.Helper()
	f := NewRerankerFeatures(0.9, 0.6, 0.8, "warning", 3, 5.0, 200, true)
	v := f.Vector()
	expected := [10]float64{0.9, 0.6, 0.8, 1, 0, 0, 3, 5.0, 200, 1}
	for i := range v {
		if v[i] != expected[i] {
			t.Errorf("Vector()[%d] = %v, want %v", i, v[i], expected[i])
		}
	}
}

func testSigmoid(x float64) float64 {
	return 1.0 / (1.0 + math.Exp(-x))
}

func TestRerankerPredict(t *testing.T) {
	w := &RerankerWeights{
		Coefficients: [10]float64{2.0, 1.0, 1.5, 3.0, 2.0, 1.0, 0.5, -0.1, 0.01, 0.5},
		Bias:         -1.0,
	}

	// High-value warning with strong cosine similarity.
	high := NewRerankerFeatures(0.95, 0.8, 0.9, "warning", 3, 1.0, 150, true)
	// Low-value general memory with weak similarity.
	low := NewRerankerFeatures(0.1, 0.2, 0.3, "general", 0, 30.0, 50, false)

	pHigh := w.Predict(high)
	pLow := w.Predict(low)

	if pHigh < 0 || pHigh > 1 {
		t.Errorf("Predict(high) = %v, want in [0,1]", pHigh)
	}
	if pLow < 0 || pLow > 1 {
		t.Errorf("Predict(low) = %v, want in [0,1]", pLow)
	}
	if pHigh <= pLow {
		t.Errorf("expected Predict(high) > Predict(low), got %v <= %v", pHigh, pLow)
	}

	// Verify the sigmoid math manually.
	vHigh := high.Vector()
	dot := w.Bias
	for i := 0; i < 10; i++ {
		dot += w.Coefficients[i] * vHigh[i]
	}
	want := testSigmoid(dot)
	if math.Abs(pHigh-want) > 1e-12 {
		t.Errorf("Predict(high) = %v, manual sigmoid = %v", pHigh, want)
	}
}

func TestRerankerFinalScore(t *testing.T) {
	w := &RerankerWeights{
		Coefficients: [10]float64{1, 1, 1, 1, 1, 1, 0.1, -0.01, 0.001, 0.5},
		Bias:         0,
	}

	f := NewRerankerFeatures(0.7, 0.5, 0.6, "decision", 1, 2.0, 80, true)
	pred := w.Predict(f)
	cosine := 0.7

	got := w.FinalScore(cosine, f)
	want := 0.6*cosine + 0.4*pred
	if math.Abs(got-want) > 1e-12 {
		t.Errorf("FinalScore = %v, want 0.6*%v + 0.4*%v = %v", got, cosine, pred, want)
	}
}

func TestTrainReranker_MinSamples(t *testing.T) {
	rows := make([]TrainingRow, 49)
	for i := range rows {
		rows[i] = TrainingRow{
			Features: NewRerankerFeatures(0.5, 0.5, 0.5, "general", 0, 1, 100, false),
			Label:    float64(i % 2),
			Weight:   1.0,
		}
	}
	_, err := TrainReranker(rows)
	if err == nil {
		t.Fatal("expected error for fewer than 50 rows, got nil")
	}
}

func TestTrainReranker_Convergence(t *testing.T) {
	rows := make([]TrainingRow, 100)
	for i := 0; i < 50; i++ {
		// Positive: warning + high cosine similarity.
		rows[i] = TrainingRow{
			Features: NewRerankerFeatures(
				0.8+0.2*float64(i%5)/5.0, // cosine 0.8-1.0
				0.7,
				0.85,
				"warning",
				2,
				float64(i%10),
				150,
				true,
			),
			Label:  1.0,
			Weight: 1.0,
		}
	}
	for i := 50; i < 100; i++ {
		// Negative: low-salience general memory, weak similarity.
		rows[i] = TrainingRow{
			Features: NewRerankerFeatures(
				0.1+0.2*float64(i%5)/5.0, // cosine 0.1-0.3
				0.2,
				0.2,
				"general",
				0,
				float64(20+i%10),
				40,
				false,
			),
			Label:  0.0,
			Weight: 1.0,
		}
	}

	w, err := TrainReranker(rows)
	if err != nil {
		t.Fatalf("TrainReranker: %v", err)
	}

	// Coefficients should be bounded.
	for i, c := range w.Coefficients {
		if c < -5 || c > 5 {
			t.Errorf("Coefficient[%d] = %v, want in [-5, 5]", i, c)
		}
	}

	// SampleCount should match.
	if w.SampleCount != 100 {
		t.Errorf("SampleCount = %d, want 100", w.SampleCount)
	}

	// TrainedAt should be recent.
	if time.Since(w.TrainedAt) > 10*time.Second {
		t.Errorf("TrainedAt too old: %v", w.TrainedAt)
	}

	// Model should discriminate: high-sim warnings should score higher than low-sim generals.
	posFeatures := NewRerankerFeatures(0.9, 0.7, 0.85, "warning", 2, 3.0, 150, true)
	negFeatures := NewRerankerFeatures(0.15, 0.2, 0.2, "general", 0, 25.0, 40, false)

	pPos := w.Predict(posFeatures)
	pNeg := w.Predict(negFeatures)
	if pPos <= pNeg {
		t.Errorf("expected positive prediction > negative, got %v <= %v", pPos, pNeg)
	}
}

func TestTrainReranker_WeightedLoss(t *testing.T) {
	makeRows := func(lateWeight float64) []TrainingRow {
		rows := make([]TrainingRow, 100)
		for i := 0; i < 50; i++ {
			rows[i] = TrainingRow{
				Features: NewRerankerFeatures(0.9, 0.7, 0.8, "warning", 2, 1.0, 120, true),
				Label:    1.0,
				Weight:   1.0, // early phase
			}
		}
		for i := 50; i < 100; i++ {
			// Late-phase rows: continuity type, moderate cosine, labeled positive.
			rows[i] = TrainingRow{
				Features: NewRerankerFeatures(0.5, 0.4, 0.5, "continuity", 1, 5.0, 80, false),
				Label:    1.0,
				Weight:   lateWeight,
			}
		}
		return rows
	}

	wEqual, err := TrainReranker(makeRows(1.0))
	if err != nil {
		t.Fatalf("TrainReranker (equal): %v", err)
	}
	wHeavy, err := TrainReranker(makeRows(2.0))
	if err != nil {
		t.Fatalf("TrainReranker (heavy): %v", err)
	}

	// At least one coefficient should differ between the two models,
	// demonstrating that the weight parameter has influence.
	anyDiff := false
	for i := 0; i < 10; i++ {
		if math.Abs(wEqual.Coefficients[i]-wHeavy.Coefficients[i]) > 1e-6 {
			anyDiff = true
			break
		}
	}
	if !anyDiff && math.Abs(wEqual.Bias-wHeavy.Bias) <= 1e-6 {
		t.Error("expected coefficient differences between equal-weight and heavy-weight models")
	}
}

func TestRerankerIsStale(t *testing.T) {
	w := &RerankerWeights{
		TrainedAt: time.Now(),
	}
	if w.IsStale(7) {
		t.Error("freshly trained weights should not be stale at 7 days")
	}

	w.TrainedAt = time.Now().Add(-8 * 24 * time.Hour)
	if !w.IsStale(7) {
		t.Error("8-day-old weights should be stale at maxAgeDays=7")
	}

	// Boundary: exactly at the limit — not yet stale (Before is strict <).
	w.TrainedAt = time.Now().Add(-7 * 24 * time.Hour)
	if w.IsStale(7) {
		t.Error("exactly 7-day-old weights should not yet be stale at maxAgeDays=7")
	}
}

func TestRerankerDecay(t *testing.T) {
	w := &RerankerWeights{
		Coefficients: [10]float64{2.0, -1.5, 0.8, 3.0, -2.0, 1.0, 0.5, -0.3, 0.1, 1.5},
		Bias:         0.5,
	}

	origCoeffs := w.Coefficients
	origBias := w.Bias

	w.Decay(0.1) // Shrink 10% toward 0.

	for i := 0; i < 10; i++ {
		expected := origCoeffs[i] * (1 - 0.1)
		if math.Abs(w.Coefficients[i]-expected) > 1e-12 {
			t.Errorf("Coefficients[%d] after Decay(0.1) = %v, want %v", i, w.Coefficients[i], expected)
		}
		// Should be closer to zero than the original.
		if math.Abs(w.Coefficients[i]) > math.Abs(origCoeffs[i])+1e-12 {
			t.Errorf("Coefficients[%d] moved away from zero: %v -> %v", i, origCoeffs[i], w.Coefficients[i])
		}
	}

	// Bias should also decay.
	expectedBias := origBias * (1 - 0.1)
	if math.Abs(w.Bias-expectedBias) > 1e-12 {
		t.Errorf("Bias after Decay(0.1) = %v, want %v", w.Bias, expectedBias)
	}
}

func TestRerankerJSON(t *testing.T) {
	original := &RerankerWeights{
		Coefficients: [10]float64{0.5, -1.2, 0.3, 2.1, -0.8, 0.0, 1.1, -0.4, 0.7, 0.9},
		Bias:         -0.25,
		TrainedAt:    time.Now().Truncate(time.Second), // Truncate for JSON round-trip.
		SampleCount:  150,
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	var restored RerankerWeights
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}

	if original.Coefficients != restored.Coefficients {
		t.Errorf("Coefficients mismatch: %v != %v", original.Coefficients, restored.Coefficients)
	}
	if original.Bias != restored.Bias {
		t.Errorf("Bias mismatch: %v != %v", original.Bias, restored.Bias)
	}
	if !original.TrainedAt.Equal(restored.TrainedAt) {
		t.Errorf("TrainedAt mismatch: %v != %v", original.TrainedAt, restored.TrainedAt)
	}
	if original.SampleCount != restored.SampleCount {
		t.Errorf("SampleCount mismatch: %d != %d", original.SampleCount, restored.SampleCount)
	}
}
