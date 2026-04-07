# Context & Memory Architecture — Design Spec

**Status:** Draft
**Date:** 2026-04-07

## Problem Statement

DualMem's context assembly works well for *warm* sessions — when there are checkpoints, recent memories, and knowledge docs that anchor retrieval to specific files. But it breaks down in three scenarios:

### 1. Cold-start retrieval is noisy

When an agent asks "how does guardian credential gating work" and there are no prior memories, the system falls back to HDC+BM25 codemap search. This returns modules ranked by token overlap — often surfacing unrelated files that happen to share vocabulary. There's no structural navigation path from the query to the answer.

### 2. Context is flat, not file-centric

`AssembleContextWith` renders context as a sequence of typed sections: codemap → checkpoints → knowledge docs → memories → episodes. But an LLM agent doesn't think in sections — it thinks in terms of "what do I need to know about `auth.go` to work on this bug?" The current format forces the agent to mentally correlate scattered fragments about the same file across multiple context blocks.

### 3. Annotations decay silently

Memories reference files by path, but when code changes (renames, refactors, deletions), the annotations become orphaned or misleading. The current staleness detection (structural diff at session boundaries) only catches the most obvious cases — it doesn't handle gradual semantic drift where a memory's description of a file is no longer accurate even though the file still exists.

### 4. Bootstrapping requires manual effort

The `codebase-onboard` skill works but demands a deliberate agent session. There's no incremental path from "project exists" to "system has useful annotations." New projects sit in a cold-start trap until someone explicitly runs onboarding.

## Proposed Architecture: File-Centric Annotations

### Core Idea

**Every piece of context is an annotation anchored to file paths.** Context assembly identifies relevant files first, then gathers all annotations for those files. The output is grouped by file, not by type.

This doesn't replace the existing type system (warnings, decisions, knowledge docs) — it changes the *retrieval and rendering* layer. The same memories exist; they're just organized differently when presented to the agent.

### Architecture Overview

```
┌─────────────────────────────────────────────────────┐
│                    Agent Query                       │
│           "how does credential gating work"          │
└─────────────────────┬───────────────────────────────┘
                      │
                      ▼
┌─────────────────────────────────────────────────────┐
│              File Discovery Phase                    │
│                                                     │
│  ┌─────────────┐  ┌──────────────┐  ┌────────────┐ │
│  │ Checkpoint  │  │  HDC+BM25    │  │  Structural │ │
│  │ file hints  │  │  code search │  │  graph walk │ │
│  └──────┬──────┘  └──────┬───────┘  └──────┬─────┘ │
│         │                │                  │       │
│         └────────────────┼──────────────────┘       │
│                          │                          │
│              File Ranking & Dedup                   │
│              (co-change boost,                      │
│               graph expansion)                      │
└─────────────────────┬───────────────────────────────┘
                      │ ranked file list
                      ▼
┌─────────────────────────────────────────────────────┐
│           Annotation Gathering Phase                 │
│                                                     │
│  For each file:                                     │
│    ├── Memories (warning, decision, map, trace)     │
│    ├── Knowledge docs covering this file            │
│    ├── Codemap entry (structural summary)           │
│    ├── Tree-sitter tag summary (def/ref)            │
│    └── Co-change neighbors (related files)          │
└─────────────────────┬───────────────────────────────┘
                      │
                      ▼
┌─────────────────────────────────────────────────────┐
│             Context Rendering Phase                  │
│                                                     │
│  Budget: 2000-3000 tokens                           │
│                                                     │
│  Priority order per file:                           │
│    1. Checkpoints (always, compact)                 │
│    2. Warnings (always, highest signal)              │
│    3. Knowledge doc excerpt                         │
│    4. Decisions                                     │
│    5. Codemap structural summary                    │
│    6. Related file links (co-change)                │
│                                                     │
│  Files ordered by: discovery score × annotation     │
│  density. Budget allocated greedily.                │
└─────────────────────────────────────────────────────┘
```

## 1. Annotation Lifecycle

