package dualmem

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// ticketPrefixRe matches ticket prefixes like [LC-1729] or [PROJ-42].
var ticketPrefixRe = regexp.MustCompile(`\[([A-Z]+-\d+)\]`)

// extractTicketPrefix extracts a ticket prefix from a commit subject.
// "[LC-1729] guardian credential issuance" → "LC-1729"
func extractTicketPrefix(subject string) string {
	m := ticketPrefixRe.FindStringSubmatch(subject)
	if len(m) >= 2 {
		return m[1]
	}
	return ""
}

// groupCommitsByTicket groups commits by their ticket prefix.
// Commits without a ticket prefix are discarded.
func groupCommitsByTicket(commits []GitCommit) map[string][]GitCommit {
	groups := make(map[string][]GitCommit)
	for _, c := range commits {
		ticket := extractTicketPrefix(c.Subject)
		if ticket == "" {
			continue
		}
		groups[ticket] = append(groups[ticket], c)
	}
	return groups
}

// buildWorkflowClusters creates WorkflowClusters from ticket-grouped commits.
// Only tickets with >= minCommits are included.
func buildWorkflowClusters(groups map[string][]GitCommit, minCommits int) []WorkflowCluster {
	var clusters []WorkflowCluster

	for ticket, commits := range groups {
		if len(commits) < minCommits {
			continue
		}

		// Union files, collect subjects, find most recent date.
		fileSet := make(map[string]int) // file → commit count (centrality)
		subjectSet := make(map[string]bool)
		var mostRecent time.Time

		for _, c := range commits {
			if c.Date.After(mostRecent) {
				mostRecent = c.Date
			}
			// Strip ticket prefix from subject for display.
			subj := strings.TrimSpace(ticketPrefixRe.ReplaceAllString(c.Subject, ""))
			if subj != "" && !subjectSet[subj] {
				subjectSet[subj] = true
			}
			for _, f := range c.Files {
				fileSet[f]++
			}
		}

		files := make([]string, 0, len(fileSet))
		for f := range fileSet {
			files = append(files, f)
		}

		subjects := make([]string, 0, len(subjectSet))
		for s := range subjectSet {
			subjects = append(subjects, s)
		}

		// Recency score: exponential decay from now, half-life 3 months.
		daysSince := time.Since(mostRecent).Hours() / 24
		score := 1.0 / (1.0 + daysSince/90.0)

		clusters = append(clusters, WorkflowCluster{
			ID:       ticket,
			Tickets:  []string{ticket},
			Subjects: subjects,
			Files:    files,
			Commits:  len(commits),
			Score:    score,
		})
	}

	return clusters
}

// mergeOverlappingClusters merges clusters whose file sets have Jaccard similarity
// above the threshold. Caps merged clusters at maxFiles.
func mergeOverlappingClusters(clusters []WorkflowCluster, threshold float64, maxFiles int) []WorkflowCluster {
	if maxFiles <= 0 {
		maxFiles = 100
	}

	// Build file sets for each cluster.
	type clusterWithSet struct {
		cluster WorkflowCluster
		files   map[string]bool
		merged  bool
	}

	items := make([]clusterWithSet, len(clusters))
	for i, c := range clusters {
		fs := make(map[string]bool, len(c.Files))
		for _, f := range c.Files {
			fs[f] = true
		}
		items[i] = clusterWithSet{cluster: c, files: fs}
	}

	// Greedy merge: for each pair, merge if Jaccard > threshold.
	for i := 0; i < len(items); i++ {
		if items[i].merged {
			continue
		}
		for j := i + 1; j < len(items); j++ {
			if items[j].merged {
				continue
			}
			// Compute Jaccard similarity.
			intersection := 0
			for f := range items[i].files {
				if items[j].files[f] {
					intersection++
				}
			}
			union := len(items[i].files) + len(items[j].files) - intersection
			if union == 0 {
				continue
			}
			jaccard := float64(intersection) / float64(union)
			if jaccard < threshold {
				continue
			}

			// Check size cap.
			mergedSize := len(items[i].files) + len(items[j].files) - intersection
			if mergedSize > maxFiles {
				continue
			}

			// Merge j into i.
			for f := range items[j].files {
				items[i].files[f] = true
			}
			items[i].cluster.Tickets = append(items[i].cluster.Tickets, items[j].cluster.Tickets...)
			items[i].cluster.Subjects = append(items[i].cluster.Subjects, items[j].cluster.Subjects...)
			items[i].cluster.Commits += items[j].cluster.Commits
			if items[j].cluster.Score > items[i].cluster.Score {
				items[i].cluster.Score = items[j].cluster.Score
			}
			// Rebuild files from set.
			items[i].cluster.Files = make([]string, 0, len(items[i].files))
			for f := range items[i].files {
				items[i].cluster.Files = append(items[i].cluster.Files, f)
			}
			items[j].merged = true
		}
	}

	// Collect unmerged clusters and regenerate IDs.
	var result []WorkflowCluster
	for _, item := range items {
		if item.merged {
			continue
		}
		item.cluster.ID = nameWorkflow(item.cluster.Subjects)
		result = append(result, item.cluster)
	}
	return result
}

