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
