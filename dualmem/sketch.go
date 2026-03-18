package dualmem

import (
	"context"
	"sort"
	"time"
)

// SketchPath manages the hierarchical compression layers.
type SketchPath struct {
	store     Store
	projector *Projector
	embedder  EmbeddingProvider
}

// NewSketchPath creates a Sketch Path manager.
func NewSketchPath(store Store, projector *Projector, embedder EmbeddingProvider) *SketchPath {
	return &SketchPath{
		store:     store,
		projector: projector,
		embedder:  embedder,
	}
}

// IngestRaw stores a raw memory for later episode summarization.
func (sp *SketchPath) IngestRaw(ctx context.Context, userID, content, sector, sessionID string, embedding []float32) error {
	return sp.store.InsertSketchRaw(userID, content, sector, sessionID, embedding)
}

// SearchEpisodes searches recent episodes by cosine similarity (full 768d).
func (sp *SketchPath) SearchEpisodes(ctx context.Context, queryEmbedding []float32, userID string, recentDays int) ([]Episode, error) {
	var after *time.Time
	if recentDays > 0 {
		t := time.Now().AddDate(0, 0, -recentDays)
		after = &t
	}

	episodes, err := sp.store.GetEpisodes(userID, after)
	if err != nil {
		return nil, err
	}

	type scored struct {
		episodeWithVector
		similarity float64
	}
	var results []scored
	for _, ep := range episodes {
		sim := CosineSimilarity(queryEmbedding, ep.Vector)
		results = append(results, scored{ep, sim})
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].similarity > results[j].similarity
	})

	// Return top 3
	limit := 3
	if len(results) < limit {
		limit = len(results)
	}
	var out []Episode
	for _, r := range results[:limit] {
		ep := r.Episode
		ep.Similarity = r.similarity
		out = append(out, ep)
	}
	return out, nil
}

// SearchArcs searches narrative arcs using projected 128d embeddings.
func (sp *SketchPath) SearchArcs(ctx context.Context, queryEmbedding []float32, userID string) ([]Arc, error) {
	arcs, err := sp.store.GetArcs(userID)
	if err != nil {
		return nil, err
	}

	// Project query to 128d
	query128 := sp.projector.ProjectTo128(queryEmbedding)

	type scored struct {
		arcWithVector
		similarity float64
	}
	var results []scored
	for _, a := range arcs {
		sim := CosineSimilarity(query128, a.SketchedVector)
		results = append(results, scored{a, sim})
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].similarity > results[j].similarity
	})

	// Return top 3
	limit := 3
	if len(results) < limit {
		limit = len(results)
	}
	var out []Arc
	for _, r := range results[:limit] {
		arc := r.Arc
		arc.Similarity = r.similarity
		out = append(out, arc)
	}
	return out, nil
}

// GetProfile returns the user's profile sketch.
func (sp *SketchPath) GetProfile(ctx context.Context, userID string) (*ProfileSketch, error) {
	p, err := sp.store.GetProfile(userID)
	if err != nil || p == nil {
		return nil, err
	}
	return &p.ProfileSketch, nil
}
