#!/usr/bin/env python3
"""Head-to-head benchmark: dualmem vs claude-mem
Drives both systems from a shared corpus and measures performance."""

import json
import subprocess
import time
import sys
import os
import urllib.request
import urllib.parse
import urllib.error

CORPUS_PATH = os.path.join(os.path.dirname(__file__), "corpus.json")
RESULTS_DIR = os.path.join(os.path.dirname(__file__), "results")
DUALMEM = os.path.expanduser("~/go/bin/dualmem")
CM_API = "http://localhost:37777"
BENCH_NS = "bench:acme"
DM_DB = os.path.expanduser("~/.local/share/dualmem/memories.db")

os.makedirs(RESULTS_DIR, exist_ok=True)

def load_corpus():
    with open(CORPUS_PATH) as f:
        return json.load(f)

def cm_api(path, method="GET", data=None):
    """Call claude-mem API."""
    url = f"{CM_API}{path}"
    req = urllib.request.Request(url)
    req.add_header("Content-Type", "application/json")
    if data:
        req.method = "POST"
        body = json.dumps(data).encode()
    else:
        req.method = method
        body = None
    try:
        with urllib.request.urlopen(req, body, timeout=10) as resp:
            return json.loads(resp.read())
    except Exception as e:
        return {"error": str(e)}

def dm_run(args, timeout=30):
    """Run dualmem CLI and return (stdout, elapsed_ms)."""
    start = time.monotonic()
    try:
        result = subprocess.run(
            [DUALMEM] + args,
            capture_output=True, text=True, timeout=timeout
        )
        elapsed = int((time.monotonic() - start) * 1000)
        return result.stdout.strip(), elapsed
    except subprocess.TimeoutExpired:
        elapsed = int((time.monotonic() - start) * 1000)
        return "(timeout)", elapsed

def bench_write(corpus):
    """Benchmark 1: Write performance."""
    print("\n=== BENCHMARK 1: Write Performance ===")
    memories = corpus["memories"]
    n = len(memories)

    # --- dualmem writes ---
    print(f"Seeding {n} memories into dualmem...")
    dm_start = time.monotonic()
    dm_count = 0
    dm_errors = 0
    for m in memories:
        args = [
            "add",
            "--text", m["text"],
            "--type", m["type"],
            "--ns", BENCH_NS,
            "--salience", "0.7",
        ]
        if m.get("files"):
            args += ["--files", ",".join(m["files"])]
        try:
            subprocess.run([DUALMEM] + args, capture_output=True, timeout=30)
            dm_count += 1
        except Exception:
            dm_errors += 1
        if dm_count % 25 == 0:
            print(f"  dualmem: {dm_count}/{n}")
    dm_total = int((time.monotonic() - dm_start) * 1000)
    dm_per = dm_total // max(n, 1)
    print(f"  dualmem: {dm_total}ms total, {dm_per}ms/write, {dm_errors} errors")

    # --- claude-mem writes ---
    print(f"Seeding {n} memories into claude-mem...")
    cm_start = time.monotonic()
    cm_count = 0
    cm_errors = 0
    for m in memories:
        title = m["text"][:60] + ("..." if len(m["text"]) > 60 else "")
        result = cm_api("/api/memory/save", data={
            "text": m["text"],
            "title": title,
            "project": "acme-webapp"
        })
        if "error" in result:
            cm_errors += 1
        else:
            cm_count += 1
        if cm_count % 25 == 0:
            print(f"  claude-mem: {cm_count}/{n}")
    cm_total = int((time.monotonic() - cm_start) * 1000)
    cm_per = cm_total // max(n, 1)
    print(f"  claude-mem: {cm_total}ms total, {cm_per}ms/write, {cm_errors} errors")

    # DB sizes
    dm_dbsize = os.path.getsize(DM_DB) if os.path.exists(DM_DB) else 0
    cm_dbpath = os.path.expanduser("~/.claude-mem/claude-mem.db")
    cm_dbsize = os.path.getsize(cm_dbpath) if os.path.exists(cm_dbpath) else 0
    cm_stats = cm_api("/api/stats")

    # dualmem status
    dm_status, _ = dm_run(["status", "--ns", BENCH_NS])

    return {
        "dualmem": {
            "total_ms": dm_total, "per_ms": dm_per, "count": dm_count,
            "errors": dm_errors, "db_bytes": dm_dbsize,
        },
        "claude_mem": {
            "total_ms": cm_total, "per_ms": cm_per, "count": cm_count,
            "errors": cm_errors, "db_bytes": cm_dbsize,
            "observations": cm_stats.get("database", {}).get("observations", 0),
        },
        "dm_status": dm_status,
        "cm_stats": cm_stats,
    }

