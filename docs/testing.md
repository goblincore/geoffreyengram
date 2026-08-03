# Testing

The default suite is offline and uses fixtures or deterministic test doubles. It must not require a provider key or modify a real home directory.

```bash
# Default repository suite
go test ./...

# Harness protocol, installer, and documented command contract
go test ./dualmem/harness ./dualmem/integrate ./cmd/dualmem
go test ./cmd/dualmem -run TestDocumentedIntegrateCommands -v

# Focused engine and benchmark-style fixture tests
go test ./dualmem -v
go test ./dualmem -run TestBench -v
go test ./dualmem -run TestSearchBenchmark -v
```

## Legacy compatibility tier

The legacy tier exercises the retained root `engram` compatibility surface. It is separate from the default DualMem suite because new work must target `dualmem`.

```bash
go test -tags legacy .
```

## Live-provider tier

Live tests are opt-in and require configured provider credentials. They may make network requests and therefore must never be part of an unreviewed default run.

```bash
DUALMEM_LIVE_TESTS=1 go test ./dualmem -run TestBenchLive -v
```

`dualmem integrate doctor` is not a live test: it reads local integration state only and does not construct a provider client or contact the network. For installer changes, use a disposable home path and verify both planning and output:

```bash
mkdir -p /tmp/dualmem-test-home
dualmem integrate --harness all --dry-run --home /tmp/dualmem-test-home
dualmem integrate doctor --home /tmp/dualmem-test-home --project "$PWD" --json
```

The second command can return exit status `1` when it correctly reports missing integration or drift; `2` indicates an argument or inspection error.

## Test categories

| Category | Primary package | Coverage |
| --- | --- | --- |
| Engine | `./dualmem` | memory storage, ranking, facts, code maps, and offline fixtures |
| Harness protocol | `./dualmem/harness` | project identity, event normalization, native adapters, runtime behavior |
| Integration safety | `./dualmem/integrate` | ownership, no-clobber writes, backups, migrations, and diagnostics |
| CLI | `./cmd/dualmem` | argument parsing, lifecycle fail-open behavior, and documented command examples |
| Legacy | root package with `legacy` tag | deprecated `engram` compatibility only |
