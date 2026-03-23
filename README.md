# geoffreyengram

Cognitive memory engine for AI agents. Two systems in one Go library:

- **Engram** — character memory for NPCs, companions, chatbots. Cognitive sectors, natural decay, entity graphs, reflective synthesis, multimodal embedding.
- **DualMem** — agent memory for coding tools (Claude Code, Cursor, etc.). Dual-path routing, hierarchical compression, token-budget-aware retrieval.

Pure Go, SQLite (no CGO), single binary.

## Engram — Character Memory

For NPCs and companion AI that should remember players across sessions.

Memories are classified into 4 cognitive sectors and scored by a composite formula that weights similarity, salience, recency, and entity associations:

| Sector | Stores | Decay |
|--------|--------|-------|
| **Episodic** | Events, visits, encounters | Slow |
| **Semantic** | Facts, preferences, knowledge | Moderate |
| **Procedural** | Skills, routines, how-tos | Moderate |
| **Emotional** | Feelings, reactions, mood | Slow |

Reflective memories (patterns, meta-observations) are not classified from input — they're synthesized by the `Reflect()` method between conversations.

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

### Key features

- **Composite scoring**: `(similarity*0.6 + salience*0.2 + recency*0.1 + linkWeight*0.1) * sectorWeight`
- **Waypoint entity graph**: one-hop associative expansion — mentioning a song surfaces the person you heard it with
- **Exponential decay**: per-sector rates, configurable. Important memories persist, small talk fades
- **High-salience guarantee**: top-2 important memories always surface regardless of query similarity
- **Reflective synthesis**: between conversations, an LLM synthesizes patterns ("they always mention music when stressed")
- **Multimodal memory**: embed images and audio via Gemini Embedding 2 — NPCs can "hear" music or "see" photos
- **Embedding-based classification**: zero extra API calls per `Add()` — reuses the already-computed embedding (96% accuracy on 50-example benchmark)
- **Time simulation**: `SimulateTimePassing(userID, days)` artificially ages memories and runs decay — test what a character remembers after months pass
- **Session threading**: `SessionID` / `ParentID` for conversation chains
- **Per-character personality**: sector weights let an emotional character prioritize feelings over facts

### Multimodal

NPCs can form memories from images and audio via Gemini Embedding 2 — text, images, and audio share the same vector space:

```go
mem, _ := engram.Init(engram.Config{
    DBPath:            "./data/memory.db",
    EmbeddingProvider: engram.NewGeminiEmbedder2(apiKey, 768),
})

// NPC hears music playing
mem.AddWithOptions(engram.AddOptions{
    UserID:      "lily:player123",
    Media:       &engram.MediaContent{MimeType: "audio/mpeg", Data: trackBytes},
    Description: "Electronic music playing in the club",
})

// Text search finds the audio memory
results := mem.Search("what music was playing?", "lily:player123", 5, nil)
```

### Providers

All pluggable via interfaces:

| Provider | Type | Notes |
|----------|------|-------|
| `GeminiEmbedder` | Text embedding | gemini-embedding-001 |
| `GeminiEmbedder2` | Multimodal embedding | text/image/audio, gemini-embedding-2 |
| `OpenAIEmbedder` | Text embedding | text-embedding-3-small/large |
| `OllamaEmbedder` | Text embedding | Local, no API key |
| `EmbeddingClassifier` | Sector classification | Default. Zero API calls, 96% accuracy |
| `HeuristicClassifier` | Sector classification | Keyword-based, no API needed |
| `GeminiReflector` | Reflective synthesis | Explicit opt-in |

### MCP Server

```bash
go install github.com/goblincore/geoffreyengram/cmd/engram-mcp
ENGRAM_DB_PATH=./data/engram.db GEMINI_API_KEY=key engram-mcp
```

Tools: `remember`, `recall`, `reflect`, `get_session`, `inspect`. Supports multimodal via `media_base64` + `mime_type` fields.

## DualMem — Agent Memory

For coding agents and multi-agent systems where context window is the bottleneck.

Routes memories by importance: critical ones stay at full fidelity, everything else gets compressed into a hierarchical sketch.

Sectors are configurable via `SectorConfig`. Ships with two presets:

| Preset | Sectors | Detail Bias | Use Case |
|--------|---------|-------------|----------|
| `CodingSectors()` | decision, warning, map, continuity | decision, warning | Coding agents (default) |
| `NPCSectors()` | episodic, semantic, procedural, emotional | semantic, procedural | NPC/character memory |

