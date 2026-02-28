package engram

import (
	"context"
	"log"
	"sort"
	"sync"
)

// scored pairs a memory+vector with its computed similarity to the query.
type scored struct {
	memoryWithVector
	similarity float64
}

// Engram is the cognitive memory engine.
// It provides Search, Add, and Reflect methods for persistent character memory.
type Engram struct {
	store         *Store
	embedder      EmbeddingProvider
	classifier    SectorClassifier
	extractor     EntityExtractor
	reflector     ReflectionProvider
	config        Config
	mu            sync.RWMutex
	cancelDecay   context.CancelFunc
	cancelReflect context.CancelFunc
}

// Init creates an Engram instance, runs DB migrations, and starts the decay worker.
func Init(cfg Config) (*Engram, error) {
	cfg.ApplyDefaults()

	store, err := NewStore(cfg.DBPath)
	if err != nil {
		return nil, err
	}

	// Resolve providers: use explicit config, or construct defaults from GeminiAPIKey
	embedder := cfg.EmbeddingProvider
	if embedder == nil && cfg.GeminiAPIKey != "" {
		embedder = NewGeminiEmbedder(cfg.GeminiAPIKey, cfg.EmbedDimension)
	}

	classifier := cfg.Classifier
	if classifier == nil {
		if cfg.GeminiAPIKey != "" {
			classifier = NewLLMClassifier(cfg.GeminiAPIKey, store)
		} else {
			classifier = NewHeuristicClassifier("") // heuristic-only, no LLM
		}
	}

	extractor := cfg.EntityExtractor
	if extractor == nil {
		extractor = &DefaultEntityExtractor{}
	}

	cm := &Engram{
		store:      store,
		embedder:   embedder,
		classifier: classifier,
		extractor:  extractor,
		reflector:  cfg.ReflectionProvider, // explicit opt-in only, never auto-constructed
		config:     cfg,
	}

	cm.startDecayWorker(cfg.DecayInterval)

	// Start optional reflection worker
	if cfg.ReflectionInterval > 0 && cm.reflector != nil {
		cm.startReflectionWorker(cfg.ReflectionInterval)
	}

	log.Printf("[engram] Initialized (db=%s, decay=%v)", cfg.DBPath, cfg.DecayInterval)

	return cm, nil
}

// Search retrieves relevant memories for a user, scored by the composite formula.
func (cm *Engram) Search(query, userID string, limit int, weights SectorWeights) []SearchResult {
	if userID == "" {
		return nil
	}
	if limit <= 0 {
		limit = 5
	}
	if weights == nil {
		weights = DefaultSectorWeights()
	}

	// 1. Embed the query
	if cm.embedder == nil {
		log.Printf("[engram] No embedding provider configured")
		return nil
	}
	queryVec, err := cm.embedder.Embed(context.Background(), query, "RETRIEVAL_QUERY")
	if err != nil {
		log.Printf("[engram] Embed query failed: %v", err)
		return nil
	}

	// 2. Load all memories + vectors for this user
	candidates, err := cm.store.GetMemoriesWithVectors(userID)
	if err != nil {
		log.Printf("[engram] Load memories failed: %v", err)
		return nil
	}
	if len(candidates) == 0 {
		return nil
	}

	// 3. Compute similarity for each candidate
	var scoredCandidates []scored
	for _, c := range candidates {
		if c.Vector == nil {
			continue
		}
		sim := CosineSimilarity(queryVec, c.Vector)
		scoredCandidates = append(scoredCandidates, scored{c, sim})
	}

	// Sort by similarity, take top candidates for waypoint expansion
	sort.Slice(scoredCandidates, func(i, j int) bool {
		return scoredCandidates[i].similarity > scoredCandidates[j].similarity
	})

	// Cap candidates for expansion (top 20 by similarity)
	expandLimit := 20
	if len(scoredCandidates) < expandLimit {
		expandLimit = len(scoredCandidates)
	}
	topCandidates := scoredCandidates[:expandLimit]

	// 4. Expand via waypoint graph (one-hop)
	seedMWVs := make([]memoryWithVector, len(topCandidates))
	for i, sc := range topCandidates {
		seedMWVs[i] = sc.memoryWithVector
	}
	linkWeights := ExpandViaWaypoints(cm.store, seedMWVs, userID)

	sw := cm.config.scoringWeights

	// 5. Compute composite scores with personality weights
	var results []SearchResult
	for _, sc := range scoredCandidates {
		sectorWeight := weights[sc.Sector]
		if sectorWeight == 0 {
			sectorWeight = 1.0
		}

		linkWeight := linkWeights[sc.ID] // 0 if not linked

		days := DaysSince(sc.LastAccessedAt)
		composite := CompositeScore(sc.similarity, sc.DecayScore, days, linkWeight, sectorWeight, sw)

		results = append(results, SearchResult{
			Memory:         sc.Memory,
			CompositeScore: composite,
			Similarity:     sc.similarity,
		})
	}

	// 6. Sort by composite score, take top-k
	sort.Slice(results, func(i, j int) bool {
		return results[i].CompositeScore > results[j].CompositeScore
	})
	if len(results) > limit {
		results = results[:limit]
	}

	// 6b. High-salience guarantee
	results = cm.guaranteeHighSalience(results, scoredCandidates, weights, linkWeights, limit)

	// 7. Reinforce accessed memories
	for _, r := range results {
		if err := cm.store.ReinforceSalience(r.ID, 0.15); err != nil {
			log.Printf("[engram] Reinforce failed for memory %d: %v", r.ID, err)
		}
	}

	return results
}

