// DualMem LLM-as-Judge Benchmark
//
// Compares 3 memory modes for coding agent tasks:
//   - No memory (baseline)
//   - Flat cosine similarity search
//   - Full DualMem (AssembleContext with priority/budget/intent)
//
// Usage:
//
//	GEMINI_API_KEY=... go run ./examples/dualmem-bench/
//	GEMINI_API_KEY=... go run ./examples/dualmem-bench/ --task decision
//	GEMINI_API_KEY=... go run ./examples/dualmem-bench/ --list
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

	"github.com/goblincore/geoffreyengram/dualmem"
)

// --- Types ---

type modeName string

const (
	modeNone    modeName = "none"
	modeFlat    modeName = "flat"
	modeDualMem modeName = "dualmem"
)

var allModes = []modeName{modeNone, modeFlat, modeDualMem}

type judgeScores struct {
	Mode        string `json:"mode"`
	Scores      scores `json:"scores"`
	Explanation string `json:"explanation"`
}

type scores struct {
	MemoryRecall    float64 `json:"memory_recall"`
	Relevance       float64 `json:"relevance"`
	Prioritization  float64 `json:"prioritization"`
	Completeness    float64 `json:"completeness"`
	NoHallucination float64 `json:"no_hallucination"`
}

func (s scores) average() float64 {
	return (s.MemoryRecall + s.Relevance + s.Prioritization + s.Completeness + s.NoHallucination) / 5.0
}

// --- Gemini Client ---

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
		limit := len(body)
		if limit > 300 {
			limit = 300
		}
		return "", fmt.Errorf("gemini %d: %s", resp.StatusCode, string(body[:limit]))
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

func rateLimitDelay() {
	time.Sleep(1200 * time.Millisecond)
}

// --- Mode Runners ---

func buildAgentPrompt(t *task, memoryContext string) string {
	var b strings.Builder
	b.WriteString(t.Setup)
	b.WriteString("\n\n")

	if memoryContext != "" {
		b.WriteString("=== Project Memory Context ===\n")
		b.WriteString(memoryContext)
		b.WriteString("\n=== End Context ===\n\n")
	}

	fmt.Fprintf(&b, "User question: %s\n\n", t.Probe)
	b.WriteString("Answer concisely based on the context provided. If you have relevant project memory, reference it specifically.")
	return b.String()
}

// runNone generates a response with zero memory context.
func runNone(ctx context.Context, gemini *geminiClient, t *task) (string, error) {
	prompt := buildAgentPrompt(t, "")
	rateLimitDelay()
	return gemini.generate(ctx, prompt, 512, 0.3)
}

// runFlat seeds a DualMem engine, then does raw cosine search (no AssembleContext).
func runFlat(ctx context.Context, gemini *geminiClient, t *task, embedder dualmem.EmbeddingProvider) (string, error) {
	engine, cleanup, err := seedEngine(t, embedder)
	if err != nil {
		return "", err
	}
	defer cleanup()

	// Embed query
	queryEmb, err := embedder.Embed(ctx, t.Probe, "RETRIEVAL_QUERY")
	if err != nil {
		return "", fmt.Errorf("embed query: %w", err)
	}

	// Raw cosine search on Detail memories
	results, err := engine.DualSearch(ctx, "bench-user", t.Probe, dualmem.SearchOpts{
		Limit:          10,
		IncludeSketch:  false, // flat = detail only
		QueryEmbedding: queryEmb,
	})
	if err != nil {
		return "", fmt.Errorf("search: %w", err)
	}

	// Format as simple list
	var memCtx strings.Builder
	for _, dm := range results.DetailMemories {
		memCtx.WriteString("- " + dm.Text + "\n")
	}

	prompt := buildAgentPrompt(t, memCtx.String())
	rateLimitDelay()
	return gemini.generate(ctx, prompt, 512, 0.3)
}

