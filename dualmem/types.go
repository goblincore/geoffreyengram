package dualmem

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// --- Provider interfaces ---

// EmbeddingProvider generates vector embeddings from text.
// Compatible with engram.EmbeddingProvider.
type EmbeddingProvider interface {
	Embed(ctx context.Context, text string, taskType string) ([]float32, error)
	Dimension() int
	// ModelName returns the embedding model identifier (e.g. "gemini-embedding-001").
	// Used to detect provider changes across sessions and annotate stored embeddings.
	ModelName() string
}

// SectorClassifier determines which cognitive sector a memory belongs to.
// Compatible with engram.SectorClassifier.
type SectorClassifier interface {
	Classify(content string) string
}

// EntityExtractor pulls entities from memory content.
// Compatible with engram.EntityExtractor.
type EntityExtractor interface {
	Extract(content string) []Entity
}

// SummarizerProvider generates text summaries for the compression pipeline.
type SummarizerProvider interface {
	SummarizeEpisode(ctx context.Context, messages []string) (string, error)
	SummarizeArc(ctx context.Context, episodes []string) (string, error)
	UpdateProfile(ctx context.Context, currentProfile *ProfileSketch, newArcs []string) (*ProfileSketch, error)
}

// PromoteOpts configures behavior when promoting sketch memories to Detail Path.
type PromoteOpts struct {
	Type     string  // Override type ("warning", "decision", "continuity")
	Salience float64 // Override salience (0 = default 0.7 for promotions)
}

// --- Input/Output types ---

// MemoryInput is the input for Add/AddWithOptions.
type MemoryInput struct {
	UserMessage      string   // The user's message
	AssistantMessage string   // The assistant's response
	SessionID        string   // Optional conversation session ID
	ParentID         string   // Optional parent memory ID (threading)
	SectorHint       string   // Optional: skip classification
	Salience         float64  // Optional: override default 0.5 (0 = use default)
	Entities         []Entity // Optional: pre-extracted entities
	Type             string   // Optional: "decision", "warning", "continuity", "" (general)
	Files            []string // Optional: associated file paths (e.g., source files relevant to this memory)
}

// Entity is an extracted entity for associative linking.
type Entity struct {
	Text string `json:"text"`
	Type string `json:"type"` // "person", "music_artist", "song", "topic", "place"
}

// EntityNode is a canonical entity in the knowledge graph.
type EntityNode struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Type         string    `json:"type"` // "person", "file", "function", "concept", "tool", "project"
	Namespace    string    `json:"namespace"`
	MentionCount int       `json:"mention_count"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// EntityEdge is a relationship between two entities.
type EntityEdge struct {
	ID        string    `json:"id"`
	SourceID  string    `json:"source_id"`
	TargetID  string    `json:"target_id"`
	Relation  string    `json:"relation"` // "uses", "modifies", "depends_on", "implements", etc.
	Strength  float64   `json:"strength"`
	Mentions  int       `json:"mentions"`
	Namespace string    `json:"namespace"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// EntityTriple is an extracted relationship for the entity graph (used during distillation).
type EntityTriple struct {
	Source   Entity `json:"source"`
	Relation string `json:"relation"`
	Target   Entity `json:"target"`
}

// GraphExpansionResult holds memory IDs found via entity graph traversal.
type GraphExpansionResult struct {
	MemoryIDs    []string     // memory IDs found via graph traversal
	MatchedNodes []EntityNode // entities that matched the query
}

