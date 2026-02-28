# geoffreyengram — Cognitive Memory for AI Characters

## Immediate Task: Create the Repo + Initial Docs

1. Create new repo at `~/Projects/2026/geoffreyengram`
2. Initialize Go module: `github.com/goblincore/geoffreyengram`
3. Write README.md summarizing this architecture document
4. Copy cogmem package from `club-mutant/services/dream-npc-go/npc/cogmem/` as starting point
5. git init + initial commit

The Club Mutant cogmem will eventually be replaced by importing this package, but existing implementation stays for now.

---

# Architecture Document

## What Is This

A standalone cognitive memory engine for AI characters — NPCs, companions, chatbots, agents. Instead of flat key-value memory or raw conversation logs, cogmem organizes memories into **cognitive sectors** (episodic, semantic, procedural, emotional, reflective) with natural decay, associative recall, and reflective synthesis.

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

## What Makes cogmem Different

| Capability | Flat memory / RAG | cogmem |
|-----------|-------------------|--------|
| Organization | One bucket | 5 cognitive sectors |
| Importance | Everything equal | Salience scoring (0–1) |
| Forgetting | Manual delete or forever | Natural exponential decay |
| Associations | None | Waypoint entity graph |
| Retrieval | Keyword / vector match | Composite: similarity × salience × recency × associations × sector weight |
| Between conversations | Nothing | Reflective synthesis — character notices patterns, forms opinions |
| Personality | N/A | Per-character sector weights (a warm bartender values emotional memories; a scholar values semantic) |

## The Cognitive Model

### Five Sectors

| Sector | What it stores | Decay rate | Example |
|--------|---------------|------------|---------|
| **Episodic** | Events, experiences | Medium (λ=0.02) | "Player visited Tokyo last month" |
| **Semantic** | Facts, knowledge | Very slow (λ=0.005) | "Player's name is Alex, likes jazz" |
| **Procedural** | Skills, how-tos, routines | Slow (λ=0.008) | "Player always orders a Nebula Fizz" |
| **Emotional** | Sentiments, feelings | Medium-fast (λ=0.03) | "Player seemed sad last conversation" |
| **Reflective** | Meta-observations, patterns | Slow (λ=0.01) | "Player always mentions music when stressed" |

### Composite Scoring

```
score = (0.6 × similarity + 0.2 × salience + 0.1 × recency + 0.1 × linkWeight) × sectorWeight
```

- **Similarity** (60%) — cosine similarity between query and memory embeddings
- **Salience** (20%) — how important this memory is (0–1, boosted by access, decays over time)
- **Recency** (10%) — exponential decay from last access
- **Link weight** (10%) — bonus from waypoint graph connections
- **Sector weight** — per-character multiplier (bartender: episodic 1.5×, emotional 1.5×)

### Waypoint Graph

Memories are linked through shared entities (people, places, topics). When you recall "Japan," the graph also surfaces memories about "jazz" (because you mentioned jazz bars in Tokyo), "their dog" (because they mentioned missing the dog while traveling), etc. One-hop associative expansion.

### Natural Decay

Important memories persist. Trivial ones fade. High-salience memories decay slowly; low-salience memories expire naturally. Background worker runs every 12 hours. No manual cleanup needed.

### High-Salience Guarantee

Explicit user requests ("Always greet me with Howdy Cowboy") get stored with high salience. Even when the search query has low cosine similarity (a casual "hi"), these memories are guaranteed to surface — up to 2 high-salience memories injected per search regardless of similarity score.

## Reflective Synthesis (the differentiator)

The difference between "NPC with a database" and "character that thinks":

```
Player leaves the bar
        ↓
    [Time passes]
        ↓
Reflective worker fires for this player
        ↓
Loads recent episodic memories → finds patterns
        ↓
LLM generates a "thought" the character would have
  → "They always mention music when they're sad"
  → "They've been talking about Japan a lot — I found a poet they'd love"
        ↓
Stored as SectorReflective (high salience)
        ↓
Player returns → reflection surfaces in greeting
        ↓
"hey... I was thinking about what you said about Japan.
 do you know Kobayashi Issa? his haiku remind me of home..."
```

