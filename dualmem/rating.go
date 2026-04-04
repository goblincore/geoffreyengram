package dualmem

import (
	"encoding/json"
	"fmt"
	"time"
)

// --- Snapshot types ---

// ContextSnapshot records the assembled context for a session, enabling
// retrospective rating by the consuming agent.
type ContextSnapshot struct {
	ID        string            `json:"id"`
	Namespace string            `json:"namespace"`
	Query     string            `json:"query"`
	Sources   []SnapshotSource  `json:"sources"`
	TokensUsed int              `json:"tokens_used"`
	CreatedAt time.Time         `json:"created_at"`
}

// SnapshotSource records a single item in the assembled context with its
// features at assembly time — these become re-ranker training features.
type SnapshotSource struct {
	ID         string  `json:"id"`
	Type       string  `json:"type"`       // "detail", "knowledge", "checkpoint", "codemap"
	CosineSim  float64 `json:"cosine_sim"`
	Importance float64 `json:"importance"`
	Salience   float64 `json:"salience"`
	Sector     string  `json:"sector"`
	MemType    string  `json:"mem_type"`   // "warning", "decision", "continuity", ""
	FileOverlap int    `json:"file_overlap"`
	AgeDays    float64 `json:"age_days"`
	TextLength int     `json:"text_length"`
}

// --- Rating types ---

// ContextRating is an agent-provided relevance rating for a single memory item.
type ContextRating struct {
	ID         string    `json:"id"`
	Namespace  string    `json:"namespace"`
	SnapshotID string    `json:"snapshot_id"`
	MemoryID   string    `json:"memory_id"`
	MemoryType string    `json:"memory_type"`
	Phase      string    `json:"phase"` // "early" or "late"
	Rating     int       `json:"rating"` // 0, 1, 2
	// Denormalized features (copied from snapshot at rating time)
	CosineSim  float64   `json:"cosine_sim"`
	Importance float64   `json:"importance"`
	Salience   float64   `json:"salience"`
	Sector     string    `json:"sector"`
	MemType    string    `json:"mem_type"`
	FileOverlap int      `json:"file_overlap"`
	AgeDays    float64   `json:"age_days"`
	TextLength int       `json:"text_length"`
	CreatedAt  time.Time `json:"created_at"`
}

// SessionRating is a holistic session-level quality score.
type SessionRating struct {
	ID          string    `json:"id"`
	Namespace   string    `json:"namespace"`
	SessionID   string    `json:"session_id"`
	Score       int       `json:"score"` // 1-5
	Explanation string    `json:"explanation"`
	QueryUsed   string    `json:"query_used"`
	TokensUsed  int       `json:"tokens_used"`
	CreatedAt   time.Time `json:"created_at"`
}

// RatingStats holds aggregate quality metrics.
type RatingStats struct {
	TotalRatings   int     `json:"total_ratings"`
	TotalSessions  int     `json:"total_sessions"`
	AvgPrecision   float64 `json:"avg_precision"`   // items rated 1-2 / total items
	AvgSessionScore float64 `json:"avg_session_score"`
	ByMemType      map[string]TypeStats `json:"by_mem_type"`
	RecentPrecision []float64           `json:"recent_precision"` // last N sessions
}

// TypeStats holds per-memory-type quality metrics.
type TypeStats struct {
	AvgRating    float64 `json:"avg_rating"`
	RelevantPct  float64 `json:"relevant_pct"` // % rated 1 or 2
	Count        int     `json:"count"`
}

// --- Snapshot persistence ---

// SaveSnapshot stores a context snapshot for later rating.
func (e *Engine) SaveSnapshot(snapshot *ContextSnapshot) error {
	sourcesJSON, err := json.Marshal(snapshot.Sources)
	if err != nil {
		return fmt.Errorf("marshal sources: %w", err)
	}
	return e.store.InsertSnapshot(snapshot.ID, snapshot.Namespace, snapshot.Query, nil, string(sourcesJSON), snapshot.TokensUsed)
}

