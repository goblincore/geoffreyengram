package main

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"time"
)

// unravelAdapterPath is the path to the Node.js adapter script.
var unravelAdapterPath = filepath.Join("benchmarks", "contextual", "adapters", "unravel.js")

// runUnravel executes UnravelAI search for IR metrics.
func runUnravel(q Query) AdapterOutput {
	corpusPath, ok := corpusPaths[q.Corpus]
	if !ok {
		fmt.Printf("  WARNING: unknown corpus %q, skipping unravel\n", q.Corpus)
		return AdapterOutput{}
	}

	start := time.Now()
	cmd := exec.Command("node", unravelAdapterPath,
		"--mode", "search",
		"--corpus", corpusPath,
		"--query", q.Query)
	out, err := cmd.Output()
	latency := time.Since(start).Milliseconds()

	if err != nil {
		stderr := ""
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = string(ee.Stderr)
		}
		fmt.Printf("  WARNING: unravel search failed for %q: %v %s\n", q.Query, err, stderr)
		return AdapterOutput{LatencyMs: latency}
	}

	var result struct {
		Results  []SearchResult `json:"results"`
		Latency  int64          `json:"latency_ms"`
		APICalls int            `json:"api_calls"`
		Error    string         `json:"error"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		fmt.Printf("  WARNING: unravel parse error: %v\n", err)
		return AdapterOutput{LatencyMs: latency}
	}

	if result.Error != "" {
		fmt.Printf("  WARNING: unravel error: %s\n", result.Error)
	}

	return AdapterOutput{
		Results:   result.Results,
		LatencyMs: result.Latency,
		APICalls:  result.APICalls,
	}
}

// getUnravelReport runs UnravelAI consult for report quality evaluation.
func getUnravelReport(q Query) string {
	corpusPath, ok := corpusPaths[q.Corpus]
	if !ok {
		return ""
	}

	cmd := exec.Command("node", unravelAdapterPath,
		"--mode", "consult",
		"--corpus", corpusPath,
		"--query", q.Query)
	out, err := cmd.Output()
	if err != nil {
		fmt.Printf("  WARNING: unravel consult failed for %q: %v\n", q.Query, err)
		return ""
	}

	var result struct {
		Report  string `json:"report"`
		Latency int64  `json:"latency_ms"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		return string(out)
	}
	return result.Report
}
