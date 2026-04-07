# Graph-Based Codemap with Memory Curation

**Date:** 2026-04-07
**Status:** Proposed
**Scope:** Replace HDC-based codemap with tree-sitter tags + PageRank graph + git co-change. Add memory staleness detection and curation.

## Problem

The current codemap has three critical issues:

1. **Unusable on large repos.** Cold scan on LearnCard (3000+ TS files) takes ~161 seconds per query because `ScanCodebase()` walks, parses, and HDC-encodes every file with no caching. Benchmark was killed.
2. **Too coarse.** Module-level results (e.g., `dualmem/` instead of `dualmem/hdc.go`) cap precision. NDCG@10 = 0.35 on home turf. P@3 = 0.19.
3. **Reinventing solved problems.** Hand-rolled tree-sitter queries per language in `parse_treesitter.go` duplicate what standardized `tags.scm` files already provide. The HDC+BM25 ranking is custom and unproven; aider's PageRank approach is battle-tested across thousands of users.

Additionally, the dualmem memory system accumulates stale memories and knowledge docs that degrade context quality. Memories reference files and symbols that no longer exist, and there is no mechanism to detect or deprioritize them.

## Solution Overview

Replace the HDC codemap pipeline with:

1. **Tree-sitter `tags.scm` extraction** — standardized, multi-language symbol extraction
2. **Definition-Reference graph + PageRank** — file-level importance ranking, personalized by task context
3. **SQLite tag cache with mtime invalidation** — parse once, reuse until file changes
4. **Git co-change graph** — behavioral file relationships from commit history, merged into PageRank personalization
5. **Memory staleness detection** — git + symbol-level checks to flag and deprioritize stale memories

## Design

### 1. Tag Extraction Layer

Replace `parse_treesitter.go`'s custom per-language queries with standard `tags.scm` files.

**Tags.scm files:** Embed tag query files from the tree-sitter-language-pack (same ones used by RepoMapper/aider) via `//go:embed`. Initial language support: Go, JavaScript (covers TypeScript), Python, Rust. Extensible by dropping new `.scm` files into the embedded directory.

**Tag structure:**
```go
type Tag struct {
    File    string // relative file path
    Name    string // symbol name (e.g., "validateJWT")
    Kind    string // "def" or "ref"
    SubKind string // "function", "class", "call", "type", "method", etc.
    Line    int    // line number in file
}
```

**Extraction function:**
```go
func ExtractTags(filePath string, lang string) ([]Tag, error)
```

- One tree-sitter parse per file, one query execution using the loaded `tags.scm`
- Kind derived from capture name: `@name.definition.*` -> "def", `@name.reference.*` -> "ref"
- SubKind from the suffix: `definition.function` -> "function", `reference.call` -> "call"
- Graceful fallback: if no `tags.scm` for a language, skip file (no crash)
- Uses existing `gotreesitter` bindings — `ts.NewQuery(queryStr, lang)` already works

**What this replaces:** The `langConfig` structs, `typeQuery`/`entryQuery`/`importQuery`/`identQuery` fields, and per-language `isExported` functions in `parse_treesitter.go`. All ~400 lines replaced by ~80 lines of generic tags.scm loading + query execution.

### 2. Definition-Reference Graph + PageRank

Build a file-level dependency graph from extracted tags. Rank files by importance using personalized PageRank.

**Graph construction:**
```
For all files in repo:
  Extract tags (cached)
  Build indexes:
    defines[symbolName]    -> set{fileA, fileB, ...}  // files that define this symbol
    references[symbolName] -> set{fileC, fileD, ...}  // files that reference this symbol

For each symbol in references:
  For each (refFile, defFile) pair where refFile != defFile:
    Add directed edge: refFile -> defFile
```

This produces a `nx.MultiDiGraph`-equivalent in Go (using a simple adjacency map — no need for a full graph library since we only need PageRank).

**PageRank with personalization:**

Standard PageRank with damping factor 0.85, personalized toward task-relevant files:

| Source | Personalization Weight | Notes |
|--------|----------------------|-------|
| Files matching query terms | 100x | Symbol name or path substring match |
| Files from active checkpoints/memories | 10x | dualmem-unique enhancement |
| Git co-change neighbors of seed files | 5x | Behavioral signal, see Section 4 |
| All other files | 1x (uniform) | Base distribution |

