package main

// task defines an end-to-end LLM-as-judge benchmark task.
type task struct {
	Name        string         // CLI slug
	Title       string         // Display name
	Description string         // One-liner
	Setup       string         // Project context (injected into agent prompt)
	History     []historyEntry // Memories accumulated over "past sessions"
	Probe       string         // The question the agent must answer
	GoldAnswer  string         // What a perfect answer looks like (for judge reference)
}

type historyEntry struct {
	Text     string
	Type     string // "warning", "decision", "continuity", ""
	Sector   string
	Salience float64
	Files    []string
	DaysAgo  int // how many days ago this was added (for temporal realism)
}

var allTasks = []task{
	taskRememberDecision,
	taskRespectWarning,
	taskSessionContinuity,
	taskNoiseFiltering,
}

func taskByName(name string) *task {
	for i := range allTasks {
		if allTasks[i].Name == name {
			return &allTasks[i]
		}
	}
	return nil
}

// taskRememberDecision tests whether the agent can recall a specific past decision.
var taskRememberDecision = task{
	Name:        "decision",
	Title:       "Remember the Decision",
	Description: "Can the agent recall why we chose SQLite over Postgres?",
	Setup:       "You are an AI coding assistant with access to project memory. Use the provided context to answer the user's question about the project.",
	History: []historyEntry{
		// Session 1: exploration (10 days ago)
		{Text: "Project structure: cmd/ for CLI, internal/ for core logic, store/ for persistence", Sector: "map", Salience: 0.5, DaysAgo: 10},
		{Text: "Go module uses go 1.22 with workspace mode for local development", Sector: "decision", Salience: 0.5, DaysAgo: 10},
		// Session 2: database decision (7 days ago)
		{Text: "Evaluated Postgres, SQLite, and BadgerDB for the storage backend. Chose SQLite because this is a CLI tool that needs zero-setup — users shouldn't need to run a database server. Postgres was better for concurrent access but overkill for single-user CLI.", Type: "decision", Sector: "decision", Salience: 0.8, Files: []string{"store/sqlite.go"}, DaysAgo: 7},
		{Text: "SQLite WAL mode enabled for better read concurrency during search operations", Type: "decision", Sector: "decision", Salience: 0.6, Files: []string{"store/sqlite.go"}, DaysAgo: 7},
		// Session 3: unrelated work (3 days ago)
		{Text: "Added cobra CLI framework with subcommands: add, search, context, gc", Sector: "map", Salience: 0.5, Files: []string{"cmd/main.go"}, DaysAgo: 3},
		{Text: "Configured golangci-lint with errcheck and staticcheck enabled", Sector: "map", Salience: 0.3, DaysAgo: 3},
		{Text: "Unit tests use testify/assert for cleaner assertions", Sector: "decision", Salience: 0.4, DaysAgo: 3},
		// Noise
		{Text: "README uses shields.io badges for Go version and test status", Sector: "map", Salience: 0.2, DaysAgo: 5},
		{Text: "MIT license chosen for maximum adoption potential", Sector: "decision", Salience: 0.3, DaysAgo: 10},
	},
	Probe:      "What database are we using and why did we choose it?",
	GoldAnswer: "We're using SQLite because this is a CLI tool that needs zero-setup — users shouldn't need to run a database server. We evaluated Postgres and BadgerDB but chose SQLite for its simplicity. WAL mode is enabled for better read concurrency.",
}

