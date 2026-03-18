# geoffreyengram

Cognitive memory engine for AI characters. NPCs, companions, chatbots, agents — any AI that should remember you, notice patterns, and think between conversations.

Instead of flat key-value memory or raw conversation logs, geoffreyengram organizes memories into **cognitive sectors** with natural decay, associative recall, and reflective synthesis.

Inspired by [CaviraOSS/OpenMemory](https://github.com/CaviraOSS/OpenMemory)'s cognitive model (which targets coding assistants), reapplied to AI characters — where it arguably matters more.

## The Problem

Every AI companion app has terrible memory. Replika forgets things from the same day. Character.ai has 400-character manual memory tags. Kindroid makes users curate their own "Lorebooks." The universal complaint: *"Every conversation feels like they're meeting me for the first time."*

Existing memory infrastructure (Mem0, Zep) is generic — built for coding assistants and chatbots. Nobody is building cognitive memory specifically for character AI.

## How It Works

### Five Cognitive Sectors

| Sector | What it stores | Decay rate | Example |
|--------|---------------|------------|---------|
| **Episodic** | Events, experiences | Slow | "Player visited Tokyo last month" |
| **Semantic** | Facts, knowledge | Warm | "Player's name is Alex, likes jazz" |
| **Procedural** | Skills, routines | Warm | "Player always orders a Nebula Fizz" |
| **Emotional** | Sentiments, feelings | Slow | "Player seemed sad last conversation" |
| **Reflective** | Meta-observations | Cold | "Player always mentions music when stressed" |

Each memory is automatically classified into a sector. Sectors have different decay rates — facts persist while small talk fades. Characters can weight sectors differently: a warm bartender values emotional memories; a scholar values semantic ones.

### Composite Scoring

Retrieval ranks memories by a configurable blended score:

```
score = (w1 x similarity + w2 x salience + w3 x recency + w4 x linkWeight) x sectorWeight
```

Default weights: similarity=0.6, salience=0.2, recency=0.1, linkWeight=0.1. All configurable via `ScoringWeights`.

- **Similarity** — cosine similarity between query and memory embeddings
- **Salience** — how important this memory is (boosted when accessed, decays over time)
- **Recency** — exponential decay from last access
- **Link weight** — bonus from waypoint graph connections (associative recall)
- **Sector weight** — per-character multiplier

### Waypoint Graph

Memories are linked through shared entities (people, places, topics). When you recall "Japan," the graph also surfaces memories about "jazz" and "their dog" — because the player mentioned jazz bars in Tokyo and missing the dog while traveling. One-hop associative expansion.

### Natural Decay

Important memories persist. Trivial ones fade. High-salience memories decay slowly; low-salience memories expire naturally. A background worker runs periodically (default: every 12 hours). Per-sector decay rates are configurable.

### Reflective Synthesis

The difference between "NPC with a database" and "character that thinks." Between conversations, the engine synthesizes higher-order observations:

```
Player leaves → [time passes] → reflective worker fires
  → loads recent memories → filters out existing reflections
  → calls ReflectionProvider → finds patterns
  → deduplicates against existing reflections (cosine > 0.85)
  → stores as high-salience reflective memories
  → surfaces naturally when player returns
```

Reflection requires an explicit `ReflectionProvider` — it's opt-in because it involves LLM generation calls. A built-in `GeminiReflector` is provided, or implement the interface for any LLM.

### Conversation Threading

Memories are linked into conversation chains via `SessionID` and `ParentID`. Retrieve an entire conversation session, find the last session for a user, or filter searches to specific time windows.

## Quick Start

### As a Go Library

```go
import engram "github.com/goblincore/geoffreyengram"

// Initialize with Gemini (convenience)
mem, err := engram.Init(engram.Config{
    DBPath:       "./data/memory.db",
    GeminiAPIKey: os.Getenv("GEMINI_API_KEY"),
})
defer mem.Close()

// Or bring your own providers
mem, err := engram.Init(engram.Config{
    DBPath:            "./data/memory.db",
    EmbeddingProvider: myOllamaEmbedder,
    Classifier:        myCustomClassifier,
    EntityExtractor:   myGameItemExtractor,
})

// Store a conversation (simple)
mem.Add("I just got back from Tokyo!", "That's amazing! How was it?", "character:player123")

// Store with full options (session threading, salience, entities)
memID, err := mem.AddWithOptions(engram.AddOptions{
    UserID:           "character:player123",
    UserMessage:      "I just got back from Tokyo!",
    AssistantMessage: "That's amazing! How was it?",
    SessionID:        "sess-abc123",
    ParentID:         previousMemID,
    Salience:         0.8,
})

// Search with per-character personality weights
weights := engram.DefaultSectorWeights()
weights[engram.SectorEmotional] = 1.5  // this character values emotional memories
results := mem.Search("tell me about japan", "character:player123", 5, weights)

for _, r := range results {
    fmt.Printf("[%s] %s (score=%.2f)\n", r.Sector, r.Summary, r.CompositeScore)
}

// Search with temporal filters
results = mem.SearchWithOptions(engram.SearchOptions{
    Query:   "japan trip",
    UserID:  "character:player123",
    Limit:   5,
    Sectors: []engram.Sector{engram.SectorEpisodic, engram.SectorEmotional},
    After:   &lastWeek,
})

// Retrieve a conversation session
session, _ := mem.GetSession("sess-abc123")
lastSession, _ := mem.GetLastSession("character:player123")

// Trigger reflective synthesis (requires ReflectionProvider)
reflections, err := mem.Reflect(ctx, engram.ReflectOptions{
    UserID:           "character:player123",
    CharacterContext: "You're a bartender who notices patterns in your regulars",
    MinMemories:      5,
})
```

### Pluggable Providers

```go
// Embedding — bring your own vector provider
type EmbeddingProvider interface {
    Embed(ctx context.Context, text, taskType string) ([]float32, error)
    Dimension() int
}

// Classification — sector routing
type SectorClassifier interface {
    Classify(content string) Sector
}

// Entity extraction — for waypoint graph
type EntityExtractor interface {
    Extract(content string) []Entity
}

// Reflective synthesis — opt-in LLM reflection
type ReflectionProvider interface {
    Reflect(ctx context.Context, memories []Memory, characterContext string) ([]Reflection, error)
}
```

Built-in implementations:
- **Embedding:** `GeminiEmbedder`, `OpenAIEmbedder`, `OllamaEmbedder`
- **Classification:** `HeuristicClassifier`
- **Entity extraction:** `DefaultEntityExtractor`
- **Reflection:** `GeminiReflector`

```go
// OpenAI embeddings
embedder := engram.NewOpenAIEmbedder(os.Getenv("OPENAI_API_KEY"),
    engram.WithOpenAIModel("text-embedding-3-small"),
    engram.WithOpenAIDimension(768),
)

// Local Ollama embeddings (no API key needed)
embedder := engram.NewOllamaEmbedder("nomic-embed-text", 768)

// Azure OpenAI or compatible APIs
embedder := engram.NewOpenAIEmbedder(apiKey,
    engram.WithOpenAIBaseURL("https://your-instance.openai.azure.com"),
)
```

### As an MCP Server

```bash
go install github.com/goblincore/geoffreyengram/cmd/engram-mcp

# Configure via environment
export ENGRAM_DB_PATH=./data/engram.db
export GEMINI_API_KEY=your-key

engram-mcp  # starts MCP stdio server
```

Tools: `remember`, `recall`, `reflect`, `get_session`, `inspect`

## Architecture

```
Your Game Server / Chatbot / AI Agent
           |
           v
   geoffreyengram (library or MCP server)
   |            |             |              |
   SQLite    Embeddings    Classification  Reflection
  (local)   (pluggable)   (pluggable)     (opt-in)
```

**Local-first.** SQLite database, single binary, no cloud dependency. All providers are pluggable — Gemini included as the default, but swap in OpenAI, Ollama, or your own.

**Two integration patterns:**

- **Server-driven (Pattern A):** Your code calls `Search()` and `Add()` explicitly. You control when memory is read/written. Simple, predictable, cheaper.
- **Agent-driven (Pattern B):** The LLM has `recall`/`remember` as MCP tools and decides when to use them. The character has agency over its own memory. More autonomous, more emergent, more LLM calls.

## Local Chat Example

Test geoffreyengram in a live conversation using Ollama. No API keys needed — fully local.

```bash
# Pull models
ollama pull nomic-embed-text             # embeddings
ollama pull hadad/LFM2.5-1.2B:Q4_K_M    # chat (or llama3, mistral, etc.)

# Start chatting
go run ./examples/chat/ --chat-model hadad/LFM2.5-1.2B:Q4_K_M

# Custom character + model
go run ./examples/chat/ --chat-model mistral --character "Sifu Chen" --prompt ./sifu.txt
```

The example uses **Pattern A (server-driven)**: your code calls `Search()` before each LLM call to inject memories into the prompt, and `Add()` after to store the exchange. The LLM just sees memories as context — no tool-use capability required, so even small models work well.

Type `/memories` during a conversation to see what the character remembers (sectors, salience, decay scores).

## Comparison Example

Does cognitive memory actually produce better characters? Run the comparison test to find out.

The example runs a scripted multi-session conversation through 3 memory modes — **stateless** (no memory), **flat RAG** (embed + cosine top-k), and **full engram** (sectors, decay, waypoints, reflection) — then uses LLM-as-judge to score the results.

```bash
# List available scenarios
GEMINI_API_KEY=... go run ./examples/comparison/ --list

# Run a specific scenario
GEMINI_API_KEY=... go run ./examples/comparison/ --scenario lily

# Interactive selection
GEMINI_API_KEY=... go run ./examples/comparison/
```

### Scenarios

Each scenario is designed to stress-test a different aspect of cognitive memory:

| Scenario | Character | What it tests |
|----------|-----------|---------------|
| `lily` | Bartender at Club Mutant | **Emotional + episodic** — relationship building, warmth after a time gap |
| `sifu` | Wing Chun instructor | **Procedural** — skill sequences, remembering student-specific struggles |
| `nyx` | Archivist in a fantasy library | **Semantic** — cross-referencing facts, entity linking across visits |
| `reeves` | Therapist | **All 5 sectors** — emotional patterns, facts, techniques, reflective synthesis |

Each scenario follows the same structure: 3 history sessions building up memories, a time gap (where engram mode runs reflective synthesis), and a probe session where the character's greeting reveals what it actually remembers.

Results are printed to the terminal and written to `examples/comparison/results_<name>.md` for easy human comparison — each mode's full conversation shown end-to-end.

## DualMem: Dual-Path Agent Memory

As memory scales (500+ memories per user, multiple NPCs, multi-agent scenarios), flat vector search faces compounding problems: recall quality degrades, retrieval cost scales linearly, and LLM context windows get wasted on low-value memories.

**DualMem** solves this with a dual-path architecture inspired by [RandNLA Attention](https://arxiv.org/abs/2410.13720):

```
Incoming Memory → Importance Scorer (no LLM call)
                     │
              score ≥ 0.65        score < 0.65
                     │                  │
            ┌────────▼──────┐  ┌────────▼─────────┐
            │  Detail Path  │  │   Sketch Path     │
            │  (Top-K=100)  │  │  (Hierarchical)   │
            │               │  │                   │
            │  Full text    │  │  L1: Episodes     │
            │  Full 768d    │  │      (~200 tokens) │
            │  Full meta    │  │  L2: Arcs         │
            └───────────────┘  │      (128d, ~100t) │
                               │  L3: Profile      │
                               │      (64d, ~50t)   │
                               └───────────────────┘
```

- **Detail Path**: Fixed-capacity store of uncompressed, full-fidelity memories per user. Names, explicit preferences, critical events — anything requiring exact recall.
- **Sketch Path**: Hierarchical compression using random projection (Johnson-Lindenstrauss lemma). Episodes summarize conversation blocks, arcs cluster related episodes into themes, and a continuously-updated profile sketch captures the user's essence in ~50 tokens.

### Key Features

- **No LLM on the Add path** — importance scoring uses heuristics (sector type, salience, entity specificity, novelty via cosine scan). Fire-and-forget latency preserved.
- **Token-budget-aware retrieval** — `AssembleContext()` fills a token budget with profile → arcs → detail memories → episodes, in priority order.
- **Drop-in compatible** with Engram — `Add()` and `Search()` match the same signatures. Swap one line.
- **Random projection** — 768d → 128d (arcs) and 768d → 64d (profiles) via sparse {-1, +1} matrices with JL distance preservation guarantees.
- **Background compression pipeline** — episodes, arcs, and profile updates run as configurable-interval workers (same pattern as Engram's decay/reflection workers).

### Usage

```go
import "github.com/goblincore/geoffreyengram/dualmem"

engine, err := dualmem.New(dualmem.Config{
    SQLitePath:        "./data/dualmem.db",
    EmbeddingProvider: myEmbedder,       // required
    Classifier:        myClassifier,     // optional
    EntityExtractor:   myExtractor,      // optional
    Summarizer:        myGeminiSummarizer, // optional (enables compression pipeline)
    MaxDetailPerUser:  100,
    ImportanceTheta:   0.65,
})
defer engine.Close()

// Drop-in compatible with Engram
engine.Add("I love dark roast coffee", "Great taste!", "lily:player123")
results := engine.Search("coffee", "lily:player123", 5, nil)

// Dual-path search (both paths)
result, _ := engine.DualSearch(ctx, "lily:player123", "what does this player like?",
    dualmem.SearchOpts{Limit: 5, IncludeSketch: true})
// result.DetailMemories, result.Episodes, result.Arcs, result.Profile

// Token-budget-aware context assembly (the key differentiator)
block, _ := engine.AssembleContext(ctx, "lily:player123", "greet the player", 500)
// block.Text is pre-formatted for LLM prompt injection
// block.TokenCount <= 500
// block.Sources traces each fragment to its origin
```

### CLI Tool

```bash
# Install
go install github.com/goblincore/geoffreyengram/cmd/dualmem@latest

# Add a memory (namespace auto-detected from cwd)
dualmem add --text "Auth middleware rewrite is compliance-driven, not tech debt" --salience 0.8

# Add with explicit namespace and sector
dualmem add --ns "claude:club-mutant" --text "Player prefers terse responses" --sector semantic

# Assemble context with token budget (key command for session start)
dualmem context "auth system" --budget 3000

# Search
dualmem search "user preferences" --limit 5

# Get profile sketch
dualmem profile

# Status
dualmem status
```

Configure via `~/.config/dualmem/config.yaml` or `.dualmem.yaml` in project root. Requires `GEMINI_API_KEY` or `GOOGLE_API_KEY` environment variable.

### Claude Code Skills

Place these in your project's `.claude/commands/` to use DualMem from Claude Code:

- **`/memory-load`** — Load project memory context at session start (`dualmem context`)
- **`/memory-save`** — Save important decisions/preferences (`dualmem add`)
- **`/memory-search`** — Search project memory (`dualmem search`)

### HTTP API

```
POST /v1/memory/add       — add a memory
POST /v1/memory/search    — dual-path search
POST /v1/memory/context   — AssembleContext (token-budget-aware)
GET  /v1/memory/profile/:userID — get user profile sketch
POST /v1/memory/promote   — pin a memory to Detail Path
POST /v1/memory/demote    — demote a memory to Sketch Path
```

### Namespace-Based Multi-Agent Memory

`userID` is flexible — use it for NPC isolation, agent memory, or shared world state:

```go
engine.Add(msg, resp, "npc:lily:player123")      // Lily's memories of this player
engine.Add(msg, resp, "npc:watcher:player123")    // Watcher's different perspective
engine.Add(msg, resp, "shared:world")             // World events all NPCs share
engine.Add(msg, resp, "claude:project-abc")       // Claude agent's project memory
```

## Project Structure

```
geoffreyengram/
├── engram.go          # Core engine (Init, Search, Add, Reflect, Close)
├── types.go           # Sector, Memory, Entity, Config, SearchResult, options
├── providers.go       # EmbeddingProvider, SectorClassifier, EntityExtractor interfaces
├── store.go           # SQLite persistence, versioned migrations, temporal queries
├── scoring.go         # Composite scoring, cosine similarity, decay factor
├── decay_worker.go    # Background decay goroutine
├── classify.go        # HeuristicClassifier (keyword-based)
├── classify_llm.go    # LLMClassifier (heuristic + async LLM reclassification)
├── embed.go           # GeminiEmbedder
├── embed_openai.go    # OpenAIEmbedder (text-embedding-3-small/large)
├── embed_ollama.go    # OllamaEmbedder (local, no API key)
├── waypoints.go       # Entity graph, DefaultEntityExtractor
├── reflect.go         # Reflect method, deduplication, ReflectionProvider interface
├── reflect_gemini.go  # GeminiReflector (built-in LLM reflector)
├── reflect_worker.go  # Background reflection goroutine
├── dualmem/           # Dual-path agent memory engine
│   ├── dualmem.go     # Core engine (New, Add, Search, DualSearch, AssembleContext)
│   ├── types.go       # Types, Config, Store interface, provider interfaces
│   ├── scorer.go      # Importance scoring (sector + salience + specificity + novelty)
│   ├── project.go     # JL random projection (768→128, 768→64)
│   ├── detail.go      # Detail Path (Top-K store, capacity management, search)
│   ├── sketch.go      # Sketch Path (episodes, arcs, profiles)
│   ├── pipeline.go    # Background compression workers + agglomerative clustering
│   ├── store_sqlite.go # SQLite backend
│   ├── embed_gemini.go # Gemini embedding provider
│   └── api.go         # HTTP API handlers
├── *_test.go          # 97 tests across all subsystems
├── cmd/
│   ├── dualmem/       # CLI tool (add, search, context, profile, status)
│   └── engram-mcp/    # MCP stdio server (5 tools)
├── examples/
│   ├── chat/          # Local REPL chat with Ollama (Pattern A)
│   └── comparison/    # Multi-scenario comparison test (4 scenarios)
└── docs/
    └── ARCHITECTURE.md
```

## Status

This project was extracted from a production NPC memory system ([Club Mutant](https://github.com/goblincore/club-mutant)) where it powers a bartender character named Lily who remembers players, suggests music, and greets returning visitors with personalized messages.

### What works now
- 5-sector cognitive model with automatic heuristic classification
- Pluggable provider interfaces (embedding, classification, entity extraction, reflection)
- Embedding providers: Gemini, OpenAI, Ollama (local)
- Composite scoring with configurable weights (`ScoringWeights`)
- SQLite persistence with vector storage and versioned migrations
- Exponential decay with configurable per-sector rates and background worker
- Waypoint entity graph with one-hop associative expansion
- High-salience guarantee (important memories always surface)
- Conversation threading (`SessionID`, `ParentID`)
- Temporal queries (time-window search, session retrieval, last-session lookup)
- Reflective synthesis engine with deduplication
- MCP server with 5 tools (`remember`, `recall`, `reflect`, `get_session`, `inspect`)
- Async LLM sector reclassification (heuristic + background Gemini refinement)
- **DualMem dual-path memory** — importance-scored routing, hierarchical compression, token-budget-aware context assembly, HTTP API
- 97 tests across all subsystems

### Roadmap
- [x] LLM-powered sector classification (async reclassification via Gemini)
- [x] Comparison examples (4 scenarios testing each cognitive sector)
- [x] DualMem dual-path memory engine (Detail Path + Sketch Path)
- [ ] DualMem: Postgres/pgvector store backend
- [ ] DualMem: Data migration script (Engram SQLite → DualMem)
- [ ] DualMem: MCP server integration
- [ ] Local ONNX inference via hugot (`//go:build onnx`, CGO)
  - [ ] `OnnxEmbedder` — all-MiniLM-L6-v2 (~80MB, ~2ms, 384-dim, replaces API embedding calls)
  - [ ] `DistilBERTClassifier` — fine-tuned sector classification (~17MB, ~2ms, replaces LLM reclassification)
- [ ] Benchmark suite

## Why Go

| Factor | Go | Rust |
|--------|----|----|
| Performance | Excellent for server workloads | Marginal improvement |
| Binary | Single, zero deps | Single, zero deps |
| Background workers | Goroutines (natural fit) | Tokio (fine, more complex) |
| SQLite | Pure Go (no CGO needed) | rusqlite (mature) |
| Game engine embed | Not practical | Via C FFI / Wasm |

The LLM call (100-2000ms) dwarfs any IPC overhead (1ms localhost). The character's brain should be a sidecar service — it persists across sessions, survives crashes, and serves multiple game instances. Rust/Wasm only matters for offline browser games, a niche addressable later.

## License

MIT
