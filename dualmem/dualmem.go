// Package dualmem implements a dual-path agent memory system.
//
// It splits memories into two mathematically distinct storage paths:
//   - Detail Path: fixed-capacity store of uncompressed, full-fidelity memories
//   - Sketch Path: hierarchical compression (episodes → arcs → profile)
//
// Routing is determined by an importance scorer that runs at insert time
// without LLM calls, preserving fire-and-forget latency.
package dualmem

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Engine is the main DualMem implementation.
type Engine struct {
	mu         sync.RWMutex
	store      Store
	detail     *DetailPath
	sketch     *SketchPath
	pipeline   *Pipeline
	projector  *Projector
	embedder   EmbeddingProvider
	classifier SectorClassifier
	extractor  EntityExtractor
	cfg        *Config
}

// New creates and initializes a DualMem engine.
func New(cfg Config) (*Engine, error) {
	cfg.ApplyDefaults()

	if cfg.EmbeddingProvider == nil {
		return nil, fmt.Errorf("dualmem: EmbeddingProvider is required")
	}

	// Open store
	store, err := NewSQLiteStore(cfg.SQLitePath)
	if err != nil {
		return nil, err
	}

	// Initialize or load projection seed
	seed := cfg.ProjectionSeed
	if seed == 0 {
		stored, err := store.GetConfigValue("projection_seed")
		if err != nil {
			store.Close()
			return nil, fmt.Errorf("dualmem: get projection seed: %w", err)
		}
		if stored != "" {
			seed, _ = strconv.ParseInt(stored, 10, 64)
		}
		if seed == 0 {
			seed = time.Now().UnixNano()
			if err := store.SetConfigValue("projection_seed", strconv.FormatInt(seed, 10)); err != nil {
				store.Close()
				return nil, fmt.Errorf("dualmem: store projection seed: %w", err)
			}
		}
	}

	projector := NewProjector(seed, cfg.EmbeddingProvider.Dimension())

	e := &Engine{
		store:      store,
		detail:     NewDetailPath(store, cfg.EmbeddingProvider, cfg.ImportanceTheta, cfg.MaxDetailPerUser, cfg.Sectors.DetailBias),
		sketch:     NewSketchPath(store, projector, cfg.EmbeddingProvider),
		projector:  projector,
		embedder:   cfg.EmbeddingProvider,
		classifier: cfg.Classifier,
		extractor:  cfg.EntityExtractor,
		cfg:        &cfg,
	}

	// Start compression pipeline if summarizer is provided
	if cfg.Summarizer != nil {
		e.pipeline = NewPipeline(store, cfg.EmbeddingProvider, cfg.Summarizer, projector, cfg.EntityExtractor, &cfg)
		e.pipeline.Start()
	}

	return e, nil
}

// --- Drop-in compatibility with Engram ---

// Add stores a memory with the same signature as Engram's Add.
// Routes to Detail or Sketch Path based on importance scoring.
func (e *Engine) Add(userMessage, assistantMessage, userID string) {
	ctx := context.Background()
	_ = e.AddWithOptions(ctx, MemoryInput{
		UserMessage:      userMessage,
		AssistantMessage: assistantMessage,
	}, userID)
}

// Search matches Engram's Search signature for drop-in compatibility.
func (e *Engine) Search(query, userID string, limit int, weights map[string]float64) []DetailMemory {
	ctx := context.Background()
	results, err := e.DualSearch(ctx, userID, query, SearchOpts{
		Limit:   limit,
		Weights: weights,
	})
	if err != nil || results == nil {
		return nil
	}
	return results.DetailMemories
}

// Embedder returns the engine's embedding provider.
func (e *Engine) Embedder() EmbeddingProvider { return e.embedder }

// --- Extended API ---

