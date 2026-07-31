# Harness-Agnostic DualMem Integration Design

**Date:** 2026-07-31  
**Status:** Approved design  
**Scope:** A harness-neutral DualMem protocol with first-party Claude Code, Codex, and pi integrations using one database and one project identity

## Summary

DualMem already stores useful project knowledge independently of any model harness, but its installation and event handling are currently Claude Code-specific. Importing that setup into Codex copied incompatible hook matchers and split namespace guidance. The existing pi extension reaches the same store, but it independently reimplements namespace resolution, credentials, lifecycle behavior, and command invocation with user-specific paths.

The integration will be reorganized around a versioned, harness-neutral event protocol. Claude Code, Codex, and pi become first-party adapters that translate native lifecycle data into the same semantic events and invoke one runtime. Unknown future harnesses can emit the normalized protocol directly without changing memory semantics. Harness drivers handle installation and diagnostics separately from the protocol.

Phase 1 delivers the protocol, project identity boundary, three adapters, three installation drivers, security repair, diagnostics, documentation, and a trustworthy default test suite. Phase 2 adds Codex and pi transcript readers. Phase 3 improves project identity and memory quality without changing the adapter contract.

## Investigation Findings

### What works today

- `dualmem` is installed at `~/go/bin/dualmem`; Claude Code, Codex, and pi can invoke it.
- All three harnesses can use the SQLite database configured at `~/.local/share/dualmem/memories.db`.
- Running DualMem inside `/Users/donny/Projects/2026/geoffreyengram` resolves the existing namespace `claude:geoffreyengram`.
- Existing Geoffrey Engram context and facts are available under that namespace.
- Claude Code has working structured hooks and transcript distillation.
- Codex loads global `AGENTS.md` instructions and has its lifecycle-hook feature enabled.
- Pi's extension already handles session start, structured reads, touched-file logging, and a native DualMem tool.

### What is broken or unsafe

- The repository bootstrap path configures only `~/.claude` and overwrites settings instead of merging DualMem-owned entries.
- An installed hook embeds Gemini and Z.AI credential values in an executable script and is not sufficiently permission-restricted.
- The Codex import copied Claude matcher names such as `Read`, `Glob`, and `Grep`. Codex's unified execution and canonical `apply_patch` payloads do not follow that contract.
- Imported Codex instructions changed `claude:*` namespaces to `Codex:*`, splitting one project into parallel memory spaces.
- A projectless Codex task uses its generated task directory as `cwd`; it cannot safely infer a repository without a project hint.
- The pi extension hardcodes the DualMem binary and a Claude-owned environment-file path.
- Pi computes `claude:<basename(cwd)>` itself, which can fragment worktrees and duplicates logic already present in the CLI.
- Pi duplicates runtime concerns such as timeouts, error formatting, file-index caching, and command construction.
- Pi has no prompt-aware retrieval and its transcript format is not supported by the current distiller.
- DualMem distillation currently understands Claude Code transcripts but not Codex or pi transcripts.
- Query retrieval can fail when the embedding provider is unavailable even though some context paths can degrade to lexical or pinned output.
- The root `engram` package is the original character/NPC memory engine. Current development and documentation focus on `dualmem/`.
- The root tests include provider-backed and socket-backed behavior that should not define the supported default verification gate.
- Two contextual benchmark tests assert an old strict-file contract even though commit `384bb934` intentionally introduced module-aware precision and sibling-aware recall.

## Goals

1. Define memory lifecycle semantics without coupling them to a particular agent harness.
2. Make Claude Code, Codex, and pi first-party integrations over the same protocol and project identity.
3. Allow a future harness to integrate immediately by emitting normalized events, without modifying the engine.
4. Preserve each harness's native strengths while reporting capability differences honestly.
5. Keep existing `claude:*` memories available throughout migration.
6. Prevent credentials from appearing in generated code, configuration output, logs, or version control.
7. Make installation repeatable, idempotent, non-destructive, diagnosable, and selectively reversible.
8. Formally deprecate the original root `engram` package and restore an offline default test suite.
9. Define follow-up transcript support without delaying Phase 1 correctness and security work.

## Non-Goals

