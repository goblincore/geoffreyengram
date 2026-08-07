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
	home := populatedHarnessHome(t)

	result, err := Plan(context.Background(), Options{Home: home, Harnesses: []string{"all"}}, BuiltinBundle())
	if err != nil {
		t.Fatal(err)
	}
	wantPaths := []string{
		filepath.Join(home, ".config", "dualmem", "bin", "dualmem-run"),
		filepath.Join(home, ".config", "dualmem", "env"),
		filepath.Join(home, ".claude", "CLAUDE.md"),
		filepath.Join(home, ".claude", "settings.json"),
		filepath.Join(home, ".codex", "AGENTS.md"),
		filepath.Join(home, ".codex", "hooks.json"),
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
		"PreToolUse":  {"Read"},
		"PostToolUse": {"Edit|Write"},
	})

	codex := readHookDocument(t, filepath.Join(home, ".codex", "hooks.json"))
	if enabled, _ := codex.Future["enabled"].(bool); !enabled {
		t.Fatalf("Codex unrelated JSON was not preserved: %#v", codex.Future)
	}
	assertManagedMatchers(t, codex, "codex", map[string][]string{
		"PostToolUse": {"apply_patch"},
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
	assertPiExtensionContract(t, string(piBytes))
	piChange := mustChange(t, result.Changes, piPath)
	if piChange.Action != ActionUpdate || piChange.BackupPath == "" {
		t.Fatalf("pi extension change = %#v, want backed-up update", piChange)
	}
	assertFile(t, piChange.BackupPath, string(readFixture(t, "pi-extension-existing.ts")), 0o600)

	wantCapabilities := map[string][]Capability{
		"claude": {"file_read", "file_write"},
		"codex":  {"file_write"},
		"pi":     {"file_read", "file_write", "session_end", "tool"},
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

func TestBuiltinDriversRemoveDeprecatedProviderDependentHooks(t *testing.T) {
	home := populatedHarnessHome(t)
	for _, test := range []struct {
		path    string
		adapter string
	}{
		{path: filepath.Join(home, ".claude", "settings.json"), adapter: "claude"},
		{path: filepath.Join(home, ".codex", "hooks.json"), adapter: "codex"},
	} {
		document := readJSONMap(t, test.path)
		hooks := document["hooks"].(map[string]any)
		for _, event := range []string{"SessionStart", "UserPromptSubmit"} {
			spec := hookSpec{event: event, adapter: test.adapter}
			hooks[event] = []any{decodeJSONMap(t, managedHookGroup(spec))}
		}
		writeJSONMap(t, test.path, document)
	}

	result, err := Plan(context.Background(), Options{Home: home, Harnesses: []string{"all"}}, BuiltinBundle())
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		filepath.Join(home, ".claude", "settings.json"),
		filepath.Join(home, ".codex", "hooks.json"),
	} {
		change := mustChange(t, result.Changes, path)
		var document map[string]any
		if err := json.Unmarshal(change.After, &document); err != nil {
			t.Fatal(err)
		}
		hooks := document["hooks"].(map[string]any)
		for _, event := range []string{"SessionStart", "UserPromptSubmit"} {
			if _, exists := hooks[event]; exists {
				t.Fatalf("%s retained deprecated automatic %s hook", path, event)
			}
		}
	}
}

func TestClaudeDriverMigratesOnlyRecognizedLegacyCredentialAssignments(t *testing.T) {
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

func TestClaudeDriverIgnoresCredentialShapedCommandsOutsideRecognizedHooks(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	installed, err := Plan(context.Background(), Options{Home: home, Harnesses: []string{"claude"}}, BuiltinBundle())
	if err != nil {
		t.Fatal(err)
	}
	if err := Apply(installed); err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	document := readJSONMap(t, settingsPath)
	document["future"] = map[string]any{
		"command": recognizedLegacyCommand("claude", fixtureCredential),
	}
	writeJSONMap(t, settingsPath, document)
	beforeSettings := readFile(t, settingsPath)
	envPath := filepath.Join(home, ".config", "dualmem", "env")
	beforeEnv := readFile(t, envPath)

	result, err := Plan(context.Background(), Options{Home: home, Harnesses: []string{"claude"}}, BuiltinBundle())
	if err != nil {
		t.Fatal(err)
	}
	assertUnchangedBytes(t, mustChange(t, result.Changes, settingsPath), beforeSettings)
	assertUnchangedBytes(t, mustChange(t, result.Changes, envPath), beforeEnv)
}

func TestClaudeDriverIgnoresUnrelatedHookCommandContainingDualmemWord(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	installed, err := Plan(context.Background(), Options{Home: home, Harnesses: []string{"claude"}}, BuiltinBundle())
	if err != nil {
		t.Fatal(err)
	}
	if err := Apply(installed); err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	document := readJSONMap(t, settingsPath)
	hooks := document["hooks"].(map[string]any)
	unrelated := []any{
		map[string]any{"hooks": []any{map[string]any{
			"type": "command", "command": "export GEMINI_API_KEY='" + fixtureCredential + "' && printf dualmem", "timeout": float64(5),
		}}},
		map[string]any{"hooks": []any{map[string]any{
			"type": "command", "command": recognizedLegacyCommand("codex", fixtureCredential), "timeout": float64(5),
		}}},
		map[string]any{"hooks": []any{map[string]any{
			"type": "prompt", "command": recognizedLegacyCommand("claude", fixtureCredential), "timeout": float64(5),
		}}},
	}
	hooks["SessionStart"] = unrelated
	writeJSONMap(t, settingsPath, document)
	beforeSettings := readFile(t, settingsPath)
	envPath := filepath.Join(home, ".config", "dualmem", "env")
	beforeEnv := readFile(t, envPath)

	result, err := Plan(context.Background(), Options{Home: home, Harnesses: []string{"claude"}}, BuiltinBundle())
	if err != nil {
		t.Fatal(err)
	}
	assertUnchangedBytes(t, mustChange(t, result.Changes, settingsPath), beforeSettings)
	assertUnchangedBytes(t, mustChange(t, result.Changes, envPath), beforeEnv)
}

func TestClaudeDriverRejectsCredentialConflictsBeforeProducingChanges(t *testing.T) {
	tests := []struct {
		name       string
		commands   []string
		envContent string
	}{
		{
			name: "within one command",
			commands: []string{
				"export GEMINI_API_KEY='fixture-value-one' && export GEMINI_API_KEY='fixture-value-two' && \"$HOME/go/bin/dualmem\" hook --adapter claude",
			},
		},
		{
			name: "across recognized hooks",
			commands: []string{
				recognizedLegacyCommand("claude", "fixture-value-one"),
				recognizedLegacyCommand("claude", "fixture-value-two"),
			},
		},
		{
			name:       "against protected env",
			commands:   []string{recognizedLegacyCommand("claude", "fixture-value-two")},
			envContent: "GEMINI_API_KEY='fixture-value-one'\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); err != nil {
				t.Fatal(err)
			}
			settingsPath := filepath.Join(home, ".claude", "settings.json")
			writeLegacyHookCommands(t, settingsPath, test.commands, nil)
			watched := map[string][]byte{settingsPath: readFile(t, settingsPath)}
			if test.envContent != "" {
				envPath := filepath.Join(home, ".config", "dualmem", "env")
				if err := os.MkdirAll(filepath.Dir(envPath), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(envPath, []byte(test.envContent), 0o600); err != nil {
					t.Fatal(err)
				}
				watched[envPath] = readFile(t, envPath)
			}

			result, err := Plan(context.Background(), Options{Home: home, Harnesses: []string{"claude"}}, BuiltinBundle())
			if err == nil {
				t.Fatal("credential conflict was accepted")
			}
			if len(result.Changes) != 0 {
				t.Fatalf("conflicting plan exposed %d changes", len(result.Changes))
			}
			for path, before := range watched {
				if after := readFile(t, path); !slices.Equal(after, before) {
					t.Fatalf("conflicting plan changed %s", path)
				}
			}
		})
	}
}

func TestClaudeDriverDeduplicatesIdenticalCredentialAssignments(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	command := "export GEMINI_API_KEY='" + fixtureCredential + "' && export GEMINI_API_KEY='" + fixtureCredential + "' && \"$HOME/go/bin/dualmem\" hook --adapter claude"
	writeLegacyHookCommands(t, settingsPath, []string{command, recognizedLegacyCommand("claude", fixtureCredential)}, nil)
	envPath := filepath.Join(home, ".config", "dualmem", "env")
	if err := os.MkdirAll(filepath.Dir(envPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(envPath, []byte("GEMINI_API_KEY='"+fixtureCredential+"'\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := Plan(context.Background(), Options{Home: home, Harnesses: []string{"claude"}}, BuiltinBundle())
	if err != nil {
		t.Fatal(err)
	}
	envChange := mustChange(t, result.Changes, envPath)
	if envChange.Action != ActionUnchanged || strings.Count(string(envChange.After), "GEMINI_API_KEY=") != 1 {
		t.Fatalf("identical assignments were not deduplicated: action=%s", envChange.Action)
	}
}

func TestClaudeDriverMigrationPreservesGroupMetadataWithEmptyHooks(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	writeLegacyHookCommands(t, settingsPath, []string{recognizedLegacyCommand("claude", fixtureCredential)}, map[string]any{
		"description": "preserve group metadata",
	})

	result, err := Plan(context.Background(), Options{Home: home, Harnesses: []string{"claude"}}, BuiltinBundle())
	if err != nil {
		t.Fatal(err)
	}
	settings := mustChange(t, result.Changes, settingsPath)
	var document map[string]any
	if err := json.Unmarshal(settings.After, &document); err != nil {
		t.Fatal(err)
	}
	groups := document["hooks"].(map[string]any)["SessionStart"].([]any)
	found := false
	for _, rawGroup := range groups {
		group := rawGroup.(map[string]any)
		if group["description"] != "preserve group metadata" {
			continue
		}
		found = true
		if hooks, ok := group["hooks"].([]any); !ok || len(hooks) != 0 {
			t.Fatalf("metadata group hooks = %#v, want retained empty array", group["hooks"])
		}
	}
	if !found {
		t.Fatal("migration removed unrelated group metadata")
	}
}

func TestCodexDriverTargetedUninstallKeepsCommonAssetsForManagedPi(t *testing.T) {
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

func TestTargetedUninstallRetainsCommonAssetsForModifiedPiLauncherDependency(t *testing.T) {
	for _, test := range []struct {
		name       string
		harness    string
		dependency string
	}{
		{name: "Codex with launcher path", harness: "codex", dependency: `const command = "dualmem-run";`},
		{name: "Claude with launcher override", harness: "claude", dependency: `const command = process.env.DUALMEM_RUN;`},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			installed, err := Plan(context.Background(), Options{Home: home, Harnesses: []string{test.harness}}, BuiltinBundle())
			if err != nil {
				t.Fatal(err)
			}
			if err := Apply(installed); err != nil {
				t.Fatal(err)
			}

			extensionPath := filepath.Join(home, ".pi", "agent", "extensions", "dualmem.ts")
			if err := os.MkdirAll(filepath.Dir(extensionPath), 0o700); err != nil {
				t.Fatal(err)
			}
			modified := []byte("// retained local pi integration\n" + test.dependency + "\n")
			if err := os.WriteFile(extensionPath, modified, 0o700); err != nil {
				t.Fatal(err)
			}
			piDetection := detectionByName(t, mustPlan(t, home, test.harness).Detections, "pi")
			if piDetection.Managed {
				t.Fatal("noncanonical pi extension without managed instructions was detected as managed")
			}

			envPath := filepath.Join(home, ".config", "dualmem", "env")
			launcherPath := filepath.Join(home, ".config", "dualmem", "bin", "dualmem-run")
			beforeEnv := readFile(t, envPath)
			beforeLauncher := readFile(t, launcherPath)

			uninstall, err := Plan(context.Background(), Options{Home: home, Harnesses: []string{test.harness}, Uninstall: true}, BuiltinBundle())
			if err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{envPath, launcherPath} {
				if got := mustChange(t, uninstall.Changes, path).Action; got != ActionUnchanged {
					t.Fatalf("targeted uninstall planned %s for pi dependency %s", got, path)
				}
			}
			if err := Apply(uninstall); err != nil {
				t.Fatal(err)
			}
			if after := readFile(t, envPath); !slices.Equal(after, beforeEnv) {
				t.Fatal("targeted uninstall changed the shared env needed by retained pi")
			}
			if after := readFile(t, launcherPath); !slices.Equal(after, beforeLauncher) {
				t.Fatal("targeted uninstall changed the shared launcher needed by retained pi")
			}
			if after := readFile(t, extensionPath); !slices.Equal(after, modified) {
				t.Fatal("targeted uninstall changed the retained pi extension")
			}
		})
	}
}

func TestTargetedUninstallRetainsCommonAssetsForModifiedHookLauncherDependency(t *testing.T) {
	for _, test := range []struct {
		name       string
		harness    string
		directory  string
		configName string
		adapter    string
	}{
		{name: "Claude", harness: "claude", directory: ".claude", configName: "settings.json", adapter: "claude"},
		{name: "Codex", harness: "codex", directory: ".codex", configName: "hooks.json", adapter: "codex"},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			if err := os.MkdirAll(filepath.Join(home, test.directory), 0o700); err != nil {
				t.Fatal(err)
			}
			installed, err := Plan(context.Background(), Options{Home: home, Harnesses: []string{test.harness}}, BuiltinBundle())
			if err != nil {
				t.Fatal(err)
			}
			if err := Apply(installed); err != nil {
				t.Fatal(err)
			}

			configPath := filepath.Join(home, test.directory, test.configName)
			document := readJSONMap(t, configPath)
			hooks := document["hooks"].(map[string]any)
			modified := false
			for _, rawGroups := range hooks {
				for _, rawGroup := range rawGroups.([]any) {
					group := rawGroup.(map[string]any)
					for _, rawHook := range group["hooks"].([]any) {
						hook := rawHook.(map[string]any)
						command, _ := hook["command"].(string)
						if strings.Contains(command, "--adapter "+test.adapter) {
							hook["command"] = command + " --retained-local-option"
							modified = true
							break
						}
					}
					if modified {
						break
					}
				}
				if modified {
					break
				}
			}
			if !modified {
				t.Fatal("managed hook command was not found")
			}
			writeJSONMap(t, configPath, document)

			envPath := filepath.Join(home, ".config", "dualmem", "env")
			launcherPath := filepath.Join(home, ".config", "dualmem", "bin", "dualmem-run")
			uninstall, err := Plan(context.Background(), Options{Home: home, Harnesses: []string{test.harness}, Uninstall: true}, BuiltinBundle())
			if err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{envPath, launcherPath} {
				if got := mustChange(t, uninstall.Changes, path).Action; got != ActionUnchanged {
					t.Fatalf("targeted uninstall planned %s for shared dependency %s", got, path)
				}
			}
			if err := Apply(uninstall); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(readFile(t, configPath)), "--retained-local-option") {
				t.Fatal("targeted uninstall removed the modified dependent hook")
			}
			assertFile(t, launcherPath, string(launcherAsset), 0o700)
		})
	}
}

func TestTargetedUninstallRetainsCommonAssetsForHookLauncherEnvironmentDependency(t *testing.T) {
	home := t.TempDir()
	installed, err := Plan(context.Background(), Options{Home: home, Harnesses: []string{"codex"}}, BuiltinBundle())
	if err != nil {
		t.Fatal(err)
	}
	if err := Apply(installed); err != nil {
		t.Fatal(err)
	}

	configPath := filepath.Join(home, ".codex", "hooks.json")
	document := readJSONMap(t, configPath)
	hooks := document["hooks"].(map[string]any)
	groups := hooks["PostToolUse"].([]any)
	group := groups[0].(map[string]any)
	hook := group["hooks"].([]any)[0].(map[string]any)
	hook["command"] = `"$DUALMEM_RUN" hook --adapter codex`
	writeJSONMap(t, configPath, document)

	envPath := filepath.Join(home, ".config", "dualmem", "env")
	launcherPath := filepath.Join(home, ".config", "dualmem", "bin", "dualmem-run")
	uninstall, err := Plan(context.Background(), Options{Home: home, Harnesses: []string{"codex"}, Uninstall: true}, BuiltinBundle())
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{envPath, launcherPath} {
		if got := mustChange(t, uninstall.Changes, path).Action; got != ActionUnchanged {
			t.Fatalf("targeted uninstall planned %s for DUALMEM_RUN dependency %s", got, path)
		}
	}
}

func TestCodexDriverUninstallRemovesExactManagedHookAndPreservesSibling(t *testing.T) {
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

func TestHarnessUninstallPreservesUnrelatedEmptyManagedEventArrays(t *testing.T) {
	tests := []struct {
		name       string
		harness    string
		directory  string
		configName string
		event      string
	}{
		{name: "Claude", harness: "claude", directory: ".claude", configName: "settings.json", event: "PreToolUse"},
		{name: "Codex", harness: "codex", directory: ".codex", configName: "hooks.json", event: "PostToolUse"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			if err := os.MkdirAll(filepath.Join(home, test.directory), 0o700); err != nil {
				t.Fatal(err)
			}
			installed, err := Plan(context.Background(), Options{Home: home, Harnesses: []string{test.harness}}, BuiltinBundle())
			if err != nil {
				t.Fatal(err)
			}
			if err := Apply(installed); err != nil {
				t.Fatal(err)
			}
			configPath := filepath.Join(home, test.directory, test.configName)
			document := readJSONMap(t, configPath)
			hooks := document["hooks"].(map[string]any)
			hooks[test.event] = []any{}
			writeJSONMap(t, configPath, document)

			uninstall, err := Plan(context.Background(), Options{Home: home, Harnesses: []string{test.harness}, Uninstall: true}, BuiltinBundle())
			if err != nil {
				t.Fatal(err)
			}
			if err := Apply(uninstall); err != nil {
				t.Fatal(err)
			}
			after := readJSONMap(t, configPath)
			afterHooks, ok := after["hooks"].(map[string]any)
			if !ok {
				t.Fatalf("uninstall removed hooks object while preserving empty %s array", test.event)
			}
			rawEvent, exists := afterHooks[test.event]
			if !exists {
				t.Fatalf("uninstall removed unrelated empty %s array", test.event)
			}
			if groups, ok := rawEvent.([]any); !ok || len(groups) != 0 {
				t.Fatalf("%s = %#v, want empty array", test.event, rawEvent)
			}
		})
	}
}

func TestPiDriverRefusesUninstallWhenModifiedExtensionStillUsesSharedLauncher(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".pi", "agent"), 0o700); err != nil {
		t.Fatal(err)
	}
	installed, err := Plan(context.Background(), Options{Home: home, Harnesses: []string{"pi"}}, BuiltinBundle())
	if err != nil {
		t.Fatal(err)
	}
	if err := Apply(installed); err != nil {
		t.Fatal(err)
	}
	extensionPath := filepath.Join(home, ".pi", "agent", "extensions", "dualmem.ts")
	launcherPath := filepath.Join(home, ".config", "dualmem", "bin", "dualmem-run")
	modified := append(readFile(t, extensionPath), []byte("\n// retained local customization\n")...)
	if err := os.WriteFile(extensionPath, modified, 0o700); err != nil {
		t.Fatal(err)
	}
	beforeLauncher := readFile(t, launcherPath)

	result, err := Plan(context.Background(), Options{Home: home, Harnesses: []string{"pi"}, Uninstall: true}, BuiltinBundle())
	if err == nil {
		t.Fatal("unsafe pi uninstall was accepted")
	}
	if len(result.Changes) != 0 {
		t.Fatalf("unsafe pi uninstall exposed %d partial changes", len(result.Changes))
	}
	if after := readFile(t, extensionPath); !slices.Equal(after, modified) {
		t.Fatal("unsafe uninstall rewrote the modified pi extension")
	}
	if after := readFile(t, launcherPath); !slices.Equal(after, beforeLauncher) {
		t.Fatal("unsafe uninstall removed or changed the shared launcher")
	}
}

