package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Plan represents a dispatch plan file with frontmatter and body.
type Plan struct {
	// Metadata set by the CLI or user
	Name string
	Path string

	// Frontmatter fields
	Title         string
	Status        string
	Project       string
	Model         string
	APIKeyEnv     string
	BaseURL       string
	Branch        string
	BaseBranch    string // ref the worktree branches FROM; empty means HEAD
	Priority      int
	MaxRuntime    string
	Created       string
	DependsOn     []string
	AllowedTools  string
	Harness       string // "claude" (default) or "opencode"
	Setup         string // per-task provisioning command run in the worktree before the agent; "none"/"skip" disables; empty falls back to DEFAULT_SETUP_CMD
	SetupTimeout  string // max duration for the setup command (e.g. "5m"); empty falls back to DEFAULT_SETUP_TIMEOUT
	StartedAt     string
	FinishedAt    string
	ExitCode      int
	Report        string
	CreatedBy     string
	PlanningModel string

	// Body is everything after the closing --- delimiter
	Body string
}

// parsePlanFile reads a plan markdown file and returns a Plan struct.
func parsePlanFile(path string) (*Plan, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read plan file: %w", err)
	}

	content := string(data)

	// Must start with ---
	if !strings.HasPrefix(content, "---") {
		return nil, fmt.Errorf("plan file does not start with frontmatter delimiter")
	}

	// Find the closing ---
	rest := content[3:] // skip opening ---
	// skip newline after opening ---
	if len(rest) > 0 && rest[0] == '\n' {
		rest = rest[1:]
	}

	idx := strings.Index(rest, "\n---")
	if idx < 0 {
		return nil, fmt.Errorf("plan file missing closing frontmatter delimiter")
	}

	frontmatter := rest[:idx]
	body := rest[idx+4:] // skip \n---
	// Skip the newline immediately after the closing ---
	if len(body) > 0 && body[0] == '\n' {
		body = body[1:]
	}

	plan := &Plan{
		Name: strings.TrimSuffix(filepath.Base(path), ".md"),
		Path: path,
	}

	for _, line := range strings.Split(frontmatter, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		k, v := splitKV(line)
		switch k {
		case "title":
			plan.Title = v
		case "status":
			plan.Status = v
		case "project":
			plan.Project = v
		case "model":
			plan.Model = v
		case "api_key_env":
			plan.APIKeyEnv = v
		case "base_url":
			plan.BaseURL = v
		case "branch":
			plan.Branch = v
		case "base_branch":
			plan.BaseBranch = v
		case "priority":
			n, _ := strconv.Atoi(v)
			plan.Priority = n
		case "max_runtime":
			plan.MaxRuntime = v
		case "created":
			plan.Created = v
		case "depends_on":
			plan.DependsOn = parseDependsList(v)
		case "allowed_tools":
			plan.AllowedTools = v
		case "harness":
			plan.Harness = v
		case "setup":
			plan.Setup = v
		case "setup_timeout":
			plan.SetupTimeout = v
		case "started_at":
			plan.StartedAt = v
		case "finished_at":
			plan.FinishedAt = v
		case "exit_code":
			n, _ := strconv.Atoi(v)
			plan.ExitCode = n
		case "report":
			plan.Report = v
		case "created_by":
			plan.CreatedBy = v
		case "planning_model":
			plan.PlanningModel = v
		}
	}

	plan.Body = body
	return plan, nil
}

// splitKV splits a frontmatter line into key and value.
// It strips surrounding quotes from the value and ignores inline comments.
func splitKV(line string) (string, string) {
	idx := strings.Index(line, ":")
	if idx < 0 {
		return strings.TrimSpace(line), ""
	}
	key := strings.TrimSpace(line[:idx])
	val := strings.TrimSpace(line[idx+1:])

	// Strip inline comment (space + #)
	if ci := strings.Index(val, " #"); ci >= 0 {
		val = strings.TrimSpace(val[:ci])
	}

	// Strip surrounding quotes
	if len(val) >= 2 {
		if (val[0] == '"' && val[len(val)-1] == '"') ||
			(val[0] == '\'' && val[len(val)-1] == '\'') {
			val = val[1 : len(val)-1]
		}
	}

	return key, val
}