// BuildSnapshot creates a ContextSnapshot from an assembled ContextBlock.
// Called internally by AssembleContextWith to persist the snapshot.
func BuildSnapshot(id, namespace, query string, block *ContextBlock, details []DetailMemory, now time.Time) *ContextSnapshot {
	var sources []SnapshotSource
	for _, src := range block.Sources {
		ss := SnapshotSource{
			ID:   src.ID,
			Type: src.Type,
		}
		// Enrich with detail memory features if available
		for _, dm := range details {
			if dm.ID == src.ID {
				ss.CosineSim = dm.Similarity
				ss.Importance = dm.ImportanceScore
				ss.Salience = dm.Salience
				ss.Sector = dm.Sector
				ss.MemType = dm.Type
				ss.FileOverlap = 0 // computed by caller if needed
				ss.AgeDays = now.Sub(dm.CreatedAt).Hours() / 24
				ss.TextLength = estimateTokens(dm.Text)
				break
			}
		}
		sources = append(sources, ss)
	}
	return &ContextSnapshot{
		ID:         id,
		Namespace:  namespace,
		Query:      query,
		Sources:    sources,
		TokensUsed: block.TokenCount,
		CreatedAt:  now,
	}
}

// --- Rating submission ---

// SubmitRatings stores a batch of item-level ratings for a snapshot.
func (e *Engine) SubmitRatings(namespace, snapshotID, phase string, ratings map[string]int) error {
	// Load the snapshot to get features
	snapshot, err := e.store.GetSnapshot(snapshotID)
	if err != nil {
		return fmt.Errorf("get snapshot %s: %w", snapshotID, err)
	}

	// Build a source lookup
	sourceMap := make(map[string]SnapshotSource)
	var sources []SnapshotSource
	if err := json.Unmarshal([]byte(snapshot.SourceIDsJSON), &sources); err != nil {
		return fmt.Errorf("unmarshal sources: %w", err)
	}
	for _, s := range sources {
		sourceMap[s.ID] = s
	}

	for memID, rating := range ratings {
		if rating < 0 || rating > 2 {
			continue
		}
		src := sourceMap[memID]
		cr := &ContextRating{
			ID:          generateID(),
			Namespace:   namespace,
			SnapshotID:  snapshotID,
			MemoryID:    memID,
			MemoryType:  src.Type,
			Phase:       phase,
			Rating:      rating,
			CosineSim:   src.CosineSim,
			Importance:  src.Importance,
			Salience:    src.Salience,
			Sector:      src.Sector,
			MemType:     src.MemType,
			FileOverlap: src.FileOverlap,
			AgeDays:     src.AgeDays,
			TextLength:  src.TextLength,
		}
		if err := e.store.InsertRating(cr); err != nil {
			return fmt.Errorf("insert rating for %s: %w", memID, err)
		}
	}
	return nil
}

// SubmitImplicitRatings records behavioral signals from progressive disclosure.
// Items the agent chose to fetch get rating=2 (highly relevant); items in the
// snapshot but not fetched get rating=0 (not relevant enough to spend tokens on).
// Phase is "implicit" with training weight 1.5 (between early=1.0 and late=2.0).
func (e *Engine) SubmitImplicitRatings(namespace, snapshotID string, fetchedIDs []string) error {
	snapshot, err := e.store.GetSnapshot(snapshotID)
	if err != nil {
		return fmt.Errorf("get snapshot %s: %w", snapshotID, err)
	}

	var sources []SnapshotSource
	if err := json.Unmarshal([]byte(snapshot.SourceIDsJSON), &sources); err != nil {
		return fmt.Errorf("unmarshal sources: %w", err)
	}

	fetched := make(map[string]bool, len(fetchedIDs))
	for _, id := range fetchedIDs {
		fetched[id] = true
	}

	for _, src := range sources {
		rating := 0
		if fetched[src.ID] {
			rating = 2
		}
		cr := &ContextRating{
			ID:          generateID(),
			Namespace:   namespace,
			SnapshotID:  snapshotID,
			MemoryID:    src.ID,
			MemoryType:  src.Type,
			Phase:       "implicit",
			Rating:      rating,
			CosineSim:   src.CosineSim,
			Importance:  src.Importance,
			Salience:    src.Salience,
			Sector:      src.Sector,
			MemType:     src.MemType,
			FileOverlap: src.FileOverlap,
			AgeDays:     src.AgeDays,
			TextLength:  src.TextLength,
		}
		if err := e.store.InsertRating(cr); err != nil {
			return fmt.Errorf("insert implicit rating for %s: %w", src.ID, err)
		}
	}
	return nil
}

