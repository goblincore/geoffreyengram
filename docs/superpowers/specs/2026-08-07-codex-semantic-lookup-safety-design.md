# Codex Semantic Lookup Safety Design

**Date:** 2026-08-07
**Status:** Approved direction; pending implementation
**Scope:** Credential-safe Gemini requests and network-capability-aware DualMem CLI initialization

## Summary

DualMem semantic lookup works from a normal terminal but fails inside Codex's restricted command sandbox because the sandbox cannot resolve the Gemini API hostname without explicit network permission. The lookup failure is environmental, but two existing DualMem behaviors make it unsafe and unnecessarily noisy:

1. Gemini API keys are placed in request query strings. Go includes the full request URL in transport errors, so a DNS failure can print the credential.
2. CLI engine construction eagerly initializes `EmbeddingClassifier`, which performs provider requests even for local-only context assembly that does not require an embedding.

The fix separates engine construction from optional provider work, moves Gemini authentication into the documented request header, and guarantees that errors and diagnostics never contain credentials. Commands that inherently require semantic embeddings continue to require network access; local context paths remain useful without it.

## Evidence and History

The failure was reproduced with the same command in two execution environments:

- With network permission, semantic lookup succeeded against the configured Gemini provider.
- In the default Codex sandbox, DNS lookup of `generativelanguage.googleapis.com` failed with `no such host`.

This is not a newly invalid endpoint or an API rejection: the request does not reach Google in the failing environment.

Git history shows that URL-based API-key authentication has existed since March 2026 and eager `EmbeddingClassifier` construction since March 20, 2026. The behavior became newly visible in Codex after the August 3 cross-harness launcher began loading the shared DualMem environment consistently. Normal terminal invocations retained network access, while Codex tool execution remained restricted.

## Goals

1. Prevent Gemini credentials from appearing in URLs, transport errors, HTTP errors, logs, or diagnostics.
2. Avoid provider calls while constructing engines for operations that do not need classification or semantic retrieval.
3. Keep session-start and other local-only context paths functional without network access.
4. Return stable, actionable, credential-free errors when an explicitly semantic command cannot reach its provider.
5. Preserve existing semantic behavior when network access is available.
6. Cover every Gemini HTTP caller in the supported `dualmem/` package.

## Non-Goals

- Bypassing Codex sandbox or network-approval controls.
- Adding a daemon, proxy, or alternative remote embedding provider.
- Building lexical ranking as a replacement for semantic search.
- Changing memory namespaces, storage paths, ranking algorithms, or transcript ingestion.
- Rotating credentials automatically.

## Root Cause

### Sandbox boundary

Codex command execution is network-restricted by default. A provider-backed operation therefore cannot assume DNS or outbound HTTPS access. The shared launcher correctly loads the protected environment, but environment availability does not grant network capability.

### Credential in request URL

`GeminiEmbedder`, `GeminiSummarizer`, and `GeminiClassifier` currently authenticate with `?key=<credential>`. When `net/http` returns a transport error, its error includes the request URL. Wrapping that error with `%w` preserves the credential-bearing URL all the way to the CLI.

### Eager provider work

`newEngine` constructs `EmbeddingClassifier` for every interactive command. Classifier construction embeds sector anchors immediately. This means an otherwise local `context "session context"` invocation contacts Gemini before the v2 pinned assembly can take its documented embedding-free path.

## Chosen Design

### 1. Header-based Gemini authentication

All supported Gemini REST callers use a credential-free endpoint URL and set:

```text
x-goog-api-key: <credential>
```

The request URL contains only the scheme, host, API version, model, and method. This follows the Gemini REST API's documented authentication mechanism and prevents the standard Go transport error from carrying the key.

The change applies consistently to:

- `dualmem/embed_gemini.go`
- `dualmem/summarize_gemini.go`
- `dualmem/classify_llm.go`

HTTP response bodies remain bounded. Before surfacing a provider error, the caller also redacts the active API key if an upstream service ever reflects it in a response body.

### 2. Capability-aware engine construction

