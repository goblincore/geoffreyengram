package main

// Query represents a benchmark query with ground truth.
type Query struct {
	ID          string      `json:"id"`
	Corpus      string      `json:"corpus"`
	Type        string      `json:"type"` // feature_location, dependency_tracing, concept_search, bug_localization, structural_understanding, feasibility
	Query       string      `json:"query"`
	Difficulty  string      `json:"difficulty"` // easy, medium, hard
	Confidence  string      `json:"confidence"` // high, medium, low
	GroundTruth GroundTruth `json:"ground_truth"`
}

// GroundTruth holds the expected answers for a query.
type GroundTruth struct {
	Files       []string `json:"files"`
	Modules     []string `json:"modules"`
	Concepts    []string `json:"concepts"`
	Explanation string   `json:"explanation"`
}

// SearchResult is a single ranked result from an adapter.
type SearchResult struct {
	Path  string  `json:"path"`
	Score float64 `json:"score"`
}

// AdapterOutput is what an adapter returns for a query.
type AdapterOutput struct {
	Results   []SearchResult `json:"results"`
	Report    string         `json:"report"`     // consult/context output text
	LatencyMs int64          `json:"latency_ms"`
	APICalls  int            `json:"api_calls"`
}

// IRMetrics holds computed IR metrics for a single query.
type IRMetrics struct {
	PrecisionAt3 float64 `json:"precision_at_3"`
	PrecisionAt5 float64 `json:"precision_at_5"`
	RecallAt5    float64 `json:"recall_at_5"`
	RecallAt10   float64 `json:"recall_at_10"`
	NDCG10       float64 `json:"ndcg_10"`
	LatencyMs    int64   `json:"latency_ms"`
	APICalls     int     `json:"api_calls"`
}

// JudgeScores holds LLM judge scores for a report.
type JudgeScores struct {
	Accuracy      float64 `json:"accuracy"`
	Completeness  float64 `json:"completeness"`
	Relevance     float64 `json:"relevance"`
	Actionability float64 `json:"actionability"`
	Rationale     string  `json:"rationale"`
}

// HeadToHead holds the result of an A/B comparison.
type HeadToHead struct {
	Winner    string `json:"winner"` // "dualmem", "unravel", "tie"
	Rationale string `json:"rationale"`
}

// QueryResult holds all results for a single query.
type QueryResult struct {
	Query        Query        `json:"query"`
	DualMem      IRMetrics    `json:"dualmem_ir"`
	Unravel      IRMetrics    `json:"unravel_ir"`
	DualMemJudge *JudgeScores `json:"dualmem_judge,omitempty"`
	UnravelJudge *JudgeScores `json:"unravel_judge,omitempty"`
	H2H          *HeadToHead  `json:"head_to_head,omitempty"`
}

// BenchmarkRun is the top-level output structure.
type BenchmarkRun struct {
	RunDate       string                          `json:"run_date"`
	Corpora       []string                        `json:"corpora"`
	Summary       map[string]IRMetrics            `json:"summary"`
	ReportQuality map[string]JudgeScores          `json:"report_quality,omitempty"`
	H2HSummary    *H2HSummary                     `json:"head_to_head_summary,omitempty"`
	ByCorpus      map[string]map[string]IRMetrics `json:"by_corpus"`
	ByQueryType   map[string]map[string]IRMetrics `json:"by_query_type"`
	ByDifficulty  map[string]map[string]IRMetrics `json:"by_difficulty"`
	PerQuery      []QueryResult                   `json:"per_query"`
}

// H2HSummary aggregates head-to-head results.
type H2HSummary struct {
	DualMemWins int `json:"dualmem_wins"`
	UnravelWins int `json:"unravel_wins"`
	Ties        int `json:"ties"`
}
