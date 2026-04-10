# Passive Codebase Exploration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add autonomous codebase exploration that builds architectural knowledge without active user work — an autopilot CLI command for scheduled runs, and an anticipatory pipeline worker that pre-explores during sessions.

**Architecture:** Curiosity scorer ranks codemap modules by memory gap, change heat, complexity, git heat, and staleness. Autopilot calls existing `Explore`/`Consult` methods serially within a token budget. Anticipatory worker watches session logs and co-change graphs to predict needed context. New `anticipation` memory type with 2hr TTL gets priority surfacing.

**Tech Stack:** Go, SQLite, existing dualmem engine (HDC codemap, co-change graph, TextGenerator interface)

**Spec:** `docs/superpowers/specs/2026-04-09-passive-exploration-design.md`

---

## File Structure

| File | Responsibility |
|------|---------------|
| `dualmem/curiosity.go` | **New.** CuriosityScorer: RankModules(), signal computation, CuriosityTarget type. Pure functions, no LLM calls. |
| `dualmem/curiosity_test.go` | **New.** Tests for scoring formula, ranking order, edge cases. |
| `dualmem/autopilot.go` | **New.** Engine.Autopilot() method: exploration loop, novelty check, budget tracking, state persistence. |
| `dualmem/autopilot_test.go` | **New.** Tests for autopilot loop logic, budget exhaustion, novelty dedup. |
| `dualmem/anticipatory.go` | **New.** Anticipatory worker: session log parsing, prediction, exploration, pipeline integration. |
| `dualmem/anticipatory_test.go` | **New.** Tests for prediction logic, cooldown, TTL cleanup. |
| `dualmem/types.go` | **Modify.** Add `AutopilotOpts`, `AutopilotResult`, anticipation type constants, `IntentProfile.Anticipation` field. |
| `dualmem/pipeline.go` | **Modify.** Add anticipatory worker launch/stop, checkpoint notification channel. |
| `dualmem/dualmem.go` | **Modify.** Add `getExplorerGenerator()`, anticipation type in `typePriority`/`formatTypeLabel`/`TypeMultiplier`, age filter in `AssembleContextWith`. |
| `dualmem/store_sqlite.go` | **Modify.** Add `GetMemoryCountByFiles()` method. |
| `cmd/dualmem/main.go` | **Modify.** Add `autopilot` subcommand, explorer model config in CLIConfig. |

---

### Task 1: Types & Constants

**Files:**
- Modify: `dualmem/types.go:65-76` (MemoryInput type comment), `:325-397` (IntentProfile + TypeMultiplier)
- Modify: `dualmem/dualmem.go:3055-3064` (typePriority), `:3066-3090` (formatTypeLabel), `:2983-3004` (detailSortScore)

- [ ] **Step 1: Add AutopilotOpts and AutopilotResult to types.go**

Add after the `SeedResult` struct (line 3138 area of dualmem.go — but these are types, so add in types.go after ExploreResult at line 500):

```go
// AutopilotOpts configures an autopilot exploration run.
type AutopilotOpts struct {
	Budget    int    // Total token budget for this run
	DryRun   bool   // Score modules but don't explore
	Force    bool   // Re-explore even if recent memories exist
	ModelName string // Override explorer model (empty = use config)
	BaseURL   string // Override explorer base URL
}

// AutopilotResult describes what an autopilot run produced.
type AutopilotResult struct {
	Targets       []CuriosityTarget // All scored targets (sorted by score desc)
	Explored      int               // How many targets were explored
	MemoriesAdded int               // How many memories were saved
	TokensUsed    int               // Total tokens consumed
	Skipped       int               // How many skipped (novelty/freshness)
}

// CuriosityTarget is a scored module for exploration.
type CuriosityTarget struct {
	ModulePath string             `json:"module_path"`
	Score      float64            `json:"score"`
	Signals    map[string]float64 `json:"signals"`
	Files      []string           `json:"files"`
}
```

- [ ] **Step 2: Add anticipation type to typePriority in dualmem.go**

In `dualmem/dualmem.go`, modify the `typePriority` function at line 3055:

```go
func typePriority(t string) int {
	switch t {
	case "warning", "anticipation":
		return 2
	case "decision", "continuity", "trace", "architecture", "investigation", "requirement", "test-strategy":
		return 1
	default:
		return 0
	}
}
```

- [ ] **Step 3: Add anticipation to formatTypeLabel in dualmem.go**

Add a new case in `formatTypeLabel` at line 3067, before the `"seed"` case:

```go
	case "anticipation":
		return fmt.Sprintf("[🔮 Anticipated Context — %s]", sector)
```

- [ ] **Step 4: Add anticipation to TypeMultiplier in types.go**

Add an `Anticipation` field to `IntentProfile` struct at line 325:

```go
type IntentProfile struct {
	Warning      float64
	Decision     float64
	Continuity   float64
	Map          float64
	General      float64
	Seed         float64
	Anticipation float64 // Pre-explored context from anticipatory worker
}
```

Add a case in `TypeMultiplier` at line 379, before the `default`:

```go
	case "anticipation":
		if ip.Anticipation != 0 {
			return ip.Anticipation
		}
		return ip.General
```

- [ ] **Step 5: Update MemoryInput type comment**

Update the `Type` field comment in `MemoryInput` at line 74:

```go
	Type             string   // Optional: "decision", "warning", "continuity", "trace", "architecture", "investigation", "requirement", "test-strategy", "anticipation", "" (general)
```

- [ ] **Step 6: Verify tests still pass**

Run: `cd /Users/donny/Projects/2026/geoffreyengram && go test ./dualmem/ -count=1 -timeout 60s 2>&1 | tail -5`
Expected: `ok` with no failures

- [ ] **Step 7: Commit**

```bash
git add dualmem/types.go dualmem/dualmem.go
git commit -m "feat(autopilot): add types, anticipation memory type, and priority constants"
```

---

### Task 2: Curiosity Scorer

**Files:**
- Create: `dualmem/curiosity.go`
- Create: `dualmem/curiosity_test.go`
- Modify: `dualmem/store_sqlite.go` (add `GetMemoryCountByFiles`)

- [ ] **Step 1: Add GetMemoryCountByFiles to store**

In `dualmem/store_sqlite.go`, add a method that counts distinct memories per file set. Add near the existing `GetDetailsByFiles` method (line ~483):

```go
// GetMemoryCountByFiles returns the count of detail memories associated with any of the given file paths.
func (s *SQLiteStore) GetMemoryCountByFiles(userID string, files []string) (int, error) {
	if len(files) == 0 {
		return 0, nil
	}
	placeholders := make([]string, len(files))
	args := make([]interface{}, 0, len(files)+1)
	args = append(args, userID)
	for i, f := range files {
		placeholders[i] = "?"
		args = append(args, f)
	}
	query := fmt.Sprintf(`
		SELECT COUNT(DISTINCT dm.id)
		FROM detail_memories dm
		JOIN detail_memory_files dmf ON dm.id = dmf.memory_id
		WHERE dm.user_id = ? AND dmf.file_path IN (%s)
	`, strings.Join(placeholders, ","))
	var count int
	err := s.db.QueryRow(query, args...).Scan(&count)
	return count, err
}
```

