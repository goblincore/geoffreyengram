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
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Engine is the main DualMem implementation.
type Engine struct {
	mu             sync.RWMutex
	store          Store
	detail         *DetailPath
	sketch         *SketchPath
	pipeline       *Pipeline
	projector      *Projector
	embedder       EmbeddingProvider
	classifier     SectorClassifier
	extractor      EntityExtractor
	cfg            *Config
	OnScanProgress func(ScanProgress) // optional callback for scan progress reporting
}

// NewForCodeSearch creates a minimal Engine for code search only.
// No embedder, projector, or pipeline is initialized.
// Use this for CLI commands that only need GetCodeMap / ScanCodebase.
func NewForCodeSearch(cfg Config) (*Engine, error) {
	cfg.ApplyDefaults()

	store, err := NewSQLiteStore(cfg.SQLitePath)
	if err != nil {
		return nil, err
	}

	return &Engine{
		store: store,
		cfg:   &cfg,
	}, nil
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
	if e.pipeline != nil {
		e.pipeline.engine = e
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
		switch input.Type {
		case "trace":
			salience = 0.8 // exploration traces represent significant effort
		case "architecture":
			salience = 0.8 // architectural knowledge is high-value
		case "investigation":
			salience = 0.75 // research findings worth surfacing
		case "requirement":
			salience = 0.75 // disambiguated requirements are important
		default:
			salience = 0.5
		}
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

		// Link entities to graph (best-effort)
		if ns := e.namespace(); ns != "" && len(entities) > 0 {
			e.linkEntitiesToGraph(dm.ID, ns, entities)
		}

		// Build co-change edges from file associations (best-effort)
		if ns := e.namespace(); ns != "" && len(input.Files) >= 2 {
			e.buildCoChangeEdges(ns, dm.ID, input.Files)
		}

		// If a memory was demoted, route it to sketch
		if demoted != "" && demoted != content {
			e.sketch.IngestRaw(ctx, userID, demoted, sector, input.SessionID, nil)
			e.notifyPipeline()
		}

		// Type-aware supersession: demote older memories of the same type
		// that are semantically similar to the new one.
		e.supersedeByType(ctx, userID, dm.ID, input.Type, embedding)
	} else {
		// Route to Sketch Path
		if err := e.sketch.IngestRaw(ctx, userID, content, sector, input.SessionID, embedding); err != nil {
			return fmt.Errorf("dualmem: sketch ingest: %w", err)
		}
		e.notifyPipeline()
	}

	return nil
}

// namespace returns the engine's namespace derived from RootDir config.
func (e *Engine) namespace() string {
	if e.cfg.RootDir == "" {
		return ""
	}
	return "claude:" + filepath.Base(e.cfg.RootDir)
}

