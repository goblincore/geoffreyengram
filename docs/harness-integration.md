# Harness integration protocol

DualMem uses **DualMem Event v1** as the harness-neutral lifecycle boundary. A harness may translate its native hook payload through `dualmem hook --adapter claude|codex`, or invoke `dualmem event` with a normalized JSON event. pi uses the latter route through its installed extension.

## Event envelope

Send one JSON object on standard input to `dualmem event`:

```json
{
  "schema_version": "1.0",
  "kind": "prompt",
  "harness": "future-harness",
  "cwd": "/workspace/repo",
  "session_id": "optional-session-id",
  "prompt": "Map the authentication flow",
  "files": ["auth.go"],
  "artifact_ref": "optional-transcript-reference",
  "tool": { "name": "read", "phase": "pre" },
  "metadata": { "optional": "string" }
}
```

| Field | Required | Meaning |
| --- | --- | --- |
| `schema_version` | yes | A numeric `MAJOR.MINOR` protocol version such as `1.0`; major version `1` is supported. |
| `kind` | yes for useful runtime work | `session_start`, `prompt`, `file_read`, `file_write`, or `session_end`. |
| `harness` | yes | The caller’s stable harness name. |
| `cwd` | yes | Directory used for project resolution and relative paths. |
| `project.root`, `project.namespace` | no | Explicit project identity override. |
| `session_id`, `prompt`, `files`, `tool`, `metadata` | no | Lifecycle-specific data. |
| `artifact_ref` | no | Preserved only for `session_end`. |

Input is limited to 64 KiB. Metadata is limited to 32 entries and each value to 1024 bytes. Relative file paths are normalized against `cwd` and deduplicated.

## Response envelope

`dualmem event` writes one JSON response:

```json
{
  "schema_version": "1.0",
  "action": "inject_context",
  "context": "Relevant project memory",
  "diagnostics": []
}
```

`action` is one of:

- `inject_context` when relevant context is available;
- `recorded` when lifecycle activity was stored; or
- `none` when there is nothing to inject or record.

`diagnostics` contains stable, redacted lifecycle codes when memory processing is unavailable. The encoded response, including its trailing newline, is capped at 24 KiB; context is shortened before it can exceed that bound.

## Versioning and failure behavior

Version 1 accepts numeric versions whose major component is `1` (for example `1.0` or `1.12`). Values such as `1.bogus` are invalid. Unknown JSON fields and unknown event kinds are accepted for forward compatibility; the current runtime returns `none` for an event kind it does not implement. A new incompatible protocol requires a new major version.

Lifecycle execution is fail-open. Invalid input or unavailable memory normally produces a versioned no-action response with redacted diagnostics and exits successfully so the calling harness is not blocked. Native Claude and Codex adapters emit their native empty response when no context can be supplied.

If the normal response itself cannot be encoded within the output bound, the emergency fallback is the bare JSON object `{}` plus a redacted diagnostic on standard error; it is not a normal response envelope. Command-line argument errors are different: they exit with status `2`.

## Shell-free invocation

Harnesses should use an argument-vector API, not concatenate an untrusted prompt into a shell command. This TypeScript-shaped example sends the normalized envelope without a shell:

```ts
import { execFile } from "node:child_process";

const child = execFile("dualmem", ["event"], { cwd: repoRoot });
child.stdin.end(JSON.stringify({
  schema_version: "1.0",
  kind: "prompt",
  harness: "my-harness",
  cwd: repoRoot,
  prompt: userPrompt,
}));
```

The bundled launcher, `~/.config/dualmem/bin/dualmem-run`, loads the protected shared environment file before executing the configured `dualmem` binary. A future harness may use it for explicit user-initiated DualMem commands. Automatic lifecycle calls must not treat repository configuration as authority to select providers, endpoints, credential variable names, or storage paths.

## Automatic lifecycle security boundary

The first-party installer registers only lifecycle signals that the subprocess can handle locally: file reads/writes and session-end activity where the harness exposes them. It does not register provider-backed session-start or prompt hooks. Although those kinds remain reserved by Event v1, the automatic CLI runner returns a redacted fail-open response for them; task-aware provider retrieval remains an explicit `dualmem context`, search, consult, or tool action.

For automatic calls, the CLI loads trusted user configuration and accepts only `default_namespace` from a repository `.dualmem.yaml`. File reads use the configured local SQLite store without initializing an embedding or synthesis provider. File writes and session end record bounded metadata-only activity. These paths make no provider network request.

Prompt duplicate suppression, when a trusted in-process caller supplies a memory implementation, is an in-memory runtime optimization only. Its cache lasts for that `Runtime` process. Separately launched hook subprocesses do not share it and must not claim cross-invocation deduplication.

## Project identity

The runtime resolves Git’s common directory, so a primary checkout and its linked worktrees share a project identity. By default that identity uses the compatibility namespace `claude:<project>`. This preserves existing memory stores across Claude Code, Codex, pi, and future adapters; it does not create separate per-harness namespaces.
