# Explore Command — Design Spec

**Date:** 2026-04-07
**Depends on:** Consult command (implemented), CodeMap + HDC search, Co-change graph

---

## Problem

`consult` synthesizes explanations from metadata only — file names, structural edges, memories. Both GLM 5.1 and Flash Lite hallucinate equally when given this evidence because the bottleneck is evidence quality, not model quality. The LLM is guessing at what code does from file names and import graphs.

Meanwhile, Claude *can* read files itself, but:
- It doesn't know which files matter until it's already spent tool calls searching
- Reading 10 files costs ~20K context tokens
- Pre-session injection needs grounded code *before* Claude starts

## Solution

A code-reading evidence pipeline that ranks files, extracts relevant snippets within a token budget, and feeds real code to the LLM. One core engine method, three entry points:

1. **`consult` upgrade** — synthesis prompt gets real code instead of metadata
2. **`explore` CLI** — standalone snippets-first output for context injection
3. **Pre-hook** — auto-runs `explore` on session start for grounded briefing

## Core: `ReadCodeEvidence`

New engine method that all three entry points share.

```go
func (e *Engine) ReadCodeEvidence(ctx context.Context, namespace, query string, opts ReadCodeOpts) (*CodeEvidence, error)
```

### Types

```go
type ReadCodeOpts struct {
    MaxTokens    int      // token budget for snippets (default 4000)
    MaxFiles     int      // max files to read (default 8)
    SeedPaths    []string // optional pre-ranked paths to prioritize
}

type CodeEvidence struct {
    Snippets    []CodeSnippet
    TotalTokens int
}

type CodeSnippet struct {
    FilePath    string // relative to repo root
    StartLine   int
    EndLine     int
    Content     string
    Relevance   string // "hdc", "structural", "co-change"
    TokenCount  int
}
```

### Internal Flow

1. **Rank modules** — HDC+BM25 search against query (existing `SearchCodeMap` or inline HDC scoring from `Consult`)
2. **Expand** — add structural neighbors via `GetStructuralNeighborPaths` and co-change via `GetCoChangeForPaths`
3. **Resolve to files** — for each ranked module, expand `ModuleMap.Files` to actual file paths (relative to `CodeMap.RootDir`)
4. **Hybrid extraction**:
   - Small files (<150 lines): read entire file
   - Large files (≥150 lines): match `FileInfo.Identifiers` against query tokens, extract functions/types containing matched identifiers. Use a simple line-range extractor — find the identifier, expand to enclosing block (function/type boundaries via brace/indent counting).
5. **Token budgeting** — fill greedily, highest-ranked file first. Skip a file if adding it would exceed `MaxTokens`. Use `estimateTokens()` (existing) for counting.
6. **Return** `CodeEvidence` with ranked snippets and total token count.

### Identifier Matching

To decide which symbols to extract from large files:

1. Tokenize the query into words (reuse `hdcTokenize`)
2. For each file's `FileInfo.Identifiers`, score by overlap with query tokens (case-insensitive, camelCase/snake_case split)
3. Extract the top-scoring identifiers' enclosing blocks
4. If no identifiers match (score = 0), fall back to reading the first 80 lines of the file

### Block Extraction

Simple heuristic for finding the block around an identifier:

- **Go**: scan for `func <name>` or `type <name>`, read until matching closing brace at the same indent level
- **TypeScript/JavaScript**: scan for `function <name>`, `const <name>`, `class <name>`, `export`, read until matching closing brace
- **Python**: scan for `def <name>` or `class <name>`, read until dedent
- **Fallback**: for unknown languages, take the identifier's line ± 15 lines

Maximum block size: 60 lines. If a block exceeds this, truncate at 60 lines with a `// ... (truncated)` marker.

### File Reading

`ReadCodeEvidence` needs to read actual files from disk. It resolves paths via:

```go
fullPath := filepath.Join(codeMap.RootDir, snippet.FilePath)
```

Files are read via `os.ReadFile`. If a file doesn't exist (deleted since last scan), skip it silently.

## Entry Point 1: `consult` Upgrade

### Changes to `Engine.Consult`

After gathering structural evidence (step 3 in current flow), add:

```go
// 3.5 Read code evidence
codeEvidence, err := e.ReadCodeEvidence(ctx, namespace, query, ReadCodeOpts{
    MaxTokens: budget / 2, // half budget for code, half for synthesis
    MaxFiles:  6,
    SeedPaths: seedPaths,
})
```

### Updated Synthesis Prompt

```
Given the following code and structural evidence about "{query}" in this codebase,
write a concise explanation (200-400 words) of how this subsystem works.

Include: key files and their roles, data/control flow between components,
important design decisions or constraints. Reference specific code where helpful.

[Code Snippets]
--- dualmem/hdc.go:15-42 ---
func (enc *HDCEncoder) EncodeModule(m ModuleMap) []float32 {
    ...actual code...
}

[Structural Evidence]
{call graph + co-change as before}

[Existing Memories]
{matching detail memories}
```

Key changes from current prompt:
- "Do NOT include code snippets" → "Reference specific code where helpful"
- `[Code Snippets]` section added before structural evidence
- LLM now has real code to ground its explanation

### Cache Behavior

- Existing knowledge docs from metadata-only synthesis remain valid — cache hit path unchanged
- New `--fresh` flag on CLI forces re-synthesis with code evidence
- `synthesize --force` regenerates all docs (now with code grounding since `Consult` is the synthesis engine)

