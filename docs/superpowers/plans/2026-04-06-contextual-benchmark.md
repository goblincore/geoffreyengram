# Phase 4: Contextual Benchmark Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a benchmark framework comparing dualmem vs UnravelAI on contextual codebase understanding across 3 corpora with IR metrics and LLM-as-judge report quality scoring.

**Architecture:** Go CLI runner that loads query JSONs, runs each query through dualmem (native Go) and UnravelAI (Node.js child process) adapters, computes Precision@K/Recall@K/NDCG@10, and optionally runs Claude Opus as judge for report quality comparison. Results output as JSON + markdown.

**Tech Stack:** Go (benchmark runner, dualmem adapter, judge, aggregator), Node.js (UnravelAI adapter), `claude` CLI (judge), JSON (queries/results)

---

### Task 1: Scaffold Benchmark Module

**Files:**
- Create: `benchmarks/contextual/main.go`
- Create: `benchmarks/contextual/types.go`
- Create: `benchmarks/contextual/results/.gitkeep`

- [ ] **Step 1: Create types.go with shared data structures**

```go
package main

// Query represents a benchmark query with ground truth.
type Query struct {
	ID         string      `json:"id"`
	Corpus     string      `json:"corpus"`
	Type       string      `json:"type"` // feature_location, dependency_tracing, concept_search, bug_localization, structural_understanding, feasibility
	Query      string      `json:"query"`
	Difficulty string      `json:"difficulty"` // easy, medium, hard
	Confidence string      `json:"confidence"` // high, medium, low
	GroundTruth GroundTruth `json:"ground_truth"`
}

// GroundTruth holds the expected answers for a query.
type GroundTruth struct {
	Files       []string `json:"files"`
	Modules     []string `json:"modules"`
	Concepts    []string `json:"concepts"`
	Explanation string   `json:"explanation"`
}

// SearchResult is a single ranked result from an adapter.
type SearchResult struct {
	Path  string  `json:"path"`
	Score float64 `json:"score"`
}

// AdapterOutput is what an adapter returns for a query.
type AdapterOutput struct {
	Results   []SearchResult `json:"results"`
	Report    string         `json:"report"`     // consult/context output text
	LatencyMs int64          `json:"latency_ms"`
	APICalls  int            `json:"api_calls"`
}

// IRMetrics holds computed IR metrics for a single query.
type IRMetrics struct {
	PrecisionAt3  float64 `json:"precision_at_3"`
	PrecisionAt5  float64 `json:"precision_at_5"`
	RecallAt5     float64 `json:"recall_at_5"`
	RecallAt10    float64 `json:"recall_at_10"`
	NDCG10        float64 `json:"ndcg_10"`
	LatencyMs     int64   `json:"latency_ms"`
	APICalls      int     `json:"api_calls"`
}

// JudgeScores holds LLM judge scores for a report.
type JudgeScores struct {
	Accuracy      float64 `json:"accuracy"`
	Completeness  float64 `json:"completeness"`
	Relevance     float64 `json:"relevance"`
	Actionability float64 `json:"actionability"`
	Rationale     string  `json:"rationale"`
}

// HeadToHead holds the result of an A/B comparison.
type HeadToHead struct {
	Winner    string `json:"winner"` // "dualmem", "unravel", "tie"
	Rationale string `json:"rationale"`
}

// QueryResult holds all results for a single query.
type QueryResult struct {
	Query        Query       `json:"query"`
	DualMem      IRMetrics   `json:"dualmem_ir"`
	Unravel      IRMetrics   `json:"unravel_ir"`
	DualMemJudge *JudgeScores `json:"dualmem_judge,omitempty"`
	UnravelJudge *JudgeScores `json:"unravel_judge,omitempty"`
	H2H          *HeadToHead  `json:"head_to_head,omitempty"`
}

// BenchmarkRun is the top-level output structure.
type BenchmarkRun struct {
	RunDate      string                  `json:"run_date"`
	Corpora      []string                `json:"corpora"`
	Summary      map[string]IRMetrics    `json:"summary"`
	ReportQuality map[string]JudgeScores `json:"report_quality,omitempty"`
	H2HSummary   *H2HSummary             `json:"head_to_head_summary,omitempty"`
	ByCorpus     map[string]map[string]IRMetrics `json:"by_corpus"`
	ByQueryType  map[string]map[string]IRMetrics `json:"by_query_type"`
	ByDifficulty map[string]map[string]IRMetrics `json:"by_difficulty"`
	PerQuery     []QueryResult           `json:"per_query"`
}

// H2HSummary aggregates head-to-head results.
type H2HSummary struct {
	DualMemWins int `json:"dualmem_wins"`
	UnravelWins int `json:"unravel_wins"`
	Ties        int `json:"ties"`
}
```

- [ ] **Step 2: Create main.go with flag parsing and orchestration skeleton**

