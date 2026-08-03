package integrate

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

const (
	managedInstructionsBegin = "<!-- BEGIN DUALMEM -->"
	managedInstructionsEnd   = "<!-- END DUALMEM -->"
	piPromptHookBegin        = "  // BEGIN DUALMEM PI PROMPT HOOK\n"
	piPromptHookEnd          = "  // END DUALMEM PI PROMPT HOOK\n"
)

//go:embed assets/launcher.sh
var launcherAsset []byte

//go:embed assets/pi-extension.ts
var piExtensionAsset []byte

//go:embed assets/instructions.md
var instructionsAsset []byte

var piPromptSupportProbe = installedPiSupportsPromptHook

type builtinCommonPlanner struct{}

type fileState struct {
	exists bool
	mode   fs.FileMode
	bytes  []byte
}

func BuiltinBundle() Bundle {
	promptSupported := piPromptSupportProbe()
	return Bundle{
		Common: builtinCommonPlanner{},
		Drivers: []Driver{
			claudeDriver{},
			codexDriver{},
			piDriver{promptSupported: promptSupported},
		},
	}
}

func (builtinCommonPlanner) PlanCommon(_ context.Context, request CommonRequest) ([]Change, error) {
	envPath := filepath.Join(request.Home, ".config", "dualmem", "env")
	launcherPath := filepath.Join(request.Home, ".config", "dualmem", "bin", "dualmem-run")

	if request.Uninstall && len(request.RemainingHarnesses) == 0 {
		var changes []Change
		envChange, ok, err := planEmptyOwnedRemoval(envPath)
		if err != nil {
			return nil, err
		}
		if ok {
			changes = append(changes, envChange)
		}
		launcherChange, ok, err := planOwnedRemoval(launcherPath, launcherAsset)
		if err != nil {
			return nil, err
		}
		if ok {
			changes = append(changes, launcherChange)
		}
		return changes, nil
	}
	if !request.Uninstall && len(request.RemainingHarnesses) == 0 {
		return nil, nil
	}

	credentials := map[string]string{}
	if !request.Uninstall {
		var err error
		credentials, err = collectLegacyCredentials(request.Home, request.RemainingHarnesses)
		if err != nil {
			return nil, err
		}
	}
	envState, err := readFileState(envPath)
	if err != nil {
		return nil, err
	}
	envBytes := append([]byte(nil), envState.bytes...)
	if !envState.exists {
		envBytes = []byte{}
	}
	envBytes = appendCredentialAssignments(envBytes, credentials)
	envChange := changeForContent(envPath, envState, envBytes, 0o600)

	launcherState, err := readFileState(launcherPath)
	if err != nil {
		return nil, err
	}
	launcherChange := changeForContent(launcherPath, launcherState, launcherAsset, 0o700)
	return []Change{envChange, launcherChange}, nil
}

func readFileState(path string) (fileState, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return fileState{}, nil
	}
	if err != nil {
		return fileState{}, fmt.Errorf("inspect integration target %q: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fileState{}, fmt.Errorf("integration target %q is a symlink", path)
	}
	if !info.Mode().IsRegular() {
		return fileState{}, fmt.Errorf("integration target %q is not a regular file", path)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return fileState{}, fmt.Errorf("read integration target %q: %w", path, err)
	}
	return fileState{exists: true, mode: info.Mode().Perm(), bytes: content}, nil
}

func changeForContent(path string, current fileState, desired []byte, mode fs.FileMode) Change {
	after := append([]byte(nil), desired...)
	if !current.exists {
		return Change{Path: path, Action: ActionCreate, Mode: mode, After: after}
	}
	action := ActionUpdate
	if bytes.Equal(current.bytes, desired) && current.mode.Perm() == mode.Perm() {
		action = ActionUnchanged
	}
	return Change{
		Path: path, Action: action, Mode: mode,
		Before: append([]byte(nil), current.bytes...), After: after,
	}
}

func unchangedFile(path string, current fileState) Change {
	content := append([]byte(nil), current.bytes...)
	return Change{Path: path, Action: ActionUnchanged, Mode: current.mode.Perm(), Before: content, After: append([]byte(nil), content...)}
}

