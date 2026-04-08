# geoffreyengram

Cognitive memory engine for AI agents. Gives coding tools (Claude Code, Cursor, etc.) cross-session memory with token-budget-aware retrieval, learned file relationships, and structural code search.

Pure Go, SQLite (no CGO), single binary.

## Architecture

```
                            ┌─────────────────────────────────────────────┐
                            │              CONTEXT ASSEMBLY               │
                            │                                             │
  ┌──────────┐              │   Query + Token Budget                      │
  │ dualmem  │              │         │                                   │
  │   add    │──┐           │         ▼                                   │
  └──────────┘  │  ┌────────│── Structural Diff (what changed?)           │
  ┌──────────┐  │  │        │── Codemap (cold start only, ~400 tok)      │
  │ dualmem  │──┤  │        │── Checkpoints (session handoffs)            │
  │ distill  │  │  │        │── File Annotations (grouped by file)  ◄────│── NEW
  └──────────┘  │  │        │── Knowledge Docs (synthesized concepts)     │
                ▼  │        │── Detail Memories (decisions, warnings)     │
          ┌─────────────┐   │── Sketch (arcs, profile)                   │
          │ Importance   │  │         │                                   │
          │   Router     │  │         ▼                                   │
          │  (no LLM)    │  │   Token-budgeted context block             │
          └──────┬───────┘  └─────────────────────────────────────────────┘
           ┌─────┴─────┐
           │           │
     ≥ 0.65│     < 0.65│        ┌─────────────────────────────┐
           ▼           ▼        │       STRUCTURAL LAYERS     │
    ┌────────────┐ ┌────────┐   │                             │
    │Detail Path │ │ Sketch │   │  Co-Change Graph            │
    │full fidelity│ │episodes│   │   └─ file pairs + strength  │
    │top-100     │ │→ arcs  │   │  Entity Graph               │
    │768d embeds │ │→ profile│   │   └─ typed edges + links    │
    └─────┬──────┘ └────────┘   │  HDC Codemap                │
          │                     │   └─ 2048d, no API calls     │
          ├── file associations │  Structural Graph            │
          ├── entity extraction │   └─ calls, imports, contains│
          └── synthesis ──────► │  Knowledge Docs              │
                                │   └─ clustered summaries     │
                                └─────────────────────────────┘
```

**Dual-path routing**: Memories are scored at insert time (no LLM calls). High-importance memories (decisions, warnings) stay at full fidelity in the Detail Path. Everything else is compressed into a hierarchical sketch: episodes (30-day retention) → narrative arcs (90-day, 128d projection) → user profile (64d projection).

**File-centric context**: The primary retrieval model is file-centric — `FileAnnotations` pulls warnings, decisions, traces, knowledge docs, and checkpoints indexed by file path. Context output groups related memories under their files instead of flat type-sorted lists. File paths are extracted from checkpoints and recent memories, creating a natural "what do I need to know about the files I'm working on?" view.

**Smart codemap toggle**: The structural codemap is only included on cold starts (no active checkpoints, explore/default intent). For task-specific sessions, the ~400 tokens are reallocated to file-centric context and memories.

**Context assembly**: Given a query and token budget, `AssembleContext` packs context in priority order: structural diff → codemap (cold start only) → checkpoints → file annotations → knowledge docs → profile → arcs → detail memories → episodes. Task-aware: auto-detects intent (debug/continue/feature/explore) and adjusts memory type weights. Supports **progressive disclosure** (`--index` mode) — outputs a compact index of available items with token costs, then the agent fetches only what it needs via `dualmem show`.

## Quickstart

```bash
go install github.com/goblincore/geoffreyengram/cmd/dualmem@latest
export GEMINI_API_KEY=your-key  # add to ~/.zshrc

dualmem add --text "Auth uses JWT, not sessions" --type decision
dualmem add --text "Don't touch rateLimiter cleanup()" --type warning --salience 0.9 --files "rate_limiter.go"
dualmem search "authentication" --limit 5
dualmem search-code "authentication middleware"       # HDC-powered, no API calls
dualmem context "fix the auth bug" --budget 3000      # token-budgeted context
dualmem consult "how does auth work?"                 # synthesized intelligence report
dualmem explore "credential issuance" --budget 3000   # grounded code briefing
dualmem index                                         # pre-warm codebase index
```

