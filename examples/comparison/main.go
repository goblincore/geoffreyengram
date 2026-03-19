// Comparison test: geoffreyengram vs no memory vs flat RAG.
//
// Runs a scripted multi-session player scenario through 3 memory modes,
// generates character responses for each, and uses LLM-as-judge to evaluate.
//
// Usage:
//
//	GEMINI_API_KEY=... go run ./examples/comparison/
//	GEMINI_API_KEY=... go run ./examples/comparison/ --scenario lily
//	GEMINI_API_KEY=... go run ./examples/comparison/ --list
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	engram "github.com/goblincore/geoffreyengram"
)

// --- Types ---

type modeName string

const (
	modeStateless modeName = "stateless"
	modeFlatRAG   modeName = "flat-rag"
	modeEngram    modeName = "engram"
)

var allModes = []modeName{modeStateless, modeFlatRAG, modeEngram}

type turn struct {
	player string
}

type session struct {
	name  string
	turns []turn
	isGap bool // time gap: no player interaction, reflection fires
}

// Per-mode response for a single turn.
type modeResponse struct {
	mode     modeName
	response string
}

type judgeScores struct {
	Mode        string `json:"mode"`
	Scores      scores `json:"scores"`
	Explanation string `json:"explanation"`
}

type scores struct {
	Recall      float64 `json:"recall"`
	Relevance   float64 `json:"relevance"`
	Personality float64 `json:"personality"`
	Insight     float64 `json:"insight"`
	Naturalness float64 `json:"naturalness"`
}

func (s scores) average() float64 {
	return (s.Recall + s.Relevance + s.Personality + s.Insight + s.Naturalness) / 5.0
}

// --- Flat RAG Store ---

type flatMemory struct {
	content string
	vector  []float32
}

type flatRAGStore struct {
	embedder engram.EmbeddingProvider
	memories []flatMemory
}

func newFlatRAGStore(embedder engram.EmbeddingProvider) *flatRAGStore {
	return &flatRAGStore{embedder: embedder}
}

func (f *flatRAGStore) store(ctx context.Context, content string) error {
	vec, err := f.embedder.Embed(ctx, content, "RETRIEVAL_DOCUMENT")
	if err != nil {
		return fmt.Errorf("flat-rag embed: %w", err)
	}
	f.memories = append(f.memories, flatMemory{content: content, vector: vec})
	return nil
}

func (f *flatRAGStore) retrieve(ctx context.Context, query string, limit int) ([]string, error) {
	if len(f.memories) == 0 {
		return nil, nil
	}
	queryVec, err := f.embedder.Embed(ctx, query, "RETRIEVAL_QUERY")
	if err != nil {
		return nil, fmt.Errorf("flat-rag query embed: %w", err)
	}

	type scored struct {
		content string
		sim     float64
	}
	var results []scored
	for _, m := range f.memories {
		sim := engram.CosineSimilarity(queryVec, m.vector)
		results = append(results, scored{m.content, sim})
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].sim > results[j].sim
	})
	if len(results) > limit {
		results = results[:limit]
	}

	var out []string
	for _, r := range results {
		out = append(out, r.content)
	}
	return out, nil
}

// --- Gemini Chat Client ---

type geminiClient struct {
	apiKey string
	model  string
	client *http.Client
}