// AddWithOptions stores a memory with full options, routing to Detail or Sketch Path.
func (e *Engine) AddWithOptions(ctx context.Context, input MemoryInput, userID string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	// Compose content
	content := input.UserMessage
	if input.AssistantMessage != "" {
		content = fmt.Sprintf("User: %s\nAssistant: %s", input.UserMessage, input.AssistantMessage)
	}

	// Determine salience
	salience := input.Salience
	if salience == 0 {
		salience = 0.5
	}

	// Embed first — needed for EmbeddingClassifier (zero extra API calls)
	embedding, err := e.embedder.Embed(ctx, content, "RETRIEVAL_DOCUMENT")
	if err != nil {
		return fmt.Errorf("dualmem: embed: %w", err)
	}

	// Classify sector — prefer ClassifyFromEmbedding when available (reuses embedding)
	sector := input.SectorHint
	if sector == "" && e.classifier != nil {
		if ec, ok := e.classifier.(*EmbeddingClassifier); ok {
			sector = ec.ClassifyFromEmbedding(embedding)
		} else {
			sector = e.classifier.Classify(content)
		}
	}
	if sector == "" {
		sector = e.cfg.Sectors.Default
	}

	// Extract entities
	entities := input.Entities
	if len(entities) == 0 && e.extractor != nil {
		for _, ext := range e.extractor.Extract(content) {
			entities = append(entities, Entity{Text: ext.Text, Type: ext.Type})
		}
	}

	// Get existing detail memories for novelty scoring
	existingDetails, err := e.store.GetDetailMemories(userID)
	if err != nil {
		return fmt.Errorf("dualmem: get details: %w", err)
	}

	// Score importance
	score, isDetail := e.detail.ScoreAndRoute(sector, salience, content, embedding, existingDetails, input.Type)

	if isDetail {
		dm := &DetailMemory{
			ID:              generateID(),
			Text:            content,
			Sector:          sector,
			Salience:        salience,
			ImportanceScore: score,
			Entities:        entities,
			Type:            input.Type,
			Files:           input.Files,
			SessionID:       input.SessionID,
			CreatedAt:       time.Now(),
			LastAccessedAt:  time.Now(),
		}

		demoted, err := e.detail.Insert(ctx, dm, embedding, userID)
		if err != nil {
			return fmt.Errorf("dualmem: detail insert: %w", err)
		}

		// If a memory was demoted, route it to sketch
		if demoted != "" && demoted != content {
			e.sketch.IngestRaw(ctx, userID, demoted, sector, input.SessionID, nil)
			e.notifyPipeline()
		}

		// Continuity supersession: when a new continuity entry is added,
		// auto-demote older continuity entries about the same topic.
		if input.Type == "continuity" {
			e.supersedeContinuity(ctx, userID, dm.ID, embedding)
		}
	} else {
		// Route to Sketch Path
		if err := e.sketch.IngestRaw(ctx, userID, content, sector, input.SessionID, embedding); err != nil {
			return fmt.Errorf("dualmem: sketch ingest: %w", err)
		}
		e.notifyPipeline()
	}

	return nil
}

// DualSearch performs parallel search across both paths.
func (e *Engine) DualSearch(ctx context.Context, userID string, query string, opts SearchOpts) (*DualSearchResult, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	// Embed query (or use pre-computed)
	var queryEmb []float32
	if opts.QueryEmbedding != nil {
		queryEmb = opts.QueryEmbedding
	} else {
		var err error
		queryEmb, err = e.embedder.Embed(ctx, query, "RETRIEVAL_QUERY")
		if err != nil {
			return nil, fmt.Errorf("dualmem: embed query: %w", err)
		}
	}

	result := &DualSearchResult{}

	// Detail Path search
	limit := opts.Limit
	if limit <= 0 {
		limit = 5
	}
	details, err := e.detail.Search(ctx, queryEmb, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("dualmem: detail search: %w", err)
	}
	result.DetailMemories = details

	// Sketch Path searches (if sketch is enabled, default true)
	includeSketch := opts.IncludeSketch
	if !includeSketch && opts.Limit == 0 {
		includeSketch = true // default
	}

	if includeSketch {
		// Search recent episodes (last 7 days at full resolution)
		episodes, err := e.sketch.SearchEpisodes(ctx, queryEmb, userID, 7)
		if err == nil {
			result.Episodes = episodes
		}

		// Search arcs at 128d
		arcs, err := e.sketch.SearchArcs(ctx, queryEmb, userID)
		if err == nil {
			result.Arcs = arcs
		}

		// Get profile
		profile, err := e.sketch.GetProfile(ctx, userID)
		if err == nil {
			result.Profile = profile
		}
	}

	return result, nil
}

