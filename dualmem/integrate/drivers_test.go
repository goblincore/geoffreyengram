package integrate

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

const fixtureCredential = "fixture-not-a-real-credential"

type hookDocument struct {
	Theme  string         `json:"theme"`
	Future map[string]any `json:"future"`
	Hooks  map[string][]struct {
		Matcher string `json:"matcher"`
		Hooks   []struct {
			Type    string `json:"type"`
			Command string `json:"command"`
			Timeout int    `json:"timeout"`
		} `json:"hooks"`
	} `json:"hooks"`
}

func TestBuiltinDriversInstallPreservesHarnessStateAndIsIdempotent(t *testing.T) {
	setPiPromptSupport(t, true)
	home := populatedHarnessHome(t)

	result, err := Plan(context.Background(), Options{Home: home, Harnesses: []string{"all"}}, BuiltinBundle())
	if err != nil {
		t.Fatal(err)
	}
	wantPaths := []string{
		filepath.Join(home, ".claude", "CLAUDE.md"),
		filepath.Join(home, ".claude", "settings.json"),
		filepath.Join(home, ".codex", "AGENTS.md"),
		filepath.Join(home, ".codex", "hooks.json"),
		filepath.Join(home, ".config", "dualmem", "bin", "dualmem-run"),
		filepath.Join(home, ".config", "dualmem", "env"),
		filepath.Join(home, ".pi", "agent", "AGENTS.md"),
		filepath.Join(home, ".pi", "agent", "extensions", "dualmem.ts"),
	}
	if got := changePaths(result.Changes); !slices.Equal(got, wantPaths) {
		t.Fatalf("change paths = %v, want %v", got, wantPaths)
	}
	for _, change := range result.Changes {
		if change.Action == ActionUnchanged {
			t.Fatalf("fresh install unexpectedly left %s unchanged", change.Path)
		}
	}
	if err := Apply(result); err != nil {
		t.Fatal(err)
	}

	envPath := filepath.Join(home, ".config", "dualmem", "env")
	launcherPath := filepath.Join(home, ".config", "dualmem", "bin", "dualmem-run")
	assertFile(t, envPath, "", 0o600)
	assertFile(t, launcherPath, string(launcherAsset), 0o700)

	claude := readHookDocument(t, filepath.Join(home, ".claude", "settings.json"))
	if claude.Theme != "night" || len(claude.Hooks["Stop"]) != 1 {
		t.Fatalf("Claude unrelated JSON was not preserved: %#v", claude)
	}
	assertManagedMatchers(t, claude, "claude", map[string][]string{
		"SessionStart":     {""},
		"UserPromptSubmit": {""},
		"PreToolUse":       {"Read"},
		"PostToolUse":      {"Edit|Write"},
	})

	codex := readHookDocument(t, filepath.Join(home, ".codex", "hooks.json"))
	if enabled, _ := codex.Future["enabled"].(bool); !enabled {
		t.Fatalf("Codex unrelated JSON was not preserved: %#v", codex.Future)
	}
	assertManagedMatchers(t, codex, "codex", map[string][]string{
		"SessionStart":     {""},
		"UserPromptSubmit": {""},
		"PostToolUse":      {"apply_patch"},
	})
	for _, group := range codex.Hooks["PostToolUse"] {
		for _, hook := range group.Hooks {
			if strings.Contains(hook.Command, "--adapter codex") && strings.ContainsAny(group.Matcher, "REW") {
				t.Fatalf("Codex imported a Claude-only matcher %q", group.Matcher)
			}
		}
	}

	for _, instructionPath := range []string{
		filepath.Join(home, ".claude", "CLAUDE.md"),
		filepath.Join(home, ".codex", "AGENTS.md"),
		filepath.Join(home, ".pi", "agent", "AGENTS.md"),
	} {
		body, err := os.ReadFile(instructionPath)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), "unrelated instructions") || !strings.Contains(string(body), "<!-- BEGIN DUALMEM -->") {
			t.Fatalf("managed instructions did not preserve unrelated text in %s", instructionPath)
		}
		if strings.Contains(string(body), "claude:") {
			t.Fatalf("managed instructions exposed legacy namespace ownership in %s", instructionPath)
		}
	}

	piPath := filepath.Join(home, ".pi", "agent", "extensions", "dualmem.ts")
	piBytes, err := os.ReadFile(piPath)
	if err != nil {
		t.Fatal(err)
	}
	assertPiExtensionContract(t, string(piBytes), true)
	piChange := mustChange(t, result.Changes, piPath)
	if piChange.Action != ActionUpdate || piChange.BackupPath == "" {
		t.Fatalf("pi extension change = %#v, want backed-up update", piChange)
	}
	assertFile(t, piChange.BackupPath, string(readFixture(t, "pi-extension-existing.ts")), 0o600)

	wantCapabilities := map[string][]Capability{
		"claude": {"file_read", "file_write", "prompt", "session_start"},
		"codex":  {"file_write", "prompt", "session_start"},
		"pi":     {"file_read", "file_write", "prompt", "session_end", "session_start", "tool"},
	}
	for _, detection := range result.Detections {
		if !detection.Present {
			t.Fatalf("%s was not detected as present", detection.Harness)
		}
		if got := detection.Capabilities; !reflect.DeepEqual(got, wantCapabilities[detection.Harness]) {
			t.Fatalf("%s capabilities = %v, want %v", detection.Harness, got, wantCapabilities[detection.Harness])
		}
	}

	second, err := Plan(context.Background(), Options{Home: home, Harnesses: []string{"all"}}, BuiltinBundle())
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Changes) != len(wantPaths) {
		t.Fatalf("second plan has %d changes, want %d", len(second.Changes), len(wantPaths))
	}
	for _, change := range second.Changes {
		if change.Action != ActionUnchanged {
			t.Fatalf("second plan change for %s = %s, want unchanged", change.Path, change.Action)
		}
	}
}

