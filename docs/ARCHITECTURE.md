# geoffreyengram — Architecture

## What Is This

A standalone cognitive memory engine for AI characters — NPCs, companions, chatbots, agents. Instead of flat key-value memory or raw conversation logs, geoffreyengram organizes memories into **cognitive sectors** (episodic, semantic, procedural, emotional, reflective) with natural decay, associative recall, and reflective synthesis.

Born from CaviraOSS's OpenMemory model (which targets coding assistants), reapplied to where it arguably matters more: characters that remember you, notice patterns, and think between conversations.

## Why This Matters

Every AI companion app has terrible memory:

| Platform | Memory | The problem |
|----------|--------|-------------|
| Replika | Context window only | Forgets things from the same day |
| Character.ai | 400-char manual tags | Doesn't transfer across chats |
| Kindroid | User-curated "Lorebooks" | Works, but users do all the work |
| Nomi | Good in-session | Weak across sessions |

The universal complaint: *"Every conversation feels like they're meeting me for the first time."*

Existing memory infra (Mem0, Zep) is generic — built for coding assistants and chatbots. Nobody is building cognitive memory **specifically for character AI**.

## What Makes geoffreyengram Different

| Capability | Flat memory / RAG | geoffreyengram |
|-----------|-------------------|----------------|
| Organization | One bucket | 5 cognitive sectors |
| Importance | Everything equal | Salience scoring (0-1) |
| Forgetting | Manual delete or forever | Natural exponential decay |
| Associations | None | Waypoint entity graph |
| Retrieval | Keyword / vector match | Composite: similarity x salience x recency x associations x sector weight |
| Between conversations | Nothing | Reflective synthesis — character notices patterns, forms opinions |
| Personality | N/A | Per-character sector weights |
| Conversation tracking | None | Session threading with parent chains |

## The Cognitive Model

### Five Sectors

| Sector | What it stores | Decay lambda | Example |
|--------|---------------|-------------|---------|
| **Episodic** | Events, experiences | 0.005 (slow) | "Player visited Tokyo last month" |
| **Semantic** | Facts, knowledge | 0.02 (warm) | "Player's name is Alex, likes jazz" |
| **Procedural** | Skills, how-tos, routines | 0.02 (warm) | "Player always orders a Nebula Fizz" |
| **Emotional** | Sentiments, feelings | 0.005 (slow) | "Player seemed sad last conversation" |
| **Reflective** | Meta-observations, patterns | 0.05 (cold) | "Player always mentions music when stressed" |

Decay rates are configurable per-sector via `Config.DecayRates`.

### Composite Scoring

```
score = (w1 x similarity + w2 x salience + w3 x recency + w4 x linkWeight) x sectorWeight
```

Default weights (configurable via `ScoringWeights`):
- **Similarity** (0.6) — cosine similarity between query and memory embeddings
- **Salience** (0.2) — how important this memory is (0-1, boosted by access, decays over time)
- **Recency** (0.1) — exponential decay from last access
- **Link weight** (0.1) — bonus from waypoint graph connections
- **Sector weight** — per-character multiplier (bartender: episodic 1.5x, emotional 1.5x)

### Waypoint Graph

Memories are linked through shared entities (people, places, topics). When you recall "Japan," the graph also surfaces memories about "jazz" (because you mentioned jazz bars in Tokyo), "their dog" (because they mentioned missing the dog while traveling), etc. One-hop associative expansion.

Entity extraction is pluggable via `EntityExtractor`. The built-in `DefaultEntityExtractor` handles:
- Bracketed names: `[Alex]`
- Quoted strings: `"Nebula Fizz"`
- Capitalized phrases (2+ words)
- Configurable `KnownEntities` for domain-specific terms

### Natural Decay

Important memories persist. Trivial ones fade. High-salience memories decay slowly; low-salience memories expire naturally. Background worker runs periodically (default: every 12 hours). Per-sector decay rates are configurable. Memories that decay below `MinDecayScore` (default: 0.01) are deleted.

