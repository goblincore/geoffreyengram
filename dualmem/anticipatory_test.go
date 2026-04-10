package dualmem

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseSessionLog(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "session.jsonl")

	now := time.Now()
	entries := []string{
		fmt.Sprintf(`{"ts":"%s","tool":"Read","files":"auth.go"}`, now.Add(-30*time.Second).Format(time.RFC3339)),
		fmt.Sprintf(`{"ts":"%s","tool":"Edit","files":"auth.go"}`, now.Add(-20*time.Second).Format(time.RFC3339)),
		fmt.Sprintf(`{"ts":"%s","tool":"Read","files":"middleware.go"}`, now.Add(-10*time.Second).Format(time.RFC3339)),
	}
	os.WriteFile(logPath, []byte(strings.Join(entries, "\n")+"\n"), 0644)

	files, err := parseRecentSessionFiles(logPath, 2*time.Minute)
	if err != nil {
		t.Fatalf("parseRecentSessionFiles: %v", err)
	}
	if len(files) != 2 {
		t.Errorf("expected 2 unique files, got %d: %v", len(files), files)
	}
}

func TestPredictExplorationTargets(t *testing.T) {
	recentFiles := []string{"auth.go", "middleware.go"}
	cochangeNeighbors := map[string]float64{
		"jwt.go":     0.8,
		"session.go": 0.6,
		"auth.go":    0.9,
	}
	structuralNeighbors := map[string]float64{
		"jwt.go":    0.7,
		"crypto.go": 0.5,
	}

	candidates := predictExplorationTargets(recentFiles, cochangeNeighbors, structuralNeighbors, nil, 3)
	if len(candidates) == 0 {
		t.Fatal("expected at least 1 candidate")
	}
	for _, c := range candidates {
		if c == "auth.go" || c == "middleware.go" {
			t.Errorf("candidate %s should be filtered", c)
		}
	}
	if candidates[0] != "jwt.go" {
		t.Errorf("expected jwt.go first, got %s", candidates[0])
	}
}
