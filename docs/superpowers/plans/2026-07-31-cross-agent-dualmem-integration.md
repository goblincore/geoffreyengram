# Cross-Agent DualMem Integration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make Claude Code and Codex use the same per-project DualMem namespace and lifecycle hooks, while formally deprecating the repository's old character-memory package and restoring a trustworthy default test suite.

**Architecture:** Add a small client-neutral hook adapter inside `dualmem/`, expose it through `dualmem hook`, and manage both clients through an idempotent `dualmem integrate` command. Keep `claude:<project>` as the Phase 1 namespace for backward compatibility, with all client-specific payload handling normalized before it reaches memory operations. Keep installation diagnostics independent of model providers so configuration problems can be detected offline.

**Tech Stack:** Go 1.24, Go standard library JSON/filesystem APIs, `gopkg.in/yaml.v3`, existing DualMem engine and CLI, table-driven Go tests, Markdown documentation.

## Global Constraints

- Never print, commit, or embed API-key values. Read credentials only from `~/.config/dualmem/env` (mode `0600`) or the process environment.
- Preserve unrelated user configuration when merging Claude or Codex hook files. Back up a file before the first material rewrite.
- The managed integration must be idempotent: a second identical run produces no filesystem changes.
- Phase 1 keeps the canonical namespace `claude:<git-root-name>` so existing memories remain visible to both clients. A vendor-neutral namespace migration is explicitly deferred.
- Hooks are advisory. Hook parse, lookup, and persistence failures must fail open and emit concise diagnostics to stderr without blocking the agent action.
- Default tests must be offline and hermetic. Network-backed tests require `DUALMEM_LIVE_TESTS=1`; deprecated character-engine tests require the `legacy` build tag.
- Use `apply_patch` for hand edits. Do not modify the user's unrelated dirty checkout; work only in the isolated feature worktree.

---

## Task 1: Make the default test contract honest

**Files:**

- Create: `doc.go`
- Modify: `classify_embedding_bench_test.go`
- Modify: `classify_embedding_test.go`
- Modify: `classify_llm_test.go`
- Modify: `classify_test.go`
- Modify: `embed_gemini2_test.go`
- Modify: `embed_ollama_test.go`
- Modify: `embed_openai_test.go`
- Modify: `multimodal_test.go`
- Modify: `reflect_test.go`
- Modify: `scoring_test.go`
- Modify: `store_test.go`
- Modify: `temporal_test.go`
- Modify: `waypoints_test.go`
- Modify: `dualmem/bench_test.go`
- Modify: `benchmarks/contextual/metrics.go`
- Modify: `benchmarks/contextual/metrics_test.go`
- Modify: `docs/testing.md`

- [ ] **Step 1: Write failing tests for the current contextual metric contract**

In `benchmarks/contextual/metrics_test.go`, update the sibling-file expectations and add a negative case proving that an unrelated module receives no credit:

```go
func TestModuleAwarePrecisionAtK(t *testing.T) {
    // auth/middleware.go is relevant because auth/ is a ground-truth module.
    // ui/button.go is unrelated and must remain a miss.
    if !isRelevantMatch("auth/middleware.go", gt) { ... }
}

func TestSiblingAwareRecallAtK(t *testing.T) {
    // One auth/ result covers all ground-truth files in auth/.
}
```

Run: `go test ./benchmarks/contextual -run 'Test(ModuleAwarePrecision|SiblingAwareRecall)' -v`

Expected: FAIL to compile because the test names the intended `isRelevantMatch` contract while the implementation still exposes `isStrictMatch`.

- [ ] **Step 2: Align names, comments, and calculations with module-aware behavior**

In `benchmarks/contextual/metrics.go`:

- Rename `isStrictMatch` to `isRelevantMatch`.
- Document that exact files, parent modules, and files within a ground-truth module are relevant.
- Change the `precisionAtK` comment from “strict precision” to “module-aware precision.”
- Retain sibling-aware recall and clarify why one result may cover several ground-truth files in the same module.

Update the old expectations in `metrics_test.go` to `Recall@5 == 1.0` and module-aware precision for the `dualmem/` case.

Run: `go test ./benchmarks/contextual -v`

Expected: PASS.

- [ ] **Step 3: Gate live and deprecated tests explicitly**

Add `//go:build legacy` as the first line of every root character-engine test listed above. Add a package deprecation notice in `doc.go`:

