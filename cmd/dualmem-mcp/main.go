// dualmem-mcp exposes DualMem as an MCP stdio server with HDC-powered code search.
//
// This is the universal harness adapter — register it in any MCP-compatible AI coding
// tool (Claude Code, OpenCode, Windsurf, Cursor, etc.) for cross-session memory,
// code search, and context assembly with zero harness-specific configuration.
//
// Environment variables:
//
//	DUALMEM_DB_PATH    — SQLite database path (default: ~/.local/share/dualmem/memories.db)
//	DUALMEM_ROOT_DIR   — Codebase root directory (default: cwd)
//	DUALMEM_NS_PREFIX  — Namespace prefix (default: "claude:")
//	GEMINI_API_KEY     — Gemini API key for embeddings (required)
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
	"sort"
	"strings"
	"time"

	"github.com/goblincore/geoffreyengram/dualmem"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	dbPath := os.Getenv("DUALMEM_DB_PATH")
	if dbPath == "" {
		home, _ := os.UserHomeDir()
		dbPath = filepath.Join(home, ".local", "share", "dualmem", "memories.db")
	}

	rootDir := os.Getenv("DUALMEM_ROOT_DIR")
	if rootDir == "" {
		rootDir, _ = os.Getwd()
	}
	rootDir, _ = filepath.Abs(rootDir)

	nsPrefix := os.Getenv("DUALMEM_NS_PREFIX")
	if nsPrefix == "" {
		nsPrefix = "claude:"
	}

	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		log.Fatal("dualmem-mcp: GEMINI_API_KEY is required")
	}

	// Server-managed session ID (no dependency on harness-specific env vars)
	sessionID := uuid.New().String()[:8]

	// Log startup info to stderr (visible in MCP host logs)
	namespace := nsPrefix + filepath.Base(rootDir)
	fmt.Fprintf(os.Stderr, "dualmem-mcp: root=%s db=%s ns=%s session=%s\n", rootDir, dbPath, namespace, sessionID)

	embedder := dualmem.NewGeminiEmbedder(apiKey, 768)

	ctx := context.Background()
	classifier, err := dualmem.NewEmbeddingClassifier(ctx, embedder, dualmem.CodingSectors())
	if err != nil {
		log.Fatalf("dualmem classifier init: %v", err)
	}

	summarizer := dualmem.NewGeminiSummarizer(apiKey, "")

	engine, err := dualmem.New(dualmem.Config{
		SQLitePath:        dbPath,
		EmbeddingProvider: embedder,
		Classifier:        classifier,
		Summarizer:        summarizer,
		RootDir:           rootDir,
	})
	if err != nil {
		log.Fatalf("dualmem init: %v", err)
	}
	defer engine.Close()

	server := mcp.NewServer(&mcp.Implementation{
		Name:    "dualmem",
		Version: "0.2.0",
	}, nil)

	// =====================================================================
	// Tools
	// =====================================================================

	// --- Tool: get_context ---
	mcp.AddTool(server, &mcp.Tool{
		Name: "get_context",
		Description: `Assemble a complete context block within a token budget. Combines: code map, structural diff (what changed since last session), checkpoints (in-progress work), knowledge docs, and relevant memories.

WHEN TO USE: Call this at the start of every session to load cross-session context. Pass a description of what you're about to work on as the query — this ranks memories by relevance to your task.

The result includes a snapshot_id for instrumentation.`,
	}, getContextHandler(engine, namespace))

	// --- Tool: search_codebase ---
	mcp.AddTool(server, &mcp.Tool{
		Name: "search_codebase",
		Description: `Search the codebase by natural language query using HDC vector ranking. Returns modules ranked by relevance with summaries, key types, entry points, and imports. No API calls needed — instant local results.

WHEN TO USE: Before using grep/glob to find files. Handles conceptual queries like "authentication flow" or "database migrations" that exact-match search can't.`,
	}, searchCodebaseHandler(engine, namespace))

	// --- Tool: search_memory ---
	mcp.AddTool(server, &mcp.Tool{
		Name: "search_memory",
		Description: `Search cross-session memories by semantic similarity. Finds past decisions, warnings, debugging insights, file maps, and continuity notes from previous sessions.

WHEN TO USE: Before exploring files for a feature — a previous session may have already mapped the relevant code paths.`,
	}, searchMemoryHandler(engine, namespace))

	// --- Tool: save_memory ---
	mcp.AddTool(server, &mcp.Tool{
		Name: "save_memory",
		Description: `Save a memory for cross-session recall. Persists decisions, warnings, file maps, debugging insights, or session continuity notes.

WHEN TO USE: When you learn something non-obvious that would help a fresh session. Use type="warning" for code that should NOT be changed, type="decision" for rejected/chosen approaches, type="continuity" for in-progress work handoffs. Set salience=0.9 for critical items.`,
	}, saveMemoryHandler(engine, namespace))

	// --- Tool: get_codemap ---
	mcp.AddTool(server, &mcp.Tool{
		Name: "get_codemap",
		Description: `Get the structural code map of the project. Returns system overview and per-module details (types, entry points, imports). Useful for understanding project architecture at a glance.`,
	}, getCodemapHandler(engine, namespace))

	// --- Tool: file_context (dualmem v2 rework) ---
	mcp.AddTool(server, &mcp.Tool{
		Name: "file_context",
		Description: `Pull everything the memory system knows about a file path: durable facts citing it (with provenance), existing file annotations (warnings, decisions, checkpoints, knowledge docs, workflow hints), and git co-change neighbors (files that change together with this one, derived from history).

WHEN TO USE: Before modifying a file — check for warnings/constraints and find related context. Pay special attention to ⚠ Warning entries, which flag code that should NOT be changed.`,
	}, fileContextHandler(engine, namespace, rootDir, sessionID))

	// --- Tool: file_index ---
	mcp.AddTool(server, &mcp.Tool{
		Name: "file_index",
		Description: `List all files that have associated memories. Use to quickly check which files in the project have cross-session context attached.`,
	}, fileIndexHandler(engine, namespace))

	// --- Tool: checkpoint ---
	mcp.AddTool(server, &mcp.Tool{
		Name: "checkpoint",
		Description: `Save or list structured session handoffs. A checkpoint records: task description, status (in_progress/blocked/paused/completed), active files, completed steps, remaining steps, and blockers.

WHEN TO USE: When pausing work on a task, or at session end, to hand off context to the next session. Call with action="list" to see existing checkpoints, action="save" to create one.`,
	}, checkpointHandler(engine, namespace))

	// --- Tool: distill_session ---
	mcp.AddTool(server, &mcp.Tool{
		Name: "distill_session",
		Description: `Extract and save memories from a session transcript or summary. Pass the key learnings, decisions, and discoveries from this session as text — the system will classify them, extract entities, deduplicate against existing memories, and persist them.

WHEN TO USE: At session end (wrap-up). Summarize what you learned, decided, or discovered, and pass it as the text parameter.`,
	}, distillSessionHandler(engine, namespace, sessionID))

	// --- Tool: get_diff ---
	mcp.AddTool(server, &mcp.Tool{
		Name: "get_diff",
		Description: `Show what changed in the codebase since the last session. Returns added, modified, and deleted files based on git history. Useful at session start to understand recent changes.`,
	}, getDiffHandler(engine, namespace, rootDir))

	// --- Tool: recall (dualmem v2 pull tool) ---
	mcp.AddTool(server, &mcp.Tool{
		Name: "recall",
		Description: `Recall durable facts (decisions, dead-ends, gotchas, preferences) by semantic similarity to a query. Returns self-contained facts with provenance: source (verified/inferred/doc/migrated), short commit SHA, date, and the files they cite.

WHEN TO USE: At a decision point or before exploring a feature — pull precise context instead of relying on the pinned session block. Pass the concept, approach, or file you're reasoning about. Optional kind narrows to one of: decision, deadend, gotcha, preference, reference.`,
	}, recallHandler(engine, namespace, sessionID))

	// --- Tool: precedent (dualmem v2 pull tool) ---
	mcp.AddTool(server, &mcp.Tool{
		Name: "precedent",
		Description: `Did we already try, decide, or reject this approach? Sugar over recall restricted to decision + deadend facts — surfaces prior decisions and the dead-ends we already hit so you don't re-litigate or re-explore.

WHEN TO USE: Before committing to an approach. If this returns a match, read it first — it may already settle the question or warn you off a path that failed.`,
	}, precedentHandler(engine, namespace, sessionID))

	// --- Tool: get_status ---
	mcp.AddTool(server, &mcp.Tool{
		Name: "get_status",
		Description: `Get memory system health: counts of detail memories, knowledge docs, checkpoints, and database info.`,
	}, getStatusHandler(engine, namespace))

	// --- Tool: list_knowledge_docs ---
	mcp.AddTool(server, &mcp.Tool{
		Name: "list_knowledge_docs",
		Description: `List or search synthesized knowledge documents. Knowledge docs are LLM-generated concept summaries that cluster related memories into coherent topics. Pass a query to search by relevance, or omit for a full listing.`,
	}, listKnowledgeDocsHandler(engine, namespace))

	// =====================================================================
	// Prompts
	// =====================================================================

	server.AddPrompt(&mcp.Prompt{
		Name:        "session-start",
		Description: "Load cross-session context at the start of a new session. Call get_context with a description of your task.",
	}, func(ctx context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		return &mcp.GetPromptResult{
			Description: "Session start workflow",
			Messages: []*mcp.PromptMessage{
				{
					Role: "user",
					Content: &mcp.TextContent{
						Text: `Start of session. Use the get_context tool with a description of the current task to load cross-session context. Internalize the result as background knowledge — don't repeat it back unless asked. Pay special attention to:
- ⚠ Warning entries: code that should NOT be changed
- ★ Decision entries: past choices and their rationale
- ↻ Continuity entries: in-progress work from previous sessions
- Checkpoints: structured handoffs with remaining work`,
					},
				},
			},
		}, nil
	})

	server.AddPrompt(&mcp.Prompt{
		Name:        "wrap-up",
		Description: "End-of-session wrap-up — save learnings and checkpoint in-progress work.",
	}, func(ctx context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		return &mcp.GetPromptResult{
			Description: "Session wrap-up workflow",
			Messages: []*mcp.PromptMessage{
				{
					Role: "user",
					Content: &mcp.TextContent{
						Text: `End of session. Please:
1. Summarize what you learned, decided, or discovered this session
2. Call distill_session with that summary to extract and persist memories
3. If there's in-progress work, call checkpoint with action="save" to hand off to the next session`,
					},
				},
			},
		}, nil
	})

	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Fatalf("dualmem-mcp: %v", err)
	}
}