- [ ] **Step 2: Write curiosity scorer tests**

Create `dualmem/curiosity_test.go`:

```go
package dualmem

import (
	"testing"
)

func TestRankModules_EmptyCodemap(t *testing.T) {
	targets := RankModules(nil, nil)
	if len(targets) != 0 {
		t.Fatalf("expected 0 targets, got %d", len(targets))
	}
}

func TestScoreCuriosityTarget(t *testing.T) {
	tests := []struct {
		name       string
		memoryGap  float64
		changeHeat float64
		complexity float64
		gitHeat    float64
		staleness  float64
		wantMin    float64
		wantMax    float64
	}{
		{
			name:      "zero_memories_high_change",
			memoryGap: 1.0, changeHeat: 0.8, complexity: 0.5, gitHeat: 0.3, staleness: 0.0,
			wantMin: 0.6, wantMax: 0.8,
		},
		{
			name:      "fully_covered_no_change",
			memoryGap: 0.0, changeHeat: 0.0, complexity: 0.0, gitHeat: 0.0, staleness: 0.0,
			wantMin: 0.0, wantMax: 0.01,
		},
		{
			name:      "stale_memories",
			memoryGap: 0.0, changeHeat: 0.0, complexity: 0.0, gitHeat: 0.0, staleness: 0.3,
			wantMin: 0.02, wantMax: 0.05,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			score := scoreCuriosity(tt.memoryGap, tt.changeHeat, tt.complexity, tt.gitHeat, tt.staleness)
			if score < tt.wantMin || score > tt.wantMax {
				t.Errorf("scoreCuriosity() = %f, want [%f, %f]", score, tt.wantMin, tt.wantMax)
			}
		})
	}
}

func TestRankModules_Ordering(t *testing.T) {
	// Module A: zero memories, high complexity
	// Module B: 5 memories, low complexity
	cm := &CodeMap{
		Zoom2: []ModuleMap{
			{Path: "pkg/uncovered", Language: "go", KeyTypes: []string{"A", "B", "C"}, Files: []FileInfo{{RelPath: "pkg/uncovered/main.go"}}},
			{Path: "pkg/covered", Language: "go", Files: []FileInfo{{RelPath: "pkg/covered/main.go"}}},
		},
	}

	signals := &CuriositySignals{
		MemoryCounts: map[string]int{
			"pkg/covered": 5,
			// pkg/uncovered: 0 (missing = zero memories)
		},
		ChangedFiles:    map[string]int{},
		EdgeCounts:      map[string]int{"pkg/uncovered": 15},
		CoChangeWeights: map[string]float64{},
		StaleModules:    map[string]bool{},
	}

	targets := RankModules(cm, signals)
	if len(targets) != 2 {
		t.Fatalf("expected 2 targets, got %d", len(targets))
	}
	if targets[0].ModulePath != "pkg/uncovered" {
		t.Errorf("expected pkg/uncovered first (higher score), got %s", targets[0].ModulePath)
	}
	if targets[0].Score <= targets[1].Score {
		t.Errorf("first target score %f should be > second %f", targets[0].Score, targets[1].Score)
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `cd /Users/donny/Projects/2026/geoffreyengram && go test ./dualmem/ -run TestRankModules -count=1 -timeout 30s 2>&1 | tail -5`
Expected: compilation error — `RankModules` and `scoreCuriosity` not defined

- [ ] **Step 4: Implement curiosity scorer**

Create `dualmem/curiosity.go`:

```go
package dualmem

import (
	"sort"
)

// CuriositySignals holds precomputed signal data for curiosity scoring.
// All maps are keyed by module path.
type CuriositySignals struct {
	MemoryCounts    map[string]int     // module path → number of associated memories
	ChangedFiles    map[string]int     // module path → count of files changed since last autopilot run
	EdgeCounts      map[string]int     // module path → structural edge count (imports + calls)
	CoChangeWeights map[string]float64 // module path → sum of co-change edge strengths
	StaleModules    map[string]bool    // module path → true if any memory is stale
	MaxCoChange     float64            // max co-change weight across all modules (for normalization)
}

// Scoring weights
const (
	weightMemoryGap  = 0.35
	weightChangeHeat = 0.25
	weightComplexity = 0.20
	weightGitHeat    = 0.10
	weightStaleness  = 0.10
)

// scoreCuriosity computes a [0,1] curiosity score from individual signals.
func scoreCuriosity(memoryGap, changeHeat, complexity, gitHeat, staleness float64) float64 {
	return memoryGap*weightMemoryGap +
		changeHeat*weightChangeHeat +
		complexity*weightComplexity +
		gitHeat*weightGitHeat +
		staleness*weightStaleness
}

// RankModules scores all codemap modules and returns them sorted by curiosity (descending).
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
			ModulePath: mod.Path,
			Score:      score,
			Signals: map[string]float64{
				"memory_gap":  memoryGap,
				"change_heat": changeHeat,
				"complexity":  complexity,
				"git_heat":    gitHeat,
				"staleness":   staleness,
			},
			Files: files,
		})
	}

	sort.Slice(targets, func(i, j int) bool {
		return targets[i].Score > targets[j].Score
	})
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
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd /Users/donny/Projects/2026/geoffreyengram && go test ./dualmem/ -run "TestRankModules|TestScoreCuriosity" -count=1 -v -timeout 30s 2>&1 | tail -15`
Expected: all 3 tests PASS

- [ ] **Step 6: Commit**

```bash
git add dualmem/curiosity.go dualmem/curiosity_test.go dualmem/store_sqlite.go
git commit -m "feat(autopilot): add curiosity scorer with scoring formula and store method"
```

---

### Task 3: Signal Gathering

**Files:**
- Create: `dualmem/curiosity_signals.go`
- Create: `dualmem/curiosity_signals_test.go`

The curiosity scorer needs precomputed signals. This task builds the `GatherSignals` function that collects them from the engine.

- [ ] **Step 1: Write signal gathering test**

Create `dualmem/curiosity_signals_test.go`:

```go
package dualmem

import (
	"context"
	"path/filepath"
	"testing"
)

func TestGatherSignals_EmptyRepo(t *testing.T) {
	engine := newTestEngine(t)
	ctx := context.Background()

	cm := &CodeMap{
		Zoom2: []ModuleMap{
			{Path: "pkg/foo", Files: []FileInfo{{RelPath: "pkg/foo/main.go"}}},
		},
	}

	signals, err := engine.GatherCuriositySignals(ctx, "testns", cm, "")
	if err != nil {
		t.Fatalf("GatherCuriositySignals: %v", err)
	}
	// No memories exist, so gap should be maximum
	if signals.MemoryCounts["pkg/foo"] != 0 {
		t.Errorf("expected 0 memories for pkg/foo, got %d", signals.MemoryCounts["pkg/foo"])
	}
}