CLI engine construction accepts an operation profile instead of assuming every command needs every provider capability:

- **Local read:** SQLite, project identity, code-map metadata, checkpoints, and pinned facts. No classifier initialization and no provider request.
- **Semantic read:** an embedding provider is available for query embeddings, but no sector classifier is initialized.
- **Write/classify:** embedding provider plus classifier and summarizer are initialized as required by the write path.
- **Generate:** semantic provider plus the configured text generator for consult, synthesis, exploration, or distillation.

The profile is internal CLI policy, not a harness name. Commands declare required capabilities explicitly, keeping the design harness-neutral.

`context "session context"` uses the local-read profile. A context request with a specific semantic query uses semantic-read capability but may degrade to the existing pinned local sections if query embedding is unavailable. `search`, `recall`, and other operations whose contract is semantic return a safe error rather than pretending they completed a semantic search.

### 3. Safe provider-unavailable errors

Provider errors are classified at the CLI boundary into a small credential-free diagnostic. For a DNS or blocked-network failure, the human-facing message is equivalent to:

```text
semantic provider unavailable: network access is required; retry with network permission
```

The underlying error remains available to tests and internal classification, but user-facing formatting must not include request headers, credentials, response bodies containing the active key, or credential-bearing URLs.

This design does not request or bypass permission automatically. The calling harness decides whether to rerun an explicitly semantic command with network approval.

### 4. Defense in depth

Header authentication removes the primary leak. Redaction remains at the provider error boundary so future URL or response changes cannot reintroduce the same class of exposure. Tests use sentinel credentials and assert that neither direct errors nor wrapped CLI output contain them.

## Command Behavior

| Command/path | Network unavailable | Network available |
|---|---|---|
| `context "session context"` | Returns local pinned context without provider warning | Same local behavior; no provider request |
| `context "specific query"` | Returns safe local fallback with concise diagnostic | Adds query-relevant semantic memories |
| `search` / `recall` | Exits nonzero with safe retry guidance | Performs semantic retrieval |
| `add` / `checkpoint` | Uses only capabilities required by the operation; provider failures are safe | Existing persistence behavior is preserved |
| `consult` / `distill` / synthesis | Exits safely if required generation capability is unavailable | Existing provider-backed behavior is preserved |

Exact command-to-profile mapping will be enumerated in the implementation plan after auditing each command's actual engine dependencies.

## Testing Strategy

Tests are offline and use `httptest` servers or deterministic failing transports.

1. Embed requests contain `x-goog-api-key` and never contain a `key` query parameter.
2. Summarizer requests contain header authentication and a credential-free URL.
3. Classifier requests contain header authentication and a credential-free URL.
4. A transport error whose formatting includes the request URL does not reveal the sentinel credential.
5. A provider response body that reflects the sentinel credential is redacted.
6. Local engine construction performs zero outbound HTTP requests.
7. Session-context assembly succeeds with a transport that fails every request.
8. Semantic commands retain success behavior with their mock providers.
9. Semantic commands return stable safe diagnostics for DNS-like failures.
10. The default offline suite remains provider-free.

Tests must assert secret absence from complete error/output strings, not merely compare a preferred message.

## Credential Rotation

The current Gemini credential has appeared in a local Codex tool transcript through the existing transport-error path. There is no evidence it was committed to reachable Git history, but it should be rotated after the safe request/error handling is deployed. Rotating afterward avoids exposing the replacement key during verification of the old code path.

## Rollout

1. Add failing credential-absence tests for each Gemini caller.
2. Move authentication into headers and add response redaction.
3. Add failing tests for provider-free local engine construction.
4. Introduce capability-aware engine construction and map local context commands.
5. Add safe provider-unavailable CLI diagnostics.
6. Run focused tests, the default offline suite, static checks, and exact-secret scans.
7. Install the rebuilt binary, verify sandboxed local context and approved-network semantic lookup, then rotate the Gemini credential.

The changes should be committed independently from unrelated functionality so the security correction can be reviewed and applied on its own.
