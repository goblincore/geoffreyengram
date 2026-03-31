# Automatic Session Distillation + Entity Graph Layer

**Date:** 2026-03-30
**Status:** Draft
**Inspired by:** [Signet-AI](https://github.com/Signet-AI/signetai) analysis

## Context

DualMem currently relies on the agent (or user) to explicitly call `dualmem add` to save memories. This means important decisions, warnings, and context from a session can be lost if the agent forgets to save or the user doesn't run `/wrap-up`. Signet-AI's key advantage is **ambient capture** — sessions are distilled automatically without agent involvement.

Additionally, dualmem stores entities as flat JSON arrays per memory (`entities_json`) but doesn't link them across memories. This limits retrieval to embedding similarity and keyword matching. A lightweight entity graph enables traversal-based retrieval — finding memories connected through shared entities even when they don't share vocabulary.

These two features share a foundation: distillation produces both extracted facts and entity relationship triples, which feed directly into the graph layer.

## Feature 1: Automatic Session Distillation

### Overview

New `dualmem distill` CLI command that reads a session transcript and uses Gemini to extract structured memories (facts, decisions, warnings) and entity triples. Triggered automatically via Claude Code post-session hook.

### Transcript Discovery

**Priority order:**
1. `--file <path>` or stdin — explicit transcript input (any harness)
2. Auto-detect Claude Code sessions from `~/.claude/projects/*/sessions/`
   - Find the most recent session file in the project matching the current working directory
   - Parse CC's session format (JSON lines with role/content pairs)
3. If neither found, error with usage guidance

**`--auto` flag:** Used by the hook. Skips if no new session found since last distill (tracked via `dualmem_config` table key `last_distill_session_id`).

### Extraction Pipeline

**Step 1: Transcript preparation**
- Parse message pairs from session file
- Filter to substantive exchanges (skip tool-call-only turns, single-word responses)
- Truncate to last ~12,000 characters if transcript exceeds limit (Signet's proven cap)
- Include file paths mentioned in tool calls as additional context

**Step 2: LLM extraction (single Gemini call)**

Structured prompt requesting JSON output:

```json
{
  "facts": [
    {
      "text": "Chose SQLite over Postgres for zero-setup CLI experience",
      "type": "decision",
      "salience": 0.85,
      "files": ["store_sqlite.go"],
      "entities": [
        {"text": "SQLite", "type": "tool"},
        {"text": "Postgres", "type": "tool"}
      ]
    }
  ],
  "entity_triples": [
    {
      "source": {"text": "store_sqlite.go", "type": "file"},
      "relation": "implements",
      "target": {"text": "Store interface", "type": "concept"}
    }
  ],
  "session_summary": "Brief one-line summary of what was accomplished"
}
```

**Fact types:** `decision`, `warning`, `continuity`, `map`, `general` (matching existing memory types).

**Entity types:** `person`, `file`, `function`, `concept`, `tool`, `project` (extensible).

**Step 3: Deduplication**
- For each extracted fact, compute embedding via existing `EmbeddingProvider`
- Check against existing detail memories using `CosineSimilarity`
- Skip if max similarity > 0.90 (near-duplicate)
- If 0.80-0.90 similarity, check for contradiction potential (flag for future contradiction detection feature)

**Step 4: Write**
- Insert each fact via existing `AddWithOptions` path:
  - `Type` from extracted type
  - `Salience` from extracted salience (floored at 0.6 for distilled facts — they passed LLM filtering)
  - `Files` from extracted files
  - `Entities` from extracted per-fact entities
  - `SessionID` set to source session identifier
- Insert entity triples into graph tables (see Feature 2)

**Step 5: Post-distill synthesis check**
- If ≥3 new memories were added, auto-run `Synthesize` to update knowledge docs
- Respects existing staleness checks — won't re-synthesize docs that are still fresh

### CLI Interface

```bash
# Automatic (called by hook)
dualmem distill --auto

# Manual with auto-detected CC session
dualmem distill

# Explicit transcript file
dualmem distill --file /path/to/transcript.json

# From stdin (pipe from other harness)
cat transcript.json | dualmem distill --stdin

# Preview without writing
dualmem distill --dry-run

# JSON output (for programmatic use)
dualmem distill --json

# Override namespace
dualmem distill --ns "claude:myproject"
```

### Claude Code Hook Integration

Add to project's `.claude/settings.json`:

```json
{
  "hooks": {
    "Stop": [{
      "matcher": "",
      "command": "~/go/bin/dualmem distill --auto --ns \"claude:$(basename $(pwd))\" 2>/dev/null || true"
    }]
  }
}
```

The `|| true` ensures hook failure never blocks the session. The `--auto` flag handles idempotency (won't re-distill the same session).

### Gemini Integration

Reuse existing `GeminiSummarizer` (already in `dualmem/knowledge.go`) which wraps the Gemini API. Add a new method or extend `TextGenerator` interface:

```go
type TextGenerator interface {
    GenerateText(ctx context.Context, prompt string, maxTokens int) (string, error)
}
```

The distillation prompt goes through this same interface. No new API client needed.

### Safety & Failure Modes

- **Raw-first:** Never modify existing memories. Only add new ones.
- **Idempotent:** Track `last_distill_session_id` to prevent re-processing.
- **Fail-open:** If Gemini is unavailable, log error and exit cleanly. Next session can catch up.
- **Budget guard:** Cap at 20 extracted facts per session (like Signet). Prevents runaway costs.
- **`--dry-run`:** Preview extracted facts without writing.

---

## Feature 2: Entity Graph Layer

### Overview

Three new SQLite tables forming a lightweight knowledge graph. Entities are nodes, relationships are edges, and memory_entity_links connect memories to their entities. Enables graph-boosted retrieval in DualSearch.

### Schema

```sql
-- Schema version bump: v3 → v4

-- Canonical entity nodes
CREATE TABLE IF NOT EXISTS entity_nodes (
    id            TEXT PRIMARY KEY,
    name          TEXT NOT NULL,
    type          TEXT NOT NULL,
    namespace     TEXT NOT NULL,
    mention_count INTEGER NOT NULL DEFAULT 1,
    created_at    TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at    TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_entity_canonical
    ON entity_nodes(namespace, lower(name), type);

-- Relationships between entities
CREATE TABLE IF NOT EXISTS entity_edges (
    id          TEXT PRIMARY KEY,
    source_id   TEXT NOT NULL REFERENCES entity_nodes(id),
    target_id   TEXT NOT NULL REFERENCES entity_nodes(id),
    relation    TEXT NOT NULL,
    strength    REAL NOT NULL DEFAULT 1.0,
    mentions    INTEGER NOT NULL DEFAULT 1,
    namespace   TEXT NOT NULL,
    created_at  TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at  TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_edge_source ON entity_edges(source_id);
CREATE INDEX IF NOT EXISTS idx_edge_target ON entity_edges(target_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_edge_unique
    ON entity_edges(source_id, target_id, relation);

-- Memory-to-entity links (many-to-many)
CREATE TABLE IF NOT EXISTS memory_entity_links (
    memory_id TEXT NOT NULL,
    entity_id TEXT NOT NULL REFERENCES entity_nodes(id),
    namespace TEXT NOT NULL,
    PRIMARY KEY (memory_id, entity_id)
);
CREATE INDEX IF NOT EXISTS idx_mel_entity ON memory_entity_links(entity_id);
CREATE INDEX IF NOT EXISTS idx_mel_memory ON memory_entity_links(memory_id);
```

### Entity Operations

**UpsertEntity:**
- Match on `(namespace, lower(name), type)`
- If exists: increment `mention_count`, update `updated_at`
- If new: insert with `mention_count = 1`
- Return entity ID (used for edge/link creation)

**UpsertEdge:**
- Match on `(source_id, target_id, relation)`
- If exists: increment `mentions`, update `strength` via running average: `(strength * mentions + 1.0) / (mentions + 1)`, update `updated_at`
- If new: insert with `strength = 1.0, mentions = 1`

**LinkMemoryToEntity:**
- Insert into `memory_entity_links` (ignore on conflict)

### Write-Time Integration

**On `AddWithOptions` (existing path):**
1. Existing heuristic `EntityExtractor.Extract(content)` runs → returns `[]Entity`
2. For each entity: `UpsertEntity(namespace, entity.Text, entity.Type)` → get `entityID`
3. `LinkMemoryToEntity(memoryID, entityID)`
4. No edges created at this level (heuristics don't extract relationships)

**On `distill` (new path):**
1. LLM extraction produces entity triples with relationship strings
2. For each triple:
   - `UpsertEntity(source)` → `sourceID`
   - `UpsertEntity(target)` → `targetID`
   - `UpsertEdge(sourceID, targetID, relation)`
3. For each fact's entities: `LinkMemoryToEntity(factMemoryID, entityID)`

### Read-Time Integration — Graph Boost in DualSearch

**New optional phase in `DetailPath.Search`:**

1. Extract entities from query text (existing heuristic extractor)
2. Match against `entity_nodes` where `lower(name) LIKE '%' || lower(queryEntity) || '%'`
   - Cap: 20 matched entities, ordered by `mention_count DESC`
3. Expand 1 hop bidirectionally through `entity_edges`:
   ```sql
   SELECT DISTINCT target_id FROM entity_edges WHERE source_id IN (?)
   UNION
   SELECT DISTINCT source_id FROM entity_edges WHERE target_id IN (?)
   ```
   - Cap: 50 neighbor entities
4. Collect memory IDs via `memory_entity_links`:
   ```sql
   SELECT DISTINCT memory_id FROM memory_entity_links
   WHERE entity_id IN (?)
   ```
   - Cap: 200 memory IDs
5. Apply additive boost to matched memories' hybrid scores:
   - `adjustedScore = hybridScore + graphBoostWeight`
   - Default `graphBoostWeight = 0.15` (configurable)

**Performance guard:** Entire graph expansion bounded by 50ms deadline. On timeout, return partial results (like Signet's approach).

### AssembleContext Integration

In `AssembleContextWith`, after DualSearch returns results:
- Graph-boosted memories naturally rank higher
- No separate assembly step needed — the boost integrates into existing scoring

### CLI Commands

```bash
# View entity graph stats
dualmem entities --ns "claude:geoffreyengram"

# Search entities
dualmem entities search "SQLite" --ns "claude:geoffreyengram"

# Show entity relationships (1-hop)
dualmem entities show "store_sqlite.go" --ns "claude:geoffreyengram"

# List most-connected entities
dualmem entities top --limit 20 --ns "claude:geoffreyengram"
```

---

## Implementation Order

1. **Entity graph schema + store methods** — tables, UpsertEntity, UpsertEdge, LinkMemoryToEntity, graph expansion query
2. **Wire entity graph into AddWithOptions** — heuristic entities → graph on every add
3. **Distillation command** — transcript parsing, Gemini extraction, dedup, write
4. **Wire distillation entities into graph** — LLM triples → edges
5. **Graph boost in DualSearch** — 1-hop expansion, additive scoring
6. **Claude Code hook setup** — settings.json template, `--auto` flag
7. **Post-distill synthesis** — auto-run `synthesize` when ≥3 new memories

## Files to Modify

| File | Changes |
|------|---------|
| `dualmem/store_sqlite.go` | Schema v4 migration, entity CRUD methods |
| `dualmem/types.go` | EntityNode, EntityEdge, EntityLink types |
| `dualmem/dualmem.go` | Wire entity graph into AddWithOptions, graph boost in DualSearch |
| `dualmem/distill.go` | **New file** — transcript parsing, LLM extraction, dedup, write pipeline |
| `dualmem/detail.go` | Graph boost integration in Search |
| `cmd/dualmem/main.go` | `distill` and `entities` subcommands |
| `.claude/settings.json` | Hook template for post-session distillation |

## Testing Strategy

- **Unit tests** for entity CRUD (upsert, link, expansion queries)
- **Unit tests** for transcript parsing (CC format, stdin, edge cases)
- **Unit tests** for extraction prompt parsing (valid JSON, malformed JSON, empty sessions)
- **Integration test** for full distill pipeline (mock Gemini response → verify memories + entities written)
- **Integration test** for graph-boosted search (add memories with shared entities → verify boost affects ranking)
- **Benchmark** for graph expansion query (should be <50ms for 1000 entities)

## Follow-Up Features (Not In This Spec)

- **Memory supersede semantics** — `superseded_by` column, explicit marking on update
- **Predictive scoring / regret signals** — log injected memories, correlate with session file activity
- **Contradiction detection** — syntactic fast path (negation/antonyms) + LLM semantic check during distillation
- **Hub dampening** — penalize entities above P90 mention count (prevents common entities from dominating)
- **Aspect hierarchy** — classify entity attributes vs. constraints (Signet's structural classification)
