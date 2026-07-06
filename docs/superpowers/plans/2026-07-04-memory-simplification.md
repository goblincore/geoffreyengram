# Memory System Simplification & Cold-Start Fix

**Date:** 2026-07-04
**Status:** Draft
**Motivation:** The memory system works but is bloated — ~34k lines in `dualmem/`, ~15 ranking-adjacent subsystems, and no way to tell which layers earn their tokens. Separately, on large monorepos (LearnCard), session start blocks on a full codebase rescan and `get_context` returns a useless "indexing" state.

---

## Problem 1: Cold-start indexing blocks context (immediate pain)

### Root cause

`dualmem/dualmem.go` — `getOrGenerateCodeMap()`:

1. **Commit-keyed cache invalidation.** The stored codemap is only used if `stored.GitCommit == currentCommit`. Any new commit — even one touching a single file — invalidates the entire codemap.
2. **Full synchronous rescan.** On invalidation, `ScanCodebase(rootDir, ...)` walks and parses the whole tree *inline*, inside context assembly. On LearnCard this takes minutes.
3. **Blocking placement.** Codemap generation sits on the critical path of `get_context` / session-start, so the first session after new commits (e.g. reopening the laptop after pulling) gets either a long stall or an "Indexing… N/M dirs" placeholder instead of context.

### Fix (Phase 1)

- **Stale-while-revalidate.** If a stored codemap exists for the namespace, serve it immediately regardless of commit mismatch. Annotate output with `(codemap from <short-sha>, N commits behind)` and kick off a refresh in the background (or on the next explicit `dualmem map`/`scan` invocation — decide during implementation; background goroutines in a short-lived CLI process may need a detached worker or a `--refresh` flag on the hook).
- **Incremental rescan.** When `stored.GitCommit != HEAD`, compute `git diff --name-status <stored>..HEAD`, re-parse only changed files, and patch Zoom1/Zoom2 + structural edges in place. Full walk only when no stored map exists or the diff is huge (> ~30% of files).
- **Never block.** Hard invariant: `AssembleContext` must not trigger a filesystem walk. If no codemap exists at all (true first run), emit a one-line notice ("no codemap yet — run `dualmem map`") and proceed with memories/checkpoints, which don't depend on the scan.
- **Tests:** regression test that `AssembleContext` completes in bounded time with a stale commit + large synthetic tree; incremental-patch correctness vs full rescan.

### Acceptance

- Reopening a session on LearnCard after new commits returns useful context in < 1s.
- No "indexing" state ever appears in `get_context` output.

---

## Problem 2: Layer bloat with no attribution

### Inventory (keep / suspect)

**Earning their keep (per benchmarks + design):**
- Detail path + no-LLM importance scorer
- File-centric annotations (drives the benchmark wins over claude-mem)
- Checkpoints
- Query-aware budget packing + intent detection
- HDC code search (cheap, no API calls)

**Suspect (value unproven):**
- Sketch path (episodes → arcs → profile, 128d/64d projections) — roadmap queries still lose to claude-mem 3.0 vs 7.2, which is exactly the use case sketch was built for
- Two co-change graphs: `cochange.go` (memory-based) *and* `git_cochange.go` — likely mergeable
- Entity graph + structural boost
- Learned reranker (`reranker.go`) — trained on `rate_context` ratings that rarely get submitted
- Workflow hints, curiosity signals
- Autopilot + anticipatory worker — `anticipation-stats` exists but hit rate hasn't gated anything

### Phase 2 — Attribution before deletion

Don't delete on vibes; make the system report on itself.

- **Source tagging.** Every item in an assembled context block already flows through snapshot machinery (`rating.go`). Extend snapshots to record `source_layer` (detail / sketch-arc / sketch-profile / file-annotation / knowledge-doc / codemap / cochange-boost / entity-boost / reranker-delta) and token cost per item.
- **Passive usefulness signal.** Correlate context items with what the session actually touched: files read/edited (from distill transcripts or hook logs) vs files referenced by each item. An item citing a file the session touched = a hit. No user effort required, unlike `rate_context`.
- **`dualmem layer-stats`.** Scorecard command: per layer — tokens spent, items served, hit rate, and delta when the reranker reorders. Include anticipation hit rate here too.
- **Benchmark integration.** Run `dualmem benchmark` with per-layer ablation flags (`--disable=entity,sketch,...`) so cold/warm comparisons can isolate a layer's contribution.

### Phase 3 — Prune with evidence

After 1–2 weeks of scorecard data on real projects (geoffreyengram + LearnCard):

- Delete or feature-flag layers with negligible hit rate / ablation delta. Prime candidates: entity graph, reranker, workflow hints, sketch arcs+profile (keep episodes if distill depends on them).
- Merge the two co-change graphs into one (git-derived as base, memory associations as edge-weight boost).
- Collapse to a documented core: **store + detail memories + file annotations + checkpoints + codemap + context assembler**. Everything else lives behind an explicit optional boundary (build tag, config flag, or separate package) with its scorecard justification noted in `docs/architecture.md`.
- Target: meaningful reduction in `dualmem/` LOC and a `docs/architecture.md` where every box has a "why it exists" line backed by data.

---

## Sequencing

| Phase | What | Why first |
|---|---|---|
| 1 | Stale-while-revalidate + incremental codemap scan | Daily pain; independent of everything else |
| 2 | Layer attribution + `layer-stats` + ablation flags | Cheap plumbing; must precede pruning |
| 3 | Evidence-based pruning + core consolidation | Needs Phase 2 data |

## Non-goals

- No new retrieval layers. This plan only removes, merges, or instruments.
- No storage backend changes (SQLite stays).
- No changes to the MCP tool surface in Phases 1–2 (Phase 3 may remove tools whose backing layer is deleted).

## Open questions

- Background refresh mechanics: detached worker process vs refresh-on-next-CLI-invocation vs hook-triggered `--refresh`? (Short-lived CLI processes can't own long goroutines.)
- Should sketch episodes survive even if arcs/profile are cut, given distill writes into them?
- Is the passive file-touch signal available in Claude Code hooks today, or does it need the distill transcript path?
