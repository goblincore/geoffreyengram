# Phase 4: Contextual Codebase Understanding Benchmark

## Context

DualMem and UnravelAI both aim to give coding agents structured codebase understanding. This benchmark compares them head-to-head on contextual retrieval quality, report usefulness, latency, and cost.

Existing benchmarks measure dualmem in isolation:
- `search_bench_test.go` — HDC/Hybrid/Grep search accuracy (current: 88% hybrid)
- `bench_test.go` — context assembly quality (current: 0.94 NDCG, 0.75 precision, 0.89 recall)

This benchmark adds cross-system comparison and tests against real codebases at varying scale.

## Corpora

| Corpus | Language | Scale | Role |
|---|---|---|---|
| geoffreyengram | Go | ~11 modules | DualMem home turf |
| unravelai | TS | ~18 core files | UnravelAI home turf |
| LearnCard | TS monorepo | Large (100+ components) | Neutral, tests scaling |

Paths:
- geoffreyengram: `/Users/donny/Projects/2026/geoffreyengram`
- unravelai: `/tmp/unravelai`
- LearnCard: `/Users/donny/Work/LearnCard`

## Query Design

14 queries per corpus (42 total), distributed across 6 query types:

| Query Type | Count | Example |
|---|---|---|
| Feature location | 3 | "Where is the HDC encoding logic?" |
| Dependency tracing | 2 | "What breaks if I change the DB schema?" |
| Concept search | 3 | "How does caching work?" |
| Bug localization | 2 | "API returns stale data after updates" |
| Structural understanding | 2 | "Request lifecycle from HTTP to DB" |
| Feasibility | 2 | "Can I add X without touching Y?" |

Each query has a difficulty level (easy/medium/hard) for stratified analysis.

### Ground Truth Generation Workflow

1. **LLM-generated draft** — Claude explores each codebase and generates all 42 query+ground-truth entries
2. **Confidence tagging** — each entry gets `"confidence": "high|medium|low"` based on answer certainty
3. **Human review** — user reviews medium/low confidence entries only (dependency tracing and feasibility queries will naturally be lower confidence since answers are more subjective)
4. **Iteration** — flagged entries are corrected and re-verified

This minimizes human effort while keeping accuracy. High-confidence feature location queries (e.g., "where is X?") can be skim-approved; subjective queries get careful review.

### Ground Truth Format

```json
{
  "id": "ge-feat-01",
  "corpus": "geoffreyengram",
  "type": "feature_location",
  "query": "Where is the HDC encoding logic?",
  "difficulty": "easy",
  "confidence": "high",
  "ground_truth": {
    "files": ["dualmem/hdc.go"],
    "modules": ["dualmem/"],
    "concepts": ["HDCEncoder", "EncodeModule", "EncodeQuery", "BasisVector"],
    "explanation": "HDC encoding lives entirely in hdc.go — 2048-dim vectors with 4-layer encoding (path/symbols/lang/content)"
  }
}
```

- `files` — exact files for Precision/Recall
- `modules` — module-level matches for coarser NDCG evaluation
- `concepts` — key symbols that should appear in reports (for judge scoring)
- `explanation` — reference answer for LLM-as-judge

Ground truth is LLM-generated from codebase exploration, then human-verified.

### Matching Rules

- **Strict match**: file path in results → counts for Precision@K, Recall@K
- **Module match**: any file within the module → counts for NDCG (rewards close misses)
- **Concept match**: symbol/keyword in report text → used by judge for completeness scoring

## Architecture

```
benchmarks/contextual/
├── queries/
│   ├── geoffreyengram.json
│   ├── unravelai.json
│   └── learncard.json
├── adapters/
│   ├── dualmem.go          # Native Go — calls search-code, context, consult
│   └── unravel.js          # Node.js — library-level search + MCP consult
├── judge/
│   └── judge.go            # Claude Opus judge via claude CLI
├── compare.go              # Aggregation, table output, JSON results
├── main.go                 # CLI entry point
└── results/                # Output (gitignored)
    ├── YYYY-MM-DD-run.json
    └── YYYY-MM-DD-run.md
```

## Adapters

### DualMem Adapter (Go, native)

Three modes matching the three evaluation layers:

```
SearchResults = dualmem search-code "<query>" → ranked modules with scores
ConsultReport = dualmem consult "<query>" → structured 4-section report
ContextBlock  = dualmem context "<query>" --budget 3000 → assembled context
```

