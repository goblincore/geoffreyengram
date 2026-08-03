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
| `session_start` | Injects project and shared-infrastructure context. |
| `tool_call` read | Supplies file-scoped context when available. |
| `tool_result` edit/write | Records file activity. |
| `session_shutdown` | Records session-end activity. |
| `before_agent_start` | Injects prompt context only when the installed pi exposes this hook. |
| Registered `dualmem` tool | Runs approved DualMem subcommands through an argument vector. |

Prompt support is probed from the installed pi type definitions during planning; it is not guaranteed across pi versions. pi transcript distillation is Phase 2, so session-end activity does not imply transcript ingestion.

## Restart and trust

Restart pi after installation so it reloads its extension directory. On startup, allow the extension only after reviewing the dry-run output and verifying that the command points to the shared launcher. Then use `dualmem integrate doctor --json` to confirm the extension and instruction block are both present.

To uninstall, plan first:

```bash
dualmem integrate --harness pi --uninstall --dry-run
dualmem integrate --harness pi --uninstall
```

If you edited `dualmem.ts` and it still refers to the shared launcher, uninstall refuses to remove the launcher. Remove or update that dependency intentionally, then rerun the plan.