```go
// Package engram contains the deprecated character-memory engine.
//
// Deprecated: use github.com/goblincore/geoffreyengram/dualmem for
// cross-session project memory. This package receives no new features.
package engram
```

Change the live benchmark guard in `dualmem/bench_test.go` so presence of a provider key is insufficient:

```go
if os.Getenv("DUALMEM_LIVE_TESTS") != "1" {
    t.Skip("set DUALMEM_LIVE_TESTS=1 to run provider-backed tests")
}
```

Apply the same explicit gate to any other provider-backed test discovered by `rg -n 'GEMINI_API_KEY|OPENAI_API_KEY|ANTHROPIC_API_KEY|ZAI_API_KEY' --glob '*_test.go'`.

- [ ] **Step 4: Document and verify both test tiers**

Update `docs/testing.md` with these canonical commands:

```bash
go test ./...
go test -tags legacy .
DUALMEM_LIVE_TESTS=1 go test ./dualmem -run TestBenchLive -v
```

Run:

```bash
go test ./...
go test -tags legacy .
git diff --check
```

Expected: the default suite passes without network/socket side effects; legacy tests compile and run only when requested. If a legacy test genuinely requires a local service, make that individual test skip with a precise prerequisite rather than weakening the default suite.

- [ ] **Step 5: Commit the test contract**

```bash
git add doc.go '*_test.go' dualmem/bench_test.go benchmarks/contextual/metrics.go benchmarks/contextual/metrics_test.go docs/testing.md
git commit -m "test: separate active, legacy, and live suites"
```

---

## Task 2: Normalize Claude and Codex hook events

**Files:**

- Create: `dualmem/agenthooks/types.go`
- Create: `dualmem/agenthooks/normalize.go`
- Create: `dualmem/agenthooks/normalize_test.go`
- Create: `dualmem/agenthooks/testdata/claude-session-start.json`
- Create: `dualmem/agenthooks/testdata/claude-user-prompt.json`
- Create: `dualmem/agenthooks/testdata/claude-read.json`
- Create: `dualmem/agenthooks/testdata/codex-session-start.json`
- Create: `dualmem/agenthooks/testdata/codex-user-prompt.json`
- Create: `dualmem/agenthooks/testdata/codex-apply-patch.json`

- [ ] **Step 1: Define the normalized contract in tests**

Create table-driven tests around this public API:

```go
type Client string

const (
    ClientClaude Client = "claude"
    ClientCodex  Client = "codex"
)

type EventKind string

const (
    EventSessionStart EventKind = "session_start"
    EventUserPrompt   EventKind = "user_prompt"
    EventFileRead     EventKind = "file_read"
    EventFileWrite    EventKind = "file_write"
    EventOther        EventKind = "other"
)

type Event struct {
    Client    Client
    Kind      EventKind
    CWD       string
    SessionID string
    Prompt    string
    Files     []string
    ToolName  string
}

func Normalize(client Client, raw []byte) (Event, error)
```

Fixtures must cover:

- Claude `SessionStart`, `UserPromptSubmit`, and `Read` payloads.
- Codex session/prompt payloads as emitted by the installed hook feature.
- Codex unified-exec `apply_patch` input with one and multiple file headers.
- Unknown fields and unknown tools.
- Malformed JSON, missing CWD, relative paths, and duplicate paths.
- A hostile patch string containing shell syntax, proving normalization only parses text and never executes it.

Run: `go test ./dualmem/agenthooks -v`

Expected: FAIL because the package does not exist.

- [ ] **Step 2: Implement deterministic normalization**

Implement `Normalize` with client-specific private wire structs and shared helpers:

```go
func normalizeClaude(raw []byte) (Event, error)
func normalizeCodex(raw []byte) (Event, error)
func ExtractPatchPaths(patch string) []string
func NormalizePaths(cwd string, paths []string) []string
```

Requirements:

- Accept only `ClientClaude` and `ClientCodex`; return a typed error otherwise.
- Clean and deduplicate paths while preserving first-seen order.
- Resolve relative paths against event CWD, but do not require that a file already exists.
- Parse `*** Add File`, `*** Update File`, and `*** Delete File` headers from `apply_patch` text.
- Treat unrecognized tools as `EventOther`, not an error.
- Never invoke a shell or interpolate patch content into a command.

Run: `go test ./dualmem/agenthooks -v`

Expected: PASS.

- [ ] **Step 3: Commit the adapter**

```bash
git add dualmem/agenthooks
git commit -m "feat: normalize Claude and Codex hook events"
```