// nameWorkflow generates a slug from commit subjects by finding common tokens.
func nameWorkflow(subjects []string) string {
	if len(subjects) == 0 {
		return "unknown"
	}

	// Count token frequency across subjects.
	tokenCounts := make(map[string]int)
	stopWords := map[string]bool{
		"the": true, "a": true, "an": true, "and": true, "or": true, "to": true,
		"in": true, "for": true, "of": true, "with": true, "on": true, "is": true,
		"fix": true, "add": true, "update": true, "refactor": true, "remove": true,
		"feat": true, "chore": true, "test": true, "docs": true, "wip": true,
	}

	for _, subj := range subjects {
		seen := make(map[string]bool)
		words := strings.Fields(strings.ToLower(subj))
		for _, w := range words {
			// Strip punctuation.
			w = strings.Trim(w, ".,;:!?()[]{}\"'`-")
			if len(w) < 2 || stopWords[w] {
				continue
			}
			if !seen[w] {
				seen[w] = true
				tokenCounts[w]++
			}
		}
	}

	// Find tokens appearing in >40% of subjects.
	threshold := max(len(subjects)*4/10, 1)
	type scored struct {
		word  string
		count int
	}
	var common []scored
	for w, c := range tokenCounts {
		if c >= threshold {
			common = append(common, scored{w, c})
		}
	}
	sort.Slice(common, func(i, j int) bool {
		return common[i].count > common[j].count
	})

	// Take top 3 tokens.
	var parts []string
	for i, s := range common {
		if i >= 3 {
			break
		}
		parts = append(parts, s.word)
	}
	if len(parts) == 0 {
		// Fallback: first 3 words of first subject.
		words := strings.Fields(strings.ToLower(subjects[0]))
		for i, w := range words {
			if i >= 3 {
				break
			}
			w = strings.Trim(w, ".,;:!?()[]{}\"'`-")
			if w != "" {
				parts = append(parts, w)
			}
		}
	}

	return strings.Join(parts, "-")
}

// selectWorkflowFiles picks representative files from a workflow cluster,
// prioritizing files that appear in the most commits (highest centrality).
// Ensures at least one file per distinct top-level directory for breadth.
func selectWorkflowFiles(allFiles []string, commits []GitCommit, rootDir string, maxTokens int) []string {
	// Count how many commits each file appears in.
	fileCounts := make(map[string]int)
	for _, c := range commits {
		for _, f := range c.Files {
			fileCounts[f]++
		}
	}

	// Filter to source files that exist.
	var candidates []string
	for _, f := range allFiles {
		if !sourceExts[filepath.Ext(f)] {
			continue
		}
		fullPath := filepath.Join(rootDir, f)
		if info, err := os.Stat(fullPath); err == nil && !info.IsDir() && info.Size() <= 100*1024 {
			candidates = append(candidates, f)
		}
	}

	// Sort by commit count descending.
	sort.Slice(candidates, func(i, j int) bool {
		return fileCounts[candidates[i]] > fileCounts[candidates[j]]
	})

	// Ensure directory breadth: pick at least one from each top-level dir.
	dirCovered := make(map[string]bool)
	var selected []string
	tokensUsed := 0

	// First pass: one per directory (most central file from each).
	for _, f := range candidates {
		dir := areaKeyFromDir(filepath.Dir(f))
		if dirCovered[dir] {
			continue
		}
		info, err := os.Stat(filepath.Join(rootDir, f))
		if err != nil {
			continue
		}
		toks := int(info.Size()) / 4
		if tokensUsed+toks > maxTokens {
			continue
		}
		selected = append(selected, f)
		dirCovered[dir] = true
		tokensUsed += toks
	}

	// Second pass: fill remaining budget with highest-centrality files.
	for _, f := range candidates {
		if tokensUsed >= maxTokens {
			break
		}
		// Skip if already selected.
		already := false
		for _, s := range selected {
			if s == f {
				already = true
				break
			}
		}
		if already {
			continue
		}
		info, err := os.Stat(filepath.Join(rootDir, f))
		if err != nil {
			continue
		}
		toks := int(info.Size()) / 4
		if tokensUsed+toks > maxTokens {
			continue
		}
		selected = append(selected, f)
		tokensUsed += toks
	}

	return selected
}

