package dualmem

import (
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// StalenessResult describes why a memory may be stale.
type StalenessResult struct {
	IsStale      bool    `json:"is_stale"`
	Reason       string  `json:"reason,omitempty"`
	ChangedRatio float64 `json:"changed_ratio,omitempty"` // fraction of referenced files that changed
}

// CheckFileStaleness checks whether files referenced by a memory have changed
// since a given commit. A memory is considered stale if more than 50% of its
// referenced files have been modified or deleted.
func CheckFileStaleness(repoDir, sinceCommit string, files []string) StalenessResult {
	if len(files) == 0 {
		return StalenessResult{IsStale: false}
	}
	if sinceCommit == "" {
		return StalenessResult{IsStale: false}
	}

	// Get list of files changed between sinceCommit and HEAD
	cmd := exec.Command("git", "diff", "--name-only", sinceCommit+"..HEAD")
	cmd.Dir = repoDir
	out, err := cmd.Output()
	if err != nil {
		// If git fails (bad commit, not a repo), we can't determine staleness
		return StalenessResult{IsStale: false, Reason: "git diff failed: " + err.Error()}
	}

	changedSet := make(map[string]bool)
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			changedSet[line] = true
		}
	}

	changed := 0
	for _, f := range files {
		if changedSet[f] {
			changed++
		}
	}

	ratio := float64(changed) / float64(len(files))
	stale := ratio > 0.5

	result := StalenessResult{
		IsStale:      stale,
		ChangedRatio: ratio,
	}
	if stale {
		result.Reason = fmt.Sprintf("%d/%d referenced files changed since %s", changed, len(files), sinceCommit)
	}
	return result
}

// CheckSymbolStaleness checks whether symbol names mentioned in a memory's text
// still exist in the current set of tags for a file. Tags are expected to be
// symbol names (e.g., "ValidateToken", "Engine", "ScanCodebase").
func CheckSymbolStaleness(file, memoryText string, currentTags []Tag) StalenessResult {
	// Extract potential symbol names from memory text (CamelCase, snake_case, or dot-separated)
	re := regexp.MustCompile(`\b([A-Z][a-zA-Z0-9]+(?:\.[A-Z][a-zA-Z0-9]+)*)\b`)
	matches := re.FindAllString(memoryText, -1)

	if len(matches) == 0 {
		return StalenessResult{IsStale: false}
	}

	// Build set of current symbol names
	tagSet := make(map[string]bool, len(currentTags))
	for _, tag := range currentTags {
		tagSet[tag.Name] = true
	}

	missing := 0
	for _, sym := range matches {
		if !tagSet[sym] {
			missing++
		}
	}

	// If any mentioned symbol is missing, consider it potentially stale
	if missing > 0 {
		return StalenessResult{
			IsStale: true,
			Reason:  fmt.Sprintf("%d/%d mentioned symbols not found in current tags for %s", missing, len(matches), file),
		}
	}
	return StalenessResult{IsStale: false}
}

// CheckAgeStaleness checks whether a memory is too old. A memory is stale if
// its age exceeds maxAgeDays and it has not been recently reinforced.
func CheckAgeStaleness(createdAt time.Time, maxAgeDays int, reinforced bool) StalenessResult {
	if reinforced {
		return StalenessResult{IsStale: false}
	}

	age := time.Since(createdAt)
	maxAge := time.Duration(maxAgeDays) * 24 * time.Hour

	if age > maxAge {
		return StalenessResult{
			IsStale: true,
			Reason:  fmt.Sprintf("memory is %d days old (max %d days)", int(age.Hours()/24), maxAgeDays),
		}
	}
	return StalenessResult{IsStale: false}
}

// Tag represents a named symbol extracted from a source file.
// Used by CheckSymbolStaleness to verify that symbols mentioned in memories
// still exist in the current codebase.
type Tag struct {
	Name string `json:"name"`
	Kind string `json:"kind"` // "function", "type", "method", "variable", etc.
	File string `json:"file"`
}