For Claude Code integration, copy [`docs/example-claude-md.md`](docs/example-claude-md.md) into your `~/.claude/CLAUDE.md`.

## Features

### Memory types and routing

Memories are classified into sectors and scored by importance. Typed memories (`--type warning`, `--type decision`) are biased toward the Detail Path:

```bash
dualmem add --type warning --text "Don't touch rateLimiter cleanup()" --files "rate_limiter.go" --salience 0.9
dualmem add --type decision --text "Rejected Postgres, chose SQLite" --files "store_sqlite.go"
dualmem add --type continuity --text "Done: JWT. Remaining: refresh tokens" --files "auth.go,jwt.go"
dualmem add --type trace --text "routes/index.ts:320 → contact-methods.ts:187\nOTP verification flow" --files "index.ts,contact-methods.ts"
```

Context assembly prioritizes: warnings first, then decisions/continuity/traces, then general memories. Intent-aware weighting adjusts per query — debugging boosts warnings (2x), exploring boosts traces and maps (2x), resuming boosts continuity (2x).

### Checkpoints — structured session handoffs

```bash
dualmem checkpoint --task "auth refactor" --status in_progress \
  --files "auth.go,middleware.go" \
  --done "JWT validation,middleware" --remaining "refresh tokens,logout"
dualmem checkpoint --list
```

### Co-change graph — learned file relationships

When a memory mentions multiple files, DualMem automatically builds co-change edges between all file pairs. Over time, this creates a learned map of which files change together.

```bash
dualmem cochange auth.go                      # files that co-change with auth.go
dualmem cochange auth.go --min-strength 2.0   # only strong relationships
dualmem cochange --decay                      # apply 90-day half-life decay
```

**Context assembly integration**: File hints from checkpoints and memories are expanded through co-change neighbors. Working on `auth.go`? The codemap will also highlight `middleware.go` and `jwt.go` if they historically co-change — even if their code structure (HDC vectors) is dissimilar.

Edges include concept labels (entity names that explain *why* files co-change), populated automatically during `dualmem synthesize`.

### Entity graph — structure-aware retrieval

Entities extracted from memories are stored in a knowledge graph. Search applies **structure-aware** graph traversal — direct entity matches get full boost, expanded neighbors get reduced boost scaled by edge type:

| Edge type | Multiplier | Meaning |
|-----------|-----------|---------|
| `depends_on` | 1.0 | Strong structural relationship |
| `implements` | 0.9 | Strong behavioral relationship |
| `modifies` | 0.8 | Moderate change relationship |
| `uses` | 0.7 | Moderate usage relationship |
| `relates` | 0.5 | Weak association |

```bash
dualmem entities                          # graph stats
dualmem entities search "SQLite"          # find entities by name
dualmem entities show "store_sqlite.go"   # entity + relationships
```

### Knowledge documents — synthesized context

`dualmem synthesize` clusters related memories and produces coherent concept documents. These appear in context output before individual memories, saving tokens:

```bash
dualmem synthesize --dry-run   # preview clusters
dualmem synthesize             # synthesize knowledge docs
dualmem docs                   # list docs
dualmem docs show <topic>      # read a doc
```

### Consult — lazy-synthesis intelligence reports

Ask a question about any subsystem and get a structured explanation. If a cached knowledge doc matches (cosine ≥ 0.75), it's served instantly. Otherwise, structural evidence is gathered (HDC codemap + call graph + co-change + memories) and Gemini Flash synthesizes a narrative — which is then cached as a knowledge doc for future queries.

```bash
dualmem consult "how does auth work?"
dualmem consult "how does the HDC encoder work?" --budget 2000
```

Output is a 3-section report: **Explanation** (synthesized narrative), **Structural Evidence** (call graph edges + co-change neighbors), **Relevant Files** (HDC-ranked with relevance source). Includes a confidence score based on knowledge doc match, structural edge coverage, and memory count.

### Explore — grounded code briefings

Read ranked source files and produce a snippets-first briefing. Unlike `consult` (which caches knowledge docs), `explore` is ephemeral — it reads actual code and summarizes what it finds:

