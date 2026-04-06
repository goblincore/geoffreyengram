# Consult Command — Design Spec

**Date:** 2026-04-06
**Phase:** 3 of Unravel Comparison Plan
**Depends on:** Phase 1 (structural graph) + Phase 2 (weighted traversal)

---

## Problem

An agent encounters a concept mid-task ("Families", "credential issuance", "auth middleware") and needs a semantically complete explanation of how that subsystem works — not just file pointers, but the actual flow, key abstractions, and design decisions. The existing `context` command provides session-start orientation (codemap + ranked memories) but doesn't answer "explain how X works" with structural depth or narrative coherence.

## Solution

`dualmem consult "how does auth work?"` — a lazy-synthesis intelligence command that:

1. Checks for a cached knowledge doc matching the query
2. If found (≥0.75 cosine), serves it with structural supplement
3. If not found, gathers structural evidence, calls Gemini 2.5 Flash to synthesize a narrative explanation, saves it as a knowledge doc, and returns it

The result is a 3-section structured report: Explanation, Structural Evidence, Relevant Files.

## How It Differs from `context`

| Dimension | `context` | `consult` |
|-----------|-----------|-----------|
| **Purpose** | Session-start orientation | Mid-session "explain this subsystem" |
| **Output** | Flat token-budgeted stream | 3-section structured report |
| **Synthesis** | None (raw memories + codemap) | LLM-synthesized narrative explanation |
| **Caching** | Snapshot for rating | Knowledge doc for reuse |
| **API calls** | Zero (HDC + embedding) | Gemini Flash when cache misses |
| **Confidence** | None | Explicit score with label |

## Output Structure

Three sections, structured text with headers:

```
Confidence: 0.82 (high) — 2 knowledge docs, 12 structural edges, 4 memories

=== Explanation ===
Auth starts in middleware.go which intercepts HTTP requests and validates
JWTs via jwt.go. Sessions are stored in Redis through store.go with a
7-day TTL for refresh tokens. The rate limiter in middleware.go
intentionally skips nil checks on the hot path...
  Files: auth.go, middleware.go, jwt.go, store.go

=== Structural Evidence ===
[Call Graph]
middleware.go → jwt.go (calls: ValidateToken, RefreshToken)
middleware.go → store.go (calls: GetSession, SaveSession)
jwt.go → store.go (calls: GetSigningKey)

[Co-Change Neighbors]
middleware.go ↔ auth.go (0.85)
jwt.go ↔ store.go (0.72)

=== Relevant Files ===
  1. middleware.go    — HTTP request interception, auth middleware (HDC: 0.91)
  2. jwt.go          — JWT validation and refresh (HDC: 0.87)
  3. store.go        — Session storage, Redis backend (structural: 0.78)
  4. auth.go         — Auth configuration and routes (co-change: 0.85)
```

## ConsultReport Type

```go
type ConsultReport struct {
    Query       string   // original query
    Intent      Intent   // detected intent (debug/feature/explore/continue)
    Confidence  float64  // 0.0-1.0
    ConfLabel   string   // "high" (≥0.7), "medium" (≥0.4), "low" (<0.4)
    Explanation string   // knowledge doc content (cached or freshly synthesized)
    Evidence    string   // call graph + co-change (structured text)
    RelevantFiles []RankedFile // HDC-ranked files with relevance source
    Synthesized bool     // true if LLM was called this invocation
    DocTopic    string   // knowledge doc topic used/created
    TokenCount  int      // total tokens in rendered output
}

type RankedFile struct {
    Path   string
    Score  float64
    Source string // "hdc", "structural", "co-change", "memory"
    Desc   string // one-line module description
}
```

## Engine Method

```go
func (e *Engine) Consult(ctx context.Context, namespace, query string, budget int) (*ConsultReport, error)
```

### Internal Flow

1. **Detect intent** — `DetectIntent(query)`
2. **Embed query** — `e.embedder.Embed(ctx, query, "RETRIEVAL_QUERY")`
3. **Knowledge doc lookup** — `e.GetKnowledgeDocs(ctx, namespace, query, queryEmb, 3)`
   - If top doc cosine ≥ 0.75 → cache hit, use its content as Explanation
4. **Gather evidence** (always, for structural sections):
   - HDC codemap search → top-10 modules (existing `getOrGenerateCodeMap` + `HDCEncodeQuery`)
   - Structural neighbors → `GetStructuralNeighborPaths(namespace, seedPaths, 2)`
   - Co-change → `GetCoChangeForPaths(namespace, seedPaths, 1.0)`
   - Detail memories → `DualSearch(ctx, namespace, query, searchOpts)`
