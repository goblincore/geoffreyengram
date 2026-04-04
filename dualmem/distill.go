package dualmem

import (
	"context"
	"crypto/md5"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// DistillOpts configures the distill operation.
type DistillOpts struct {
	File      string // Explicit transcript file path
	Stdin     bool   // Read from stdin
	Text      string // Raw transcript text (e.g. from MCP tool call)
	Auto      bool   // Auto-detect CC session, skip if already distilled
	DryRun    bool   // Preview without writing
	Namespace string // Override namespace
	MaxFacts  int    // Max facts to extract (default 20)
}

// DistillResult describes what distillation produced.
type DistillResult struct {
	SessionID string          `json:"session_id"`
	Facts     []DistilledFact `json:"facts"`
	Triples   []EntityTriple  `json:"triples,omitempty"`
	Summary   string          `json:"summary"`
	Skipped   int             `json:"skipped"` // near-duplicate facts skipped
	Written   int             `json:"written"`  // facts actually written
	DryRun    bool            `json:"dry_run"`
}

// DistilledFact is an extracted memory from a session transcript.
type DistilledFact struct {
	Text     string   `json:"text"`
	Type     string   `json:"type"`     // "decision", "warning", "continuity", "map", "general"
	Salience float64  `json:"salience"`
	Files    []string `json:"files,omitempty"`
	Entities []Entity `json:"entities,omitempty"`
}

// distillExtractionResponse is the expected JSON response from the LLM.
type distillExtractionResponse struct {
	Facts          []DistilledFact `json:"facts"`
	EntityTriples  []EntityTriple  `json:"entity_triples"`
	SessionSummary string          `json:"session_summary"`
}

// CCMessage represents a message in a Claude Code session transcript.
type CCMessage struct {
	Role    string `json:"role"`              // "user", "assistant", "tool_use", "tool_result"
	Content string `json:"content"`           // text content
	Type    string `json:"type,omitempty"`
}

// Distill extracts memories from a session transcript.
func (e *Engine) Distill(ctx context.Context, opts DistillOpts, userID string) (*DistillResult, error) {
	if opts.MaxFacts <= 0 {
		opts.MaxFacts = 20
	}

	// Step 1: Get transcript
	transcript, sessionID, err := e.loadTranscript(opts)
	if err != nil {
		return nil, fmt.Errorf("load transcript: %w", err)
	}

	if transcript == "" {
		return &DistillResult{SessionID: sessionID, Summary: "empty session"}, nil
	}

	// Check if already distilled (--auto mode)
	if opts.Auto {
		lastDistilled, _ := e.store.GetConfigValue("last_distill_session_id")
		if lastDistilled == sessionID && sessionID != "" {
			return &DistillResult{SessionID: sessionID, Summary: "already distilled"}, nil
		}
	}

	// Step 1b: Enrich transcript with auto-captured file interactions (if available)
	transcript = enrichWithSessionLog(transcript, opts.Namespace, e.cfg.RootDir)

	// Step 2: Extract facts via LLM
	extraction, err := e.extractFromTranscript(ctx, transcript, opts.MaxFacts)
	if err != nil {
		return nil, fmt.Errorf("extract: %w", err)
	}

	result := &DistillResult{
		SessionID: sessionID,
		Facts:     extraction.Facts,
		Triples:   extraction.EntityTriples,
		Summary:   extraction.SessionSummary,
		DryRun:    opts.DryRun,
	}

	if opts.DryRun {
		result.Written = len(extraction.Facts)
		return result, nil
	}

	// Step 3: Dedup and write facts
	namespace := opts.Namespace
	if namespace == "" {
		namespace = e.namespace()
	}

	written := 0
	skipped := 0
	for _, fact := range extraction.Facts {
		// Floor salience at 0.6 for distilled facts
		salience := fact.Salience
		if salience < 0.6 {
			salience = 0.6
		}

		// Check for near-duplicates
		if e.isNearDuplicate(ctx, fact.Text, userID, 0.90) {
			skipped++
			continue
		}

		// Write via AddWithOptions (takes its own lock — do NOT hold mu here)
		err := e.AddWithOptions(ctx, MemoryInput{
			UserMessage: fact.Text,
			SectorHint:  fact.Type,
			Salience:    salience,
			Entities:    fact.Entities,
			Type:        fact.Type,
			Files:       fact.Files,
			SessionID:   sessionID,
		}, userID)
		if err != nil {
			continue // best-effort
		}
		written++
	}

	// Step 4: Write entity triples to graph
	if namespace != "" {
		for _, triple := range extraction.EntityTriples {
			sourceID, err := e.store.UpsertEntity(&EntityNode{
				Name:      triple.Source.Text,
				Type:      triple.Source.Type,
				Namespace: namespace,
			})
			if err != nil {
				continue
			}
			targetID, err := e.store.UpsertEntity(&EntityNode{
				Name:      triple.Target.Text,
				Type:      triple.Target.Type,
				Namespace: namespace,
			})
			if err != nil {
				continue
			}
			_ = e.store.UpsertEdge(&EntityEdge{
				SourceID:  sourceID,
				TargetID:  targetID,
				Relation:  triple.Relation,
				Namespace: namespace,
			})
		}
	}

	result.Skipped = skipped

	// Step 4b: Persist session summary as a continuity memory
	if extraction.SessionSummary != "" && !opts.DryRun {
		summaryFiles := collectAllFiles(extraction.Facts)
		summaryErr := e.AddWithOptions(ctx, MemoryInput{
			UserMessage: extraction.SessionSummary,
			Type:        "continuity",
			Salience:    0.75,
			Files:       summaryFiles,
			SessionID:   sessionID,
		}, userID)
		if summaryErr == nil {
			written++
		}
	}

	result.Written = written

	// Step 5: Mark as distilled
	if sessionID != "" {
		_ = e.store.SetConfigValue("last_distill_session_id", sessionID)
	}

	// Step 6: Auto-synthesize if enough new memories
	if written >= 3 {
		// Best-effort synthesis — don't block on failure
		_, _ = e.Synthesize(ctx, namespace, &SynthesizeOpts{})
	}

	return result, nil
}

// loadTranscript reads a session transcript from the configured source.
func (e *Engine) loadTranscript(opts DistillOpts) (string, string, error) {
	// Priority 1: Explicit file
	if opts.File != "" {
		data, err := os.ReadFile(opts.File)
		if err != nil {
			return "", "", fmt.Errorf("read file %s: %w", opts.File, err)
		}
		sessionID := filepath.Base(opts.File)
		return parseTranscript(data), sessionID, nil
	}

	// Priority 2: Inline text (from MCP or programmatic callers)
	if opts.Text != "" {
		return parseTranscript([]byte(opts.Text)), "inline-" + time.Now().Format("20060102-150405"), nil
	}

	// Priority 3: Stdin
	if opts.Stdin {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", "", fmt.Errorf("read stdin: %w", err)
		}
		return parseTranscript(data), "stdin-" + time.Now().Format("20060102-150405"), nil
	}

	// Priority 3: Auto-detect Claude Code session
	transcript, sessionID, err := findLatestCCSession(e.cfg.RootDir)
	if err != nil {
		return "", "", fmt.Errorf("auto-detect CC session: %w", err)
	}
	return transcript, sessionID, nil
}