```bash
dualmem explore "how does credential issuance work?" --budget 3000
dualmem explore "auth middleware" --ns learncard
```

### Index — pre-warm codebase index

On first run, `explore`, `consult`, and `search-code` scan the codebase to build a codemap + structural edge graph. For large repos (4000+ files), use `index` to pre-warm the cache with progress output:

```bash
dualmem index                    # scan cwd, verbose progress
dualmem index --ns myproject     # explicit namespace
dualmem index --force            # re-index even if cache is fresh
```

The index is cached by git commit — subsequent commands on the same commit skip the scan entirely.

### HDC-powered code search

Find relevant code modules by natural language — uses hyperdimensional computing (2048-dim vectors from path, symbols, language, identifiers), no API calls, ~55us/query:

```bash
dualmem search-code "authentication middleware"
dualmem search-code "database connection pooling" --limit 5
dualmem search-code "auth" --graph   # experimental: PageRank + tag extraction
```

### Session distillation — ambient memory capture

Automatically extract memories from session transcripts at session end:

```bash
dualmem distill              # auto-detect latest Claude Code session
dualmem distill --dry-run    # preview extracted facts
dualmem distill --auto       # idempotent (for hooks)
```

### File-scoped recall

Inject relevant memories when Claude opens a file — warnings about `rate_limiter.go` surface even if the session task is "refactor error handling":

```bash
dualmem file-context rate_limiter.go   # memories for a specific file (warnings, decisions, maps, traces)
dualmem file-index                     # regenerate file index for hook
```

`FileAnnotations` is also used internally by `AssembleContext` — file paths from checkpoints and memories are expanded into grouped annotations in the `[File Context]` section of context output.

### Staleness detection

Memories can go stale when code changes. `gc --stale` checks each memory against the current codebase:

```bash
dualmem gc --stale              # check all memories for staleness
dualmem gc --stale --dry-run    # preview only
```

Three checks: **file staleness** (>50% of referenced files changed since memory was written), **symbol staleness** (identifiers mentioned in memory text no longer exist in current tags), and **age staleness** (memory older than 30 days without reinforcement). Stale memories are flagged in context output so the agent knows to verify before acting on them.

### Context quality ratings & adaptive re-ranker

Measure whether assembled context is actually useful. The consuming agent (e.g., Claude) rates each context item at two moments — prospectively when context loads, and retrospectively at session end.

```bash
dualmem context "auth refactor" --budget 3000
# → outputs context + snapshot ID (e.g. snap_17752...)

# Agent rates items after reading context (early) or at session end (late):
dualmem rate --snapshot snap_17752... --phase early --ratings '{"mem_abc": 2, "mem_def": 0}'
dualmem rate --snapshot snap_17752... --phase late  --ratings '{"mem_abc": 2, "mem_ghi": 1}'

# Session-level rating:
dualmem rate --session --score 4 --explanation "warnings were helpful, codemap too broad"

# View accumulated stats:
dualmem stats
```

Once 50+ ratings accumulate, train a learned re-ranker that improves retrieval ordering:

```bash
dualmem train   # fits logistic regression from ratings → stores weights
```

The re-ranker adjusts candidate memory ordering during `AssembleContext` using 10 features (cosine similarity, importance, salience, memory type, file overlap, age, etc.). Late-phase ratings are weighted 2x over early-phase in training. Safety rails: coefficients bounded to [-5, 5], re-ranker only adjusts ordering (never filters), `--no-rerank` flag for comparison.

### Progressive disclosure — index-then-fetch

Instead of committing the full token budget upfront, probe what's available first:

```bash
dualmem context "auth refactor" --budget 3000 --index
# → compact table (~300 tokens): type icons, IDs, titles, token costs
# → includes snapshot ID for relevance tracking

dualmem show --snapshot snap_17753... mem_a3f2 kdoc_dualmem chk_auth
# → full text of selected items only (~700 tokens instead of 3000)
# → implicitly rates fetched items as relevant, skipped as irrelevant
```

The agent's fetch/skip behavior feeds directly into the re-ranker training pipeline — no explicit rating step needed. Fetched items get rating=2, skipped items get rating=0, with training weight 1.5 (between early=1.0 and late=2.0).