// CoChangeEdge represents a co-change relationship between two files.
// Files that appear together in memories (or git commits) accumulate co-change strength.
type CoChangeEdge struct {
	SourcePath string    `json:"source_path"`
	TargetPath string    `json:"target_path"`
	Namespace  string    `json:"namespace"`
	Strength   float64   `json:"strength"`   // Weighted by frequency + recency
	CoCount    int       `json:"co_count"`    // Raw count of co-occurrences
	MemoryIDs  []string  `json:"memory_ids"`  // Memory IDs that evidenced this co-change
	Concepts   []string  `json:"concepts"`    // Entity names that bind these files
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// ExpandedEntityEdge is a neighbor entity ID with the edge that connects it.
// Used by structure-aware graph boost to differentiate edge types and strengths.
type ExpandedEntityEdge struct {
	NeighborID string
	Relation   string
	Strength   float64
}

// GraphBoostConfig configures entity graph boosting in search.
// When set, memories linked to entities matching the query get an additive score boost.
type GraphBoostConfig struct {
	Namespace   string  // Entity graph namespace to query
	BoostWeight float64 // Additive boost for graph-matched memories (default 0.15)
	MaxHops     int     // Graph expansion hops (default 1)
}

// SearchOpts configures dual-path search.
type SearchOpts struct {
	Limit          int                // Max detail memories to return (default 5)
	Weights        map[string]float64 // Per-sector retrieval weights (nil = equal)
	After          *time.Time         // Temporal filter: only after
	Before         *time.Time         // Temporal filter: only before
	SessionID      string             // Filter to session
	Sectors        []string           // Filter to sectors
	IncludeSketch  bool               // Include sketch results (default true)
	TokenBudget    int                // If > 0, limits total context size
	QueryEmbedding []float32          // Pre-computed query embedding; if non-nil, DualSearch skips embedding
	MinSimilarity  float64            // If > 0, exclude detail memories below this cosine similarity
	QueryText      string             // Raw query text for keyword boosting in detail search
	GraphBoost     *GraphBoostConfig  // If non-nil, apply entity graph boost to search results
}

// DualSearchResult contains results from both paths.
type DualSearchResult struct {
	DetailMemories []DetailMemory // Exact, uncompressed matches
	Episodes       []Episode      // Recent episode summaries
	Arcs           []Arc          // Compressed narrative arcs
	Profile        *ProfileSketch // User profile gist (nil if none exists)
}

// DetailMemory is a full-fidelity memory from the Detail Path.
type DetailMemory struct {
	ID              string
	Text            string
	Sector          string
	Salience        float64
	ImportanceScore float64
	Entities        []Entity
	Type            string   // Memory type: "decision", "warning", "continuity", "" (general)
	Files           []string // Associated file paths
	SessionID       string
	CreatedAt       time.Time
	LastAccessedAt  time.Time
	AccessCount     int
	Similarity      float64 // query similarity (set during search)
}

// Episode is a Level 1 sketch — summarized conversation block.
type Episode struct {
	ID            string
	SummaryText   string
	Entities      []Entity
	EmotionalTone string
	CreatedAt     time.Time
	Similarity    float64
}

// Arc is a Level 2 sketch — compressed narrative theme.
type Arc struct {
	ID          string
	SummaryText string
	Entities    []Entity
	EpisodeIDs  []string
	CreatedAt   time.Time
	Similarity  float64
}

// ProfileSketch is the Level 3 user gist.
type ProfileSketch struct {
	UserID            string   `json:"user_id"`
	Interests         []string `json:"interests"`
	PersonalityTraits []string `json:"personality_traits"`
	RelationshipHist  string   `json:"relationship_history"`
	KeyPreferences    []string `json:"key_preferences"`
	CommStyle         string   `json:"communication_style"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// ContextBlock is the pre-formatted output of AssembleContext.
type ContextBlock struct {
	Text       string      // Pre-formatted for LLM prompt injection
	TokenCount int         // Estimated tokens used (chars/4 heuristic)
	Sources    []SourceRef // For attribution/debugging
	Intent     Intent      // Detected or explicit intent used for assembly
}

// SourceRef traces a context fragment back to its source.
type SourceRef struct {
	Type string // "detail", "episode", "arc", "profile"
	ID   string
}

// --- Checkpoints ---

// Checkpoint is a structured session handoff record.
// Stored as JSON in a detail memory with Type="checkpoint".
type Checkpoint struct {
	Task            string   `json:"task"`                       // Short description of what was being done
	Status          string   `json:"status"`                     // "in_progress", "blocked", "paused", "completed"
	FilesActive     []string `json:"files_active,omitempty"`     // Files being worked on (e.g. "auth.go:42-80")
	DecisionPending string   `json:"decision_pending,omitempty"` // Specific question/choice being deliberated
	CompletedSteps  []string `json:"completed_steps,omitempty"`  // What's done
	RemainingSteps  []string `json:"remaining_steps,omitempty"`  // What's left
	BlockedOn       string   `json:"blocked_on,omitempty"`       // What's blocking progress
}

// FormatForContext renders a checkpoint as a compact structured block.
func (cp *Checkpoint) FormatForContext() string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("[Checkpoint: %s] status=%s", cp.Task, cp.Status))
	if len(cp.FilesActive) > 0 {
		sb.WriteString("\n  Files: " + strings.Join(cp.FilesActive, ", "))
	}
	if cp.DecisionPending != "" {
		sb.WriteString("\n  Pending decision: " + cp.DecisionPending)
	}
	if len(cp.CompletedSteps) > 0 {
		sb.WriteString("\n  Done: " + strings.Join(cp.CompletedSteps, "; "))
	}
	if len(cp.RemainingSteps) > 0 {
		sb.WriteString("\n  Remaining: " + strings.Join(cp.RemainingSteps, "; "))
	}
	if cp.BlockedOn != "" {
		sb.WriteString("\n  Blocked on: " + cp.BlockedOn)
	}
	return sb.String()
}

// --- Task intent ---

// Intent represents the detected purpose of a query, used to adjust
// memory type weights during context assembly.
type Intent string

const (
	IntentDefault  Intent = ""
	IntentDebug    Intent = "debug"
	IntentContinue Intent = "continue"
	IntentFeature  Intent = "feature"
	IntentExplore  Intent = "explore"
)

// IntentProfile defines per-type weight multipliers for a given intent.
// Values > 1.0 boost that type, < 1.0 suppress it.
type IntentProfile struct {
	Warning    float64
	Decision   float64
	Continuity float64
	Map        float64
	General    float64
	Seed       float64 // Auto-generated codebase context memories
}

// IntentProfiles maps each intent to its weight multipliers.
var IntentProfiles = map[Intent]IntentProfile{
	IntentDebug: {
		Warning:    2.0,
		Decision:   1.5,
		Continuity: 0.5,
		Map:        1.0,
		General:    0.8,
		Seed:       0.6,
	},
	IntentContinue: {
		Warning:    1.0,
		Decision:   1.0,
		Continuity: 2.0,
		Map:        1.5,
		General:    0.8,
		Seed:       0.5,
	},
	IntentFeature: {
		Warning:    1.0,
		Decision:   1.5,
		Map:        1.5,
		Continuity: 0.8,
		General:    1.0,
		Seed:       1.0,
	},
	IntentExplore: {
		Warning:    0.8,
		Decision:   1.0,
		Continuity: 0.5,
		Map:        2.0,
		General:    1.2,
		Seed:       1.2,
	},
}

// TypeMultiplier returns the weight multiplier for a given memory type under this profile.
func (ip IntentProfile) TypeMultiplier(memType string) float64 {
	switch memType {
	case "warning":
		return ip.Warning
	case "decision":
		return ip.Decision
	case "continuity":
		return ip.Continuity
	case "map":
		return ip.Map
	case "seed":
		if ip.Seed != 0 {
			return ip.Seed
		}
		return ip.General
	default:
		return ip.General
	}
}

// --- Knowledge documents ---

// KnowledgeDoc is a synthesized concept document, created by clustering and
// summarizing related detail memories. Think of it as one section of an
// auto-generated AGENTS.md — coherent prose about a system or concept.
type KnowledgeDoc struct {
	ID         string    `json:"id"`
	Namespace  string    `json:"namespace"`
	Topic      string    `json:"topic"`       // short identifier: "hdc-encoder", "auth-middleware"
	Content    string    `json:"content"`     // synthesized markdown prose
	Files      []string  `json:"files"`       // associated file paths
	SourceIDs  []string  `json:"source_ids"`  // detail memory IDs that were synthesized
	Embedding  []float32 `json:"-"`           // 768-dim for relevance ranking
	TokenCount int       `json:"token_count"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// FormatForContext renders a knowledge doc as a labeled context section.
func (kd *KnowledgeDoc) FormatForContext() string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("[Knowledge: %s]\n", kd.Topic))
	sb.WriteString(kd.Content)
	if len(kd.Files) > 0 {
		sb.WriteString("\n  Files: " + strings.Join(kd.Files, ", "))
	}
	return sb.String()
}