- Requiring every harness to expose identical lifecycle capabilities.
- Replacing a harness's own built-in memory feature.
- Parsing arbitrary shell syntax to infer filesystem activity.
- Replacing DualMem's database, ranking, or context-assembly architecture in Phase 1.
- Migrating existing namespaces before aliases and rollback behavior exist.
- Shipping a long-running daemon or remote event service.
- Loading arbitrary executable adapter plugins into the DualMem process.
- Deleting the deprecated root package in this PR.

## Considered Approaches

### 1. Versioned neutral event protocol with adapters — selected

Define one semantic event envelope and one runtime. First-party adapters translate Claude, Codex, and pi payloads; future harnesses may emit the envelope directly. Installation drivers are independent of event processing.

This makes harness identity metadata rather than memory identity, prevents duplicated lifecycle logic, and gives adapter implementations deterministic contract tests.

### 2. CLI conventions only

Documenting commands for each harness would require less initial code, but every integration would duplicate project resolution, caching, output shaping, redaction, timeout, and fail-open behavior. Existing pi code demonstrates that drift already occurs under this model.

### 3. Background daemon or event bus

A daemon could centralize credentials and background work, but it adds lifecycle, packaging, locking, and recovery complexity before the local event contract has stabilized. The event protocol leaves this option open later without requiring it now.

## Architecture

```text
Claude lifecycle payload ─> Claude adapter ─┐
Codex lifecycle payload ──> Codex adapter ──┼─> DualMem Event v1 ─> policy/runtime ─> shared store
pi extension event ───────> pi adapter ─────┤
future harness ───────────> normalized JSON ┘

                                 project identity resolver ────────┘
```

The system has four independently testable layers:

1. **Protocol:** versioned semantic events and responses.
2. **Adapters:** native payload translation only; no memory policy.
3. **Runtime:** project resolution, retrieval/capture policy, caching, redaction, and fail-open behavior.
4. **Harness drivers:** detection, installation, diagnosis, and removal of DualMem-managed configuration.

The engine and store never branch on Claude, Codex, or pi. A harness name may be retained as provenance, but it cannot select a different namespace or memory policy by itself.

## DualMem Event v1

The stable input boundary is a JSON envelope accepted by `dualmem event`:

```json
{
  "schema_version": "1.0",
  "kind": "session_start",
  "harness": "pi",
  "cwd": "/path/to/project",
  "project": {
    "root": "",
    "namespace": ""
  },
  "session_id": "opaque-session-id",
  "prompt": "",
  "files": [],
  "artifact_ref": "",
  "tool": { "name": "", "phase": "" },
  "metadata": {}
}
```

`project.root` is an optional repository path supplied by the harness; `project.namespace` is an explicit user-selected storage namespace and takes precedence. `artifact_ref` is an optional local transcript or activity-log path and is accepted only for `session_end`. Required fields otherwise depend on `kind`; absent optional fields use zero values. `metadata` is a bounded map of sanitized scalar values chosen by the adapter and remains opaque to the runtime unless a future protocol version standardizes a key. It must not contain full patches, file contents, transcripts, environment dumps, or secrets.

Supported Phase 1 event kinds:

- `session_start`: request bounded project and optional infrastructure context.
- `prompt`: request task-aware context for a substantive prompt.
- `file_read`: retrieve file-scoped warnings, decisions, and landmarks.
- `file_write`: record touched-file metadata and co-change evidence.
- `session_end`: record a local transcript/log reference when available.

Explicit operations such as search, add, checkpoint, consult, and cochange remain stable CLI/tool APIs rather than being forced through lifecycle events.

Protocol compatibility follows semantic versioning. Readers accept unknown optional fields and unknown event kinds as non-fatal no-ops. An unsupported major version produces a concise diagnostic without blocking the harness action.

`dualmem hook --adapter <name>` remains the compatibility entry point for native hook payloads. It translates through a registered adapter and then invokes the same runtime as `dualmem event`. The normalized command is the documented path for future harnesses that do not need a bundled adapter.

The runtime returns a neutral response before adapter formatting:

```json
{
  "schema_version": "1.0",
  "action": "inject_context",
  "context": "bounded model-visible text",
  "diagnostics": []
}
```

`action` is `inject_context`, `recorded`, or `none`. Diagnostics contain codes and redacted messages, never raw input. Native adapters convert this response into the exact output shape required by their harness.