### Creation

Annotations are created through the existing `AddWithOptions` API — no new write path needed. The `Files` field on `MemoryInput` is the anchor. The key change is making file anchoring *mandatory* for high-signal types:

| Type | Current | Proposed |
|------|---------|----------|
| `warning` | Files optional | **Files required** — warn at add time if empty |
| `decision` | Files optional | **Files recommended** — auto-infer from context when possible |
| `continuity` | Files optional | **Files optional** — task-level, may span many files |
| `map` | Files typical | **Files required** — a map *is* a file annotation |
| `seed` | Files typical | No change |
| Knowledge docs | Files via `clusterMemories` | No change |

For decisions without explicit files: the agent (or the `SectorClassifier`) can infer file associations from the decision text. For example, "Rejected Postgres for CLI — chose SQLite" mentions `store_sqlite.go` implicitly. This inference doesn't need to be perfect — a fuzzy match against the codemap file index is sufficient.

### Update & Expiry

**Tiered freshness strategy:**

| Signal | Action | Implementation |
|--------|--------|----------------|
| File renamed | Re-anchor annotation to new path | Git diff detects rename, updates `files_json` |
| File deleted | Mark annotation as orphaned, demote after 7 days | `GarbageCollect` checks path existence |
| File changed significantly | Mark as `STALE?` in context, suggest re-validation | Structural diff line-level comparison |
| Annotation not accessed in 30 days | Demote to sketch path | Existing `accessRecencyFactor` logic |
| Same-topic continuity added | Supersede older entry | Existing `supersedeContinuity` logic |

**New: Git-aware re-anchoring.** When a structural diff detects a file rename (`git diff --diff-filter=R`), the store runs a bulk update:

```sql
-- Pseudocode for rename re-anchoring
UPDATE detail_memories
SET files_json = replace(files_json, old_path, new_path)
WHERE files_json LIKE '%' || old_path || '%'
  AND user_id = ?;
```

This is a best-effort operation — it won't catch cases where a file is split into two. But it handles the most common case (directory restructuring, file renames) correctly.

**New: Semantic staleness scoring.** Beyond the existing "file changed" check, compute a lightweight staleness score for each annotation:

```
staleness = (lines_changed / total_lines) × age_days × (1 / access_count)
```

Annotations on heavily-modified files that haven't been re-validated score high. These get flagged in context with `[STALE — verify before relying]` and are prioritized for re-validation during the next `synthesize` run.

### Deletion

Annotations are deleted through the existing paths:
- Detail path eviction (capacity management)
- Garbage collection (access cold, git stale, superseded)
- Explicit demotion via `DemoteFromDetail`

No changes to deletion semantics — the lifecycle is unchanged, just the *triggering* becomes more proactive through the staleness scoring above.

## 2. Retrieval Strategy: Three-Stage File Discovery

The current cold-start problem is that HDC+BM25 returns modules by token overlap, which is noisy for conceptual queries. The solution is a **three-stage pipeline** that progressively refines relevance:

### Stage 1: Seed Collection (0 API calls)

Gather initial file candidates from deterministic sources:

1. **Checkpoint file hints** — Files from active checkpoints (warm path, always present for returning sessions)
2. **Recent memory file associations** — Files mentioned in memories accessed in the last 7 days
3. **HDC codemap search** — Current hybrid search returns top-10 modules (deterministic, instant)
4. **BM25 file-level search** — `SearchCodeMapFiles` ranks individual files within top modules
5. **Structural graph walk** — Expand seed paths via call/import edges (2 hops, beam width 5)

Each source produces a `(file_path, score)` pair. Scores are normalized per-source to [0, 1].

### Stage 2: Co-Change Expansion (0 API calls)

Expand the seed set through the co-change graph:

1. Look up co-change neighbors for each seed file
2. Apply blended scoring: `neighbor_score = seed_score × 0.7 + co_change_strength × 0.3`
3. Prune below threshold (0.2)
4. Keep top-15 expanded files

