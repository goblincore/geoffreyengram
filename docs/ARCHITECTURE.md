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

### Form Factor: Go Library + MCP Server

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
2. If `GeminiAPIKey` set, construct `GeminiEmbedder` + `LLMClassifier` (heuristic sync + async LLM reclassification)
3. If no API key, fall back to `HeuristicClassifier` (keyword-only, no LLM)
4. `DefaultEntityExtractor` is always the fallback for entity extraction
5. `ReflectionProvider` is **never** auto-constructed — always explicit opt-in

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

### MCP Server (`cmd/engram-mcp`)

A thin stdio MCP server wrapping the library API. Configured via environment variables (`ENGRAM_DB_PATH`, `GEMINI_API_KEY`).

```
engram-mcp
|
+-- remember     — Store a memory (wraps AddWithOptions)
|   params: user_id, user_message, assistant_message, session_id?, parent_id?, sector_hint?, salience?
|
+-- recall       — Search memories (wraps SearchWithOptions)
|   params: query, user_id, limit?, session_id?, sectors[]?, after?, before?
|
+-- reflect      — Trigger reflective synthesis (wraps Reflect)
|   params: user_id, character_context?, memory_window?, sectors[]?, min_memories?
|
+-- get_session  — Retrieve conversation session (wraps GetSession/GetLastSession)
|   params: user_id, session_id?
|
+-- inspect      — Browse recent memories (wraps ListRecent)
    params: user_id, limit?, sectors[]?
```

Built with the official MCP Go SDK (`github.com/modelcontextprotocol/go-sdk`). Install:

```bash
go install github.com/goblincore/geoffreyengram/cmd/engram-mcp
```

### Embedding Providers

Three built-in providers implement `EmbeddingProvider`:

| Provider | Constructor | API Key | Default Model | Default Dim |
|----------|-------------|---------|---------------|-------------|
| `GeminiEmbedder` | `NewGeminiEmbedder(key, dim)` | Required | gemini-embedding-001 | 768 |
| `OpenAIEmbedder` | `NewOpenAIEmbedder(key, opts...)` | Required | text-embedding-3-small | 1536 |
| `OllamaEmbedder` | `NewOllamaEmbedder(model, dim, opts...)` | None | User-specified | User-specified |

OpenAI supports functional options: `WithOpenAIModel`, `WithOpenAIDimension`, `WithOpenAIBaseURL` (for Azure/proxies).
Ollama supports: `WithOllamaHost` (default `http://localhost:11434`).

### Sector Classification

Two built-in classifiers implement `SectorClassifier`:

**`HeuristicClassifier`** — keyword-based scoring. Each sector has a list of signal words (e.g., "feel", "happy", "sad" → emotional; "how to", "technique", "step" → procedural). Scores accumulate at +0.3 per hit, highest sector wins. If confidence < 0.6 and an API key is available, falls back to a synchronous Gemini call. Zero-cost for keyword-rich content, but misclassifies natural conversation that lacks signal words.

**`LLMClassifier`** — wraps the heuristic + async LLM reclassification. The write path stays fast:

```
Add() called
  → HeuristicClassifier.Classify() → returns sector instantly (0ms)
  → Memory stored with heuristic sector
  → SubmitForReclassification(memID, content) → non-blocking send to buffered channel
  → Background goroutine:
      → Calls Gemini with sector descriptions + examples
      → If LLM sector ≠ heuristic sector → UpdateMemorySector() (updates both tables)
      → If sectors match → no-op
```

The channel buffer holds 64 pending requests. If full, new requests are silently dropped — the heuristic sector is kept as a reasonable fallback. `Close()` drains remaining requests before shutdown.

This means a memory stored as "semantic" (heuristic default for ambiguous content like "I just got back from Tokyo") gets reclassified to "episodic" ~200-500ms later when the LLM responds — before the next `Search()` call in a typical conversation flow.

## Comparison Example (`examples/comparison/`)

A runnable validation that cognitive memory actually produces better characters than naive approaches.

### How It Works

A scripted multi-session conversation is played through 3 memory modes simultaneously:

| Mode | Memory Strategy | What it isolates |
|------|----------------|------------------|
| **Stateless** | None | Baseline — every session is a first meeting |
| **Flat RAG** | Embed + cosine top-k (in-memory) | Does cognitive structure matter vs naive retrieval? |
| **Full Engram** | Sectors, composite scoring, waypoints, decay, reflection | The full system |

Flat RAG and Engram share the same `GeminiEmbedder` so the only variable is the retrieval strategy.

Each scenario follows: **3 history sessions** (building up memories) → **time gap** (engram runs `Reflect()`) → **probe session** (evaluate the greeting). An LLM-as-judge rates each mode's probe response on recall, relevance, personality, insight, and naturalness (1-5).

### Scenarios

Four scenarios test different aspects of cognitive memory:

| Scenario | Character | Primary Sectors | What the probe tests |
|----------|-----------|----------------|---------------------|
| `lily` | Bartender Lily at Club Mutant | Emotional, Episodic | Does she remember Alex is a jazz pianist, went to Tokyo, was stressed? Does she show warmth? |
| `sifu` | Wing Chun instructor Sifu Chen | Procedural, Semantic | Does he remember the skill sequence (stance → punch → block), Kai's elbow problem, and suggest a logical next step? |
| `nyx` | Archivist Nyx in the Athenaeum | Semantic, Reflective | Does she cross-reference Ashenmoor, Valdris, and Thornwall? Does the waypoint graph link the entities? |
| `reeves` | Therapist Dr. Reeves | All 5 sectors | Does he recall Morgan's anxiety patterns, partner Sam, coworker Dana, the journaling technique, and meal-skipping stress response? |

