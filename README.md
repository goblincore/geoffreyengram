# geoffreyengram

Cognitive memory engine for AI agents. NPCs, coding assistants, chatbots, companions — any AI that should remember context across sessions, notice patterns, and think between conversations.

Two memory systems in one library:

- **Engram** — flat cognitive memory with 5 sectors, composite scoring, entity graphs, and reflective synthesis. Simple, effective for small-scale use (< 500 memories per user).
- **DualMem** — dual-path architecture that routes memories by importance to a high-fidelity Detail Path or a compressed Sketch Path. Designed for scale: token-budget-aware retrieval, hierarchical compression, and multi-agent namespace isolation.

## The Problem

AI memory is either too simple or too generic.

**For NPC/character AI:** Replika forgets things from the same day. Character.ai has 400-character manual memory tags. Kindroid makes users curate their own "Lorebooks." Every conversation feels like a first meeting.

**For coding agents:** Claude Code, Cursor, and similar agents lose all context between sessions. Architectural decisions, debugging insights, user preferences, file navigation paths — all rediscovered every conversation. The built-in memory systems (MEMORY.md files) don't scale, can't search semantically, and waste context window on irrelevant memories.

**For multi-agent systems:** Each agent needs its own memory namespace, but shared world state needs to be accessible to all. No existing system handles both isolation and sharing cleanly.

geoffreyengram solves all three with cognitive sectors, importance-scored routing, and namespace-based isolation.

## Two Systems

### Engram: Flat Cognitive Memory

Best for: NPC characters, small-scale chatbots, single-agent use under ~500 memories.

Five cognitive sectors with natural decay, associative recall via entity graphs, and reflective synthesis:

| Sector | What it stores | Decay rate | Example |
|--------|---------------|------------|---------|
| **Episodic** | Events, experiences | Slow | "Player visited Tokyo last month" |
| **Semantic** | Facts, knowledge | Warm | "Player's name is Alex, likes jazz" |
| **Procedural** | Skills, routines | Warm | "Player always orders a Nebula Fizz" |
| **Emotional** | Sentiments, feelings | Slow | "Player seemed sad last conversation" |
| **Reflective** | Meta-observations | Cold | "Player always mentions music when stressed" |

Retrieval uses composite scoring: `(similarity×0.6 + salience×0.2 + recency×0.1 + linkWeight×0.1) × sectorWeight`. Waypoint entity graph provides one-hop associative expansion. Background workers handle decay and reflective synthesis.

```go
import engram "github.com/goblincore/geoffreyengram"

mem, _ := engram.Init(engram.Config{
    DBPath:       "./data/memory.db",
    GeminiAPIKey: os.Getenv("GEMINI_API_KEY"),
})
defer mem.Close()

mem.Add("I just got back from Tokyo!", "That's amazing!", "lily:player123")
results := mem.Search("japan trip", "lily:player123", 5, nil)
```

### DualMem: Dual-Path Agent Memory

Best for: coding agents, multi-agent workflows, high-memory-pressure scenarios, any use case over ~500 memories.

