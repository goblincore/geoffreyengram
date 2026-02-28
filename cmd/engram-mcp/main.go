// engram-mcp exposes geoffreyengram as an MCP stdio server.
//
// Environment variables:
//
//	ENGRAM_DB_PATH   — SQLite database path (default: ./data/engram.db)
//	GEMINI_API_KEY   — Gemini API key for embeddings + optional reflection
//
// Usage:
//
//	go install github.com/goblincore/geoffreyengram/cmd/engram-mcp
//	engram-mcp
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	engram "github.com/goblincore/geoffreyengram"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	dbPath := os.Getenv("ENGRAM_DB_PATH")
	if dbPath == "" {
		dbPath = "./data/engram.db"
	}

	apiKey := os.Getenv("GEMINI_API_KEY")

	cfg := engram.Config{
		DBPath:       dbPath,
		GeminiAPIKey: apiKey,
	}

	cm, err := engram.Init(cfg)
	if err != nil {
		log.Fatalf("engram init: %v", err)
	}
	defer cm.Close()

	server := mcp.NewServer(&mcp.Implementation{
		Name:    "engram-mcp",
		Version: "1.0.0",
	}, nil)

	// --- Tool: remember ---
	mcp.AddTool(server, &mcp.Tool{
		Name:        "remember",
		Description: "Store a memory from a conversation exchange. Returns the memory ID for chaining.",
	}, rememberHandler(cm))

	// --- Tool: recall ---
	mcp.AddTool(server, &mcp.Tool{
		Name:        "recall",
		Description: "Search memories by semantic similarity with composite scoring. Supports temporal and sector filters.",
	}, recallHandler(cm))

	// --- Tool: reflect ---
	mcp.AddTool(server, &mcp.Tool{
		Name:        "reflect",
		Description: "Trigger reflective synthesis — analyze recent memories and generate higher-order observations. Requires a ReflectionProvider to be configured.",
	}, reflectHandler(cm))

	// --- Tool: get_session ---
	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_session",
		Description: "Retrieve all memories from a conversation session. If no session_id is given, returns the user's most recent session.",
	}, getSessionHandler(cm))

	// --- Tool: inspect ---
	mcp.AddTool(server, &mcp.Tool{
		Name:        "inspect",
		Description: "Browse recent memories for a user. Useful for debugging and understanding what the character remembers.",
	}, inspectHandler(cm))

	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Fatalf("engram-mcp: %v", err)
	}
}

// --- Input types ---

type rememberInput struct {
	UserID           string  `json:"user_id"               jsonschema:"User/character pair ID, e.g. lily:player123"`
	UserMessage      string  `json:"user_message"          jsonschema:"What the user said"`
	AssistantMessage string  `json:"assistant_message"     jsonschema:"What the character/assistant replied"`
	SessionID        string  `json:"session_id,omitempty"  jsonschema:"Optional conversation session ID for threading"`
	ParentID         int64   `json:"parent_id,omitempty"   jsonschema:"Optional parent memory ID for conversation chains"`
	SectorHint       string  `json:"sector_hint,omitempty" jsonschema:"Optional sector override: episodic, semantic, procedural, emotional, reflective"`
	Salience         float64 `json:"salience,omitempty"    jsonschema:"Optional salience score 0.0-1.0 (default 0.5)"`
}

type recallInput struct {
	Query     string   `json:"query"               jsonschema:"Search query to find relevant memories"`
	UserID    string   `json:"user_id"              jsonschema:"User/character pair ID"`
	Limit     int      `json:"limit,omitempty"      jsonschema:"Max results to return (default 5)"`
	SessionID string   `json:"session_id,omitempty" jsonschema:"Filter to a specific session"`
	Sectors   []string `json:"sectors,omitempty"    jsonschema:"Filter to specific sectors: episodic, semantic, procedural, emotional, reflective"`
	After     string   `json:"after,omitempty"      jsonschema:"Only memories after this RFC3339 timestamp"`
	Before    string   `json:"before,omitempty"     jsonschema:"Only memories before this RFC3339 timestamp"`
}

type reflectInput struct {
	UserID           string   `json:"user_id"                     jsonschema:"User/character pair ID"`
	CharacterContext string   `json:"character_context,omitempty" jsonschema:"Character personality description to shape reflections"`
	MemoryWindow     int      `json:"memory_window,omitempty"     jsonschema:"How many recent memories to analyze (default 50)"`
	Sectors          []string `json:"sectors,omitempty"           jsonschema:"Which sectors to draw from"`
	MinMemories      int      `json:"min_memories,omitempty"      jsonschema:"Minimum memories needed before reflecting (default 5)"`
}

type getSessionInput struct {
	UserID    string `json:"user_id"              jsonschema:"User/character pair ID (required when getting last session)"`
	SessionID string `json:"session_id,omitempty" jsonschema:"Specific session ID. If empty, returns the last session for the user."`
}

type inspectInput struct {
	UserID  string   `json:"user_id"            jsonschema:"User/character pair ID"`
	Limit   int      `json:"limit,omitempty"    jsonschema:"Max memories to list (default 20)"`
	Sectors []string `json:"sectors,omitempty"  jsonschema:"Filter to specific sectors"`
}

// --- Handlers ---