// =====================================================================
// Input types
// =====================================================================

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
	Type     string  `json:"type,omitempty" jsonschema:"Memory type: warning, decision, continuity, map (default: general)"`
	Files    string  `json:"files,omitempty" jsonschema:"Comma-separated file paths associated with this memory"`
	Salience float64 `json:"salience,omitempty" jsonschema:"Importance score 0.0-1.0 (default 0.7)"`
}

type recallInput struct {
	Query string `json:"query" jsonschema:"Concept, approach, or file you are reasoning about"`
	Kind  string `json:"kind,omitempty" jsonschema:"Optional fact kind filter: decision, deadend, gotcha, preference, reference"`
	Limit int    `json:"limit,omitempty" jsonschema:"Max facts to return (default 5)"`
}

type precedentInput struct {
	Approach string `json:"approach" jsonschema:"The approach or decision you are considering — did we already try/decide/reject this?"`
	Limit    int    `json:"limit,omitempty" jsonschema:"Max facts to return (default 5)"`
}

type fileContextInput struct {
	Path  string `json:"path" jsonschema:"File name or path to look up context for (basename or repo-relative path)"`
	Limit int    `json:"limit,omitempty" jsonschema:"Max facts/annotations to return per section (default 5)"`
}

type fileIndexInput struct{}