This is the key insight: **co-change edges encode real functional relationships** between files. Files that always change together are semantically related, even if their names or vocabulary don't overlap with the query.

### Stage 3: Annotation-Guided Re-ranking (0 API calls)

Re-rank files by annotation density and type priority:

```
file_score = discovery_score
           + 2.0 × (has_warning_for_file ? 1 : 0)
           + 1.5 × (has_decision_for_file ? 1 : 0)
           + 1.0 × (has_knowledge_doc_covering_file ? 1 : 0)
           + 0.5 × log(1 + annotation_count_for_file)
```

Files with existing high-signal annotations (warnings, decisions) are boosted because they represent validated knowledge. The agent has been there before and left important notes.

**Why this doesn't need embeddings or LLM calls:** Stages 1-3 use only the codemap index (HDC vectors + BM25 token frequencies, both pre-computed), the structural graph (pre-computed during `StoreCodeMap`), and the co-change graph (built incrementally from memory file associations). The entire pipeline runs in microseconds.

### Fallback: When No Files Are Found

If all three stages produce zero candidates (truly cold start on a project with no memories and no codemap):

1. Fall back to full codemap rendering (current behavior)
2. Include the `[No cross-session memories found]` hint
3. The codemap itself provides file-level context for navigation

This is the same as the current cold-start path — the improvement is that the *warm* path is much better.

## 3. Context Rendering: File-Grouped Format

### Current Format (Flat)

```
[Codebase Map]
  dualmem/ — Go package dualmem. Types: Engine, ContextOpts...
  cmd/ — Go binary (package main)

[Checkpoint: auth refactor] status=in_progress
  Files: auth.go, middleware.go

[Knowledge: hdc-encoder]
  The HDC encoder produces 2048-dimensional vectors...
  Files: dualmem/hdc.go

[⚠ Warning — decision (importance: 0.90)]
  Don't change rateLimiter.cleanup() — it skips nil for hot path
  Files: rate_limiter.go

[Decision — decision (importance: 0.80)]
  Chose SQLite over Postgres for CLI
  Files: store_sqlite.go
```

### Proposed Format (File-Grouped)

```
[Session: auth refactor (in_progress)]
  Files: auth.go, middleware.go
  Done: JWT validation, middleware
  Remaining: refresh tokens, logout

[File: dualmem/hdc.go]
  Summary: HDC encoder (2048-dim, 4-layer architecture)
  ⚠ Don't change splitCamelCase — Go has no lookahead
  ★ Content layer uses identifiers (1.0) + imports (0.8)
  Related: dualmem/codemap.go (0.92), dualmem/detail.go (0.71)

[File: store_sqlite.go]
  ★ Chose SQLite over Postgres for zero-setup CLI
  Related: store.go (0.85)

[File: auth.go]
  ★ JWT validation uses HMAC-SHA256 with rotating keys
  ⚠ Don't touch token.expiry check — intentional 5-min buffer
```

### Key Differences

1. **Files are primary, not types.** The agent sees "everything about `auth.go`" in one block rather than hunting through sections.

2. **Codemap entries are inline.** Each file's structural summary appears alongside its annotations — no separate "Codebase Map" section.

3. **Related files are links.** The `Related:` line shows co-change neighbors with strength scores, giving the agent a navigation graph without consuming much budget.

4. **Checkpoints are rendered first** (unchanged) — they define the task scope.

5. **Knowledge docs are inlined per-file.** Instead of a separate `[Knowledge: topic]` block, the relevant excerpt appears under each file that the doc covers.

### Budget Allocation

With a 2000-3000 token budget:

| Component | Current | Proposed |
|-----------|---------|----------|
| Structural diff | ≤150 | ≤100 (compressed) |
| Checkpoints | Variable | ≤200 (hard cap) |
| File annotations | Rest | Rest |
| Codemap overview | ≤400 (separate) | 0 (inlined per-file) |
| Knowledge docs | ≤60% remaining | Inlined per-file |
| Episodes/profiles | Omitted in budget-constrained | Omitted (available via progressive disclosure) |

