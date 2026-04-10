package dualmem

import "testing"

func TestRankModules_EmptyCodemap(t *testing.T) {
	targets := RankModules(nil, nil)
	if len(targets) != 0 {
		t.Fatalf("expected 0 targets, got %d", len(targets))
	}
}

func TestScoreCuriosityTarget(t *testing.T) {
	tests := []struct {
		name                        string
		mg, ch, cx, gh, st, wMin, wMax float64
	}{
		{"zero_memories_high_change", 1.0, 0.8, 0.5, 0.3, 0.0, 0.6, 0.8},
		{"fully_covered_no_change", 0.0, 0.0, 0.0, 0.0, 0.0, 0.0, 0.01},
		{"stale_memories", 0.0, 0.0, 0.0, 0.0, 0.3, 0.02, 0.05},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			score := scoreCuriosity(tt.mg, tt.ch, tt.cx, tt.gh, tt.st)
			if score < tt.wMin || score > tt.wMax {
				t.Errorf("score=%f want [%f,%f]", score, tt.wMin, tt.wMax)
			}
		})
	}
}

func TestRankModules_Ordering(t *testing.T) {
	cm := &CodeMap{Zoom2: []ModuleMap{
		{Path: "pkg/uncovered", Language: "go", KeyTypes: []string{"A", "B", "C"}, Files: []FileInfo{{RelPath: "pkg/uncovered/main.go"}}},
		{Path: "pkg/covered", Language: "go", Files: []FileInfo{{RelPath: "pkg/covered/main.go"}}},
	}}
	signals := &CuriositySignals{
		MemoryCounts:    map[string]int{"pkg/covered": 5},
		ChangedFiles:    map[string]int{},
		EdgeCounts:      map[string]int{"pkg/uncovered": 15},
		CoChangeWeights: map[string]float64{},
		StaleModules:    map[string]bool{},
	}
	targets := RankModules(cm, signals)
	if len(targets) != 2 {
		t.Fatalf("expected 2, got %d", len(targets))
	}
	if targets[0].ModulePath != "pkg/uncovered" {
		t.Errorf("expected uncovered first, got %s", targets[0].ModulePath)
	}
	if targets[0].Score <= targets[1].Score {
		t.Errorf("wrong order: %f <= %f", targets[0].Score, targets[1].Score)
	}
}