// runDualMem uses full AssembleContext with priority, budget, and intent detection.
func runDualMem(ctx context.Context, gemini *geminiClient, t *task, embedder dualmem.EmbeddingProvider) (string, error) {
	engine, cleanup, err := seedEngine(t, embedder)
	if err != nil {
		return "", err
	}
	defer cleanup()

	block, err := engine.AssembleContext(ctx, "bench-user", t.Probe, 3000)
	if err != nil {
		return "", fmt.Errorf("assemble: %w", err)
	}

	prompt := buildAgentPrompt(t, block.Text)
	rateLimitDelay()
	return gemini.generate(ctx, prompt, 512, 0.3)
}

// seedEngine creates a temp DualMem engine and populates it with task history.
func seedEngine(t *task, embedder dualmem.EmbeddingProvider) (*dualmem.Engine, func(), error) {
	tmpDir, err := os.MkdirTemp("", "dualmem-bench-*")
	if err != nil {
		return nil, nil, err
	}

	dbPath := filepath.Join(tmpDir, "bench.db")
	engine, err := dualmem.New(dualmem.Config{
		SQLitePath:        dbPath,
		EmbeddingProvider: embedder,
		MaxDetailPerUser:  200,
		ImportanceTheta:   0.65,
	})
	if err != nil {
		os.RemoveAll(tmpDir)
		return nil, nil, err
	}

	ctx := context.Background()
	for _, h := range t.History {
		salience := h.Salience
		if salience == 0 {
			salience = 0.5
		}
		err := engine.AddWithOptions(ctx, dualmem.MemoryInput{
			UserMessage: h.Text,
			SectorHint:  h.Sector,
			Salience:    salience,
			Type:        h.Type,
			Files:       h.Files,
		}, "bench-user")
		if err != nil {
			engine.Close()
			os.RemoveAll(tmpDir)
			return nil, nil, fmt.Errorf("seed %q: %w", h.Text[:min(40, len(h.Text))], err)
		}
	}

	cleanup := func() {
		engine.Close()
		os.RemoveAll(tmpDir)
	}
	return engine, cleanup, nil
}

// --- LLM-as-Judge ---

