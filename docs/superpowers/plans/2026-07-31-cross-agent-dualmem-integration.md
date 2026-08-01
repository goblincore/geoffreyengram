# Harness-Agnostic DualMem Integration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a versioned harness-neutral event runtime with first-party Claude Code, Codex, and pi integrations that share one project identity and DualMem store.

**Architecture:** Add `dualmem/harness` as the stable protocol, project-resolution, adapter, and runtime boundary. Native Claude/Codex payload decoders and pi's generated TypeScript adapter feed `DualMem Event v1`; a separate `dualmem/integrate` package owns detection, safe configuration changes, diagnostics, and targeted uninstall for each harness. Existing `claude:<project>` keys remain compatibility storage names behind the shared resolver.

**Tech Stack:** Go 1.24, Go standard library JSON/filesystem/process APIs, `gopkg.in/yaml.v3`, embedded text assets, TypeScript source emitted as a pi extension, table-driven Go tests, Markdown documentation.

## Global Constraints

- Never print, commit, or embed API-key values. Read credentials only from `~/.config/dualmem/env` with mode `0600`, or from the inherited process environment.
- `DualMem Event v1` is the memory-semantics boundary. The engine and store must not branch on harness names.
- Claude and Codex native decoders are explicitly registered Go adapters; pi translates in its generated TypeScript extension and submits normalized events.
- Do not dynamically load arbitrary adapter code in Phase 1.
- Preserve unrelated harness configuration. Back up a file before its first material rewrite and use stable managed markers for instruction text.
- Installation is idempotent. A second identical plan contains only `unchanged` entries.
- Hooks fail open with bounded, redacted diagnostics. Invalid installer arguments and unsafe configuration fail before writes.
- Never execute or reinterpret shell/patch text. Extract only structured paths or `apply_patch` envelope headers.
- Phase 1 retains `claude:<git-common-root-name>` and `claude:infra` as compatibility storage keys; adapters never concatenate these prefixes.
- Default tests are offline and hermetic. Provider-backed tests require `DUALMEM_LIVE_TESTS=1`; deprecated root-package tests require `-tags legacy`.
- Work only in the isolated feature worktree and preserve the user's unrelated dirty checkout.

---

### Task 1: Establish the active, legacy, and live test tiers

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

**Interfaces:**

- Consumes: Existing root `engram` package, maintained `dualmem` package, and contextual benchmark functions.
- Produces: Hermetic `go test ./...`, opt-in `go test -tags legacy .`, and explicit live-test gating used by the final verification task.

- [ ] **Step 1: Write the corrected contextual metric tests**

Rename the intended matcher in tests and make module/sibling semantics explicit:

```go
func TestModuleAwarePrecisionAtK(t *testing.T) {
	gt := GroundTruth{Files: []string{"auth/jwt.go"}, Modules: []string{"auth/"}}
	if !isRelevantMatch("auth/middleware.go", gt) {
		t.Fatal("file in a ground-truth module should be relevant")
	}
	if isRelevantMatch("ui/button.go", gt) {
		t.Fatal("unrelated module should not be relevant")
	}
}

func TestSiblingAwareRecallAtK(t *testing.T) {
	results := []SearchResult{{Path: "auth/jwt.go", Score: 0.9}}
	gt := GroundTruth{Files: []string{"auth/jwt.go", "auth/oauth.go"}, Modules: []string{"auth/"}}
	if got := recallAtK(results, gt, 5); got != 1.0 {
		t.Fatalf("Recall@5 = %.3f, want 1.0", got)
	}
}
```

- [ ] **Step 2: Run the focused tests and confirm RED**

Run: `go test ./benchmarks/contextual -run 'Test(ModuleAwarePrecision|SiblingAwareRecall)' -v`

Expected: FAIL to compile because `isRelevantMatch` does not exist.

- [ ] **Step 3: Align metric names and comments with behavior**

In `benchmarks/contextual/metrics.go`, rename `isStrictMatch` to `isRelevantMatch`, update `precisionAtK` to call it, and describe exact-file, parent-module, and file-in-ground-truth-module matches. Keep sibling-aware recall. Update stale `2/3` and strict-miss assertions to the intentional module-aware values.

- [ ] **Step 4: Run contextual tests and confirm GREEN**

Run: `go test ./benchmarks/contextual -v`

Expected: PASS.

- [ ] **Step 5: Mark the original package deprecated**

Create `doc.go`:

```go
// Package engram contains the deprecated character-memory engine.
//
// Deprecated: use github.com/goblincore/geoffreyengram/dualmem and the
// dualmem CLI for cross-session project memory. This package receives no new features.
package engram
```

Add `//go:build legacy` followed by a blank line before `package engram` in every listed root test.

- [ ] **Step 6: Gate every provider-backed test explicitly**

Find candidates with:

```bash
rg -n 'GEMINI_API_KEY|OPENAI_API_KEY|ANTHROPIC_API_KEY|ZAI_API_KEY' --glob '*_test.go'
```

Before provider construction, require:

```go
if os.Getenv("DUALMEM_LIVE_TESTS") != "1" {
	t.Skip("set DUALMEM_LIVE_TESTS=1 to run provider-backed tests")
}
```

The presence of a provider key alone must not activate a test.

- [ ] **Step 7: Document and verify the tiers**

Add these commands to `docs/testing.md`:

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

Expected: default tests pass without network or listening sockets. Legacy tests run only when selected; a legacy test requiring an unavailable service skips with a precise prerequisite.

- [ ] **Step 8: Commit the test contract**

```bash
git add doc.go *_test.go dualmem/bench_test.go benchmarks/contextual/metrics.go benchmarks/contextual/metrics_test.go docs/testing.md
git commit -m "test: separate active legacy and live suites"
```

---

### Task 2: Define DualMem Event v1 and shared project identity

**Files:**

- Create: `dualmem/harness/protocol.go`
- Create: `dualmem/harness/protocol_test.go`
- Create: `dualmem/harness/project.go`
- Create: `dualmem/harness/project_test.go`

**Interfaces:**

- Consumes: Standard JSON and Git common-directory behavior already used by `cmd/dualmem/main.go:resolveNamespace`.
- Produces: `harness.Event`, `harness.Response`, `harness.DecodeEvent`, `harness.EncodeResponse`, and `harness.ResolveProject` for all later tasks.

- [ ] **Step 1: Write failing protocol tests**

Test exact JSON names, size limits, event validation, unknown optional fields, unknown kinds, unsupported major versions, and metadata restrictions:

```go
func TestDecodeEventV1(t *testing.T) {
	raw := `{"schema_version":"1.0","kind":"session_start","harness":"pi","cwd":"/repo","project":{"root":"","namespace":""},"session_id":"s1","files":[],"metadata":{"model":"test"}}`
	event, err := DecodeEvent(strings.NewReader(raw), 64<<10)
	if err != nil {
		t.Fatal(err)
	}
	if event.Kind != EventSessionStart || event.Harness != "pi" || event.SessionID != "s1" {
		t.Fatalf("unexpected event: %#v", event)
	}
}

func TestDecodeEventRejectsUnsupportedMajor(t *testing.T) {
	raw := `{"schema_version":"2.0","kind":"session_start","harness":"future","cwd":"/repo"}`
	_, err := DecodeEvent(strings.NewReader(raw), 64<<10)
	if !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("error = %v, want ErrUnsupportedVersion", err)
	}
}
```

- [ ] **Step 2: Run protocol tests and confirm RED**

Run: `go test ./dualmem/harness -run 'TestDecodeEvent' -v`

Expected: FAIL because `dualmem/harness` does not exist.

- [ ] **Step 3: Implement the protocol types and codecs**

Define these exact public types in `protocol.go`:

```go
type EventKind string

const (
	EventSessionStart EventKind = "session_start"
	EventPrompt       EventKind = "prompt"
	EventFileRead     EventKind = "file_read"
	EventFileWrite    EventKind = "file_write"
	EventSessionEnd   EventKind = "session_end"
)

type ProjectRef struct {
	Root      string `json:"root,omitempty"`
	Namespace string `json:"namespace,omitempty"`
}

type ToolRef struct {
	Name  string `json:"name,omitempty"`
	Phase string `json:"phase,omitempty"`
}

type Event struct {
	SchemaVersion string            `json:"schema_version"`
	Kind          EventKind         `json:"kind"`
	Harness       string            `json:"harness"`
	CWD           string            `json:"cwd"`
	Project       ProjectRef        `json:"project,omitempty"`
	SessionID     string            `json:"session_id,omitempty"`
	Prompt        string            `json:"prompt,omitempty"`
	Files         []string          `json:"files,omitempty"`
	ArtifactRef   string            `json:"artifact_ref,omitempty"`
	Tool          ToolRef           `json:"tool,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"`
}

type Action string

const (
	ActionInjectContext Action = "inject_context"
	ActionRecorded      Action = "recorded"
	ActionNone          Action = "none"
)

type Diagnostic struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type Response struct {
	SchemaVersion string       `json:"schema_version"`
	Action        Action       `json:"action"`
	Context       string       `json:"context,omitempty"`
	Diagnostics   []Diagnostic `json:"diagnostics,omitempty"`
}

var ErrUnsupportedVersion = errors.New("unsupported event schema version")

func DecodeEvent(r io.Reader, maxBytes int64) (Event, error)
func EncodeResponse(w io.Writer, response Response) error
func NormalizePaths(cwd string, paths []string) []string
```

Accept unknown event kinds as valid no-op candidates, but reject missing `schema_version`, `harness`, or `cwd`, non-v1 major versions, oversized input, metadata values over 1024 bytes, and more than 32 metadata entries. Only `session_end` may retain `ArtifactRef`.

- [ ] **Step 4: Write project-resolution tests**

Cover explicit namespace, explicit root, main repository, linked worktree, configured fallback, and projectless rejection. Inject Git lookup rather than invoking real Git in unit tests:

```go
type ResolveOptions struct {
	LegacyPrefix           string
	ConfiguredProject      string
	AllowDirectoryFallback bool
	GitCommonDir           func(context.Context, string) (string, error)
}

func TestResolveProjectWorktreeUsesCommonRoot(t *testing.T) {
	opts := DefaultResolveOptions()
	opts.GitCommonDir = func(context.Context, string) (string, error) {
		return "/projects/geoffreyengram/.git", nil
	}
	got, err := ResolveProject(context.Background(), Event{CWD: "/tmp/feature-worktree"}, opts)
	if err != nil {
		t.Fatal(err)
	}
	if got.Namespace != "claude:geoffreyengram" {
		t.Fatalf("namespace = %q", got.Namespace)
	}
}
```

- [ ] **Step 5: Run project tests and confirm RED**

Run: `go test ./dualmem/harness -run TestResolveProject -v`

Expected: FAIL because resolver types do not exist.

- [ ] **Step 6: Implement project identity resolution**

Add:

```go
type ProjectIdentity struct {
	Root        string
	Name        string
	Namespace   string
	Projectless bool
}

func DefaultResolveOptions() ResolveOptions
func ResolveProject(ctx context.Context, event Event, opts ResolveOptions) (ProjectIdentity, error)
```

Use explicit `Project.Namespace` first. Resolve `Project.Root`, then `CWD`, through `git rev-parse --path-format=absolute --git-common-dir`. Convert a common directory ending in `.git` to its parent repository root. Apply `LegacyPrefix` only inside the resolver. Reject an unresolved project unless `AllowDirectoryFallback` is true.

- [ ] **Step 7: Run the package tests and commit**

Run: `go test ./dualmem/harness -v`

Expected: PASS.

```bash
git add dualmem/harness/protocol.go dualmem/harness/protocol_test.go dualmem/harness/project.go dualmem/harness/project_test.go
git commit -m "feat: define harness-neutral memory events"
```

---

### Task 3: Add native payload adapters and contract fixtures

**Files:**

- Create: `dualmem/harness/adapter.go`
- Create: `dualmem/harness/adapter_claude.go`
- Create: `dualmem/harness/adapter_codex.go`
- Create: `dualmem/harness/adapter_test.go`
- Create: `dualmem/harness/testdata/claude-session-start.json`
- Create: `dualmem/harness/testdata/claude-user-prompt.json`
- Create: `dualmem/harness/testdata/claude-read.json`
- Create: `dualmem/harness/testdata/codex-session-start.json`
- Create: `dualmem/harness/testdata/codex-user-prompt.json`
- Create: `dualmem/harness/testdata/codex-apply-patch.json`
- Create: `dualmem/harness/testdata/pi-session-start.json`
- Create: `dualmem/harness/testdata/pi-read.json`

**Interfaces:**

- Consumes: `harness.Event` and `harness.Response` from Task 2.
- Produces: `harness.Adapter`, `harness.Registry`, `harness.BuiltinAdapters`, and sanitized fixtures used by the runtime and installer smoke tests.

- [ ] **Step 1: Write adapter registry and fixture tests**

Define the intended interface in tests:

```go
type Adapter interface {
	Name() string
	Decode(raw []byte) ([]Event, error)
	Encode(Response) ([]byte, error)
}

func TestBuiltinAdapters(t *testing.T) {
	registry := BuiltinAdapters()
	for _, name := range []string{"claude", "codex"} {
		if _, ok := registry.Get(name); !ok {
			t.Fatalf("missing adapter %q", name)
		}
	}
}
```

Fixture tables assert semantic equivalence for session start and prompt events, structured Claude reads, Codex patch paths, unknown tools, malformed JSON, duplicate/relative paths, and extra fields. Pi fixtures are already normalized and must decode through `DecodeEvent`, proving that pi needs no Go-native decoder.

- [ ] **Step 2: Run adapter tests and confirm RED**

Run: `go test ./dualmem/harness -run 'TestBuiltinAdapters|TestAdapter' -v`

Expected: FAIL because adapter types do not exist.

- [ ] **Step 3: Implement registry and Claude decoder**

Add:

```go
type Registry struct {
	adapters map[string]Adapter
}

func NewRegistry(adapters ...Adapter) (Registry, error)
func BuiltinAdapters() Registry
func (r Registry) Get(name string) (Adapter, bool)
```

Reject duplicate or blank adapter names. Claude decoding maps `SessionStart`, `UserPromptSubmit`, `Read`, `Edit`, and `Write` to semantic events. Unknown tools return an empty slice without error. Claude output encoding uses the client-supported additional-context JSON shape.

- [ ] **Step 4: Implement Codex decoder without shell evaluation**

Add private Codex wire structs and:

```go
func ExtractPatchPaths(patch string) []string
```

Parse only `*** Add File:`, `*** Update File:`, and `*** Delete File:` lines. Treat `Bash` as opaque activity and never execute its command. A hostile fixture containing `$(command)` and backticks must remain inert and produce paths only from patch headers. Codex output encoding uses its supported additional-context shape.