// SynthesizeOpts configures the Synthesize operation.
type SynthesizeOpts struct {
	Force  bool   // Re-synthesize all docs regardless of staleness
	Topic  string // Only synthesize this specific topic (empty = all)
	DryRun bool   // Report what would be done without making changes
}

// SynthesizeResult describes what Synthesize produced.
type SynthesizeResult struct {
	Created  []KnowledgeDoc // newly created docs
	Updated  []KnowledgeDoc // re-synthesized existing docs
	Skipped  int            // docs already fresh
	Orphaned int            // memories that didn't cluster (< 3 in group)
	Warnings []string
	DryRun   bool
}

// --- Import graph / clustering ---

// ImportGraph represents import relationships between code modules.
type ImportGraph struct {
	Nodes map[string]*ModuleMap // path → module
	Edges []ImportEdge          // directed import edges
	Adj   map[string][]string   // adjacency list (undirected)
}

// ImportEdge is a directed import from one module to another.
type ImportEdge struct {
	From string // importer module path
	To   string // imported module path
}

// Cluster is a group of related modules detected by import-graph analysis.
type Cluster struct {
	Name    string      `json:"name"`    // Auto-generated from dominant types/paths
	Modules []ModuleMap `json:"modules"` // The modules in this cluster
}

// --- Garbage collection ---

