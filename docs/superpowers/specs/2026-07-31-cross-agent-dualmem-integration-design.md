# Cross-Agent DualMem Integration Design

**Date:** 2026-07-31  
**Status:** Approved design  
**Scope:** Claude Code and local Codex clients using one DualMem database and one project-memory identity

## Summary

DualMem already stores useful project knowledge independently of any model harness, but its current installation path is Claude Code-specific. Importing that setup into Codex copied Claude hook matchers and rewrote namespace guidance, producing a partial integration: explicit DualMem CLI calls work, but automatic Codex recall and capture do not behave like Claude Code, and Codex writes are directed toward empty `Codex:*` namespaces instead of the existing `claude:*` memory spaces.

The integration will become adapter-based. Claude Code and Codex retain client-native hook configuration, while small adapters normalize their lifecycle payloads into shared DualMem operations. Both clients use the existing SQLite store and the same project namespace. Delivery is staged: Phase 1 fixes correctness, security, installation, startup recall, and reliable hook coverage; Phase 2 adds Codex transcript distillation and fuller passive-capture parity; Phase 3 improves memory quality and project identity.

## Investigation Findings

### What works today

- `dualmem` is installed at `~/go/bin/dualmem` and both clients can invoke it.
- Both clients read and write the same SQLite database configured at `~/.local/share/dualmem/memories.db`.
- Running DualMem inside `/Users/donny/Projects/2026/geoffreyengram` auto-detects `claude:geoffreyengram`.
- Existing Geoffrey Engram context and facts are available under `claude:geoffreyengram`.
- Codex loads the imported global `~/.codex/AGENTS.md`, so instruction-driven DualMem calls are possible.
- Codex lifecycle hooks are supported and enabled.

### What is broken or unsafe

- The repository bootstrap script configures only `~/.claude` and overwrites `settings.json` rather than merging DualMem entries into existing configuration.
- The generated file-recall hook embeds Gemini and Z.AI credential values directly in an executable script. The installed script is not permission-restricted.
- The Codex import copied Claude hook matchers such as `Read`, `Glob`, and `Grep`. Codex represents unified shell execution as `Bash`; it does not emit those Claude tool names.
- Codex allows `Edit` and `Write` as matcher aliases for `apply_patch`, but the hook payload reports the canonical tool name `apply_patch`. The imported autocapture script rejects that canonical name and exits.
- The imported Codex instructions changed namespaces from `claude:*` to `Codex:*`. DualMem itself still auto-detects `claude:<git-root>`, so the instructions split shared memory into empty parallel namespaces.
- A projectless Codex task uses its generated task directory as `cwd`; auto-detection therefore cannot select a repository namespace. Tasks must be attached to a local project, or a namespace must be supplied explicitly.
- DualMem distillation currently understands Claude Code session transcripts but not Codex transcripts.
- Query retrieval can fail when the embedding provider is unavailable even though some context assembly paths degrade to lexical or pinned-memory output.
- The root `engram` package is the original character/NPC memory engine. Its classifier code was last materially changed in March 2026, while current documentation and development focus on `dualmem/`.
- The ordinary root test suite includes a live Gemini benchmark that activates whenever a provider key happens to be present, plus HTTP tests that require local sockets. These are legacy-package concerns rather than the supported DualMem verification gate.
- Two contextual benchmark tests still assert the original strict-file metric contract even though commit `384bb934` intentionally changed precision and recall to award module and sibling coverage. The implementation and commit history agree; the tests and comments are stale.

## Goals

1. Claude Code and Codex read and write the same project memories without copying data between stores.
2. Both clients receive automatic project and infrastructure context at appropriate lifecycle points.
3. Hook behavior is native to each client and covered by deterministic adapter tests.
4. Credentials never appear in hook scripts, hook output, generated instructions, logs, or version control.
5. Installation is repeatable, idempotent, non-destructive, and diagnosable.
6. Existing `claude:*` memories remain available throughout the migration.
7. Codex transcript distillation has a defined follow-up path without delaying the correctness and security repair.
8. The original root `engram` package is formally deprecated and separated from the default DualMem test gate.