type checkpointInput struct {
	Action          string   `json:"action" jsonschema:"'save' to create a checkpoint, 'list' to view existing checkpoints"`
	Task            string   `json:"task,omitempty" jsonschema:"Short description of the task (required for save)"`
	Status          string   `json:"status,omitempty" jsonschema:"in_progress, blocked, paused, or completed (default: in_progress)"`
	Files           []string `json:"files,omitempty" jsonschema:"Files being worked on"`
	CompletedSteps  []string `json:"completed_steps,omitempty" jsonschema:"What has been done"`
	RemainingSteps  []string `json:"remaining_steps,omitempty" jsonschema:"What is left to do"`
	BlockedOn       string   `json:"blocked_on,omitempty" jsonschema:"What is blocking progress"`
	DecisionPending string   `json:"decision_pending,omitempty" jsonschema:"Question or choice being deliberated"`
}

type distillSessionInput struct {
	Text    string `json:"text" jsonschema:"Session summary or transcript to extract memories from"`
	DryRun  bool   `json:"dry_run,omitempty" jsonschema:"Preview what would be extracted without saving"`
}

type getDiffInput struct{}

type getStatusInput struct{}

type listKnowledgeDocsInput struct {
	Query string `json:"query,omitempty" jsonschema:"Optional search query to rank docs by relevance"`
	Limit int    `json:"limit,omitempty" jsonschema:"Max docs to return (default 10)"`
}

