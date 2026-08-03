package integrate

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
)

type piDriver struct{}

func (piDriver) Name() string { return "pi" }

func (driver piDriver) Detect(_ context.Context, home string) (Detection, error) {
	extensionPath := filepath.Join(home, ".pi", "agent", "extensions", "dualmem.ts")
	instructionsPath := filepath.Join(home, ".pi", "agent", "AGENTS.md")
	present := harnessPresent(filepath.Join(home, ".pi", "agent"), extensionPath, instructionsPath)
	extension, err := readFileState(extensionPath)
	if err != nil {
		return Detection{}, err
	}
	managed := extension.exists && bytes.Equal(extension.bytes, renderedPiExtension())
	instructions, err := readFileState(instructionsPath)
	if err != nil {
		return Detection{}, err
	}
	managed = managed || instructions.exists && containsManagedInstructions(instructions.bytes)
	capabilities := []Capability{"file_read", "file_write", "session_end", "tool"}
	return Detection{Present: present, Managed: managed, Capabilities: capabilities}, nil
}

func (driver piDriver) Plan(_ context.Context, request DriverRequest) ([]Change, error) {
	extensionPath := filepath.Join(request.Home, ".pi", "agent", "extensions", "dualmem.ts")
	instructionsPath := filepath.Join(request.Home, ".pi", "agent", "AGENTS.md")
	var changes []Change
	current, err := readFileState(extensionPath)
	if err != nil {
		return nil, err
	}
	if request.Uninstall {
		if current.exists {
			var extension Change
			switch {
			case bytes.Equal(current.bytes, renderedPiExtension()):
				extension = Change{
					Path: extensionPath, Action: ActionDelete, Mode: current.mode.Perm(),
					Before:      append([]byte(nil), current.bytes...),
					DeleteProof: ownedAssetDeleteProof(renderedPiExtension()),
				}
			default:
				if piExtensionUsesSharedLauncher(current.bytes) {
					return nil, fmt.Errorf("modified pi extension still depends on the shared DualMem launcher")
				}
				extension = unchangedFile(extensionPath, current)
			}
			changes = append(changes, extension)
		}
	} else {
		changes = append(changes, changeForContent(extensionPath, current, renderedPiExtension(), 0o700))
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

func piExtensionUsesSharedLauncher(raw []byte) bool {
	return bytes.Contains(raw, []byte("dualmem-run")) || bytes.Contains(raw, []byte("DUALMEM_RUN"))
}
