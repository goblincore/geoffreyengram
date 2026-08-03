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
| `schema_version` | yes | A `1.x` protocol version. |
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

Version 1 accepts any `1.x` major version. Unknown JSON fields and unknown event kinds are accepted for forward compatibility; the current runtime returns `none` for an event kind it does not implement. A new incompatible protocol requires a new major version.

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

The bundled launcher, `~/.config/dualmem/bin/dualmem-run`, loads the protected shared environment file before executing the configured `dualmem` binary. A future harness should call that launcher if it needs the same credential environment.

## Project identity

The runtime resolves Git’s common directory, so a primary checkout and its linked worktrees share a project identity. By default that identity uses the compatibility namespace `claude:<project>`. This preserves existing memory stores across Claude Code, Codex, pi, and future adapters; it does not create separate per-harness namespaces.