// =====================================================================
// Handlers
// =====================================================================

func searchCodebaseHandler(engine *dualmem.Engine, ns string) func(context.Context, *mcp.CallToolRequest, searchCodebaseInput) (*mcp.CallToolResult, any, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, input searchCodebaseInput) (*mcp.CallToolResult, any, error) {
		if input.Query == "" {
			return nil, nil, fmt.Errorf("query is required")
		}
		limit := input.Limit
		if limit <= 0 {
			limit = 10
		}

		cm, idx := engine.GetCodeMap(ctx, ns)
		if cm == nil {
			return textResult("No code map available. Ensure DUALMEM_ROOT_DIR points to a source directory."), nil, nil
		}

		results := dualmem.SearchCodeMap(cm, idx, input.Query, limit)

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

		cm, idx := engine.GetCodeMap(ctx, ns)
		if cm == nil {
			return textResult("No code map available."), nil, nil
		}

		var embs map[string][]float32
		if idx != nil {
			embs = idx.HDCVectors
		}
		text := cm.RenderAtBudget(maxTokens, nil, embs, nil)
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

		text := block.Text

		return textResult(text), nil, nil
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

func fileContextHandler(engine *dualmem.Engine, ns, rootDir, sessionID string) func(context.Context, *mcp.CallToolRequest, fileContextInput) (*mcp.CallToolResult, any, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, input fileContextInput) (*mcp.CallToolResult, any, error) {
		if input.Path == "" {
			return nil, nil, fmt.Errorf("path is required")
		}
		limit := input.Limit
		if limit <= 0 {
			limit = 5
		}

		// git co-change neighbors are derived purely from history (git_cochange.go),
		// never from the memory-based cochange layer slated for deletion. Best-effort:
		// any error or empty graph just omits the section.
		neighbors := gitCoChangeNeighbors(rootDir, input.Path, 5)

		// Existing file annotations: detail memories, knowledge docs, checkpoints,
		// workflow hints. None of these touch the entity graph or memory cochange.
		annotations := engine.FileAnnotations(ctx, ns, []string{input.Path}, limit)

		// Durable facts citing this path (v2 primary store).
		facts, err := engine.FactsForFile(ns, input.Path, limit)
		if err != nil {
			return nil, nil, fmt.Errorf("file facts lookup failed: %w", err)
		}

		// Record served fact IDs for task 7 instrumentation. Only facts are facts —
		// annotations and co-change neighbors are not durable-fact IDs.
		engine.RecordServed(sessionID, servedFactIDs(facts), "file_context")

		if len(facts) == 0 && len(annotations) == 0 && len(neighbors) == 0 {
			return textResult(fmt.Sprintf("No context for %s.", input.Path)), nil, nil
		}

		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("Context for %s:\n", input.Path))

		if len(facts) > 0 {
			sb.WriteString("\nFacts:\n")
			sb.WriteString(formatFacts(facts))
		}

		if len(annotations) > 0 {
			sb.WriteString("\nAnnotations:\n")
			var lines []string
			for _, a := range annotations {
				icon := annotationIcon(a.Type)
				lines = append(lines, fmt.Sprintf("%s[%s] %s", icon, a.Type, a.Text))
			}
			sb.WriteString(strings.Join(lines, "\n"))
		}

		if len(neighbors) > 0 {
			sb.WriteString("\nCo-change neighbors (git-derived):\n")
			// Stable ordering by descending weight.
			type kv struct {
				path   string
				weight float64
			}
			sorted := make([]kv, 0, len(neighbors))
			for p, w := range neighbors {
				sorted = append(sorted, kv{p, w})
			}
			sort.Slice(sorted, func(i, j int) bool { return sorted[i].weight > sorted[j].weight })
			for _, n := range sorted {
				sb.WriteString(fmt.Sprintf("  %s (co-change %.2f)\n", n.path, n.weight))
			}
		}

		return textResult(strings.TrimRight(sb.String(), "\n")), nil, nil
	}
}