```
Incoming Memory → Importance Scorer
                       │
                score ≥ 0.65           score < 0.65
                       │                     │
             ┌─────────▼───────┐   ┌─────────▼──────────┐
             │   Detail Path   │   │    Sketch Path      │
             │   (Top-K=100)   │   │   (Hierarchical)    │
             │                 │   │                     │
             │  Full text      │   │  L1: Episodes       │
             │  Full 768d vec  │   │  L2: Narrative Arcs  │
             │  Full metadata  │   │  L3: User Profile    │
             └─────────────────┘   └─────────────────────┘
```

**AssembleContext** is the key method — given a token budget, it assembles a structured context block in priority order: structural diff → code map (query-ranked) → checkpoints → profile → arcs → detail memories → episodes. Returns exactly what fits. **Task-aware**: auto-detects intent from the query (debug, continue, feature, explore) and adjusts memory type weights accordingly — debugging boosts warnings, resuming boosts continuity.

```go
import "github.com/goblincore/geoffreyengram/dualmem"

engine, _ := dualmem.New(dualmem.Config{
    SQLitePath:        "./data/dualmem.db",
    EmbeddingProvider: dualmem.NewGeminiEmbedder(apiKey, 768),
})
defer engine.Close()

engine.Add("chose SQLite for zero-setup", "Good call", "claude:my-project")

block, _ := engine.AssembleContext(ctx, "claude:my-project", "database choice", 500)
// block.Text — pre-formatted, fits within budget
// block.Sources — traces each fragment to detail/episode/arc/profile
```

### CLI

```bash
go install github.com/goblincore/geoffreyengram/cmd/dualmem@latest
export GEMINI_API_KEY=your-key

dualmem add --text "Auth uses JWT, not sessions" --type decision --salience 0.9
dualmem search "database" --limit 5
dualmem context "auth system" --budget 3000             # memories + code map + diff
dualmem context "fix the bug" --intent debug            # explicit intent override
dualmem checkpoint --task "auth refactor" --status in_progress --files "auth.go" --done "JWT" --remaining "refresh,logout"
dualmem checkpoint --list                               # view active checkpoints
dualmem map                                             # codebase structure map
dualmem diff                                            # changes since last session
dualmem promote --id <memory-id>                        # move sketch → detail
dualmem promote --all --type warning                    # batch re-evaluate all raw sketch entries
dualmem demote --id <memory-id>                         # move detail → sketch
dualmem gc                                              # clean stale/expired/superseded memories
dualmem gc --dry-run --verbose                          # preview what gc would do
dualmem profile
dualmem status
```

### Claude Code integration

Copy [`docs/example-claude-md.md`](docs/example-claude-md.md) into your `~/.claude/CLAUDE.md` to give Claude Code cross-session memory. It teaches the agent to automatically load context at session start, save decisions/warnings/checkpoints during work, and search memory before exploring the codebase.

### Memory types

Typed memories are prioritized in context assembly — warnings first, then decisions:

```bash
dualmem add --type warning --text "Don't touch rateLimiter cleanup()" --files "rate_limiter.go" --salience 0.9
dualmem add --type decision --text "Rejected Postgres, chose SQLite" --files "store_sqlite.go"
dualmem add --type continuity --text "Done: JWT. Remaining: refresh tokens" --files "auth.go,jwt.go"
```

### Task-aware context

`AssembleContext` auto-detects the task intent from the query string and adjusts how memory types are ranked. This means "fix the bug" prioritizes warnings and decisions, while "resume work" prioritizes continuity entries and file maps.

| Intent | Trigger keywords | Boosts | Suppresses |
|--------|-----------------|--------|------------|
| `debug` | fix, bug, error, crash, fail, debug | warnings (2x), decisions (1.5x) | continuity (0.5x) |
| `continue` | continue, resume, pick up, session context | continuity (2x), maps (1.5x) | general (0.8x) |
| `feature` | add, implement, create, build | decisions (1.5x), maps (1.5x) | continuity (0.8x) |
| `explore` | where is, how does, explain, architecture | maps (2x), general (1.2x) | continuity (0.5x) |

Override with `--intent`:

```bash
dualmem context "auth system" --intent debug    # force debug weighting
dualmem context "auth system" --intent continue  # force continue weighting
```

### Checkpoints

