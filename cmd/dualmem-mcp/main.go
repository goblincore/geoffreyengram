// dualmem-mcp exposes DualMem as an MCP stdio server with HDC-powered code search.
//
// Environment variables:
//
//	DUALMEM_DB_PATH  — SQLite database path (default: ~/.dualmem/memory.db)
//	DUALMEM_ROOT_DIR — Codebase root directory (default: cwd)
//	GEMINI_API_KEY   — Gemini API key for embeddings (required)
//
// Usage:
//
//	go install github.com/goblincore/geoffreyengram/cmd/dualmem-mcp@latest
//	dualmem-mcp
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/goblincore/geoffreyengram/dualmem"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	dbPath := os.Getenv("DUALMEM_DB_PATH")
	if dbPath == "" {
		home, _ := os.UserHomeDir()
		dbPath = filepath.Join(home, ".dualmem", "memory.db")
	}

	rootDir := os.Getenv("DUALMEM_ROOT_DIR")
	if rootDir == "" {
		rootDir, _ = os.Getwd()
	}

	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		log.Fatal("dualmem-mcp: GEMINI_API_KEY is required")
	}

	embedder := dualmem.NewGeminiEmbedder(apiKey, 768)

	// Classifier needs context for anchor embedding
	ctx := context.Background()
	classifier, err := dualmem.NewEmbeddingClassifier(ctx, embedder, dualmem.CodingSectors())
	if err != nil {
		log.Fatalf("dualmem classifier init: %v", err)
	}

	engine, err := dualmem.New(dualmem.Config{
		SQLitePath:        dbPath,
		EmbeddingProvider: embedder,
		Classifier:        classifier,
		RootDir:           rootDir,
	})
	if err != nil {
		log.Fatalf("dualmem init: %v", err)
	}
	defer engine.Close()

	// Derive namespace from directory name
	namespace := "claude:" + filepath.Base(rootDir)

	server := mcp.NewServer(&mcp.Implementation{
		Name:    "dualmem-mcp",
		Version: "0.1.0",
	}, nil)

	// --- Tool: search_codebase ---
	mcp.AddTool(server, &mcp.Tool{
		Name:        "search_codebase",
		Description: "Search the codebase by natural language query using HDC vector ranking. Returns modules ranked by relevance — no extra API calls, instant results. Use this instead of grep/glob to find relevant files.",
	}, searchCodebaseHandler(engine, namespace))

	// --- Tool: get_codemap ---
	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_codemap",
		Description: "Get the full structural code map of the project. Returns system overview and per-module details (types, entry points, imports). Use for 'what does this project look like?' questions.",
	}, getCodemapHandler(engine, namespace))

	// --- Tool: search_memory ---
	mcp.AddTool(server, &mcp.Tool{
		Name:        "search_memory",
		Description: "Search cross-session memories by semantic similarity. Finds past decisions, warnings, debugging insights, and continuity notes.",
	}, searchMemoryHandler(engine, namespace))

	// --- Tool: get_context ---
	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_context",
		Description: "Assemble a complete context block within a token budget. Combines: code map (HDC-ranked), structural diff, checkpoints, and relevant memories. Best for session-start context loading.",
	}, getContextHandler(engine, namespace))

	// --- Tool: save_memory ---
	mcp.AddTool(server, &mcp.Tool{
		Name:        "save_memory",
		Description: "Save a memory for cross-session recall. Use for decisions, warnings, debugging insights, or session continuity notes.",
	}, saveMemoryHandler(engine, namespace))

	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Fatalf("dualmem-mcp: %v", err)
	}
}

// --- Input types ---

type searchCodebaseInput struct {
	Query string `json:"query" jsonschema:"Natural language query to find relevant code modules"`
	Limit int    `json:"limit,omitempty" jsonschema:"Max results to return (default 10)"`
}

type getCodemapInput struct {
	MaxTokens int `json:"max_tokens,omitempty" jsonschema:"Token budget for the code map output (default 400)"`
}

type searchMemoryInput struct {
	Query         string  `json:"query" jsonschema:"Search query to find relevant memories"`
	Limit         int     `json:"limit,omitempty" jsonschema:"Max results (default 5)"`
	MinSimilarity float64 `json:"min_similarity,omitempty" jsonschema:"Minimum cosine similarity threshold (0.0-1.0)"`
}

type getContextInput struct {
	Query  string `json:"query" jsonschema:"Query or task description for context ranking"`
	Budget int    `json:"budget,omitempty" jsonschema:"Token budget (default 3000)"`
	Intent string `json:"intent,omitempty" jsonschema:"Override intent: debug, continue, feature, explore"`
}

type saveMemoryInput struct {
	Text     string  `json:"text" jsonschema:"Memory content to save"`
	Type     string  `json:"type,omitempty" jsonschema:"Memory type: warning, decision, continuity (default: general)"`
	Files    string  `json:"files,omitempty" jsonschema:"Comma-separated file paths associated with this memory"`
	Salience float64 `json:"salience,omitempty" jsonschema:"Importance score 0.0-1.0 (default 0.7)"`
}

// --- Handlers ---