func recallHandler(engine *dualmem.Engine, ns, sessionID string) func(context.Context, *mcp.CallToolRequest, recallInput) (*mcp.CallToolResult, any, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, input recallInput) (*mcp.CallToolResult, any, error) {
		if input.Query == "" {
			return nil, nil, fmt.Errorf("query is required")
		}
		limit := input.Limit
		if limit <= 0 {
			limit = 5
		}

		facts, err := engine.SearchFacts(ctx, input.Query, ns, input.Kind, limit)
		if err != nil {
			return nil, nil, fmt.Errorf("recall failed: %w", err)
		}

		engine.RecordServed(sessionID, servedFactIDs(facts), "recall")

		if len(facts) == 0 {
			return textResult("No matching facts found."), nil, nil
		}
		return textResult(fmt.Sprintf("Recalled %d fact(s):\n\n%s", len(facts), formatFacts(facts))), nil, nil
	}
}

func precedentHandler(engine *dualmem.Engine, ns, sessionID string) func(context.Context, *mcp.CallToolRequest, precedentInput) (*mcp.CallToolResult, any, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, input precedentInput) (*mcp.CallToolResult, any, error) {
		if input.Approach == "" {
			return nil, nil, fmt.Errorf("approach is required")
		}
		limit := input.Limit
		if limit <= 0 {
			limit = 5
		}

		facts, err := engine.SearchFactsMulti(ctx, input.Approach, ns, []string{dualmem.FactKindDecision, dualmem.FactKindDeadEnd}, limit)
		if err != nil {
			return nil, nil, fmt.Errorf("precedent search failed: %w", err)
		}

		engine.RecordServed(sessionID, servedFactIDs(facts), "precedent")

		if len(facts) == 0 {
			return textResult("No prior decision or dead-end found for this approach."), nil, nil
		}
		return textResult(fmt.Sprintf("%d prior decision(s)/dead-end(s):\n\n%s", len(facts), formatFacts(facts))), nil, nil
	}
}

func fileIndexHandler(engine *dualmem.Engine, ns string) func(context.Context, *mcp.CallToolRequest, fileIndexInput) (*mcp.CallToolResult, any, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, input fileIndexInput) (*mcp.CallToolResult, any, error) {
		files, err := engine.FileIndex(ctx, ns)
		if err != nil {
			return nil, nil, fmt.Errorf("file index failed: %w", err)
		}

		if len(files) == 0 {
			return textResult("No files with associated memories."), nil, nil
		}
		return textResult(fmt.Sprintf("Files with memories (%d):\n%s", len(files), strings.Join(files, "\n"))), nil, nil
	}
}

func checkpointHandler(engine *dualmem.Engine, ns string) func(context.Context, *mcp.CallToolRequest, checkpointInput) (*mcp.CallToolResult, any, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, input checkpointInput) (*mcp.CallToolResult, any, error) {
		switch input.Action {
		case "list", "":
			cps, err := engine.GetCheckpoints(ctx, ns)
			if err != nil {
				return nil, nil, fmt.Errorf("get checkpoints: %w", err)
			}
			if len(cps) == 0 {
				return textResult("No active checkpoints."), nil, nil
			}
			var lines []string
			for _, cp := range cps {
				lines = append(lines, cp.FormatForContext())
			}
			return textResult(strings.Join(lines, "\n\n")), nil, nil

		case "save":
			if input.Task == "" {
				return nil, nil, fmt.Errorf("task is required for save action")
			}
			status := input.Status
			if status == "" {
				status = "in_progress"
			}
			cp := &dualmem.Checkpoint{
				Task:            input.Task,
				Status:          status,
				FilesActive:     input.Files,
				CompletedSteps:  input.CompletedSteps,
				RemainingSteps:  input.RemainingSteps,
				BlockedOn:       input.BlockedOn,
				DecisionPending: input.DecisionPending,
			}
			if err := engine.SaveCheckpoint(ctx, ns, cp); err != nil {
				return nil, nil, fmt.Errorf("save checkpoint: %w", err)
			}
			return textResult("Checkpoint saved."), nil, nil

		default:
			return nil, nil, fmt.Errorf("unknown action %q — use 'save' or 'list'", input.Action)
		}
	}
}