func rememberHandler(cm *engram.Engram) func(context.Context, *mcp.CallToolRequest, rememberInput) (*mcp.CallToolResult, any, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, input rememberInput) (*mcp.CallToolResult, any, error) {
		id, err := cm.AddWithOptions(engram.AddOptions{
			UserID:           input.UserID,
			UserMessage:      input.UserMessage,
			AssistantMessage: input.AssistantMessage,
			SessionID:        input.SessionID,
			ParentID:         input.ParentID,
			SectorHint:       engram.Sector(input.SectorHint),
			Salience:         input.Salience,
		})
		if err != nil {
			return textResult(fmt.Sprintf("error: %v", err)), nil, nil
		}
		return textResult(jsonString(map[string]any{
			"memory_id": id,
			"status":    "stored",
		})), nil, nil
	}
}

func recallHandler(cm *engram.Engram) func(context.Context, *mcp.CallToolRequest, recallInput) (*mcp.CallToolResult, any, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, input recallInput) (*mcp.CallToolResult, any, error) {
		opts := engram.SearchOptions{
			Query:     input.Query,
			UserID:    input.UserID,
			Limit:     input.Limit,
			SessionID: input.SessionID,
		}

		if input.After != "" {
			t, err := time.Parse(time.RFC3339, input.After)
			if err != nil {
				return textResult(fmt.Sprintf("invalid 'after' timestamp: %v", err)), nil, nil
			}
			opts.After = &t
		}
		if input.Before != "" {
			t, err := time.Parse(time.RFC3339, input.Before)
			if err != nil {
				return textResult(fmt.Sprintf("invalid 'before' timestamp: %v", err)), nil, nil
			}
			opts.Before = &t
		}
		for _, s := range input.Sectors {
			opts.Sectors = append(opts.Sectors, engram.Sector(s))
		}

		results := cm.SearchWithOptions(opts)

		out := make([]map[string]any, len(results))
		for i, r := range results {
			out[i] = searchResultToMap(r)
		}
		return textResult(jsonString(out)), nil, nil
	}
}

func reflectHandler(cm *engram.Engram) func(context.Context, *mcp.CallToolRequest, reflectInput) (*mcp.CallToolResult, any, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, input reflectInput) (*mcp.CallToolResult, any, error) {
		opts := engram.ReflectOptions{
			UserID:           input.UserID,
			CharacterContext: input.CharacterContext,
			MemoryWindow:     input.MemoryWindow,
			MinMemories:      input.MinMemories,
		}
		for _, s := range input.Sectors {
			opts.Sectors = append(opts.Sectors, engram.Sector(s))
		}

		memories, err := cm.Reflect(ctx, opts)
		if err != nil {
			return textResult(fmt.Sprintf("error: %v", err)), nil, nil
		}

		if len(memories) == 0 {
			return textResult(`{"status": "no_new_reflections", "message": "Not enough memories or all observations are duplicates"}`), nil, nil
		}

		out := make([]map[string]any, len(memories))
		for i, m := range memories {
			out[i] = memoryToMap(m)
		}
		return textResult(jsonString(out)), nil, nil
	}
}

func getSessionHandler(cm *engram.Engram) func(context.Context, *mcp.CallToolRequest, getSessionInput) (*mcp.CallToolResult, any, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, input getSessionInput) (*mcp.CallToolResult, any, error) {
		var memories []engram.Memory
		var err error

		if input.SessionID != "" {
			memories, err = cm.GetSession(input.SessionID)
		} else if input.UserID != "" {
			memories, err = cm.GetLastSession(input.UserID)
		} else {
			return textResult(`{"error": "provide either session_id or user_id"}`), nil, nil
		}

		if err != nil {
			return textResult(fmt.Sprintf("error: %v", err)), nil, nil
		}

		out := make([]map[string]any, len(memories))
		for i, m := range memories {
			out[i] = memoryToMap(m)
		}
		return textResult(jsonString(out)), nil, nil
	}
}

func inspectHandler(cm *engram.Engram) func(context.Context, *mcp.CallToolRequest, inspectInput) (*mcp.CallToolResult, any, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, input inspectInput) (*mcp.CallToolResult, any, error) {
		limit := input.Limit
		if limit <= 0 {
			limit = 20
		}

		var sectors []engram.Sector
		for _, s := range input.Sectors {
			sectors = append(sectors, engram.Sector(s))
		}

		memories, err := cm.ListRecent(input.UserID, limit, sectors)
		if err != nil {
			return textResult(fmt.Sprintf("error: %v", err)), nil, nil
		}

		out := make([]map[string]any, len(memories))
		for i, m := range memories {
			out[i] = memoryToMap(m)
		}
		return textResult(jsonString(out)), nil, nil
	}
}

// --- Helpers ---

func textResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: text},
		},
	}
}

func memoryToMap(m engram.Memory) map[string]any {
	return map[string]any{
		"id":          m.ID,
		"content":     m.Content,
		"sector":      m.Sector,
		"salience":    m.Salience,
		"decay_score": m.DecayScore,
		"summary":     m.Summary,
		"session_id":  m.SessionID,
		"parent_id":   m.ParentID,
		"created_at":  m.CreatedAt.Format(time.RFC3339),
	}
}

func searchResultToMap(r engram.SearchResult) map[string]any {
	m := memoryToMap(r.Memory)
	m["composite_score"] = r.CompositeScore
	m["similarity"] = r.Similarity
	return m
}

func jsonString(v any) string {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprintf(`{"error": "marshal: %v"}`, err)
	}
	return string(data)
}
