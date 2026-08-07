package harness

import (
	"encoding/json"
	"fmt"
	"strings"
)

type codexAdapter struct{}

type codexPayload struct {
	SessionID     string          `json:"session_id"`
	CWD           string          `json:"cwd"`
	HookEventName string          `json:"hook_event_name"`
	Prompt        string          `json:"prompt"`
	ToolName      string          `json:"tool_name"`
	ToolInput     json.RawMessage `json:"tool_input"`
}

type codexToolInput struct {
	Command string `json:"command"`
}

func (codexAdapter) Name() string {
	return "codex"
}

func (codexAdapter) Decode(raw []byte) ([]Event, error) {
	var payload codexPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("decode Codex payload: %w", err)
	}

	switch payload.HookEventName {
	case "SessionStart":
		event := nativeEvent("codex", payload.CWD, payload.SessionID, EventSessionStart)
		return []Event{event}, nil
	case "UserPromptSubmit":
		event := nativeEvent("codex", payload.CWD, payload.SessionID, EventPrompt)
		event.Prompt = payload.Prompt
		return []Event{event}, nil
	}

	phase, ok := nativeToolPhase(payload.HookEventName)
	if !ok || payload.ToolName != "apply_patch" {
		return []Event{}, nil
	}
	if len(payload.ToolInput) == 0 {
		return []Event{}, nil
	}
	var toolInput codexToolInput
	if err := json.Unmarshal(payload.ToolInput, &toolInput); err != nil {
		return nil, fmt.Errorf("decode Codex apply_patch input: %w", err)
	}
	paths := NormalizePaths(payload.CWD, ExtractPatchPaths(toolInput.Command))
	if len(paths) == 0 {
		return []Event{}, nil
	}

	event := nativeEvent("codex", payload.CWD, payload.SessionID, EventFileWrite)
	event.Files = paths
	event.Tool = ToolRef{Name: payload.ToolName, Phase: phase}
	return []Event{event}, nil
}

func (codexAdapter) Encode(event Event, response Response) ([]byte, error) {
	return encodeNativeResponse(event, response)
}

func ExtractPatchPaths(patch string) []string {
	prefixes := [...]string{
		"*** Add File:",
		"*** Update File:",
		"*** Delete File:",
	}
	var paths []string
	for _, line := range strings.Split(patch, "\n") {
		line = strings.TrimSuffix(line, "\r")
		for _, prefix := range prefixes {
			path, ok := strings.CutPrefix(line, prefix)
			if !ok {
				continue
			}
			path = strings.TrimSpace(path)
			if path != "" {
				paths = append(paths, path)
			}
			break
		}
	}
	return paths
}