```go
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	corpora := flag.String("corpora", "geoffreyengram,unravelai,learncard", "Comma-separated corpus names")
	outputDir := flag.String("output", "benchmarks/contextual/results", "Output directory")
	skipJudge := flag.Bool("skip-judge", false, "Skip LLM judge (IR metrics only)")
	skipUnravel := flag.Bool("skip-unravel", false, "Skip UnravelAI (dualmem only)")
	corpusOnly := flag.String("corpus-only", "", "Run single corpus")
	queryType := flag.String("query-type", "", "Run one query type only")
	verbose := flag.Bool("verbose", false, "Log per-query details")
	flag.Parse()

	corpusList := strings.Split(*corpora, ",")
	if *corpusOnly != "" {
		corpusList = []string{*corpusOnly}
	}

	// Load queries
	allQueries, err := loadQueries(corpusList, *queryType)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading queries: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Loaded %d queries across %d corpora\n", len(allQueries), len(corpusList))

	// Run benchmark
	var results []QueryResult
	for i, q := range allQueries {
		if *verbose {
			fmt.Printf("[%d/%d] %s: %s\n", i+1, len(allQueries), q.Corpus, q.Query)
		}

		qr := QueryResult{Query: q}

		// DualMem
		dmOut := runDualMem(q)
		qr.DualMem = computeIRMetrics(dmOut, q.GroundTruth)

		// UnravelAI
		if !*skipUnravel {
			unOut := runUnravel(q)
			qr.Unravel = computeIRMetrics(unOut, q.GroundTruth)
		}

		// Judge
		if !*skipJudge {
			dmReport := getDualMemReport(q)
			qr.DualMemJudge = judgeReport(q, dmReport)

			if !*skipUnravel {
				unReport := getUnravelReport(q)
				qr.UnravelJudge = judgeReport(q, unReport)
				qr.H2H = judgeHeadToHead(q, dmReport, unReport)
			}
		}

		results = append(results, qr)

		if *verbose {
			fmt.Printf("  dualmem: P@3=%.2f R@5=%.2f NDCG=%.2f %dms\n",
				qr.DualMem.PrecisionAt3, qr.DualMem.RecallAt5, qr.DualMem.NDCG10, qr.DualMem.LatencyMs)
			if !*skipUnravel {
				fmt.Printf("  unravel: P@3=%.2f R@5=%.2f NDCG=%.2f %dms\n",
					qr.Unravel.PrecisionAt3, qr.Unravel.RecallAt5, qr.Unravel.NDCG10, qr.Unravel.LatencyMs)
			}
		}
	}

	// Aggregate and output
	run := aggregate(results, corpusList, *skipJudge, *skipUnravel)
	writeResults(run, *outputDir)
}

// loadQueries reads query JSON files for the given corpora.
func loadQueries(corpora []string, filterType string) ([]Query, error) {
	var all []Query
	for _, corpus := range corpora {
		path := filepath.Join("benchmarks", "contextual", "queries", corpus+".json")
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		var queries []Query
		if err := json.Unmarshal(data, &queries); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		for _, q := range queries {
			if filterType == "" || q.Type == filterType {
				all = append(all, q)
			}
		}
	}
	return all, nil
}

// writeResults saves JSON and markdown outputs.
func writeResults(run *BenchmarkRun, outputDir string) {
	os.MkdirAll(outputDir, 0755)
	date := time.Now().Format("2006-01-02")

	// JSON
	jsonPath := filepath.Join(outputDir, date+"-run.json")
	data, _ := json.MarshalIndent(run, "", "  ")
	os.WriteFile(jsonPath, data, 0644)
	fmt.Printf("\nResults written to %s\n", jsonPath)

	// Markdown
	mdPath := filepath.Join(outputDir, date+"-run.md")
	md := formatMarkdown(run)
	os.WriteFile(mdPath, []byte(md), 0644)
	fmt.Printf("Report written to %s\n", mdPath)
}
```

- [ ] **Step 3: Create .gitkeep for results directory**

```bash
mkdir -p benchmarks/contextual/results
touch benchmarks/contextual/results/.gitkeep
echo "results/*.json" >> benchmarks/contextual/.gitignore
echo "results/*.md" >> benchmarks/contextual/.gitignore
```

- [ ] **Step 4: Verify it compiles (with stub functions)**

Add temporary stubs at the bottom of `main.go` so it compiles:

```go
func runDualMem(q Query) AdapterOutput      { return AdapterOutput{} }
func runUnravel(q Query) AdapterOutput       { return AdapterOutput{} }
func getDualMemReport(q Query) string        { return "" }
func getUnravelReport(q Query) string        { return "" }
func judgeReport(q Query, report string) *JudgeScores { return nil }
func judgeHeadToHead(q Query, a, b string) *HeadToHead { return nil }
func computeIRMetrics(out AdapterOutput, gt GroundTruth) IRMetrics { return IRMetrics{} }
func aggregate(results []QueryResult, corpora []string, skipJudge, skipUnravel bool) *BenchmarkRun { return &BenchmarkRun{} }
func formatMarkdown(run *BenchmarkRun) string { return "" }
```

Run: `go build ./benchmarks/contextual/`
Expected: compiles without errors

- [ ] **Step 5: Commit**

```bash
git add benchmarks/contextual/
git commit -m "feat(bench): scaffold contextual benchmark module with types and CLI"
```

---

### Task 2: IR Metrics Computation

**Files:**
- Create: `benchmarks/contextual/metrics.go`
- Create: `benchmarks/contextual/metrics_test.go`

- [ ] **Step 1: Write failing tests for IR metrics**