### High-Salience Guarantee

Explicit user requests ("Always greet me with Howdy Cowboy") get stored with high salience. Even when the search query has low cosine similarity (a casual "hi"), these memories are guaranteed to surface — up to 2 high-salience memories (salience >= 0.6) injected per search regardless of similarity score.

## Reflective Synthesis

The difference between "NPC with a database" and "character that thinks":

```
Player leaves the bar
        |
    [Time passes]
        |
Reflective worker fires for this player
        |
Loads recent memories (configurable window, default 50)
        |
Filters out existing reflective memories (don't reflect on reflections)
        |
Calls ReflectionProvider with character context
        |
LLM generates 1-3 observations:
  -> "They always mention music when they're sad"
  -> "They've been talking about Japan a lot"
        |
Deduplicates against existing reflections (cosine > 0.85 = duplicate)
        |
Stores as SectorReflective with salience clamping (min 0.7, max 1.0)
        |
Player returns -> reflection surfaces in greeting
```

### ReflectionProvider Interface

Reflection is **explicit opt-in** — never auto-constructed. It involves LLM generation calls and callers should control when and how reflection happens.

```go
type ReflectionProvider interface {
    Reflect(ctx context.Context, memories []Memory, characterContext string) ([]Reflection, error)
}
```

Built-in: `GeminiReflector` — prompts Gemini to find 1-3 patterns across recent memories and returns structured JSON observations with salience scores and entities.

### Reflection Worker

Optional background goroutine (same pattern as decay worker). Enabled when `Config.ReflectionInterval > 0` and a `ReflectionProvider` is configured. Iterates all active users and triggers `Reflect()` for each.

## Temporal Enrichment

### Conversation Threading

Memories carry `SessionID` (conversation session identifier) and `ParentID` (previous memory in the chain). This enables:

- **Session retrieval**: `GetSession(sessionID)` returns all memories from a conversation in chronological order
- **Last session**: `GetLastSession(userID)` finds the most recent session
- **Parent chains**: Thread memories together for conversation continuity

### Time-Window Queries

`SearchWithOptions` supports temporal filters:
- `After` / `Before` — restrict to a time range
- `SessionID` — filter to a specific conversation
- `Sectors` — filter to specific memory types

## Architecture

### Form Factor: Go Library (+ MCP Server planned)

```go
import engram "github.com/goblincore/geoffreyengram"
```

Single flat package. No sub-packages. All types, interfaces, and implementations live at the top level.

### Pluggable Provider Interfaces

```go
// providers.go — three core decoupling interfaces

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
```

Provider resolution in `Init()`:
1. If explicit provider set in Config, use it
2. If `GeminiAPIKey` set, construct `GeminiEmbedder` + `HeuristicClassifier`
3. `DefaultEntityExtractor` is always the fallback for entity extraction
4. `ReflectionProvider` is **never** auto-constructed — always explicit opt-in

### Storage

SQLite via `modernc.org/sqlite` (pure Go, no CGO). Versioned migrations with a `schema_version` table:

- **v1**: memories, vectors, waypoints, associations tables
- **v2**: `session_id` and `parent_id` columns + indexes

Vector storage: raw `float32` slices encoded as binary blobs alongside memory sector tags.

### Two Integration Patterns

**Pattern A — Server-driven (simple, predictable, cheaper)**
```
Player says "hi"
  -> Your server calls Search(query, userID) -> gets memories
  -> Your server builds LLM prompt with memories
  -> LLM generates response
  -> Your server calls Add(userMsg, assistantMsg, userID)
```

**Pattern B — Agent-driven (autonomous, emergent, higher cost)**
```
Player says "hi"
  -> LLM agent has recall/remember as MCP tools
  -> LLM decides when to search and store memories
  -> Character has agency over its own memory
```

### MCP Server Tools (planned)