func searchCodebaseHandler(engine *dualmem.Engine, ns string) func(context.Context, *mcp.CallToolRequest, searchCodebaseInput) (*mcp.CallToolResult, any, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, input searchCodebaseInput) (*mcp.CallToolResult, any, error) {
		if input.Query == "" {
			return nil, nil, fmt.Errorf("query is required")
		}
		limit := input.Limit
		if limit <= 0 {
			limit = 10
		}

		cm, embs := engine.GetCodeMap(ctx, ns)
		if cm == nil {
			return textResult("No code map available. Ensure DUALMEM_ROOT_DIR points to a source directory."), nil, nil
		}

		results := dualmem.SearchCodeMap(cm, embs, input.Query, limit)

		type moduleOut struct {
			Path        string   `json:"path"`
			Similarity  float64  `json:"similarity"`
			Summary     string   `json:"summary"`
			Language    string   `json:"language"`
			KeyTypes    []string `json:"key_types,omitempty"`
			EntryPoints []string `json:"entry_points,omitempty"`
			Imports     []string `json:"imports,omitempty"`
			Identifiers []string `json:"identifiers,omitempty"`
		}

		out := make([]moduleOut, len(results))
		for i, r := range results {
			out[i] = moduleOut{
				Path:        r.Path,
				Similarity:  r.Similarity,
				Summary:     r.Summary,
				Language:    r.Language,
				KeyTypes:    r.KeyTypes,
				EntryPoints: r.EntryPoints,
				Imports:     r.Imports,
				Identifiers: r.Identifiers,
			}
		}

		b, _ := json.MarshalIndent(out, "", "  ")
		return textResult(string(b)), nil, nil
	}
}

func getCodemapHandler(engine *dualmem.Engine, ns string) func(context.Context, *mcp.CallToolRequest, getCodemapInput) (*mcp.CallToolResult, any, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, input getCodemapInput) (*mcp.CallToolResult, any, error) {
		maxTokens := input.MaxTokens
		if maxTokens <= 0 {
			maxTokens = 400
		}

		cm, embs := engine.GetCodeMap(ctx, ns)
		if cm == nil {
			return textResult("No code map available."), nil, nil
		}

		text := cm.RenderAtBudget(maxTokens, nil, embs)
		return textResult(text), nil, nil
	}
}

func searchMemoryHandler(engine *dualmem.Engine, ns string) func(context.Context, *mcp.CallToolRequest, searchMemoryInput) (*mcp.CallToolResult, any, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, input searchMemoryInput) (*mcp.CallToolResult, any, error) {
		if input.Query == "" {
			return nil, nil, fmt.Errorf("query is required")
		}
		limit := input.Limit
		if limit <= 0 {
			limit = 5
		}

		results, err := engine.DualSearch(ctx, ns, input.Query, dualmem.SearchOpts{
			Limit:         limit,
			IncludeSketch: true,
			MinSimilarity: input.MinSimilarity,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("search failed: %w", err)
		}

		var lines []string
		for _, d := range results.DetailMemories {
			prefix := ""
			if d.Type != "" {
				prefix = fmt.Sprintf("[%s] ", d.Type)
			}
			line := fmt.Sprintf("%s%s (sim=%.3f)", prefix, d.Text, d.Similarity)
			if len(d.Files) > 0 {
				line += fmt.Sprintf("\n  Files: %s", strings.Join(d.Files, ", "))
			}
			lines = append(lines, line)
		}
		for _, ep := range results.Episodes {
			lines = append(lines, fmt.Sprintf("[episode] %s", ep.SummaryText))
		}

		if len(lines) == 0 {
			return textResult("No memories found."), nil, nil
		}
		return textResult(strings.Join(lines, "\n\n")), nil, nil
	}
}

func getContextHandler(engine *dualmem.Engine, ns string) func(context.Context, *mcp.CallToolRequest, getContextInput) (*mcp.CallToolResult, any, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, input getContextInput) (*mcp.CallToolResult, any, error) {
		budget := input.Budget
		if budget <= 0 {
			budget = 3000
		}

		var opts *dualmem.ContextOpts
		if input.Intent != "" {
			opts = &dualmem.ContextOpts{Intent: dualmem.Intent(input.Intent)}
		}

		block, err := engine.AssembleContextWith(ctx, ns, input.Query, budget, opts)
		if err != nil {
			return nil, nil, fmt.Errorf("context assembly failed: %w", err)
		}

		return textResult(block.Text), nil, nil
	}
}

func saveMemoryHandler(engine *dualmem.Engine, ns string) func(context.Context, *mcp.CallToolRequest, saveMemoryInput) (*mcp.CallToolResult, any, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, input saveMemoryInput) (*mcp.CallToolResult, any, error) {
		if input.Text == "" {
			return nil, nil, fmt.Errorf("text is required")
		}

		salience := input.Salience
		if salience <= 0 {
			salience = 0.7
		}

		mi := dualmem.MemoryInput{
			UserMessage: input.Text,
			SectorHint:  input.Type,
			Salience:    salience,
		}
		if input.Files != "" {
			mi.Files = strings.Split(input.Files, ",")
			for i := range mi.Files {
				mi.Files[i] = strings.TrimSpace(mi.Files[i])
			}
		}

		err := engine.AddWithOptions(ctx, mi, ns)
		if err != nil {
			return nil, nil, fmt.Errorf("save failed: %w", err)
		}

		return textResult("Memory saved."), nil, nil
	}
}

func textResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: text},
		},
	}
}