def bench_search(corpus):
    """Benchmark 2: Search quality."""
    print("\n=== BENCHMARK 2: Search Quality ===")
    queries = corpus["search_queries"]
    memories = {m["id"]: m for m in corpus["memories"]}

    results = []
    for q in queries:
        qid = q["id"]
        query = q["query"]
        expected_ids = q["expected_ids"]

        # --- dualmem --- (flags must come before positional args for Go flag.FlagSet)
        dm_out, dm_ms = dm_run(["search", "--limit", "10", "--json", "--ns", BENCH_NS, query])
        dm_hits = 0
        for eid in expected_ids:
            snippet = memories[eid]["text"][:30].lower()
            if snippet in dm_out.lower():
                dm_hits += 1
        dm_recall = dm_hits / max(len(expected_ids), 1)

        # --- claude-mem ---
        cm_start = time.monotonic()
        encoded = urllib.parse.quote(query)
        cm_out = cm_api(f"/api/search?q={encoded}")
        cm_ms = int((time.monotonic() - cm_start) * 1000)

        cm_hits = 0
        cm_text = json.dumps(cm_out).lower()
        for eid in expected_ids:
            snippet = memories[eid]["text"][:30].lower()
            if snippet in cm_text:
                cm_hits += 1
        cm_recall = cm_hits / max(len(expected_ids), 1)

        results.append({
            "qid": qid, "query": query,
            "dm_recall": dm_recall, "dm_ms": dm_ms,
            "cm_recall": cm_recall, "cm_ms": cm_ms,
            "expected_count": len(expected_ids),
        })
        print(f"  {qid}: dm_recall={dm_recall:.2f} ({dm_ms}ms) cm_recall={cm_recall:.2f} ({cm_ms}ms)")

    # Averages
    n = len(results)
    dm_avg_recall = sum(r["dm_recall"] for r in results) / n
    cm_avg_recall = sum(r["cm_recall"] for r in results) / n
    dm_avg_ms = sum(r["dm_ms"] for r in results) // n
    cm_avg_ms = sum(r["cm_ms"] for r in results) // n

    print(f"\n  Summary: dm_recall={dm_avg_recall:.3f} ({dm_avg_ms}ms avg) | cm_recall={cm_avg_recall:.3f} ({cm_avg_ms}ms avg)")
    return {
        "per_query": results,
        "dualmem": {"avg_recall": dm_avg_recall, "avg_ms": dm_avg_ms},
        "claude_mem": {"avg_recall": cm_avg_recall, "avg_ms": cm_avg_ms},
    }

def bench_context(corpus):
    """Benchmark 3: Context assembly."""
    print("\n=== BENCHMARK 3: Context Assembly ===")
    scenarios = [
        "Starting a new session, what should I know about this project?",
        "I need to fix a bug in the payment processing flow",
        "What authentication changes are in progress?",
        "Tell me about the deployment pipeline",
        "What performance gotchas should I watch out for?",
    ]

    results = []
    for s in scenarios:
        # dualmem (flags before positional args)
        dm_out, dm_ms = dm_run(["context", "--budget", "3000", "--ns", BENCH_NS, s])
        dm_tokens = len(dm_out) // 4

        # claude-mem (fetch observations — their context injection is a hook, not API)
        cm_start = time.monotonic()
        cm_out = cm_api("/api/observations?limit=30&project=acme-webapp")
        cm_ms = int((time.monotonic() - cm_start) * 1000)
        cm_tokens = len(json.dumps(cm_out)) // 4

        results.append({
            "scenario": s[:50], "dm_ms": dm_ms, "dm_tokens": dm_tokens,
            "cm_ms": cm_ms, "cm_tokens": cm_tokens,
        })
        print(f"  \"{s[:40]}...\" dm={dm_ms}ms/{dm_tokens}tok  cm={cm_ms}ms/{cm_tokens}tok")

    n = len(results)
    return {
        "per_scenario": results,
        "dualmem": {
            "avg_ms": sum(r["dm_ms"] for r in results) // n,
            "avg_tokens": sum(r["dm_tokens"] for r in results) // n,
        },
        "claude_mem": {
            "avg_ms": sum(r["cm_ms"] for r in results) // n,
            "avg_tokens": sum(r["cm_tokens"] for r in results) // n,
        },
    }