```go
package main

import (
	"math"
	"testing"
)

func TestPrecisionAtK(t *testing.T) {
	results := []SearchResult{
		{Path: "auth/jwt.go", Score: 0.9},
		{Path: "auth/middleware.go", Score: 0.8},
		{Path: "ui/button.go", Score: 0.7},
		{Path: "auth/oauth.go", Score: 0.6},
		{Path: "db/store.go", Score: 0.5},
	}
	gt := GroundTruth{
		Files:   []string{"auth/jwt.go", "auth/middleware.go", "auth/oauth.go"},
		Modules: []string{"auth/"},
	}

	p3 := precisionAtK(results, gt, 3)
	if math.Abs(p3-2.0/3.0) > 0.01 {
		t.Errorf("Precision@3: got %.3f, want %.3f", p3, 2.0/3.0)
	}

	p5 := precisionAtK(results, gt, 5)
	if math.Abs(p5-3.0/5.0) > 0.01 {
		t.Errorf("Precision@5: got %.3f, want %.3f", p5, 3.0/5.0)
	}
}

func TestRecallAtK(t *testing.T) {
	results := []SearchResult{
		{Path: "auth/jwt.go", Score: 0.9},
		{Path: "ui/button.go", Score: 0.8},
		{Path: "auth/middleware.go", Score: 0.7},
	}
	gt := GroundTruth{
		Files:   []string{"auth/jwt.go", "auth/middleware.go", "auth/oauth.go"},
		Modules: []string{"auth/"},
	}

	r5 := recallAtK(results, gt, 5)
	if math.Abs(r5-2.0/3.0) > 0.01 {
		t.Errorf("Recall@5: got %.3f, want %.3f", r5, 2.0/3.0)
	}
}

func TestNDCGAt10(t *testing.T) {
	// Perfect ordering: all relevant results first
	perfect := []SearchResult{
		{Path: "auth/jwt.go", Score: 0.9},
		{Path: "auth/middleware.go", Score: 0.8},
		{Path: "auth/oauth.go", Score: 0.7},
	}
	gt := GroundTruth{
		Files:   []string{"auth/jwt.go", "auth/middleware.go", "auth/oauth.go"},
		Modules: []string{"auth/"},
	}

	ndcg := ndcgAtK(perfect, gt, 10)
	if math.Abs(ndcg-1.0) > 0.01 {
		t.Errorf("NDCG@10 perfect: got %.3f, want 1.0", ndcg)
	}

	// Imperfect: relevant result at position 3 instead of 1
	imperfect := []SearchResult{
		{Path: "ui/button.go", Score: 0.9},
		{Path: "db/store.go", Score: 0.8},
		{Path: "auth/jwt.go", Score: 0.7},
	}
	gt2 := GroundTruth{Files: []string{"auth/jwt.go"}, Modules: []string{"auth/"}}
	ndcg2 := ndcgAtK(imperfect, gt2, 10)
	if ndcg2 >= 1.0 || ndcg2 <= 0.0 {
		t.Errorf("NDCG@10 imperfect: got %.3f, want between 0 and 1", ndcg2)
	}
}

func TestModuleMatch(t *testing.T) {
	results := []SearchResult{
		{Path: "dualmem/hdc.go", Score: 0.9},
	}
	gt := GroundTruth{
		Files:   []string{"dualmem/codemap.go"}, // strict miss
		Modules: []string{"dualmem/"},            // module hit
	}

	// Strict precision should be 0 (wrong file)
	p := precisionAtK(results, gt, 3)
	if p != 0.0 {
		t.Errorf("Strict precision should be 0 for wrong file, got %.3f", p)
	}

	// Module-aware NDCG should give partial credit
	ndcg := ndcgAtK(results, gt, 10)
	if ndcg <= 0.0 {
		t.Errorf("Module-aware NDCG should give partial credit, got %.3f", ndcg)
	}
}

func TestComputeIRMetrics(t *testing.T) {
	out := AdapterOutput{
		Results: []SearchResult{
			{Path: "auth/jwt.go", Score: 0.9},
			{Path: "auth/middleware.go", Score: 0.8},
			{Path: "ui/button.go", Score: 0.7},
		},
		LatencyMs: 42,
		APICalls:  0,
	}
	gt := GroundTruth{
		Files:   []string{"auth/jwt.go", "auth/middleware.go"},
		Modules: []string{"auth/"},
	}

	m := computeIRMetrics(out, gt)
	if m.LatencyMs != 42 {
		t.Errorf("latency: got %d, want 42", m.LatencyMs)
	}
	if m.PrecisionAt3 < 0.5 {
		t.Errorf("P@3 too low: %.3f", m.PrecisionAt3)
	}
	if m.RecallAt5 < 0.9 {
		t.Errorf("R@5 too low: %.3f", m.RecallAt5)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./benchmarks/contextual/ -run TestPrecision -v`
Expected: FAIL — `precisionAtK` undefined

- [ ] **Step 3: Implement metrics.go**

```go
package main

import (
	"math"
	"strings"
)

// isStrictMatch checks if a result path exactly matches a ground truth file.
func isStrictMatch(path string, gt GroundTruth) bool {
	for _, f := range gt.Files {
		if path == f {
			return true
		}
	}
	return false
}

// isModuleMatch checks if a result path falls within a ground truth module.
func isModuleMatch(path string, gt GroundTruth) bool {
	for _, m := range gt.Modules {
		if strings.HasPrefix(path, m) {
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

// recallAtK computes strict recall at K.
func recallAtK(results []SearchResult, gt GroundTruth, k int) float64 {
	if len(gt.Files) == 0 {
		return 0
	}
	n := k
	if n > len(results) {
		n = len(results)
	}
	found := make(map[string]bool)
	for i := 0; i < n; i++ {
		if isStrictMatch(results[i].Path, gt) {
			found[results[i].Path] = true
		}
	}
	return float64(len(found)) / float64(len(gt.Files))
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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./benchmarks/contextual/ -run 'TestPrecision|TestRecall|TestNDCG|TestModule|TestCompute' -v`
Expected: all PASS

- [ ] **Step 5: Commit**

```bash
git add benchmarks/contextual/metrics.go benchmarks/contextual/metrics_test.go
git commit -m "feat(bench): add IR metrics computation (Precision@K, Recall@K, NDCG@10)"
```

---

### Task 3: DualMem Adapter

**Files:**
- Create: `benchmarks/contextual/adapters/dualmem.go`
- Modify: `benchmarks/contextual/main.go` (replace stubs)

- [ ] **Step 1: Create dualmem adapter**

```go
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
		Path       string  `json:"path"`
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
```

- [ ] **Step 2: Remove the runDualMem and getDualMemReport stubs from main.go**

Remove these lines from main.go:
```go
func runDualMem(q Query) AdapterOutput      { return AdapterOutput{} }
func getDualMemReport(q Query) string        { return "" }
```

- [ ] **Step 3: Verify it compiles**

