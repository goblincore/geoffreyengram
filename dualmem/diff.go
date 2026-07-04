package dualmem

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// StructuralDiff summarizes codebase changes between two points in time.
type StructuralDiff struct {
	FromCommit     string   `json:"from_commit"`
	ToCommit       string   `json:"to_commit"`
	FromBranch     string   `json:"from_branch"`
	ToBranch       string   `json:"to_branch"`
	SameBranch     bool     `json:"same_branch"`
	FilesAdded     []string `json:"files_added,omitempty"`
	FilesModified  []string `json:"files_modified,omitempty"`
	FilesDeleted   []string `json:"files_deleted,omitempty"`
	Summary        string   `json:"summary"`
	StaleMemoryIDs []string `json:"stale_memory_ids,omitempty"`
}

// TotalChanges returns the total number of changed files.
func (d *StructuralDiff) TotalChanges() int {
	return len(d.FilesAdded) + len(d.FilesModified) + len(d.FilesDeleted)
}

// ComputeStructuralDiff compares the current git state against a previous session marker.
// Returns nil if not in a git repo or if marker is nil (first session).
func ComputeStructuralDiff(rootDir string, lastMarker *SessionMarker) (*StructuralDiff, error) {
	if lastMarker == nil {
		return nil, nil
	}

	// Check if we're in a git repo
	if !isGitRepo(rootDir) {
		return nil, nil
	}

	currentBranch := gitCurrentBranch(rootDir)
	currentCommit := gitCurrentCommit(rootDir)

	if currentCommit == "" || lastMarker.Commit == "" {
		return nil, nil
	}

	// Same commit = no changes
	if currentCommit == lastMarker.Commit {
		return &StructuralDiff{
			FromCommit: lastMarker.Commit,
			ToCommit:   currentCommit,
			FromBranch: lastMarker.Branch,
			ToBranch:   currentBranch,
			SameBranch: currentBranch == lastMarker.Branch,
			Summary:    "No changes since last session.",
		}, nil
	}

	diff := &StructuralDiff{
		FromCommit: lastMarker.Commit,
		ToCommit:   currentCommit,
		FromBranch: lastMarker.Branch,
		ToBranch:   currentBranch,
		SameBranch: currentBranch == lastMarker.Branch,
	}

	// Try to get diff
	if commitExists(rootDir, lastMarker.Commit) {
		if diff.SameBranch {
			// Same branch: simple range diff
			parseNameStatus(rootDir, lastMarker.Commit+".."+currentCommit, diff)
		} else {
			// Different branch: diff from merge base
			mergeBase := gitMergeBase(rootDir, lastMarker.Commit, currentCommit)
			if mergeBase != "" {
				parseNameStatus(rootDir, mergeBase+".."+currentCommit, diff)
			}
		}
	} else {
		// Commit gone (rebased/force-pushed): fall back to timestamp
		parseNameStatusSince(rootDir, lastMarker.Timestamp, diff)
	}

	diff.Summary = buildDiffSummary(diff, lastMarker)
	return diff, nil
}

// DetectStaleMemories cross-references changed files against stored memories.
// Returns IDs of memories whose associated files have been modified or deleted.
func DetectStaleMemories(diff *StructuralDiff, store Store, namespace string) []string {
	if diff == nil || diff.TotalChanges() == 0 {
		return nil
	}

	// Build set of changed files
	changedFiles := make(map[string]bool)
	for _, f := range diff.FilesModified {
		changedFiles[f] = true
	}
	for _, f := range diff.FilesDeleted {
		changedFiles[f] = true
	}

	// Load detail memories with file associations
	details, err := store.GetDetailMemories(namespace)
	if err != nil {
		return nil
	}

	var staleIDs []string
	for _, d := range details {
		if len(d.Files) == 0 {
			continue
		}
		for _, f := range d.Files {
			if changedFiles[f] {
				staleIDs = append(staleIDs, d.ID)
				break
			}
		}
	}
	return staleIDs
}

// GetGitState returns the current branch and commit hash for session marker recording.
func GetGitState(rootDir string) (branch, commit string) {
	if !isGitRepo(rootDir) {
		return "", ""
	}
	return gitCurrentBranch(rootDir), gitCurrentCommit(rootDir)
}

// countCommitsBetween returns the number of commits reachable from `to` but
// not from `from` (i.e. how far behind a stored code map is). Returns 0 when
// the range can't be resolved (shallow clone, rebased/GC'd commit, non-repo).
func countCommitsBetween(rootDir, from, to string) int {
	if from == "" || to == "" || from == to || !isGitRepo(rootDir) {
		return 0
	}
	cmd := exec.Command("git", "rev-list", "--count", from+".."+to)
	cmd.Dir = rootDir
	out, err := cmd.Output()
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0
	}
	return n
}

