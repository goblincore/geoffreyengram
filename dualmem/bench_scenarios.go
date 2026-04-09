package dualmem

// benchScenario defines a retrieval quality benchmark scenario.
type benchScenario struct {
	Name        string
	Description string
	Memories    []benchMemory // Memories to seed (mix of relevant + noise)
	Queries     []benchQuery  // Queries to evaluate
}

// benchMemory is a memory to seed into the engine.
type benchMemory struct {
	Text     string
	Type     string   // "warning", "decision", "continuity", ""
	Sector   string   // sector hint
	Salience float64  // 0 = default
	Files    []string // associated files
	Tag      string   // label for ground-truth matching (not stored)
}

// benchQuery is a query to evaluate against the seeded memories.
type benchQuery struct {
	Query        string
	TokenBudget  int
	ExpectedTags []string // tags of memories that SHOULD appear in results
	ForbidTags   []string // tags of memories that should NOT appear
	OrderedTags  []string // optional: first N should appear in this order
}

// benchScenarios returns the full set of retrieval quality benchmarks.
func benchScenarios() []benchScenario {
	return []benchScenario{
		scenarioAuthSystem(),
		scenarioWarningPriority(),
		scenarioTokenBudgetPressure(),
		scenarioNoiseResistance(),
		scenarioCrossSector(),
		scenarioIntentAware(),
		scenarioCoChangeBoost(),
		scenarioEntityGraphBoost(),
		scenarioKnowledgeDocPreference(),
		scenarioSupersede(),
	}
}

// scenarioAuthSystem tests topical retrieval — auth memories found, unrelated excluded.
func scenarioAuthSystem() benchScenario {
	return benchScenario{
		Name:        "Auth System",
		Description: "Tests topical retrieval: auth-related memories should surface, unrelated should not",
		Memories: []benchMemory{
			// Auth-related (3)
			{Text: "JWT validation middleware validates tokens in auth/jwt.go using RS256 signing", Type: "decision", Sector: "decision", Salience: 0.8, Files: []string{"auth/jwt.go"}, Tag: "auth-jwt"},
			{Text: "Authentication uses refresh token rotation with 7-day expiry stored in httponly cookies", Type: "decision", Sector: "decision", Salience: 0.7, Tag: "auth-refresh"},
			{Text: "Do not change: auth middleware skips token validation for /health and /metrics endpoints intentionally", Type: "warning", Sector: "warning", Salience: 0.9, Files: []string{"auth/middleware.go"}, Tag: "auth-warning"},
			// Unrelated noise (5)
			{Text: "Button component uses Tailwind CSS classes for responsive design", Sector: "map", Salience: 0.4, Tag: "ui-button"},
			{Text: "Database migrations run via goose in the migrations/ directory", Sector: "map", Salience: 0.5, Tag: "db-migrations"},
			{Text: "CI pipeline uses GitHub Actions with matrix builds for Go 1.21 and 1.22", Sector: "map", Salience: 0.4, Tag: "ci-pipeline"},
			{Text: "Logging uses structured JSON format via slog package", Sector: "decision", Salience: 0.5, Tag: "logging"},
			{Text: "Frontend build outputs to dist/ directory using Vite bundler", Sector: "map", Salience: 0.4, Tag: "frontend-build"},
		},
		Queries: []benchQuery{
			{
				Query:        "authentication flow",
				TokenBudget:  2000,
				ExpectedTags: []string{"auth-jwt", "auth-refresh", "auth-warning"},
				ForbidTags:   []string{"ui-button", "ci-pipeline", "frontend-build"},
			},
		},
	}
}

// scenarioWarningPriority tests that warnings appear before decisions and general memories.
func scenarioWarningPriority() benchScenario {
	return benchScenario{
		Name:        "Warning Priority",
		Description: "Tests type-priority ordering: warnings should appear before decisions and general memories",
		Memories: []benchMemory{
			{Text: "Rate limiter cleanup intentionally skips nil check for hot path performance", Type: "warning", Sector: "warning", Salience: 0.9, Files: []string{"rate_limiter.go"}, Tag: "warning-ratelimit"},
			{Text: "Chose token bucket algorithm over sliding window for rate limiting simplicity", Type: "decision", Sector: "decision", Salience: 0.7, Files: []string{"rate_limiter.go"}, Tag: "decision-algo"},
			{Text: "Rate limiter is configured per-endpoint in config.yaml with burst and sustained limits", Type: "", Sector: "map", Salience: 0.5, Files: []string{"rate_limiter.go", "config.yaml"}, Tag: "general-config"},
			{Text: "Rate limiter tests use mock clock for deterministic timing", Type: "decision", Sector: "decision", Salience: 0.6, Files: []string{"rate_limiter_test.go"}, Tag: "decision-tests"},
			// Noise
			{Text: "API versioning uses URL path prefix /v1/ /v2/", Sector: "map", Salience: 0.4, Tag: "api-versioning"},
			{Text: "Health check endpoint returns database connection status", Sector: "map", Salience: 0.3, Tag: "health-check"},
		},
		Queries: []benchQuery{
			{
				Query:        "rate limiter implementation",
				TokenBudget:  2000,
				ExpectedTags: []string{"warning-ratelimit", "decision-algo", "general-config"},
				OrderedTags:  []string{"warning-ratelimit", "decision-algo"}, // warning before decision
			},
		},
	}
}