// taskRespectWarning tests whether warnings are surfaced and influence the agent's advice.
var taskRespectWarning = task{
	Name:        "warning",
	Title:       "Respect the Warning",
	Description: "Does a warning about fragile code surface when the user wants to refactor?",
	Setup:       "You are an AI coding assistant with access to project memory. Use the provided context to answer the user's question, paying special attention to any warnings.",
	History: []historyEntry{
		// The critical warning
		{Text: "DO NOT TOUCH: rateLimiter.cleanup() intentionally skips nil check on the bucket map. This is a hot-path optimization — adding a nil check caused 15% throughput regression in benchmarks. The nil case is impossible in practice because init() always populates the map.", Type: "warning", Sector: "warning", Salience: 0.9, Files: []string{"ratelimiter/cleanup.go"}, DaysAgo: 14},
		// Supporting context
		{Text: "Rate limiter uses token bucket algorithm with per-endpoint configuration", Type: "decision", Sector: "decision", Salience: 0.7, Files: []string{"ratelimiter/bucket.go"}, DaysAgo: 14},
		{Text: "Benchmarks showed 50k req/s sustained throughput with current implementation", Sector: "decision", Salience: 0.6, Files: []string{"ratelimiter/bench_test.go"}, DaysAgo: 14},
		// Routine work (noise)
		{Text: "Added Prometheus metrics for rate limit hits and misses", Sector: "map", Salience: 0.5, Files: []string{"ratelimiter/metrics.go"}, DaysAgo: 7},
		{Text: "HTTP middleware wraps rate limiter for per-IP limiting", Sector: "map", Salience: 0.5, Files: []string{"middleware/ratelimit.go"}, DaysAgo: 7},
		{Text: "Docker compose includes Redis for distributed rate limiting in staging", Sector: "map", Salience: 0.4, DaysAgo: 5},
		{Text: "Error responses follow RFC 7807 Problem Details format", Sector: "decision", Salience: 0.4, DaysAgo: 5},
		{Text: "Structured logging with slog, JSON format in production", Sector: "decision", Salience: 0.3, DaysAgo: 3},
	},
	Probe:      "I'm going to refactor the rate limiter cleanup code. It looks like it's missing a nil check — should I add one?",
	GoldAnswer: "Do NOT add a nil check to rateLimiter.cleanup(). This is intentional — the nil check was removed as a hot-path optimization because it caused a 15% throughput regression in benchmarks. The nil case is impossible because init() always populates the bucket map.",
}

// taskSessionContinuity tests picking up where the last session left off.
var taskSessionContinuity = task{
	Name:        "continuity",
	Title:       "Session Continuity",
	Description: "Can the agent pick up incomplete work from a previous session?",
	Setup:       "You are an AI coding assistant with access to project memory. The user is starting a new session and wants to continue their previous work.",
	History: []historyEntry{
		// Session 1: started auth refactor (5 days ago)
		{Text: "Started auth refactor: migrating from session cookies to JWT tokens. Done: JWT generation in auth/jwt.go, token validation middleware. Remaining: refresh token rotation, logout endpoint, updating tests.", Type: "continuity", Sector: "continuity", Salience: 0.7, Files: []string{"auth/jwt.go", "auth/middleware.go"}, DaysAgo: 5},
		{Text: "JWT uses RS256 with key rotation every 90 days — keys stored in auth/keys/", Type: "decision", Sector: "decision", Salience: 0.7, Files: []string{"auth/jwt.go", "auth/keys/"}, DaysAgo: 5},
		// Session 2: more progress (2 days ago)
		{Text: "Auth refactor update: refresh token rotation implemented. Still remaining: logout endpoint (needs token blacklist), updating integration tests.", Type: "continuity", Sector: "continuity", Salience: 0.7, Files: []string{"auth/refresh.go"}, DaysAgo: 2},
		{Text: "Refresh tokens use opaque strings stored in DB, not JWTs — prevents token size bloat", Type: "decision", Sector: "decision", Salience: 0.6, Files: []string{"auth/refresh.go"}, DaysAgo: 2},
		// Unrelated noise
		{Text: "Updated CI to run tests in parallel with -count=1 flag", Sector: "map", Salience: 0.3, DaysAgo: 4},
		{Text: "Added healthcheck endpoint that validates database connectivity", Sector: "map", Salience: 0.4, DaysAgo: 3},
		{Text: "GraphQL schema uses gqlgen with server.go entrypoint", Sector: "map", Salience: 0.4, DaysAgo: 6},
	},
	Probe:      "What was I working on? Where did I leave off?",
	GoldAnswer: "You were refactoring auth from session cookies to JWT tokens. So far you've completed: JWT generation, token validation middleware, and refresh token rotation. Still remaining: logout endpoint (needs a token blacklist) and updating integration tests.",
}

