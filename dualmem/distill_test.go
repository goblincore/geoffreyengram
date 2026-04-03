package dualmem

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseTranscript_JSONL(t *testing.T) {
	input := strings.Join([]string{
		`{"role":"user","content":"Add dark mode support to the settings page"}`,
		`{"role":"assistant","content":"I'll add dark mode support. Let me start by creating the theme toggle component."}`,
		`{"role":"tool","content":"Reading file settings.go..."}`,
		`{"role":"assistant","type":"tool_use","content":"cat settings.go"}`,
		`{"role":"assistant","type":"tool_result","content":"package settings\n..."}`,
		`{"role":"user","content":"short"}`,
		`{"role":"user","content":"Looks good, but use a CSS variable approach instead of inline styles"}`,
	}, "\n")

	result := parseTranscript([]byte(input))

	// Should contain user and assistant messages
	if !strings.Contains(result, "User: Add dark mode support") {
		t.Errorf("expected user message, got: %s", result)
	}
	if !strings.Contains(result, "Assistant: I'll add dark mode support") {
		t.Errorf("expected assistant message, got: %s", result)
	}

	// Tool messages should be filtered out
	if strings.Contains(result, "Reading file settings.go") {
		t.Error("tool role message should be filtered out")
	}
	if strings.Contains(result, "cat settings.go") {
		t.Error("tool_use message should be filtered out")
	}
	if strings.Contains(result, "package settings") {
		t.Error("tool_result message should be filtered out")
	}

	// Short message (< 10 chars) should be skipped
	if strings.Contains(result, "User: short") {
		t.Error("short message should be skipped")
	}

	// The CSS variable message should be included
	if !strings.Contains(result, "CSS variable approach") {
		t.Errorf("expected second user message, got: %s", result)
	}
}

func TestParseTranscript_PlainText(t *testing.T) {
	input := "This is a plain text transcript with no JSON.\nIt should pass through unchanged."
	result := parseTranscript([]byte(input))

	if result != input {
		t.Errorf("plain text should pass through, got: %q", result)
	}
}

func TestParseTranscript_Truncation(t *testing.T) {
	// Build a JSONL transcript larger than 12000 chars
	var lines []string
	for i := 0; i < 200; i++ {
		lines = append(lines, `{"role":"user","content":"This is a fairly long message to inflate the transcript size beyond the truncation threshold number `+
			strings.Repeat("x", 50)+`"}`)
	}
	input := strings.Join(lines, "\n")

	result := parseTranscript([]byte(input))
	if len(result) > 12000 {
		t.Errorf("expected truncation to ~12000 chars, got %d", len(result))
	}

	// Also test plain text truncation
	longPlain := strings.Repeat("A", 15000)
	plainResult := parseTranscript([]byte(longPlain))
	if len(plainResult) > 12000 {
		t.Errorf("expected plain text truncation to 12000, got %d", len(plainResult))
	}
}

func TestParseDistillResponse_Valid(t *testing.T) {
	input := `{
		"facts": [
			{
				"text": "Chose SQLite over Postgres for zero-setup CLI",
				"type": "decision",
				"salience": 0.9,
				"files": ["store_sqlite.go"],
				"entities": [{"text": "SQLite", "type": "tool"}]
			},
			{
				"text": "Auth middleware is in nakama RPCs, not middleware/",
				"type": "map",
				"salience": 0.7,
				"files": ["nakama/rpc.go"]
			}
		],
		"entity_triples": [
			{
				"source": {"text": "dualmem", "type": "project"},
				"relation": "uses",
				"target": {"text": "SQLite", "type": "tool"}
			}
		],
		"session_summary": "Refactored storage layer to use SQLite"
	}`

	result, err := parseDistillResponse(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.Facts) != 2 {
		t.Fatalf("expected 2 facts, got %d", len(result.Facts))
	}
	if result.Facts[0].Type != "decision" {
		t.Errorf("expected type decision, got %s", result.Facts[0].Type)
	}
	if result.Facts[0].Salience != 0.9 {
		t.Errorf("expected salience 0.9, got %f", result.Facts[0].Salience)
	}
	if len(result.Facts[0].Files) != 1 || result.Facts[0].Files[0] != "store_sqlite.go" {
		t.Errorf("expected files [store_sqlite.go], got %v", result.Facts[0].Files)
	}
	if len(result.EntityTriples) != 1 {
		t.Fatalf("expected 1 triple, got %d", len(result.EntityTriples))
	}
	if result.EntityTriples[0].Relation != "uses" {
		t.Errorf("expected relation 'uses', got %s", result.EntityTriples[0].Relation)
	}
	if result.SessionSummary != "Refactored storage layer to use SQLite" {
		t.Errorf("unexpected summary: %s", result.SessionSummary)
	}
}