This isn't reactive recall — it's the character forming opinions and making connections *between* conversations.

## Architecture

### Form Factor: Go Library + MCP Server

```bash
# Embed in your Go game server (what Club Mutant does)
go get github.com/goblincore/cogmem

# Run as standalone MCP server for any app
go install github.com/goblincore/cogmem/cmd/cogmem-mcp

# Debug/inspect memories
go install github.com/goblincore/cogmem/cmd/cogmem-inspect
```

### Why Go (not Rust)

| Factor | Go | Rust |
|--------|----|----|
| Already written | ✅ ~1300 lines, production-tested | Would be full rewrite |
| Single binary | ✅ | ✅ |
| Background workers | ✅ Goroutines (natural fit) | Tokio (fine, more complex) |
| SQLite | modernc.org (pure Go, no CGO) | rusqlite (mature) |
| MCP SDK | mcp-go (good) | mcp-rust (good) |
| Game engine embed | ❌ Not practical | ✅ Via C FFI / Wasm |

**Why embedding in a game engine doesn't matter:** The LLM call (100-2000ms) dwarfs any IPC overhead (1ms localhost). The character's brain needs to be a sidecar service anyway — it persists across sessions, survives crashes, and serves multiple game instances. Rust/Wasm only matters for offline browser games, which is a niche that can be addressed later if demand appears.

### MCP Server Tools

```
cogmem-mcp
│
├── remember    — Store a memory
│   params: content, user_id, sector_hint?, entities?, session_id?
│
├── recall      — Search memories
│   params: query, user_id, limit?, time_after?, time_before?, sectors?
│
├── reflect     — Trigger reflective synthesis
│   params: user_id
│   returns: new reflective observations generated
│
├── forget      — Remove specific memories
│   params: memory_id | user_id + query
│
└── inspect     — Debug/browse (admin)
    params: user_id, sector?, limit?
```

### Two Integration Patterns

**Pattern A — Server-driven (simple, predictable, cheaper)**
```
Player says "hi"
  → Your server calls recall(user_id) → gets memories
  → Your server builds LLM prompt with memories
  → LLM generates response
  → Your server calls remember(response)
```
You control when memory is read/written. Deterministic pipeline. This is what Club Mutant does today.

**Pattern B — Agent-driven (autonomous, emergent, higher cost)**
```
Player says "hi"
  → LLM agent has recall/remember as tools
  → LLM decides: "Let me check if I know them" → calls recall
  → LLM decides: "This is worth remembering" → calls remember
  → LLM decides: "This small talk isn't worth storing" → skips remember
```
The character has **agency over its own memory** — it decides what to remember, what to search for, what to forget. More LLM calls, but more emergent behavior. Same MCP tools support both patterns.

### Pluggable Providers

```go
// Embedding — bring your own vector provider
type EmbeddingProvider interface {
    Embed(text string, taskType string) ([]float32, error)
    Dimension() int
}
// Built-in: Gemini, OpenAI, Ollama (local, no API key)

// Classification — sector routing
type SectorClassifier interface {
    Classify(content string) Sector
}
// Built-in: heuristic keywords + LLM fallback

// Entity extraction — domain-specific
type EntityExtractor interface {
    Extract(content string) []Entity
}
// Built-in: brackets, quotes, capitalized phrases
// Users add: MusicExtractor, GameItemExtractor, etc.
```

## Project Structure

