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

Built-in implementations: `GeminiEmbedder`, `HeuristicClassifier`, `DefaultEntityExtractor`, `GeminiReflector`.

### As an MCP Server (planned)

```bash
go install github.com/goblincore/geoffreyengram/cmd/engram-mcp
engram-mcp  # starts MCP stdio server
```

Tools: `remember`, `recall`, `reflect`, `forget`, `inspect`

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

## Project Structure

```
geoffreyengram/
├── engram.go          # Core engine (Init, Search, Add, Reflect, Close)
├── types.go           # Sector, Memory, Entity, Config, SearchResult, options
├── providers.go       # EmbeddingProvider, SectorClassifier, EntityExtractor interfaces
├── store.go           # SQLite persistence, versioned migrations, temporal queries
├── scoring.go         # Composite scoring, cosine similarity, decay factor
├── decay_worker.go    # Background decay goroutine
├── classify.go        # HeuristicClassifier (keyword + LLM fallback)
├── embed.go           # GeminiEmbedder (768-dim)
├── waypoints.go       # Entity graph, DefaultEntityExtractor
├── reflect.go         # Reflect method, deduplication, ReflectionProvider interface
├── reflect_gemini.go  # GeminiReflector (built-in LLM reflector)
├── reflect_worker.go  # Background reflection goroutine
├── *_test.go          # 58 tests covering scoring, classification, storage,
│                      #   temporal queries, reflection, entity extraction
└── docs/
    └── ARCHITECTURE.md
```

## Status

This project was extracted from a production NPC memory system ([Club Mutant](https://github.com/goblincore/club-mutant)) where it powers a bartender character named Lily who remembers players, suggests music, and greets returning visitors with personalized messages.

### What works now
- 5-sector cognitive model with automatic heuristic classification
- Pluggable provider interfaces (embedding, classification, entity extraction, reflection)
- Composite scoring with configurable weights (`ScoringWeights`)
- SQLite persistence with vector storage and versioned migrations
- Exponential decay with configurable per-sector rates and background worker
- Waypoint entity graph with one-hop associative expansion
- High-salience guarantee (important memories always surface)
- Conversation threading (`SessionID`, `ParentID`)
- Temporal queries (time-window search, session retrieval, last-session lookup)
- Reflective synthesis engine with deduplication
- Built-in Gemini provider (embeddings + reflection)
- 58 tests across all subsystems

### Roadmap
- [ ] MCP server (`cmd/engram-mcp`)
- [ ] Additional embedding providers (OpenAI, Ollama)
- [ ] LLM-powered sector classification (currently heuristic-only in practice)
- [ ] Examples (NPC bartender, simple companion)
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
