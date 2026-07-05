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

// legacyDistillEnabled reports whether the v1 detail-memory/entity-graph
// output of Distill should also run. Default is true (preserve existing
// behavior); task 9 of the v2 plan flips this to false in one line, and later
// the field plus all legacy-guarded code are deleted.
func (c *Config) legacyDistillEnabled() bool {
	if c.LegacyDistill == nil {
		return true
	}
	return *c.LegacyDistill
}

// DistillResult describes what distillation produced.
//
// In v2 the primary output is fact candidates (Candidates / FactsWritten /
// FactsSuperseded / FactsIdentical / FactsMalformed). The legacy fields (Facts,
// Triples, Written, Skipped) describe the v1 detail-memory path, which only
// runs while cfg.LegacyDistill is enabled (default on; task 9 flips it off).
type DistillResult struct {
	SessionID string          `json:"session_id"`
	Facts     []DistilledFact `json:"facts"`             // legacy v1 detail-memory candidates
	Triples   []EntityTriple  `json:"triples,omitempty"` // legacy v1 entity graph
	Summary   string          `json:"summary"`
	Skipped   int             `json:"skipped"` // legacy: near-duplicate detail memories skipped
	Written   int             `json:"written"` // legacy: detail memories actually written
	DryRun    bool            `json:"dry_run"`

	// v2 fact candidates (the primary output).
	Candidates      []FactCandidate `json:"candidates,omitempty"`
	FactsWritten    int             `json:"facts_written"`    // candidates inserted as new facts
	FactsSuperseded int             `json:"facts_superseded"` // candidates that superseded a near-dup
	FactsIdentical  int             `json:"facts_identical"`  // candidates that were exact-text no-ops
	FactsMalformed  int             `json:"facts_malformed"`  // candidates skipped (bad kind / empty / unparseable)
}

