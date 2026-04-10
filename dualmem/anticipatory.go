package dualmem

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

type sessionLogEntry struct {
	Ts    string `json:"ts"`
	Tool  string `json:"tool"`
	Files string `json:"files"`
}

// parseRecentSessionFiles reads a session JSONL log and returns unique file paths
// from entries within the given duration window.
func parseRecentSessionFiles(logPath string, window time.Duration) ([]string, error) {
	f, err := os.Open(logPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	cutoff := time.Now().Add(-window)
	fileSet := make(map[string]bool)

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var entry sessionLogEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}
		ts, err := time.Parse(time.RFC3339, entry.Ts)
		if err != nil {
			continue
		}
		if ts.Before(cutoff) {
			continue
		}
		if entry.Files != "" {
			fileSet[entry.Files] = true
		}
	}

	files := make([]string, 0, len(fileSet))
	for f := range fileSet {
		files = append(files, f)
	}
	return files, nil
}

// predictExplorationTargets returns the top-N files to pre-explore based on
// co-change and structural neighbors, filtering out already-touched files.
func predictExplorationTargets(recentFiles []string, cochangeNeighbors, structuralNeighbors map[string]float64, freshMemoryFiles map[string]bool, limit int) []string {
	touchedSet := make(map[string]bool)
	for _, f := range recentFiles {
		touchedSet[f] = true
	}

	scoreMap := make(map[string]float64)
	for path, strength := range cochangeNeighbors {
		if touchedSet[path] || (freshMemoryFiles != nil && freshMemoryFiles[path]) {
			continue
		}
		scoreMap[path] += strength * 0.5
	}
	for path, proximity := range structuralNeighbors {
		if touchedSet[path] || (freshMemoryFiles != nil && freshMemoryFiles[path]) {
			continue
		}
		scoreMap[path] += proximity * 0.5
	}

	type candidate struct {
		path  string
		score float64
	}
	candidates := make([]candidate, 0, len(scoreMap))
	for path, score := range scoreMap {
		candidates = append(candidates, candidate{path, score})
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].score > candidates[j].score
	})

	result := make([]string, 0, limit)
	for i := 0; i < len(candidates) && i < limit; i++ {
		result = append(result, candidates[i].path)
	}
	return result
}

// anticipatoryWorker runs as a pipeline goroutine during active sessions.
func (e *Engine) anticipatoryWorker(ctx context.Context, namespace string, logPath string) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	cooldown := make(map[string]time.Time)
	cooldownDuration := 10 * time.Minute

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := e.getExplorerGenerator(); err != nil {
				continue
			}

			recentFiles, err := parseRecentSessionFiles(logPath, 2*time.Minute)
			if err != nil || len(recentFiles) == 0 {
				continue
			}

			cochangeNeighbors := e.GetCoChangeForPaths(namespace, recentFiles, 0.5)
			structuralNeighbors := e.GetStructuralNeighborPaths(namespace, recentFiles, 2)

			candidates := predictExplorationTargets(recentFiles, cochangeNeighbors, structuralNeighbors, nil, 3)

			explored := 0
			for _, path := range candidates {
				if explored >= 2 {
					break
				}
				if cooldownTime, ok := cooldown[path]; ok && time.Since(cooldownTime) < cooldownDuration {
					continue
				}

				result, err := e.Explore(ctx, namespace, path, 2000)
				if err != nil || result.Summary == "" {
					continue
				}

				source := fmt.Sprintf("cochange:%s→%s", strings.Join(recentFiles[:min(len(recentFiles), 2)], ","), path)
				e.AddWithOptions(ctx, MemoryInput{
					UserMessage: result.Summary,
					SectorHint:  source,
					Type:        "anticipation",
					Salience:    0.65,
					Files:       []string{path},
				}, namespace)

				cooldown[path] = time.Now()
				explored++
			}
		}
	}
}