// scenarioTokenBudgetPressure tests budget allocation under tight constraints.
func scenarioTokenBudgetPressure() benchScenario {
	memories := []benchMemory{
		// High-priority (should survive budget pressure)
		{Text: "CRITICAL: Never modify the consensus algorithm without full team review", Type: "warning", Sector: "warning", Salience: 0.9, Tag: "high-warning"},
		{Text: "Chose Raft consensus over Paxos for implementation simplicity", Type: "decision", Sector: "decision", Salience: 0.8, Tag: "high-decision"},
		{Text: "Currently refactoring the storage layer from single-node to distributed", Type: "continuity", Sector: "continuity", Salience: 0.7, Tag: "high-continuity"},
	}
	// Low-priority filler (should be excluded under budget pressure)
	for i := 0; i < 15; i++ {
		memories = append(memories, benchMemory{
			Text:     fillerMemory(i),
			Sector:   "map",
			Salience: 0.3,
			Tag:      "filler",
		})
	}

	return benchScenario{
		Name:        "Token Budget Pressure",
		Description: "Tests budget allocation: high-priority memories fit within tight budget, low-priority excluded",
		Memories:    memories,
		Queries: []benchQuery{
			{
				Query:        "distributed storage consensus",
				TokenBudget:  500, // Very tight budget
				ExpectedTags: []string{"high-warning", "high-decision"},
			},
		},
	}
}

// scenarioNoiseResistance tests finding relevant memories amid lots of noise.
func scenarioNoiseResistance() benchScenario {
	memories := []benchMemory{
		// Signal (2 relevant memories)
		{Text: "WebSocket connection manager in ws/manager.go handles reconnection with exponential backoff", Type: "decision", Sector: "decision", Salience: 0.7, Files: []string{"ws/manager.go"}, Tag: "ws-reconnect"},
		{Text: "WebSocket authentication requires a valid JWT token in the initial handshake message", Type: "warning", Sector: "warning", Salience: 0.8, Files: []string{"ws/auth.go"}, Tag: "ws-auth"},
	}
	// 25 noisy memories about various unrelated topics
	noiseTopics := []string{
		"Docker compose uses port 5432 for PostgreSQL",
		"README updated with new installation steps for M1 Macs",
		"Terraform state stored in S3 bucket infra-state-prod",
		"Go module requires minimum Go 1.21 for slog support",
		"CSS theme variables defined in :root scope in globals.css",
		"Kubernetes deployment uses rolling update strategy with maxSurge 25%",
		"Redis cache TTL set to 5 minutes for session data",
		"GraphQL schema uses code-first approach with gqlgen",
		"Error codes follow HTTP status semantics in the REST API",
		"Feature flags stored in LaunchDarkly project prod-flags",
		"Monitoring alerts configured in Datadog for p99 latency above 500ms",
		"Backup script runs nightly at 2am UTC via cron job",
		"CDN configured with CloudFront distribution for static assets",
		"Email notifications use SendGrid API v3 with templates",
		"Mobile app uses Firebase push notifications for Android and iOS",
		"Payment processing webhook endpoint validates Stripe signatures",
		"Search indexing uses Elasticsearch 8 with custom analyzers",
		"Load balancer health checks hit /healthz endpoint every 30 seconds",
		"Database connection pool maxes at 20 connections per service instance",
		"API rate limiting returns 429 status with Retry-After header",
		"Git hooks run linting and type checking on pre-commit",
		"Log rotation configured for 7 days retention with logrotate",
		"SSL certificates auto-renewed via Let's Encrypt certbot",
		"Deployment pipeline uses blue-green strategy with canary at 10%",
		"Internationalization uses i18next with lazy-loaded translation files",
	}
	for i, topic := range noiseTopics {
		memories = append(memories, benchMemory{
			Text:     topic,
			Sector:   "map",
			Salience: 0.3 + float64(i%3)*0.1,
			Tag:      "noise",
		})
	}

	return benchScenario{
		Name:        "Noise Resistance",
		Description: "Tests signal-to-noise: 2 relevant memories must be found amid 25 noisy ones",
		Memories:    memories,
		Queries: []benchQuery{
			{
				Query:        "WebSocket connection handling",
				TokenBudget:  2000,
				ExpectedTags: []string{"ws-reconnect", "ws-auth"},
			},
		},
	}
}