def bench_code():
    """Benchmark 4: Code search (dualmem exclusive)."""
    print("\n=== BENCHMARK 4: Code Search (dualmem exclusive) ===")
    queries = [
        "embedding provider gemini API key",
        "entity graph waypoints associative",
        "HDC vector encoding basis rotation",
        "importance scoring novelty threshold",
        "context assembly budget progressive",
        "sketch compression episodes arcs",
        "sqlite store migration schema",
        "MCP tool handler registration",
    ]

    results = []
    for q in queries:
        out, ms = dm_run(["search-code", q])
        top1 = out.split("\n")[0] if out else "(none)"
        results.append({"query": q, "ms": ms, "top1": top1[:80]})
        print(f"  \"{q}\" → {top1[:60]} ({ms}ms)")

    n = len(results)
    hits = sum(1 for r in results if r["top1"] and r["top1"] != "(none)" and r["top1"] != "(timeout)")
    avg_ms = sum(r["ms"] for r in results) // n

    print(f"\n  Hit rate: {hits}/{n} ({hits/n*100:.0f}%), Avg: {avg_ms}ms")
    return {"results": results, "hit_rate": hits/n, "avg_ms": avg_ms}

def bench_compress(corpus):
    """Benchmark 5: Compression efficiency."""
    print("\n=== BENCHMARK 5: Compression Efficiency ===")

    # dualmem distill
    dm_distill, dm_distill_ms = dm_run(["distill", "--ns", BENCH_NS])
    dm_status, _ = dm_run(["status", "--ns", BENCH_NS])
    dm_dbsize = os.path.getsize(DM_DB) if os.path.exists(DM_DB) else 0

    # claude-mem
    cm_stats = cm_api("/api/stats")
    cm_dbpath = os.path.expanduser("~/.claude-mem/claude-mem.db")
    cm_dbsize = os.path.getsize(cm_dbpath) if os.path.exists(cm_dbpath) else 0

    # Raw corpus text size
    raw_size = sum(len(m["text"]) for m in corpus["memories"])

    print(f"  Raw corpus: {raw_size} bytes ({raw_size/1024:.1f}KB)")
    print(f"  dualmem DB: {dm_dbsize/1024:.1f}KB")
    print(f"  claude-mem DB: {cm_dbsize/1024:.1f}KB")
    print(f"  dualmem distill: {dm_distill}")

    return {
        "raw_bytes": raw_size,
        "dualmem": {"db_bytes": dm_dbsize, "distill_output": dm_distill, "status": dm_status},
        "claude_mem": {"db_bytes": cm_dbsize, "stats": cm_stats},
    }