// AssembleContext is the key differentiator: token-budget-aware context assembly.
// It retrieves from both paths and formats a structured context block that fits
// within the given token budget. If Config.RootDir is set, includes structural
// diffs and code maps before memories.
func (e *Engine) AssembleContext(ctx context.Context, userID string, query string, tokenBudget int) (*ContextBlock, error) {
	if tokenBudget <= 0 {
		tokenBudget = 2000
	}

	// Embed query FIRST — shared by DualSearch and codemap ranking
	var queryEmb []float32
	if query != "" {
		var err error
		queryEmb, err = e.embedder.Embed(ctx, query, "RETRIEVAL_QUERY")
		if err != nil {
			return nil, fmt.Errorf("dualmem: embed query: %w", err)
		}
	}

	results, err := e.DualSearch(ctx, userID, query, SearchOpts{
		Limit:          10,
		IncludeSketch:  true,
		QueryEmbedding: queryEmb,
	})
	if err != nil {
		return nil, err
	}

	var parts []string
	var sources []SourceRef
	tokensUsed := 0
	staleIDs := make(map[string]bool)

	// --- Structural diff (≤150 tokens) ---
	if e.cfg.RootDir != "" && tokenBudget >= 200 {
		lastMarker, _ := e.store.GetLatestSessionMarker(userID)
		diff, _ := ComputeStructuralDiff(e.cfg.RootDir, lastMarker)
		if diff != nil && diff.TotalChanges() > 0 {
			diffText := diff.Summary
			diffTokens := estimateTokens(diffText)
			if diffTokens <= 150 && tokensUsed+diffTokens <= tokenBudget {
				parts = append(parts, "[Changes Since Last Session]\n"+diffText)
				sources = append(sources, SourceRef{Type: "diff", ID: diff.ToCommit})
				tokensUsed += diffTokens
			}
			staleList := DetectStaleMemories(diff, e.store, userID)
			for _, id := range staleList {
				staleIDs[id] = true
			}
		}
	}

	// --- Code map (≤400 tokens, query-aware ranking) ---
	if e.cfg.RootDir != "" && tokenBudget >= 500 {
		mapBudget := 400
		if tokenBudget-tokensUsed-200 < mapBudget {
			mapBudget = tokenBudget - tokensUsed - 200
		}
		if mapBudget > 50 {
			codeMap, moduleEmbs := e.getOrGenerateCodeMap(ctx, userID)
			if codeMap != nil {
				mapText := codeMap.RenderAtBudget(mapBudget, queryEmb, moduleEmbs)
				mapTokens := estimateTokens(mapText)
				parts = append(parts, "[Codebase Map]\n"+mapText)
				sources = append(sources, SourceRef{Type: "codemap", ID: userID})
				tokensUsed += mapTokens
			}
		}
	}

	// 1. Profile sketch (~50 tokens) — always include if available
	if results.Profile != nil {
		profileText := formatProfile(results.Profile)
		profileTokens := estimateTokens(profileText)
		if tokensUsed+profileTokens <= tokenBudget {
			parts = append(parts, "[User Profile]\n"+profileText)
			sources = append(sources, SourceRef{Type: "profile", ID: results.Profile.UserID})
			tokensUsed += profileTokens
		}
	}

	// 2. Narrative arcs (~100-200 tokens each)
	for _, arc := range results.Arcs {
		arcText := arc.SummaryText
		arcTokens := estimateTokens(arcText)
		if tokensUsed+arcTokens > tokenBudget {
			break
		}
		parts = append(parts, "[Narrative Arc]\n"+arcText)
		sources = append(sources, SourceRef{Type: "arc", ID: arc.ID})
		tokensUsed += arcTokens
	}

	// 3. Detail memories — sort by type priority (warnings first, then decisions),
	// with access-recency weighting to sink stale memories.
	now := time.Now()
	sort.SliceStable(results.DetailMemories, func(i, j int) bool {
		pi := typePriority(results.DetailMemories[i].Type)
		pj := typePriority(results.DetailMemories[j].Type)
		if pi != pj {
			return pi > pj
		}
		// Within same priority tier, boost recently accessed memories.
		// Warnings (priority 2) are exempt from recency decay.
		ri := accessRecencyFactor(results.DetailMemories[i].LastAccessedAt, results.DetailMemories[i].Type, now)
		rj := accessRecencyFactor(results.DetailMemories[j].LastAccessedAt, results.DetailMemories[j].Type, now)
		return ri > rj
	})
	for _, dm := range results.DetailMemories {
		dmTokens := estimateTokens(dm.Text) + estimateTokens(strings.Join(dm.Files, ", "))
		if tokensUsed+dmTokens > tokenBudget {
			break
		}
		label := formatTypeLabel(dm.Type, dm.Sector, dm.ImportanceScore)
		if staleIDs[dm.ID] {
			label += " [STALE? file changed since last session]"
		}
		entry := label + "\n" + dm.Text
		if len(dm.Files) > 0 {
			entry += "\n  Files: " + strings.Join(dm.Files, ", ")
		}
		parts = append(parts, entry)
		sources = append(sources, SourceRef{Type: "detail", ID: dm.ID})
		tokensUsed += dmTokens
	}

	// 4. Recent episodes (if budget remains)
	for _, ep := range results.Episodes {
		epText := ep.SummaryText
		epTokens := estimateTokens(epText)
		if tokensUsed+epTokens > tokenBudget {
			break
		}
		parts = append(parts, "[Recent Episode]\n"+epText)
		sources = append(sources, SourceRef{Type: "episode", ID: ep.ID})
		tokensUsed += epTokens
	}

	text := strings.Join(parts, "\n\n")

	// Record session marker for next time
	if e.cfg.RootDir != "" {
		e.recordSessionMarker(userID)
	}

	return &ContextBlock{
		Text:       text,
		TokenCount: tokensUsed,
		Sources:    sources,
	}, nil
}

