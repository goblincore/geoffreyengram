package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
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

// defaultSetupTimeout bounds a worktree setup command when none is configured.
const defaultSetupTimeout = 5 * time.Minute

// maxPlaintextReportBytes bounds non-JSON stdout kept as a report fallback.
const maxPlaintextReportBytes = 64 * 1024

// maxStreamEventBytes bounds memory retained while assembling one JSONL event.
// Longer lines are still fully drained from stdout.
const maxStreamEventBytes = 1024 * 1024

func capturePlanOutput(stdout io.Reader, lb *LogBuffer, report io.Writer) error {
	reader := bufio.NewReaderSize(stdout, 64*1024)
	lineBuffer := make([]byte, 0, 64*1024)
	plaintextReportBytes := 0
	jsonValidator := newJSONLineValidator()
	oversized := false

	for {
		fragment, readErr := reader.ReadSlice('\n')
		jsonValidator.write(fragment)
		if !oversized {
			remaining := maxStreamEventBytes - len(lineBuffer)
			if len(fragment) > remaining {
				lineBuffer = append(lineBuffer, fragment[:remaining]...)
				oversized = true
			} else {
				lineBuffer = append(lineBuffer, fragment...)
			}
		}

		if readErr == bufio.ErrBufferFull {
			continue
		}

		if len(lineBuffer) > 0 {
			raw := strings.TrimSuffix(string(lineBuffer), "\n")
			raw = strings.TrimSuffix(raw, "\r")

			line := ""
			if !oversized {
				line = parseStreamEvent(raw)
			}
			if line != "" {
				lb.Append(line)
				fmt.Fprintln(report, line)
			} else if !oversized && !json.Valid([]byte(raw)) {
				appendPlaintextReport(report, raw, &plaintextReportBytes)
			} else if oversized && !jsonValidator.isValid() {
				appendPlaintextReport(report, raw, &plaintextReportBytes)
			}
		}

		lineBuffer = lineBuffer[:0]
		jsonValidator = newJSONLineValidator()
		oversized = false

		switch readErr {
		case nil:
			continue
		case io.EOF:
			return nil
		default:
			return readErr
		}
	}
}

func appendPlaintextReport(report io.Writer, raw string, written *int) {
	if *written >= maxPlaintextReportBytes {
		return
	}

	remaining := maxPlaintextReportBytes - *written
	if len(raw)+1 > remaining {
		raw = raw[:remaining-1]
	}
	fmt.Fprintln(report, raw)
	*written += len(raw) + 1
}

// encoding/json uses the same nesting limit when deciding whether JSON is
// valid. Keeping the streaming validator aligned prevents classification from
// changing merely because a line crossed maxStreamEventBytes.
const maxJSONNestingDepth = 10000

type jsonLexMode uint8

const (
	jsonLexNormal jsonLexMode = iota
	jsonLexString
	jsonLexNumber
	jsonLexLiteral
)

type jsonFrameState uint8

const (
	jsonObjectKeyOrEnd jsonFrameState = iota
	jsonObjectKey
	jsonObjectColon
	jsonObjectValue
	jsonObjectCommaOrEnd
	jsonArrayValueOrEnd
	jsonArrayValue
	jsonArrayCommaOrEnd
)

type jsonNumberState uint8

const (
	jsonNumberSign jsonNumberState = iota
	jsonNumberZero
	jsonNumberInteger
	jsonNumberDot
	jsonNumberFraction
	jsonNumberExponent
	jsonNumberExponentSign
	jsonNumberExponentDigits
)

type jsonFrame struct {
	kind  byte
	state jsonFrameState
}

// jsonLineValidator validates one JSON value incrementally without retaining
// payload text. Its fixed stack keeps memory bounded even for hostile output.
type jsonLineValidator struct {
	valid           bool
	nestingExceeded bool
	rootComplete    bool
	frames          [maxJSONNestingDepth]jsonFrame
	depth           int
	mode            jsonLexMode
	stringKey       bool
	escaped         bool
	unicodeLeft     uint8
	numberState     jsonNumberState
	literal         string
	literalIndex    int
}