func TestParseDistillResponse_WithCodeFences(t *testing.T) {
	input := "```json\n" + `{
		"facts": [{"text": "test fact", "type": "general", "salience": 0.7}],
		"entity_triples": [],
		"session_summary": "test"
	}` + "\n```"

	result, err := parseDistillResponse(input)
	if err != nil {
		t.Fatalf("unexpected error parsing code-fenced response: %v", err)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("expected 1 fact, got %d", len(result.Facts))
	}
	if result.Facts[0].Text != "test fact" {
		t.Errorf("expected 'test fact', got %q", result.Facts[0].Text)
	}

	// Also test bare ``` fences (without json language tag)
	input2 := "```\n" + `{
		"facts": [{"text": "bare fence", "type": "warning", "salience": 0.8}],
		"entity_triples": [],
		"session_summary": "bare"
	}` + "\n```"

	result2, err := parseDistillResponse(input2)
	if err != nil {
		t.Fatalf("unexpected error parsing bare code-fenced response: %v", err)
	}
	if result2.Facts[0].Text != "bare fence" {
		t.Errorf("expected 'bare fence', got %q", result2.Facts[0].Text)
	}
}

func TestParseDistillResponse_InvalidJSON(t *testing.T) {
	_, err := parseDistillResponse("this is not json at all")
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	if !strings.Contains(err.Error(), "parse extraction response") {
		t.Errorf("expected 'parse extraction response' in error, got: %v", err)
	}
}