func planOwnedRemoval(path string, canonical []byte) (Change, bool, error) {
	current, err := readFileState(path)
	if err != nil || !current.exists {
		return Change{}, false, err
	}
	if !bytes.Equal(current.bytes, canonical) {
		return unchangedFile(path, current), true, nil
	}
	return Change{
		Path: path, Action: ActionDelete, Mode: current.mode.Perm(),
		Before:      append([]byte(nil), current.bytes...),
		DeleteProof: ownedAssetDeleteProof(canonical),
	}, true, nil
}

func planEmptyOwnedRemoval(path string) (Change, bool, error) {
	current, err := readFileState(path)
	if err != nil || !current.exists {
		return Change{}, false, err
	}
	if len(current.bytes) != 0 {
		return unchangedFile(path, current), true, nil
	}
	return Change{
		Path: path, Action: ActionDelete, Mode: current.mode.Perm(),
		Before:      append([]byte(nil), current.bytes...),
		DeleteProof: ownedAssetDeleteProof([]byte{}),
	}, true, nil
}

func planManagedInstructions(path string, uninstall bool) (Change, bool, error) {
	current, err := readFileState(path)
	if err != nil {
		return Change{}, false, err
	}
	if uninstall && !current.exists {
		return Change{}, false, nil
	}
	input := string(current.bytes)
	var output string
	if uninstall {
		output, err = RemoveManagedBlock(input, managedInstructionsBegin, managedInstructionsEnd)
	} else {
		output, err = ReplaceManagedBlock(input, managedInstructionsBegin, managedInstructionsEnd, strings.TrimSuffix(string(instructionsAsset), "\n"))
	}
	if err != nil {
		return Change{}, false, err
	}
	mode := fs.FileMode(0o644)
	if current.exists {
		mode = current.mode.Perm()
	}
	if uninstall && output == "" {
		return Change{
			Path: path, Action: ActionDelete, Mode: mode,
			Before:      append([]byte(nil), current.bytes...),
			DeleteProof: managedBlockDeleteProof(managedInstructionsBegin, managedInstructionsEnd),
		}, true, nil
	}
	return changeForContent(path, current, []byte(output), mode), true, nil
}

func containsManagedInstructions(raw []byte) bool {
	rangeInfo, err := findManagedBlock(string(raw), managedInstructionsBegin, managedInstructionsEnd)
	return err == nil && rangeInfo.found
}

func harnessPresent(paths ...string) bool {
	for _, path := range paths {
		if _, err := os.Lstat(path); err == nil {
			return true
		}
	}
	return false
}

func collectLegacyCredentials(home string, harnesses []string) (map[string]string, error) {
	credentials := make(map[string]string)
	for _, harness := range harnesses {
		var path string
		switch harness {
		case "claude":
			path = filepath.Join(home, ".claude", "settings.json")
		case "codex":
			path = filepath.Join(home, ".codex", "hooks.json")
		default:
			continue
		}
		state, err := readFileState(path)
		if err != nil {
			return nil, err
		}
		if !state.exists {
			continue
		}
		commands, err := JSONHookCommands(state.bytes)
		if err != nil {
			return nil, fmt.Errorf("inspect legacy %s hooks: %w", harness, err)
		}
		for _, command := range commands {
			assignments, legacy, err := parseLegacyCredentialCommand(command)
			if err != nil {
				return nil, fmt.Errorf("legacy credential migration for %s is ambiguous", harness)
			}
			if !legacy {
				continue
			}
			for name, value := range assignments {
				if prior, exists := credentials[name]; exists && prior != value {
					return nil, fmt.Errorf("legacy credential migration has conflicting assignment for %s", name)
				}
				credentials[name] = value
			}
		}
	}
	return credentials, nil
}

func JSONHookCommands(raw []byte) ([]string, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var commands []string
	var visit func(any)
	visit = func(value any) {
		switch typed := value.(type) {
		case map[string]any:
			for key, child := range typed {
				if key == "command" {
					if command, ok := child.(string); ok {
						commands = append(commands, command)
					}
					continue
				}
				visit(child)
			}
		case []any:
			for _, child := range typed {
				visit(child)
			}
		}
	}
	visit(value)
	return commands, nil
}

