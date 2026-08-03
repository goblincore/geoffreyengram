package integrate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"path/filepath"
	"reflect"
)

type claudeDriver struct{}

type hookSpec struct {
	event   string
	matcher string
	adapter string
}

var claudeHookSpecs = []hookSpec{
	{event: "SessionStart", adapter: "claude"},
	{event: "UserPromptSubmit", adapter: "claude"},
	{event: "PreToolUse", matcher: "Read", adapter: "claude"},
	{event: "PostToolUse", matcher: "Edit|Write", adapter: "claude"},
}

func (claudeDriver) Name() string { return "claude" }

func (claudeDriver) Detect(_ context.Context, home string) (Detection, error) {
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	instructionsPath := filepath.Join(home, ".claude", "CLAUDE.md")
	present := harnessPresent(filepath.Join(home, ".claude"), settingsPath, instructionsPath)
	managed, err := hookDocumentManaged(settingsPath, claudeHookSpecs)
	if err != nil {
		return Detection{}, err
	}
	instructions, err := readFileState(instructionsPath)
	if err != nil {
		return Detection{}, err
	}
	managed = managed || instructions.exists && containsManagedInstructions(instructions.bytes)
	return Detection{
		Present: present, Managed: managed,
		Capabilities: []Capability{"session_start", "prompt", "file_read", "file_write"},
	}, nil
}

func (claudeDriver) Plan(_ context.Context, request DriverRequest) ([]Change, error) {
	settingsPath := filepath.Join(request.Home, ".claude", "settings.json")
	instructionsPath := filepath.Join(request.Home, ".claude", "CLAUDE.md")
	var changes []Change
	settings, ok, err := planHookDocument(settingsPath, claudeHookSpecs, request.Uninstall)
	if err != nil {
		return nil, err
	}
	if ok {
		changes = append(changes, settings)
	}
	instructions, ok, err := planManagedInstructions(instructionsPath, request.Uninstall)
	if err != nil {
		return nil, err
	}
	if ok {
		changes = append(changes, instructions)
	}
	return changes, nil
}

func planHookDocument(path string, specs []hookSpec, uninstall bool) (Change, bool, error) {
	current, err := readFileState(path)
	if err != nil {
		return Change{}, false, err
	}
	if uninstall && !current.exists {
		return Change{}, false, nil
	}
	raw := current.bytes
	if !current.exists {
		raw = []byte("{}\n")
	}
	merged, changed, err := mergeHookDocument(raw, specs, uninstall)
	if err != nil {
		return Change{}, false, err
	}
	mode := fs.FileMode(0o600)
	if current.exists {
		mode = current.mode.Perm()
	}
	if !changed && current.exists {
		return changeForContent(path, current, current.bytes, mode), true, nil
	}
	return changeForContent(path, current, merged, mode), true, nil
}

func hookDocumentManaged(path string, specs []hookSpec) (bool, error) {
	current, err := readFileState(path)
	if err != nil || !current.exists {
		return false, err
	}
	document, hooks, err := decodeHookDocument(current.bytes)
	_ = document
	if err != nil {
		return false, err
	}
	for _, spec := range specs {
		groups, err := decodeHookGroups(hooks[spec.event])
		if err != nil {
			return false, err
		}
		managed := managedHookGroup(spec)
		for _, group := range groups {
			if jsonSemanticEqual(group, managed) {
				return true, nil
			}
		}
	}
	return false, nil
}

