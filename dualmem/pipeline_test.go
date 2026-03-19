package dualmem

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// mockSummarizer returns deterministic summaries for testing.
type mockSummarizer struct {
	episodeCalls int
	arcCalls     int
	profileCalls int
}

func (m *mockSummarizer) SummarizeEpisode(_ context.Context, messages []string) (string, error) {
	m.episodeCalls++
	return "Episode: discussed " + messages[0], nil
}

func (m *mockSummarizer) SummarizeArc(_ context.Context, episodes []string) (string, error) {
	m.arcCalls++
	return "Arc: themes from " + episodes[0], nil
}

func (m *mockSummarizer) UpdateProfile(_ context.Context, current *ProfileSketch, newArcs []string) (*ProfileSketch, error) {
	m.profileCalls++
	profile := &ProfileSketch{
		Interests:         []string{"testing"},
		PersonalityTraits: []string{"methodical"},
		CommStyle:         "direct",
	}
	if current != nil {
		profile.Interests = append(current.Interests, "testing")
	}
	return profile, nil
}

func newTestPipeline(t *testing.T) (*Pipeline, *SQLiteStore, *mockSummarizer) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	embedder := &mockEmbedder{dim: 768}
	summarizer := &mockSummarizer{}
	projector := NewProjector(42, 768)
	extractor := &mockExtractor{}

	cfg := &Config{}
	cfg.ApplyDefaults()

	pipeline := NewPipeline(store, embedder, summarizer, projector, extractor, cfg)
	return pipeline, store, summarizer
}

func TestProcessEpisodes(t *testing.T) {
	pipeline, store, summarizer := newTestPipeline(t)
	ctx := context.Background()

	// Insert raw entries for same user+session
	for _, msg := range []string{
		"I love dark roast coffee",
		"My favorite cafe is Blue Bottle",
		"I go there every Saturday morning",
	} {
		if err := store.InsertSketchRaw("user1", msg, "episodic", "sess1", nil); err != nil {
			t.Fatalf("InsertSketchRaw: %v", err)
		}
	}

	// processEpisodes uses GetUnprocessedRaw("", 100) which now queries all users
	if err := pipeline.processEpisodes(ctx); err != nil {
		t.Fatalf("processEpisodes: %v", err)
	}

	// Verify episode was created
	episodes, err := store.GetEpisodes("user1", nil)
	if err != nil {
		t.Fatalf("GetEpisodes: %v", err)
	}
	if len(episodes) != 1 {
		t.Fatalf("got %d episodes, want 1", len(episodes))
	}
	if episodes[0].SummaryText == "" {
		t.Error("episode summary is empty")
	}

	// Verify raw entries are marked processed
	raw, _ := store.GetUnprocessedRaw("user1", 100)
	if len(raw) != 0 {
		t.Errorf("got %d unprocessed raw, want 0", len(raw))
	}

	if summarizer.episodeCalls != 1 {
		t.Errorf("episode calls = %d, want 1", summarizer.episodeCalls)
	}
}

func TestProcessEpisodesSkipsSingleEntry(t *testing.T) {
	pipeline, store, summarizer := newTestPipeline(t)
	ctx := context.Background()

	// Insert only 1 raw entry — should not trigger episode creation
	store.InsertSketchRaw("user1", "Just one message", "episodic", "sess1", nil)

	if err := pipeline.processEpisodes(ctx); err != nil {
		t.Fatalf("processEpisodes: %v", err)
	}

	episodes, _ := store.GetEpisodes("user1", nil)
	if len(episodes) != 0 {
		t.Errorf("got %d episodes from single entry, want 0", len(episodes))
	}
	if summarizer.episodeCalls != 0 {
		t.Errorf("episode calls = %d, want 0", summarizer.episodeCalls)
	}
}

func TestProcessEpisodesGroupsBySession(t *testing.T) {
	pipeline, store, summarizer := newTestPipeline(t)
	ctx := context.Background()

	// Two sessions for same user
	store.InsertSketchRaw("user1", "Session 1 msg A", "episodic", "sess1", nil)
	store.InsertSketchRaw("user1", "Session 1 msg B", "episodic", "sess1", nil)
	store.InsertSketchRaw("user1", "Session 2 msg A", "episodic", "sess2", nil)
	store.InsertSketchRaw("user1", "Session 2 msg B", "episodic", "sess2", nil)

	if err := pipeline.processEpisodes(ctx); err != nil {
		t.Fatalf("processEpisodes: %v", err)
	}

	episodes, _ := store.GetEpisodes("user1", nil)
	if len(episodes) != 2 {
		t.Errorf("got %d episodes, want 2 (one per session)", len(episodes))
	}
	if summarizer.episodeCalls != 2 {
		t.Errorf("episode calls = %d, want 2", summarizer.episodeCalls)
	}
}