- [ ] **Step 5: Run adapter and protocol tests**

Run: `go test ./dualmem/harness -v`

Expected: PASS.

- [ ] **Step 6: Commit adapters**

```bash
git add dualmem/harness/adapter.go dualmem/harness/adapter_claude.go dualmem/harness/adapter_codex.go dualmem/harness/adapter_test.go dualmem/harness/testdata
git commit -m "feat: adapt Claude and Codex lifecycle payloads"
```

---

### Task 4: Implement the shared runtime and CLI entry points

**Files:**

- Create: `dualmem/harness/runtime.go`
- Create: `dualmem/harness/runtime_test.go`
- Create: `dualmem/harness/activity.go`
- Create: `dualmem/harness/activity_test.go`
- Create: `cmd/dualmem/event.go`
- Create: `cmd/dualmem/event_test.go`
- Modify: `cmd/dualmem/main.go`

**Interfaces:**

- Consumes: Task 2 protocol/project APIs, Task 3 adapter registry, and existing `dualmem.Engine` methods.
- Produces: `harness.Runtime.Handle`, `dualmem event`, and `dualmem hook --adapter` for all harness integrations.

- [ ] **Step 1: Write runtime behavior tests with fakes**

Define these boundaries:

```go
type Memory interface {
	AssembleContext(context.Context, string, string, int) (*dualmem.ContextBlock, error)
	FileContext(context.Context, string, string, int) ([]dualmem.DetailMemory, error)
}

type Activity struct {
	Timestamp time.Time `json:"timestamp"`
	Harness   string    `json:"harness"`
	Namespace string    `json:"namespace"`
	SessionID string    `json:"session_id,omitempty"`
	Kind      EventKind `json:"kind"`
	Files     []string  `json:"files,omitempty"`
	Artifact  string    `json:"artifact,omitempty"`
}

type ActivitySink interface {
	Record(context.Context, Activity) error
}

type Runtime struct {
	Memory              Memory
	Activity            ActivitySink
	ResolveOptions      ResolveOptions
	ProjectBudget       int
	InfrastructureNS    string
	InfrastructureBudget int
	PromptBudget        int
	MaxContextBytes     int
}

func (r *Runtime) Handle(ctx context.Context, event Event) Response
```

Tests cover project and infrastructure startup context, prompt queries, trivial/duplicate prompt suppression, file-read formatting, file-write activity, session-end artifact reference, unknown-event no-op, bounded context, and memory/activity failures becoming diagnostics with `ActionNone` rather than returned errors.

- [ ] **Step 2: Run runtime tests and confirm RED**

Run: `go test ./dualmem/harness -run 'TestRuntime|TestJSONLActivity' -v`

Expected: FAIL because runtime and activity types do not exist.

- [ ] **Step 3: Implement runtime policy and JSONL activity sink**

Defaults are exact:

- Project startup budget: 3000 tokens.
- Infrastructure budget: 1500 tokens.
- Prompt budget: 1500 tokens.
- File-context limit: 5 memories per file.
- Maximum model-visible response: 24 KiB.
- Input event limit: 64 KiB.
- Activity root: `filepath.Join(os.UserCacheDir(), "dualmem", "activity")`, partitioned by a filename-safe namespace and session ID.

Implement an in-memory per-session prompt-hash cache. Add `JSONLActivitySink` using append-only JSON lines with mode `0600`; record metadata only, not prompts, patches, file contents, or command text.

- [ ] **Step 4: Write CLI tests**

Test dependency-injected runners:

```go
func runEvent(ctx context.Context, runtime *harness.Runtime, in io.Reader, out, errOut io.Writer) int
func runHook(ctx context.Context, runtime *harness.Runtime, registry harness.Registry, adapterName string, in io.Reader, out, errOut io.Writer) int
```

Assertions:

- `event` accepts normalized pi/future-harness JSON and emits neutral `Response` JSON.
- `hook --adapter claude|codex` accepts native fixtures and emits native response JSON.
- Unknown adapter is exit `2`.
- Malformed hook input and runtime failures emit redacted diagnostics but exit `0`.
- No output includes strings matching fixture secret sentinels.

- [ ] **Step 5: Run CLI tests and confirm RED**

Run: `go test ./cmd/dualmem -run 'TestRunEvent|TestRunHook' -v`

Expected: FAIL because CLI runners do not exist.

- [ ] **Step 6: Implement CLI dispatch**

Add commands and usage:

```text
dualmem event
dualmem hook --adapter claude|codex
```

Construct the engine only after arguments and input are valid. Reuse `resolveNamespace` behavior only through the new `harness.ResolveProject`; update existing CLI namespace auto-detection to delegate to the shared resolver so worktree behavior has one implementation.

- [ ] **Step 7: Run tests and commit**

Run:

```bash
go test ./dualmem/harness ./cmd/dualmem
git diff --check
```

Expected: PASS.

