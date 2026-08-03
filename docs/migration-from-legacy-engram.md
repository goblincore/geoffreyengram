# Migration from legacy engram

**Deprecated:** the root `engram` interface is legacy compatibility only. Use `dualmem` for new setup, automation, and integrations.

This migration keeps the existing project memory namespace. DualMem’s harness runtime resolves the shared Git root (including linked worktrees) and uses the compatibility key `claude:<project>` by default. A Codex or pi session therefore sees the same store a Claude-only setup previously used.

## 1. Preserve existing data

Before any v1 data curation, create the built-in safety-net export:

```bash
dualmem archive-v1
dualmem migrate-v2
```

`archive-v1` writes a JSON archive of the v1 store; `migrate-v2` curates supported v1 detail memories and knowledge documents into durable facts. Review the output before removing any older tooling.

## 2. Plan the harness migration

Do not paste credentials into hooks. First inspect the plan:

```bash
dualmem integrate --harness all --dry-run
```

For an existing Claude setup, the installer recognizes only actual old DualMem hook invocations for the Claude adapter. It can move static, unambiguous provider assignments into `~/.config/dualmem/env` (mode `0600`) and replaces the old hook with the shared launcher. It leaves unrelated hooks and instruction content intact.

The installer stops if it sees an ambiguous command, an unsupported shell expression, or conflicting credentials within a hook, across hooks, or against the shared environment. Resolve the conflict manually in the protected environment file; do not force a key value into a hook command.

## 3. Apply and check

```bash
dualmem integrate --harness all
dualmem integrate doctor --json
```

`doctor` is offline-only and reports integration drift, insecure permissions, literal credential-shaped values, project/worktree identity, and capability availability. Findings never include credential values. Restart each configured harness after the installer changes its configuration.

## 4. Retire old manual configuration carefully

Use targeted uninstall only for the managed integration you intend to remove:

```bash
dualmem integrate --harness claude --uninstall --dry-run
```

The installer deletes only files or marked blocks it can prove it owns. It creates backups before updates, refuses symlinks and changed-after-planning targets, and keeps shared support files while another managed harness remains. Keep unrelated legacy tooling until the dry run shows exactly the intended changes.

## Legacy CLAUDE.md guidance

Old guidance that derives a namespace from the current directory should be removed. The shared runtime already resolves the project and worktree identity. New Claude instructions should tell the agent to use `dualmem` and let the runtime choose the compatibility namespace.
