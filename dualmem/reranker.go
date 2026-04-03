// reranker.go implements a pure-Go logistic regression re-ranker for memory
// retrieval. It learns from context quality ratings to improve the ordering of
// candidate memories returned by DualSearch / AssembleContext.
//
// The model is intentionally simple: 10 hand-crafted features, no external
// dependencies, and a weighted binary cross-entropy training loop that
// converges in well under a second on typical dataset sizes (50-5000 rows).
package dualmem

import (
	"fmt"
	"math"
	"time"
)

// RerankerFeatures holds the feature vector for a single candidate memory.
type RerankerFeatures struct {
	CosineSim        float64 // raw cosine similarity from HDC search
	Importance       float64 // importance score assigned at insert time
	Salience         float64 // user-supplied salience (0-1)
	TypeIsWarning    float64 // 1.0 if memory type is "warning", else 0.0
	TypeIsDecision   float64 // 1.0 if memory type is "decision", else 0.0
	TypeIsContinuity float64 // 1.0 if memory type is "continuity", else 0.0
	FileOverlap      float64 // count of overlapping files with the query context
	AgeDays          float64 // age of the memory in days
	TextLength       float64 // estimated token count of the memory text
	SectorMatch      float64 // 1.0 if memory sector matches the intent preference
}

// RerankerWeights stores the learned logistic regression coefficients.
type RerankerWeights struct {
	Coefficients [10]float64 `json:"coefficients"` // one per feature
	Bias         float64     `json:"bias"`
	TrainedAt    time.Time   `json:"trained_at"`
	SampleCount  int         `json:"sample_count"`
}

// TrainingRow is a labeled training example derived from context_ratings.
type TrainingRow struct {
	Features RerankerFeatures
	Label    float64 // 0.0 or 1.0 (rating >= 1 maps to relevant)
	Weight   float64 // sample weight: 1.0 for early phase, 2.0 for late phase
}

// featureCount is the number of features in the logistic regression model.
const featureCount = 10

// Training hyper-parameters.
const (
	defaultLearningRate = 0.01
	maxIterations       = 1000
	convergenceEps      = 1e-6
	minTrainingSamples  = 50
	coefficientBound    = 5.0
)

// NewRerankerFeatures constructs a RerankerFeatures value, mapping the string
// memType to its one-hot encoding (warning, decision, continuity) and
// converting the integer/bool fields to float64.
func NewRerankerFeatures(
	cosineSim, importance, salience float64,
	memType string,
	fileOverlap int,
	ageDays float64,
	textLength int,
	sectorMatch bool,
) RerankerFeatures {
	f := RerankerFeatures{
		CosineSim:   cosineSim,
		Importance:  importance,
		Salience:    salience,
		FileOverlap: float64(fileOverlap),
		AgeDays:     ageDays,
		TextLength:  float64(textLength),
	}
	switch memType {
	case "warning":
		f.TypeIsWarning = 1.0
	case "decision":
		f.TypeIsDecision = 1.0
	case "continuity":
		f.TypeIsContinuity = 1.0
	}
	if sectorMatch {
		f.SectorMatch = 1.0
	}
	return f
}

// Vector returns the feature values as a fixed-size array, in the same order
// as the Coefficients array in RerankerWeights.
func (f RerankerFeatures) Vector() [10]float64 {
	return [10]float64{
		f.CosineSim,
		f.Importance,
		f.Salience,
		f.TypeIsWarning,
		f.TypeIsDecision,
		f.TypeIsContinuity,
		f.FileOverlap,
		f.AgeDays,
		f.TextLength,
		f.SectorMatch,
	}
}

// sigmoid computes the standard logistic function.
func sigmoid(z float64) float64 {
	return 1.0 / (1.0 + math.Exp(-z))
}

// Predict returns P(relevant) for the given features using the trained
// logistic regression model: sigmoid(w . x + b).
func (w *RerankerWeights) Predict(f RerankerFeatures) float64 {
	v := f.Vector()
	z := w.Bias
	for i := 0; i < featureCount; i++ {
		z += w.Coefficients[i] * v[i]
	}
	return sigmoid(z)
}

// FinalScore blends the raw cosine similarity with the learned re-ranker
// prediction. The blend ratio (0.6 cosine + 0.4 learned) gives the retrieval
// system a smooth upgrade path: even with a poorly-trained model the cosine
// backbone keeps results reasonable.
func (w *RerankerWeights) FinalScore(cosineSim float64, f RerankerFeatures) float64 {
	return 0.6*cosineSim + 0.4*w.Predict(f)
}