## Adapter Contract

An adapter has one purpose: translate a native event into zero or more `DualMem Event v1` envelopes and format the runtime response in the harness's accepted shape.

First-party adapters are compiled and registered explicitly. The registry avoids a closed client enum while keeping execution auditable. Unknown third-party harnesses do not need in-process code: they emit normalized JSON. Arbitrary executable plugin loading is excluded from Phase 1 for security and packaging simplicity.

Adapters must:

- preserve structured file paths when provided;
- clean and deduplicate paths without requiring them to exist;
- treat unrecognized native tools as no-ops;
- never execute or reinterpret shell text;
- avoid placing raw patches or file contents into event metadata;
- return deterministic parse errors suitable for redacted diagnostics.

Codex `apply_patch` envelope paths are structured enough to extract without evaluating patch content. Codex `Bash` commands remain opaque activity metadata. Claude uses structured hook payloads. Pi's TypeScript extension emits the normalized envelope directly wherever its extension API exposes sufficient structure.

## Project Identity and Namespace Compatibility

Project identity resolution is a runtime service, not adapter logic. It resolves, in order:

1. An explicit `project.namespace` supplied by the user.
2. A supplied `project.root`, resolved through its Git common directory when applicable.
3. The event `cwd`, resolved through its Git common directory so linked worktrees share the main repository identity.
4. A configured project identity.
5. The current directory basename only when no Git identity exists and projectless behavior is explicitly allowed.

Phase 1 maps the resolved Geoffrey Engram identity to `claude:geoffreyengram` because that storage key contains existing data. The `claude:` prefix is documented as a legacy storage namespace, not ownership by a harness. Adapters receive only the resolved project context; they do not concatenate namespace prefixes.

Cross-cutting tooling remains under the existing `claude:infra` key in Phase 1. The configuration surface refers to it as the infrastructure namespace, hiding its legacy storage spelling from adapter code.

Phase 3 introduces stable vendor-neutral identities, preferably configured in `.dualmem.yaml` and otherwise derived from a normalized Git remote. Aliases map legacy keys to stable identities. Migration is additive and reversible until alias behavior is verified.

Projectless sessions do not borrow a guessed repository namespace. They may receive infrastructure context only unless the harness provides a project root or the user selects a project explicitly.

## Lifecycle and Runtime Policy

### Session start

Load a bounded project context plus optional infrastructure context. Pre-warm the file-memory index. Retrieval failure is non-blocking.

### Prompt-aware retrieval

Use substantive prompt text as a task-aware query. Skip acknowledgements and duplicate prompt hashes. Cache by session, project identity, and query hash to avoid duplicate injection and provider calls.

### File-scoped retrieval

When exact paths are available, return relevant warnings, decisions, maps, and traces. Claude can gate structured reads. Pi can steer context before processing read results. Codex reliably supplies structured edit paths but has limited pre-read interception, so its instructions retain an explicit `file-context` fallback.

### Capture

Record file-touch telemetry separately from durable semantic memory. File-write events may associate paths and provenance, but never persist a patch body or full file content. Durable decisions, warnings, architecture findings, requirements, investigations, and checkpoints remain explicit operations.

### Session end and distillation

Phase 1 accepts a local transcript/log reference and records availability. Existing Claude distillation remains intact. Phase 2 adds versioned Codex and pi transcript readers that normalize supported turns and tool events into the existing distillation model. Malformed or unknown transcript records are skipped with diagnostics; command text is never executed.

## Capability Model

Drivers advertise supported capabilities so installation and doctor output do not imply false parity.

| Capability | Claude Code | Codex | pi |
|---|---:|---:|---:|
| Session start | Yes | Yes | Yes |
| Prompt-aware context | Yes | Yes | Extension-dependent |
| Pre-read context | Yes | Limited | Yes |
| Structured writes | Yes | `apply_patch` | Yes |
| Native DualMem tool | Instructions/CLI | Skill/CLI | Yes |
| Transcript distillation | Existing | Phase 2 | Phase 2 |

A driver reports the installed capability set and any unavailable optional capability. Lack of transcript support is a warning or informational finding, not a broken installation.

## Installation and Configuration

