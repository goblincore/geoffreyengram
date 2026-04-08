# Architecture

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
  │ distill  │  │  │        │── File Annotations (grouped by file)       │
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

## Dual-path routing

Memories are scored at insert time (no LLM calls). High-importance memories (decisions, warnings) stay at full fidelity in the Detail Path. Everything else is compressed into a hierarchical sketch: episodes (30-day retention) → narrative arcs (90-day, 128d projection) → user profile (64d projection).

## File-centric context

The primary retrieval model is file-centric — `FileAnnotations` pulls warnings, decisions, traces, knowledge docs, and checkpoints indexed by file path. Context output groups related memories under their files instead of flat type-sorted lists. File paths are extracted from checkpoints and recent memories, creating a natural "what do I need to know about the files I'm working on?" view.

## Smart codemap toggle

The structural codemap is only included on cold starts (no active checkpoints, explore/default intent). For task-specific sessions, the ~400 tokens are reallocated to file-centric context and memories.

## Context assembly

Given a query and token budget, `AssembleContext` packs context in priority order: structural diff → codemap (cold start only) → checkpoints → file annotations → knowledge docs → profile → arcs → detail memories → episodes. Task-aware: auto-detects intent (debug/continue/feature/explore) and adjusts memory type weights. Supports progressive disclosure (`--index` mode) — outputs a compact index of available items with token costs, then the agent fetches only what it needs via `dualmem show`.

## Project structure

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
