# Exploration Trace Capture

**Date**: 2026-04-04
**Status**: Draft

## Problem

When Claude explores a codebase to understand how something works (e.g., tracing an OTP verification flow across 4+ files via sequential grep→read cycles), the discovered understanding is lost after the session. Future sessions that need the same understanding must repeat the entire exploration from scratch.

Today's autocapture hook logs *files touched* but not *what was discovered*. Distill extracts general facts from transcripts but doesn't specifically target exploration patterns, and transcript truncation may lose the details.

## Solution

Add a new `trace` memory type that captures code flow discoveries — both the structured path and the semantic understanding. Traces are captured via two complementary mechanisms:

1. **Real-time nudge**: Enhanced autocapture hook detects exploration patterns and prompts Claude to save what it discovered.
2. **Distill safety net**: Enhanced extraction prompt specifically looks for unexported exploration traces in session transcripts.

## Design

### 1. New memory type: `trace`

Stored in `detail_memories` with `type="trace"`. Default salience: **0.8** (exploration traces represent significant effort worth preserving).

**Text format convention** — structured trace followed by prose:

```
routes/index.ts:320 (verifiedContactRoute)
  → brain-service/routes/contact-methods.ts:187 (extractContactMethod)
  → brain-service/routes/contact-methods.ts:203 (sendOtp)

OTP verification starts at verifiedContactRoute, which delegates to
contact-methods.ts in brain-service. The contactMethod and otpChallenge
are extracted from ctx. OTP is sent via challenge, NOT a magic link.
```

**CLI interface**: `dualmem add --type trace --text "..." --files "file1.ts,file2.ts"`

**Files field**: Contains all files in the trace, enabling file-recall to surface the trace when any constituent file is opened.

### 2. Enhanced autocapture hook

**Current**: Logs `{ts, tool, files}` per tool call to session JSONL.

**Enhanced**: Also captures search context:

```json
{"ts":"...","tool":"Grep","files":"routes/index.ts","query":"verifiedContactRoute"}
{"ts":"...","tool":"Read","files":"routes/index.ts","lines":"311-370"}
{"ts":"...","tool":"Grep","files":"contact-methods.ts","query":"sendOtp|verifyOtp"}
{"ts":"...","tool":"Read","files":"contact-methods.ts","lines":"216-315"}
```

New fields:
- `query`: Grep/search pattern used (extracted from `tool_input.pattern` or `tool_input.command`)
- `lines`: Line range read (extracted from `tool_input.offset` + `tool_input.limit`, or full file indicator)

### 3. Exploration pattern detection (in autocapture hook)

After each JSONL append, the hook scans the tail of the session log for exploration signatures.

**Detection criteria** (all must be met):
- 4+ grep/read tool calls within the last ~2 minutes
- Touching 3+ distinct files (by basename)
- At least 1 grep call (passive reading of many files doesn't count — exploration involves *searching*)

**Nudge output** (PostToolUse hooks can output text that Claude sees):

```
💡 You've been tracing a code flow across 4 files (index.ts, contact-methods.ts, ...).
Consider saving what you discovered:
  dualmem add --type trace --text "<structured trace + what you learned>" --files "file1,file2,..."
```

**Cooldown**: After triggering, suppress for 10 minutes (tracked via a timestamp file at `/tmp/dualmem-trace-nudge-<hash>`). Prevents nagging during extended exploration of the same area.

**Why nudge Claude instead of auto-saving**: Only Claude knows *what the exploration means*. The hook sees tool calls; Claude understands "this is how OTP verification works." The nudge triggers the save; Claude provides the understanding.

### 4. Distill enhancement

Add trace extraction to the distill prompt:

```
Additionally, look for exploration patterns in the transcript — sequences where
the assistant searched for symbols, read multiple files, and traced how code flows
connect across files. For each exploration trace found, extract:
- Type: "trace"
- Text: Structured trace (file:line → file:line with function names) followed by
  a prose explanation of what was discovered
- Files: All files involved in the trace
- Salience: 0.8
```

The enriched session JSONL (with grep queries and line ranges) gives the LLM enough context to reconstruct traces even from truncated transcripts.

**Deduplication**: Existing near-duplicate check (0.90 cosine threshold) prevents distill from creating duplicates of traces Claude already saved during the session.

### 5. Retrieval integration

**File-recall hook** (`dualmem-file-recall.sh`):
- Add `trace` to the type filter (currently: `warning`, `decision`, `map`)
- When Claude opens `contact-methods.ts`, it sees:
  ```
  [trace] OTP verification: routes/index.ts:320 → contact-methods.ts:187 → :203 (0.80)
  ```

**Context assembly** (`detailSortScore` in `dualmem.go`):
- `trace` gets same type priority as `map` (both represent structural understanding)
- Intent-aware boost: `explore` intent multiplies trace score by 1.5x

**Search**: Traces are findable via `dualmem search "OTP verification"` through standard embedding similarity.

### 6. Code changes

**Go code** (minimal):

| File | Change |
|------|--------|
| `dualmem/dualmem.go` | Add `"trace"` to valid types in `AddWithOptions()`. Default salience 0.8 for trace type. Add `"trace"` to type filter in `FileContext()` (line 341). |
| `dualmem/dualmem.go` | Add `"trace"` to `typePriority()` at priority 1 (same as decision/continuity). Add `"trace"` label in `formatTypeLabel()`. |
| `dualmem/types.go` | Add `case "trace": return ip.Map` to `TypeMultiplier()` so traces inherit map's intent weights (2.0x for explore). |
| `dualmem/distill.go` | Update extraction prompt to include trace pattern recognition instructions. |

**Shell hooks**:

| File | Change |
|------|--------|
| `~/.claude/hooks/dualmem-autocapture.sh` | Add `query` and `lines` extraction from tool_input. Add tail-scan exploration detection. Add nudge output with cooldown. |
| `~/.claude/hooks/dualmem-file-recall.sh` | Add `trace` to the type filter for `file-context` query. |

**No schema changes**: Traces use existing `detail_memories` table with `type="trace"`.

## Verification

1. **Manual trace save**: `dualmem add --type trace --text "A.go:10 → B.go:20\nFlow explanation" --files "A.go,B.go"` → verify stored with salience 0.8
2. **File recall**: After saving, `dualmem file-context A.go` should return the trace
3. **Search**: `dualmem search "flow explanation"` should find the trace
4. **Context assembly**: `dualmem context "explore the codebase"` should include the trace with explore-intent boost
5. **Hook detection**: Simulate 4+ grep/read calls on 3+ files in a session, verify nudge output appears
6. **Cooldown**: Verify nudge doesn't repeat within 10 minutes
7. **Distill extraction**: Run distill on a transcript containing an exploration sequence, verify trace type is extracted
8. **Deduplication**: Save a trace manually, then run distill on same session — verify no duplicate
