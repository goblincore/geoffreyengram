# Passive Codebase Exploration — Design Spec

**Date:** 2026-04-09
**Status:** Draft

## Problem

The primary context source for AI coding agents is CLAUDE.md/AGENTS.md plus memories accumulated through active work. The `seed` command provides shallow cluster descriptions, and `codebase-onboard` requires manual initiation. There's no mechanism for continuously building architectural understanding of a codebase without actively working on it.

Agents cold-starting on unfamiliar parts of a codebase waste tokens re-discovering file relationships, call chains, and architectural decisions that could have been pre-mapped.

## Solution

Two complementary systems sharing a curiosity-scoring approach:

1. **Autopilot** (`dualmem autopilot`) — a CLI command that autonomously explores the codebase on a schedule or manually, generating architectural/investigation memories for under-covered areas.
2. **Anticipatory Worker** — a pipeline goroutine that runs during active sessions, predicting what context the agent will need next and pre-exploring it before the agent gets there.

Both produce memories that surface through existing infrastructure (file-context hook, context assembly).

## Architecture Overview

```
┌─────────────────────────────────┐
│  Autopilot CLI                  │
│  (scheduled / manual)           │
│                                 │
│  CodeMap → CuriosityScorer →    │
│  Explore/Consult loop →         │
│  Save investigation memories    │
└───────────────┬─────────────────┘
                │ writes to
                ▼
┌─────────────────────────────────┐
│  dualmem SQLite                 │
│  (detail_memories, knowledge)   │
├─────────────────────────────────┤
│  Surfacing:                     │
│  • PreToolUse file-context hook │
│  • AssembleContext ranking       │
│  • Anticipation type priority   │
└───────────────▲─────────────────┘
                │ writes to
┌───────────────┴─────────────────┐
│  Anticipatory Worker            │
│  (pipeline goroutine)           │
│                                 │
│  Session signals → predict →    │
│  Explore neighbors →            │
│  Save anticipation memories     │
└─────────────────────────────────┘
```

## Component 1: Autopilot CLI

### Command

```
dualmem autopilot [--budget 100000] [--dry-run] [--model glm-5.1] [--base-url ...] [--force] [--stats]
```

### Flow

1. **Load codemap** — use cached if git HEAD unchanged, else re-scan via `ScanCodebase`
2. **Score modules** — run `CuriosityScorer.RankModules()` on all `Zoom2` modules
3. **Explore loop** (within token budget):
   - Check novelty: skip if cosine ≥ 0.90 against existing memories for module files
   - For targets with `changeHeat > 0.5` or `staleness > 0.3`: call `Consult()` → knowledge doc
   - Otherwise: call `Explore()` → code snippets + LLM summary
   - Save as `DetailMemory{Type: "investigation", Salience: 0.6}` with `--files` tags
4. **Update state** — set `autopilot_last_commit` in config store
5. **Print summary** — modules explored, memories created, tokens spent

### Curiosity Scorer

Pure function, no LLM calls. Operates on codemap modules.

```go
type CuriosityTarget struct {
    ModulePath  string
    Score       float64
    Signals     map[string]float64  // individual signal values for debugging
    MemoryGap   float64             // 1.0 = zero memories, 0.0 = ≥3 memories
    ChangeHeat  float64             // git changes since last run, normalized
    Complexity  float64             // (fan-in + fan-out) / 20, capped at 1.0
    GitHeat     float64             // co-change weight from history
    Staleness   float64             // 0.3 if existing memories stale, else 0.0
    Files       []string            // specific files in this module
}
```

**Scoring formula:**
```
score = memoryGap*0.35 + changeHeat*0.25 + complexity*0.2 + gitHeat*0.1 + staleness*0.1
```

**Signal computation:**
- `memoryGap`: Query `GetDetailsByFiles(ns, moduleFiles)`. Count = len(results). Gap = `1.0 - min(1.0, count/3)`.
- `changeHeat`: `ComputeStructuralDiff` from `autopilot_last_commit`. Count changed files overlapping module. Normalize: `min(1.0, changedFiles/5)`.
- `complexity`: Count structural edges (imports + calls) where this module is source or target. `min(1.0, edgeCount/20)`.
- `gitHeat`: `GetCoChangeForPaths(ns, moduleFiles, 0.3)`. Sum edge strengths, normalize by max across all modules.
- `staleness`: For each existing memory touching module files, check if file was modified after memory creation (via git). If any are stale: 0.3, else 0.0.