---

## Task 3: Add the client-neutral hook runtime

**Files:**

- Create: `cmd/dualmem/hook.go`
- Create: `cmd/dualmem/hook_test.go`
- Modify: `cmd/dualmem/main.go`

- [ ] **Step 1: Specify hook behavior with dependency-injected tests**

Test this command boundary without constructing a network-backed engine:

```go
type hookMemory interface {
    AssembleContext(ctx context.Context, namespace, query string, tokenBudget int) (*dualmem.ContextBlock, error)
    FileContext(ctx context.Context, namespace, filename string, limit int) ([]dualmem.DetailMemory, error)
    AddWithOptions(ctx context.Context, input dualmem.MemoryInput, namespace string) error
}

type hookOptions struct {
    Client agenthooks.Client
    In     io.Reader
    Out    io.Writer
    Err    io.Writer
}

func runHook(ctx context.Context, mem hookMemory, opts hookOptions) error
```

Cover:

- Session start requests project context with a 3000-token budget.
- User prompt uses the prompt text as a task-aware context query.
- File read returns cached file context for each normalized path.
- File write records file associations without storing patch bodies or full file contents.
- Unknown events are a successful no-op.
- Memory failures produce a short stderr warning and still return success.
- Output is valid hook JSON for the selected client and contains only bounded additional context.

Run: `go test ./cmd/dualmem -run TestRunHook -v`

Expected: FAIL because `runHook` does not exist.

- [ ] **Step 2: Implement `dualmem hook`**

Add `hook` dispatch and usage text in `cmd/dualmem/main.go`:

```text
dualmem hook --client claude|codex
```

In `hook.go`:

- Read one JSON event from stdin with a size limit.
- Normalize it through `agenthooks.Normalize`.
- Derive the namespace through the existing project namespace resolver; do not prefix based on the current client.
- Use the established `claude:<git-root-name>` namespace.
- Return client-appropriate additional-context JSON.
- Bound context output and redact obvious credential-shaped values before writing stdout/stderr.
- Keep the process exit status zero for malformed/unknown hook payloads and memory-provider failures; reserve nonzero status for invalid CLI arguments.

Run:

```bash
go test ./cmd/dualmem -run 'TestRunHook|TestHookArgs' -v
go test ./dualmem/agenthooks ./cmd/dualmem
```

Expected: PASS.

- [ ] **Step 3: Commit the runtime**

```bash
git add cmd/dualmem/main.go cmd/dualmem/hook.go cmd/dualmem/hook_test.go
git commit -m "feat: add cross-client hook runtime"
```

---

## Task 4: Build a secure, idempotent integration installer

**Files:**

- Create: `dualmem/integrate/types.go`
- Create: `dualmem/integrate/plan.go`
- Create: `dualmem/integrate/plan_test.go`
- Create: `dualmem/integrate/apply.go`
- Create: `dualmem/integrate/apply_test.go`
- Create: `dualmem/integrate/testdata/claude-settings-existing.json`
- Create: `dualmem/integrate/testdata/codex-hooks-existing.json`
- Create: `cmd/dualmem/integrate.go`
- Create: `cmd/dualmem/integrate_test.go`
- Modify: `cmd/dualmem/main.go`

- [ ] **Step 1: Test planning and merge behavior before filesystem writes**

Define a pure planning API:

```go
type Client string

const (
    ClientClaude Client = "claude"
    ClientCodex  Client = "codex"
)

type Options struct {
    Home    string
    Clients []Client
    DryRun  bool
}

type Change struct {
    Path       string
    Action     string // create, update, unchanged
    Mode       fs.FileMode
    Before     []byte
    After      []byte
    BackupPath string
}

func Plan(opts Options) ([]Change, error)
func Apply(changes []Change) error
```

Tests must prove:

- `--client claude`, `--client codex`, and `--client all` select only the intended files.
- Existing unrelated JSON fields and hooks survive structural merges.
- Managed hooks call `dualmem hook --client <client>` and source the shared env without embedded secret values.
- Codex matcher names use the installed Codex schema, including unified `Bash`/`apply_patch` handling rather than imported Claude-only matcher names.
- Managed instruction text is enclosed in stable begin/end markers in `~/.claude/CLAUDE.md` and `~/.codex/AGENTS.md`.
- The env file is planned with `0600`; hook wrapper directories/files are `0700`/`0700`.
- A second `Plan` after `Apply` returns only `unchanged` changes.
- Invalid JSON stops before any file is changed.
- Existing files receive a timestamped backup on first update.
- Dry-run reports paths/actions/modes but no file contents or credential values.