// Add stores a new memory from a conversation exchange.
// Safe to call from a goroutine.
func (cm *Engram) Add(userMessage, assistantMessage, userID string) {
	cm.AddWithOptions(AddOptions{
		UserID:           userID,
		UserMessage:      userMessage,
		AssistantMessage: assistantMessage,
	})
}

// AddWithOptions stores a new memory with full temporal and metadata control.
// Returns the memory ID (useful for chaining parent_id) and any error.
func (cm *Engram) AddWithOptions(opts AddOptions) (int64, error) {
	if opts.UserID == "" {
		return 0, nil
	}

	cm.mu.Lock()
	defer cm.mu.Unlock()

	// 1. Build content
	content := opts.UserMessage + " | " + opts.AssistantMessage

	// 2. Classify sector (or use hint)
	sector := opts.SectorHint
	if sector == "" {
		sector = cm.classifier.Classify(content)
	}

	// 3. Generate embedding
	var vec []float32
	if cm.embedder != nil {
		var err error
		vec, err = cm.embedder.Embed(context.Background(), content, "RETRIEVAL_DOCUMENT")
		if err != nil {
			log.Printf("[engram] Embed failed, storing without vector: %v", err)
		}
	}

	// 4. Generate summary
	summary := buildSummary(opts.UserMessage, opts.AssistantMessage, 200)

	// 5. Resolve salience
	salience := opts.Salience
	if salience == 0 {
		salience = 0.5
	}

	// 6. Store memory
	mem := Memory{
		Content:   content,
		Sector:    sector,
		Salience:  salience,
		UserID:    opts.UserID,
		Summary:   summary,
		SessionID: opts.SessionID,
		ParentID:  opts.ParentID,
	}
	memID, err := cm.store.InsertMemory(mem)
	if err != nil {
		log.Printf("[engram] Insert memory failed: %v", err)
		return 0, err
	}

	// 7. Store vector (if embedding succeeded)
	if vec != nil {
		if err := cm.store.InsertVector(memID, sector, vec); err != nil {
			log.Printf("[engram] Insert vector failed: %v", err)
		}
	}

	// 7b. Submit for async LLM reclassification (if available and no manual hint)
	if opts.SectorHint == "" {
		if lc, ok := cm.classifier.(*LLMClassifier); ok {
			lc.SubmitForReclassification(memID, content)
		}
	}

	// 8. Extract entities and create waypoint associations
	entities := opts.Entities
	if entities == nil {
		entities = cm.extractor.Extract(content)
	}
	for _, entity := range entities {
		wpID, err := cm.store.UpsertWaypoint(entity.Text, entity.Type)
		if err != nil {
			continue
		}
		cm.store.InsertAssociation(memID, wpID, 0.5)
	}

	// 9. Enforce per-user memory cap
	if err := cm.store.EnforceMemoryLimit(opts.UserID, cm.config.MaxMemoriesPerUser); err != nil {
		log.Printf("[engram] Enforce limit failed: %v", err)
	}

	log.Printf("[engram] Stored memory #%d [%s] for %s (%d entities)", memID, sector, opts.UserID, len(entities))
	return memID, nil
}