// linkEntitiesToGraph upserts entities into the graph and links them to a memory.
func (e *Engine) linkEntitiesToGraph(memoryID, namespace string, entities []Entity) {
	for _, ent := range entities {
		if ent.Text == "" {
			continue
		}
		entityID, err := e.store.UpsertEntity(&EntityNode{
			Name:      ent.Text,
			Type:      ent.Type,
			Namespace: namespace,
		})
		if err != nil {
			continue // best-effort: don't fail the add
		}
		e.store.LinkMemoryToEntity(memoryID, entityID, namespace)
	}
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

	// Compute entity graph boost if configured
	var graphBoostedIDs map[string]float64
	if opts.GraphBoost != nil && opts.QueryText != "" {
		graphBoostedIDs = e.computeGraphBoost(opts.GraphBoost, opts.QueryText)
	}

	details, err := e.detail.Search(ctx, queryEmb, userID, limit, opts.QueryText, graphBoostedIDs)
	if err != nil {
		return nil, fmt.Errorf("dualmem: detail search: %w", err)
	}
	// Apply minimum similarity threshold if configured
	if opts.MinSimilarity > 0 {
		filtered := details[:0]
		for _, dm := range details {
			if dm.Similarity >= opts.MinSimilarity {
				filtered = append(filtered, dm)
			}
		}
		details = filtered
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

// FileContext returns detail memories associated with a specific file.
// Only returns high-signal types (warning, decision, map). No embedding needed.
func (e *Engine) FileContext(ctx context.Context, userID string, filename string, limit int) ([]DetailMemory, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	basename := filepath.Base(filename)
	if limit <= 0 {
		limit = 5
	}

	rows, err := e.store.GetDetailsByFiles(userID, basename, []string{"warning", "decision", "map", "trace"}, limit)
	if err != nil {
		return nil, fmt.Errorf("dualmem: file context: %w", err)
	}

	result := make([]DetailMemory, len(rows))
	for i, r := range rows {
		result[i] = DetailMemory{
			ID:              r.ID,
			Text:            r.Text,
			ImportanceScore: r.ImportanceScore,
			Sector:          r.Sector,
			Entities:        r.Entities,
			Type:            r.Type,
			Files:           r.Files,
			Salience:        r.Salience,
			SessionID:       r.SessionID,
			CreatedAt:       r.CreatedAt,
		}
	}
	return result, nil
}

// GetWorkflowHintsForFile returns workflow hints for a single file.
func (e *Engine) GetWorkflowHintsForFile(ctx context.Context, userID string, filename string) ([]WorkflowHint, error) {
	return e.store.GetWorkflowHintsForFiles(userID, []string{filename})
}

// FileIndex returns all basenames that have associated high-signal memories.
// Used to generate the file index for the Read hook fast-path.
func (e *Engine) FileIndex(ctx context.Context, userID string) ([]string, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	files, err := e.store.GetFilesWithMemories(userID, []string{"warning", "decision", "map"})
	if err != nil {
		return nil, fmt.Errorf("dualmem: file index: %w", err)
	}

	seen := make(map[string]bool, len(files))
	for _, f := range files {
		seen[f] = true
	}

	// Also include files from knowledge docs
	docs, err := e.store.GetKnowledgeDocs(userID)
	if err != nil {
		return nil, fmt.Errorf("dualmem: file index (knowledge docs): %w", err)
	}
	for _, doc := range docs {
		for _, f := range doc.Files {
			base := filepath.Base(f)
			if !seen[base] {
				seen[base] = true
				files = append(files, base)
			}
		}
	}

	return files, nil
}

// Edge-type multipliers for structure-aware graph boosting.
// These weight how strongly an edge type should contribute to memory boost.
var edgeTypeMultiplier = map[string]float64{
	"depends_on": 1.0,
	"implements": 0.9,
	"modifies":   0.8,
	"uses":       0.7,
	"relates":    0.5,
}

// computeGraphBoost extracts entity candidates from query text, expands them
// through the entity graph, and returns a map of memory ID → boost weight
// for memories connected to those entities.
//
// Structure-aware: direct entity matches get full boost, expanded neighbors
// get a reduced boost scaled by edge type and edge strength.
func (e *Engine) computeGraphBoost(cfg *GraphBoostConfig, queryText string) map[string]float64 {
	boostWeight := cfg.BoostWeight
	if boostWeight <= 0 {
		boostWeight = 0.20
	}
	maxHops := cfg.MaxHops
	if maxHops <= 0 {
		maxHops = 1
	}
	ns := cfg.Namespace

	candidates := extractEntityCandidates(queryText)
	if len(candidates) == 0 {
		return nil
	}

	// Look up matching entity nodes for each candidate
	var matchedEntityIDs []string
	seen := make(map[string]bool)
	for _, name := range candidates {
		nodes, err := e.store.GetEntitiesByName(ns, name, 5)
		if err != nil {
			continue
		}
		for _, node := range nodes {
			if !seen[node.ID] {
				seen[node.ID] = true
				matchedEntityIDs = append(matchedEntityIDs, node.ID)
			}
		}
	}
	if len(matchedEntityIDs) == 0 {
		return nil
	}

	// Track which entities are direct matches vs expanded
	directEntitySet := make(map[string]bool, len(matchedEntityIDs))
	for _, eid := range matchedEntityIDs {
		directEntitySet[eid] = true
	}

	// Expand with edge info for structure-aware boosting
	allEntityIDs := make([]string, len(matchedEntityIDs))
	copy(allEntityIDs, matchedEntityIDs)

	// Map from expanded entity ID → best (relation, strength) for boost calculation
	expandedEdgeInfo := make(map[string]ExpandedEntityEdge)

	for hop := 0; hop < maxHops; hop++ {
		expanded, err := e.store.ExpandEntitiesWithEdges(ns, allEntityIDs, 10)
		if err != nil {
			break
		}
		for _, edge := range expanded {
			if !seen[edge.NeighborID] {
				seen[edge.NeighborID] = true
				allEntityIDs = append(allEntityIDs, edge.NeighborID)
				expandedEdgeInfo[edge.NeighborID] = edge
			}
		}
	}

	// Collect memory IDs linked to direct entities (full boost)
	directMemIDs, err := e.store.GetMemoryIDsByEntities(matchedEntityIDs)
	if err != nil {
		directMemIDs = nil
	}

	if len(directMemIDs) == 0 && len(expandedEdgeInfo) == 0 {
		return nil
	}

	result := make(map[string]float64)

	// Direct matches get full boost weight
	for _, mid := range directMemIDs {
		result[mid] = boostWeight
	}

	// Expanded matches: query per-entity so we can apply the correct edge multiplier
	expandedBase := boostWeight * 0.5
	for eid, edge := range expandedEdgeInfo {
		memIDs, err := e.store.GetMemoryIDsByEntities([]string{eid})
		if err != nil || len(memIDs) == 0 {
			continue
		}
		mult := 0.5 // default for unknown edge types
		if m, ok := edgeTypeMultiplier[edge.Relation]; ok {
			mult = m * edge.Strength
		}
		boost := expandedBase * mult
		for _, mid := range memIDs {
			if _, alreadyDirect := result[mid]; alreadyDirect {
				continue // direct match takes precedence
			}
			if boost > result[mid] {
				result[mid] = boost // keep best boost per memory
			}
		}
	}

	return result
}

// extractEntityCandidates returns normalized entity name candidates from query text.
// Produces unigrams (single words >= 2 chars) and bigrams (adjacent word pairs).
func extractEntityCandidates(query string) []string {
	words := strings.Fields(strings.ToLower(query))
	// Filter out very short/common words for entity lookup
	var filtered []string
	for _, w := range words {
		w = strings.Trim(w, ".,;:!?\"'()[]")
		if len(w) >= 2 {
			filtered = append(filtered, w)
		}
	}

	// Unigrams
	candidates := make([]string, len(filtered))
	copy(candidates, filtered)

	// Bigrams (for multi-word entity names like "auth middleware")
	for i := 0; i+1 < len(filtered); i++ {
		candidates = append(candidates, filtered[i]+" "+filtered[i+1])
	}

	return candidates
}

// GetStructuralNeighborPaths returns a map of file path -> blended score for paths
// reachable from the given seed paths via the structural edge graph.
// Uses Unravel's blended scoring: score = parentScore * 0.7 + edgeWeight * 0.3
// with beam search (top-K per hop, prune below threshold).
func (e *Engine) GetStructuralNeighborPaths(namespace string, seedPaths []string, maxHops int) map[string]float64 {
	if len(seedPaths) == 0 {
		return nil
	}
	if maxHops <= 0 {
		maxHops = 2
	}

	const (
		beamWidth      = 5   // top-K paths per hop
		pruneThreshold = 0.2 // minimum score to continue expansion
		parentDecay    = 0.7 // parent score contribution
		edgeContrib    = 0.3 // edge weight contribution
	)

	// Initialize seed paths with score 1.0
	scores := make(map[string]float64)
	for _, p := range seedPaths {
		scores[p] = 1.0
	}

	// Multi-hop expansion with beam search
	frontier := make([]string, len(seedPaths))
	copy(frontier, seedPaths)

	for hop := 0; hop < maxHops; hop++ {
		type candidate struct {
			path  string
			score float64
		}
		var candidates []candidate

		for _, path := range frontier {
			parentScore := scores[path]
			if parentScore < pruneThreshold {
				continue
			}

			neighbors, err := e.store.GetStructuralNeighbors(namespace, path, nil, 20)
			if err != nil {
				continue
			}

			for _, edge := range neighbors {
				// Determine the neighbor path (the other end of the edge)
				neighborPath := edge.TargetPath
				if edge.TargetPath == path {
					neighborPath = edge.SourcePath
				}

				// Blended score: parentScore * decay + edgeWeight * contrib
				score := parentScore*parentDecay + edge.Weight*edgeContrib

				if score >= pruneThreshold {
					candidates = append(candidates, candidate{path: neighborPath, score: score})
				}
			}
		}

		if len(candidates) == 0 {
			break
		}

		// Beam search: sort by score, keep top-K
		sort.Slice(candidates, func(i, j int) bool {
			return candidates[i].score > candidates[j].score
		})
		if len(candidates) > beamWidth {
			candidates = candidates[:beamWidth]
		}

		// Update scores and build next frontier
		frontier = frontier[:0]
		for _, c := range candidates {
			if existing, ok := scores[c.path]; !ok || c.score > existing {
				scores[c.path] = c.score
				frontier = append(frontier, c.path)
			}
		}
	}

	// Remove seed paths from result (we only want the expansion)
	for _, p := range seedPaths {
		delete(scores, p)
	}

	return scores
}

// Annotation represents a piece of context associated with a file path.
type Annotation struct {
	FilePath string  `json:"file_path"`
	Type     string  `json:"type"` // "decision", "warning", "continuity", "knowledge", "checkpoint", "structure"
	Text     string  `json:"text"`
	Source   string  `json:"source"` // "memory", "knowledge_doc", "checkpoint"
	Salience float64 `json:"salience"`
	MemoryID string  `json:"memory_id,omitempty"` // for dedup tracking
}

// FileAnnotations retrieves annotations from ALL sources for a set of file paths.
// It queries detail memories, knowledge docs, and checkpoints, returning
// high-signal context grouped by file. Does not query codemap — that's handled
// separately by the smart toggle.
func (e *Engine) FileAnnotations(ctx context.Context, namespace string, filePaths []string, limit int) []Annotation {
	if len(filePaths) == 0 || limit <= 0 {
		return nil
	}

	e.mu.RLock()
	defer e.mu.RUnlock()

	// Build a set of requested basenames for matching
	requestedBases := make(map[string]bool, len(filePaths))
	requestedFull := make(map[string]bool, len(filePaths))
	for _, p := range filePaths {
		requestedBases[filepath.Base(p)] = true
		requestedFull[p] = true
	}

	// Track seen memory IDs for dedup
	seenIDs := make(map[string]bool)
	var annotations []Annotation

	// 1. Detail memories — high-signal types
	highSignalTypes := []string{"warning", "decision", "map", "trace"}
	for _, fp := range filePaths {
		basename := filepath.Base(fp)
		rows, err := e.store.GetDetailsByFiles(namespace, basename, highSignalTypes, limit*2)
		if err != nil {
			continue
		}
		for _, dm := range rows {
			if seenIDs[dm.ID] {
				continue
			}
			seenIDs[dm.ID] = true

			// Find which of the requested files this memory covers
			matchedFile := ""
			for _, mf := range dm.Files {
				if requestedBases[filepath.Base(mf)] || requestedFull[mf] {
					matchedFile = filepath.Base(mf)
					break
				}
			}
			if matchedFile == "" {
				matchedFile = fp
			}

			annotations = append(annotations, Annotation{
				FilePath: matchedFile,
				Type:     dm.Type,
				Text:     dm.Text,
				Source:   "memory",
				Salience: dm.Salience,
				MemoryID: dm.ID,
			})
		}
	}

	// 2. Knowledge docs — match by files field overlap
	docs, err := e.store.GetKnowledgeDocs(namespace)
	if err == nil {
		for _, doc := range docs {
			if seenIDs[doc.ID] {
				continue
			}
			for _, df := range doc.Files {
				if requestedBases[filepath.Base(df)] || requestedFull[df] {
					seenIDs[doc.ID] = true
					annotations = append(annotations, Annotation{
						FilePath: filepath.Base(df),
						Type:     "knowledge",
						Text:     doc.Content,
						Source:   "knowledge_doc",
						Salience: 0.8, // knowledge docs are synthesized → high salience
						MemoryID: doc.ID,
					})
					break // one annotation per doc
				}
			}
		}
	}

	// 3. Checkpoints — filter by file path overlap
	checkpoints, _ := e.GetCheckpoints(ctx, namespace)
	for _, cp := range checkpoints {
		cpID := "cp_" + cp.Task
		if seenIDs[cpID] {
			continue
		}
		for _, cf := range cp.FilesActive {
			// Strip line ranges (e.g., "auth.go:42-80" → "auth.go")
			base := cf
			if idx := strings.Index(cf, ":"); idx > 0 {
				base = cf[:idx]
			}
			base = filepath.Base(base)
			if requestedBases[base] {
				seenIDs[cpID] = true
				annotations = append(annotations, Annotation{
					FilePath: base,
					Type:     "checkpoint",
					Text:     cp.FormatForContext(),
					Source:   "checkpoint",
					Salience: 0.8,
					MemoryID: cpID,
				})
				break
			}
		}
	}

	// 4. Workflow hints — lightweight pointers to autopilot workflow memories
	workflowHints, _ := e.store.GetWorkflowHintsForFiles(namespace, filePaths)
	for _, wh := range workflowHints {
		whID := "wh_" + wh.WorkflowID
		if seenIDs[whID] {
			continue
		}
		seenIDs[whID] = true
		ticketStr := ""
		if len(wh.Tickets) > 0 {
			ticketStr = " (" + strings.Join(wh.Tickets, ", ") + ")"
		}
		hintText := fmt.Sprintf(`"%s"%s — search "workflow:%s" for full detail`, wh.Summary, ticketStr, wh.WorkflowID)
		if len(hintText) > 200 {
			hintText = hintText[:197] + "..."
		}
		annotations = append(annotations, Annotation{
			FilePath: wh.MatchedFile,
			Type:     "workflow",
			Text:     hintText,
			Source:   "workflow_hint",
			Salience: 0.85,
			MemoryID: whID,
		})
	}

	// Sort by salience descending
	sort.Slice(annotations, func(i, j int) bool {
		return annotations[i].Salience > annotations[j].Salience
	})

	// Apply limit
	if len(annotations) > limit {
		annotations = annotations[:limit]
	}

	return annotations
}

// AssembleContext is the key differentiator: token-budget-aware context assembly.
// It retrieves from both paths and formats a structured context block that fits
// within the given token budget. If Config.RootDir is set, includes structural
// diffs and code maps before memories.
// ContextOpts configures AssembleContext behavior.
type ContextOpts struct {
	Intent            Intent  // Explicit intent override (empty = auto-detect from query)
	MinSimilarity     float64 // If > 0, exclude detail memories below this cosine similarity
	DisableGraphBoost bool    // If true, skip entity graph boost in search
}

// AssembleOptions selects between the v2 pinned-block assembly (default) and
// the v1 legacy assembly, and tunes the v2 pinned block. See
// docs/superpowers/plans/2026-07-04-dualmem-v2.md ("Assembly v2").
//
// The v2 pinned block is a tiny (<500 token), LLM-free, scan-free context
// header: latest handoff, user-global preferences, facts touching changed
// files, and a one-line codemap status. Deep retrieval moves to pull tools
// (task 6). Set Legacy=true to opt back into the v1 token-budgeted block
// (kept verbatim until task 9 deletes the v1 layer).
type AssembleOptions struct {
	// Legacy selects the v1 assembly path. False (zero value) = v2 pinned
	// block, which is the production default.
	Legacy bool

	// SessionID, when set on the v2 path, attributes the pinned block's served
	// facts for instrumentation (task 7): every fact emitted into the block is
	// logged to served_facts so the distill-time file-touch correlator can
	// credit hits. Empty = the block is still assembled, but its facts are not
	// logged (use during one-off or test runs).
	SessionID string

	// PinnedBudget is the hard token cap for the v2 pinned block. Defaults to
	// DefaultPinnedBudget when zero or negative.
	PinnedBudget int

	// ChangedFactCap limits how many changed-file facts item 3 may emit. Zero
	// means DefaultChangedFactCap.
	ChangedFactCap int
}

// DefaultPinnedBudget is the hard cap on the v2 pinned block. See plan §
// "Assembly v2" — 500 tokens is a placeholder to be tuned after task 7
// instrumentation lands.
const DefaultPinnedBudget = 500

// DefaultChangedFactCap bounds item 3 (facts touching changed files) so a
// large diff can't crowd out the handoff and preferences.
const DefaultChangedFactCap = 6

// Assemble is the v2-default context assembly entry point. With the zero-value
// AssembleOptions (Legacy=false) it emits the v2 pinned block; with Legacy=true
// it delegates to the v1 token-budgeted block (AssembleContextWith).
//
// AssembleContext / AssembleContextWith remain as v1-only entry points so
// existing callers and the SWR regression tests keep their exact behavior.
func (e *Engine) Assemble(ctx context.Context, userID, query string, tokenBudget int, opts AssembleOptions) (*ContextBlock, error) {
	if opts.Legacy {
		return e.AssembleContextWith(ctx, userID, query, tokenBudget, nil)
	}
	return e.assemblePinnedV2(ctx, userID, query, opts)
}

func (e *Engine) AssembleContext(ctx context.Context, userID string, query string, tokenBudget int) (*ContextBlock, error) {
	return e.AssembleContextWith(ctx, userID, query, tokenBudget, nil)
}

func (e *Engine) AssembleContextWith(ctx context.Context, userID string, query string, tokenBudget int, opts *ContextOpts) (*ContextBlock, error) {
	if tokenBudget <= 0 {
		tokenBudget = 2000
	}

	// Resolve intent: explicit override > auto-detect > default
	intent := IntentDefault
	if opts != nil && opts.Intent != "" {
		intent = opts.Intent
	} else {
		intent = DetectIntent(query)
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

	var minSim float64
	if opts != nil {
		minSim = opts.MinSimilarity
	}
	searchOpts := SearchOpts{
		Limit:          10,
		IncludeSketch:  true,
		QueryEmbedding: queryEmb,
		MinSimilarity:  minSim,
		QueryText:      query,
	}
	if opts == nil || !opts.DisableGraphBoost {
		searchOpts.GraphBoost = &GraphBoostConfig{
			Namespace:   userID,
			BoostWeight: 0.15,
			MaxHops:     1,
		}
	}
	results, err := e.DualSearch(ctx, userID, query, searchOpts)
	if err != nil {
		return nil, err
	}

	var parts []string
	var sources []SourceRef
	tokensUsed := 0
	staleIDs := make(map[string]bool)
	coveredIDs := make(map[string]bool) // tracks memory IDs already rendered (dedup across sections)

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

	// --- Load checkpoints early (for file hints) ---
	checkpoints, _ := e.GetCheckpoints(ctx, userID)

	// --- Code map (smart toggle: only show on cold start / explore) ---
	showCodemap := len(checkpoints) == 0 && (intent == IntentExplore || intent == IntentDefault)
	if showCodemap && e.cfg.RootDir != "" && tokenBudget >= 500 {
		mapBudget := 400
		if tokenBudget-tokensUsed-200 < mapBudget {
			mapBudget = tokenBudget - tokensUsed - 200
		}
		if mapBudget > 50 {
			codeMap, codeIdx := e.getOrGenerateCodeMap(ctx, userID)
			if codeMap != nil {
				codemapQuery := HDCEncodeQuery(query)
				var moduleEmbs map[string][]float32
				if codeIdx != nil {
					moduleEmbs = codeIdx.HDCVectors
				}
				boostPaths := extractFileHints(checkpoints, results.DetailMemories)
				var cochangeNeighbors map[string]float64
				if ns := e.namespace(); ns != "" && len(boostPaths) > 0 {
					cochangeNeighbors = e.GetCoChangeForPaths(ns, boostPaths, 1.0)
					structuralNeighbors := e.GetStructuralNeighborPaths(ns, boostPaths, 2)
					if cochangeNeighbors == nil {
						cochangeNeighbors = structuralNeighbors
					} else {
						for path, score := range structuralNeighbors {
							if existing, ok := cochangeNeighbors[path]; !ok || score > existing {
								cochangeNeighbors[path] = score
							}
						}
					}
				}
				useBoostOnly := intent == IntentContinue && len(boostPaths) > 0
				mapText := codeMap.RenderAtBudgetWithCoChange(mapBudget, codemapQuery, moduleEmbs, boostPaths, cochangeNeighbors, useBoostOnly)
				mapTokens := estimateTokens(mapText)
				header := "[Codebase Map]"
				if codeMap.Stale {
					if codeMap.CommitsBehind > 0 {
						header = fmt.Sprintf("[Codebase Map — %d commits behind, run `dualmem index` to refresh]", codeMap.CommitsBehind)
					} else {
						header = "[Codebase Map — stale, run `dualmem index` to refresh]"
					}
				}
				parts = append(parts, header+"\n"+mapText)
				sources = append(sources, SourceRef{Type: "codemap", ID: userID})
				tokensUsed += mapTokens
			}
		}
	}

	// 0. Checkpoints — structured session handoffs, rendered after codemap
	for _, cp := range checkpoints {
		cpText := cp.FormatForContext()
		cpTokens := estimateTokens(cpText)
		if tokensUsed+cpTokens <= tokenBudget {
			parts = append(parts, cpText)
			sources = append(sources, SourceRef{Type: "checkpoint", ID: cp.Task})
			tokensUsed += cpTokens
		}
	}

	// Workflow Hints — ticket-prefix lookup from active checkpoints
	ticketPrefixes := extractTicketsFromCheckpoints(checkpoints)
	if len(ticketPrefixes) > 0 {
		workflowHints, _ := e.store.GetWorkflowHintsForTickets(userID, ticketPrefixes)
		if len(workflowHints) > 0 {
			var hintLines []string
			for _, wh := range workflowHints {
				ticketStr := ""
				if len(wh.Tickets) > 0 {
					ticketStr = " (" + strings.Join(wh.Tickets, ", ") + ")"
				}
				hintLines = append(hintLines, fmt.Sprintf(`📎 "%s"%s — search "workflow:%s"`, wh.Summary, ticketStr, wh.WorkflowID))
			}
			whText := "[Workflow Hints]\n" + strings.Join(hintLines, "\n")
			whTokens := estimateTokens(whText)
			if tokensUsed+whTokens <= tokenBudget {
				parts = append(parts, whText)
				sources = append(sources, SourceRef{Type: "workflow_hint", ID: "tickets"})
				tokensUsed += whTokens
			}
		}
	}

	// --- File Context — file-centric annotation retrieval ---
	// Collect relevant file paths from checkpoints and detail memories,
	// then pull ALL annotations for those files (memories, knowledge docs, checkpoints).
	fileContextPaths := extractFileHints(checkpoints, results.DetailMemories)
	if len(fileContextPaths) > 0 {
		fileAnns := e.FileAnnotations(ctx, userID, fileContextPaths, 20)
		if len(fileAnns) > 0 {
			// Group annotations by file path
			grouped := make(map[string][]Annotation)
			var fileOrder []string
			for _, ann := range fileAnns {
				if _, exists := grouped[ann.FilePath]; !exists {
					fileOrder = append(fileOrder, ann.FilePath)
				}
				grouped[ann.FilePath] = append(grouped[ann.FilePath], ann)
			}

			var fcParts []string
			for _, fp := range fileOrder {
				anns := grouped[fp]
				var entry strings.Builder
				entry.WriteString(fp + ":")
				for _, ann := range anns {
					// Truncate long text for inline display
					text := ann.Text
					if len(text) > 200 {
						text = text[:197] + "..."
					}
					text = strings.ReplaceAll(text, "\n", " ")
					entry.WriteString(fmt.Sprintf("\n  [%s] %s", ann.Type, text))
				}
				fcParts = append(fcParts, entry.String())
			}
			fcText := "[File Context]\n" + strings.Join(fcParts, "\n")
			fcTokens := estimateTokens(fcText)
			if tokensUsed+fcTokens <= tokenBudget {
				parts = append(parts, fcText)
				tokensUsed += fcTokens
				// Track which memory IDs were rendered to avoid duplication
				for _, ann := range fileAnns {
					if ann.MemoryID != "" {
						coveredIDs[ann.MemoryID] = true
						sources = append(sources, SourceRef{Type: "detail", ID: ann.MemoryID})
					}
				}
			}
		}
	}

	// 1. Knowledge docs — synthesized concept documents, ranked by query relevance.
	// These replace raw memories for topics that have been synthesized.
	kdocs, _ := e.GetKnowledgeDocs(ctx, userID, query, queryEmb, 0)
	if len(kdocs) > 0 {
		// Budget: knowledge docs get up to 60% of remaining budget
		kdocBudget := (tokenBudget - tokensUsed) * 60 / 100
		kdocUsed := 0
		for _, kd := range kdocs {
			kdTokens := estimateTokens(kd.FormatForContext())
			if kdocUsed+kdTokens > kdocBudget {
				break
			}
			parts = append(parts, kd.FormatForContext())
			sources = append(sources, SourceRef{Type: "knowledge", ID: kd.Topic})
			tokensUsed += kdTokens
			kdocUsed += kdTokens
			// Mark source memories as covered so they don't duplicate
			for _, id := range kd.SourceIDs {
				coveredIDs[id] = true
			}
		}
	}

	// 2. Profile sketch (~50 tokens) — always include if available
	if results.Profile != nil {
		profileText := formatProfile(results.Profile)
		profileTokens := estimateTokens(profileText)
		if tokensUsed+profileTokens <= tokenBudget {
			parts = append(parts, "[User Profile]\n"+profileText)
			sources = append(sources, SourceRef{Type: "profile", ID: results.Profile.UserID})
			tokensUsed += profileTokens
		}
	}

	// 3. Narrative arcs (~100-200 tokens each)
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

	// 4. Detail memories — only uncovered ones (not already in a knowledge doc).
	// Sorted by type priority with access-recency weighting and intent-aware multipliers.
	now := time.Now()
	var intentProfile *IntentProfile
	if profile, ok := IntentProfiles[intent]; ok {
		intentProfile = &profile
	}

	sort.SliceStable(results.DetailMemories, func(i, j int) bool {
		si := detailSortScore(results.DetailMemories[i], intentProfile, now, query)
		sj := detailSortScore(results.DetailMemories[j], intentProfile, now, query)
		return si > sj
	})
	for _, dm := range results.DetailMemories {
		if dm.Type == "checkpoint" {
			continue // already rendered as structured block above
		}
		if coveredIDs[dm.ID] {
			continue // already covered by a knowledge doc
		}
		// Filter expired anticipation memories (2-hour TTL)
		if dm.Type == "anticipation" && time.Since(dm.CreatedAt) > 2*time.Hour {
			continue
		}
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

	// 5. Recent episodes (if budget remains)
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

	// If no meaningful content was assembled (no memories, no knowledge docs, no checkpoints),
	// suggest running codebase-onboard for initial context.
	hasMemories := len(results.DetailMemories) > 0
	hasKnowledge := len(kdocs) > 0
	hasCheckpoints := len(checkpoints) > 0
	if !hasMemories && !hasKnowledge && !hasCheckpoints {
		parts = append(parts, "[No cross-session memories found. Run /codebase-onboard for initial context.]")
	}

	// Knowledge gap hint: if codemap exists but no knowledge docs cover this namespace,
	// suggest consult for deeper understanding.
	hasCodemap := false
	if e.cfg.RootDir != "" {
		cm, _ := e.getOrGenerateCodeMap(ctx, userID)
		if cm != nil {
			hasCodemap = true
		}
	}
	if hasCodemap && !hasKnowledge {
		parts = append(parts, "[Tip: Run `dualmem consult \"topic\"` for deeper understanding of this codebase's subsystems]")
	}

	text := strings.Join(parts, "\n\n")

	// Record session marker for next time
	if e.cfg.RootDir != "" {
		e.recordSessionMarker(userID)
	}

	block := &ContextBlock{
		Text:       text,
		TokenCount: tokensUsed,
		Sources:    sources,
		Intent:     intent,
	}

	// Persist context snapshot for retrospective rating.
	// Best-effort — snapshot failure must not break context assembly.
	snapID := fmt.Sprintf("snap_%d", time.Now().UnixNano())
	snapshot := BuildSnapshot(snapID, userID, query, block, results.DetailMemories, time.Now())
	if err := e.SaveSnapshot(snapshot); err == nil {
		block.SnapshotID = snapID
	}

	return block, nil
}

// extractFileHints collects file paths from checkpoints and detail memories
// to boost codemap modules that are relevant to ongoing work.
func extractFileHints(checkpoints []Checkpoint, memories []DetailMemory) []string {
	seen := make(map[string]bool)
	var hints []string

	for _, cp := range checkpoints {
		for _, f := range cp.FilesActive {
			// Strip line ranges (e.g., "auth.go:42-80" → "auth.go")
			if idx := strings.Index(f, ":"); idx > 0 {
				f = f[:idx]
			}
			f = strings.TrimSpace(f)
			if f != "" && !seen[f] {
				seen[f] = true
				hints = append(hints, f)
			}
		}
	}
	for _, dm := range memories {
		for _, f := range dm.Files {
			f = strings.TrimSpace(f)
			if f != "" && !seen[f] {
				seen[f] = true
				hints = append(hints, f)
			}
		}
	}
	return hints
}

// extractTicketsFromCheckpoints scans checkpoint Task and Text fields for ticket prefixes.
// Returns deduplicated ticket IDs like ["LC-1635", "LC-1729"].
func extractTicketsFromCheckpoints(checkpoints []Checkpoint) []string {
	re := regexp.MustCompile(`[A-Z]+-\d+`)
	seen := make(map[string]bool)
	var tickets []string

	for _, cp := range checkpoints {
		for _, m := range re.FindAllString(cp.Task, -1) {
			if !seen[m] {
				seen[m] = true
				tickets = append(tickets, m)
			}
		}
		for _, step := range cp.CompletedSteps {
			for _, m := range re.FindAllString(step, -1) {
				if !seen[m] {
					seen[m] = true
					tickets = append(tickets, m)
				}
			}
		}
		for _, step := range cp.RemainingSteps {
			for _, m := range re.FindAllString(step, -1) {
				if !seen[m] {
					seen[m] = true
					tickets = append(tickets, m)
				}
			}
		}
	}

	return tickets
}

// AssembleContextIndex returns a compact index of available context items
// without rendering their full text. This implements progressive disclosure:
// the agent sees what exists and the token cost, then fetches specific items
// via ShowItems. Much cheaper than AssembleContext for session-start probing.
func (e *Engine) AssembleContextIndex(ctx context.Context, userID string, query string, tokenBudget int, opts *ContextOpts) (*ContextIndex, error) {
	if tokenBudget <= 0 {
		tokenBudget = 2000
	}

	intent := IntentDefault
	if opts != nil && opts.Intent != "" {
		intent = opts.Intent
	} else {
		intent = DetectIntent(query)
	}

	// Embed query for search
	var queryEmb []float32
	if query != "" {
		var err error
		queryEmb, err = e.embedder.Embed(ctx, query, "RETRIEVAL_QUERY")
		if err != nil {
			return nil, fmt.Errorf("dualmem: embed query: %w", err)
		}
	}

	var minSim float64
	if opts != nil {
		minSim = opts.MinSimilarity
	}
	searchOpts := SearchOpts{
		Limit:          20, // fetch more candidates for index
		IncludeSketch:  true,
		QueryEmbedding: queryEmb,
		MinSimilarity:  minSim,
		QueryText:      query,
	}
	if opts == nil || !opts.DisableGraphBoost {
		searchOpts.GraphBoost = &GraphBoostConfig{
			Namespace:   userID,
			BoostWeight: 0.15,
			MaxHops:     1,
		}
	}
	results, err := e.DualSearch(ctx, userID, query, searchOpts)
	if err != nil {
		return nil, err
	}

	var items []IndexEntry
	totalTokens := 0

	// Checkpoints
	checkpoints, _ := e.GetCheckpoints(ctx, userID)
	for _, cp := range checkpoints {
		cpText := cp.FormatForContext()
		toks := estimateTokens(cpText)
		items = append(items, IndexEntry{
			ID:         "chk_" + sanitizeID(cp.Task),
			Type:       "checkpoint",
			TypeIcon:   "📋",
			Title:      truncateTitle(cpText, 60),
			TokenCount: toks,
			Files:      cp.FilesActive,
		})
		totalTokens += toks
	}

	// Knowledge docs
	kdocs, _ := e.GetKnowledgeDocs(ctx, userID, query, queryEmb, 0)
	coveredIDs := make(map[string]bool)
	for _, kd := range kdocs {
		toks := kd.TokenCount
		if toks == 0 {
			toks = estimateTokens(kd.FormatForContext())
		}
		items = append(items, IndexEntry{
			ID:         "kdoc_" + sanitizeID(kd.Topic),
			Type:       "knowledge",
			TypeIcon:   "📖",
			Title:      truncateTitle(kd.Content, 60),
			TokenCount: toks,
			Files:      kd.Files,
		})
		totalTokens += toks
		for _, id := range kd.SourceIDs {
			coveredIDs[id] = true
		}
	}

	// Detail memories — sorted by importance, excluding covered and checkpoints
	now := time.Now()
	var intentProfile *IntentProfile
	if profile, ok := IntentProfiles[intent]; ok {
		intentProfile = &profile
	}
	sort.SliceStable(results.DetailMemories, func(i, j int) bool {
		si := detailSortScore(results.DetailMemories[i], intentProfile, now, query)
		sj := detailSortScore(results.DetailMemories[j], intentProfile, now, query)
		return si > sj
	})
	for _, dm := range results.DetailMemories {
		if dm.Type == "checkpoint" || coveredIDs[dm.ID] {
			continue
		}
		toks := estimateTokens(dm.Text) + estimateTokens(strings.Join(dm.Files, ", "))
		icon := typeIcon(dm.Type)
		typeLabel := dm.Type
		if typeLabel == "" {
			typeLabel = "memory"
		}
		items = append(items, IndexEntry{
			ID:         dm.ID,
			Type:       typeLabel,
			TypeIcon:   icon,
			Title:      truncateTitle(dm.Text, 60),
			TokenCount: toks,
			Files:      dm.Files,
		})
		totalTokens += toks
	}

	// Structural items: summarize availability without detailed entries
	var alsoItems []string
	if len(results.Episodes) > 0 {
		epTokens := 0
		for _, ep := range results.Episodes {
			epTokens += estimateTokens(ep.SummaryText)
		}
		alsoItems = append(alsoItems, fmt.Sprintf("%d episodes (~%d tokens)", len(results.Episodes), epTokens))
		totalTokens += epTokens
	}
	if len(results.Arcs) > 0 {
		arcTokens := 0
		for _, arc := range results.Arcs {
			arcTokens += estimateTokens(arc.SummaryText)
		}
		alsoItems = append(alsoItems, fmt.Sprintf("%d arcs (~%d tokens)", len(results.Arcs), arcTokens))
		totalTokens += arcTokens
	}
	if e.cfg.RootDir != "" {
		alsoItems = append(alsoItems, "codemap available")
	}

	also := ""
	if len(alsoItems) > 0 {
		also = strings.Join(alsoItems, ", ")
	}

	idx := &ContextIndex{
		Items:         items,
		TotalTokens:   totalTokens,
		Intent:        intent,
		AlsoAvailable: also,
	}

	// Persist a context snapshot for the rating pipeline.
	// All index items are recorded as candidates — ShowItems will later
	// mark which ones were actually fetched (implicit relevance signal).
	snapID := fmt.Sprintf("snap_%d", time.Now().UnixNano())
	snapshot := buildIndexSnapshot(snapID, userID, query, items, results.DetailMemories, now)
	if err := e.SaveSnapshot(snapshot); err == nil {
		idx.SnapshotID = snapID
	}

	return idx, nil
}

// Consult returns a structured intelligence report about a codebase subsystem or concept.
// It checks for a cached knowledge doc matching the query. If found (cosine ≥ 0.75),
// serves it. If not, gathers structural evidence and synthesizes a new knowledge doc
// via LLM, caching it for future queries.
func (e *Engine) Consult(ctx context.Context, namespace, query string, budget int) (*ConsultReport, error) {
	if budget <= 0 {
		budget = 2000
	}

	intent := DetectIntent(query)

	// 1. Embed query
	queryEmb, err := e.embedder.Embed(ctx, query, "RETRIEVAL_QUERY")
	if err != nil {
		return nil, fmt.Errorf("consult: embed query: %w", err)
	}

	// 2. Knowledge doc lookup
	kdocs, err := e.GetKnowledgeDocs(ctx, namespace, query, queryEmb, 3)
	if err != nil {
		return nil, fmt.Errorf("consult: get knowledge docs: %w", err)
	}

	var explanation string
	var docTopic string
	var docMatchScore float64
	var synthesized bool

	if len(kdocs) > 0 {
		// Check cosine similarity of top doc against query
		if len(kdocs[0].Embedding) > 0 && len(queryEmb) > 0 {
			docMatchScore = CosineSimilarity(kdocs[0].Embedding, queryEmb)
		}
	}

	// 3. Gather structural evidence (always, for sections 2 & 3)
	var seedPaths []string
	var rankedFiles []RankedFile

	// HDC codemap search for relevant modules
	codeMap, codeIdx := e.getOrGenerateCodeMap(ctx, namespace)
	if codeMap != nil {
		hdcQuery := HDCEncodeQuery(query)
		var moduleEmbs map[string][]float32
		if codeIdx != nil {
			moduleEmbs = codeIdx.HDCVectors
		}
		// Score all modules
		for _, mod := range codeMap.Zoom2 {
			if emb, ok := moduleEmbs[mod.Path]; ok {
				score := CosineSimilarity(hdcQuery, emb)
				if score > 0.3 {
					rankedFiles = append(rankedFiles, RankedFile{
						Path:   mod.Path,
						Score:  float64(score),
						Source: "hdc",
						Desc:   mod.Summary,
					})
					seedPaths = append(seedPaths, mod.Path)
				}
			}
		}
		// Sort by score descending
		sort.Slice(rankedFiles, func(i, j int) bool {
			return rankedFiles[i].Score > rankedFiles[j].Score
		})
		// Keep top 10
		if len(rankedFiles) > 10 {
			rankedFiles = rankedFiles[:10]
		}
		if len(seedPaths) > 10 {
			seedPaths = seedPaths[:10]
		}
	}

	// Structural neighbors via call/import edges
	structuralNeighbors := e.GetStructuralNeighborPaths(namespace, seedPaths, 2)

	// Co-change neighbors
	cochangeNeighbors := e.GetCoChangeForPaths(namespace, seedPaths, 0.5)

	// Get structural edges for call graph rendering
	var callEdges []StructuralEdge
	for _, path := range seedPaths {
		edges, err := e.store.GetStructuralEdgesForPath(namespace, path, 50)
		if err == nil {
			callEdges = append(callEdges, edges...)
		}
	}

	// Build evidence text
	evidenceText := formatCallGraph(callEdges, cochangeNeighbors)

	// Merge structural + co-change files into ranked list
	seen := make(map[string]bool)
	for _, f := range rankedFiles {
		seen[f.Path] = true
	}
	for path, score := range structuralNeighbors {
		if !seen[path] {
			rankedFiles = append(rankedFiles, RankedFile{Path: path, Score: score, Source: "structural"})
			seen[path] = true
		}
	}
	for path, score := range cochangeNeighbors {
		if !seen[path] {
			rankedFiles = append(rankedFiles, RankedFile{Path: path, Score: score, Source: "co-change"})
			seen[path] = true
		}
	}

	// 3.5 Read code evidence for grounded synthesis
	var codeEvidence *CodeEvidence
	if codeMap != nil {
		codeEvidence, _ = e.ReadCodeEvidence(ctx, namespace, query, ReadCodeOpts{
			MaxTokens: budget / 2,
			MaxFiles:  6,
			SeedPaths: seedPaths,
		}, codeMap, codeIdx)
	}

	// 4. Search memories for context
	searchOpts := SearchOpts{
		Limit:          5,
		IncludeSketch:  false,
		QueryEmbedding: queryEmb,
		QueryText:      query,
	}
	searchResults, _ := e.DualSearch(ctx, namespace, query, searchOpts)

	memoryCount := 0
	if searchResults != nil {
		memoryCount = len(searchResults.DetailMemories)
	}

	// 5. Serve cached doc or synthesize
	if docMatchScore >= 0.75 && len(kdocs) > 0 {
		// Cache hit
		explanation = kdocs[0].Content
		docTopic = kdocs[0].Topic
	} else {
		// Cache miss — synthesize
		gen, err := e.getSynthesisGenerator()
		if err != nil {
			return nil, fmt.Errorf("consult: %w", err)
		}

		// Build synthesis prompt
		var promptParts []string
		promptParts = append(promptParts, fmt.Sprintf(
			"Given the following code and structural evidence about %q in this codebase, "+
				"write a concise explanation (200-400 words) of how this subsystem works.\n\n"+
				"Include: key files and their roles, data/control flow between components, "+
				"important design decisions or constraints. Reference specific code where helpful.", query))

		if codeEvidence != nil && len(codeEvidence.Snippets) > 0 {
			var snippetText strings.Builder
			for _, s := range codeEvidence.Snippets {
				snippetText.WriteString(fmt.Sprintf("--- %s:%d-%d ---\n", s.FilePath, s.StartLine, s.EndLine))
				snippetText.WriteString(s.Content)
				snippetText.WriteString("\n\n")
			}
			promptParts = append(promptParts, "[Code Snippets]\n"+snippetText.String())
		}

		if evidenceText != "" {
			promptParts = append(promptParts, "[Structural Evidence]\n"+evidenceText)
		}

		// Add relevant files
		if len(rankedFiles) > 0 {
			var fileList []string
			for _, f := range rankedFiles {
				entry := f.Path
				if f.Desc != "" {
					entry += " — " + f.Desc
				}
				fileList = append(fileList, entry)
			}
			promptParts = append(promptParts, "[Relevant Files]\n"+strings.Join(fileList, "\n"))
		}

		// Add matching memories
		if searchResults != nil {
			var memTexts []string
			for _, dm := range searchResults.DetailMemories {
				memTexts = append(memTexts, dm.Text)
			}
			if len(memTexts) > 0 {
				promptParts = append(promptParts, "[Existing Memories]\n"+strings.Join(memTexts, "\n"))
			}
		}

		prompt := strings.Join(promptParts, "\n\n")
		content, err := gen.GenerateText(ctx, prompt, 500)
		if err != nil {
			return nil, fmt.Errorf("consult: synthesize: %w", err)
		}

		explanation = strings.TrimSpace(content)
		synthesized = true

		// Normalize topic
		docTopic = strings.ToLower(strings.TrimSpace(query))
		if len(docTopic) > 80 {
			docTopic = docTopic[:80]
		}

		// Dedup: check if existing doc covers ≥50% of the same files
		var docFiles []string
		for _, f := range rankedFiles {
			docFiles = append(docFiles, f.Path)
		}

		var existingDoc *KnowledgeDoc
		for i, kd := range kdocs {
			overlap := fileOverlap(kd.Files, docFiles)
			if overlap >= 0.5 {
				existingDoc = &kdocs[i]
				docTopic = existingDoc.Topic
				break
			}
		}

		// Embed the synthesized content
		docEmb, err := e.embedder.Embed(ctx, explanation, "RETRIEVAL_DOCUMENT")
		if err != nil {
			// Non-fatal — save without embedding
			docEmb = nil
		}

		// Collect source IDs from memories
		var sourceIDs []string
		if searchResults != nil {
			for _, dm := range searchResults.DetailMemories {
				sourceIDs = append(sourceIDs, dm.ID)
			}
		}

		now := time.Now()
		doc := &KnowledgeDoc{
			ID:         generateID(),
			Namespace:  namespace,
			Topic:      docTopic,
			Content:    explanation,
			Files:      docFiles,
			SourceIDs:  sourceIDs,
			Embedding:  docEmb,
			TokenCount: estimateTokens(explanation),
			CreatedAt:  now,
			UpdatedAt:  now,
		}

		if existingDoc != nil {
			doc.ID = existingDoc.ID
			doc.CreatedAt = existingDoc.CreatedAt
		}

		// Best-effort save
		_ = e.store.UpsertKnowledgeDoc(doc, docEmb)
	}

	// 6. Compute confidence
	confidence, confLabel := computeConsultConfidence(docMatchScore, len(callEdges), memoryCount)

	report := &ConsultReport{
		Query:         query,
		Intent:        intent,
		Confidence:    confidence,
		ConfLabel:     confLabel,
		Explanation:   explanation,
		Evidence:      evidenceText,
		RelevantFiles: rankedFiles,
		Synthesized:   synthesized,
		DocTopic:      docTopic,
		TokenCount:    estimateTokens(explanation) + estimateTokens(evidenceText),
	}

	return report, nil
}

// findFileIdentifiers looks up identifiers for a file path in the codemap.
func findFileIdentifiers(cm *CodeMap, filePath string) []string {
	for _, mod := range cm.Zoom2 {
		for _, fi := range mod.Files {
			if fi.RelPath == filePath {
				return fi.Identifiers
			}
		}
	}
	return nil
}

// ReadCodeEvidence ranks files against a query and extracts relevant code snippets
// within a token budget. This is the core evidence pipeline used by Consult and Explore.
func (e *Engine) ReadCodeEvidence(ctx context.Context, namespace, query string, opts ReadCodeOpts, cm *CodeMap, idx *CodeIndex) (*CodeEvidence, error) {
	if opts.MaxTokens <= 0 {
		opts.MaxTokens = 4000
	}
	if opts.MaxFiles <= 0 {
		opts.MaxFiles = 8
	}

	fileResults := SearchCodeMapFiles(cm, idx, query, opts.MaxFiles*2)

	if len(opts.SeedPaths) > 0 {
		seedSet := make(map[string]bool)
		for _, sp := range opts.SeedPaths {
			seedSet[sp] = true
		}
		var boosted, rest []FileResult
		for _, fr := range fileResults {
			if seedSet[fr.FilePath] {
				boosted = append(boosted, fr)
			} else {
				rest = append(rest, fr)
			}
		}
		fileResults = append(boosted, rest...)
	}

	if len(fileResults) > opts.MaxFiles {
		fileResults = fileResults[:opts.MaxFiles]
	}

	evidence := &CodeEvidence{}
	rootDir := cm.RootDir

	// Expand directory/module-path results into actual source files.
	// Module paths can be directories ("helpers/auth/") or virtual paths
	// ("helpers/guardian-approval.helpers/") where the actual file is
	// "helpers/guardian-approval.helpers.ts". We try multiple resolutions.
	var expanded []FileResult
	sourceExts := []string{".ts", ".tsx", ".js", ".jsx", ".go", ".py", ".rs"}
	for _, fr := range fileResults {
		fullPath := filepath.Join(rootDir, fr.FilePath)
		info, statErr := os.Stat(fullPath)

		if statErr == nil && !info.IsDir() {
			// Direct file hit
			expanded = append(expanded, fr)
			continue
		}

		if statErr == nil && info.IsDir() {
			// Real directory — list source files inside
			entries, err := os.ReadDir(fullPath)
			if err != nil {
				continue
			}
			for _, e := range entries {
				if e.IsDir() {
					continue
				}
				ext := filepath.Ext(e.Name())
				for _, se := range sourceExts {
					if ext == se {
						expanded = append(expanded, FileResult{
							ModulePath:    fr.ModulePath,
							FilePath:      filepath.Join(fr.FilePath, e.Name()),
							FileScore:     fr.FileScore,
							ModuleScore:   fr.ModuleScore,
							CombinedScore: fr.CombinedScore,
							Summary:       fr.Summary,
						})
						break
					}
				}
			}
			continue
		}

		// Path doesn't exist — try stripping trailing slash and adding extensions.
		// Module path "helpers/foo/" might map to "helpers/foo.ts".
		basePath := strings.TrimSuffix(fr.FilePath, "/")
		for _, ext := range sourceExts {
			candidate := filepath.Join(rootDir, basePath+ext)
			if _, err := os.Stat(candidate); err == nil {
				expanded = append(expanded, FileResult{
					ModulePath:    fr.ModulePath,
					FilePath:      basePath + ext,
					FileScore:     fr.FileScore,
					ModuleScore:   fr.ModuleScore,
					CombinedScore: fr.CombinedScore,
					Summary:       fr.Summary,
				})
				break
			}
		}
		// Also try the directory without trailing slash (in case it's a real dir)
		if info2, err := os.Stat(filepath.Join(rootDir, basePath)); err == nil && info2.IsDir() {
			entries, err := os.ReadDir(filepath.Join(rootDir, basePath))
			if err != nil {
				continue
			}
			for _, e := range entries {
				if e.IsDir() {
					continue
				}
				ext := filepath.Ext(e.Name())
				for _, se := range sourceExts {
					if ext == se {
						expanded = append(expanded, FileResult{
							ModulePath:    fr.ModulePath,
							FilePath:      filepath.Join(basePath, e.Name()),
							FileScore:     fr.FileScore,
							ModuleScore:   fr.ModuleScore,
							CombinedScore: fr.CombinedScore,
							Summary:       fr.Summary,
						})
						break
					}
				}
			}
		}
	}
	fileResults = expanded
	if len(fileResults) > opts.MaxFiles {
		fileResults = fileResults[:opts.MaxFiles]
	}

	for _, fr := range fileResults {
		if evidence.TotalTokens >= opts.MaxTokens {
			break
		}

		fullPath := filepath.Join(rootDir, fr.FilePath)
		lines, err := readFileLines(fullPath)
		if err != nil {
			continue
		}

		numLines := len(lines)
		lang := detectLangFromPath(fr.FilePath)
		remaining := opts.MaxTokens - evidence.TotalTokens

		if numLines < 150 {
			content := strings.Join(lines, "\n")
			tokens := estimateTokens(content)
			if tokens > remaining {
				continue
			}
			evidence.Snippets = append(evidence.Snippets, CodeSnippet{
				FilePath:   fr.FilePath,
				StartLine:  1,
				EndLine:    numLines,
				Content:    content,
				Relevance:  fr.Summary,
				TokenCount: tokens,
			})
			evidence.TotalTokens += tokens
		} else {
			fileIdentifiers := findFileIdentifiers(cm, fr.FilePath)
			matches := matchIdentifiers(fileIdentifiers, query)

			if len(matches) == 0 {
				end := 80
				if end > numLines {
					end = numLines
				}
				content := strings.Join(lines[:end], "\n")
				tokens := estimateTokens(content)
				if tokens > remaining {
					continue
				}
				evidence.Snippets = append(evidence.Snippets, CodeSnippet{
					FilePath:   fr.FilePath,
					StartLine:  1,
					EndLine:    end,
					Content:    content,
					Relevance:  fr.Summary,
					TokenCount: tokens,
				})
				evidence.TotalTokens += tokens
			} else {
				for _, ident := range matches {
					if evidence.TotalTokens >= opts.MaxTokens {
						break
					}
					startIdx, endIdx := extractBlock(lines, ident, lang)
					if startIdx < 0 {
						continue
					}
					content := strings.Join(lines[startIdx:endIdx+1], "\n")
					tokens := estimateTokens(content)
					if tokens > opts.MaxTokens-evidence.TotalTokens {
						continue
					}
					evidence.Snippets = append(evidence.Snippets, CodeSnippet{
						FilePath:   fr.FilePath,
						StartLine:  startIdx + 1,
						EndLine:    endIdx + 1,
						Content:    content,
						Relevance:  fr.Summary,
						TokenCount: tokens,
					})
					evidence.TotalTokens += tokens
				}
			}
		}
	}

	return evidence, nil
}

// Explore reads ranked code files and produces a grounded briefing with code snippets
// and a short summary. Unlike Consult (which caches knowledge docs), Explore is ephemeral.
func (e *Engine) Explore(ctx context.Context, namespace, query string, budget int) (*ExploreResult, error) {
	if budget <= 0 {
		budget = 4000
	}

	codeMap, codeIdx := e.getOrGenerateCodeMap(ctx, namespace)

	var seedPaths []string
	if codeMap != nil {
		hdcQuery := HDCEncodeQuery(query)
		var moduleEmbs map[string][]float32
		if codeIdx != nil {
			moduleEmbs = codeIdx.HDCVectors
		}
		for _, mod := range codeMap.Zoom2 {
			if emb, ok := moduleEmbs[mod.Path]; ok {
				score := CosineSimilarity(hdcQuery, emb)
				if score > 0.3 {
					seedPaths = append(seedPaths, mod.Path)
				}
			}
		}
		if len(seedPaths) > 10 {
			seedPaths = seedPaths[:10]
		}
	}

	var codeEvidence *CodeEvidence
	if codeMap != nil {
		codeEvidence, _ = e.ReadCodeEvidence(ctx, namespace, query, ReadCodeOpts{
			MaxTokens: budget,
			MaxFiles:  8,
			SeedPaths: seedPaths,
		}, codeMap, codeIdx)
	}

	if codeEvidence == nil {
		codeEvidence = &CodeEvidence{}
	}

	var summary string
	if len(codeEvidence.Snippets) > 0 {
		gen, err := e.getSynthesisGenerator()
		if err == nil {
			var snippetText strings.Builder
			for _, s := range codeEvidence.Snippets {
				snippetText.WriteString(fmt.Sprintf("--- %s ---\n%s\n\n", s.FilePath, s.Content))
			}
			prompt := fmt.Sprintf(
				"Given these code snippets about %q, write a 50-100 word summary of how this subsystem works. "+
					"Focus on: key functions, data flow, and design patterns. Reference specific code.\n\n%s", query, snippetText.String())
			content, err := gen.GenerateText(ctx, prompt, 200)
			if err == nil {
				summary = strings.TrimSpace(content)
			}
		}
	}

	return &ExploreResult{
		Query:       query,
		Evidence:    codeEvidence,
		Summary:     summary,
		TotalTokens: codeEvidence.TotalTokens,
	}, nil
}

// computeConsultConfidence calculates a confidence score for a consult report.
func computeConsultConfidence(docMatchScore float64, edgeCount, memoryCount int) (float64, string) {
	edgeNorm := float64(edgeCount) / 10.0
	if edgeNorm > 1.0 {
		edgeNorm = 1.0
	}
	memNorm := float64(memoryCount) / 5.0
	if memNorm > 1.0 {
		memNorm = 1.0
	}

	score := docMatchScore*0.4 + edgeNorm*0.3 + memNorm*0.3

	var label string
	switch {
	case score >= 0.7:
		label = "high"
	case score >= 0.4:
		label = "medium"
	default:
		label = "low"
	}
	return score, label
}

// formatCallGraph renders structural edges and co-change neighbors as readable text.
func formatCallGraph(edges []StructuralEdge, cochange map[string]float64) string {
	var sb strings.Builder

	if len(edges) > 0 {
		sb.WriteString("[Call Graph]\n")
		// Deduplicate by source+target+type
		type edgeKey struct{ src, tgt, typ string }
		seen := make(map[edgeKey]bool)
		for _, e := range edges {
			k := edgeKey{e.SourcePath, e.TargetPath, e.EdgeType}
			if seen[k] {
				continue
			}
			seen[k] = true
			callee := e.CalleeName
			if callee == "" {
				callee = e.EdgeType
			}
			sb.WriteString(fmt.Sprintf("%s → %s (%s: %s)\n", e.SourcePath, e.TargetPath, e.EdgeType, callee))
		}
	}

	if len(cochange) > 0 {
		if sb.Len() > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString("[Co-Change Neighbors]\n")
		// Sort by score descending
		type pair struct {
			path  string
			score float64
		}
		var pairs []pair
		for p, s := range cochange {
			pairs = append(pairs, pair{p, s})
		}
		sort.Slice(pairs, func(i, j int) bool { return pairs[i].score > pairs[j].score })
		for _, p := range pairs {
			sb.WriteString(fmt.Sprintf("%s (%.2f)\n", p.path, p.score))
		}
	}

	return sb.String()
}

// fileOverlap returns the fraction of files in a that also appear in b.
func fileOverlap(a, b []string) float64 {
	if len(a) == 0 {
		return 0
	}
	bSet := make(map[string]bool, len(b))
	for _, f := range b {
		bSet[f] = true
	}
	overlap := 0
	for _, f := range a {
		if bSet[f] {
			overlap++
		}
	}
	return float64(overlap) / float64(len(a))
}

// buildIndexSnapshot creates a ContextSnapshot from index items, enriching
// detail memory items with their search features for re-ranker training.
func buildIndexSnapshot(id, namespace, query string, items []IndexEntry, details []DetailMemory, now time.Time) *ContextSnapshot {
	// Build lookup for detail memory features
	detailMap := make(map[string]*DetailMemory, len(details))
	for i := range details {
		detailMap[details[i].ID] = &details[i]
	}

	var sources []SnapshotSource
	for _, item := range items {
		ss := SnapshotSource{
			ID:         item.ID,
			Type:       item.Type,
			TextLength: item.TokenCount,
		}
		// Enrich with detail memory features if this is a detail memory
		if dm, ok := detailMap[item.ID]; ok {
			ss.CosineSim = dm.Similarity
			ss.Importance = dm.ImportanceScore
			ss.Salience = dm.Salience
			ss.Sector = dm.Sector
			ss.MemType = dm.Type
			ss.AgeDays = now.Sub(dm.CreatedAt).Hours() / 24
		}
		sources = append(sources, ss)
	}

	totalTokens := 0
	for _, item := range items {
		totalTokens += item.TokenCount
	}

	return &ContextSnapshot{
		ID:         id,
		Namespace:  namespace,
		Query:      query,
		Sources:    sources,
		TokensUsed: totalTokens,
		CreatedAt:  now,
	}
}

// ShowItems renders full text for specific memory items by ID.
// Accepts mixed ID prefixes: raw IDs (detail memories), chk_* (checkpoints),
// kdoc_* (knowledge docs), ep_* (episodes).
// If snapshotID is non-empty, submits implicit ratings: fetched items get
// rating=2 (relevant), all other items in the snapshot get rating=0 (skipped).
func (e *Engine) ShowItems(ctx context.Context, namespace string, ids []string, snapshotID string) (string, error) {
	var parts []string

	for _, id := range ids {
		var text string
		switch {
		case strings.HasPrefix(id, "chk_"):
			task := id[4:]
			checkpoints, _ := e.GetCheckpoints(ctx, namespace)
			for _, cp := range checkpoints {
				if sanitizeID(cp.Task) == task || cp.Task == task {
					text = cp.FormatForContext()
					break
				}
			}
			if text == "" {
				text = fmt.Sprintf("[Checkpoint %q not found]", task)
			}

		case strings.HasPrefix(id, "kdoc_"):
			topic := id[5:]
			docs, _ := e.store.GetKnowledgeDocs(namespace)
			for _, kd := range docs {
				if sanitizeID(kd.Topic) == topic || kd.Topic == topic {
					text = kd.FormatForContext()
					break
				}
			}
			if text == "" {
				text = fmt.Sprintf("[Knowledge doc %q not found]", topic)
			}

		case strings.HasPrefix(id, "ep_"):
			epID := id[3:]
			ep, err := e.store.GetEpisodeByID(epID)
			if err == nil && ep != nil {
				text = "[Recent Episode]\n" + ep.SummaryText
			} else {
				text = fmt.Sprintf("[Episode %q not found]", epID)
			}

		default:
			// Assume it's a detail memory ID
			d, err := e.store.GetDetailByID(id)
			if err == nil && d != nil {
				label := formatTypeLabel(d.Type, d.Sector, d.ImportanceScore)
				text = label + "\n" + d.Text
				if len(d.Files) > 0 {
					text += "\n  Files: " + strings.Join(d.Files, ", ")
				}
				// Touch the memory to update access tracking
				_ = e.store.TouchDetail(id)
			} else {
				text = fmt.Sprintf("[Memory %q not found]", id)
			}
		}
		parts = append(parts, text)
	}

	// Record implicit ratings if a snapshot was provided
	if snapshotID != "" {
		_ = e.SubmitImplicitRatings(namespace, snapshotID, ids) // best-effort
	}

	return strings.Join(parts, "\n\n"), nil
}

// typeIcon returns a compact icon for a memory type, used in index listings.
func typeIcon(memType string) string {
	switch memType {
	case "warning":
		return "⚠"
	case "decision":
		return "★"
	case "continuity":
		return "↻"
	case "seed":
		return "🌱"
	case "architecture":
		return "🏗"
	case "investigation":
		return "🔎"
	case "autopilot":
		return "🤖"
	case "requirement":
		return "📋"
	case "test-strategy":
		return "🧪"
	default:
		return ""
	}
}

// truncateTitle returns the first n characters of s, appending "…" if truncated.
func truncateTitle(s string, n int) string {
	// Strip newlines for compact display
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.TrimSpace(s)
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
}

// sanitizeID converts a human-readable name to a stable ID component.
// Lowercases, replaces spaces/special chars with hyphens.
func sanitizeID(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, " ", "-")
	// Strip anything that's not alphanumeric or hyphen
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// GetCodeMap returns the cached code map and hybrid CodeIndex for a namespace.
// Stale-while-revalidate: if a stored map exists it is served immediately even
// when the git commit has moved (marked with Stale/CommitsBehind) — it never
// blocks on a rescan. A full scan only runs inline when no map exists at all
// (true first run). Use RefreshCodeMap to force regeneration.
func (e *Engine) GetCodeMap(ctx context.Context, namespace string) (*CodeMap, *CodeIndex) {
	return e.getOrGenerateCodeMap(ctx, namespace)
}

func (e *Engine) getOrGenerateCodeMap(ctx context.Context, namespace string) (*CodeMap, *CodeIndex) {
	_, currentCommit := GetGitState(e.cfg.RootDir)

	stored, _ := e.store.GetCodeMap(namespace)

	// Serve any stored map immediately — fresh or stale. Never block context
	// assembly on a codebase rescan (large monorepos can take minutes).
	if stored != nil {
		cm := &CodeMap{
			Namespace:   stored.Namespace,
			RootDir:     stored.RootDir,
			Zoom1:       stored.Zoom1,
			Zoom2:       UnmarshalZoom2(stored.Zoom2JSON),
			GeneratedAt: stored.GeneratedAt,
			GitCommit:   stored.GitCommit,
		}
		if currentCommit != "" && stored.GitCommit != currentCommit {
			cm.Stale = true
			cm.CommitsBehind = countCommitsBetween(e.cfg.RootDir, stored.GitCommit, currentCommit)
		}
		return cm, BuildCodeIndex(cm)
	}

	// True first run: no stored map. Generate inline.
	cm, idx, err := e.RefreshCodeMap(ctx, namespace)
	if err != nil {
		return nil, nil
	}
	return cm, idx
}

// RefreshCodeMap rescans the codebase and persists the result. When a stored
// map exists and git can enumerate what changed since it was built, only the
// affected directories are re-parsed (incremental); otherwise it falls back to
// a full filesystem walk. Called by the CLI `map` command, on true first run,
// and by background refresh workers.
func (e *Engine) RefreshCodeMap(ctx context.Context, namespace string) (*CodeMap, *CodeIndex, error) {
	_, currentCommit := GetGitState(e.cfg.RootDir)

	if cm, idx := e.tryIncrementalRefresh(namespace, currentCommit); cm != nil {
		return cm, idx, nil
	}

	// Unified scan: code map + structural edges in one pass
	result, err := ScanCodebase(e.cfg.RootDir, e.OnScanProgress)
	if err != nil {
		return nil, nil, err
	}
	cm := result.CodeMap
	cm.Namespace = namespace
	cm.GitCommit = currentCommit

	e.store.UpsertCodeMap(namespace, e.cfg.RootDir, cm.Zoom1, cm.MarshalZoom2(), cm.GitCommit)

	if len(result.Edges) > 0 {
		for i := range result.Edges {
			result.Edges[i].Namespace = namespace
		}
		e.store.InsertStructuralEdges(namespace, result.Edges)
	}

	return cm, BuildCodeIndex(cm), nil
}

// Incremental refresh guards: above these thresholds a full walk is likely
// cheaper (and certainly simpler) than many targeted rescans.
const (
	maxIncrementalFiles = 500
	maxIncrementalDirs  = 100
)

// tryIncrementalRefresh re-parses only the directories touched since the
// stored map's commit, merging the results into the stored map. Returns
// (nil, nil) when incremental refresh isn't possible — no stored map, no git
// state, unresolvable diff, or too many changes — and the caller falls back
// to a full scan.
func (e *Engine) tryIncrementalRefresh(namespace, currentCommit string) (*CodeMap, *CodeIndex) {
	if currentCommit == "" {
		return nil, nil
	}
	stored, err := e.store.GetCodeMap(namespace)
	if err != nil || stored == nil || stored.GitCommit == "" {
		return nil, nil
	}

	changed, ok := ChangedFilesSince(e.cfg.RootDir, stored.GitCommit)
	if !ok || len(changed) > maxIncrementalFiles {
		return nil, nil
	}

	// Group changed files by directory (git emits slash-separated paths).
	dirSet := make(map[string]bool)
	for _, f := range changed {
		dir := "."
		if i := strings.LastIndex(f, "/"); i >= 0 {
			dir = f[:i]
		}
		dirSet[dir] = true
	}
	if len(dirSet) > maxIncrementalDirs {
		return nil, nil
	}

	oldModules := UnmarshalZoom2(stored.Zoom2JSON)

	if len(dirSet) == 0 {
		// Nothing changed on disk — the map content is still valid, just
		// re-stamp the commit so staleness checks stop firing.
		cm := &CodeMap{
			Namespace:   namespace,
			RootDir:     stored.RootDir,
			Zoom1:       stored.Zoom1,
			Zoom2:       oldModules,
			GitCommit:   currentCommit,
			GeneratedAt: time.Now(),
		}
		if err := e.store.UpsertCodeMap(namespace, e.cfg.RootDir, cm.Zoom1, cm.MarshalZoom2(), currentCommit); err != nil {
			return nil, nil
		}
		return cm, BuildCodeIndex(cm)
	}

	dirs := make([]string, 0, len(dirSet))
	for d := range dirSet {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)

	if e.OnScanProgress != nil {
		e.OnScanProgress(ScanProgress{Phase: "incremental", DirsFound: len(dirs), FileCount: len(changed)})
	}

	newMods, newEdges, err := ScanDirs(e.cfg.RootDir, dirs)
	if err != nil {
		return nil, nil
	}

	// Merge: drop modules owned by rescanned dirs, add their replacements.
	merged := make([]ModuleMap, 0, len(oldModules)+len(newMods))
	for _, m := range oldModules {
		if !dirSet[moduleOwnerDir(m.Path)] {
			merged = append(merged, m)
		}
	}
	merged = append(merged, newMods...)
	sort.Slice(merged, func(i, j int) bool {
		return merged[i].Path < merged[j].Path
	})

	cm := &CodeMap{
		Namespace:   namespace,
		RootDir:     e.cfg.RootDir,
		Zoom1:       synthesizeZoom1(merged, e.cfg.RootDir),
		Zoom2:       merged,
		GitCommit:   currentCommit,
		GeneratedAt: time.Now(),
	}
	if err := e.store.UpsertCodeMap(namespace, e.cfg.RootDir, cm.Zoom1, cm.MarshalZoom2(), currentCommit); err != nil {
		return nil, nil
	}

	for i := range newEdges {
		newEdges[i].Namespace = namespace
	}
	e.store.ReplaceStructuralEdgesForDirs(namespace, dirs, newEdges)

	return cm, BuildCodeIndex(cm)
}

func flattenEmbeddings(embs map[string]ModuleEmbedding) map[string][]float32 {
	flat := make(map[string][]float32, len(embs))
	for k, v := range embs {
		flat[k] = v.Embedding
	}
	return flat
}

// StoreCodeMap persists a code map to the database, embeds modules, and stores
// structural edges. Called by the CLI `map` command.
func (e *Engine) StoreCodeMap(ctx context.Context, namespace string, cm *CodeMap, edges []StructuralEdge) error {
	err := e.store.UpsertCodeMap(namespace, cm.RootDir, cm.Zoom1, cm.MarshalZoom2(), cm.GitCommit)
	if err != nil {
		return err
	}
	embs, embErr := EmbedCodeMap(ctx, cm, e.embedder)
	if embErr == nil && embs != nil {
		e.store.UpsertCodeMapEmbeddings(namespace, embs, e.embedder.ModelName())
	}
	// Store structural edges from unified scan
	if len(edges) > 0 {
		for i := range edges {
			edges[i].Namespace = namespace
		}
		e.store.InsertStructuralEdges(namespace, edges)
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

// SaveCheckpoint stores a structured checkpoint, auto-superseding any previous
// checkpoint with the same task name for this user.
func (e *Engine) SaveCheckpoint(ctx context.Context, userID string, cp *Checkpoint) error {
	if cp.Task == "" {
		return fmt.Errorf("dualmem: checkpoint task is required")
	}
	if cp.Status == "" {
		cp.Status = "in_progress"
	}

	// Serialize checkpoint to JSON
	cpJSON, err := json.Marshal(cp)
	if err != nil {
		return fmt.Errorf("dualmem: marshal checkpoint: %w", err)
	}

	// Delete previous checkpoints for the same task
	existing, err := e.store.GetDetailsByType(userID, "checkpoint")
	if err == nil {
		for _, dm := range existing {
			var old Checkpoint
			if json.Unmarshal([]byte(dm.Text), &old) == nil && old.Task == cp.Task {
				e.store.DeleteDetail(dm.ID)
			}
		}
	}

	// Store as a high-salience detail memory
	return e.AddWithOptions(ctx, MemoryInput{
		UserMessage: string(cpJSON),
		SectorHint:  "continuity",
		Salience:    0.8,
		Type:        "checkpoint",
	}, userID)
}

// GetCheckpoints returns all active checkpoints for a user.
func (e *Engine) GetCheckpoints(ctx context.Context, userID string) ([]Checkpoint, error) {
	details, err := e.store.GetDetailsByType(userID, "checkpoint")
	if err != nil {
		return nil, err
	}

	var checkpoints []Checkpoint
	for _, dm := range details {
		var cp Checkpoint
		if json.Unmarshal([]byte(dm.Text), &cp) == nil {
			checkpoints = append(checkpoints, cp)
		}
	}
	return checkpoints, nil
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

// supersedeThresholds maps memory types to their cosine similarity threshold
// for automatic supersession. When a new memory of a given type is added,
// older memories of the same type with cosine similarity >= the threshold
// are demoted to sketch path.
var supersedeThresholds = map[string]float64{
	"continuity":    0.75,
	"decision":      0.80,
	"architecture":  0.80,
	"investigation": 0.80,
	"requirement":   0.80,
	"test-strategy": 0.80,
	"trace":         0.80,
	"warning":       0.85,
}

// supersedeByType demotes older memories of the same type that are semantically
// similar to the newly added one (cosine similarity >= type-specific threshold).
// This prevents stale entries from accumulating when a newer memory covers the same topic.
func (e *Engine) supersedeByType(ctx context.Context, userID, newID, memType string, newEmbedding []float32) {
	threshold, ok := supersedeThresholds[memType]
	if !ok {
		return
	}

	existing, err := e.store.GetDetailsByType(userID, memType)
	if err != nil || len(existing) <= 1 {
		return
	}

	for _, old := range existing {
		if old.ID == newID {
			continue
		}
		sim := CosineSimilarity(newEmbedding, old.Vector)
		if sim >= threshold {
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

	// 4. Type-aware supersession scan — for each type with a threshold,
	// group by cosine similarity, keep newest per cluster, demote rest.
	for memType, threshold := range supersedeThresholds {
		memories, _ := e.store.GetDetailsByType(userID, memType)
		if len(memories) <= 1 {
			continue
		}
		kept := make(map[string]bool)
		demotedSet := make(map[string]bool)
		for i, c := range memories {
			if demotedSet[c.ID] {
				continue
			}
			kept[c.ID] = true
			for j := i + 1; j < len(memories); j++ {
				other := memories[j]
				if demotedSet[other.ID] || kept[other.ID] {
					continue
				}
				sim := CosineSimilarity(c.Vector, other.Vector)
				if sim >= threshold {
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
					demotedSet[other.ID] = true
					report.SupersededMemories++
				}
			}
		}
	}

	// 5. Access-cold demotion — details not accessed in 30+ days with importance < 0.8
	details, _ := e.store.GetDetailMemories(userID)
	for _, d := range details {
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

// --- Health check ---

// HealthCheck is the result of a database health inspection.
type HealthCheck struct {
	Status  string // "healthy", "warnings", "issues"
	Checks  []HealthEntry
	Summary HealthSummary
}

// HealthEntry describes a single health check result.
type HealthEntry struct {
	Name    string // e.g. "Embedding model consistency"
	Status  string // "ok", "warning", "error"
	Message string // human-readable description
	Detail  string // optional extra info
}

// HealthSummary contains aggregate counts from the health inspection.
type HealthSummary struct {
	TotalDetailMemories int
	TotalSketchRaw      int
	TotalEpisodes       int
	TotalArcs           int
	TotalKnowledgeDocs  int
	TotalEntityNodes    int
	TotalEntityEdges    int
	OldestMemory        time.Time
	NewestMemory        time.Time
	Namespaces          []string
	DatabaseSize        int64 // bytes
}

// Health inspects the database for the given namespace and returns a report
// with actionable findings. Works without an API key (no embedding calls).
func (e *Engine) Health(ctx context.Context, namespace string) (*HealthCheck, error) {
	hc := &HealthCheck{Status: "healthy"}

	// --- Gather data ---

	detailCount, _ := e.store.GetDetailCount(namespace)
	sketchRawCount, _ := e.store.GetSketchRawCount(namespace)

	episodes, _ := e.store.GetEpisodes(namespace, nil)
	episodeCount := len(episodes)

	arcs, _ := e.store.GetArcs(namespace)
	arcCount := len(arcs)

	docs, _ := e.store.GetKnowledgeDocs(namespace)
	docCount := len(docs)

	entityNodes, entityEdges, _, _ := e.store.GetEntityStats(namespace)

	oldestMem, newestMem, _ := e.store.GetMemoryTimeRange(namespace)

	namespaces, _ := e.store.ListNamespaces()

	var dbSize int64
	if info, err := os.Stat(e.cfg.SQLitePath); err == nil {
		dbSize = info.Size()
	}

	// --- Run checks ---

	// 1. Embedding model consistency
	modelCounts, _ := e.store.GetEmbeddingModelCounts(namespace)
	if len(modelCounts) == 0 {
		hc.addCheck("Embedding model consistency", "ok", "No memories to check")
	} else if len(modelCounts) == 1 {
		for model, count := range modelCounts {
			hc.addCheck("Embedding model consistency", "ok",
				fmt.Sprintf("All %d memories use %s", count, model))
			break
		}
	} else {
		var parts []string
		total := 0
		for model, count := range modelCounts {
			parts = append(parts, fmt.Sprintf("%s (%d)", model, count))
			total += count
		}
		sort.Strings(parts)
		hc.addCheck("Embedding model consistency", "warning",
			fmt.Sprintf("Mixed embedding models across %d memories", total),
			strings.Join(parts, ", "))
	}

	// 2. Orphaned sketch raw
	sevenDaysAgo := time.Now().AddDate(0, 0, -7)
	staleCount, _ := e.store.GetStaleSketchRawCount(namespace, sevenDaysAgo)
	if staleCount > 0 {
		hc.addCheck("Orphaned sketch records", "warning",
			fmt.Sprintf("%d unprocessed records older than 7 days", staleCount),
			"Run 'dualmem gc' or check the compression pipeline")
	} else {
		hc.addCheck("Orphaned sketch records", "ok", "No stale unprocessed records")
	}

	// 3. Knowledge doc staleness
	allMemories, _ := e.store.GetDetailMemories(namespace)
	staleDocs := 0
	for i := range docs {
		if isDocStale(&docs[i], allMemories) {
			staleDocs++
		}
	}
	if staleDocs > 0 {
		hc.addCheck("Knowledge doc staleness", "warning",
			fmt.Sprintf("%d docs may need re-synthesis", staleDocs),
			"Run 'dualmem synthesize' to update")
	} else {
		hc.addCheck("Knowledge doc staleness", "ok",
			fmt.Sprintf("All %d knowledge docs are up to date", docCount))
	}

	// 4. Empty namespace detection
	if detailCount == 0 {
		hc.addCheck("Namespace populated", "error",
			"No memories in namespace",
			"Add memories with 'dualmem add'")
	} else {
		hc.addCheck("Namespace populated", "ok",
			fmt.Sprintf("%d memories in active namespace", detailCount))
	}

	// 5. Entity graph connectivity
	isolatedNodes, _ := e.store.GetIsolatedEntityNodeCount(namespace)
	if isolatedNodes > 0 {
		hc.addCheck("Entity graph connectivity", "warning",
			fmt.Sprintf("%d nodes, %d edges, %d isolated nodes",
				entityNodes, entityEdges, isolatedNodes))
	} else {
		hc.addCheck("Entity graph connectivity", "ok",
			fmt.Sprintf("%d nodes, %d edges, no isolated nodes",
				entityNodes, entityEdges))
	}

	// 6. Memory recency
	if newestMem.IsZero() {
		hc.addCheck("Memory recency", "ok", "No memories yet")
	} else {
		daysSinceLast := time.Since(newestMem).Hours() / 24
		if daysSinceLast > 30 {
			hc.addCheck("Memory recency", "warning",
				fmt.Sprintf("Last memory added %.0f days ago", daysSinceLast),
				"System may not be actively used")
		} else {
			hc.addCheck("Memory recency", "ok",
				fmt.Sprintf("Last memory added %.0f days ago", daysSinceLast))
		}
	}

	// 7. Database size
	hc.addCheck("Database size", "ok", fmtDBSize(dbSize))

	// --- Build summary ---
	hc.Summary = HealthSummary{
		TotalDetailMemories: detailCount,
		TotalSketchRaw:      sketchRawCount,
		TotalEpisodes:       episodeCount,
		TotalArcs:           arcCount,
		TotalKnowledgeDocs:  docCount,
		TotalEntityNodes:    entityNodes,
		TotalEntityEdges:    entityEdges,
		OldestMemory:        oldestMem,
		NewestMemory:        newestMem,
		Namespaces:          namespaces,
		DatabaseSize:        dbSize,
	}

	return hc, nil
}

func (hc *HealthCheck) addCheck(name, status, message string, detail ...string) {
	entry := HealthEntry{
		Name:    name,
		Status:  status,
		Message: message,
	}
	if len(detail) > 0 {
		entry.Detail = detail[0]
	}
	hc.Checks = append(hc.Checks, entry)

	switch status {
	case "error":
		hc.Status = "issues"
	case "warning":
		if hc.Status != "issues" {
			hc.Status = "warnings"
		}
	}
}

func fmtDBSize(bytes int64) string {
	const (
		KB = 1024
		MB = KB * 1024
		GB = MB * 1024
	)
	switch {
	case bytes >= GB:
		return fmt.Sprintf("%.1f GB", float64(bytes)/float64(GB))
	case bytes >= MB:
		return fmt.Sprintf("%.1f MB", float64(bytes)/float64(MB))
	case bytes >= KB:
		return fmt.Sprintf("%.1f KB", float64(bytes)/float64(KB))
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}

// GetStore returns the underlying store for direct entity graph queries.
func (e *Engine) GetStore() Store {
	return e.store
}

// Close shuts down the engine and all background workers.
func (e *Engine) Close() error {
	if e.pipeline != nil {
		e.pipeline.Stop()
	}
	return e.store.Close()
}

// GetConfigValue reads a config value from the store.
func (e *Engine) GetConfigValue(key string) (string, error) {
	return e.store.GetConfigValue(key)
}

// SetConfigValue writes a config value to the store.
func (e *Engine) SetConfigValue(key, value string) error {
	return e.store.SetConfigValue(key, value)
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
// detailSortScore computes a composite sort score for a detail memory.
// Combines type priority, access recency, optional intent-based multiplier,
// and a keyword boost when queryText is provided. Memories that contain exact
// identifier matches (e.g. "LC-1663") are boosted 3x to surface them above
// type-priority and recency ordering.
func detailSortScore(dm DetailMemory, intentProfile *IntentProfile, now time.Time, queryText string) float64 {
	base := float64(typePriority(dm.Type))
	recency := accessRecencyFactor(dm.LastAccessedAt, dm.Type, now)
	score := (base + 1.0) * recency // +1 so general (priority 0) still has a base

	if intentProfile != nil {
		score *= intentProfile.TypeMultiplier(dm.Type)
	}

	// Keyword boost: exact identifier match in memory text floats it to the top
	if queryText != "" {
		ids := extractIdentifiers(queryText)
		textLower := strings.ToLower(dm.Text)
		for _, id := range ids {
			if strings.Contains(textLower, strings.ToLower(id)) {
				score *= 3.0
				break
			}
		}
	}

	return score
}

// DetectIntent infers task intent from a query string using keyword matching.
// Returns IntentDefault if no strong signal is found.
func DetectIntent(query string) Intent {
	q := strings.ToLower(query)

	// Debug: error investigation, bug fixing, troubleshooting
	debugKeywords := []string{"fix", "bug", "error", "crash", "fail", "broken", "debug", "issue", "wrong", "trace", "stack", "panic", "nil pointer", "undefined"}
	for _, kw := range debugKeywords {
		if strings.Contains(q, kw) {
			return IntentDebug
		}
	}

	// Plan: roadmap, sprint planning, status reviews
	planKeywords := []string{"plan", "roadmap", "schedule", "sprint", "milestone", "quarter", "q1", "q2", "q3", "q4", "priorities", "backlog", "remaining work", "status update"}
	for _, kw := range planKeywords {
		if strings.Contains(q, kw) {
			return IntentPlan
		}
	}

	// Continue: session resumption, picking up work, status checks
	continueKeywords := []string{"continue", "resume", "pick up", "where we left", "last session", "in progress", "what was i", "session context", "what were we", "what next", "what's next", "next step", "what should", "todo", "status", "progress", "what's in progress", "what are we working on", "ongoing"}
	for _, kw := range continueKeywords {
		if strings.Contains(q, kw) {
			return IntentContinue
		}
	}

	// Explore: codebase navigation, understanding
	exploreKeywords := []string{"where is", "find", "how does", "what does", "explain", "understand", "explore", "navigate", "structure", "overview", "architecture"}
	for _, kw := range exploreKeywords {
		if strings.Contains(q, kw) {
			return IntentExplore
		}
	}

	// Feature: building new things
	featureKeywords := []string{"add", "implement", "create", "build", "new feature", "introduce", "wire up", "integrate", "extend"}
	for _, kw := range featureKeywords {
		if strings.Contains(q, kw) {
			return IntentFeature
		}
	}

	return IntentDefault
}

func typePriority(t string) int {
	switch t {
	case "warning", "anticipation":
		return 2
	case "decision", "continuity", "trace", "architecture", "investigation", "requirement", "test-strategy", "autopilot":
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
	case "trace":
		return fmt.Sprintf("[🔍 Trace — %s (importance: %.2f)]", sector, importance)
	case "architecture":
		return fmt.Sprintf("[🏗 Architecture — %s (importance: %.2f)]", sector, importance)
	case "investigation":
		return fmt.Sprintf("[🔎 Investigation — %s (importance: %.2f)]", sector, importance)
	case "autopilot":
		return fmt.Sprintf("[🤖 Autopilot — %s (importance: %.2f)]", sector, importance)
	case "requirement":
		return fmt.Sprintf("[📋 Requirement — %s (importance: %.2f)]", sector, importance)
	case "test-strategy":
		return fmt.Sprintf("[🧪 Test Strategy — %s (importance: %.2f)]", sector, importance)
	case "anticipation":
		return fmt.Sprintf("[🔮 Anticipated Context — %s]", sector)
	case "seed":
		return fmt.Sprintf("[Codebase Context — %s]", sector)
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

// --- Seed memories ---

// TextGenerator is an optional interface for LLM text generation.
// Implemented by GeminiSummarizer and AnthropicSummarizer.
type TextGenerator interface {
	GenerateText(ctx context.Context, prompt string, maxTokens int) (string, error)
}

// getSynthesisGenerator returns the best available TextGenerator for
// high-stakes synthesis (Consult, Synthesize, Seed). Prefers the dedicated
// SynthesisGenerator if configured, falls back to the main Summarizer.
func (e *Engine) getSynthesisGenerator() (TextGenerator, error) {
	if e.cfg.SynthesisGenerator != nil {
		return e.cfg.SynthesisGenerator, nil
	}
	gen, ok := e.cfg.Summarizer.(TextGenerator)
	if !ok {
		return nil, fmt.Errorf("dualmem: summarizer does not implement TextGenerator")
	}
	return gen, nil
}

// getExplorerGenerator returns the best available TextGenerator for autopilot/anticipatory
// exploration. Prefers ExplorerGenerator if configured, falls back to getSynthesisGenerator.
func (e *Engine) getExplorerGenerator() (TextGenerator, error) {
	if e.cfg.ExplorerGenerator != nil {
		return e.cfg.ExplorerGenerator, nil
	}
	return e.getSynthesisGenerator()
}

// SetExplorerGenerator sets the dedicated model for autopilot/anticipatory exploration.
func (e *Engine) SetExplorerGenerator(gen TextGenerator) {
	e.cfg.ExplorerGenerator = gen
}

// SeedResult describes what SeedMemories produced.
type SeedResult struct {
	Clusters []Cluster
	Memories []DetailMemory
	Warnings []string
	DryRun   bool
}

// SeedMemories generates semantic relationship memories from codebase structure.
// It builds an import graph from the codemap, detects clusters, and uses an LLM
// to generate descriptions for each cluster. Stores results as type='seed' detail memories.
func (e *Engine) SeedMemories(ctx context.Context, userID string, force bool, dryRun bool) (*SeedResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	// Check for existing seeds
	if !force {
		count, err := e.store.CountSeedMemories(userID)
		if err != nil {
			return nil, fmt.Errorf("dualmem: count seeds: %w", err)
		}
		if count > 0 {
			return nil, fmt.Errorf("dualmem: %d seed memories already exist for namespace %q (use --force to replace)", count, userID)
		}
	}

	// Load or generate codemap
	codeMap, _ := e.getOrGenerateCodeMap(ctx, userID)
	if codeMap == nil {
		return nil, fmt.Errorf("dualmem: no codemap available (set RootDir in config)")
	}
	if len(codeMap.Zoom2) == 0 {
		return nil, fmt.Errorf("dualmem: codemap has no modules")
	}

	// Build import graph and detect clusters
	graph := BuildImportGraph(codeMap.Zoom2)
	clusters := DetectClusters(graph)

	if len(clusters) == 0 {
		return &SeedResult{}, nil
	}

	result := &SeedResult{
		Clusters: clusters,
		DryRun:   dryRun,
	}

	if dryRun {
		return result, nil
	}

	// Check for text generator (only needed for non-dry-run)
	gen, err := e.getSynthesisGenerator()
	if err != nil {
		return nil, err
	}

	// Delete existing seeds if force
	if force {
		if err := e.store.DeleteSeedMemories(userID); err != nil {
			return nil, fmt.Errorf("dualmem: delete old seeds: %w", err)
		}
	}

	// Generate descriptions and store seeds
	maxSeeds := e.cfg.MaxSeedPerUser
	for i, cluster := range clusters {
		if i >= maxSeeds {
			break
		}

		// Generate LLM description (continue on individual failures)
		prompt := FormatClusterPrompt(cluster)
		description, err := gen.GenerateText(ctx, prompt, 300)
		if err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("cluster %q: %v", cluster.Name, err))
			continue
		}

		// Collect file paths from cluster modules
		var files []string
		for _, m := range cluster.Modules {
			files = append(files, m.Path)
		}

		// Embed the description
		embedding, err := e.embedder.Embed(ctx, description, "RETRIEVAL_DOCUMENT")
		if err != nil {
			return nil, fmt.Errorf("dualmem: embed seed %q: %w", cluster.Name, err)
		}

		// Store as seed memory
		dm := &DetailMemory{
			ID:              generateID(),
			Text:            description,
			Sector:          cluster.Name,
			Salience:        0.5, // lower than CLI default (0.7) so organic outranks
			ImportanceScore: 0.7,
			Type:            "seed",
			Files:           files,
			CreatedAt:       time.Now(),
			LastAccessedAt:  time.Now(),
		}

		if err := e.store.InsertDetail(dm, embedding, userID); err != nil {
			return nil, fmt.Errorf("dualmem: insert seed %q: %w", cluster.Name, err)
		}

		result.Memories = append(result.Memories, *dm)
	}

	return result, nil
}

// --- Knowledge documents ---

// Synthesize clusters related memories and creates/updates knowledge docs.
// Each doc is a coherent, concept-oriented document synthesized by LLM.
func (e *Engine) Synthesize(ctx context.Context, namespace string, opts *SynthesizeOpts) (*SynthesizeResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if opts == nil {
		opts = &SynthesizeOpts{}
	}

	result := &SynthesizeResult{DryRun: opts.DryRun}

	// Get text generator
	var gen TextGenerator
	if !opts.DryRun {
		var err error
		gen, err = e.getSynthesisGenerator()
		if err != nil {
			return nil, err
		}
	}

	// Load existing docs
	existingDocs, err := e.store.GetKnowledgeDocs(namespace)
	if err != nil {
		return nil, fmt.Errorf("dualmem: get knowledge docs: %w", err)
	}

	// If synthesizing a specific topic, handle that case
	if opts.Topic != "" {
		return e.synthesizeTopic(ctx, gen, namespace, opts.Topic, existingDocs, opts)
	}

	// Load all detail memories (excluding checkpoints and seeds)
	allMemories, err := e.store.GetDetailMemories(namespace)
	if err != nil {
		return nil, fmt.Errorf("dualmem: get memories: %w", err)
	}

	var eligible []detailWithVector
	for _, dm := range allMemories {
		if dm.Type != "checkpoint" && dm.Type != "seed" {
			eligible = append(eligible, dm)
		}
	}

	if len(eligible) == 0 {
		return result, nil
	}

	// Cluster memories
	clusters, orphaned := clusterMemories(eligible, existingDocs, 3)
	result.Orphaned = orphaned

	if opts.DryRun {
		for _, c := range clusters {
			result.Created = append(result.Created, KnowledgeDoc{
				Topic:     c.topic,
				Files:     c.files,
				SourceIDs: memoryIDs(c.members),
			})
		}
		return result, nil
	}

	// Synthesize each cluster
	for _, cluster := range clusters {
		// Check if existing doc exists for this topic
		var existingDoc *KnowledgeDoc
		for i, doc := range existingDocs {
			if doc.Topic == cluster.topic {
				existingDoc = &existingDocs[i]
				break
			}
		}

		// Skip if not force and doc exists and isn't stale
		if existingDoc != nil && !opts.Force && !isDocStale(existingDoc, eligible) {
			result.Skipped++
			continue
		}

		doc, err := synthesizeDoc(ctx, gen, e.embedder, cluster, namespace)
		if err != nil {
			result.Warnings = append(result.Warnings, err.Error())
			continue
		}

		// Preserve original creation time if updating
		if existingDoc != nil {
			doc.ID = existingDoc.ID
			doc.CreatedAt = existingDoc.CreatedAt
		}

		if err := e.store.UpsertKnowledgeDoc(doc, doc.Embedding); err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("store %q: %v", doc.Topic, err))
			continue
		}

		if existingDoc != nil {
			result.Updated = append(result.Updated, *doc)
		} else {
			result.Created = append(result.Created, *doc)
		}
	}

	// Enrich co-change edges with concept labels from entity graph (best-effort).
	// This runs after synthesis so that newly extracted entities are available.
	if enriched, err := e.EnrichCoChangeWithEntities(namespace); err == nil && enriched > 0 {
		result.Warnings = append(result.Warnings, fmt.Sprintf("enriched %d co-change edges with concept labels", enriched))
	}

	return result, nil
}

// synthesizeTopic re-synthesizes a single topic.
func (e *Engine) synthesizeTopic(ctx context.Context, gen TextGenerator, namespace, topic string, existingDocs []KnowledgeDoc, opts *SynthesizeOpts) (*SynthesizeResult, error) {
	result := &SynthesizeResult{DryRun: opts.DryRun}

	var targetDoc *KnowledgeDoc
	for i, doc := range existingDocs {
		if doc.Topic == topic {
			targetDoc = &existingDocs[i]
			break
		}
	}
	if targetDoc == nil {
		return nil, fmt.Errorf("dualmem: no knowledge doc with topic %q", topic)
	}

	// Load source memories
	allMemories, err := e.store.GetDetailMemories(namespace)
	if err != nil {
		return nil, fmt.Errorf("dualmem: get memories: %w", err)
	}

	sourceSet := make(map[string]bool)
	for _, id := range targetDoc.SourceIDs {
		sourceSet[id] = true
	}

	var sources []detailWithVector
	for _, dm := range allMemories {
		if sourceSet[dm.ID] {
			sources = append(sources, dm)
		}
	}

	// Also add new memories that overlap by files
	for _, dm := range allMemories {
		if !sourceSet[dm.ID] && dm.Type != "checkpoint" && dm.Type != "seed" {
			if filesOverlap(dm.Files, targetDoc.Files) >= 2 {
				sources = append(sources, dm)
				sourceSet[dm.ID] = true
			}
		}
	}

	if len(sources) == 0 {
		return result, nil
	}

	if opts.DryRun {
		result.Updated = append(result.Updated, KnowledgeDoc{
			Topic:     topic,
			Files:     targetDoc.Files,
			SourceIDs: memoryIDs(sources),
		})
		return result, nil
	}

	cluster := memoryCluster{
		topic:   topic,
		files:   mergeFiles(targetDoc.Files, collectFiles(sources)),
		members: sources,
	}

	doc, err := synthesizeDoc(ctx, gen, e.embedder, cluster, namespace)
	if err != nil {
		return nil, err
	}
	doc.ID = targetDoc.ID
	doc.CreatedAt = targetDoc.CreatedAt

	if err := e.store.UpsertKnowledgeDoc(doc, doc.Embedding); err != nil {
		return nil, fmt.Errorf("dualmem: store %q: %w", topic, err)
	}

	result.Updated = append(result.Updated, *doc)
	return result, nil
}

// GetKnowledgeDocs returns docs ranked by relevance to query.
func (e *Engine) GetKnowledgeDocs(ctx context.Context, namespace, query string, queryEmb []float32, limit int) ([]KnowledgeDoc, error) {
	docs, err := e.store.GetKnowledgeDocs(namespace)
	if err != nil {
		return nil, err
	}

	if len(queryEmb) > 0 && len(docs) > 0 {
		// Rank by cosine similarity to query
		type scored struct {
			doc   KnowledgeDoc
			score float64
		}
		var ranked []scored
		for _, doc := range docs {
			sim := 0.0
			if len(doc.Embedding) > 0 {
				sim = CosineSimilarity(queryEmb, doc.Embedding)
			}
			ranked = append(ranked, scored{doc, sim})
		}
		sort.Slice(ranked, func(i, j int) bool { return ranked[i].score > ranked[j].score })

		docs = make([]KnowledgeDoc, 0, len(ranked))
		for _, r := range ranked {
			docs = append(docs, r.doc)
		}
	}

	if limit > 0 && len(docs) > limit {
		docs = docs[:limit]
	}
	return docs, nil
}

// ListKnowledgeDocs returns all docs for a namespace.
func (e *Engine) ListKnowledgeDocs(ctx context.Context, namespace string) ([]KnowledgeDoc, error) {
	return e.store.GetKnowledgeDocs(namespace)
}

// DeleteKnowledgeDoc removes a doc by topic (source memories preserved).
func (e *Engine) DeleteKnowledgeDoc(ctx context.Context, namespace, topic string) error {
	return e.store.DeleteKnowledgeDoc(namespace, topic)
}

// SynthesizeAll runs synthesis across all namespaces in the database.
func (e *Engine) SynthesizeAll(ctx context.Context, opts *SynthesizeOpts) (map[string]*SynthesizeResult, error) {
	namespaces, err := e.store.ListNamespaces()
	if err != nil {
		return nil, fmt.Errorf("dualmem: list namespaces: %w", err)
	}

	results := make(map[string]*SynthesizeResult)
	for _, ns := range namespaces {
		result, err := e.Synthesize(ctx, ns, opts)
		if err != nil {
			// Log warning but continue to other namespaces
			if results[ns] == nil {
				results[ns] = &SynthesizeResult{}
			}
			results[ns].Warnings = append(results[ns].Warnings, err.Error())
			continue
		}
		results[ns] = result
	}
	return results, nil
}

func memoryIDs(memories []detailWithVector) []string {
	ids := make([]string, len(memories))
	for i, m := range memories {
		ids[i] = m.ID
	}
	return ids
}

func collectFiles(memories []detailWithVector) []string {
	set := make(map[string]bool)
	for _, m := range memories {
		for _, f := range m.Files {
			set[f] = true
		}
	}
	result := make([]string, 0, len(set))
	for f := range set {
		result = append(result, f)
	}
	sort.Strings(result)
	return result
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