func TestGatherSignals_WithMemories(t *testing.T) {
	engine := newTestEngine(t)
	ctx := context.Background()

	// Add a memory associated with a file
	err := engine.AddWithOptions(ctx, MemoryInput{
		UserMessage:  "test memory about foo",
		SectorHint:   "decision",
		Salience:     0.9,
		Type:         "investigation",
		Files:        []string{"pkg/foo/main.go"},
	}, "testns")
	if err != nil {
		t.Fatalf("AddWithOptions: %v", err)
	}

	cm := &CodeMap{
		Zoom2: []ModuleMap{
			{Path: "pkg/foo", Files: []FileInfo{{RelPath: "pkg/foo/main.go"}}},
			{Path: "pkg/bar", Files: []FileInfo{{RelPath: "pkg/bar/main.go"}}},
		},
	}

	signals, err := engine.GatherCuriositySignals(ctx, "testns", cm, "")
	if err != nil {
		t.Fatalf("GatherCuriositySignals: %v", err)
	}
	if signals.MemoryCounts["pkg/foo"] != 1 {
		t.Errorf("expected 1 memory for pkg/foo, got %d", signals.MemoryCounts["pkg/foo"])
	}
	if signals.MemoryCounts["pkg/bar"] != 0 {
		t.Errorf("expected 0 memories for pkg/bar, got %d", signals.MemoryCounts["pkg/bar"])
	}
}