func TestClaudeDriverMigratesOnlyRecognizedLegacyCredentialAssignments(t *testing.T) {
	setPiPromptSupport(t, true)
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	command := "export GEMINI_API_KEY='" + fixtureCredential + "' && \"$HOME/go/bin/dualmem\" hook --adapter claude"
	writeLegacyHookDocument(t, filepath.Join(home, ".claude", "settings.json"), command)

	result, err := Plan(context.Background(), Options{Home: home, Harnesses: []string{"claude"}}, BuiltinBundle())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.String(), fixtureCredential) {
		t.Fatal("plan summary exposed a migrated credential value")
	}
	settingsChange := mustChange(t, result.Changes, filepath.Join(home, ".claude", "settings.json"))
	if strings.Contains(string(settingsChange.After), fixtureCredential) {
		t.Fatal("managed Claude command retained a migrated credential value")
	}
	envChange := mustChange(t, result.Changes, filepath.Join(home, ".config", "dualmem", "env"))
	if !strings.Contains(string(envChange.After), fixtureCredential) {
		t.Fatal("protected env bytes did not receive the recognized credential assignment")
	}
	if err := Apply(result); err != nil {
		t.Fatal(err)
	}
	assertMode(t, filepath.Join(home, ".config", "dualmem", "env"), 0o600)

	second, err := Plan(context.Background(), Options{Home: home, Harnesses: []string{"claude"}}, BuiltinBundle())
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range second.Changes {
		if change.Action != ActionUnchanged {
			t.Fatalf("credential migration was not idempotent: %#v", change)
		}
	}
}

func TestClaudeDriverRejectsAmbiguousLegacyShellWithoutWrites(t *testing.T) {
	setPiPromptSupport(t, true)
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	writeLegacyHookDocument(t, settingsPath, "export GEMINI_API_KEY=$(credential-helper) && dualmem hook --adapter claude")
	before, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}

	result, err := Plan(context.Background(), Options{Home: home, Harnesses: []string{"claude"}}, BuiltinBundle())
	if err == nil {
		t.Fatal("ambiguous legacy shell syntax was accepted")
	}
	if len(result.Changes) != 0 {
		t.Fatalf("failed plan exposed %d partial changes", len(result.Changes))
	}
	after, readErr := os.ReadFile(settingsPath)
	if readErr != nil || !slices.Equal(before, after) {
		t.Fatalf("failed plan changed legacy settings: %v", readErr)
	}
	for _, path := range []string{
		filepath.Join(home, ".config", "dualmem", "env"),
		filepath.Join(home, ".config", "dualmem", "bin", "dualmem-run"),
	} {
		if _, statErr := os.Lstat(path); !os.IsNotExist(statErr) {
			t.Fatalf("failed plan wrote %s: %v", path, statErr)
		}
	}
}