func TestPiDriverOmitsProviderDependentPromptHookAndCapability(t *testing.T) {
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
	assertPiExtensionContract(t, string(extension.After))
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

func writeLegacyHookCommands(t *testing.T, path string, commands []string, groupMetadata map[string]any) {
	t.Helper()
	groups := make([]any, 0, len(commands))
	for _, command := range commands {
		group := map[string]any{
			"hooks": []any{map[string]any{
				"type": "command", "command": command, "timeout": float64(5),
			}},
		}
		for key, value := range groupMetadata {
			group[key] = value
		}
		groups = append(groups, group)
	}
	document := map[string]any{
		"unrelated": "preserved",
		"hooks": map[string]any{
			"SessionStart": groups,
		},
	}
	writeJSONMap(t, path, document)
}

func recognizedLegacyCommand(adapter, value string) string {
	return "export GEMINI_API_KEY='" + value + "' && \"$HOME/go/bin/dualmem\" hook --adapter " + adapter
}

func readJSONMap(t *testing.T, path string) map[string]any {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal(readFile(t, path), &document); err != nil {
		t.Fatal(err)
	}
	return document
}

func writeJSONMap(t *testing.T, path string, document map[string]any) {
	t.Helper()
	raw, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func assertUnchangedBytes(t *testing.T, change Change, want []byte) {
	t.Helper()
	if change.Action != ActionUnchanged || !slices.Equal(change.Before, want) || !slices.Equal(change.After, want) {
		t.Fatalf("change for %s = %s with changed bytes, want unchanged", change.Path, change.Action)
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

func assertPiExtensionContract(t *testing.T, source string) {
	t.Helper()
	for _, required := range []string{
		"process.env.DUALMEM_RUN",
		".config\", \"dualmem\", \"bin\", \"dualmem-run",
		"execFile(DUALMEM_RUN, args",
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
	if strings.Contains(source, `submitEvent("session_start"`) || strings.Contains(source, "before_agent_start") {
		t.Fatal("pi extension registers provider-dependent automatic context hooks")
	}
}

func decodeJSONMap(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded
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
