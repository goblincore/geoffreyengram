package engram

import (
	"testing"
	"time"
)

func TestSessionChaining(t *testing.T) {
	s := testStore(t)

	// Insert 3 memories in the same session with parent chain
	id1, _ := s.InsertMemory(Memory{
		Content: "hello", Sector: SectorEpisodic, Salience: 0.5,
		UserID: "u1", Summary: "hello", SessionID: "sess-abc", ParentID: 0,
	})
	id2, _ := s.InsertMemory(Memory{
		Content: "how are you", Sector: SectorEpisodic, Salience: 0.5,
		UserID: "u1", Summary: "how are you", SessionID: "sess-abc", ParentID: id1,
	})
	s.InsertMemory(Memory{
		Content: "goodbye", Sector: SectorEpisodic, Salience: 0.5,
		UserID: "u1", Summary: "goodbye", SessionID: "sess-abc", ParentID: id2,
	})

	// Also insert a memory in a different session
	s.InsertMemory(Memory{
		Content: "other session", Sector: SectorSemantic, Salience: 0.5,
		UserID: "u1", Summary: "other", SessionID: "sess-xyz", ParentID: 0,
	})

	mems, err := s.GetSessionMemories("sess-abc")
	if err != nil {
		t.Fatal(err)
	}
	if len(mems) != 3 {
		t.Fatalf("expected 3 memories in session, got %d", len(mems))
	}
	if mems[0].Content != "hello" {
		t.Errorf("expected first memory 'hello', got '%s'", mems[0].Content)
	}
	if mems[2].ParentID != id2 {
		t.Errorf("expected parent_id %d, got %d", id2, mems[2].ParentID)
	}
}

func TestGetLastSessionID(t *testing.T) {
	s := testStore(t)

	// Insert memories with controlled timestamps to ensure ordering
	s.db.Exec(`INSERT INTO memories (content, sector, salience, decay_score, summary, user_id, created_at, session_id, parent_id)
		VALUES ('old', 'semantic', 0.5, 0.5, 'old', 'u1', '2024-01-01 12:00:00', 'sess-1', 0)`)
	s.db.Exec(`INSERT INTO memories (content, sector, salience, decay_score, summary, user_id, created_at, session_id, parent_id)
		VALUES ('new', 'semantic', 0.5, 0.5, 'new', 'u1', '2024-06-01 12:00:00', 'sess-2', 0)`)

	sessionID, err := s.GetLastSessionID("u1")
	if err != nil {
		t.Fatal(err)
	}
	if sessionID != "sess-2" {
		t.Errorf("expected sess-2, got %s", sessionID)
	}
}

func TestGetLastSessionIDEmpty(t *testing.T) {
	s := testStore(t)

	// Memory without session_id
	s.InsertMemory(Memory{Content: "no session", Sector: SectorSemantic, Salience: 0.5, UserID: "u1", Summary: "ns"})

	sessionID, err := s.GetLastSessionID("u1")
	if err != nil {
		t.Fatal(err)
	}
	if sessionID != "" {
		t.Errorf("expected empty session ID, got %s", sessionID)
	}
}

func TestGetRecentMemories(t *testing.T) {
	s := testStore(t)

	s.InsertMemory(Memory{Content: "a", Sector: SectorEpisodic, Salience: 0.5, UserID: "u1", Summary: "a"})
	s.InsertMemory(Memory{Content: "b", Sector: SectorSemantic, Salience: 0.5, UserID: "u1", Summary: "b"})
	s.InsertMemory(Memory{Content: "c", Sector: SectorEmotional, Salience: 0.5, UserID: "u1", Summary: "c"})

	// Get 2 most recent
	mems, err := s.GetRecentMemories("u1", 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(mems) != 2 {
		t.Fatalf("expected 2 recent memories, got %d", len(mems))
	}
}

func TestGetRecentMemoriesFilterBySector(t *testing.T) {
	s := testStore(t)

	s.InsertMemory(Memory{Content: "epi", Sector: SectorEpisodic, Salience: 0.5, UserID: "u1", Summary: "e"})
	s.InsertMemory(Memory{Content: "sem", Sector: SectorSemantic, Salience: 0.5, UserID: "u1", Summary: "s"})
	s.InsertMemory(Memory{Content: "emo", Sector: SectorEmotional, Salience: 0.5, UserID: "u1", Summary: "m"})

	mems, err := s.GetRecentMemories("u1", 10, []Sector{SectorEpisodic, SectorEmotional})
	if err != nil {
		t.Fatal(err)
	}
	if len(mems) != 2 {
		t.Fatalf("expected 2 memories (episodic+emotional), got %d", len(mems))
	}
	for _, m := range mems {
		if m.Sector != SectorEpisodic && m.Sector != SectorEmotional {
			t.Errorf("unexpected sector %s", m.Sector)
		}
	}
}

func TestGetMemoriesInTimeWindow(t *testing.T) {
	s := testStore(t)

	// Insert memories and control created_at via direct SQL
	s.db.Exec(`INSERT INTO memories (content, sector, salience, decay_score, summary, user_id, created_at, session_id, parent_id)
		VALUES ('old', 'semantic', 0.5, 0.5, 'old', 'u1', '2024-01-01 12:00:00', '', 0)`)
	s.db.Exec(`INSERT INTO memories (content, sector, salience, decay_score, summary, user_id, created_at, session_id, parent_id)
		VALUES ('recent', 'semantic', 0.5, 0.5, 'recent', 'u1', '2024-06-15 12:00:00', '', 0)`)
	s.db.Exec(`INSERT INTO memories (content, sector, salience, decay_score, summary, user_id, created_at, session_id, parent_id)
		VALUES ('future', 'semantic', 0.5, 0.5, 'future', 'u1', '2025-01-01 12:00:00', '', 0)`)

	after := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)
	before := time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC)

	mems, err := s.GetMemoriesInTimeWindow("u1", after, before)
	if err != nil {
		t.Fatal(err)
	}
	if len(mems) != 1 {
		t.Fatalf("expected 1 memory in window, got %d", len(mems))
	}
	if mems[0].Content != "recent" {
		t.Errorf("expected 'recent', got '%s'", mems[0].Content)
	}
}

func TestGetActiveUserIDs(t *testing.T) {
	s := testStore(t)

	s.InsertMemory(Memory{Content: "a", Sector: SectorSemantic, Salience: 0.5, UserID: "user-a", Summary: "a"})
	s.InsertMemory(Memory{Content: "b", Sector: SectorSemantic, Salience: 0.5, UserID: "user-b", Summary: "b"})
	s.InsertMemory(Memory{Content: "c", Sector: SectorSemantic, Salience: 0.5, UserID: "user-a", Summary: "c"})

	ids, err := s.GetActiveUserIDs()
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 {
		t.Errorf("expected 2 distinct user IDs, got %d", len(ids))
	}
}

func TestMemoryWithVectorsIncludesTemporalFields(t *testing.T) {
	s := testStore(t)

	s.InsertMemory(Memory{
		Content: "temporal", Sector: SectorEpisodic, Salience: 0.5,
		UserID: "u1", Summary: "t", SessionID: "sess-99", ParentID: 42,
	})

	mwvs, err := s.GetMemoriesWithVectors("u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(mwvs) != 1 {
		t.Fatal("expected 1 memory")
	}
	if mwvs[0].SessionID != "sess-99" {
		t.Errorf("expected session_id 'sess-99', got '%s'", mwvs[0].SessionID)
	}
	if mwvs[0].ParentID != 42 {
		t.Errorf("expected parent_id 42, got %d", mwvs[0].ParentID)
	}
}
