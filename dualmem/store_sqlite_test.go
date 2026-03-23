package dualmem

import (
	"path/filepath"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *SQLiteStore {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestStoreDetailCRUD(t *testing.T) {
	store := newTestStore(t)

	dm := &DetailMemory{
		ID:              "test-1",
		Text:            "John likes dark roast coffee",
		Sector:          "semantic",
		Salience:        0.8,
		ImportanceScore: 0.75,
		Entities:        []Entity{{Text: "John", Type: "person"}},
		SessionID:       "session-1",
		CreatedAt:       time.Now(),
		LastAccessedAt:  time.Now(),
	}
	vec := make([]float32, 768)
	vec[0] = 1.0

	// Insert
	if err := store.InsertDetail(dm, vec, "user1"); err != nil {
		t.Fatalf("InsertDetail: %v", err)
	}

	// Count
	count, err := store.GetDetailCount("user1")
	if err != nil {
		t.Fatalf("GetDetailCount: %v", err)
	}
	if count != 1 {
		t.Errorf("count = %d, want 1", count)
	}

	// Get all
	details, err := store.GetDetailMemories("user1")
	if err != nil {
		t.Fatalf("GetDetailMemories: %v", err)
	}
	if len(details) != 1 {
		t.Fatalf("got %d details, want 1", len(details))
	}
	if details[0].Text != dm.Text {
		t.Errorf("text = %q, want %q", details[0].Text, dm.Text)
	}
	if len(details[0].Entities) != 1 || details[0].Entities[0].Text != "John" {
		t.Errorf("entities mismatch: %+v", details[0].Entities)
	}

	// Touch
	if err := store.TouchDetail("test-1"); err != nil {
		t.Fatalf("TouchDetail: %v", err)
	}
	details, _ = store.GetDetailMemories("user1")
	if details[0].AccessCount != 1 {
		t.Errorf("access_count = %d, want 1", details[0].AccessCount)
	}

	// Update importance
	if err := store.UpdateDetailImportance("test-1", 0.5); err != nil {
		t.Fatalf("UpdateDetailImportance: %v", err)
	}
	details, _ = store.GetDetailMemories("user1")
	if details[0].ImportanceScore != 0.5 {
		t.Errorf("importance = %v, want 0.5", details[0].ImportanceScore)
	}

	// Delete
	if err := store.DeleteDetail("test-1"); err != nil {
		t.Fatalf("DeleteDetail: %v", err)
	}
	count, _ = store.GetDetailCount("user1")
	if count != 0 {
		t.Errorf("count after delete = %d, want 0", count)
	}
}

func TestStoreLowestImportanceDetail(t *testing.T) {
	store := newTestStore(t)

	vec := make([]float32, 768)
	for _, dm := range []*DetailMemory{
		{ID: "high", Text: "important", ImportanceScore: 0.9, Salience: 0.9},
		{ID: "low", Text: "less important", ImportanceScore: 0.3, Salience: 0.3},
		{ID: "mid", Text: "medium", ImportanceScore: 0.6, Salience: 0.6},
	} {
		store.InsertDetail(dm, vec, "user1")
	}

	lowest, err := store.GetLowestImportanceDetail("user1")
	if err != nil {
		t.Fatalf("GetLowestImportanceDetail: %v", err)
	}
	if lowest == nil {
		t.Fatal("expected lowest, got nil")
	}
	if lowest.ID != "low" {
		t.Errorf("lowest = %q, want 'low'", lowest.ID)
	}
}

func TestStoreSketchRaw(t *testing.T) {
	store := newTestStore(t)

	// Insert
	if err := store.InsertSketchRaw("user1", "chatted about weather", "episodic", "sess1", nil); err != nil {
		t.Fatalf("InsertSketchRaw: %v", err)
	}

	// Get unprocessed
	raw, err := store.GetUnprocessedRaw("user1", 10)
	if err != nil {
		t.Fatalf("GetUnprocessedRaw: %v", err)
	}
	if len(raw) != 1 {
		t.Fatalf("got %d raw, want 1", len(raw))
	}
	if raw[0].Content != "chatted about weather" {
		t.Errorf("content = %q", raw[0].Content)
	}

	// Mark processed
	if err := store.MarkRawProcessed([]string{raw[0].ID}); err != nil {
		t.Fatalf("MarkRawProcessed: %v", err)
	}

	raw, _ = store.GetUnprocessedRaw("user1", 10)
	if len(raw) != 0 {
		t.Errorf("after mark: got %d raw, want 0", len(raw))
	}
}

func TestStoreGetSketchRawByID(t *testing.T) {
	store := newTestStore(t)

	// Insert a raw entry
	vec := make([]float32, 768)
	vec[0] = 0.3
	if err := store.InsertSketchRaw("user1", "test raw content", "decision", "sess-1", vec); err != nil {
		t.Fatalf("InsertSketchRaw: %v", err)
	}

	// Find it via GetAllSketchRaw to get the ID
	raws, _ := store.GetAllSketchRaw("user1", 10)
	if len(raws) != 1 {
		t.Fatalf("expected 1 raw, got %d", len(raws))
	}
	id := raws[0].ID

	// Get by ID
	raw, err := store.GetSketchRawByID(id)
	if err != nil {
		t.Fatalf("GetSketchRawByID: %v", err)
	}
	if raw == nil {
		t.Fatal("expected raw, got nil")
	}
	if raw.Content != "test raw content" {
		t.Errorf("content = %q", raw.Content)
	}
	if raw.Sector != "decision" {
		t.Errorf("sector = %q, want 'decision'", raw.Sector)
	}
	if len(raw.Embedding) != 768 {
		t.Errorf("embedding dim = %d, want 768", len(raw.Embedding))
	}

	// Not found
	raw, err = store.GetSketchRawByID("nonexistent")
	if err != nil {
		t.Fatalf("GetSketchRawByID: %v", err)
	}
	if raw != nil {
		t.Error("expected nil for nonexistent ID")
	}
}

func TestStoreDeleteSketchRaw(t *testing.T) {
	store := newTestStore(t)

	store.InsertSketchRaw("user1", "to be deleted", "decision", "", nil)
	raws, _ := store.GetAllSketchRaw("user1", 10)
	if len(raws) != 1 {
		t.Fatalf("expected 1 raw, got %d", len(raws))
	}

	if err := store.DeleteSketchRaw(raws[0].ID); err != nil {
		t.Fatalf("DeleteSketchRaw: %v", err)
	}

	raws, _ = store.GetAllSketchRaw("user1", 10)
	if len(raws) != 0 {
		t.Errorf("expected 0 raws after delete, got %d", len(raws))
	}
}

func TestStoreGetEpisodeByID(t *testing.T) {
	store := newTestStore(t)

	vec := make([]float32, 768)
	vec[0] = 0.5
	ep := &Episode{
		ID:            "ep-lookup-1",
		SummaryText:   "Episode about testing lookups",
		Entities:      []Entity{{Text: "testing", Type: "topic"}},
		EmotionalTone: "neutral",
	}
	if err := store.InsertEpisode(ep, vec, "user1", "test-model", nil); err != nil {
		t.Fatalf("InsertEpisode: %v", err)
	}

	// Get by ID
	got, err := store.GetEpisodeByID("ep-lookup-1")
	if err != nil {
		t.Fatalf("GetEpisodeByID: %v", err)
	}
	if got == nil {
		t.Fatal("expected episode, got nil")
	}
	if got.SummaryText != "Episode about testing lookups" {
		t.Errorf("summary = %q", got.SummaryText)
	}
	if len(got.Vector) != 768 {
		t.Errorf("vector dim = %d, want 768", len(got.Vector))
	}

	// Not found
	got, err = store.GetEpisodeByID("nonexistent")
	if err != nil {
		t.Fatalf("GetEpisodeByID: %v", err)
	}
	if got != nil {
		t.Error("expected nil for nonexistent ID")
	}
}

func TestStoreGetAllSketchRaw(t *testing.T) {
	store := newTestStore(t)

	// Insert some, mark some processed
	store.InsertSketchRaw("user1", "raw 1", "decision", "", nil)
	store.InsertSketchRaw("user1", "raw 2", "warning", "", nil)

	raws, _ := store.GetUnprocessedRaw("user1", 10)
	if len(raws) >= 1 {
		store.MarkRawProcessed([]string{raws[0].ID})
	}

	// GetUnprocessedRaw should return 1
	unprocessed, _ := store.GetUnprocessedRaw("user1", 10)
	if len(unprocessed) != 1 {
		t.Errorf("unprocessed = %d, want 1", len(unprocessed))
	}

	// GetAllSketchRaw should return 2 (both processed and unprocessed)
	all, err := store.GetAllSketchRaw("user1", 10)
	if err != nil {
		t.Fatalf("GetAllSketchRaw: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("all = %d, want 2", len(all))
	}
}

func TestStoreEpisodes(t *testing.T) {
	store := newTestStore(t)

	vec := make([]float32, 768)
	vec[0] = 0.5

	ep := &Episode{
		ID:            "ep-1",
		SummaryText:   "Discussion about coffee preferences",
		Entities:      []Entity{{Text: "coffee", Type: "topic"}},
		EmotionalTone: "positive",
		CreatedAt:     time.Now(),
	}

	if err := store.InsertEpisode(ep, vec, "user1", "test-model", []string{"raw-1", "raw-2"}); err != nil {
		t.Fatalf("InsertEpisode: %v", err)
	}

	episodes, err := store.GetEpisodes("user1", nil)
	if err != nil {
		t.Fatalf("GetEpisodes: %v", err)
	}
	if len(episodes) != 1 {
		t.Fatalf("got %d episodes, want 1", len(episodes))
	}
	if episodes[0].SummaryText != ep.SummaryText {
		t.Errorf("summary = %q", episodes[0].SummaryText)
	}
}

func TestStoreProfileUpsert(t *testing.T) {
	store := newTestStore(t)

	vec := make([]float32, 64)
	profile := &ProfileSketch{
		UserID:            "user1",
		Interests:         []string{"music", "coffee"},
		PersonalityTraits: []string{"curious"},
		CommStyle:         "casual",
	}

	if err := store.UpsertProfile(profile, vec, "test-model", 42); err != nil {
		t.Fatalf("UpsertProfile: %v", err)
	}

	got, err := store.GetProfile("user1")
	if err != nil {
		t.Fatalf("GetProfile: %v", err)
	}
	if got == nil {
		t.Fatal("expected profile, got nil")
	}
	if got.CommStyle != "casual" {
		t.Errorf("comm_style = %q, want 'casual'", got.CommStyle)
	}
	if len(got.Interests) != 2 {
		t.Errorf("interests = %v, want 2 items", got.Interests)
	}

	// Upsert again
	profile.CommStyle = "formal"
	if err := store.UpsertProfile(profile, vec, "test-model", 42); err != nil {
		t.Fatalf("UpsertProfile (update): %v", err)
	}

	got, _ = store.GetProfile("user1")
	if got.CommStyle != "formal" {
		t.Errorf("updated comm_style = %q, want 'formal'", got.CommStyle)
	}
}

func TestStoreConfig(t *testing.T) {
	store := newTestStore(t)

	// Get non-existent
	val, err := store.GetConfigValue("projection_seed")
	if err != nil {
		t.Fatalf("GetConfigValue: %v", err)
	}
	if val != "" {
		t.Errorf("got %q, want empty string", val)
	}

	// Set
	if err := store.SetConfigValue("projection_seed", "12345"); err != nil {
		t.Fatalf("SetConfigValue: %v", err)
	}

	val, _ = store.GetConfigValue("projection_seed")
	if val != "12345" {
		t.Errorf("got %q, want '12345'", val)
	}

	// Update
	store.SetConfigValue("projection_seed", "67890")
	val, _ = store.GetConfigValue("projection_seed")
	if val != "67890" {
		t.Errorf("got %q, want '67890'", val)
	}
}

func TestGetUsersNeedingProfileUpdate(t *testing.T) {
	store := newTestStore(t)

	vec128 := make([]float32, 128)
	vec64 := make([]float32, 64)
	yesterday := time.Now().AddDate(0, 0, -1).Format("2006-01-02 15:04:05")

	// user1: has arcs, no profile → needs update
	store.InsertArc(&Arc{
		ID:          "arc-u1",
		SummaryText: "user1 arc",
		CreatedAt:   time.Now(),
	}, vec128, "user1", "test-model", 42)

	// user2: has stale profile + newer arc → needs update
	// Create profile, then backdate it, then add a fresh arc
	store.UpsertProfile(&ProfileSketch{
		UserID:    "user2",
		CommStyle: "old",
	}, vec64, "test-model", 42)
	store.db.Exec(`UPDATE profiles SET updated_at = ? WHERE user_id = 'user2'`, yesterday)
	store.InsertArc(&Arc{
		ID:          "arc-u2-new",
		SummaryText: "user2 newer arc",
		CreatedAt:   time.Now(),
	}, vec128, "user2", "test-model", 42)

	// user3: has arcs + fresh profile → does NOT need update
	// Insert arc first, backdate it, then create fresh profile
	store.InsertArc(&Arc{
		ID:          "arc-u3",
		SummaryText: "user3 arc",
		CreatedAt:   time.Now(),
	}, vec128, "user3", "test-model", 42)
	store.db.Exec(`UPDATE arcs SET created_at = ? WHERE user_id = 'user3'`, yesterday)
	store.UpsertProfile(&ProfileSketch{
		UserID:    "user3",
		CommStyle: "current",
	}, vec64, "test-model", 42)

	users, err := store.GetUsersNeedingProfileUpdate()
	if err != nil {
		t.Fatalf("GetUsersNeedingProfileUpdate: %v", err)
	}

	// Should return user1 and user2, not user3
	userSet := make(map[string]bool)
	for _, u := range users {
		userSet[u] = true
	}

	if !userSet["user1"] {
		t.Error("user1 should need profile update (no profile)")
	}
	if !userSet["user2"] {
		t.Error("user2 should need profile update (stale profile)")
	}
	if userSet["user3"] {
		t.Error("user3 should NOT need profile update (fresh profile)")
	}
	t.Logf("Users needing update: %v", users)
}