Run: `go build ./benchmarks/contextual/`
Expected: compiles without errors

- [ ] **Step 4: Commit**

```bash
git add benchmarks/contextual/
git commit -m "feat(bench): add dualmem adapter (search-code + consult)"
```

---

### Task 4: UnravelAI Adapter

**Files:**
- Create: `benchmarks/contextual/adapters/unravel.js`
- Create: `benchmarks/contextual/adapters/unravel.go`
- Modify: `benchmarks/contextual/main.go` (replace stubs)

- [ ] **Step 1: Create the Node.js adapter script**

```javascript
#!/usr/bin/env node
// benchmarks/contextual/adapters/unravel.js
//
// Adapter for UnravelAI — called by the Go benchmark runner.
// Two modes:
//   --mode search  → library-level graph search (for IR metrics)
//   --mode consult → MCP-level consult (for report quality)
//
// Usage:
//   node unravel.js --mode search --corpus /path/to/repo --query "where is auth?"
//   node unravel.js --mode consult --corpus /path/to/repo --query "where is auth?"

import { GraphBuilder } from '/tmp/unravelai/unravel-mcp/core/graph-builder.js';
import { SearchEngine, queryGraphForFiles } from '/tmp/unravelai/unravel-mcp/core/search.js';
import { parseCode } from '/tmp/unravelai/unravel-mcp/core/ast-engine-ts.js';
import fs from 'fs';
import path from 'path';
import { execSync } from 'child_process';

const args = process.argv.slice(2);
function getArg(name) {
  const idx = args.indexOf('--' + name);
  return idx >= 0 && idx + 1 < args.length ? args[idx + 1] : null;
}

const mode = getArg('mode') || 'search';
const corpusPath = getArg('corpus');
const query = getArg('query');

if (!corpusPath || !query) {
  console.error(JSON.stringify({ error: 'Missing --corpus or --query' }));
  process.exit(1);
}

// Recursively find source files
function findSourceFiles(dir, exts = ['.ts', '.tsx', '.js', '.jsx', '.go', '.py', '.rs']) {
  const results = [];
  try {
    const entries = fs.readdirSync(dir, { withFileTypes: true });
    for (const entry of entries) {
      const full = path.join(dir, entry.name);
      if (entry.isDirectory()) {
        if (['node_modules', '.git', 'dist', 'build', '.next', 'vendor'].includes(entry.name)) continue;
        results.push(...findSourceFiles(full, exts));
      } else if (exts.some(ext => entry.name.endsWith(ext))) {
        results.push(full);
      }
    }
  } catch (e) { /* skip unreadable dirs */ }
  return results;
}

async function runSearch() {
  const start = Date.now();

  // Build knowledge graph
  const builder = new GraphBuilder(path.basename(corpusPath));
  const files = findSourceFiles(corpusPath);

  for (const filePath of files) {
    const relPath = path.relative(corpusPath, filePath);
    try {
      const code = fs.readFileSync(filePath, 'utf-8');
      // Try AST analysis for TS/JS files
      if (filePath.match(/\.(ts|tsx|js|jsx)$/)) {
        try {
          const analysis = parseCode(code, filePath);
          builder.addFileWithAnalysis(relPath, analysis, null);
        } catch {
          builder.addFile(relPath, '', [], 'unknown');
        }
      } else {
        builder.addFile(relPath, '', [], 'unknown');
      }
    } catch {
      builder.addFile(relPath, '', [], 'unknown');
    }
  }

  const graph = builder.build();

  // Query the graph
  const searchResults = queryGraphForFiles(graph, query, 10);
  const latency = Date.now() - start;

  const results = (searchResults || []).map(r => ({
    path: r.filePath || r.file || r.name || '',
    score: r.score || r.relevance || 0,
  }));

  console.log(JSON.stringify({ results, latency_ms: latency, api_calls: 0 }));
}

async function runConsult() {
  // For MCP consult, we spawn the MCP server and send a tool call
  // This is more complex — for now, use the search results + basic formatting
  // as UnravelAI's consult is tightly coupled to MCP transport
  const start = Date.now();

  try {
    // Try MCP stdio approach
    const { spawn } = await import('child_process');
    const mcp = spawn('node', ['/tmp/unravelai/unravel-mcp/index.js'], {
      stdio: ['pipe', 'pipe', 'pipe'],
      cwd: corpusPath,
    });

    const toolCall = {
      jsonrpc: '2.0',
      id: 1,
      method: 'tools/call',
      params: {
        name: 'unravel.consult',
        arguments: { query, scope: corpusPath },
      },
    };

    // Initialize MCP
    const initMsg = {
      jsonrpc: '2.0',
      id: 0,
      method: 'initialize',
      params: {
        protocolVersion: '2024-11-05',
        capabilities: {},
        clientInfo: { name: 'benchmark', version: '1.0.0' },
      },
    };

    let response = '';
    mcp.stdout.on('data', (data) => { response += data.toString(); });

    // Send init, wait, send tool call
    mcp.stdin.write(JSON.stringify(initMsg) + '\n');

    await new Promise(resolve => setTimeout(resolve, 2000));
    mcp.stdin.write(JSON.stringify(toolCall) + '\n');
    await new Promise(resolve => setTimeout(resolve, 10000));

    mcp.kill();

    const latency = Date.now() - start;

    // Try to parse the last JSON-RPC response
    const lines = response.split('\n').filter(l => l.trim());
    let report = '';
    for (const line of lines.reverse()) {
      try {
        const parsed = JSON.parse(line);
        if (parsed.result?.content) {
          report = parsed.result.content.map(c => c.text || '').join('\n');
          break;
        }
      } catch { continue; }
    }

    console.log(JSON.stringify({ report, latency_ms: latency }));
  } catch (e) {
    const latency = Date.now() - start;
    console.log(JSON.stringify({ report: '', latency_ms: latency, error: e.message }));
  }
}

if (mode === 'search') {
  runSearch().catch(e => {
    console.error(JSON.stringify({ error: e.message }));
    process.exit(1);
  });
} else if (mode === 'consult') {
  runConsult().catch(e => {
    console.error(JSON.stringify({ error: e.message }));
    process.exit(1);
  });
} else {
  console.error(JSON.stringify({ error: `Unknown mode: ${mode}` }));
  process.exit(1);
}
```

