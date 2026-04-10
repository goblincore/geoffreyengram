package dualmem

import (
	"context"
	"fmt"
	"log"
	"time"
)

// Pipeline manages the background compression workers:
// - Episode Summarizer: batches raw sketch entries → episode summaries
// - Arc Builder: clusters episodes → narrative arcs
// - Profile Updater: merges arcs → user profile sketch
type Pipeline struct {
	store       Store
	embedder    EmbeddingProvider
	summarizer  SummarizerProvider
	projector   *Projector
	extractor   EntityExtractor

	episodeDebounce  time.Duration // wait after last notify before processing
	arcInterval      time.Duration
	profileInterval  time.Duration

	// notify is signalled by Add when a memory is routed to the Sketch Path.
	// Episode worker only wakes when there's actual work to do.
	notify chan struct{}

	cancelEpisode      context.CancelFunc
	cancelArc          context.CancelFunc
	cancelProfile      context.CancelFunc
	cancelAnticipatory context.CancelFunc
	engine             *Engine
}

// NewPipeline creates a compression pipeline.
func NewPipeline(store Store, embedder EmbeddingProvider, summarizer SummarizerProvider, projector *Projector, extractor EntityExtractor, cfg *Config) *Pipeline {
	debounce := cfg.EpisodeBatchInterval
	if debounce < 10*time.Second {
		debounce = 30 * time.Second
	}
	return &Pipeline{
		store:           store,
		embedder:        embedder,
		summarizer:      summarizer,
		projector:       projector,
		extractor:       extractor,
		episodeDebounce: debounce,
		arcInterval:     cfg.ArcBuildInterval,
		profileInterval: cfg.ProfileUpdateInterval,
		notify:          make(chan struct{}, 16),
	}
}

// Notify signals the pipeline that new sketch data is available.
// Non-blocking — extra signals are coalesced.
func (p *Pipeline) Notify() {
	select {
	case p.notify <- struct{}{}:
	default: // channel full, already pending — skip
	}
}

// Start launches all background workers.
func (p *Pipeline) Start() {
	if p.summarizer == nil {
		return // No summarizer = no pipeline
	}

	ctx1, cancel1 := context.WithCancel(context.Background())
	p.cancelEpisode = cancel1
	go p.episodeWorker(ctx1)

	ctx2, cancel2 := context.WithCancel(context.Background())
	p.cancelArc = cancel2
	go p.arcWorker(ctx2)

	ctx3, cancel3 := context.WithCancel(context.Background())
	p.cancelProfile = cancel3
	go p.profileWorker(ctx3)
}

// Stop shuts down all workers.
func (p *Pipeline) Stop() {
	if p.cancelEpisode != nil {
		p.cancelEpisode()
	}
	if p.cancelArc != nil {
		p.cancelArc()
	}
	if p.cancelProfile != nil {
		p.cancelProfile()
	}
	p.StopAnticipatory()
}

// StartAnticipatory launches the anticipatory worker for a specific session.
func (p *Pipeline) StartAnticipatory(namespace, sessionLogPath string) {
	if p.engine == nil || sessionLogPath == "" {
		return
	}
	if p.cancelAnticipatory != nil {
		return // already running
	}
	ctx, cancel := context.WithCancel(context.Background())
	p.cancelAnticipatory = cancel
	go p.engine.anticipatoryWorker(ctx, namespace, sessionLogPath)
}

// StopAnticipatory stops the anticipatory worker.
func (p *Pipeline) StopAnticipatory() {
	if p.cancelAnticipatory != nil {
		p.cancelAnticipatory()
		p.cancelAnticipatory = nil
	}
}

// --- Episode Worker ---

// episodeWorker sleeps until notified that sketch data is available,
// then debounces — waits for a quiet period after the last notification
// before processing, so rapid messages batch naturally.
func (p *Pipeline) episodeWorker(ctx context.Context) {
	var timer *time.Timer

	for {
		select {
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			return

		case <-p.notify:
			// Reset debounce timer on each notification
			if timer != nil {
				timer.Stop()
			}
			timer = time.NewTimer(p.episodeDebounce)

		case <-func() <-chan time.Time {
			if timer != nil {
				return timer.C
			}
			return nil // block forever if no timer
		}():
			timer = nil
			if err := p.processEpisodes(ctx); err != nil {
				log.Printf("dualmem: episode worker: %v", err)
			}
		}
	}
}

