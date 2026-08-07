package dualmem

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// GeminiSummarizer uses Gemini Flash for the compression pipeline:
// episode summarization, arc summarization, and profile updates.
// Implements dualmem.SummarizerProvider.
type GeminiSummarizer struct {
	apiKey string
	model  string
	client *http.Client
}

// NewGeminiSummarizer creates a summarizer using Gemini Flash.
// Model defaults to "gemini-2.5-flash-lite" if empty.
func NewGeminiSummarizer(apiKey, model string) *GeminiSummarizer {
	if model == "" {
		model = "gemini-2.5-flash-lite"
	}
	return &GeminiSummarizer{
		apiKey: apiKey,
		model:  model,
		client: &http.Client{Timeout: 90 * time.Second},
	}
}

func (s *GeminiSummarizer) SummarizeEpisode(ctx context.Context, messages []string) (string, error) {
	prompt := `Summarize this batch of conversation messages into a single episode summary.
Focus on: what was discussed, any names/entities mentioned, the emotional tone, and key facts or preferences revealed.
Keep it under 200 tokens. Write in third person past tense. Be specific — preserve names, numbers, and concrete details.

Messages:
` + strings.Join(messages, "\n---\n")

	return s.generate(ctx, prompt, 300)
}

func (s *GeminiSummarizer) SummarizeArc(ctx context.Context, episodes []string) (string, error) {
	prompt := `These episode summaries describe interactions with the same person over time. Synthesize them into a single narrative arc — a thematic summary of what this period was about.
Focus on: recurring themes, relationship evolution, key events, and patterns.
Keep it under 100 tokens. Write in present tense ("the user is interested in...").

Episodes:
` + strings.Join(episodes, "\n---\n")

	return s.generate(ctx, prompt, 200)
}

func (s *GeminiSummarizer) UpdateProfile(ctx context.Context, current *ProfileSketch, newArcs []string) (*ProfileSketch, error) {
	var currentJSON string
	if current != nil {
		b, _ := json.Marshal(current)
		currentJSON = string(b)
	} else {
		currentJSON = "null (no existing profile — create from scratch)"
	}

	prompt := `You are updating a user profile sketch based on new narrative arcs from recent interactions.
Merge the new information with the existing profile. Keep what's still relevant, update what's changed, add new insights.

Current profile:
` + currentJSON + `

New narrative arcs:
` + strings.Join(newArcs, "\n---\n") + `

Respond with ONLY valid JSON matching this exact structure (no markdown, no explanation):
{
  "interests": ["list of interests/hobbies"],
  "personality_traits": ["observable traits"],
  "relationship_history": "brief narrative of how interactions have evolved",
  "key_preferences": ["specific preferences expressed"],
  "communication_style": "how they communicate"
}`

	raw, err := s.generate(ctx, prompt, 500)
	if err != nil {
		return nil, err
	}

	// Strip markdown code fences if present
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)

	var profile ProfileSketch
	if err := json.Unmarshal([]byte(raw), &profile); err != nil {
		return nil, fmt.Errorf("dualmem: parse profile JSON: %w (raw: %s)", err, raw[:min(len(raw), 200)])
	}
	return &profile, nil
}

// GenerateText sends a raw prompt to Gemini and returns the response text.
// Used by SeedMemories for cluster description generation.
func (s *GeminiSummarizer) GenerateText(ctx context.Context, prompt string, maxTokens int) (string, error) {
	return s.generate(ctx, prompt, maxTokens)
}

func (s *GeminiSummarizer) generate(ctx context.Context, prompt string, maxTokens int) (string, error) {
	url := "https://generativelanguage.googleapis.com/v1beta/models/" + s.model + ":generateContent"

	reqBody := map[string]any{
		"contents": []map[string]any{
			{"role": "user", "parts": []map[string]any{{"text": prompt}}},
		},
		"generationConfig": map[string]any{
			"maxOutputTokens": maxTokens,
			"temperature":     0.3,
		},
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", s.apiKey)

	resp, err := s.client.Do(req)
	if err != nil {
		return "", newProviderUnavailableError("gemini", "summarize", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		bodyText := redactCredential(string(body), s.apiKey)
		return "", fmt.Errorf("dualmem: gemini summarize %d: %s", resp.StatusCode, bodyText[:min(len(bodyText), 200)])
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
		return "", fmt.Errorf("dualmem: decode: %w", err)
	}

	if len(geminiResp.Candidates) == 0 || len(geminiResp.Candidates[0].Content.Parts) == 0 {
		return "", fmt.Errorf("dualmem: empty summarize response")
	}

	// Concatenate all text parts (Gemini may split long responses).
	var parts []string
	for _, part := range geminiResp.Candidates[0].Content.Parts {
		if part.Text != "" {
			parts = append(parts, part.Text)
		}
	}
	if len(parts) == 0 {
		return "", fmt.Errorf("dualmem: no text in gemini response")
	}

	return strings.TrimSpace(strings.Join(parts, "")), nil
}