// SearchWithOptions retrieves memories with temporal and session filters.
func (cm *Engram) SearchWithOptions(opts SearchOptions) []SearchResult {
	if opts.UserID == "" {
		return nil
	}
	if opts.Limit <= 0 {
		opts.Limit = 5
	}
	if opts.Weights == nil {
		opts.Weights = DefaultSectorWeights()
	}

	if cm.embedder == nil {
		log.Printf("[engram] No embedding provider configured")
		return nil
	}
	queryVec, err := cm.embedder.Embed(context.Background(), opts.Query, "RETRIEVAL_QUERY")
	if err != nil {
		log.Printf("[engram] Embed query failed: %v", err)
		return nil
	}

	candidates, err := cm.store.GetMemoriesWithVectors(opts.UserID)
	if err != nil {
		log.Printf("[engram] Load memories failed: %v", err)
		return nil
	}

	// Apply temporal and sector filters
	var filtered []memoryWithVector
	for _, c := range candidates {
		if opts.After != nil && c.CreatedAt.Before(*opts.After) {
			continue
		}
		if opts.Before != nil && c.CreatedAt.After(*opts.Before) {
			continue
		}
		if opts.SessionID != "" && c.SessionID != opts.SessionID {
			continue
		}
		if len(opts.Sectors) > 0 {
			match := false
			for _, s := range opts.Sectors {
				if c.Sector == s {
					match = true
					break
				}
			}
			if !match {
				continue
			}
		}
		filtered = append(filtered, c)
	}

	if len(filtered) == 0 {
		return nil
	}

	var scoredCandidates []scored
	for _, c := range filtered {
		if c.Vector == nil {
			continue
		}
		sim := CosineSimilarity(queryVec, c.Vector)
		scoredCandidates = append(scoredCandidates, scored{c, sim})
	}

	sort.Slice(scoredCandidates, func(i, j int) bool {
		return scoredCandidates[i].similarity > scoredCandidates[j].similarity
	})

	expandLimit := 20
	if len(scoredCandidates) < expandLimit {
		expandLimit = len(scoredCandidates)
	}
	topCandidates := scoredCandidates[:expandLimit]

	seedMWVs := make([]memoryWithVector, len(topCandidates))
	for i, sc := range topCandidates {
		seedMWVs[i] = sc.memoryWithVector
	}
	linkWeights := ExpandViaWaypoints(cm.store, seedMWVs, opts.UserID)

	sw := cm.config.scoringWeights

	var results []SearchResult
	for _, sc := range scoredCandidates {
		sectorWeight := opts.Weights[sc.Sector]
		if sectorWeight == 0 {
			sectorWeight = 1.0
		}
		lw := linkWeights[sc.ID]
		days := DaysSince(sc.LastAccessedAt)
		composite := CompositeScore(sc.similarity, sc.DecayScore, days, lw, sectorWeight, sw)
		results = append(results, SearchResult{
			Memory:         sc.Memory,
			CompositeScore: composite,
			Similarity:     sc.similarity,
		})
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].CompositeScore > results[j].CompositeScore
	})
	if len(results) > opts.Limit {
		results = results[:opts.Limit]
	}

	results = cm.guaranteeHighSalience(results, scoredCandidates, opts.Weights, linkWeights, opts.Limit)

	for _, r := range results {
		cm.store.ReinforceSalience(r.ID, 0.15)
	}

	return results
}