// getOrGenerateCodeMap loads a stored code map or generates one on the fly.
// Returns the code map and per-module embeddings (for query-aware ranking).
func (e *Engine) getOrGenerateCodeMap(ctx context.Context, namespace string) (*CodeMap, map[string][]float32) {
	_, currentCommit := GetGitState(e.cfg.RootDir)

	stored, _ := e.store.GetCodeMap(namespace)
	storedEmbs, storedModel, _ := e.store.GetCodeMapEmbeddings(namespace)

	// Use cache if git commit matches and embedding model matches
	if stored != nil && stored.GitCommit == currentCommit && currentCommit != "" {
		cm := &CodeMap{
			Namespace:   stored.Namespace,
			RootDir:     stored.RootDir,
			Zoom1:       stored.Zoom1,
			Zoom2:       UnmarshalZoom2(stored.Zoom2JSON),
			GeneratedAt: stored.GeneratedAt,
			GitCommit:   stored.GitCommit,
		}
		if storedModel == e.embedder.ModelName() && len(storedEmbs) > 0 {
			return cm, storedEmbs
		}
		// Re-embed with current model
		embs, err := EmbedCodeMap(ctx, cm, e.embedder)
		if err == nil && embs != nil {
			flat := flattenEmbeddings(embs)
			e.store.UpsertCodeMapEmbeddings(namespace, embs, e.embedder.ModelName())
			return cm, flat
		}
		return cm, nil
	}

	// Regenerate
	cm, err := ScanCodebase(e.cfg.RootDir)
	if err != nil {
		return nil, nil
	}
	cm.Namespace = namespace
	cm.GitCommit = currentCommit

	e.store.UpsertCodeMap(namespace, e.cfg.RootDir, cm.Zoom1, cm.MarshalZoom2(), cm.GitCommit)

	embs, err := EmbedCodeMap(ctx, cm, e.embedder)
	if err == nil && embs != nil {
		flat := flattenEmbeddings(embs)
		e.store.UpsertCodeMapEmbeddings(namespace, embs, e.embedder.ModelName())
		return cm, flat
	}

	return cm, nil
}

func flattenEmbeddings(embs map[string]ModuleEmbedding) map[string][]float32 {
	flat := make(map[string][]float32, len(embs))
	for k, v := range embs {
		flat[k] = v.Embedding
	}
	return flat
}

