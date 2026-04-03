package dualmem

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func newRatingTestEngine(t *testing.T) *Engine {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "rating-test.db")
	engine, err := New(Config{
		SQLitePath:        dbPath,
		EmbeddingProvider: &mockEmbedder{dim: 768},
		Classifier:        &sectorPassthrough{},
		EntityExtractor:   &mockExtractor{},
		MaxDetailPerUser:  100,
		ImportanceTheta:   0.65,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { engine.Close() })
	return engine
}

func TestBuildSnapshot(t *testing.T) {
	block := &ContextBlock{
		TokenCount: 500,
		Sources: []SourceRef{
			{Type: "detail", ID: "mem_1"},
			{Type: "detail", ID: "mem_2"},
			{Type: "codemap", ID: "ns"},
		},
	}
	details := []DetailMemory{
		{
			ID:              "mem_1",
			Text:            "JWT auth uses RS256",
			Sector:          "decision",
			Salience:        0.8,
			ImportanceScore: 0.75,
			Type:            "decision",
			Similarity:      0.85,
			CreatedAt:       time.Now().Add(-48 * time.Hour),
		},
		{
			ID:              "mem_2",
			Text:            "Don't touch cleanup()",
			Sector:          "warning",
			Salience:        0.9,
			ImportanceScore: 0.90,
			Type:            "warning",
			Similarity:      0.72,
			CreatedAt:       time.Now().Add(-24 * time.Hour),
		},
	}

	snap := BuildSnapshot("snap_1", "test-ns", "auth flow", block, details, time.Now())

	if snap.ID != "snap_1" {
		t.Errorf("expected ID snap_1, got %s", snap.ID)
	}
	if len(snap.Sources) != 3 {
		t.Fatalf("expected 3 sources, got %d", len(snap.Sources))
	}

	// First source should have enriched features from detail
	s0 := snap.Sources[0]
	if s0.CosineSim != 0.85 {
		t.Errorf("source 0 cosine_sim: want 0.85, got %f", s0.CosineSim)
	}
	if s0.MemType != "decision" {
		t.Errorf("source 0 mem_type: want decision, got %s", s0.MemType)
	}

	// Codemap source should have no enrichment
	s2 := snap.Sources[2]
	if s2.CosineSim != 0 {
		t.Errorf("codemap source should have zero cosine_sim, got %f", s2.CosineSim)
	}
}

func TestSnapshotPersistence(t *testing.T) {
	engine := newRatingTestEngine(t)

	snap := &ContextSnapshot{
		ID:         "snap_test_1",
		Namespace:  "test-ns",
		Query:      "auth flow",
		Sources:    []SnapshotSource{{ID: "mem_1", Type: "detail", CosineSim: 0.85}},
		TokensUsed: 500,
		CreatedAt:  time.Now(),
	}

	if err := engine.SaveSnapshot(snap); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}

	stored, err := engine.store.GetSnapshot("snap_test_1")
	if err != nil {
		t.Fatalf("GetSnapshot: %v", err)
	}
	if stored.Namespace != "test-ns" {
		t.Errorf("namespace: want test-ns, got %s", stored.Namespace)
	}
	if stored.TokensUsed != 500 {
		t.Errorf("tokens: want 500, got %d", stored.TokensUsed)
	}
}

func TestSubmitRatings(t *testing.T) {
	engine := newRatingTestEngine(t)

	// Save a snapshot first
	snap := &ContextSnapshot{
		ID:        "snap_rate_1",
		Namespace: "test-ns",
		Query:     "auth flow",
		Sources: []SnapshotSource{
			{ID: "mem_1", Type: "detail", CosineSim: 0.85, Importance: 0.75, Salience: 0.8, Sector: "decision", MemType: "decision", AgeDays: 2.0, TextLength: 20},
			{ID: "mem_2", Type: "detail", CosineSim: 0.72, Importance: 0.90, Salience: 0.9, Sector: "warning", MemType: "warning", AgeDays: 1.0, TextLength: 15},
		},
		TokensUsed: 500,
	}
	if err := engine.SaveSnapshot(snap); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}

	// Submit ratings
	ratings := map[string]int{"mem_1": 2, "mem_2": 1}
	if err := engine.SubmitRatings("test-ns", "snap_rate_1", "early", ratings); err != nil {
		t.Fatalf("SubmitRatings: %v", err)
	}

	// Verify stored
	all, err := engine.store.GetAllRatings("test-ns")
	if err != nil {
		t.Fatalf("GetAllRatings: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 ratings, got %d", len(all))
	}

	// Check features were denormalized
	for _, r := range all {
		if r.MemoryID == "mem_1" {
			if r.Rating != 2 {
				t.Errorf("mem_1 rating: want 2, got %d", r.Rating)
			}
			if r.CosineSim != 0.85 {
				t.Errorf("mem_1 cosine_sim: want 0.85, got %f", r.CosineSim)
			}
			if r.Phase != "early" {
				t.Errorf("mem_1 phase: want early, got %s", r.Phase)
			}
		}
	}
}