## Non-Goals

- Enabling or replacing Codex's separate built-in local-memory feature.
- Parsing arbitrary shell syntax to guess every file read by Codex.
- Replacing DualMem's storage, ranking, or context-assembly architecture in Phase 1.
- Automatically migrating existing namespaces to a new vendor-neutral scheme before aliasing and rollback behavior exist.
- Storing API keys in the repository or generating credential-bearing scripts.
- Deleting the deprecated root package in this PR; removal requires a separate versioned compatibility decision.

## Considered Approaches

### 1. Adapter-first staged parity — selected

Each client receives native hook configuration and a thin adapter. Adapters normalize client payloads and call stable DualMem commands. Phase 1 covers shared context, warnings, saves, checkpoints, and secure installation. Transcript parity follows separately.

This approach preserves working Claude behavior, avoids brittle shell parsing, and creates testable boundaries for additional clients.

### 2. Patch the imported Codex files only

Changing `Codex:*` back to `claude:*` and adjusting a few matchers would repair the current machine quickly, but the repository installer would remain unsafe and future imports or reinstalls could recreate the defects. This is acceptable only as a temporary migration action generated by the durable installer.

### 3. Replace hooks with a long-running daemon or plugin

A daemon could own credentials, event normalization, and background work, but it would add lifecycle, packaging, and failure-state complexity before basic parity is established. The adapter interface keeps that option open without requiring it now.

## Architecture

```text
Claude Code lifecycle JSON ──> Claude adapter ─┐
                                               ├─> normalized event ─> DualMem CLI ─> shared SQLite DB
Codex lifecycle JSON ────────> Codex adapter ──┘
```

The normalized event boundary distinguishes memory semantics from harness details:

- `session_start`: load generic project context and `claude:infra`.
- `prompt`: request task-aware context using the substantive user prompt.
- `file_read`: retrieve file-scoped warnings and observations when an exact file path is available.
- `file_edit`: retrieve edit-time warnings and record touched paths.
- `file_touch`: append normalized activity for later distillation.
- `session_end`: make transcript location and client metadata available to a supported distiller.

Adapters must never evaluate shell text. Codex `Bash` commands may be recorded as opaque activity metadata, but Phase 1 will not claim exact file coverage from arbitrary command parsing. Codex `apply_patch` paths are reliable and can be extracted from the patch envelope.

## Namespace and Project Identity

Phase 1 retains `claude:<git-root-basename>` as the canonical namespace because it contains existing data and is what the current CLI auto-detects. Both Claude Code and Codex documentation will state that the prefix is a legacy storage identity, not client ownership. Cross-cutting tooling remains in `claude:infra`.

Phase 3 introduces a vendor-neutral project identity, preferably configured in `.dualmem.yaml` and otherwise derived from the normalized Git remote. The store will support aliases from legacy names such as `claude:geoffreyengram` to the stable identity. Worktrees resolve through the common Git directory or remote, not the worktree folder basename. Namespace migration will be additive and reversible until aliases have been verified.

Projectless sessions do not silently borrow a guessed repository namespace. They receive infrastructure context only unless the client supplies a project root or the user explicitly selects a namespace.

## Retrieval Lifecycle

### Session start

Both clients load a bounded generic project context and `claude:infra`. A context failure is non-blocking: the hook emits a concise diagnostic and allows the session to continue.

### Prompt-aware retrieval

For substantive prompts, an adapter requests a smaller task-aware context block using the actual prompt. Short acknowledgements and repeated prompt hashes are skipped. Results are cached per session and namespace so repeated turns do not incur unnecessary provider calls or duplicate context.

### File-scoped retrieval

Claude Code continues to use its structured `Read` payload. Codex performs reliable pre-edit recall for `apply_patch`. Instructions tell Codex to call `file-context` explicitly before reading a known high-risk file when no structured file-read hook exists. Arbitrary Bash parsing is excluded because false file paths and shell quoting edge cases would make the memory gate unreliable.

