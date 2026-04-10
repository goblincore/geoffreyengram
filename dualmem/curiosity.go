package dualmem

import (
	"sort"
)

type CuriositySignals struct {
	MemoryCounts    map[string]int
	ChangedFiles    map[string]int
	EdgeCounts      map[string]int
	CoChangeWeights map[string]float64
	StaleModules    map[string]bool
	MaxCoChange     float64
}

const (
	weightMemoryGap  = 0.35
	weightChangeHeat = 0.25
	weightComplexity = 0.20
	weightGitHeat    = 0.10
	weightStaleness  = 0.10
)

func scoreCuriosity(memoryGap, changeHeat, complexity, gitHeat, staleness float64) float64 {
	return memoryGap*weightMemoryGap + changeHeat*weightChangeHeat + complexity*weightComplexity + gitHeat*weightGitHeat + staleness*weightStaleness
}

func RankModules(cm *CodeMap, signals *CuriositySignals) []CuriosityTarget {
	if cm == nil || signals == nil {
		return nil
	}
	targets := make([]CuriosityTarget, 0, len(cm.Zoom2))
	for _, mod := range cm.Zoom2 {
		memCount := signals.MemoryCounts[mod.Path]
		memoryGap := 1.0 - clampf(float64(memCount)/3.0, 0, 1)
		changedCount := signals.ChangedFiles[mod.Path]
		changeHeat := clampf(float64(changedCount)/5.0, 0, 1)
		edgeCount := signals.EdgeCounts[mod.Path]
		complexity := clampf(float64(edgeCount)/20.0, 0, 1)
		var gitHeat float64
		if signals.MaxCoChange > 0 {
			gitHeat = clampf(signals.CoChangeWeights[mod.Path]/signals.MaxCoChange, 0, 1)
		}
		var staleness float64
		if signals.StaleModules[mod.Path] {
			staleness = 0.3
		}
		score := scoreCuriosity(memoryGap, changeHeat, complexity, gitHeat, staleness)
		files := make([]string, 0, len(mod.Files))
		for _, f := range mod.Files {
			files = append(files, f.RelPath)
		}
		targets = append(targets, CuriosityTarget{
			ModulePath: mod.Path, Score: score,
			Signals: map[string]float64{"memory_gap": memoryGap, "change_heat": changeHeat, "complexity": complexity, "git_heat": gitHeat, "staleness": staleness},
			Files: files,
		})
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].Score > targets[j].Score })
	return targets
}

func clampf(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