### Auto-capture — ambient file interaction logging

A PostToolUse hook logs file interactions during the session to a local JSONL buffer (no API calls, ~5ms). At distill time, this enriches the transcript with a complete file-touch timeline.

The hook also detects **exploration patterns** — when the agent does 4+ grep/read calls across 3+ files within 2 minutes (tracing a code flow), it nudges the agent to save what it discovered as a `trace` memory. This captures the structured path and semantic understanding before it's lost:

```bash
# Hook auto-logs: {tool, files, timestamp, query, lines} for Read/Write/Edit/Grep/Glob/Bash
# Exploration detection triggers nudge: "Consider saving: dualmem add --type trace ..."
# At session end:
dualmem distill --auto  # transcript + file interactions → richer memory extraction
```

### Seed — cold-start context

Pre-seed context for new projects by analyzing codebase structure:

```bash
dualmem seed --dry-run   # preview clusters
dualmem seed             # generate seed memories
```

## CLI Reference

```bash
# Memory
dualmem add --text "..." --type decision --salience 0.9 --files "a.go,b.go"
dualmem search "query" --limit 5
dualmem search "LC-1663"                                # identifier pre-filter

# Code search (HDC-powered, no API calls)
dualmem search-code "authentication middleware"
dualmem search-code "auth" --graph                      # experimental: PageRank + tags

# Context assembly
dualmem context "auth system" --budget 3000
dualmem context "fix the bug" --intent debug
dualmem context "task" --budget 3000 --index              # progressive disclosure
dualmem show --snapshot snap_xxx mem_a kdoc_b              # fetch specific items

# Session management
dualmem checkpoint --task "auth refactor" --status in_progress --files "auth.go" --done "JWT" --remaining "refresh,logout"
dualmem checkpoint --list
dualmem map                                             # graph-based codebase map (default)
dualmem map --legacy                                    # HDC-based codebase map
dualmem map --refresh                                   # force full re-scan
dualmem map --git-graph                                 # diagnostic: git co-change edges
dualmem diff                                            # changes since last session

# Seed and distillation
dualmem seed [--dry-run] [--force]
dualmem distill [--dry-run] [--auto] [--file path]

# File-scoped recall
dualmem file-context rate_limiter.go
dualmem file-index

# Entity graph
dualmem entities [stats|search|show|top]

# Co-change graph
dualmem cochange <file> [--min-strength N] [--decay]

# Structural graph
dualmem graph                                           # edge statistics
dualmem graph --json                                    # JSON output

# Knowledge docs
dualmem docs [list|show|delete|export]
dualmem synthesize [--dry-run] [--force] [--all]
dualmem consult "how does X work?" [--budget 2000]  # lazy-synthesis intelligence report

# Context quality
dualmem rate --snapshot <id> --phase late --ratings '{"mem_id": 2}'
dualmem rate --session --score 4 --explanation "..."
dualmem train                                           # fit re-ranker from ratings
dualmem stats                                           # quality metrics + trends

# Maintenance
dualmem promote --id <id> [--type warning --salience 0.9]
dualmem demote --id <id>
dualmem gc [--dry-run --verbose]
dualmem gc --stale                                      # check for stale memories
dualmem profile
dualmem status
```

## Claude Code Integration

Copy [`docs/example-claude-md.md`](docs/example-claude-md.md) into your `~/.claude/CLAUDE.md`. It teaches the agent to:
- Load context at session start (`dualmem context`)
- Save decisions, warnings, and checkpoints during work
- Search memory before exploring (`dualmem search`)
- Use HDC code search (`dualmem search-code`)
- Always include `--files` to build the co-change graph

**Hooks** (add to `.claude/settings.json`):
- **Stop hook**: `dualmem distill --auto` — ambient memory capture at session end
- **PreToolUse Read hook**: `dualmem file-context` — inject file-scoped memories when Claude opens a file
- **PostToolUse hook**: `dualmem-autocapture.sh` — log file interactions to session JSONL for distill enrichment

See [`docs/example-claude-md.md`](docs/example-claude-md.md) for the full template.

## Engram — Character Memory for NPCs