## Capture and Distillation

Phase 1 normalizes structured file events and preserves explicit agent-authored memories, checkpoints, decisions, warnings, requirements, investigations, and test strategies. File-touch telemetry remains separate from durable semantic memory.

Phase 2 adds a `CodexTranscriptReader` alongside the existing Claude transcript reader. It reads Codex rollout JSONL, maps user/assistant turns and supported tool calls into the existing distillation model, and associates transcripts with the namespace resolved from session `cwd` and Git metadata. Transcript formats are treated as versioned adapters; malformed or unknown events are skipped with diagnostics rather than aborting the full session.

Codex Bash commands remain opaque transcript evidence unless a future structured filesystem event exists. The distiller may use model-visible command text as supporting evidence, but it must not execute or reinterpret it.

## Installation and Configuration

The repository will provide one cross-agent bootstrap entry point with explicit `--client claude`, `--client codex`, and `--client all` modes. It will:

1. Detect installed clients and existing configuration.
2. Back up files it will modify.
3. Merge only DualMem-owned hook entries, preserving unrelated settings.
4. Install versioned client adapters that source credentials at runtime.
5. Install or update client-specific instruction sections without rewriting unrelated user guidance.
6. Set executable scripts to mode `0700` and credential files to mode `0600`.
7. Refuse to place literal credential values in hook scripts or configuration output.
8. Print the exact hook-review or restart step required by each client.
9. Support `--dry-run` and idempotent repeated execution.

The canonical credential source will be a permission-restricted file under `~/.config/dualmem/`, or an already-exported process environment. Migration may read the existing credential-bearing hook once to preserve values locally, but it must not print them. After successful verification, the old credential-bearing script is replaced and the affected provider keys are rotated.

## Diagnostics

A `dualmem doctor` integration section will report, without exposing secrets:

- resolved database path and namespace;
- whether the current directory is a Git project, worktree, or projectless folder;
- installed and enabled Claude/Codex hooks;
- unsupported or mismatched tool matchers;
- credential-variable presence and file permission safety;
- split legacy namespaces containing divergent data;
- transcript-reader availability;
- embedding and synthesis provider reachability;
- stale code index and checkpoint state.

Diagnostics distinguish required failures from optional provider/network warnings. A lexical fallback test verifies that useful memory retrieval remains available when remote embeddings cannot be reached.

## Security and Privacy

- No credential value is accepted as a command-line argument because process listings and shell history can expose arguments.
- Hook output is treated as model-visible and must never include environment dumps or credential-bearing command text.
- Installed secrets use `0600`; hook scripts use `0700`.
- Bootstrap logs redact variable values and paths that would reveal secret material.
- Transcript distillation operates locally until it deliberately invokes the configured provider, following existing DualMem provider policy.
- The current Gemini and Z.AI credentials must be rotated because their literal values were present in an imported hook and entered a diagnostic transcript.

## Legacy Package and Test Suite Policy

The root `engram` package is formally deprecated because the project now focuses on `dualmem/`. Deprecation is explicit and reversible:

- Add package documentation with Go's `Deprecated:` convention directing users to `github.com/goblincore/geoffreyengram/dualmem` and the `dualmem` CLI.
- Update the README and testing guide so supported commands target `dualmem/`, active command packages, and maintained benchmark packages.
- Keep the legacy implementation available for compatibility during the deprecation window.
- Move root-package tests behind a `legacy` build tag so ordinary verification does not exercise an inactive product surface.
- Keep a documented `go test -tags legacy .` command for maintainers who need compatibility coverage.
- Require an additional explicit `DUALMEM_LIVE_TESTS=1` opt-in for any test that contacts Gemini or another remote provider. Merely having an API key in the environment never activates network tests.
- Preserve meaningful socket-backed legacy tests under the legacy tag rather than deleting them solely because a restricted sandbox cannot bind a port.