// GCOptions configures the GarbageCollect operation.
type GCOptions struct {
	DryRun  bool // If true, report what would be done without making changes
	Verbose bool // If true, log each affected entry
}

// GCReport summarizes what GarbageCollect did (or would do in dry-run mode).
type GCReport struct {
	ExpiredEpisodes     int // Episodes past retention, deleted
	ExpiredArcs         int // Arcs past retention, deleted
	StaleDetails        int // Git-stale details, demoted to sketch
	SupersededContinuity int // Duplicate continuity entries, demoted
	AccessColdDetails   int // Unaccessed details below importance threshold, demoted
	Entries             []GCEntry // Individual entries affected (populated when Verbose=true)
}

// GCEntry describes a single memory affected by GC.
type GCEntry struct {
	ID     string
	Action string // "delete", "demote"
	Reason string // "expired_episode", "expired_arc", "git_stale", "superseded", "access_cold"
	Text   string // Truncated content for display
}

// --- Sector configuration ---

// SectorConfig defines the classification sectors and their behavior.
type SectorConfig struct {
	// Anchors maps sector name → natural-language description for embedding classification.
	// Each description is embedded once at init and used for zero-shot cosine classification.
	Anchors map[string]string

	// DetailBias lists sectors that get a 1.0 bonus in importance scoring
	// (i.e., biased toward Detail Path). Sectors not listed get 0.0.
	DetailBias []string

	// Default is the fallback sector when classification fails or no classifier is configured.
	Default string
}

// CodingSectors returns sector config optimized for coding agent memory.
func CodingSectors() *SectorConfig {
	return &SectorConfig{
		Anchors: map[string]string{
			"decision":   "A choice was made between alternatives. An architectural decision, a rejected approach, a dead end discovered, or a trade-off evaluated.",
			"warning":    "Something must not be changed or requires caution. Fragile code, intentional quirks, invariants that must hold, or known pitfalls.",
			"map":        "Where things are in the codebase. File relationships, navigation landmarks, which files implement a feature, change coupling between files.",
			"continuity": "Work in progress. What was done, what remains, session handoffs, current status of an ongoing task.",
		},
		DetailBias: []string{"decision", "warning"},
		Default:    "decision",
	}
}

// NPCSectors returns sector config for NPC/character memory (engram-compatible).
func NPCSectors() *SectorConfig {
	return &SectorConfig{
		Anchors: map[string]string{
			"episodic":   "Something happened at a specific time. A visit, encounter, meeting, or event that someone experienced or witnessed.",
			"semantic":   "A fact, preference, or stable truth about someone. What they like, who they are, biographical details, knowledge.",
			"procedural": "How to do something. A technique, method, process, recipe, instructions, or step-by-step skill.",
			"emotional":  "Someone felt or expressed an emotion. They seemed happy, sad, angry, nervous, excited, or moved during an interaction.",
		},
		DetailBias: []string{"semantic", "procedural"},
		Default:    "semantic",
	}
}

// --- Configuration ---