**Budget per file:** Start with 150 tokens per file. If a file has warnings, give it 250. If a file only has a structural summary, give it 50. This naturally prioritizes files with richer annotations.

**Greedy allocation:** Sort files by `file_score` (from retrieval). Fill budget greedily. When budget runs out, stop. The agent can use progressive disclosure (`AssembleContextIndex` + `ShowItems`) for any files cut off.

### Progressive Disclosure Integration

The file-grouped format works naturally with progressive disclosure:

```
Available context (3 files, ~800 tokens):

  📋 dualmem/hdc.go (~250 tokens) — ⚠ Warning, ★ Decision, 1 map
  📋 store_sqlite.go (~150 tokens) — ★ Decision
  📋 auth.go (~400 tokens) — ⚠ Warning, ★ Decision, Knowledge doc

  Also available: codemap (65 modules), 3 episodes, structural diff

Run `dualmem show hdc.go store_sqlite.go` to fetch.
```

This replaces the current type-based index with a file-based index, which maps more naturally to how agents think about context.

## 4. Knowledge Doc Integration

### Current Synthesis Triggers

Knowledge docs are synthesized when:
- `dualmem synthesize` CLI command is run
- `dualmem consult` command triggers synthesis on cache miss (cosine < 0.75)

### Proposed: Event-Driven Synthesis

Add synthesis triggers that fire automatically:

| Trigger | Action | Condition |
|---------|--------|-----------|
| Memory added | Check if uncovered memory count ≥ threshold | `countNewUncoveredMemories() ≥ 5` |
| `GarbageCollect` runs | Re-synthesize docs with stale source memories | `isDocStale()` check |
| File changed (structural diff) | Re-synthesize docs covering changed files | During `recordSessionMarker` |
| `SeedMemories` completes | Auto-synthesize from seed clusters | New project bootstrap |

**Lazy synthesis is fine.** We don't need to synthesize on every memory addition. The threshold check (≥5 uncovered) ensures synthesis only fires when there's enough new material. Between syntheses, uncovered memories are served directly in context as individual annotations.

### Staleness Detection Enhancement

Current: A knowledge doc is stale if any source memory was created after the doc's `updated_at`.

Proposed: **Three-tier staleness:**

1. **Structural staleness** — Source files changed since last synthesis (git diff). This is the strongest signal.
2. **Source staleness** — New memories were added to the doc's file set that aren't in `source_ids`. The doc is incomplete.
3. **Temporal staleness** — Doc hasn't been re-synthesized in 30+ days. Low urgency.

Docs with structural staleness are re-synthesized immediately. Source-stale docs are re-synthesized on the next `synthesize` run. Temporally-stale docs are re-synthesized on the next `synthesize --force`.

### Knowledge Doc Rendering in File-Grouped Context

Instead of rendering the full doc text in context, extract the relevant portion for each file:

```go
// For a file-grouped context block:
func (kd *KnowledgeDoc) ExcerptForFile(filePath string) string {
    // If the doc mentions this file specifically, return the
    // paragraph that mentions it (simple heuristic: find the
    // paragraph containing the filename or its basename).
    // If no specific mention, return the first 50 tokens as a summary.
}
```

This prevents a 300-token knowledge doc from consuming 300 tokens of budget for every file it covers. Instead, each file gets a targeted excerpt.

## 5. Seeding Strategy

### Current Approach

The `codebase-onboard` skill runs an agent that:
1. Explores the codebase
2. Saves memories via `dualmem add`
3. Runs `dualmem seed` to generate cluster-based seed memories
4. Runs `dualmem synthesize` to create knowledge docs

This works but requires a full session dedicated to onboarding.

### Proposed: Incremental Bootstrap

**Phase 1: Automatic codemap + seeds (0 effort)**

When `dualmem context` or `dualmem search-code` is first called on a project:
1. Auto-generate the codemap (already happens — `getOrGenerateCodeMap`)
2. Auto-run `SeedMemories` if no seeds exist (currently requires explicit `dualmem seed` command)
3. Auto-run `Synthesize` if seed memories were just created