```bash
git add dualmem/harness/runtime.go dualmem/harness/runtime_test.go dualmem/harness/activity.go dualmem/harness/activity_test.go cmd/dualmem/event.go cmd/dualmem/event_test.go cmd/dualmem/main.go
git commit -m "feat: run harness-neutral memory events"
```

---

### Task 5: Build the safe integration planner and applier

**Files:**

- Create: `dualmem/integrate/types.go`
- Create: `dualmem/integrate/plan.go`
- Create: `dualmem/integrate/plan_test.go`
- Create: `dualmem/integrate/apply.go`
- Create: `dualmem/integrate/apply_test.go`
- Create: `dualmem/integrate/managed.go`
- Create: `dualmem/integrate/managed_test.go`

**Interfaces:**

- Consumes: No provider-backed engine; standard filesystem operations only.
- Produces: `integrate.Driver`, `integrate.CommonPlanner`, `integrate.Bundle`, `integrate.Plan`, `integrate.Apply`, managed-block helpers, backups, and uninstall-safe change actions for Task 6 drivers.

- [ ] **Step 1: Write planner and managed-block tests**

Define:

```go
type Action string

const (
	ActionCreate    Action = "create"
	ActionUpdate    Action = "update"
	ActionUnchanged Action = "unchanged"
	ActionDelete    Action = "delete"
)

type Capability string

type Detection struct {
	Harness      string
	Installed    bool
	Capabilities []Capability
}

type Change struct {
	Path       string
	Action     Action
	Mode       fs.FileMode
	Before     []byte
	After      []byte
	BackupPath string
}

type Driver interface {
	Name() string
	Detect(context.Context, string) (Detection, error)
	Plan(context.Context, DriverRequest) ([]Change, error)
}

type CommonRequest struct {
	Home               string
	Uninstall          bool
	RemainingHarnesses []string
}

type CommonPlanner interface {
	PlanCommon(context.Context, CommonRequest) ([]Change, error)
}

type Bundle struct {
	Common  CommonPlanner
	Drivers []Driver
}

type DriverRequest struct {
	Home      string
	Uninstall bool
}

type Options struct {
	Home      string
	Harnesses []string
	DryRun    bool
	Uninstall bool
}

type Result struct {
	Detections []Detection
	Changes    []Change
}

func Plan(ctx context.Context, opts Options, bundle Bundle) (Result, error)
func Apply(result Result) error
func ReplaceManagedBlock(input, begin, end, body string) (string, error)
func RemoveManagedBlock(input, begin, end string) (string, error)
```

Test duplicate driver names, unknown harnesses, `all` selecting detected drivers, explicit harness selection, malformed/overlapping markers, idempotence, and delete actions limited to wholly DualMem-owned files. The common planner runs once for any install. During targeted uninstall it keeps shared assets while any managed harness remains and deletes them only after the final managed harness is removed.

- [ ] **Step 2: Run planner tests and confirm RED**

Run: `go test ./dualmem/integrate -run 'TestPlan|TestManagedBlock' -v`

Expected: FAIL because the package does not exist.

- [ ] **Step 3: Implement pure planning and managed blocks**

Planning reads existing state but never writes. Sort drivers and changes by name/path for deterministic output. `all` selects only detected harnesses; explicit names select those drivers even when their config directory must be created. Run the common planner exactly once after harness detection and pass it the unselected/remaining managed harness names. Managed markers are exact and unique; duplicate or unmatched markers return an error before changes are produced.

- [ ] **Step 4: Write atomic apply tests**

Use temporary homes to prove:

- create/update modes are applied exactly;
- update creates a timestamped restrictive backup;
- invalid JSON discovered during planning causes zero writes;
- an injected write failure leaves the original file intact;
- a second plan after apply contains only `unchanged`;
- uninstall removes only wholly owned files or marked blocks;
- dry-run never calls `Apply` and result summaries omit `Before`/`After` bytes.

- [ ] **Step 5: Run apply tests and confirm RED**

Run: `go test ./dualmem/integrate -run TestApply -v`

Expected: FAIL because `Apply` is not implemented.

- [ ] **Step 6: Implement atomic writes and backups**

Use same-directory temporary files, restrictive creation modes, `Chmod`, `Sync`, `Close`, and `Rename`. Never follow a planned target through a symlink; return an error before writing. Refuse `ActionDelete` unless `Before` exactly matches the installed owned asset or the file becomes empty after removing a managed block.

- [ ] **Step 7: Run and commit**

Run: `go test ./dualmem/integrate -v`

Expected: PASS.

```bash
git add dualmem/integrate/types.go dualmem/integrate/plan.go dualmem/integrate/plan_test.go dualmem/integrate/apply.go dualmem/integrate/apply_test.go dualmem/integrate/managed.go dualmem/integrate/managed_test.go
git commit -m "feat: plan safe harness integrations"
```

---

### Task 6: Add Claude, Codex, and pi installation drivers

**Files:**