```
cogmem/
├── cogmem.go          # Core engine (Init, Search, Add, Reflect, Close)
├── config.go          # Configurable scoring weights, decay rates, caps
├── types.go           # Sector, Memory, Entity, SearchResult
├── store.go           # SQLite persistence + migrations
├── scoring.go         # Composite scoring + cosine similarity
├── decay.go           # Decay worker + exponential model
├── waypoints.go       # Entity graph (pluggable extractors)
├── classify.go        # Sector classification (heuristic + LLM fallback)
├── reflect.go         # Reflective synthesis engine
├── temporal.go        # Time windows, conversation threading
│
├── embed/             # Embedding providers
│   ├── provider.go    # Interface
│   ├── gemini.go
│   ├── openai.go
│   └── ollama.go
│
├── extract/           # Entity extractors
│   ├── extractor.go   # Interface
│   └── default.go     # Brackets, quotes, capitalized phrases
│
├── cmd/
│   ├── cogmem-mcp/    # MCP server binary
│   └── cogmem-inspect/# CLI inspector
│
└── examples/
    ├── npc-bartender/ # Club Mutant use case
    └── companion/     # Simple AI companion example
```

## What Exists vs. What Needs Building

### Already built (~80% general-purpose):
- ✅ Sector model, types, scoring formula
- ✅ SQLite persistence + vector storage
- ✅ Exponential decay + background worker
- ✅ Waypoint entity graph + one-hop expansion
- ✅ Gemini embeddings + sector classification
- ✅ High-salience guarantee
- ✅ cogmem-inspect CLI tool

### Needs extraction/genericizing:
- ⚠️ Hardcoded Gemini model → EmbeddingProvider interface
- ⚠️ Hardcoded music artist list → pluggable EntityExtractor
- ⚠️ Summary format assumes "user → npc" → generic formatter
- ⚠️ Scoring weights hardcoded → configurable via Config

### Needs building (new):
- 🔨 MCP server (cmd/cogmem-mcp)
- 🔨 Reflective synthesis engine (reflect.go)
- 🔨 Conversation threading (session_id, parent_id)
- 🔨 Time-window queries (SearchTimeWindow)
- 🔨 OpenAI + Ollama embedding providers
- 🔨 Configurable scoring weights

## Implementation Phases

### Phase 1: Extract & Genericize (2-3 days)
- New repo, copy cogmem package
- Add EmbeddingProvider interface (keep Gemini default, add Ollama)
- Add EntityExtractor interface (move music artists to example)
- Make scoring weights + decay rates configurable
- Generic summary builder
- Tests against existing SQLite test data

### Phase 2: MCP Server (2-3 days)
- cmd/cogmem-mcp using mcp-go SDK
- Tools: remember, recall, forget, inspect
- Config via env vars or YAML
- Test: connect from Claude Desktop, verify remember/recall cycle

### Phase 3: Temporal Enrichment (2-3 days)
- session_id + parent_id columns in memories table
- SearchTimeWindow() method
- Conversation threading support
- "What happened last time?" queries

### Phase 4: Reflective Synthesis (3-4 days)
- Reflect() method — periodic pattern detection across recent memories
- LLM-powered reflection ("Given these memories, what patterns emerge?")
- Store reflective observations as high-salience memories
- Configurable reflection interval + triggers

### Phase 5: Polish & Examples (2-3 days)
- Club Mutant example (extract current integration)
- Simple companion chatbot example
- README, docs, API reference
- Benchmark: 1000 memories, search latency < 50ms

## Who Uses This

| Audience | How they use it | Value |
|----------|----------------|-------|
| **Game devs** | Library embed or MCP sidecar | NPCs that remember players across sessions |
| **AI companion builders** | MCP server as memory backend | Solves the "forgetting" problem (Replika/Character.ai gap) |
| **SillyTavern community** | MCP plugin | Drop-in long-term memory for custom characters |
| **Agent builders** | Library or MCP | Agents with cognitive structure, not just flat memory |

## Name Candidates
- **cogmem** — straightforward, technical
- **engram** — neuroscience term for a memory trace
- **myco** — mycorrhizal network (underground fungal memory network, fits the alien flower origin story)