Run: `go test ./dualmem/integrate -v`

Expected: FAIL because the package does not exist.

- [ ] **Step 2: Implement atomic, permission-safe application**

Implement structural JSON merge with `encoding/json`. Use same-directory temporary files, `fsync`/close, `chmod`, and rename for atomic writes. Create backups with restrictive permissions and never include credential values in status output.

The installer may migrate values from a legacy hook only by writing them directly to the protected env file. It must not print them, place them in generated hook commands, or expose them in a dry-run diff. If safe extraction is ambiguous, report a manual migration requirement and leave the original file unchanged.

Run: `go test ./dualmem/integrate -v`

Expected: PASS.

- [ ] **Step 3: Add the CLI surface**

Support:

```text
dualmem integrate --client claude|codex|all [--home PATH] [--dry-run]
```

Parse flags in `cmd/dualmem/integrate.go`, default `--home` to `os.UserHomeDir()`, print a concise change summary, and require no model-provider configuration merely to plan/install hooks.

Run:

```bash
go test ./cmd/dualmem -run 'TestIntegrate' -v
go test ./dualmem/integrate ./cmd/dualmem
```

Expected: PASS.

- [ ] **Step 4: Commit the installer**

```bash
git add dualmem/integrate cmd/dualmem/main.go cmd/dualmem/integrate.go cmd/dualmem/integrate_test.go
git commit -m "feat: install DualMem for Claude and Codex"
```

---

## Task 5: Add offline integration diagnostics

**Files:**

- Create: `dualmem/integrate/doctor.go`
- Create: `dualmem/integrate/doctor_test.go`
- Modify: `cmd/dualmem/integrate.go`
- Modify: `cmd/dualmem/integrate_test.go`

- [ ] **Step 1: Write failing diagnostics tests**

Add:

```go
type Severity string

const (
    SeverityOK      Severity = "ok"
    SeverityWarning Severity = "warning"
    SeverityError   Severity = "error"
)

type Finding struct {
    Code     string
    Severity Severity
    Message  string
    Fix      string
}

func Doctor(opts Options, projectDir string) ([]Finding, error)
```

Fixture-driven tests must detect:

- Claude and Codex configured to different namespace prefixes.
- Claude matcher names incorrectly copied into Codex hooks.
- Missing hook commands or missing managed instruction blocks.
- Env/hook files that are group- or world-readable.
- Literal credential-shaped values embedded in a hook command.
- A non-git/projectless CWD and a worktree resolving to its repository root name.
- Codex transcript ingestion reported as “not installed (Phase 2)” rather than falsely healthy.
- A fully installed shared setup returning only `ok` findings.

Run: `go test ./dualmem/integrate -run TestDoctor -v`

Expected: FAIL because `Doctor` does not exist.

- [ ] **Step 2: Implement `dualmem integrate doctor`**

Diagnostics must inspect local configuration and permissions only; they must not construct a provider-backed engine or make network calls. Add a human-readable CLI report and a machine-readable mode:

```text
dualmem integrate doctor [--home PATH] [--project PATH] [--json]
```

Exit codes:

- `0`: only `ok` findings.
- `1`: one or more warnings/errors found.
- `2`: invalid arguments or unreadable configuration.

Run:

```bash
go test ./dualmem/integrate -run TestDoctor -v
go test ./cmd/dualmem -run TestIntegrateDoctor -v
```

Expected: PASS.

- [ ] **Step 3: Commit diagnostics**

```bash
git add dualmem/integrate/doctor.go dualmem/integrate/doctor_test.go cmd/dualmem/integrate.go cmd/dualmem/integrate_test.go
git commit -m "feat: diagnose cross-client DualMem setup"
```

---

## Task 6: Refresh user-facing documentation

**Files:**

- Modify: `README.md`
- Modify: `docs/testing.md`
- Modify: `docs/example-claude-md.md`
- Create: `docs/codex-integration.md`
- Create: `docs/migration-from-legacy-engram.md`

- [ ] **Step 1: Add documentation assertions where practical**

Extend `cmd/dualmem/integrate_test.go` to assert that command usage examples shown in docs are accepted by the CLI parser. Add a repository check that the README no longer describes DualMem as Claude-only and that deprecated-package directions point to `dualmem/`.

