package dualmem

import (
	"context"
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
	gen := &mockTextGen{response: "This module handles authentication."}
	engine.cfg.ExplorerGenerator = gen
	engine.cfg.SynthesisGenerator = gen
	return engine, gen
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