- [ ] **Step 2: Create the Go-side UnravelAI adapter**

```go
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
```

- [ ] **Step 3: Remove the runUnravel and getUnravelReport stubs from main.go**

Remove these lines from main.go:
```go
func runUnravel(q Query) AdapterOutput       { return AdapterOutput{} }
func getUnravelReport(q Query) string        { return "" }
```

- [ ] **Step 4: Verify both adapters compile**

Run: `go build ./benchmarks/contextual/`
Expected: compiles without errors

- [ ] **Step 5: Commit**

```bash
git add benchmarks/contextual/adapters/
git commit -m "feat(bench): add UnravelAI adapter (library search + MCP consult)"
```

---

### Task 5: LLM Judge via Claude CLI

**Files:**
- Create: `benchmarks/contextual/judge.go`
- Modify: `benchmarks/contextual/main.go` (replace stubs)

- [ ] **Step 1: Implement judge.go**

```go
package main

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// judgeReport scores a single report using Claude Opus via the claude CLI.
func judgeReport(q Query, report string) *JudgeScores {
	if report == "" {
		return &JudgeScores{}
	}

	prompt := fmt.Sprintf(`You are evaluating a codebase understanding report. Score it on 4 dimensions (1-5 each).

QUERY: %s

GROUND TRUTH (reference answer):
Files: %s
Concepts: %s
Explanation: %s

REPORT TO EVALUATE:
%s

Score each dimension 1-5:
- Accuracy: Are stated facts correct?
- Completeness: Does it cover the relevant files and concepts from ground truth?
- Relevance: Is noise minimized? Good signal-to-noise ratio?
- Actionability: Could a developer act on this immediately?

Respond ONLY with JSON (no markdown):
{"accuracy": N, "completeness": N, "relevance": N, "actionability": N, "rationale": "brief explanation"}`,
		q.Query,
		strings.Join(q.GroundTruth.Files, ", "),
		strings.Join(q.GroundTruth.Concepts, ", "),
		q.GroundTruth.Explanation,
		truncateReport(report, 3000))

	out := runClaude(prompt)
	if out == "" {
		return &JudgeScores{}
	}

	var scores JudgeScores
	// Extract JSON from response (claude may wrap it in text)
	jsonStr := extractJSON(out)
	if err := json.Unmarshal([]byte(jsonStr), &scores); err != nil {
		fmt.Printf("  WARNING: judge parse error: %v\nRaw: %s\n", err, out[:min(200, len(out))])
		return &JudgeScores{}
	}
	return &scores
}

// judgeHeadToHead compares two reports anonymously using Claude Opus.
func judgeHeadToHead(q Query, reportA, reportB string) *HeadToHead {
	if reportA == "" && reportB == "" {
		return &HeadToHead{Winner: "tie", Rationale: "both reports empty"}
	}

	// Shuffle A/B to avoid position bias (deterministic per query ID)
	aFirst := len(q.ID)%2 == 0
	var first, second, firstLabel, secondLabel string
	if aFirst {
		first, second = reportA, reportB
		firstLabel, secondLabel = "dualmem", "unravel"
	} else {
		first, second = reportB, reportA
		firstLabel, secondLabel = "unravel", "dualmem"
	}

	prompt := fmt.Sprintf(`You are comparing two codebase understanding reports for the same query. Pick which is more useful for a developer.

QUERY: %s

GROUND TRUTH:
Files: %s
Explanation: %s

REPORT A:
%s

REPORT B:
%s

Which report is more useful for answering the query? Consider accuracy, completeness, relevance, and actionability.

Respond ONLY with JSON (no markdown):
{"winner": "A" or "B" or "tie", "rationale": "brief explanation"}`,
		q.Query,
		strings.Join(q.GroundTruth.Files, ", "),
		q.GroundTruth.Explanation,
		truncateReport(first, 2000),
		truncateReport(second, 2000))

	out := runClaude(prompt)
	if out == "" {
		return &HeadToHead{Winner: "tie", Rationale: "judge failed"}
	}

	var result struct {
		Winner    string `json:"winner"`
		Rationale string `json:"rationale"`
	}
	jsonStr := extractJSON(out)
	if err := json.Unmarshal([]byte(jsonStr), &result); err != nil {
		return &HeadToHead{Winner: "tie", Rationale: "parse error"}
	}

	// Map A/B back to system names
	winner := "tie"
	switch strings.ToUpper(result.Winner) {
	case "A":
		winner = firstLabel
	case "B":
		winner = secondLabel
	}

	return &HeadToHead{Winner: winner, Rationale: result.Rationale}
}

// runClaude executes a prompt via the claude CLI and returns the response.
func runClaude(prompt string) string {
	cmd := exec.Command("claude", "-p", prompt, "--model", "claude-opus-4-6")
	out, err := cmd.Output()
	if err != nil {
		stderr := ""
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = string(ee.Stderr)
		}
		fmt.Printf("  WARNING: claude CLI failed: %v %s\n", err, stderr)
		return ""
	}
	return strings.TrimSpace(string(out))
}

// truncateReport truncates a report to maxChars.
func truncateReport(report string, maxChars int) string {
	if len(report) <= maxChars {
		return report
	}
	return report[:maxChars] + "\n... (truncated)"
}

// extractJSON finds the first JSON object in a string.
func extractJSON(s string) string {
	start := strings.Index(s, "{")
	if start < 0 {
		return s
	}
	// Find matching closing brace
	depth := 0
	for i := start; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return s[start:]
}

// min returns the smaller of two ints.
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// parseScore extracts a numeric score from a string like "4" or "4.5".
func parseScore(s string) float64 {
	s = strings.TrimSpace(s)
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return v
}
```