The repository provides one entry point:

```text
dualmem integrate --harness claude|codex|pi|all [--home PATH] [--dry-run]
```

`all` installs only detected harnesses unless a harness is explicitly named. Each first-party driver implements the same operations:

- `detect`: inspect whether the harness and relevant configuration exist;
- `plan`: calculate owned creates/updates without writing;
- `apply`: back up and atomically write only planned managed content;
- `doctor`: report installed capabilities and unsafe or inconsistent state;
- `uninstall`: remove only marked DualMem-owned entries and files.

The common installer:

1. Preserves unrelated settings and instruction text through structural merges and stable managed markers.
2. Creates timestamped backups before the first material rewrite.
3. Installs adapters or wrappers that source credentials at runtime.
4. Stores credentials only in `~/.config/dualmem/env` with mode `0600` or accepts an already-exported environment.
5. Uses mode `0700` for generated executable wrappers and extension directories/files where applicable.
6. Never displays credential values in dry-run output, diffs, logs, or errors.
7. Reports harness-specific trust, reload, or restart steps.
8. Produces no filesystem changes on a second identical run.

For pi, the driver replaces the current hardcoded extension with a generated thin extension. The extension emits normalized events, delegates namespace and policy to the runtime, and retains pi's useful native DualMem tool. Command execution uses argument arrays, shared configuration, bounded timeouts, and redacted errors.

Migration may read an existing credential-bearing hook once to move values into the protected env file, but it must never print them. If extraction is ambiguous, installation stops before modification and reports a manual step. After verified migration, the old hook is replaced and exposed provider keys are rotated by the user.

## Diagnostics

`dualmem integrate doctor` inspects integration state without constructing a provider-backed engine or making network calls. It reports:

- detected harnesses and installed capability sets;
- resolved project identity, storage namespace, and worktree/projectless status;
- missing adapters, hooks, extensions, managed instructions, or native-tool registration;
- incompatible matcher/tool names;
- credential-variable presence and file-permission safety;
- literal credential-shaped values embedded in commands or generated files;
- split legacy namespaces containing divergent data;
- transcript-reader availability by harness;
- idempotence drift between installed state and the current integration plan.

Provider reachability remains part of the existing engine health command, not installation doctor. This keeps integration diagnosis deterministic and available offline.

## Error Handling and Security

- Hooks are advisory and fail open. Parse, lookup, timeout, and persistence failures emit short redacted diagnostics and allow the harness action to continue.
- Invalid CLI arguments and unsafe installer input fail closed before writes.
- Configuration writes use same-directory temporary files, restrictive modes, close/sync, and atomic rename.
- Hook input has a size limit; response context has a token/byte limit.
- No credential value is accepted on the command line.
- Hook output is model-visible and never includes environment dumps or credential-bearing command text.
- Adapter metadata is bounded and filtered through an allowlist.
- Arbitrary adapter code is not dynamically loaded in Phase 1.
- The current Gemini and Z.AI credentials must be rotated because literal values appeared in an imported hook and diagnostic output.

## Legacy Package and Test Suite Policy

The root `engram` package is formally deprecated because the project now focuses on `dualmem/`:

- Add Go package documentation with `Deprecated:` pointing to `github.com/goblincore/geoffreyengram/dualmem` and the CLI.
- Keep the implementation available during the compatibility window.
- Move root-package tests behind a `legacy` build tag.
- Document `go test -tags legacy .` for compatibility maintainers.
- Require `DUALMEM_LIVE_TESTS=1` for every provider-backed test; merely having an API key never activates network tests.
- Preserve meaningful socket-backed legacy tests under the legacy tag rather than deleting them because a restricted sandbox cannot bind a port.

The contextual benchmark remains maintained. Its names, comments, and expectations will match the module-aware contract introduced by commit `384bb934`: files in a ground-truth module count toward precision, and sibling results cover ground-truth files in the same module for recall. Tests are removed only when their behavior is deprecated or duplicated by stronger coverage.

## Testing Strategy

Tests use temporary homes, temporary Git repositories, and sanitized payload/transcript fixtures. No test reads real credentials or modifies live harness configuration.

Phase 1 verification includes:

