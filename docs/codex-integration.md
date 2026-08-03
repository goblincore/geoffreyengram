# Codex integration

DualMem’s Codex integration shares the same per-project memory namespace as Claude Code and pi. Install it through the safe installer rather than copying a hook by hand:

```bash
dualmem integrate --harness codex --dry-run
dualmem integrate --harness codex
dualmem integrate doctor --json
```

The installer manages only its entries in:

- `~/.codex/hooks.json` for lifecycle hooks; and
- `~/.codex/AGENTS.md` for a marked DualMem instruction block.

It uses `~/.config/dualmem/bin/dualmem-run` so credentials stay in `~/.config/dualmem/env` rather than in Codex configuration. Review the dry-run paths and actions before applying. Existing unrelated hooks and AGENTS.md content remain in place.

## Coverage and limits

| Codex signal | DualMem behavior |
| --- | --- |
| Session start | Injects project and shared-infrastructure context when available. |
| User prompt | Injects task-relevant context; trivial acknowledgements are suppressed. |
| `apply_patch` completion | Records changed paths parsed from the patch. |

Each Codex hook launches a separate DualMem process, so the runtime's in-memory duplicate-prompt cache does not persist across hook invocations. Do not rely on duplicate suppression in the installed integration. Codex also does not currently provide a general file-read lifecycle integration here, and edits outside `apply_patch` are not observed by the installed adapter. Codex transcript distillation is Phase 2; this integration does not read or distill Codex conversation transcripts.

## Restart and trust

After applying the plan, restart Codex so it reloads `hooks.json` and `AGENTS.md`. On the next session start, confirm that the launcher path is trusted by your local Codex configuration and that `dualmem integrate doctor --json` reports installed capabilities rather than missing integration. If your installation prompts before running a new hook command, approve only the reviewed launcher path—not a copied command containing provider credentials.

For removal, review the targeted plan first:

```bash
dualmem integrate --harness codex --uninstall --dry-run
dualmem integrate --harness codex --uninstall
```

The installer removes only its managed hooks and marked instructions. It keeps the shared launcher if Claude Code or pi still uses it.
