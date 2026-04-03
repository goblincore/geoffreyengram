# Context Quality Benchmarks & Adaptive Rating System

## Context

DualMem assembles cross-session context for coding agents — warnings, decisions, continuity notes, codemap, knowledge docs. The system has good IR-level metrics (precision/recall/NDCG in `bench_test.go`, search accuracy in `search_bench_test.go`) but lacks measurement of **downstream usefulness**: does the context actually help the agent do its job better?

The goal is a three-tier benchmark strategy that measures usefulness at every level, plus a live feedback loop that teaches the system what "right stuff" means over time.

## Design

### Tier 1: Live Self-Rating with Adaptive Re-Ranker

The most valuable tier — captures real-world signal from actual usage.

#### 1a. Item-Level Rating (agent-driven, two windows)

The agent that *consumed* the context (e.g., Claude in Claude Code) is the best judge of whether it was useful. Rather than using a separate LLM, the active agent rates the context at two natural moments.

**Rating windows:**

1. **Early (at context load)** — Right after `dualmem context` returns, the agent reads the assembled context and immediately knows which items look relevant to the current task. This is a natural moment to rate — the agent is already triaging the context. Captures prospective signal.

2. **Late (during distill)** — At session end, the agent rates retrospectively based on what actually mattered. An item that looked irrelevant might have turned out to be key (or vice versa). Captures ground-truth signal.

Both ratings are stored with a `phase` field (`early` or `late`). When both exist for the same item+snapshot, the re-ranker can learn from the delta (prospective vs actual usefulness). Late ratings are weighted higher for training since they reflect ground truth.

**Flow:**
1. `dualmem context` assembles and returns context as normal. The assembled context block (including source IDs and features) is persisted to a `context_snapshots` table.
2. The agent reads the context and rates items it finds relevant (early rating):
   ```bash
   dualmem rate --snapshot <id> --phase early --ratings '{"mem_abc": 2, "mem_def": 0}'
   ```
3. The agent does its work using the context throughout the session.
4. At session end, during `dualmem distill`, the agent reviews the context snapshot and rates based on what actually helped (late rating):
   ```bash
   dualmem rate --snapshot <id> --phase late --ratings '{"mem_abc": 2, "mem_def": 1, "mem_ghi": 0}'
   ```

Rating scale (0-2):
- 0 = noise (not relevant to this task)
- 1 = partially relevant (tangentially useful)
- 2 = directly relevant (essential context)

**Storage** — new tables (schema migration v9):

```sql
CREATE TABLE context_snapshots (
    id              TEXT PRIMARY KEY,
    namespace       TEXT NOT NULL,
    query           TEXT NOT NULL,
    query_embedding BLOB,
    source_ids_json TEXT NOT NULL,  -- JSON array of {id, type, cosine_sim, importance, ...}
    tokens_used     INTEGER,
    created_at      TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_snapshots_ns ON context_snapshots(namespace, created_at);

CREATE TABLE context_ratings (
    id              TEXT PRIMARY KEY,
    namespace       TEXT NOT NULL,
    snapshot_id     TEXT NOT NULL REFERENCES context_snapshots(id),
    memory_id       TEXT NOT NULL,  -- FK to detail_memories or knowledge_docs
    memory_type     TEXT NOT NULL,  -- 'detail', 'knowledge', 'checkpoint', 'codemap'
    phase           TEXT NOT NULL DEFAULT 'late',  -- 'early' or 'late'
    rating          INTEGER NOT NULL, -- 0, 1, 2
    -- Features for re-ranker training (denormalized from snapshot):
    cosine_sim      REAL,
    importance      REAL,
    salience        REAL,
    sector          TEXT,
    mem_type        TEXT,           -- warning/decision/continuity/general
    file_overlap    INTEGER,        -- # files in common between query context and memory
    age_days        REAL,
    text_length     INTEGER,
    created_at      TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_ratings_ns ON context_ratings(namespace);
CREATE INDEX idx_ratings_memory ON context_ratings(memory_id);
CREATE INDEX idx_ratings_snapshot ON context_ratings(snapshot_id);
CREATE INDEX idx_ratings_phase ON context_ratings(phase);
```

Each rating row stores the feature vector alongside the label — this is the re-ranker training data. Features are denormalized from the snapshot at rating time so they reflect the state when context was assembled (not current state).

**Cost**: Zero extra LLM calls — the active agent (Claude) is already running and does the rating as part of its distill workflow. The only cost is the `context_snapshots` write at assembly time (a few KB of JSON per session) and the `dualmem rate` CLI calls during distill.

#### 1b. Session-Level Rating (during distill)

During `dualmem distill` (already runs at session end), add a session usefulness score. The distill pipeline already summarizes what happened — extend it to include:

```
Session usefulness rating: 4/5
Reason: Context correctly surfaced the auth warning and ongoing refactor status.
        Codemap was too broad — included unrelated frontend modules.
```

**Storage** — new `session_ratings` table:

```sql
CREATE TABLE session_ratings (
    id          TEXT PRIMARY KEY,
    namespace   TEXT NOT NULL,
    session_id  TEXT NOT NULL,
    score       INTEGER NOT NULL,  -- 1-5
    explanation TEXT,
    query_used  TEXT,              -- the original context query
    tokens_used INTEGER,
    created_at  TEXT NOT NULL DEFAULT (datetime('now'))
);
```

#### 1c. Learned Re-Ranker

A simple linear model that learns from accumulated ratings to improve retrieval ordering.

**Features** (per candidate memory):
- `cosine_sim` — embedding similarity to query
- `importance` — importance score from `ImportanceScorer`
- `salience` — user-provided salience
- `type_is_warning` — 1 if warning, 0 otherwise
- `type_is_decision` — 1 if decision, 0 otherwise
- `type_is_continuity` — 1 if continuity, 0 otherwise
- `file_overlap` — number of files in common with query context (from checkpoints, etc.)
- `age_days` — days since memory was created
- `text_length` — token estimate of memory text
- `sector_match` — 1 if memory sector matches detected intent's preferred sector

**Model**: Logistic regression. Output = P(relevant | features). Coefficients stored in `dualmem_config` table as JSON.

**Training**: `dualmem train` CLI command. Runs when you have 50+ rated items. Reads from `context_ratings`, fits logistic regression coefficients via gradient descent (pure Go, no dependencies). Late-phase ratings are weighted 2x vs early-phase ratings in the loss function (ground truth > prospective guess). When both early and late exist for the same item+snapshot, the late rating takes precedence. Stores learned weights.

**Application**: During `AssembleContext`, after initial cosine-similarity retrieval pulls candidate memories, the re-ranker re-scores each candidate:

```
rerank_score = sigmoid(w · features)
final_score = 0.6 * cosine_sim + 0.4 * rerank_score
```

When the re-ranker has insufficient data (<50 ratings), it's a no-op — the system falls back to current behavior.

**Safety rails**:
- Re-ranker only adjusts ordering, never filters. A memory can be ranked lower but not excluded.
- `--no-rerank` flag on `context` command disables the re-ranker for comparison.
- Coefficients are bounded: no single feature weight can exceed 5.0 in absolute value.
- If re-ranker hasn't been retrained in 60+ days, it decays toward equal weights.

#### 1d. Stats Dashboard

`dualmem stats` CLI command:

```
Context Quality Stats (claude:geoffreyengram)
─────────────────────────────────────────────
  Total ratings:     247 items across 34 sessions
  Avg precision:     0.73 (items rated 1-2 / total items)
  Avg session score: 3.8 / 5.0

  By memory type:
    warning      avg=1.82  (91% relevant)
    decision     avg=1.45  (78% relevant)
    continuity   avg=1.31  (71% relevant)
    general      avg=0.67  (34% relevant)
    codemap      avg=1.12  (56% relevant)

  Re-ranker status: trained (187 samples, last: 2026-04-01)
  Top features:     type_is_warning (+2.1), cosine_sim (+1.8), file_overlap (+1.3)

  Trend (last 7 sessions):
    Precision: 0.68 → 0.73 → 0.71 → 0.78 → 0.75 → 0.79 → 0.81
```

### Tier 2: Synthetic Task Suite (LLM-as-Judge)

Extends the existing `examples/dualmem-bench/` from 4 tasks to ~12, covering three task types.

#### Task Type A: Question Answering (existing + expanded)

Already have: `decision`, `warning`, `continuity`, `noise`. Add:

- **Cross-reference** — "Which files need to change if I modify the auth middleware?" Tests whether file associations and co-change data surface correctly.
- **Temporal** — "What changed in the last week?" Tests whether continuity memories and recency ranking work.

#### Task Type B: Code Generation Guidance

New task type. The agent is asked to write or modify code. Judge scores whether the response references the right constraints/patterns from memory.

- **Constrained generation** — "Write a new API endpoint for user deletion." Memory contains: payment amounts must be cents (int64), Stripe signature validation required on webhooks, RFC 7807 error format. Judge checks if the response incorporates these constraints.
- **Warning-aware refactor** — "Refactor the rate limiter to use sliding window." Memory contains a warning not to touch cleanup(). Judge checks if the response preserves the warning constraint.
- **Pattern continuation** — "Add a new database model." Memory contains decisions about UUIDs as PKs, sqlc for queries, goose for migrations. Judge checks if the response follows established patterns.

#### Task Type C: Context Triage

New task type. Instead of judging the agent's final answer, directly judge the assembled context block. "Given task X, is this context useful?"

