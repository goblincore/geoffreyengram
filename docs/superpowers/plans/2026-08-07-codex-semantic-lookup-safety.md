# Codex Semantic Lookup Safety Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Keep DualMem's local context paths offline inside Codex while making every Gemini request and provider failure credential-safe.

**Architecture:** Gemini callers authenticate with `x-goog-api-key` and wrap transport failures in a stable provider-unavailable error whose text never includes request details. The CLI constructs engines from composable capabilities so local operations do not require a key or initialize classifiers, while semantic, write, and generation commands opt into only the providers they use. Query-aware context may fall back to pinned local data and reports one bounded safe diagnostic.

**Tech Stack:** Go 1.22+, `net/http`, SQLite, the existing `dualmem` engine and CLI, and offline custom-`RoundTripper` tests.

## Global Constraints

- Never place Gemini credentials in a URL, error, log, diagnostic, fixture, or committed file.
- All supported Gemini REST callers authenticate with the exact header name `x-goog-api-key`.
- Provider response bodies remain bounded to 200 bytes and redact the active credential before formatting.
- `context "session context"` must not require an API key, initialize `EmbeddingClassifier`, or perform provider I/O.
- Semantic-only commands may fail without network access, but errors must contain `network access is required; retry with network permission` and no credential-bearing URL.
- Tests are offline and use sentinel credentials only; no live provider, real credential, or DNS dependency is permitted.
- Preserve existing semantic results when network access and provider credentials are available.
- Do not bypass Codex sandbox controls, add a proxy/daemon, change namespaces, or add transcript-ingestion work.

---

### Task 1: Credential-safe Gemini HTTP transport

**Files:**
- Create: `dualmem/provider_errors.go`
- Create: `dualmem/provider_errors_test.go`
- Create: `dualmem/embed_gemini_test.go`
- Create: `dualmem/summarize_gemini_test.go`
- Create: `dualmem/classify_llm_security_test.go`
- Modify: `dualmem/embed_gemini.go:50-93`
- Modify: `dualmem/summarize_gemini.go:110-143`
- Modify: `dualmem/classify_llm.go:47-91`

**Interfaces:**
- Produces: `newProviderUnavailableError(provider, operation string, cause error) error`.
- Produces: `redactCredential(text, credential string) string`.
- Produces: credential-free errors whose cause remains available through `errors.Is` / `errors.As`.

- [ ] **Step 1: Write the failing shared error tests**

Create `dualmem/provider_errors_test.go` with:

```go
package dualmem

import (
    "errors"
    "fmt"
    "net/http"
    "strings"
    "testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
    return f(req)
}

func TestProviderUnavailableErrorIsSafeAndUnwraps(t *testing.T) {
    cause := errors.New("dial tcp: lookup provider.invalid: no such host")
    err := newProviderUnavailableError("gemini", "embed", cause)
    if !errors.Is(err, cause) {
        t.Fatal("provider error must preserve its cause")
    }
    const want = "dualmem: gemini embed unavailable: network access is required; retry with network permission"
    if got := err.Error(); got != want {
        t.Fatalf("error = %q, want %q", got, want)
    }
}

func TestRedactCredential(t *testing.T) {
    const secret = "fixture-secret-never-real"
    got := redactCredential(fmt.Sprintf("provider reflected %s twice: %s", secret, secret), secret)
    if strings.Contains(got, secret) || strings.Count(got, "[REDACTED]") != 2 {
        t.Fatalf("redaction failed: %q", got)
    }
}
```

- [ ] **Step 2: Write failing authentication and leak-regression tests**

Use `roundTripFunc` in each caller test. Assert every request has an empty `key` query parameter and the sentinel in the `x-goog-api-key` header:

```go
if got := req.URL.Query().Get("key"); got != "" {
    t.Fatalf("credential appeared in URL query: %q", got)
}
if got := req.Header.Get("x-goog-api-key"); got != secret {
    t.Fatalf("x-goog-api-key = %q, want sentinel", got)
}
```

Return these exact success fixtures:

- Embedder: `{"embedding":{"values":[0.1,0.2]}}`
- Summarizer: `{"candidates":[{"content":{"parts":[{"text":"summary"}]}}]}`
- Classifier: `{"candidates":[{"content":{"parts":[{"text":"semantic"}]}}]}` with `CodingSectors()`

Add `TestGeminiEmbedderTransportErrorDoesNotExposeCredential`: make the transport return `fmt.Errorf("failed request to %s", req.URL.String())`; assert the returned error lacks the sentinel and contains the required network guidance. Add `TestGeminiEmbedderHTTPErrorRedactsReflectedCredential`: return HTTP 500 with a body containing the sentinel; assert the error contains `[REDACTED]` and not the sentinel.

