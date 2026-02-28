package engram

import "time"

// Sector represents one of the 5 cognitive memory sectors.
type Sector string

const (
	SectorEpisodic   Sector = "episodic"   // Events, temporal experiences
	SectorSemantic   Sector = "semantic"   // Facts, knowledge
	SectorProcedural Sector = "procedural" // Skills, capabilities
	SectorEmotional  Sector = "emotional"  // Feelings, sentiments
	SectorReflective Sector = "reflective" // Insights, meta-cognition
)

// DefaultDecayRates returns the default per-sector exponential decay rates (per day).
// Lower lambda = slower decay (memories persist longer).
func DefaultDecayRates() map[Sector]float64 {
	return map[Sector]float64{
		SectorEpisodic:   0.005, // hot — events linger long
		SectorSemantic:   0.02,  // warm — facts persist
		SectorProcedural: 0.02,  // warm
		SectorEmotional:  0.005, // hot — feelings persist
		SectorReflective: 0.05,  // cold — insights fade fastest
	}
}

// SectorWeights defines per-personality retrieval weighting.
// Values are multipliers on a sector's contribution to composite score.
type SectorWeights map[Sector]float64

// DefaultSectorWeights returns equal weighting across all sectors.
func DefaultSectorWeights() SectorWeights {
	return SectorWeights{
		SectorEpisodic:   1.0,
		SectorSemantic:   1.0,
		SectorProcedural: 1.0,
		SectorEmotional:  1.0,
		SectorReflective: 1.0,
	}
}

// ScoringWeights controls the composite score formula coefficients.
type ScoringWeights struct {
	Similarity float64 // default 0.6
	Salience   float64 // default 0.2
	Recency    float64 // default 0.1
	LinkWeight float64 // default 0.1
}

// DefaultScoringWeights returns the standard composite formula weights.
func DefaultScoringWeights() ScoringWeights {
	return ScoringWeights{
		Similarity: 0.6,
		Salience:   0.2,
		Recency:    0.1,
		LinkWeight: 0.1,
	}
}

// Memory is the core memory record stored in SQLite.
type Memory struct {
	ID             int64
	Content        string
	Sector         Sector
	Salience       float64 // 0.0 – 1.0
	DecayScore     float64 // Current decayed salience
	LastAccessedAt time.Time
	AccessCount    int
	CreatedAt      time.Time
	UserID         string // e.g. "lily_bartender:player123"
	Summary        string // Short text injected into prompts
	SessionID      string // Conversation session identifier (UUID or caller-provided)
	ParentID       int64  // Previous memory in the conversation chain (0 = none)
}

// AddOptions provides the full API for storing memories with temporal context.
type AddOptions struct {
	UserID           string
	UserMessage      string
	AssistantMessage string
	SessionID        string   // Optional session identifier
	ParentID         int64    // Optional parent memory ID (for threading)
	SectorHint       Sector   // Optional: skip classification
	Salience         float64  // Optional: override default 0.5
	Entities         []Entity // Optional: pre-extracted entities
}

// SearchOptions extends basic search with temporal and session filters.
type SearchOptions struct {
	Query     string
	UserID    string
	Limit     int
	Weights   SectorWeights
	After     *time.Time // Only memories created after this time
	Before    *time.Time // Only memories created before this time
	SessionID string     // Filter to a specific session
	Sectors   []Sector   // Filter to specific sectors
}

// SearchResult is a scored memory returned from retrieval.
type SearchResult struct {
	Memory
	CompositeScore float64
	Similarity     float64
}

// Entity represents an extracted entity for the waypoint graph.
type Entity struct {
	Text string
	Type string // "person", "music_artist", "song", "topic", "place"
}

// Config holds Engram initialization parameters.
type Config struct {
	// Storage
	DBPath             string        // Path to SQLite file (default: ./data/engram.db)
	MaxMemoriesPerUser int           // Default 500
	MinDecayScore      float64       // Memories below this are deleted (default 0.01)

	// Providers (nil = use defaults)
	EmbeddingProvider EmbeddingProvider
	Classifier        SectorClassifier
	EntityExtractor   EntityExtractor

	// Scoring (nil = use defaults)
	ScoringWeights *ScoringWeights

	// Decay
	DecayInterval time.Duration      // Default 12h
	DecayRates    map[Sector]float64 // Per-sector lambda overrides (nil = defaults)

	// Reflection (explicit opt-in — never auto-constructed)
	ReflectionProvider ReflectionProvider
	ReflectionInterval time.Duration // 0 = no automatic reflection (default)

	// Legacy / convenience: used to construct default GeminiEmbedder + HeuristicClassifier
	GeminiAPIKey   string
	EmbedDimension int // Default 768

	// resolved holds the merged decay rates after ApplyDefaults
	decayRates map[Sector]float64
	// resolved scoring weights
	scoringWeights ScoringWeights
}

// ApplyDefaults fills zero-valued fields with sensible defaults.
func (c *Config) ApplyDefaults() {
	if c.DBPath == "" {
		c.DBPath = "./data/engram.db"
	}
	if c.EmbedDimension == 0 {
		c.EmbedDimension = 768
	}
	if c.DecayInterval == 0 {
		c.DecayInterval = 12 * time.Hour
	}
	if c.MaxMemoriesPerUser == 0 {
		c.MaxMemoriesPerUser = 500
	}
	if c.MinDecayScore == 0 {
		c.MinDecayScore = 0.01
	}

	// Resolve decay rates: defaults merged with overrides
	c.decayRates = DefaultDecayRates()
	for sector, lambda := range c.DecayRates {
		c.decayRates[sector] = lambda
	}

	// Resolve scoring weights
	if c.ScoringWeights != nil {
		c.scoringWeights = *c.ScoringWeights
	} else {
		c.scoringWeights = DefaultScoringWeights()
	}
}
