package main

// taskPrecisionTriage tests context assembly precision — how much noise ends up in context?
var taskPrecisionTriage = task{
	Name:        "precision-triage",
	Title:       "Precision Triage",
	Description: "Is the assembled context mostly relevant, or full of noise?",
	Setup:       "You are evaluating the quality of context provided to an AI assistant. Rate how relevant the context is to the given task.",
	History: func() []historyEntry {
		entries := []historyEntry{
			// 3 relevant memories about search feature
			{Text: "Search uses Elasticsearch 8 with BM25 scoring and custom analyzers for code content", Type: "decision", Sector: "decision", Salience: 0.7, Files: []string{"search/elastic.go"}, DaysAgo: 10},
			{Text: "Search indexing runs asynchronously via worker queue after document updates", Type: "decision", Sector: "decision", Salience: 0.6, Files: []string{"search/indexer.go"}, DaysAgo: 10},
			{Text: "DO NOT disable search result caching — it was added after the Elasticsearch cluster hit rate limits during a traffic spike", Type: "warning", Sector: "warning", Salience: 0.9, Files: []string{"search/cache.go"}, DaysAgo: 20},
		}
		// 47 noise memories
		noise := []string{
			"Docker multi-stage build for smaller production images",
			"Kubernetes rolling update with maxSurge 25%",
			"GitHub Actions workflow matrix for Go 1.21 and 1.22",
			"README badges for code coverage and Go version",
			"MIT license chosen for broad adoption",
			"EditorConfig for consistent indentation",
			"Pre-commit hooks for gofmt and golint",
			"Dependabot weekly dependency updates",
			"CODEOWNERS mapping directories to team leads",
			"VS Code workspace settings for Go",
			"Makefile with build test lint targets",
			"Redis used for session storage with 24h TTL",
			"GraphQL schema code-first with gqlgen",
			"CSS uses Tailwind with custom design tokens",
			"Frontend bundled with Vite output to dist",
			"React Router v6 with lazy loaded routes",
			"Form validation uses zod schemas",
			"E2E tests use Playwright",
			"Feature flags via environment variables",
			"Sentry error tracking in production",
			"OpenTelemetry tracing with Jaeger",
			"API docs auto-generated from OpenAPI spec",
			"Prometheus metrics on /metrics endpoint",
			"Load balancer health checks every 30s",
			"Database connection pool max 20 connections",
			"API rate limiting returns 429 with Retry-After",
			"Git hooks run type checking on pre-commit",
			"Log rotation 7 days retention with logrotate",
			"SSL certs auto-renewed via Let's Encrypt",
			"Blue-green deployment with canary at 10%",
			"i18n uses i18next with lazy-loaded translations",
			"Mobile push notifications via Firebase",
			"Email notifications use SendGrid v3",
			"CDN configured with CloudFront for static assets",
			"Backup script nightly at 2am UTC via cron",
			"Monitoring alerts for p99 latency above 500ms",
			"Feature flags stored in LaunchDarkly",
			"Error codes follow HTTP status semantics",
			"Request ID middleware adds X-Request-ID header",
			"Token bucket rate limiting per IP",
			"Database migrations with golang-migrate",
			"Test fixtures use testcontainers for Postgres",
			"pprof endpoints for profiling in development",
			"Structured logging with zerolog JSON format",
			"API versioning via URL path /v1/ /v2/",
			"CORS configured for frontend origin in dev",
			"Health check returns database and Redis status",
		}
		for i, n := range noise {
			entries = append(entries, historyEntry{
				Text: n, Sector: "map", Salience: 0.2 + float64(i%3)*0.1, DaysAgo: i + 1,
			})
		}
		return entries
	}(),
	Probe:      "I'm debugging the search indexing pipeline. It's not updating results after document edits.",
	GoldAnswer: "The context should primarily contain the 3 search-related memories: Elasticsearch 8 with BM25 scoring, async indexing via worker queue, and the warning about search result caching. Noise about Docker, Kubernetes, frontend, etc. should be minimal.",
}

// taskRecallTriage tests whether all critical memories for a task surface.
var taskRecallTriage = task{
	Name:        "recall-triage",
	Title:       "Recall Triage",
	Description: "Do all critical memories surface for a complex task?",
	Setup:       "You are an AI coding assistant with access to project memory. Provide comprehensive guidance for the task using all relevant project context.",
	History: []historyEntry{
		// 5 critical memories about API authentication
		{Text: "API authentication uses JWT with RS256 signing, keys in auth/keys/ directory", Type: "decision", Sector: "decision", Salience: 0.8, Files: []string{"auth/jwt.go"}, DaysAgo: 30},
		{Text: "Refresh tokens are opaque strings stored in PostgreSQL, NOT JWTs — prevents token size bloat", Type: "decision", Sector: "decision", Salience: 0.7, Files: []string{"auth/refresh.go"}, DaysAgo: 25},
		{Text: "CRITICAL: Auth middleware MUST skip token validation for /health, /metrics, and /docs endpoints — these are used by monitoring and load balancers", Type: "warning", Sector: "warning", Salience: 0.9, Files: []string{"auth/middleware.go"}, DaysAgo: 20},
		{Text: "Rate limiting is applied AFTER auth middleware so that rate limit counts are per-user, not per-IP", Type: "decision", Sector: "decision", Salience: 0.7, Files: []string{"api/router.go"}, DaysAgo: 15},
		{Text: "Token blacklist for logout uses Redis SET with automatic expiry matching token lifetime", Type: "decision", Sector: "decision", Salience: 0.6, Files: []string{"auth/blacklist.go"}, DaysAgo: 10},
		// Noise
		{Text: "Frontend uses React 18 with TypeScript", Sector: "map", Salience: 0.3, DaysAgo: 20},
		{Text: "Docker compose for local development", Sector: "map", Salience: 0.3, DaysAgo: 15},
		{Text: "CI pipeline uses GitHub Actions", Sector: "map", Salience: 0.2, DaysAgo: 10},
		{Text: "README updated with API documentation links", Sector: "map", Salience: 0.2, DaysAgo: 5},
		{Text: "Added CODEOWNERS file for PR review routing", Sector: "map", Salience: 0.2, DaysAgo: 3},
	},
	Probe:      "I'm adding a new admin API endpoint that requires authentication. Walk me through the full auth setup I need to follow.",
	GoldAnswer: "All 5 critical auth memories should surface: (1) JWT with RS256 signing and key location, (2) refresh tokens as opaque strings in PostgreSQL, (3) auth middleware skip list for health/metrics/docs, (4) rate limiting applied after auth for per-user counting, (5) token blacklist via Redis SET for logout. The response should incorporate all of these to give comprehensive auth setup guidance.",
}
