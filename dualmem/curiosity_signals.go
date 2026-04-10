package dualmem

import (
	"context"
	"time"
)

// GatherCuriositySignals builds a CuriositySignals from live codebase state.
// It cross-references the codemap modules with memory counts, git-changed files,
// structural edge complexity, co-change weights, and memory staleness.
//
// edges comes from CodemapScanResult.Edges — callers should pass the result of
// a recent ScanCodebase call to avoid a redundant scan.
//
// lastCommit, if non-empty, is used as the baseline for detecting changed files.
// Pass "" to skip git-change detection (e.g., in tests or non-git repos).
func (e *Engine) GatherCuriositySignals(ctx context.Context, namespace string, cm *CodeMap, edges []StructuralEdge, lastCommit string) (*CuriositySignals, error) {
	signals := &CuriositySignals{
		MemoryCounts:    make(map[string]int),
		ChangedFiles:    make(map[string]int),
		EdgeCounts:      make(map[string]int),
		CoChangeWeights: make(map[string]float64),
		StaleModules:    make(map[string]bool),
	}

	if cm == nil {
		return signals, nil
	}

	// Build a map from file path → module path for reverse lookups.
	fileToModule := make(map[string]string, len(cm.Zoom2)*5)
	for _, mod := range cm.Zoom2 {
		for _, f := range mod.Files {
			fileToModule[f.RelPath] = mod.Path
		}
	}

	// 1. Memory counts per module.
	// Use GetMemoryCountByFiles if the store exposes it; otherwise fall back to 0.
	type fileCounter interface {
		GetMemoryCountByFiles(userID string, files []string) (int, error)
	}
	if fc, ok := e.store.(fileCounter); ok {
		for _, mod := range cm.Zoom2 {
			files := make([]string, 0, len(mod.Files))
			for _, f := range mod.Files {
				files = append(files, f.RelPath)
			}
			count, err := fc.GetMemoryCountByFiles(namespace, files)
			if err == nil {
				signals.MemoryCounts[mod.Path] = count
			}
		}
	}

	// 2. Git-changed files since lastCommit.
	if lastCommit != "" && e.cfg != nil && e.cfg.RootDir != "" {
		marker := &SessionMarker{
			Commit:    lastCommit,
			Timestamp: time.Now().Add(-24 * time.Hour), // sentinel
		}
		diff, err := ComputeStructuralDiff(e.cfg.RootDir, marker)
		if err == nil && diff != nil {
			for _, f := range diff.FilesModified {
				if modPath, ok := fileToModule[f]; ok {
					signals.ChangedFiles[modPath]++
				}
			}
			for _, f := range diff.FilesAdded {
				if modPath, ok := fileToModule[f]; ok {
					signals.ChangedFiles[modPath]++
				}
			}
		}
	}

	// 3. Structural edge complexity per module.
	// Count edges where either endpoint belongs to this module.
	for _, edge := range edges {
		if modPath, ok := fileToModule[edge.SourcePath]; ok {
			signals.EdgeCounts[modPath]++
		}
		if modPath, ok := fileToModule[edge.TargetPath]; ok {
			signals.EdgeCounts[modPath]++
		}
	}

	// 4. Co-change weights — aggregate neighbor strengths per module.
	allFiles := make([]string, 0, len(fileToModule))
	for f := range fileToModule {
		allFiles = append(allFiles, f)
	}
	cochange := e.GetCoChangeForPaths(namespace, allFiles, 0.0)
	for filePath, strength := range cochange {
		if modPath, ok := fileToModule[filePath]; ok {
			if strength > signals.CoChangeWeights[modPath] {
				signals.CoChangeWeights[modPath] = strength
			}
			if strength > signals.MaxCoChange {
				signals.MaxCoChange = strength
			}
		}
	}

	// 5. Stale modules — modules with memories but changed files.
	for modPath, memCount := range signals.MemoryCounts {
		if memCount > 0 && signals.ChangedFiles[modPath] > 0 {
			signals.StaleModules[modPath] = true
		}
	}

	return signals, nil
}
