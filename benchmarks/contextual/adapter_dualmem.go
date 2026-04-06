package main

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// corpusPaths maps corpus names to filesystem paths.
var corpusPaths = map[string]string{
	"geoffreyengram": "/Users/donny/Projects/2026/geoffreyengram",
	"unravelai":      "/tmp/unravelai",
	"learncard":      "/Users/donny/Work/LearnCard",
}

// dualmemBinary is the path to the dualmem CLI.
const dualmemBinary = "/Users/donny/go/bin/dualmem"

// runDualMem executes dualmem search-code for IR metrics.
func runDualMem(q Query) AdapterOutput {
	corpusPath, ok := corpusPaths[q.Corpus]
	if !ok {
		fmt.Printf("  WARNING: unknown corpus %q, skipping dualmem\n", q.Corpus)
		return AdapterOutput{}
	}

	start := time.Now()

	// Run search-code from the corpus directory
	cmd := exec.Command(dualmemBinary, "search-code", q.Query, "--json")
	cmd.Dir = corpusPath
	out, err := cmd.Output()
	latency := time.Since(start).Milliseconds()

	if err != nil {
		fmt.Printf("  WARNING: dualmem search-code failed for %q: %v\n", q.Query, err)
		return AdapterOutput{LatencyMs: latency}
	}

	var raw []struct {
		Path        string  `json:"path"`
		HybridScore float64 `json:"hybrid_score"`
		Similarity  float64 `json:"similarity"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		// Try line-based parsing if not JSON array
		return parseDualMemText(out, latency)
	}

	var results []SearchResult
	for _, r := range raw {
		score := r.HybridScore
		if score == 0 {
			score = r.Similarity
		}
		results = append(results, SearchResult{Path: r.Path, Score: score})
	}

	return AdapterOutput{
		Results:   results,
		LatencyMs: latency,
		APICalls:  0, // HDC is deterministic, no API calls
	}
}

// parseDualMemText handles non-JSON output from search-code.
func parseDualMemText(out []byte, latencyMs int64) AdapterOutput {
	var results []SearchResult
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "=") {
			continue
		}
		// Try to extract path and score from formatted output
		// Format: "  0.826  ./dualmem/  (go, 8 types, ...)"
		parts := strings.Fields(line)
		if len(parts) >= 2 {
			var score float64
			fmt.Sscanf(parts[0], "%f", &score)
			if score > 0 {
				path := strings.TrimSuffix(parts[1], "/")
				path = strings.TrimPrefix(path, "./")
				results = append(results, SearchResult{Path: path, Score: score})
			}
		}
	}
	return AdapterOutput{Results: results, LatencyMs: latencyMs, APICalls: 0}
}

// getDualMemReport runs dualmem consult for report quality evaluation.
func getDualMemReport(q Query) string {
	corpusPath, ok := corpusPaths[q.Corpus]
	if !ok {
		return ""
	}

	cmd := exec.Command(dualmemBinary, "consult", q.Query)
	cmd.Dir = corpusPath
	out, err := cmd.Output()
	if err != nil {
		fmt.Printf("  WARNING: dualmem consult failed for %q: %v\n", q.Query, err)
		return ""
	}
	return string(out)
}
