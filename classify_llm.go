package engram

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

// LLMClassifier provides synchronous heuristic classification with async LLM
// reclassification. On Classify(), the heuristic result is returned immediately
// (zero latency). After a memory is stored, SubmitForReclassification sends
// the memory to a background worker that calls Gemini for a more accurate
// sector classification and updates the DB if different.
type LLMClassifier struct {
	heuristic *HeuristicClassifier
	apiKey    string
	baseURL   string // Gemini API base URL (overridable for tests)
	client    *http.Client
	store     *Store
	reclassCh chan reclassRequest
	done      chan struct{}
}

type reclassRequest struct {
	memoryID int64
	content  string
}

const (
	reclassBufferSize = 64                    // max pending reclassifications
	reclassTimeout    = 10 * time.Second      // per-request timeout
	reclassDelay      = 200 * time.Millisecond // delay between requests (rate limit)
)

// NewLLMClassifier creates a classifier that uses heuristics synchronously
// and LLM reclassification asynchronously. The background worker starts
// immediately and runs until Close() is called.
func NewLLMClassifier(apiKey string, store *Store) *LLMClassifier {
	lc := &LLMClassifier{
		heuristic: NewHeuristicClassifier(""), // no API key — pure heuristic, no fallback
		apiKey:    apiKey,
		baseURL:   "https://generativelanguage.googleapis.com/v1beta/models/gemini-2.5-flash-lite:generateContent",
		client:    &http.Client{Timeout: reclassTimeout},
		store:     store,
		reclassCh: make(chan reclassRequest, reclassBufferSize),
		done:      make(chan struct{}),
	}
	go lc.worker()
	return lc
}

// Classify returns the heuristic sector immediately. This satisfies the
// SectorClassifier interface and adds zero latency to the Add path.
func (lc *LLMClassifier) Classify(content string) Sector {
	sector, _ := lc.heuristic.heuristicClassify(content)
	return sector
}

// SubmitForReclassification queues a memory for async LLM reclassification.
// Non-blocking: if the buffer is full, the request is dropped silently.
func (lc *LLMClassifier) SubmitForReclassification(memoryID int64, content string) {
	select {
	case lc.reclassCh <- reclassRequest{memoryID: memoryID, content: content}:
	default:
		// Channel full — drop this reclassification. The heuristic sector
		// is kept, which is acceptable. This prevents unbounded memory growth.
	}
}

// Close stops the background worker and waits for pending reclassifications
// to drain (up to any already in the buffer).
func (lc *LLMClassifier) Close() {
	close(lc.reclassCh)
	<-lc.done
}

// worker processes reclassification requests from the channel.
func (lc *LLMClassifier) worker() {
	defer close(lc.done)

	for req := range lc.reclassCh {
		lc.reclassify(req)
		time.Sleep(reclassDelay)
	}
}

// reclassify calls Gemini to classify the content and updates the DB if
// the LLM sector differs from the heuristic sector.
func (lc *LLMClassifier) reclassify(req reclassRequest) {
	llmSector, err := lc.llmClassify(req.content)
	if err != nil {
		log.Printf("[engram] LLM reclassify failed for memory #%d: %v", req.memoryID, err)
		return
	}

	// Only update if LLM disagrees with the heuristic
	heuristicSector, _ := lc.heuristic.heuristicClassify(req.content)
	if llmSector == heuristicSector {
		return
	}

	if err := lc.store.UpdateMemorySector(req.memoryID, llmSector); err != nil {
		log.Printf("[engram] Update sector failed for memory #%d: %v", req.memoryID, err)
		return
	}

	log.Printf("[engram] Reclassified memory #%d: %s → %s", req.memoryID, heuristicSector, llmSector)
}

// llmClassify calls Gemini to classify content into a sector.
func (lc *LLMClassifier) llmClassify(content string) (Sector, error) {
	url := lc.baseURL + "?key=" + lc.apiKey

	prompt := `Classify this memory into exactly one cognitive sector. Reply with ONLY the sector name, nothing else.

Sectors:
- episodic: specific events, experiences, things that happened at a particular time
- semantic: facts, knowledge, preferences, stable truths about someone
- procedural: skills, techniques, how-to knowledge, learned methods
- emotional: feelings, sentiments, emotional reactions, moods
- reflective: patterns, meta-observations, insights connecting multiple experiences

Memory: "` + content + `"`

	reqBody := map[string]any{
		"contents": []map[string]any{
			{"role": "user", "parts": []map[string]any{{"text": prompt}}},
		},
		"generationConfig": map[string]any{
			"maxOutputTokens": 10,
			"temperature":     0.0,
		},
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return SectorSemantic, err
	}

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return SectorSemantic, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := lc.client.Do(req)
	if err != nil {
		return SectorSemantic, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return SectorSemantic, &classifyError{status: resp.StatusCode, body: string(body[:min(len(body), 300)])}
	}

	var geminiResp struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&geminiResp); err != nil {
		return SectorSemantic, err
	}

	if len(geminiResp.Candidates) == 0 || len(geminiResp.Candidates[0].Content.Parts) == 0 {
		return SectorSemantic, &classifyError{body: "empty response"}
	}

	text := strings.TrimSpace(strings.ToLower(geminiResp.Candidates[0].Content.Parts[0].Text))
	switch {
	case strings.Contains(text, "episodic"):
		return SectorEpisodic, nil
	case strings.Contains(text, "semantic"):
		return SectorSemantic, nil
	case strings.Contains(text, "procedural"):
		return SectorProcedural, nil
	case strings.Contains(text, "emotional"):
		return SectorEmotional, nil
	case strings.Contains(text, "reflective"):
		return SectorReflective, nil
	default:
		return SectorSemantic, nil
	}
}