func newJSONLineValidator() jsonLineValidator {
	return jsonLineValidator{valid: true}
}

func (v *jsonLineValidator) write(data []byte) {
	for _, b := range data {
		if !v.valid {
			return
		}
		switch v.mode {
		case jsonLexNormal:
			v.writeNormal(b)
		case jsonLexString:
			v.writeString(b)
		case jsonLexNumber:
			v.writeNumber(b)
		case jsonLexLiteral:
			v.writeLiteral(b)
		}
	}
}

func (v *jsonLineValidator) isValid() bool {
	if v.nestingExceeded || !v.valid {
		return false
	}
	if v.mode == jsonLexNumber {
		if !v.numberCanEnd() {
			return false
		}
		v.mode = jsonLexNormal
		v.completeValue()
	} else if v.mode != jsonLexNormal {
		return false
	}
	return v.valid && v.rootComplete && v.depth == 0
}

func (v *jsonLineValidator) writeNormal(b byte) {
	if isJSONWhitespace(b) {
		return
	}
	if v.depth == 0 {
		if v.rootComplete {
			v.valid = false
			return
		}
		v.startValue(b)
		return
	}

	frame := &v.frames[v.depth-1]
	switch frame.state {
	case jsonObjectKeyOrEnd:
		if b == '}' {
			v.closeContainer('{')
		} else if b == '"' {
			v.startString(true)
		} else {
			v.valid = false
		}
	case jsonObjectKey:
		if b == '"' {
			v.startString(true)
		} else {
			v.valid = false
		}
	case jsonObjectColon:
		if b == ':' {
			frame.state = jsonObjectValue
		} else {
			v.valid = false
		}
	case jsonObjectValue:
		v.startValue(b)
	case jsonObjectCommaOrEnd:
		switch b {
		case ',':
			frame.state = jsonObjectKey
		case '}':
			v.closeContainer('{')
		default:
			v.valid = false
		}
	case jsonArrayValueOrEnd:
		if b == ']' {
			v.closeContainer('[')
		} else {
			v.startValue(b)
		}
	case jsonArrayValue:
		v.startValue(b)
	case jsonArrayCommaOrEnd:
		switch b {
		case ',':
			frame.state = jsonArrayValue
		case ']':
			v.closeContainer('[')
		default:
			v.valid = false
		}
	}
}

func (v *jsonLineValidator) startValue(b byte) {
	switch {
	case b == '{':
		v.pushContainer('{', jsonObjectKeyOrEnd)
	case b == '[':
		v.pushContainer('[', jsonArrayValueOrEnd)
	case b == '"':
		v.startString(false)
	case b == 't':
		v.startLiteral("true")
	case b == 'f':
		v.startLiteral("false")
	case b == 'n':
		v.startLiteral("null")
	case b == '-':
		v.mode = jsonLexNumber
		v.numberState = jsonNumberSign
	case b == '0':
		v.mode = jsonLexNumber
		v.numberState = jsonNumberZero
	case b >= '1' && b <= '9':
		v.mode = jsonLexNumber
		v.numberState = jsonNumberInteger
	default:
		v.valid = false
	}
}

func (v *jsonLineValidator) pushContainer(kind byte, state jsonFrameState) {
	if v.depth == len(v.frames) {
		v.nestingExceeded = true
		v.valid = false
		return
	}
	v.frames[v.depth] = jsonFrame{kind: kind, state: state}
	v.depth++
}

func (v *jsonLineValidator) closeContainer(kind byte) {
	if v.depth == 0 || v.frames[v.depth-1].kind != kind {
		v.valid = false
		return
	}
	v.depth--
	v.completeValue()
}

func (v *jsonLineValidator) completeValue() {
	if v.depth == 0 {
		if v.rootComplete {
			v.valid = false
			return
		}
		v.rootComplete = true
		return
	}

	frame := &v.frames[v.depth-1]
	switch frame.state {
	case jsonObjectValue:
		frame.state = jsonObjectCommaOrEnd
	case jsonArrayValueOrEnd, jsonArrayValue:
		frame.state = jsonArrayCommaOrEnd
	default:
		v.valid = false
	}
}

