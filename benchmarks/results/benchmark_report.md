# dualmem vs claude-mem: Head-to-Head Benchmark Results

Generated: 2026-04-07 21:11

## Executive Summary

| Dimension | dualmem | claude-mem | Notes |
|-----------|---------|------------|-------|
| Write speed | 0.3 writes/s (3.4s/write) | 775 writes/s (1ms/write) | dualmem embeds at write time; claude-mem defers embedding to ChromaDB sync |
| **Search recall@10** | **0.558** | 0.00 (no ChromaDB) | dualmem wins decisively; claude-mem can't do semantic search without ChromaDB |
| Search latency | ~2100ms | 2ms | dualmem: Gemini API embed + cosine. claude-mem: SQLite filter only |
| Context assembly | ~2400ms, ~720 tokens | 2ms, ~4300 tokens | dualmem: query-aware, relevant. claude-mem: returns everything unchanged |
| Code search | 201ms, 100% hit rate | N/A | Unique dualmem capability |
| DB size | 80MB (shared, all projects) | 296KB | dualmem stores 768d vectors inline; claude-mem delegates vectors to ChromaDB |

**Important caveats**: This is NOT an apples-to-apples comparison. The two systems have fundamentally different architectures that make direct speed comparisons misleading. See analysis below.

---

## Why The Raw Numbers Are Misleading

### Write Performance (3.4s vs 1ms)

**dualmem** embeds each memory at write time via the Gemini API (768d vector, ~500ms network round-trip), classifies sector, scores importance, and checks cosine novelty against existing memories. This is the full indexing pipeline.

**claude-mem** stores raw text to SQLite and returns immediately. Embedding is deferred to ChromaDB sync, which happens asynchronously via MCP. The "1ms write" only captures the SQLite INSERT, not the full indexing cost.

**Real comparison**: dualmem trades write latency for instant search readiness. claude-mem trades search capability for instant writes. Different tradeoffs, not better/worse.

### Search Quality (0.558 vs 0.00) -- CORRECTED

After fixing a tilde-expansion bug in config loading and a Go flag-ordering issue:

- **dualmem**: Average Recall@10 = **0.558** across 20 queries. Best: 1.00 on "storage and file uploads", "webhook and event processing", "what frontend technologies are we using". Weakest: 0.00 on "data pipeline and analytics", "what still needs to be done for mobile". 79 of 100 memories were stored in Detail (21 routed to Sketch via novelty dedup).
- **claude-mem**: 0.00 recall — without ChromaDB, the `/api/search` endpoint only supports metadata filtering, not text-based semantic search.

**Key insight**: dualmem's hybrid HDC+BM25 search with Gemini embeddings delivers strong semantic recall. claude-mem requires an external vector DB (ChromaDB) for equivalent functionality.

### DB Size (80MB vs 296KB)

dualmem's 80MB is a **shared database** containing all project memories (not just the 100 benchmark memories), plus 768d float32 vectors stored inline (3KB per vector). claude-mem's 296KB contains only the 100 benchmark observations with no vectors (ChromaDB stores those separately).

Fair comparison would be: dualmem's 100 memories × ~4KB (text + vector) = ~400KB raw data, vs claude-mem's 296KB (text only, no vectors).

### Context Assembly (2.4s vs 2ms) -- CORRECTED

**dualmem** embeds the query via Gemini API (~500ms), then runs 7-tier budget-aware assembly with intent detection. The output varies by query — payment queries get different context than infrastructure queries. Token usage ranged from **595-962** across scenarios (average ~720), staying well within the 3000-token budget and delivering only relevant content.

**claude-mem** returned the same 4338 tokens for every query — it's just fetching the 30 most recent observations, unranked and unfiltered. This is their "observations list" endpoint, not their full context injection pipeline (which runs as a Claude Code hook, not via API).

**Key insight**: dualmem uses ~6x fewer tokens than claude-mem while delivering query-relevant context. Less noise = better LLM performance.

---

## Benchmark 4: Code Search (dualmem exclusive)

This is dualmem's unique advantage. claude-mem has no code search — users rely on Claude's built-in grep/glob tools.

| Query | Latency | Top Result |
|-------|---------|------------|
| embedding provider gemini API key | 212ms | providers.go (score=0.60) |
| entity graph waypoints associative | 195ms | dualmem/bench_scenarios.go (0.47) |
| HDC vector encoding basis rotation | 202ms | store.go (0.51) |
| importance scoring novelty threshold | 180ms | types.go (0.47) |
| context assembly budget progressive | 190ms | dualmem/bench_scenarios.go (0.42) |
| sketch compression episodes arcs | 235ms | dualmem/pipeline.go (0.46) |
| sqlite store migration schema | 201ms | store.go (0.54) |
| MCP tool handler registration | 197ms | cmd/engram-mcp/main.go (0.48) |

**100% hit rate, 201ms average** — finds relevant code modules by natural language without any API calls (pure local HDC+BM25).

---

## Architecture Comparison