Structured session handoffs that replace free-form continuity prose. Checkpoints have typed fields for task, status, files, completed/remaining steps, and blockers. They auto-supersede — saving a new checkpoint for the same task replaces the old one.

```bash
# Save a checkpoint
dualmem checkpoint --task "auth refactor" --status in_progress \
  --files "auth.go,middleware.go" \
  --done "JWT validation,middleware setup" \
  --remaining "refresh tokens,logout endpoint" \
  --decision "JWT vs session tokens"

# List active checkpoints
dualmem checkpoint --list

# Checkpoint with blocker
dualmem checkpoint --task "auth refactor" --status blocked --blocked "waiting for API spec"
```

In `dualmem context` output, checkpoints render as compact structured blocks at the top:

```
[Checkpoint: auth refactor] status=in_progress
  Files: auth.go, middleware.go
  Pending decision: JWT vs session tokens
  Done: JWT validation; middleware setup
  Remaining: refresh tokens; logout endpoint
```

### Promote / demote

Memories can be moved between paths. Promote moves a sketch memory (raw entry or episode) to the Detail Path; demote does the reverse.

```bash
# Single promote — by memory ID (from search results)
dualmem promote --id abc123

# Promote with type/salience override
dualmem promote --id abc123 --type warning --salience 0.9

# Batch re-evaluate — re-scores all raw sketch entries with current scorer
# Useful for recovering old memories saved before the type-boost fix
dualmem promote --all --type warning

# Demote — move from detail to sketch
dualmem demote --id abc123
```

Also available via API: `POST /v1/memory/promote` with `{"user_id", "memory_id", "type", "salience"}`.

### Garbage collection

Over time, continuity entries accumulate — you add "remaining: auth, payments" and later "remaining: payments" but the old one is still there saying auth isn't done. `dualmem gc` cleans these up automatically.

```bash
# Preview what would be cleaned
dualmem gc --dry-run --verbose

# Run cleanup
dualmem gc
```

GC runs five cleanup strategies in order:

1. **Expire episodes** — delete episodes past the retention window (default 30 days)
2. **Expire arcs** — delete arcs past the retention window (default 90 days)
3. **Git-stale demotion** — demote detail memories whose associated files changed since last session (warnings exempt)
4. **Continuity supersession** — when multiple continuity entries cover the same topic (cosine similarity > 0.75), keep the newest, demote the rest
5. **Access-cold demotion** — demote detail memories not accessed in 30+ days with importance < 0.8 (warnings exempt)

Supersession also runs automatically on `add --type continuity` — adding a new continuity entry auto-demotes older similar ones, so stale status entries don't accumulate.

Additionally, `AssembleContext` applies access-recency weighting: memories not accessed in 60+ days are ranked lower within their priority tier. Warnings are always exempt from recency decay.

### Session context

`dualmem context` assembles a token-budget-aware context block for the start of each coding session. It combines four things:

**Code map** — multi-resolution structural summary of the codebase. Zoom-1 is a one-line system overview, zoom-2 is per-module with key types and entry points. Generated via Go AST parsing and TypeScript regex heuristics. Query-aware: module summaries are embedded at scan time, then ranked by cosine similarity to the query at render time. Aggressively filters noise directories (assets, icons, sass, .github, .changeset, etc.). Auto-regenerates when git HEAD changes.

**Structural diff** — git-based delta since the last session. Shows added/modified/deleted files with elapsed time and branch info. Handles branch switches, rebases, and force-pushes gracefully.

**Structured checkpoints** — typed session handoffs (task, status, files, steps, blockers) render as compact structured blocks before other memories. Auto-supersede by task name.

**Stale memory detection** — memories with `--files` associations are cross-referenced against the diff. If a referenced file was modified or deleted, the memory is tagged `[STALE?]` in context output.

```
$ dualmem context "session context" --budget 3000

[Changes Since Last Session]
Since last session (9h ago, branch main):
  Modified (4): cmd/dualmem/main.go, dualmem/dualmem.go, dualmem/store_sqlite.go, dualmem/types.go
  Added (4): dualmem/codemap.go, dualmem/codemap_test.go, dualmem/diff.go, dualmem/diff_test.go

[Codebase Map]
Go project. packages: ., dualmem. binaries: cmd/dualmem, cmd/engram-mcp, examples/comparison.

  ./ — Go package engram
    Types: struct EmbeddingClassifier, struct LLMClassifier, struct Store, ...
    Entry: NewEmbeddingClassifier(), NewLLMClassifier(), CompositeScore(), ...
  dualmem/ — Go package dualmem
    Types: struct StructuralDiff, struct SketchPath, struct SQLiteStore, ...
    Entry: ComputeStructuralDiff(), DetectStaleMemories(), NewSketchPath(), ...

[⚠ Warning — warning (importance: 0.83)]
Don't touch: rateLimiter cleanup() skips nil check intentionally

[Decision — decision (importance: 0.70)] [STALE? file changed since last session]
Chose SQLite for zero-setup
  Files: store_sqlite.go
```

