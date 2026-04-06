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
func parseStreamEvent(raw string) string {
	var ev struct {
		Type    string `json:"type"`
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
	if ev.Type != "assistant" {
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
	prompt := plan.Body
	if e.Config.Preamble != "" {
		prompt = e.Config.Preamble + "\n\n" + prompt
	}
	tools := plan.AllowedTools
	if tools == "" {
		tools = "Bash"
	}

	harness := plan.Harness
	if harness == "" {
		harness = "claude"
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

	// Stream lines in a goroutine. Parse stream-json events for the UI log,
	// write raw JSON to the report file.
	scanDone := make(chan struct{})
	go func() {
		defer close(scanDone)
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 1024*1024), 1024*1024) // 1MB for large JSON lines
		for scanner.Scan() {
			raw := scanner.Text()
			fmt.Fprintln(reportFile, raw) // raw to report
			if line := parseStreamEvent(raw); line != "" {
				lb.Append(line)
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

// CancelRunning cancels the currently running task if one exists.
// Returns true if a task was cancelled, false if nothing was running.
func (e *Engine) CancelRunning() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.running == nil {
		return false
	}
	e.running.Cancel()
	return true
}
