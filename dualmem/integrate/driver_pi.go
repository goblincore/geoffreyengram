package integrate

import (
	"bytes"
	"context"
	"path/filepath"
)

type piDriver struct {
	promptSupported bool
}

func (piDriver) Name() string { return "pi" }

func (driver piDriver) Detect(_ context.Context, home string) (Detection, error) {
	extensionPath := filepath.Join(home, ".pi", "agent", "extensions", "dualmem.ts")
	instructionsPath := filepath.Join(home, ".pi", "agent", "AGENTS.md")
	present := harnessPresent(filepath.Join(home, ".pi", "agent"), extensionPath, instructionsPath)
	extension, err := readFileState(extensionPath)
	if err != nil {
		return Detection{}, err
	}
	managed := extension.exists && (bytes.Equal(extension.bytes, renderedPiExtension(true)) || bytes.Equal(extension.bytes, renderedPiExtension(false)))
	instructions, err := readFileState(instructionsPath)
	if err != nil {
		return Detection{}, err
	}
	managed = managed || instructions.exists && containsManagedInstructions(instructions.bytes)
	capabilities := []Capability{"session_start", "file_read", "file_write", "session_end", "tool"}
	if driver.promptSupported {
		capabilities = append(capabilities, "prompt")
	}
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
			case bytes.Equal(current.bytes, renderedPiExtension(true)):
				extension = Change{
					Path: extensionPath, Action: ActionDelete, Mode: current.mode.Perm(),
					Before:      append([]byte(nil), current.bytes...),
					DeleteProof: ownedAssetDeleteProof(renderedPiExtension(true)),
				}
			case bytes.Equal(current.bytes, renderedPiExtension(false)):
				extension = Change{
					Path: extensionPath, Action: ActionDelete, Mode: current.mode.Perm(),
					Before:      append([]byte(nil), current.bytes...),
					DeleteProof: ownedAssetDeleteProof(renderedPiExtension(false)),
				}
			default:
				extension = unchangedFile(extensionPath, current)
			}
			changes = append(changes, extension)
		}
	} else {
		changes = append(changes, changeForContent(extensionPath, current, renderedPiExtension(driver.promptSupported), 0o700))
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
