# Using DualMem with OpenCode CLI

This guide sets up dualmem as an MCP server in [OpenCode CLI](https://opencode.ai), giving you cross-session memory, HDC-powered code search, and context assembly with any model (Claude, GPT, Gemini, DeepSeek, local models, etc.).

Your existing Claude Code + CLI setup continues to work unchanged — both harnesses share the same SQLite database.

## Prerequisites

```bash
# Install dualmem-mcp (requires Go)
go install github.com/goblincore/geoffreyengram/cmd/dualmem-mcp@latest

# Verify it's in your PATH
which dualmem-mcp   # should print ~/go/bin/dualmem-mcp

# You need a Gemini API key for embeddings
# Get one at https://aistudio.google.com/apikey
export GEMINI_API_KEY="your-key-here"
```

## Step 1: Configure MCP Server

Create or edit `opencode.json` in your project root:

```json
{
  "mcp": {
    "dualmem": {
      "type": "local",
      "command": ["dualmem-mcp"],
      "environment": {
        "GEMINI_API_KEY": "your-gemini-api-key",
        "DUALMEM_NS_PREFIX": "opencode:"
      },
      "enabled": true
    }
  }
}
```

**Note on `DUALMEM_NS_PREFIX`**: Set this to `"opencode:"` to keep OpenCode memories in a separate namespace from Claude Code. Set it to `"claude:"` (or omit it) if you want both harnesses to share the same memory pool. Sharing is recommended if you work on the same project from both tools.

For global config (all projects), put this in `~/.config/opencode/opencode.json` instead.

### If dualmem-mcp isn't in PATH

Use the full path:

```json
{
  "mcp": {
    "dualmem": {
      "type": "local",
      "command": ["/Users/you/go/bin/dualmem-mcp"],
      "environment": {
        "GEMINI_API_KEY": "your-gemini-api-key"
      },
      "enabled": true
    }
  }
}
```

## Step 2: Add Project Instructions (AGENTS.md)

OpenCode reads `AGENTS.md` (or falls back to `CLAUDE.md`) for project instructions. Add dualmem usage guidance:

```markdown
## Cross-Session Memory (dualmem)

This project uses dualmem for cross-session memory via MCP tools.

### Session start
Call `get_context` with a description of your task to load relevant memories,
code map, and checkpoints from previous sessions. Internalize the result —
don't repeat it back.

### During work
- Before modifying a file, call `file_context` to check for warnings
- Use `search_codebase` instead of grep for conceptual queries
- Use `search_memory` before exploring files — a previous session may have mapped them
- Call `save_memory` when you discover something non-obvious:
  - type="warning" for code that should NOT be changed
  - type="decision" for rejected/chosen approaches
  - salience=0.9 for critical items

### Session end
- Call `distill_session` with a summary of what you learned/decided/discovered
- Call `checkpoint` with action="save" if work is in progress
```

## Step 3: Verify

Launch OpenCode and check MCP tools are available:

```bash
opencode
# In the TUI, check that dualmem tools appear in the tool list
# Or use the MCP debug command:
opencode mcp debug dualmem
```

You should see 13 tools: `get_context`, `search_codebase`, `search_memory`, `save_memory`, `get_codemap`, `file_context`, `file_index`, `checkpoint`, `distill_session`, `get_diff`, `rate_context`, `get_status`, `list_knowledge_docs`.

## Available MCP Tools

| Tool | Purpose |
|------|---------|
| `get_context` | Load session-start context (code map + memories + checkpoints) |
| `search_codebase` | HDC-powered natural language code search |
| `search_memory` | Search past decisions, warnings, insights |
| `save_memory` | Save a memory for future sessions |
| `get_codemap` | Project structure overview |
| `file_context` | Get warnings/decisions for a specific file |
| `file_index` | List files with attached memories |
| `checkpoint` | Save/list in-progress work handoffs |
| `distill_session` | Extract memories from session summary |
| `get_diff` | What changed since last session |
| `rate_context` | Rate context quality (improves ranking) |
| `get_status` | Memory system health |
| `list_knowledge_docs` | Browse synthesized knowledge docs |

## Optional: Skills

OpenCode supports skills (reusable instruction sets) in `.opencode/skills/`. You can create a dualmem skill for session lifecycle:

```bash
mkdir -p .opencode/skills/memory-load
```

Create `.opencode/skills/memory-load/SKILL.md`:

```markdown
---
name: memory-load
description: Load cross-session context from dualmem at session start
---

Call the `get_context` MCP tool with a description of the current task.
Budget defaults to 3000 tokens. Use intent="debug" for bug fixing,
intent="continue" for resuming work.

Internalize the context silently — use it to inform your work but
don't repeat it back unless asked.
```

And `.opencode/skills/wrap-up/SKILL.md`:

```markdown
---
name: wrap-up
description: End-of-session wrap-up — save learnings via dualmem
---

1. Summarize key learnings, decisions, and discoveries from this session
2. Call `distill_session` with that summary
3. If work is in progress, call `checkpoint` with action="save"
4. If you have a snapshot_id from get_context, call `rate_context`
```

## Coexistence with Claude Code

Both Claude Code and OpenCode can use the same dualmem database simultaneously. The default database is at `~/.local/share/dualmem/memories.db`.

**Shared namespace** (recommended): Both use `claude:projectname` — memories from either tool are visible in both.

**Separate namespaces**: Set `DUALMEM_NS_PREFIX=opencode:` in the OpenCode MCP config. Claude Code memories stay under `claude:projectname`, OpenCode under `opencode:projectname`. They won't see each other's memories.

**No conflicts**: SQLite handles concurrent reads safely. Writes from both tools are serialized by the database lock.

## Troubleshooting

**MCP server not starting**: Check `opencode mcp debug dualmem` for error output. Common issues:
- `GEMINI_API_KEY` not set or invalid
- `dualmem-mcp` binary not found (check PATH or use full path)

**No memories appearing**: First session won't have prior context. Use `save_memory` or `distill_session` to populate, or run `dualmem seed` from CLI to auto-generate codebase context.

**Want to use CLI commands too**: The dualmem CLI works alongside the MCP server. Run `dualmem search "query"`, `dualmem context "task"`, etc. directly in your terminal. Both use the same database.