**PageRank implementation:** Iterative power method (standard algorithm, ~20 iterations to converge). No external graph library needed — the algorithm is ~40 lines of Go. Alternatively, use a lightweight Go graph lib if one fits.

**Output:**
```go
type RankedFile struct {
    Path        string   // relative file path
    Score       float64  // PageRank score
    Definitions []Tag    // definition tags in this file
}
```

Sorted by score descending. This replaces `CodeMap` + `[]ModuleMap` with a flat ranked file list.

**Rendering to budget:** Walk ranked files top-down. For each file, render its definition tags (symbol name + line) in a format similar to aider's repo map. Stop when token budget is reached. The format:

```
dualmem/hdc.go:
  Types: struct CodeIndex, struct BM25Index
  Entry: HDCEncodeCodeMap(), HDCEncodeQuery(), BuildCodeIndex()

dualmem/codemap.go:
  Types: struct ModuleMap, struct CodeMap
  Entry: ScanCodebase(), SearchCodeMap(), RenderAtBudget()
```

This is the same format as current `dualmem map` output but file-level instead of module-level.

### 3. SQLite Tag Cache

Cache extracted tags per-file with mtime-based invalidation.

**New table: `codemap_tags`**
```sql
CREATE TABLE codemap_tags (
    namespace  TEXT NOT NULL,
    file_path  TEXT NOT NULL,
    file_mtime INTEGER NOT NULL,  -- unix timestamp from stat()
    language   TEXT NOT NULL,
    tags_json  TEXT NOT NULL,      -- JSON array of Tag structs
    scanned_at INTEGER NOT NULL,
    PRIMARY KEY (namespace, file_path)
);
```

**Scan flow:**
1. Walk directory tree (respecting `.gitignore`, excluding `node_modules/`, `.git/`, `benchmarks/`, etc.)
2. For each source file: `stat()` to get mtime
3. Compare against cached mtime in `codemap_tags`
   - Match: load `tags_json` from cache
   - Mismatch: re-parse with tree-sitter, update cache row
   - File deleted from disk: delete cache row
4. Build graph from all tags (cached + freshly parsed)

**Performance expectations:**
- First scan (LearnCard, 3000 files): ~30-60s (tree-sitter parsing is fast; I/O is the bottleneck)
- Subsequent scans (few files changed): <2s
- Query on warm cache: <100ms (graph construction from cached tags + PageRank iteration)

