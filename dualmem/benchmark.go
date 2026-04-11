package dualmem

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// BenchmarkOpts controls a cold-vs-warm benchmark run.
type BenchmarkOpts struct {
	Queries     []string // explicit queries; if empty, auto-generate from git log
	TokenBudget int      // per-query budget (default 4000)
	AutoQueries int      // number of queries to generate from git (default 5)
}

// BenchQueryResult holds the comparison for a single query.
type BenchQueryResult struct {
	Query       string         `json:"query"`
	ColdTokens  int            `json:"cold_tokens"`
	WarmTokens  int            `json:"warm_tokens"`
	ColdSources int            `json:"cold_sources"`
	WarmSources int            `json:"warm_sources"`
	MemoryTypes map[string]int `json:"memory_types"` // types surfaced in warm (decision, warning, etc)
	ColdLatency time.Duration  `json:"cold_latency"`
	WarmLatency time.Duration  `json:"warm_latency"`
}

// BenchSummary aggregates metrics across all queries.
type BenchSummary struct {
	AvgMemoryLift float64 `json:"avg_memory_lift"` // avg additional sources per query (warm - cold)
	Coverage      float64 `json:"coverage"`         // modules with memories / total modules
	TotalMemories int     `json:"total_memories"`
	ModulesTotal  int     `json:"modules_total"`
	ModulesCover  int     `json:"modules_covered"`
}

// BenchmarkResult is the full output of a benchmark run.
type BenchmarkResult struct {
	Queries []BenchQueryResult `json:"queries"`
	Summary BenchSummary       `json:"summary"`
}

// Benchmark compares cold-start (no memories) vs warm (with memories) context assembly
// on the current project using real queries auto-generated from git history.
func (e *Engine) Benchmark(ctx context.Context, namespace string, opts BenchmarkOpts) (*BenchmarkResult, error) {
	if opts.TokenBudget <= 0 {
		opts.TokenBudget = 4000
	}
	if opts.AutoQueries <= 0 {
		opts.AutoQueries = 5
	}

	// Auto-generate queries from git log if none provided.
	queries := opts.Queries
	if len(queries) == 0 {
		var err error
		queries, err = gitLogQueries(e.cfg.RootDir, opts.AutoQueries)
		if err != nil {
			return nil, fmt.Errorf("benchmark: auto-generate queries: %w", err)
		}
		if len(queries) == 0 {
			return nil, fmt.Errorf("benchmark: no git commits to generate queries from")
		}
	}

	// Cold namespace: a temporary namespace that has no memories.
	coldNS := "__benchmark_cold_" + namespace

	results := make([]BenchQueryResult, 0, len(queries))
	for _, q := range queries {
		qr := BenchQueryResult{
			Query:       q,
			MemoryTypes: make(map[string]int),
		}

		// Cold run: use empty namespace (no memories, just codemap/diff).
		start := time.Now()
		coldBlock, err := e.AssembleContextWith(ctx, coldNS, q, opts.TokenBudget, &ContextOpts{
			Intent: IntentExplore,
		})
		qr.ColdLatency = time.Since(start)
		if err != nil {
			return nil, fmt.Errorf("benchmark: cold run for %q: %w", q, err)
		}
		qr.ColdTokens = coldBlock.TokenCount
		qr.ColdSources = len(coldBlock.Sources)

		// Warm run: use real namespace (with all memories).
		start = time.Now()
		warmBlock, err := e.AssembleContextWith(ctx, namespace, q, opts.TokenBudget, nil)
		qr.WarmLatency = time.Since(start)
		if err != nil {
			return nil, fmt.Errorf("benchmark: warm run for %q: %w", q, err)
		}
		qr.WarmTokens = warmBlock.TokenCount
		qr.WarmSources = len(warmBlock.Sources)

		// Count memory types from warm sources (exclude codemap/diff/checkpoint).
		for _, src := range warmBlock.Sources {
			switch src.Type {
			case "codemap", "diff":
				// structural, not memory
			default:
				qr.MemoryTypes[src.Type]++
			}
		}

		results = append(results, qr)
	}

	// Coverage stats via autopilot dry-run.
	summary := BenchSummary{}
	apResult, err := e.Autopilot(ctx, namespace, AutopilotOpts{DryRun: true})
	if err == nil && apResult != nil {
		total := len(apResult.Targets)
		covered := 0
		for _, t := range apResult.Targets {
			if t.Signals["memory_gap"] < 1.0 { // has at least some memory
				covered++
			}
		}
		summary.ModulesTotal = total
		summary.ModulesCover = covered
		if total > 0 {
			summary.Coverage = float64(covered) / float64(total)
		}
	}

	// Compute averages.
	if len(results) > 0 {
		var totalLift float64
		for _, r := range results {
			totalLift += float64(r.WarmSources - r.ColdSources)
		}
		summary.AvgMemoryLift = totalLift / float64(len(results))
	}

	// Count total memories.
	type detailCounter interface {
		GetDetailCount(userID string) (int, error)
	}
	if dc, ok := e.store.(detailCounter); ok {
		if n, err := dc.GetDetailCount(namespace); err == nil {
			summary.TotalMemories = n
		}
	}

	return &BenchmarkResult{
		Queries: results,
		Summary: summary,
	}, nil
}

// FormatBenchmarkTable returns a human-readable table for a BenchmarkResult.
func FormatBenchmarkTable(r *BenchmarkResult, namespace string) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("=== DualMem Benchmark (%s) ===\n\n", namespace))
	sb.WriteString(fmt.Sprintf("%-40s | %7s | %7s | %5s | Types\n", "Query", "Cold", "Warm", "+Mems"))
	sb.WriteString(strings.Repeat("-", 90) + "\n")

	for _, q := range r.Queries {
		query := q.Query
		if len(query) > 40 {
			query = query[:37] + "..."
		}
		lift := q.WarmSources - q.ColdSources

		// Format memory types.
		var types []string
		for t, n := range q.MemoryTypes {
			types = append(types, fmt.Sprintf("%d %s", n, t))
		}
		typeStr := strings.Join(types, ", ")
		if typeStr == "" {
			typeStr = "-"
		}

		sb.WriteString(fmt.Sprintf("%-40s | %5d tk | %5d tk | %+4d  | %s\n",
			query, q.ColdTokens, q.WarmTokens, lift, typeStr))
	}

	sb.WriteString(strings.Repeat("-", 90) + "\n")
	sb.WriteString(fmt.Sprintf("Summary: %d queries, avg lift %+.1f sources, coverage %d/%d (%.0f%%)\n",
		len(r.Queries), r.Summary.AvgMemoryLift,
		r.Summary.ModulesCover, r.Summary.ModulesTotal, r.Summary.Coverage*100))
	if r.Summary.TotalMemories > 0 {
		sb.WriteString(fmt.Sprintf("Total memories in store: %d\n", r.Summary.TotalMemories))
	}

	return sb.String()
}

// gitLogQueries extracts recent commit messages as benchmark queries.
func gitLogQueries(rootDir string, n int) ([]string, error) {
	if rootDir == "" {
		return nil, fmt.Errorf("no root dir")
	}
	cmd := exec.Command("git", "log", "--oneline", fmt.Sprintf("-%d", n))
	cmd.Dir = rootDir
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var queries []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Strip the short hash prefix (e.g., "abc1234 feat: add auth" -> "feat: add auth")
		if idx := strings.Index(line, " "); idx > 0 {
			line = line[idx+1:]
		}
		queries = append(queries, line)
	}
	return queries, nil
}