This requires making `SeedMemories` callable without an explicit `--force` flag when the namespace has zero seeds. The LLM call for cluster description is the only cost (~$0.01 for a medium project).

**Phase 2: Passive annotation capture (0 extra effort)**

Every time an agent uses `dualmem add --files`, the annotation is anchored. The co-change graph is built incrementally from file co-occurrence. After 5-10 sessions on a project, there's enough annotation density for effective file-grouped context.

**Phase 3: Active exploration (current onboard skill)**

The `codebase-onboard` skill remains available for projects that want deep initial coverage. But it's no longer the *only* path to useful context.

### Annotation Density Metric

Track how "annotated" a project is:

```
coverage = files_with_annotations / total_source_files
density  = total_annotations / files_with_annotations
```

A project with `coverage > 0.3` and `density > 2.0` has enough annotations for effective file-grouped context. Below that, fall back to the flat codemap-first format.

This metric is cheap to compute and can be shown in `dualmem health`.

## 6. Token Efficiency: Budget Allocation

### Priority Order (File-Grouped Model)

Given a budget of B tokens:

1. **Structural diff** — 100 tokens max. Only if changes exist since last session.
2. **Active checkpoints** — 200 tokens max. Always included.
3. **File annotations** — Remaining budget, allocated per file:

For each file (sorted by discovery score):

| Annotation Type | Max Tokens | Include Condition |
|----------------|------------|-------------------|
| Warning | 50 each | Always (highest priority) |
| Decision | 40 each | If discovery score > 0.5 |
| Knowledge doc excerpt | 60 per file | If available |
| Structural summary | 30 per file | If no knowledge doc |
| Co-change neighbors | 20 per file | Top-3 neighbors |
| Map/trace | 30 each | If budget remains |

**Budget tracking:** Tokens are tracked per-file. When a file's allocation is exhausted, remaining annotations for that file are truncated with `[+N more annotations for this file]`.

### Comparison with Current Allocation

| Scenario | Current | Proposed |
|----------|---------|----------|
| Warm session, focused task | Codemap (400) + checkpoints (200) + knowledge (600) + memories (rest) | Checkpoints (200) + 3-5 annotated files (rest) |
| Warm session, broad query | Same, more codemap modules | Checkpoints (200) + 5-8 annotated files (less per file) |
| Cold start, no memories | Full codemap (400) + hint message | Full codemap (400) + hint message (unchanged) |
| Cold start, seeded | Codemap (400) + seeds (rest) | Seeds rendered as file annotations (unchanged) |

The key improvement is in warm sessions: the codemap's 400 tokens are reclaimed by inlining structural summaries per-file (30 tokens × 5 files = 150, saving 250 tokens for actual annotations).

### Progressive Disclosure Budget

For `AssembleContextIndex`, the index listing should cost ≤100 tokens. The proposed file-based index:

```
3 files annotated (total ~800 tokens):
  dualmem/hdc.go — ⚠★ (250t)
  store_sqlite.go — ★ (150t)
  auth.go — ⚠★📖 (400t)
+ codemap (65 modules), 3 episodes, structural diff
```

This is ~50 tokens, well within budget.

## Data Model Changes

### Schema Version 12 (Proposed)

