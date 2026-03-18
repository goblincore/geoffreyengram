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

// GeminiClassifier uses Gemini Flash Lite for accurate sector classification.
// Synchronous — adds ~100-200ms to the Add path. Suitable for CLI usage
// where the user is waiting anyway. For NPC/real-time usage, use a heuristic
// classifier or the async LLMClassifier from the engram package.
type GeminiClassifier struct {
	apiKey string
	client *http.Client
}

// NewGeminiClassifier creates a synchronous LLM-based sector classifier.
func NewGeminiClassifier(apiKey string) *GeminiClassifier {
	return &GeminiClassifier{
		apiKey: apiKey,
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

// Classify calls Gemini Flash Lite to classify content into a cognitive sector.
// Returns "semantic" as fallback on any error.
func (c *GeminiClassifier) Classify(content string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	sector, err := c.llmClassify(ctx, content)
	if err != nil {
		return "semantic" // safe default
	}
	return sector
}

func (c *GeminiClassifier) llmClassify(ctx context.Context, content string) (string, error) {
	url := "https://generativelanguage.googleapis.com/v1beta/models/gemini-2.5-flash-lite:generateContent?key=" + c.apiKey

	prompt := `Classify this memory into exactly one cognitive sector. Reply with ONLY the sector name, nothing else.

Sectors:
- episodic: specific events, experiences, things that happened at a particular time
- semantic: facts, knowledge, preferences, stable truths about someone or something
- procedural: skills, techniques, how-to knowledge, learned methods, processes
- emotional: feelings, sentiments, emotional reactions, moods
- reflective: patterns, meta-observations, insights connecting multiple experiences

Memory: "` + content + `"`

	reqBody := map[string]any{
		"contents": []map[string]any{
			{"role": "user", "parts": []map[string]any{{"text": prompt}}},
		},
		"generationConfig": map[string]any{
			"maxOutputTokens": 10,
			"temperature":     0.0,
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

	resp, err := c.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("gemini classify %d: %s", resp.StatusCode, string(body[:min(len(body), 200)]))
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
		return "", err
	}

	if len(geminiResp.Candidates) == 0 || len(geminiResp.Candidates[0].Content.Parts) == 0 {
		return "", fmt.Errorf("empty response")
	}

	text := strings.TrimSpace(strings.ToLower(geminiResp.Candidates[0].Content.Parts[0].Text))
	switch {
	case strings.Contains(text, "episodic"):
		return "episodic", nil
	case strings.Contains(text, "semantic"):
		return "semantic", nil
	case strings.Contains(text, "procedural"):
		return "procedural", nil
	case strings.Contains(text, "emotional"):
		return "emotional", nil
	case strings.Contains(text, "reflective"):
		return "reflective", nil
	default:
		return "semantic", nil
	}
}