// scenarioCrossSector tests retrieval across multiple coding sectors.
func scenarioCrossSector() benchScenario {
	return benchScenario{
		Name:        "Cross-Sector Retrieval",
		Description: "Tests sector diversity: relevant memories from multiple sectors should surface",
		Memories: []benchMemory{
			// Decision sector
			{Text: "Database schema uses UUIDs as primary keys instead of auto-increment integers", Type: "decision", Sector: "decision", Salience: 0.7, Files: []string{"models/base.go"}, Tag: "db-decision"},
			// Warning sector
			{Text: "Do not add foreign key constraints to the events table — it breaks bulk insert performance", Type: "warning", Sector: "warning", Salience: 0.9, Files: []string{"models/events.go"}, Tag: "db-warning"},
			// Map sector
			{Text: "Database models in models/ directory, migrations in migrations/, queries in store/", Type: "", Sector: "map", Salience: 0.5, Files: []string{"models/", "migrations/", "store/"}, Tag: "db-map"},
			// Continuity sector
			{Text: "In progress: migrating from raw SQL to sqlc for type-safe queries", Type: "continuity", Sector: "continuity", Salience: 0.6, Files: []string{"store/queries.sql"}, Tag: "db-continuity"},
			// Unrelated
			{Text: "Frontend routing uses React Router v6 with lazy loading", Sector: "map", Salience: 0.4, Tag: "frontend-routing"},
			{Text: "API documentation generated from OpenAPI spec in docs/api.yaml", Sector: "map", Salience: 0.4, Tag: "api-docs"},
		},
		Queries: []benchQuery{
			{
				Query:        "database schema and models",
				TokenBudget:  2000,
				ExpectedTags: []string{"db-decision", "db-warning", "db-map", "db-continuity"},
				ForbidTags:   []string{"frontend-routing"},
			},
		},
	}
}

// scenarioIntentAware tests that intent detection changes what gets surfaced.
func scenarioIntentAware() benchScenario {
	return benchScenario{
		Name:        "Intent-Aware Retrieval",
		Description: "Tests intent detection: debug query should boost warnings, continue query should boost continuity",
		Memories: []benchMemory{
			{Text: "API handler panics when request body exceeds 10MB limit without proper error boundary", Type: "warning", Sector: "warning", Salience: 0.9, Files: []string{"api/handler.go"}, Tag: "api-warning"},
			{Text: "Chose chi router over gin for its stdlib net/http compatibility", Type: "decision", Sector: "decision", Salience: 0.7, Files: []string{"api/router.go"}, Tag: "api-decision"},
			{Text: "In progress: adding request validation middleware with go-playground/validator", Type: "continuity", Sector: "continuity", Salience: 0.6, Files: []string{"api/middleware.go"}, Tag: "api-continuity"},
			{Text: "API routes defined in api/router.go, handlers in api/handler.go, middleware in api/middleware.go", Type: "", Sector: "map", Salience: 0.5, Files: []string{"api/"}, Tag: "api-map"},
		},
		Queries: []benchQuery{
			{
				// Debug intent — warning should be prioritized
				Query:        "fix API handler crash",
				TokenBudget:  2000,
				ExpectedTags: []string{"api-warning"},
				OrderedTags:  []string{"api-warning"}, // must be first
			},
			{
				// Continue intent — continuity should be prioritized
				Query:        "continue API middleware work where we left off",
				TokenBudget:  2000,
				ExpectedTags: []string{"api-continuity"},
			},
		},
	}
}

