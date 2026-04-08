# Example CLAUDE.md — DualMem Integration

Copy the section below into your `~/.claude/CLAUDE.md` (global) or `.claude/CLAUDE.md` (per-project) to give Claude Code cross-session memory via DualMem.

**Prerequisites**: Install the CLI and set your API key:
```bash
go install github.com/goblincore/geoffreyengram/cmd/dualmem@latest
export GEMINI_API_KEY=your-key  # add to ~/.zshrc or equivalent
```

> **Important**: Use the full path `~/go/bin/dualmem` in CLAUDE.md — Claude Code's GUI does not inherit your shell PATH.

---

## DualMem — Cross-Session Project Memory

### Automatic Behavior (no user action needed)

**On session start**: Run this to load relevant context from past sessions:
```
~/go/bin/dualmem context "session context" --budget 3000
```
Internalize the output silently — use it to inform your responses but don't repeat it back unless asked.

Context is **task-aware**: intent is auto-detected from the query (debug/continue/feature/explore/plan) and adjusts memory ranking. Planning queries ("roadmap", "sprint", "Q3 status") boost continuity memories 2.5x. Override with `--intent debug` if the user's first message is clearly a bug fix, etc.

**During the session**: When you learn something important that should persist across conversations, save it:
```
~/go/bin/dualmem add --text "<what you learned>"
```

### What to save — use `--type` for structured memory patterns

**Decisions** (`--type decision`) — when the user rejects an approach or we settle on a choice:
```
~/go/bin/dualmem add --type decision --text "Rejected Postgres for CLI — chose SQLite for zero-setup" --files "store_sqlite.go"
```

**Warnings** (`--type warning`) — when code is intentionally unusual, fragile, or should not be "fixed":
```
~/go/bin/dualmem add --type warning --text "Don't touch: rateLimiter cleanup() skips nil check intentionally for hot path" --files "rate_limiter.go" --salience 0.9
```

**Session continuity** — prefer `checkpoint` for structured handoffs (auto-supersedes by task):
```
~/go/bin/dualmem checkpoint --task "auth refactor" --status in_progress --files "auth.go,middleware.go" --done "JWT validation,middleware" --remaining "refresh tokens,logout"
```
For simple notes, `--type continuity` still works:
```
~/go/bin/dualmem add --type continuity --text "In progress: auth refactor. Done: JWT validation. Remaining: refresh tokens" --files "auth.go"
```

**File landmarks** — after navigating the codebase to implement or debug something:
```
~/go/bin/dualmem add --text "Auth flow: routes.go → middleware.go → jwt.go" --files "routes.go,middleware.go,jwt.go" --sector procedural
```

**IMPORTANT: Always include `--files` when saving memories about code.** File associations feed the co-change graph — DualMem automatically learns which files change together across sessions. This means future context assembly will surface related files you didn't explicitly ask for.

**Dead ends** — where something is NOT, to avoid re-searching:
```
~/go/bin/dualmem add --text "Auth logic is NOT in middleware/ — it's in the RPC handlers" --sector semantic
```

**User corrections** — "don't do X", "prefer Y", style preferences
**Debugging insights** — root causes that took effort to find

### Salience guide
- `--salience 0.9` for warnings and critical decisions
- Default (0.7) for most facts, file maps, and preferences
- `--sector semantic` for facts/knowledge, `--sector procedural` for processes/file-maps/how-tos

### Type priority
Warnings and decisions are surfaced first in context assembly. When you load context at session start, pay special attention to Warning entries — these flag code that should NOT be changed.

**Namespace**: Auto-detected from cwd (directory name → `claude:<dirname>`). Override with `--ns "claude:<project>"` if needed.

### When to search explicitly
Before grepping/globbing for files related to a feature, search memory first — a previous session may have already mapped the relevant files:
```
~/go/bin/dualmem search "<feature or concept>" --limit 5
```

### Code search (HDC-powered)
Find relevant modules by natural language query — uses hyperdimensional computing, no API calls, instant:
```
~/go/bin/dualmem search-code "<natural language query>"
```
Returns modules ranked by structural similarity (path, symbols, imports, identifiers). Useful for fuzzy queries where you don't know what to grep for ("where does auth happen?", "what handles codebase scanning?"). For exact symbol lookup, grep is still better.

### Symbol extraction
Extract a single function or type from a file without reading the whole thing:
```
~/go/bin/dualmem unfold <file> <symbol-name>
```
Returns the symbol's source with line numbers. Supports Go, TypeScript, Python, Rust. Use this instead of reading a 3000-line file when you only need one function.

### Co-change graph
Before modifying a file, check what else might need to change:
```
~/go/bin/dualmem cochange auth.go
```
This shows files that historically co-change with `auth.go`, ranked by strength. The co-change graph builds automatically from `--files` associations — no explicit action needed.

### File-read gate (PreToolUse hook)
The PreToolUse hook on `Read` calls `dualmem file-context --gate <file>`. For files >1500 bytes with cached memories, it injects a structured context block before the read — showing type-labeled observations with dates. This primes the agent with relevant context (~400 tokens) before the full file read (~5-50k tokens). Small files and files with no memories pass through silently.

---

## Optional: Disable built-in MEMORY.md

If you want DualMem to be the only memory system (recommended to avoid duplication), add this at the top of your CLAUDE.md:

```markdown
## Memory System: Use dualmem, NOT MEMORY.md

**Do NOT use the MEMORY.md file-based memory system.** Do NOT write to `/memory/` files or update `MEMORY.md`. The `Write` and `Edit` tools must not be used for memory persistence.

Use **dualmem** exclusively for all cross-session memory.
```

---

## Optional: MCP Server

If you prefer MCP tools over CLI (trades token efficiency for discoverability):

```bash
go install github.com/goblincore/geoffreyengram/cmd/dualmem-mcp@latest
claude mcp add dualmem -s user -- dualmem-mcp
```

Tools: `search_codebase`, `get_codemap`, `search_memory`, `get_context`, `save_memory`.