// parseDependsList parses a YAML-style inline list or comma-separated string.
// Handles: [], [a, b, c], a,b,c
func parseDependsList(s string) []string {
	s = strings.TrimSpace(s)
	if s == "[]" || s == "" {
		return []string{}
	}

	// Strip square brackets if present
	if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
		s = s[1 : len(s)-1]
	}

	parts := strings.Split(s, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		// Strip quotes
		if len(p) >= 2 {
			if (p[0] == '"' && p[len(p)-1] == '"') ||
				(p[0] == '\'' && p[len(p)-1] == '\'') {
				p = p[1 : len(p)-1]
			}
		}
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}

// setFrontmatterField updates a single frontmatter key in-place, or inserts it
// before the closing --- if not found. Body is preserved unchanged.
func setFrontmatterField(path, key, value string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read plan file: %w", err)
	}
	content := string(data)

	if !strings.HasPrefix(content, "---") {
		return fmt.Errorf("plan file does not start with frontmatter delimiter")
	}

	rest := content[3:]
	if len(rest) > 0 && rest[0] == '\n' {
		rest = rest[1:]
	}

	idx := strings.Index(rest, "\n---")
	if idx < 0 {
		return fmt.Errorf("plan file missing closing frontmatter delimiter")
	}

	frontmatter := rest[:idx]
	afterClose := rest[idx:] // includes \n---...

	lines := strings.Split(frontmatter, "\n")
	found := false
	for i, line := range lines {
		k, _ := splitKV(line)
		if k == key {
			// Preserve original indentation (none expected, but safe)
			lines[i] = key + ": " + value
			found = true
			break
		}
	}

	if !found {
		lines = append(lines, key+": "+value)
	}

	newContent := "---\n" + strings.Join(lines, "\n") + afterClose
	return os.WriteFile(path, []byte(newContent), 0644)
}

// writePlanFile writes a Plan to its Path with formatted frontmatter and body.
func writePlanFile(p *Plan) error {
	var sb strings.Builder
	sb.WriteString("---\n")

	writeLine := func(k, v string) {
		if v != "" {
			sb.WriteString(k + ": " + v + "\n")
		}
	}
	writeQuoted := func(k, v string) {
		if v != "" {
			sb.WriteString(k + ": \"" + v + "\"\n")
		}
	}
	writeInt := func(k string, v int) {
		sb.WriteString(fmt.Sprintf("%s: %d\n", k, v))
	}

	writeQuoted("title", p.Title)
	writeLine("status", p.Status)
	writeLine("project", p.Project)
	writeLine("model", p.Model)
	writeLine("api_key_env", p.APIKeyEnv)
	writeLine("base_url", p.BaseURL)
	writeLine("branch", p.Branch)
	writeLine("base_branch", p.BaseBranch)
	if p.Priority != 0 {
		writeInt("priority", p.Priority)
	}
	writeLine("max_runtime", p.MaxRuntime)
	writeLine("created", p.Created)

	// depends_on list
	if p.DependsOn != nil {
		if len(p.DependsOn) == 0 {
			sb.WriteString("depends_on: []\n")
		} else {
			sb.WriteString("depends_on: [" + strings.Join(p.DependsOn, ", ") + "]\n")
		}
	}

	writeLine("allowed_tools", p.AllowedTools)
	writeLine("harness", p.Harness)
	writeLine("started_at", p.StartedAt)
	writeLine("finished_at", p.FinishedAt)
	if p.ExitCode != 0 {
		writeInt("exit_code", p.ExitCode)
	}
	writeLine("report", p.Report)
	writeLine("created_by", p.CreatedBy)
	writeLine("planning_model", p.PlanningModel)

	sb.WriteString("---\n")
	sb.WriteString(p.Body)

	return os.WriteFile(p.Path, []byte(sb.String()), 0644)
}