```sql
-- New: File annotation index for fast per-file lookups
CREATE TABLE IF NOT EXISTS file_annotations (
    file_path   TEXT NOT NULL,
    memory_id   TEXT NOT NULL REFERENCES detail_memories(id),
    namespace   TEXT NOT NULL,
    annotation_type TEXT NOT NULL,  -- 'warning', 'decision', 'map', 'trace', 'seed'
    created_at  TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (namespace, file_path, memory_id)
);
CREATE INDEX IF NOT EXISTS idx_annotations_file ON file_annotations(namespace, file_path);
CREATE INDEX IF NOT EXISTS idx_annotations_type ON file_annotations(namespace, file_path, annotation_type);

-- New: Annotation staleness tracking
CREATE TABLE IF NOT EXISTS annotation_freshness (
    memory_id       TEXT NOT NULL PRIMARY KEY REFERENCES detail_memories(id),
    namespace       TEXT NOT NULL,
    file_path       TEXT NOT NULL,
    last_validated  TEXT NOT NULL DEFAULT (datetime('now')),  -- last time annotation was confirmed accurate
    staleness_score REAL NOT NULL DEFAULT 0.0,                -- computed staleness (0 = fresh)
    git_commit      TEXT DEFAULT '',                           -- commit at which annotation was last validated
    FOREIGN KEY (memory_id) REFERENCES detail_memories(id)
);
CREATE INDEX IF NOT EXISTS idx_freshness_ns ON annotation_freshness(namespace, staleness_score DESC);

-- Existing tables: no changes to detail_memories, knowledge_docs, etc.
-- file_annotations is a denormalized index table derived from detail_memories.files_json
```

**Why a separate table instead of querying `detail_memories.files_json` directly?**

SQLite's `JSON_EXTRACT` is slow for large datasets and can't be efficiently indexed. The `file_annotations` table is a materialized view that's updated on `InsertDetail` and `DeleteDetail`. It makes the file-grouped rendering query fast:

```sql
SELECT dm.* FROM detail_memories dm
JOIN file_annotations fa ON dm.id = fa.memory_id
WHERE fa.namespace = ? AND fa.file_path = ?
ORDER BY CASE fa.annotation_type
    WHEN 'warning' THEN 0
    WHEN 'decision' THEN 1
    WHEN 'map' THEN 2
    ELSE 3
END;
```

### Backward Compatibility

- `file_annotations` is a derived table — it can be rebuilt from `detail_memories.files_json` at any time
- The existing `AssembleContextWith` method continues to work unchanged
- The new file-grouped renderer is a separate method (`AssembleContextFileGrouped`) that can be enabled via config flag
- Migration populates `file_annotations` from existing `detail_memories` rows

## Implementation Plan

### Phase 1: File Annotation Index (Low Risk)

1. Add `file_annotations` table (schema v12)
2. Populate from existing `detail_memories.files_json` in migration
3. Update `InsertDetail` and `DeleteDetail` to maintain the index
4. Add `GetAnnotationsForFile(namespace, filePath)` to Store interface
5. Add annotation density metric to `Health`

**Effort:** ~200 lines. **Risk:** Minimal (additive, no existing behavior changes).

### Phase 2: Three-Stage Retrieval (Medium Risk)

1. Extract file discovery from `AssembleContextWith` into `DiscoverFiles(query) []ScoredFile`
2. Implement the three-stage pipeline (seed → co-change → re-rank)
3. Wire into existing `AssembleContextWith` as the file hint source
4. Keep the existing codemap rendering as fallback when `DiscoverFiles` returns few results

**Effort:** ~300 lines. **Risk:** Medium (changes retrieval path, needs benchmarking).

### Phase 3: File-Grouped Renderer (Medium Risk)

1. Implement `AssembleContextFileGrouped` as alternative to `AssembleContextWith`
2. Implement knowledge doc per-file excerpt extraction
3. Implement budget allocation per-file with greedy fill
4. Add config flag to switch between flat and file-grouped rendering
5. Update `AssembleContextIndex` to use file-based entries

**Effort:** ~400 lines. **Risk:** Medium (new rendering logic, needs prompt engineering to verify LLM effectiveness).

### Phase 4: Lifecycle Automation (Low Risk)

1. Add git-aware re-anchoring in `GarbageCollect`
2. Add staleness scoring in `annotation_freshness`
3. Add event-driven synthesis triggers (≥5 uncovered memories threshold)
4. Make `SeedMemories` auto-run on first context call for new namespaces

**Effort:** ~250 lines. **Risk:** Low (extends existing GC/synthesis paths).

## Trade-offs and Risks

### File-Grouped vs. Flat Context