func (v *jsonLineValidator) startString(key bool) {
	v.mode = jsonLexString
	v.stringKey = key
	v.escaped = false
	v.unicodeLeft = 0
}

func (v *jsonLineValidator) writeString(b byte) {
	if v.unicodeLeft > 0 {
		if !isHexDigit(b) {
			v.valid = false
			return
		}
		v.unicodeLeft--
		return
	}
	if v.escaped {
		v.escaped = false
		if b == 'u' {
			v.unicodeLeft = 4
			return
		}
		if !strings.ContainsRune(`"\/bfnrt`, rune(b)) {
			v.valid = false
		}
		return
	}

	switch {
	case b == '\\':
		v.escaped = true
	case b == '"':
		v.mode = jsonLexNormal
		if v.stringKey {
			if v.depth == 0 {
				v.valid = false
				return
			}
			v.frames[v.depth-1].state = jsonObjectColon
		} else {
			v.completeValue()
		}
	case b < 0x20:
		v.valid = false
	}
}

func (v *jsonLineValidator) startLiteral(literal string) {
	v.mode = jsonLexLiteral
	v.literal = literal
	v.literalIndex = 1
}

func (v *jsonLineValidator) writeLiteral(b byte) {
	if v.literalIndex >= len(v.literal) || b != v.literal[v.literalIndex] {
		v.valid = false
		return
	}
	v.literalIndex++
	if v.literalIndex == len(v.literal) {
		v.mode = jsonLexNormal
		v.completeValue()
	}
}

func (v *jsonLineValidator) writeNumber(b byte) {
	switch v.numberState {
	case jsonNumberSign:
		if b == '0' {
			v.numberState = jsonNumberZero
		} else if b >= '1' && b <= '9' {
			v.numberState = jsonNumberInteger
		} else {
			v.valid = false
		}
	case jsonNumberZero:
		v.writeNumberSuffix(b)
	case jsonNumberInteger:
		if b >= '0' && b <= '9' {
			return
		}
		v.writeNumberSuffix(b)
	case jsonNumberDot:
		if b >= '0' && b <= '9' {
			v.numberState = jsonNumberFraction
		} else {
			v.valid = false
		}
	case jsonNumberFraction:
		if b >= '0' && b <= '9' {
			return
		}
		if b == 'e' || b == 'E' {
			v.numberState = jsonNumberExponent
		} else {
			v.finishNumber(b)
		}
	case jsonNumberExponent:
		if b == '+' || b == '-' {
			v.numberState = jsonNumberExponentSign
		} else if b >= '0' && b <= '9' {
			v.numberState = jsonNumberExponentDigits
		} else {
			v.valid = false
		}
	case jsonNumberExponentSign:
		if b >= '0' && b <= '9' {
			v.numberState = jsonNumberExponentDigits
		} else {
			v.valid = false
		}
	case jsonNumberExponentDigits:
		if b >= '0' && b <= '9' {
			return
		}
		v.finishNumber(b)
	}
}

func (v *jsonLineValidator) writeNumberSuffix(b byte) {
	switch b {
	case '.':
		v.numberState = jsonNumberDot
	case 'e', 'E':
		v.numberState = jsonNumberExponent
	default:
		v.finishNumber(b)
	}
}

func (v *jsonLineValidator) finishNumber(delimiter byte) {
	v.mode = jsonLexNormal
	v.completeValue()
	if v.valid {
		v.writeNormal(delimiter)
	}
}

func (v *jsonLineValidator) numberCanEnd() bool {
	switch v.numberState {
	case jsonNumberZero, jsonNumberInteger, jsonNumberFraction, jsonNumberExponentDigits:
		return true
	default:
		return false
	}
}

func isJSONWhitespace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\r' || b == '\n'
}

func isHexDigit(b byte) bool {
	return b >= '0' && b <= '9' || b >= 'a' && b <= 'f' || b >= 'A' && b <= 'F'
}

