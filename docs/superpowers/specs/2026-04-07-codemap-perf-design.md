# Unified Codemap Scan — Performance Fix for Large Repos

**Date:** 2026-04-07  
**Status:** Draft  
**Problem:** `dualmem explore` hangs on large repos (4500+ TS files) because `ScanCodebase` and `BuildStructuralGraph` each walk the repo and tree-sitter parse all files independently — double work, no progress feedback.

## Root Cause

`getOrGenerateCodeMap` (dualmem.go:2061) calls two functions sequentially:

1. **`ScanCodebase`** (codemap.go:86) — walks repo, parallel tree-sitter parse via `runtime.NumCPU()` worker pool, builds `CodeMap` with module exports
2. **`BuildStructuralGraph`** (structural_graph.go:361) — walks repo *again*, sequential tree-sitter parse (no worker pool), extracts call/import edges

Both collect nearly identical `dirInfo` structs. Both parse the same files with tree-sitter. The structural graph path also lacks test file filtering (`.test.ts`, `.spec.ts`) that codemap has. On LearnCard (~4537 TS files), this means ~9000 tree-sitter parses with no timeout or progress output.

## Design

### 1. Unified Walk + Parse

Merge the two walks into a single `ScanCodebase` call that returns both codemap modules and structural edges.

**New return type:**

```go
type ScanResult struct {
    CodeMap *CodeMap
    Edges   []StructuralEdge
}
```

**Changes to `ScanCodebase` (codemap.go):**
- Signature: `func ScanCodebase(rootDir string, onProgress func(ScanProgress)) (*ScanResult, error)`
- The existing parallel worker pool (one goroutine per directory, bounded by `runtime.NumCPU()`) is extended: each worker produces both `[]ModuleMap` and `[]StructuralEdge` for its directory
- Test file filtering (`.test.ts`, `.spec.ts`, etc.) applies to both module parsing and edge extraction
- The walk result type includes edge data alongside module data

**Worker logic per directory (pseudocode):**

```
for each dir:
  // existing: parse exports for codemap
  mods = parseTSModuleSplit(dir.relPath, dir.tsFiles)
  // new: extract call/import edges (same files, already read)
  edges = extractTSCallEdges(dir.relPath, dir.tsFiles)
  edges += extractImportEdges(dir.relPath, dir.tsFiles, &tsLangConfig)
  return mods, edges
```

The per-directory extraction functions (`extractTSCallEdges`, `extractGoCallEdges`, `extractImportEdges`, etc.) remain in `structural_graph.go` unchanged — they just get called from inside the unified worker pool instead of from their own walk loop.

**`BuildStructuralGraph` as a standalone entry point is removed.** Its duplicate walk + `dirInfo` collection is deleted. The extraction functions stay.

### 2. Progress Reporting

**Callback type:**

```go
type ScanProgress struct {
    Phase     string // "walking", "parsing"
    DirsFound int    // total dirs discovered (set during walking, fixed during parsing)
    DirsDone  int    // dirs completed (parsing phase)
    FileCount int    // total source files found
}
```

- `ScanCodebase` accepts an optional `onProgress func(ScanProgress)` callback (nil-safe)
- During walk phase: report every 100 dirs or 500ms
- During parse phase: report as workers complete directories
- Stderr output format:
  ```
  Indexing codebase... 847 dirs found
  Parsing... 234/847 dirs (1,892 files)
  Parsing... 847/847 dirs (4,537 files) ✓
  ```

### 3. `dualmem index` Command

New CLI command for explicit pre-warming:

```
dualmem index                    # scan cwd, verbose progress
dualmem index --ns learncard     # explicit namespace
dualmem index --force            # re-index even if cache is fresh
```

Verbose output includes per-language file counts, edge count, module count, and timing:

```
Indexing /Users/donny/Work/LearnCard...
Walking... 847 dirs
Parsing... 847/847 dirs (4,537 files)
  Go: 0, TypeScript: 4,201, Python: 12, Rust: 0
  Structural edges: 12,340
  Modules: 423
Cached at commit abc1234 (took 8.2s)
```

Under the hood: calls `ScanCodebase(rootDir, verboseProgressCallback)` and persists via existing `UpsertCodeMap` + `InsertStructuralEdges`.

`--force` flag skips the git-commit cache check, useful after changing skip rules or when cache seems stale.

### 4. Integration Points

**Engine progress field:**
- Add `OnScanProgress func(ScanProgress)` field to `Engine` struct
- CLI commands set this before calling engine methods that trigger indexing
- `getOrGenerateCodeMap` passes `e.OnScanProgress` to `ScanCodebase`

**`getOrGenerateCodeMap` (dualmem.go:2061):**
- Calls unified `ScanCodebase` once instead of `ScanCodebase` + `BuildStructuralGraph`
- Gets `*ScanResult` with both `CodeMap` and `Edges`
- Caches codemap and inserts structural edges from the single result

**CLI commands (`cmd/dualmem/main.go`):**
- `cmdIndex` — new function, verbose progress to stderr
- `cmdExplore`, `cmdConsult`, `cmdMap`, `cmdContext` — wire stderr progress callback before calling engine methods that trigger indexing

**No changes to:** SQLite schema, HDC encoding, memory search, knowledge docs, reranker, or any other subsystem.

## Files Affected

| File | Change |
|------|--------|
| `dualmem/codemap.go` | `ScanCodebase` returns `*ScanResult`, accepts progress callback, worker pool extracts edges alongside modules |
| `dualmem/structural_graph.go` | Remove `BuildStructuralGraph` function and its duplicate walk/dirInfo. Keep `extractTSCallEdges`, `extractGoCallEdges`, `extractImportEdges`, etc. |
| `dualmem/dualmem.go` | `getOrGenerateCodeMap` uses unified scan, wires progress callback |
| `dualmem/types.go` | Add `ScanResult`, `ScanProgress` types |
| `cmd/dualmem/main.go` | Add `cmdIndex`, wire progress to stderr in existing commands |

## Acceptance Criteria

- `dualmem explore` on LearnCard completes in under 15s on first run (was: hung indefinitely)
- Subsequent runs on same commit hit cache and complete in <1s
- `dualmem index` shows verbose progress and caches result
- `dualmem map`, `explore`, `consult` show stderr progress during auto-indexing
- Existing tests pass (`go test ./dualmem/...`)
- No double walk or double tree-sitter parse of the same files