// Config holds DualMem initialization parameters.
type Config struct {
	// Storage backend (SQLite-only for now; Postgres planned)
	SQLitePath string // Path to SQLite database file

	// Providers (reuses engram-compatible interfaces)
	EmbeddingProvider EmbeddingProvider
	Classifier        SectorClassifier
	EntityExtractor   EntityExtractor
	Summarizer        SummarizerProvider // LLM for episode/arc/profile summarization

	// Sector classification
	Sectors *SectorConfig // nil = CodingSectors (default)

	// Capacity
	MaxDetailPerUser int     // Default 100
	MaxSeedPerUser   int     // Default 30 (separate cap for auto-generated seed memories)
	ImportanceTheta  float64 // Default 0.65

	// Pipeline intervals
	EpisodeBatchInterval  time.Duration // Debounce delay after last Add before episode processing. Default 30s.
	ArcBuildInterval      time.Duration // Default 24h
	ProfileUpdateInterval time.Duration // Default 7 days

	// Retention
	EpisodeRetentionDays int // Default 30
	ArcRetentionDays     int // Default 90

	// Random projection
	ProjectionSeed int64 // 0 = auto-generate and store

	// Codebase context (optional — enables code maps + structural diffs)
	RootDir string // Project root directory (for code map scanning + git diffs)
}

// ApplyDefaults fills zero-valued fields with sensible defaults.
func (c *Config) ApplyDefaults() {
	if c.SQLitePath == "" {
		c.SQLitePath = "./data/dualmem.db"
	}
	if c.Sectors == nil {
		c.Sectors = CodingSectors()
	}
	if c.MaxDetailPerUser == 0 {
		c.MaxDetailPerUser = 100
	}
	if c.MaxSeedPerUser == 0 {
		c.MaxSeedPerUser = 30
	}
	if c.ImportanceTheta == 0 {
		c.ImportanceTheta = 0.65
	}
	if c.EpisodeBatchInterval == 0 {
		c.EpisodeBatchInterval = 30 * time.Second
	}
	if c.ArcBuildInterval == 0 {
		c.ArcBuildInterval = 24 * time.Hour
	}
	if c.ProfileUpdateInterval == 0 {
		c.ProfileUpdateInterval = 7 * 24 * time.Hour
	}
	if c.EpisodeRetentionDays == 0 {
		c.EpisodeRetentionDays = 30
	}
	if c.ArcRetentionDays == 0 {
		c.ArcRetentionDays = 90
	}
}

// --- Store interface ---

