package dualmem

import (
	"context"
	"sort"
	"strings"
	"unicode"
)

// DetailPath manages the high-fidelity Top-K memory store.
type DetailPath struct {
	store      Store
	scorer     *ImportanceScorer
	embedder   EmbeddingProvider
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
	// Exclude seeds from organic cap — seeds have their own capacity limit
	count, err := dp.store.GetDetailCountExcludingSeeds(userID)
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

// extractIdentifiers returns tokens from the query that contain both letters and digits.
// These are specific identifiers like "LC-1663", "PR-42", "v2.1" that benefit from
// exact substring matching rather than semantic similarity.
func extractIdentifiers(query string) []string {
	var identifiers []string
	for _, word := range strings.Fields(query) {
		// Strip common punctuation
		word = strings.Trim(word, ".,;:!?\"'()[]")
		if len(word) < 2 {
			continue
		}
		hasLetter := false
		hasDigit := false
		for _, r := range word {
			if unicode.IsLetter(r) {
				hasLetter = true
			}
			if unicode.IsDigit(r) {
				hasDigit = true
			}
		}
		if hasLetter && hasDigit {
			identifiers = append(identifiers, word)
		}
	}
	return identifiers
}

// keywordMatchScore scores how well a memory text matches the query using keyword overlap.
// Returns a score in [0, 2.0]:
//   - Each query token found in the text contributes up to 1.0 total (proportional)
//   - Each identifier (mixed letter+digit token) found as a case-insensitive substring adds 1.0 bonus
//
// Reuses hdcTokenize for consistent tokenization.
func keywordMatchScore(query, text string) float64 {
	if query == "" || text == "" {
		return 0
	}
	textLower := strings.ToLower(text)

	// Token overlap score: fraction of query tokens found in text
	tokens := hdcTokenize(query)
	tokenScore := 0.0
	if len(tokens) > 0 {
		matches := 0
		for _, tok := range tokens {
			if strings.Contains(textLower, tok) {
				matches++
			}
		}
		tokenScore = float64(matches) / float64(len(tokens))
	}

	// Identifier bonus: exact substring match for mixed letter+digit tokens
	identifierBonus := 0.0
	identifiers := extractIdentifiers(query)
	for _, id := range identifiers {
		if strings.Contains(textLower, strings.ToLower(id)) {
			identifierBonus += 1.0
			break // one identifier match is enough for the full bonus
		}
	}

	return tokenScore + identifierBonus
}

// Search performs hybrid cosine+keyword similarity search against the Detail Path.
// When queryText is non-empty, blends cosine similarity with keyword match scores
// using AdaptiveAlpha (same pattern as code search). When queryText is empty,
// falls back to pure cosine similarity.
// Returns top-M results sorted by hybrid score, plus high-salience guarantees.
func (dp *DetailPath) Search(ctx context.Context, queryEmbedding []float32, userID string, limit int, queryText string) ([]DetailMemory, error) {
	details, err := dp.store.GetDetailMemories(userID)
	if err != nil {
		return nil, err
	}
	if len(details) == 0 {
		return nil, nil
	}

	type scored struct {
		detailWithVector
		similarity   float64
		hybridScore  float64
	}

	var results []scored
	for _, d := range details {
		sim := CosineSimilarity(queryEmbedding, d.Vector)
		results = append(results, scored{d, sim, sim})
	}

	// Hybrid scoring: blend cosine with keyword match when queryText is provided
	if queryText != "" {
		// Compute keyword scores for all memories
		kwScores := make([]float64, len(results))
		maxKW := 0.0
		for i, r := range results {
			kw := keywordMatchScore(queryText, r.Text)
			kwScores[i] = kw
			if kw > maxKW {
				maxKW = kw
			}
		}

		// Compute cosine score spread for AdaptiveAlpha
		cosScores := make([]float64, len(results))
		for i, r := range results {
			cosScores[i] = r.similarity
		}
		sort.Float64s(cosScores) // ascending
		// Reverse to descending for AdaptiveAlpha
		for i, j := 0, len(cosScores)-1; i < j; i, j = i+1, j-1 {
			cosScores[i], cosScores[j] = cosScores[j], cosScores[i]
		}
		alpha := AdaptiveAlpha(cosScores)

		// Apply hybrid blend
		for i := range results {
			// Normalize cosine to [0,1]: cosine similarity is in [-1,1]
			cosNorm := (results[i].similarity + 1.0) / 2.0
			kwNorm := 0.0
			if maxKW > 0 {
				kwNorm = kwScores[i] / maxKW
			}
			results[i].hybridScore = alpha*cosNorm + (1.0-alpha)*kwNorm
		}
	}

	// Sort by hybrid score descending
	sort.Slice(results, func(i, j int) bool {
		return results[i].hybridScore > results[j].hybridScore
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
	// even if they weren't in top-M by hybrid score.
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
