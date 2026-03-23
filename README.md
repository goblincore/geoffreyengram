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

**AssembleContext** is the key method — given a token budget, it assembles a structured context block in priority order: structural diff → code map (query-ranked) → profile → arcs → detail memories → episodes. Returns exactly what fits.

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
dualmem context "auth system" --budget 3000   # memories + code map + diff
dualmem map                                   # codebase structure map
dualmem diff                                  # changes since last session
dualmem profile
dualmem status
```

### Memory types

Typed memories are prioritized in context assembly — warnings first, then decisions:

```bash
dualmem add --type warning --text "Don't touch rateLimiter cleanup()" --files "rate_limiter.go" --salience 0.9
dualmem add --type decision --text "Rejected Postgres, chose SQLite" --files "store_sqlite.go"
dualmem add --type continuity --text "Done: JWT. Remaining: refresh tokens" --files "auth.go,jwt.go"
```

### Session context

`dualmem context` assembles a token-budget-aware context block for the start of each coding session. It combines three things:

**Code map** — multi-resolution structural summary of the codebase. Zoom-1 is a one-line system overview, zoom-2 is per-module with key types and entry points. Generated via Go AST parsing and TypeScript regex heuristics. Query-aware: module summaries are embedded at scan time, then ranked by cosine similarity to the query at render time. Aggressively filters noise directories (assets, icons, sass, .github, .changeset, etc.). Auto-regenerates when git HEAD changes.

**Structural diff** — git-based delta since the last session. Shows added/modified/deleted files with elapsed time and branch info. Handles branch switches, rebases, and force-pushes gracefully.

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
│   ├── dualmem.go         # Engine (New, Add, DualSearch, AssembleContext)
│   ├── types.go           # Config, SectorConfig, Store interface, DetailMemory, Episode, Arc, Profile
│   ├── detail.go          # Detail Path (importance scoring, capacity management)
│   ├── sketch.go          # Sketch Path (episodes, arcs, profiles)
│   ├── pipeline.go        # Background compression workers
│   ├── codemap.go         # Codebase scanner (Go AST + TS regex), query-aware ranking via embeddings
│   ├── diff.go            # Git-based structural diffs, stale memory detection
│   ├── classify_embedding.go  # EmbeddingClassifier (ported from engram)
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

Extracted from [Club Mutant](https://github.com/goblincore/club-mutant) (production NPC memory) and extended for coding agent use. 154 tests passing.

## License

MIT