def write_report(write_r, search_r, context_r, code_r, compress_r):
    """Generate final markdown report."""
    report = os.path.join(RESULTS_DIR, "benchmark_report.md")

    with open(report, "w") as f:
        f.write("# dualmem vs claude-mem: Head-to-Head Benchmark Results\n\n")
        f.write(f"Generated: {time.strftime('%Y-%m-%d %H:%M:%S')}\n\n")

        # Summary table
        f.write("## Executive Summary\n\n")
        f.write("| Dimension | dualmem | claude-mem | Winner |\n")
        f.write("|-----------|---------|------------|--------|\n")

        w = write_r
        dm_wps = 100 / (w["dualmem"]["total_ms"] / 1000) if w["dualmem"]["total_ms"] > 0 else 0
        cm_wps = 100 / (w["claude_mem"]["total_ms"] / 1000) if w["claude_mem"]["total_ms"] > 0 else 0
        write_winner = "dualmem" if dm_wps > cm_wps else "claude-mem"
        f.write(f"| Write speed | {dm_wps:.1f} writes/s | {cm_wps:.1f} writes/s | {write_winner} |\n")

        s = search_r
        search_winner = "dualmem" if s["dualmem"]["avg_recall"] > s["claude_mem"]["avg_recall"] else ("claude-mem" if s["claude_mem"]["avg_recall"] > s["dualmem"]["avg_recall"] else "tie")
        f.write(f"| Search recall | {s['dualmem']['avg_recall']:.3f} | {s['claude_mem']['avg_recall']:.3f} | {search_winner} |\n")

        search_speed_winner = "dualmem" if s["dualmem"]["avg_ms"] < s["claude_mem"]["avg_ms"] else "claude-mem"
        f.write(f"| Search latency | {s['dualmem']['avg_ms']}ms | {s['claude_mem']['avg_ms']}ms | {search_speed_winner} |\n")

        c = context_r
        ctx_winner = "dualmem" if c["dualmem"]["avg_ms"] < c["claude_mem"]["avg_ms"] else "claude-mem"
        f.write(f"| Context latency | {c['dualmem']['avg_ms']}ms | {c['claude_mem']['avg_ms']}ms | {ctx_winner} |\n")

        f.write(f"| Code search | {code_r['avg_ms']}ms, {code_r['hit_rate']*100:.0f}% hits | N/A | dualmem (exclusive) |\n")

        comp = compress_r
        f.write(f"| DB size (100 mems) | {comp['dualmem']['db_bytes']/1024:.1f}KB | {comp['claude_mem']['db_bytes']/1024:.1f}KB | {'dualmem' if comp['dualmem']['db_bytes'] < comp['claude_mem']['db_bytes'] else 'claude-mem'} |\n")

        # Detailed sections
        f.write("\n---\n\n## Benchmark 1: Write Performance\n\n")
        f.write(f"| Metric | dualmem | claude-mem |\n|---|---|---|\n")
        f.write(f"| Total time (100 writes) | {w['dualmem']['total_ms']}ms | {w['claude_mem']['total_ms']}ms |\n")
        f.write(f"| Per-write latency | {w['dualmem']['per_ms']}ms | {w['claude_mem']['per_ms']}ms |\n")
        f.write(f"| Writes/sec | {dm_wps:.1f} | {cm_wps:.1f} |\n")
        f.write(f"| Memories stored | {w['dualmem']['count']} | {w['claude_mem']['observations']} |\n")
        f.write(f"| DB size | {w['dualmem']['db_bytes']/1024:.1f}KB | {w['claude_mem']['db_bytes']/1024:.1f}KB |\n")
        f.write(f"| Errors | {w['dualmem']['errors']} | {w['claude_mem']['errors']} |\n")

        f.write("\n## Benchmark 2: Search Quality\n\n")
        f.write("| QID | Query | DM Recall | DM ms | CM Recall | CM ms |\n")
        f.write("|-----|-------|-----------|-------|-----------|-------|\n")
        for r in search_r["per_query"]:
            f.write(f"| {r['qid']} | {r['query'][:40]}... | {r['dm_recall']:.2f} | {r['dm_ms']} | {r['cm_recall']:.2f} | {r['cm_ms']} |\n")
        f.write(f"\n**Averages**: dualmem recall={s['dualmem']['avg_recall']:.3f} ({s['dualmem']['avg_ms']}ms) | claude-mem recall={s['claude_mem']['avg_recall']:.3f} ({s['claude_mem']['avg_ms']}ms)\n")

        f.write("\n## Benchmark 3: Context Assembly\n\n")
        f.write("| Scenario | DM ms | DM tokens | CM ms | CM tokens |\n")
        f.write("|----------|-------|-----------|-------|----------|\n")
        for r in context_r["per_scenario"]:
            f.write(f"| {r['scenario']} | {r['dm_ms']} | {r['dm_tokens']} | {r['cm_ms']} | {r['cm_tokens']} |\n")

        f.write("\n## Benchmark 4: Code Search (dualmem exclusive)\n\n")
        f.write("| Query | Latency | Top Result |\n|-------|---------|------------|\n")
        for r in code_r["results"]:
            f.write(f"| {r['query']} | {r['ms']}ms | {r['top1']} |\n")
        f.write(f"\nHit rate: {code_r['hit_rate']*100:.0f}%, Avg latency: {code_r['avg_ms']}ms\n")

        f.write("\n## Benchmark 5: Storage Efficiency\n\n")
        f.write(f"Raw corpus: {comp['raw_bytes']} bytes ({comp['raw_bytes']/1024:.1f}KB)\n\n")
        f.write(f"| System | DB Size | Architecture |\n|---|---|---|\n")
        f.write(f"| dualmem | {comp['dualmem']['db_bytes']/1024:.1f}KB | Dual-path + 3-tier sketch compression |\n")
        f.write(f"| claude-mem | {comp['claude_mem']['db_bytes']/1024:.1f}KB | Flat observations + session summaries |\n")

        f.write("\n---\n\n## Architecture Comparison\n\n")
        f.write("| Feature | dualmem | claude-mem |\n|---|---|---|\n")
        f.write("| Language | Go (compiled binary) | TypeScript (Bun runtime) |\n")
        f.write("| Storage | SQLite (shared, WAL) | SQLite (WAL) + ChromaDB (optional) |\n")
        f.write("| Embeddings | Gemini 768d (inline) | ChromaDB managed (external) |\n")
        f.write("| Search | Hybrid HDC+BM25 (adaptive alpha) | 3-strategy orchestrator |\n")
        f.write("| Compression | 3-tier: raw->episodes->arcs->profile | Observation summaries |\n")
        f.write("| Code search | HDC+BM25 on tree-sitter AST | None |\n")
        f.write("| Knowledge synthesis | LLM clustering -> docs | Session summaries |\n")
        f.write("| Memory types | decision/warning/continuity/checkpoint/trace/seed | decision/bugfix/feature/refactor/discovery/change |\n")
        f.write("| Context assembly | 7-tier budget-aware + intent detection | Progressive disclosure (configurable count) |\n")
        f.write("| Privacy | None | <private> tag exclusion |\n")
        f.write("| Web UI | dispatch-ui (task dispatch) | localhost:37777 memory viewer |\n")
        f.write("| Token economics | None | discovery_tokens vs read_tokens tracking |\n")
        f.write("| Deduplication | Cosine novelty scoring | SHA256 content hash (30s window) |\n")

        f.write("\n## Features Worth Adopting from claude-mem\n\n")
        f.write("1. **Privacy tags** (`<private>`) - exclude sensitive content from storage\n")
        f.write("2. **Token economics tracking** - measure ROI of memory (tokens spent to create vs tokens saved on retrieval)\n")
        f.write("3. **Web memory viewer** - real-time stream of memories at localhost (we have dispatch-ui but no memory browser)\n")
        f.write("4. **Richer observation types** - bugfix/feature/refactor/discovery/change (vs our decision/warning/continuity)\n")
        f.write("5. **Content hash dedup** - simpler than cosine novelty, good for exact/near-exact duplicates\n")
        f.write("6. **Session summaries** - automatic end-of-session LLM compression (we have distill but it's manual)\n")

    print(f"\nReport written to {report}")
    return report

def main():
    bench = sys.argv[1] if len(sys.argv) > 1 else "all"
    corpus = load_corpus()

    if bench in ("write", "all"):
        write_r = bench_write(corpus)
    if bench in ("search", "all"):
        search_r = bench_search(corpus)
    if bench in ("context", "all"):
        context_r = bench_context(corpus)
    if bench in ("code", "all"):
        code_r = bench_code()
    if bench in ("compress", "all"):
        compress_r = bench_compress(corpus)

    if bench == "all":
        report = write_report(write_r, search_r, context_r, code_r, compress_r)
        print(f"\n{'='*60}")
        print(f"  ALL BENCHMARKS COMPLETE")
        print(f"  Report: {report}")
        print(f"{'='*60}")

        # Also dump raw JSON for further analysis
        raw_path = os.path.join(RESULTS_DIR, "raw_results.json")
        with open(raw_path, "w") as f:
            json.dump({
                "write": write_r,
                "search": search_r,
                "context": context_r,
                "code": code_r,
                "compress": compress_r,
            }, f, indent=2, default=str)
        print(f"  Raw data: {raw_path}")

if __name__ == "__main__":
    main()
