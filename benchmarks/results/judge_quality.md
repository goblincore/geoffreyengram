# LLM-as-Judge: Context Quality Comparison

Generated: 2026-04-07 22:05:37
Judge: GLM 5.1 via z.ai (position-randomized A/B)

## Summary

| Metric | dualmem | claude-mem |
|---|---|---|
| Overall quality (avg of 4 dims) | **6.5/10** | **3.4/10** |
| Avg tokens used | 704 | 477 |
| Quality per token | 9.16/1k tok | 7.03/1k tok |
| Scenarios evaluated | 5 | 5 |

## Per-Scenario Results

### s1: I need to add a new payment method (Apple Pay) to the checko

Tokens: dualmem=617, claude-mem=492

| Dimension | dualmem | claude-mem |
|---|---|---|
| relevance | 6 | 3 |
| completeness | 4 | 3 |
| signal_noise | 5 | 2 |
| actionability | 8 | 2 |
| **Average** | **5.8** | **2.5** |

dualmem notes: Provides PostgreSQL and feature flag context, but misses Stripe integration, circuit breaker, and CQRS. Half the context is irrelevant noise (e.g., file uploads, GraphQL, Go version).

claude-mem notes: Contains PostgreSQL and circuit breaker info, but they are buried in a massive table of 20 mostly irrelevant results. Lacks Stripe, CQRS, and feature flags. Truncated text makes it unactionable.

### s2: The WebSocket connections are dropping under load, I need to

Tokens: dualmem=664, claude-mem=456

| Dimension | dualmem | claude-mem |
|---|---|---|
| relevance | 8 | 3 |
| completeness | 7 | 4 |
| signal_noise | 6 | 2 |
| actionability | 9 | 2 |
| **Average** | **7.5** | **2.8** |

dualmem notes: Surfaces the critical WebSocket hub mutex warning, SSE goroutine issue, and connection pool limits with actionable advice. Missing the GraphQL subscription leak. Contains many irrelevant items (logging, routing, forms, uploads) that dilute signal.

claude-mem notes: Contains the relevant observations (#209, #188, #183) but they are buried in a flat table of 20 mostly unrelated items. Titles are truncated, no detail is visible, and the GraphQL subscription leak is missing. Developer would have to click through multiple items to find useful information.

### s3: I'm setting up the CI/CD pipeline for a new microservice

Tokens: dualmem=1097, claude-mem=473

| Dimension | dualmem | claude-mem |
|---|---|---|
| relevance | 9 | 4 |
| completeness | 7 | 2 |
| signal_noise | 8 | 3 |
| actionability | 9 | 2 |
| **Average** | **8.2** | **2.8** |

dualmem notes: File-scoped context surfaces GitHub Actions ARM64 runners, Argo CD, Argo Rollouts canary config, and related CI decisions clearly. Missing blue-green deploy script, ko for images, and Docker Compose for local dev, but otherwise highly actionable.

claude-mem notes: Returns a generic table of truncated observations. Only surfaces Argo Rollouts; misses GitHub Actions ARM64 runners, Argo CD, blue-green, ko, and Docker Compose. Developer must click into each item to read full context.

### s4: A junior developer is about to refactor the caching layer, w

Tokens: dualmem=579, claude-mem=462

| Dimension | dualmem | claude-mem |
|---|---|---|
| relevance | 8 | 2 |
| completeness | 7 | 2 |
| signal_noise | 7 | 1 |
| actionability | 9 | 1 |
| **Average** | **7.8** | **1.5** |

dualmem notes: Directly surfaces 4 of the 6 ideal points (LRU cache, DNS caching, S3 presigned TTL, Redis rate limiting) with clear, actionable warnings. Missing cache invalidation via pub/sub and user search reindex timing. Contains some noise from unrelated subsystems (auth, frontend, database).

claude-mem notes: Only returns a raw table of truncated, uncontextualized observations. Barely surfaces DNS caching and rate limiter info, but completely misses the LRU cache, S3 presigned URL TTL, pub/sub, and search reindexing. Mostly noise from completely unrelated features.

### s5: We're planning the Q3 roadmap, what's the current state of i

Tokens: dualmem=564, claude-mem=501

| Dimension | dualmem | claude-mem |
|---|---|---|
| relevance | 3 | 8 |
| completeness | 2 | 8 |
| signal_noise | 4 | 7 |
| actionability | 3 | 6 |
| **Average** | **3.0** | **7.2** |

dualmem notes: Provides almost none of the requested continuity items for Q3 roadmap planning. Instead, it returns a disjointed list of low-level code warnings and architectural decisions that require further investigation to act upon.

claude-mem notes: Surfaces most of the requested continuity items (gRPC, K8s, search, CI pipeline) in a clean list format. However, it is missing some key items (auth refactor, notifications, OAuth, mobile app) and the truncated text limits immediate actionability.

