package dualmem

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

// gitDefaultBranch returns the default branch name (main or master) for the repo.
func gitDefaultBranch(dir string) string {
	// Try the remote HEAD first.
	cmd := exec.Command("git", "symbolic-ref", "refs/remotes/origin/HEAD", "--short")
	cmd.Dir = dir
	if out, err := cmd.Output(); err == nil {
		branch := strings.TrimSpace(string(out))
		// Strip "origin/" prefix.
		if i := strings.LastIndex(branch, "/"); i >= 0 {
			branch = branch[i+1:]
		}
		return branch
	}
	// Fallback: check if main exists, else master.
	for _, name := range []string{"main", "master"} {
		cmd := exec.Command("git", "rev-parse", "--verify", name)
		cmd.Dir = dir
		if cmd.Run() == nil {
			return name
		}
	}
	return "main"
}

// gitCheckout switches the working tree to the given branch.
func gitCheckout(dir, branch string) error {
	cmd := exec.Command("git", "checkout", branch)
	cmd.Dir = dir
	return cmd.Run()
}

// readTargetFiles reads source files for a curiosity target, returning
// concatenated content and an approximate token count. It caps total
// content to maxTokens (estimated as chars/4).
func readTargetFiles(rootDir string, files []string, maxTokens int) (string, int) {
	var sb strings.Builder
	tokensUsed := 0
	sourceExts := map[string]bool{
		".go": true, ".ts": true, ".tsx": true, ".js": true, ".jsx": true,
		".py": true, ".rs": true, ".java": true, ".rb": true, ".css": true,
	}

	for _, f := range files {
		if tokensUsed >= maxTokens {
			break
		}
		fullPath := filepath.Join(rootDir, f)
		info, err := os.Stat(fullPath)
		if err != nil {
			continue
		}

		if info.IsDir() {
			// Read source files in the directory.
			entries, err := os.ReadDir(fullPath)
			if err != nil {
				continue
			}
			for _, entry := range entries {
				if entry.IsDir() || !sourceExts[filepath.Ext(entry.Name())] {
					continue
				}
				content, err := os.ReadFile(filepath.Join(fullPath, entry.Name()))
				if err != nil {
					continue
				}
				toks := len(content) / 4
				if tokensUsed+toks > maxTokens {
					// Truncate to fit budget.
					remaining := (maxTokens - tokensUsed) * 4
					if remaining > 0 {
						sb.WriteString(fmt.Sprintf("--- %s ---\n", filepath.Join(f, entry.Name())))
						sb.Write(content[:remaining])
						sb.WriteString("\n\n")
						tokensUsed = maxTokens
					}
					break
				}
				sb.WriteString(fmt.Sprintf("--- %s ---\n", filepath.Join(f, entry.Name())))
				sb.Write(content)
				sb.WriteString("\n\n")
				tokensUsed += toks
			}
			continue
		}

		// Regular file.
		content, err := os.ReadFile(fullPath)
		if err != nil {
			continue
		}
		toks := len(content) / 4
		if tokensUsed+toks > maxTokens {
			remaining := (maxTokens - tokensUsed) * 4
			if remaining > 0 {
				sb.WriteString(fmt.Sprintf("--- %s ---\n", f))
				sb.Write(content[:remaining])
				sb.WriteString("\n\n")
				tokensUsed = maxTokens
			}
			break
		}
		sb.WriteString(fmt.Sprintf("--- %s ---\n", f))
		sb.Write(content)
		sb.WriteString("\n\n")
		tokensUsed += toks
	}

	return sb.String(), tokensUsed
}

// Autopilot proactively explores the codebase ranked by curiosity signals and saves
// investigation memories. It respects a token budget and can be run in dry-run mode
// to preview targets without any LLM calls or memory writes.
//
// It always checks out the default branch (main/master) before scanning to ensure
// consistent results regardless of which branch the repo is on.
func (e *Engine) Autopilot(ctx context.Context, namespace string, opts AutopilotOpts) (*AutopilotResult, error) {
	if opts.Budget <= 0 {
		opts.Budget = 20000
	}

	result := &AutopilotResult{}

	if e.cfg == nil || e.cfg.RootDir == "" {
		return result, nil
	}

	// 0. Switch to the default branch for consistent scanning.
	originalBranch := gitCurrentBranch(e.cfg.RootDir)
	defaultBranch := gitDefaultBranch(e.cfg.RootDir)
	if originalBranch != "" && originalBranch != defaultBranch {
		if err := gitCheckout(e.cfg.RootDir, defaultBranch); err == nil {
			defer gitCheckout(e.cfg.RootDir, originalBranch)
		}
	}

	// 1. Load or scan codemap (once — not repeated per target).
	cm, edges, err := e.loadOrScanCodemap(ctx, namespace)
	if err != nil {
		return nil, err
	}
	if cm == nil {
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
	// Instead of calling Explore() (which rebuilds the codemap + HDC index each time),
	// read target files directly — we already know which files to look at from ranking.
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

		// Read source files directly instead of doing a full Explore().
		codeContent, tokensUsed := readTargetFiles(e.cfg.RootDir, target.Files, targetBudget)
		if codeContent == "" {
			result.Skipped++
			continue
		}

		// Generate summary from the code.
		prompt := fmt.Sprintf(
			"Briefly describe what the module %s does (50-100 words). "+
				"Focus on: key functions, data flow, and design patterns. Reference specific code.\n\n%s",
			target.ModulePath, codeContent)
		summary, err := gen.GenerateText(ctx, prompt, 200)
		if err != nil || strings.TrimSpace(summary) == "" {
			result.Skipped++
			continue
		}
		summary = strings.TrimSpace(summary)
		tokensUsed += len(summary) / 4

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