- [ ] **Step 2: Remove the judge stubs from main.go**

Remove these lines from main.go:
```go
func judgeReport(q Query, report string) *JudgeScores { return nil }
func judgeHeadToHead(q Query, a, b string) *HeadToHead { return nil }
```

- [ ] **Step 3: Verify it compiles**

Run: `go build ./benchmarks/contextual/`
Expected: compiles without errors

- [ ] **Step 4: Commit**

```bash
git add benchmarks/contextual/judge.go
git commit -m "feat(bench): add LLM judge via claude CLI (dimensional + head-to-head)"
```

---

### Task 6: Aggregation & Output

**Files:**
- Create: `benchmarks/contextual/aggregate.go`
- Modify: `benchmarks/contextual/main.go` (replace stubs)

- [ ] **Step 1: Implement aggregate.go**

```go
package main

import (
	"fmt"
	"strings"
	"time"
)

// aggregate computes summary statistics from per-query results.
func aggregate(results []QueryResult, corpora []string, skipJudge, skipUnravel bool) *BenchmarkRun {
	run := &BenchmarkRun{
		RunDate:      time.Now().Format("2006-01-02"),
		Corpora:      corpora,
		Summary:      make(map[string]IRMetrics),
		ByCorpus:     make(map[string]map[string]IRMetrics),
		ByQueryType:  make(map[string]map[string]IRMetrics),
		ByDifficulty: make(map[string]map[string]IRMetrics),
		PerQuery:     results,
	}

	systems := []string{"dualmem"}
	if !skipUnravel {
		systems = append(systems, "unravel")
	}

	// Overall summary
	for _, sys := range systems {
		run.Summary[sys] = avgIR(results, sys, func(q QueryResult) bool { return true })
	}

	// By corpus
	for _, corpus := range corpora {
		run.ByCorpus[corpus] = make(map[string]IRMetrics)
		for _, sys := range systems {
			run.ByCorpus[corpus][sys] = avgIR(results, sys, func(q QueryResult) bool {
				return q.Query.Corpus == corpus
			})
		}
	}

	// By query type
	queryTypes := []string{"feature_location", "dependency_tracing", "concept_search", "bug_localization", "structural_understanding", "feasibility"}
	for _, qt := range queryTypes {
		run.ByQueryType[qt] = make(map[string]IRMetrics)
		for _, sys := range systems {
			run.ByQueryType[qt][sys] = avgIR(results, sys, func(q QueryResult) bool {
				return q.Query.Type == qt
			})
		}
	}

	// By difficulty
	for _, diff := range []string{"easy", "medium", "hard"} {
		run.ByDifficulty[diff] = make(map[string]IRMetrics)
		for _, sys := range systems {
			run.ByDifficulty[diff][sys] = avgIR(results, sys, func(q QueryResult) bool {
				return q.Query.Difficulty == diff
			})
		}
	}

	// Report quality
	if !skipJudge {
		run.ReportQuality = make(map[string]JudgeScores)
		run.ReportQuality["dualmem"] = avgJudge(results, func(q QueryResult) *JudgeScores { return q.DualMemJudge })
		if !skipUnravel {
			run.ReportQuality["unravel"] = avgJudge(results, func(q QueryResult) *JudgeScores { return q.UnravelJudge })
			run.H2HSummary = countH2H(results)
		}
	}

	return run
}

// avgIR averages IR metrics for a system across filtered results.
func avgIR(results []QueryResult, system string, filter func(QueryResult) bool) IRMetrics {
	var sum IRMetrics
	count := 0
	for _, r := range results {
		if !filter(r) {
			continue
		}
		var m IRMetrics
		if system == "dualmem" {
			m = r.DualMem
		} else {
			m = r.Unravel
		}
		sum.PrecisionAt3 += m.PrecisionAt3
		sum.PrecisionAt5 += m.PrecisionAt5
		sum.RecallAt5 += m.RecallAt5
		sum.RecallAt10 += m.RecallAt10
		sum.NDCG10 += m.NDCG10
		sum.LatencyMs += m.LatencyMs
		sum.APICalls += m.APICalls
		count++
	}
	if count == 0 {
		return IRMetrics{}
	}
	n := float64(count)
	return IRMetrics{
		PrecisionAt3: sum.PrecisionAt3 / n,
		PrecisionAt5: sum.PrecisionAt5 / n,
		RecallAt5:    sum.RecallAt5 / n,
		RecallAt10:   sum.RecallAt10 / n,
		NDCG10:       sum.NDCG10 / n,
		LatencyMs:    sum.LatencyMs / int64(count),
		APICalls:     sum.APICalls,
	}
}

// avgJudge averages judge scores across results.
func avgJudge(results []QueryResult, getter func(QueryResult) *JudgeScores) JudgeScores {
	var sum JudgeScores
	count := 0
	for _, r := range results {
		j := getter(r)
		if j == nil {
			continue
		}
		sum.Accuracy += j.Accuracy
		sum.Completeness += j.Completeness
		sum.Relevance += j.Relevance
		sum.Actionability += j.Actionability
		count++
	}
	if count == 0 {
		return JudgeScores{}
	}
	n := float64(count)
	return JudgeScores{
		Accuracy:      sum.Accuracy / n,
		Completeness:  sum.Completeness / n,
		Relevance:     sum.Relevance / n,
		Actionability: sum.Actionability / n,
	}
}

// countH2H tallies head-to-head results.
func countH2H(results []QueryResult) *H2HSummary {
	s := &H2HSummary{}
	for _, r := range results {
		if r.H2H == nil {
			continue
		}
		switch r.H2H.Winner {
		case "dualmem":
			s.DualMemWins++
		case "unravel":
			s.UnravelWins++
		default:
			s.Ties++
		}
	}
	return s
}

// formatMarkdown generates a markdown report from benchmark results.
func formatMarkdown(run *BenchmarkRun) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("# Contextual Benchmark Results — %s\n\n", run.RunDate))

	// Overall summary
	sb.WriteString("## Overall Summary\n\n")
	sb.WriteString("| System | P@3 | P@5 | R@5 | R@10 | NDCG@10 | Latency (ms) | API Calls |\n")
	sb.WriteString("|--------|-----|-----|-----|------|---------|-------------|----------|\n")
	for _, sys := range []string{"dualmem", "unravel"} {
		m, ok := run.Summary[sys]
		if !ok {
			continue
		}
		sb.WriteString(fmt.Sprintf("| %s | %.2f | %.2f | %.2f | %.2f | %.2f | %d | %d |\n",
			sys, m.PrecisionAt3, m.PrecisionAt5, m.RecallAt5, m.RecallAt10, m.NDCG10, m.LatencyMs, m.APICalls))
	}
	sb.WriteString("\n")

	// By corpus
	sb.WriteString("## By Corpus\n\n")
	for corpus, systems := range run.ByCorpus {
		sb.WriteString(fmt.Sprintf("### %s\n\n", corpus))
		sb.WriteString("| System | P@3 | P@5 | R@5 | R@10 | NDCG@10 | Latency |\n")
		sb.WriteString("|--------|-----|-----|-----|------|---------|--------|\n")
		for _, sys := range []string{"dualmem", "unravel"} {
			m, ok := systems[sys]
			if !ok {
				continue
			}
			sb.WriteString(fmt.Sprintf("| %s | %.2f | %.2f | %.2f | %.2f | %.2f | %dms |\n",
				sys, m.PrecisionAt3, m.PrecisionAt5, m.RecallAt5, m.RecallAt10, m.NDCG10, m.LatencyMs))
		}
		sb.WriteString("\n")
	}

	// By query type
	sb.WriteString("## By Query Type\n\n")
	sb.WriteString("| Type | System | P@3 | R@5 | NDCG@10 |\n")
	sb.WriteString("|------|--------|-----|-----|--------|\n")
	for qt, systems := range run.ByQueryType {
		for _, sys := range []string{"dualmem", "unravel"} {
			m, ok := systems[sys]
			if !ok {
				continue
			}
			sb.WriteString(fmt.Sprintf("| %s | %s | %.2f | %.2f | %.2f |\n",
				qt, sys, m.PrecisionAt3, m.RecallAt5, m.NDCG10))
		}
	}
	sb.WriteString("\n")

	// Report quality
	if run.ReportQuality != nil {
		sb.WriteString("## Report Quality (LLM Judge)\n\n")
		sb.WriteString("| System | Accuracy | Completeness | Relevance | Actionability |\n")
		sb.WriteString("|--------|----------|-------------|-----------|-------------|\n")
		for _, sys := range []string{"dualmem", "unravel"} {
			j, ok := run.ReportQuality[sys]
			if !ok {
				continue
			}
			sb.WriteString(fmt.Sprintf("| %s | %.1f | %.1f | %.1f | %.1f |\n",
				sys, j.Accuracy, j.Completeness, j.Relevance, j.Actionability))
		}
		sb.WriteString("\n")
	}

	// Head-to-head
	if run.H2HSummary != nil {
		sb.WriteString("## Head-to-Head\n\n")
		total := run.H2HSummary.DualMemWins + run.H2HSummary.UnravelWins + run.H2HSummary.Ties
		sb.WriteString(fmt.Sprintf("- DualMem wins: **%d** (%.0f%%)\n", run.H2HSummary.DualMemWins, pct(run.H2HSummary.DualMemWins, total)))
		sb.WriteString(fmt.Sprintf("- Unravel wins: **%d** (%.0f%%)\n", run.H2HSummary.UnravelWins, pct(run.H2HSummary.UnravelWins, total)))
		sb.WriteString(fmt.Sprintf("- Ties: **%d** (%.0f%%)\n", run.H2HSummary.Ties, pct(run.H2HSummary.Ties, total)))
		sb.WriteString("\n")
	}

	return sb.String()
}

func pct(n, total int) float64 {
	if total == 0 {
		return 0
	}
	return float64(n) / float64(total) * 100
}
```

