package dualmem

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// TestSQLitePRAGMANotInDSN verifies that NewSQLiteStore does NOT use
// query params in the DSN. modernc.org/sqlite silently ignores DSN params,
// causing writes to go to an in-memory DB instead of disk.
//
// Regression: 2026-04-07 — writes appeared to succeed but never persisted.
func TestSQLitePRAGMANotInDSN(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}

	// Insert a test memory
	dm := &DetailMemory{
		ID:              "pragma-test-001",
		Text:            "PRAGMA regression test",
		Sector:          "semantic",
		Salience:        0.7,
		ImportanceScore: 0.7,
		Type:            "decision",
	}
	if err := store.InsertDetail(dm, make([]float32, 768), "test:pragma"); err != nil {
		t.Fatalf("InsertDetail: %v", err)
	}

	// Close the store
	store.Close()

	// Verify the file was actually written to (not in-memory)
	info, err := os.Stat(dbPath)
	if err != nil {
		t.Fatalf("DB file doesn't exist after Close: %v", err)
	}
	if info.Size() < 4096 {
		t.Fatalf("DB file suspiciously small (%d bytes), writes may have gone to in-memory DB", info.Size())
	}

	// Re-open with raw sqlite3 and verify the data persisted
	rawDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("raw sql.Open: %v", err)
	}
	defer rawDB.Close()

	var text string
	err = rawDB.QueryRow("SELECT text FROM detail_memories WHERE id = ?", "pragma-test-001").Scan(&text)
	if err != nil {
		t.Fatalf("Data not found in DB file after re-open: %v", err)
	}
	if text != "PRAGMA regression test" {
		t.Fatalf("Wrong text: got %q, want %q", text, "PRAGMA regression test")
	}
}

// TestSQLiteJournalMode verifies WAL or DELETE mode is active (not default).
func TestSQLiteJournalMode(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()

	var jm string
	if err := store.db.QueryRow("PRAGMA journal_mode").Scan(&jm); err != nil {
		t.Fatalf("PRAGMA journal_mode: %v", err)
	}

	// Accept WAL or DELETE — both work. Reject "" or "memory".
	if jm != "wal" && jm != "delete" {
		t.Fatalf("Unexpected journal_mode: %q (expected wal or delete)", jm)
	}
}

// TestSQLiteWritePersistsAcrossClose verifies that writes survive Close+reopen.
// This is the core persistence guarantee that the DSN bug violated.
func TestSQLiteWritePersistsAcrossClose(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "persist.db")

	// Write phase
	store1, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore (write): %v", err)
	}

	for i := 0; i < 10; i++ {
		dm := &DetailMemory{
			ID:              generateID(),
			Text:            "persist test memory",
			Sector:          "semantic",
			Salience:        0.7,
			ImportanceScore: 0.7,
			Type:            "decision",
		}
		if err := store1.InsertDetail(dm, make([]float32, 768), "test:persist"); err != nil {
			t.Fatalf("InsertDetail %d: %v", i, err)
		}
	}
	store1.Close()

	// Read phase (new store instance)
	store2, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore (read): %v", err)
	}
	defer store2.Close()

	memories, err := store2.GetDetailMemories("test:persist")
	if err != nil {
		t.Fatalf("GetDetailMemories: %v", err)
	}
	if len(memories) != 10 {
		t.Fatalf("Expected 10 memories after reopen, got %d", len(memories))
	}
}
