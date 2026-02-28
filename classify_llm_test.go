package engram

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// geminiClassifyResponse builds a mock Gemini response for classification.
func geminiClassifyResponse(sector string) string {
	resp := map[string]any{
		"candidates": []map[string]any{
			{
				"content": map[string]any{
					"parts": []map[string]any{
						{"text": sector},
					},
				},
			},
		},
	}
	b, _ := json.Marshal(resp)
	return string(b)
}

func TestLLMClassifier_ClassifyReturnsHeuristic(t *testing.T) {
	// Classify should return the heuristic result immediately, no LLM call
	store := testStoreForClassify(t)
	lc := NewLLMClassifier("test-key", store)
	defer lc.Close()

	// Content with clear emotional signals
	sector := lc.Classify("I feel happy and excited about this")
	if sector != SectorEmotional {
		t.Errorf("expected emotional, got %s", sector)
	}

	// Content with procedural signals
	sector = lc.Classify("the technique and method to do this step by step")
	if sector != SectorProcedural {
		t.Errorf("expected procedural, got %s", sector)
	}
}

func TestLLMClassifier_ReclassifiesViaMockGemini(t *testing.T) {
	store := testStoreForClassify(t)

	// Insert a memory that will be reclassified
	mem := Memory{
		Content:  "I just got back from Tokyo",
		Sector:   SectorSemantic, // heuristic default
		Salience: 0.5,
		UserID:   "test:user",
		Summary:  "test summary",
	}
	memID, err := store.InsertMemory(mem)
	if err != nil {
		t.Fatalf("insert memory: %v", err)
	}
	if err := store.InsertVector(memID, SectorSemantic, make([]float32, 3)); err != nil {
		t.Fatalf("insert vector: %v", err)
	}

	// Mock Gemini server that always returns "episodic"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(geminiClassifyResponse("episodic")))
	}))
	defer server.Close()

	lc := NewLLMClassifier("test-key", store)
	lc.baseURL = server.URL
	defer lc.Close()

	// Submit for reclassification
	lc.SubmitForReclassification(memID, "I just got back from Tokyo")

	// Wait for the async worker to process
	time.Sleep(500 * time.Millisecond)

	// Verify the sector was updated in the DB
	mems, err := store.GetMemoriesWithVectors("test:user")
	if err != nil {
		t.Fatalf("get memories: %v", err)
	}
	if len(mems) == 0 {
		t.Fatal("no memories found")
	}
	if mems[0].Sector != SectorEpisodic {
		t.Errorf("expected sector to be reclassified to episodic, got %s", mems[0].Sector)
	}
}

func TestLLMClassifier_NoUpdateWhenSectorMatches(t *testing.T) {
	store := testStoreForClassify(t)

	// Insert a memory with emotional sector
	mem := Memory{
		Content:  "I feel happy and grateful",
		Sector:   SectorEmotional,
		Salience: 0.5,
		UserID:   "test:user",
		Summary:  "test summary",
	}
	memID, err := store.InsertMemory(mem)
	if err != nil {
		t.Fatalf("insert memory: %v", err)
	}

	// Mock Gemini that returns "emotional" (same as heuristic)
	var callCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(geminiClassifyResponse("emotional")))
	}))
	defer server.Close()

	lc := NewLLMClassifier("test-key", store)
	lc.baseURL = server.URL
	defer lc.Close()

	lc.SubmitForReclassification(memID, "I feel happy and grateful")
	time.Sleep(500 * time.Millisecond)

	// LLM was called but no DB update should happen (sectors match)
	if callCount.Load() == 0 {
		t.Error("expected LLM to be called")
	}

	// Sector should still be emotional
	mems, err := store.GetMemoriesWithVectors("test:user")
	if err != nil {
		t.Fatalf("get memories: %v", err)
	}
	if mems[0].Sector != SectorEmotional {
		t.Errorf("expected sector to remain emotional, got %s", mems[0].Sector)
	}
}

