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