// fillerMemory generates a unique low-importance memory for padding.
func fillerMemory(i int) string {
	fillers := []string{
		"Updated README with badge for code coverage percentage",
		"Added .editorconfig for consistent indentation across editors",
		"Configured pre-commit hooks for gofmt and golint checks",
		"Set up GitHub Dependabot for weekly dependency updates",
		"Added CODEOWNERS file mapping directories to team leads",
		"Configured VS Code workspace settings for Go development",
		"Added Makefile targets for build test lint and coverage",
		"Set up Docker multi-stage build for smaller production images",
		"Configured GitHub Actions matrix build for multiple Go versions",
		"Added contributing guide with PR template and code style notes",
		"Set up renovate bot for automated dependency updates",
		"Added license header checker to CI pipeline",
		"Configured golangci-lint with custom rules for the project",
		"Added git-cliff for automated changelog generation",
		"Set up codecov integration with minimum coverage threshold",
	}
	return fillers[i%len(fillers)]
}

// scenarioCoChangeBoost tests whether memories about file A surface when querying about file B,
// when A and B co-change together.
func scenarioCoChangeBoost() benchScenario {
	return benchScenario{
		Name:        "Co-Change Boost",
		Description: "Tests co-change: memory about file A should surface when querying about co-changing file B",
		Memories: []benchMemory{
			// Memory about auth/jwt.go
			{Text: "JWT validation uses RS256 signing with key rotation every 90 days", Type: "decision", Sector: "decision", Salience: 0.7, Files: []string{"auth/jwt.go"}, Tag: "jwt-decision"},
			// Memory about auth/middleware.go (co-changes with jwt.go)
			{Text: "Auth middleware extracts bearer token from Authorization header", Type: "", Sector: "map", Salience: 0.5, Files: []string{"auth/middleware.go"}, Tag: "middleware-map"},
			// Noise
			{Text: "Frontend uses React 18 with concurrent rendering mode", Sector: "map", Salience: 0.3, Tag: "frontend-react"},
			{Text: "Docker compose exposes port 3000 for development", Sector: "map", Salience: 0.3, Tag: "docker-dev"},
			{Text: "CI pipeline runs on every push to main branch", Sector: "map", Salience: 0.3, Tag: "ci-pipeline"},
		},
		Queries: []benchQuery{
			{
				// Querying about jwt.go should also surface middleware.go memory via co-change
				Query:        "JWT token validation in auth/jwt.go",
				TokenBudget:  2000,
				ExpectedTags: []string{"jwt-decision", "middleware-map"},
				ForbidTags:   []string{"frontend-react", "docker-dev"},
			},
		},
	}
}

// scenarioEntityGraphBoost tests whether entity graph connections cause related memories to surface.
func scenarioEntityGraphBoost() benchScenario {
	return benchScenario{
		Name:        "Entity Graph Boost",
		Description: "Tests entity graph: memories linked to related entities should get boosted",
		Memories: []benchMemory{
			// Auth-related memories with overlapping entities
			{Text: "AuthService handles JWT generation and validation for all API endpoints", Type: "decision", Sector: "decision", Salience: 0.7, Files: []string{"auth/service.go"}, Tag: "auth-service"},
			{Text: "TokenStore persists refresh tokens in Redis with 7-day TTL", Type: "decision", Sector: "decision", Salience: 0.6, Files: []string{"auth/token_store.go"}, Tag: "token-store"},
			// Indirectly related (shares entity "authentication")
			{Text: "OAuth2 provider integration for Google and GitHub SSO login", Type: "decision", Sector: "decision", Salience: 0.6, Files: []string{"auth/oauth.go"}, Tag: "oauth-provider"},
			// Noise
			{Text: "Payment webhook validates Stripe signature before processing", Type: "warning", Sector: "warning", Salience: 0.8, Files: []string{"payments/webhook.go"}, Tag: "payment-webhook"},
			{Text: "Email service uses SendGrid API v3 with templates", Sector: "map", Salience: 0.4, Tag: "email-sendgrid"},
			{Text: "Search uses Elasticsearch 8 with custom analyzers", Sector: "map", Salience: 0.3, Tag: "search-elastic"},
		},
		Queries: []benchQuery{
			{
				Query:        "authentication service and token management",
				TokenBudget:  2000,
				ExpectedTags: []string{"auth-service", "token-store", "oauth-provider"},
				ForbidTags:   []string{"email-sendgrid", "search-elastic"},
			},
		},
	}
}

