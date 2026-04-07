package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// parseStreamEvent extracts a human-readable log line from a stream-json event.
// Supports both Claude Code (stream-json) and OpenCode (--format json) event formats.
func parseStreamEvent(raw string) string {
	// Quick type peek to dispatch to the right parser
	var peek struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal([]byte(raw), &peek); err != nil {
		return ""
	}

	switch peek.Type {
	// --- Claude Code events ---
	case "assistant":
		return parseClaudeEvent(raw)

	// --- OpenCode events ---
	case "text":
		return parseOpenCodeTextEvent(raw)
	case "tool_use":
		return parseOpenCodeToolEvent(raw)
	case "step_finish":
		return parseOpenCodeStepFinish(raw)

	// --- Pi events ---
	case "message_end":
		return parsePiMessageEnd(raw)
	case "tool_call":
		return parsePiToolCall(raw)

	// --- Shared ---
	case "error":
		return parseOpenCodeError(raw)
	}
	return ""
}

// parseClaudeEvent handles Claude Code stream-json "assistant" events.
func parseClaudeEvent(raw string) string {
	var ev struct {
		Message struct {
			Content []struct {
				Type  string          `json:"type"`
				Text  string          `json:"text"`
				Name  string          `json:"name"`
				Input json.RawMessage `json:"input"`
			} `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal([]byte(raw), &ev); err != nil {
		return ""
	}
	var parts []string
	for _, c := range ev.Message.Content {
		switch c.Type {
		case "text":
			if c.Text != "" {
				parts = append(parts, c.Text)
			}
		case "tool_use":
			var input map[string]any
			json.Unmarshal(c.Input, &input)
			name := c.Name
			if cmd, ok := input["command"].(string); ok && name == "Bash" {
				cmd = strings.ReplaceAll(cmd, "\n", " ")
				if len(cmd) > 120 {
					cmd = cmd[:120] + "..."
				}
				parts = append(parts, fmt.Sprintf("[%s] %s", name, cmd))
			} else if fp, ok := input["file_path"].(string); ok {
				parts = append(parts, fmt.Sprintf("[%s] %s", name, fp))
			} else if p, ok := input["pattern"].(string); ok {
				parts = append(parts, fmt.Sprintf("[%s] %s", name, p))
			} else {
				parts = append(parts, fmt.Sprintf("[%s]", name))
			}
		}
	}
	if len(parts) > 0 {
		return strings.Join(parts, "\n")
	}
	return ""
}

// parseOpenCodeTextEvent handles OpenCode "text" events: {"type":"text","part":{"text":"..."}}.
func parseOpenCodeTextEvent(raw string) string {
	var ev struct {
		Part struct {
			Text string `json:"text"`
		} `json:"part"`
	}
	if err := json.Unmarshal([]byte(raw), &ev); err != nil || ev.Part.Text == "" {
		return ""
	}
	text := ev.Part.Text
	if len(text) > 200 {
		text = text[:200] + "..."
	}
	return text
}

// parseOpenCodeToolEvent handles OpenCode "tool_use" events:
// {"type":"tool_use","part":{"tool":"Bash","state":{"input":{...},"output":"...","title":"...","status":"completed"}}}.
func parseOpenCodeToolEvent(raw string) string {
	var ev struct {
		Part struct {
			Tool  string `json:"tool"`
			State struct {
				Status string          `json:"status"`
				Title  string          `json:"title"`
				Input  json.RawMessage `json:"input"`
				Output string          `json:"output"`
			} `json:"state"`
		} `json:"part"`
	}
	if err := json.Unmarshal([]byte(raw), &ev); err != nil {
		return ""
	}
	tool := ev.Part.Tool
	if tool == "" {
		return ""
	}

	// Try to extract useful info from input
	var input map[string]any
	json.Unmarshal(ev.Part.State.Input, &input)

	if ev.Part.State.Title != "" {
		return fmt.Sprintf("[%s] %s", tool, ev.Part.State.Title)
	}
	if cmd, ok := input["command"].(string); ok && (tool == "Bash" || tool == "bash") {
		cmd = strings.ReplaceAll(cmd, "\n", " ")
		if len(cmd) > 120 {
			cmd = cmd[:120] + "..."
		}
		return fmt.Sprintf("[%s] %s", tool, cmd)
	}
	if fp, ok := input["file_path"].(string); ok {
		return fmt.Sprintf("[%s] %s", tool, fp)
	}
	if p, ok := input["pattern"].(string); ok {
		return fmt.Sprintf("[%s] %s", tool, p)
	}
	return fmt.Sprintf("[%s]", tool)
}

// parseOpenCodeStepFinish handles OpenCode "step_finish" events with token/cost summary.
func parseOpenCodeStepFinish(raw string) string {
	var ev struct {
		Part struct {
			Reason string `json:"reason"`
			Tokens struct {
				Input  int `json:"input"`
				Output int `json:"output"`
				Total  int `json:"total"`
			} `json:"tokens"`
			Cost float64 `json:"cost"`
		} `json:"part"`
	}
	if err := json.Unmarshal([]byte(raw), &ev); err != nil {
		return ""
	}
	if ev.Part.Reason == "stop" {
		return fmt.Sprintf("[step done] %d tokens (in=%d out=%d)",
			ev.Part.Tokens.Total, ev.Part.Tokens.Input, ev.Part.Tokens.Output)
	}
	return ""
}

// parseOpenCodeError handles OpenCode "error" events.
func parseOpenCodeError(raw string) string {
	var ev struct {
		Part struct {
			Error string `json:"error"`
		} `json:"part"`
	}
	if err := json.Unmarshal([]byte(raw), &ev); err != nil || ev.Part.Error == "" {
		return ""
	}
	return fmt.Sprintf("[ERROR] %s", ev.Part.Error)
}

// parsePiMessageEnd handles Pi "message_end" events for assistant messages.
// Pi format: {"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"..."}]}}
func parsePiMessageEnd(raw string) string {
	var ev struct {
		Message struct {
			Role    string `json:"role"`
			Content []struct {
				Type  string `json:"type"`
				Text  string `json:"text"`
				Name  string `json:"name"`
				Input string `json:"input"`
			} `json:"content"`
			Usage struct {
				Input  int `json:"input"`
				Output int `json:"output"`
			} `json:"usage"`
		} `json:"message"`
	}
	if err := json.Unmarshal([]byte(raw), &ev); err != nil {
		return ""
	}
	if ev.Message.Role != "assistant" {
		return ""
	}
	var parts []string
	for _, c := range ev.Message.Content {
		switch c.Type {
		case "text":
			if c.Text != "" {
				text := c.Text
				if len(text) > 200 {
					text = text[:200] + "..."
				}
				parts = append(parts, text)
			}
		case "tool_use":
			if c.Name != "" {
				parts = append(parts, fmt.Sprintf("[%s]", c.Name))
			}
		}
	}
	if ev.Message.Usage.Output > 0 {
		parts = append(parts, fmt.Sprintf("(%d tokens)", ev.Message.Usage.Input+ev.Message.Usage.Output))
	}
	if len(parts) > 0 {
		return strings.Join(parts, "\n")
	}
	return ""
}

// parsePiToolCall handles Pi "tool_call" events.
// Pi format: {"type":"tool_call","tool":{"name":"bash","input":{"command":"..."}}}
func parsePiToolCall(raw string) string {
	var ev struct {
		Tool struct {
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"tool"`
	}
	if err := json.Unmarshal([]byte(raw), &ev); err != nil || ev.Tool.Name == "" {
		return ""
	}
	var input map[string]any
	json.Unmarshal(ev.Tool.Input, &input)

	name := ev.Tool.Name
	if cmd, ok := input["command"].(string); ok && (name == "bash" || name == "Bash") {
		cmd = strings.ReplaceAll(cmd, "\n", " ")
		if len(cmd) > 120 {
			cmd = cmd[:120] + "..."
		}
		return fmt.Sprintf("[%s] %s", name, cmd)
	}
	if fp, ok := input["file_path"].(string); ok {
		return fmt.Sprintf("[%s] %s", name, fp)
	}
	if p, ok := input["path"].(string); ok {
		return fmt.Sprintf("[%s] %s", name, p)
	}
	return fmt.Sprintf("[%s]", name)
}

// parseMaxRuntime converts strings like "10m", "1h", "1800" to time.Duration.
// Returns 0 if the string is empty or unparseable.
func parseMaxRuntime(s string) time.Duration {
	if s == "" {
		return 0
	}
	// Try Go duration format first (e.g. "10m", "1h", "30s")
	if d, err := time.ParseDuration(s); err == nil {
		return d
	}
	// Fall back to bare integer seconds
	if n, err := strconv.Atoi(s); err == nil {
		return time.Duration(n) * time.Second
	}
	return 0
}

// ExecutePlan runs the claude CLI for the given plan, streams output to the
// plan's LogBuffer and a report file, then updates the plan frontmatter.
func (e *Engine) ExecutePlan(ctx context.Context, plan *Plan) error {
	// 1. Claim the plan: mark as running with started_at
	now := time.Now().UTC().Format(time.RFC3339)
	if err := setFrontmatterField(plan.Path, "status", "running"); err != nil {
		return fmt.Errorf("set status running: %w", err)
	}
	if err := setFrontmatterField(plan.Path, "started_at", now); err != nil {
		return fmt.Errorf("set started_at: %w", err)
	}

	// 2. Create a LogBuffer for this task
	e.mu.Lock()
	lb := &LogBuffer{}
	e.logs[plan.Name] = lb
	e.mu.Unlock()

	// 3. Resolve API key: plan.APIKeyEnv → e.Config.EnvVars → os.Getenv
	apiKeyEnv := plan.APIKeyEnv
	if apiKeyEnv == "" {
		apiKeyEnv = e.Config.DefaultAuthTokenEnv
	}
	if apiKeyEnv == "" {
		apiKeyEnv = "ANTHROPIC_API_KEY"
	}
	apiKey := e.Config.EnvVars[apiKeyEnv]
	if apiKey == "" {
		apiKey = os.Getenv(apiKeyEnv)
	}

	// 4. Build environment
	baseURL := plan.BaseURL
	if baseURL == "" {
		baseURL = e.Config.DefaultBaseURL
	}
	model := plan.Model
	if model == "" {
		model = e.Config.DefaultModel
	}

	// If no API key is resolved, inherit parent env so CLI uses subscription auth.
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
	if model != "" {
		envPairs = append(envPairs, "ANTHROPIC_DEFAULT_SONNET_MODEL="+model)
	}
	// Preserve PATH and HOME so the subprocess can find tools
	if p := os.Getenv("PATH"); p != "" {
		if e.Config.ExtraPath != "" {
			p = e.Config.ExtraPath + ":" + p
		}
		envPairs = append(envPairs, "PATH="+p)
	}
	if h := os.Getenv("HOME"); h != "" {
		envPairs = append(envPairs, "HOME="+h)
	}
	// Pass through all .env vars (ZAI_API_KEY, etc.) so opencode/other harnesses can use them
	for k, v := range e.Config.EnvVars {
		envPairs = append(envPairs, k+"="+v)
	}
	// Ensure dualmem uses the project's namespace, not the worktree directory name
	projectName := filepath.Base(plan.Project)
	envPairs = append(envPairs, "DUALMEM_NAMESPACE=claude:"+projectName)
	envPairs = append(envPairs, "DUALMEM_ROOT_DIR="+plan.Project)

	// 5. Create context with timeout
	execCtx := ctx
	var cancel context.CancelFunc
	if d := parseMaxRuntime(plan.MaxRuntime); d > 0 {
		execCtx, cancel = context.WithTimeout(ctx, d)
	} else {
		execCtx, cancel = context.WithCancel(ctx)
	}
	defer cancel()

	// 6. Store cancel func for UI cancellation
	e.mu.Lock()
	e.running = &RunningTask{Plan: plan, Cancel: cancel}
	e.mu.Unlock()

	// 7. Create a git worktree for isolation
	workDir := plan.Project // fallback: run in project dir
	branch := plan.Branch
	if branch == "" {
		branch = "dispatch/" + plan.Name
	}

	home, _ := os.UserHomeDir()
	worktreeBase := filepath.Join(home, ".claude", "dispatch", "worktrees")
	os.MkdirAll(worktreeBase, 0755)
	worktreePath := filepath.Join(worktreeBase, plan.Name)

	// Remove stale worktree if it exists from a previous run
	exec.Command("git", "-C", plan.Project, "worktree", "remove", "--force", worktreePath).Run()
	// Delete stale branch if it exists
	exec.Command("git", "-C", plan.Project, "branch", "-D", branch).Run()

	wtCmd := exec.Command("git", "-C", plan.Project, "worktree", "add", worktreePath, "-b", branch, "HEAD")
	if out, err := wtCmd.CombinedOutput(); err != nil {
		lb.Append(fmt.Sprintf("[dispatch] worktree creation failed: %s — running in project dir", strings.TrimSpace(string(out))))
	} else {
		workDir = worktreePath
		lb.Append(fmt.Sprintf("[dispatch] created worktree at %s (branch: %s)", worktreePath, branch))

		// Symlink .claude/ from main repo so Claude Code finds CLAUDE.md
		claudeDir := filepath.Join(plan.Project, ".claude")
		if _, err := os.Stat(claudeDir); err == nil {
			os.Symlink(claudeDir, filepath.Join(worktreePath, ".claude"))
		}
	}

	// 8. Build command
	harness := plan.Harness
	if harness == "" {
		harness = "claude"
	}

	prompt := plan.Body
	if e.Config.Preamble != "" && harness != "pi" {
		// Pi has its own edit tool that works fine — only inject preamble for Claude CLI
		prompt = e.Config.Preamble + "\n\n" + prompt
	}
	// Inject dualmem context + CLI instructions for harnesses without MCP support (Pi).
	// Claude CLI gets context via dualmem hooks; Pi doesn't have MCP, so we prepend it.
	if harness == "pi" {
		dualmemBin := filepath.Join(os.Getenv("HOME"), "go", "bin", "dualmem")
		ns := "claude:" + projectName

		// Fetch project context
		dualmemCmd := exec.Command(dualmemBin, "context", prompt, "--budget", "2000", "--ns", ns)
		dualmemCmd.Dir = plan.Project
		var contextBlock string
		if ctxOut, err := dualmemCmd.Output(); err == nil && len(ctxOut) > 100 {
			contextBlock = "[Project Memory]\n" + string(ctxOut)
		}

		// Build dualmem CLI instructions so Pi can save memories during the session
		dualmemInstructions := fmt.Sprintf(`[Memory System — dualmem CLI]
You have access to a cross-session memory system via the dualmem CLI (%s).
Use bash to run these commands when appropriate:

SAVE important discoveries (decisions, warnings, file relationships):
  %s add --text "what you learned" --ns "%s"
  %s add --type decision --text "chose X over Y because..." --ns "%s" --files "file1.go,file2.go"
  %s add --type warning --text "don't change X, it's intentional" --ns "%s" --salience 0.9

SEARCH for existing knowledge before exploring:
  %s search "query" --ns "%s" --limit 5

SEARCH CODE by natural language:
  %s search-code "what you're looking for"

SAVE at end of task if you learned something non-obvious:
  %s checkpoint --task "task name" --status done --done "what was completed" --ns "%s"

Save memories when you:
- Make a decision between alternatives
- Discover a non-obvious file relationship or constraint
- Find a root cause that took effort
- Hit a dead end worth remembering`, dualmemBin, dualmemBin, ns, dualmemBin, ns, dualmemBin, ns, dualmemBin, ns, dualmemBin, dualmemBin, ns)

		prompt = dualmemInstructions + "\n\n" + contextBlock + "\n\n[Task]\n" + prompt
	}
	tools := plan.AllowedTools
	if tools == "" {
		tools = "Bash"
	}

	var cmd *exec.Cmd
	switch harness {
	case "opencode":
		opencodeBin := e.Config.OpenCodeBin
		if opencodeBin == "" {
			opencodeBin = "opencode"
		}
		// opencode run "<prompt>" --model provider/model --format json --dir <workdir>
		args := []string{"run"}
		if model != "" {
			args = append(args, "--model", model)
		}
		args = append(args, "--format", "json")
		args = append(args, "--dir", workDir)
		args = append(args, prompt)
		cmd = exec.CommandContext(execCtx, opencodeBin, args...)

	case "pi":
		piBin := e.Config.PiBin
		if piBin == "" {
			piBin = "pi"
		}
		// pi -p --mode json --no-session --model provider/model "prompt"
		args := []string{"-p", "--mode", "json", "--no-session"}
		if model != "" {
			args = append(args, "--model", model)
		}
		args = append(args, prompt)
		cmd = exec.CommandContext(execCtx, piBin, args...)

	default: // "claude"
		claudeBin := e.Config.ClaudeBin
		if claudeBin == "" {
			claudeBin = "claude"
		}
		args := []string{
			"--bare",
			"-p", prompt,
			"--allowed-tools", tools,
			"--output-format", "stream-json",
			"--verbose",
			"--no-session-persistence",
		}
		cmd = exec.CommandContext(execCtx, claudeBin, args...)
	}
	cmd.WaitDelay = 1 * time.Second

	// 9. Set working directory and environment
	cmd.Dir = workDir
	cmd.Env = envPairs

	// 9. Pipe stdout; let stderr go to os.Stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	cmd.Stderr = os.Stderr

	// 10. Open report file
	if err := os.MkdirAll(e.Config.ReportsDir, 0755); err != nil {
		return fmt.Errorf("create reports dir: %w", err)
	}
	reportName := fmt.Sprintf("%s-%s.txt", plan.Name, time.Now().UTC().Format("20060102-150405"))
	reportPath := filepath.Join(e.Config.ReportsDir, reportName)
	reportFile, err := os.Create(reportPath)
	if err != nil {
		return fmt.Errorf("create report file: %w", err)
	}
	defer reportFile.Close()

	// Write report header
	header := fmt.Sprintf("# Report: %s\nStarted: %s\n\n", plan.Name, now)
	fmt.Fprint(reportFile, header)
	lb.Append(strings.TrimRight(header, "\n"))

	// Start the process
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start command: %w", err)
	}

	// Stream lines in a goroutine. Parse events for both the UI log and report.
	// Report gets human-readable parsed lines; raw JSON is only kept if no parse succeeds.
	scanDone := make(chan struct{})
	go func() {
		defer close(scanDone)
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 1024*1024), 1024*1024) // 1MB for large JSON lines
		for scanner.Scan() {
			raw := scanner.Text()
			if line := parseStreamEvent(raw); line != "" {
				lb.Append(line)
				fmt.Fprintln(reportFile, line)
			}
		}
	}()

	// 11. Wait for process; when it exits, the pipe's write end closes and the
	// scanner goroutine unblocks. cmd.WaitDelay ensures we don't block forever if
	// the process is stubborn.
	waitErr := cmd.Wait()
	<-scanDone

	// Determine exit code and status
	exitCode := 0
	status := "done"

	if waitErr != nil {
		// Check for timeout (context deadline exceeded)
		if execCtx.Err() == context.DeadlineExceeded {
			exitCode = 124
			status = "failed"
		} else if exitErr, ok := waitErr.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
			if exitCode != 0 {
				status = "failed"
			}
		} else {
			// Some other error (e.g. context cancelled)
			status = "failed"
		}
	}

	// 12. Write report footer
	finishedAt := time.Now().UTC().Format(time.RFC3339)
	footer := fmt.Sprintf("\nFinished: %s\nStatus: %s\nExit code: %d\n", finishedAt, status, exitCode)
	if workDir != plan.Project {
		footer += fmt.Sprintf("Worktree: %s\n", workDir)
	}
	fmt.Fprint(reportFile, footer)
	lb.Append(strings.TrimSpace(footer))

	// Update frontmatter
	_ = setFrontmatterField(plan.Path, "status", status)
	_ = setFrontmatterField(plan.Path, "finished_at", finishedAt)
	_ = setFrontmatterField(plan.Path, "exit_code", strconv.Itoa(exitCode))
	_ = setFrontmatterField(plan.Path, "report", reportPath)

	// Log worktree location for inspection
	if workDir != plan.Project {
		lb.Append(fmt.Sprintf("[dispatch] worktree preserved at %s for inspection", workDir))
	}

	// 13. Clear running
	e.mu.Lock()
	e.running = nil
	e.mu.Unlock()

	return nil
}

