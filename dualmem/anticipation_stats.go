package dualmem

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// AnticipationStats summarizes the hit rate of anticipatory pre-exploration.
type AnticipationStats struct {
	Total       int                 `json:"total"`
	Hits        int                 `json:"hits"`
	Misses      int                 `json:"misses"`
	HitRate     float64             `json:"hit_rate"`
	AvgLeadTime time.Duration       `json:"avg_lead_time_ns"`
	BySource    map[string]*HitMiss `json:"by_source"`
	TopHits     []FileHitStat       `json:"top_hits,omitempty"`
	TopMisses   []FileHitStat       `json:"top_misses,omitempty"`
}

// HitMiss tracks hit/miss counts for a source category.
type HitMiss struct {
	Hits   int `json:"hits"`
	Misses int `json:"misses"`
}

// FileHitStat tracks hit rate for a specific file.
type FileHitStat struct {
	Path    string  `json:"path"`
	Hits    int     `json:"hits"`
	Total   int     `json:"total"`
	HitRate float64 `json:"hit_rate"`
}

// ComputeAnticipationStats analyzes anticipation memories to determine
// whether pre-explored files were actually useful (surfaced in context assembly
// or touched by later non-anticipation memories within the time window).
func (e *Engine) ComputeAnticipationStats(ctx context.Context, namespace string, window time.Duration) (*AnticipationStats, error) {
	// Get all anticipation memories.
	type byTypeGetter interface {
		GetDetailsByType(userID, memType string) ([]detailWithVector, error)
	}
	btg, ok := e.store.(byTypeGetter)
	if !ok {
		return nil, fmt.Errorf("store does not support GetDetailsByType")
	}
	antMems, err := btg.GetDetailsByType(namespace, "anticipation")
	if err != nil {
		return nil, fmt.Errorf("get anticipation memories: %w", err)
	}

	// Get all non-anticipation memories for hit checking.
	type allGetter interface {
		GetDetailMemories(userID string) ([]detailWithVector, error)
	}
	ag, ok := e.store.(allGetter)
	if !ok {
		return nil, fmt.Errorf("store does not support GetDetailMemories")
	}
	allMems, err := ag.GetDetailMemories(namespace)
	if err != nil {
		return nil, fmt.Errorf("get all memories: %w", err)
	}

	// Build file→time index of non-anticipation memory touches.
	type fileTouches struct {
		earliest time.Time
	}
	touchIndex := make(map[string]fileTouches)
	for _, m := range allMems {
		if m.Type == "anticipation" {
			continue
		}
		for _, f := range m.Files {
			existing, ok := touchIndex[f]
			if !ok || m.CreatedAt.Before(existing.earliest) {
				touchIndex[f] = fileTouches{earliest: m.CreatedAt}
			}
		}
	}

	// Check context snapshots for surfaced memories.
	surfacedIDs := make(map[string]bool)
	type snapshotSearcher interface {
		SearchSnapshotsForMemory(namespace, memoryID string) (bool, error)
	}
	ss, hasSnapshotSearch := e.store.(snapshotSearcher)

	stats := &AnticipationStats{
		BySource: make(map[string]*HitMiss),
	}
	fileStats := make(map[string]*FileHitStat)
	var totalLeadTime time.Duration
	leadTimeCount := 0

	for _, am := range antMems {
		stats.Total++

		// Parse source type from Sector field (e.g., "cochange:a,b→c").
		// The anticipatory worker stores the source in MemoryInput.SectorHint,
		// which maps to DetailMemory.Sector.
		sourceType := parseSourceType(am.Sector)
		if _, ok := stats.BySource[sourceType]; !ok {
			stats.BySource[sourceType] = &HitMiss{}
		}

		// Determine target file from Files field.
		targetFile := ""
		if len(am.Files) > 0 {
			targetFile = am.Files[0]
		}

		hit := false

		// Check 1: Was this memory surfaced in a context snapshot?
		if hasSnapshotSearch && !surfacedIDs[am.ID] {
			if found, err := ss.SearchSnapshotsForMemory(namespace, am.ID); err == nil && found {
				hit = true
				surfacedIDs[am.ID] = true
			}
		}

		// Check 2: Was the target file touched by non-anticipation memories within window?
		if !hit && targetFile != "" {
			if touch, ok := touchIndex[targetFile]; ok {
				leadTime := touch.earliest.Sub(am.CreatedAt)
				if leadTime > 0 && leadTime <= window {
					hit = true
					totalLeadTime += leadTime
					leadTimeCount++
				}
			}
		}

		if hit {
			stats.Hits++
			stats.BySource[sourceType].Hits++
		} else {
			stats.Misses++
			stats.BySource[sourceType].Misses++
		}

		// Track per-file stats.
		if targetFile != "" {
			fs, ok := fileStats[targetFile]
			if !ok {
				fs = &FileHitStat{Path: targetFile}
				fileStats[targetFile] = fs
			}
			fs.Total++
			if hit {
				fs.Hits++
			}
		}
	}

	if stats.Total > 0 {
		stats.HitRate = float64(stats.Hits) / float64(stats.Total)
	}
	if leadTimeCount > 0 {
		stats.AvgLeadTime = totalLeadTime / time.Duration(leadTimeCount)
	}

	// Sort file stats into top hits and top misses.
	var allFileStats []FileHitStat
	for _, fs := range fileStats {
		fs.HitRate = float64(fs.Hits) / float64(fs.Total)
		allFileStats = append(allFileStats, *fs)
	}

	sort.Slice(allFileStats, func(i, j int) bool {
		return allFileStats[i].HitRate > allFileStats[j].HitRate
	})

	for _, fs := range allFileStats {
		if fs.HitRate > 0 && len(stats.TopHits) < 5 {
			stats.TopHits = append(stats.TopHits, fs)
		}
	}
	// Reverse sort for misses.
	sort.Slice(allFileStats, func(i, j int) bool {
		return allFileStats[i].HitRate < allFileStats[j].HitRate
	})
	for _, fs := range allFileStats {
		if fs.HitRate < 1.0 && len(stats.TopMisses) < 5 {
			stats.TopMisses = append(stats.TopMisses, fs)
		}
	}

	return stats, nil
}

