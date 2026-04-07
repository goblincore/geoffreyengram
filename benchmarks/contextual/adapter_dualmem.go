package main

import (
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

	// Run search-code from the corpus directory (text output — no --json flag)
	cmd := exec.Command(dualmemBinary, "search-code", q.Query)
	cmd.Dir = corpusPath
	out, err := cmd.Output()
	latency := time.Since(start).Milliseconds()

	if err != nil {
		fmt.Printf("  WARNING: dualmem search-code failed for %q: %v\n", q.Query, err)
		return AdapterOutput{LatencyMs: latency}
	}

	return parseDualMemText(out, latency)
}

// parseDualMemText handles text output from search-code.
// Format: "  1. dualmem/                       score=0.7473 (hdc=0.28 kw=3.45)  Go package dualmem"
func parseDualMemText(out []byte, latencyMs int64) AdapterOutput {
	var results []SearchResult
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		// Match numbered result lines: "N. path/ score=X.XXX ..."
		if len(line) == 0 {
			continue
		}
		// Find "score=" in the line
		scoreIdx := strings.Index(line, "score=")
		if scoreIdx < 0 {
			continue
		}
		// Extract path: between "N. " and first whitespace after path
		dotIdx := strings.Index(line, ". ")
		if dotIdx < 0 {
			continue
		}
		rest := strings.TrimSpace(line[dotIdx+2:])
		pathEnd := strings.IndexByte(rest, ' ')
		if pathEnd < 0 {
			continue
		}
		path := strings.TrimSpace(rest[:pathEnd])
		path = strings.TrimSuffix(path, "/")
		path = strings.TrimPrefix(path, "./")

		// Extract score
		var score float64
		fmt.Sscanf(line[scoreIdx+6:], "%f", &score)
		if score > 0 && path != "" {
			results = append(results, SearchResult{Path: path, Score: score})
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