Calls the dualmem binary directly (or imports the library — TBD based on what's cleaner).

### UnravelAI Adapter (Node.js child process)

**Library-level** (for IR metrics):
```
// Go shells out:
// node benchmarks/contextual/adapters/unravel.js --mode search --corpus <path> --query "<query>"
//
// unravel.js imports:
GraphBuilder.buildFromDirectory(corpusPath)
SearchEngine.queryGraphForFiles(query, graph) → ranked files with scores
//
// Returns JSON to stdout:
// {"results": [{"file": "...", "score": 0.85}], "latency_ms": 42}
```

**MCP-level** (for report quality):
```
// --mode consult
// Spawns: node /tmp/unravelai/unravel-mcp/index.js --stdio
// Sends consult tool call, captures 4-section intelligence report JSON
```

**Latency measurement:**
- Wall clock per query (includes process spawn — realistic overhead)
- Separate "warm" latency (graph pre-built) vs "cold" (includes graph build)

## Metrics

### IR Metrics (per query, aggregated per corpus and overall)

| Metric | Description |
|---|---|
| Precision@3 | Of top 3 results, fraction in ground truth |
| Precision@5 | Of top 5 results, fraction in ground truth |
| Recall@5 | Of ground truth files, fraction in top 5 |
| Recall@10 | Of ground truth files, fraction in top 10 |
| NDCG@10 | Ordering quality — correct results ranked higher? |
| Latency (ms) | Wall clock per query |
| API cost | Number of external API calls (Gemini, etc.) |

### Report Quality (LLM-as-judge)

**Judge**: Claude Opus via `claude -p "..." --model claude-opus-4-6`. Uses subscription, no API key needed.

**Dimensional scoring** (1-5 each, per report):
- **Accuracy** — Are stated facts correct?
- **Completeness** — Covers all relevant aspects from ground truth?
- **Relevance** — Is noise minimized? Signal-to-noise ratio.
- **Actionability** — Could a developer act on this immediately?

**Head-to-head preference** (per query):
- Present both reports anonymously (shuffled A/B order to avoid position bias)
- Judge picks: A wins / B wins / tie
- Include short rationale for debugging
- Aggregate as win rate

Total judge calls: 42 queries × 3 (dualmem dimensional + unravel dimensional + head-to-head) = ~126 calls via `claude` CLI.

## Output

### JSON (`results/YYYY-MM-DD-run.json`)

```json
{
  "run_date": "2026-04-06",
  "corpora": ["geoffreyengram", "unravelai", "learncard"],
  "summary": {
    "dualmem": {"precision_at_3": 0.78, "recall_at_5": 0.85, "ndcg_10": 0.91, "avg_latency_ms": 12, "api_calls": 0},
    "unravel": {"precision_at_3": 0.72, "recall_at_5": 0.80, "ndcg_10": 0.87, "avg_latency_ms": 340, "api_calls": 14}
  },
  "report_quality": {
    "dualmem": {"accuracy": 3.8, "completeness": 4.1, "relevance": 4.0, "actionability": 3.9},
    "unravel": {"accuracy": 4.0, "completeness": 3.7, "relevance": 3.5, "actionability": 3.6},
    "head_to_head": {"dualmem_wins": 22, "unravel_wins": 15, "ties": 5}
  },
  "by_corpus": { "..." : "..." },
  "by_query_type": { "..." : "..." },
  "by_difficulty": { "..." : "..." },
  "per_query": [ "..." ]
}
```

### Markdown (`results/YYYY-MM-DD-run.md`)

- Summary table (overall + per-corpus)
- Breakdown by query type
- Breakdown by difficulty
- Head-to-head win rate
- Notable outliers

## CLI

```bash
go run ./benchmarks/contextual/ \
  --corpora geoffreyengram,unravelai,learncard \
  --output results/

# Optional flags:
#   --corpus-only geoffreyengram    Run single corpus
#   --skip-judge                    IR metrics only, no judge calls
#   --skip-unravel                  DualMem-only (regression testing)
#   --query-type feature_location   Run one query type only
#   --verbose                       Log per-query details
```

## Dependencies

- Go 1.25+ (benchmark runner)
- Node.js (UnravelAI adapter)
- `claude` CLI (judge — must be on PATH)
- `~/go/bin/dualmem` (dualmem adapter)
- Gemini API key (for dualmem embeddings if running live search)

## Files to Create

| File | Purpose |
|---|---|
| `benchmarks/contextual/main.go` | CLI entry point, flag parsing, orchestration |
| `benchmarks/contextual/compare.go` | Metric computation, aggregation, output formatting |
| `benchmarks/contextual/judge/judge.go` | LLM-as-judge via claude CLI |
| `benchmarks/contextual/adapters/dualmem.go` | DualMem adapter |
| `benchmarks/contextual/adapters/unravel.js` | UnravelAI adapter (library + MCP) |
| `benchmarks/contextual/queries/geoffreyengram.json` | 14 queries + ground truth |
| `benchmarks/contextual/queries/unravelai.json` | 14 queries + ground truth |
| `benchmarks/contextual/queries/learncard.json` | 14 queries + ground truth |
| `benchmarks/contextual/results/.gitkeep` | Output directory |

## Files to Modify

| File | Change |
|---|---|
| `docs/unravel-comparison-plan.md` | Mark Phase 4 as in-progress, link to this spec |

## Verification

```bash
# IR metrics only (fast, no judge):
go run ./benchmarks/contextual/ --skip-judge --corpus-only geoffreyengram

# Full run with judge:
go run ./benchmarks/contextual/

# Single query type for debugging:
go run ./benchmarks/contextual/ --query-type feature_location --verbose
```