// resolveSetupCommand returns the provisioning command to run in a fresh
// worktree before the agent starts. The per-task "setup:" field takes
// precedence over the project-wide DEFAULT_SETUP_CMD. An empty result means
// "no setup": either nothing is configured, or it is explicitly disabled with
// "none"/"skip" (case-insensitive) so a task can opt out of an inherited default.
func resolveSetupCommand(plan *Plan, cfg *DispatchConfig) string {
	cmd := strings.TrimSpace(plan.Setup)
	if cmd == "" {
		cmd = strings.TrimSpace(cfg.DefaultSetupCmd)
	}
	if cmd == "" || strings.EqualFold(cmd, "none") || strings.EqualFold(cmd, "skip") {
		return ""
	}
	return cmd
}

// resolveSetupTimeout returns the max duration for the setup command, applying
// the per-task override, then the project default, then a built-in fallback.
func resolveSetupTimeout(plan *Plan, cfg *DispatchConfig) time.Duration {
	for _, candidate := range []string{plan.SetupTimeout, cfg.DefaultSetupTimeout} {
		if d := parseMaxRuntime(candidate); d > 0 {
			return d
		}
	}
	return defaultSetupTimeout
}

// runSetup executes the worktree provisioning command via "sh -c", streaming
// combined stdout/stderr into the task's LogBuffer (and report, if non-nil)
// with a "[dispatch][setup]" prefix. It returns the command's exit code (124
// on timeout, -1 if the process could not run) and a non-nil error on failure.
func (e *Engine) runSetup(ctx context.Context, setupCmd, workDir string, env []string, timeout time.Duration, lb *LogBuffer, report io.Writer) (int, error) {
	sctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(sctx, "sh", "-c", setupCmd)
	cmd.Dir = workDir
	cmd.Env = env
	cmd.WaitDelay = 1 * time.Second

	pr, pw, err := os.Pipe()
	if err != nil {
		return -1, err
	}
	cmd.Stdout = pw
	cmd.Stderr = pw

	if err := cmd.Start(); err != nil {
		pw.Close()
		pr.Close()
		return -1, err
	}
	// Close the parent's write end so the scanner sees EOF when the child exits.
	pw.Close()

	scanDone := make(chan struct{})
	go func() {
		defer close(scanDone)
		scanner := bufio.NewScanner(pr)
		scanner.Buffer(make([]byte, 256*1024), 256*1024)
		for scanner.Scan() {
			line := "[dispatch][setup] " + scanner.Text()
			lb.Append(line)
			if report != nil {
				fmt.Fprintln(report, line)
			}
		}
	}()

	waitErr := cmd.Wait()
	<-scanDone
	pr.Close()

	if waitErr != nil {
		if sctx.Err() == context.DeadlineExceeded {
			return 124, fmt.Errorf("setup command timed out after %s", timeout)
		}
		if exitErr, ok := waitErr.(*exec.ExitError); ok {
			return exitErr.ExitCode(), fmt.Errorf("setup command exited %d", exitErr.ExitCode())
		}
		return -1, waitErr
	}
	return 0, nil
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

	// 6. Register in running map; deregister on completion
	e.mu.Lock()
	e.running[plan.Name] = &RunningTask{Plan: plan, Cancel: cancel}
	e.mu.Unlock()

	defer func() {
		e.mu.Lock()
		delete(e.running, plan.Name)
		e.mu.Unlock()
	}()

	// 7. Create a git worktree for isolation
	workDir := plan.Project // fallback: run in project dir
	setupRan := false       // whether worktree provisioning (setup cmd) ran successfully
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

	// Resolve the base commit for the worktree:
	// - If depends_on has entries, branch from the first dependency's branch
	// - Otherwise branch from HEAD (main)
	startPoint := "HEAD"
	var depBranches []string
	if len(plan.DependsOn) > 0 {
		for _, dep := range plan.DependsOn {
			depName := strings.TrimSuffix(dep, ".md")
			depPlan, err := parsePlanFile(filepath.Join(e.Config.PlansDir, depName+".md"))
			if err != nil {
				continue
			}
			depBranch := depPlan.Branch
			if depBranch == "" {
				depBranch = "dispatch/" + depName
			}
			depBranches = append(depBranches, depBranch)
		}
		if len(depBranches) > 0 {
			startPoint = depBranches[0]
			lb.Append(fmt.Sprintf("[dispatch] branching from dependency: %s", startPoint))
		}
	}

	wtCmd := exec.Command("git", "-C", plan.Project, "worktree", "add", worktreePath, "-b", branch, startPoint)
	if out, err := wtCmd.CombinedOutput(); err != nil {
		lb.Append(fmt.Sprintf("[dispatch] worktree creation failed: %s — running in project dir", strings.TrimSpace(string(out))))
	} else {
		workDir = worktreePath
		lb.Append(fmt.Sprintf("[dispatch] created worktree at %s (branch: %s from %s)", worktreePath, branch, startPoint))

		// If multiple dependencies, merge the remaining ones into the worktree
		if len(depBranches) > 1 {
			for _, depBranch := range depBranches[1:] {
				mergeCmd := exec.Command("git", "-C", worktreePath, "merge", depBranch, "--no-edit",
					"-m", fmt.Sprintf("dispatch: merge dependency %s", depBranch))
				if mergeOut, mergeErr := mergeCmd.CombinedOutput(); mergeErr != nil {
					lb.Append(fmt.Sprintf("[dispatch] WARNING: merge of %s failed: %s — may need manual resolution",
						depBranch, strings.TrimSpace(string(mergeOut))))
				} else {
					lb.Append(fmt.Sprintf("[dispatch] merged dependency branch: %s", depBranch))
				}
			}
		}

		// Symlink .claude/ from main repo so Claude Code finds CLAUDE.md
		claudeDir := filepath.Join(plan.Project, ".claude")
		if _, err := os.Stat(claudeDir); err == nil {
			os.Symlink(claudeDir, filepath.Join(worktreePath, ".claude"))
		}

		// Provision the worktree before the agent runs (e.g. install deps). A fresh
		// worktree has no gitignored artifacts like node_modules, so without this the
		// agent wastes a turn discovering a bare checkout and reinstalls on every task.
		if setupCmd := resolveSetupCommand(plan, e.Config); setupCmd != "" {
			timeout := resolveSetupTimeout(plan, e.Config)
			lb.Append(fmt.Sprintf("[dispatch] running setup (timeout %s): %s", timeout, setupCmd))
			if code, serr := e.runSetup(execCtx, setupCmd, workDir, envPairs, timeout, lb, nil); serr != nil {
				// Provisioning failed: the worktree can't pass verification anyway, so
				// fail fast before spending agent tokens. Preserve it for inspection.
				finishedAt := time.Now().UTC().Format(time.RFC3339)
				lb.Append(fmt.Sprintf("[dispatch] setup failed (exit %d): %v — aborting before agent run; worktree preserved at %s", code, serr, workDir))
				_ = setFrontmatterField(plan.Path, "status", "failed")
				_ = setFrontmatterField(plan.Path, "finished_at", finishedAt)
				_ = setFrontmatterField(plan.Path, "exit_code", strconv.Itoa(code))
				return fmt.Errorf("setup command failed for plan %s: %w", plan.Name, serr)
			}
			lb.Append("[dispatch] setup complete")
			setupRan = true
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
	// Inject worktree orientation so the agent knows its environment
	if workDir != plan.Project {
		worktreeNote := fmt.Sprintf(`[Environment: Git Worktree]
You are running in an isolated git worktree, NOT the main repository.
- Worktree path: %s
- Main repo: %s
- Branch: %s
All files you read/edit are in the worktree. Use absolute paths based on the worktree path above.
The worktree is a full copy of the repo — your edits will not affect the main repo.
`, workDir, plan.Project, branch)
		if setupRan {
			worktreeNote += "Dependencies were already provisioned for this worktree by the dispatch setup step — do NOT reinstall them.\n"
		}
		prompt = worktreeNote + "\n" + prompt
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

	// Stream lines in a goroutine. The reader drains arbitrarily long lines while
	// retaining bounded buffers for event parsing and plaintext report fallback.
	scanDone := make(chan error, 1)
	go func() {
		scanDone <- capturePlanOutput(stdout, lb, reportFile)
	}()

	// 11. Wait for process; when it exits, the pipe's write end closes and the
	// capture goroutine unblocks. cmd.WaitDelay ensures we don't block forever if
	// the process is stubborn.
	waitErr := cmd.Wait()
	if scanErr := <-scanDone; scanErr != nil {
		msg := fmt.Sprintf("[dispatch] stdout capture failed: %v", scanErr)
		lb.Append(msg)
		fmt.Fprintln(reportFile, msg)
	}

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

	// Auto-commit unstaged changes in the worktree so that depends_on plans
	// can branch from this plan's work. Without this, changes are lost when
	// the worktree is cleaned up, and dependent plans get a clean branch.
	if workDir != plan.Project {
		// Check for unstaged/untracked changes
		statusCmd := exec.Command("git", "-C", workDir, "status", "--porcelain")
		statusOut, statusErr := statusCmd.CombinedOutput()
		statusStr := strings.TrimSpace(string(statusOut))
		if statusErr != nil {
			msg := fmt.Sprintf("[dispatch] git status failed: %s", statusStr)
			lb.Append(msg)
			fmt.Fprintf(reportFile, "%s\n", msg)
		} else if len(statusStr) > 0 {
			// Stage all changes
			addCmd := exec.Command("git", "-C", workDir, "add", "-A")
			if addOut, addErr := addCmd.CombinedOutput(); addErr != nil {
				msg := fmt.Sprintf("[dispatch] git add failed: %s", strings.TrimSpace(string(addOut)))
				lb.Append(msg)
				fmt.Fprintf(reportFile, "%s\n", msg)
			} else {
				// Commit with plan metadata
				commitMsg := fmt.Sprintf("dispatch(%s): %s\n\nStatus: %s\nModel: %s\nHarness: %s",
					plan.Name, plan.Title, status, plan.Model, plan.Harness)
				commitCmd := exec.Command("git", "-C", workDir, "commit", "-m", commitMsg)
				commitOut, commitErr := commitCmd.CombinedOutput()
				if commitErr != nil {
					msg := fmt.Sprintf("[dispatch] auto-commit failed: %s", strings.TrimSpace(string(commitOut)))
					lb.Append(msg)
					fmt.Fprintf(reportFile, "%s\n", msg)
				} else {
					msg := fmt.Sprintf("[dispatch] auto-committed changes on branch %s", branch)
					lb.Append(msg)
					fmt.Fprintf(reportFile, "%s\n", msg)
				}
			}
		} else {
			msg := "[dispatch] no changes to commit in worktree"
			lb.Append(msg)
			fmt.Fprintf(reportFile, "%s\n", msg)
		}
	}

	// Worktree cleanup: remove directory on success, preserve on failure
	if workDir != plan.Project {
		if status == "done" && e.Config.AutoCleanupWorktrees {
			_, rmErr := exec.Command("git", "-C", plan.Project, "worktree", "remove", workDir).CombinedOutput()
			if rmErr != nil {
				// Retry with --force (handles case where commit failed and changes remain)
				rmOut2, rmErr2 := exec.Command("git", "-C", plan.Project, "worktree", "remove", "--force", workDir).CombinedOutput()
				if rmErr2 != nil {
					msg := fmt.Sprintf("[dispatch] worktree cleanup failed (even with --force): %s", strings.TrimSpace(string(rmOut2)))
					lb.Append(msg)
					fmt.Fprintf(reportFile, "%s\n", msg)
				} else {
					msg := fmt.Sprintf("[dispatch] worktree force-cleaned (branch %s preserved, but uncommitted changes were lost)", branch)
					lb.Append(msg)
					fmt.Fprintf(reportFile, "%s\n", msg)
				}
			} else {
				msg := fmt.Sprintf("[dispatch] worktree cleaned up (branch %s preserved for merge)", branch)
				lb.Append(msg)
				fmt.Fprintf(reportFile, "%s\n", msg)
			}
		} else {
			msg := fmt.Sprintf("[dispatch] worktree preserved at %s for inspection", workDir)
			lb.Append(msg)
			fmt.Fprintf(reportFile, "%s\n", msg)
		}
	}

	return nil
}
