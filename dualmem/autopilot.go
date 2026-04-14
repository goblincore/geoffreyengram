package dualmem

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// sourceExts lists file extensions that autopilot considers source code.
var sourceExts = map[string]bool{
	".go": true, ".ts": true, ".tsx": true, ".js": true, ".jsx": true,
	".py": true, ".rs": true, ".java": true, ".rb": true, ".css": true,
}

// entryPointNames are files prioritized within an area (likely to contain key interfaces).
var entryPointNames = map[string]bool{
	"index.ts": true, "index.tsx": true, "index.js": true, "index.jsx": true,
	"main.go": true, "main.ts": true, "main.py": true, "main.rs": true,
	"mod.rs": true, "__init__.py": true, "lib.rs": true,
	"types.ts": true, "types.go": true, "models.py": true,
}

// walkSourceDirs walks the repo and returns directories containing source files,
// grouped by directory path (relative to rootDir).
func walkSourceDirs(rootDir string) (map[string][]string, error) {
	dirs := make(map[string][]string)

	err := filepath.Walk(rootDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			if skipDirs[info.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !sourceExts[filepath.Ext(info.Name())] {
			return nil
		}
		if info.Size() > 100*1024 {
			return nil
		}
		rel, err := filepath.Rel(rootDir, path)
		if err != nil {
			return nil
		}
		dir := filepath.Dir(rel)
		dirs[dir] = append(dirs[dir], rel)
		return nil
	})
	return dirs, err
}

// areaKeyFromDir extracts a depth-2 path prefix as the area grouping key.
// "packages/learn-card-core/src/lib" → "packages/learn-card-core"
// "src/utils" → "src/utils"
// "." → "."
func areaKeyFromDir(dir string) string {
	if dir == "." {
		return "."
	}
	parts := strings.Split(filepath.ToSlash(dir), "/")
	if len(parts) > 2 {
		parts = parts[:2]
	}
	return strings.Join(parts, "/")
}

// groupDirsIntoAreas aggregates directories by depth-2 prefix into areas.
func groupDirsIntoAreas(dirFiles map[string][]string, recentlyChanged map[string]bool) []Area {
	areaMap := make(map[string]*Area)

	for dir, files := range dirFiles {
		key := areaKeyFromDir(dir)
		area, ok := areaMap[key]
		if !ok {
			area = &Area{Key: key, Score: 0.35}
			areaMap[key] = area
		}
		area.Dirs = append(area.Dirs, dir)
		area.Files = append(area.Files, files...)

		// Boost score if any file in this dir was recently changed.
		for _, f := range files {
			if recentlyChanged[f] {
				if area.Score < 0.7 {
					area.Score = 0.7
				}
				break
			}
		}
	}

	areas := make([]Area, 0, len(areaMap))
	for _, area := range areaMap {
		// Deduplicate files (shouldn't happen but defensive).
		seen := make(map[string]bool, len(area.Files))
		deduped := area.Files[:0]
		for _, f := range area.Files {
			if !seen[f] {
				seen[f] = true
				deduped = append(deduped, f)
			}
		}
		area.Files = deduped
		areas = append(areas, *area)
	}

	sort.Slice(areas, func(i, j int) bool {
		if areas[i].Score != areas[j].Score {
			return areas[i].Score > areas[j].Score
		}
		return areas[i].Key < areas[j].Key
	})

	return areas
}

// prioritizeFiles reorders files so recently changed and entry-point files come first.
func prioritizeFiles(files []string, recentlyChanged map[string]bool) []string {
	type scored struct {
		path  string
		score int
	}
	scored_ := make([]scored, len(files))
	for i, f := range files {
		s := 0
		if recentlyChanged[f] {
			s += 2
		}
		if entryPointNames[filepath.Base(f)] {
			s += 1
		}
		scored_[i] = scored{f, s}
	}
	sort.SliceStable(scored_, func(i, j int) bool {
		return scored_[i].score > scored_[j].score
	})
	result := make([]string, len(files))
	for i, s := range scored_ {
		result[i] = s.path
	}
	return result
}

// buildAreaPrompt creates a Consult-quality prompt for analyzing an area.
func buildAreaPrompt(areaKey string, fileList []string, codeContent string) string {
	// Show file list (capped at 50 to avoid bloat).
	fileListStr := ""
	for i, f := range fileList {
		if i >= 50 {
			fileListStr += fmt.Sprintf("... and %d more files\n", len(fileList)-50)
			break
		}
		fileListStr += f + "\n"
	}

	return fmt.Sprintf(`You are analyzing the "%s" area of a codebase.

Files in this area:
%s
[Source Code]
%s

Write a detailed architectural analysis covering ALL of the following sections. Each section MUST have at least 2-3 sentences. Your total response should be 300-500 words minimum.

## PURPOSE
What is this area responsible for? What problem does it solve?

## KEY PATTERNS
What design patterns, data structures, or architectural decisions are used? Name specific patterns.

## DATA FLOW
How does data enter, transform, and exit this area? What are the key interfaces and exports?

## DEPENDENCIES
What does this area import from or export to other parts of the codebase?

## GOTCHAS
What non-obvious constraints, edge cases, or things should a future developer know about?

IMPORTANT: Reference specific files and function names from the source code above. Be concrete, not generic. Do NOT be brief — write a thorough analysis.`, areaKey, fileListStr, codeContent)
}