Also includes **Engram**, a character memory system for NPCs, companions, and chatbots. Memories are classified into cognitive sectors (episodic, semantic, procedural, emotional) with natural exponential decay, entity graph associations, and reflective synthesis.

- Composite scoring: `(similarity*0.6 + salience*0.2 + recency*0.1 + linkWeight*0.1) * sectorWeight`
- Waypoint entity graph: mentioning a song surfaces the person you heard it with
- Multimodal: text, images, audio in same vector space (Gemini Embedding 2)
- Reflective synthesis: `Reflect()` creates meta-memories between conversations
- MCP server: `engram-mcp` with tools for remember, recall, reflect

```go
import engram "github.com/goblincore/geoffreyengram"

mem, _ := engram.Init(engram.Config{
    DBPath:       "./data/memory.db",
    GeminiAPIKey: os.Getenv("GEMINI_API_KEY"),
})
defer mem.Close()

mem.Add("I just got back from Berlin!", "That's amazing!", "lily:player123")
results := mem.Search("travel", "lily:player123", 5, nil)
```

## Project Structure

```
geoffreyengram/
├── engram.go                # Engram core (Init, Search, Add, Reflect)
├── dualmem/                 # DualMem agent memory engine
│   ├── dualmem.go           # Engine (Add, DualSearch, AssembleContext, FileAnnotations, Consult)
│   ├── types.go             # Config, Store interface, Intent, all data types
│   ├── detail.go            # Detail Path (importance scoring, capacity management)
│   ├── sketch.go            # Sketch Path (episodes, arcs, profiles)
│   ├── scorer.go            # Importance scoring (sector bias, salience, specificity, novelty)
│   ├── pipeline.go          # Background compression workers
│   ├── codemap.go           # HDC-based codebase scanner, module search, co-change-aware ranking
│   ├── hdc.go               # HDC encoder (2048-dim, 4-layer: path/symbols/lang/content)
│   ├── scan.go              # Graph-based scanner (RepoScanner, ScanAndRank pipeline)
│   ├── graph.go             # File dependency graph + personalized PageRank
│   ├── tags.go              # Tree-sitter tag extraction (Go, TS, Python, Rust)
│   ├── tag_cache.go         # SQLite-cached tags with mtime invalidation
│   ├── git_cochange.go      # Git co-change graph (commit history mining, time decay)
│   ├── parse_treesitter.go  # Regex-based TS/Python/Rust parsing (codemap path)
│   ├── cochange.go          # Memory co-change graph (builder, query, entity enrichment)
│   ├── structural_graph.go  # Structural edge graph (calls, imports, contains)
│   ├── staleness.go         # Memory staleness detection (file, symbol, age checks)
│   ├── diff.go              # Git-based structural diffs
│   ├── knowledge.go         # Knowledge doc synthesis
│   ├── distill.go           # Session transcript distillation
│   ├── rating.go            # Context quality ratings, snapshots, training rows
│   ├── reranker.go          # Learned re-ranker (logistic regression from ratings)
│   ├── store_sqlite.go      # SQLite backend (migrations v1-v9)
│   ├── project.go           # Random projection, cosine similarity
│   ├── embed_gemini.go      # Gemini embedding API
│   ├── summarize_gemini.go  # Gemini summarization API
│   └── format.go            # Output formatting utilities
├── cmd/
│   ├── dualmem/             # CLI tool
│   ├── dualmem-mcp/         # MCP server (13 tools)
│   ├── dispatch-ui/         # Dispatch dashboard (web UI)
│   └── engram-mcp/          # Engram MCP server
└── examples/
    ├── chat/                # Local REPL with Ollama
    ├── comparison/          # Memory mode comparison + LLM judge
    └── dualmem-bench/       # LLM-as-judge benchmark (9 tasks: QA, codegen, triage)
```

## Dispatch UI

A web dashboard for managing headless Claude Code agent tasks. Replaces the `dispatch.sh` + cron system with a Go-native orchestrator.

### Quick start

```bash
go build -o ~/go/bin/dispatch-ui ./cmd/dispatch-ui/
dispatch-ui
# Open http://localhost:8090
```

