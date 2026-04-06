package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDispatchConfig(t *testing.T) {
	dir := t.TempDir()

	confPath := filepath.Join(dir, "dispatch.conf")
	confContent := `PLANS_DIR="` + dir + `/plans"
REPORTS_DIR="` + dir + `/reports"
LOG_FILE="` + dir + `/dispatch.log"
DEFAULT_MODEL="glm-5.1"
DEFAULT_AUTH_TOKEN_ENV="ZAI_API_KEY"
DEFAULT_BASE_URL="https://api.z.ai/api/anthropic"
DEFAULT_MAX_RUNTIME="1800"
CLAUDE_BIN="/usr/local/bin/claude"
LISTEN_ADDR="127.0.0.1:9090"
PLANNING_MODEL="claude-opus-4-6"
PLANNING_API_KEY_ENV="MY_PLANNING_KEY"
PLANNING_BASE_URL="https://planning.api.example.com"
`
	if err := os.WriteFile(confPath, []byte(confContent), 0644); err != nil {
		t.Fatal(err)
	}

	envPath := filepath.Join(dir, ".env")
	envContent := `export ZAI_API_KEY="somekey"
export ANTHROPIC_API_KEY="anotherkey"
`
	if err := os.WriteFile(envPath, []byte(envContent), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadDispatchConfig(confPath, envPath)
	if err != nil {
		t.Fatalf("loadDispatchConfig: %v", err)
	}

	if cfg.PlansDir != dir+"/plans" {
		t.Errorf("PlansDir: got %q, want %q", cfg.PlansDir, dir+"/plans")
	}
	if cfg.ReportsDir != dir+"/reports" {
		t.Errorf("ReportsDir: got %q, want %q", cfg.ReportsDir, dir+"/reports")
	}
	if cfg.DefaultModel != "glm-5.1" {
		t.Errorf("DefaultModel: got %q, want %q", cfg.DefaultModel, "glm-5.1")
	}
	if cfg.DefaultAuthTokenEnv != "ZAI_API_KEY" {
		t.Errorf("DefaultAuthTokenEnv: got %q", cfg.DefaultAuthTokenEnv)
	}
	if cfg.DefaultBaseURL != "https://api.z.ai/api/anthropic" {
		t.Errorf("DefaultBaseURL: got %q", cfg.DefaultBaseURL)
	}
	if cfg.DefaultMaxRuntime != "1800" {
		t.Errorf("DefaultMaxRuntime: got %q", cfg.DefaultMaxRuntime)
	}
	if cfg.ClaudeBin != "/usr/local/bin/claude" {
		t.Errorf("ClaudeBin: got %q", cfg.ClaudeBin)
	}
	if cfg.ListenAddr != "127.0.0.1:9090" {
		t.Errorf("ListenAddr: got %q", cfg.ListenAddr)
	}
	if cfg.PlanningModel != "claude-opus-4-6" {
		t.Errorf("PlanningModel: got %q", cfg.PlanningModel)
	}
	if cfg.PlanningAPIKeyEnv != "MY_PLANNING_KEY" {
		t.Errorf("PlanningAPIKeyEnv: got %q", cfg.PlanningAPIKeyEnv)
	}
	if cfg.PlanningBaseURL != "https://planning.api.example.com" {
		t.Errorf("PlanningBaseURL: got %q", cfg.PlanningBaseURL)
	}

	// Check .env vars
	if cfg.EnvVars["ZAI_API_KEY"] != "somekey" {
		t.Errorf("EnvVars[ZAI_API_KEY]: got %q", cfg.EnvVars["ZAI_API_KEY"])
	}
	if cfg.EnvVars["ANTHROPIC_API_KEY"] != "anotherkey" {
		t.Errorf("EnvVars[ANTHROPIC_API_KEY]: got %q", cfg.EnvVars["ANTHROPIC_API_KEY"])
	}
}

func TestLoadDispatchConfigDefaults(t *testing.T) {
	dir := t.TempDir()

	// Minimal conf, no listen addr or planning fields
	confPath := filepath.Join(dir, "dispatch.conf")
	if err := os.WriteFile(confPath, []byte("DEFAULT_MODEL=\"claude-sonnet-4-6\"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// No .env file
	envPath := filepath.Join(dir, ".env")

	cfg, err := loadDispatchConfig(confPath, envPath)
	if err != nil {
		t.Fatalf("loadDispatchConfig: %v", err)
	}

	if cfg.ListenAddr != "127.0.0.1:8090" {
		t.Errorf("ListenAddr default: got %q, want 127.0.0.1:8090", cfg.ListenAddr)
	}
	if cfg.PlanningModel != "claude-opus-4-6" {
		t.Errorf("PlanningModel default: got %q, want claude-opus-4-6", cfg.PlanningModel)
	}
	if cfg.PlanningAPIKeyEnv != "ANTHROPIC_API_KEY" {
		t.Errorf("PlanningAPIKeyEnv default: got %q, want ANTHROPIC_API_KEY", cfg.PlanningAPIKeyEnv)
	}
}

func TestLoadDispatchConfigHomeExpansion(t *testing.T) {
	dir := t.TempDir()

	home, _ := os.UserHomeDir()
	confPath := filepath.Join(dir, "dispatch.conf")
	confContent := "PLANS_DIR=\"$HOME/.claude/plans\"\n"
	if err := os.WriteFile(confPath, []byte(confContent), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadDispatchConfig(confPath, filepath.Join(dir, ".env"))
	if err != nil {
		t.Fatalf("loadDispatchConfig: %v", err)
	}

	expected := home + "/.claude/plans"
	if cfg.PlansDir != expected {
		t.Errorf("PlansDir: got %q, want %q", cfg.PlansDir, expected)
	}
}

func TestListPlans(t *testing.T) {
	dir := t.TempDir()
	plansDir := filepath.Join(dir, "plans")
	if err := os.MkdirAll(plansDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create plan files
	plans := []struct {
		name    string
		content string
	}{
		{
			"plan-a.md",
			"---\ntitle: Plan A\nstatus: pending\npriority: 5\n---\nBody A\n",
		},
		{
			"plan-b.md",
			"---\ntitle: Plan B\nstatus: running\npriority: 3\n---\nBody B\n",
		},
		{
			"plan-c.md",
			"---\ntitle: Plan C\nstatus: done\npriority: 1\n---\nBody C\n",
		},
		{
			"plan-d.md",
			"---\ntitle: Plan D\nstatus: pending\npriority: 1\n---\nBody D\n",
		},
	}
	for _, p := range plans {
		path := filepath.Join(plansDir, p.name)
		if err := os.WriteFile(path, []byte(p.content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	cfg := &DispatchConfig{PlansDir: plansDir}
	eng := NewEngine(cfg)

	result, err := eng.ListPlans()
	if err != nil {
		t.Fatalf("ListPlans: %v", err)
	}

	if len(result) != 4 {
		t.Fatalf("expected 4 plans, got %d", len(result))
	}

	// running should be first
	if result[0].Status != "running" {
		t.Errorf("first plan should be running, got %q", result[0].Status)
	}
	// done should be last
	if result[len(result)-1].Status != "done" {
		t.Errorf("last plan should be done, got %q", result[len(result)-1].Status)
	}

	// pending plans: plan-a (priority 5) should come before plan-d (priority 1)
	var pendingPlans []*Plan
	for _, p := range result {
		if p.Status == "pending" {
			pendingPlans = append(pendingPlans, p)
		}
	}
	if len(pendingPlans) != 2 {
		t.Fatalf("expected 2 pending plans, got %d", len(pendingPlans))
	}
	if pendingPlans[0].Name != "plan-a" {
		t.Errorf("first pending should be plan-a (priority 5), got %q", pendingPlans[0].Name)
	}
}

func TestDependenciesMet(t *testing.T) {
	dir := t.TempDir()
	plansDir := filepath.Join(dir, "plans")
	if err := os.MkdirAll(plansDir, 0755); err != nil {
		t.Fatal(err)
	}

	// dep-a: done
	os.WriteFile(filepath.Join(plansDir, "dep-a.md"), []byte("---\ntitle: Dep A\nstatus: done\n---\nBody\n"), 0644)
	// dep-b: pending
	os.WriteFile(filepath.Join(plansDir, "dep-b.md"), []byte("---\ntitle: Dep B\nstatus: pending\n---\nBody\n"), 0644)

	cfg := &DispatchConfig{PlansDir: plansDir}
	eng := NewEngine(cfg)

	// Plan with only done dependencies → met
	planAllDone := &Plan{DependsOn: []string{"dep-a"}}
	if !eng.dependenciesMet(planAllDone) {
		t.Error("dependencies should be met when all deps are done")
	}

	// Plan with a pending dependency → not met
	planNotDone := &Plan{DependsOn: []string{"dep-a", "dep-b"}}
	if eng.dependenciesMet(planNotDone) {
		t.Error("dependencies should not be met when dep-b is pending")
	}

	// Plan with no dependencies → always met
	planNoDeps := &Plan{DependsOn: []string{}}
	if !eng.dependenciesMet(planNoDeps) {
		t.Error("empty deps should always be met")
	}
}

func TestLogBuffer(t *testing.T) {
	lb := &LogBuffer{}

	lb.Append("line1")
	lb.Append("line2")

	ch, unsub := lb.Subscribe()
	defer unsub()

	// Should receive existing lines
	got := []string{<-ch, <-ch}
	if got[0] != "line1" || got[1] != "line2" {
		t.Errorf("existing lines: got %v", got)
	}

	// Append after subscribe
	lb.Append("line3")
	line3 := <-ch
	if line3 != "line3" {
		t.Errorf("new line: got %q, want line3", line3)
	}
}