The `map` and `diff` commands are also available standalone:

```bash
dualmem map    # print codebase structure without memories
dualmem diff   # print changes since last session
```

## Engram vs DualMem

Both systems share embedding providers and SQLite storage, but they're designed for different problems:

| | **Engram** | **DualMem** |
|---|---|---|
| **Designed for** | NPCs, companions, chatbots | Coding agents, multi-agent systems |
| **Search scoring** | Composite: similarity + salience + recency + entity links | Pure cosine similarity (Detail Path) |
| **Memory decay** | Exponential decay — old small talk fades naturally | Garbage collection (expiry, supersession, access-cold demotion) + hierarchical compression |
| **Entity graph** | Waypoints — mentioning a song surfaces the person you heard it with | Entities stored as metadata only, no associative expansion |
| **Reflective synthesis** | `Reflect()` creates meta-memories ("they always mention music when stressed") | Not available |
| **Multimodal** | Text, images, audio in same vector space (Gemini Embedding 2) | Text only |
| **Compression** | None — every memory at full fidelity | Episodes → arcs → profiles (hierarchical sketch) |
| **Context assembly** | Manual (you format search results) | `AssembleContext()` with token budget, code maps, stale detection |
| **Capacity** | Unbounded (decay handles relevance) | Detail: fixed top-K. Sketch: compressed hierarchy |
| **Best for** | Characters that should feel alive — remembering, forgetting, making associations | Agents that need structured context within tight token budgets |

## Project Structure

```
geoffreyengram/
├── engram.go              # Core engine (Init, Search, Add, Reflect, SimulateTimePassing)
├── types.go               # Modality, Sector, Memory, Config, scoring types
├── providers.go           # EmbeddingProvider (Embed, Dimension, ModelName), MultimodalEmbeddingProvider, SectorClassifier
├── store.go               # SQLite persistence, versioned migrations (v1-v3)
├── scoring.go             # Composite scoring, cosine similarity, decay
├── classify_embedding.go  # EmbeddingClassifier (default, 96% accuracy)
├── classify.go            # HeuristicClassifier (keyword fallback)
├── embed_gemini2.go       # GeminiEmbedder2 (multimodal: text/image/audio)
├── embed.go               # GeminiEmbedder (text-only, v1)
├── embed_openai.go        # OpenAIEmbedder
├── embed_ollama.go        # OllamaEmbedder
├── waypoints.go           # Entity graph, associative expansion
├── reflect.go             # ReflectionProvider, Reflect method
├── dualmem/               # Dual-path agent memory engine
│   ├── dualmem.go         # Engine (New, Add, DualSearch, AssembleContext, SaveCheckpoint, GarbageCollect, PromoteToDetail)
│   ├── types.go           # Config, SectorConfig, Intent, Checkpoint, GCOptions, Store interface
│   ├── detail.go          # Detail Path (importance scoring, capacity management)
│   ├── sketch.go          # Sketch Path (episodes, arcs, profiles)
│   ├── pipeline.go        # Background compression workers
│   ├── codemap.go         # Codebase scanner (Go AST + TS regex), query-aware ranking via embeddings
│   ├── diff.go            # Git-based structural diffs, stale memory detection
│   ├── classify_embedding.go  # EmbeddingClassifier (ported from engram)
│   ├── summarize_gemini.go    # GeminiSummarizer (episode/arc/profile compression)
│   ├── store_sqlite.go    # SQLite backend (migrations v1-v5)
│   └── project.go         # Random projection, cosine similarity
├── cmd/
│   ├── dualmem/           # CLI tool
│   └── engram-mcp/        # MCP server (5 tools)
└── examples/
    ├── chat/              # Local REPL with Ollama
    └── comparison/        # Memory mode comparison + LLM judge
```

## Status

Extracted from [Club Mutant](https://github.com/goblincore/club-mutant) (production NPC memory) and extended for coding agent use. 216 tests passing.

## License

MIT
