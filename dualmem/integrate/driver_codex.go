package integrate

import (
	"context"
	"path/filepath"
)

type codexDriver struct{}

var codexHookSpecs = []hookSpec{
	{event: "SessionStart", adapter: "codex"},
	{event: "UserPromptSubmit", adapter: "codex"},
	{event: "PostToolUse", matcher: "apply_patch", adapter: "codex"},
}

func (codexDriver) Name() string { return "codex" }

func (codexDriver) Detect(_ context.Context, home string) (Detection, error) {
	hooksPath := filepath.Join(home, ".codex", "hooks.json")
	instructionsPath := filepath.Join(home, ".codex", "AGENTS.md")
	present := harnessPresent(filepath.Join(home, ".codex"), hooksPath, instructionsPath)
	managed, err := hookDocumentManaged(hooksPath, codexHookSpecs)
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
		Capabilities: []Capability{"session_start", "prompt", "file_write"},
	}, nil
}

func (codexDriver) Plan(_ context.Context, request DriverRequest) ([]Change, error) {
	hooksPath := filepath.Join(request.Home, ".codex", "hooks.json")
	instructionsPath := filepath.Join(request.Home, ".codex", "AGENTS.md")
	var changes []Change
	hooks, ok, err := planHookDocument(hooksPath, codexHookSpecs, request.Uninstall)
	if err != nil {
		return nil, err
	}
	if ok {
		changes = append(changes, hooks)
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
