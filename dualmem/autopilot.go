package dualmem

import (
	"context"
	"fmt"
	"strings"
)

// loadOrScanCodemap returns the codemap and structural edges for a namespace.
// It first tries getOrGenerateCodeMap (which reads from cache or regenerates).
// If RootDir is not set, it returns nil/empty rather than an error.
// Edges are fetched by re-running ScanCodebase only when a fresh scan is triggered;
// otherwise they come from the store via a lightweight re-scan on the cached dir.
func (e *Engine) loadOrScanCodemap(ctx context.Context, namespace string) (*CodeMap, []StructuralEdge, error) {
	if e.cfg == nil || e.cfg.RootDir == "" {
		return nil, nil, nil
	}

	// ScanCodebase gives us both CodeMap and Edges in one pass.
	result, err := ScanCodebase(e.cfg.RootDir, e.OnScanProgress)
	if err != nil {
		return nil, nil, fmt.Errorf("autopilot: scan codebase: %w", err)
	}
	cm := result.CodeMap
	_, currentCommit := GetGitState(e.cfg.RootDir)
	cm.Namespace = namespace
	cm.GitCommit = currentCommit

	// Persist the fresh scan (best-effort).
	e.store.UpsertCodeMap(namespace, e.cfg.RootDir, cm.Zoom1, cm.MarshalZoom2(), cm.GitCommit)
	if len(result.Edges) > 0 {
		edges := make([]StructuralEdge, len(result.Edges))
		copy(edges, result.Edges)
		for i := range edges {
			edges[i].Namespace = namespace
		}
		e.store.InsertStructuralEdges(namespace, edges)
		return cm, edges, nil
	}
	return cm, nil, nil
}

// hasRecentInvestigation returns true if there are investigation memories
// already associated with any of the given files.
func (e *Engine) hasRecentInvestigation(ctx context.Context, namespace string, files []string) bool {
	type fileCounter interface {
		GetMemoryCountByFiles(userID string, files []string) (int, error)
	}
	fc, ok := e.store.(fileCounter)
	if !ok || len(files) == 0 {
		return false
	}
	count, err := fc.GetMemoryCountByFiles(namespace, files)
	if err != nil {
		return false
	}
	return count > 0
}

// Autopilot proactively explores the codebase ranked by curiosity signals and saves
// investigation memories. It respects a token budget and can be run in dry-run mode
// to preview targets without any LLM calls or memory writes.
func (e *Engine) Autopilot(ctx context.Context, namespace string, opts AutopilotOpts) (*AutopilotResult, error) {
	if opts.Budget <= 0 {
		opts.Budget = 20000
	}

	result := &AutopilotResult{}

	// 1. Load or scan codemap.
	cm, edges, err := e.loadOrScanCodemap(ctx, namespace)
	if err != nil {
		return nil, err
	}
	if cm == nil {
		// No RootDir configured — return an empty result gracefully.
		return result, nil
	}

	// 2. Get last autopilot commit from config store.
	lastCommit, _ := e.store.GetConfigValue("autopilot_last_commit_" + namespace)

	// 3. Gather curiosity signals.
	signals, err := e.GatherCuriositySignals(ctx, namespace, cm, edges, lastCommit)
	if err != nil {
		return nil, fmt.Errorf("autopilot: gather signals: %w", err)
	}

	// 4. Rank modules.
	targets := RankModules(cm, signals)
	result.Targets = targets

	// 5. Dry run — return ranked targets without exploring.
	if opts.DryRun {
		return result, nil
	}

	// 6. Get explorer generator.
	gen, err := e.getExplorerGenerator()
	if err != nil {
		return nil, fmt.Errorf("autopilot: no text generator: %w", err)
	}

	// 7. Loop through targets within budget.
	tokensRemaining := opts.Budget
	for _, target := range targets {
		if tokensRemaining <= 0 {
			break
		}

		// Skip if already well-documented and not forced.
		if !opts.Force && e.hasRecentInvestigation(ctx, namespace, target.Files) {
			result.Skipped++
			continue
		}

		// Per-target budget: use up to 25% of remaining or 4000, whichever is smaller.
		targetBudget := min(tokensRemaining/4, 4000)
		if targetBudget < 200 {
			break
		}

		// Build a short query for this module.
		query := fmt.Sprintf("Explain the role and design of module %s", target.ModulePath)

		// Fetch code evidence via Explore.
		exploreResult, err := e.Explore(ctx, namespace, query, targetBudget)
		if err != nil {
			// Non-fatal: skip this target.
			continue
		}

		tokensUsed := 0
		if exploreResult.Evidence != nil {
			for _, s := range exploreResult.Evidence.Snippets {
				tokensUsed += len(s.Content) / 4
			}
		}

		summary := exploreResult.Summary
		if summary == "" && exploreResult.Evidence != nil && len(exploreResult.Evidence.Snippets) > 0 {
			// Fallback: ask the generator directly.
			var sb strings.Builder
			sb.WriteString("Briefly describe what this module does:\n\n")
			for _, s := range exploreResult.Evidence.Snippets {
				sb.WriteString(s.Content)
				sb.WriteString("\n")
			}
			summary, _ = gen.GenerateText(ctx, sb.String(), 300)
			tokensUsed += len(summary) / 4
		}

		if summary == "" {
			result.Skipped++
			continue
		}

		// 8. Save result as an investigation memory.
		memText := fmt.Sprintf("[autopilot] %s: %s", target.ModulePath, summary)
		memErr := e.AddWithOptions(ctx, MemoryInput{
			UserMessage: memText,
			Type:        "investigation",
			Files:       target.Files,
			Salience:    0.7,
		}, namespace)
		if memErr == nil {
			result.MemoriesAdded++
		}

		result.Explored++
		result.TokensUsed += tokensUsed
		tokensRemaining -= tokensUsed
	}

	// 9. Update autopilot_last_commit.
	_, currentCommit := GetGitState(e.cfg.RootDir)
	if currentCommit != "" {
		e.store.SetConfigValue("autopilot_last_commit_"+namespace, currentCommit)
	}

	return result, nil
}
