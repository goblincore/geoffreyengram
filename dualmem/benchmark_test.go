package dualmem

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBenchmark_ColdVsWarm(t *testing.T) {
	engine := newTestEngine(t)
	ctx := context.Background()
	ns := "benchtest"

	// Seed some memories to create a "warm" state.
	for _, text := range []string{
		"Auth middleware validates JWT tokens in routes.go",
		"Database connection pool is configured in db.go",
		"Rate limiter uses token bucket algorithm",
	} {
		engine.AddWithOptions(ctx, MemoryInput{
			UserMessage: text,
			Type:        "decision",
			Files:       []string{"routes.go"},
		}, ns)
	}

	result, err := engine.Benchmark(ctx, ns, BenchmarkOpts{
		Queries:     []string{"auth middleware", "database connection"},
		TokenBudget: 2000,
	})
	if err != nil {
		t.Fatalf("Benchmark: %v", err)
	}

	if len(result.Queries) != 2 {
		t.Fatalf("expected 2 queries, got %d", len(result.Queries))
	}

	// Warm should have at least as many sources as cold.
	for _, q := range result.Queries {
		if q.WarmSources < q.ColdSources {
			t.Errorf("query %q: warm sources (%d) < cold sources (%d)", q.Query, q.WarmSources, q.ColdSources)
		}
	}
}

func TestBenchmark_FormatTable(t *testing.T) {
	result := &BenchmarkResult{
		Queries: []BenchQueryResult{
			{
				Query:       "test query one",
				ColdTokens:  500,
				WarmTokens:  1200,
				ColdSources: 1,
				WarmSources: 4,
				MemoryTypes: map[string]int{"decision": 2, "warning": 1},
			},
		},
		Summary: BenchSummary{
			AvgMemoryLift: 3.0,
			Coverage:      0.31,
			ModulesTotal:  86,
			ModulesCover:  27,
			TotalMemories: 42,
		},
	}

	output := FormatBenchmarkTable(result, "test:ns")
	if output == "" {
		t.Fatal("empty output")
	}

	// Should contain key elements.
	for _, want := range []string{"DualMem Benchmark", "test query one", "Cold", "Warm", "27/86"} {
		if !strings.Contains(output, want) {
			t.Errorf("output missing %q:\n%s", want, output)
		}
	}
}

func TestGitLogQueries(t *testing.T) {
	// Create a temp git repo with some commits.
	dir := t.TempDir()
	commands := [][]string{
		{"git", "init"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
	}
	for _, args := range commands {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if err := cmd.Run(); err != nil {
			t.Fatalf("setup %v: %v", args, err)
		}
	}

	// Create commits.
	for i, msg := range []string{"feat: add auth", "fix: database bug", "refactor: cleanup"} {
		f := filepath.Join(dir, "file"+string(rune('a'+i))+".txt")
		os.WriteFile(f, []byte("content"), 0o644)
		cmd := exec.Command("git", "add", ".")
		cmd.Dir = dir
		cmd.Run()
		cmd = exec.Command("git", "commit", "-m", msg)
		cmd.Dir = dir
		if err := cmd.Run(); err != nil {
			t.Fatalf("commit %q: %v", msg, err)
		}
	}

	queries, err := gitLogQueries(dir, 3)
	if err != nil {
		t.Fatalf("gitLogQueries: %v", err)
	}

	if len(queries) != 3 {
		t.Fatalf("expected 3 queries, got %d: %v", len(queries), queries)
	}

	// Most recent commit first.
	if queries[0] != "refactor: cleanup" {
		t.Errorf("expected first query 'refactor: cleanup', got %q", queries[0])
	}
}
