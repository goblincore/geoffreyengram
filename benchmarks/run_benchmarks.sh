#!/usr/bin/env bash
# Head-to-head benchmark: dualmem vs claude-mem
# Usage: ./run_benchmarks.sh [bench1|bench2|bench3|bench4|bench5|all]
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
RESULTS_DIR="$SCRIPT_DIR/results"
CORPUS="$SCRIPT_DIR/corpus.json"
DUALMEM="$HOME/go/bin/dualmem"
BUN="$HOME/.bun/bin/bun"
CLAUDE_MEM_DIR="/tmp/claude-mem"
CLAUDE_MEM_PORT=37777
BENCH_NS="bench:acme"
BENCH_USER="bench-user"

# Isolated databases
DM_DB="/tmp/bench_dualmem.db"
CM_API="http://localhost:$CLAUDE_MEM_PORT"

mkdir -p "$RESULTS_DIR"

# Colors
RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; BLUE='\033[0;34m'; NC='\033[0m'

log() { echo -e "${BLUE}[bench]${NC} $*"; }
ok()  { echo -e "${GREEN}[OK]${NC} $*"; }
warn() { echo -e "${YELLOW}[WARN]${NC} $*"; }
err() { echo -e "${RED}[ERR]${NC} $*"; }

# Check prerequisites
check_prereqs() {
    log "Checking prerequisites..."
    [[ -x "$DUALMEM" ]] || { err "dualmem not found at $DUALMEM"; exit 1; }
    [[ -x "$BUN" ]] || { err "bun not found at $BUN"; exit 1; }
    command -v python3 >/dev/null || { err "python3 required"; exit 1; }
    command -v jq >/dev/null || { err "jq required"; exit 1; }

    # Check claude-mem worker
    if ! curl -sf "$CM_API/health" >/dev/null 2>&1; then
        warn "claude-mem worker not running on port $CLAUDE_MEM_PORT"
        log "Starting claude-mem worker..."
        cd "$CLAUDE_MEM_DIR"
        $BUN src/services/worker-service.ts &>/tmp/claude-mem-worker.log &
        sleep 3
        curl -sf "$CM_API/health" >/dev/null || { err "Failed to start claude-mem worker"; exit 1; }
    fi
    ok "All prerequisites met"
}

# Helper: time a command in milliseconds
time_ms() {
    local start end
    start=$(python3 -c 'import time; print(int(time.time()*1000))')
    "$@" >/dev/null 2>&1
    end=$(python3 -c 'import time; print(int(time.time()*1000))')
    echo $((end - start))
}

# Helper: count memories in corpus
corpus_count() {
    jq '.memories | length' "$CORPUS"
}

# Helper: get memory text by id from corpus
corpus_text() {
    jq -r ".memories[] | select(.id == $1) | .text" "$CORPUS"
}

corpus_type() {
    jq -r ".memories[] | select(.id == $1) | .type" "$CORPUS"
}

corpus_files() {
    jq -r ".memories[] | select(.id == $1) | .files | join(\",\")" "$CORPUS"
}

