package dualmem

import (
	"context"
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
}

// SourceRef traces a context fragment back to its source.
type SourceRef struct {
	Type string // "detail", "episode", "arc", "profile"
	ID   string
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

	// Sketch Path — raw staging
	InsertSketchRaw(userID, content, sector, sessionID string, embedding []float32) error
	GetUnprocessedRaw(userID string, limit int) ([]sketchRaw, error)
	MarkRawProcessed(ids []string) error

	// Sketch Path — episodes
	InsertEpisode(ep *Episode, embedding []float32, userID, embeddingModel string, sourceRawIDs []string) error
	GetEpisodes(userID string, after *time.Time) ([]episodeWithVector, error)
	GetExpiredEpisodes(before time.Time) ([]episodeWithVector, error)
	DeleteEpisode(id string) error

	// Sketch Path — arcs
	InsertArc(arc *Arc, sketchedEmbedding []float32, userID, embeddingModel string, projectionSeed int64) error
	GetArcs(userID string) ([]arcWithVector, error)
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

	// Session markers
	InsertSessionMarker(marker *SessionMarker) error
	GetLatestSessionMarker(namespace string) (*SessionMarker, error)

	// Lifecycle
	Close() error
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