### Configuration

New config fields in `CLIConfig`:

```go
type CLIConfig struct {
    // ... existing fields ...
    ExplorerModel   string `yaml:"explorer_model"`    // e.g., "glm-5.1"
    ExplorerBaseURL string `yaml:"explorer_base_url"` // API endpoint for explorer model
}
```

Falls back to existing `SynthesisGenerator` if not set. The anticipatory worker also uses this config — if neither `ExplorerModel` nor `SynthesisGenerator` is configured, the anticipatory worker is disabled (no-op).

### Scheduling

Intended to be run via:
- **CC scheduled tasks**: `dualmem autopilot --budget 50000` on a daily cron
- **System cron/launchd**: for non-CC environments
- **Manual**: `dualmem autopilot --budget 100000` before starting a new project phase

## Component 2: Anticipatory Session Worker

### Trigger

New goroutine in `Pipeline`, started alongside episode/arc/profile workers. Active only when a session is detected (autocapture JSONL exists).

### Signal Sources

**1. File activity** — parse autocapture JSONL (`$TMPDIR/dualmem-session-*.jsonl`):
- Track file offset to avoid re-reading
- Collect file reads/writes from last 2 minutes
- Reuse the session log discovery logic from `distill.go:523-625`

**2. Query context** — stored as engine state when `AssembleContext` is called:
- The query string and detected `Intent`
- Updated on each `context` call during a session

**3. Checkpoints** — notified via channel when `SaveCheckpoint` is called:
- `FilesActive`: files the agent is working with
- `RemainingSteps`: what the agent plans to do next

### Prediction Logic

```
1. recentFiles = parseSessionLog(last 2 minutes)
2. cochangeNeighbors = GetCoChangeForPaths(ns, recentFiles, strength ≥ 0.5)
3. structuralNeighbors = GetStructuralNeighborPaths(ns, recentFiles, 2 hops)
4. candidates = union(cochange, structural) - recentFiles - freshMemoryFiles(<1hr)
5. Score each candidate:
     recency*0.3 + coChangeStrength*0.3 + structuralProximity*0.3 - alreadyCached*0.1
6. Take top 3
7. For each: Explore(ctx, ns, candidate, 2000 tokens)
8. Save as Type: "anticipation", Salience: 0.65
```

### Resource Control

- **Polling interval**: 15-second ticker
- **LLM provider**: uses dedicated `explorer_model` TextGenerator (separate from main agent)
- **Max per cycle**: 2 LLM calls
- **Cooldown**: won't re-explore a module within 10 minutes
- **Session detection**: only runs when `$TMPDIR/dualmem-session-*.jsonl` exists for current namespace

### Pipeline Integration

```go
// In Pipeline struct
type Pipeline struct {
    // ... existing fields ...
    anticipatoryCancel context.CancelFunc
    checkpointCh       chan Checkpoint  // notified by SaveCheckpoint
    sessionLogPath     string           // resolved from namespace
    explorerGen        TextGenerator    // dedicated explorer model
}
```

Started in `Pipeline.Start()`, stopped in `Pipeline.Stop()`. Uses the same lifecycle pattern as existing workers.

## Component 3: Anticipation Memory Type

### New Memory Type: `anticipation`

Distinguishes pre-explored context from organic investigation memories.

| Property | Value |
|----------|-------|
| Type | `"anticipation"` |
| Salience | 0.65 |
| typePriority | 2 (same as warnings — surfaced first) |
| TTL | 2 hours (auto-expire) |
| Metadata | `anticipation_source`: signal description (e.g., `"cochange:auth.go→middleware.go"`) |

### Context Assembly Rendering

When `AssembleContext` includes anticipation memories, they render with a distinct header:

```
[Anticipated Context — cochange:auth.go→middleware.go]
The middleware auth flow uses JWT validation via jwt.go:ValidateToken()...
```