Inspired by [RandNLA Attention](https://arxiv.org/abs/2410.13720)'s dual-path architecture — keeps critical memories at full fidelity while compressing everything else into a hierarchical sketch:

```
Incoming Memory → Importance Scorer → LLM Sector Classification (Gemini Flash Lite)
                         │
                  score ≥ 0.65           score < 0.65
                         │                     │
               ┌─────────▼───────┐   ┌─────────▼──────────┐
               │   Detail Path   │   │    Sketch Path      │
               │   (Top-K=100)   │   │   (Hierarchical)    │
               │                 │   │                     │
               │  Full text      │   │  L1: Episodes       │
               │  Full 768d      │   │      (~200 tokens)   │
               │  Full metadata  │   │  L2: Narrative Arcs  │
               └─────────────────┘   │      (128d, ~100t)   │
                                     │  L3: User Profile    │
                                     │      (64d, ~50t)     │
                                     └─────────────────────┘
```

**Detail Path** — fixed-capacity store of uncompressed, full-fidelity memories. Facts, decisions, explicit preferences, critical constraints. Searched at full 768d resolution with high-salience guarantee (top-2 important memories always surface).

**Sketch Path** — hierarchical compression via background workers:
- **Episodes** (~200 tokens) — LLM-summarized conversation blocks, 768d embeddings
- **Narrative Arcs** (~100 tokens) — clustered episodes via agglomerative clustering (entity Jaccard + temporal proximity + embedding similarity), projected to 128d via Johnson-Lindenstrauss random projection
- **User Profile** (~50 tokens) — continuously updated structured summary, projected to 64d

**Importance Scorer** — routes memories without LLM calls (preserving fire-and-forget latency):
```
importance = (sectorBonus × 0.3) + (salience × 0.3) + (specificity × 0.2) + (novelty × 0.2)
```
Semantic/procedural sectors score high (facts need precision). Episodic/emotional score low (they compress well). Specificity detected via regex (proper nouns, numbers, dates, quotes). Novelty drops as cosine similarity to existing Detail memories increases.

**Sector Classification** — Gemini 2.5 Flash Lite classifies memories into the correct cognitive sector in ~100-200ms. Falls back to heuristic keyword matching if the LLM is unavailable.

**AssembleContext** — the key differentiator. Given a token budget, assembles a structured context block in priority order: profile sketch → narrative arcs → detail memories → recent episodes. Returns exactly what fits, with source attribution.

```go
import "github.com/goblincore/geoffreyengram/dualmem"

engine, _ := dualmem.New(dualmem.Config{
    SQLitePath:        "./data/dualmem.db",
    EmbeddingProvider: dualmem.NewGeminiEmbedder(apiKey, 768),
    Classifier:        dualmem.NewGeminiClassifier(apiKey),
})
defer engine.Close()

// Drop-in compatible with Engram
engine.Add("I love dark roast coffee", "Great taste!", "lily:player123")
results := engine.Search("coffee", "lily:player123", 5, nil)

// Token-budget-aware context assembly
block, _ := engine.AssembleContext(ctx, "lily:player123", "greet the player", 500)
// block.Text — pre-formatted for LLM prompt
// block.TokenCount — guaranteed <= budget
// block.Sources — traces each fragment to detail/episode/arc/profile
```

### Namespace-Based Multi-Agent Memory

`userID` is flexible — use it for NPC isolation, agent memory, or shared world state:

```go
engine.Add(msg, resp, "npc:lily:player123")      // Lily's memories of this player
engine.Add(msg, resp, "npc:watcher:player123")    // Watcher's different perspective
engine.Add(msg, resp, "shared:world")             // World events all NPCs share
engine.Add(msg, resp, "claude:club-mutant")       // Claude agent's project memory
```

## CLI Tool

DualMem ships as a CLI binary for use by coding agents (Claude Code, Cursor, etc.) and humans.

```bash
go install github.com/goblincore/geoffreyengram/cmd/dualmem@latest
export GEMINI_API_KEY=your-key

# Add a memory (namespace auto-detected from cwd)
dualmem add --text "Auth middleware rewrite is compliance-driven, not tech debt" --salience 0.9

# Assemble context with token budget (key command for session start)
dualmem context "auth system" --budget 3000

# Search
dualmem search "user preferences" --limit 5

# Profile, status
dualmem profile
dualmem status
```

Sector classification is automatic via Gemini Flash Lite — no `--sector` flag needed. The CLI defaults to 0.7 salience (intentional saves are important by definition).

Configure via `~/.config/dualmem/config.yaml` or `.dualmem.yaml` in project root.

## Agent Integration (Claude Code, Cursor, etc.)

DualMem is designed to be used by AI coding agents. The key insight: **the system is only as useful as the instructions that tell the agent when and what to save.** Without good triggers, the agent won't build useful memory.

### Setup: `~/.claude/CLAUDE.md`

Add instructions to your global `~/.claude/CLAUDE.md` (or project-level `.claude/CLAUDE.md`) that tell the agent to:

1. **Load context at session start** — run `dualmem context "session context" --budget 3000` and internalize it silently
2. **Save typed memories during the session** — using `--type` tags so memories are structured and prioritized
3. **Search before grepping** — check if a previous session already mapped the relevant files

### Memory Types — `--type` flag

DualMem supports typed memories that are prioritized in context assembly. Warnings appear first, then decisions, then general memories:

```bash
# ⚠ Warnings — code that should NOT be changed (surfaced first)
dualmem add --type warning --text "Don't touch rateLimiter cleanup() — skips nil check for hot path" \
  --files "rate_limiter.go" --salience 0.9

# Decisions — settled choices with rationale (surfaced second)
dualmem add --type decision --text "Rejected Postgres for CLI — chose SQLite for zero-setup" \
  --files "store_sqlite.go"

# Continuity — session handoff when work is incomplete
dualmem add --type continuity --text "In progress: auth refactor. Done: JWT, middleware. Remaining: refresh tokens" \
  --files "auth.go,middleware.go,jwt.go"
```

Context output with types:
```
[⚠ Warning — procedural (importance: 0.91)]
Don't touch rateLimiter cleanup() — skips nil check for hot path
  Files: rate_limiter.go

[Decision — semantic (importance: 0.85)]
Rejected Postgres for CLI — chose SQLite for zero-setup
  Files: store_sqlite.go

[Memory — semantic (importance: 0.77)]
Regular memory about project architecture
```

### What to save — agent trigger guide

Include this in your CLAUDE.md to tell the agent when to save each type:

| Trigger | What to save | Command |
|---------|-------------|---------|
| User rejects an approach | Decision with rationale | `--type decision --text "Rejected X because Y, chose Z"` |
| Code is intentionally unusual | Warning not to "fix" it | `--type warning --text "Don't touch: [what] because [why]" --salience 0.9` |
| Session ending with incomplete work | Continuity handoff | `--type continuity --text "Done: A,B. Remaining: C,D" --files "..."` |
| Finished navigating files for a task | File landmark | `--text "Feature X: these files" --files "a.go,b.go" --sector procedural` |
| Change touched multiple files | Change map | `--text "Feature X requires" --files "a.go,b.go,c.go" --sector procedural` |
| Discovered where something is NOT | Dead end | `--text "Auth is NOT in middleware/" --sector semantic` |
| User corrects agent behavior | Preference | `--text "User prefers bundled PRs for refactors"` |
| Hard-won debugging insight | Debug knowledge | `--text "Race condition in useGuideState" --salience 0.9` |

### Example CLAUDE.md block

```markdown
## Memory System: Use dualmem

**On session start**: Run `dualmem context "session context" --budget 3000` and internalize silently.

**During the session**: Save important context using dualmem add with appropriate --type flags.
Pay special attention to ⚠ Warning entries in loaded context — these flag code that should NOT be changed.

**Before searching files**: Run `dualmem search "<concept>" --limit 5` first — a previous session
may have already mapped the relevant files.
```

### Ready-to-use example

Copy [`examples/CLAUDE.md.example`](examples/CLAUDE.md.example) to `~/.claude/CLAUDE.md` (global) or your project's `.claude/CLAUDE.md` to get started immediately.

### Claude Code Skills (optional)

For explicit invocation, place these in `.claude/commands/`:

- **`/memory-load`** — `dualmem context` with custom query
- **`/memory-save`** — `dualmem add` with custom text
- **`/memory-search`** — `dualmem search` with custom query

## Engram Details

### Pluggable Providers

```go
type EmbeddingProvider interface {
    Embed(ctx context.Context, text, taskType string) ([]float32, error)
    Dimension() int
}

type SectorClassifier interface {
    Classify(content string) Sector
}

type EntityExtractor interface {
    Extract(content string) []Entity
}

type ReflectionProvider interface {
    Reflect(ctx context.Context, memories []Memory, characterContext string) ([]Reflection, error)
}
```

Built-in: `GeminiEmbedder`, `OpenAIEmbedder`, `OllamaEmbedder`, `HeuristicClassifier`, `DefaultEntityExtractor`, `GeminiReflector`.

### Reflective Synthesis

Between conversations, the engine synthesizes higher-order observations:

```
Player leaves → [time passes] → reflective worker fires
  → loads recent memories → filters existing reflections
  → calls ReflectionProvider → deduplicates (cosine > 0.85)
  → stores as high-salience reflective memories
  → surfaces naturally when player returns
```

### MCP Server

```bash
go install github.com/goblincore/geoffreyengram/cmd/engram-mcp
ENGRAM_DB_PATH=./data/engram.db GEMINI_API_KEY=key engram-mcp
```

Tools: `remember`, `recall`, `reflect`, `get_session`, `inspect`

### HTTP API (DualMem)

```
POST /v1/memory/add       — add a memory
POST /v1/memory/search    — dual-path search
POST /v1/memory/context   — AssembleContext (token-budget-aware)
GET  /v1/memory/profile/:userID — get user profile sketch
POST /v1/memory/promote   — pin a memory to Detail Path
POST /v1/memory/demote    — demote a memory to Sketch Path
```

## Local Chat Example

Test Engram in a live conversation using Ollama. No API keys needed — fully local.

```bash
ollama pull nomic-embed-text
ollama pull hadad/LFM2.5-1.2B:Q4_K_M

go run ./examples/chat/ --chat-model hadad/LFM2.5-1.2B:Q4_K_M
```

Type `/memories` during a conversation to see what the character remembers.

## Comparison Example

Run a scripted multi-session conversation through 3 memory modes — **stateless**, **flat RAG**, and **full engram** — then LLM-as-judge scores the results.

```bash
GEMINI_API_KEY=... go run ./examples/comparison/ --scenario lily
```

| Scenario | Character | What it tests |
|----------|-----------|---------------|
| `lily` | Bartender | Emotional + episodic — relationship building |
| `sifu` | Wing Chun instructor | Procedural — skill sequences |
| `nyx` | Archivist | Semantic — cross-referencing facts |
| `reeves` | Therapist | All 5 sectors |

## Project Structure

```
geoffreyengram/
├── engram.go              # Engram core (Init, Search, Add, Reflect, Close)
├── types.go               # Sector, Memory, Entity, Config, scoring types
├── providers.go           # EmbeddingProvider, SectorClassifier, EntityExtractor
├── store.go               # SQLite persistence, versioned migrations
├── scoring.go             # Composite scoring, cosine similarity, decay
├── classify.go            # HeuristicClassifier
├── classify_llm.go        # LLMClassifier (async Gemini reclassification)
├── embed.go               # GeminiEmbedder
├── embed_openai.go        # OpenAIEmbedder
├── embed_ollama.go        # OllamaEmbedder
├── waypoints.go           # Entity graph, associative expansion
├── reflect.go             # ReflectionProvider, deduplication
├── reflect_gemini.go      # GeminiReflector
├── decay_worker.go        # Background decay
├── reflect_worker.go      # Background reflection
├── dualmem/               # Dual-path agent memory engine
│   ├── dualmem.go         # Engine (New, Add, Search, DualSearch, AssembleContext)
│   ├── types.go           # Types, Config, Store interface
│   ├── scorer.go          # Importance scoring formula
│   ├── project.go         # JL random projection (768→128, 768→64)
│   ├── detail.go          # Detail Path (Top-K, capacity, high-salience guarantee)
│   ├── sketch.go          # Sketch Path (episodes, arcs, profiles)
│   ├── pipeline.go        # Compression workers + agglomerative clustering
│   ├── store_sqlite.go    # SQLite backend
│   ├── embed_gemini.go    # Gemini embedding provider
│   ├── classify_llm.go    # Gemini Flash Lite sector classifier
│   └── api.go             # HTTP API handlers
├── cmd/
│   ├── dualmem/           # CLI tool (add, search, context, profile, status)
│   └── engram-mcp/        # MCP stdio server (5 tools)
└── examples/
    ├── chat/              # Local REPL chat with Ollama
    └── comparison/        # Multi-scenario memory comparison test
```

## Status

Extracted from a production NPC memory system ([Club Mutant](https://github.com/goblincore/club-mutant)) and extended for coding agent use.

### What works now
- **Engram**: 5-sector cognitive model, composite scoring, waypoint entity graph, exponential decay, reflective synthesis, conversation threading, MCP server, async LLM reclassification
- **DualMem**: importance-scored dual-path routing, Gemini Flash Lite classification, hierarchical compression pipeline, JL random projection, token-budget-aware context assembly, CLI tool, HTTP API, Claude Code integration
- **Providers**: Gemini, OpenAI, Ollama (embedding); Heuristic + Gemini (classification)
- **97 tests** across all subsystems

### Roadmap
- [x] LLM-powered sector classification
- [x] Comparison examples (4 scenarios)
- [x] DualMem dual-path engine
- [x] CLI tool + Claude Code integration
- [x] Gemini Flash Lite sector classifier
- [ ] DualMem: Postgres/pgvector store backend
- [ ] DualMem: MCP server (typed tool schemas, resource exposure)
- [ ] DualMem: Summarizer provider (Gemini Flash for episode/arc/profile compression)
- [ ] DualMem: Data migration script (Engram SQLite → DualMem)
- [ ] Local ONNX inference (`OnnxEmbedder`, `DistilBERTClassifier`)
- [ ] Benchmark suite

## Why Go

The LLM call (100-2000ms) dwarfs any IPC overhead (1ms localhost). Memory should be a sidecar service — persists across sessions, survives crashes, serves multiple clients. Pure Go SQLite (no CGO), single binary, goroutines for background workers.

## License

MIT
