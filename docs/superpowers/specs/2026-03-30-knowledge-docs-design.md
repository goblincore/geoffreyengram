# Knowledge Documents — Design Spec

**Status:** Draft
**Date:** 2026-03-30

## Context

DualMem's current context assembly produces a flat list of individual memory entries — each a point-in-time snapshot. While the capture and search are sophisticated (HDC embeddings, hybrid scoring, intent-aware ranking), the *output* reads like a grab bag rather than a curated guide.

By contrast, hand-written AGENTS.md files (like the one in LearnCard) are immediately effective: they explain *how systems work* in coherent prose, organized by concept. The problem is they require manual maintenance.

**Goal:** Add a **knowledge document** layer that automatically synthesizes related raw memories into coherent, concept-oriented documents — giving the quality of curated AGENTS.md files with the automation of dualmem's capture system.

## Design

### What is a Knowledge Document?

A knowledge doc is a coherent, concept-oriented document synthesized by an LLM from related raw memories. It explains *how something works* rather than listing *what happened*.

**Example transformation — 5 raw memories:**
- "HDC encoder uses 2048-dim vectors"
- "Content layer extracts identifiers and imports"
- "FNV-64+PCG for basis vectors"
- "splitCamelCase uses regexp (Go has no lookahead)"
- "4-layer architecture: path/symbols/lang/content"

**Become one knowledge doc ("HDC Encoder"):**
> The HDC encoder produces 2048-dimensional binary vectors using a 4-layer architecture (path, symbols, language, content). Basis vectors are generated deterministically via FNV-64 hashing + PCG PRNG. The content layer extracts identifiers and import paths from source code. Note: splitCamelCase uses regexp because Go doesn't support lookahead assertions.

Knowledge docs don't replace raw memories — they're a *view* on top. Raw memories remain the source of truth and are still independently searchable.

### Storage Schema

New SQLite table in the existing `memories.db`:

```sql
CREATE TABLE knowledge_docs (
    id              TEXT PRIMARY KEY,
    namespace       TEXT NOT NULL,
    topic           TEXT NOT NULL,        -- short identifier: "hdc-encoder", "auth-middleware"
    content         TEXT NOT NULL,        -- synthesized markdown prose
    files_json      TEXT,                 -- JSON array of associated file paths
    source_ids_json TEXT,                 -- JSON array of detail memory IDs that were synthesized
    embedding       BLOB,                 -- 768-dim vector for relevance ranking
    token_count     INTEGER DEFAULT 0,
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL,
    UNIQUE(namespace, topic)
);
CREATE INDEX idx_kdocs_ns ON knowledge_docs(namespace);
```

**Relationship to existing tables:** `source_ids_json` references `detail_memories.id`. When a detail memory is evicted to sketch, the knowledge doc persists — it's already synthesized and self-contained.

### Synthesis Process

#### Clustering

When synthesis runs, memories are grouped into topics by:

1. **Existing doc matching** — new memories are first matched against existing doc topics via embedding similarity + file overlap. If a memory's files overlap with an existing doc's files, or its embedding is similar (cosine > 0.6), it's assigned to that doc.
2. **File overlap** — remaining unmatched memories sharing ≥2 files are clustered together.
3. **Semantic similarity** — remaining memories with embedding cosine > 0.7 are clustered.
4. **Singletons** — memories that don't cluster stay as individual entries (served as "recent unsynthesized" in context).

Minimum cluster size for doc creation: **3 memories**. Below that, they stay as raw entries.

#### LLM Synthesis

Uses the existing `TextGenerator` interface (`dualmem/dualmem.go:1212`):

```go
type TextGenerator interface {
    GenerateText(ctx context.Context, prompt string, maxTokens int) (string, error)
}
```

Already implemented by `GeminiSummarizer` and used by `SeedMemories`. The synthesis prompt pattern:

```
You are writing a knowledge document for a software project. Given these
related memories, write a concise document explaining this system/concept.

Topic: {auto-detected or existing topic name}
Associated files: {file list}

Memories:
{numbered list of memory texts}

Guidelines:
- Focus on how things work, architectural decisions, and constraints
- Include any warnings about code that shouldn't be changed
- Write as if explaining to a developer joining the project
- Be concise — aim for 100-300 words
- Use markdown formatting where helpful
```