// projectSlug converts an absolute path to the Claude Code project directory
// slug convention: /Users/donny/foo → -Users-donny-foo
func projectSlug(rootDir string) string {
	return strings.ReplaceAll(rootDir, string(filepath.Separator), "-")
}

// findLatestCCSession finds the most recent Claude Code session file.
// It prefers sessions from the current project (derived from rootDir),
// falling back to all projects if none are found.
func findLatestCCSession(rootDir string) (string, string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", "", fmt.Errorf("get home dir: %w", err)
	}

	// Claude Code stores sessions as .jsonl files directly in
	// ~/.claude/projects/<project-slug>/<session-uuid>.jsonl
	claudeDir := filepath.Join(homeDir, ".claude", "projects")

	type sessionFile struct {
		path    string
		modTime time.Time
	}

	// collectSessions scans a directory for .jsonl session files.
	collectSessions := func(dir string) []sessionFile {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil
		}
		var out []sessionFile
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			out = append(out, sessionFile{
				path:    filepath.Join(dir, e.Name()),
				modTime: info.ModTime(),
			})
		}
		return out
	}

	// Try current project first
	var sessions []sessionFile
	if rootDir != "" {
		slug := projectSlug(rootDir)
		sessions = collectSessions(filepath.Join(claudeDir, slug))
	}

	// Fall back to scanning all project directories
	if len(sessions) == 0 {
		entries, err := os.ReadDir(claudeDir)
		if err != nil {
			return "", "", fmt.Errorf("read claude projects dir: %w", err)
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			sessions = append(sessions, collectSessions(filepath.Join(claudeDir, entry.Name()))...)
		}
	}

	if len(sessions) == 0 {
		return "", "", fmt.Errorf("no Claude Code sessions found in %s", claudeDir)
	}

	// Sort by modification time, most recent first
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].modTime.After(sessions[j].modTime)
	})

	latest := sessions[0]
	data, err := os.ReadFile(latest.path)
	if err != nil {
		return "", "", fmt.Errorf("read session %s: %w", latest.path, err)
	}

	sessionID := strings.TrimSuffix(filepath.Base(latest.path), ".jsonl")
	return parseTranscript(data), sessionID, nil
}