- [ ] **Step 2: Remove the aggregate and formatMarkdown stubs from main.go**

Remove these lines from main.go:
```go
func aggregate(results []QueryResult, corpora []string, skipJudge, skipUnravel bool) *BenchmarkRun { return &BenchmarkRun{} }
func formatMarkdown(run *BenchmarkRun) string { return "" }
```

- [ ] **Step 3: Verify it compiles**

Run: `go build ./benchmarks/contextual/`
Expected: compiles without errors

- [ ] **Step 4: Commit**

```bash
git add benchmarks/contextual/aggregate.go
git commit -m "feat(bench): add aggregation and markdown report output"
```

---

### Task 7: Ground Truth — geoffreyengram Queries

**Files:**
- Create: `benchmarks/contextual/queries/geoffreyengram.json`

- [ ] **Step 1: Generate geoffreyengram ground truth**

Explore the codebase thoroughly and generate 14 queries with ground truth. Use `dualmem search-code`, `dualmem map`, file reads, and grep to verify each answer. Tag confidence levels.

The queries must cover:
- 3 feature_location (easy/medium)
- 2 dependency_tracing (medium/hard)
- 3 concept_search (easy/medium)
- 2 bug_localization (medium/hard)
- 2 structural_understanding (medium)
- 2 feasibility (hard)