### Auto-Expiry

`AssembleContext` filters anticipation memories by age — only those created within the last 2 hours are included in context rendering. No background cleanup needed; expired anticipation memories are simply ignored during assembly and eventually pruned by the normal memory cap.

## Component 4: Benchmarking

### Level 1: Coverage Metrics (automated, cheap)

Tracked by `dualmem autopilot --stats`:

- **Module coverage**: `modules_with_memories / total_modules` (before/after)
- **Memory freshness**: average age of memories per module, % stale
- **Anticipation hit rate**: % of anticipation memories that were surfaced via context assembly (tracked via snapshot) vs. expired unused

### Level 2: Agent Task Benchmark (periodic, expensive)

New CLI: `dualmem benchmark [--corpus <path>] [--tasks <path>]`

**Task format** (JSON):
```json
[
  {
    "query": "Explain the auth flow from login to JWT validation",
    "expected_files": ["auth.go", "middleware.go", "jwt.go"],
    "reference_answer": "The auth flow starts at..."
  }
]
```

**Benchmark flow:**
1. For each task, run cold (no memories → fresh namespace) and warm (after autopilot)
2. Cold: `Consult(query)` with empty memory store → response
3. Warm: `Consult(query)` with autopilot-generated memories → response
4. Judge: LLM comparison (cold vs. warm vs. reference) with scoring rubric
5. Metrics: response quality (0-10), file precision/recall, token cost

**Output:** `benchmarks/results/autopilot_benchmark.json` with structured comparison data.

**Key success metric:** Warm responses should score ≥20% higher than cold on quality, with ≥50% file recall improvement. Anticipation hit rate should be >50% during real sessions.

## Existing Code Reuse

| Component | Reuses |
|-----------|--------|
| Curiosity scorer | `GetDetailsByFiles`, `ComputeStructuralDiff`, `GetCoChangeForPaths`, `GetStructuralEdgesForPath` |
| Autopilot exploration | `Explore()`, `Consult()`, `AddWithOptions()`, `ScanCodebase()` |
| Anticipatory worker | Pipeline pattern from `pipeline.go`, session log parsing from `distill.go`, `GetCoChangeForPaths`, `GetStructuralNeighborPaths` |
| Anticipation surfacing | `detailSortScore` in context assembly, `FileContext` hook |
| Benchmarking | Existing `benchmarks/` directory patterns, `Consult()` for warm/cold comparison |
| Config | `CLIConfig` in `main.go`, `GetConfigValue`/`SetConfigValue` in store |

## Key Files to Modify

| File | Changes |
|------|---------|
| `dualmem/dualmem.go` | `Autopilot()` engine method, curiosity scoring, `anticipation` type handling in `detailSortScore` |
| `dualmem/pipeline.go` | Anticipatory worker goroutine, checkpoint notification channel |
| `dualmem/types.go` | `CuriosityTarget`, `AutopilotResult`, `AutopilotOpts`, `anticipation` type constants |
| `dualmem/store_sqlite.go` | `GetMemoryCountByModule()`, anticipation TTL cleanup, autopilot state persistence |
| `cmd/dualmem/main.go` | `autopilot` subcommand, `benchmark` subcommand, explorer model config |
| `dualmem/codemap.go` | Per-module complexity scoring from structural edges |

## Evolution Path

v1 (this spec): Approach A — independent scorers, serial exploration, JSONL polling.

v2 (if value proven): Factor curiosity scoring into shared `CuriosityEngine` with `SignalSource` interface (Approach B). Add configurable weights. Potentially move to event-driven (Approach C) if polling latency is a problem.

## Verification

1. **Unit tests**: Curiosity scorer with synthetic codemap/memory fixtures → verify scoring formula, ranking order
2. **Integration test**: Autopilot on the geoffreyengram repo itself → verify memories created, no duplicates
3. **Anticipatory test**: Mock session log with file touches → verify predictions match co-change graph
4. **Benchmark**: Run Level 2 benchmark on geoffreyengram → compare cold vs. warm Consult quality
5. **End-to-end**: Schedule autopilot via CC scheduled tasks → verify memories appear in next session's context
