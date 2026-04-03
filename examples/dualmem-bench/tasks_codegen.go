package main

// taskConstrainedGeneration tests whether the agent incorporates project constraints
// from memory when generating new code.
var taskConstrainedGeneration = task{
	Name:        "constrained-gen",
	Title:       "Constrained Code Generation",
	Description: "Does the agent apply stored constraints when writing new code?",
	Setup:       "You are an AI coding assistant with access to project memory. Use the provided context to write code that follows existing project patterns and constraints.",
	History: []historyEntry{
		{Text: "Payment amounts stored as cents (int64) not dollars (float64) to avoid floating-point rounding errors. This was a P0 bug fix.", Type: "decision", Sector: "decision", Salience: 0.8, Files: []string{"payments/models.go"}, DaysAgo: 20},
		{Text: "CRITICAL: All webhook endpoints MUST validate Stripe signatures before processing. Last time this was removed during a refactor, we processed $12k in fraudulent charges.", Type: "warning", Sector: "warning", Salience: 0.9, Files: []string{"payments/webhook.go"}, DaysAgo: 30},
		{Text: "Error responses follow RFC 7807 Problem Details format with type, title, status, detail fields", Type: "decision", Sector: "decision", Salience: 0.7, Files: []string{"api/errors.go"}, DaysAgo: 15},
		{Text: "All API handlers use chi router middleware chain for auth, logging, and rate limiting", Type: "decision", Sector: "decision", Salience: 0.6, Files: []string{"api/router.go"}, DaysAgo: 10},
		// Noise
		{Text: "Frontend uses React 18 with TypeScript and Tailwind CSS", Sector: "map", Salience: 0.3, DaysAgo: 25},
		{Text: "Docker compose includes PostgreSQL, Redis, and MinIO for local development", Sector: "map", Salience: 0.3, DaysAgo: 20},
		{Text: "CI runs golangci-lint with custom config on every PR", Sector: "map", Salience: 0.2, DaysAgo: 15},
	},
	Probe:      "Write a new webhook endpoint that handles Stripe refund events. Include the handler function signature and key implementation details.",
	GoldAnswer: "The handler should: (1) validate Stripe signatures before processing, (2) use int64 cents for refund amounts not float64, (3) return RFC 7807 Problem Details on errors, (4) be registered on the chi router with the standard middleware chain.",
}

// taskWarningAwareRefactor tests whether the agent respects warnings during code changes.
var taskWarningAwareRefactor = task{
	Name:        "warning-refactor",
	Title:       "Warning-Aware Refactoring",
	Description: "Does the agent preserve warning constraints during a refactor?",
	Setup:       "You are an AI coding assistant with access to project memory. Use the provided context when suggesting code changes, especially respecting any warnings.",
	History: []historyEntry{
		{Text: "DO NOT TOUCH: cache.evict() intentionally uses a spinlock instead of sync.Mutex. The mutex version caused 30% throughput regression under high contention. The spinlock is safe because evict() holds the lock for <100ns.", Type: "warning", Sector: "warning", Salience: 0.9, Files: []string{"cache/evict.go"}, DaysAgo: 30},
		{Text: "Cache uses LRU eviction with a maximum of 10000 entries per shard", Type: "decision", Sector: "decision", Salience: 0.7, Files: []string{"cache/lru.go"}, DaysAgo: 25},
		{Text: "Cache is sharded by key hash into 16 shards for better concurrency", Type: "decision", Sector: "decision", Salience: 0.7, Files: []string{"cache/shard.go"}, DaysAgo: 25},
		{Text: "Cache benchmarks run nightly and results are tracked in benchmarks/ directory", Sector: "map", Salience: 0.5, Files: []string{"cache/bench_test.go"}, DaysAgo: 10},
		// Noise
		{Text: "Logging uses structured JSON with slog package", Sector: "decision", Salience: 0.4, DaysAgo: 20},
		{Text: "HTTP server uses graceful shutdown with 30s timeout", Sector: "map", Salience: 0.3, DaysAgo: 15},
	},
	Probe:      "I want to refactor the cache eviction code to use sync.Mutex for better readability. What should I know?",
	GoldAnswer: "Do NOT switch cache.evict() to sync.Mutex. The spinlock is intentional — the mutex version caused a 30% throughput regression under high contention. The spinlock is safe because evict() holds it for <100ns. If you need to refactor other parts of the cache, that's fine, but the spinlock in evict() must be preserved.",
}

// taskPatternContinuation tests whether the agent follows established project patterns.
var taskPatternContinuation = task{
	Name:        "pattern-continue",
	Title:       "Pattern Continuation",
	Description: "Does the agent follow established patterns when adding new functionality?",
	Setup:       "You are an AI coding assistant with access to project memory. Use the provided context to ensure new code follows existing project conventions.",
	History: []historyEntry{
		{Text: "All database models use UUIDs (uuid.New()) as primary keys, never auto-increment integers", Type: "decision", Sector: "decision", Salience: 0.7, Files: []string{"models/base.go"}, DaysAgo: 30},
		{Text: "Database queries use sqlc for type-safe generated Go code from SQL", Type: "decision", Sector: "decision", Salience: 0.7, Files: []string{"store/queries.sql", "store/db.go"}, DaysAgo: 25},
		{Text: "Migrations use golang-migrate with sequential numbered files in migrations/", Type: "decision", Sector: "decision", Salience: 0.6, Files: []string{"migrations/"}, DaysAgo: 25},
		{Text: "All models implement Validatable interface with Validate() error method", Type: "decision", Sector: "decision", Salience: 0.6, Files: []string{"models/base.go"}, DaysAgo: 20},
		// Noise
		{Text: "E2E tests use Playwright with page object pattern", Sector: "decision", Salience: 0.3, DaysAgo: 10},
		{Text: "Feature flags managed via environment variables", Sector: "map", Salience: 0.2, DaysAgo: 5},
	},
	Probe:      "I need to add a new 'comments' feature with a database model for storing user comments on posts. How should I set this up?",
	GoldAnswer: "Create the comments model with: (1) UUID primary key (uuid.New()), (2) implement the Validatable interface with Validate() error, (3) write SQL queries in store/queries.sql using sqlc conventions, (4) add a migration file in migrations/ with sequential numbering using golang-migrate format.",
}