Max tokens per doc: **500** (keeps docs concise, ~100-300 words).

#### When Synthesis Happens

1. **Explicit command:** `dualmem synthesize` — scans all memories, creates/updates docs.
2. **Staleness detection:** During `AssembleContext`, if any doc has source memories that were updated since `doc.updated_at`, or if ≥5 new memories exist that aren't covered by any doc, mark as stale. Stale docs are re-synthesized on the spot (adds latency but ensures freshness).
3. **Topic-specific:** `dualmem synthesize --topic "hdc-encoder"` — re-synthesize one doc.
4. **Force:** `dualmem synthesize --force` — re-synthesize all docs regardless of staleness.

#### Update Logic

When new memories arrive for an existing topic:
- The new memory IDs are added to `source_ids_json`
- The doc is marked stale (staleness = source memories newer than `updated_at`)
- On next synthesis (explicit or lazy), the full set of source memories is re-fed to the LLM
- The old doc content is replaced entirely (no incremental append)

### Context Assembly Changes

**New assembly order in `AssembleContextWith`:**

1. **Structural diff** (≤150 tokens) — unchanged
2. **Code map** (≤400 tokens) — unchanged
3. **Checkpoints** (pinned, highest priority) — unchanged
4. **Knowledge docs** (NEW) — ranked by query relevance, rendered as `[Knowledge: Topic]` sections
5. **Recent unsynthesized memories** (NEW) — only memories not covered by any doc, from last few sessions
6. Profile/arcs/episodes — fill remaining budget (lower priority)

**Knowledge doc rendering:**
```
[Knowledge: HDC Encoder]
The HDC encoder produces 2048-dimensional binary vectors using a 4-layer
architecture (path, symbols, language, content). Basis vectors are generated
deterministically via FNV-64 hashing + PCG PRNG...
  Files: dualmem/hdc.go, dualmem/codemap.go
```

**Ranking:** Knowledge docs are ranked by the same hybrid scoring used for detail memories — cosine similarity to query embedding + keyword overlap. Intent-aware multipliers apply (debug intent boosts docs containing warnings, etc.).