// buildWorkflowPrompt creates a workflow-oriented prompt for cross-cutting analysis.
func buildWorkflowPrompt(cluster WorkflowCluster, fileList []string, codeContent string) string {
	// Show commit subjects (capped at 20).
	var subjectLines string
	for i, s := range cluster.Subjects {
		if i >= 20 {
			subjectLines += fmt.Sprintf("... and %d more commits\n", len(cluster.Subjects)-20)
			break
		}
		subjectLines += "- " + s + "\n"
	}

	// Show file list (capped at 50).
	var fileListStr string
	for i, f := range fileList {
		if i >= 50 {
			fileListStr += fmt.Sprintf("... and %d more files\n", len(fileList)-50)
			break
		}
		fileListStr += f + "\n"
	}

	return fmt.Sprintf(`You are analyzing a cross-cutting workflow in a codebase. This workflow spans multiple services and packages.

Tickets: %s
Commits (%d total):
%s
Files involved:
%s
[Source Code]
%s

Trace this workflow end-to-end. Write a thorough analysis (300-600 words) covering ALL sections:

## WORKFLOW SUMMARY
What does this workflow accomplish from the user's perspective?

## TRIGGER
What initiates this workflow? (User action, API call, event?)

## DATA FLOW
Trace data from trigger through each layer (UI → API → service → DB).
Name specific functions and files at each step.

## CROSS-SERVICE BOUNDARIES
Which packages/services are involved? How do they communicate?

## SIDE EFFECTS
What else happens? (Notifications, cache invalidation, events emitted?)

## ERROR HANDLING
How are failures handled at each boundary?

IMPORTANT: Reference specific files and function names from the source code above. Be concrete, not generic. Do NOT be brief.`,
		strings.Join(cluster.Tickets, ", "),
		cluster.Commits,
		subjectLines,
		fileListStr,
		codeContent)
}

// hasWorkflowCoverage returns true if a workflow has already been analyzed.
func (e *Engine) hasWorkflowCoverage(ctx context.Context, namespace, workflowID string) bool {
	type workflowCounter interface {
		GetWorkflowMemoryCount(userID, workflowID string) (int, error)
	}
	wc, ok := e.store.(workflowCounter)
	if !ok {
		return false
	}
	count, err := wc.GetWorkflowMemoryCount(namespace, workflowID)
	if err != nil {
		return false
	}
	return count > 0
}

