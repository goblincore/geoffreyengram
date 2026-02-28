package engram

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

// GeminiReflector generates reflections using the Gemini API.
// Implements ReflectionProvider.
type GeminiReflector struct {
	apiKey string
	model  string
	client *http.Client
}

// NewGeminiReflector creates a reflection provider using Gemini.
func NewGeminiReflector(apiKey string) *GeminiReflector {
	return &GeminiReflector{
		apiKey: apiKey,
		model:  "gemini-2.5-flash-lite",
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

// Reflect analyzes recent memories and generates reflective observations.
func (r *GeminiReflector) Reflect(ctx context.Context, memories []Memory, characterContext string) ([]Reflection, error) {
	if r.apiKey == "" {
		return nil, fmt.Errorf("no API key for reflection")
	}

	prompt := buildReflectionPrompt(memories, characterContext)

	url := "https://generativelanguage.googleapis.com/v1beta/models/" + r.model + ":generateContent?key=" + r.apiKey

	reqBody := map[string]any{
		"contents": []map[string]any{
			{"role": "user", "parts": []map[string]any{{"text": prompt}}},
		},
		"generationConfig": map[string]any{
			"maxOutputTokens": 1024,
			"temperature":     0.7,
			"responseMimeType": "application/json",
		},
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("gemini reflect %d: %s", resp.StatusCode, string(body[:min(len(body), 300)]))
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
		return nil, fmt.Errorf("decode: %w", err)
	}

	if len(geminiResp.Candidates) == 0 || len(geminiResp.Candidates[0].Content.Parts) == 0 {
		return nil, fmt.Errorf("empty response")
	}

	text := strings.TrimSpace(geminiResp.Candidates[0].Content.Parts[0].Text)
	return parseReflections(text)
}

// buildReflectionPrompt formats memories into a prompt for the LLM.
func buildReflectionPrompt(memories []Memory, characterContext string) string {
	var b strings.Builder

	b.WriteString("You are analyzing memories stored by an AI character to find patterns and form observations.\n\n")

	if characterContext != "" {
		b.WriteString("Character context: ")
		b.WriteString(characterContext)
		b.WriteString("\n\n")
	}

	b.WriteString("Here are recent memories (newest first):\n\n")
	for i, m := range memories {
		fmt.Fprintf(&b, "%d. [%s] (%s) %q\n",
			i+1,
			m.CreatedAt.Format("2006-01-02"),
			m.Sector,
			m.Summary,
		)
	}

	b.WriteString(`
Based on these memories, identify 1-3 meaningful patterns, connections, or observations
the character would naturally notice. Each observation should be something that would
make the character feel more real — like noticing someone always mentions music when
they're feeling down, or connecting two seemingly unrelated things the person said.

Respond with a JSON array:
[{"content": "observation text", "salience": 0.7, "entities": [{"text": "entity", "type": "topic"}]}]

Only include genuinely insightful observations. If there are no clear patterns, return [].
`)

	return b.String()
}

// parseReflections parses the JSON response into Reflection structs.
func parseReflections(text string) ([]Reflection, error) {
	// Try to extract JSON array from the response
	text = strings.TrimSpace(text)

	// Handle possible markdown code blocks
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

	type rawReflection struct {
		Content  string `json:"content"`
		Salience float64 `json:"salience"`
		Entities []struct {
			Text string `json:"text"`
			Type string `json:"type"`
		} `json:"entities"`
	}

	var raw []rawReflection
	if err := json.Unmarshal([]byte(text), &raw); err != nil {
		return nil, fmt.Errorf("parse reflections: %w", err)
	}

	var reflections []Reflection
	for _, r := range raw {
		if r.Content == "" {
			continue
		}
		ref := Reflection{
			Content:  r.Content,
			Salience: r.Salience,
		}
		for _, e := range r.Entities {
			if e.Text != "" {
				ref.Entities = append(ref.Entities, Entity{Text: e.Text, Type: e.Type})
			}
		}
		reflections = append(reflections, ref)
	}

	return reflections, nil
}