- Create: `dualmem/integrate/assets.go`
- Create: `dualmem/integrate/assets/launcher.sh`
- Create: `dualmem/integrate/assets/pi-extension.ts`
- Create: `dualmem/integrate/assets/instructions.md`
- Create: `dualmem/integrate/driver_claude.go`
- Create: `dualmem/integrate/driver_codex.go`
- Create: `dualmem/integrate/driver_pi.go`
- Create: `dualmem/integrate/drivers_test.go`
- Create: `dualmem/integrate/testdata/claude-settings-existing.json`
- Create: `dualmem/integrate/testdata/codex-hooks-existing.json`
- Create: `dualmem/integrate/testdata/pi-extension-existing.ts`
- Create: `cmd/dualmem/integrate.go`
- Create: `cmd/dualmem/integrate_test.go`
- Modify: `cmd/dualmem/main.go`

**Interfaces:**

- Consumes: Task 5 `Driver`/planning APIs and Task 4 `dualmem event`/`dualmem hook` commands.
- Produces: `integrate.BuiltinBundle` and `dualmem integrate --harness` for real installation and Task 7 doctor.

- [ ] **Step 1: Write driver fixture tests**

Test these exact owned targets:

- Shared: `~/.config/dualmem/env` (`0600`) and `~/.config/dualmem/bin/dualmem-run` (`0700`).
- Claude: merged `~/.claude/settings.json` and a managed block in `~/.claude/CLAUDE.md`.
- Codex: merged `~/.codex/hooks.json` and a managed block in `~/.codex/AGENTS.md`.
- Pi: owned `~/.pi/agent/extensions/dualmem.ts` and a managed block in `~/.pi/agent/AGENTS.md`.

Assertions include unrelated JSON/hooks/instructions preserved, correct native matcher names, launcher commands containing no literal key values, exact modes, detected capability sets, and second-plan idempotence.

Also cover safe migration from legacy credential-bearing hooks: recognized `export NAME=value` assignments move directly into the protected env file without appearing in result summaries; ambiguous shell syntax stops planning with zero writes. Targeted uninstall of one harness leaves the common launcher/env in place while another managed harness remains.

- [ ] **Step 2: Run driver tests and confirm RED**

Run: `go test ./dualmem/integrate -run TestBuiltinDrivers -v`

Expected: FAIL because the drivers and assets do not exist.

- [ ] **Step 3: Implement the shared launcher and embedded assets**

`launcher.sh` must be exactly equivalent to:

```sh
#!/bin/sh
set -eu
DUALMEM_ENV_FILE="${DUALMEM_ENV_FILE:-$HOME/.config/dualmem/env}"
if [ -r "$DUALMEM_ENV_FILE" ]; then
  set -a
  . "$DUALMEM_ENV_FILE"
  set +a
fi
DUALMEM_BIN="${DUALMEM_BIN:-$HOME/go/bin/dualmem}"
exec "$DUALMEM_BIN" "$@"
```

Embed assets with `//go:embed`. The installer creates an empty protected env file when absent; it never asks for or prints credential values.

Implement `BuiltinBundle() Bundle`; its common planner owns the env and launcher exactly once. Harness drivers never delete shared assets directly.

- [ ] **Step 4: Implement Claude and Codex drivers**

Use `encoding/json` structural merges. Claude hooks invoke `dualmem-run hook --adapter claude`. Codex hooks use the installed Codex schema and invoke `dualmem-run hook --adapter codex`; do not copy Claude-only matcher names. Both managed instruction blocks describe harness-neutral project memory and instruct explicit saves/checkpoints without exposing the legacy prefix as harness ownership.

- [ ] **Step 5: Implement the pi adapter extension and driver**

The generated TypeScript extension must:

- use `DUALMEM_RUN` or `$HOME/.config/dualmem/bin/dualmem-run`, never a user-specific absolute path;
- submit `session_start`, structured `file_read`, `file_write`, and `session_end` events through `dualmem event`;
- retain a native `dualmem` tool for search, add, checkpoint, cochange, unfold, context, consult, and related supported commands;
- pass command arguments as arrays through `execFile`, not shell strings;
- cap timeout and output size;
- surface redacted fail-open diagnostics;
- let the shared resolver choose namespaces;
- skip prompt integration when the installed pi API lacks a supported prompt hook and advertise that capability accurately.

The driver backs up an existing hand-written extension before installing the owned asset.

- [ ] **Step 6: Run driver tests and confirm GREEN**

Run: `go test ./dualmem/integrate -run 'TestBuiltinDrivers|TestClaudeDriver|TestCodexDriver|TestPiDriver' -v`

Expected: PASS.

- [ ] **Step 7: Add and test the integration CLI**

Support:

```text
dualmem integrate --harness claude|codex|pi|all [--home PATH] [--dry-run]
dualmem integrate --harness claude|codex|pi|all --uninstall [--home PATH] [--dry-run]
```

`--home` defaults to `os.UserHomeDir()`. Dry-run prints only harness, path, action, mode, and backup path. It never prints file bodies. `integrate` does not construct a model provider or require API keys.

Run: `go test ./cmd/dualmem -run TestIntegrate -v`

Expected: PASS.

- [ ] **Step 8: Commit drivers and CLI**

