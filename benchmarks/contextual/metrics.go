package main

import (
	"math"
	"strings"
)

// isStrictMatch checks if a result path matches a ground truth file.
// Handles both directions:
//   - result "dualmem/hdc.go" matches gt file "dualmem/hdc.go" (exact)
//   - result "dualmem" matches gt file "dualmem/hdc.go" (module contains file)
func isStrictMatch(path string, gt GroundTruth) bool {
	for _, f := range gt.Files {
		if path == f {
			return true
		}
		// Module-level result contains a ground truth file
		if strings.HasPrefix(f, path+"/") {
			return true
		}
	}
	return false
}

// isModuleMatch checks if a result path falls within a ground truth module,
// or if a result module contains ground truth module files.
func isModuleMatch(path string, gt GroundTruth) bool {
	for _, m := range gt.Modules {
		// Result is inside a ground truth module
		if strings.HasPrefix(path, m) || strings.HasPrefix(path+"/", m) {
			return true
		}
		// Result module contains ground truth module
		if strings.HasPrefix(m, path+"/") {
			return true
		}
	}
	return false
}

// precisionAtK computes strict precision at K.
func precisionAtK(results []SearchResult, gt GroundTruth, k int) float64 {
	if len(results) == 0 || k == 0 {
		return 0
	}
	n := k
	if n > len(results) {
		n = len(results)
	}
	hits := 0
	for i := 0; i < n; i++ {
		if isStrictMatch(results[i].Path, gt) {
			hits++
		}
	}
	return float64(hits) / float64(n)
}

// recallAtK computes recall at K — counts how many ground truth files are
// covered by the top-K results (including module-level matches).
func recallAtK(results []SearchResult, gt GroundTruth, k int) float64 {
	if len(gt.Files) == 0 {
		return 0
	}
	n := k
	if n > len(results) {
		n = len(results)
	}
	// For each ground truth file, check if any result covers it
	covered := 0
	for _, f := range gt.Files {
		for i := 0; i < n; i++ {
			rp := results[i].Path
			// Exact match or module contains file
			if rp == f || strings.HasPrefix(f, rp+"/") {
				covered++
				break
			}
		}
	}
	return float64(covered) / float64(len(gt.Files))
}

// ndcgAtK computes NDCG@K with module-aware partial relevance.
// Strict file match = relevance 2, module match = relevance 1, no match = 0.
func ndcgAtK(results []SearchResult, gt GroundTruth, k int) float64 {
	if len(gt.Files) == 0 && len(gt.Modules) == 0 {
		return 0
	}
	n := k
	if n > len(results) {
		n = len(results)
	}

	// DCG
	dcg := 0.0
	for i := 0; i < n; i++ {
		rel := 0.0
		if isStrictMatch(results[i].Path, gt) {
			rel = 2.0
		} else if isModuleMatch(results[i].Path, gt) {
			rel = 1.0
		}
		dcg += rel / math.Log2(float64(i+2))
	}

	// Ideal DCG: all strict matches first (rel=2), then module matches (rel=1)
	var idealRels []float64
	for range gt.Files {
		idealRels = append(idealRels, 2.0)
	}
	// Count unique modules not already covered by files
	moduleOnly := 0
	for _, m := range gt.Modules {
		hasFile := false
		for _, f := range gt.Files {
			if strings.HasPrefix(f, m) {
				hasFile = true
				break
			}
		}
		if !hasFile {
			moduleOnly++
		}
	}
	for i := 0; i < moduleOnly; i++ {
		idealRels = append(idealRels, 1.0)
	}

	idcg := 0.0
	idealN := len(idealRels)
	if idealN > k {
		idealN = k
	}
	for i := 0; i < idealN; i++ {
		idcg += idealRels[i] / math.Log2(float64(i+2))
	}

	if idcg == 0 {
		return 0
	}
	return dcg / idcg
}

// computeIRMetrics computes all IR metrics for an adapter's output against ground truth.
func computeIRMetrics(out AdapterOutput, gt GroundTruth) IRMetrics {
	return IRMetrics{
		PrecisionAt3: precisionAtK(out.Results, gt, 3),
		PrecisionAt5: precisionAtK(out.Results, gt, 5),
		RecallAt5:    recallAtK(out.Results, gt, 5),
		RecallAt10:   recallAtK(out.Results, gt, 10),
		NDCG10:       ndcgAtK(out.Results, gt, 10),
		LatencyMs:    out.LatencyMs,
		APICalls:     out.APICalls,
	}
}