func TestCodexDriverTargetedUninstallKeepsCommonAssetsForManagedPi(t *testing.T) {
	setPiPromptSupport(t, true)
	home := populatedHarnessHome(t)
	installed, err := Plan(context.Background(), Options{Home: home, Harnesses: []string{"all"}}, BuiltinBundle())
	if err != nil {
		t.Fatal(err)
	}
	if err := Apply(installed); err != nil {
		t.Fatal(err)
	}

	uninstall, err := Plan(context.Background(), Options{Home: home, Harnesses: []string{"codex"}, Uninstall: true}, BuiltinBundle())
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		filepath.Join(home, ".config", "dualmem", "env"),
		filepath.Join(home, ".config", "dualmem", "bin", "dualmem-run"),
	} {
		if got := mustChange(t, uninstall.Changes, path).Action; got != ActionUnchanged {
			t.Fatalf("targeted uninstall planned %s for shared %s", got, path)
		}
	}
	if err := Apply(uninstall); err != nil {
		t.Fatal(err)
	}
	assertFile(t, filepath.Join(home, ".config", "dualmem", "bin", "dualmem-run"), string(launcherAsset), 0o700)
	piDetection := detectionByName(t, mustPlan(t, home, "pi").Detections, "pi")
	if !piDetection.Managed {
		t.Fatal("targeted Codex uninstall made the remaining pi integration unmanaged")
	}
}

func TestCodexDriverUninstallRemovesExactManagedHookAndPreservesSibling(t *testing.T) {
	setPiPromptSupport(t, true)
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	installed, err := Plan(context.Background(), Options{Home: home, Harnesses: []string{"codex"}}, BuiltinBundle())
	if err != nil {
		t.Fatal(err)
	}
	if err := Apply(installed); err != nil {
		t.Fatal(err)
	}

	hooksPath := filepath.Join(home, ".codex", "hooks.json")
	raw, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	hooks := document["hooks"].(map[string]any)
	groups := hooks["PostToolUse"].([]any)
	for _, rawGroup := range groups {
		group := rawGroup.(map[string]any)
		if group["matcher"] != "apply_patch" {
			continue
		}
		groupHooks := group["hooks"].([]any)
		group["hooks"] = append(groupHooks, map[string]any{
			"type": "command", "command": "printf unrelated-sibling", "timeout": float64(3),
		})
	}
	modified, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hooksPath, append(modified, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}

	uninstall, err := Plan(context.Background(), Options{Home: home, Harnesses: []string{"codex"}, Uninstall: true}, BuiltinBundle())
	if err != nil {
		t.Fatal(err)
	}
	if err := Apply(uninstall); err != nil {
		t.Fatal(err)
	}
	after := readHookDocument(t, hooksPath)
	foundSibling := false
	for _, group := range after.Hooks["PostToolUse"] {
		for _, hook := range group.Hooks {
			if strings.Contains(hook.Command, "--adapter codex") {
				t.Fatal("uninstall retained the exact managed hook command in an edited group")
			}
			if hook.Command == "printf unrelated-sibling" {
				foundSibling = true
			}
		}
	}
	if !foundSibling {
		t.Fatal("uninstall removed an unrelated sibling hook")
	}
}

func TestPiDriverOmitsUnsupportedPromptHookAndCapability(t *testing.T) {
	setPiPromptSupport(t, false)
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".pi", "agent"), 0o700); err != nil {
		t.Fatal(err)
	}
	result, err := Plan(context.Background(), Options{Home: home, Harnesses: []string{"pi"}}, BuiltinBundle())
	if err != nil {
		t.Fatal(err)
	}
	piDetection := detectionByName(t, result.Detections, "pi")
	if slices.Contains(piDetection.Capabilities, Capability("prompt")) {
		t.Fatalf("unsupported prompt capability was advertised: %v", piDetection.Capabilities)
	}
	extension := mustChange(t, result.Changes, filepath.Join(home, ".pi", "agent", "extensions", "dualmem.ts"))
	assertPiExtensionContract(t, string(extension.After), false)
}

func populatedHarnessHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	for _, dir := range []string{
		filepath.Join(home, ".claude"),
		filepath.Join(home, ".codex"),
		filepath.Join(home, ".pi", "agent", "extensions"),
	} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeFixture(t, "claude-settings-existing.json", filepath.Join(home, ".claude", "settings.json"), 0o600)
	writeFixture(t, "codex-hooks-existing.json", filepath.Join(home, ".codex", "hooks.json"), 0o600)
	writeFixture(t, "pi-extension-existing.ts", filepath.Join(home, ".pi", "agent", "extensions", "dualmem.ts"), 0o600)
	for _, path := range []string{
		filepath.Join(home, ".claude", "CLAUDE.md"),
		filepath.Join(home, ".codex", "AGENTS.md"),
		filepath.Join(home, ".pi", "agent", "AGENTS.md"),
	} {
		if err := os.WriteFile(path, []byte("unrelated instructions\n"), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	return home
}

func setPiPromptSupport(t *testing.T, supported bool) {
	t.Helper()
	original := piPromptSupportProbe
	piPromptSupportProbe = func() bool { return supported }
	t.Cleanup(func() { piPromptSupportProbe = original })
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return content
}

func writeFixture(t *testing.T, name, path string, mode fs.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, readFixture(t, name), mode); err != nil {
		t.Fatal(err)
	}
}

func writeLegacyHookDocument(t *testing.T, path, command string) {
	t.Helper()
	document := map[string]any{
		"unrelated": "preserved",
		"hooks": map[string]any{
			"SessionStart": []any{map[string]any{
				"hooks": []any{map[string]any{
					"type": "command", "command": command, "timeout": 5,
				}},
			}},
		},
	}
	encoded, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(encoded, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readHookDocument(t *testing.T, path string) hookDocument {
	t.Helper()
	var document hookDocument
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	return document
}

func assertManagedMatchers(t *testing.T, document hookDocument, adapter string, want map[string][]string) {
	t.Helper()
	got := make(map[string][]string)
	for event, groups := range document.Hooks {
		for _, group := range groups {
			for _, hook := range group.Hooks {
				if !strings.Contains(hook.Command, "--adapter "+adapter) {
					continue
				}
				if strings.Contains(hook.Command, fixtureCredential) || strings.Contains(hook.Command, "export ") {
					t.Fatalf("%s command contains a literal credential assignment", adapter)
				}
				if hook.Type != "command" || hook.Timeout <= 0 || !strings.Contains(hook.Command, "dualmem-run") {
					t.Fatalf("invalid managed %s hook: %#v", adapter, hook)
				}
				got[event] = append(got[event], group.Matcher)
			}
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s managed matchers = %#v, want %#v", adapter, got, want)
	}
}

func assertPiExtensionContract(t *testing.T, source string, promptSupported bool) {
	t.Helper()
	for _, required := range []string{
		"process.env.DUALMEM_RUN",
		".config\", \"dualmem\", \"bin\", \"dualmem-run",
		"execFile(DUALMEM_RUN, args",
		"session_start",
		"file_read",
		"file_write",
		"session_end",
		"pi.registerTool",
		"maxBuffer",
		"timeout",
		"dualmem lifecycle unavailable",
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("pi extension missing %q", required)
		}
	}
	for _, forbidden := range []string{"/Users/", "--ns", "exec("} {
		if strings.Contains(source, forbidden) {
			t.Fatalf("pi extension contains forbidden text %q", forbidden)
		}
	}
	if got := strings.Contains(source, "before_agent_start"); got != promptSupported {
		t.Fatalf("pi prompt hook present = %t, want %t", got, promptSupported)
	}
}

func mustChange(t *testing.T, changes []Change, path string) Change {
	t.Helper()
	for _, change := range changes {
		if change.Path == path {
			return change
		}
	}
	t.Fatalf("missing change for %s", path)
	return Change{}
}

func detectionByName(t *testing.T, detections []Detection, name string) Detection {
	t.Helper()
	for _, detection := range detections {
		if detection.Harness == name {
			return detection
		}
	}
	t.Fatalf("missing detection for %s", name)
	return Detection{}
}

func mustPlan(t *testing.T, home, harness string) Result {
	t.Helper()
	result, err := Plan(context.Background(), Options{Home: home, Harnesses: []string{harness}}, BuiltinBundle())
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func assertMode(t *testing.T, path string, want fs.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want.Perm() {
		t.Fatalf("%s mode = %04o, want %04o", path, got, want.Perm())
	}
}