- [ ] **Step 3: Run the new tests and verify RED**

Run:

```bash
go test ./dualmem -run 'TestProviderUnavailable|TestRedactCredential|TestGemini(Embedder|Summarizer|Classifier)' -count=1
```

Expected: FAIL because the helpers do not exist, URLs still contain `?key=`, and transport errors retain the sentinel.

- [ ] **Step 4: Implement safe errors and header authentication**

Implement `dualmem/provider_errors.go` exactly as:

```go
package dualmem

import (
    "fmt"
    "strings"
)

type providerUnavailableError struct {
    provider  string
    operation string
    cause     error
}

func (e *providerUnavailableError) Error() string {
    return fmt.Sprintf("dualmem: %s %s unavailable: network access is required; retry with network permission", e.provider, e.operation)
}

func (e *providerUnavailableError) Unwrap() error { return e.cause }

func newProviderUnavailableError(provider, operation string, cause error) error {
    return &providerUnavailableError{provider: provider, operation: operation, cause: cause}
}

func redactCredential(text, credential string) string {
    if credential == "" {
        return text
    }
    return strings.ReplaceAll(text, credential, "[REDACTED]")
}
```

In all three Gemini callers, remove `?key=` from the URL and set `req.Header.Set("x-goog-api-key", apiKey)`. Wrap `client.Do` failures with `newProviderUnavailableError("gemini", operation, err)`, using operations `embed`, `summarize`, and `classify`. Redact the bounded non-200 body before formatting it.

- [ ] **Step 5: Verify GREEN and commit**

Run:

```bash
go test ./dualmem -run 'TestProviderUnavailable|TestRedactCredential|TestGemini(Embedder|Summarizer|Classifier)' -count=1
go test ./dualmem -count=1
```

Expected: PASS with pristine output. Then:

```bash
git add dualmem/provider_errors.go dualmem/provider_errors_test.go dualmem/embed_gemini.go dualmem/embed_gemini_test.go dualmem/summarize_gemini.go dualmem/summarize_gemini_test.go dualmem/classify_llm.go dualmem/classify_llm_security_test.go
git commit -m "fix: keep Gemini credentials out of errors"
```

---

### Task 2: Capability-aware CLI engine construction

**Files:**
- Modify: `cmd/dualmem/main.go:204-264,428-3348`
- Modify: `cmd/dualmem/main_test.go`
- Modify: `dualmem/assemble_v2.go:180-201`
- Modify: `dualmem/assemble_v2_test.go`

**Interfaces:**
- Produces: `engineCapabilities` with fields `requireEmbedding`, `initializeClassifier`, `initializePipeline`, `loadSynthesis`, and `loadExplorer`.
- Produces: `engineLocal`, `engineSemantic`, `engineEmbedWrite`, `engineClassifiedWrite`, `engineGenerate`, `engineExplore`, and `engineAutopilot`.
- Changes: `newEngine(cfg CLIConfig)` to `newEngine(cfg CLIConfig, capabilities engineCapabilities)`.
- Produces: `dualmem.IsSessionStartQuery(query string) bool`.

- [ ] **Step 1: Write failing local-engine and query-selection tests**

Add `TestNewEngineLocalDoesNotRequireProviderKey` to `cmd/dualmem/main_test.go`. Clear `GEMINI_API_KEY` and `GOOGLE_API_KEY`, use a temporary SQLite path, call `newEngine(cfg, engineLocal)`, and require success. Add table-driven `TestContextCapabilities` covering default/session-start sentinels, a specific query, `--legacy`, and `--index`; only the sentinel cases without legacy/index select `engineLocal`.

Add `TestIsSessionStartQuery` to `dualmem/assemble_v2_test.go`. Require true for `""`, `"session"`, `"session start"`, `"session context"`, and `"context"` with case/whitespace normalization, and false for `"why does auth fail"`.

- [ ] **Step 2: Run the focused tests and verify RED**

```bash
go test ./cmd/dualmem ./dualmem -run 'TestNewEngineLocal|TestContextCapabilities|TestIsSessionStartQuery' -count=1
```

Expected: FAIL because the capability type, context selector, and exported sentinel helper do not exist.

- [ ] **Step 3: Implement the capability model**

Add:

```go
type engineCapabilities struct {
    requireEmbedding     bool
    initializeClassifier bool
    initializePipeline   bool
    loadSynthesis        bool
    loadExplorer         bool
}

var (
    engineLocal           = engineCapabilities{}
    engineSemantic        = engineCapabilities{requireEmbedding: true}
    engineEmbedWrite      = engineCapabilities{requireEmbedding: true, initializePipeline: true}
    engineClassifiedWrite = engineCapabilities{requireEmbedding: true, initializeClassifier: true, initializePipeline: true}
    engineGenerate        = engineCapabilities{requireEmbedding: true, initializePipeline: true, loadSynthesis: true}
    engineExplore         = engineCapabilities{loadExplorer: true}
    engineAutopilot       = engineCapabilities{requireEmbedding: true, initializeClassifier: true, initializePipeline: true, loadSynthesis: true, loadExplorer: true}
)
```

`newEngine` always creates a `GeminiEmbedder` as the engine's dimension/model descriptor, but rejects an empty key only when embedding, classifier, or pipeline capability requires it. Initialize `EmbeddingClassifier`, `GeminiSummarizer`, synthesis generator, and explorer generator only when their flags are set. Export the existing sentinel list through `IsSessionStartQuery` without duplicating it. Implement `contextCapabilities(query string, legacy, index bool)` so legacy, index, and specific queries return `engineSemantic`.

- [ ] **Step 4: Map every engine call site exactly**

| CLI path | Capability |
|---|---|
| `cmdAdd` | `engineClassifiedWrite` |
| `cmdSearch`, `cmdStatus`, `cmdBenchmark`, `cmdRecall`, `cmdPrecedent` | `engineSemantic` |
| `cmdContext` | `contextCapabilities(query, *legacyMode, *indexMode)` |
| `cmdPromote` | `engineClassifiedWrite` |
| `cmdCheckpoint` | `engineEmbedWrite` |
| `cmdGC` | `engineSemantic` for `--stale`, otherwise `engineLocal` |
| `cmdSeed` | `engineLocal` for `--dry-run`, otherwise `engineGenerate` |
| `cmdAutopilot` | `engineLocal` for `--dry-run` or `--stats`, otherwise `engineAutopilot` |
| `cmdMigrateV2`, `cmdDistill`, `cmdConsult`, `cmdConsultCompare` | `engineGenerate` |
| `cmdSynthesize` | `engineLocal` for `--dry-run`, otherwise `engineGenerate` |
| `cmdFacts import` | `engineEmbedWrite` for `--commit`, otherwise `engineLocal` |
| `cmdExplore` | `engineExplore` |
| All remaining `newEngine` call sites | `engineLocal` |

For each conditional path, calculate one `capabilities` value immediately before engine construction; do not duplicate engine-opening branches.

- [ ] **Step 5: Verify GREEN and commit**

```bash
go test ./cmd/dualmem ./dualmem -run 'TestNewEngineLocal|TestContextCapabilities|TestIsSessionStartQuery' -count=1
go test ./cmd/dualmem -count=1
go test ./dualmem -run 'TestPinnedBlock' -count=1
```

Expected: PASS. Then:

```bash
git add cmd/dualmem/main.go cmd/dualmem/main_test.go dualmem/assemble_v2.go dualmem/assemble_v2_test.go
git commit -m "refactor: initialize DualMem providers by capability"
```

---

### Task 3: Safe local fallback for query-aware context

**Files:**
- Modify: `dualmem/types.go:230-241`
- Modify: `dualmem/assemble_v2.go:90-120,229-290`
- Modify: `dualmem/assemble_v2_test.go`
- Modify: `cmd/dualmem/main.go:611-709`
- Modify: `cmd/dualmem/main_test.go`
- Modify: `docs/codex-integration.md`

**Interfaces:**
- Changes: `ContextBlock` gains `Diagnostics []string`.
- Changes: `queryRelevantV2(...)` returns `([]pinnedItem, error)` instead of silently discarding provider failures.
- Consumes: Task 1's credential-safe provider errors and Task 2's capability selection.

- [ ] **Step 1: Write failing fallback tests**

Add an `unavailableEmbedder` in `dualmem/assemble_v2_test.go` whose `Embed` increments a call count and returns `newProviderUnavailableError("gemini", "embed", errors.New("fixture DNS failure"))`. Seed a local checkpoint, call `Assemble` with a specific query, and require the checkpoint plus exactly one diagnostic equal to Task 1's safe message. Add a sentinel test that calls `Assemble` with `"session context"`, requires zero diagnostics, and asserts the embedder call count remains zero.

Extract `printContextDiagnostics(io.Writer, []string)` in `cmd/dualmem/main.go`. Add a test requiring one `warning: ` line per diagnostic and absence of an injected sentinel credential.

- [ ] **Step 2: Run the tests and verify RED**