**Token budget allocation:** Knowledge docs get priority after checkpoints. Budget split:
- Diff: ≤150
- Codemap: ≤400
- Checkpoints: unlimited (they're critical)
- Knowledge docs: up to 60% of remaining budget
- Recent unsynthesized: up to 30% of remaining budget
- Sketch (arcs/episodes/profile): remaining

### CLI Interface

**New commands:**

```bash
# Synthesize: cluster memories into knowledge docs
dualmem synthesize                      # create/update all stale docs
dualmem synthesize --force              # re-synthesize all docs
dualmem synthesize --topic "auth"       # re-synthesize a specific doc
dualmem synthesize --dry-run            # show what would be created/updated

# Browse knowledge docs
dualmem docs                            # list all docs: topic, token count, freshness, source count
dualmem docs show "hdc-encoder"         # print a specific doc's content
dualmem docs export [--dir path]        # export all docs as markdown files
dualmem docs delete "old-topic"         # remove a doc (source memories kept)
dualmem docs rename "old" "new"         # rename a topic
```

**Enhanced existing commands:**

```bash
# context now serves knowledge docs as primary content
dualmem context "query" --budget 3000

# add is unchanged, but new memories trigger staleness checks on related docs
dualmem add --text "..." --files "auth.go"
```

### Go Types

```go
// KnowledgeDoc is a synthesized concept document.
type KnowledgeDoc struct {
    ID            string    `json:"id"`
    Namespace     string    `json:"namespace"`
    Topic         string    `json:"topic"`
    Content       string    `json:"content"`        // synthesized markdown
    Files         []string  `json:"files"`          // associated file paths
    SourceIDs     []string  `json:"source_ids"`     // detail memory IDs
    Embedding     []float32 `json:"embedding"`
    TokenCount    int       `json:"token_count"`
    CreatedAt     time.Time `json:"created_at"`
    UpdatedAt     time.Time `json:"updated_at"`
}

// SynthesizeResult describes what Synthesize produced.
type SynthesizeResult struct {
    Created  []KnowledgeDoc // newly created docs
    Updated  []KnowledgeDoc // re-synthesized existing docs
    Skipped  int            // docs already fresh
    Orphaned int            // memories that didn't cluster
    Warnings []string
    DryRun   bool
}
```

### Engine Methods

```go
// Synthesize clusters memories and creates/updates knowledge docs.
func (e *Engine) Synthesize(ctx context.Context, namespace string, opts *SynthesizeOpts) (*SynthesizeResult, error)

// GetKnowledgeDocs returns docs ranked by relevance to query.
func (e *Engine) GetKnowledgeDocs(ctx context.Context, namespace string, query string, queryEmb []float32, limit int) ([]KnowledgeDoc, error)

// ListKnowledgeDocs returns all docs for a namespace (for CLI listing).
func (e *Engine) ListKnowledgeDocs(ctx context.Context, namespace string) ([]KnowledgeDoc, error)

// DeleteKnowledgeDoc removes a doc by topic (source memories preserved).
func (e *Engine) DeleteKnowledgeDoc(ctx context.Context, namespace string, topic string) error
```

### Store Interface Additions

```go
// Added to the Store interface:
UpsertKnowledgeDoc(doc *KnowledgeDoc) error
GetKnowledgeDocs(namespace string) ([]KnowledgeDoc, error)
GetKnowledgeDocByTopic(namespace, topic string) (*KnowledgeDoc, error)
DeleteKnowledgeDoc(namespace, topic string) error
GetUncoveredMemories(namespace string, coveredIDs []string) ([]DetailMemory, error)
```

## Files to Modify

| File | Change |
|------|--------|
| `dualmem/types.go` | Add `KnowledgeDoc`, `SynthesizeResult`, `SynthesizeOpts` types |
| `dualmem/store_sqlite.go` | Add `knowledge_docs` table, CRUD methods, migration |
| `dualmem/knowledge.go` | NEW — synthesis logic: clustering, LLM calls, staleness detection |
| `dualmem/dualmem.go` | Add `Synthesize`, `GetKnowledgeDocs`, `ListKnowledgeDocs`, `DeleteKnowledgeDoc` methods; update `AssembleContextWith` to serve docs |
| `cmd/dualmem/main.go` | Add `synthesize`, `docs` CLI subcommands |
| `dualmem/knowledge_test.go` | NEW — tests for clustering, synthesis, context integration |

## Verification

### Unit Tests
- Clustering: given N memories with file overlaps and similar embeddings, verify correct grouping
- Synthesis prompt: verify correct prompt construction from memory cluster
- Staleness detection: add memory, verify doc marked stale
- Context assembly: verify knowledge docs appear before raw memories, with correct headers
- Budget allocation: verify knowledge docs don't exceed 60% of remaining budget
- Edge cases: empty namespace, single memory (no doc created), doc with deleted source memories

### Integration Tests
- End-to-end: `add` 5+ related memories → `synthesize` → `context` serves coherent doc
- Update flow: add new memory to existing topic → re-synthesize → verify doc updated
- CLI: test `synthesize`, `docs`, `docs show`, `docs delete` commands

### Manual Verification
```bash
# Add several related memories
dualmem add --text "Auth uses JWT in middleware.go" --files "middleware.go"
dualmem add --text "Refresh tokens stored in Redis, 7-day TTL" --files "auth.go,middleware.go"
dualmem add --text "Warning: rate limiter skips nil check intentionally" --files "middleware.go" --type warning
dualmem add --text "Auth refactor: moved validation from handler to middleware" --files "auth.go,middleware.go" --type decision

# Synthesize
dualmem synthesize

# Verify doc was created
dualmem docs
dualmem docs show "auth-middleware"  # should read as coherent prose

# Verify context assembly
dualmem context "how does auth work" --budget 3000
# Should show [Knowledge: Auth Middleware] section, not individual memory entries
```
