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
	// File-level result is inside a ground truth module
	for _, m := range gt.Modules {
		if strings.HasPrefix(path, m) {
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
// covered by the top-K results. A GT file is covered if any result:
//   - matches it exactly (file-level)
//   - is a parent module containing it (module-level)
//   - shares the same parent directory (sibling in same module)
func recallAtK(results []SearchResult, gt GroundTruth, k int) float64 {
	if len(gt.Files) == 0 {
		return 0
	}
	n := k
	if n > len(results) {
		n = len(results)
	}

	// Collect result directories for sibling matching
	resultDirs := make(map[string]bool)
	for i := 0; i < n; i++ {
		rp := results[i].Path
		dir := parentDir(rp)
		resultDirs[dir] = true
	}

	// For each ground truth file, check if any result covers it
	covered := 0
	for _, f := range gt.Files {
		found := false
		for i := 0; i < n; i++ {
			rp := results[i].Path
			// Exact file match
			if rp == f {
				found = true
				break
			}
			// Module-level result contains the GT file
			if strings.HasPrefix(f, rp+"/") {
				found = true
				break
			}
		}
		// Sibling match: a result is in the same directory as the GT file
		if !found && resultDirs[parentDir(f)] {
			found = true
		}
		if found {
			covered++
		}
	}
	return float64(covered) / float64(len(gt.Files))
}

// parentDir returns the directory portion of a path (e.g., "dualmem/hdc.go" → "dualmem").
func parentDir(path string) string {
	idx := strings.LastIndex(path, "/")
	if idx < 0 {
		return "."
	}
	return path[:idx]
}

// ndcgAtK computes NDCG@K with deduplicated relevance scoring.
// Each GT file can only be "claimed" by one result (the first match).
// Strict file match = relevance 2, module match = relevance 1, no match = 0.
func ndcgAtK(results []SearchResult, gt GroundTruth, k int) float64 {
	if len(gt.Files) == 0 && len(gt.Modules) == 0 {
		return 0
	}
	n := k
	if n > len(results) {
		n = len(results)
	}

	// Track which GT files/modules have been claimed by a result
	claimedFiles := make(map[string]bool)
	claimedModules := make(map[string]bool)

	// DCG with deduplication
	dcg := 0.0
	for i := 0; i < n; i++ {
		rel := relevanceScore(results[i].Path, gt, claimedFiles, claimedModules)
		dcg += rel / math.Log2(float64(i+2))
	}

	// Ideal DCG: all strict matches first (rel=2), then module-only matches (rel=1)
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

// relevanceScore computes deduplicated relevance for a result.
// Each GT file/module can only be claimed once (by the first matching result).
func relevanceScore(path string, gt GroundTruth, claimedFiles, claimedModules map[string]bool) float64 {
	// Check exact file match (highest relevance)
	for _, f := range gt.Files {
		if claimedFiles[f] {
			continue
		}
		if path == f {
			claimedFiles[f] = true
			return 2.0
		}
		// Module-level result contains a ground truth file
		if strings.HasPrefix(f, path+"/") {
			claimedFiles[f] = true
			return 2.0
		}
	}
	// Check file-in-module match
	for _, m := range gt.Modules {
		if claimedModules[m] {
			continue
		}
		if strings.HasPrefix(path, m) {
			// File result is inside a GT module — check if it matches a specific GT file
			for _, f := range gt.Files {
				if !claimedFiles[f] && path == f {
					claimedFiles[f] = true
					return 2.0
				}
			}
			// File is in the right module but not a specific GT file — partial credit
			claimedModules[m] = true
			return 1.0
		}
	}
	return 0.0
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