// StoreCodeMap persists a code map to the database and embeds modules. Called by the CLI `map` command.
func (e *Engine) StoreCodeMap(ctx context.Context, namespace string, cm *CodeMap) error {
	err := e.store.UpsertCodeMap(namespace, cm.RootDir, cm.Zoom1, cm.MarshalZoom2(), cm.GitCommit)
	if err != nil {
		return err
	}
	embs, embErr := EmbedCodeMap(ctx, cm, e.embedder)
	if embErr == nil && embs != nil {
		e.store.UpsertCodeMapEmbeddings(namespace, embs, e.embedder.ModelName())
	}
	return nil
}

// GetLatestSessionMarker returns the most recent session marker for a namespace.
// Used by the CLI `diff` command.
func (e *Engine) GetLatestSessionMarker(namespace string) (*SessionMarker, error) {
	return e.store.GetLatestSessionMarker(namespace)
}

// recordSessionMarker saves the current git state for structural diffs.
func (e *Engine) recordSessionMarker(namespace string) {
	branch, commit := GetGitState(e.cfg.RootDir)
	if commit == "" {
		return
	}
	marker := &SessionMarker{
		ID:        fmt.Sprintf("sm-%d", time.Now().UnixNano()),
		Namespace: namespace,
		Branch:    branch,
		Commit:    commit,
	}
	e.store.InsertSessionMarker(marker)
}

// GetProfile returns the user's profile sketch.
func (e *Engine) GetProfile(ctx context.Context, userID string) (*ProfileSketch, error) {
	return e.sketch.GetProfile(ctx, userID)
}

// PromoteToDetail moves a sketch memory (raw or episode) to the Detail Path.
// Searches sketch_raw then episodes by ID. opts is optional (nil = defaults).
func (e *Engine) PromoteToDetail(ctx context.Context, userID string, memoryID string, opts *PromoteOpts) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	var text, sector, sessionID string
	var embedding []float32
	var entities []Entity
	var sourceType string // "raw" or "episode"

	// Try sketch_raw first
	raw, err := e.store.GetSketchRawByID(memoryID)
	if err != nil {
		return fmt.Errorf("dualmem: lookup raw: %w", err)
	}
	if raw != nil {
		text = raw.Content
		sector = raw.Sector
		sessionID = raw.SessionID
		embedding = raw.Embedding
		sourceType = "raw"
	}

	// Try episodes if not found in raw
	if sourceType == "" {
		ep, err := e.store.GetEpisodeByID(memoryID)
		if err != nil {
			return fmt.Errorf("dualmem: lookup episode: %w", err)
		}
		if ep != nil {
			text = ep.SummaryText
			entities = ep.Entities
			embedding = ep.Vector
			sourceType = "episode"
		}
	}

	if sourceType == "" {
		return fmt.Errorf("dualmem: memory %s not found in sketch path", memoryID)
	}

	// Embed if needed (raw entries may have nil embedding)
	if len(embedding) == 0 || len(embedding) != e.embedder.Dimension() {
		embedding, err = e.embedder.Embed(ctx, text, "RETRIEVAL_DOCUMENT")
		if err != nil {
			return fmt.Errorf("dualmem: embed for promote: %w", err)
		}
	}

	// Classify sector if empty
	if sector == "" && e.classifier != nil {
		if ec, ok := e.classifier.(*EmbeddingClassifier); ok {
			sector = ec.ClassifyFromEmbedding(embedding)
		} else {
			sector = e.classifier.Classify(text)
		}
	}
	if sector == "" {
		sector = e.cfg.Sectors.Default
	}

	// Extract entities if none
	if len(entities) == 0 && e.extractor != nil {
		for _, ext := range e.extractor.Extract(text) {
			entities = append(entities, Entity{Text: ext.Text, Type: ext.Type})
		}
	}

	// Apply opts
	memType := ""
	salience := 0.7
	var files []string
	if opts != nil {
		if opts.Type != "" {
			memType = opts.Type
		}
		if opts.Salience > 0 {
			salience = opts.Salience
		}
	}

	// Score, but guarantee Detail routing
	existingDetails, _ := e.store.GetDetailMemories(userID)
	score, _ := e.detail.ScoreAndRoute(sector, salience, text, embedding, existingDetails, memType)
	if score < e.cfg.ImportanceTheta+0.05 {
		score = e.cfg.ImportanceTheta + 0.05
	}

	dm := &DetailMemory{
		ID:              generateID(),
		Text:            text,
		Sector:          sector,
		Salience:        salience,
		ImportanceScore: score,
		Entities:        entities,
		Type:            memType,
		Files:           files,
		SessionID:       sessionID,
		CreatedAt:       time.Now(),
		LastAccessedAt:  time.Now(),
	}

	demoted, err := e.detail.Insert(ctx, dm, embedding, userID)
	if err != nil {
		return fmt.Errorf("dualmem: detail insert on promote: %w", err)
	}

	// Route demoted memory to sketch if needed
	if demoted != "" && demoted != text {
		e.sketch.IngestRaw(ctx, userID, demoted, sector, sessionID, nil)
	}

	// Delete from source table
	switch sourceType {
	case "raw":
		e.store.DeleteSketchRaw(memoryID)
	case "episode":
		e.store.DeleteEpisode(memoryID)
	}

	return nil
}

