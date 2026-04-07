# Phase 4 Benchmark Fixes: Cache, File-Level Search, Docs Indexing

**Date:** 2026-04-06
**Status:** Draft
**Context:** [Phase 4 Benchmark Findings](2026-04-06-benchmark-findings.md)

## Problem

Phase 4 benchmarks revealed three issues limiting dualmem's code search quality:

1. **Cold scan per query** — `search-code` CLI calls `ScanCodebase()` raw every invocation. On LearnCard (~27 packages, hundreds of TS files) this takes ~161s/query. The Engine has git-commit caching but the CLI bypasses it.
2. **Module-level results** — search returns directory-level paths (`dualmem/`) not file-level (`dualmem/hdc.go`). P@3=0.19 because the right module contains 50 files and we can't distinguish which is relevant.
3. **No doc indexing** — queries about user-facing behavior ("how does consent flow work") can't find answers in markdown docs, only source files.

## Fix 1: Wire CLI Through Engine (Codemap Cache)

### Change

`cmdSearchCode()` in `cmd/dualmem/main.go` currently calls `ScanCodebase()` directly (line 1111). Change it to open the SQLite store, create a lightweight Engine, and call `Engine.GetCodeMap()`.

### How it works

`Engine.getOrGenerateCodeMap()` already:
- Checks the `code_maps` table for a cached codemap matching the current git commit
- On hit: deserializes and returns (fast — no filesystem walk)
- On miss: calls `ScanCodebase()`, stores result, returns

### Cache invalidation

- Keyed on git commit hash via `GetGitState()`
- Same commit = cache hit (<1s)
- New commit = full rescan (~161s on LearnCard, but only once)
- Uncommitted changes = empty commit string = no cache (acceptable — matches current behavior)

### No new tables needed

`code_maps` (migration v4) and `code_map_embeddings` (migration v5) already exist.

### CLI changes

`cmdSearchCode()` needs:
- Open store at default DB path (same logic as other commands)
- Create `Engine` with `EngineConfig{RootDir: rootDir}` + store
- Call `engine.GetCodeMap(ctx, namespace)` instead of `ScanCodebase(rootDir)`
- Namespace derived from rootDir basename (existing pattern)

### Deferred: per-file mtime tracking

A future improvement can add a `codemap_files` table storing `(namespace, file_path, mtime, parsed_json)` for incremental rescans proportional to changed files. Not in this spec — git-commit caching is sufficient for now.

### Expected impact

LearnCard warm queries: ~161s → <1s. Cold scan unchanged.

---

## Fix 2: Two-Stage File Search

### Change

New `SearchCodeMapFiles()` function. Stage 1 = existing module-level search. Stage 2 = BM25 ranking of individual files within top-K modules.

### Per-file metadata collection

During scanning, collect per-file identifiers instead of aggregating to module level.

**New type:**

```go
type FileInfo struct {
    RelPath     string   `json:"rel_path"`     // e.g. "dualmem/hdc.go"
    Identifiers []string `json:"identifiers"`  // exported + unexported symbols
    Imports     []string `json:"imports"`       // file-level imports
}
```

**New field on ModuleMap:**

```go
type ModuleMap struct {
    // ... existing fields ...
    Files []FileInfo `json:"files,omitempty"`
}
```

### Parser changes

- **Go** (`parseGoPackage`): Currently iterates `pkg.Files` but aggregates all types/identifiers to the module. Change to also collect per-file: for each `*ast.File`, record its identifiers (exported types, functions, unexported names) in a `FileInfo`. Module-level aggregation stays unchanged for backward compat.
- **TS** (`parseTSModuleSplit`): Already produces per-file entries for large files (≥5 exports or ≥300 lines). For files that stay grouped, add `FileInfo` collection during the existing parse.
- **Python** (`parsePythonModule`): Collect per-file identifiers from the regex-based parser.
- **Rust** (`parseRustModule`): Collect per-file identifiers from the regex-based parser.

### Stage 2 scoring

For each top-K module (K=5 from stage 1), score its `Files` entries:

1. Tokenize query into terms (reuse existing `tokenize()`)
2. For each `FileInfo`, compute BM25 over `Identifiers` + filename tokens
3. Combined score = `0.6 * normalizedModuleScore + 0.4 * normalizedFileScore`

### New function

```go
func SearchCodeMapFiles(cm *CodeMap, idx *CodeIndex, query string, limit int) []FileResult
```