func TestLLMClassifier_ChannelDropWhenFull(t *testing.T) {
	store := testStoreForClassify(t)

	// Slow mock server to block the worker on the first request
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Second)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(geminiClassifyResponse("semantic")))
	}))
	defer server.Close()

	lc := NewLLMClassifier("test-key", store)
	lc.baseURL = server.URL
	// Note: we intentionally do NOT defer lc.Close() here because the worker
	// is blocked on the slow server and would take too long to drain.

	// Fill the buffer + overflow — should not block or panic.
	// The worker is stuck on the first request, so the channel fills up
	// and subsequent sends should be silently dropped.
	done := make(chan struct{})
	go func() {
		for i := 0; i < reclassBufferSize+10; i++ {
			lc.SubmitForReclassification(int64(i+1), "test content")
		}
		close(done)
	}()

	select {
	case <-done:
		// good — all sends completed without blocking
	case <-time.After(2 * time.Second):
		t.Fatal("SubmitForReclassification blocked when channel was full")
	}
}

func TestLLMClassifier_CloseGraceful(t *testing.T) {
	store := testStoreForClassify(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(geminiClassifyResponse("semantic")))
	}))
	defer server.Close()

	lc := NewLLMClassifier("test-key", store)
	lc.baseURL = server.URL

	// Submit a few items
	lc.SubmitForReclassification(1, "test content")
	lc.SubmitForReclassification(2, "test content 2")

	// Close should drain and return without hanging
	done := make(chan struct{})
	go func() {
		lc.Close()
		close(done)
	}()

	select {
	case <-done:
		// good
	case <-time.After(5 * time.Second):
		t.Fatal("Close() timed out — worker did not drain")
	}
}

func TestLLMClassifier_LLMErrorFallsBack(t *testing.T) {
	store := testStoreForClassify(t)

	// Insert a memory
	mem := Memory{
		Content:  "I just got back from Tokyo",
		Sector:   SectorSemantic,
		Salience: 0.5,
		UserID:   "test:user",
		Summary:  "test summary",
	}
	memID, err := store.InsertMemory(mem)
	if err != nil {
		t.Fatalf("insert memory: %v", err)
	}

	// Mock server that returns 500
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal error"))
	}))
	defer server.Close()

	lc := NewLLMClassifier("test-key", store)
	lc.baseURL = server.URL
	defer lc.Close()

	lc.SubmitForReclassification(memID, "I just got back from Tokyo")
	time.Sleep(500 * time.Millisecond)

	// Sector should remain unchanged (LLM failed, no update)
	mems, err := store.GetMemoriesWithVectors("test:user")
	if err != nil {
		t.Fatalf("get memories: %v", err)
	}
	if mems[0].Sector != SectorSemantic {
		t.Errorf("expected sector to remain semantic after LLM error, got %s", mems[0].Sector)
	}
}

func TestUpdateMemorySector(t *testing.T) {
	store := testStoreForClassify(t)

	// Insert memory + vector
	mem := Memory{
		Content:  "test content",
		Sector:   SectorSemantic,
		Salience: 0.5,
		UserID:   "test:user",
		Summary:  "test",
	}
	memID, err := store.InsertMemory(mem)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := store.InsertVector(memID, SectorSemantic, make([]float32, 3)); err != nil {
		t.Fatalf("insert vector: %v", err)
	}

	// Update sector
	if err := store.UpdateMemorySector(memID, SectorEpisodic); err != nil {
		t.Fatalf("update sector: %v", err)
	}

	// Verify memory table updated
	mems, err := store.GetMemoriesWithVectors("test:user")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(mems) != 1 {
		t.Fatalf("expected 1 memory, got %d", len(mems))
	}
	if mems[0].Sector != SectorEpisodic {
		t.Errorf("expected episodic, got %s", mems[0].Sector)
	}

	// Verify vector table updated
	var vecSector string
	err = store.db.QueryRow(`SELECT sector FROM vectors WHERE memory_id = ?`, memID).Scan(&vecSector)
	if err != nil {
		t.Fatalf("query vector sector: %v", err)
	}
	if Sector(vecSector) != SectorEpisodic {
		t.Errorf("vector sector expected episodic, got %s", vecSector)
	}
}

// testStore creates a temporary SQLite store for testing.
func testStoreForClassify(t *testing.T) *Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	// Ensure parent dir exists
	os.MkdirAll(filepath.Dir(dbPath), 0755)
	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}