1. Hermetic tests for `dualmem/...`, command packages, and maintained benchmarks.
2. Protocol schema, version compatibility, size-bound, and metadata-filter tests.
3. Adapter fixtures for Claude, Codex, and pi native events.
4. Cross-adapter contract tests proving equivalent events produce identical runtime operations.
5. Project identity tests covering main repositories, linked worktrees, explicit hints, and projectless folders.
6. Driver tests covering detection, structural merge, managed markers, backups, permissions, dry run, idempotence, and targeted uninstall.
7. Doctor fixtures for capability gaps, mismatched hooks, split namespaces, embedded-secret detection, and permission failures.
8. Optional legacy tests behind `-tags legacy`.
9. Optional provider-backed tests behind `DUALMEM_LIVE_TESTS=1`.

Acceptance requires:

- Default verification performs no network calls and requires no listening sockets.
- Present provider keys do not activate live tests without explicit opt-in.
- The root package is marked deprecated and excluded from the default gate.
- Contextual metric tests enforce the maintained module-aware contract.
- Repeated installation preserves unrelated Claude, Codex, and pi configuration and becomes a no-op.
- Generated files contain credential variable names but no credential values.
- Permission modes are correct.
- All three harnesses resolve the Geoffrey Engram repository to `claude:geoffreyengram` through the shared resolver.
- Equivalent session, prompt, read, and write fixtures yield equivalent runtime calls where each harness advertises the capability.
- Unsupported events and unsupported protocol major versions fail open with deterministic diagnostics.
- Doctor distinguishes missing optional transcript support from a broken Phase 1 installation.

Phase 2 fixture checks require Claude, Codex, and pi transcripts describing equivalent work to produce equivalent normalized session records. Unknown transcript entries must not prevent supported records from being distilled, and transcript command text must never execute.

## Delivery Phases

### Phase 1: Harness-neutral correctness, security, and installation

- Formally deprecate the root package and separate legacy/live tests from the default gate.
- Repair stale contextual benchmark expectations.
- Add `DualMem Event v1`, the shared runtime, and project identity resolver.
- Add first-party Claude, Codex, and pi adapters.
- Add Claude, Codex, and pi integration drivers plus normalized-event documentation for future harnesses.
- Replace credential-bearing and hardcoded hooks/extensions with shared runtime configuration.
- Add offline integration doctor, dry-run, and targeted uninstall.
- Refresh the README around harness-neutral project memory, deprecation, installation, capabilities, and test tiers.
- Migrate and verify the current machine through the released installer.

### Phase 2: Transcript and passive-capture parity

- Add versioned Codex and pi transcript readers.
- Normalize supported turn, tool, and session metadata.
- Distill all supported harnesses into the same project identity and memory types.
- Add fixture compatibility tests for known transcript versions.

### Phase 3: Memory usefulness and stable identity

- Introduce stable vendor-neutral identities with legacy namespace aliases.
- Add robust offline lexical retrieval across search paths.
- Capture provenance and explicit useful/irrelevant/misleading feedback.
- Improve supersession, decay, and stale-memory reporting.
- Keep semantic memory separate from activity telemetry.

## Documentation

The README will lead with harness-neutral, per-project memory and list Claude Code, Codex, and pi as first-party integrations. It will document the normalized event protocol for other harnesses, capability differences, the legacy `claude:*` storage key, `dualmem integrate --harness all`, dry-run, doctor, uninstall, credential handling, package deprecation, and default/legacy/live test commands.

Harness-specific setup guides cover native configuration and limitations. The human-readable Obsidian investigation note mirrors the architecture, implementation phases, security follow-up, and future ideas without containing secrets.

## Rollout and Recovery

The installer creates timestamped backups and reports their paths. Dry-run output is reviewed before applying changes. Harness integrations can be installed or removed independently without changing the database. Existing `claude:*` data is never deleted or rewritten in Phase 1.

If a hook or extension causes latency or errors, its managed integration can be removed while instruction-driven CLI recall remains available. The current hand-written pi extension and imported Claude/Codex hooks remain recoverable from backups until all three harnesses pass doctor and event-contract smoke tests.

The draft PR will include this design, the revised Phase 1 implementation plan, implementation, tests, and documentation. Transcript readers and namespace migration remain explicitly out of scope for that PR.