func (p *Pipeline) processEpisodes(ctx context.Context) error {
	// Get all active user IDs from raw entries
	// For simplicity, we process a batch across all users
	// In production, iterate per-user from a user list
	raw, err := p.store.GetUnprocessedRaw("", 100) // empty userID = all users
	if err != nil || len(raw) == 0 {
		return err
	}

	// Group by user + session (or by temporal proximity if no session)
	type batchKey struct {
		userID    string
		sessionID string
	}
	batches := make(map[batchKey][]sketchRaw)
	for _, r := range raw {
		key := batchKey{r.UserID, r.SessionID}
		batches[key] = append(batches[key], r)
	}

	for key, batch := range batches {
		if len(batch) < 2 {
			continue // Wait for more content
		}

		// Collect message texts
		var messages []string
		var rawIDs []string
		for _, r := range batch {
			messages = append(messages, r.Content)
			rawIDs = append(rawIDs, r.ID)
		}

		// Summarize
		summary, err := p.summarizer.SummarizeEpisode(ctx, messages)
		if err != nil {
			log.Printf("dualmem: episode summarize for user %s: %v", key.userID, err)
			continue
		}

		// Embed the summary
		embedding, err := p.embedder.Embed(ctx, summary, "RETRIEVAL_DOCUMENT")
		if err != nil {
			log.Printf("dualmem: episode embed for user %s: %v", key.userID, err)
			continue
		}

		// Extract entities from summary
		var entities []Entity
		if p.extractor != nil {
			for _, e := range p.extractor.Extract(summary) {
				entities = append(entities, Entity{Text: e.Text, Type: e.Type})
			}
		}

		ep := &Episode{
			ID:          generateID(),
			SummaryText: summary,
			Entities:    entities,
			CreatedAt:   time.Now(),
		}

		if err := p.store.InsertEpisode(ep, embedding, key.userID, "text-embedding-004", rawIDs); err != nil {
			log.Printf("dualmem: episode insert for user %s: %v", key.userID, err)
			continue
		}

		// Mark raw as processed
		p.store.MarkRawProcessed(rawIDs)
	}

	return nil
}

// --- Arc Worker ---

func (p *Pipeline) arcWorker(ctx context.Context) {
	ticker := time.NewTicker(p.arcInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := p.processArcs(ctx); err != nil {
				log.Printf("dualmem: arc worker: %v", err)
			}
		}
	}
}

func (p *Pipeline) processArcs(ctx context.Context) error {
	// Find expired episodes to cluster into arcs
	cutoff := time.Now().AddDate(0, 0, -30) // Episodes older than 30 days
	expired, err := p.store.GetExpiredEpisodes(time.Now())
	if err != nil || len(expired) == 0 {
		return err
	}

	// Group by user
	byUser := make(map[string][]episodeWithVector)
	for _, ep := range expired {
		byUser[ep.UserID] = append(byUser[ep.UserID], ep)
	}

	for userID, episodes := range byUser {
		if len(episodes) < 2 {
			continue
		}

		// Cluster episodes using the composite distance metric
		clusters := clusterEpisodes(episodes)

		for _, cluster := range clusters {
			if len(cluster) == 0 {
				continue
			}

			// Collect episode texts and IDs
			var texts []string
			var episodeIDs []string
			var allEntities []Entity
			for _, ep := range cluster {
				texts = append(texts, ep.SummaryText)
				episodeIDs = append(episodeIDs, ep.ID)
				allEntities = append(allEntities, ep.Entities...)
			}

			// Summarize arc
			summary, err := p.summarizer.SummarizeArc(ctx, texts)
			if err != nil {
				log.Printf("dualmem: arc summarize for user %s: %v", userID, err)
				continue
			}

			// Embed and project to 128d
			fullEmb, err := p.embedder.Embed(ctx, summary, "RETRIEVAL_DOCUMENT")
			if err != nil {
				log.Printf("dualmem: arc embed for user %s: %v", userID, err)
				continue
			}
			sketched := p.projector.ProjectTo128(fullEmb)

			arc := &Arc{
				ID:          generateID(),
				SummaryText: summary,
				Entities:    deduplicateEntities(allEntities),
				EpisodeIDs:  episodeIDs,
				CreatedAt:   time.Now(),
			}

			if err := p.store.InsertArc(arc, sketched, userID, "text-embedding-004", p.projector.Seed); err != nil {
				log.Printf("dualmem: arc insert for user %s: %v", userID, err)
				continue
			}

			// Delete consumed episodes
			for _, ep := range cluster {
				p.store.DeleteEpisode(ep.ID)
			}
		}
		_ = cutoff // used for context
	}

	return nil
}

// --- Profile Worker ---

func (p *Pipeline) profileWorker(ctx context.Context) {
	ticker := time.NewTicker(p.profileInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := p.processProfiles(ctx); err != nil {
				log.Printf("dualmem: profile worker: %v", err)
			}
		}
	}
}

func (p *Pipeline) processProfiles(ctx context.Context) error {
	users, err := p.store.GetUsersNeedingProfileUpdate()
	if err != nil {
		return fmt.Errorf("get users needing profile update: %w", err)
	}

	for _, userID := range users {
		if err := p.UpdateProfile(ctx, userID); err != nil {
			log.Printf("dualmem: profile update for user %s: %v", userID, err)
		}
	}
	return nil
}