func TestProcessArcs(t *testing.T) {
	pipeline, store, summarizer := newTestPipeline(t)
	ctx := context.Background()

	// Insert episodes then backdate their expires_at to make them expired
	vec := make([]float32, 768)
	vec[0] = 0.5
	for i, summary := range []string{
		"Discussed coffee preferences and cafe visits",
		"Talked about weekend coffee rituals and brewing",
	} {
		ep := &Episode{
			ID:          "ep-" + string(rune('a'+i)),
			SummaryText: summary,
			Entities:    []Entity{{Text: "coffee", Type: "topic"}},
			CreatedAt:   time.Now(),
		}
		if err := store.InsertEpisode(ep, vec, "user1", "test-model", nil); err != nil {
			t.Fatalf("InsertEpisode: %v", err)
		}
	}

	// Backdate expires_at so GetExpiredEpisodes finds them
	yesterday := time.Now().AddDate(0, 0, -1).Format("2006-01-02 15:04:05")
	store.db.Exec(`UPDATE episodes SET expires_at = ?`, yesterday)

	// Process arcs
	if err := pipeline.processArcs(ctx); err != nil {
		t.Fatalf("processArcs: %v", err)
	}

	// Verify arc was created
	arcs, err := store.GetArcs("user1")
	if err != nil {
		t.Fatalf("GetArcs: %v", err)
	}
	if len(arcs) != 1 {
		t.Fatalf("got %d arcs, want 1", len(arcs))
	}
	if arcs[0].SummaryText == "" {
		t.Error("arc summary is empty")
	}

	// Verify consumed episodes are deleted
	episodes, _ := store.GetEpisodes("user1", nil)
	if len(episodes) != 0 {
		t.Errorf("got %d episodes after arc processing, want 0", len(episodes))
	}

	if summarizer.arcCalls != 1 {
		t.Errorf("arc calls = %d, want 1", summarizer.arcCalls)
	}
}

func TestProcessArcsSkipsSingleEpisode(t *testing.T) {
	pipeline, store, summarizer := newTestPipeline(t)
	ctx := context.Background()

	// Only one expired episode — need at least 2 for clustering
	vec := make([]float32, 768)
	ep := &Episode{
		ID:          "ep-solo",
		SummaryText: "Single episode",
		CreatedAt:   time.Now(),
	}
	store.InsertEpisode(ep, vec, "user1", "test-model", nil)

	// Backdate expires_at
	yesterday := time.Now().AddDate(0, 0, -1).Format("2006-01-02 15:04:05")
	store.db.Exec(`UPDATE episodes SET expires_at = ?`, yesterday)

	pipeline.processArcs(ctx)

	arcs, _ := store.GetArcs("user1")
	if len(arcs) != 0 {
		t.Errorf("got %d arcs from single episode, want 0", len(arcs))
	}
	if summarizer.arcCalls != 0 {
		t.Errorf("arc calls = %d, want 0", summarizer.arcCalls)
	}
}

func TestUpdateProfile(t *testing.T) {
	pipeline, store, summarizer := newTestPipeline(t)
	ctx := context.Background()

	// Insert arcs
	vec128 := make([]float32, 128)
	for _, summary := range []string{
		"User is interested in coffee and cafes",
		"User enjoys weekend morning rituals",
	} {
		arc := &Arc{
			ID:          generateID(),
			SummaryText: summary,
			CreatedAt:   time.Now(),
		}
		store.InsertArc(arc, vec128, "user1", "test-model", 42)
	}

	// Update profile
	if err := pipeline.UpdateProfile(ctx, "user1"); err != nil {
		t.Fatalf("UpdateProfile: %v", err)
	}

	// Verify profile created
	profile, err := store.GetProfile("user1")
	if err != nil {
		t.Fatalf("GetProfile: %v", err)
	}
	if profile == nil {
		t.Fatal("expected profile, got nil")
	}
	if profile.CommStyle != "direct" {
		t.Errorf("comm_style = %q, want 'direct'", profile.CommStyle)
	}
	if summarizer.profileCalls != 1 {
		t.Errorf("profile calls = %d, want 1", summarizer.profileCalls)
	}
}