// hasAreaCoverage returns true if autopilot has already generated a summary for this area.
func (e *Engine) hasAreaCoverage(ctx context.Context, namespace, areaKey string) bool {
	type areaCounter interface {
		GetAutopilotMemoryCountByArea(userID, areaKey string) (int, error)
	}
	ac, ok := e.store.(areaCounter)
	if !ok {
		return false
	}
	count, err := ac.GetAutopilotMemoryCountByArea(namespace, areaKey)
	if err != nil {
		return false
	}
	return count > 0
}

// HasAutopilotCoverage returns true if autopilot has already explored an area.
// Exported for use by the CLI stats command.
func (e *Engine) HasAutopilotCoverage(ctx context.Context, namespace string, files []string) bool {
	// For backward compat with stats: derive area key from first file's dir.
	if len(files) == 0 {
		return false
	}
	dir := filepath.Dir(files[0])
	areaKey := areaKeyFromDir(dir)
	return e.hasAreaCoverage(ctx, namespace, areaKey)
}

// gitDefaultBranch returns the default branch name (main or master) for the repo.
func gitDefaultBranch(dir string) string {
	cmd := exec.Command("git", "symbolic-ref", "refs/remotes/origin/HEAD", "--short")
	cmd.Dir = dir
	if out, err := cmd.Output(); err == nil {
		branch := strings.TrimSpace(string(out))
		if i := strings.LastIndex(branch, "/"); i >= 0 {
			branch = branch[i+1:]
		}
		return branch
	}
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

// gitRecentlyChanged returns files changed in the last N commits on the current branch.
func gitRecentlyChanged(dir string, n int) map[string]bool {
	cmd := exec.Command("git", "log", fmt.Sprintf("-%d", n), "--name-only", "--pretty=format:", "--diff-filter=ACMR")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	changed := make(map[string]bool)
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			changed[line] = true
		}
	}
	return changed
}