Internally calls `SearchCodeMap()` for stage 1, then ranks files within top modules.

**Return type:**

```go
type FileResult struct {
    ModulePath    string  `json:"module_path"`
    FilePath      string  `json:"file_path"`
    FileScore     float64 `json:"file_score"`
    ModuleScore   float64 `json:"module_score"`
    CombinedScore float64 `json:"combined_score"`
    Summary       string  `json:"summary"`
}
```

### CLI integration

`search-code` defaults to file-level results via `SearchCodeMapFiles()`. Add `--module-only` flag to get the old module-level behavior.

Output format changes from:
```
1. dualmem/    score=0.70  Go package dualmem
```
to:
```
1. dualmem/hdc.go          score=0.82  (module: dualmem/ 0.70, file: 0.95)
2. dualmem/codemap.go      score=0.68  (module: dualmem/ 0.70, file: 0.65)
```

### Expected impact

P@3 on geoffreyengram: 0.19 → ~0.5+. NDCG improves as correct files rank higher.

---

## Fix 3: Docs in Search

### Change

`ScanCodebase()` gains markdown collection. Doc files become `ModuleMap` entries in the same pipeline.

### Which files

- `*.md` files found during the existing directory walk
- Includes: `docs/`, repo root (`README.md`), `.claude/` (`CLAUDE.md`, `AGENTS.md`)
- Skip: files < 100 bytes
- Same depth/dir exclusions as source files

### Parsing — new `parseMarkdownFile()`

```go
func parseMarkdownFile(relPath string, absPath string) *ModuleMap {
    // Read file content
    // Extract identifiers from:
    //   - ## headings → identifiers
    //   - **bold terms** → identifiers
    //   - `backtick code refs` → identifiers
    // Extract summary from first non-empty paragraph, truncated to 100 chars
    // Return ModuleMap{
    //     Path: relPath (e.g. "docs/consent-flow.md"),
    //     Language: "markdown",
    //     Identifiers: extracted terms,
    //     Summary: first paragraph,
    //     FileCount: 1,
    // }
}
```

Simple regex extraction — no markdown AST library. Headings and code references are the primary BM25 signal.

### Integration

In `ScanCodebase()`, after the existing language-specific switch block, add a case for `.md` files:

```go
case len(d.mdFiles) > 0:
    for _, mdFile := range d.mdFiles {
        mod := parseMarkdownFile(relPath, mdFile)
        if mod != nil {
            modules = append(modules, *mod)
        }
    }
```

Each doc is its own module (not grouped by directory). This means stage-2 file search is a no-op for docs — the module *is* the file.

### HDC encoding

Docs get HDC vectors like any other module. The content layer (identifiers + imports) provides the discriminative signal via heading terms and code references.

### Expected impact

Queries about user-facing behavior surface relevant docs alongside implementation files. The consent flow benchmark query should improve.

---

## Fix 4: Exclude Benchmarks from Scan

Add `"benchmarks"` to `skipDirs` in `codemap.go:42`. Prevents benchmark corpus from affecting BM25 IDF values, stabilizing NDCG across runs.

---

## Files Modified

| File | Changes |
|---|---|
| `cmd/dualmem/main.go` | `cmdSearchCode()` uses Engine instead of raw `ScanCodebase()`; `--module-only` flag; file-level output format |
| `dualmem/codemap.go` | `parseMarkdownFile()`; md file collection in `ScanCodebase()`; `FileInfo` type; per-file identifier collection in Go/TS/Python/Rust parsers; `SearchCodeMapFiles()` function; `"benchmarks"` in `skipDirs` |
| `dualmem/hdc.go` | `FileResult` type (search result struct) |
| `dualmem/parse_treesitter.go` | Per-file `FileInfo` collection for TS files that stay grouped in modules |

## Testing

- **Unit tests** for `parseMarkdownFile()` — headings, bold, backticks extracted correctly
- **Unit tests** for `SearchCodeMapFiles()` — file-level results rank higher than module-level for targeted queries
- **Integration test** for cache: scan → store → retrieve → verify match
- **Benchmark re-run** on geoffreyengram corpus to measure P@3/NDCG improvement
- **Benchmark re-run** on LearnCard to verify warm query latency <1s

## Success Criteria

1. LearnCard warm query latency < 1s (currently ~161s)
2. geoffreyengram P@3 ≥ 0.45 (currently 0.19)
3. Doc-related queries surface relevant `.md` files in top-5 results
4. All existing tests pass (no regressions)