Write the JSON file with all 14 entries following the Query schema from types.go.

- [ ] **Step 2: Validate JSON parses correctly**

```bash
cd /Users/donny/Projects/2026/geoffreyengram
go run -C benchmarks/contextual/ . --corpus-only geoffreyengram --skip-judge --skip-unravel 2>&1 | head -5
```

Expected: "Loaded 14 queries across 1 corpora" (may error on adapter calls — that's fine, just validates JSON loading)

- [ ] **Step 3: Commit**

```bash
git add benchmarks/contextual/queries/geoffreyengram.json
git commit -m "feat(bench): add geoffreyengram ground truth queries (14 queries)"
```

---

### Task 8: Ground Truth — unravelai Queries

**Files:**
- Create: `benchmarks/contextual/queries/unravelai.json`

- [ ] **Step 1: Generate unravelai ground truth**

Explore `/tmp/unravelai` and generate 14 queries. Focus on their core subsystems: graph builder, search engine, AST engine, pattern store, consult orchestration, embeddings. Tag confidence.

Same distribution: 3 feature_location, 2 dependency_tracing, 3 concept_search, 2 bug_localization, 2 structural_understanding, 2 feasibility.

- [ ] **Step 2: Validate JSON parses correctly**

```bash
go run -C benchmarks/contextual/ . --corpus-only unravelai --skip-judge --skip-unravel 2>&1 | head -5
```

Expected: "Loaded 14 queries across 1 corpora"

- [ ] **Step 3: Commit**

```bash
git add benchmarks/contextual/queries/unravelai.json
git commit -m "feat(bench): add unravelai ground truth queries (14 queries)"
```

---

### Task 9: Ground Truth — LearnCard Queries

**Files:**
- Create: `benchmarks/contextual/queries/learncard.json`

- [ ] **Step 1: Generate LearnCard ground truth**

Explore `/Users/donny/Work/LearnCard` and generate 14 queries. Focus on the monorepo structure: apps/learn-card-app, packages/learn-card-core, packages/plugins, packages/learn-card-network, etc. Tag confidence — many will be medium/low given the repo size.

Same distribution: 3 feature_location, 2 dependency_tracing, 3 concept_search, 2 bug_localization, 2 structural_understanding, 2 feasibility.

- [ ] **Step 2: Validate JSON parses correctly**

```bash
go run -C benchmarks/contextual/ . --corpus-only learncard --skip-judge --skip-unravel 2>&1 | head -5
```

Expected: "Loaded 14 queries across 1 corpora"

- [ ] **Step 3: Commit**

```bash
git add benchmarks/contextual/queries/learncard.json
git commit -m "feat(bench): add LearnCard ground truth queries (14 queries)"
```

---

### Task 10: End-to-End Integration Test

**Files:**
- Modify: `benchmarks/contextual/main.go` (minor fixes from integration)

- [ ] **Step 1: Run dualmem-only on geoffreyengram (fast smoke test)**

```bash
cd /Users/donny/Projects/2026/geoffreyengram
go run ./benchmarks/contextual/ --corpus-only geoffreyengram --skip-judge --skip-unravel --verbose
```

Expected: Per-query IR metrics printed. Precision/Recall > 0 for most queries. No panics.

- [ ] **Step 2: Run dualmem + unravel on geoffreyengram (adapter integration)**

```bash
go run ./benchmarks/contextual/ --corpus-only geoffreyengram --skip-judge --verbose
```

Expected: Both systems produce results. Unravel adapter spawns Node.js successfully.

- [ ] **Step 3: Run full benchmark with judge on a single query type (judge integration)**

```bash
go run ./benchmarks/contextual/ --corpus-only geoffreyengram --query-type feature_location --verbose
```

Expected: Judge scores printed. JSON and markdown files created in results/.

- [ ] **Step 4: Fix any issues found during integration**

Review results, fix adapter parsing issues, adjust query ground truth if needed.

- [ ] **Step 5: Run full benchmark across all 3 corpora**

```bash
go run ./benchmarks/contextual/ --verbose
```

Expected: Complete run with JSON + markdown output. Review the markdown report for sanity.

- [ ] **Step 6: Commit any fixes**

```bash
git add benchmarks/contextual/
git commit -m "fix(bench): integration fixes from end-to-end testing"
```

---

### Task 11: User Review of Ground Truth

**Files:**
- Possibly modify: `benchmarks/contextual/queries/*.json`

- [ ] **Step 1: Present medium/low confidence queries to user for review**

Run through all query JSON files and list entries where `confidence` is `medium` or `low`. Present to user for verification.

- [ ] **Step 2: Incorporate user feedback**

Update ground truth based on user corrections. Re-run affected queries to verify metrics still compute correctly.

- [ ] **Step 3: Commit corrections**

```bash
git add benchmarks/contextual/queries/
git commit -m "fix(bench): update ground truth from user review"
```

---

### Task 12: Final Documentation & Cleanup

**Files:**
- Modify: `docs/unravel-comparison-plan.md`
- Modify: `benchmarks/contextual/.gitignore`

- [ ] **Step 1: Update comparison plan to mark Phase 4 as complete**

Add to the Execution Order section:
```
4. **Phase 4: Benchmark** ✅ — `benchmarks/contextual/` with 42 queries, IR metrics, and LLM-as-judge
```

- [ ] **Step 2: Ensure results are gitignored**

Verify `benchmarks/contextual/.gitignore` contains:
```
results/*.json
results/*.md
```

- [ ] **Step 3: Commit**

```bash
git add docs/unravel-comparison-plan.md benchmarks/contextual/.gitignore
git commit -m "docs: mark Phase 4 benchmark as complete in comparison plan"
```