**Pro:** More natural for agents. Reduces cognitive load by co-locating related information. Saves tokens by eliminating redundant codemap section.

**Con:** Less predictable structure. An agent trained on the flat format may need adjustment. Files without annotations get less context than they would from the full codemap.

**Mitigation:** Config flag to switch formats. A/B test with context quality benchmarks. Keep flat format as fallback.

### Materialized Annotation Index

**Pro:** Fast per-file lookups. Enables file-grouped rendering without JSON_EXTRACT overhead.

**Con:** Another table to maintain consistency. Write amplification (every `InsertDetail` writes to both `detail_memories` and `file_annotations`).

**Mitigation:** `file_annotations` is purely a denormalized index — it can always be rebuilt from `files_json`. Write amplification is minimal (one extra INSERT per file in a memory, typically 1-3 files).

### Event-Driven Synthesis

**Pro:** Knowledge docs stay fresh without manual intervention. Reduces stale context.

**Con:** LLM cost per synthesis (~$0.01-0.05). Synthesis during memory add could slow the write path.

**Mitigation:** Synthesis is async (threshold check + background job). The write path only increments a counter; actual synthesis is triggered by `GarbageCollect` or a separate background goroutine.

### Automatic Seeding

**Pro:** Zero-effort bootstrap for new projects. Removes cold-start barrier.

**Con:** LLM cost on first use. May generate low-quality seeds for unusual project structures.

**Mitigation:** Seeds have low salience (0.5) — they're easily outranked by organic memories. `SeedMemories` already has a `--force` flag; auto-seeding uses `force=false` semantics (skip if seeds exist).

## Open Questions

1. **Should the file-grouped format replace the flat format entirely, or coexist as an option?** The flat format is simpler and well-tested. The file-grouped format may be better for agents but needs validation. Recommendation: ship as option, measure quality via context benchmarks, decide after 2 weeks of usage data.

2. **How should knowledge doc excerpts work for docs that cover many files?** A doc about "the auth system" might cover 8 files. Per-file excerpts could be redundant. Should we show the full doc once (under the most-relevant file) and reference it from others? Or always excerpt?

3. **What's the right threshold for the annotation density metric?** The proposed `coverage > 0.3, density > 2.0` is a guess. It needs calibration against real projects and quality benchmarks.

4. **Should co-change graph expansion be capped?** In a project where everything is tightly coupled, co-change expansion could pull in the entire codebase. The proposed beam width (5) and threshold (0.2) may need tuning.

5. **How does the file-grouped format interact with progressive disclosure?** The current `ShowItems` API fetches by memory ID. With file-grouped context, the agent might want to "fetch all annotations for file X" — a new API like `ShowFile(namespace, filePath)` would be useful.

6. **Should annotation staleness be a hard gate?** Currently, stale annotations are shown with a `[STALE?]` label. Should highly-stale annotations (staleness_score > threshold) be excluded entirely, or always shown with a warning? Excluding them risks losing important warnings. Showing them risks misleading the agent.

7. **How does the file-centric model handle memories with no file associations?** Some memories are conceptual ("The project uses event sourcing") and don't map to specific files. These would fall through the file-grouped renderer. Options: (a) attach them to all files, (b) render them in a separate "General" section, (c) require file associations for all memories. Recommendation: (b) — a small "General Context" section at the end for file-less memories.

## Appendix: Current Context Assembly Priority Order

For reference, the current `AssembleContextWith` priority (from `dualmem.go`):

```
1. Structural diff (≤150 tokens) — changes since last session
2. Code map (≤400 tokens) — PageRank-ranked module summaries
3. Checkpoints — structured session handoffs
4. Knowledge docs (≤60% remaining) — synthesized concept docs
5. Profile sketch (~50 tokens) — user preferences
6. Narrative arcs (~100-200 tokens each) — compressed themes
7. Detail memories — sorted by type priority + recency + intent
8. Recent episodes — if budget remains
```

The proposed file-grouped renderer preserves this priority *within each file block*, but changes the outer loop from "type-first" to "file-first."