// ReEvaluateSketchRaw re-scores all raw sketch entries for a user and promotes
// those that now exceed the importance threshold. Useful for recovering old
// memories that were scored before the type-boost fix.
func (e *Engine) ReEvaluateSketchRaw(ctx context.Context, userID string, opts *PromoteOpts) (int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	raws, err := e.store.GetAllSketchRaw(userID, 1000)
	if err != nil {
		return 0, fmt.Errorf("dualmem: get all raw: %w", err)
	}

	memType := ""
	salience := 0.5
	if opts != nil {
		if opts.Type != "" {
			memType = opts.Type
		}
		if opts.Salience > 0 {
			salience = opts.Salience
		}
	}

	existingDetails, _ := e.store.GetDetailMemories(userID)
	promoted := 0

	for _, raw := range raws {
		embedding := raw.Embedding
		if len(embedding) == 0 || len(embedding) != e.embedder.Dimension() {
			embedding, err = e.embedder.Embed(ctx, raw.Content, "RETRIEVAL_DOCUMENT")
			if err != nil {
				continue // skip entries that fail to embed
			}
		}

		sector := raw.Sector
		if sector == "" && e.classifier != nil {
			if ec, ok := e.classifier.(*EmbeddingClassifier); ok {
				sector = ec.ClassifyFromEmbedding(embedding)
			} else {
				sector = e.classifier.Classify(raw.Content)
			}
		}
		if sector == "" {
			sector = e.cfg.Sectors.Default
		}

		score, isDetail := e.detail.ScoreAndRoute(sector, salience, raw.Content, embedding, existingDetails, memType)
		if !isDetail {
			continue
		}

		var entities []Entity
		if e.extractor != nil {
			for _, ext := range e.extractor.Extract(raw.Content) {
				entities = append(entities, Entity{Text: ext.Text, Type: ext.Type})
			}
		}

		dm := &DetailMemory{
			ID:              generateID(),
			Text:            raw.Content,
			Sector:          sector,
			Salience:        salience,
			ImportanceScore: score,
			Entities:        entities,
			Type:            memType,
			SessionID:       raw.SessionID,
			CreatedAt:       time.Now(),
			LastAccessedAt:  time.Now(),
		}

		demoted, err := e.detail.Insert(ctx, dm, embedding, userID)
		if err != nil {
			continue
		}

		if demoted != "" && demoted != raw.Content {
			e.sketch.IngestRaw(ctx, userID, demoted, sector, raw.SessionID, nil)
		}

		e.store.DeleteSketchRaw(raw.ID)
		promoted++

		// Refresh existing details for novelty scoring of next entry
		existingDetails, _ = e.store.GetDetailMemories(userID)
	}

	return promoted, nil
}

// supersedeContinuity demotes older continuity entries that are semantically
// similar to the newly added one (cosine similarity > 0.75). This prevents
// stale "remaining: X" entries from accumulating when X has been completed
// and a newer continuity entry reflects the updated status.
func (e *Engine) supersedeContinuity(ctx context.Context, userID, newID string, newEmbedding []float32) {
	existing, err := e.store.GetDetailsByType(userID, "continuity")
	if err != nil || len(existing) <= 1 {
		return
	}

	const supersessionThreshold = 0.75
	for _, old := range existing {
		if old.ID == newID {
			continue
		}
		sim := CosineSimilarity(newEmbedding, old.Vector)
		if sim >= supersessionThreshold {
			// Demote the older entry to sketch path
			e.sketch.IngestRaw(ctx, userID, old.Text, old.Sector, old.SessionID, old.Vector)
			e.store.DeleteDetail(old.ID)
		}
	}
}