// TrainReranker fits a logistic regression model via gradient descent on the
// provided training rows.
//
// Feature standardization is applied internally: each feature is shifted and
// scaled to zero mean / unit variance before training, then the transform is
// baked into the final coefficients so that Predict can be called on raw
// (un-standardized) features.
//
// Returns an error if fewer than minTrainingSamples rows are provided.
func TrainReranker(rows []TrainingRow) (*RerankerWeights, error) {
	n := len(rows)
	if n < minTrainingSamples {
		return nil, fmt.Errorf("reranker: need at least %d training rows, got %d", minTrainingSamples, n)
	}

	// --- Extract feature matrix and compute standardization stats -----------
	mean, std := computeFeatureStats(rows)

	// Build standardized feature matrix (n x featureCount).
	X := make([][featureCount]float64, n)
	for i, row := range rows {
		v := row.Features.Vector()
		for j := 0; j < featureCount; j++ {
			if std[j] > 0 {
				X[i][j] = (v[j] - mean[j]) / std[j]
			}
			// If std == 0 the feature is constant; leave at 0.
		}
	}

	// --- Gradient descent (weighted binary cross-entropy) -------------------
	var coef [featureCount]float64
	bias := 0.0
	prevLoss := math.MaxFloat64

	for iter := 0; iter < maxIterations; iter++ {
		// Accumulate gradients.
		var gradCoef [featureCount]float64
		gradBias := 0.0
		loss := 0.0
		totalWeight := 0.0

		for i := 0; i < n; i++ {
			z := bias
			for j := 0; j < featureCount; j++ {
				z += coef[j] * X[i][j]
			}
			p := sigmoid(z)

			y := rows[i].Label
			w := rows[i].Weight
			if w <= 0 {
				w = 1.0
			}
			totalWeight += w

			// Weighted binary cross-entropy loss.
			// Clamp p to avoid log(0).
			pClamped := math.Max(1e-15, math.Min(1-1e-15, p))
			loss += -w * (y*math.Log(pClamped) + (1-y)*math.Log(1-pClamped))

			// Gradient of loss w.r.t. each parameter.
			err := w * (p - y)
			gradBias += err
			for j := 0; j < featureCount; j++ {
				gradCoef[j] += err * X[i][j]
			}
		}

		// Normalize by total weight.
		if totalWeight > 0 {
			loss /= totalWeight
			gradBias /= totalWeight
			for j := 0; j < featureCount; j++ {
				gradCoef[j] /= totalWeight
			}
		}

		// Parameter update.
		bias -= defaultLearningRate * gradBias
		for j := 0; j < featureCount; j++ {
			coef[j] -= defaultLearningRate * gradCoef[j]
		}

		// Convergence check.
		if math.Abs(prevLoss-loss) < convergenceEps {
			break
		}
		prevLoss = loss
	}

	// --- Un-standardize coefficients ----------------------------------------
	// The model was trained on standardized features:
	//   z = sum(coef_j * (x_j - mean_j) / std_j) + bias
	//     = sum((coef_j / std_j) * x_j) + (bias - sum(coef_j * mean_j / std_j))
	//
	// So for raw features: coef'_j = coef_j / std_j, bias' adjusted accordingly.
	var finalCoef [featureCount]float64
	finalBias := bias
	for j := 0; j < featureCount; j++ {
		if std[j] > 0 {
			finalCoef[j] = coef[j] / std[j]
			finalBias -= coef[j] * mean[j] / std[j]
		}
		// Clamp to [-5, 5].
		finalCoef[j] = clampCoefficient(finalCoef[j])
	}

	return &RerankerWeights{
		Coefficients: finalCoef,
		Bias:         finalBias,
		TrainedAt:    time.Now(),
		SampleCount:  n,
	}, nil
}

// IsStale returns true if the model was trained more than maxAgeDays ago.
func (w *RerankerWeights) IsStale(maxAgeDays int) bool {
	cutoff := time.Now().AddDate(0, 0, -maxAgeDays)
	return w.TrainedAt.Before(cutoff)
}

// Decay moves all coefficients toward zero by the given fraction (0-1).
// A fraction of 0.1 reduces each coefficient by 10% of its current value.
// This is useful for graceful staleness decay when the model has not been
// retrained recently.
func (w *RerankerWeights) Decay(fraction float64) {
	for i := range w.Coefficients {
		w.Coefficients[i] *= (1.0 - fraction)
	}
	w.Bias *= (1.0 - fraction)
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// computeFeatureStats returns the per-feature mean and standard deviation
// across all training rows.
func computeFeatureStats(rows []TrainingRow) (mean, std [featureCount]float64) {
	n := float64(len(rows))
	if n == 0 {
		return
	}

	// Sum.
	for _, row := range rows {
		v := row.Features.Vector()
		for j := 0; j < featureCount; j++ {
			mean[j] += v[j]
		}
	}
	for j := 0; j < featureCount; j++ {
		mean[j] /= n
	}

	// Variance.
	for _, row := range rows {
		v := row.Features.Vector()
		for j := 0; j < featureCount; j++ {
			d := v[j] - mean[j]
			std[j] += d * d
		}
	}
	for j := 0; j < featureCount; j++ {
		std[j] = math.Sqrt(std[j] / n)
	}
	return
}

// clampCoefficient restricts a coefficient to [-coefficientBound, coefficientBound].
func clampCoefficient(v float64) float64 {
	if v > coefficientBound {
		return coefficientBound
	}
	if v < -coefficientBound {
		return -coefficientBound
	}
	return v
}
