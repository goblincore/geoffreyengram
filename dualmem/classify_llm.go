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
	apiKey  string
	client  *http.Client
	sectors *SectorConfig
}

// NewGeminiClassifier creates a synchronous LLM-based sector classifier.
// The sectors config determines which sectors are available and their descriptions.
func NewGeminiClassifier(apiKey string, sectors *SectorConfig) *GeminiClassifier {
	return &GeminiClassifier{
		apiKey:  apiKey,
		client:  &http.Client{Timeout: 10 * time.Second},
		sectors: sectors,
	}
}

// Classify calls Gemini Flash Lite to classify content into a cognitive sector.
// Returns the default sector as fallback on any error.
func (c *GeminiClassifier) Classify(content string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	sector, err := c.llmClassify(ctx, content)
	if err != nil {
		return c.sectors.Default
	}
	return sector
}

func (c *GeminiClassifier) llmClassify(ctx context.Context, content string) (string, error) {
	url := "https://generativelanguage.googleapis.com/v1beta/models/gemini-2.5-flash-lite:generateContent"

	// Build sector list dynamically from config
	var sectorLines []string
	for name, desc := range c.sectors.Anchors {
		sectorLines = append(sectorLines, fmt.Sprintf("- %s: %s", name, desc))
	}

	prompt := "Classify this memory into exactly one sector. Reply with ONLY the sector name, nothing else.\n\nSectors:\n" +
		strings.Join(sectorLines, "\n") +
		"\n\nMemory: \"" + content + "\""

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
	req.Header.Set("x-goog-api-key", c.apiKey)

	resp, err := c.client.Do(req)
	if err != nil {
		return "", newProviderUnavailableError("gemini", "classify", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		bodyText := redactCredential(string(body), c.apiKey)
		return "", fmt.Errorf("gemini classify %d: %s", resp.StatusCode, bodyText[:min(len(bodyText), 200)])
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

	// Match against configured sector names
	for name := range c.sectors.Anchors {
		if strings.Contains(text, strings.ToLower(name)) {
			return name, nil
		}
	}
	return c.sectors.Default, nil
}