// DemoteFromDetail removes a memory from Detail Path and routes to Sketch.
func (e *Engine) DemoteFromDetail(ctx context.Context, userID string, memoryID string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	// Get the detail memory
	details, err := e.store.GetDetailMemories(userID)
	if err != nil {
		return err
	}

	for _, d := range details {
		if d.ID == memoryID {
			// Route to sketch
			if err := e.sketch.IngestRaw(ctx, userID, d.Text, d.Sector, d.SessionID, d.Vector); err != nil {
				return err
			}
			// Delete from detail
			return e.store.DeleteDetail(memoryID)
		}
	}

	return fmt.Errorf("dualmem: memory %s not found in detail path", memoryID)
}

// GarbageCollect runs comprehensive cleanup of stale, expired, and superseded memories.
// Steps: expire episodes/arcs, demote git-stale details, supersede duplicate continuity,
// demote access-cold details.
func (e *Engine) GarbageCollect(ctx context.Context, userID string, opts GCOptions) (*GCReport, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	report := &GCReport{}
	now := time.Now()

	// 1. Expire old episodes
	expired, err := e.store.GetExpiredEpisodes(now)
	if err == nil {
		for _, ep := range expired {
			if opts.Verbose {
				report.Entries = append(report.Entries, GCEntry{
					ID: ep.ID, Action: "delete", Reason: "expired_episode",
					Text: truncateGC(ep.SummaryText, 80),
				})
			}
			if !opts.DryRun {
				e.store.DeleteEpisode(ep.ID)
			}
			report.ExpiredEpisodes++
		}
	}

	// 2. Expire old arcs
	expiredArcs, err := e.store.GetExpiredArcs(now)
	if err == nil {
		for _, arc := range expiredArcs {
			if opts.Verbose {
				report.Entries = append(report.Entries, GCEntry{
					ID: arc.ID, Action: "delete", Reason: "expired_arc",
					Text: truncateGC(arc.SummaryText, 80),
				})
			}
			if !opts.DryRun {
				e.store.DeleteArc(arc.ID)
			}
			report.ExpiredArcs++
		}
	}

	// 3. Git-stale detail demotion
	if e.cfg.RootDir != "" {
		lastMarker, _ := e.store.GetLatestSessionMarker(userID)
		diff, _ := ComputeStructuralDiff(e.cfg.RootDir, lastMarker)
		if diff != nil {
			staleIDs := DetectStaleMemories(diff, e.store, userID)
			details, _ := e.store.GetDetailMemories(userID)
			detailMap := make(map[string]detailWithVector)
			for _, d := range details {
				detailMap[d.ID] = d
			}
			for _, id := range staleIDs {
				d, ok := detailMap[id]
				if !ok {
					continue
				}
				// Don't demote warnings — they're intentional invariants
				if d.Type == "warning" {
					continue
				}
				if opts.Verbose {
					report.Entries = append(report.Entries, GCEntry{
						ID: id, Action: "demote", Reason: "git_stale",
						Text: truncateGC(d.Text, 80),
					})
				}
				if !opts.DryRun {
					e.sketch.IngestRaw(ctx, userID, d.Text, d.Sector, d.SessionID, d.Vector)
					e.store.DeleteDetail(id)
				}
				report.StaleDetails++
			}
		}
	}

	// 4. Continuity supersession scan — group by cosine similarity, keep newest per cluster
	continuity, _ := e.store.GetDetailsByType(userID, "continuity")
	if len(continuity) > 1 {
		// Mark which IDs to keep (newest in each similarity cluster)
		kept := make(map[string]bool)
		demoted := make(map[string]bool)
		for i, c := range continuity {
			if demoted[c.ID] {
				continue
			}
			kept[c.ID] = true
			for j := i + 1; j < len(continuity); j++ {
				other := continuity[j]
				if demoted[other.ID] || kept[other.ID] {
					continue
				}
				sim := CosineSimilarity(c.Vector, other.Vector)
				if sim >= 0.75 {
					// c is newer (sorted by created_at DESC), demote other
					if opts.Verbose {
						report.Entries = append(report.Entries, GCEntry{
							ID: other.ID, Action: "demote", Reason: "superseded",
							Text: truncateGC(other.Text, 80),
						})
					}
					if !opts.DryRun {
						e.sketch.IngestRaw(ctx, userID, other.Text, other.Sector, other.SessionID, other.Vector)
						e.store.DeleteDetail(other.ID)
					}
					demoted[other.ID] = true
					report.SupersededContinuity++
				}
			}
		}
	}

	// 5. Access-cold demotion — details not accessed in 30+ days with importance < 0.8
	details, _ := e.store.GetDetailMemories(userID)
	for _, d := range details {
		if d.Type == "warning" {
			continue // never demote warnings
		}
		daysSinceAccess := now.Sub(d.LastAccessedAt).Hours() / 24
		if daysSinceAccess >= 30 && d.ImportanceScore < 0.8 {
			if opts.Verbose {
				report.Entries = append(report.Entries, GCEntry{
					ID: d.ID, Action: "demote", Reason: "access_cold",
					Text: truncateGC(d.Text, 80),
				})
			}
			if !opts.DryRun {
				e.sketch.IngestRaw(ctx, userID, d.Text, d.Sector, d.SessionID, d.Vector)
				e.store.DeleteDetail(d.ID)
			}
			report.AccessColdDetails++
		}
	}

	return report, nil
}