### Running

```bash
# List scenarios
go run ./examples/comparison/ --list

# Run a specific scenario (~60s, ~40 Gemini API calls)
GEMINI_API_KEY=... go run ./examples/comparison/ --scenario lily

# Interactive selection
GEMINI_API_KEY=... go run ./examples/comparison/
```

### Output

- **Terminal**: Interleaved turn-by-turn comparison, score table, judge explanations
- **Markdown**: `examples/comparison/results_<name>.md` — each mode's full conversation end-to-end for easy human reading

### Architecture

```
examples/comparison/
├── main.go        # Runners (stateless, flat-rag, engram), judge, output, CLI
└── scenarios.go   # Scenario struct + 4 scenario definitions
```

The `Scenario` struct encapsulates character prompt, player name, session script, sector weights, retrieval limit, and judge context. Runners are fully generic — adding a new scenario means adding a new entry to `AllScenarios` in `scenarios.go`.

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
├── classify_llm.go     # LLMClassifier (heuristic sync + async Gemini reclassification)
├── embed.go            # GeminiEmbedder (768-dim)
├── embed_openai.go     # OpenAIEmbedder (text-embedding-3-small/large)
├── embed_ollama.go     # OllamaEmbedder (local, no API key)
├── waypoints.go        # Waypoint entity graph, DefaultEntityExtractor,
|                       #   ExpandViaWaypoints (one-hop)
├── reflect.go          # Reflect method, ReflectionProvider interface,
|                       #   Reflection type, ReflectOptions, deduplication
├── reflect_gemini.go   # GeminiReflector (prompts Gemini for pattern detection)
├── reflect_worker.go   # Background reflection goroutine
|
├── scoring_test.go     # CompositeScore, CosineSimilarity, DecayFactor tests
├── classify_test.go    # Heuristic classification per sector
├── classify_llm_test.go # LLM classifier async reclassification tests
├── store_test.go       # SQLite operations, vector encode/decode, decay sweep
├── waypoints_test.go   # Entity extraction (brackets, quotes, known entities)
├── temporal_test.go    # Session chaining, time-window, GetLastSession
├── embed_openai_test.go # OpenAI embedder tests (httptest mock)
├── embed_ollama_test.go # Ollama embedder tests (httptest mock)
├── reflect_test.go     # Reflection storage, dedup, salience clamping, parsing
|
├── cmd/
|   └── engram-mcp/
|       └── main.go     # MCP stdio server (5 tools)
├── examples/
|   └── comparison/
|       ├── main.go     # Runners, judge, output, CLI
|       └── scenarios.go # Scenario struct + 4 scenario definitions
├── docs/
|   └── ARCHITECTURE.md # This file
├── go.mod
├── go.sum
└── .gitignore
```

## Implementation Status

### Completed

- **Phase 1: Extract & Genericize** — Provider interfaces (`EmbeddingProvider`, `SectorClassifier`, `EntityExtractor`), configurable `ScoringWeights` and `DecayRates`, renamed implementations (`GeminiEmbedder`, `HeuristicClassifier`, `DefaultEntityExtractor`), removed hardcoded domain data, comprehensive test suite
- **Phase 2: MCP Server** — `cmd/engram-mcp` with 5 tools (`remember`, `recall`, `reflect`, `get_session`, `inspect`), official MCP Go SDK, stdio transport, env-based config
- **Phase 3: Temporal Enrichment** — `SessionID`/`ParentID` on memories, versioned schema migrations, `AddWithOptions`/`SearchWithOptions` API, time-window and session queries, `GetSession`/`GetLastSession` convenience methods
- **Phase 4: Reflective Synthesis** — `ReflectionProvider` interface, `Reflect()` method with deduplication (cosine > 0.85), `GeminiReflector` built-in, optional background reflection worker, salience clamping
- **Additional Providers** — `OpenAIEmbedder` (text-embedding-3-small/large, Azure support), `OllamaEmbedder` (local, no API key)

- **Phase 5: Examples** — Multi-scenario comparison test (`examples/comparison/`): 4 scenarios (Lily/emotional, Sifu/procedural, Nyx/semantic, Reeves/all-sector) with stateless vs flat-RAG vs engram evaluation, LLM-as-judge scoring, CLI selection
- **Async LLM Classification** — `LLMClassifier` with heuristic sync + Gemini async reclassification, non-blocking buffered channel, `UpdateMemorySector` for post-hoc correction, 81 tests passing

### Remaining

- Fine-tuned DistilBERT classifier (local ONNX, ~2ms inference, no API calls)
- Benchmark suite

## Who Uses This

| Audience | How they use it | Value |
|----------|----------------|-------|
| **Game devs** | Library embed or MCP sidecar | NPCs that remember players across sessions |
| **AI companion builders** | MCP server as memory backend | Solves the "forgetting" problem |
| **SillyTavern community** | MCP plugin | Drop-in long-term memory for custom characters |
| **Agent builders** | Library or MCP | Agents with cognitive structure, not just flat memory |
