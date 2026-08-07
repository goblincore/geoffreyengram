# geoffreyengram

DualMem is harness-neutral, cross-session project memory. One project namespace is shared by Claude Code, Codex, and pi, so a decision saved in one harness is available to the others.

The first-party integrations use the same lifecycle runtime but do not claim identical hooks:

| Harness | Installed lifecycle coverage | Limitation |
| --- | --- | --- |
| Claude Code | file read and file write | Automatic provider-backed session/prompt retrieval is not installed. |
| Codex | `apply_patch` file writes | No general file-read hook; edits outside `apply_patch` are not observed. |
| pi | file read/write, session end, and explicit tool access | Automatic provider-backed session/prompt retrieval is not installed. |

Codex and pi transcript distillation are Phase 2 work. Local file context and activity recording are available now; use the explicit `dualmem context` or harness tool workflow for task-aware retrieval.

## Install safely

Install the CLI, then plan changes before applying them:

```bash
go install github.com/goblincore/geoffreyengram/cmd/dualmem@latest
dualmem integrate --harness all --dry-run
dualmem integrate --harness all
dualmem integrate doctor --json
```

`--harness all` configures only detected harnesses. To configure a specific harness even before it has existing configuration, select it explicitly:

```bash
dualmem integrate --harness codex --dry-run
dualmem integrate --harness codex
```

The installer writes a shared launcher at `~/.config/dualmem/bin/dualmem-run` and a credential environment file at `~/.config/dualmem/env` (mode `0600`). Shared prerequisites are published before dependent harness configuration; uninstall removes harness entries before shared cleanup. It adds only its own hooks or managed instruction blocks, creates backups before updates, refuses symlink targets and changed-after-planning files, and removes only content it can prove it owns. Always review a dry run. `dualmem integrate doctor` is offline-only: it checks local paths, modes, credentials-shaped literals, project identity, and integration drift without constructing a provider or contacting a network.

Targeted uninstall is equally deliberate:

```bash
dualmem integrate --harness codex --uninstall --dry-run
dualmem integrate --harness codex --uninstall
```

The shared launcher remains while another managed harness needs it. A targeted pi uninstall stops if a modified extension still references that launcher. Modified noncanonical Claude, Codex, or pi integrations that still name `dualmem-run` or `DUALMEM_RUN` retain both shared assets until that dependency is removed intentionally.

### Credentials and rotation

Keep provider credentials as environment references in `~/.config/dualmem/env`; hooks and instructions should never contain literal key values. During installation, recognized static credentials from old Claude or Codex DualMem hook commands can be migrated into that file. Ambiguous shell expressions and conflicting values are rejected rather than guessed.

To rotate a key, update the provider credential in the shared env file (preserving mode `0600`), restart the affected harness, and run `dualmem integrate doctor`. The dry run, installer output, and doctor output deliberately do not print key values.

## One project identity across harnesses

DualMem resolves a Git repository’s common directory, so its main checkout and linked worktrees use one namespace. For compatibility with existing stores, the runtime’s default namespace is the hidden legacy key `claude:<project>`—for example, `claude:geoffreyengram`—even when the caller is Codex or pi. That is an implementation compatibility key, not a Claude-only memory space. A non-Git directory can fall back to its directory name; `integrate doctor` reports that weaker identity.

## DualMem Event v1

`DualMem Event v1` is the boundary for future harnesses: adapters translate native hook payloads into a normalized event, or a harness invokes `dualmem event` directly. The protocol supports session start, prompt, file read/write, and session end events; the runtime returns injected context, recorded activity, or no action. See [the harness protocol](docs/harness-integration.md) for the JSON schema, size limits, versioning, and fail-open contract.

Automatic `event` and `hook` subprocesses have a narrower security boundary than interactive commands. They accept only repository `default_namespace` configuration, never initialize a provider, serve file context from local SQLite, and record metadata-only activity. Session-start and prompt events fail open with redacted diagnostics; provider-backed retrieval requires an explicit user or agent command.

## Everyday memory commands

```bash
dualmem add --text "Auth uses JWT, not sessions" --type decision --files "auth.go"
dualmem search "authentication"
dualmem context "fix the auth bug" --budget 3000
dualmem checkpoint --task "auth refactor" --status in_progress --files "auth.go" --done "JWT validation" --remaining "refresh tokens"
```

Memories are stored in SQLite and ranked with project, file, and task context. See [features](docs/features.md) for the memory model and [testing](docs/testing.md) for test tiers.

## Migration and deprecated interfaces

**Deprecated:** the root `engram` interface is retained only for legacy compatibility. New integrations and automation must use `dualmem`.

Existing Claude-only users should follow [migration from legacy engram](docs/migration-from-legacy-engram.md). The installer preserves unrelated hooks and instruction content, while converting recognized legacy DualMem hooks to the shared launcher.

## Documentation

| Doc | Contents |
| --- | --- |
| [Harness protocol](docs/harness-integration.md) | `DualMem Event v1`, responses, limits, and future adapters |
| [Claude Code](docs/example-claude-md.md) | Claude integration and managed instructions |
| [Codex](docs/codex-integration.md) | Codex hooks, trust/restart, and coverage limits |
| [pi](docs/pi-integration.md) | pi extension, local lifecycle coverage, and explicit tool access |
| [Legacy migration](docs/migration-from-legacy-engram.md) | Safe move from `engram` and inline hooks |
| [Testing](docs/testing.md) | Default, legacy, and live-provider test commands |

## License

MIT
