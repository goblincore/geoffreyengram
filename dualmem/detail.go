package dualmem

import (
	"context"
	"sort"
)

// DetailPath manages the high-fidelity Top-K memory store.
type DetailPath struct {
	store     Store
	scorer    *ImportanceScorer
	embedder  EmbeddingProvider
	maxPerUser int
}

// NewDetailPath creates a Detail Path manager.
// detailBias lists sector names that get a scoring bonus toward the Detail Path.
func NewDetailPath(store Store, embedder EmbeddingProvider, theta float64, maxPerUser int, detailBias []string) *DetailPath {
	biasSet := make(map[string]bool, len(detailBias))
	for _, s := range detailBias {
		biasSet[s] = true
	}
	return &DetailPath{
		store:      store,
		scorer:     &ImportanceScorer{Theta: theta, DetailBias: biasSet},
		embedder:   embedder,
		maxPerUser: maxPerUser,
	}
}

// ScoreAndRoute computes importance and returns (score, isDetail).
// existingDetails should be the user's current Detail memories for novelty calculation.
func (dp *DetailPath) ScoreAndRoute(sector string, salience float64, content string, embedding []float32, existingDetails []detailWithVector, memType string) (float64, bool) {
	maxSim := 0.0
	for _, d := range existingDetails {
		sim := CosineSimilarity(embedding, d.Vector)
		if sim > maxSim {
			maxSim = sim
		}
	}

	score := dp.scorer.Score(sector, salience, content, maxSim, memType)
	return score, dp.scorer.IsDetail(score)
}

// Insert adds a memory to the Detail Path, handling capacity management.
// If the user is at capacity, demotes the lowest-importance memory.
// Returns the demoted memory text (for sketch ingestion) or empty string.
func (dp *DetailPath) Insert(ctx context.Context, dm *DetailMemory, embedding []float32, userID string) (demotedText string, err error) {
	count, err := dp.store.GetDetailCount(userID)
	if err != nil {
		return "", err
	}

	// Capacity management: demote lowest if at cap
	if count >= dp.maxPerUser {
		lowest, err := dp.store.GetLowestImportanceDetail(userID)
		if err != nil {
			return "", err
		}
		if lowest != nil && lowest.ImportanceScore < dm.ImportanceScore {
			demotedText = lowest.Text
			if err := dp.store.DeleteDetail(lowest.ID); err != nil {
				return "", err
			}
		} else {
			// New memory isn't important enough; route to sketch instead
			return dm.Text, nil
		}
	}

	return demotedText, dp.store.InsertDetail(dm, embedding, userID)
}

// Search performs cosine similarity search against the Detail Path.
// Returns top-M results sorted by similarity, plus high-salience guarantees.
func (dp *DetailPath) Search(ctx context.Context, queryEmbedding []float32, userID string, limit int) ([]DetailMemory, error) {
	details, err := dp.store.GetDetailMemories(userID)
	if err != nil {
		return nil, err
	}
	if len(details) == 0 {
		return nil, nil
	}

	// Score all by cosine similarity
	type scored struct {
		detailWithVector
		similarity float64
	}
	var results []scored
	for _, d := range details {
		sim := CosineSimilarity(queryEmbedding, d.Vector)
		results = append(results, scored{d, sim})
	}

	// Sort by similarity descending
	sort.Slice(results, func(i, j int) bool {
		return results[i].similarity > results[j].similarity
	})

	// Take top-M
	if limit <= 0 {
		limit = 5
	}
	var topM []DetailMemory
	seen := make(map[string]bool)
	for i, r := range results {
		if i >= limit {
			break
		}
		dm := r.DetailMemory
		dm.Similarity = r.similarity
		topM = append(topM, dm)
		seen[dm.ID] = true

		// Touch for access tracking
		dp.store.TouchDetail(dm.ID)
	}

	// High-salience guarantee: inject up to 2 high-salience memories
	// even if they weren't in top-M by similarity.
	highSalCount := 0
	for _, r := range results {
		if highSalCount >= 2 {
			break
		}
		if seen[r.ID] {
			continue
		}
		if r.Salience >= 0.6 {
			dm := r.DetailMemory
			dm.Similarity = r.similarity
			topM = append(topM, dm)
			highSalCount++
		}
	}

	return topM, nil
}