// parseTranscript converts raw transcript data into a formatted string.
// Handles both JSONL (Claude Code) and plain text formats.
func parseTranscript(data []byte) string {
	text := string(data)

	// Try JSONL format (Claude Code sessions)
	lines := strings.Split(text, "\n")
	var messages []string
	isJSONL := false

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if line[0] != '{' {
			continue
		}

		var msg map[string]interface{}
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue
		}
		isJSONL = true

		role, _ := msg["role"].(string)
		msgType, _ := msg["type"].(string)

		// Skip tool use/result messages — focus on human-assistant dialogue
		if msgType == "tool_use" || msgType == "tool_result" || role == "tool" {
			continue
		}

		// Extract text content
		content := ""
		if c, ok := msg["content"].(string); ok {
			content = c
		} else if arr, ok := msg["content"].([]interface{}); ok {
			// Content might be an array of content blocks
			for _, item := range arr {
				if block, ok := item.(map[string]interface{}); ok {
					if t, ok := block["text"].(string); ok {
						content += t + "\n"
					}
				}
			}
		}

		content = strings.TrimSpace(content)
		if content == "" || len(content) < 10 {
			continue // skip trivial messages
		}

		prefix := "User"
		if role == "assistant" {
			prefix = "Assistant"
		}
		messages = append(messages, fmt.Sprintf("%s: %s", prefix, content))
	}

	if isJSONL && len(messages) > 0 {
		result := strings.Join(messages, "\n\n")
		// Truncate to last ~12000 chars
		if len(result) > 12000 {
			result = result[len(result)-12000:]
		}
		return result
	}

	// Plain text fallback
	if len(text) > 12000 {
		text = text[len(text)-12000:]
	}
	return text
}

// extractFromTranscript uses the LLM to extract structured facts from a transcript.
func (e *Engine) extractFromTranscript(ctx context.Context, transcript string, maxFacts int) (*distillExtractionResponse, error) {
	// Need a TextGenerator (GeminiSummarizer implements this)
	gen, ok := e.cfg.Summarizer.(TextGenerator)
	if !ok || gen == nil {
		return nil, fmt.Errorf("summarizer does not implement TextGenerator (needed for distillation)")
	}

	prompt := formatDistillPrompt(transcript, maxFacts)

	response, err := gen.GenerateText(ctx, prompt, 2000)
	if err != nil {
		return nil, fmt.Errorf("LLM extraction: %w", err)
	}

	return parseDistillResponse(response)
}