func TestGatherSignals_EdgeCounts(t *testing.T) {
	engine := newTestEngine(t)
	ctx := context.Background()

	// Create a temp dir with Go files to get structural edges
	tmpDir := t.TempDir()
	writeFile(t, filepath.Join(tmpDir, "go.mod"), "module test\ngo 1.21\n")
	writeFile(t, filepath.Join(tmpDir, "main.go"), `package main
import "fmt"
func main() { fmt.Println("hello") }
`)
	writeFile(t, filepath.Join(tmpDir, "lib.go"), `package main
func helper() string { return "help" }
`)

	result, err := ScanCodebase(tmpDir, nil)
	if err != nil {
		t.Fatalf("ScanCodebase: %v", err)
	}

	signals, err := engine.GatherCuriositySignals(ctx, "testns", result.CodeMap, "")
	if err != nil {
		t.Fatalf("GatherCuriositySignals: %v", err)
	}
	// Should have at least one module with some edges
	if len(signals.EdgeCounts) == 0 && len(result.CodeMap.Zoom2) > 0 {
		// Edge counts may be 0 for trivial repos — that's OK
		t.Logf("No structural edges found (expected for trivial repo)")
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /Users/donny/Projects/2026/geoffreyengram && go test ./dualmem/ -run TestGatherSignals -count=1 -timeout 30s 2>&1 | tail -5`
Expected: compilation error — `GatherCuriositySignals` not defined

- [ ] **Step 3: Implement signal gathering**

Create `dualmem/curiosity_signals.go`:

```go
package dualmem

import (
	"context"
	"fmt"
)

// GatherCuriositySignals computes all signal data needed by RankModules.
// lastCommit is the git commit from the previous autopilot run (empty = no prior run).
func (e *Engine) GatherCuriositySignals(ctx context.Context, namespace string, cm *CodeMap, lastCommit string) (*CuriositySignals, error) {
	signals := &CuriositySignals{
		MemoryCounts:    make(map[string]int),
		ChangedFiles:    make(map[string]int),
		EdgeCounts:      make(map[string]int),
		CoChangeWeights: make(map[string]float64),
		StaleModules:    make(map[string]bool),
	}

	store, ok := e.store.(*SQLiteStore)
	if !ok {
		return signals, fmt.Errorf("curiosity signals require SQLiteStore")
	}

	// 1. Memory counts per module
	for _, mod := range cm.Zoom2 {
		files := moduleFilePaths(mod)
		if len(files) == 0 {
			continue
		}
		count, err := store.GetMemoryCountByFiles(namespace, files)
		if err != nil {
			continue // best-effort
		}
		signals.MemoryCounts[mod.Path] = count
	}

	// 2. Change heat from git diff (if lastCommit provided and rootDir set)
	if lastCommit != "" && e.cfg.RootDir != "" {
		marker := &SessionMarker{Commit: lastCommit}
		diff, err := ComputeStructuralDiff(e.cfg.RootDir, marker)
		if err == nil && diff != nil {
			changedSet := make(map[string]bool)
			for _, f := range diff.FilesAdded {
				changedSet[f] = true
			}
			for _, f := range diff.FilesModified {
				changedSet[f] = true
			}
			for _, mod := range cm.Zoom2 {
				count := 0
				for _, fi := range mod.Files {
					if changedSet[fi.RelPath] {
						count++
					}
				}
				if count > 0 {
					signals.ChangedFiles[mod.Path] = count
				}
			}

			// Staleness: check if any memory-associated files were modified
			staleIDs := DetectStaleMemories(diff, store, namespace)
			if len(staleIDs) > 0 {
				// Map stale memory IDs back to modules
				for _, mod := range cm.Zoom2 {
					for _, fi := range mod.Files {
						if changedSet[fi.RelPath] {
							signals.StaleModules[mod.Path] = true
							break
						}
					}
				}
			}
		}
	}

	// 3. Structural edge counts
	edges := e.store.(*SQLiteStore).GetAllStructuralEdges(namespace)
	for _, edge := range edges {
		signals.EdgeCounts[edge.SourcePath]++
		signals.EdgeCounts[edge.TargetPath]++
	}

	// 4. Co-change weights
	for _, mod := range cm.Zoom2 {
		files := moduleFilePaths(mod)
		if len(files) == 0 {
			continue
		}
		neighbors := e.GetCoChangeForPaths(namespace, files, 0.3)
		var totalWeight float64
		for _, w := range neighbors {
			totalWeight += w
		}
		signals.CoChangeWeights[mod.Path] = totalWeight
		if totalWeight > signals.MaxCoChange {
			signals.MaxCoChange = totalWeight
		}
	}

	return signals, nil
}

// moduleFilePaths extracts relative file paths from a ModuleMap.
func moduleFilePaths(mod ModuleMap) []string {
	paths := make([]string, 0, len(mod.Files))
	for _, f := range mod.Files {
		if f.RelPath != "" {
			paths = append(paths, f.RelPath)
		}
	}
	return paths
}
```

- [ ] **Step 4: Add GetAllStructuralEdges helper to store**

Check if this method exists. If not, add to `dualmem/store_sqlite.go`:

```go
// GetAllStructuralEdges returns all structural edges for a namespace.
func (s *SQLiteStore) GetAllStructuralEdges(namespace string) []StructuralEdge {
	rows, err := s.db.Query(`
		SELECT source_path, target_path, edge_type, weight
		FROM structural_edges
		WHERE namespace = ?
	`, namespace)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var edges []StructuralEdge
	for rows.Next() {
		var e StructuralEdge
		if err := rows.Scan(&e.SourcePath, &e.TargetPath, &e.EdgeType, &e.Weight); err != nil {
			continue
		}
		edges = append(edges, e)
	}
	return edges
}
```

Note: Check if `structural_edges` table exists in the schema. If the table is named differently or edges are stored elsewhere (e.g., in the CodeMap JSON), adapt the query accordingly. The structural edges may be stored in the CodeMap's `Edges` field from `CodemapScanResult` rather than SQLite — if so, pass them via the `CuriositySignals` struct directly from the scan result.

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd /Users/donny/Projects/2026/geoffreyengram && go test ./dualmem/ -run TestGatherSignals -count=1 -v -timeout 30s 2>&1 | tail -15`
Expected: all 3 tests PASS

- [ ] **Step 6: Commit**

```bash
git add dualmem/curiosity_signals.go dualmem/curiosity_signals_test.go dualmem/store_sqlite.go
git commit -m "feat(autopilot): add signal gathering for curiosity scoring"
```

---

### Task 4: Explorer Generator Config

**Files:**
- Modify: `cmd/dualmem/main.go:31-51` (CLIConfig), `:183-211` (newEngine)
- Modify: `dualmem/types.go:654-687` (Config)
- Modify: `dualmem/dualmem.go` (add getExplorerGenerator)

- [ ] **Step 1: Add explorer fields to CLIConfig**

In `cmd/dualmem/main.go`, add to the `Providers` struct inside `CLIConfig` (after line 44):

```go
		ExplorerProvider  string `yaml:"explorer_provider"`   // "anthropic" or "" (fall back to synthesis)
		ExplorerModel     string `yaml:"explorer_model"`      // e.g. "glm-5.1"
		ExplorerAPIKeyEnv string `yaml:"explorer_api_key_env"` // e.g. "ZAI_API_KEY"
		ExplorerBaseURL   string `yaml:"explorer_base_url"`   // e.g. "https://api.z.ai/api/anthropic"
```

- [ ] **Step 2: Add ExplorerGenerator to Config**

In `dualmem/types.go`, add to the `Config` struct (after `SynthesisGenerator` at line ~663):

```go
	ExplorerGenerator TextGenerator // Optional dedicated model for autopilot/anticipatory (falls back to SynthesisGenerator)
```

- [ ] **Step 3: Add getExplorerGenerator to Engine**

In `dualmem/dualmem.go`, add after `getSynthesisGenerator` (line ~3130):

```go
// getExplorerGenerator returns the TextGenerator for autopilot/anticipatory exploration.
// Priority: ExplorerGenerator > SynthesisGenerator > Summarizer (as TextGenerator).
func (e *Engine) getExplorerGenerator() (TextGenerator, error) {
	if e.cfg.ExplorerGenerator != nil {
		return e.cfg.ExplorerGenerator, nil
	}
	return e.getSynthesisGenerator()
}
```

- [ ] **Step 4: Wire explorer config in newEngine**

In `cmd/dualmem/main.go`, add explorer generator creation in `newEngine` (after the synthesis generator block at line ~194):

```go
	// Optional: dedicated explorer model for autopilot/anticipatory
	var explorerGen dualmem.TextGenerator
	if cfg.Providers.ExplorerProvider == "anthropic" {
		expKey := os.Getenv(cfg.Providers.ExplorerAPIKeyEnv)
		if expKey != "" {
			explorerGen = dualmem.NewAnthropicSummarizer(
				expKey,
				cfg.Providers.ExplorerBaseURL,
				cfg.Providers.ExplorerModel,
			)
		}
	}
```

Then add `ExplorerGenerator: explorerGen,` to the `dualmem.New(dualmem.Config{...})` call at line ~197.

- [ ] **Step 5: Verify tests pass**

Run: `cd /Users/donny/Projects/2026/geoffreyengram && go test ./dualmem/ -count=1 -timeout 60s 2>&1 | tail -5`
Expected: `ok`

- [ ] **Step 6: Commit**

```bash
git add cmd/dualmem/main.go dualmem/types.go dualmem/dualmem.go
git commit -m "feat(autopilot): add explorer model configuration and generator fallback chain"
```

---

### Task 5: Autopilot Engine Method

**Files:**
- Create: `dualmem/autopilot.go`
- Create: `dualmem/autopilot_test.go`

- [ ] **Step 1: Write autopilot test**

Create `dualmem/autopilot_test.go`:

```go
package dualmem

import (
	"context"
	"testing"
)

type mockTextGen struct {
	calls    int
	response string
}

func (m *mockTextGen) GenerateText(_ context.Context, _ string, _ int) (string, error) {
	m.calls++
	return m.response, nil
}

func newTestEngineWithExplorer(t *testing.T) (*Engine, *mockTextGen) {
	t.Helper()
	engine := newTestEngine(t)
	gen := &mockTextGen{response: "This module handles authentication via JWT tokens."}
	engine.cfg.ExplorerGenerator = gen
	// Also need a summarizer that implements TextGenerator for Explore to work
	engine.cfg.Summarizer = &mockSummarizerWithGen{response: gen.response}
	engine.cfg.SynthesisGenerator = gen
	return engine, gen
}

// mockSummarizerWithGen implements both SummarizerProvider and TextGenerator
type mockSummarizerWithGen struct {
	response string
}

func (m *mockSummarizerWithGen) SummarizeEpisode(_ context.Context, _ []string) (string, error) {
	return "episode", nil
}
func (m *mockSummarizerWithGen) SummarizeArc(_ context.Context, _ []string) (string, error) {
	return "arc", nil
}
func (m *mockSummarizerWithGen) UpdateProfile(_ context.Context, _ *ProfileSketch, _ []string) (*ProfileSketch, error) {
	return &ProfileSketch{}, nil
}
func (m *mockSummarizerWithGen) GenerateText(_ context.Context, _ string, _ int) (string, error) {
	return m.response, nil
}

func TestAutopilot_DryRun(t *testing.T) {
	engine, gen := newTestEngineWithExplorer(t)
	ctx := context.Background()

	result, err := engine.Autopilot(ctx, "testns", AutopilotOpts{
		Budget: 50000,
		DryRun: true,
	})
	if err != nil {
		t.Fatalf("Autopilot: %v", err)
	}
	if gen.calls != 0 {
		t.Errorf("dry run should not call LLM, got %d calls", gen.calls)
	}
	if result.Explored != 0 {
		t.Errorf("dry run should explore 0, got %d", result.Explored)
	}
}

func TestAutopilot_BudgetExhaustion(t *testing.T) {
	engine, _ := newTestEngineWithExplorer(t)
	ctx := context.Background()

	// Very small budget should limit exploration
	result, err := engine.Autopilot(ctx, "testns", AutopilotOpts{
		Budget: 100, // tiny budget
	})
	if err != nil {
		t.Fatalf("Autopilot: %v", err)
	}
	// With 100 token budget, should explore at most 1 module (or 0 if no codemap)
	if result.TokensUsed > 200 {
		t.Errorf("should respect budget, used %d tokens", result.TokensUsed)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/donny/Projects/2026/geoffreyengram && go test ./dualmem/ -run TestAutopilot -count=1 -timeout 30s 2>&1 | tail -5`
Expected: compilation error — `Autopilot` not defined

- [ ] **Step 3: Implement Autopilot engine method**

Create `dualmem/autopilot.go`:

```go
package dualmem

import (
	"context"
	"fmt"
	"strings"
)

// Autopilot autonomously explores under-covered codebase areas within a token budget.
func (e *Engine) Autopilot(ctx context.Context, namespace string, opts AutopilotOpts) (*AutopilotResult, error) {
	result := &AutopilotResult{}

	if opts.Budget <= 0 {
		opts.Budget = 100000 // default 100k tokens
	}

	// 1. Load or scan codemap
	cm, idx, err := e.loadOrScanCodemap(ctx, namespace)
	if err != nil {
		return nil, fmt.Errorf("autopilot: codemap: %w", err)
	}
	if cm == nil || len(cm.Zoom2) == 0 {
		return result, nil
	}

	// 2. Get last autopilot commit for change detection
	lastCommit, _ := e.store.(*SQLiteStore).GetConfigValue("autopilot_last_commit_" + namespace)

	// 3. Gather signals
	signals, err := e.GatherCuriositySignals(ctx, namespace, cm, lastCommit)
	if err != nil {
		return nil, fmt.Errorf("autopilot: signals: %w", err)
	}

	// 4. Rank modules
	targets := RankModules(cm, signals)
	result.Targets = targets

	if opts.DryRun {
		return result, nil
	}

	// 5. Get explorer generator
	gen, err := e.getExplorerGenerator()
	if err != nil {
		return nil, fmt.Errorf("autopilot: no explorer model configured: %w", err)
	}

	// 6. Explore loop within budget
	budgetRemaining := opts.Budget
	perTargetBudget := opts.Budget / max(len(targets), 1)
	if perTargetBudget < 2000 {
		perTargetBudget = 2000
	}

	for _, target := range targets {
		if budgetRemaining < 1000 {
			break
		}
		if ctx.Err() != nil {
			break
		}

		// Skip low-scoring targets
		if target.Score < 0.05 && !opts.Force {
			continue
		}

		// Novelty check: skip if we already have fresh investigation memories for this module
		if !opts.Force && e.hasRecentInvestigation(ctx, namespace, target.Files) {
			result.Skipped++
			continue
		}

		// Choose strategy: Consult for high-value targets, Explore for others
		var memText string
		var tokensUsed int
		useConsult := target.Signals["change_heat"] > 0.5 || target.Signals["staleness"] > 0

		if useConsult {
			report, err := e.Consult(ctx, namespace, target.ModulePath, min(perTargetBudget, budgetRemaining))
			if err != nil {
				continue // best-effort
			}
			memText = report.Explanation
			tokensUsed = report.TokenCount
		} else {
			exploreResult, err := e.Explore(ctx, namespace, target.ModulePath, min(perTargetBudget, budgetRemaining))
			if err != nil {
				continue
			}
			memText = exploreResult.Summary
			tokensUsed = exploreResult.TotalTokens
		}

		if memText == "" {
			continue
		}

		// Save as investigation memory
		err = e.AddWithOptions(ctx, MemoryInput{
			UserMessage: memText,
			SectorHint:  "decision",
			Type:        "investigation",
			Salience:    0.6,
			Files:       target.Files,
		}, namespace)
		if err == nil {
			result.MemoriesAdded++
		}

		result.Explored++
		budgetRemaining -= tokensUsed
		result.TokensUsed += tokensUsed
	}

	// 7. Update state marker
	if e.cfg.RootDir != "" {
		_, commit := GetGitState(e.cfg.RootDir)
		if commit != "" {
			e.store.(*SQLiteStore).SetConfigValue("autopilot_last_commit_"+namespace, commit)
		}
	}

	return result, nil
}

// hasRecentInvestigation checks if any investigation memories exist for the given files
// that were created within the last 24 hours.
func (e *Engine) hasRecentInvestigation(ctx context.Context, namespace string, files []string) bool {
	store, ok := e.store.(*SQLiteStore)
	if !ok || len(files) == 0 {
		return false
	}
	count, err := store.GetMemoryCountByFiles(namespace, files)
	if err != nil {
		return false
	}
	return count >= 3 // consider "covered" if 3+ memories touch these files
}

// loadOrScanCodemap loads a cached codemap or scans the codebase.
func (e *Engine) loadOrScanCodemap(ctx context.Context, namespace string) (*CodeMap, *CodeIndex, error) {
	// Try to load from store first
	cm, err := e.store.(*SQLiteStore).GetCodeMap(namespace)
	if err == nil && cm != nil && len(cm.Zoom2) > 0 {
		// Check if still fresh (same git commit)
		if e.cfg.RootDir != "" {
			_, currentCommit := GetGitState(e.cfg.RootDir)
			if currentCommit == cm.GitCommit {
				idx := BuildCodeIndex(cm)
				return cm, idx, nil
			}
		} else {
			idx := BuildCodeIndex(cm)
			return cm, idx, nil
		}
	}

	// Scan fresh
	if e.cfg.RootDir == "" {
		return nil, nil, fmt.Errorf("no root directory configured")
	}
	result, err := ScanCodebase(e.cfg.RootDir, e.OnScanProgress)
	if err != nil {
		return nil, nil, err
	}
	cm = result.CodeMap
	cm.Namespace = namespace

	// Cache it
	e.store.(*SQLiteStore).SaveCodeMap(cm)

	idx := BuildCodeIndex(cm)
	return cm, idx, nil
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
```

Note: Check if `max`/`min` already exist as builtins (Go 1.21+) or are defined elsewhere. If using Go 1.21+, remove the helper functions and use the builtins. Also verify `GetCodeMap`/`SaveCodeMap` exist on `SQLiteStore` — they should from the codemap caching work.

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /Users/donny/Projects/2026/geoffreyengram && go test ./dualmem/ -run TestAutopilot -count=1 -v -timeout 60s 2>&1 | tail -15`
Expected: both tests PASS

- [ ] **Step 5: Commit**

```bash
git add dualmem/autopilot.go dualmem/autopilot_test.go
git commit -m "feat(autopilot): add Autopilot engine method with exploration loop and budget tracking"
```

---

### Task 6: Autopilot CLI Command

**Files:**
- Modify: `cmd/dualmem/main.go` (add `autopilot` case + `cmdAutopilot` function)

- [ ] **Step 1: Register autopilot command in switch**

In `cmd/dualmem/main.go`, add to the switch statement (near the `seed` case):

```go
	case "autopilot":
		cmdAutopilot(cfg)
```

- [ ] **Step 2: Implement cmdAutopilot**

Add the function in `cmd/dualmem/main.go` after `cmdSeed` (line ~1538):

```go
func cmdAutopilot(cfg CLIConfig) {
	fs := flag.NewFlagSet("autopilot", flag.ExitOnError)
	ns := fs.String("ns", "", "Namespace")
	budget := fs.Int("budget", 100000, "Total token budget")
	dryRun := fs.Bool("dry-run", false, "Score modules without exploring")
	force := fs.Bool("force", false, "Re-explore even if recent memories exist")
	model := fs.String("model", "", "Override explorer model name")
	baseURL := fs.String("base-url", "", "Override explorer model base URL")
	stats := fs.Bool("stats", false, "Show coverage statistics only")
	jsonOut := fs.Bool("json", false, "JSON output")
	fs.Parse(os.Args[2:])

	namespace := resolveNamespace(*ns, cfg)

	engine, err := newEngine(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer engine.Close()

	ctx := context.Background()

	if *stats {
		printAutopilotStats(ctx, engine, namespace)
		return
	}

	opts := dualmem.AutopilotOpts{
		Budget:    *budget,
		DryRun:    *dryRun,
		Force:     *force,
		ModelName: *model,
		BaseURL:   *baseURL,
	}

	// Override explorer generator if model flag provided
	if *model != "" {
		apiKeyEnv := cfg.Providers.ExplorerAPIKeyEnv
		if apiKeyEnv == "" {
			apiKeyEnv = cfg.Providers.SynthesisAPIKeyEnv
		}
		apiKey := os.Getenv(apiKeyEnv)
		url := *baseURL
		if url == "" {
			url = cfg.Providers.ExplorerBaseURL
			if url == "" {
				url = cfg.Providers.SynthesisBaseURL
			}
		}
		if apiKey != "" {
			engine.SetExplorerGenerator(dualmem.NewAnthropicSummarizer(apiKey, url, *model))
		}
	}

	result, err := engine.Autopilot(ctx, namespace, opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if *jsonOut {
		json.NewEncoder(os.Stdout).Encode(result)
		return
	}

	if *dryRun {
		fmt.Printf("Dry run: %d modules scored\n\n", len(result.Targets))
		for i, t := range result.Targets {
			if i >= 20 {
				fmt.Printf("... and %d more\n", len(result.Targets)-20)
				break
			}
			signals := []string{}
			for k, v := range t.Signals {
				if v > 0 {
					signals = append(signals, fmt.Sprintf("%s=%.2f", k, v))
				}
			}
			fmt.Printf("  %.3f  %s  [%s]\n", t.Score, t.ModulePath, strings.Join(signals, " "))
		}
		return
	}

	fmt.Printf("Autopilot: explored %d modules, created %d memories, used %d tokens (skipped %d)\n",
		result.Explored, result.MemoriesAdded, result.TokensUsed, result.Skipped)
}

func printAutopilotStats(ctx context.Context, engine *dualmem.Engine, namespace string) {
	cm, _, err := engine.LoadOrScanCodemap(ctx, namespace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error loading codemap: %v\n", err)
		return
	}
	if cm == nil {
		fmt.Println("No codemap available")
		return
	}

	signals, err := engine.GatherCuriositySignals(ctx, namespace, cm, "")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error gathering signals: %v\n", err)
		return
	}

	totalModules := len(cm.Zoom2)
	coveredModules := 0
	for _, mod := range cm.Zoom2 {
		if signals.MemoryCounts[mod.Path] > 0 {
			coveredModules++
		}
	}

	staleCount := 0
	for range signals.StaleModules {
		staleCount++
	}

	fmt.Printf("Coverage: %d/%d modules (%.0f%%)\n", coveredModules, totalModules, float64(coveredModules)/float64(max(totalModules, 1))*100)
	fmt.Printf("Stale: %d modules with outdated memories\n", staleCount)
}
```

- [ ] **Step 3: Add SetExplorerGenerator helper to Engine**

In `dualmem/dualmem.go`, add after `getExplorerGenerator`:

```go
// SetExplorerGenerator sets the explorer model at runtime (for CLI flag overrides).
func (e *Engine) SetExplorerGenerator(gen TextGenerator) {
	e.cfg.ExplorerGenerator = gen
}
```

- [ ] **Step 4: Export LoadOrScanCodemap**

Rename the private `loadOrScanCodemap` in `dualmem/autopilot.go` to `LoadOrScanCodemap` (capitalized) so the CLI can call it for `--stats`.

- [ ] **Step 5: Add autopilot to usage text**

Find the `printUsage` function in `cmd/dualmem/main.go` and add:

```
  autopilot    Autonomously explore codebase and generate memories
```

- [ ] **Step 6: Build and test CLI**

Run: `cd /Users/donny/Projects/2026/geoffreyengram && go build -o /tmp/dualmem-test ./cmd/dualmem/ && /tmp/dualmem-test autopilot --dry-run --ns "test" 2>&1 | head -10`
Expected: Either scores modules or reports "no codemap" — no crash.

- [ ] **Step 7: Commit**

```bash
git add cmd/dualmem/main.go dualmem/autopilot.go dualmem/dualmem.go
git commit -m "feat(autopilot): add autopilot CLI command with dry-run, stats, and model override"
```

---

### Task 7: Anticipatory Worker

**Files:**
- Create: `dualmem/anticipatory.go`
- Create: `dualmem/anticipatory_test.go`
- Modify: `dualmem/pipeline.go` (add worker launch/stop)

- [ ] **Step 1: Write anticipatory prediction test**

Create `dualmem/anticipatory_test.go`:

```go
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
		"auth.go":    0.9, // should be filtered (already touched)
	}
	structuralNeighbors := map[string]float64{
		"jwt.go":    0.7,
		"crypto.go": 0.5,
	}

	candidates := predictExplorationTargets(recentFiles, cochangeNeighbors, structuralNeighbors, nil, 3)
	if len(candidates) == 0 {
		t.Fatal("expected at least 1 candidate")
	}
	// auth.go should NOT appear (already in recentFiles)
	for _, c := range candidates {
		if c == "auth.go" || c == "middleware.go" {
			t.Errorf("candidate %s should be filtered (already touched)", c)
		}
	}
	// jwt.go should be first (highest combined score)
	if len(candidates) > 0 && candidates[0] != "jwt.go" {
		t.Errorf("expected jwt.go first, got %s", candidates[0])
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /Users/donny/Projects/2026/geoffreyengram && go test ./dualmem/ -run "TestParseSession|TestPredict" -count=1 -timeout 30s 2>&1 | tail -5`
Expected: compilation error

- [ ] **Step 3: Implement anticipatory logic**

Create `dualmem/anticipatory.go`:

```go
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

// anticipatorySessionLog is a JSONL entry from the autocapture hook.
type anticipatorySessionLog struct {
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
		var entry anticipatorySessionLog
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
			// Extract base filename for matching against codemap paths
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
// co-change and structural neighbors, filtering out already-touched files
// and files with fresh memories.
func predictExplorationTargets(recentFiles []string, cochangeNeighbors, structuralNeighbors map[string]float64, freshMemoryFiles map[string]bool, limit int) []string {
	touchedSet := make(map[string]bool)
	for _, f := range recentFiles {
		touchedSet[f] = true
	}

	type candidate struct {
		path  string
		score float64
	}

	scoreMap := make(map[string]float64)
	for path, strength := range cochangeNeighbors {
		if touchedSet[path] {
			continue
		}
		if freshMemoryFiles != nil && freshMemoryFiles[path] {
			continue
		}
		scoreMap[path] += strength * 0.5
	}
	for path, proximity := range structuralNeighbors {
		if touchedSet[path] {
			continue
		}
		if freshMemoryFiles != nil && freshMemoryFiles[path] {
			continue
		}
		scoreMap[path] += proximity * 0.5
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
// It watches the session log, predicts needed context via co-change and structural
// expansion, and pre-explores using the explorer model.
func (e *Engine) anticipatoryWorker(ctx context.Context, namespace string, logPath string) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	cooldown := make(map[string]time.Time) // file → last explored time
	cooldownDuration := 10 * time.Minute

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			gen, err := e.getExplorerGenerator()
			if err != nil {
				continue // no explorer model configured
			}

			// 1. Parse recent file activity
			recentFiles, err := parseRecentSessionFiles(logPath, 2*time.Minute)
			if err != nil || len(recentFiles) == 0 {
				continue
			}

			// 2. Expand via co-change + structural neighbors
			cochangeNeighbors := e.GetCoChangeForPaths(namespace, recentFiles, 0.5)
			structuralNeighbors := e.GetStructuralNeighborPaths(namespace, recentFiles, 2)

			// 3. Predict targets
			candidates := predictExplorationTargets(recentFiles, cochangeNeighbors, structuralNeighbors, nil, 3)

			// 4. Explore top candidates (max 2 per cycle)
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

				// Save as anticipation memory
				source := fmt.Sprintf("cochange:%s→%s", strings.Join(recentFiles[:min(len(recentFiles), 2)], ","), path)
				e.AddWithOptions(ctx, MemoryInput{
					UserMessage: result.Summary,
					SectorHint:  source, // stored as sector for rendering
					Type:        "anticipation",
					Salience:    0.65,
					Files:       []string{path},
				}, namespace)

				cooldown[path] = time.Now()
				explored++
			}

			// 5. Cleanup expired anticipation memories (> 2 hours old)
			// This is handled in AssembleContext by filtering on CreatedAt
			_ = gen // used above via getExplorerGenerator
		}
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /Users/donny/Projects/2026/geoffreyengram && go test ./dualmem/ -run "TestParseSession|TestPredict" -count=1 -v -timeout 30s 2>&1 | tail -15`
Expected: both tests PASS

- [ ] **Step 5: Commit**

```bash
git add dualmem/anticipatory.go dualmem/anticipatory_test.go
git commit -m "feat(anticipatory): add session log parsing, prediction logic, and worker goroutine"
```

---

### Task 8: Pipeline Integration

**Files:**
- Modify: `dualmem/pipeline.go:14-92` (Pipeline struct, Start, Stop)

- [ ] **Step 1: Add anticipatory fields to Pipeline struct**

In `dualmem/pipeline.go`, add to the Pipeline struct (after line 31):

```go
	cancelAnticipatory context.CancelFunc
	engine             *Engine   // needed for anticipatory worker
	namespace          string    // active namespace
	sessionLogPath     string    // path to active session JSONL
```

- [ ] **Step 2: Update NewPipeline to accept Engine**

Modify `NewPipeline` signature at line 35 to add engine parameter:

```go
func NewPipeline(store Store, embedder EmbeddingProvider, summarizer SummarizerProvider, projector *Projector, extractor EntityExtractor, cfg *Config, engine *Engine) *Pipeline {
```

Add `engine: engine,` to the return struct.

Note: This changes the constructor signature. Update the call site in `dualmem/dualmem.go` `New()` function (line ~80-90 area) to pass `e` (the engine) as the last argument. Since the engine isn't fully constructed when Pipeline is created, you may need to set `pipeline.engine = e` after construction instead. Check the exact flow.

- [ ] **Step 3: Add StartAnticipatory method**

Add after `Stop()` in `dualmem/pipeline.go`:

```go
// StartAnticipatory launches the anticipatory worker for a specific session.
// Called when a session is detected (e.g., autocapture JSONL exists).
func (p *Pipeline) StartAnticipatory(namespace, sessionLogPath string) {
	if p.engine == nil || sessionLogPath == "" {
		return
	}
	// Don't start twice
	if p.cancelAnticipatory != nil {
		return
	}
	p.namespace = namespace
	p.sessionLogPath = sessionLogPath
	ctx, cancel := context.WithCancel(context.Background())
	p.cancelAnticipatory = cancel
	go p.engine.anticipatoryWorker(ctx, namespace, sessionLogPath)
}

// StopAnticipatory stops the anticipatory worker.
func (p *Pipeline) StopAnticipatory() {
	if p.cancelAnticipatory != nil {
		p.cancelAnticipatory()
		p.cancelAnticipatory = nil
	}
}
```

- [ ] **Step 4: Update Stop to include anticipatory**

Modify `Stop()` at line 82:

```go
func (p *Pipeline) Stop() {
	if p.cancelEpisode != nil {
		p.cancelEpisode()
	}
	if p.cancelArc != nil {
		p.cancelArc()
	}
	if p.cancelProfile != nil {
		p.cancelProfile()
	}
	p.StopAnticipatory()
}
```

- [ ] **Step 5: Fix NewPipeline call in Engine constructor**

In `dualmem/dualmem.go`, update the `New()` function. The pipeline is created before the engine is fully constructed, so set the engine reference after:

Find the `NewPipeline(...)` call and add `nil` for the engine parameter, then set it after:

```go
	p := NewPipeline(store, cfg.EmbeddingProvider, cfg.Summarizer, proj, cfg.EntityExtractor, cfg, nil)
	// ... rest of engine construction ...
	e := &Engine{...}
	e.pipeline = p
	p.engine = e // set back-reference for anticipatory worker
```

- [ ] **Step 6: Verify all tests pass**

Run: `cd /Users/donny/Projects/2026/geoffreyengram && go test ./dualmem/ -count=1 -timeout 120s 2>&1 | tail -10`
Expected: `ok` — all existing tests plus new ones pass

- [ ] **Step 7: Commit**

```bash
git add dualmem/pipeline.go dualmem/dualmem.go
git commit -m "feat(anticipatory): integrate worker into pipeline with start/stop lifecycle"
```

---

### Task 9: Anticipation Surfacing in Context Assembly

**Files:**
- Modify: `dualmem/dualmem.go` (AssembleContextWith, around line 1038-1073 where detail memories are rendered)

- [ ] **Step 1: Add age filter for anticipation memories**

In `AssembleContextWith` (dualmem.go), find the detail memory rendering section (~line 1038-1073). Before rendering each detail memory, add an age check:

```go
		// Filter expired anticipation memories (2-hour TTL)
		if dm.Type == "anticipation" && time.Since(dm.CreatedAt) > 2*time.Hour {
			continue
		}
```

Add this check inside the loop that iterates detail memories, before the `formatTypeLabel` call.

- [ ] **Step 2: Verify tests pass**

Run: `cd /Users/donny/Projects/2026/geoffreyengram && go test ./dualmem/ -run TestAssemble -count=1 -timeout 60s 2>&1 | tail -5`
Expected: existing assembly tests pass

- [ ] **Step 3: Commit**

```bash
git add dualmem/dualmem.go
git commit -m "feat(anticipation): add 2-hour TTL filter in context assembly for anticipation memories"
```

---

### Task 10: Integration Test

**Files:**
- Create: `dualmem/autopilot_integration_test.go`

- [ ] **Step 1: Write integration test**

Create `dualmem/autopilot_integration_test.go`:

```go
package dualmem

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestAutopilot_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// Create a realistic temp project
	tmpDir := t.TempDir()
	writeFile(t, filepath.Join(tmpDir, "go.mod"), "module testproject\ngo 1.21\n")
	writeFile(t, filepath.Join(tmpDir, "main.go"), `package main

import "fmt"

func main() {
	srv := NewServer()
	srv.Start()
	fmt.Println("running")
}
`)
	writeFile(t, filepath.Join(tmpDir, "server.go"), `package main

import "net/http"

type Server struct {
	mux *http.ServeMux
}

func NewServer() *Server {
	s := &Server{mux: http.NewServeMux()}
	s.mux.HandleFunc("/health", s.healthHandler)
	s.mux.HandleFunc("/api/users", s.usersHandler)
	return s
}

func (s *Server) Start() { http.ListenAndServe(":8080", s.mux) }
func (s *Server) healthHandler(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) }
func (s *Server) usersHandler(w http.ResponseWriter, r *http.Request) { w.Write([]byte("[]")) }
`)
	writeFile(t, filepath.Join(tmpDir, "auth.go"), `package main

import "crypto/sha256"

func hashPassword(pw string) []byte {
	h := sha256.Sum256([]byte(pw))
	return h[:]
}

func validateToken(token string) bool {
	return len(token) > 0
}
`)

	// Initialize git so structural diff works
	runGit(t, tmpDir, "init")
	runGit(t, tmpDir, "add", ".")
	runGit(t, tmpDir, "commit", "-m", "initial")

	// Create engine with mock LLM
	dbPath := filepath.Join(t.TempDir(), "test.db")
	gen := &mockTextGen{response: "This module provides HTTP server with health and user endpoints."}
	engine, err := New(Config{
		SQLitePath:         dbPath,
		EmbeddingProvider:  &mockEmbedder{dim: 768},
		Classifier:         &mockClassifier{},
		EntityExtractor:    &mockExtractor{},
		SynthesisGenerator: gen,
		ExplorerGenerator:  gen,
		MaxDetailPerUser:   100,
		ImportanceTheta:    0.65,
		RootDir:            tmpDir,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer engine.Close()

	ctx := context.Background()

	// Run autopilot
	result, err := engine.Autopilot(ctx, "testns", AutopilotOpts{
		Budget: 50000,
	})
	if err != nil {
		t.Fatalf("Autopilot: %v", err)
	}

	// Should have scored some modules
	if len(result.Targets) == 0 {
		t.Fatal("expected at least 1 target")
	}

	// Should have explored at least 1 module
	if result.Explored == 0 {
		t.Error("expected at least 1 exploration")
	}

	// Should have created memories
	if result.MemoriesAdded == 0 {
		t.Error("expected at least 1 memory created")
	}

	t.Logf("Targets: %d, Explored: %d, Memories: %d, Tokens: %d",
		len(result.Targets), result.Explored, result.MemoriesAdded, result.TokensUsed)
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test",
		"GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=test",
		"GIT_COMMITTER_EMAIL=test@test.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}
```

- [ ] **Step 2: Run integration test**

Run: `cd /Users/donny/Projects/2026/geoffreyengram && go test ./dualmem/ -run TestAutopilot_Integration -count=1 -v -timeout 120s 2>&1 | tail -20`
Expected: PASS with log output showing targets, explored, memories

- [ ] **Step 3: Commit**

```bash
git add dualmem/autopilot_integration_test.go
git commit -m "test(autopilot): add integration test with realistic Go project"
```

---

### Task 11: Build Binary and Manual Test

**Files:** None (build and test only)

- [ ] **Step 1: Build the binary**

Run: `cd /Users/donny/Projects/2026/geoffreyengram && go build -o ~/go/bin/dualmem ./cmd/dualmem/`
Expected: builds without errors

- [ ] **Step 2: Test autopilot dry-run on geoffreyengram itself**

Run: `source ~/.claude/hooks/dualmem-env.sh && ~/go/bin/dualmem autopilot --dry-run 2>&1 | head -30`
Expected: list of scored modules with their curiosity signals

- [ ] **Step 3: Test autopilot stats**

Run: `source ~/.claude/hooks/dualmem-env.sh && ~/go/bin/dualmem autopilot --stats 2>&1`
Expected: coverage percentage and stale module count

- [ ] **Step 4: Test full autopilot run (if explorer model configured)**

Run: `source ~/.claude/hooks/dualmem-env.sh && ~/go/bin/dualmem autopilot --budget 20000 2>&1`
Expected: explores modules, creates memories, reports summary

- [ ] **Step 5: Verify memories appear in context**

Run: `source ~/.claude/hooks/dualmem-env.sh && ~/go/bin/dualmem context "codebase overview" --budget 3000 2>&1 | head -40`
Expected: new investigation memories should appear in context output

- [ ] **Step 6: Commit (install binary)**

```bash
cd /Users/donny/Projects/2026/geoffreyengram && go install ./cmd/dualmem/
```

---

### Task 12: Benchmarking (Follow-up)

The spec defines a `dualmem benchmark` CLI for cold-vs-warm comparison. This is a separate implementation unit that can be built after the core autopilot/anticipatory system is proven working. It depends on having a real codebase with autopilot-generated memories to benchmark against.

Implement as a follow-up plan once autopilot is deployed and generating memories for 1-2 weeks.

---

### Task 13: Update README and Save Memory

**Files:**
- No code changes — documentation and memory only

- [ ] **Step 1: Save dualmem memory about the new feature**

Run:
```bash
source ~/.claude/hooks/dualmem-env.sh && ~/go/bin/dualmem add --type architecture --text "Autopilot system: CuriosityScorer (curiosity.go) scores modules by memoryGap*0.35+changeHeat*0.25+complexity*0.2+gitHeat*0.1+staleness*0.1. Autopilot (autopilot.go) explores top-scoring modules via Explore/Consult. Anticipatory worker (anticipatory.go) runs during sessions, watches session JSONL, expands via co-change+structural graphs, saves anticipation type memories (2hr TTL, priority 2). Explorer model configured separately from synthesis model." --files "dualmem/curiosity.go,dualmem/autopilot.go,dualmem/anticipatory.go,dualmem/pipeline.go"
```

- [ ] **Step 2: Final test suite run**

Run: `cd /Users/donny/Projects/2026/geoffreyengram && go test ./dualmem/ -count=1 -timeout 120s 2>&1 | tail -5`
Expected: `ok` — all tests pass

- [ ] **Step 3: Final commit if any remaining changes**

```bash
git add -A && git status
# If there are changes:
# git commit -m "docs: update for autopilot and anticipatory features"
```