func distillSessionHandler(engine *dualmem.Engine, ns, sessionID string) func(context.Context, *mcp.CallToolRequest, distillSessionInput) (*mcp.CallToolResult, any, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, input distillSessionInput) (*mcp.CallToolResult, any, error) {
		if input.Text == "" {
			return nil, nil, fmt.Errorf("text is required — pass a session summary or transcript")
		}

		result, err := engine.Distill(ctx, dualmem.DistillOpts{
			Text:      input.Text,
			DryRun:    input.DryRun,
			Namespace: ns,
		}, ns)
		if err != nil {
			return nil, nil, fmt.Errorf("distill failed: %w", err)
		}

		var sb strings.Builder
		if input.DryRun {
			sb.WriteString("[DRY RUN] ")
		}
		sb.WriteString(fmt.Sprintf("Proposed %d fact candidate(s): %d new, %d superseded, %d identical, %d malformed.\n",
			len(result.Candidates), result.FactsWritten, result.FactsSuperseded, result.FactsIdentical, result.FactsMalformed))

		if result.Summary != "" {
			sb.WriteString(fmt.Sprintf("Session summary: %s\n", result.Summary))
		}

		for _, c := range result.Candidates {
			icon := ""
			switch c.Kind {
			case "gotcha", "deadend":
				icon = "⚠ "
			case "decision":
				icon = "★ "
			}
			sb.WriteString(fmt.Sprintf("\n  %s[%s] %s", icon, c.Kind, c.Text))
		}

		return textResult(sb.String()), nil, nil
	}
}

func getDiffHandler(engine *dualmem.Engine, ns, rootDir string) func(context.Context, *mcp.CallToolRequest, getDiffInput) (*mcp.CallToolResult, any, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, input getDiffInput) (*mcp.CallToolResult, any, error) {
		marker, err := engine.GetLatestSessionMarker(ns)
		if err != nil {
			return nil, nil, fmt.Errorf("get session marker: %w", err)
		}

		diff, err := dualmem.ComputeStructuralDiff(rootDir, marker)
		if err != nil {
			return nil, nil, fmt.Errorf("compute diff: %w", err)
		}
		if diff == nil {
			return textResult("No previous session marker found — this appears to be the first session."), nil, nil
		}

		if diff.TotalChanges() == 0 {
			return textResult(diff.Summary), nil, nil
		}

		var sb strings.Builder
		sb.WriteString(diff.Summary + "\n")
		if len(diff.FilesAdded) > 0 {
			sb.WriteString(fmt.Sprintf("\nAdded (%d):\n  %s", len(diff.FilesAdded), strings.Join(diff.FilesAdded, "\n  ")))
		}
		if len(diff.FilesModified) > 0 {
			sb.WriteString(fmt.Sprintf("\nModified (%d):\n  %s", len(diff.FilesModified), strings.Join(diff.FilesModified, "\n  ")))
		}
		if len(diff.FilesDeleted) > 0 {
			sb.WriteString(fmt.Sprintf("\nDeleted (%d):\n  %s", len(diff.FilesDeleted), strings.Join(diff.FilesDeleted, "\n  ")))
		}
		return textResult(sb.String()), nil, nil
	}
}

func getStatusHandler(engine *dualmem.Engine, ns string) func(context.Context, *mcp.CallToolRequest, getStatusInput) (*mcp.CallToolResult, any, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, input getStatusInput) (*mcp.CallToolResult, any, error) {
		store := engine.GetStore()

		detailCount, _ := store.GetDetailCount(ns)
		seedCount, _ := store.CountSeedMemories(ns)
		docs, _ := engine.ListKnowledgeDocs(ctx, ns)
		cps, _ := engine.GetCheckpoints(ctx, ns)
		files, _ := engine.FileIndex(ctx, ns)

		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("DualMem Status for %s\n", ns))
		sb.WriteString(fmt.Sprintf("  Detail memories: %d (including %d seeds)\n", detailCount, seedCount))
		sb.WriteString(fmt.Sprintf("  Knowledge docs:  %d\n", len(docs)))
		sb.WriteString(fmt.Sprintf("  Checkpoints:     %d\n", len(cps)))
		sb.WriteString(fmt.Sprintf("  Files tracked:   %d\n", len(files)))
		sb.WriteString(fmt.Sprintf("  Session:         %s\n", time.Now().Format("2006-01-02 15:04")))

		return textResult(sb.String()), nil, nil
	}
}