// DistilledFact is an extracted memory from a session transcript.
type DistilledFact struct {
	Text     string   `json:"text"`
	Type     string   `json:"type"` // "decision", "warning", "continuity", "map", "general"
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

// FactCandidate is a kind-classified durable fact proposed by the v2 distiller
// from a session transcript. It is the primary output of Distill (see
// docs/superpowers/plans/2026-07-04-dualmem-v2.md, Phase 4). Each candidate is
// deduped against existing live facts before insert: identical text is a no-op,
// an embedding near-duplicate supersedes the old fact.
type FactCandidate struct {
	Kind  string   `json:"kind"` // decision | deadend | gotcha | preference | reference
	Text  string   `json:"text"` // 1-3 self-contained sentences
	Files []string `json:"files,omitempty"`
}

// factCandidateResponse is the expected JSON response from the v2 extraction
// prompt. The model is explicitly told that an empty list is a valid answer.
type factCandidateResponse struct {
	Candidates []FactCandidate `json:"candidates"`
}

// CCMessage represents a message in a Claude Code session transcript.
type CCMessage struct {
	Role    string `json:"role"`    // "user", "assistant", "tool_use", "tool_result"
	Content string `json:"content"` // text content
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

	// Step 1c: Collect files the session touched (reads + edits), from both the
	// auto-capture hook log and the transcript itself, so the file-touch hit
	// signal can credit served facts. Best-effort: an empty set just means no
	// facts get credited this pass.
	touched := collectTouchedFiles(opts.Namespace, e.cfg.RootDir, transcript)

	namespace := opts.Namespace
	if namespace == "" {
		namespace = e.namespace()
	}

	result := &DistillResult{
		SessionID: sessionID,
		DryRun:    opts.DryRun,
	}

	// --- v2 primary path: propose durable fact candidates ---
	//
	// The distiller's job in v2 is no longer "write detail memories + update
	// layers"; it is "propose durable facts from the transcript". Candidates are
	// kind-classified, deduped against live facts before insert (identical text
	// is a no-op; an embedding near-duplicate supersedes the old fact), and
	// stamped source=inferred with the current git commit + session id. This
	// path always runs (even after task 9 flips the legacy path off).
	// See docs/superpowers/plans/2026-07-04-dualmem-v2.md (Phase 4).
	candidates, candErr := e.extractFactCandidates(ctx, transcript, opts.MaxFacts)
	if candErr != nil {
		// Resilience: a failed/empty extraction never fails the whole distill;
		// we just record zero candidates and fall through to the legacy path.
		result.Summary = fmt.Sprintf("v2 extraction failed: %v", candErr)
	} else {
		result.Candidates = candidates
		if !opts.DryRun {
			for _, c := range candidates {
				status, _ := e.distillInsertFactCandidate(ctx, c, namespace, sessionID)
				switch status {
				case "inserted":
					result.FactsWritten++
				case "superseded":
					result.FactsSuperseded++
				case "identical":
					result.FactsIdentical++
				default: // "malformed" or "error"
					result.FactsMalformed++
				}
			}
		} else {
			result.FactsWritten = len(candidates) // dry-run preview count
		}
	}

	// --- legacy v1 path: detail memories + entity graph + synthesis ---
	//
	// Behind a single switch so task 9 can cut it in one line (flip the default
	// in Config.legacyDistillEnabled, then delete this block + the legacy
	// extraction functions). When enabled (default), this runs in ADDITION to
	// the v2 path so existing behavior is preserved during the transition.
	if e.cfg.legacyDistillEnabled() {
		extraction, err := e.extractFromTranscript(ctx, transcript, opts.MaxFacts)
		if err != nil {
			// Don't fail the whole distill on a legacy-path error; surface it.
			if result.Summary == "" {
				result.Summary = fmt.Sprintf("legacy extract failed: %v", err)
			}
		} else {
			result.Facts = extraction.Facts
			result.Triples = extraction.EntityTriples
			if extraction.SessionSummary != "" {
				result.Summary = extraction.SessionSummary
			}

			if opts.DryRun {
				result.Written = len(extraction.Facts)
			} else {
				result.Written, result.Skipped = e.distillWriteLegacy(ctx, extraction, namespace, sessionID, userID)
			}
		}
	}

	// Step 5: Mark as distilled
	if sessionID != "" && !opts.DryRun {
		_ = e.store.SetConfigValue("last_distill_session_id", sessionID)
	}

	// Step 5b: Credit file-touch hits for any served facts whose cited files
	// this session actually touched. The passive usefulness signal that the
	// deleted rate_context loop was meant to provide. Best-effort: errors are
	// swallowed so a stats failure never blocks distillation.
	if sessionID != "" && !opts.DryRun && len(touched) > 0 {
		_, _ = e.RecordFileTouches(sessionID, touched)
	}

	if result.Summary == "" {
		result.Summary = fmt.Sprintf("%d fact candidate(s): %d new, %d superseded, %d identical",
			len(result.Candidates), result.FactsWritten, result.FactsSuperseded, result.FactsIdentical)
	}

	return result, nil
}

// distillWriteLegacy runs the v1 detail-memory + entity-graph + session-summary
// + synthesis writes from a legacy extraction. It is the entirety of the legacy
// output path, isolated here so task 9 can delete it in one cut. Best-effort:
// individual write errors are skipped, never returned. Returns (written, skipped).
func (e *Engine) distillWriteLegacy(ctx context.Context, extraction *distillExtractionResponse, namespace, sessionID, userID string) (written, skipped int) {
	for _, fact := range extraction.Facts {
		salience := fact.Salience
		if salience < 0.6 {
			salience = 0.6
		}
		if e.isNearDuplicate(ctx, fact.Text, userID, 0.90) {
			skipped++
			continue
		}
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
			continue
		}
		written++
	}

	// Entity triples to graph
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

	// Persist session summary as a continuity memory
	if extraction.SessionSummary != "" {
		summaryFiles := collectAllFiles(extraction.Facts)
		if err := e.AddWithOptions(ctx, MemoryInput{
			UserMessage: extraction.SessionSummary,
			Type:        "continuity",
			Salience:    0.75,
			Files:       summaryFiles,
			SessionID:   sessionID,
		}, userID); err == nil {
			written++
		}
	}

	// Auto-synthesize if enough new memories
	if written >= 3 {
		_, _ = e.Synthesize(ctx, namespace, &SynthesizeOpts{})
	}
	return written, skipped
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

// distillSupersedeThreshold is the cosine similarity at or above which a
// candidate fact supersedes an existing live fact instead of being inserted as
// a duplicate. Matches the migrate-v2 dedupe threshold.
const distillSupersedeThreshold = 0.90

// getDistillGenerator returns the best available TextGenerator for v2 fact
// extraction. Prefers a dedicated SynthesisGenerator, then falls back to the
// Summarizer if it implements TextGenerator. Returns an error if none is set
// (v2 extraction needs an LLM).
func (e *Engine) getDistillGenerator() (TextGenerator, error) {
	if e.cfg.SynthesisGenerator != nil {
		return e.cfg.SynthesisGenerator, nil
	}
	gen, ok := e.cfg.Summarizer.(TextGenerator)
	if !ok || gen == nil {
		return nil, fmt.Errorf("dualmem: no TextGenerator configured (need SynthesisGenerator or a TextGenerator-capable Summarizer for v2 distill)")
	}
	return gen, nil
}

// extractFactCandidates runs the v2 extraction prompt against the transcript
// and returns kind-classified fact candidates. It is resilient: a malformed
// response yields a parse error (which the caller treats as "zero candidates,
// keep going"), and individual candidates with invalid kind/empty text are
// dropped by parseFactCandidateResponse rather than failing the whole batch.
func (e *Engine) extractFactCandidates(ctx context.Context, transcript string, maxFacts int) ([]FactCandidate, error) {
	gen, err := e.getDistillGenerator()
	if err != nil {
		return nil, err
	}
	if maxFacts <= 0 {
		maxFacts = 20
	}
	prompt := formatFactCandidatePrompt(transcript, maxFacts)
	response, err := gen.GenerateText(ctx, prompt, 2000)
	if err != nil {
		return nil, fmt.Errorf("v2 LLM extraction: %w", err)
	}
	return parseFactCandidateResponse(response)
}

// formatFactCandidatePrompt builds the v2 fact-candidate extraction prompt.
//
// The prompt is deliberately biased toward FEW, high-value, durable facts:
// narrative/episodic material is skipped, and an empty candidate list is an
// explicitly valid answer. Each candidate must be self-contained (1-3
// sentences) and tagged with one of the v2 kinds plus any associated file paths.
func formatFactCandidatePrompt(transcript string, maxFacts int) string {
	return fmt.Sprintf(`You are a durable-memory curator for a coding agent. Read this session transcript and propose the FEW facts worth remembering permanently for future sessions on this codebase.

Only extract facts that are:
- Durable: still true after the session ends (not "what I'm about to do").
- Non-obvious: not re-derivable from reading the code or git log.
- Self-contained: a future reader understands them WITHOUT this transcript.

For each fact, classify into exactly ONE kind:
- "decision": a choice made between alternatives, or an approach selected/rejected. Architectural decisions, library/framework choices, trade-offs.
- "deadend": an approach that was tried and failed or was abandoned, with WHY. Highest value — record these aggressively.
- "gotcha": a fragile invariant, a non-obvious constraint, something that must not be changed, a pitfall that cost time.
- "preference": a stable user/team preference about HOW to work (style, tooling, conventions) — these are user-global, not repo-specific.
- "reference": where something lives or how a subsystem connects (file relationships, control flow), only when non-obvious from the code.

Respond with ONLY valid JSON, no markdown fences, no prose:
{
  "candidates": [
    {
      "kind": "decision|deadend|gotcha|preference|reference",
      "text": "1-3 self-contained sentences stating the fact plainly.",
      "files": ["repo/relative/path.go"]
    }
  ]
}

Rules:
- Prefer FEW high-value facts. An empty "candidates": [] is a GOOD answer for an unremarkable session — do not pad.
- SKIP narrative/episodic material: what the assistant did step-by-step, tool output, greetings, status updates, "we discussed X". Those are NOT facts.
- Each text must read as a standalone statement a future agent can act on, without seeing this session.
- Include files ONLY when the fact is genuinely tied to those paths.
- Maximum %d candidates. If more come to mind, keep only the most durable and non-obvious.

TRANSCRIPT:
%s`, maxFacts, transcript)
}

// parseFactCandidateResponse parses the v2 LLM JSON response into candidates.
// It strips markdown fences, drops candidates with an invalid kind or empty text
// (counting them as malformed), and tolerates partially-broken JSON by returning
// whatever valid candidates it could extract. A completely unparseable body
// returns an error so the caller can record it without failing the distill.
func parseFactCandidateResponse(response string) ([]FactCandidate, error) {
	response = strings.TrimSpace(response)
	// Strip ```json ... ``` or ``` ... ``` fences.
	if strings.HasPrefix(response, "```json") {
		response = strings.TrimPrefix(response, "```json")
		response = strings.TrimSuffix(response, "```")
		response = strings.TrimSpace(response)
	} else if strings.HasPrefix(response, "```") {
		response = strings.TrimPrefix(response, "```")
		response = strings.TrimSuffix(response, "```")
		response = strings.TrimSpace(response)
	}

	var parsed factCandidateResponse
	if err := json.Unmarshal([]byte(response), &parsed); err != nil {
		return nil, fmt.Errorf("parse fact-candidate response: %w (response: %.200s)", err, response)
	}

	out := make([]FactCandidate, 0, len(parsed.Candidates))
	for _, c := range parsed.Candidates {
		if !ValidFactKinds[c.Kind] {
			continue // malformed kind — skip
		}
		if strings.TrimSpace(c.Text) == "" {
			continue // empty text — skip
		}
		out = append(out, c)
	}
	return out, nil
}

// distillInsertFactCandidate inserts (or supersedes) a single v2 fact candidate.
//
// Dedup contract against live (non-superseded) facts in the candidate's target
// namespace:
//   - identical text (after trim) → no-op, returns "identical";
//   - embedding near-duplicate (cosine >= distillSupersedeThreshold) → the old
//     fact is superseded by the new candidate, returns "superseded";
//   - otherwise → a new fact is inserted, returns "inserted".
//
// Preferences are stored user-global (namespace ""); all other kinds are
// repo-scoped to the distill namespace. Source is always "inferred"; git commit
// and session id are stamped via AddFact/SupersedeFact provenance. A candidate
// with an invalid kind or empty text returns "malformed"; any other error
// returns "error". None of these fail the distill.
func (e *Engine) distillInsertFactCandidate(ctx context.Context, c FactCandidate, namespace, sessionID string) (status string, err error) {
	if !ValidFactKinds[c.Kind] {
		return "malformed", fmt.Errorf("invalid fact kind %q", c.Kind)
	}
	text := strings.TrimSpace(c.Text)
	if text == "" {
		return "malformed", fmt.Errorf("empty fact text")
	}

	// Preferences are user-global; everything else is repo-scoped.
	ns := namespace
	if c.Kind == FactKindPreference {
		ns = ""
	}
	files := c.Files
	if files == nil {
		files = []string{}
	}

	// Embed once; reuse for both the identical/near-dup checks and the insert.
	emb, err := e.embedder.Embed(ctx, text, "RETRIEVAL_DOCUMENT")
	if err != nil {
		return "error", fmt.Errorf("embed candidate: %w", err)
	}

	// Load live facts in this namespace for the dedup check.
	e.mu.RLock()
	live, lerr := e.store.GetFactsByNamespaces([]string{ns}, "", false)
	e.mu.RUnlock()
	if lerr != nil {
		return "error", fmt.Errorf("load live facts: %w", lerr)
	}

	// Identical text → no-op.
	for _, f := range live {
		if strings.TrimSpace(f.Text) == text {
			return "identical", nil
		}
	}

	// Near-duplicate (cosine >= threshold) → supersede the closest match.
	var bestMatch *Fact
	bestSim := distillSupersedeThreshold - 1 // start below threshold
	for _, f := range live {
		sim := CosineSimilarity(emb, f.Vector)
		if sim >= distillSupersedeThreshold && sim > bestSim {
			bestMatch = f
			bestSim = sim
		}
	}
	if bestMatch != nil {
		if _, err := e.SupersedeFact(ctx, bestMatch.ID, Fact{
			Namespace: ns,
			Kind:      c.Kind,
			Source:    FactSourceInferred,
			Text:      text,
			Files:     files,
			SessionID: sessionID,
		}); err != nil {
			return "error", fmt.Errorf("supersede near-dup: %w", err)
		}
		return "superseded", nil
	}

	// Otherwise insert a new fact.
	if _, err := e.AddFact(ctx, Fact{
		Namespace: ns,
		Kind:      c.Kind,
		Source:    FactSourceInferred,
		Text:      text,
		Files:     files,
		SessionID: sessionID,
	}); err != nil {
		return "error", fmt.Errorf("insert fact: %w", err)
	}
	return "inserted", nil
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

// collectTouchedFiles returns the set of repo-relative paths the session read
// or edited. It is the input to the file-touch hit signal (RecordFileTouches).
// Two sources are merged:
//  1. The auto-capture hook log ($TMPDIR/dualmem-session-<nshash>-*.jsonl) —
//     each line carries a `files` field naming the path the tool touched. This
//     is the authoritative source where available (Claude Code only).
//  2. A light scan of the transcript text for path-like tokens as a fallback
//     for harnesses without the hook (best-effort, no false positives matter:
//     the hit correlator only credits facts whose cited files match).
func collectTouchedFiles(namespace, rootDir, transcript string) []string {
	seen := make(map[string]bool)
	var out []string
	add := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		out = append(out, p)
	}

	// Source 1: auto-capture hook log.
	for _, p := range readSessionLogFiles(namespace, rootDir) {
		add(p)
	}

	// Source 2: transcript path-token scan. We only accept tokens that look
	// like repo-relative paths (contain a slash and a dotted extension) to keep
	// false positives down — the correlator intersects against fact files, so a
	// stray token only matters if it coincidentally matches a cited path.
	if transcript != "" {
		for _, tok := range scanPathTokens(transcript) {
			add(tok)
		}
	}
	return out
}


// readSessionLogFiles returns the deduplicated `files` values from the most
// recent auto-capture hook log for this namespace. Returns nil when the hook
// is unavailable (non-CC harness, cleared TMPDIR). The namespace-hash
// convention must match md5Short (and the hook script).
func readSessionLogFiles(namespace, rootDir string) []string {
	nsBase := ""
	if rootDir != "" {
		nsBase = filepath.Base(rootDir)
	} else if namespace != "" {
		nsBase = strings.TrimPrefix(namespace, "claude:")
	}
	if nsBase == "" {
		return nil
	}
	tmpDir := os.TempDir()
	pattern := filepath.Join(tmpDir, fmt.Sprintf("dualmem-session-%s-*.jsonl", md5Short(nsBase)))
	matches, err := filepath.Glob(pattern)
	if err != nil || len(matches) == 0 {
		return nil
	}
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
		return nil
	}
	type logEntry struct {
		Files string `json:"files"`
	}
	var out []string
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
			out = append(out, entry.Files)
		}
	}
	return out
}

