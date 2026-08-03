# pi integration

The pi integration is a native extension that sends normalized `DualMem Event v1` envelopes to the shared launcher. Install it through the planner:

```bash
dualmem integrate --harness pi --dry-run
dualmem integrate --harness pi
dualmem integrate doctor --json
```

The installer creates or updates:

- `~/.pi/agent/extensions/dualmem.ts`; and
- `~/.pi/agent/AGENTS.md` (a marked instruction block).

The extension calls `~/.config/dualmem/bin/dualmem-run` by default, or the `DUALMEM_RUN` environment override. It never embeds a provider credential.

## Coverage and limits

| pi signal | DualMem behavior |
| --- | --- |
| `tool_call` read | Supplies file-scoped context when available. |
| `tool_result` edit/write | Records file activity. |
| `session_shutdown` | Records session-end activity. |
| Registered `dualmem` tool | Runs approved DualMem subcommands through an argument vector. |

Automatic session-start and prompt handlers are deliberately omitted: they would require provider-backed retrieval from an automatic subprocess. The registered tool remains the explicit route for `context`, search, consult, add, and checkpoint operations. Local lifecycle failures, response diagnostics, and successful-exit stderr are surfaced only as the redacted warning `dualmem lifecycle unavailable`; raw diagnostic text is never shown. pi transcript distillation is Phase 2, so session-end activity does not imply transcript ingestion.

## Restart and trust

Restart pi after installation so it reloads its extension directory. On startup, allow the extension only after reviewing the dry-run output and verifying that the command points to the shared launcher. Then use `dualmem integrate doctor --json` to confirm the extension and instruction block are both present.

To uninstall, plan first:

```bash
dualmem integrate --harness pi --uninstall --dry-run
dualmem integrate --harness pi --uninstall
```

If you target pi itself and a modified `dualmem.ts` still refers to the shared launcher, uninstall refuses the plan. During a targeted Claude or Codex uninstall, a retained noncanonical pi extension that mentions `dualmem-run` or `DUALMEM_RUN` keeps both the shared launcher and environment file even when pi's managed instruction block is missing. Remove or update that dependency intentionally before expecting common assets to be removed.