func runJudge(ctx context.Context, gemini *geminiClient, t *task, noneResp, flatResp, dualResp string) ([]judgeScores, error) {
	prompt := fmt.Sprintf(`You are evaluating an AI coding assistant's ability to use project memory.

Task: %s
Gold answer (what a perfect response includes): %s

The assistant responded in 3 modes:

Response A (no memory): %q

Response B (flat similarity search): %q

Response C (structured memory with priority): %q

Rate each response 1-5 on:
1. Memory recall — does the answer reference specific past decisions, warnings, or context?
2. Relevance — is the retrieved context appropriate for the question?
3. Prioritization — are warnings and critical information properly weighted over routine details?
4. Completeness — does it capture all relevant past context without missing key facts?
5. No hallucination — does it avoid inventing context that doesn't exist in the memory?

Return ONLY a JSON object:
{"responses": [{"mode": "A", "scores": {"memory_recall": N, "relevance": N, "prioritization": N, "completeness": N, "no_hallucination": N}, "explanation": "..."}, {"mode": "B", "scores": {"memory_recall": N, "relevance": N, "prioritization": N, "completeness": N, "no_hallucination": N}, "explanation": "..."}, {"mode": "C", "scores": {"memory_recall": N, "relevance": N, "prioritization": N, "completeness": N, "no_hallucination": N}, "explanation": "..."}]}`,
		t.Description, t.GoldAnswer, noneResp, flatResp, dualResp)

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

func printReport(t *task, responses map[modeName]string, judgeResults []judgeScores) {
	fmt.Println()
	fmt.Println("═══════════════════════════════════════════════════════════")
	fmt.Println("  DualMem Benchmark — LLM-as-Judge")
	fmt.Println("═══════════════════════════════════════════════════════════")
	fmt.Println()
	fmt.Printf("  Task:  %s\n", t.Title)
	fmt.Printf("  Probe: %s\n", t.Probe)
	fmt.Println()

	fmt.Println("── Responses ───────────────────────────────────────────")
	fmt.Println()
	for _, mode := range allModes {
		fmt.Printf("  [%-8s] %s\n\n", mode, responses[mode])
	}

	if len(judgeResults) == 0 {
		fmt.Println("  (No judge evaluation available)")
		return
	}

	fmt.Println("── Evaluation Scores ────────────────────────────────────")
	fmt.Println()

	modeMap := map[string]modeName{"A": modeNone, "B": modeFlat, "C": modeDualMem}
	scoresByMode := make(map[modeName]scores)
	explanations := make(map[modeName]string)
	for _, js := range judgeResults {
		if name, ok := modeMap[js.Mode]; ok {
			scoresByMode[name] = js.Scores
			explanations[name] = js.Explanation
		}
	}

	fields := []string{"memory_recall", "relevance", "prioritization", "completeness", "no_hallucination"}
	labels := []string{"Memory Recall", "Relevance", "Prioritization", "Completeness", "No Hallucination"}

	fmt.Printf("  %-18s %8s %8s %10s\n", "", "None", "Flat", "DualMem")
	fmt.Println("  " + strings.Repeat("─", 48))

	for i, f := range fields {
		fmt.Printf("  %-18s", labels[i])
		for _, mode := range allModes {
			s := scoresByMode[mode]
			fmt.Printf(" %7.1f", getScoreField(s, f))
		}
		fmt.Println()
	}

	fmt.Println("  " + strings.Repeat("─", 48))
	fmt.Printf("  %-18s", "Average")
	for _, mode := range allModes {
		s := scoresByMode[mode]
		fmt.Printf(" %7.1f", s.average())
	}
	fmt.Println()
	fmt.Println()

	fmt.Println("── Judge Explanations ───────────────────────────────────")
	fmt.Println()
	for _, mode := range allModes {
		if exp, ok := explanations[mode]; ok {
			fmt.Printf("  [%-8s] %s\n\n", mode, exp)
		}
	}
	fmt.Println("═══════════════════════════════════════════════════════════")
}

func writeResultsFile(path string, t *task, responses map[modeName]string, judgeResults []judgeScores) error {
	var b strings.Builder

	b.WriteString("# DualMem Benchmark Results\n\n")
	b.WriteString(fmt.Sprintf("**Task:** %s  \n", t.Title))
	b.WriteString(fmt.Sprintf("**Probe:** %s  \n", t.Probe))
	b.WriteString(fmt.Sprintf("**Generated:** %s  \n\n", time.Now().Format("2006-01-02 15:04")))

	b.WriteString("## Responses\n\n")
	for _, mode := range allModes {
		b.WriteString(fmt.Sprintf("### %s\n\n%s\n\n", mode, responses[mode]))
	}

	if len(judgeResults) > 0 {
		modeMap := map[string]modeName{"A": modeNone, "B": modeFlat, "C": modeDualMem}
		scoresByMode := make(map[modeName]scores)
		for _, js := range judgeResults {
			if name, ok := modeMap[js.Mode]; ok {
				scoresByMode[name] = js.Scores
			}
		}

		b.WriteString("## Scores\n\n")
		b.WriteString("| Metric | None | Flat | DualMem |\n")
		b.WriteString("|--------|------|------|--------|\n")

		fields := []string{"memory_recall", "relevance", "prioritization", "completeness", "no_hallucination"}
		labels := []string{"Memory Recall", "Relevance", "Prioritization", "Completeness", "No Hallucination"}
		for i, f := range fields {
			b.WriteString(fmt.Sprintf("| **%s** ", labels[i]))
			for _, mode := range allModes {
				s := scoresByMode[mode]
				b.WriteString(fmt.Sprintf("| %.1f ", getScoreField(s, f)))
			}
			b.WriteString("|\n")
		}
		b.WriteString("| **Average** ")
		for _, mode := range allModes {
			s := scoresByMode[mode]
			b.WriteString(fmt.Sprintf("| **%.1f** ", s.average()))
		}
		b.WriteString("|\n")
	}

	return os.WriteFile(path, []byte(b.String()), 0644)
}

func getScoreField(s scores, field string) float64 {
	switch field {
	case "memory_recall":
		return s.MemoryRecall
	case "relevance":
		return s.Relevance
	case "prioritization":
		return s.Prioritization
	case "completeness":
		return s.Completeness
	case "no_hallucination":
		return s.NoHallucination
	}
	return 0
}

// --- Aggregate ---

type aggregateResult struct {
	Mode   modeName
	Scores scores
	Count  int
}

func printAggregate(all map[string]map[modeName]scores) {
	fmt.Println()
	fmt.Println("═══════════════════════════════════════════════════════════")
	fmt.Println("  Aggregate Results (all tasks)")
	fmt.Println("═══════════════════════════════════════════════════════════")
	fmt.Println()

	agg := make(map[modeName]*aggregateResult)
	for _, mode := range allModes {
		agg[mode] = &aggregateResult{Mode: mode}
	}

	for _, modeScores := range all {
		for mode, s := range modeScores {
			a := agg[mode]
			a.Scores.MemoryRecall += s.MemoryRecall
			a.Scores.Relevance += s.Relevance
			a.Scores.Prioritization += s.Prioritization
			a.Scores.Completeness += s.Completeness
			a.Scores.NoHallucination += s.NoHallucination
			a.Count++
		}
	}

	fields := []string{"Memory Recall", "Relevance", "Prioritization", "Completeness", "No Hallucination"}

	fmt.Printf("  %-18s %8s %8s %10s\n", "", "None", "Flat", "DualMem")
	fmt.Println("  " + strings.Repeat("─", 48))

	for i, label := range fields {
		fmt.Printf("  %-18s", label)
		for _, mode := range allModes {
			a := agg[mode]
			if a.Count == 0 {
				fmt.Printf(" %7s", "-")
				continue
			}
			var val float64
			switch i {
			case 0:
				val = a.Scores.MemoryRecall
			case 1:
				val = a.Scores.Relevance
			case 2:
				val = a.Scores.Prioritization
			case 3:
				val = a.Scores.Completeness
			case 4:
				val = a.Scores.NoHallucination
			}
			fmt.Printf(" %7.1f", val/float64(a.Count))
		}
		fmt.Println()
	}

	fmt.Println("  " + strings.Repeat("─", 48))
	fmt.Printf("  %-18s", "Average")
	for _, mode := range allModes {
		a := agg[mode]
		if a.Count == 0 {
			fmt.Printf(" %7s", "-")
			continue
		}
		total := (a.Scores.MemoryRecall + a.Scores.Relevance + a.Scores.Prioritization + a.Scores.Completeness + a.Scores.NoHallucination) / (5.0 * float64(a.Count))
		fmt.Printf(" %7.1f", total)
	}
	fmt.Println()
	fmt.Println()
	fmt.Println("═══════════════════════════════════════════════════════════")
}

// --- CLI ---

func printTaskList() {
	fmt.Println("Available tasks:")
	fmt.Println()
	for i, t := range allTasks {
		fmt.Printf("  %d. %-12s %s\n", i+1, t.Name, t.Description)
	}
	fmt.Println()
	fmt.Println("Usage: go run ./examples/dualmem-bench/ --task <name>")
	fmt.Println("       go run ./examples/dualmem-bench/ --task all")
}

// --- Main ---

func main() {
	taskFlag := flag.String("task", "", "Task to run (e.g. decision, warning, continuity, noise, all)")
	listFlag := flag.Bool("list", false, "List available tasks and exit")
	flag.Parse()

	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" && !*listFlag {
		fmt.Fprintln(os.Stderr, "GEMINI_API_KEY environment variable required")
		os.Exit(1)
	}

	if *listFlag {
		printTaskList()
		return
	}

	// Determine which tasks to run
	var tasks []*task
	if *taskFlag == "all" || *taskFlag == "" {
		for i := range allTasks {
			tasks = append(tasks, &allTasks[i])
		}
	} else {
		t := taskByName(*taskFlag)
		if t == nil {
			fmt.Fprintf(os.Stderr, "Unknown task: %q\nAvailable: ", *taskFlag)
			for _, t := range allTasks {
				fmt.Fprintf(os.Stderr, "%s ", t.Name)
			}
			fmt.Fprintln(os.Stderr)
			os.Exit(1)
		}
		tasks = append(tasks, t)
	}

	ctx := context.Background()
	gemini := newGeminiClient(apiKey)

	// Use Gemini embeddings for real semantic similarity
	embedder := newGeminiEmbedder(apiKey, 768)

	fmt.Println("╔═══════════════════════════════════════════════════════╗")
	fmt.Println("║  DualMem Benchmark — LLM-as-Judge                   ║")
	fmt.Println("║  No Memory vs Flat Search vs Full DualMem            ║")
	fmt.Println("╚═══════════════════════════════════════════════════════╝")
	fmt.Println()

	allScores := make(map[string]map[modeName]scores)

	for ti, t := range tasks {
		fmt.Printf("[Task %d/%d] %s\n", ti+1, len(tasks), t.Title)

		// Mode 1: No memory
		fmt.Println("  Running no-memory mode...")
		noneResp, err := runNone(ctx, gemini, t)
		if err != nil {
			log.Printf("  FAILED: %v", err)
			continue
		}

		// Mode 2: Flat search
		fmt.Println("  Running flat-search mode...")
		flatResp, err := runFlat(ctx, gemini, t, embedder)
		if err != nil {
			log.Printf("  FAILED: %v", err)
			continue
		}

		// Mode 3: Full DualMem
		fmt.Println("  Running dualmem mode...")
		dualResp, err := runDualMem(ctx, gemini, t, embedder)
		if err != nil {
			log.Printf("  FAILED: %v", err)
			continue
		}

		responses := map[modeName]string{
			modeNone:    noneResp,
			modeFlat:    flatResp,
			modeDualMem: dualResp,
		}

		// Judge
		fmt.Println("  Running LLM-as-judge...")
		judgeResults, err := runJudge(ctx, gemini, t, noneResp, flatResp, dualResp)
		if err != nil {
			log.Printf("  Judge failed: %v", err)
		}

		printReport(t, responses, judgeResults)

		// Save per-task results
		resultsPath := filepath.Join("examples", "dualmem-bench", fmt.Sprintf("results_%s.md", t.Name))
		if err := writeResultsFile(resultsPath, t, responses, judgeResults); err != nil {
			log.Printf("  Failed to write results: %v", err)
		}

		// Collect for aggregate
		if len(judgeResults) > 0 {
			modeMap := map[string]modeName{"A": modeNone, "B": modeFlat, "C": modeDualMem}
			taskScores := make(map[modeName]scores)
			for _, js := range judgeResults {
				if name, ok := modeMap[js.Mode]; ok {
					taskScores[name] = js.Scores
				}
			}
			allScores[t.Name] = taskScores
		}
	}

	// Aggregate results if multiple tasks
	if len(allScores) > 1 {
		printAggregate(allScores)
	}
}