// formatDistillPrompt builds the extraction prompt.
func formatDistillPrompt(transcript string, maxFacts int) string {
	return fmt.Sprintf(`You are a memory extraction system. Analyze this coding session transcript and extract the most important facts, decisions, and context that should be remembered for future sessions.

For each fact, classify it:
- "decision": A choice made between alternatives, an approach selected or rejected
- "warning": Something that must not be changed, is fragile, or requires caution
- "continuity": Work in progress — what was done and what remains
- "map": Where things are in the codebase, file relationships
- "trace": A code flow discovery — sequences where the assistant traced how code connects across files (e.g., function A calls B which triggers C). Include structured trace (file:line → file:line) followed by prose explanation. Salience: 0.8
- "general": Other useful context

Extract entity relationships as triples (source → relation → target).
Entity types: "person", "file", "function", "concept", "tool", "project"

Respond with ONLY valid JSON (no markdown, no explanation):
{
  "facts": [
    {
      "text": "concise description of the fact",
      "type": "decision|warning|continuity|map|general",
      "salience": 0.6-1.0,
      "files": ["relevant/file/paths"],
      "entities": [{"text": "name", "type": "concept|file|tool|..."}]
    }
  ],
  "entity_triples": [
    {
      "source": {"text": "name", "type": "type"},
      "relation": "implements|uses|modifies|depends_on|discussed_with",
      "target": {"text": "name", "type": "type"}
    }
  ],
  "session_summary": "One-line summary of what was accomplished"
}

Rules:
- Maximum %d facts (prioritize decisions, warnings, and continuity over general facts)
- Be concise — each fact should be 1-2 sentences
- Include file paths when relevant
- Salience: 0.9 for warnings/critical decisions, 0.7-0.8 for important context, 0.6 for general
- Skip trivial exchanges, greetings, and tool-only interactions
- Focus on what a future AI agent needs to know to continue this work
- Look for exploration patterns: sequences of grep/read across multiple files that trace code flows. Extract these as "trace" type with structured paths and what was discovered

TRANSCRIPT:
%s`, maxFacts, transcript)
}

// parseDistillResponse parses the LLM's JSON response.
func parseDistillResponse(response string) (*distillExtractionResponse, error) {
	// Strip markdown code fences if present
	response = strings.TrimSpace(response)
	if strings.HasPrefix(response, "```json") {
		response = strings.TrimPrefix(response, "```json")
		response = strings.TrimSuffix(response, "```")
		response = strings.TrimSpace(response)
	} else if strings.HasPrefix(response, "```") {
		response = strings.TrimPrefix(response, "```")
		response = strings.TrimSuffix(response, "```")
		response = strings.TrimSpace(response)
	}

	var result distillExtractionResponse
	if err := json.Unmarshal([]byte(response), &result); err != nil {
		return nil, fmt.Errorf("parse extraction response: %w (response: %.200s)", err, response)
	}

	// Validate and clamp salience values
	for i := range result.Facts {
		if result.Facts[i].Salience < 0.1 {
			result.Facts[i].Salience = 0.6
		}
		if result.Facts[i].Salience > 1.0 {
			result.Facts[i].Salience = 1.0
		}
		// Validate type
		switch result.Facts[i].Type {
		case "decision", "warning", "continuity", "map", "trace", "general":
			// valid
		default:
			result.Facts[i].Type = "general"
		}
	}

	return &result, nil
}

// collectAllFiles returns the union of all file paths from distilled facts.
func collectAllFiles(facts []DistilledFact) []string {
	seen := make(map[string]bool)
	var result []string
	for _, f := range facts {
		for _, path := range f.Files {
			if !seen[path] {
				seen[path] = true
				result = append(result, path)
			}
		}
	}
	return result
}