// Store defines the persistence layer for DualMem.
// Implementations: SQLiteStore (local/dev), PostgresStore (production, future).
type Store interface {
	// Detail Path
	InsertDetail(dm *DetailMemory, embedding []float32, userID string) error
	GetDetailMemories(userID string) ([]detailWithVector, error)
	GetDetailCount(userID string) (int, error)
	GetLowestImportanceDetail(userID string) (*detailWithVector, error)
	DeleteDetail(id string) error
	UpdateDetailImportance(id string, score float64) error
	TouchDetail(id string) error

	// Detail Path — filtered queries
	GetDetailsByType(userID, memType string) ([]detailWithVector, error)
	GetDetailsByFiles(userID, filename string, types []string, limit int) ([]detailWithVector, error)
	GetFilesWithMemories(userID string, types []string) ([]string, error)

	// Detail Path — seed memories (separate cap from organic)
	GetDetailCountExcludingSeeds(userID string) (int, error)
	CountSeedMemories(userID string) (int, error)
	DeleteSeedMemories(userID string) error

	// Sketch Path — raw staging
	InsertSketchRaw(userID, content, sector, sessionID string, embedding []float32) error
	GetUnprocessedRaw(userID string, limit int) ([]sketchRaw, error)
	GetAllSketchRaw(userID string, limit int) ([]sketchRaw, error)
	GetSketchRawByID(id string) (*sketchRaw, error)
	DeleteSketchRaw(id string) error
	MarkRawProcessed(ids []string) error

	// Sketch Path — episodes
	InsertEpisode(ep *Episode, embedding []float32, userID, embeddingModel string, sourceRawIDs []string) error
	GetEpisodes(userID string, after *time.Time) ([]episodeWithVector, error)
	GetEpisodeByID(id string) (*episodeWithVector, error)
	GetExpiredEpisodes(before time.Time) ([]episodeWithVector, error)
	DeleteEpisode(id string) error

	// Sketch Path — arcs
	InsertArc(arc *Arc, sketchedEmbedding []float32, userID, embeddingModel string, projectionSeed int64) error
	GetArcs(userID string) ([]arcWithVector, error)
	GetExpiredArcs(before time.Time) ([]arcWithVector, error)
	DeleteArc(id string) error

	// Sketch Path — profiles
	UpsertProfile(profile *ProfileSketch, sketchedEmbedding []float32, embeddingModel string, projectionSeed int64) error
	GetProfile(userID string) (*profileWithVector, error)
	GetUsersNeedingProfileUpdate() ([]string, error)

	// Config
	GetConfigValue(key string) (string, error)
	SetConfigValue(key, value string) error

	// Code maps
	UpsertCodeMap(namespace, rootDir, zoom1, zoom2JSON, gitCommit string) error
	GetCodeMap(namespace string) (*StoredCodeMap, error)

	// Code map embeddings
	UpsertCodeMapEmbeddings(namespace string, embeddings map[string]ModuleEmbedding, embeddingModel string) error
	GetCodeMapEmbeddings(namespace string) (map[string][]float32, string, error)
	DeleteCodeMapEmbeddings(namespace string) error

	// Namespaces
	ListNamespaces() ([]string, error)

	// Knowledge documents
	UpsertKnowledgeDoc(doc *KnowledgeDoc, embedding []float32) error
	GetKnowledgeDocs(namespace string) ([]KnowledgeDoc, error)
	GetKnowledgeDocByTopic(namespace, topic string) (*KnowledgeDoc, error)
	DeleteKnowledgeDoc(namespace, topic string) error
	// GetUncoveredMemories returns detail memories whose IDs are NOT in any knowledge doc's source_ids.
	GetUncoveredMemories(namespace string) ([]detailWithVector, error)

	// Session markers
	InsertSessionMarker(marker *SessionMarker) error
	GetLatestSessionMarker(namespace string) (*SessionMarker, error)

	// Entity graph
	UpsertEntity(node *EntityNode) (string, error) // returns entity ID
	UpsertEdge(edge *EntityEdge) error
	LinkMemoryToEntity(memoryID, entityID, namespace string) error
	ExpandEntities(namespace string, entityIDs []string, maxNeighbors int) ([]string, error) // returns neighbor entity IDs
	GetMemoryIDsByEntities(entityIDs []string) ([]string, error)
	GetEntitiesByName(namespace, name string, limit int) ([]EntityNode, error)
	GetEntityStats(namespace string) (totalNodes int, totalEdges int, totalLinks int, err error)
	GetTopEntities(namespace string, limit int) ([]EntityNode, error)
	GetEntityEdges(entityID string) ([]EntityEdge, error)
	// ExpandEntitiesWithEdges returns neighbor entity IDs with edge type and strength info.
	ExpandEntitiesWithEdges(namespace string, entityIDs []string, maxNeighbors int) ([]ExpandedEntityEdge, error)

	// Co-change graph
	UpsertCoChange(namespace, source, target, memoryID string) error
	GetCoChangeNeighbors(namespace, path string, minStrength float64, limit int) ([]CoChangeEdge, error)
	GetCoChangePaths(namespace string, paths []string, limit int) ([]CoChangeEdge, error)
	GetCoChangeAll(namespace string, limit int) ([]CoChangeEdge, error)
	UpdateCoChangeConcepts(namespace, source, target, conceptsJSON string) error
	DecayCoChange(namespace string, halfLifeDays int) error

	// Lifecycle
	Close() error
}

// ModuleEmbedding pairs a module's summary text with its embedding vector.
type ModuleEmbedding struct {
	Summary   string
	Embedding []float32
}

// StoredCodeMap is a code map as persisted in SQLite.
type StoredCodeMap struct {
	Namespace   string
	RootDir     string
	Zoom1       string
	Zoom2JSON   string
	GeneratedAt time.Time
	GitCommit   string
}

// SessionMarker records git state at the end of a dualmem context call.
type SessionMarker struct {
	ID        string
	Namespace string
	Branch    string
	Commit    string
	Timestamp time.Time
}

// Internal types pairing domain objects with their embeddings.
type detailWithVector struct {
	DetailMemory
	Vector []float32
	UserID string
}

type sketchRaw struct {
	ID        string
	UserID    string
	Content   string
	Sector    string
	SessionID string
	Embedding []float32
	CreatedAt time.Time
}

type episodeWithVector struct {
	Episode
	Vector []float32
	UserID string
}

type arcWithVector struct {
	Arc
	SketchedVector []float32
	UserID         string
}

type profileWithVector struct {
	ProfileSketch
	SketchedVector []float32
}