```bash
git add dualmem/integrate/assets.go dualmem/integrate/assets dualmem/integrate/driver_claude.go dualmem/integrate/driver_codex.go dualmem/integrate/driver_pi.go dualmem/integrate/drivers_test.go dualmem/integrate/testdata cmd/dualmem/integrate.go cmd/dualmem/integrate_test.go cmd/dualmem/main.go
git commit -m "feat: install DualMem across agent harnesses"
```

---

### Task 7: Add offline capability and security diagnostics

**Files:**

- Create: `dualmem/integrate/doctor.go`
- Create: `dualmem/integrate/doctor_test.go`
- Modify: `cmd/dualmem/integrate.go`
- Modify: `cmd/dualmem/integrate_test.go`

**Interfaces:**

- Consumes: Task 6 built-in drivers/detections and Task 2 project resolver.
- Produces: `integrate.Doctor` and `dualmem integrate doctor` used before and after live migration.

- [ ] **Step 1: Write diagnostics fixture tests**

Define:

```go
type Severity string

const (
	SeverityOK      Severity = "ok"
	SeverityInfo    Severity = "info"
	SeverityWarning Severity = "warning"
	SeverityError   Severity = "error"
)

type Finding struct {
	Code     string   `json:"code"`
	Severity Severity `json:"severity"`
	Harness  string   `json:"harness,omitempty"`
	Message  string   `json:"message"`
	Fix      string   `json:"fix,omitempty"`
}

type DoctorOptions struct {
	Home       string
	ProjectDir string
}

func Doctor(ctx context.Context, opts DoctorOptions, bundle Bundle) ([]Finding, error)
```

Fixtures must detect split namespace guidance, Claude matchers copied into Codex, missing hooks/extensions/managed blocks, group/world-readable env or launcher files, literal credential-shaped values, projectless CWD, worktree identity, installer drift, and missing Codex/pi transcript readers as informational warnings. A fully installed Phase 1 setup returns no error-severity findings.

- [ ] **Step 2: Run diagnostics tests and confirm RED**

Run: `go test ./dualmem/integrate -run TestDoctor -v`

Expected: FAIL because `Doctor` does not exist.

- [ ] **Step 3: Implement offline doctor**

Doctor reads local configuration, planned state, modes, and project identity only. It never constructs `dualmem.Engine` or contacts a provider. Secret detection reports file and code but never the matched value. Capability findings are compared against each driver's advertised detection.

- [ ] **Step 4: Add CLI output and exit codes**

Support:

```text
dualmem integrate doctor [--home PATH] [--project PATH] [--json]
```

Exit `0` when findings are only `ok`/`info`, `1` when warnings or errors exist, and `2` for invalid arguments or unreadable configuration. Human output groups findings by harness; JSON output is a stable array of `Finding`.

- [ ] **Step 5: Run and commit**

Run:

```bash
go test ./dualmem/integrate -run TestDoctor -v
go test ./cmd/dualmem -run TestIntegrateDoctor -v
```

Expected: PASS.

```bash
git add dualmem/integrate/doctor.go dualmem/integrate/doctor_test.go cmd/dualmem/integrate.go cmd/dualmem/integrate_test.go
git commit -m "feat: diagnose harness memory integrations"
```

---

### Task 8: Refresh harness-neutral documentation

**Files:**

- Modify: `README.md`
- Modify: `docs/testing.md`
- Modify: `docs/example-claude-md.md`
- Create: `docs/harness-integration.md`
- Create: `docs/codex-integration.md`
- Create: `docs/pi-integration.md`
- Create: `docs/migration-from-legacy-engram.md`
- Modify: `cmd/dualmem/integrate_test.go`

**Interfaces:**

- Consumes: Commands, paths, capabilities, and limitations implemented in Tasks 1–7.
- Produces: Accurate setup/reference material and parser-backed command examples.

- [ ] **Step 1: Add failing documentation-command assertions**

Add `TestDocumentedIntegrateCommands` to parse these argument lists without touching the real home:

```go
[][]string{
	{"integrate", "--harness", "all", "--dry-run", "--home", "/tmp/home"},
	{"integrate", "--harness", "pi", "--home", "/tmp/home"},
	{"integrate", "doctor", "--home", "/tmp/home", "--project", "/tmp/repo", "--json"},
	{"integrate", "--harness", "codex", "--uninstall", "--dry-run", "--home", "/tmp/home"},
}
```

Also assert the README contains “Claude Code, Codex, and pi”, `dualmem integrate --harness all`, `DualMem Event v1`, and `Deprecated:` guidance.

- [ ] **Step 2: Run documentation assertions and confirm RED**

Run: `go test ./cmd/dualmem -run TestDocumentedIntegrateCommands -v`

Expected: FAIL until docs and parser examples agree.

- [ ] **Step 3: Rewrite README and testing guidance**

The README must:

- lead with harness-neutral per-project memory;
- list Claude Code, Codex, and pi as first-party integrations;
- explain capability differences without claiming false parity;
- document `DualMem Event v1` as the future-harness boundary;
- explain the hidden legacy `claude:<project>` compatibility key and worktree resolution;
- show install, dry-run, doctor, and targeted-uninstall commands;
- document credential modes and key rotation without key values;
- mark root `engram` deprecated;
- show default, legacy, and live-provider test commands;
- state that Codex and pi transcript distillation are Phase 2.

`docs/harness-integration.md` publishes the event/response JSON schemas, size limits, version rules, fail-open behavior, and a shell-free invocation example. Harness-specific docs cover configuration, capabilities, restart/trust steps, and limitations.

- [ ] **Step 4: Run docs checks and commit**

Run:

```bash
go test ./cmd/dualmem -run TestDocumentedIntegrateCommands -v
rg -n 'Claude Code, Codex, and pi|DualMem Event v1|dualmem integrate --harness all|Deprecated:' README.md docs
git diff --check
```

Expected: PASS.

```bash
git add README.md docs cmd/dualmem/integrate_test.go
git commit -m "docs: describe harness-neutral DualMem integration"
```

---

### Task 9: Verify, migrate this machine, and prepare the draft PR

**Files:**

- Modify only through installer: `~/.config/dualmem/env`
- Modify only through installer: `~/.config/dualmem/bin/dualmem-run`
- Modify only through installer: `~/.claude/settings.json`
- Modify only through installer: `~/.claude/CLAUDE.md`
- Modify only through installer: `~/.codex/hooks.json`
- Modify only through installer: `~/.codex/AGENTS.md`
- Modify only through installer: `~/.pi/agent/extensions/dualmem.ts`
- Modify only through installer: `~/.pi/agent/AGENTS.md`
- Update outside repository by appending: `Codex Notes/Research/2026-07-31-cross-agent-dualmem-integration.md`

**Interfaces:**

- Consumes: All implementation tasks and the user's existing harness configurations.
- Produces: Fresh verification evidence, safely migrated local integrations, updated human reference note, and a draft GitHub PR.

- [ ] **Step 1: Run fresh repository verification**

Use `superpowers:verification-before-completion` and run:

```bash
gofmt -w doc.go dualmem/harness dualmem/integrate cmd/dualmem benchmarks/contextual
go test ./...
go test -tags legacy .
go vet ./...
git diff --check
git status --short
```

Expected: all active checks pass; legacy checks are explicitly selected; only intended feature-branch changes exist.

- [ ] **Step 2: Build and review a real dry run**

```bash
go build -o /tmp/dualmem-harness-neutral ./cmd/dualmem
/tmp/dualmem-harness-neutral integrate --harness all --dry-run
```

Confirm output contains only harness, path, action, mode, and backup path; no credential value or file body may appear. Obtain execution approval before modifying live home configuration if it is not already granted.

- [ ] **Step 3: Apply and diagnose all three harnesses**

```bash
/tmp/dualmem-harness-neutral integrate --harness all
/tmp/dualmem-harness-neutral integrate doctor --project /Users/donny/Projects/2026/geoffreyengram
/tmp/dualmem-harness-neutral integrate --harness all --dry-run
```

Expected: Claude, Codex, and pi are detected; all resolve Geoffrey Engram through `claude:geoffreyengram`; security-critical modes pass; the second dry run is unchanged; missing Codex/pi transcript readers are informational Phase 2 findings.

- [ ] **Step 4: Smoke-test semantic equivalence**

Feed sanitized session-start and file-event fixtures through:

```bash
/tmp/dualmem-harness-neutral hook --adapter claude
/tmp/dualmem-harness-neutral hook --adapter codex
/tmp/dualmem-harness-neutral event
```

Use fixture stdin for each command and compare normalized actions, project identity, and bounded context. Do not add synthetic durable memories. Then start one real session in each installed harness and verify doctor remains clean after their lifecycle events.

- [ ] **Step 5: Record security follow-up and update Obsidian**

Append an `## Update YYYY-MM-DD` section to the existing Obsidian note with:

- implemented command names and capability matrix;
- repository verification results;
- live migration/doctor results;
- remaining Codex/pi transcript work;
- provider key-rotation status.

The installer removes embedded credentials but cannot revoke exposed keys. Report affected providers without reproducing values and ask the user to rotate them in their provider consoles.

- [ ] **Step 6: Request review and re-verify**

Use `superpowers:requesting-code-review`, address actionable findings, then run fresh:

```bash
go test ./...
go test -tags legacy .
go vet ./...
git diff --check
git log --oneline --decorate origin/main..HEAD
```

- [ ] **Step 7: Push and open a draft PR**

Use `github:yeet` to push the feature branch and open a draft PR against `goblincore/geoffreyengram`.

The PR summary must state:

- `DualMem Event v1` and shared project identity;
- first-party Claude, Codex, and pi integration;
- secure, idempotent installer/doctor/uninstall;
- formal legacy-package deprecation and separated test tiers;
- updated harness-neutral README and setup guides;
- transcript readers and vendor-neutral namespace migration remain Phase 2/3.
