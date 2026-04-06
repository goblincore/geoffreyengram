package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// GeneratePlan uses the configured planning model (via claude CLI) to expand a
// short description into a full plan markdown body.
func (e *Engine) GeneratePlan(ctx context.Context, description, project string) (string, error) {
	// 1. Gather project context from 4 sources.
	var ctxParts []string

	// Source 1: .claude/CLAUDE.md
	claudeMDPath := filepath.Join(project, ".claude", "CLAUDE.md")
	if data, err := os.ReadFile(claudeMDPath); err == nil {
		ctxParts = append(ctxParts, "## Project CLAUDE.md\n\n"+string(data))
	}

	// Source 2: git log --oneline -20
	gitCtx, gitCancel := context.WithTimeout(ctx, 10*time.Second)
	defer gitCancel()
	gitCmd := exec.CommandContext(gitCtx, "git", "log", "--oneline", "-20")
	gitCmd.Dir = project
	if out, err := gitCmd.Output(); err == nil && len(out) > 0 {
		ctxParts = append(ctxParts, "## Recent git log\n\n"+string(out))
	}

	// Source 3: Walk project dir for code files (up to 50)
	var codePaths []string
	skipDirs := map[string]bool{
		"node_modules": true,
		"vendor":       true,
	}
	codeExts := map[string]bool{
		".go": true, ".ts": true, ".js": true, ".py": true, ".rs": true,
	}
	walkDone := false
	_ = filepath.Walk(project, func(path string, info os.FileInfo, err error) error {
		if err != nil || walkDone {
			return nil
		}
		if info.IsDir() {
			base := info.Name()
			if strings.HasPrefix(base, ".") || skipDirs[base] {
				return filepath.SkipDir
			}
			return nil
		}
		if codeExts[filepath.Ext(path)] {
			rel, relErr := filepath.Rel(project, path)
			if relErr == nil {
				codePaths = append(codePaths, rel)
			}
			if len(codePaths) >= 50 {
				walkDone = true
				return filepath.SkipAll
			}
		}
		return nil
	})
	if len(codePaths) > 0 {
		ctxParts = append(ctxParts, "## Code files in project\n\n"+strings.Join(codePaths, "\n"))
	}

	// Source 4: Most recent "done" plan for this project (as format example)
	plans, _ := e.ListPlans()
	for _, p := range plans {
		if p.Status == "done" && p.Project == project && p.Body != "" {
			ctxParts = append(ctxParts, "## Example plan (most recent done plan for this project)\n\n"+p.Body)
			break
		}
	}

	// 2. Build meta-prompt
	projectContext := strings.Join(ctxParts, "\n\n---\n\n")

	metaPrompt := fmt.Sprintf(`You are a technical planning assistant. Your job is to expand a short task description into a detailed, actionable plan for an AI coding agent.

Given the project context and description below, produce a plan with exactly these sections:

## Context
Brief description of what this task is about and why it matters.

## Key files to read first
A bulleted list of file paths the agent should read before starting work.

## Instructions
Step-by-step numbered instructions for completing the task. Be specific and actionable.

## Acceptance criteria
A bulleted checklist of conditions that must be true for the task to be considered done.

Rules:
- Do NOT include YAML frontmatter — output only the markdown body starting with ## Context
- Be specific to the project structure shown in the context
- Keep instructions precise and unambiguous
- Assume the agent has access to standard coding tools (read, edit, bash, grep)

---

## Project context

%s

---

## Task description

%s

---

Now write the plan:`, projectContext, description)

	// 3. Resolve planning model auth
	apiKeyEnv := e.Config.PlanningAPIKeyEnv
	if apiKeyEnv == "" {
		apiKeyEnv = "ANTHROPIC_API_KEY"
	}
	apiKey := e.Config.EnvVars[apiKeyEnv]
	if apiKey == "" {
		apiKey = os.Getenv(apiKeyEnv)
	}

	baseURL := e.Config.PlanningBaseURL

	// 4. Build environment
	// If no API key is configured, inherit the parent environment so the CLI
	// uses the user's subscription auth (no key override needed).
	var envPairs []string
	if apiKey != "" {
		if baseURL != "" {
			envPairs = append(envPairs,
				"ANTHROPIC_AUTH_TOKEN="+apiKey,
				"ANTHROPIC_BASE_URL="+baseURL,
			)
		} else {
			envPairs = append(envPairs, "ANTHROPIC_API_KEY="+apiKey)
		}
	}
	if model := e.Config.PlanningModel; model != "" {
		envPairs = append(envPairs, "ANTHROPIC_DEFAULT_SONNET_MODEL="+model)
	}
	// Always inherit PATH and HOME
	if p := os.Getenv("PATH"); p != "" {
		envPairs = append(envPairs, "PATH="+p)
	}
	if h := os.Getenv("HOME"); h != "" {
		envPairs = append(envPairs, "HOME="+h)
	}

	// 5. Set 5 minute timeout
	genCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	// 6. Run claude CLI
	claudeBin := e.Config.ClaudeBin
	if claudeBin == "" {
		claudeBin = "claude"
	}

	args := []string{
		"--bare",
		"-p", metaPrompt,
		"--output-format", "text",
		"--no-session-persistence",
	}
	cmd := exec.CommandContext(genCtx, claudeBin, args...)
	cmd.Env = envPairs

	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("claude CLI failed: %w\noutput: %s", err, string(out))
	}

	return strings.TrimSpace(string(out)), nil
}