The dashboard reads your existing `~/.claude/dispatch/plans/` directory. Existing plan files appear automatically.

### Features

- **Task queue** — see pending, running, done, and failed tasks at a glance
- **Live log streaming** — watch agent output in real-time via SSE
- **Task creation** — write a short description, Opus generates the full plan, review and dispatch
- **Task management** — cancel running tasks, retry failed ones, adjust priority, delete
- **Multi-harness** — dispatch to Claude CLI, Pi, or OpenCode (set `harness:` in plan frontmatter)
- **Dualmem integration** — Pi tasks get project memory injected + CLI instructions for saving memories

### Creating a task from the UI

1. Click **"+ New Task"**
2. Fill in: task name, short description, project path, executor model, priority, max runtime
3. Click **"Generate Plan"** — the planning model (default: Opus via your subscription) expands the description into a full plan with context, instructions, and acceptance criteria
4. Review/edit the generated plan
5. Click **"Dispatch"** — the task enters the queue and runs on the next poll cycle (5s)

### Creating a task manually

Drop a `.md` file into `~/.claude/dispatch/plans/` with this format:

```yaml
---
title: "My task title"
status: pending
project: /path/to/your/project
model: glm-5.1
harness: claude          # "claude" (default), "pi", or "opencode"
priority: 2
max_runtime: 25m
allowed_tools: Edit,Write,Bash,Read,Glob,Grep
---

## Context
What this task is about.

## Instructions
Step-by-step instructions for the agent.

## Acceptance criteria
- Tests pass
- Code compiles
```

To use your Claude subscription instead of an API key, omit `api_key_env` and `base_url` — the CLI will use your subscription auth.

### Configuration

The server reads `~/.claude/dispatch/dispatch.conf`:

```bash
PLANS_DIR="$HOME/.claude/dispatch/plans"
REPORTS_DIR="$HOME/.claude/dispatch/reports"
CLAUDE_BIN="$HOME/.local/bin/claude"
DEFAULT_MODEL="glm-5.1"
LISTEN_ADDR="127.0.0.1:8090"
PLANNING_MODEL="claude-opus-4-6"
```

API keys go in `~/.claude/dispatch/.env`:

```bash
export ZAI_API_KEY="your-key"
export ANTHROPIC_API_KEY="your-key"
export GEMINI_API_KEY="your-key"   # needed for dualmem embedding search
```

### Harness comparison (GLM 5.1)

| Harness | Time | Streaming | Edit tool | Notes |
|---------|------|-----------|-----------|-------|
| **Pi** | 1m 50s | ✅ Real-time | ✅ Native works | Recommended. Minimal, fast, no Edit issues |
| **Claude CLI** | 4m 37s | ✅ Real-time | ⚠️ Needs Python workaround | Auto-injected preamble tells GLM to use Python for edits |
| **OpenCode** | 15m+ timeout | ❌ Buffers until exit | N/A | Not recommended — stdout buffering makes it unusable |

Pi setup: `npm install -g @mariozechner/pi-coding-agent`, configure `~/.pi/agent/models.json` with your provider.

## Graph-Based Codemap

The `map` command uses a graph-based pipeline: tree-sitter tag extraction → file-level dependency graph → personalized PageRank ranking. The scan pipeline caches tags in SQLite (keyed by file mtime) for fast incremental re-scans.

```bash
dualmem map                    # graph-based codemap (default)
dualmem map --legacy           # HDC-based codemap (original)
dualmem map --refresh          # force full re-scan, clear tag cache
dualmem map --git-graph        # diagnostic: dump git co-change edges
dualmem graph                  # structural edge statistics
dualmem graph --json           # JSON output
```

The `map` output is personalized by query — modules matching the query get higher PageRank weight via adaptive damping. Git co-change edges provide a secondary signal: files that historically change together are boosted in ranking.

**Note**: `search-code` still defaults to the HDC+BM25 approach (no tag extraction, ~55us/query). The graph-based search is available via `search-code --graph` but is slower and experimental.

### Structural neighbors in context assembly

Structural neighbors (from the dependency graph) are merged with co-change neighbors to expand file hints during context assembly:

```
score = parentScore * 0.7 + edgeWeight * 0.3
```