// pathTokenExtents lists file extensions whose path-like tokens we accept from
// the transcript fallback scan. Kept conservative to limit false positives.
var pathTokenExtents = map[string]bool{
	".go": true, ".py": true, ".rs": true, ".ts": true, ".tsx": true,
	".js": true, ".jsx": true, ".java": true, ".kt": true, ".rb": true,
	".swift": true, ".c": true, ".h": true, ".cpp": true, ".cc": true,
	".hpp": true, ".md": true, ".yaml": true, ".yml": true, ".json": true,
	".toml": true, ".sh": true,
}

// scanPathTokens extracts repo-relative-looking path tokens from free text.
// A token qualifies if it contains a slash and ends with a known source-file
// extension. It's a coarse fallback for the hook log; the correlator's
// intersection with cited files keeps false positives harmless.
func scanPathTokens(text string) []string {
	var out []string
	fields := strings.Fields(text)
	for _, f := range fields {
		// Trim surrounding punctuation that tokenizers often leave attached.
		f = strings.Trim(f, "\"'`()*,;:<>[](){}")
		if !strings.Contains(f, "/") {
			continue
		}
		dot := strings.LastIndex(f, ".")
		if dot < 0 {
			continue
		}
		ext := strings.ToLower(f[dot:])
		if !pathTokenExtents[ext] {
			continue
		}
		out = append(out, f)
	}
	return out
}