**Cache invalidation:**
- Per-file: mtime comparison (primary)
- Full clear: `dualmem map --refresh` (manual)
- Branch switch: detected via git HEAD comparison, triggers full re-validation (but most files will have same mtime so it's fast)
- No TTL — code doesn't expire, it changes

### 4. Git Co-Change Integration

Mine git commit history for behavioral file relationships.

**Data extraction:**
```bash
git log --name-only --format="%H %s" --since="6 months ago"
```

Single git command. Parse output into:
```go
type GitCommit struct {
    Hash    string
    Subject string   // commit message first line
    Files   []string // files touched in this commit
}
```

**Graph construction:**
- For each commit, create edges between all pairs of touched files (N choose 2)
- Edge weight = number of commits where both files appear together
- Recency weighting: recent commits count more (exponential decay, half-life 3 months)
- Commit subject stored as edge annotation ("reason")

**Adaptive depth:**
- Start with 6 months of history
- If resulting graph has < 50 edges, expand to full history (`--since` removed)
- Configurable via `--depth` flag (0 = full history)

**Storage:** Extend existing `cochange` tables with a `source` column:
```sql
ALTER TABLE cochange ADD COLUMN source TEXT DEFAULT 'memory';
-- New rows from git get source = 'git'
```

This merges git-derived and memory-derived co-change into one graph. Both feed into PageRank personalization.

**Refresh:** Git co-change is rebuilt on `dualmem map` or when the stored HEAD doesn't match current HEAD. Incremental: only process commits since last stored HEAD.

**How it feeds into PageRank:** When a query has seed files (from term matching or memory), their git co-change neighbors get 5x personalization weight. This means files that behaviorally relate (always change together) get boosted even without code-level references.

### 5. Memory Staleness & Curation

Detect and deprioritize memories that reference outdated code.

**Three staleness signals:**

1. **File-level:** For memories referencing files, run `git diff --name-only <memory_commit>..<HEAD>`. If >50% of referenced files had significant changes (not just whitespace), flag as stale.

2. **Symbol-level:** Using the tag cache, check if symbols mentioned in memory text still exist in referenced files. If a memory says "Auth logic is in validateJWT() in auth.go" but the tag cache shows no `validateJWT` definition in `auth.go`, flag as stale.

3. **Age-based decay:** Memories older than 30 days (configurable) without reinforcement (no newer memories referencing same files/concepts) get increasing staleness scores.

**Schema addition:**
```sql
ALTER TABLE details ADD COLUMN stale_since INTEGER;  -- unix timestamp, NULL = not stale
ALTER TABLE details ADD COLUMN stale_reason TEXT;     -- human-readable reason
```

**Actions:**
- `dualmem gc --stale` — identify and report stale memories with reasons
- `dualmem gc --stale --purge` — delete stale memories (explicit user action)
- No auto-deletion — always surface first

**Integration with context assembly:**
- `AssembleContext` applies a staleness penalty: stale memories get effective salience * 0.5
- Fresh memories about the same files naturally supersede stale ones via the existing importance scoring
- Knowledge docs sourced primarily from stale memories get flagged for re-synthesis

### 6. CLI & Integration Surface

**Modified commands:**

- **`dualmem map`** — rebuilt: scan -> extract tags (cached) -> build graph -> PageRank -> render to budget. Same output format. New flags: `--refresh`, `--depth <months>`, `--git-graph` (diagnostic: dump co-change graph).

- **`dualmem search-code "<query>"`** — uses tag cache + graph with query-personalized PageRank. Returns ranked files (not modules). Query terms matched against tag names + file paths.

- **`dualmem context "<task>"`** — context assembly uses graph-ranked codemap instead of HDC-ranked. Personalization from task intent + checkpoint files + co-change neighbors. Staleness penalties applied.

- **`dualmem gc --stale`** — new mode: report stale memories. `--stale --purge` to clean up.

**What gets removed (from codemap pipeline only):**
- `HDCEncodeCodeMap()` / `HDCEncodeQuery()` / `BuildCodeIndex()` / `SearchCodeMap()` — replaced by graph+PageRank
- `CodeIndex` struct, `BM25Score()`, `AdaptiveAlpha()` — HDC+BM25 search index
- Custom per-language query structs in `parse_treesitter.go` (`langConfig`, `typeQuery`, `entryQuery`, `importQuery`, `identQuery`, per-language `isExported` functions)
- Module-level `ModuleMap` grouping logic, `parseTSModuleSplit()`, `classifyFileSignificance()`
- `RenderAtBudget()` — replaced by new graph-aware renderer

**What stays (untouched):**
- `HDCEncode()` / basis vector generation in `hdc.go` — still used for memory embedding, not codemap
- Memory add/search/checkpoint
- Distill pipeline
- Entity graph
- `structural_graph.go` call-edge extraction (could be merged into the reference graph later, but not in scope)

## Success Criteria

Measured against the same benchmark suite (42 queries across geoffreyengram, unravelai, LearnCard):

| Metric | Current | Target |
|--------|---------|--------|
| LearnCard cold scan | ~161s | <60s |
| LearnCard warm query | N/A (killed) | <2s |
| geoffreyengram NDCG@10 | 0.35 | >0.55 |
| geoffreyengram P@3 | 0.19 | >0.40 |
| Memory staleness detection | none | flags >80% of actually-stale memories |

## Testing Strategy

- **Tag extraction:** Unit tests comparing `ExtractTags()` output against known Go/TS/Python files with expected definitions and references
- **PageRank:** Unit test with a small hand-crafted graph verifying ranking order
- **Cache:** Integration test: parse, verify cache hit, modify file, verify re-parse
- **Git co-change:** Integration test in a temp git repo with known commit history
- **Staleness:** Unit test with memories referencing files that are then modified
- **Benchmark:** Re-run the 42-query benchmark suite comparing before/after metrics

## Non-Goals

- Full LSP-style "go to definition" navigation (we just need ranking, not precise resolution)
- Cross-repo analysis (single repo at a time)
- Real-time incremental parsing (we re-scan on command, not on file save)
- Replacing HDC for memory embedding (that's a separate system)