- **Precision-focused** — Seed 50 memories, ask a narrow question. Judge rates what fraction of the assembled context is relevant.
- **Recall-focused** — Seed memories where 5 are critical for a task. Judge checks if all 5 appear.
- **Budget efficiency** — Same task at 500, 1000, 2000 token budgets. Judge rates if the system makes good budget allocation decisions.

#### Output Format

Existing report format plus:
- `results.json` — machine-readable results for tracking over time
- `README-benchmark.md` — auto-generated markdown table suitable for embedding in README

### Tier 3: Feature Micro-Benchmarks (Deterministic)

Extends existing `bench_test.go` and `bench_scenarios.go`. No API keys needed. Runs in CI.

New scenarios:

- **Co-change boost** — Memory mentions file A. File A co-changes with file B. Query is about file B. Does B's memory get boosted?
- **Entity graph boost** — Memories linked to entity "AuthService". Query mentions "authentication." Do entity-linked memories get boosted?
- **Knowledge doc vs raw** — Same content available as raw memories and as a synthesized knowledge doc. Does the knowledge doc get preferred (fewer tokens, same info)?
- **Re-ranker regression** — If re-ranker weights exist, verify they improve ordering vs no re-ranker on the benchmark scenarios. If no weights exist, test is skipped.

These extend the existing `benchScenario` / `benchQuery` framework with ground-truth tags.

## Files to Modify/Create

**New files:**
- `dualmem/rating.go` — Rating logic: snapshot persistence, feature extraction, rating storage, stats queries
- `dualmem/rating_test.go` — Tests for rating pipeline
- `dualmem/reranker.go` — Linear model: training (gradient descent), inference, serialization
- `dualmem/reranker_test.go` — Tests for re-ranker
- `examples/dualmem-bench/tasks_codegen.go` — Code generation guidance tasks
- `examples/dualmem-bench/tasks_triage.go` — Context triage tasks
- `examples/dualmem-bench/tasks_qa.go` — Expanded QA tasks (move existing + add new)

**Modified files:**
- `dualmem/store_sqlite.go` — Migration v9: `context_ratings` and `session_ratings` tables
- `dualmem/dualmem.go` — Persist context snapshot in `AssembleContext`, wire re-ranker into candidate scoring
- `dualmem/distill.go` — Add session-level rating during distill pipeline
- `cmd/dualmem/main.go` — New CLI commands: `rate`, `train`, `stats`
- `docs/example-claude-md.md` — Update CLAUDE.md template with distill rating instructions
- `dualmem/bench_scenarios.go` — New micro-benchmark scenarios
- `dualmem/bench_test.go` — New scenario tests + re-ranker regression test
- `examples/dualmem-bench/scenarios.go` — Restructure existing tasks, add new task types
- `examples/dualmem-bench/main.go` — JSON output, README table generation

**Existing code to reuse:**
- `dualmem/scorer.go:ImportanceScorer` — Feature extraction for re-ranker mirrors importance scoring features
- `dualmem/bench_test.go:evaluateByIDs()` — Precision/recall/NDCG computation reused for context triage tasks
- `dualmem/bench_scenarios.go:benchScenario` — Same framework for new micro-benchmark scenarios
- `examples/dualmem-bench/main.go:runJudge()` — Judge pattern extended for new task types

## Verification

### Unit tests
```bash
go test ./dualmem/ -run TestRating        # rating pipeline
go test ./dualmem/ -run TestReranker      # linear model train/predict
go test ./dualmem/ -run TestBench         # all micro-benchmarks including new scenarios
```

### Live rating test
```bash
# 1. Context assembly persists a snapshot:
~/go/bin/dualmem context "auth refactor" --budget 3000
# Should print context as normal + log snapshot ID

# 2. After session work, rate the snapshot (called by agent during distill):
~/go/bin/dualmem rate --snapshot <id> --ratings '{"mem_abc": 2, "mem_def": 0}'

# 3. Session-level rating:
~/go/bin/dualmem rate --session --score 4 --explanation "warnings were helpful, codemap too broad"

# 4. View accumulated stats:
~/go/bin/dualmem stats
```

### Re-ranker training
```bash
# After accumulating 50+ ratings:
~/go/bin/dualmem train
# Should output learned weights and validation accuracy

# Compare with/without re-ranker:
~/go/bin/dualmem context "auth refactor" --no-rerank
~/go/bin/dualmem context "auth refactor"
```

### Synthetic benchmark suite
```bash
GEMINI_API_KEY=... go run ./examples/dualmem-bench/ --task all
# Should run all ~12 tasks, output per-task + aggregate scores + results.json
```

### Regression guard
```bash
go test ./dualmem/ -run TestBench
# DualMem avg recall >= flat avg recall (existing guard)
# New: re-ranker scenarios pass if weights exist
```
