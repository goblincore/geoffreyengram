package dualmem

import (
	"context"
	"path/filepath"
	"testing"
)

type mockTextGen struct {
	calls    int
	response string
}

func (m *mockTextGen) GenerateText(_ context.Context, _ string, _ int) (string, error) {
	m.calls++
	return m.response, nil
}

func newTestEngineWithExplorer(t *testing.T) (*Engine, *mockTextGen) {
	t.Helper()
	engine := newTestEngine(t)
	gen := &mockTextGen{response: "This area handles authentication and session management. Key files include auth.go for JWT validation and middleware.go for request interception."}
	engine.cfg.ExplorerGenerator = gen
	engine.cfg.SynthesisGenerator = gen
	return engine, gen
}

func TestAreaKeyFromDir(t *testing.T) {
	tests := []struct {
		dir  string
		want string
	}{
		{".", "."},
		{"src", "src"},
		{"src/utils", "src/utils"},
		{"packages/learn-card-core", "packages/learn-card-core"},
		{"packages/learn-card-core/src/lib", "packages/learn-card-core"},
		{"apps/learn-card-app/src/components/auth", "apps/learn-card-app"},
		{"services/lca-api/routes/v1", "services/lca-api"},
	}
	for _, tt := range tests {
		got := areaKeyFromDir(tt.dir)
		// Normalize to forward slashes for comparison.
		got = filepath.ToSlash(got)
		if got != tt.want {
			t.Errorf("areaKeyFromDir(%q) = %q, want %q", tt.dir, got, tt.want)
		}
	}
}

func TestGroupDirsIntoAreas(t *testing.T) {
	dirFiles := map[string][]string{
		"packages/core/src":     {"packages/core/src/index.ts", "packages/core/src/main.ts"},
		"packages/core/lib":     {"packages/core/lib/utils.ts"},
		"packages/types":        {"packages/types/index.ts"},
		"apps/web/src":          {"apps/web/src/App.tsx"},
		"apps/web/src/auth":     {"apps/web/src/auth/Login.tsx"},
	}
	recentlyChanged := map[string]bool{
		"apps/web/src/auth/Login.tsx": true,
	}

	areas := groupDirsIntoAreas(dirFiles, recentlyChanged)

	if len(areas) != 3 {
		t.Fatalf("expected 3 areas, got %d", len(areas))
	}

	// apps/web should be first (score 0.7 due to recently changed file).
	if areas[0].Key != "apps/web" {
		t.Errorf("expected first area to be apps/web, got %s", areas[0].Key)
	}
	if areas[0].Score != 0.7 {
		t.Errorf("expected apps/web score 0.7, got %f", areas[0].Score)
	}
	if len(areas[0].Files) != 2 {
		t.Errorf("expected 2 files in apps/web, got %d", len(areas[0].Files))
	}

	// Check that packages/core has 3 files merged from 2 dirs.
	for _, area := range areas {
		if area.Key == "packages/core" {
			if len(area.Files) != 3 {
				t.Errorf("expected 3 files in packages/core, got %d", len(area.Files))
			}
			if len(area.Dirs) != 2 {
				t.Errorf("expected 2 dirs in packages/core, got %d", len(area.Dirs))
			}
		}
	}
}

func TestPrioritizeFiles(t *testing.T) {
	files := []string{
		"src/utils.ts",
		"src/index.ts",
		"src/helpers.ts",
		"src/main.go",
	}
	recentlyChanged := map[string]bool{
		"src/helpers.ts": true,
	}

	result := prioritizeFiles(files, recentlyChanged)

	// helpers.ts should be first (recently changed + not entry point = score 2)
	// index.ts and main.go should come next (entry points = score 1)
	// utils.ts should be last (score 0)
	if result[0] != "src/helpers.ts" {
		t.Errorf("expected helpers.ts first (recently changed), got %s", result[0])
	}
	if result[len(result)-1] != "src/utils.ts" {
		t.Errorf("expected utils.ts last, got %s", result[len(result)-1])
	}
}

func TestBuildAreaPrompt(t *testing.T) {
	prompt := buildAreaPrompt("packages/core", []string{"src/index.ts", "src/main.ts"}, "const x = 1;")
	if !contains(prompt, "packages/core") {
		t.Error("prompt should include area key")
	}
	if !contains(prompt, "src/index.ts") {
		t.Error("prompt should include file list")
	}
	if !contains(prompt, "PURPOSE") {
		t.Error("prompt should include PURPOSE section")
	}
	if !contains(prompt, "GOTCHAS") {
		t.Error("prompt should include GOTCHAS section")
	}
	if !contains(prompt, "const x = 1") {
		t.Error("prompt should include code content")
	}
}

func contains(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 && len(s) >= len(substr) && (s == substr || len(s) > len(substr) && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestAutopilot_DryRun(t *testing.T) {
	engine, gen := newTestEngineWithExplorer(t)
	ctx := context.Background()
	result, err := engine.Autopilot(ctx, "testns", AutopilotOpts{Budget: 50000, DryRun: true})
	if err != nil {
		t.Fatalf("Autopilot: %v", err)
	}
	if gen.calls != 0 {
		t.Errorf("dry run should not call LLM, got %d calls", gen.calls)
	}
	if result.Explored != 0 {
		t.Errorf("dry run should explore 0, got %d", result.Explored)
	}
	if result.Areas == 0 && len(result.Targets) > 0 {
		t.Error("Areas count should be set in dry run")
	}
}

func TestAutopilot_BudgetExhaustion(t *testing.T) {
	engine, _ := newTestEngineWithExplorer(t)
	ctx := context.Background()
	result, err := engine.Autopilot(ctx, "testns", AutopilotOpts{Budget: 100})
	if err != nil {
		t.Fatalf("Autopilot: %v", err)
	}
	if result.TokensUsed > 200 {
		t.Errorf("should respect budget, used %d", result.TokensUsed)
	}
}