func newGeminiClient(apiKey string) *geminiClient {
	return &geminiClient{
		apiKey: apiKey,
		model:  "gemini-2.5-flash-lite",
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

func (g *geminiClient) generate(ctx context.Context, prompt string, maxTokens int, temperature float64) (string, error) {
	url := "https://generativelanguage.googleapis.com/v1beta/models/" +
		g.model + ":generateContent?key=" + g.apiKey

	reqBody := map[string]any{
		"contents": []map[string]any{
			{"role": "user", "parts": []map[string]any{{"text": prompt}}},
		},
		"generationConfig": map[string]any{
			"maxOutputTokens": maxTokens,
			"temperature":     temperature,
		},
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return "", fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := g.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("gemini %d: %s", resp.StatusCode, string(body[:min(len(body), 300)]))
	}

	var geminiResp struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&geminiResp); err != nil {
		return "", fmt.Errorf("decode: %w", err)
	}

	if len(geminiResp.Candidates) == 0 || len(geminiResp.Candidates[0].Content.Parts) == 0 {
		return "", fmt.Errorf("empty response")
	}

	return strings.TrimSpace(geminiResp.Candidates[0].Content.Parts[0].Text), nil
}

func buildCharacterPrompt(sc *Scenario, playerMessage string, memories []string) string {
	var b strings.Builder
	b.WriteString(sc.CharacterPrompt)
	b.WriteString("\n\n")

	if len(memories) > 0 {
		b.WriteString("Relevant memories from past conversations:\n")
		for _, m := range memories {
			fmt.Fprintf(&b, "- %s\n", m)
		}
		b.WriteString("\n")
	}

	fmt.Fprintf(&b, "The player just said: %q\n\n", playerMessage)
	b.WriteString(sc.ResponseInstruction)
	return b.String()
}

// rateLimitDelay sleeps between Gemini API calls to stay under free tier limits.
func rateLimitDelay() {
	time.Sleep(1200 * time.Millisecond)
}

// --- Mode Runners ---

// runStateless generates responses with no memory at all.
func runStateless(ctx context.Context, gemini *geminiClient, sc *Scenario) (map[int][]string, error) {
	results := make(map[int][]string)
	for si, sess := range sc.Sessions {
		if sess.isGap {
			continue
		}
		for _, t := range sess.turns {
			prompt := buildCharacterPrompt(sc, t.player, nil)
			rateLimitDelay()
			resp, err := gemini.generate(ctx, prompt, 256, 0.7)
			if err != nil {
				return nil, fmt.Errorf("stateless session %d: %w", si+1, err)
			}
			results[si] = append(results[si], resp)
		}
	}
	return results, nil
}

// runFlatRAG generates responses using flat vector similarity retrieval.
func runFlatRAG(ctx context.Context, gemini *geminiClient, embedder engram.EmbeddingProvider, sc *Scenario) (map[int][]string, error) {
	store := newFlatRAGStore(embedder)
	results := make(map[int][]string)
	limit := retrievalLimit(sc)

	for si, sess := range sc.Sessions {
		if sess.isGap {
			continue
		}
		for _, t := range sess.turns {
			// Retrieve relevant memories (top-k by cosine similarity)
			memories, err := store.retrieve(ctx, t.player, limit)
			if err != nil {
				log.Printf("[flat-rag] retrieve error: %v", err)
			}

			// Generate character response
			prompt := buildCharacterPrompt(sc, t.player, memories)
			rateLimitDelay()
			resp, err := gemini.generate(ctx, prompt, 256, 0.7)
			if err != nil {
				return nil, fmt.Errorf("flat-rag session %d: %w", si+1, err)
			}
			results[si] = append(results[si], resp)

			// Store the exchange
			content := fmt.Sprintf("%s: %s | %s: %s", sc.PlayerName, t.player, sc.CharacterName, resp)
			if err := store.store(ctx, content); err != nil {
				log.Printf("[flat-rag] store error: %v", err)
			}
		}
	}
	return results, nil
}

// runEngram generates responses using the full geoffreyengram engine.
func runEngram(ctx context.Context, gemini *geminiClient, apiKey string, sc *Scenario) (map[int][]string, error) {
	tmpDir, err := os.MkdirTemp("", "engram-comparison-*")
	if err != nil {
		return nil, fmt.Errorf("temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "engram.db")

	em, err := engram.Init(engram.Config{
		DBPath:             dbPath,
		GeminiAPIKey:       apiKey,
		EmbedDimension:     768,
		DecayInterval:      1 * time.Hour,
		ReflectionProvider: engram.NewGeminiReflector(apiKey),
	})
	if err != nil {
		return nil, fmt.Errorf("engram init: %w", err)
	}
	defer em.Close()

	results := make(map[int][]string)
	sessionCounter := 0
	limit := retrievalLimit(sc)

	for si, sess := range sc.Sessions {
		sessionCounter++
		sessionID := fmt.Sprintf("session-%d", sessionCounter)

		if sess.isGap {
			fmt.Println("  [engram] Running reflection...")
			rateLimitDelay()
			reflections, rErr := em.Reflect(ctx, engram.ReflectOptions{
				UserID:           sc.UserID,
				CharacterContext: sc.CharacterPrompt,
				MemoryWindow:     50,
				MinMemories:      3,
			})
			if rErr != nil {
				log.Printf("[engram] reflect error: %v", rErr)
			} else {
				fmt.Printf("  [engram] Generated %d reflections\n", len(reflections))
			}
			continue
		}

		var parentID int64
		for _, t := range sess.turns {
			// Search with personality weights
			rateLimitDelay()
			searchResults := em.Search(t.player, sc.UserID, limit, sc.SectorWeights)
			var memories []string
			for _, sr := range searchResults {
				memories = append(memories, sr.Summary)
			}

			// Generate character response
			prompt := buildCharacterPrompt(sc, t.player, memories)
			rateLimitDelay()
			resp, err := gemini.generate(ctx, prompt, 256, 0.7)
			if err != nil {
				return nil, fmt.Errorf("engram session %d: %w", si+1, err)
			}
			results[si] = append(results[si], resp)

			// Store with session threading
			memID, storeErr := em.AddWithOptions(engram.AddOptions{
				UserID:           sc.UserID,
				UserMessage:      t.player,
				AssistantMessage: resp,
				SessionID:        sessionID,
				ParentID:         parentID,
			})
			if storeErr != nil {
				log.Printf("[engram] add error: %v", storeErr)
			}
			parentID = memID
		}
	}
	return results, nil
}

// --- LLM-as-Judge ---

func runJudge(ctx context.Context, gemini *geminiClient, sc *Scenario, statelessResp, flatRAGResp, engramResp string) ([]judgeScores, error) {
	prompt := fmt.Sprintf(`You are evaluating AI character memory quality. %s

The character responded:

Response A (no memory): %q

Response B (flat retrieval): %q

Response C (cognitive memory): %q

Rate each response 1-5 on:
1. Memory recall — does the character remember specific facts about %s?
2. Relevance — are the referenced memories appropriate for this moment?
3. Personality — does the character feel consistent and authentic?
4. Insight — does the character show understanding beyond surface facts?
5. Naturalness — does the response feel natural, not like a database dump?

Return ONLY a JSON object with this exact structure:
{"responses": [{"mode": "A", "scores": {"recall": N, "relevance": N, "personality": N, "insight": N, "naturalness": N}, "explanation": "..."}, {"mode": "B", "scores": {"recall": N, "relevance": N, "personality": N, "insight": N, "naturalness": N}, "explanation": "..."}, {"mode": "C", "scores": {"recall": N, "relevance": N, "personality": N, "insight": N, "naturalness": N}, "explanation": "..."}]}`,
		sc.JudgeContext, statelessResp, flatRAGResp, engramResp, sc.PlayerName)

	rateLimitDelay()
	resp, err := gemini.generate(ctx, prompt, 1024, 0.3)
	if err != nil {
		return nil, fmt.Errorf("judge: %w", err)
	}

	// Strip markdown code blocks
	text := strings.TrimSpace(resp)
	if strings.HasPrefix(text, "```") {
		lines := strings.Split(text, "\n")
		var jsonLines []string
		inBlock := false
		for _, line := range lines {
			if strings.HasPrefix(line, "```") {
				inBlock = !inBlock
				continue
			}
			if inBlock {
				jsonLines = append(jsonLines, line)
			}
		}
		text = strings.Join(jsonLines, "\n")
	}

	var result struct {
		Responses []judgeScores `json:"responses"`
	}
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		return nil, fmt.Errorf("parse judge response: %w\nraw: %s", err, text)
	}

	return result.Responses, nil
}

// --- Output ---

// writeResultsFile writes a markdown file with each mode's full conversation
// shown end-to-end, plus the evaluation scores. Designed for human comparison.
func writeResultsFile(
	path string,
	sc *Scenario,
	allResults map[modeName]map[int][]string,
	judgeResults []judgeScores,
) error {
	var b strings.Builder

	b.WriteString("# geoffreyengram Comparison Results\n\n")
	b.WriteString(fmt.Sprintf("**Scenario:** %s  \n", sc.Title))
	b.WriteString(fmt.Sprintf("**Description:** %s  \n", sc.Description))
	b.WriteString(fmt.Sprintf("**Generated:** %s  \n", time.Now().Format("2006-01-02 15:04")))
	b.WriteString(fmt.Sprintf("**Sessions:** %d (%d history + 1 probe)\n\n", len(sc.Sessions), len(sc.Sessions)-1))

	b.WriteString("---\n\n")

	// One full conversation per mode — much easier to read than interleaved
	modeLabels := map[modeName]string{
		modeStateless: "Mode A: Stateless (no memory)",
		modeFlatRAG:   "Mode B: Flat RAG (embed + cosine top-k)",
		modeEngram:    "Mode C: geoffreyengram (full cognitive memory)",
	}

	modeDescriptions := map[modeName]string{
		modeStateless: "No memory at all. Every session is like meeting for the first time.",
		modeFlatRAG:   "Stores conversation embeddings and retrieves by cosine similarity. No sectors, no decay, no salience, no waypoints, no reflection.",
		modeEngram:    "Full cognitive memory: 5 sectors, composite scoring (similarity × salience × recency × link weight × personality), waypoint entity graph, high-salience guarantee, conversation threading, and reflective synthesis between sessions.",
	}

	for _, mode := range allModes {
		b.WriteString(fmt.Sprintf("## %s\n\n", modeLabels[mode]))
		b.WriteString(fmt.Sprintf("> %s\n\n", modeDescriptions[mode]))

		results := allResults[mode]

		for si, sess := range sc.Sessions {
			if sess.isGap {
				b.WriteString(fmt.Sprintf("### Session %d: %s\n\n", si+1, sess.name))
				if mode == modeEngram {
					b.WriteString("*[Time passes — reflective synthesis fires, analyzing recent memories for patterns]*\n\n")
				} else {
					b.WriteString("*[Time passes — no action taken]*\n\n")
				}
				continue
			}

			b.WriteString(fmt.Sprintf("### Session %d: %s\n\n", si+1, sess.name))

			resps := results[si]
			for ti, t := range sess.turns {
				b.WriteString(fmt.Sprintf("**%s:** %s\n\n", sc.PlayerName, t.player))
				resp := "(no response)"
				if ti < len(resps) {
					resp = resps[ti]
				}
				b.WriteString(fmt.Sprintf("**%s:** %s\n\n", sc.CharacterName, resp))
			}
		}

		b.WriteString("---\n\n")
	}

	// Evaluation scores
	if len(judgeResults) > 0 {
		b.WriteString("## Evaluation Scores (LLM-as-Judge)\n\n")

		modeMap := map[string]modeName{"A": modeStateless, "B": modeFlatRAG, "C": modeEngram}
		scoresByMode := make(map[modeName]scores)
		explanations := make(map[modeName]string)
		for _, js := range judgeResults {
			if name, ok := modeMap[js.Mode]; ok {
				scoresByMode[name] = js.Scores
				explanations[name] = js.Explanation
			}
		}

		fields := []string{"Recall", "Relevance", "Personality", "Insight", "Naturalness"}
		fieldKeys := []string{"recall", "relevance", "personality", "insight", "naturalness"}

		b.WriteString("| Metric | Stateless | Flat RAG | Engram |\n")
		b.WriteString("|--------|-----------|----------|--------|\n")
		for i, f := range fields {
			b.WriteString(fmt.Sprintf("| **%s** ", f))
			for _, mode := range allModes {
				s := scoresByMode[mode]
				b.WriteString(fmt.Sprintf("| %.1f ", getScoreField(s, fieldKeys[i])))
			}
			b.WriteString("|\n")
		}
		b.WriteString("| **Average** ")
		for _, mode := range allModes {
			s := scoresByMode[mode]
			b.WriteString(fmt.Sprintf("| **%.1f** ", s.average()))
		}
		b.WriteString("|\n\n")

		b.WriteString("### Judge Explanations\n\n")
		judgeLabels := map[modeName]string{
			modeStateless: "Stateless",
			modeFlatRAG:   "Flat RAG",
			modeEngram:    "Engram",
		}
		for _, mode := range allModes {
			if exp, ok := explanations[mode]; ok {
				b.WriteString(fmt.Sprintf("**%s:** %s\n\n", judgeLabels[mode], exp))
			}
		}
	}

	return os.WriteFile(path, []byte(b.String()), 0644)
}

func printReport(
	sc *Scenario,
	allResults map[modeName]map[int][]string,
	judgeResults []judgeScores,
) {
	fmt.Println()
	fmt.Println("═══════════════════════════════════════════════════════════")
	fmt.Println("  geoffreyengram Comparison Test")
	fmt.Println("═══════════════════════════════════════════════════════════")
	fmt.Println()
	fmt.Printf("  Scenario:  %s\n", sc.Title)
	fmt.Printf("  Sessions:  %d (%d history + 1 probe)\n", len(sc.Sessions), len(sc.Sessions)-1)
	fmt.Println()

	// Session transcripts
	fmt.Println("── Session Transcripts ──────────────────────────────────")
	fmt.Println()

	for si, sess := range sc.Sessions {
		if sess.isGap {
			fmt.Printf("  Session %d: %s\n", si+1, sess.name)
			fmt.Println("    [time passes, reflection fires for engram mode]")
			fmt.Println()
			continue
		}

		isProbe := si == len(sc.Sessions)-1
		if isProbe {
			fmt.Println("── Probe Session ───────────────────────────────────────")
			fmt.Println()
		}

		fmt.Printf("  Session %d: %s\n", si+1, sess.name)
		fmt.Println()

		for ti, t := range sess.turns {
			fmt.Printf("    %s: %s\n", sc.PlayerName, t.player)
			fmt.Println()
			for _, mode := range allModes {
				resps := allResults[mode][si]
				resp := "(no response)"
				if ti < len(resps) {
					resp = resps[ti]
				}
				label := fmt.Sprintf("    [%-10s]", mode)
				fmt.Printf("%s %s\n", label, wrapText(resp, 70, len(label)+1))
			}
			fmt.Println()
		}
	}

	// Evaluation scores
	if len(judgeResults) == 0 {
		fmt.Println("  (No judge evaluation available)")
		return
	}

	fmt.Println("── Evaluation Scores ────────────────────────────────────")
	fmt.Println()

	// Map mode letters to names
	modeMap := map[string]modeName{"A": modeStateless, "B": modeFlatRAG, "C": modeEngram}
	scoresByMode := make(map[modeName]scores)
	explanations := make(map[modeName]string)
	for _, js := range judgeResults {
		if name, ok := modeMap[js.Mode]; ok {
			scoresByMode[name] = js.Scores
			explanations[name] = js.Explanation
		}
	}

	fields := []string{"recall", "relevance", "personality", "insight", "naturalness"}

	fmt.Printf("  %-14s %10s %10s %10s\n", "", "Stateless", "Flat RAG", "Engram")
	fmt.Println("  " + strings.Repeat("─", 46))

	for _, f := range fields {
		fmt.Printf("  %-14s", strings.Title(f)) //nolint:staticcheck
		for _, mode := range allModes {
			s := scoresByMode[mode]
			fmt.Printf(" %9.1f", getScoreField(s, f))
		}
		fmt.Println()
	}

	fmt.Println("  " + strings.Repeat("─", 46))
	fmt.Printf("  %-14s", "Average")
	for _, mode := range allModes {
		s := scoresByMode[mode]
		fmt.Printf(" %9.1f", s.average())
	}
	fmt.Println()
	fmt.Println()

	// Explanations
	fmt.Println("── Judge Explanations ───────────────────────────────────")
	fmt.Println()
	for _, mode := range allModes {
		if exp, ok := explanations[mode]; ok {
			fmt.Printf("  [%s] %s\n\n", mode, wrapText(exp, 68, len(string(mode))+5))
		}
	}

	fmt.Println("═══════════════════════════════════════════════════════════")
}

func getScoreField(s scores, field string) float64 {
	switch field {
	case "recall":
		return s.Recall
	case "relevance":
		return s.Relevance
	case "personality":
		return s.Personality
	case "insight":
		return s.Insight
	case "naturalness":
		return s.Naturalness
	}
	return 0
}

func wrapText(text string, width, indent int) string {
	// Simple word wrapper: if the text fits in width, return as-is.
	// Otherwise wrap at word boundaries with indent.
	if len(text) <= width {
		return text
	}

	prefix := strings.Repeat(" ", indent)
	words := strings.Fields(text)
	var lines []string
	var current string

	for _, w := range words {
		if current == "" {
			current = w
		} else if len(current)+1+len(w) <= width {
			current += " " + w
		} else {
			lines = append(lines, current)
			current = w
		}
	}
	if current != "" {
		lines = append(lines, current)
	}

	if len(lines) == 0 {
		return text
	}
	// First line has no prefix (caller already indented)
	result := lines[0]
	for _, l := range lines[1:] {
		result += "\n" + prefix + l
	}
	return result
}

// --- CLI ---

func printScenarioList() {
	fmt.Println("Available scenarios:")
	fmt.Println()
	for i, sc := range AllScenarios {
		fmt.Printf("  %d. %-12s %s\n", i+1, sc.Name, sc.Description)
	}
	fmt.Println()
	fmt.Println("Usage: go run ./examples/comparison/ --scenario <name>")
}

func interactiveSelect() *Scenario {
	printScenarioList()
	fmt.Println()
	fmt.Printf("Select scenario (1-%d): ", len(AllScenarios))

	var choice int
	if _, err := fmt.Scan(&choice); err != nil || choice < 1 || choice > len(AllScenarios) {
		fmt.Fprintln(os.Stderr, "Invalid selection")
		os.Exit(1)
	}
	fmt.Println()
	return &AllScenarios[choice-1]
}

// --- Main ---

func main() {
	scenarioFlag := flag.String("scenario", "", "Scenario to run (e.g. lily, sifu, nyx, reeves)")
	listFlag := flag.Bool("list", false, "List available scenarios and exit")
	flag.Parse()

	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" && !*listFlag {
		fmt.Fprintln(os.Stderr, "GEMINI_API_KEY environment variable required")
		os.Exit(1)
	}

	if *listFlag {
		printScenarioList()
		return
	}

	var sc *Scenario
	if *scenarioFlag != "" {
		sc = ScenarioByName(*scenarioFlag)
		if sc == nil {
			fmt.Fprintf(os.Stderr, "Unknown scenario: %q\nAvailable: ", *scenarioFlag)
			for _, s := range AllScenarios {
				fmt.Fprintf(os.Stderr, "%s ", s.Name)
			}
			fmt.Fprintln(os.Stderr)
			os.Exit(1)
		}
	} else {
		sc = interactiveSelect()
	}

	ctx := context.Background()
	gemini := newGeminiClient(apiKey)
	embedder := engram.NewGeminiEmbedder(apiKey, 768)

	fmt.Println("╔═══════════════════════════════════════════════════════╗")
	fmt.Println("║  geoffreyengram Comparison Test                      ║")
	fmt.Println("║  Stateless vs Flat RAG vs Cognitive Memory           ║")
	fmt.Println("╚═══════════════════════════════════════════════════════╝")
	fmt.Println()
	fmt.Printf("  Scenario: %s\n", sc.Title)
	fmt.Printf("  Player:   %s → %s\n", sc.PlayerName, sc.CharacterName)
	fmt.Println()

	allResults := make(map[modeName]map[int][]string)

	// Mode 1: Stateless
	fmt.Println("[1/4] Running stateless mode (no memory)...")
	statelessResults, err := runStateless(ctx, gemini, sc)
	if err != nil {
		log.Fatalf("Stateless failed: %v", err)
	}
	allResults[modeStateless] = statelessResults
	fmt.Printf("  Done (%d responses)\n\n", countResponses(statelessResults))

	// Mode 2: Flat RAG
	fmt.Println("[2/4] Running flat-rag mode (embed + cosine top-k)...")
	flatRAGResults, err := runFlatRAG(ctx, gemini, embedder, sc)
	if err != nil {
		log.Fatalf("Flat RAG failed: %v", err)
	}
	allResults[modeFlatRAG] = flatRAGResults
	fmt.Printf("  Done (%d responses)\n\n", countResponses(flatRAGResults))

	// Mode 3: Full Engram
	fmt.Println("[3/4] Running engram mode (full cognitive memory)...")
	engramResults, err := runEngram(ctx, gemini, apiKey, sc)
	if err != nil {
		log.Fatalf("Engram failed: %v", err)
	}
	allResults[modeEngram] = engramResults
	fmt.Printf("  Done (%d responses)\n\n", countResponses(engramResults))

	// LLM-as-Judge on probe session
	probeIdx := len(sc.Sessions) - 1
	fmt.Println("[4/4] Running LLM-as-judge evaluation...")

	statelessProbe := getProbeResponse(statelessResults, probeIdx)
	flatRAGProbe := getProbeResponse(flatRAGResults, probeIdx)
	engramProbe := getProbeResponse(engramResults, probeIdx)

	judgeResults, err := runJudge(ctx, gemini, sc, statelessProbe, flatRAGProbe, engramProbe)
	if err != nil {
		log.Printf("Judge evaluation failed: %v", err)
		fmt.Println("  Skipping evaluation, printing transcripts only.")
		judgeResults = nil
	} else {
		fmt.Println("  Done")
	}
	fmt.Println()

	// Write markdown results file
	resultsPath := filepath.Join("examples", "comparison", fmt.Sprintf("results_%s.md", sc.Name))
	if err := writeResultsFile(resultsPath, sc, allResults, judgeResults); err != nil {
		log.Printf("Failed to write results file: %v", err)
	} else {
		fmt.Printf("Results written to %s\n\n", resultsPath)
	}

	// Print the full report to terminal
	printReport(sc, allResults, judgeResults)
}

func countResponses(results map[int][]string) int {
	n := 0
	for _, resps := range results {
		n += len(resps)
	}
	return n
}

func getProbeResponse(results map[int][]string, probeIdx int) string {
	if resps, ok := results[probeIdx]; ok && len(resps) > 0 {
		return resps[0]
	}
	return "(no response generated)"
}