// parseSourceType extracts "cochange" or "structural" from the Sector field.
func parseSourceType(sector string) string {
	if strings.HasPrefix(sector, "cochange:") {
		return "cochange"
	}
	if strings.HasPrefix(sector, "structural:") {
		return "structural"
	}
	return "unknown"
}

// FormatAnticipationStats returns a human-readable report.
func FormatAnticipationStats(s *AnticipationStats, namespace string) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("=== Anticipation Stats (%s) ===\n", namespace))

	if s.Total == 0 {
		sb.WriteString("No anticipation memories found.\n")
		sb.WriteString("The anticipatory worker creates these during active sessions.\n")
		return sb.String()
	}

	sb.WriteString(fmt.Sprintf("Total: %d | Hits: %d (%.0f%%) | Misses: %d (%.0f%%)\n",
		s.Total, s.Hits, s.HitRate*100, s.Misses, (1-s.HitRate)*100))

	if s.AvgLeadTime > 0 {
		sb.WriteString(fmt.Sprintf("Avg lead time: %s\n", s.AvgLeadTime.Round(time.Second)))
	}

	if len(s.BySource) > 0 {
		sb.WriteString("\nBy source:\n")
		for src, hm := range s.BySource {
			total := hm.Hits + hm.Misses
			rate := float64(hm.Hits) / float64(total) * 100
			sb.WriteString(fmt.Sprintf("  %-12s %d/%d (%.0f%%)\n", src, hm.Hits, total, rate))
		}
	}

	if len(s.TopHits) > 0 {
		sb.WriteString("\nTop hits:\n")
		for _, f := range s.TopHits {
			sb.WriteString(fmt.Sprintf("  %-40s %d/%d (%.0f%%)\n", f.Path, f.Hits, f.Total, f.HitRate*100))
		}
	}

	if len(s.TopMisses) > 0 {
		sb.WriteString("\nTop misses:\n")
		for _, f := range s.TopMisses {
			sb.WriteString(fmt.Sprintf("  %-40s %d/%d (%.0f%%)\n", f.Path, f.Hits, f.Total, f.HitRate*100))
		}
	}

	return sb.String()
}
