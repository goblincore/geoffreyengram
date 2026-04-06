package dualmem

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestHealthHealthy(t *testing.T) {
	engine := newTestEngine(t)
	ctx := context.Background()

	// Add several high-salience memories that route to Detail Path
	for i := 0; i < 3; i++ {
		err := engine.AddWithOptions(ctx, MemoryInput{
			UserMessage: fmt.Sprintf("Decision memory %d about auth module", i),
			SectorHint:  "decision",
			Salience:    0.9,
		}, "test-ns")
		if err != nil {
			t.Fatalf("AddWithOptions %d: %v", i, err)
		}
	}

	hc, err := engine.Health(ctx, "test-ns")
	if err != nil {
		t.Fatalf("Health: %v", err)
	}

	if hc.Status != "healthy" {
		t.Errorf("expected status healthy, got %s", hc.Status)
		for _, c := range hc.Checks {
			t.Logf("  %s [%s]: %s", c.Name, c.Status, c.Message)
		}
	}

	if hc.Summary.TotalDetailMemories != 3 {
		t.Errorf("expected 3 detail memories, got %d", hc.Summary.TotalDetailMemories)
	}

	// Verify all checks pass
	for _, c := range hc.Checks {
		if c.Status != "ok" {
			t.Errorf("check %q: expected ok, got %s (%s)", c.Name, c.Status, c.Message)
		}
	}
}

func TestHealthMixedEmbeddingModels(t *testing.T) {
	engine := newTestEngine(t)
	ctx := context.Background()

	// Add a normal memory (gets default embedding_model)
	err := engine.AddWithOptions(ctx, MemoryInput{
		UserMessage: "Normal memory with default model",
		SectorHint:  "decision",
		Salience:    0.9,
	}, "test-ns")
	if err != nil {
		t.Fatalf("AddWithOptions: %v", err)
	}

	// Insert another memory with a different embedding_model via raw SQL
	store := engine.GetStore().(*SQLiteStore)
	vec := make([]byte, 768*4) // zero vector
	_, err = store.db.Exec(`INSERT INTO detail_memories
		(id, user_id, text, embedding, importance_score, sector, embedding_model, salience)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"mem-other", "test-ns", "Memory with different model", vec, 0.8, "decision", "other-model-v2", 0.9)
	if err != nil {
		t.Fatalf("insert other model: %v", err)
	}

	hc, err := engine.Health(ctx, "test-ns")
	if err != nil {
		t.Fatalf("Health: %v", err)
	}

	if hc.Status != "warnings" {
		t.Errorf("expected warnings, got %s", hc.Status)
	}

	// Find the embedding model check
	found := false
	for _, c := range hc.Checks {
		if c.Name == "Embedding model consistency" {
			found = true
			if c.Status != "warning" {
				t.Errorf("expected warning status, got %s", c.Status)
			}
			if c.Detail == "" {
				t.Error("expected detail to list model counts")
			}
		}
	}
	if !found {
		t.Error("Embedding model consistency check not found")
	}
}

func TestHealthOrphanedSketchRaw(t *testing.T) {
	engine := newTestEngine(t)
	ctx := context.Background()

	// Add a detail memory so the namespace isn't empty
	err := engine.AddWithOptions(ctx, MemoryInput{
		UserMessage: "Normal memory",
		SectorHint:  "decision",
		Salience:    0.9,
	}, "test-ns")
	if err != nil {
		t.Fatalf("AddWithOptions: %v", err)
	}

	// Insert stale unprocessed sketch_raw
	store := engine.GetStore().(*SQLiteStore)
	err = store.InsertSketchRaw("test-ns", "old stale content that should have been processed", "decision", "", nil)
	if err != nil {
		t.Fatalf("InsertSketchRaw: %v", err)
	}

	// Update its created_at to 8 days ago (beyond the 7-day threshold)
	eightDaysAgo := time.Now().AddDate(0, 0, -8).Format("2006-01-02 15:04:05")
	_, err = store.db.Exec(`UPDATE sketch_raw SET created_at = ? WHERE user_id = 'test-ns'`, eightDaysAgo)
	if err != nil {
		t.Fatalf("update created_at: %v", err)
	}

	hc, err := engine.Health(ctx, "test-ns")
	if err != nil {
		t.Fatalf("Health: %v", err)
	}

	if hc.Status != "warnings" {
		t.Errorf("expected warnings, got %s", hc.Status)
		for _, c := range hc.Checks {
			t.Logf("  %s [%s]: %s", c.Name, c.Status, c.Message)
		}
	}

	found := false
	for _, c := range hc.Checks {
		if c.Name == "Orphaned sketch records" {
			found = true
			if c.Status != "warning" {
				t.Errorf("expected warning, got %s", c.Status)
			}
		}
	}
	if !found {
		t.Error("Orphaned sketch records check not found")
	}
}

func TestHealthEmptyNamespace(t *testing.T) {
	engine := newTestEngine(t)
	ctx := context.Background()

	hc, err := engine.Health(ctx, "empty-ns")
	if err != nil {
		t.Fatalf("Health: %v", err)
	}

	if hc.Status != "issues" {
		t.Errorf("expected issues, got %s", hc.Status)
	}

	found := false
	for _, c := range hc.Checks {
		if c.Name == "Namespace populated" {
			found = true
			if c.Status != "error" {
				t.Errorf("expected error, got %s", c.Status)
			}
		}
	}
	if !found {
		t.Error("Namespace populated check not found")
	}

	// Summary should show zero counts
	if hc.Summary.TotalDetailMemories != 0 {
		t.Errorf("expected 0 detail memories, got %d", hc.Summary.TotalDetailMemories)
	}
}

func TestHealthSummaryCounts(t *testing.T) {
	engine := newTestEngine(t)
	ctx := context.Background()

	// Add 5 detail memories
	for i := 0; i < 5; i++ {
		err := engine.AddWithOptions(ctx, MemoryInput{
			UserMessage: fmt.Sprintf("Detail memory %d", i),
			SectorHint:  "decision",
			Salience:    0.9,
		}, "count-ns")
		if err != nil {
			t.Fatalf("AddWithOptions %d: %v", i, err)
		}
	}

	// Add 3 sketch_raw entries directly (bypass routing)
	store := engine.GetStore()
	for i := 0; i < 3; i++ {
		err := store.InsertSketchRaw("count-ns", fmt.Sprintf("Sketch raw %d", i), "decision", "", nil)
		if err != nil {
			t.Fatalf("InsertSketchRaw %d: %v", i, err)
		}
	}

	hc, err := engine.Health(ctx, "count-ns")
	if err != nil {
		t.Fatalf("Health: %v", err)
	}

	if hc.Summary.TotalDetailMemories != 5 {
		t.Errorf("expected 5 detail memories, got %d", hc.Summary.TotalDetailMemories)
	}

	if hc.Summary.TotalSketchRaw != 3 {
		t.Errorf("expected 3 sketch raw, got %d", hc.Summary.TotalSketchRaw)
	}

	if hc.Summary.DatabaseSize <= 0 {
		t.Errorf("expected positive database size, got %d", hc.Summary.DatabaseSize)
	}

	// Oldest and newest should be set
	if hc.Summary.OldestMemory.IsZero() {
		t.Error("expected non-zero oldest memory time")
	}
	if hc.Summary.NewestMemory.IsZero() {
		t.Error("expected non-zero newest memory time")
	}
}
