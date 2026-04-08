# Test Suite

```bash
go test ./dualmem/ -v                      # all unit + integration tests
go test ./dualmem/ -run TestBench -v       # context assembly benchmarks (mock embeddings)
go test ./dualmem/ -run TestBenchLive -v   # context benchmarks with real Gemini API
go test ./dualmem/ -run TestSearchBenchmark # HDC+BM25 code search accuracy
go test ./dualmem/ -run TestSQLite -v      # SQLite persistence regression tests
go test ./cmd/dualmem/ -run TestTilde -v   # config tilde expansion tests
```

## Test categories

| Category | Files | What it covers |
|----------|-------|----------------|
| **Core engine** | `dualmem_test.go` | Add, search, context assembly, dual-path routing |
| **Benchmarks** | `bench_test.go`, `bench_scenarios.go` | Precision, recall, NDCG, token utilization across 20+ queries |
| **Code search** | `search_bench_test.go` | HDC vs Hybrid vs Grep accuracy on 8 natural language queries |
| **Codemap** | `codemap_test.go` | Tree-sitter parsing, module extraction, budget-aware rendering |
| **Co-change** | `cochange_test.go` | File relationship graph, decay, path normalization |
| **Embedding** | `classify_embedding_test.go` | Sector classification from embedding vectors |
| **Structural** | `structural_graph_test.go` | Call/import/containment edges from tree-sitter AST |
| **Clustering** | `cluster_test.go` | Import graph construction, community detection |
| **SQLite** | `store_sqlite_regression_test.go` | Persistence across close/reopen, PRAGMA handling |
| **Config** | `cmd/dualmem/config_test.go` | Tilde expansion, absolute path passthrough |
| **vs claude-mem** | `benchmarks/` | Head-to-head comparison scripts (Python) |

## vs claude-mem benchmarks

```bash
# Seed both systems with shared corpus and run all benchmarks
python3 benchmarks/seed_and_bench.py all

# LLM-as-judge quality comparison (requires API key)
python3 benchmarks/judge_quality.py --judge glm      # GLM 5.1 (ZAI_API_KEY)
python3 benchmarks/judge_quality.py --judge opus     # Claude Opus (ANTHROPIC_API_KEY)
python3 benchmarks/judge_quality.py --judge sonnet   # Claude Sonnet (ANTHROPIC_API_KEY)
python3 benchmarks/judge_quality.py --judge gemini   # Gemini Flash (GEMINI_API_KEY)
```
