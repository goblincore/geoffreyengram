package harness

import (
	"encoding/json"
	"fmt"
)

type claudeAdapter struct{}

type claudePayload struct {
	SessionID     string          `json:"session_id"`
	CWD           string          `json:"cwd"`
	HookEventName string          `json:"hook_event_name"`
	Prompt        string          `json:"prompt"`
	ToolName      string          `json:"tool_name"`
	ToolInput     json.RawMessage `json:"tool_input"`
}

type claudeToolInput struct {
	FilePath string `json:"file_path"`
}

func (claudeAdapter) Name() string {
	return "claude"
}

func (claudeAdapter) Decode(raw []byte) ([]Event, error) {
	var payload claudePayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("decode Claude payload: %w", err)
	}

	switch payload.HookEventName {
	case "SessionStart":
		event := nativeEvent("claude", payload.CWD, payload.SessionID, EventSessionStart)
		return []Event{event}, nil
	case "UserPromptSubmit":
		event := nativeEvent("claude", payload.CWD, payload.SessionID, EventPrompt)
		event.Prompt = payload.Prompt
		return []Event{event}, nil
	}

	phase, ok := nativeToolPhase(payload.HookEventName)
	if !ok {
		return []Event{}, nil
	}

	kind := EventKind("")
	switch payload.ToolName {
	case "Read":
		kind = EventFileRead
	case "Edit", "Write":
		kind = EventFileWrite
	default:
		return []Event{}, nil
	}

	if len(payload.ToolInput) == 0 {
		return []Event{}, nil
	}
	var toolInput claudeToolInput
	if err := json.Unmarshal(payload.ToolInput, &toolInput); err != nil {
		return nil, fmt.Errorf("decode Claude %s input: %w", payload.ToolName, err)
	}
	paths := NormalizePaths(payload.CWD, []string{toolInput.FilePath})
	if len(paths) == 0 {
		return []Event{}, nil
	}

	event := nativeEvent("claude", payload.CWD, payload.SessionID, kind)
	event.Files = paths
	event.Tool = ToolRef{Name: payload.ToolName, Phase: phase}
	return []Event{event}, nil
}

func (claudeAdapter) Encode(event Event, response Response) ([]byte, error) {
	return encodeNativeResponse(event, response)
}