// AutopilotWorkflows discovers cross-cutting workflows from git history and
// generates end-to-end analyses for each. Complements area-based autopilot
// with behavioral documentation.
func (e *Engine) AutopilotWorkflows(ctx context.Context, namespace string, opts WorkflowAutopilotOpts) (*WorkflowAutopilotResult, error) {
	if opts.Budget <= 0 {
		opts.Budget = 20000
	}
	if opts.DepthMonths <= 0 {
		opts.DepthMonths = 6
	}
	if opts.MinCommits <= 0 {
		opts.MinCommits = 3
	}
	if opts.MaxWorkflows <= 0 {
		opts.MaxWorkflows = 20
	}

	result := &WorkflowAutopilotResult{}

	if e.cfg == nil || e.cfg.RootDir == "" {
		return result, nil
	}

	// 1. Parse git history.
	commits, err := ParseGitLog(e.cfg.RootDir, opts.DepthMonths)
	if err != nil {
		return nil, fmt.Errorf("autopilot workflows: parse git log: %w", err)
	}
	if len(commits) == 0 {
		return result, nil
	}

	// 2. Group commits by ticket prefix.
	ticketGroups := groupCommitsByTicket(commits)

	// 3. Build workflow clusters.
	clusters := buildWorkflowClusters(ticketGroups, opts.MinCommits)

	// 4. Merge overlapping clusters.
	clusters = mergeOverlappingClusters(clusters, 0.5, 100)

	// 5. Sort by score (most recent first).
	sort.Slice(clusters, func(i, j int) bool {
		if clusters[i].Score != clusters[j].Score {
			return clusters[i].Score > clusters[j].Score
		}
		return clusters[i].ID < clusters[j].ID
	})

	// 6. Cap at MaxWorkflows.
	if len(clusters) > opts.MaxWorkflows {
		clusters = clusters[:opts.MaxWorkflows]
	}

	result.Clusters = clusters

	// 7. Dry run — return discovered clusters.
	if opts.DryRun {
		return result, nil
	}

	// 8. Get explorer generator.
	gen, err := e.getExplorerGenerator()
	if err != nil {
		return nil, fmt.Errorf("autopilot workflows: no text generator: %w", err)
	}

	// 9. Throttle and retry config.
	callDelay := 1500 * time.Millisecond
	maxRetries := 3
	debug := os.Getenv("DUALMEM_DEBUG") != ""

	// 10. Explore workflows within budget.
	tokensRemaining := opts.Budget
	for _, cluster := range clusters {
		if tokensRemaining <= 0 {
			break
		}

		// Skip if already covered.
		if !opts.Force && e.hasWorkflowCoverage(ctx, namespace, cluster.ID) {
			result.Skipped++
			if debug {
				log.Printf("[workflow] SKIP(covered): %s", cluster.ID)
			}
			continue
		}

		// Budget check.
		perWorkflowBudget := min(4000, tokensRemaining)
		if perWorkflowBudget < 500 {
			break
		}

		// Collect commits for this cluster's tickets (for centrality scoring).
		var clusterCommits []GitCommit
		for _, c := range commits {
			ticket := extractTicketPrefix(c.Subject)
			for _, t := range cluster.Tickets {
				if ticket == t {
					clusterCommits = append(clusterCommits, c)
					break
				}
			}
		}

		// Select representative files.
		selected := selectWorkflowFiles(cluster.Files, clusterCommits, e.cfg.RootDir, perWorkflowBudget)
		if len(selected) == 0 {
			result.Skipped++
			if debug {
				log.Printf("[workflow] SKIP(no-source): %s", cluster.ID)
			}
			continue
		}

		// Read source code.
		codeContent, tokensUsed := readTargetFiles(e.cfg.RootDir, selected, perWorkflowBudget)
		if codeContent == "" {
			result.Skipped++
			if debug {
				log.Printf("[workflow] SKIP(empty): %s", cluster.ID)
			}
			continue
		}

		// Build prompt.
		prompt := buildWorkflowPrompt(cluster, selected, codeContent)

		// Generate analysis with retry + backoff on 429.
		var summary string
		var genErr error
		for attempt := 0; attempt <= maxRetries; attempt++ {
			if attempt > 0 {
				backoff := callDelay * time.Duration(1<<(attempt-1))
				if debug {
					log.Printf("[workflow] retry %d/%d for %s (backoff %v)", attempt, maxRetries, cluster.ID, backoff)
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
				log.Printf("[workflow] SKIP(llm): %s err=%v", cluster.ID, genErr)
			}
			if genErr != nil && strings.Contains(genErr.Error(), "429") {
				result.Error = fmt.Sprintf("rate limited after %d explored", result.Explored)
				break
			}
			continue
		}
		summary = strings.TrimSpace(summary)
		tokensUsed += len(summary) / 4

		// Save as workflow memory.
		memText := fmt.Sprintf("[workflow:%s] %s", cluster.ID, summary)
		if debug {
			log.Printf("[workflow] summary_len=%d memText_len=%d", len(summary), len(memText))
		}
		memErr := e.AddWithOptions(ctx, MemoryInput{
			UserMessage: memText,
			Type:        "autopilot",
			Files:       cluster.Files,
			Salience:    0.9,
		}, namespace)
		if memErr == nil {
			result.MemoriesAdded++
		}

		result.Explored++
		result.TokensUsed += tokensUsed
		tokensRemaining -= tokensUsed

		// Throttle.
		time.Sleep(callDelay)

		if debug {
			log.Printf("[workflow] EXPLORED: %s [%s] (%d files, %d commits, %d tokens)",
				cluster.ID, strings.Join(cluster.Tickets, ","), len(cluster.Files), cluster.Commits, tokensUsed)
		}
	}

	return result, nil
}