5. **Synthesize if cache miss** — type-assert `e.cfg.Summarizer.(TextGenerator)`, call with synthesis prompt, save as knowledge doc via `e.store.SaveKnowledgeDoc()`
6. **Compute confidence** — weighted average of: doc match score (0.4), structural edge count normalized (0.3), memory match count normalized (0.3)
7. **Render** — format ConsultReport as structured text

### Synthesis Prompt

```
Given the following structural evidence about "{query}" in a {language} codebase,
write a concise explanation (200-400 words) of how this subsystem works.

Include: key files and their roles, data/control flow between components,
important design decisions or constraints.

Do NOT include code snippets. Focus on conceptual understanding that would
help a developer work on this subsystem correctly.

[Structural Evidence]
{codemap excerpt}
{call graph edges}
{co-change pairs}

[Existing Memories]
{matching detail memories}
```

### Knowledge Doc Caching

When synthesis produces a new doc:
- `Topic` = normalized query (lowercase, trimmed, max 80 chars)
- `Files` = union of files from HDC matches + structural edges + memories
- `SourceIDs` = IDs of detail memories used in synthesis
- `Embedding` = query embedding (for future cosine matching)
- Saved via existing `store.SaveKnowledgeDoc()`
- **Dedup:** Before creating a new doc, check if any existing doc shares ≥50% of the same files. If so, update that doc's content instead of creating a duplicate. This prevents "how does auth work" and "explain the auth system" from producing separate docs.

## Agent Discovery

Two mechanisms ensure agents know to use `consult`:

1. **CLAUDE.md instructions** — add to the dualmem CLI reference:
   ```
   CONSULT for deep subsystem understanding:
     ~/go/bin/dualmem consult "how does X work?"
   ```

2. **Context hints** — when `AssembleContextWith` detects files with no knowledge doc coverage in the codemap, append:
   ```
   [Tip: Run `dualmem consult "topic"` for deeper understanding of uncovered subsystems]
   ```
   Implementation: after codemap rendering, check if any top-5 HDC matches have no knowledge doc with matching files. If ≥2 uncovered, add the hint.

## CLI Subcommand

```bash
dualmem consult "how does auth work?" [--budget 2000] [--ns claude:project]
```

- Prints the structured text report to stdout
- Exit code 0 on success, 1 on error
- Follows existing CLI patterns (flag parsing, engine creation) from `cmd/dualmem/main.go`

## Confidence Scoring

```go
func computeConsultConfidence(docMatchScore float64, edgeCount, memoryCount int) (float64, string) {
    // Normalize edge count: 10+ edges = 1.0
    edgeNorm := math.Min(float64(edgeCount)/10.0, 1.0)
    // Normalize memory count: 5+ memories = 1.0
    memNorm := math.Min(float64(memoryCount)/5.0, 1.0)

    score := docMatchScore*0.4 + edgeNorm*0.3 + memNorm*0.3

    var label string
    switch {
    case score >= 0.7:
        label = "high"
    case score >= 0.4:
        label = "medium"
    default:
        label = "low"
    }
    return score, label
}
```

## Plan Split

### Plan 1: Engine.Consult + ConsultReport type + tests
- **Files:** `dualmem/types.go` (ConsultReport, RankedFile types), `dualmem/dualmem.go` (Consult method, computeConsultConfidence), `dualmem/consult_test.go` (unit tests)
- **Tests:** mock TextGenerator, verify cache hit path, verify synthesis path, verify confidence scoring, verify structured text output format
- **Harness:** Pi + GLM 5.1
- **Estimated time:** ~15min

### Plan 2: CLI subcommand + context hints + CLAUDE.md update
- **Files:** `cmd/dualmem/main.go` (consult subcommand), `dualmem/dualmem.go` (context hint in AssembleContextWith)
- **Tests:** integration test (build + run `dualmem consult` against test DB)
- **Depends on:** Plan 1 merged to main
- **Harness:** Pi + GLM 5.1
- **Estimated time:** ~10min

## Not In Scope

- Source code snippet extraction (deferred — agent can Read files)
- MCP tool (CLI preferred per CLAUDE.md)
- Reasoning mandates per intent (can add later as a refinement)
- Knowledge doc staleness/refresh (existing `synthesize --force` handles this)