// readTargetFiles reads source files for a target, returning concatenated content
// and an approximate token count. Caps total content to maxTokens (chars/4).
func readTargetFiles(rootDir string, files []string, maxTokens int) (string, int) {
	var sb strings.Builder
	tokensUsed := 0

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

// Autopilot proactively explores the codebase by grouping source directories into
// logical areas (depth-2 path prefix) and generating one rich architectural analysis
// per area. This produces ~25 substantial memories instead of ~970 shallow ones.
func (e *Engine) Autopilot(ctx context.Context, namespace string, opts AutopilotOpts) (*AutopilotResult, error) {
	if opts.Budget <= 0 {
		opts.Budget = 20000
	}

	result := &AutopilotResult{}

	if e.cfg == nil || e.cfg.RootDir == "" {
		return result, nil
	}

	// 0. Optionally switch to the default branch.
	if opts.CheckoutDefault {
		originalBranch := gitCurrentBranch(e.cfg.RootDir)
		defaultBranch := gitDefaultBranch(e.cfg.RootDir)
		if originalBranch != "" && originalBranch != defaultBranch {
			if err := gitCheckout(e.cfg.RootDir, defaultBranch); err == nil {
				defer gitCheckout(e.cfg.RootDir, originalBranch)
			}
		}
	}

	// 1. Fast file walk.
	dirFiles, err := walkSourceDirs(e.cfg.RootDir)
	if err != nil {
		return nil, fmt.Errorf("autopilot: walk source dirs: %w", err)
	}
	if len(dirFiles) == 0 {
		return result, nil
	}

	// 2. Get recently changed files for prioritization.
	recentlyChanged := gitRecentlyChanged(e.cfg.RootDir, 50)

	// 3. Group directories into areas.
	areas := groupDirsIntoAreas(dirFiles, recentlyChanged)
	result.Areas = len(areas)

	// 4. Build targets for backward compat with dry-run output.
	targets := make([]CuriosityTarget, len(areas))
	for i, area := range areas {
		targets[i] = CuriosityTarget{
			ModulePath: area.Key,
			Score:      area.Score,
			Files:      area.Files,
			Signals:    map[string]float64{"change_heat": area.Score, "file_count": float64(len(area.Files))},
		}
	}
	result.Targets = targets

	// 5. Dry run — return areas without exploring.
	if opts.DryRun {
		return result, nil
	}

	// 6. Get explorer generator.
	gen, err := e.getExplorerGenerator()
	if err != nil {
		return nil, fmt.Errorf("autopilot: no text generator: %w", err)
	}

	// 7. Allocate per-area code budget (input tokens).
	// Cap at 3000 tokens (~12KB of code) to keep prompts within LLM context limits.
	perAreaBudget := opts.Budget / max(len(areas), 1)
	if perAreaBudget < 1000 {
		perAreaBudget = 1000
	}
	if perAreaBudget > 3000 {
		perAreaBudget = 3000
	}

	// 8. Throttle and retry config.
	callDelay := 1500 * time.Millisecond
	maxRetries := 3
	debug := os.Getenv("DUALMEM_DEBUG") != ""

	// 9. Loop through areas.
	tokensRemaining := opts.Budget
	for _, area := range areas {
		if tokensRemaining <= 0 {
			break
		}

		// Skip if already covered and not forced.
		if !opts.Force && e.hasAreaCoverage(ctx, namespace, area.Key) {
			result.Skipped++
			if debug {
				log.Printf("[autopilot] SKIP(covered): %s", area.Key)
			}
			continue
		}

		// Budget check.
		areaBudget := min(perAreaBudget, tokensRemaining)
		if areaBudget < 500 {
			break
		}

		// Prioritize files within the area.
		prioritized := prioritizeFiles(area.Files, recentlyChanged)

		// Read source code.
		codeContent, tokensUsed := readTargetFiles(e.cfg.RootDir, prioritized, areaBudget)
		if codeContent == "" {
			result.Skipped++
			if debug {
				log.Printf("[autopilot] SKIP(empty): %s (%d files)", area.Key, len(area.Files))
			}
			continue
		}

		// Build rich prompt.
		prompt := buildAreaPrompt(area.Key, area.Files, codeContent)

		// Generate analysis with retry + backoff on 429.
		var summary string
		var genErr error
		for attempt := 0; attempt <= maxRetries; attempt++ {
			if attempt > 0 {
				backoff := callDelay * time.Duration(1<<(attempt-1))
				if debug {
					log.Printf("[autopilot] retry %d/%d for %s (backoff %v)", attempt, maxRetries, area.Key, backoff)
				}
				time.Sleep(backoff)
			}
			summary, genErr = gen.GenerateText(ctx, prompt, 2048)
			if genErr == nil {
				break
			}
			if !strings.Contains(genErr.Error(), "429") {
				break
			}
		}

		if genErr != nil || strings.TrimSpace(summary) == "" {
			result.Skipped++
			if debug {
				log.Printf("[autopilot] SKIP(llm): %s err=%v", area.Key, genErr)
			}
			if genErr != nil && strings.Contains(genErr.Error(), "429") {
				result.Error = fmt.Sprintf("rate limited by LLM provider after %d explored (retries exhausted)", result.Explored)
				break
			}
			continue
		}
		summary = strings.TrimSpace(summary)
		tokensUsed += len(summary) / 4

		// Save as investigation memory with area prefix.
		memText := fmt.Sprintf("[autopilot:%s] %s", area.Key, summary)
		if debug {
			log.Printf("[autopilot] summary_len=%d memText_len=%d", len(summary), len(memText))
		}
		memErr := e.AddWithOptions(ctx, MemoryInput{
			UserMessage: memText,
			Type:        "autopilot",
			Files:       area.Files,
			Salience:    0.85,
		}, namespace)
		if memErr == nil {
			result.MemoriesAdded++
		}

		result.Explored++
		result.TokensUsed += tokensUsed
		tokensRemaining -= tokensUsed

		// Throttle between successful calls.
		time.Sleep(callDelay)

		if debug {
			log.Printf("[autopilot] EXPLORED: %s (%d files, %d tokens)", area.Key, len(area.Files), tokensUsed)
		}
	}

	// 10. Update autopilot_last_commit.
	_, currentCommit := GetGitState(e.cfg.RootDir)
	if currentCommit != "" {
		e.store.SetConfigValue("autopilot_last_commit_"+namespace, currentCommit)
	}

	return result, nil
}

// loadOrScanCodemap returns the codemap and structural edges for a namespace.
// Used by ranked/curiosity-based exploration (not the default autopilot path).
func (e *Engine) loadOrScanCodemap(ctx context.Context, namespace string) (*CodeMap, []StructuralEdge, error) {
	if e.cfg == nil || e.cfg.RootDir == "" {
		return nil, nil, nil
	}

	result, err := ScanCodebase(e.cfg.RootDir, e.OnScanProgress)
	if err != nil {
		return nil, nil, fmt.Errorf("autopilot: scan codebase: %w", err)
	}
	cm := result.CodeMap
	_, currentCommit := GetGitState(e.cfg.RootDir)
	cm.Namespace = namespace
	cm.GitCommit = currentCommit

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
