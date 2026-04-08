#!/usr/bin/env python3
"""LLM-as-Judge: Compare context quality between dualmem and claude-mem.

For each scenario, we:
1. Get context from dualmem (via CLI)
2. Get context from claude-mem (via API)
3. Ask an LLM judge to rate both on relevance, completeness, noise, and usefulness
4. Aggregate scores

Uses Claude CLI as judge (claude -p "..." --model claude-sonnet-4-6).
"""

import json
import subprocess
import time
import os
import urllib.request

CORPUS_PATH = os.path.join(os.path.dirname(__file__), "corpus.json")
RESULTS_DIR = os.path.join(os.path.dirname(__file__), "results")
DUALMEM = os.path.expanduser("~/go/bin/dualmem")
CM_API = "http://localhost:37777"
BENCH_NS = "bench:acme"

os.makedirs(RESULTS_DIR, exist_ok=True)

# Scenarios: each has a user task and what good context would include
SCENARIOS = [
    {
        "id": "s1",
        "task": "I need to add a new payment method (Apple Pay) to the checkout flow",
        "ideal_context": "Should surface: PostgreSQL choice for ACID/payments, Stripe integration status, circuit breaker for external APIs, checkout feature flags, CQRS order processing",
    },
    {
        "id": "s2",
        "task": "The WebSocket connections are dropping under load, I need to investigate",
        "ideal_context": "Should surface: WebSocket hub global mutex warning (>10k needs sharding), SSE goroutine warning, GraphQL subscription goroutine leak, connection pool limits",
    },
    {
        "id": "s3",
        "task": "I'm setting up the CI/CD pipeline for a new microservice",
        "ideal_context": "Should surface: GitHub Actions with ARM64 runners, Argo CD for GitOps, canary deployments via Argo Rollouts, blue-green deploy script, ko for container images, Docker Compose for local dev",
    },
    {
        "id": "s4",
        "task": "A junior developer is about to refactor the caching layer, what should they know?",
        "ideal_context": "Should surface: LRU cache warning (don't replace with groupcache), Redis rate limiting, cache invalidation via pub/sub, S3 presigned URL cache TTL, DNS resolver caching, user search reindex timing",
    },
    {
        "id": "s5",
        "task": "We're planning the Q3 roadmap, what's the current state of in-progress work?",
        "ideal_context": "Should surface: all continuity items — gRPC migration, auth refactor, K8s migration, search feature, CI pipeline, notifications, API docs, analytics pipeline, OAuth providers, mobile app, etc.",
    },
]

JUDGE_PROMPT_TEMPLATE = """You are evaluating the quality of context provided by two different AI memory systems.

A developer is about to start this task:
"{task}"

Ideally, the context would include:
{ideal_context}

## System A context:
```
{context_a}
```

## System B context:
```
{context_b}
```

Rate each system on these dimensions (1-10 scale):

1. **Relevance**: How relevant is the context to the specific task? (10 = everything is directly useful, 1 = mostly noise)
2. **Completeness**: Does it cover the key information the developer needs? (10 = covers all ideal points, 1 = missing everything)
3. **Signal-to-noise**: What fraction of the context is useful vs filler? (10 = all signal, 1 = all noise)
4. **Actionability**: Could a developer act on this context immediately? (10 = clear next steps, 1 = just raw data)

Respond ONLY with this exact JSON format, no other text:
{{
  "system_a": {{"relevance": N, "completeness": N, "signal_noise": N, "actionability": N, "notes": "brief explanation"}},
  "system_b": {{"relevance": N, "completeness": N, "signal_noise": N, "actionability": N, "notes": "brief explanation"}}
}}"""


def get_dualmem_context(task):
    """Get context from dualmem."""
    try:
        result = subprocess.run(
            [DUALMEM, "context", "--budget", "3000", "--ns", BENCH_NS, task],
            capture_output=True, text=True, timeout=30
        )
        return result.stdout.strip() or "(empty context)"
    except Exception as e:
        return f"(error: {e})"