Run: `go test ./cmd/dualmem -run TestDocumentedIntegrateCommands -v`

Expected: FAIL until the documented commands and parser agree.

- [ ] **Step 2: Rewrite the README around the current project focus**

Update `README.md` to:

- Lead with cross-session, per-project memory shared by Claude Code and Codex.
- Explain the Phase 1 `claude:<project>` compatibility namespace and worktree behavior.
- Provide `dualmem integrate --client all`, dry-run, and doctor examples.
- Describe secure credential storage and required permissions without showing real keys.
- Mark the root `engram` character engine as deprecated and link to its migration note.
- Show default, legacy, and live test commands.
- State that Codex transcript ingestion remains a Phase 2 enhancement; hooks provide Phase 1 parity.

Update `docs/example-claude-md.md` to use the generated managed block and add equivalent Codex guidance in `docs/codex-integration.md`. In `docs/migration-from-legacy-engram.md`, state support boundaries and how to run legacy tests.

- [ ] **Step 3: Verify documentation and commit**

Run:

```bash
go test ./cmd/dualmem -run TestDocumentedIntegrateCommands -v
rg -n 'Claude Code and Codex|dualmem integrate --client all|Deprecated' README.md docs
git diff --check
```

Expected: PASS, and all three themes are discoverable from the README.

```bash
git add README.md docs
git commit -m "docs: present DualMem as cross-agent project memory"
```

---

## Task 7: Verify Phase 1 end to end and migrate this machine

**Files:**

- Modify only through the installer: `~/.claude/settings.json`
- Modify only through the installer: `~/.claude/CLAUDE.md`
- Modify only through the installer: `~/.codex/hooks.json`
- Modify only through the installer: `~/.codex/AGENTS.md`
- Create only through the installer: `~/.config/dualmem/env`
- Update: `docs/superpowers/specs/2026-07-31-cross-agent-dualmem-integration-design.md` only if implementation decisions diverged

- [ ] **Step 1: Run the complete repository verification**

Use the verification-before-completion skill and run fresh:

```bash
gofmt -w doc.go dualmem/agenthooks dualmem/integrate cmd/dualmem benchmarks/contextual
go test ./...
go test -tags legacy .
go vet ./...
git diff --check
git status --short
```

Expected: all active checks pass; only intended feature-branch files are changed.

- [ ] **Step 2: Build and dry-run the real migration**

```bash
go build -o /tmp/dualmem-cross-agent ./cmd/dualmem
/tmp/dualmem-cross-agent integrate --client all --dry-run
```

Review the reported paths/actions/modes. Confirm that no literal provider key appears in output. Because this changes files outside the repository, obtain explicit execution approval if the current session does not already have it.

- [ ] **Step 3: Apply, diagnose, and prove shared namespace behavior**

```bash
/tmp/dualmem-cross-agent integrate --client all
/tmp/dualmem-cross-agent integrate doctor --project /Users/donny/Projects/2026/geoffreyengram
/tmp/dualmem-cross-agent integrate --client all --dry-run
```

Expected:

- Doctor reports both clients targeting `claude:geoffreyengram`.
- Hook/config modes are secure.
- The second dry-run reports no material changes.
- Existing unrelated Claude/Codex settings remain present.

Exercise one sanitized session-start and one file-event fixture through both client modes, then query the namespace directly to confirm that both see the same context. Do not add synthetic verification memories to the durable store unless they are clearly labeled and removed afterward.

- [ ] **Step 4: Record the security follow-up**

The installer can remove embedded credential values from hook scripts, but it cannot revoke exposed provider keys. Report the affected providers without reproducing their values and ask the user to rotate them in the provider consoles. Re-run doctor after the user updates `~/.config/dualmem/env`.

- [ ] **Step 5: Final review and PR preparation**

Use `superpowers:requesting-code-review`, address actionable findings, then re-run:

```bash
go test ./...
go test -tags legacy .
go vet ./...
git diff --check
git log --oneline --decorate origin/main..HEAD
```

Update the Obsidian investigation note with the implemented command names, verification result, remaining Phase 2 transcript work, and key-rotation status. Then use `github:yeet` to push the branch and open a draft PR against `goblincore/geoffreyengram`.

Expected PR scope:

- Shared Claude/Codex project namespace and hook lifecycle support.
- Secure, idempotent integration install and doctor commands.
- Formal root-package deprecation and separated test tiers.
- Updated README/setup/testing documentation.
- No transcript ingestion or namespace rename in Phase 1.
