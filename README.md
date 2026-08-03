# geoffreyengram

DualMem is harness-neutral, cross-session project memory. One project namespace is shared by Claude Code, Codex, and pi, so a decision saved in one harness is available to the others.

The first-party integrations use the same lifecycle runtime but do not claim identical hooks:

| Harness | Installed lifecycle coverage | Limitation |
| --- | --- | --- |
| Claude Code | session start, prompt, file read, file write | Native Claude hooks only. |
| Codex | session start, prompt, `apply_patch` file writes | No general file-read hook; only `apply_patch` writes are observed. |
| pi | session start, file read/write, session end, tool access; prompt when the installed pi supports `before_agent_start` | Prompt injection is feature-detected, not assumed. |

Codex and pi transcript distillation are Phase 2 work. Lifecycle context and activity recording are available now; do not expect a transcript reader for those harnesses.

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

The installer writes a shared launcher at `~/.config/dualmem/bin/dualmem-run` and a credential environment file at `~/.config/dualmem/env` (mode `0600`). It adds only its own hooks or managed instruction blocks, creates backups before updates, refuses symlink targets and changed-after-planning files, and removes only content it can prove it owns. Always review a dry run. `dualmem integrate doctor` is offline-only: it checks local paths, modes, credentials-shaped literals, project identity, and integration drift without constructing a provider or contacting a network.

Targeted uninstall is equally deliberate:

```bash
dualmem integrate --harness codex --uninstall --dry-run
dualmem integrate --harness codex --uninstall
```

The shared launcher remains while another managed harness needs it. If a pi extension was modified but still references that launcher, uninstall stops rather than leaving a broken extension.

### Credentials and rotation

Keep provider credentials as environment references in `~/.config/dualmem/env`; hooks and instructions should never contain literal key values. During installation, recognized static credentials from old Claude or Codex DualMem hook commands can be migrated into that file. Ambiguous shell expressions and conflicting values are rejected rather than guessed.

To rotate a key, update the provider credential in the shared env file (preserving mode `0600`), restart the affected harness, and run `dualmem integrate doctor`. The dry run, installer output, and doctor output deliberately do not print key values.

## One project identity across harnesses

DualMem resolves a Git repository’s common directory, so its main checkout and linked worktrees use one namespace. For compatibility with existing stores, the runtime’s default namespace is the hidden legacy key `claude:<project>`—for example, `claude:geoffreyengram`—even when the caller is Codex or pi. That is an implementation compatibility key, not a Claude-only memory space. A non-Git directory can fall back to its directory name; `integrate doctor` reports that weaker identity.

## DualMem Event v1

`DualMem Event v1` is the boundary for future harnesses: adapters translate native hook payloads into a normalized event, or a harness invokes `dualmem event` directly. The protocol supports session start, prompt, file read/write, and session end events; the runtime returns injected context, recorded activity, or no action. See [the harness protocol](docs/harness-integration.md) for the JSON schema, size limits, versioning, and fail-open contract.

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
| [pi](docs/pi-integration.md) | pi extension, feature detection, and coverage limits |
| [Legacy migration](docs/migration-from-legacy-engram.md) | Safe move from `engram` and inline hooks |
| [Testing](docs/testing.md) | Default, legacy, and live-provider test commands |

## License

MIT