def get_claudemem_context(task):
    """Get context from claude-mem using ChromaDB-powered semantic search."""
    try:
        # Use the search endpoint (ChromaDB-backed semantic search)
        encoded = urllib.parse.quote(task)
        url = f"{CM_API}/api/search?query={encoded}&limit=20"
        req = urllib.request.Request(url)
        with urllib.request.urlopen(req, timeout=30) as resp:
            data = json.loads(resp.read())

        # The search returns structured content
        content = data.get("content", [])
        if content:
            return content[0].get("text", "(empty search result)")

        # Fallback: try observations list
        url2 = f"{CM_API}/api/observations?limit=30&project=acme-webapp"
        req2 = urllib.request.Request(url2)
        with urllib.request.urlopen(req2, timeout=10) as resp2:
            data2 = json.loads(resp2.read())

        items = data2.get("items", [])
        if not items:
            return "(no observations)"

        lines = []
        for obs in items[:30]:
            obs_type = obs.get("type", "?")
            title = obs.get("title", "")
            narrative = obs.get("narrative", "")
            line = f"[{obs_type}] {title}"
            if narrative:
                line += f"\n  {narrative[:200]}"
            lines.append(line)

        return "\n\n".join(lines) or "(no observations)"
    except Exception as e:
        return f"(error: {e})"


def judge_contexts(task, ideal, ctx_a, ctx_b, label_a="dualmem", label_b="claude-mem"):
    """Use Claude as judge to compare context quality."""
    prompt = JUDGE_PROMPT_TEMPLATE.format(
        task=task,
        ideal_context=ideal,
        context_a=ctx_a[:4000],  # Truncate to avoid token limits
        context_b=ctx_b[:4000],
    )

    # Use GLM 5.1 via z.ai as judge
    api_key = os.environ.get("ZAI_API_KEY", "")
    if not api_key:
        return {"error": "ZAI_API_KEY not set"}

    url = "https://api.z.ai/api/anthropic/v1/messages"
    payload = {
        "model": "glm-5.1",
        "max_tokens": 2048,
        "messages": [{"role": "user", "content": prompt}],
        "temperature": 0.1,
    }

    try:
        req = urllib.request.Request(url, json.dumps(payload).encode(), {
            "Content-Type": "application/json",
            "x-api-key": api_key,
            "anthropic-version": "2023-06-01",
        })
        with urllib.request.urlopen(req, timeout=90) as resp:
            data = json.loads(resp.read())

        text = data["content"][0]["text"]
        # Strip markdown code fences if present
        text = text.replace("```json", "").replace("```", "").strip()
        start = text.find("{")
        end = text.rfind("}") + 1
        if start >= 0 and end > start:
            return json.loads(text[start:end])
        else:
            return {"error": f"No JSON in response: {text[:200]}"}
    except json.JSONDecodeError as e:
        return {"error": f"JSON parse: {e}, raw: {text[:200] if 'text' in dir() else 'no text'}"}
    except Exception as e:
        return {"error": str(e)}


