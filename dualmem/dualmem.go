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
		tokenBudget = 2000 // default budget
	}

	results, err := e.DualSearch(ctx, userID, query, SearchOpts{
		Limit:         10,
		IncludeSketch: true,
	})
	if err != nil {
		return nil, err
	}

	var parts []string
	var sources []SourceRef
	tokensUsed := 0

	// Build set of stale memory IDs (populated by diff below)
	staleIDs := make(map[string]bool)

	// --- NEW: Structural diff (≤150 tokens) ---
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

			// Detect stale memories
			staleList := DetectStaleMemories(diff, e.store, userID)
			for _, id := range staleList {
				staleIDs[id] = true
			}
		}
	}

	// --- NEW: Code map (≤400 tokens, auto-generate if missing) ---
	if e.cfg.RootDir != "" && tokenBudget >= 500 {
		mapBudget := 400
		if tokenBudget-tokensUsed-200 < mapBudget {
			mapBudget = tokenBudget - tokensUsed - 200 // reserve 200 for memories
		}
		if mapBudget > 50 {
			codeMap := e.getOrGenerateCodeMap(userID)
			if codeMap != nil {
				mapText := codeMap.RenderAtBudget(mapBudget, nil, nil)
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

	// 3. Detail memories — sort by type priority (warnings first, then decisions)
	sort.SliceStable(results.DetailMemories, func(i, j int) bool {
		return typePriority(results.DetailMemories[i].Type) > typePriority(results.DetailMemories[j].Type)
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
func (e *Engine) getOrGenerateCodeMap(namespace string) *CodeMap {
	stored, _ := e.store.GetCodeMap(namespace)
	if stored != nil {
		return &CodeMap{
			Namespace:   stored.Namespace,
			RootDir:     stored.RootDir,
			Zoom1:       stored.Zoom1,
			Zoom2:       UnmarshalZoom2(stored.Zoom2JSON),
			GeneratedAt: stored.GeneratedAt,
			GitCommit:   stored.GitCommit,
		}
	}

	// Auto-generate
	cm, err := ScanCodebase(e.cfg.RootDir)
	if err != nil {
		return nil
	}
	cm.Namespace = namespace

	// Get current git commit
	_, commit := GetGitState(e.cfg.RootDir)
	cm.GitCommit = commit

	// Store for next time
	e.store.UpsertCodeMap(namespace, e.cfg.RootDir, cm.Zoom1, cm.MarshalZoom2(), cm.GitCommit)

	return cm
}

// StoreCodeMap persists a code map to the database. Called by the CLI `map` command.
func (e *Engine) StoreCodeMap(namespace string, cm *CodeMap) error {
	return e.store.UpsertCodeMap(namespace, cm.RootDir, cm.Zoom1, cm.MarshalZoom2(), cm.GitCommit)
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

// PromoteToDetail pins a sketch memory to the Detail Path.
func (e *Engine) PromoteToDetail(ctx context.Context, userID string, memoryID string) error {
	// This would require loading the sketch memory, embedding it, and inserting into detail.
	// Placeholder for now — would need a way to identify sketch items by ID.
	return fmt.Errorf("dualmem: PromoteToDetail not yet implemented")
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