func TestProcessProfilesFindsStaleUsers(t *testing.T) {
	pipeline, store, summarizer := newTestPipeline(t)
	ctx := context.Background()

	vec64 := make([]float32, 64)
	vec128 := make([]float32, 128)

	// user1: has stale profile — backdate profile, then add arc
	store.UpsertProfile(&ProfileSketch{
		UserID:    "user1",
		Interests: []string{"old interest"},
		CommStyle: "old",
	}, vec64, "test-model", 42)
	// Backdate profile's updated_at to yesterday
	yesterday := time.Now().AddDate(0, 0, -1).Format("2006-01-02 15:04:05")
	store.db.Exec(`UPDATE profiles SET updated_at = ? WHERE user_id = 'user1'`, yesterday)

	// Insert arc with today's timestamp (created_at > profile.updated_at)
	store.InsertArc(&Arc{
		ID:          "arc-new",
		SummaryText: "New arc about testing",
		CreatedAt:   time.Now(),
	}, vec128, "user1", "test-model", 42)

	// user2: has arcs but no profile
	store.InsertArc(&Arc{
		ID:          "arc-user2",
		SummaryText: "User2 arc",
		CreatedAt:   time.Now(),
	}, vec128, "user2", "test-model", 42)

	// Process profiles
	if err := pipeline.processProfiles(ctx); err != nil {
		t.Fatalf("processProfiles: %v", err)
	}

	// Both users should have been updated
	if summarizer.profileCalls != 2 {
		t.Errorf("profile calls = %d, want 2", summarizer.profileCalls)
	}

	// Verify user2 now has a profile
	p2, _ := store.GetProfile("user2")
	if p2 == nil {
		t.Error("user2 should have a profile after processProfiles")
	}
}

func TestProcessProfilesSkipsUpToDate(t *testing.T) {
	pipeline, store, summarizer := newTestPipeline(t)
	ctx := context.Background()

	vec128 := make([]float32, 128)
	vec64 := make([]float32, 64)

	// Insert an arc first, then backdate it
	store.InsertArc(&Arc{
		ID:          "arc-1",
		SummaryText: "Old arc",
		CreatedAt:   time.Now(),
	}, vec128, "user1", "test-model", 42)
	yesterday := time.Now().AddDate(0, 0, -1).Format("2006-01-02 15:04:05")
	store.db.Exec(`UPDATE arcs SET created_at = ? WHERE id = 'arc-1'`, yesterday)

	// Create profile with today's timestamp (updated_at > arc.created_at)
	store.UpsertProfile(&ProfileSketch{
		UserID:    "user1",
		Interests: []string{"current"},
		CommStyle: "current",
	}, vec64, "test-model", 42)

	// Process profiles — should skip user1 (profile is fresh)
	pipeline.processProfiles(ctx)

	if summarizer.profileCalls != 0 {
		t.Errorf("profile calls = %d, want 0 (profile is up to date)", summarizer.profileCalls)
	}
}

func TestClusterEpisodes(t *testing.T) {
	now := time.Now()

	// Two similar episodes (same entities, close timestamps) + one different
	episodes := []episodeWithVector{
		{
			Episode: Episode{
				ID:          "a",
				SummaryText: "Coffee discussion",
				Entities:    []Entity{{Text: "coffee", Type: "topic"}, {Text: "cafe", Type: "place"}},
				CreatedAt:   now,
			},
			Vector: []float32{1, 0, 0, 0},
		},
		{
			Episode: Episode{
				ID:          "b",
				SummaryText: "More coffee talk",
				Entities:    []Entity{{Text: "coffee", Type: "topic"}, {Text: "brewing", Type: "topic"}},
				CreatedAt:   now.Add(1 * time.Hour),
			},
			Vector: []float32{0.9, 0.1, 0, 0},
		},
		{
			Episode: Episode{
				ID:          "c",
				SummaryText: "Programming in Go",
				Entities:    []Entity{{Text: "Go", Type: "language"}, {Text: "testing", Type: "topic"}},
				CreatedAt:   now.Add(10 * 24 * time.Hour), // 10 days later
			},
			Vector: []float32{0, 0, 1, 0},
		},
	}

	clusters := clusterEpisodes(episodes)

	// Episodes a+b should cluster together, c should be separate
	if len(clusters) < 2 {
		t.Errorf("got %d clusters, want at least 2", len(clusters))
	}

	t.Logf("Clusters: %d", len(clusters))
	for i, c := range clusters {
		var ids []string
		for _, ep := range c {
			ids = append(ids, ep.ID)
		}
		t.Logf("  Cluster %d: %v", i, ids)
	}
}