The contextual benchmark remains maintained. Its tests will be updated to the module-aware contract introduced by commit `384bb934`: a result in a ground-truth module counts toward precision, and a sibling result covers ground-truth files in that module for recall. Function names and comments will stop calling those metrics "strict." Tests are removed only when the corresponding behavior is deprecated or duplicated by stronger coverage; failing expectations are not deleted to obtain a green build.

The default verification matrix becomes:

1. Hermetic unit and integration tests for `dualmem/...` and maintained command packages.
2. Hermetic contextual benchmark metric tests.
3. Installer and client-adapter fixture tests using temporary homes and repositories.
4. Optional legacy tests behind `-tags legacy`.
5. Optional live-provider tests behind both their package/build selection and `DUALMEM_LIVE_TESTS=1`.

## Delivery Phases

### Phase 1: Correctness, security, and durable installation

- Formally deprecate the root `engram` package and separate its tests from the default gate.
- Make all maintained default tests hermetic and update stale contextual metric expectations.
- Refresh the README so it describes Claude Code and Codex support, points to the cross-agent installer, marks the root package deprecated, and publishes default, legacy, and live-test commands.
- Add cross-agent bootstrap and configuration merge behavior.
- Add tested Claude and Codex lifecycle adapters for session, prompt, structured read, and structured edit events.
- Restore one shared `claude:*` namespace in all instructions and hooks.
- Replace credential-bearing hooks with runtime credential sourcing.
- Add integration diagnostics and dry-run output.
- Repair the current machine using the released installer and complete client hook trust/review steps.

### Phase 2: Codex transcript and passive-capture parity

- Add the Codex rollout transcript reader.
- Normalize supported tool events and session metadata.
- Distill Codex sessions into the same memory types and namespace used by Claude Code.
- Add fixture-based compatibility tests for known transcript versions.

### Phase 3: Memory usefulness and identity quality

- Introduce stable project identities with legacy namespace aliases.
- Add robust offline lexical retrieval for all search paths.
- Capture memory provenance and explicit useful/irrelevant/misleading feedback.
- Improve supersession, decay, and stale-memory reporting.
- Keep durable semantic memories separate from activity telemetry.

## Testing Strategy

Tests will use temporary home directories, temporary Git repositories, fixture hook payloads, and fixture transcripts. No test reads real credentials or modifies live client configuration.

Phase 1 acceptance checks:

- Default verification performs no network calls and requires no local listening sockets.
- The deprecated root package remains buildable, is documented with `Deprecated:`, and its tests run only with `-tags legacy`.
- A present Gemini or Z.AI key does not activate a live test without `DUALMEM_LIVE_TESTS=1`.
- Contextual precision and recall tests assert the documented module-aware contract from commit `384bb934`.
- Repeated installation is idempotent and preserves unrelated Claude and Codex settings.
- Generated files contain credential variable names but no credential values.
- Installed permission modes are correct.
- Claude and Codex resolve the same repository to `claude:geoffreyengram`.
- Session-start and prompt adapters return bounded context and degrade non-fatally without network access.
- Claude `Read` and Codex `apply_patch` fixtures produce the expected normalized file events.
- Unsupported events are ignored with deterministic diagnostics.
- `dualmem doctor` detects intentionally broken matcher, permission, namespace, and provider fixtures.

Phase 2 acceptance checks:

- Claude and Codex transcript fixtures describing the same work produce equivalent normalized session records.
- Codex transcripts resolve worktrees to the parent project identity.
- Unknown transcript events do not prevent supported memories from being distilled.
- No command text from a transcript is executed.

## Rollout and Recovery

The installer creates timestamped backups before changing live configuration and reports their paths. Dry-run output is reviewed first. Claude and Codex hooks are installed independently so either client can be rolled back without changing the database. Existing `claude:*` data is never deleted or rewritten in Phase 1. If a hook causes latency or errors, it can be disabled while instruction-driven CLI recall remains available.

The initial PR will contain this design and the Phase 1 implementation plan. Implementation changes may be split into follow-up PRs if adapter, installer, and transcript-reader review boundaries are clearer separately.
