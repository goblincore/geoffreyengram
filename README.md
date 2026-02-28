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
| **Episodic** | Events, experiences | Medium | "Player visited Tokyo last month" |
| **Semantic** | Facts, knowledge | Very slow | "Player's name is Alex, likes jazz" |
| **Procedural** | Skills, routines | Slow | "Player always orders a Nebula Fizz" |
| **Emotional** | Sentiments, feelings | Medium-fast | "Player seemed sad last conversation" |
| **Reflective** | Meta-observations | Slow | "Player always mentions music when stressed" |

Each memory is automatically classified into a sector. Sectors have different decay rates — facts persist while small talk fades. Characters can weight sectors differently: a warm bartender values emotional memories; a scholar values semantic ones.

### Composite Scoring

Retrieval ranks memories by a blended score:

```
score = (0.6 x similarity + 0.2 x salience + 0.1 x recency + 0.1 x linkWeight) x sectorWeight
```

- **Similarity** — cosine similarity between query and memory embeddings
- **Salience** — how important this memory is (boosted when accessed, decays over time)
- **Recency** — exponential decay from last access
- **Link weight** — bonus from waypoint graph connections (associative recall)
- **Sector weight** — per-character multiplier

### Waypoint Graph

Memories are linked through shared entities (people, places, topics). When you recall "Japan," the graph also surfaces memories about "jazz" and "their dog" — because the player mentioned jazz bars in Tokyo and missing the dog while traveling. One-hop associative expansion.

### Natural Decay

Important memories persist. Trivial ones fade. High-salience memories decay slowly; low-salience memories expire naturally. A background worker runs periodically. No manual cleanup needed.

### Reflective Synthesis (planned)

The difference between "NPC with a database" and "character that thinks." Between conversations, the engine can synthesize higher-order observations:

```
Player leaves → [time passes] → reflective worker fires
  → loads recent memories → finds patterns
  → generates a "thought": "They always mention music when stressed"
  → stored as high-salience reflective memory
  → surfaces naturally when player returns
```

## Quick Start

### As a Go Library

```go
import "github.com/goblincore/geoffreyengram"

// Initialize
mem, err := engram.Init(engram.Config{
    DBPath:       "./data/memory.db",
    GeminiAPIKey: os.Getenv("GEMINI_API_KEY"),
})
defer mem.Close()

// Store a conversation
mem.Add("I just got back from Tokyo!", "That's amazing! How was it?", "character:player123")

// Recall relevant memories
weights := engram.DefaultSectorWeights()
weights[engram.SectorEmotional] = 1.5  // this character values emotional memories
results := mem.Search("tell me about japan", "character:player123", 5, weights)

for _, r := range results {
    fmt.Printf("[%s] %s (score=%.2f)\n", r.Sector, r.Summary, r.Score)
}
```

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
   |            |             |
   SQLite    Embeddings    Classification
  (local)   (pluggable)    (heuristic + LLM)
```

**Local-first.** SQLite database, single binary, no cloud dependency. Embedding provider is pluggable (Gemini, OpenAI, Ollama).

**Two integration patterns:**

- **Server-driven (Pattern A):** Your code calls `Search()` and `Add()` explicitly. You control when memory is read/written. Simple, predictable, cheaper.
- **Agent-driven (Pattern B):** The LLM has `recall`/`remember` as MCP tools and decides when to use them. The character has agency over its own memory. More autonomous, more emergent, more LLM calls.

## Status

This project was extracted from a production NPC memory system ([Club Mutant](https://github.com/goblincore/club-mutant)) where it powers a bartender character named Lily who remembers players, suggests music, and greets returning visitors with personalized messages.

### What works now
- 5-sector cognitive model with automatic classification
- Composite scoring with configurable per-character sector weights
- SQLite persistence with vector storage
- Exponential decay with background worker
- Waypoint entity graph with one-hop associative expansion
- High-salience guarantee (explicit requests always surface)
- Gemini embeddings (768-dim)

### Roadmap
- [ ] Pluggable embedding providers (OpenAI, Ollama)
- [ ] Pluggable entity extractors
- [ ] MCP server (`cmd/engram-mcp`)
- [ ] Reflective synthesis engine
- [ ] Conversation threading (session tracking)
- [ ] Time-window queries
- [ ] Configurable scoring weights
- [ ] Examples (NPC bartender, simple companion)

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