func truncateGC(s string, max int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > max {
		return s[:max-3] + "..."
	}
	return s
}

// Close shuts down the engine and all background workers.
func (e *Engine) Close() error {
	if e.pipeline != nil {
		e.pipeline.Stop()
	}
	return e.store.Close()
}

// notifyPipeline signals the compression pipeline that new sketch data is available.
func (e *Engine) notifyPipeline() {
	if e.pipeline != nil {
		e.pipeline.Notify()
	}
}

// --- Helpers ---

func estimateTokens(text string) int {
	return len(text) / 4
}

// typePriority returns sort priority for memory types.
// Higher = appears first in AssembleContext output.
func typePriority(t string) int {
	switch t {
	case "warning":
		return 2
	case "decision", "continuity":
		return 1
	default:
		return 0
	}
}

// formatTypeLabel creates the header label for a detail memory in assistant style.
func formatTypeLabel(memType, sector string, importance float64) string {
	switch memType {
	case "warning":
		return fmt.Sprintf("[⚠ Warning — %s (importance: %.2f)]", sector, importance)
	case "decision":
		return fmt.Sprintf("[Decision — %s (importance: %.2f)]", sector, importance)
	case "continuity":
		return fmt.Sprintf("[Continuity — %s (importance: %.2f)]", sector, importance)
	default:
		return fmt.Sprintf("[Memory — %s (importance: %.2f)]", sector, importance)
	}
}

// accessRecencyFactor returns a multiplier [0.5, 1.0] based on how recently
// a memory was accessed. Memories not accessed in 60+ days get 0.5.
// Warnings are exempt (always return 1.0) since they represent critical invariants.
func accessRecencyFactor(lastAccessed time.Time, memType string, now time.Time) float64 {
	if memType == "warning" {
		return 1.0
	}
	days := now.Sub(lastAccessed).Hours() / 24
	if days <= 0 {
		return 1.0
	}
	factor := 1.0 - (days / 60.0)
	if factor < 0.5 {
		factor = 0.5
	}
	return factor
}

func formatProfile(p *ProfileSketch) string {
	var parts []string
	if len(p.Interests) > 0 {
		parts = append(parts, "Interests: "+strings.Join(p.Interests, ", "))
	}
	if len(p.PersonalityTraits) > 0 {
		parts = append(parts, "Personality: "+strings.Join(p.PersonalityTraits, ", "))
	}
	if p.RelationshipHist != "" {
		parts = append(parts, "Relationship: "+p.RelationshipHist)
	}
	if len(p.KeyPreferences) > 0 {
		parts = append(parts, "Preferences: "+strings.Join(p.KeyPreferences, ", "))
	}
	if p.CommStyle != "" {
		parts = append(parts, "Communication: "+p.CommStyle)
	}
	return strings.Join(parts, "\n")
}
