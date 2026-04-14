# Phase 3: Lightweight Workflow Hints — Design Spec

## Problem

Phase 1 (area autopilot) and Phase 2 (workflow discovery) produce rich workflow memories, but they only surface if the agent explicitly searches for them. There's no proactive mechanism to tell the agent "hey, this file or ticket is part of a known workflow."

## Solution

Two independent hint-only paths that inject one-liner pointers into existing output:

1. **File-context hints** — when the agent reads a file, the file-context gate includes workflow hints alongside existing warning/decision annotations
2. **Ticket-prefix hints** — during context assembly, ticket prefixes extracted from active checkpoints trigger workflow hint lookup

Both paths produce compact one-liners (~50 tokens each, max 3 per path). The agent decides whether to expand via `dualmem search "workflow:<id>"`.

## Design Decision: Hint-Only (Not Auto-Expand)

We considered three models:
- **A) Hint-only** — one-liner pointers, agent expands on demand
- **B) Auto-expand for ticket matches** — inline full workflow summary (300-800 tokens) when ticket prefix matches
- **C) Budget-aware expansion** — auto-expand only when context budget has headroom

Chose **A** for initial implementation. Cheapest on tokens, zero risk of bloating context with irrelevant detail, and matches the "lightweight" philosophy. Can upgrade to B later if testing shows agents consistently expand after seeing hints.

## Architecture

### File-Context Hints

```
Agent reads file → PreToolUse hook → dualmem file-context --gate <file>
  → FileAnnotations() queries workflow autopilot memories matching the file
  → Returns WorkflowHint{WorkflowID, Tickets, Summary, MatchedFile}
  → Renders as annotation: 📎 [workflow] "credential issuance flow" (LC-1635) — search "workflow:issue-credentials" for full detail
```

### Ticket-Prefix Hints

```
dualmem context → AssembleContextWith()
  → Load active checkpoints (already happens)
  → Extract ticket prefixes from checkpoint Task + Text fields via regex [A-Z]+-\d+
  → For each prefix, query workflow autopilot memories containing that prefix
  → Deduplicate (same workflow may match multiple prefixes)
  → Render as [Workflow Hints] section, max 3 hints
```

### Ticket-Prefix Source

Ticket prefixes are extracted from **active checkpoint fields** (Task, Text), not just the raw query string. This matches real usage — the ticket context lives in the checkpoint, not the user's message.

## New Types

```go
// WorkflowHint is a lightweight pointer to a full workflow memory.
type WorkflowHint struct {
    WorkflowID  string   // e.g. "issue-credentials"
    Tickets     []string // e.g. ["LC-1635", "LC-1729"]
    Summary     string   // first ~150 chars of workflow text after the [workflow:ID] prefix
    MatchedFile string   // which file triggered the match (file-context path only)
}
```

## New Store Methods

```go
// GetWorkflowHintsForFiles returns workflow hints for autopilot memories
// whose files_json contains any of the given filenames. Max 3 results.
GetWorkflowHintsForFiles(userID string, filenames []string) ([]WorkflowHint, error)

// GetWorkflowHintsForTickets returns workflow hints for autopilot memories
// whose text contains any of the given ticket prefixes. Max 3 results.
GetWorkflowHintsForTickets(userID string, tickets []string) ([]WorkflowHint, error)
```

Two separate queries keeps concerns clean — file-context path calls the first, context assembly calls the second.

### File-context hint query

```sql
SELECT DISTINCT text
FROM detail_memories
WHERE user_id = ? AND type = 'autopilot' AND text LIKE '[workflow:%'
  AND files_json LIKE ?
LIMIT 3
```

### Ticket-prefix hint query

```sql
SELECT DISTINCT text
FROM detail_memories
WHERE user_id = ? AND type = 'autopilot' AND text LIKE '[workflow:%'
  AND (text LIKE '%LC-1635%' OR text LIKE '%LC-1729%')
LIMIT 3
```

Both queries parse the `[workflow:ID]` prefix and first ~150 chars from the text column to populate the WorkflowHint struct.

## Output Format

### File-context gate output

Appended after existing annotations:

```
[File Memory] routes/inbox.ts (3 cached observations, file ~4096tok)
  ⚠ [warning] Don't touch: rateLimiter cleanup() skips nil check intentionally (2026-03-15)
  📎 [workflow] "credential issuance flow" (LC-1635) — search "workflow:issue-credentials" for full detail
```

Uses the same annotation pattern as warnings/decisions. `📎` icon and `[workflow]` type label. Max 3 workflow hints per file.

### Context assembly output

New section between checkpoints and file-context:

```
[Workflow Hints]
📎 "credential issuance flow" (LC-1635, LC-1729) — search "workflow:issue-credentials"
📎 "guardian approval gate" (LC-1731) — search "workflow:guardian-approval"
```

Max 3 hints. Only appears if ticket prefixes were found in active checkpoints.

## CLAUDE.md Update

Add near the "When to search explicitly" section:

```markdown
### Workflow hints
When you see 📎 Workflow hints in file-context or context output, these are
lightweight pointers to full workflow analyses discovered by autopilot. If
the hint is relevant to your current task, expand it with:
  dualmem search "workflow:<id>"
This gives you the full data flow, trigger, cross-service boundaries, and
error handling for that workflow.
```

## Files Changed

| File | Change |
|------|--------|
| `dualmem/types.go` | Add `WorkflowHint` struct |
| `dualmem/store_sqlite.go` | Add `GetWorkflowHintsForFiles()` and `GetWorkflowHintsForTickets()` |
| `dualmem/dualmem.go` | Update `FileAnnotations()` to include workflow hints; update `AssembleContextWith()` to extract ticket prefixes from checkpoints and render `[Workflow Hints]` section |
| `cmd/dualmem/main.go` | Update `file-context` subcommand to render workflow hint annotations |
| `~/.claude/CLAUDE.md` | Add workflow hints documentation section |

## Testing & Verification

### File-context hints

```bash
# Should show workflow hint for a file known to be in a workflow cluster
cd ~/Work/LearnCard && source ~/.claude/hooks/dualmem-env.sh
dualmem file-context apps/learn-card-app/src/components/boost/boostCMS/BoostCMSHeader/BoostCMSHeader.tsx
# Expect: 📎 [workflow] line alongside any existing annotations

# File with no workflow associations should show no hint
dualmem file-context README.md
# Expect: no 📎 lines
```

### Ticket-prefix hints

```bash
# Checkpoint with ticket prefix should trigger workflow hint
dualmem checkpoint --task "LC-1635 credential fixes" --status in_progress --files "routes/inbox.ts"
dualmem context "fixing the issuance bug" --budget 3000
# Expect: [Workflow Hints] section with LC-1635 match

# No active checkpoint / no ticket prefix → no hints section
dualmem context "general exploration" --budget 3000
# Expect: no [Workflow Hints] section
```

### Edge cases

- Multiple ticket prefixes in one checkpoint → deduplicate if they map to the same workflow
- File in multiple workflows → show up to 3 hints, sorted by salience
- No workflow memories in DB → both paths are silent, zero overhead
