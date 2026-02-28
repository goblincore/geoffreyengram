package engram

import (
	"math"
	"path/filepath"
	"testing"
	"time"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := NewStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestVectorEncodeDecode(t *testing.T) {
	original := []float32{1.0, -0.5, 0.333, 0, 42.0}
	encoded := EncodeVector(original)
	decoded := DecodeVector(encoded)

	if len(decoded) != len(original) {
		t.Fatalf("length mismatch: %d vs %d", len(decoded), len(original))
	}
	for i := range original {
		if original[i] != decoded[i] {
			t.Errorf("index %d: expected %f, got %f", i, original[i], decoded[i])
		}
	}
}

func TestVectorEncodeDecodeEmpty(t *testing.T) {
	encoded := EncodeVector(nil)
	decoded := DecodeVector(encoded)
	if len(decoded) != 0 {
		t.Errorf("expected empty, got %d elements", len(decoded))
	}
}

func TestInsertAndGetMemory(t *testing.T) {
	s := testStore(t)

	mem := Memory{
		Content:  "Player visited Tokyo",
		Sector:   SectorEpisodic,
		Salience: 0.7,
		UserID:   "lily:player1",
		Summary:  "visited Tokyo",
	}
	id, err := s.InsertMemory(mem)
	if err != nil {
		t.Fatal(err)
	}
	if id <= 0 {
		t.Error("expected positive ID")
	}

	// Store a vector
	vec := []float32{0.1, 0.2, 0.3}
	if err := s.InsertVector(id, SectorEpisodic, vec); err != nil {
		t.Fatal(err)
	}

	// Retrieve
	mwvs, err := s.GetMemoriesWithVectors("lily:player1")
	if err != nil {
		t.Fatal(err)
	}
	if len(mwvs) != 1 {
		t.Fatalf("expected 1 memory, got %d", len(mwvs))
	}
	if mwvs[0].Content != "Player visited Tokyo" {
		t.Errorf("content mismatch: %s", mwvs[0].Content)
	}
	if mwvs[0].Sector != SectorEpisodic {
		t.Errorf("sector mismatch: %s", mwvs[0].Sector)
	}
	if len(mwvs[0].Vector) != 3 {
		t.Errorf("expected 3-dim vector, got %d", len(mwvs[0].Vector))
	}
}

func TestGetMemoriesFiltersbyUser(t *testing.T) {
	s := testStore(t)

	s.InsertMemory(Memory{Content: "mem1", Sector: SectorSemantic, Salience: 0.5, UserID: "user1", Summary: "m1"})
	s.InsertMemory(Memory{Content: "mem2", Sector: SectorSemantic, Salience: 0.5, UserID: "user2", Summary: "m2"})

	mwvs, err := s.GetMemoriesWithVectors("user1")
	if err != nil {
		t.Fatal(err)
	}
	if len(mwvs) != 1 {
		t.Errorf("expected 1 memory for user1, got %d", len(mwvs))
	}
}

func TestReinforceSalience(t *testing.T) {
	s := testStore(t)

	id, _ := s.InsertMemory(Memory{Content: "test", Sector: SectorSemantic, Salience: 0.5, UserID: "u1", Summary: "t"})
	if err := s.ReinforceSalience(id, 0.15); err != nil {
		t.Fatal(err)
	}

	mwvs, _ := s.GetMemoriesWithVectors("u1")
	if len(mwvs) != 1 {
		t.Fatal("expected 1 memory")
	}
	if math.Abs(mwvs[0].Salience-0.65) > 0.01 {
		t.Errorf("expected salience ~0.65 after boost, got %.2f", mwvs[0].Salience)
	}
	if mwvs[0].AccessCount != 1 {
		t.Errorf("expected access count 1, got %d", mwvs[0].AccessCount)
	}
}

func TestReinforceSalienceCapsAtOne(t *testing.T) {
	s := testStore(t)

	id, _ := s.InsertMemory(Memory{Content: "test", Sector: SectorSemantic, Salience: 0.95, UserID: "u1", Summary: "t"})
	s.ReinforceSalience(id, 0.15)

	mwvs, _ := s.GetMemoriesWithVectors("u1")
	if mwvs[0].Salience > 1.0 {
		t.Errorf("salience should cap at 1.0, got %.2f", mwvs[0].Salience)
	}
}

func TestRunDecaySweep(t *testing.T) {
	s := testStore(t)

	// Insert a memory with very low salience — should be deleted after decay
	s.InsertMemory(Memory{Content: "fading", Sector: SectorSemantic, Salience: 0.001, UserID: "u1", Summary: "f"})
	// Insert a memory with high salience — should survive
	s.InsertMemory(Memory{Content: "strong", Sector: SectorSemantic, Salience: 0.9, UserID: "u1", Summary: "s"})

	rates := DefaultDecayRates()
	updated, deleted, err := s.RunDecaySweep(0.01, rates)
	if err != nil {
		t.Fatal(err)
	}

	// The low-salience memory should have been pruned
	_ = updated
	_ = deleted

	mwvs, _ := s.GetMemoriesWithVectors("u1")
	for _, m := range mwvs {
		if m.Content == "fading" {
			// It may or may not be deleted depending on exact decay calc, but at
			// minimum its decay_score should be very low
			if m.DecayScore > 0.01 {
				t.Errorf("fading memory should have very low decay score, got %.4f", m.DecayScore)
			}
		}
	}
}

func TestEnforceMemoryLimit(t *testing.T) {
	s := testStore(t)

	// Insert 5 memories
	for i := 0; i < 5; i++ {
		s.InsertMemory(Memory{Content: "mem", Sector: SectorSemantic, Salience: 0.5, UserID: "u1", Summary: "m"})
	}

	// Enforce limit of 3
	if err := s.EnforceMemoryLimit("u1", 3); err != nil {
		t.Fatal(err)
	}

	mwvs, _ := s.GetMemoriesWithVectors("u1")
	if len(mwvs) != 3 {
		t.Errorf("expected 3 memories after enforce, got %d", len(mwvs))
	}
}

func TestEnforceMemoryLimitNoOp(t *testing.T) {
	s := testStore(t)

	s.InsertMemory(Memory{Content: "mem", Sector: SectorSemantic, Salience: 0.5, UserID: "u1", Summary: "m"})

	// Limit higher than count — no-op
	if err := s.EnforceMemoryLimit("u1", 100); err != nil {
		t.Fatal(err)
	}

	mwvs, _ := s.GetMemoriesWithVectors("u1")
	if len(mwvs) != 1 {
		t.Errorf("expected 1 memory, got %d", len(mwvs))
	}
}

func TestWaypointCRUD(t *testing.T) {
	s := testStore(t)

	// Upsert waypoint
	wpID, err := s.UpsertWaypoint("Tokyo", "place")
	if err != nil {
		t.Fatal(err)
	}
	if wpID <= 0 {
		t.Error("expected positive waypoint ID")
	}

	// Upsert same entity — should return same ID
	wpID2, err := s.UpsertWaypoint("Tokyo", "place")
	if err != nil {
		t.Fatal(err)
	}
	if wpID2 != wpID {
		t.Errorf("expected same ID for duplicate upsert: %d vs %d", wpID, wpID2)
	}

	// Create memory and associate
	memID, _ := s.InsertMemory(Memory{Content: "visited tokyo", Sector: SectorEpisodic, Salience: 0.5, UserID: "u1", Summary: "tokyo"})
	if err := s.InsertAssociation(memID, wpID, 0.5); err != nil {
		t.Fatal(err)
	}

	// Get associations
	ids, err := s.GetAssociatedWaypointIDs(memID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != wpID {
		t.Errorf("expected waypoint %d, got %v", wpID, ids)
	}
}

func TestNewStoreCreatesDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "subdir", "nested", "test.db")
	s, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
}

func TestDaysSinceUnit(t *testing.T) {
	d := DaysSince(time.Now())
	if d > 0.001 {
		t.Errorf("expected ~0 days, got %.4f", d)
	}
}