// ChangedFilesSince returns repo-relative (slash-separated) paths that differ
// between fromCommit and the current working tree — committed, staged, and
// unstaged changes — plus untracked files. ok=false when the diff cannot be
// computed (non-repo, rebased/GC'd commit, shallow clone), in which case
// callers should fall back to a full rescan.
func ChangedFilesSince(rootDir, fromCommit string) (files []string, ok bool) {
	if fromCommit == "" || !isGitRepo(rootDir) {
		return nil, false
	}

	cmd := exec.Command("git", "diff", "--name-only", fromCommit)
	cmd.Dir = rootDir
	out, err := cmd.Output()
	if err != nil {
		return nil, false
	}

	seen := make(map[string]bool)
	for _, line := range strings.Split(string(out), "\n") {
		if line = strings.TrimSpace(line); line != "" && !seen[line] {
			seen[line] = true
			files = append(files, line)
		}
	}

	// Untracked files are invisible to `git diff` but belong in the map.
	cmd = exec.Command("git", "ls-files", "--others", "--exclude-standard")
	cmd.Dir = rootDir
	if out, err := cmd.Output(); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			if line = strings.TrimSpace(line); line != "" && !seen[line] {
				seen[line] = true
				files = append(files, line)
			}
		}
	}

	return files, true
}

// --- Git operations (via os/exec) ---

func isGitRepo(dir string) bool {
	cmd := exec.Command("git", "rev-parse", "--git-dir")
	cmd.Dir = dir
	return cmd.Run() == nil
}

func gitCurrentBranch(dir string) string {
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func gitCurrentCommit(dir string) string {
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func commitExists(dir, commit string) bool {
	cmd := exec.Command("git", "cat-file", "-t", commit)
	cmd.Dir = dir
	return cmd.Run() == nil
}

func gitMergeBase(dir, a, b string) string {
	cmd := exec.Command("git", "merge-base", a, b)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func parseNameStatus(dir, rangeSpec string, diff *StructuralDiff) {
	cmd := exec.Command("git", "diff", "--name-status", rangeSpec)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return
	}
	parseNameStatusOutput(string(out), diff)
}

func parseNameStatusSince(dir string, since time.Time, diff *StructuralDiff) {
	sinceStr := since.Format("2006-01-02T15:04:05")
	cmd := exec.Command("git", "log", "--since="+sinceStr, "--name-status", "--format=")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return
	}
	parseNameStatusOutput(string(out), diff)
}

func parseNameStatusOutput(output string, diff *StructuralDiff) {
	// Deduplicate: a file might appear multiple times across commits
	added := make(map[string]bool)
	modified := make(map[string]bool)
	deleted := make(map[string]bool)

	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		status := parts[0]
		file := parts[1]

		switch {
		case strings.HasPrefix(status, "A"):
			added[file] = true
			delete(deleted, file) // un-delete if re-added
		case strings.HasPrefix(status, "M"):
			modified[file] = true
		case strings.HasPrefix(status, "D"):
			deleted[file] = true
			delete(added, file)
		case strings.HasPrefix(status, "R"):
			// Renamed: parts[1] is old, parts[2] is new (if present)
			if len(parts) >= 3 {
				deleted[parts[1]] = true
				added[parts[2]] = true
			}
		}
	}

	for f := range added {
		diff.FilesAdded = append(diff.FilesAdded, f)
	}
	for f := range modified {
		diff.FilesModified = append(diff.FilesModified, f)
	}
	for f := range deleted {
		diff.FilesDeleted = append(diff.FilesDeleted, f)
	}
}

// --- Summary generation (template-based, no LLM) ---

func buildDiffSummary(diff *StructuralDiff, lastMarker *SessionMarker) string {
	if diff.TotalChanges() == 0 {
		return "No changes since last session."
	}

	elapsed := time.Since(lastMarker.Timestamp)
	elapsedStr := formatElapsed(elapsed)

	var sb strings.Builder
	if diff.SameBranch {
		sb.WriteString(fmt.Sprintf("Since last session (%s ago, branch %s):\n", elapsedStr, diff.ToBranch))
	} else {
		sb.WriteString(fmt.Sprintf("Since last session (%s ago, branch changed: %s → %s):\n", elapsedStr, diff.FromBranch, diff.ToBranch))
	}

	if len(diff.FilesModified) > 0 {
		files := truncateFileList(diff.FilesModified, 5)
		sb.WriteString(fmt.Sprintf("  Modified (%d): %s\n", len(diff.FilesModified), strings.Join(files, ", ")))
	}
	if len(diff.FilesAdded) > 0 {
		files := truncateFileList(diff.FilesAdded, 5)
		sb.WriteString(fmt.Sprintf("  Added (%d): %s\n", len(diff.FilesAdded), strings.Join(files, ", ")))
	}
	if len(diff.FilesDeleted) > 0 {
		files := truncateFileList(diff.FilesDeleted, 5)
		sb.WriteString(fmt.Sprintf("  Deleted (%d): %s\n", len(diff.FilesDeleted), strings.Join(files, ", ")))
	}

	return strings.TrimRight(sb.String(), "\n")
}

func formatElapsed(d time.Duration) string {
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func truncateFileList(files []string, max int) []string {
	if len(files) <= max {
		return files
	}
	result := files[:max]
	result = append(result, fmt.Sprintf("(+%d more)", len(files)-max))
	return result
}

// MarshalDiff serializes a StructuralDiff to JSON.
func MarshalDiff(d *StructuralDiff) string {
	b, _ := json.Marshal(d)
	return string(b)
}