func parseLegacyCredentialCommand(command string) (map[string]string, bool, error) {
	trimmed := strings.TrimSpace(command)
	if !strings.Contains(strings.ToLower(trimmed), "dualmem") || !strings.Contains(trimmed, "export") {
		return nil, false, nil
	}
	if !strings.HasPrefix(trimmed, "export ") {
		return nil, false, fmt.Errorf("ambiguous legacy shell prefix")
	}
	assignments := make(map[string]string)
	rest := trimmed
	for {
		rest = strings.TrimSpace(strings.TrimPrefix(rest, "export "))
		equals := strings.IndexByte(rest, '=')
		if equals <= 0 {
			return nil, false, fmt.Errorf("ambiguous export assignment")
		}
		name := rest[:equals]
		if !recognizedCredentialName(name) {
			return nil, false, fmt.Errorf("unrecognized credential name")
		}
		rest = rest[equals+1:]
		value, tail, err := parseStaticShellValue(rest)
		if err != nil || value == "" {
			return nil, false, fmt.Errorf("ambiguous credential value")
		}
		assignments[name] = value
		rest = strings.TrimSpace(tail)
		switch {
		case strings.HasPrefix(rest, "&&"):
			rest = strings.TrimSpace(rest[2:])
		case strings.HasPrefix(rest, ";"):
			rest = strings.TrimSpace(rest[1:])
		default:
			return nil, false, fmt.Errorf("ambiguous export separator")
		}
		if strings.HasPrefix(rest, "export ") {
			continue
		}
		if !strings.Contains(strings.ToLower(rest), "dualmem") {
			return nil, false, fmt.Errorf("export is not attached to a DualMem command")
		}
		break
	}
	return assignments, true, nil
}

func parseStaticShellValue(input string) (string, string, error) {
	if input == "" {
		return "", "", fmt.Errorf("empty value")
	}
	if input[0] == '\'' {
		end := strings.IndexByte(input[1:], '\'')
		if end < 0 {
			return "", "", fmt.Errorf("unterminated quote")
		}
		end++
		return input[1:end], input[end+1:], nil
	}
	end := 0
	for end < len(input) && !strings.ContainsRune(" \t\r\n;&|", rune(input[end])) {
		if !isStaticUnquotedByte(input[end]) {
			return "", "", fmt.Errorf("dynamic shell value")
		}
		end++
	}
	if end == 0 {
		return "", "", fmt.Errorf("empty value")
	}
	return input[:end], input[end:], nil
}

func recognizedCredentialName(name string) bool {
	if name == "" || name[0] < 'A' || name[0] > 'Z' {
		return false
	}
	for i := 1; i < len(name); i++ {
		if (name[i] < 'A' || name[i] > 'Z') && (name[i] < '0' || name[i] > '9') && name[i] != '_' {
			return false
		}
	}
	for _, suffix := range []string{"_API_KEY", "_TOKEN", "_SECRET", "_PASSWORD"} {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
}

func isStaticUnquotedByte(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9' || strings.ContainsRune("_./:@%+,-", rune(value))
}

func appendCredentialAssignments(existing []byte, credentials map[string]string) []byte {
	if len(credentials) == 0 {
		return append([]byte(nil), existing...)
	}
	output := append([]byte(nil), existing...)
	if len(output) > 0 && output[len(output)-1] != '\n' {
		output = append(output, '\n')
	}
	names := make([]string, 0, len(credentials))
	for name := range credentials {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		output = append(output, name...)
		output = append(output, '=', '\'')
		output = append(output, credentials[name]...)
		output = append(output, '\'', '\n')
	}
	return output
}

func installedPiSupportsPromptHook() bool {
	piPath, err := exec.LookPath("pi")
	if err != nil {
		return false
	}
	resolved, err := filepath.EvalSymlinks(piPath)
	if err != nil {
		return false
	}
	packageRoot := filepath.Dir(filepath.Dir(resolved))
	typesPath := filepath.Join(packageRoot, "dist", "core", "extensions", "types.d.ts")
	raw, err := os.ReadFile(typesPath)
	if err != nil {
		return false
	}
	return bytes.Contains(raw, []byte(`on(event: "before_agent_start"`))
}

func renderedPiExtension(promptSupported bool) []byte {
	asset := append([]byte(nil), piExtensionAsset...)
	if promptSupported {
		return asset
	}
	begin := bytes.Index(asset, []byte(piPromptHookBegin))
	end := bytes.Index(asset, []byte(piPromptHookEnd))
	if begin < 0 || end < begin {
		return asset
	}
	end += len(piPromptHookEnd)
	return append(asset[:begin], asset[end:]...)
}