## Entry Point 2: `explore` CLI Command

```bash
dualmem explore "how does HDC encoding work?" [--budget 4000] [--ns claude:project]
```

### Output Format: Snippets-First

Optimized for context injection — code blocks are the main payload, summary is minimal.

```
[Explore: how does HDC encoding work?]
8 files, 3847 tokens

--- dualmem/hdc.go:15-42 (hdc, score=0.91) ---
func (enc *HDCEncoder) EncodeModule(m ModuleMap) []float32 {
    vec := make([]float32, HDCDim)
    // Layer 1: path tokens
    ...
}

--- dualmem/codemap.go:954-963 (structural) ---
func HDCEncodeQuery(query string) []float32 {
    enc := NewHDCEncoder()
    return enc.EncodeQuery(query)
}

... more snippets ...

[Summary]
HDC encoding produces 2048-dim hyperdimensional vectors with 4 layers
(path, symbols, language, content). Modules are encoded via EncodeModule
in hdc.go, queries via EncodeQuery. Codemap.go orchestrates search by
scoring all modules against the query vector using cosine similarity.
```

### Differences from `consult`

| Aspect | `consult` | `explore` |
|--------|-----------|-----------|
| Output format | Briefing (narrative + evidence + files) | Snippets-first (code + short summary) |
| Knowledge doc caching | Yes — saves/reuses docs | No — ephemeral |
| Summary length | 200-400 words | 50-100 words |
| Primary use case | Mid-session "explain this" | Context injection, pre-hook |
| LLM call | For narrative synthesis | For short summary only |

### Engine Method

```go
func (e *Engine) Explore(ctx context.Context, namespace, query string, budget int) (*ExploreResult, error)
```

```go
type ExploreResult struct {
    Query       string
    Evidence    *CodeEvidence
    Summary     string // 50-100 word grounded summary
    TotalTokens int
}
```

**Internal flow:**
1. Call `ReadCodeEvidence(ctx, namespace, query, opts)` with 80% of budget for snippets
2. Call `getSynthesisGenerator().GenerateText()` with snippets as context, asking for 50-100 word summary
3. Return `ExploreResult` with snippets + summary

### `FormatSnippetsFirst`

Renderer for the snippets-first format. Lives on `ExploreResult`:

```go
func (r *ExploreResult) FormatSnippetsFirst() string
```

## Entry Point 3: Pre-Hook Injection

### Hook Script: `dualmem-explore-prehook.sh`

Runs on prompt submit (PreToolUse or similar hook point). Simple shell script:

```bash
#!/bin/bash
# Extract topic from user prompt (first arg or stdin)
PROMPT="$1"

# Simple heuristic: first sentence, strip common prefixes
TOPIC=$(echo "$PROMPT" | head -1 | sed 's/^(help me |please |can you |I need to )//i' | cut -c1-120)

if [ -z "$TOPIC" ]; then
    exit 0
fi

~/go/bin/dualmem explore "$TOPIC" --budget 3000
```

### Heuristics

Current heuristics are intentionally simple — extract first sentence, strip common conversational prefixes. Good enough for most prompts. Can be improved later with better pattern matching or lightweight intent extraction.

If the prompt doesn't yield a clear topic (empty after stripping), the hook exits silently — no wasted LLM call.

### Hook Registration

In Claude Code settings (`~/.claude/settings.json`), add as a `user-prompt-submit` hook or similar. Exact hook point TBD based on what Claude Code supports for pre-session context injection.

## Testing Strategy

### Unit Tests

- `TestReadCodeEvidence_SmallFile` — file <150 lines read entirely
- `TestReadCodeEvidence_LargeFile_IdentifierMatch` — large file extracts matched blocks
- `TestReadCodeEvidence_LargeFile_NoMatch` — falls back to first 80 lines
- `TestReadCodeEvidence_TokenBudget` — stops adding files when budget exceeded
- `TestReadCodeEvidence_MissingFile` — skips deleted files gracefully
- `TestBlockExtraction_Go` — extracts Go func/type blocks
- `TestBlockExtraction_TypeScript` — extracts TS function/class blocks
- `TestBlockExtraction_Python` — extracts Python def/class blocks
- `TestExplore_SnippetsFirstFormat` — output format matches spec
- `TestConsult_WithCodeEvidence` — synthesis prompt includes code snippets

### Integration Tests

- `TestExplore_RealCodebase` — run against geoffreyengram repo, verify snippets contain actual code
- `TestConsult_GroundedSynthesis` — verify consult output references real function names from code

## Implementation Notes

- Dispatcher execution: GLM 5.1 model, pi harness
- `ReadCodeEvidence` goes in `dualmem/dualmem.go` alongside `Consult`
- Block extraction helpers go in a new `dualmem/extract.go`
- `Explore` engine method in `dualmem/dualmem.go`
- CLI subcommand in `cmd/dualmem/main.go`
- Pre-hook script in `hooks/dualmem-explore-prehook.sh` (installed to `~/.claude/hooks/`)

## Not In Scope

- AST-based extraction (regex + brace counting is good enough for v1)
- Streaming output for large explorations
- MCP tool wrapper (CLI preferred per project conventions)
- Advanced pre-hook heuristics (simple first-sentence extraction for now)
- Explore caching (ephemeral by design; consult handles caching)
