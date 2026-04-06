package main

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// judgeReport scores a single report using Claude Opus via the claude CLI.
func judgeReport(q Query, report string) *JudgeScores {
	if report == "" {
		return &JudgeScores{}
	}

	prompt := fmt.Sprintf(`You are evaluating a codebase understanding report. Score it on 4 dimensions (1-5 each).

QUERY: %s

GROUND TRUTH (reference answer):
Files: %s
Concepts: %s
Explanation: %s

REPORT TO EVALUATE:
%s

Score each dimension 1-5:
- Accuracy: Are stated facts correct?
- Completeness: Does it cover the relevant files and concepts from ground truth?
- Relevance: Is noise minimized? Good signal-to-noise ratio?
- Actionability: Could a developer act on this immediately?

Respond ONLY with JSON (no markdown):
{"accuracy": N, "completeness": N, "relevance": N, "actionability": N, "rationale": "brief explanation"}`,
		q.Query,
		strings.Join(q.GroundTruth.Files, ", "),
		strings.Join(q.GroundTruth.Concepts, ", "),
		q.GroundTruth.Explanation,
		truncateReport(report, 3000))

	out := runClaude(prompt)
	if out == "" {
		return &JudgeScores{}
	}

	var scores JudgeScores
	// Extract JSON from response (claude may wrap it in text)
	jsonStr := extractJSON(out)
	if err := json.Unmarshal([]byte(jsonStr), &scores); err != nil {
		fmt.Printf("  WARNING: judge parse error: %v\nRaw: %s\n", err, out[:min(200, len(out))])
		return &JudgeScores{}
	}
	return &scores
}

// judgeHeadToHead compares two reports anonymously using Claude Opus.
func judgeHeadToHead(q Query, reportA, reportB string) *HeadToHead {
	if reportA == "" && reportB == "" {
		return &HeadToHead{Winner: "tie", Rationale: "both reports empty"}
	}

	// Shuffle A/B to avoid position bias (deterministic per query ID)
	aFirst := len(q.ID)%2 == 0
	var first, second, firstLabel, secondLabel string
	if aFirst {
		first, second = reportA, reportB
		firstLabel, secondLabel = "dualmem", "unravel"
	} else {
		first, second = reportB, reportA
		firstLabel, secondLabel = "unravel", "dualmem"
	}

	prompt := fmt.Sprintf(`You are comparing two codebase understanding reports for the same query. Pick which is more useful for a developer.

QUERY: %s

GROUND TRUTH:
Files: %s
Explanation: %s

REPORT A:
%s

REPORT B:
%s

Which report is more useful for answering the query? Consider accuracy, completeness, relevance, and actionability.

Respond ONLY with JSON (no markdown):
{"winner": "A" or "B" or "tie", "rationale": "brief explanation"}`,
		q.Query,
		strings.Join(q.GroundTruth.Files, ", "),
		q.GroundTruth.Explanation,
		truncateReport(first, 2000),
		truncateReport(second, 2000))

	out := runClaude(prompt)
	if out == "" {
		return &HeadToHead{Winner: "tie", Rationale: "judge failed"}
	}

	var result struct {
		Winner    string `json:"winner"`
		Rationale string `json:"rationale"`
	}
	jsonStr := extractJSON(out)
	if err := json.Unmarshal([]byte(jsonStr), &result); err != nil {
		return &HeadToHead{Winner: "tie", Rationale: "parse error"}
	}

	// Map A/B back to system names
	winner := "tie"
	switch strings.ToUpper(result.Winner) {
	case "A":
		winner = firstLabel
	case "B":
		winner = secondLabel
	}

	return &HeadToHead{Winner: winner, Rationale: result.Rationale}
}

// runClaude executes a prompt via the claude CLI and returns the response.
func runClaude(prompt string) string {
	cmd := exec.Command("claude", "-p", prompt, "--model", "claude-opus-4-6")
	out, err := cmd.Output()
	if err != nil {
		stderr := ""
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = string(ee.Stderr)
		}
		fmt.Printf("  WARNING: claude CLI failed: %v %s\n", err, stderr)
		return ""
	}
	return strings.TrimSpace(string(out))
}

// truncateReport truncates a report to maxChars.
func truncateReport(report string, maxChars int) string {
	if len(report) <= maxChars {
		return report
	}
	return report[:maxChars] + "\n... (truncated)"
}

// extractJSON finds the first JSON object in a string.
func extractJSON(s string) string {
	start := strings.Index(s, "{")
	if start < 0 {
		return s
	}
	// Find matching closing brace
	depth := 0
	for i := start; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return s[start:]
}

// minInt returns the smaller of two ints.
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// parseScore extracts a numeric score from a string like "4" or "4.5".
func parseScore(s string) float64 {
	s = strings.TrimSpace(s)
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return v
}
