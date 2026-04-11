# geoffreyengram

Cross-session memory for Claude Code. Pure Go, single binary, SQLite storage.

Remembers decisions, warnings, and context across sessions so your agent doesn't re-learn your codebase every time.

## Quick Start

```bash
go install github.com/goblincore/geoffreyengram/cmd/dualmem@latest
export GEMINI_API_KEY=your-key  # for embeddings

dualmem add --text "Auth uses JWT, not sessions" --type decision
dualmem search "authentication"
dualmem context "fix the auth bug" --budget 3000
```

For Claude Code integration, add the hook config from [docs/example-claude-md.md](docs/example-claude-md.md).

## How It Works

Memories are split into two paths. An importance scorer (no LLM calls) routes each one at write time:

- **Detail path** — decisions, warnings, and anything high-salience stays at full fidelity (768d embeddings, full text, top 100 per project)
- **Sketch path** — everything else compresses over time: raw memories → episode summaries → narrative arcs → user profile

Context assembly takes a query and a token budget, then packs relevant memories in priority order. It's query-aware — a debugging session gets warnings first, a "what's in progress" session gets continuity items, a cold start gets a structural codemap. Output is grouped by file, not flat-listed.

Memories are indexed by file path. Open `rate_limiter.go` and you get the warnings about that file, even if your task is something else entirely. The `explore` command reads ranked source files and synthesizes a grounded briefing. `consult` does the same but caches the result as a knowledge document for future sessions — so the second time someone asks "how does auth work?" it's instant.

The codebase itself is indexed. Co-change graphs learn which files change together. HDC vectors (2048d, no API calls) enable natural-language code search across your repo. Tree-sitter parses structure for call/import edges. These all feed into context ranking alongside the memories.

Storage is one SQLite file. No external services.

## Features

| Feature | Description |
|---------|-------------|
| **Memory types** | Typed memories (decision, warning, continuity, trace) with intent-aware ranking |
| **Checkpoints** | Structured session handoffs — task, status, files, what's done, what remains |
| **Code search** | HDC + BM25 hybrid, finds modules by natural language, no API calls |
| **Knowledge synthesis** | Clusters related memories into concept docs, served instantly on repeat queries |
| **Explore** | Reads ranked source files, produces grounded code briefings |
| **Co-change graph** | Learns which files change together from memory file associations |
| **Entity graph** | Typed edges between concepts for structure-aware retrieval boost |
| **Session distillation** | Extracts memories from session transcripts automatically |
| **Staleness detection** | Flags memories when referenced files or symbols have changed |
| **File-scoped recall** | PreToolUse hook surfaces cached observations before file reads |
| **File-read gate** | Structured decision tree primes agent with file context (~400 tok vs 5-50k full read) |
| **Symbol extraction** | `dualmem unfold <file> <symbol>` extracts a single function/type with line numbers |
| **Planning intent** | "roadmap" and "sprint" queries auto-boost continuity memories 2.5x |
| **Autopilot** | Autonomous codebase exploration — curiosity scorer ranks modules by memory gaps, git heat, complexity, and co-change strength; LLM explores top targets and saves investigation memories |
| **Anticipatory worker** | Runs during sessions, watches file activity, pre-explores co-change and structural neighbors before the user needs them |
| **Benchmark CLI** | `dualmem benchmark` compares cold-start (codemap only) vs warm (with memories) context quality using auto-generated queries from git history |
| **Anticipation stats** | `dualmem anticipation-stats` measures hit rate of pre-explored files — were they actually needed? |

See [docs/features.md](docs/features.md) for detailed usage and examples.

## vs claude-mem

Head-to-head against [claude-mem](https://github.com/thedotmack/claude-mem) v12.0.1 (46k stars). Context quality rated by LLM judge (5 developer task scenarios, position-randomized A/B, ChromaDB enabled for claude-mem).

| Scenario | geoffreyengram | claude-mem |
|----------|---------------|------------|
| Add payment method | **5.8** | 2.5 |
| Debug WebSocket drops | **7.5** | 2.8 |
| Set up CI/CD | **8.2** | 2.8 |
| Refactor cache layer | **7.8** | 1.5 |
| Roadmap planning | 3.0 | **7.2** |
| **Average** | **6.5/10** | **3.4/10** |

geoffreyengram wins on targeted technical tasks through query-aware, file-scoped context. claude-mem wins on broad "what's in progress" queries. Full results and scripts in `benchmarks/`.

### Cold vs Warm Benchmark

`dualmem benchmark` measures the real-world benefit of accumulated memories. It auto-generates queries from recent git commits and runs context assembly twice: once cold (codemap only) and once warm (with memories). On this repo (111 memories, 31% module coverage):

```
Query                                    |    Cold |    Warm | +Sources
"add autopilot integration test"         |  397 tk | 3544 tk | +34
"add anticipation TTL filter"            |  405 tk | 3520 tk | +30
"integrate worker into pipeline"         |  296 tk | 3406 tk | +30
"add session log parsing"                |  365 tk | 3738 tk | +32
"add autopilot CLI command"              |  371 tk | 3628 tk | +33

Average: +31.8 additional context sources per query
```

Each additional source is a memory, checkpoint, episode, or knowledge doc that wouldn't exist without the memory system.

## Tests

```bash
go test ./dualmem/ -v                      # unit + integration
go test ./dualmem/ -run TestBench -v       # context assembly benchmarks
go test ./dualmem/ -run TestSearchBenchmark # code search accuracy
dualmem benchmark --auto 5                 # cold vs warm on your project
dualmem anticipation-stats                 # anticipatory worker hit rate
```

See [docs/testing.md](docs/testing.md) for the full breakdown.

## Documentation

| Doc | Contents |
|-----|----------|
| [Architecture](docs/architecture.md) | System diagram, dual-path routing, context assembly pipeline |
| [Features](docs/features.md) | All features with usage examples and code snippets |
| [CLI Reference](docs/cli-reference.md) | Every command and flag |
| [Claude Code Setup](docs/example-claude-md.md) | CLAUDE.md template for integration |
| [Dispatch UI](docs/dispatch-ui.md) | Web dashboard for headless agent tasks |
| [Testing](docs/testing.md) | Test categories and benchmark scripts |

## License

MIT