```
engram-mcp
|
+-- remember    — Store a memory
|   params: content, user_id, sector_hint?, entities?, session_id?
|
+-- recall      — Search memories
|   params: query, user_id, limit?, time_after?, time_before?, sectors?
|
+-- reflect     — Trigger reflective synthesis
|   params: user_id, character_context?
|
+-- forget      — Remove specific memories
|   params: memory_id | user_id + query
|
+-- inspect     — Debug/browse (admin)
    params: user_id, sector?, limit?
```

## Project Structure

```
geoffreyengram/
├── engram.go           # Core engine: Init, Search, Add, AddWithOptions,
|                       #   SearchWithOptions, GetSession, GetLastSession,
|                       #   Reflect, Close
├── types.go            # Sector, Memory, Entity, Config, ScoringWeights,
|                       #   SectorWeights, AddOptions, SearchOptions, SearchResult
├── providers.go        # EmbeddingProvider, SectorClassifier, EntityExtractor
├── store.go            # SQLite persistence, versioned migrations (v1-v2),
|                       #   vector storage, temporal queries
├── scoring.go          # CompositeScore, CosineSimilarity, DecayFactor, DaysSince
├── decay_worker.go     # Background decay goroutine (configurable interval)
├── classify.go         # HeuristicClassifier (keyword patterns + optional LLM)
├── embed.go            # GeminiEmbedder (768-dim, implements EmbeddingProvider)
├── waypoints.go        # Waypoint entity graph, DefaultEntityExtractor,
|                       #   ExpandViaWaypoints (one-hop)
├── reflect.go          # Reflect method, ReflectionProvider interface,
|                       #   Reflection type, ReflectOptions, deduplication
├── reflect_gemini.go   # GeminiReflector (prompts Gemini for pattern detection)
├── reflect_worker.go   # Background reflection goroutine
|
├── scoring_test.go     # CompositeScore, CosineSimilarity, DecayFactor tests
├── classify_test.go    # Heuristic classification per sector
├── store_test.go       # SQLite operations, vector encode/decode, decay sweep
├── waypoints_test.go   # Entity extraction (brackets, quotes, known entities)
├── temporal_test.go    # Session chaining, time-window, GetLastSession
├── reflect_test.go     # Reflection storage, dedup, salience clamping, parsing
|
├── docs/
|   └── ARCHITECTURE.md # This file
├── go.mod
├── go.sum
└── .gitignore
```

## Implementation Status

### Completed

- **Phase 1: Extract & Genericize** — Provider interfaces (`EmbeddingProvider`, `SectorClassifier`, `EntityExtractor`), configurable `ScoringWeights` and `DecayRates`, renamed implementations (`GeminiEmbedder`, `HeuristicClassifier`, `DefaultEntityExtractor`), removed hardcoded domain data, comprehensive test suite
- **Phase 3: Temporal Enrichment** — `SessionID`/`ParentID` on memories, versioned schema migrations, `AddWithOptions`/`SearchWithOptions` API, time-window and session queries, `GetSession`/`GetLastSession` convenience methods
- **Phase 4: Reflective Synthesis** — `ReflectionProvider` interface, `Reflect()` method with deduplication (cosine > 0.85), `GeminiReflector` built-in, optional background reflection worker, salience clamping, 58 tests passing

### Remaining

- **Phase 2: MCP Server** — `cmd/engram-mcp` using mcp-go SDK
- **Phase 5: Polish** — Additional embedding providers (OpenAI, Ollama), examples, benchmarks

## Who Uses This

| Audience | How they use it | Value |
|----------|----------------|-------|
| **Game devs** | Library embed or MCP sidecar | NPCs that remember players across sessions |
| **AI companion builders** | MCP server as memory backend | Solves the "forgetting" problem |
| **SillyTavern community** | MCP plugin | Drop-in long-term memory for custom characters |
| **Agent builders** | Library or MCP | Agents with cognitive structure, not just flat memory |