func listKnowledgeDocsHandler(engine *dualmem.Engine, ns string) func(context.Context, *mcp.CallToolRequest, listKnowledgeDocsInput) (*mcp.CallToolResult, any, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, input listKnowledgeDocsInput) (*mcp.CallToolResult, any, error) {
		limit := input.Limit
		if limit <= 0 {
			limit = 10
		}

		if input.Query != "" {
			// Embed query for ranked search
			emb, err := engine.Embedder().Embed(ctx, input.Query, "RETRIEVAL_QUERY")
			if err != nil {
				return nil, nil, fmt.Errorf("embed query: %w", err)
			}
			docs, err := engine.GetKnowledgeDocs(ctx, ns, input.Query, emb, limit)
			if err != nil {
				return nil, nil, fmt.Errorf("search knowledge docs: %w", err)
			}
			return formatKnowledgeDocs(docs), nil, nil
		}

		docs, err := engine.ListKnowledgeDocs(ctx, ns)
		if err != nil {
			return nil, nil, fmt.Errorf("list knowledge docs: %w", err)
		}
		if limit < len(docs) {
			docs = docs[:limit]
		}
		return formatKnowledgeDocs(docs), nil, nil
	}
}

// =====================================================================
// Helpers
// =====================================================================

func textResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: text},
		},
	}
}

func formatKnowledgeDocs(docs []dualmem.KnowledgeDoc) *mcp.CallToolResult {
	if len(docs) == 0 {
		return textResult("No knowledge documents found.")
	}

	var lines []string
	for _, d := range docs {
		line := fmt.Sprintf("[%s] %s (~%d tokens)", d.Topic, truncate(d.Content, 120), d.TokenCount)
		if len(d.Files) > 0 {
			line += fmt.Sprintf("\n  Files: %s", strings.Join(d.Files, ", "))
		}
		lines = append(lines, line)
	}
	return textResult(strings.Join(lines, "\n\n"))
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// formatFacts renders durable facts for an MCP tool result. Each fact shows its
// kind, text, provenance (source, short commit SHA, date), and a truncated file
// list. Long file lists are capped ("+N more") to match the existing handler
// truncation convention.
func formatFacts(facts []*dualmem.Fact) string {
	var lines []string
	for _, f := range facts {
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("[%s] %s", f.Kind, f.Text))

		// Provenance line: source, short sha, date.
		dateStr := ""
		if !f.CreatedAt.IsZero() {
			dateStr = f.CreatedAt.Format("2006-01-02")
		}
		short := f.GitCommit
		if len(short) > 7 {
			short = short[:7]
		}
		prov := fmt.Sprintf("source=%s", f.Source)
		if short != "" {
			prov += " · commit=" + short
		}
		if dateStr != "" {
			prov += " · " + dateStr
		}
		sb.WriteString("\n  " + prov)

		if len(f.Files) > 0 {
			sb.WriteString("\n  Files: " + formatFileList(f.Files, 5))
		}
		lines = append(lines, sb.String())
	}
	return strings.Join(lines, "\n\n")
}

// formatFileList joins files with commas, capping the count and appending a
// "+N more" suffix when truncated. Mirrors the CLI/MCP file-display cap.
func formatFileList(files []string, cap int) string {
	if len(files) <= cap {
		return strings.Join(files, ", ")
	}
	return strings.Join(files[:cap], ", ") + fmt.Sprintf(" (+%d more)", len(files)-cap)
}

// servedFactIDs collects the IDs of a fact slice for RecordServed.
func servedFactIDs(facts []*dualmem.Fact) []string {
	ids := make([]string, 0, len(facts))
	for _, f := range facts {
		if f != nil && f.ID != "" {
			ids = append(ids, f.ID)
		}
	}
	return ids
}

// annotationIcon maps an annotation type to its display icon.
func annotationIcon(t string) string {
	switch t {
	case "warning":
		return "⚠ "
	case "decision":
		return "★ "
	case "continuity", "checkpoint":
		return "↻ "
	case "knowledge":
		return "📖 "
	case "workflow":
		return "📎 "
	}
	return ""
}

// gitCoChangeNeighbors returns the top-N files that git-history-co-change with
// the given path, derived purely from git_cochange.go (never the memory-based
// cochange layer). Best-effort: any error or empty graph returns nil.
func gitCoChangeNeighbors(rootDir, path string, topN int) map[string]float64 {
	if rootDir == "" || path == "" {
		return nil
	}
	edges, err := dualmem.BuildGitCoChangeGraph(rootDir, 3, 50)
	if err != nil || len(edges) == 0 {
		return nil
	}
	return dualmem.CoChangeNeighbors(edges, []string{path}, topN)
}