With beam search (top-5 per hop) and pruning (threshold 0.2) to prevent score collapse across hops.

## vs claude-mem

Head-to-head comparison against [claude-mem](https://github.com/thedotmack/claude-mem) (v12.0.1, 46k stars) — the most popular Claude Code memory framework.

### Context quality (LLM-as-judge)

5 developer task scenarios rated by GLM 5.1 on relevance, completeness, signal-to-noise, and actionability (1-10 scale). Position-randomized A/B. claude-mem tested with ChromaDB vector search enabled.

| Scenario | dualmem | claude-mem |
|----------|---------|------------|
| Add Apple Pay to checkout | **5.8** | 2.5 |
| Debug WebSocket drops under load | **7.5** | 2.8 |
| Set up CI/CD for new microservice | **8.2** | 2.8 |
| Junior dev refactoring cache layer | **7.8** | 1.5 |
| Q3 roadmap planning (continuity) | 3.0 | **7.2** |
| **Average** | **6.5** | **3.4** |

dualmem wins on targeted technical tasks (4/5 scenarios) by returning query-aware, file-scoped context with warnings and decisions surfaced first. claude-mem wins on broad continuity queries where its flat recency-based retrieval happens to match the "show me everything in progress" intent.

### Architecture comparison

| | dualmem | claude-mem |
|---|---|---|
| Language | Go binary (38MB, ~5ms startup) | TypeScript/Bun + ChromaDB subprocess |
| Vectors | Gemini 768d inline at write time | ChromaDB via MCP (deferred) |
| Search | Hybrid cosine + BM25 (adaptive alpha) | ChromaDB semantic + SQLite fallback |
| Context | 7-tier budget-aware + intent detection | Progressive disclosure (count-based) |
| Code search | HDC 2048d + BM25, no API calls | None |
| Compression | 3-tier sketch (episodes → arcs → profile) | Observation summaries |

**Where dualmem is better**: Query-aware context assembly, code search, hierarchical compression, zero-dependency binary, semantic deduplication via cosine novelty.

**Where claude-mem is better**: Multi-IDE support (Claude Code, Gemini CLI, Cursor, Codex), web memory viewer UI, privacy tags, token economics tracking, richer observation types, instant writes (deferred embedding).

Full benchmark report and scripts in `benchmarks/`.

## Test suite

```bash
go test ./dualmem/ -v                      # all unit + integration tests
go test ./dualmem/ -run TestBench -v       # context assembly benchmarks (mock embeddings)
go test ./dualmem/ -run TestBenchLive -v   # context benchmarks with real Gemini API
go test ./dualmem/ -run TestSearchBenchmark # HDC+BM25 code search accuracy
go test ./dualmem/ -run TestSQLite -v      # SQLite persistence regression tests
go test ./cmd/dualmem/ -run TestTilde -v   # config tilde expansion tests
```

### Test categories

| Category | Files | What it covers |
|----------|-------|----------------|
| **Core engine** | `dualmem_test.go` | Add, search, context assembly, dual-path routing |
| **Benchmarks** | `bench_test.go`, `bench_scenarios.go` | Precision, recall, NDCG, token utilization across 20+ queries |
| **Code search** | `search_bench_test.go` | HDC vs Hybrid vs Grep accuracy on 8 natural language queries |
| **Codemap** | `codemap_test.go` | Tree-sitter parsing, module extraction, budget-aware rendering |
| **Co-change** | `cochange_test.go` | File relationship graph, decay, path normalization |
| **Embedding** | `classify_embedding_test.go` | Sector classification from embedding vectors |
| **Structural** | `structural_graph_test.go` | Call/import/containment edges from tree-sitter AST |
| **Clustering** | `cluster_test.go` | Import graph construction, community detection |
| **SQLite** | `store_sqlite_regression_test.go` | Persistence across close/reopen, PRAGMA handling |
| **Config** | `cmd/dualmem/config_test.go` | Tilde expansion, absolute path passthrough |
| **vs claude-mem** | `benchmarks/` | Head-to-head comparison scripts (Python) |

## Status

Extracted from [Club Mutant](https://github.com/goblincore/club-mutant) (production NPC memory) and extended for coding agent use.

## License

MIT