def main():
    print("\n" + "=" * 60)
    print("  LLM-as-Judge: Context Quality Comparison")
    print("  dualmem vs claude-mem")
    print("=" * 60)

    results = []

    for scenario in SCENARIOS:
        sid = scenario["id"]
        task = scenario["task"]
        ideal = scenario["ideal_context"]

        print(f"\n--- {sid}: {task[:60]}... ---")

        # Get context from both systems
        print("  Getting dualmem context...")
        dm_ctx = get_dualmem_context(task)
        dm_tokens = len(dm_ctx) // 4

        print("  Getting claude-mem context...")
        cm_ctx = get_claudemem_context(task)
        cm_tokens = len(cm_ctx) // 4

        print(f"  Tokens: dm={dm_tokens}, cm={cm_tokens}")

        # Randomize which system is A/B to avoid position bias
        import random
        if random.random() < 0.5:
            a_label, b_label = "dualmem", "claude-mem"
            a_ctx, b_ctx = dm_ctx, cm_ctx
        else:
            a_label, b_label = "claude-mem", "dualmem"
            a_ctx, b_ctx = cm_ctx, dm_ctx

        print(f"  Judging (A={a_label}, B={b_label})...")
        verdict = judge_contexts(task, ideal, a_ctx, b_ctx, a_label, b_label)

        if "error" in verdict:
            print(f"  Judge error: {verdict['error']}")
            results.append({
                "id": sid, "task": task, "error": verdict["error"],
                "dm_tokens": dm_tokens, "cm_tokens": cm_tokens,
            })
            continue

        # Map A/B back to system names
        if a_label == "dualmem":
            dm_scores = verdict.get("system_a", {})
            cm_scores = verdict.get("system_b", {})
        else:
            dm_scores = verdict.get("system_b", {})
            cm_scores = verdict.get("system_a", {})

        dm_avg = sum(dm_scores.get(k, 0) for k in ["relevance", "completeness", "signal_noise", "actionability"]) / 4
        cm_avg = sum(cm_scores.get(k, 0) for k in ["relevance", "completeness", "signal_noise", "actionability"]) / 4

        print(f"  dualmem: {dm_avg:.1f}/10 | claude-mem: {cm_avg:.1f}/10")

        results.append({
            "id": sid, "task": task,
            "dm_scores": dm_scores, "cm_scores": cm_scores,
            "dm_avg": dm_avg, "cm_avg": cm_avg,
            "dm_tokens": dm_tokens, "cm_tokens": cm_tokens,
        })

    # Write report
    report_path = os.path.join(RESULTS_DIR, "judge_quality.md")
    with open(report_path, "w") as f:
        f.write("# LLM-as-Judge: Context Quality Comparison\n\n")
        f.write(f"Generated: {time.strftime('%Y-%m-%d %H:%M:%S')}\n")
        f.write("Judge: GLM 5.1 via z.ai (position-randomized A/B)\n\n")

        # Summary
        valid = [r for r in results if "dm_avg" in r]
        if valid:
            dm_mean = sum(r["dm_avg"] for r in valid) / len(valid)
            cm_mean = sum(r["cm_avg"] for r in valid) / len(valid)
            dm_tok_mean = sum(r["dm_tokens"] for r in valid) / len(valid)
            cm_tok_mean = sum(r["cm_tokens"] for r in valid) / len(valid)

            f.write("## Summary\n\n")
            f.write("| Metric | dualmem | claude-mem |\n|---|---|---|\n")
            f.write(f"| Overall quality (avg of 4 dims) | **{dm_mean:.1f}/10** | **{cm_mean:.1f}/10** |\n")
            f.write(f"| Avg tokens used | {dm_tok_mean:.0f} | {cm_tok_mean:.0f} |\n")
            f.write(f"| Quality per token | {dm_mean/max(dm_tok_mean,1)*1000:.2f}/1k tok | {cm_mean/max(cm_tok_mean,1)*1000:.2f}/1k tok |\n")
            f.write(f"| Scenarios evaluated | {len(valid)} | {len(valid)} |\n")

        # Per-scenario
        f.write("\n## Per-Scenario Results\n\n")
        for r in results:
            f.write(f"### {r['id']}: {r['task'][:60]}\n\n")
            if "error" in r:
                f.write(f"Error: {r['error']}\n\n")
                continue

            f.write(f"Tokens: dualmem={r['dm_tokens']}, claude-mem={r['cm_tokens']}\n\n")
            f.write("| Dimension | dualmem | claude-mem |\n|---|---|---|\n")
            for dim in ["relevance", "completeness", "signal_noise", "actionability"]:
                dm_v = r["dm_scores"].get(dim, "?")
                cm_v = r["cm_scores"].get(dim, "?")
                f.write(f"| {dim} | {dm_v} | {cm_v} |\n")
            f.write(f"| **Average** | **{r['dm_avg']:.1f}** | **{r['cm_avg']:.1f}** |\n\n")

            dm_notes = r["dm_scores"].get("notes", "")
            cm_notes = r["cm_scores"].get("notes", "")
            if dm_notes:
                f.write(f"dualmem notes: {dm_notes}\n\n")
            if cm_notes:
                f.write(f"claude-mem notes: {cm_notes}\n\n")

    print(f"\nReport: {report_path}")

    # Also save raw JSON
    raw_path = os.path.join(RESULTS_DIR, "judge_raw.json")
    with open(raw_path, "w") as f:
        json.dump(results, f, indent=2)
    print(f"Raw data: {raw_path}")


if __name__ == "__main__":
    main()