// --- Gemini Embedder ---

type geminiEmbedder struct {
	apiKey string
	dim    int
	model  string
	client *http.Client
}

func newGeminiEmbedder(apiKey string, dim int) *geminiEmbedder {
	return &geminiEmbedder{
		apiKey: apiKey,
		dim:    dim,
		model:  "gemini-embedding-2-preview",
		client: &http.Client{Timeout: 15 * time.Second},
	}
}

func (g *geminiEmbedder) Embed(ctx context.Context, text string, taskType string) ([]float32, error) {
	url := "https://generativelanguage.googleapis.com/v1beta/models/" +
		g.model + ":embedContent?key=" + g.apiKey

	reqBody := map[string]any{
		"model": "models/" + g.model,
		"content": map[string]any{
			"parts": []map[string]any{{"text": text}},
		},
		"taskType":        taskType,
		"outputDimensionality": g.dim,
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	rateLimitDelay()
	resp, err := g.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		limit := len(body)
		if limit > 300 {
			limit = 300
		}
		return nil, fmt.Errorf("embed %d: %s", resp.StatusCode, string(body[:limit]))
	}

	var embResp struct {
		Embedding struct {
			Values []float32 `json:"values"`
		} `json:"embedding"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&embResp); err != nil {
		return nil, err
	}

	return embResp.Embedding.Values, nil
}

func (g *geminiEmbedder) Dimension() int     { return g.dim }
func (g *geminiEmbedder) ModelName() string   { return g.model }

// suppress unused import warning
var _ = sort.Slice
