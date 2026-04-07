package dualmem

import (
	"bufio"
	"fmt"
	"math"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// GitCommit represents a parsed git commit with its changed files.
type GitCommit struct {
	Hash    string
	Subject string
	Files   []string
	Date    time.Time
}

// GitCoChangeEdge represents a weighted co-change relationship between two files,
// derived from git commit history.
type GitCoChangeEdge struct {
	FileA   string
	FileB   string
	CoCount int
	Weight  float64
	Reasons []string
}

// cochangeKey returns a canonical (lexicographically sorted) key for a file pair.
func cochangeKey(a, b string) string {
	if a > b {
		return a + "\x00" + b
	}
	return b + "\x00" + a
}

// ParseGitLog runs git log in the given repo directory and returns parsed commits.
// depthMonths controls how far back to look (0 = all history).
func ParseGitLog(repoDir string, depthMonths int) ([]GitCommit, error) {
	sinceFlag := ""
	if depthMonths > 0 {
		sinceFlag = fmt.Sprintf("--since=%d months ago", depthMonths)
	}

	args := []string{"log", "--name-only", "--format=%H %aI %s", "--no-merges"}
	if sinceFlag != "" {
		args = append(args, sinceFlag)
	}

	cmd := exec.Command("git", args...)
	cmd.Dir = repoDir

	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git log: %w", err)
	}

	return parseGitLogOutput(string(out)), nil
}

// parseGitLogOutput parses the output of git log --name-only --format="%H %aI %s" --no-merges
func parseGitLogOutput(raw string) []GitCommit {
	var commits []GitCommit
	var current *GitCommit

	scanner := bufio.NewScanner(strings.NewReader(raw))
	for scanner.Scan() {
		line := scanner.Text()

		// Commit header lines start with a hex hash (40 chars)
		if len(line) > 41 && isHex(line[:40]) && line[40] == ' ' {
			// Flush previous commit
			if current != nil {
				commits = append(commits, *current)
			}

			// Parse: <hash> <iso-date> <subject>
			hash := line[:40]
			rest := line[41:]

			// Find the date (ISO 8601 format, ends with space before subject)
			// Format: 2024-01-15T10:30:00+07:00 or 2024-01-15T10:30:00Z
			spaceIdx := strings.Index(rest, " ")
			if spaceIdx == -1 {
				continue
			}
			dateStr := rest[:spaceIdx]
			subject := rest[spaceIdx+1:]

			date, err := time.Parse(time.RFC3339, dateStr)
			if err != nil {
				// Try alternative layouts
				date, _ = time.Parse("2006-01-02 15:04:05 -0700", dateStr)
			}

			current = &GitCommit{
				Hash:    hash,
				Subject: subject,
				Date:    date,
			}
		} else if current != nil && strings.TrimSpace(line) != "" {
			// This is a file name line
			current.Files = append(current.Files, strings.TrimSpace(line))
		}
		// Empty lines separate commits (handled by next commit header)
	}

	// Flush last commit
	if current != nil {
		commits = append(commits, *current)
	}

	return commits
}

// isHex checks if a string is all hex characters.
func isHex(s string) bool {
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// BuildGitCoChangeEdges takes parsed commits and produces co-change edges.
// For each commit with ≤50 files, it generates N-choose-2 pairs with exponential
// time decay (default 3-month half-life).
func BuildGitCoChangeEdges(commits []GitCommit) map[string]*GitCoChangeEdge {
	return BuildGitCoChangeEdgesDecay(commits, 3.0)
}

// BuildGitCoChangeEdgesDecay is like BuildGitCoChangeEdges but with configurable half-life in months.
func BuildGitCoChangeEdgesDecay(commits []GitCommit, halfLifeMonths float64) map[string]*GitCoChangeEdge {
	edges := make(map[string]*GitCoChangeEdge)
	now := time.Now()
	halfLifeSeconds := halfLifeMonths * 30.5 * 24 * 3600 // approximate months to seconds

	for _, commit := range commits {
		// Skip massive commits (likely auto-generated or dependency bumps)
		if len(commit.Files) > 50 {
			continue
		}
		// Skip commits with <2 files (no pairs to form)
		if len(commit.Files) < 2 {
			continue
		}

		// Compute time decay factor
		ageSeconds := now.Sub(commit.Date).Seconds()
		decay := math.Pow(0.5, ageSeconds/halfLifeSeconds)

		// Generate all N-choose-2 pairs
		for i := 0; i < len(commit.Files); i++ {
			for j := i + 1; j < len(commit.Files); j++ {
				a := commit.Files[i]
				b := commit.Files[j]
				key := cochangeKey(a, b)

				if edge, ok := edges[key]; ok {
					edge.CoCount++
					edge.Weight += decay
					edge.Reasons = append(edge.Reasons, commit.Hash[:8]+" "+commit.Subject)
				} else {
					edges[key] = &GitCoChangeEdge{
						FileA:   a,
						FileB:   b,
						CoCount: 1,
						Weight:  decay,
						Reasons: []string{commit.Hash[:8] + " " + commit.Subject},
					}
				}
			}
		}
	}

	return edges
}

// BuildGitCoChangeGraph is the main entry point. It parses git log with the given depth
// and builds co-change edges. If fewer than minEdges are found, it retries with full history
// (depthMonths=0).
func BuildGitCoChangeGraph(repoDir string, depthMonths, minEdges int) (map[string]*GitCoChangeEdge, error) {
	commits, err := ParseGitLog(repoDir, depthMonths)
	if err != nil {
		return nil, fmt.Errorf("parse git log: %w", err)
	}

	edges := BuildGitCoChangeEdges(commits)

	// Adaptive: if too few edges, expand to full history
	if len(edges) < minEdges && depthMonths > 0 {
		fullCommits, err := ParseGitLog(repoDir, 0)
		if err != nil {
			return nil, fmt.Errorf("parse git log (full history): %w", err)
		}
		edges = BuildGitCoChangeEdges(fullCommits)
	}

	return edges, nil
}

// CoChangeNeighbors returns the top-N neighbor files for the given seed files,
// sorted by descending weight. Returns a map of file path → weight.
func CoChangeNeighbors(edges map[string]*GitCoChangeEdge, seedFiles []string, topN int) map[string]float64 {
	seedSet := make(map[string]bool, len(seedFiles))
	for _, f := range seedFiles {
		seedSet[f] = true
	}

	// Collect all neighbors
	type neighbor struct {
		path   string
		weight float64
	}
	var neighbors []neighbor

	for _, edge := range edges {
		var candidate string
		if seedSet[edge.FileA] && !seedSet[edge.FileB] {
			candidate = edge.FileB
		} else if seedSet[edge.FileB] && !seedSet[edge.FileA] {
			candidate = edge.FileA
		}
		if candidate == "" {
			continue
		}

		// Keep max weight per neighbor
		found := false
		for i := range neighbors {
			if neighbors[i].path == candidate {
				if edge.Weight > neighbors[i].weight {
					neighbors[i].weight = edge.Weight
				}
				found = true
				break
			}
		}
		if !found {
			neighbors = append(neighbors, neighbor{candidate, edge.Weight})
		}
	}

	// Sort by weight descending
	sort.Slice(neighbors, func(i, j int) bool {
		return neighbors[i].weight > neighbors[j].weight
	})

	// Return top-N
	if topN > 0 && len(neighbors) > topN {
		neighbors = neighbors[:topN]
	}

	result := make(map[string]float64, len(neighbors))
	for _, n := range neighbors {
		result[n.path] = n.weight
	}
	return result
}
