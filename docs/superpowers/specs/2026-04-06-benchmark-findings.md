# Phase 4 Benchmark: Initial Findings & Improvement Opportunities

## Run Summary

Ran 42 queries across 3 corpora (geoffreyengram, unravelai, LearnCard) with dualmem only (UnravelAI adapter functional but returns low scores on Go/TS codebases it wasn't designed for).

### DualMem on geoffreyengram (home turf)
| Metric | Value |
|---|---|
| Precision@3 | 0.19 |
| Recall@5 | 0.75 |
| NDCG@10 | 0.35 |
| Latency | ~42ms |

### DualMem on LearnCard (large TS monorepo)
| Metric | Value |
|---|---|
| Latency per query | **~2.5 minutes** |
| (Metrics incomplete — run killed due to excessive scan time) |

---

## Finding 1: Codebase Scan is O(n) Per Query — Catastrophic on Large Repos

**Observation:** Each `search-code` invocation calls `ScanCodebase()` which:
1. Walks the entire directory tree
2. Parses every Go/TS/Python/Rust file via tree-sitter or go/ast
3. Builds the HDC codemap from scratch
4. Only then runs the query

On geoffreyengram (~11 modules, ~50 files): 42ms. Fine.
On LearnCard (27 packages, hundreds of TS files): **~161 seconds**. Unusable.

**Root cause:** There is no caching layer. `ScanCodebase()` is stateless — it doesn't persist the codemap between invocations.

### Proposed Fix: Cached Codemap with Staleness Detection

**Option A: File-based cache (simplest)**
- After `ScanCodebase()`, serialize the `CodeMap` + HDC embeddings to a `.dualmem/codemap.cache` file
- On next invocation, check if cache exists and compare mtime of source files vs cache timestamp
- If cache is fresh (no files modified since cache was written), load from cache
- If stale, re-scan only modified files and update cache incrementally

**Option B: SQLite-backed codemap (aligns with existing architecture)**
- Store codemap modules in a new `codemap_cache` SQLite table (namespace, path, language, summary, types, entries, imports, identifiers, hdc_vector, file_mtime, scan_time)
- On scan, check each file's mtime against stored mtime — only re-parse changed files
- HDC vectors for unchanged modules are reused from cache
- Fits naturally into the existing SQLite store

**Option C: In-process cache (fastest for repeated CLI calls)**
- Add a `--cache-dir` flag to `search-code` CLI
- Serialize CodeMap + CodeIndex to a gob/json file in cache dir
- Re-scan only when any source file mtime > cache mtime

**Recommendation: Option B** — it's the most robust, integrates with existing infrastructure, and supports incremental updates. The cache becomes part of the namespace and benefits from existing migration/cleanup patterns.

**Expected impact:** Reduce LearnCard query latency from ~161s to <1s for warm queries (cache hit). Cold scan still takes ~161s but only happens once.

---

## Finding 2: Module-Level Results Limit Precision

**Observation:** `search-code` returns module-level paths (`dualmem/`, `cmd/dualmem/`) not file-level paths (`dualmem/hdc.go`). This means:

- P@3 maxes out at 0.33 for single-module answers (1 module covers all ground truth files, but counts as 1 hit in top 3)
- When multiple ground truth files are in the same module, precision can't distinguish "found the right module" from "found the right file"
- NDCG is moderate (0.35) because the relevant module often appears at position 2-5, not position 1

**Root cause:** The codemap groups files into modules by directory. A query about HDC encoding returns `dualmem/` which contains 50+ files — only `hdc.go` is relevant.

### Proposed Fix: File-Level Search Results

**Option A: Sub-module splitting (already partially implemented)**
- `parse_treesitter.go` already splits large files (≥5 exports or ≥300 lines) into separate codemap entries
- Extend this: split ANY module with >N files into per-file entries
- Pro: reuses existing infrastructure, HDC vectors computed per-file
- Con: increases index size, may dilute module-level signal

**Option B: Two-stage search (module → file)**
- Stage 1: current module-level search (fast, good recall)
- Stage 2: within top-K modules, rank individual files by keyword/identifier match against the query
- Pro: best of both worlds — module-level recall + file-level precision
- Con: slightly more complex, needs file-level metadata

**Option C: Hybrid module+file index**
- Build two codemap layers: one at module level (current), one at file level
- Search both, merge results with module results as tiebreakers
- Pro: comprehensive
- Con: doubles index size and scan time

**Recommendation: Option B** — two-stage search is the highest value for lowest complexity. We already have per-file identifiers and imports from the tree-sitter parser. Stage 2 just needs to score individual files within a module by keyword overlap with the query.

**Expected impact:** Precision@3 could improve from 0.19 → 0.5+ on geoffreyengram. NDCG would improve as correct files rank higher.

---

## Finding 3: UnravelAI Returns Zero on Non-JS/TS Codebases

**Observation:** UnravelAI's `queryGraphForFiles()` returns empty or zero-score results on the geoffreyengram Go codebase. This is expected — Unravel's AST engine only handles JS/TS files deeply. Go files get `addFile()` (no structural analysis), so the graph has nodes but no meaningful edges.

**Implication:** The head-to-head comparison is only meaningful on the TS corpora (unravelai, LearnCard). On geoffreyengram, dualmem wins by default. This is fair — each system has different language coverage — but should be noted in the final report.

**No fix needed** — this is a genuine capability difference, not a bug.

---

## Finding 4: Benchmark Variance from Non-Deterministic Ranking

**Observation:** Some queries showed different NDCG scores between runs (e.g., query 1 scored NDCG=1.00 in one run and NDCG=0.50 in another). This is because:
- HDC encoding uses deterministic basis vectors, but the BM25 component depends on the full corpus term statistics
- If the benchmark itself is part of the codebase (it is), the BM25 IDF values shift as we add/modify benchmark files

**Implication:** Results should be averaged over multiple runs, or the benchmark should exclude itself from the scan.

**Proposed fix:** Add `benchmarks/` to the default scan exclusion list in `ScanCodebase()`, similar to how `node_modules/` and `.git/` are excluded.

---

## Priority Order

1. **Cached codemap** (Finding 1) — unlocks benchmarking on large repos, biggest user impact
2. **Two-stage file-level search** (Finding 2) — improves precision metrics and real-world usefulness
3. **Exclude benchmarks from scan** (Finding 4) — small fix, stabilizes benchmark results
4. **Note language coverage** in report (Finding 3) — documentation only
