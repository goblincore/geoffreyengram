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
| `apply_patch` completion | Records changed paths parsed from the patch. |

Automatic session-start and prompt hooks are deliberately not installed: they would require sending repository prompts or stored memory to a provider from an automatic subprocess. Use the managed instructions and explicit `dualmem context`, search, or consult commands when task-aware retrieval is wanted. The Event v1 runtime's prompt cache is process-local only and does not provide cross-subprocess deduplication.

Codex does not currently provide a general file-read lifecycle integration here, and edits outside `apply_patch` are not observed by the installed adapter. Codex transcript distillation is Phase 2; this integration does not read or distill Codex conversation transcripts.

## Restart and trust

After applying the plan, restart Codex so it reloads `hooks.json` and `AGENTS.md`. Confirm that the launcher path is trusted by your local Codex configuration and that `dualmem integrate doctor --json` reports `file_write` rather than missing integration. If your installation prompts before running a new hook command, approve only the reviewed launcher path—not a copied command containing provider credentials.

For removal, review the targeted plan first:

```bash
dualmem integrate --harness codex --uninstall --dry-run
dualmem integrate --harness codex --uninstall
```

The installer removes only its managed hooks and marked instructions. It also removes obsolete managed session-start/prompt hooks from earlier DualMem installs. It keeps the shared launcher if Claude Code or pi still uses it, or if a modified noncanonical hook still references it.