// scenarioSupersede tests that superseded memories don't pollute retrieval results.
// After supersession, querying should return the newer version, not the stale one.
func scenarioSupersede() benchScenario {
	return benchScenario{
		Name:        "Supersede Retrieval",
		Description: "Tests that superseded (stale) memories are excluded from retrieval, only current versions surface",
		Memories: []benchMemory{
			// These will be added sequentially — newer ones should supersede older ones.
			// The bench runner adds them in order, so supersede fires on each add.

			// Step 1-4: initial memories
			// Note: old/new pairs are designed to be lexically similar so the mock
			// hash-based embedder produces cosine similarity above the supersede threshold.
			{Text: "chose SQLite for zero-setup local storage in the store layer", Type: "decision", Sector: "decision", Salience: 0.8, Files: []string{"store.go"}, Tag: "old-decision"},
			{Text: "in progress: auth refactor, remaining: JWT validation and refresh tokens", Type: "continuity", Sector: "continuity", Salience: 0.7, Tag: "old-continuity"},
			{Text: "auth middleware skips /health endpoint check intentionally for performance", Type: "warning", Sector: "warning", Salience: 0.9, Files: []string{"auth/middleware.go"}, Tag: "old-warning"},
			{Text: "architecture: storage layer uses SQLite via store.go for persistence", Type: "architecture", Sector: "decision", Salience: 0.7, Files: []string{"store.go"}, Tag: "old-architecture"},

			// Step 5-8: superseding memories (should replace the above)
			{Text: "chose Postgres for production scalability in the store layer", Type: "decision", Sector: "decision", Salience: 0.8, Files: []string{"store.go", "migrations/"}, Tag: "new-decision"},
			{Text: "in progress: auth refactor, remaining: refresh tokens only", Type: "continuity", Sector: "continuity", Salience: 0.7, Tag: "new-continuity"},
			{Text: "auth middleware skips /health and /metrics endpoint checks intentionally for performance", Type: "warning", Sector: "warning", Salience: 0.9, Files: []string{"auth/middleware.go"}, Tag: "new-warning"},
			{Text: "architecture: storage layer uses Postgres via store.go for persistence", Type: "architecture", Sector: "decision", Salience: 0.7, Files: []string{"store.go", "migrations/"}, Tag: "new-architecture"},

			// Noise (should survive — unrelated)
			{Text: "Frontend uses React 18 with server components", Sector: "map", Salience: 0.3, Tag: "noise-frontend"},
			{Text: "CI runs on GitHub Actions with Go 1.22 matrix", Sector: "map", Salience: 0.3, Tag: "noise-ci"},
		},
		Queries: []benchQuery{
			{
				Query:        "database storage choice",
				TokenBudget:  2000,
				ExpectedTags: []string{"new-decision", "new-architecture"},
				ForbidTags:   []string{"old-decision", "old-architecture", "noise-frontend", "noise-ci"},
			},
			{
				Query:        "auth middleware endpoint skipping",
				TokenBudget:  2000,
				ExpectedTags: []string{"new-warning"},
				ForbidTags:   []string{"old-warning"},
			},
		},
	}
}

// scenarioKnowledgeDocPreference tests that knowledge docs are preferred over raw memories
// when they cover the same content.
func scenarioKnowledgeDocPreference() benchScenario {
	return benchScenario{
		Name:        "Knowledge Doc Preference",
		Description: "Tests knowledge doc preference: synthesized docs should surface instead of covered raw memories",
		Memories: []benchMemory{
			// Raw memories about database decisions (would be synthesized into a knowledge doc)
			{Text: "Database uses PostgreSQL 15 with pgvector extension for embeddings", Type: "decision", Sector: "decision", Salience: 0.7, Files: []string{"store/postgres.go"}, Tag: "db-postgres"},
			{Text: "Connection pool configured with max 20 connections per service instance", Type: "decision", Sector: "decision", Salience: 0.5, Files: []string{"store/pool.go"}, Tag: "db-pool"},
			{Text: "Migrations managed with golang-migrate, auto-run on startup in dev mode", Type: "decision", Sector: "decision", Salience: 0.5, Files: []string{"store/migrate.go"}, Tag: "db-migrate"},
			// Non-database noise
			{Text: "API gateway uses rate limiting with 100 req/s per IP", Sector: "map", Salience: 0.4, Tag: "api-ratelimit"},
			{Text: "Monitoring uses Prometheus metrics with Grafana dashboards", Sector: "map", Salience: 0.3, Tag: "monitoring-prom"},
		},
		Queries: []benchQuery{
			{
				// Should surface all three database memories (or a knowledge doc covering them)
				Query:        "database architecture and configuration",
				TokenBudget:  2000,
				ExpectedTags: []string{"db-postgres", "db-pool", "db-migrate"},
				ForbidTags:   []string{"api-ratelimit", "monitoring-prom"},
			},
		},
	}
}
