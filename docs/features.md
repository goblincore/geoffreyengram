# Features

## Memory types and routing

Memories are classified into sectors and scored by importance. Typed memories (`--type warning`, `--type decision`) are biased toward the Detail Path:

```bash
dualmem add --type warning --text "Don't touch rateLimiter cleanup()" --files "rate_limiter.go" --salience 0.9
dualmem add --type decision --text "Rejected Postgres, chose SQLite" --files "store_sqlite.go"
dualmem add --type continuity --text "Done: JWT. Remaining: refresh tokens" --files "auth.go,jwt.go"
dualmem add --type trace --text "routes/index.ts:320 → contact-methods.ts:187\nOTP verification flow" --files "index.ts,contact-methods.ts"
```

Context assembly prioritizes: warnings first, then decisions/continuity/traces, then general memories. Intent-aware weighting adjusts per query — debugging boosts warnings (2x), exploring boosts traces and maps (2x), resuming boosts continuity (2x).

## Checkpoints — structured session handoffs

```bash
dualmem checkpoint --task "auth refactor" --status in_progress \
  --files "auth.go,middleware.go" \
  --done "JWT validation,middleware" --remaining "refresh tokens,logout"
dualmem checkpoint --list
```

## Co-change graph — learned file relationships

When a memory mentions multiple files, DualMem automatically builds co-change edges between all file pairs. Over time, this creates a learned map of which files change together.

```bash
dualmem cochange auth.go                      # files that co-change with auth.go
dualmem cochange auth.go --min-strength 2.0   # only strong relationships
dualmem cochange --decay                      # apply 90-day half-life decay
```

File hints from checkpoints and memories are expanded through co-change neighbors. Working on `auth.go`? The codemap will also highlight `middleware.go` and `jwt.go` if they historically co-change — even if their code structure (HDC vectors) is dissimilar.

Edges include concept labels (entity names that explain *why* files co-change), populated automatically during `dualmem synthesize`.

## Entity graph — structure-aware retrieval

Entities extracted from memories are stored in a knowledge graph. Search applies structure-aware graph traversal — direct entity matches get full boost, expanded neighbors get reduced boost scaled by edge type:

| Edge type | Multiplier | Meaning |
|-----------|-----------|---------|
| `depends_on` | 1.0 | Strong structural relationship |
| `implements` | 0.9 | Strong behavioral relationship |
| `modifies` | 0.8 | Moderate change relationship |
| `uses` | 0.7 | Moderate usage relationship |
| `relates` | 0.5 | Weak association |

```bash
dualmem entities                          # graph stats
dualmem entities search "SQLite"          # find entities by name
dualmem entities show "store_sqlite.go"   # entity + relationships
```

## Knowledge documents — synthesized context

`dualmem synthesize` clusters related memories and produces coherent concept documents. These appear in context output before individual memories, saving tokens:

```bash
dualmem synthesize --dry-run   # preview clusters
dualmem synthesize             # synthesize knowledge docs
dualmem docs                   # list docs
dualmem docs show <topic>      # read a doc
```

## Consult — lazy-synthesis intelligence reports

Ask a question about any subsystem and get a structured explanation. If a cached knowledge doc matches (cosine >= 0.75), it's served instantly. Otherwise, structural evidence is gathered (HDC codemap + call graph + co-change + memories) and Gemini Flash synthesizes a narrative — which is then cached as a knowledge doc for future queries.

```bash
dualmem consult "how does auth work?"
dualmem consult "how does the HDC encoder work?" --budget 2000
```

Output is a 3-section report: Explanation (synthesized narrative), Structural Evidence (call graph edges + co-change neighbors), Relevant Files (HDC-ranked with relevance source). Includes a confidence score based on knowledge doc match, structural edge coverage, and memory count.

## Explore — grounded code briefings

Read ranked source files and produce a snippets-first briefing. Unlike `consult` (which caches knowledge docs), `explore` is ephemeral — it reads actual code and summarizes what it finds:

```bash
dualmem explore "how does credential issuance work?" --budget 3000
dualmem explore "auth middleware" --ns learncard
```

## Index — pre-warm codebase index

On first run, `explore`, `consult`, and `search-code` scan the codebase to build a codemap + structural edge graph. For large repos (4000+ files), use `index` to pre-warm the cache with progress output:

```bash
dualmem index                    # scan cwd, verbose progress
dualmem index --ns myproject     # explicit namespace
dualmem index --force            # re-index even if cache is fresh
```

The index is cached by git commit — subsequent commands on the same commit skip the scan entirely.

## HDC-powered code search

Find relevant code modules by natural language — uses hyperdimensional computing (2048-dim vectors from path, symbols, language, identifiers), no API calls, ~55us/query:

```bash
dualmem search-code "authentication middleware"
dualmem search-code "database connection pooling" --limit 5
dualmem search-code "auth" --graph   # experimental: PageRank + tag extraction
```

## Session distillation — ambient memory capture

Automatically extract memories from session transcripts at session end:

```bash
dualmem distill              # auto-detect latest Claude Code session
dualmem distill --dry-run    # preview extracted facts
dualmem distill --auto       # idempotent (for hooks)
```

## File-scoped recall

Inject relevant memories when Claude opens a file — warnings about `rate_limiter.go` surface even if the session task is "refactor error handling":

```bash
dualmem file-context rate_limiter.go   # memories for a specific file
dualmem file-index                     # regenerate file index for hook
```

## Staleness detection

Memories can go stale when code changes. `gc --stale` checks each memory against the current codebase:

```bash
dualmem gc --stale              # check all memories for staleness
dualmem gc --stale --dry-run    # preview only
```

Three checks: file staleness (>50% of referenced files changed), symbol staleness (identifiers no longer exist in current tags), and age staleness (>30 days without reinforcement).

## Context quality ratings & adaptive re-ranker

Measure whether assembled context is actually useful:

```bash
dualmem context "auth refactor" --budget 3000
# → outputs context + snapshot ID

dualmem rate --snapshot snap_17752... --phase late --ratings '{"mem_abc": 2, "mem_def": 0}'
dualmem rate --session --score 4 --explanation "warnings were helpful"
dualmem stats
dualmem train   # fits logistic regression from ratings → stores weights
```

## Progressive disclosure — index-then-fetch

Instead of committing the full token budget upfront, probe what's available first:

```bash
dualmem context "auth refactor" --budget 3000 --index
# → compact table (~300 tokens): type icons, IDs, titles, token costs

dualmem show --snapshot snap_17753... mem_a3f2 kdoc_dualmem chk_auth
# → full text of selected items only
```

## Auto-capture — ambient file interaction logging

A PostToolUse hook logs file interactions during the session to a local JSONL buffer (no API calls, ~5ms). At distill time, this enriches the transcript with a complete file-touch timeline.

The hook also detects exploration patterns — when the agent does 4+ grep/read calls across 3+ files within 2 minutes, it nudges the agent to save what it discovered as a `trace` memory.

## Seed — cold-start context

Pre-seed context for new projects by analyzing codebase structure:

```bash
dualmem seed --dry-run   # preview clusters
dualmem seed             # generate seed memories
```