```bash
go test ./dualmem ./cmd/dualmem -run 'TestPinnedBlock_(UnavailableSemanticFallback|SessionContextNoProviderCall)|TestPrintContextDiagnostics' -count=1
```

Expected: FAIL because diagnostics and the formatting helper do not exist.

- [ ] **Step 3: Preserve local output and propagate one safe diagnostic**

Add `Diagnostics []string` to `ContextBlock`. Change `queryRelevantV2` to retain the first error from `SearchFacts` or `DualSearch` while still returning successful items; do not duplicate the same provider failure when both paths fail. `assemblePinnedV2` appends `relevanceErr.Error()` once and includes diagnostics in its block. Text output writes diagnostics to stderr; JSON output includes the field and does not duplicate it on stderr.

Implement:

```go
func printContextDiagnostics(w io.Writer, diagnostics []string) {
    for _, diagnostic := range diagnostics {
        fmt.Fprintf(w, "warning: %s\n", diagnostic)
    }
}
```

- [ ] **Step 4: Document the behavior**

Append this section to `docs/codex-integration.md`:

```markdown
## Network-restricted semantic lookup

Local session context, file context, checkpoints, and code search do not require provider access. Commands that compute a new semantic query embedding (`search`, `recall`, `precedent`, and query-specific `context`) require outbound access to the configured provider. In a restricted Codex command sandbox, rerun the explicit semantic command with network permission when prompted. DualMem does not bypass sandbox policy and never places provider credentials in request URLs or diagnostics.
```

- [ ] **Step 5: Run full verification and commit**

```bash
gofmt -w dualmem/provider_errors.go dualmem/provider_errors_test.go dualmem/embed_gemini.go dualmem/embed_gemini_test.go dualmem/summarize_gemini.go dualmem/summarize_gemini_test.go dualmem/classify_llm.go dualmem/classify_llm_security_test.go dualmem/types.go dualmem/assemble_v2.go dualmem/assemble_v2_test.go cmd/dualmem/main.go cmd/dualmem/main_test.go
go test ./dualmem ./cmd/dualmem -count=1
go test ./... -count=1
go vet ./...
rg -n '\?key=|key=%s|embedContent\?key|generateContent\?key' dualmem cmd/dualmem
```

Expected: all Go tests and vet pass with pristine output. The final `rg` exits 1 with no matches. Then:

```bash
git add dualmem/types.go dualmem/assemble_v2.go dualmem/assemble_v2_test.go cmd/dualmem/main.go cmd/dualmem/main_test.go docs/codex-integration.md
git commit -m "fix: degrade query context safely without provider access"
```

---

### Task 4: Rebuild and verify the installed CLI safely

**Files:**
- Modify only if verification reveals a defect in Tasks 1-3; otherwise no source changes.
- Runtime target: `~/go/bin/dualmem`
- Runtime launcher: `~/.config/dualmem/bin/dualmem-run`

**Interfaces:**
- Consumes: all code from Tasks 1-3.
- Produces: a rebuilt CLI whose local context path is provider-free and whose explicit semantic path succeeds with approved network access.

- [ ] **Step 1: Build without installing**

```bash
go build -o /tmp/dualmem-safe-lookup ./cmd/dualmem
```

Expected: exit 0.

- [ ] **Step 2: Verify the local path with credentials absent**

Run the temporary binary with `GEMINI_API_KEY` and `GOOGLE_API_KEY` unset and an isolated temporary database. Invoke `context "session context"`; require exit 0 and absence of `EmbeddingClassifier`, `provider unavailable`, `no API key`, and `?key=` in combined output. The script prints only pass/fail assertions, never environment values.

- [ ] **Step 3: Verify safe sandbox failure**

Run an explicit semantic search through the shared launcher without network escalation. Inside the verification process, assert nonzero exit, presence of the required network guidance, and absence of `?key=` and the active credential. Never print the credential or raw failure output.

- [ ] **Step 4: Install and verify approved-network success**

Install the rebuilt CLI with the repository's normal Go installation command. Run local `context "session context"` without network permission and require no provider warning. Then run `search "semantic lookup verification" --limit 1` with explicit network permission and require success.

- [ ] **Step 5: Record the sanitized handoff**

Save a DualMem investigation/continuity memory associated with `dualmem/embed_gemini.go`, `cmd/dualmem/main.go`, and `dualmem/assemble_v2.go`. Report that the previously logged Gemini credential should now be rotated; do not rotate or modify credential files automatically.

- [ ] **Step 6: Commit only if verification required a source fix**

If no source changed, do not create an empty commit. If verification exposes a defect, add a failing test first, implement the minimal correction, rerun the covering and full test commands, and commit only that correction.
