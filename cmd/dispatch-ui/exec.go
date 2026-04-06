package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

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

	var envPairs []string
	if baseURL != "" {
		envPairs = append(envPairs,
			"ANTHROPIC_AUTH_TOKEN="+apiKey,
			"ANTHROPIC_BASE_URL="+baseURL,
		)
	} else {
		envPairs = append(envPairs, "ANTHROPIC_API_KEY="+apiKey)
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

	// 7. Build command
	prompt := plan.Body
	tools := plan.AllowedTools
	if tools == "" {
		tools = "Bash"
	}

	claudeBin := e.Config.ClaudeBin
	if claudeBin == "" {
		claudeBin = "claude"
	}

	args := []string{
		"--bare",
		"-p", prompt,
		"--allowed-tools", tools,
		"--output-format", "text",
		"--no-session-persistence",
	}
	cmd := exec.CommandContext(execCtx, claudeBin, args...)
	// WaitDelay: after context cancellation, wait up to this long for I/O to drain
	// before forcibly closing pipes. A short value ensures we don't block forever.
	cmd.WaitDelay = 1 * time.Second

	// 8. Set working directory and environment
	cmd.Dir = plan.Project
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

	// Stream lines in a goroutine. When the process exits (or is killed by context
	// cancellation), it closes the write end of the pipe, which unblocks the scanner.
	scanDone := make(chan struct{})
	go func() {
		defer close(scanDone)
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := scanner.Text()
			fmt.Fprintln(reportFile, line)
			lb.Append(line)
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
	fmt.Fprint(reportFile, footer)
	lb.Append(strings.TrimSpace(footer))

	// Update frontmatter
	_ = setFrontmatterField(plan.Path, "status", status)
	_ = setFrontmatterField(plan.Path, "finished_at", finishedAt)
	_ = setFrontmatterField(plan.Path, "exit_code", strconv.Itoa(exitCode))
	_ = setFrontmatterField(plan.Path, "report", reportPath)

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