// taskNoiseFiltering tests finding critical memories amid heavy noise.
var taskNoiseFiltering = task{
	Name:        "noise",
	Title:       "Noise Filtering",
	Description: "Can DualMem find 2 critical memories amid 30 routine ones?",
	Setup:       "You are an AI coding assistant with access to project memory. Answer the user's question using the provided context.",
	History: func() []historyEntry {
		entries := []historyEntry{
			// The 2 critical memories
			{Text: "CRITICAL: the payment webhook endpoint MUST validate Stripe signatures before processing. Last time someone removed the check during a refactor and we processed $12k in fraudulent charges before catching it.", Type: "warning", Sector: "warning", Salience: 0.9, Files: []string{"payments/webhook.go"}, DaysAgo: 30},
			{Text: "Payment amounts stored as cents (int64) not dollars (float64) to avoid floating point rounding errors. This was a P0 bug fix — we were losing pennies on every transaction.", Type: "decision", Sector: "decision", Salience: 0.8, Files: []string{"payments/models.go"}, DaysAgo: 20},
		}
		// 28 routine memories
		noise := []historyEntry{
			{Text: "Set up Go workspace with go.work file for local module development", Sector: "map", Salience: 0.3, DaysAgo: 25},
			{Text: "Added pre-commit hook for gofmt and go vet", Sector: "map", Salience: 0.2, DaysAgo: 24},
			{Text: "Configured GitHub Actions with Go 1.22 matrix build", Sector: "map", Salience: 0.3, DaysAgo: 23},
			{Text: "README updated with installation instructions", Sector: "map", Salience: 0.2, DaysAgo: 22},
			{Text: "Added Makefile with build, test, lint targets", Sector: "map", Salience: 0.3, DaysAgo: 21},
			{Text: "Docker compose for local development with hot reload", Sector: "map", Salience: 0.3, DaysAgo: 19},
			{Text: "Structured logging with zerolog, JSON output in production", Sector: "decision", Salience: 0.4, DaysAgo: 18},
			{Text: "API versioning via URL path prefix /api/v1/", Sector: "decision", Salience: 0.3, DaysAgo: 17},
			{Text: "CORS configured to allow frontend origin in development", Sector: "map", Salience: 0.2, DaysAgo: 16},
			{Text: "Health check endpoint returns database and Redis status", Sector: "map", Salience: 0.3, DaysAgo: 15},
			{Text: "Error responses follow RFC 7807 Problem Details", Sector: "decision", Salience: 0.3, DaysAgo: 14},
			{Text: "Request ID middleware adds X-Request-ID header for tracing", Sector: "map", Salience: 0.3, DaysAgo: 13},
			{Text: "Rate limiting middleware using token bucket per IP", Sector: "map", Salience: 0.3, DaysAgo: 12},
			{Text: "Database migrations managed with golang-migrate", Sector: "map", Salience: 0.3, DaysAgo: 11},
			{Text: "Test fixtures use testcontainers for Postgres integration tests", Sector: "decision", Salience: 0.4, DaysAgo: 10},
			{Text: "GraphQL schema generated with gqlgen from schema.graphqls", Sector: "map", Salience: 0.3, DaysAgo: 9},
			{Text: "Added pprof endpoints for profiling in development", Sector: "map", Salience: 0.2, DaysAgo: 8},
			{Text: "CSS uses Tailwind with custom design tokens", Sector: "map", Salience: 0.2, DaysAgo: 7},
			{Text: "Frontend bundled with Vite, output to dist/", Sector: "map", Salience: 0.2, DaysAgo: 6},
			{Text: "React Router v6 with lazy-loaded routes", Sector: "map", Salience: 0.2, DaysAgo: 5},
			{Text: "Form validation uses zod schemas shared with backend", Sector: "decision", Salience: 0.3, DaysAgo: 4},
			{Text: "E2E tests use Playwright with page object pattern", Sector: "decision", Salience: 0.3, DaysAgo: 3},
			{Text: "Feature flags managed in environment variables", Sector: "map", Salience: 0.2, DaysAgo: 2},
			{Text: "Sentry error tracking configured for production", Sector: "map", Salience: 0.3, DaysAgo: 1},
			{Text: "OpenTelemetry tracing with Jaeger backend for development", Sector: "map", Salience: 0.3, DaysAgo: 1},
			{Text: "API documentation auto-generated from OpenAPI spec", Sector: "map", Salience: 0.2, DaysAgo: 1},
			{Text: "Kubernetes manifests in deploy/ with kustomize overlays", Sector: "map", Salience: 0.3, DaysAgo: 1},
			{Text: "Prometheus metrics exposed on /metrics endpoint", Sector: "map", Salience: 0.3, DaysAgo: 1},
		}
		return append(entries, noise...)
	}(),
	Probe:      "I need to modify the payment processing code. What should I know before making changes?",
	GoldAnswer: "Two critical things: (1) The webhook endpoint MUST validate Stripe signatures — removing this check previously led to $12k in fraudulent charges. (2) Payment amounts are stored as cents (int64), not dollars (float64), to avoid floating-point rounding errors — this was a P0 bug fix.",
}