func TestParseDistillResponse_SalienceClamping(t *testing.T) {
	input := `{
		"facts": [
			{"text": "too low", "type": "general", "salience": 0.0},
			{"text": "too high", "type": "general", "salience": 1.5},
			{"text": "negative", "type": "general", "salience": -0.5},
			{"text": "valid low", "type": "general", "salience": 0.1},
			{"text": "invalid type", "type": "nonexistent", "salience": 0.7}
		],
		"entity_triples": [],
		"session_summary": "clamping test"
	}`

	result, err := parseDistillResponse(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	tests := []struct {
		idx             int
		desc            string
		expectedSal     float64
		expectedType    string
	}{
		{0, "too low (0.0)", 0.6, "general"},
		{1, "too high (1.5)", 1.0, "general"},
		{2, "negative (-0.5)", 0.6, "general"},
		{3, "valid low (0.1)", 0.1, "general"},
		{4, "invalid type", 0.7, "general"}, // type should default to "general"
	}

	for _, tc := range tests {
		fact := result.Facts[tc.idx]
		if fact.Salience != tc.expectedSal {
			t.Errorf("[%s] expected salience %f, got %f", tc.desc, tc.expectedSal, fact.Salience)
		}
		if fact.Type != tc.expectedType {
			t.Errorf("[%s] expected type %q, got %q", tc.desc, tc.expectedType, fact.Type)
		}
	}
}

func TestCollectAllFiles(t *testing.T) {
	facts := []DistilledFact{
		{Text: "fact 1", Files: []string{"auth.go", "middleware.go"}},
		{Text: "fact 2", Files: []string{"middleware.go", "jwt.go"}},
		{Text: "fact 3", Files: nil},
	}
	files := collectAllFiles(facts)

	fileSet := make(map[string]bool)
	for _, f := range files {
		fileSet[f] = true
	}

	if len(fileSet) != 3 {
		t.Fatalf("expected 3 unique files, got %d: %v", len(fileSet), files)
	}
	if !fileSet["auth.go"] || !fileSet["middleware.go"] || !fileSet["jwt.go"] {
		t.Errorf("missing expected files: %v", files)
	}
}

func TestProjectSlug(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"/Users/donny/Projects/2026/geoffreyengram", "-Users-donny-Projects-2026-geoffreyengram"},
		{"/Users/donny/Work/LearnCard", "-Users-donny-Work-LearnCard"},
		{"/tmp/test", "-tmp-test"},
	}
	for _, tc := range tests {
		got := projectSlug(tc.input)
		if got != tc.want {
			t.Errorf("projectSlug(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestFindLatestCCSession_CurrentProject(t *testing.T) {
	// Create temp dir mimicking ~/.claude/projects/<slug>/
	tmpHome := t.TempDir()
	claudeProjects := filepath.Join(tmpHome, ".claude", "projects")
	projectDir := filepath.Join(claudeProjects, "-tmp-myproject")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Write a fake session .jsonl file
	sessionContent := `{"role":"user","content":"Hello world, this is a test session message"}
{"role":"assistant","content":"I'll help you with that task right away."}`
	sessionFile := filepath.Join(projectDir, "abc123.jsonl")
	if err := os.WriteFile(sessionFile, []byte(sessionContent), 0o644); err != nil {
		t.Fatal(err)
	}

	// Override UserHomeDir for this test by using the exported function
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	transcript, sessionID, err := findLatestCCSession("/tmp/myproject")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sessionID != "abc123" {
		t.Errorf("expected sessionID 'abc123', got %q", sessionID)
	}
	if !strings.Contains(transcript, "Hello world") {
		t.Errorf("expected transcript to contain user message, got: %s", transcript)
	}
}

func TestFindLatestCCSession_FallbackAllProjects(t *testing.T) {
	tmpHome := t.TempDir()
	claudeProjects := filepath.Join(tmpHome, ".claude", "projects")
	otherProject := filepath.Join(claudeProjects, "-tmp-otherproject")
	if err := os.MkdirAll(otherProject, 0o755); err != nil {
		t.Fatal(err)
	}

	sessionContent := `{"role":"user","content":"Fallback session content is here for testing"}`
	if err := os.WriteFile(filepath.Join(otherProject, "sess1.jsonl"), []byte(sessionContent), 0o644); err != nil {
		t.Fatal(err)
	}

	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	// rootDir doesn't match any project slug → falls back to all
	transcript, _, err := findLatestCCSession("/tmp/nonexistent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(transcript, "Fallback session") {
		t.Errorf("expected fallback transcript, got: %s", transcript)
	}
}

func TestFindLatestCCSession_PicksMostRecent(t *testing.T) {
	tmpHome := t.TempDir()
	projectDir := filepath.Join(tmpHome, ".claude", "projects", "-tmp-proj")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Write older session
	older := filepath.Join(projectDir, "old-session.jsonl")
	if err := os.WriteFile(older, []byte(`{"role":"user","content":"This is the older session transcript content"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-1 * time.Hour)
	os.Chtimes(older, oldTime, oldTime)

	// Write newer session
	newer := filepath.Join(projectDir, "new-session.jsonl")
	if err := os.WriteFile(newer, []byte(`{"role":"user","content":"This is the newer session transcript content"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	_, sessionID, err := findLatestCCSession("/tmp/proj")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sessionID != "new-session" {
		t.Errorf("expected newest session 'new-session', got %q", sessionID)
	}
}

func TestFindLatestCCSession_NoSessions(t *testing.T) {
	tmpHome := t.TempDir()
	claudeProjects := filepath.Join(tmpHome, ".claude", "projects")
	if err := os.MkdirAll(claudeProjects, 0o755); err != nil {
		t.Fatal(err)
	}

	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	_, _, err := findLatestCCSession("/tmp/noproject")
	if err == nil {
		t.Fatal("expected error for no sessions")
	}
	if !strings.Contains(err.Error(), "no Claude Code sessions found") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestFindLatestCCSession_IgnoresNonJSONL(t *testing.T) {
	tmpHome := t.TempDir()
	projectDir := filepath.Join(tmpHome, ".claude", "projects", "-tmp-proj")
	subDir := filepath.Join(projectDir, "some-uuid-dir")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Write non-jsonl files and a directory that should be ignored
	os.WriteFile(filepath.Join(projectDir, "notes.txt"), []byte("not a session"), 0o644)
	os.WriteFile(filepath.Join(projectDir, "real.jsonl"), []byte(`{"role":"user","content":"The real session content that should be found"}`), 0o644)

	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	transcript, sessionID, err := findLatestCCSession("/tmp/proj")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sessionID != "real" {
		t.Errorf("expected 'real', got %q", sessionID)
	}
	if !strings.Contains(transcript, "real session") {
		t.Errorf("expected real session content, got: %s", transcript)
	}
}

func TestFormatDistillPrompt(t *testing.T) {
	transcript := "User: Add dark mode\n\nAssistant: I'll implement that now."
	maxFacts := 15

	prompt := formatDistillPrompt(transcript, maxFacts)

	// Should contain the transcript
	if !strings.Contains(prompt, transcript) {
		t.Error("prompt should contain the transcript")
	}

	// Should contain the max facts count
	if !strings.Contains(prompt, "Maximum 15 facts") {
		t.Errorf("prompt should contain max facts count, got: %s", prompt)
	}

	// Should contain key instruction elements
	if !strings.Contains(prompt, "memory extraction") {
		t.Error("prompt should mention memory extraction")
	}
	if !strings.Contains(prompt, "entity_triples") {
		t.Error("prompt should mention entity_triples in the JSON schema")
	}
	if !strings.Contains(prompt, "TRANSCRIPT:") {
		t.Error("prompt should contain TRANSCRIPT: marker")
	}
}