| Feature | dualmem | claude-mem |
|---|---|---|
| **Language** | Go (38MB binary, ~5ms startup) | TypeScript (Bun runtime, ~2s startup) |
| **Storage** | SQLite (shared, WAL) | SQLite (WAL) + ChromaDB (optional) |
| **Embedding** | Gemini 768d at write time (inline) | Deferred to ChromaDB via MCP |
| **Search** | Hybrid cosine+BM25 (adaptive alpha) | 3-strategy orchestrator (Chroma/SQLite/Hybrid) |
| **Memory compression** | 3-tier: raw → episodes → arcs → profile | Observation summaries |
| **Code search** | HDC 2048d + BM25 on tree-sitter AST | None |
| **Knowledge synthesis** | LLM clustering → knowledge docs | Session summaries |
| **Memory types** | decision/warning/continuity/checkpoint/trace/seed | decision/bugfix/feature/refactor/discovery/change |
| **Context** | 7-tier budget-aware + intent detection | Progressive disclosure (count-based) |
| **Privacy** | None | `<private>` tag exclusion |
| **Web UI** | dispatch-ui (task dispatch) | Memory viewer (localhost:37777) |
| **Token economics** | None | ROI tracking (discovery vs read tokens) |
| **Deduplication** | Cosine novelty scoring (semantic) | SHA256 content hash (exact, 30s window) |
| **Hook integration** | Pre-hook + post-hook (shell scripts) | 5 lifecycle hooks (session start/end, tool use, etc.) |
| **Multi-IDE** | Claude Code only | Claude Code, Gemini CLI, Cursor, Codex |

## dualmem Strengths

1. **Code search** — unique capability, no equivalent in claude-mem. HDC+BM25 hybrid finds modules by natural language in ~200ms with zero API calls.
2. **Semantic deduplication** — cosine novelty scoring prevents semantically similar memories from cluttering the store. claude-mem's hash-based dedup only catches exact duplicates within a 30s window.
3. **Hierarchical compression** — 3-tier sketch path (episodes → arcs → profile) preserves information at decreasing fidelity. claude-mem only has session summaries.
4. **Budget-aware context** — 7-tier assembly with token budget allocation and intent detection. Different queries produce different context. claude-mem returns the same observations regardless of query.
5. **Knowledge synthesis** — LLM-powered clustering produces reusable concept documents. claude-mem has nothing equivalent.
6. **Zero-overhead CLI** — Go binary starts in ~5ms with no runtime dependencies. claude-mem requires Bun + worker process.

## claude-mem Strengths

1. **Instant writes** — deferred embedding means writes are instant (1ms). Better UX for the hook-driven observation pipeline.
2. **Multi-IDE support** — works with Claude Code, Gemini CLI, Cursor, Codex, OpenClaw. dualmem is Claude Code only.
3. **Privacy controls** — `<private>` tags let users exclude sensitive content. dualmem has no equivalent.
4. **Token economics** — tracks discovery_tokens (cost to create a memory) vs read_tokens (cost to retrieve), showing ROI. Nice UX feature.
5. **Web memory viewer** — real-time UI at localhost:37777 for browsing memories, timeline, search. Much better visibility into what's stored.
6. **Richer observation types** — bugfix/feature/refactor/discovery/change captures more nuance than our decision/warning/continuity.
7. **Session lifecycle hooks** — 5 hook points (session start, user prompt, post-tool-use, summarize, session end) vs our 2 (pre/post).
8. **Content-addressed dedup** — simpler implementation, zero-cost (no embedding needed).

---

## Features Worth Adopting from claude-mem

### Priority 1: Should Implement

1. **Privacy tags** (`<private>`) — exclude sensitive content from storage. Simple to add: check for tag in AddWithOptions before storing. Low effort, high value for enterprise users.

2. **Token economics tracking** — add `discovery_tokens` and `read_tokens` fields to detail memories. Track how many tokens were spent discovering something vs how many tokens it saves in context assembly. Display in `dualmem status`.

3. **Web memory viewer** — add a `/memories` page to dispatch-ui or a standalone viewer. Show timeline, search, type filtering. claude-mem's viewer at 37777 is genuinely useful for debugging what got stored.

### Priority 2: Consider

4. **Richer memory types** — add `bugfix`, `feature`, `refactor`, `discovery` types alongside our existing set. These map well to development workflows and enable better filtering.

5. **Content hash dedup layer** — add SHA256 hash check BEFORE cosine novelty scoring. Catches exact duplicates immediately (0 cost) before falling through to the expensive embedding path.

6. **Automatic session summaries** — trigger distill automatically at session end (via hook) instead of requiring manual invocation. claude-mem does this via their session-end hook.

### Priority 3: Nice to Have

7. **More lifecycle hooks** — post-tool-use observation capture (what files were read/modified) could feed richer file context.

8. **Multi-IDE plugin packaging** — claude-mem's `.claude-plugin`, `.codex-plugin`, `.windsurf/rules` structure for multi-IDE support.

---

## Bugs Found During Benchmarking

Two dualmem bugs were discovered and fixed during this benchmark:

1. **Tilde expansion in config** (`cmd/dualmem/main.go`): The YAML config had `sqlite_path: ~/.local/share/dualmem/memories.db` but Go doesn't expand `~`. Writes went to `./~/.local/...` (relative path in cwd) instead of home directory. Fixed by adding tilde expansion in `loadConfig()`.

2. **PRAGMA DSN params** (`dualmem/store_sqlite.go`): `sql.Open("sqlite", path+"?_journal_mode=WAL&_busy_timeout=5000")` — modernc.org/sqlite doesn't support DSN query params. Fixed by setting PRAGMAs separately after open.

## Benchmark Methodology Notes

- dualmem uses real Gemini Embedding 002 (768d) for all operations
- claude-mem worker ran without ChromaDB (SQLite-only mode)
- 100-memory corpus of synthetic project memories covering decisions, warnings, and continuity items
- 20 search queries with hand-labeled ground truth
- Recall measured by substring matching of first 30 chars of expected memory text in search output
- dualmem stored 79/100 memories in Detail path (21 routed to Sketch via novelty dedup)
- claude-mem stored all 100 as observations (200 total due to earlier benchmark run)
- Both systems used localhost SQLite — no network latency for storage