func mergeHookDocument(raw []byte, specs []hookSpec, uninstall bool) ([]byte, bool, error) {
	document, hooks, err := decodeHookDocument(raw)
	if err != nil {
		return nil, false, err
	}
	changed := false
	for _, spec := range specs {
		groups, err := decodeHookGroups(hooks[spec.event])
		if err != nil {
			return nil, false, fmt.Errorf("decode %s hooks: %w", spec.event, err)
		}
		managed := managedHookGroup(spec)
		filtered := make([]json.RawMessage, 0, len(groups)+1)
		for _, group := range groups {
			if jsonSemanticEqual(group, managed) {
				changed = true
				continue
			}
			if !uninstall {
				stripped, keep, strippedLegacy, err := stripLegacyCredentialHooks(group)
				if err != nil {
					return nil, false, err
				}
				if strippedLegacy {
					changed = true
				}
				if keep {
					filtered = append(filtered, stripped)
				}
				continue
			}
			filtered = append(filtered, group)
		}
		if uninstall {
			if len(filtered) == 0 {
				if _, exists := hooks[spec.event]; exists {
					delete(hooks, spec.event)
				}
			} else if changed {
				hooks[spec.event] = mustMarshalRaw(filtered)
			}
			continue
		}
		filtered = append(filtered, managed)
		hooks[spec.event] = mustMarshalRaw(filtered)
		changed = true
	}
	if !changed {
		return append([]byte(nil), raw...), false, nil
	}
	if len(hooks) == 0 {
		delete(document, "hooks")
	} else {
		document["hooks"] = mustMarshalRaw(hooks)
	}
	encoded, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return nil, false, err
	}
	return append(encoded, '\n'), true, nil
}

func decodeHookDocument(raw []byte) (map[string]json.RawMessage, map[string]json.RawMessage, error) {
	var document map[string]json.RawMessage
	if err := json.Unmarshal(raw, &document); err != nil {
		return nil, nil, err
	}
	if document == nil {
		return nil, nil, fmt.Errorf("hook document must be a JSON object")
	}
	hooks := make(map[string]json.RawMessage)
	if rawHooks, exists := document["hooks"]; exists {
		if err := json.Unmarshal(rawHooks, &hooks); err != nil {
			return nil, nil, fmt.Errorf("hooks must be a JSON object: %w", err)
		}
		if hooks == nil {
			return nil, nil, fmt.Errorf("hooks must be a JSON object")
		}
	}
	return document, hooks, nil
}

func decodeHookGroups(raw json.RawMessage) ([]json.RawMessage, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var groups []json.RawMessage
	if err := json.Unmarshal(raw, &groups); err != nil {
		return nil, err
	}
	return groups, nil
}

func managedHookGroup(spec hookSpec) json.RawMessage {
	hook := map[string]any{
		"type":    "command",
		"command": `"$HOME/.config/dualmem/bin/dualmem-run" hook --adapter ` + spec.adapter,
		"timeout": 10,
	}
	group := map[string]any{"hooks": []any{hook}}
	if spec.matcher != "" {
		group["matcher"] = spec.matcher
	}
	return mustMarshalRaw(group)
}

func stripLegacyCredentialHooks(group json.RawMessage) (json.RawMessage, bool, bool, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(group, &object); err != nil {
		return nil, false, false, err
	}
	rawHooks, exists := object["hooks"]
	if !exists {
		return group, true, false, nil
	}
	var hooks []json.RawMessage
	if err := json.Unmarshal(rawHooks, &hooks); err != nil {
		return nil, false, false, err
	}
	filtered := make([]json.RawMessage, 0, len(hooks))
	stripped := false
	for _, rawHook := range hooks {
		var hook struct {
			Command string `json:"command"`
		}
		if err := json.Unmarshal(rawHook, &hook); err != nil {
			return nil, false, false, err
		}
		_, legacy, err := parseLegacyCredentialCommand(hook.Command)
		if err != nil {
			return nil, false, false, fmt.Errorf("legacy credential hook is ambiguous")
		}
		if legacy {
			stripped = true
			continue
		}
		filtered = append(filtered, rawHook)
	}
	if !stripped {
		return group, true, false, nil
	}
	if len(filtered) == 0 {
		return nil, false, true, nil
	}
	object["hooks"] = mustMarshalRaw(filtered)
	return mustMarshalRaw(object), true, true, nil
}

func jsonSemanticEqual(left, right []byte) bool {
	var leftValue, rightValue any
	leftDecoder := json.NewDecoder(bytes.NewReader(left))
	leftDecoder.UseNumber()
	rightDecoder := json.NewDecoder(bytes.NewReader(right))
	rightDecoder.UseNumber()
	if leftDecoder.Decode(&leftValue) != nil || rightDecoder.Decode(&rightValue) != nil {
		return false
	}
	return reflect.DeepEqual(leftValue, rightValue)
}

func mustMarshalRaw(value any) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return raw
}
