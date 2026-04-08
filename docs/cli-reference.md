# CLI Reference

```bash
# Memory
dualmem add --text "..." --type decision --salience 0.9 --files "a.go,b.go"
dualmem search "query" --limit 5
dualmem search "LC-1663"                                # identifier pre-filter

# Code search (HDC-powered, no API calls)
dualmem search-code "authentication middleware"
dualmem search-code "auth" --graph                      # experimental: PageRank + tags

# Context assembly
dualmem context "auth system" --budget 3000
dualmem context "fix the bug" --intent debug
dualmem context "task" --budget 3000 --index              # progressive disclosure
dualmem show --snapshot snap_xxx mem_a kdoc_b              # fetch specific items

# Intelligence reports
dualmem consult "how does auth work?" --budget 2000       # synthesized report (cached)
dualmem explore "credential issuance" --budget 3000       # grounded code briefing (ephemeral)

# Session management
dualmem checkpoint --task "auth refactor" --status in_progress --files "auth.go" --done "JWT" --remaining "refresh,logout"
dualmem checkpoint --list
dualmem map                                             # graph-based codebase map (default)
dualmem map --legacy                                    # HDC-based codebase map
dualmem map --refresh                                   # force full re-scan
dualmem map --git-graph                                 # diagnostic: git co-change edges
dualmem diff                                            # changes since last session

# Seed and distillation
dualmem seed [--dry-run] [--force]
dualmem distill [--dry-run] [--auto] [--file path]
dualmem index [--ns myproject] [--force]                # pre-warm codebase index

# File-scoped recall
dualmem file-context rate_limiter.go
dualmem file-index

# Entity graph
dualmem entities [stats|search|show|top]

# Co-change graph
dualmem cochange <file> [--min-strength N] [--decay]

# Structural graph
dualmem graph                                           # edge statistics
dualmem graph --json                                    # JSON output

# Knowledge docs
dualmem docs [list|show|delete|export]
dualmem synthesize [--dry-run] [--force] [--all]

# Context quality
dualmem rate --snapshot <id> --phase late --ratings '{"mem_id": 2}'
dualmem rate --session --score 4 --explanation "..."
dualmem train                                           # fit re-ranker from ratings
dualmem stats                                           # quality metrics + trends

# Maintenance
dualmem promote --id <id> [--type warning --salience 0.9]
dualmem demote --id <id>
dualmem gc [--dry-run --verbose]
dualmem gc --stale                                      # check for stale memories
dualmem profile
dualmem status
dualmem health                                          # diagnostic report
```