// SubmitSessionRating stores a session-level quality score.
func (e *Engine) SubmitSessionRating(sr *SessionRating) error {
	return e.store.InsertSessionRating(sr)
}

// --- Stats ---

// GetRatingStats computes aggregate quality metrics from accumulated ratings.
func (e *Engine) GetRatingStats(namespace string) (*RatingStats, error) {
	return e.store.GetRatingStats(namespace)
}

// GetTrainingRows extracts labeled training data for the re-ranker.
// Late-phase ratings get weight 2.0, early-phase get 1.0.
// When both phases exist for the same (snapshot, memory), late takes precedence.
func (e *Engine) GetTrainingRows(namespace string) ([]TrainingRow, error) {
	ratings, err := e.store.GetAllRatings(namespace)
	if err != nil {
		return nil, err
	}

	// Deduplicate: late > implicit > early for same snapshot+memory pair.
	// Higher-priority phases override lower ones.
	type key struct {
		snapshotID string
		memoryID   string
	}
	phasePriority := map[string]int{"early": 0, "implicit": 1, "late": 2}
	best := make(map[key]*ContextRating)
	for i := range ratings {
		r := &ratings[i]
		k := key{r.SnapshotID, r.MemoryID}
		existing, ok := best[k]
		if !ok || phasePriority[r.Phase] > phasePriority[existing.Phase] {
			best[k] = r
		}
	}

	var rows []TrainingRow
	for _, r := range best {
		label := 0.0
		if r.Rating >= 1 {
			label = 1.0
		}
		weight := 1.0
		switch r.Phase {
		case "implicit":
			weight = 1.5
		case "late":
			weight = 2.0
		}

		sectorMatch := 0.0
		// Simplified: warnings and decisions in their matching sectors count
		if (r.MemType == "warning" && r.Sector == "warning") ||
			(r.MemType == "decision" && r.Sector == "decision") {
			sectorMatch = 1.0
		}

		features := NewRerankerFeatures(
			r.CosineSim, r.Importance, r.Salience,
			r.MemType, r.FileOverlap, r.AgeDays,
			r.TextLength, sectorMatch > 0,
		)
		rows = append(rows, TrainingRow{
			Features: features,
			Label:    label,
			Weight:   weight,
		})
	}
	return rows, nil
}

// --- Snapshot source feature computation ---

// computeFileOverlap counts how many files in the memory's file list
// appear in the query-context file hints (from checkpoints etc).
func computeFileOverlap(memFiles []string, queryFileHints map[string]bool) int {
	count := 0
	for _, f := range memFiles {
		if queryFileHints[f] {
			count++
		}
	}
	return count
}

// --- Helpers ---

// estimateTokens approximates token count (chars/4). Already exists in dualmem.go
// but kept as a note — we reuse the existing one.

// precisionFromRatings computes precision: items rated 1 or 2 / total items.
func precisionFromRatings(ratings []ContextRating) float64 {
	if len(ratings) == 0 {
		return 0
	}
	relevant := 0
	for _, r := range ratings {
		if r.Rating >= 1 {
			relevant++
		}
	}
	return float64(relevant) / float64(len(ratings))
}

// --- Store snapshot types (for SQLite layer) ---

// storedSnapshot is the raw row from context_snapshots.
type storedSnapshot struct {
	ID             string
	Namespace      string
	Query          string
	QueryEmbedding []byte
	SourceIDsJSON  string
	TokensUsed     int
	CreatedAt      string
}

// generateID is defined in store_sqlite.go — reused here.
