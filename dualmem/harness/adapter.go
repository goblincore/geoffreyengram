package harness

import (
	"encoding/json"
	"fmt"
	"strings"
)

type Adapter interface {
	Name() string
	Decode(raw []byte) ([]Event, error)
	Encode(event Event, response Response) ([]byte, error)
}

type Registry struct {
	adapters map[string]Adapter
}

func NewRegistry(adapters ...Adapter) (Registry, error) {
	registry := Registry{adapters: make(map[string]Adapter, len(adapters))}
	for _, adapter := range adapters {
		if adapter == nil {
			return Registry{}, fmt.Errorf("adapter is nil")
		}
		name := strings.TrimSpace(adapter.Name())
		if name == "" {
			return Registry{}, fmt.Errorf("adapter name is blank")
		}
		if _, exists := registry.adapters[name]; exists {
			return Registry{}, fmt.Errorf("duplicate adapter name %q", name)
		}
		registry.adapters[name] = adapter
	}
	return registry, nil
}

func BuiltinAdapters() Registry {
	registry, err := NewRegistry(claudeAdapter{}, codexAdapter{})
	if err != nil {
		panic(fmt.Sprintf("register builtin adapters: %v", err))
	}
	return registry
}

func (r Registry) Get(name string) (Adapter, bool) {
	adapter, ok := r.adapters[strings.TrimSpace(name)]
	return adapter, ok
}

func nativeEvent(harness, cwd, sessionID string, kind EventKind) Event {
	return Event{
		SchemaVersion: "1.0",
		Kind:          kind,
		Harness:       harness,
		CWD:           cwd,
		SessionID:     sessionID,
	}
}

func nativeToolPhase(hookEventName string) (string, bool) {
	switch hookEventName {
	case "PreToolUse":
		return "pre", true
	case "PostToolUse":
		return "post", true
	default:
		return "", false
	}
}

func encodeNativeResponse(event Event, response Response) ([]byte, error) {
	if response.Action != ActionInjectContext || response.Context == "" {
		return json.Marshal(struct{}{})
	}

	hookEventName, err := nativeHookEventName(event)
	if err != nil {
		return nil, err
	}
	output := struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}{}
	output.HookSpecificOutput.HookEventName = hookEventName
	output.HookSpecificOutput.AdditionalContext = response.Context
	return json.Marshal(output)
}

func nativeHookEventName(event Event) (string, error) {
	switch event.Kind {
	case EventSessionStart:
		return "SessionStart", nil
	case EventPrompt:
		return "UserPromptSubmit", nil
	case EventFileRead, EventFileWrite:
		switch event.Tool.Phase {
		case "pre":
			return "PreToolUse", nil
		case "post":
			return "PostToolUse", nil
		}
	}
	return "", fmt.Errorf("cannot encode additional context for event kind %q with tool phase %q", event.Kind, event.Tool.Phase)
}