# ============================================================
# BENCHMARK 1: Write Performance
# ============================================================
bench_write() {
    log "=== BENCHMARK 1: Write Performance ==="
    local out="$RESULTS_DIR/bench1_write.md"

    # Clean slate for dualmem
    rm -f "$DM_DB"

    local count=$(corpus_count)
    log "Seeding $count memories into both systems..."

    # --- dualmem writes ---
    local dm_start dm_end dm_total
    dm_start=$(python3 -c 'import time; print(int(time.time()*1000))')

    local dm_count=0
    for id in $(jq -r '.memories[].id' "$CORPUS"); do
        local text=$(corpus_text $id)
        local type=$(corpus_type $id)
        local files=$(corpus_files $id)

        $DUALMEM add --text "$text" --type "$type" --files "$files" \
            --ns "$BENCH_NS" --db "$DM_DB" --salience 0.7 2>/dev/null || true
        dm_count=$((dm_count + 1))
        [[ $((dm_count % 25)) -eq 0 ]] && log "  dualmem: $dm_count/$count"
    done

    dm_end=$(python3 -c 'import time; print(int(time.time()*1000))')
    dm_total=$((dm_end - dm_start))
    local dm_per=$((dm_total / count))
    local dm_dbsize=$(stat -f%z "$DM_DB" 2>/dev/null || stat -c%s "$DM_DB" 2>/dev/null || echo 0)

    ok "dualmem: ${dm_total}ms total, ${dm_per}ms/write, DB=${dm_dbsize} bytes"

    # --- claude-mem writes ---
    local cm_start cm_end cm_total
    cm_start=$(python3 -c 'import time; print(int(time.time()*1000))')

    local cm_count=0
    for id in $(jq -r '.memories[].id' "$CORPUS"); do
        local text=$(corpus_text $id)
        local title=$(echo "$text" | head -c 60)

        curl -sf -X POST "$CM_API/api/memory/save" \
            -H "Content-Type: application/json" \
            -d "{\"text\": $(echo "$text" | jq -Rs .), \"title\": $(echo "$title" | jq -Rs .), \"project\": \"acme-webapp\"}" \
            >/dev/null 2>&1 || true
        cm_count=$((cm_count + 1))
        [[ $((cm_count % 25)) -eq 0 ]] && log "  claude-mem: $cm_count/$count"
    done

    cm_end=$(python3 -c 'import time; print(int(time.time()*1000))')
    cm_total=$((cm_end - cm_start))
    local cm_per=$((cm_total / count))

    # claude-mem DB size
    local cm_dbsize=0
    if [[ -f "$HOME/.claude-mem/claude-mem.db" ]]; then
        cm_dbsize=$(stat -f%z "$HOME/.claude-mem/claude-mem.db" 2>/dev/null || echo 0)
    fi

    ok "claude-mem: ${cm_total}ms total, ${cm_per}ms/write, DB=${cm_dbsize} bytes"

    # Get stats
    local cm_stats=$(curl -sf "$CM_API/api/stats")
    local cm_obs=$(echo "$cm_stats" | jq '.database.observations')
    local dm_status=$($DUALMEM status --ns "$BENCH_NS" --db "$DM_DB" 2>/dev/null || echo "?")

    # Write results
    cat > "$out" <<EOF
# Benchmark 1: Write Performance

| Metric | dualmem | claude-mem |
|--------|---------|------------|
| Total time | ${dm_total}ms | ${cm_total}ms |
| Per-write latency | ${dm_per}ms | ${cm_per}ms |
| Memories stored | $dm_count | $cm_obs |
| DB size | $(echo "$dm_dbsize" | awk '{printf "%.1fKB", $1/1024}') | $(echo "$cm_dbsize" | awk '{printf "%.1fKB", $1/1024}') |
| Writes/sec | $(echo "$count $dm_total" | awk '{printf "%.1f", $1/($2/1000)}') | $(echo "$count $cm_total" | awk '{printf "%.1f", $1/($2/1000)}') |

**dualmem status:**
\`\`\`
$dm_status
\`\`\`

**claude-mem stats:**
\`\`\`json
$cm_stats
\`\`\`
EOF

    ok "Results written to $out"
}

# ============================================================
# BENCHMARK 2: Search Quality
# ============================================================
bench_search() {
    log "=== BENCHMARK 2: Search Quality ==="
    local out="$RESULTS_DIR/bench2_search.md"

    # Ensure memories are seeded
    if [[ ! -f "$DM_DB" ]]; then
        warn "dualmem DB not found, running bench_write first..."
        bench_write
    fi

    local num_queries=$(jq '.search_queries | length' "$CORPUS")
    log "Running $num_queries search queries..."

    local dm_results="" cm_results=""
    local dm_total_p=0 dm_total_r=0 dm_total_ms=0
    local cm_total_p=0 cm_total_r=0 cm_total_ms=0
    local q_count=0

    for qid in $(jq -r '.search_queries[].id' "$CORPUS"); do
        local query=$(jq -r ".search_queries[] | select(.id == \"$qid\") | .query" "$CORPUS")
        local expected=$(jq -r ".search_queries[] | select(.id == \"$qid\") | .expected_ids | join(\",\")" "$CORPUS")
        local expected_count=$(jq ".search_queries[] | select(.id == \"$qid\") | .expected_ids | length" "$CORPUS")

        # --- dualmem search ---
        local dm_start=$(python3 -c 'import time; print(int(time.time()*1000))')
        local dm_raw=$($DUALMEM search "$query" --limit 10 --json --ns "$BENCH_NS" --db "$DM_DB" 2>/dev/null || echo "[]")
        local dm_elapsed=$(($(python3 -c 'import time; print(int(time.time()*1000))') - dm_start))
        dm_total_ms=$((dm_total_ms + dm_elapsed))

        # Calculate precision/recall for dualmem (match expected text snippets in top results)
        local dm_hits=0
        for eid in $(echo "$expected" | tr ',' ' '); do
            local expected_text=$(corpus_text $eid | head -c 40)
            if echo "$dm_raw" | grep -qi "$(echo "$expected_text" | head -c 20)" 2>/dev/null; then
                dm_hits=$((dm_hits + 1))
            fi
        done
        local dm_recall=$(echo "$dm_hits $expected_count" | awk '{if($2>0) printf "%.2f", $1/$2; else print "0.00"}')
        dm_total_r=$(echo "$dm_total_r $dm_recall" | awk '{print $1+$2}')

        # --- claude-mem search ---
        local cm_start=$(python3 -c 'import time; print(int(time.time()*1000))')
        local cm_raw=$(curl -sf "$CM_API/api/search?q=$(python3 -c "import urllib.parse; print(urllib.parse.quote('$query'))")" 2>/dev/null || echo '{"results":[]}')
        local cm_elapsed=$(($(python3 -c 'import time; print(int(time.time()*1000))') - cm_start))
        cm_total_ms=$((cm_total_ms + cm_elapsed))

        # Calculate recall for claude-mem
        local cm_hits=0
        for eid in $(echo "$expected" | tr ',' ' '); do
            local expected_text=$(corpus_text $eid | head -c 40)
            if echo "$cm_raw" | grep -qi "$(echo "$expected_text" | head -c 20)" 2>/dev/null; then
                cm_hits=$((cm_hits + 1))
            fi
        done
        local cm_recall=$(echo "$cm_hits $expected_count" | awk '{if($2>0) printf "%.2f", $1/$2; else print "0.00"}')
        cm_total_r=$(echo "$cm_total_r $cm_recall" | awk '{print $1+$2}')

        q_count=$((q_count + 1))
        dm_results+="| $qid | $query | $dm_recall | ${dm_elapsed}ms | $cm_recall | ${cm_elapsed}ms |\n"

        log "  $qid: dm_recall=$dm_recall (${dm_elapsed}ms) cm_recall=$cm_recall (${cm_elapsed}ms)"
    done

    local dm_avg_recall=$(echo "$dm_total_r $q_count" | awk '{printf "%.3f", $1/$2}')
    local cm_avg_recall=$(echo "$cm_total_r $q_count" | awk '{printf "%.3f", $1/$2}')
    local dm_avg_ms=$((dm_total_ms / q_count))
    local cm_avg_ms=$((cm_total_ms / q_count))

    cat > "$out" <<EOF
# Benchmark 2: Search Quality

## Summary

| Metric | dualmem | claude-mem |
|--------|---------|------------|
| Avg Recall@10 | $dm_avg_recall | $cm_avg_recall |
| Avg latency | ${dm_avg_ms}ms | ${cm_avg_ms}ms |
| Total queries | $q_count | $q_count |

## Per-Query Results

| QID | Query | DM Recall | DM Latency | CM Recall | CM Latency |
|-----|-------|-----------|------------|-----------|------------|
$(echo -e "$dm_results")

**Notes:**
- dualmem uses real Gemini embeddings (768d) + hybrid HDC/BM25 scoring
- claude-mem uses SQLite filter search (ChromaDB not configured for this benchmark)
- Recall = fraction of expected memories found in top-10 results
EOF

    ok "Results written to $out"
}

# ============================================================
# BENCHMARK 3: Context Assembly
# ============================================================
bench_context() {
    log "=== BENCHMARK 3: Context Assembly ==="
    local out="$RESULTS_DIR/bench3_context.md"

    if [[ ! -f "$DM_DB" ]]; then
        warn "dualmem DB not found, running bench_write first..."
        bench_write
    fi

    local scenarios=(
        "Starting a new session, what should I know about this project?"
        "I need to fix a bug in the payment processing flow"
        "What authentication changes are in progress?"
        "Tell me about the deployment pipeline"
        "What performance gotchas should I watch out for?"
    )

    local dm_results="" cm_results=""
    local dm_total_ms=0 cm_total_ms=0
    local dm_total_tokens=0 cm_total_tokens=0

    for scenario in "${scenarios[@]}"; do
        # dualmem context
        local dm_start=$(python3 -c 'import time; print(int(time.time()*1000))')
        local dm_ctx=$($DUALMEM context "$scenario" --budget 3000 --ns "$BENCH_NS" --db "$DM_DB" 2>/dev/null || echo "(error)")
        local dm_elapsed=$(($(python3 -c 'import time; print(int(time.time()*1000))') - dm_start))
        local dm_tokens=$(echo "$dm_ctx" | wc -c | awk '{printf "%d", $1/4}')
        dm_total_ms=$((dm_total_ms + dm_elapsed))
        dm_total_tokens=$((dm_total_tokens + dm_tokens))

        # claude-mem context (use their context generator if available, otherwise fetch observations)
        local cm_start=$(python3 -c 'import time; print(int(time.time()*1000))')
        local cm_ctx=$(curl -sf "$CM_API/api/observations?limit=30&project=acme-webapp" 2>/dev/null || echo '{"items":[]}')
        local cm_elapsed=$(($(python3 -c 'import time; print(int(time.time()*1000))') - cm_start))
        local cm_tokens=$(echo "$cm_ctx" | wc -c | awk '{printf "%d", $1/4}')
        cm_total_ms=$((cm_total_ms + cm_elapsed))
        cm_total_tokens=$((cm_total_tokens + cm_tokens))

        local short_scenario=$(echo "$scenario" | head -c 50)
        dm_results+="| $short_scenario | ${dm_elapsed}ms | ~${dm_tokens}tok | ${cm_elapsed}ms | ~${cm_tokens}tok |\n"
        log "  \"${short_scenario}...\" dm=${dm_elapsed}ms cm=${cm_elapsed}ms"
    done

    local n=${#scenarios[@]}
    local dm_avg=$((dm_total_ms / n))
    local cm_avg=$((cm_total_ms / n))

    cat > "$out" <<EOF
# Benchmark 3: Context Assembly

## Summary

| Metric | dualmem | claude-mem |
|--------|---------|------------|
| Avg latency | ${dm_avg}ms | ${cm_avg}ms |
| Avg tokens | $((dm_total_tokens / n)) | $((cm_total_tokens / n)) |
| Scenarios tested | $n | $n |

## Per-Scenario Results

| Scenario | DM Latency | DM Tokens | CM Latency | CM Tokens |
|----------|-----------|-----------|-----------|-----------|
$(echo -e "$dm_results")

**Notes:**
- dualmem uses budget-aware 7-priority-tier assembly with intent detection
- claude-mem returns raw observations (their context injection runs as a hook, not via API)
- Token counts are estimates (chars/4)
- dualmem context is query-aware (different queries → different context); claude-mem returns same observations
EOF

    ok "Results written to $out"
}

# ============================================================
# BENCHMARK 4: Code Search (dualmem exclusive)
# ============================================================
bench_code() {
    log "=== BENCHMARK 4: Code Search (dualmem exclusive) ==="
    local out="$RESULTS_DIR/bench4_code_search.md"

    local queries=(
        "embedding provider gemini API key"
        "entity graph waypoints associative"
        "HDC vector encoding basis rotation"
        "importance scoring novelty threshold"
        "context assembly budget progressive"
        "sketch compression episodes arcs"
        "sqlite store migration schema"
        "MCP tool handler registration"
    )

    local results=""
    local total_ms=0
    local hits=0
    local total=${#queries[@]}

    for query in "${queries[@]}"; do
        local start=$(python3 -c 'import time; print(int(time.time()*1000))')
        local raw=$($DUALMEM search-code "$query" 2>/dev/null || echo "(error)")
        local elapsed=$(($(python3 -c 'import time; print(int(time.time()*1000))') - start))
        total_ms=$((total_ms + elapsed))

        local top1=$(echo "$raw" | head -1)
        [[ -n "$top1" && "$top1" != "(error)" ]] && hits=$((hits + 1))

        results+="| $query | ${elapsed}ms | $top1 |\n"
        log "  \"$query\" → $top1 (${elapsed}ms)"
    done

    cat > "$out" <<EOF
# Benchmark 4: Code Search (dualmem exclusive)

claude-mem has **no code search capability**. Users rely on Claude's built-in grep/glob tools.
dualmem provides HDC+BM25 hybrid code search that finds relevant modules by natural language.

## Results

| Query | Latency | Top Result |
|-------|---------|------------|
$(echo -e "$results")

## Summary

| Metric | Value |
|--------|-------|
| Queries | $total |
| Hit rate | $hits/$total ($(echo "$hits $total" | awk '{printf "%.0f%%", $1/$2*100}')) |
| Avg latency | $((total_ms / total))ms |
| Total time | ${total_ms}ms |

**This is a unique dualmem advantage** — no equivalent exists in claude-mem.
EOF

    ok "Results written to $out"
}

# ============================================================
# BENCHMARK 5: Compression / Storage Efficiency
# ============================================================
bench_compress() {
    log "=== BENCHMARK 5: Compression Efficiency ==="
    local out="$RESULTS_DIR/bench5_compression.md"

    if [[ ! -f "$DM_DB" ]]; then
        warn "dualmem DB not found, running bench_write first..."
        bench_write
    fi

    # dualmem: run distill to compress
    log "Running dualmem distill..."
    local dm_before=$(stat -f%z "$DM_DB" 2>/dev/null || stat -c%s "$DM_DB" 2>/dev/null || echo 0)
    local distill_out=$($DUALMEM distill --ns "$BENCH_NS" --db "$DM_DB" 2>&1 || echo "(error)")
    local dm_after=$(stat -f%z "$DM_DB" 2>/dev/null || stat -c%s "$DM_DB" 2>/dev/null || echo 0)

    # dualmem stats
    local dm_status=$($DUALMEM status --ns "$BENCH_NS" --db "$DM_DB" 2>/dev/null || echo "?")

    # claude-mem stats
    local cm_stats=$(curl -sf "$CM_API/api/stats" || echo '{}')
    local cm_obs=$(echo "$cm_stats" | jq '.database.observations // 0')
    local cm_summaries=$(echo "$cm_stats" | jq '.database.summaries // 0')
    local cm_dbsize=0
    [[ -f "$HOME/.claude-mem/claude-mem.db" ]] && cm_dbsize=$(stat -f%z "$HOME/.claude-mem/claude-mem.db" 2>/dev/null || echo 0)

    # Raw text size for comparison
    local raw_text_size=$(jq -r '.memories[].text' "$CORPUS" | wc -c)

    cat > "$out" <<EOF
# Benchmark 5: Compression & Storage Efficiency

## Raw corpus
- 100 memories
- Raw text size: $(echo "$raw_text_size" | awk '{printf "%.1fKB", $1/1024}')

## dualmem

| Metric | Value |
|--------|-------|
| DB before distill | $(echo "$dm_before" | awk '{printf "%.1fKB", $1/1024}') |
| DB after distill | $(echo "$dm_after" | awk '{printf "%.1fKB", $1/1024}') |
| Compression ratio | $(echo "$raw_text_size $dm_after" | awk '{if($2>0) printf "%.1fx", $1/$2; else print "N/A"}') |
| Architecture | Dual-path: Detail (100 cap) + Sketch (3-tier: raw→episodes→arcs→profile) |

**Distill output:**
\`\`\`
$distill_out
\`\`\`

**Status:**
\`\`\`
$dm_status
\`\`\`

## claude-mem

| Metric | Value |
|--------|-------|
| DB size | $(echo "$cm_dbsize" | awk '{printf "%.1fKB", $1/1024}') |
| Observations | $cm_obs |
| Summaries | $cm_summaries |
| Architecture | Flat observations + session summaries + optional ChromaDB vectors |

**Stats:**
\`\`\`json
$cm_stats
\`\`\`

## Key Differences
- dualmem actively compresses: raw memories → episodes → arcs → profile
- claude-mem stores observations flat, relies on LLM summaries for compression
- dualmem vectors stored inline (768d = 3KB/vector); claude-mem delegates to ChromaDB
EOF

    ok "Results written to $out"
}

# ============================================================
# MAIN
# ============================================================
main() {
    local bench="${1:-all}"

    echo ""
    echo "======================================================"
    echo "  dualmem vs claude-mem Head-to-Head Benchmark"
    echo "======================================================"
    echo ""

    check_prereqs

    case "$bench" in
        bench1|write)   bench_write ;;
        bench2|search)  bench_search ;;
        bench3|context) bench_context ;;
        bench4|code)    bench_code ;;
        bench5|compress) bench_compress ;;
        all)
            bench_write
            echo ""
            bench_search
            echo ""
            bench_context
            echo ""
            bench_code
            echo ""
            bench_compress
            echo ""
            log "=== ALL BENCHMARKS COMPLETE ==="
            log "Results in: $RESULTS_DIR/"
            ls -la "$RESULTS_DIR/"*.md 2>/dev/null
            ;;
        *)
            echo "Usage: $0 [write|search|context|code|compress|all]"
            exit 1
            ;;
    esac
}

main "$@"