// enrichWithSessionLog appends a file-touch summary from the auto-capture hook
// to the transcript. This gives the LLM a complete picture of which files were
// read/written during the session, even if the transcript was truncated.
func enrichWithSessionLog(transcript, namespace, rootDir string) string {
	// Resolve namespace hash the same way the hook does
	nsBase := ""
	if rootDir != "" {
		nsBase = filepath.Base(rootDir)
	} else if namespace != "" {
		// Strip "claude:" prefix if present
		nsBase = strings.TrimPrefix(namespace, "claude:")
	}
	if nsBase == "" {
		return transcript
	}

	// Try to find session logs matching the namespace
	tmpDir := os.TempDir()
	pattern := filepath.Join(tmpDir, fmt.Sprintf("dualmem-session-%s-*.jsonl", md5Short(nsBase)))
	matches, err := filepath.Glob(pattern)
	if err != nil || len(matches) == 0 {
		return transcript
	}

	// Use the most recently modified log
	sort.Slice(matches, func(i, j int) bool {
		fi, _ := os.Stat(matches[i])
		fj, _ := os.Stat(matches[j])
		if fi == nil || fj == nil {
			return false
		}
		return fi.ModTime().After(fj.ModTime())
	})

	data, err := os.ReadFile(matches[0])
	if err != nil {
		return transcript
	}

	// Parse JSONL entries and deduplicate file paths
	type logEntry struct {
		Ts    string `json:"ts"`
		Tool  string `json:"tool"`
		Files string `json:"files"`
	}

	fileSet := make(map[string]int) // file → count of interactions
	toolCounts := make(map[string]int)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var entry logEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		if entry.Files != "" {
			fileSet[entry.Files]++
		}
		toolCounts[entry.Tool]++
	}

	if len(fileSet) == 0 {
		return transcript
	}

	// Build compact summary
	var sb strings.Builder
	sb.WriteString("\n\n--- FILE INTERACTIONS (auto-captured) ---\n")

	// Tool usage summary
	var tools []string
	for tool, count := range toolCounts {
		tools = append(tools, fmt.Sprintf("%s(%d)", tool, count))
	}
	sort.Strings(tools)
	sb.WriteString("Tools: " + strings.Join(tools, ", ") + "\n")

	// Files touched (sorted by frequency)
	type fileCount struct {
		path  string
		count int
	}
	var files []fileCount
	for f, c := range fileSet {
		files = append(files, fileCount{f, c})
	}
	sort.Slice(files, func(i, j int) bool {
		return files[i].count > files[j].count
	})

	sb.WriteString("Files touched:\n")
	for i, f := range files {
		if i >= 30 { // cap at 30 files
			sb.WriteString(fmt.Sprintf("  ... and %d more\n", len(files)-30))
			break
		}
		sb.WriteString(fmt.Sprintf("  %s (%dx)\n", f.path, f.count))
	}

	return transcript + sb.String()
}

// md5Short returns the first 8 chars of the md5 hex hash of "claude:<s>".
// Matches the namespace hashing in dualmem-autocapture.sh.
func md5Short(s string) string {
	input := fmt.Sprintf("claude:%s", s)
	sum := md5.Sum([]byte(input))
	h := fmt.Sprintf("%x", sum)
	if len(h) > 8 {
		return h[:8]
	}
	return h
}

// isNearDuplicate checks if content is too similar to existing memories.
func (e *Engine) isNearDuplicate(ctx context.Context, content, userID string, threshold float64) bool {
	embedding, err := e.embedder.Embed(ctx, content, "RETRIEVAL_DOCUMENT")
	if err != nil {
		return false // can't check, assume not duplicate
	}

	details, err := e.store.GetDetailMemories(userID)
	if err != nil {
		return false
	}

	for _, d := range details {
		if CosineSimilarity(embedding, d.Vector) > threshold {
			return true
		}
	}
	return false
}
