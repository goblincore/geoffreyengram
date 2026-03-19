// examples/chat is a local REPL that demonstrates geoffreyengram with Ollama.
//
// It uses Pattern A (server-driven): your code controls when memory is
// read and written. The LLM never needs to know about tools — it just sees
// retrieved memories as context in the prompt.
//
// Requirements:
//
//	ollama pull nomic-embed-text   # embedding model
//	ollama pull <your-chat-model>  # e.g. hadad/LFM2.5-1.2B:Q4_K_M, llama3, mistral
//
// Usage:
//
//	go run ./examples/chat/ --chat-model hadad/LFM2.5-1.2B:Q4_K_M
//	go run ./examples/chat/ --chat-model mistral --character "Sifu Chen"
//	go run ./examples/chat/ --prompt ./my-character.txt --chat-model llama3
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	engram "github.com/goblincore/geoffreyengram"
)

// --- Ollama chat client ---

type ollamaChat struct {
	host   string
	model  string
	client *http.Client
}

type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	Stream   bool          `json:"stream"`
	Options  chatOptions   `json:"options,omitempty"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatOptions struct {
	Temperature float64 `json:"temperature,omitempty"`
	NumPredict  int     `json:"num_predict,omitempty"`
}

type chatResponse struct {
	Message chatMessage `json:"message"`
}

func (o *ollamaChat) generate(ctx context.Context, systemPrompt, userMessage string) (string, error) {
	reqBody := chatRequest{
		Model: o.model,
		Messages: []chatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userMessage},
		},
		Stream: false,
		Options: chatOptions{
			Temperature: 0.7,
			NumPredict:  256,
		},
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", o.host+"/api/chat", bytes.NewBuffer(jsonData))
	if err != nil {
		return "", fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := o.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("ollama chat %d: %s", resp.StatusCode, string(body[:min(len(body), 300)]))
	}

	var chatResp chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
		return "", fmt.Errorf("decode: %w", err)
	}

	return strings.TrimSpace(chatResp.Message.Content), nil
}

// --- Prompt building ---

// buildSystemPrompt creates the system message: character description + recalled memories.
func buildSystemPrompt(characterPrompt, playerName string, memories []string) string {
	var sb strings.Builder
	sb.WriteString(characterPrompt)

	if len(memories) > 0 {
		sb.WriteString("\n\nHere's what you remember about ")
		sb.WriteString(playerName)
		sb.WriteString(" from past conversations:\n")
		for _, m := range memories {
			sb.WriteString("- ")
			sb.WriteString(m)
			sb.WriteString("\n")
		}
		sb.WriteString("\nUse these memories naturally in your response when relevant. Don't list them — weave them into conversation.")
	}

	return sb.String()
}

const defaultCharacterPrompt = `You are Lily, a warm bartender at Club Mutant, a neon-lit jazz bar.
You remember your regulars, notice their moods, and make personalized drink suggestions.
Keep responses short (1-3 sentences). Be warm and observant.`

// dim prints text in gray (ANSI dim).
func dim(s string) string {
	return "\033[2m" + s + "\033[0m"
}

// --- Main ---

func main() {
	// CLI flags
	chatModel := flag.String("chat-model", "", "Ollama chat model name (required, e.g. hadad/LFM2.5-1.2B:Q4_K_M, llama3, mistral)")
	embedModel := flag.String("embed-model", "nomic-embed-text", "Ollama embedding model name")
	embedDim := flag.Int("embed-dim", 768, "Embedding dimension")
	host := flag.String("host", "http://localhost:11434", "Ollama server URL")
	dbPath := flag.String("db", "./data/chat.db", "SQLite database path")
	characterName := flag.String("character", "Lily", "Character name")
	playerName := flag.String("user", "Player", "Your name (used in prompts and memory)")
	promptFile := flag.String("prompt", "", "Path to character prompt file (default: built-in Lily)")
	verbose := flag.Bool("v", false, "Verbose mode: show the full prompt sent to the model")
	flag.Parse()

	if *chatModel == "" {
		fmt.Fprintln(os.Stderr, "error: --chat-model is required")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "List your available models with: ollama list")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Examples:")
		fmt.Fprintln(os.Stderr, "  go run ./examples/chat/ --chat-model hadad/LFM2.5-1.2B:Q4_K_M")
		fmt.Fprintln(os.Stderr, "  go run ./examples/chat/ --chat-model llama3")
		fmt.Fprintln(os.Stderr, "  go run ./examples/chat/ --chat-model mistral")
		os.Exit(1)
	}

	// Suppress engram's internal log output (it's noisy for a REPL)
	log.SetOutput(io.Discard)

	// Resolve character prompt
	characterPrompt := defaultCharacterPrompt
	if *promptFile != "" {
		data, err := os.ReadFile(*promptFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error reading prompt file: %v\n", err)
			os.Exit(1)
		}
		characterPrompt = strings.TrimSpace(string(data))
	}

	// User ID: "character:player" lowercase
	userID := strings.ToLower(*characterName) + ":" + strings.ToLower(*playerName)

	// Init engram (fully local — no API keys)
	em, err := engram.Init(engram.Config{
		DBPath:            *dbPath,
		EmbeddingProvider: engram.NewOllamaEmbedder(*embedModel, *embedDim, engram.WithOllamaHost(*host)),
		DecayInterval:     12 * time.Hour,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "engram init: %v\n", err)
		os.Exit(1)
	}
	defer em.Close()

	// Init Ollama chat client
	chat := &ollamaChat{
		host:   *host,
		model:  *chatModel,
		client: &http.Client{Timeout: 120 * time.Second},
	}

	// Session management
	sessionID := fmt.Sprintf("chat-%d", time.Now().Unix())
	var parentID int64
	ctx := context.Background()

	// Check for previous sessions
	lastSession, _ := em.GetLastSession(userID)
	memCount := len(lastSession)

	// Count total memories
	allMemories, _ := em.ListRecent(userID, 1000, nil)

	fmt.Println("╔══════════════════════════════════════════════╗")
	fmt.Printf("║  engram chat — %s\n", *characterName)
	fmt.Printf("║  model: %s\n", *chatModel)
	fmt.Printf("║  embeddings: %s (%d-dim)\n", *embedModel, *embedDim)
	if len(allMemories) > 0 {
		fmt.Printf("║  %d memories loaded", len(allMemories))
		if memCount > 0 {
			fmt.Printf(" (last session: %d turns)", memCount)
		}
		fmt.Println()
	}
	fmt.Println("╠══════════════════════════════════════════════╣")
	fmt.Println("║  /memories  — show what the character remembers")
	fmt.Println("║  /recall    — show what was recalled last turn")
	fmt.Println("║  /session   — show current session info")
	fmt.Println("║  /new       — start a new session")
	fmt.Println("║  /quit      — exit")
	fmt.Println("╚══════════════════════════════════════════════╝")
	fmt.Println()

	scanner := bufio.NewScanner(os.Stdin)

	// Track last recalled memories for /recall command
	var lastRecalled []engram.SearchResult

	for {
		fmt.Printf("%s> ", *playerName)
		if !scanner.Scan() {
			break
		}
		input := strings.TrimSpace(scanner.Text())
		if input == "" {
			continue
		}

		// Handle commands
		switch strings.ToLower(input) {
		case "/quit", "/exit":
			fmt.Println("Goodbye!")
			return

		case "/memories":
			memories, err := em.ListRecent(userID, 15, nil)
			if err != nil {
				fmt.Printf("  error: %v\n", err)
				continue
			}
			if len(memories) == 0 {
				fmt.Println("  (no memories yet)")
				continue
			}
			fmt.Println()
			for _, m := range memories {
				age := time.Since(m.CreatedAt).Round(time.Second)
				fmt.Printf("  [%s] sal=%.2f decay=%.2f  %v ago\n", m.Sector, m.Salience, m.DecayScore, age)
				fmt.Printf("    %s\n", m.Summary)
			}
			fmt.Println()
			continue

		case "/recall":
			if len(lastRecalled) == 0 {
				fmt.Println("  (no memories were recalled last turn)")
				continue
			}
			fmt.Println()
			fmt.Println("  Memories recalled last turn:")
			for i, r := range lastRecalled {
				fmt.Printf("  %d. [%s] score=%.3f sim=%.3f sal=%.2f\n", i+1, r.Sector, r.CompositeScore, r.Similarity, r.Salience)
				fmt.Printf("     %s\n", r.Summary)
			}
			fmt.Println()
			continue

		case "/session":
			fmt.Printf("  session: %s\n", sessionID)
			fmt.Printf("  user_id: %s\n", userID)
			fmt.Printf("  parent_id: %d\n", parentID)
			fmt.Println()
			continue

		case "/new":
			sessionID = fmt.Sprintf("chat-%d", time.Now().Unix())
			parentID = 0
			fmt.Printf("  new session: %s\n\n", sessionID)
			continue
		}

		// 1. Search for relevant memories
		weights := engram.DefaultSectorWeights()
		results := em.Search(input, userID, 5, weights)
		lastRecalled = results

		var memories []string
		for _, r := range results {
			memories = append(memories, r.Summary)
		}

		// Show recall indicator
		if len(results) > 0 {
			fmt.Printf("%s", dim(fmt.Sprintf("  [recalled %d memories]\n", len(results))))
		}

		// 2. Build system prompt with memory context, user message is separate
		systemPrompt := buildSystemPrompt(characterPrompt, *playerName, memories)

		if *verbose {
			fmt.Println(dim("  --- system prompt ---"))
			for _, line := range strings.Split(systemPrompt, "\n") {
				fmt.Println(dim("  " + line))
			}
			fmt.Println(dim("  --- end prompt ---"))
		}

		// 3. Call Ollama (system + user as separate roles)
		resp, err := chat.generate(ctx, systemPrompt, input)
		if err != nil {
			fmt.Printf("  error: %v\n\n", err)
			continue
		}

		// 4. Store the exchange in engram
		memID, _ := em.AddWithOptions(engram.AddOptions{
			UserID:           userID,
			UserMessage:      input,
			AssistantMessage: resp,
			SessionID:        sessionID,
			ParentID:         parentID,
		})
		parentID = memID

		// 5. Print response
		fmt.Printf("%s: %s\n\n", *characterName, resp)
	}
}