// UpdateProfile updates or creates a user profile from their arcs.
func (p *Pipeline) UpdateProfile(ctx context.Context, userID string) error {
	arcs, err := p.store.GetArcs(userID)
	if err != nil {
		return err
	}
	if len(arcs) == 0 {
		return nil
	}

	// Get current profile
	existing, err := p.store.GetProfile(userID)
	if err != nil {
		return err
	}

	var currentProfile *ProfileSketch
	if existing != nil {
		currentProfile = &existing.ProfileSketch
	}

	// Collect arc texts
	var arcTexts []string
	for _, a := range arcs {
		arcTexts = append(arcTexts, a.SummaryText)
	}

	// LLM merge
	updated, err := p.summarizer.UpdateProfile(ctx, currentProfile, arcTexts)
	if err != nil {
		return fmt.Errorf("dualmem: profile update: %w", err)
	}
	updated.UserID = userID
	updated.UpdatedAt = time.Now()

	// Embed and project to 64d
	profileText := fmt.Sprintf("%v %v %s", updated.Interests, updated.KeyPreferences, updated.CommStyle)
	fullEmb, err := p.embedder.Embed(ctx, profileText, "RETRIEVAL_DOCUMENT")
	if err != nil {
		return err
	}
	sketched := p.projector.ProjectTo64(fullEmb)

	return p.store.UpsertProfile(updated, sketched, "text-embedding-004", p.projector.Seed)
}

// --- Episode Clustering ---

// clusterEpisodes groups episodes using single-linkage agglomerative clustering.
// Distance metric: 0.5*(1-entity_jaccard) + 0.3*temporal_norm + 0.2*(1-embedding_cosine)
// Merge threshold: 0.6
func clusterEpisodes(episodes []episodeWithVector) [][]episodeWithVector {
	n := len(episodes)
	if n <= 1 {
		return [][]episodeWithVector{episodes}
	}

	// Each episode starts in its own cluster
	clusters := make([][]episodeWithVector, n)
	for i, ep := range episodes {
		clusters[i] = []episodeWithVector{ep}
	}

	// Precompute pairwise distances
	dist := make([][]float64, n)
	for i := range dist {
		dist[i] = make([]float64, n)
		for j := range dist[i] {
			if i == j {
				continue
			}
			dist[i][j] = episodeDistance(episodes[i], episodes[j])
		}
	}

	active := make([]bool, n)
	for i := range active {
		active[i] = true
	}

	threshold := 0.6

	for {
		// Find closest pair of active clusters (single linkage)
		bestI, bestJ := -1, -1
		bestDist := threshold + 1

		for i := 0; i < n; i++ {
			if !active[i] {
				continue
			}
			for j := i + 1; j < n; j++ {
				if !active[j] {
					continue
				}
				d := clusterDistance(clusters[i], clusters[j], episodes, dist)
				if d < bestDist {
					bestDist = d
					bestI, bestJ = i, j
				}
			}
		}

		if bestI < 0 || bestDist > threshold {
			break // No more merges possible
		}

		// Merge j into i
		clusters[bestI] = append(clusters[bestI], clusters[bestJ]...)
		clusters[bestJ] = nil
		active[bestJ] = false
	}

	// Collect non-nil clusters
	var result [][]episodeWithVector
	for _, c := range clusters {
		if len(c) > 0 {
			result = append(result, c)
		}
	}
	return result
}

func episodeDistance(a, b episodeWithVector) float64 {
	entityJaccard := jaccard(entityTexts(a.Entities), entityTexts(b.Entities))
	temporalNorm := temporalDistance(a.CreatedAt, b.CreatedAt)
	embeddingSim := CosineSimilarity(a.Vector, b.Vector)

	return 0.5*(1-entityJaccard) + 0.3*temporalNorm + 0.2*(1-embeddingSim)
}

func clusterDistance(a, b []episodeWithVector, allEps []episodeWithVector, dist [][]float64) float64 {
	// Single linkage: minimum distance between any pair
	minDist := 2.0
	for _, ea := range a {
		for _, eb := range b {
			ia := indexOf(allEps, ea.ID)
			ib := indexOf(allEps, eb.ID)
			if ia >= 0 && ib >= 0 {
				d := dist[ia][ib]
				if d < minDist {
					minDist = d
				}
			}
		}
	}
	return minDist
}

func indexOf(eps []episodeWithVector, id string) int {
	for i, ep := range eps {
		if ep.ID == id {
			return i
		}
	}
	return -1
}

func entityTexts(entities []Entity) map[string]bool {
	set := make(map[string]bool)
	for _, e := range entities {
		set[e.Text] = true
	}
	return set
}

func jaccard(a, b map[string]bool) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 0
	}
	intersection := 0
	for k := range a {
		if b[k] {
			intersection++
		}
	}
	union := len(a) + len(b) - intersection
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}

func temporalDistance(a, b time.Time) float64 {
	days := a.Sub(b).Hours() / 24.0
	if days < 0 {
		days = -days
	}
	norm := days / 14.0
	if norm > 1.0 {
		norm = 1.0
	}
	return norm
}

func deduplicateEntities(entities []Entity) []Entity {
	seen := make(map[string]bool)
	var out []Entity
	for _, e := range entities {
		if !seen[e.Text] {
			seen[e.Text] = true
			out = append(out, e)
		}
	}
	return out
}
