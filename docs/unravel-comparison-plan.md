# UnravelAI Comparison & Implementation Plan

## Overview

Deep comparison of [UnravelAI](https://github.com/EruditeCoder108/unravelai) (cloned to `/tmp/unravelai`) with geoffreyengram's dualmem. Goal: identify ideas worth adopting for contextual codebase understanding, build a `consult` command, and benchmark both systems.

---

## Architecture Comparison

| Dimension | GeoffreyEngram (dualmem) | UnravelAI |
|-----------|------------------------|-----------|
| **Primary goal** | Cross-session memory + codebase awareness for any agent task | Debugging-specific "evidence engine" with anti-hallucination guardrails |
| **Code representation** | HDC vectors (2048-dim, 4-layer: path/symbols/lang/content) — deterministic, no API | Knowledge Graph (nodes=files/functions/classes, edges=imports/calls/mutates) + optional 768-dim Gemini embeddings |
| **Search** | Hybrid HDC+BM25 with adaptive alpha blending (~45us) | Weighted multi-hop graph traversal with beam search + keyword scoring |
| **Parsing** | tree-sitter (TS/Python/Rust) + go/ast; extracts types, entries, imports, identifiers | tree-sitter (JS/TS) + regex fallback; extracts mutations, closures, async boundaries, call graphs |
| **Context assembly** | Hierarchical token-budgeted (diff->codemap->checkpoints->knowledge->details->episodes) | Three-layer static (server desc -> per-call instructions -> task codex) |
| **Memory** | SQLite-backed with importance scoring, dual-path routing, knowledge doc synthesis | Pattern store (learnable bug signatures) + diagnosis archive (embedded) + task codex (structured notes) |
| **Structured query tool** | `context` CLI (budget-aware, intent-detected) | `consult` MCP tool (4-section intelligence report) |
| **API calls for search** | Zero (HDC is deterministic) | Optional (Gemini embeddings improve graph traversal) |
| **Language support** | Go, TS, Python, Rust | JS/TS only |

---

## Key Unravel Files to Study

| Component | File Path | Key Functions |
|-----------|-----------|---|
| **consult tool** | `unravel-mcp/index.js:2367-3351` | Tool definition + `formatConsultForAgent()` at line 2712 |
| **Knowledge Graph** | `unravel-mcp/core/graph-builder.js` | `GraphBuilder` class, node/edge construction, `addFileWithAnalysis()` |
| **Graph Search** | `unravel-mcp/core/search.js` | `SearchEngine`, `expandWeighted()` (line 130), `queryGraphForFiles()` |
| **AST Engine** | `unravel-mcp/core/ast-engine-ts.js` | `parseCode()`, mutation/closure/async detectors |
| **Cross-File** | `unravel-mcp/core/ast-project.js` | `buildModuleMap()`, `resolveSymbolOrigins()`, `runCrossFileAnalysis()` |
| **Pattern Store** | `unravel-mcp/core/pattern-store.js` | 20 bug signatures, `matchPatterns()`, `learnFromDiagnosis()` |
| **Embeddings** | `unravel-mcp/embedding.js` | Gemini Embedding 2, `archiveDiagnosis()`, `searchDiagnosisArchive()` |
| **Orchestration** | `unravel-mcp/core/orchestrate.js:547-598` | Consult mode short-circuit, CONSULT_INSTRUCTIONS |

---

## The `consult` Intelligence Report

Unravel's consult fires all intelligence layers simultaneously and returns a 4-section JSON:

### Section 1: `intelligence_brief`
- **Query classification** — regex-based: factual ("where is"), analytical (default), feasibility ("can I", "what would break")
- **Reasoning mandate** — tiered instructions per query type telling the LLM HOW to reason
- **Project overview** — loaded from `.unravel/project-overview.md` (auto-generated on first build_map)
- **Intelligence score** — readiness metric: KG nodes, codex matches, archive hits, files analyzed
- **Structural scope** — files in AST scope vs. files in KG but not analyzed

### Section 2: `structural_evidence`
- **AST facts** — mutation chains, closures, async boundaries (formatted text from orchestrate)
- **Critical source snippets** — auto-extracted +/-3 lines around AST-flagged sites (max 8 snippets)
- **Cross-file call graph** — sorted by query keyword relevance, not alphabetically
- **Symbol origins** — where imported symbols are defined

### Section 3: `memory`
- **Pattern signals** — matched bug signatures with confidence scores
- **Codex pre-briefing** — past task discoveries matching the query
- **Diagnosis archive** — past verified fixes found via semantic embedding (>=75% cosine)

### Section 4: `project_context`
- **Dependencies** — from package.json (runtime, dev, engines)
- **Git activity** — scope-filtered (only changes in analyzed files), hotspots, recent commits
- **Context files** — README, docs, CLAUDE.md — section-extracted (not full dump), max 2500 chars each

### Key Design Choices
- **Reading order** — explicitly instructed: brief -> evidence -> memory -> context
- **Inline source snippets** — reduces tool-call round trips (agent doesn't need view_file for critical locations)
- **Query-sorted call graph** — edges mentioning query keywords surface first
- **Section-extracted context files** — relevant sections only, not full file dumps
- **Intelligence score** — tells agent how much to trust the evidence

---

## Ideas to Adopt (Prioritized)

### 1. Structural Graph with Typed Edges (HIGH VALUE)

**What:** Build a graph where edges have types (calls, imports, mutates, contains) with weights.

**Why:** Our co-change graph knows *what* files relate but not *why*. A call edge is more actionable than "these changed together".

**Implementation:**
- New file: `dualmem/structural_graph.go` — types + extraction logic
- Extend tree-sitter to extract:
  - **Call sites**: function calls with callee name, line number, enclosing function
  - **Import resolution**: map imported symbols to source module
- Go: use `go/ast.Inspect` to walk function bodies for `*ast.CallExpr`
- TS/JS: tree-sitter query `(call_expression function: (identifier) @call)`
- Python: `(call function: (identifier) @call)`
- Rust: `(call_expression function: (identifier) @call)`
- SQLite migration v10: `structural_edges` table
- Edge weights (from Unravel's empirical tuning): calls=1.0, mutates=0.95, imports=0.7, contains=0.5

**gotreesitter API notes:**
- `node.StartByte()`/`node.EndByte()` for text extraction (NOT `StartPosition`)
- `node.ChildByFieldName("name", lang)` takes TWO args (name + language pointer)
- `node.Parent()` works as expected
- `node.Type(lang)` takes the language pointer

**Files to modify:**
- New: `dualmem/structural_graph.go`
- `dualmem/store_sqlite.go` — migration v10 + storage methods
- `dualmem/parse_treesitter.go` — (optional) could share extraction logic

### 2. Weighted Multi-Hop Traversal (HIGH VALUE)

**What:** Replace flat `computeGraphBoost()` with Unravel's blended scoring formula.

**Algorithm (from search.js line 163):**
```
score = parentScore * 0.7 + edgeWeight * 0.3 + semanticBonus
```
- Priority queue with beam search: top-5 paths per hop, prune below 0.2
- Edge weights from structural graph
- Semantic bonus: HDC cosine similarity * 0.4
- Max 2 hops (configurable)
- **Key insight:** blended (not multiplicative) scoring survives 2+ hops without collapse

**Current state:** `computeGraphBoost()` at `dualmem/dualmem.go:420` uses:
- Direct entity match -> boost = 0.20 (flat)
- Expanded neighbor -> boost = 0.10 * edgeTypeMultiplier * strength
- `edgeTypeMultiplier`: depends_on=1.0, implements=0.9, modifies=0.8, uses=0.7, relates=0.5

**Files to modify:**
- `dualmem/dualmem.go` -> `computeGraphBoost()` — add structural graph layer
- `dualmem/codemap.go` -> `RenderAtBudgetWithCoChange()` — integrate structural neighbors

### 3. `consult` Command (HIGH VALUE)

**What:** A new `dualmem consult "<query>"` CLI command returning a 4-section structured intelligence report.

**Report structure (adapted for dualmem):**
```
1. intelligence_brief
   - Query classification (reuse DetectIntent: debug/continue/feature/explore)
   - Confidence score (HDC spread + memory match count + graph coverage)
   - Scope: modules in analysis, modules NOT analyzed
   - Reasoning mandate (tiered per intent)

2. structural_evidence
   - Code map (HDC-ranked modules — existing)
   - Call graph excerpt (NEW — from structural graph, query-sorted)
   - Co-change neighbors (existing)
   - Critical source snippets (NEW — auto-extract +/-3 lines around important sites)

3. memory
   - Knowledge docs (existing — synthesized concept summaries)
   - Relevant detail memories (existing — intent-weighted)
   - Warnings (existing — surfaced first)

4. project_context
   - Git diff / structural diff (existing)
   - Checkpoints (existing)
   - Recent episodes (existing)
```

**Key differences from Unravel:**
- No API calls — HDC + BM25 for ranking
- Budget-aware — still respects token budget
- Cross-session memory — persists (Unravel's is session-scoped)
- Multi-language — Go, TS, Python, Rust

**Implementation:**
- New function: `Engine.Consult(ctx, query, opts)` in `dualmem/dualmem.go`
- Internally calls `AssembleContextWith()` components but restructures output as JSON with 4 sections
- New CLI subcommand: `dualmem consult "<query>" [--budget N] [--include path1,path2]`
- New MCP tool: `consult` in `cmd/dualmem-mcp/main.go`

### 4. Bug Pattern Database (MEDIUM VALUE)

**What:** Structural bug signatures detected via AST, with learnable weights.

**Unravel's patterns (from pattern-store.js):**
- Race condition: write_shared -> await_boundary -> read_shared (CWE-362, 0.95)
- Stale closure: closure_capture -> async_delay -> access (CWE-416, 0.85)
- forEach mutation: iterate -> mutate_collection (CWE-362, 0.90)
- Orphan listener: addEventListener without removeEventListener (CWE-401, 0.88)
- Weight learning: +0.05 on PASSED, -0.03 on REJECTED (asymmetric)

**How to adopt:** Store as typed memories (`--type warning`). Learning maps to importance scoring adjustments.

### 5. Diagnosis Archive (MEDIUM VALUE)

**What:** Embed symptom+resolution pairs. On next query, cosine-compare against all past resolutions.

**How it differs from knowledge docs:** They link symptoms -> resolutions explicitly. Our knowledge docs cluster by concept, not by problem->solution.

---

## Benchmark Plan

### Corpus
- geoffreyengram (Go, ~30 modules)
- unravelai (TypeScript, ~18 core files)
- One Python project (TBD)

### Query Types (10-15 per corpus)
1. Feature location: "Where is the authentication logic?"
2. Dependency tracing: "What files would break if I change the database schema?"
3. Concept search: "How does caching work in this project?"
4. Bug localization: "The API returns stale data after updates"
5. Structural understanding: "What's the request lifecycle from HTTP to database?"
6. Feasibility: "Can I add a new parser without touching the search layer?"

### Metrics
- Precision@K (K=3, 5, 10)
- Recall@K
- NDCG@10
- Latency (wall clock)
- API cost (external API calls)
- Report quality (LLM-as-judge for consult vs context output)

### Implementation
- `benchmarks/contextual/queries.json` — ground truth
- `benchmarks/contextual/bench_dualmem.go` — dualmem adapter
- `benchmarks/contextual/bench_unravel.js` — unravel adapter (import search.js, graph-builder.js)
- `benchmarks/contextual/compare.go` — aggregation + table output

---

## Execution Order

1. **Phase 1: Structural graph** — tree-sitter call graph + typed edges + SQLite migration v10
2. **Phase 2: Weighted traversal** — blended multi-hop in `computeGraphBoost()`
3. **Phase 3: `consult` command** — 4-section intelligence report
4. **Phase 4: Benchmark** — compare both systems, measure impact

---

## Implementation Code (Drafted, Not Applied)

The structural_graph.go and store_sqlite.go changes were drafted and tested against the compiler but reverted because we're on the `dispatch/compare-glm-health-v2` test branch. Key learnings from the draft:

- gotreesitter API: `ChildByFieldName(name, lang)` requires TWO args
- gotreesitter API: use `StartByte()`/`EndByte()` for position, NOT `StartPosition()`
- Call site extraction works for all 4 languages but needs enclosing-function detection
  - Go: `ast.Inspect` on `*ast.FuncDecl.Body` for `*ast.CallExpr`
  - TS/JS: tree-sitter `(call_expression function: (identifier) @call)` + walk up for enclosing function
  - Python/Rust: similar tree-sitter queries with language-specific node types
- SQLite migration v10 adds `structural_edges` table with indices on (namespace, source_path), (namespace, target_path), (namespace, edge_type)