func TestGetTrainingRows(t *testing.T) {
	engine := newRatingTestEngine(t)

	// Save snapshot + ratings for both phases
	snap := &ContextSnapshot{
		ID:        "snap_train_1",
		Namespace: "test-ns",
		Query:     "auth flow",
		Sources: []SnapshotSource{
			{ID: "mem_1", Type: "detail", CosineSim: 0.85, MemType: "warning"},
			{ID: "mem_2", Type: "detail", CosineSim: 0.3, MemType: ""},
		},
		TokensUsed: 500,
	}
	engine.SaveSnapshot(snap)

	// Early phase
	engine.SubmitRatings("test-ns", "snap_train_1", "early", map[string]int{"mem_1": 2, "mem_2": 1})
	// Late phase — overrides early for mem_2
	engine.SubmitRatings("test-ns", "snap_train_1", "late", map[string]int{"mem_1": 2, "mem_2": 0})

	rows, err := engine.GetTrainingRows("test-ns")
	if err != nil {
		t.Fatalf("GetTrainingRows: %v", err)
	}

	// Should have 2 rows (deduplicated by snapshot+memory, late takes precedence)
	if len(rows) != 2 {
		t.Fatalf("expected 2 training rows, got %d", len(rows))
	}

	// Check weights: late should be 2.0, early 1.0
	for _, row := range rows {
		if row.Features.CosineSim == 0.3 {
			// This is mem_2 — late phase took precedence
			if row.Label != 0.0 {
				t.Errorf("mem_2 label should be 0.0 (late rating was 0), got %f", row.Label)
			}
			if row.Weight != 2.0 {
				t.Errorf("mem_2 weight should be 2.0 (late phase), got %f", row.Weight)
			}
		}
	}
}

func TestRatingStats(t *testing.T) {
	engine := newRatingTestEngine(t)

	// Save snapshot + ratings
	snap := &ContextSnapshot{
		ID:        "snap_stats_1",
		Namespace: "test-ns",
		Query:     "auth flow",
		Sources: []SnapshotSource{
			{ID: "m1", Type: "detail", MemType: "warning"},
			{ID: "m2", Type: "detail", MemType: "decision"},
			{ID: "m3", Type: "detail", MemType: ""},
		},
	}
	engine.SaveSnapshot(snap)
	engine.SubmitRatings("test-ns", "snap_stats_1", "late", map[string]int{
		"m1": 2, // relevant
		"m2": 1, // relevant
		"m3": 0, // noise
	})

	// Session rating
	engine.SubmitSessionRating(&SessionRating{
		ID:        "sr_1",
		Namespace: "test-ns",
		SessionID: "sess_1",
		Score:     4,
	})

	stats, err := engine.GetRatingStats("test-ns")
	if err != nil {
		t.Fatalf("GetRatingStats: %v", err)
	}

	if stats.TotalRatings != 3 {
		t.Errorf("total ratings: want 3, got %d", stats.TotalRatings)
	}
	// 2 out of 3 are relevant (rating >= 1)
	expectedPrec := 2.0 / 3.0
	if diff := stats.AvgPrecision - expectedPrec; diff > 0.01 || diff < -0.01 {
		t.Errorf("avg precision: want ~%.2f, got %.2f", expectedPrec, stats.AvgPrecision)
	}
	if stats.AvgSessionScore != 4.0 {
		t.Errorf("avg session score: want 4.0, got %.1f", stats.AvgSessionScore)
	}
}

func TestAssembleContextPersistsSnapshot(t *testing.T) {
	engine := newRatingTestEngine(t)
	ctx := context.Background()
	userID := "snap-user"

	// Add a memory
	engine.AddWithOptions(ctx, MemoryInput{
		UserMessage: "JWT auth uses RS256",
		SectorHint:  "decision",
		Salience:    0.8,
		Type:        "decision",
	}, userID)

	// AssembleContext should auto-persist a snapshot
	block, err := engine.AssembleContext(ctx, userID, "auth flow", 2000)
	if err != nil {
		t.Fatalf("AssembleContext: %v", err)
	}

	if block.SnapshotID == "" {
		t.Fatal("expected SnapshotID to be set on ContextBlock")
	}

	// Verify we can retrieve the snapshot
	snap, err := engine.store.GetSnapshot(block.SnapshotID)
	if err != nil {
		t.Fatalf("GetSnapshot(%s): %v", block.SnapshotID, err)
	}
	if snap.Namespace != userID {
		t.Errorf("snapshot namespace: want %s, got %s", userID, snap.Namespace)
	}
}
