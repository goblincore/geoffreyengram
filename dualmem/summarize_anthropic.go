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

// AnthropicSummarizer uses an Anthropic-compatible API for text generation.
// Implements TextGenerator only (not SummarizerProvider — too expensive for
// sketch compression). Intended for high-stakes synthesis in Consult/Synthesize.
type AnthropicSummarizer struct {
	apiKey  string
	baseURL string
	model   string
	client  *http.Client
}

// NewAnthropicSummarizer creates a text generator using the Anthropic messages API.
// Defaults: model="glm-5.1", baseURL="https://api.z.ai/api/anthropic".
func NewAnthropicSummarizer(apiKey, baseURL, model string) *AnthropicSummarizer {
	if model == "" {
		model = "glm-5.1"
	}
	if baseURL == "" {
		baseURL = "https://api.z.ai/api/anthropic"
	}
	// Ensure no trailing slash
	baseURL = strings.TrimRight(baseURL, "/")
	return &AnthropicSummarizer{
		apiKey:  apiKey,
		baseURL: baseURL,
		model:   model,
		client:  &http.Client{Timeout: 90 * time.Second},
	}
}

// GenerateText sends a prompt via the Anthropic messages API and returns the response.
func (s *AnthropicSummarizer) GenerateText(ctx context.Context, prompt string, maxTokens int) (string, error) {
	if maxTokens <= 0 {
		maxTokens = 500
	}

	reqBody := map[string]any{
		"model":      s.model,
		"max_tokens": maxTokens,
		"messages": []map[string]any{
			{"role": "user", "content": prompt},
		},
		"temperature": 0.3,
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	url := s.baseURL + "/v1/messages"
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", s.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := s.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("dualmem: anthropic generate: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("dualmem: anthropic generate %d: %s", resp.StatusCode, string(body[:min(len(body), 300)]))
	}

	var anthropicResp struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&anthropicResp); err != nil {
		return "", fmt.Errorf("dualmem: decode anthropic response: %w", err)
	}

	if len(anthropicResp.Content) == 0 {
		return "", fmt.Errorf("dualmem: empty anthropic response")
	}

	// Concatenate all text blocks
	var parts []string
	for _, block := range anthropicResp.Content {
		if block.Type == "text" && block.Text != "" {
			parts = append(parts, block.Text)
		}
	}
	if len(parts) == 0 {
		return "", fmt.Errorf("dualmem: no text in anthropic response")
	}

	return strings.TrimSpace(strings.Join(parts, "\n")), nil
}

// Model returns the model name for display/logging.
func (s *AnthropicSummarizer) Model() string {
	return s.model
}