// GetSession returns all memories from a specific session, in chronological order.
func (cm *Engram) GetSession(sessionID string) ([]Memory, error) {
	return cm.store.GetSessionMemories(sessionID)
}

// GetLastSession returns all memories from the user's most recent session.
func (cm *Engram) GetLastSession(userID string) ([]Memory, error) {
	sessionID, err := cm.store.GetLastSessionID(userID)
	if err != nil || sessionID == "" {
		return nil, err
	}
	return cm.store.GetSessionMemories(sessionID)
}

// ListRecent returns the N most recent memories for a user, optionally filtered by sector.
// Intended for inspection and debugging tools (e.g., MCP inspect).
func (cm *Engram) ListRecent(userID string, limit int, sectors []Sector) ([]Memory, error) {
	return cm.store.GetRecentMemories(userID, limit, sectors)
}

// Close shuts down workers and closes the database.
func (cm *Engram) Close() error {
	if cm.cancelDecay != nil {
		cm.cancelDecay()
	}
	if cm.cancelReflect != nil {
		cm.cancelReflect()
	}
	if lc, ok := cm.classifier.(*LLMClassifier); ok {
		lc.Close()
	}
	return cm.store.Close()
}

// guaranteeHighSalience ensures the user's highest-salience memories appear in
// results even if their semantic similarity to the current query is low.
func (cm *Engram) guaranteeHighSalience(results []SearchResult, allScored []scored, weights SectorWeights, linkWeights map[int64]float64, limit int) []SearchResult {
	const salienceThreshold = 0.6
	const maxBoosts = 2

	sw := cm.config.scoringWeights

	// Collect IDs already in results
	inResults := make(map[int64]bool)
	for _, r := range results {
		inResults[r.ID] = true
	}

	// Find high-salience memories not yet in results
	var candidates []SearchResult
	for _, sc := range allScored {
		if inResults[sc.ID] || sc.Salience < salienceThreshold {
			continue
		}
		sectorWeight := weights[sc.Sector]
		if sectorWeight == 0 {
			sectorWeight = 1.0
		}
		lw := linkWeights[sc.ID]
		days := DaysSince(sc.LastAccessedAt)
		composite := CompositeScore(sc.similarity, sc.DecayScore, days, lw, sectorWeight, sw)
		candidates = append(candidates, SearchResult{
			Memory:         sc.Memory,
			CompositeScore: composite,
			Similarity:     sc.similarity,
		})
	}

	if len(candidates) == 0 {
		return results
	}

	// Sort candidates by salience (highest first)
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Salience > candidates[j].Salience
	})

	// Inject top high-salience candidates, replacing the lowest-scored results
	injected := 0
	for _, c := range candidates {
		if injected >= maxBoosts {
			break
		}
		if len(results) >= limit {
			results[len(results)-1] = c
		} else {
			results = append(results, c)
		}
		injected++
	}

	return results
}

// buildSummary creates a summary from both sides of the exchange.
// Splits budget proportionally with " | " separator.
func buildSummary(userMessage, assistantMessage string, maxLen int) string {
	userBudget := maxLen * 60 / 100
	npcBudget := maxLen - userBudget - 3 // account for " | " separator

	userPart := truncateSummary(userMessage, userBudget)
	npcPart := truncateSummary(assistantMessage, npcBudget)

	return userPart + " | " + npcPart
}

// truncateSummary returns the first n characters of s, breaking at a word boundary.
func truncateSummary(s string, n int) string {
	if len(s) <= n {
		return s
	}
	// Find last space before limit
	cut := n
	for cut > 0 && s[cut] != ' ' {
		cut--
	}
	if cut == 0 {
		cut = n
	}
	return s[:cut] + "..."
}
