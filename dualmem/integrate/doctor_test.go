package integrate

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDoctorReportsInstalledCapabilitiesAndPhaseTwoGaps(t *testing.T) {
	setPiPromptSupport(t, true)
	home := populatedHarnessHome(t)
	installed, err := Plan(context.Background(), Options{Home: home, Harnesses: []string{"all"}}, BuiltinBundle())
	if err != nil {
		t.Fatal(err)
	}
	if err := Apply(installed); err != nil {
		t.Fatal(err)
	}

	findings, err := Doctor(context.Background(), DoctorOptions{
		Home: home, ProjectDir: repositoryForDoctor(t),
	}, BuiltinBundle())
	if err != nil {
		t.Fatal(err)
	}
	for _, finding := range findings {
		if finding.Severity == SeverityError {
			t.Fatalf("installed setup has error finding: %#v", finding)
		}
	}
	assertDoctorFinding(t, findings, "project_identity", SeverityOK, "")
	assertDoctorFinding(t, findings, "transcript_reader_unavailable", SeverityInfo, "codex")
	assertDoctorFinding(t, findings, "transcript_reader_unavailable", SeverityInfo, "pi")
	assertDoctorFinding(t, findings, "installed_capabilities", SeverityOK, "claude")
}

func TestDoctorRequiresEveryManagedHookAndDoesNotOverclaimCapabilities(t *testing.T) {
	setPiPromptSupport(t, true)
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
	delete(document["hooks"].(map[string]any), "PreToolUse")
	writeJSONMap(t, settingsPath, document)

	findings, err := Doctor(context.Background(), DoctorOptions{Home: home, ProjectDir: repositoryForDoctor(t)}, BuiltinBundle())
	if err != nil {
		t.Fatal(err)
	}
	assertDoctorFinding(t, findings, "missing_integration", SeverityWarning, "claude")
	assertDoctorFinding(t, findings, "supported_capabilities", SeverityInfo, "claude")
	if hasDoctorFinding(findings, "installed_capabilities", "claude") {
		t.Fatal("partial Claude hooks were reported as installed capabilities")
	}
}

func TestDoctorScansSharedLauncherForStaticCredentialsWithoutLeakingValues(t *testing.T) {
	home := t.TempDir()
	launcherPath := filepath.Join(home, ".config", "dualmem", "bin", "dualmem-run")
	if err := os.MkdirAll(filepath.Dir(launcherPath), 0o700); err != nil {
		t.Fatal(err)
	}
	const credential = "fixture-launcher-static-value"
	if err := os.WriteFile(launcherPath, []byte("#!/bin/sh\nexport OPENAI_API_KEY='"+credential+"'\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	findings, err := Doctor(context.Background(), DoctorOptions{Home: home, ProjectDir: repositoryForDoctor(t)}, BuiltinBundle())
	if err != nil {
		t.Fatal(err)
	}
	assertDoctorFinding(t, findings, "literal_credential", SeverityError, "shared")
	for _, finding := range findings {
		if strings.Contains(finding.Message+finding.Fix, credential) {
			t.Fatalf("shared credential value leaked: %#v", finding)
		}
	}
}

func TestDoctorDoesNotTreatEnvironmentReferencesAsLiteralCredentials(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".codex", "AGENTS.md"), []byte(
		"GEMINI_API_KEY=\"$GEMINI_API_KEY\"\nOPENAI_API_KEY: \"${OPENAI_API_KEY}\"\n",
	), 0o600); err != nil {
		t.Fatal(err)
	}

	findings, err := Doctor(context.Background(), DoctorOptions{Home: home, ProjectDir: repositoryForDoctor(t)}, BuiltinBundle())
	if err != nil {
		t.Fatal(err)
	}
	if hasDoctorFinding(findings, "literal_credential", "codex") {
		t.Fatalf("environment indirection was classified as a literal credential: %#v", findings)
	}
}

func TestDoctorCredentialClassificationIgnoresEmptyAndGenericEnvironmentReferences(t *testing.T) {
	for _, input := range []string{
		"OPENAI_API_KEY=",
		"OPENAI_API_KEY=\"\"",
		"OPENAI_API_KEY=''",
		"OPENAI_API_KEY=$PROVIDER_KEY",
		"OPENAI_API_KEY=\"$PROVIDER_KEY\"",
		"OPENAI_API_KEY=${PROVIDER_KEY}",
		"OPENAI_API_KEY=\"${PROVIDER_KEY}\"",
		"OPENAI_API_KEY=process.env.PROVIDER_KEY",
	} {
		t.Run(input, func(t *testing.T) {
			if containsLiteralCredential([]byte(input)) {
				t.Fatalf("%q was classified as a static credential", input)
			}
		})
	}
	if !containsLiteralCredential([]byte("OPENAI_API_KEY='fixture-static-value'")) {
		t.Fatal("static credential-shaped value was not detected")
	}
}

func TestDoctorSharedDriftUsesValidRepairHarness(t *testing.T) {
	setPiPromptSupport(t, true)
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
	if err := os.Chmod(filepath.Join(home, ".config", "dualmem", "env"), 0o644); err != nil {
		t.Fatal(err)
	}

	findings, err := Doctor(context.Background(), DoctorOptions{Home: home, ProjectDir: repositoryForDoctor(t)}, BuiltinBundle())
	if err != nil {
		t.Fatal(err)
	}
	for _, finding := range findings {
		if finding.Code != "installer_drift" || finding.Harness != "shared" {
			continue
		}
		if strings.Contains(finding.Fix, "--harness shared") || !strings.Contains(finding.Fix, "--harness all") {
			t.Fatalf("shared drift has invalid repair command: %#v", finding)
		}
		return
	}
	t.Fatal("missing shared installer drift finding")
}

func TestDoctorFindsUnsafeAndDriftedHarnessStateWithoutLeakingCredential(t *testing.T) {
	setPiPromptSupport(t, true)
	home := populatedHarnessHome(t)
	installed, err := Plan(context.Background(), Options{Home: home, Harnesses: []string{"all"}}, BuiltinBundle())
	if err != nil {
		t.Fatal(err)
	}
	if err := Apply(installed); err != nil {
		t.Fatal(err)
	}

	codexHooks := filepath.Join(home, ".codex", "hooks.json")
	writeJSONMap(t, codexHooks, map[string]any{
		"hooks": map[string]any{
			"PreToolUse": []any{map[string]any{
				"matcher": "Read",
				"hooks": []any{map[string]any{
					"type": "command", "command": "export GEMINI_API_KEY='fixture-doctor-value' && dualmem hook --adapter codex",
				}},
			}},
		},
	})
	if err := os.WriteFile(filepath.Join(home, ".codex", "AGENTS.md"), []byte("namespace Codex:geoffreyengram\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(home, ".config", "dualmem", "env"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(home, ".config", "dualmem", "bin", "dualmem-run"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(home, ".pi", "agent", "extensions", "dualmem.ts")); err != nil {
		t.Fatal(err)
	}

	findings, err := Doctor(context.Background(), DoctorOptions{
		Home: home, ProjectDir: repositoryForDoctor(t),
	}, BuiltinBundle())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []struct {
		code     string
		severity Severity
		harness  string
	}{
		{"split_namespace_guidance", SeverityWarning, "codex"},
		{"incompatible_matcher", SeverityWarning, "codex"},
		{"literal_credential", SeverityError, "codex"},
		{"insecure_mode", SeverityError, "shared"},
		{"missing_integration", SeverityWarning, "pi"},
		{"installer_drift", SeverityWarning, "codex"},
	} {
		assertDoctorFinding(t, findings, want.code, want.severity, want.harness)
	}
	for _, finding := range findings {
		if strings.Contains(finding.Message, "fixture-doctor-value") || strings.Contains(finding.Fix, "fixture-doctor-value") {
			t.Fatalf("doctor leaked a credential-shaped value: %#v", finding)
		}
	}
}

func TestDoctorReportsProjectlessAndWorktreeIdentity(t *testing.T) {
	findings, err := Doctor(context.Background(), DoctorOptions{
		Home: t.TempDir(), ProjectDir: t.TempDir(),
	}, BuiltinBundle())
	if err != nil {
		t.Fatal(err)
	}
	assertDoctorFinding(t, findings, "projectless_cwd", SeverityWarning, "")

	worktree := repositoryForDoctor(t)
	findings, err = Doctor(context.Background(), DoctorOptions{
		Home: t.TempDir(), ProjectDir: worktree,
	}, BuiltinBundle())
	if err != nil {
		t.Fatal(err)
	}
	assertDoctorFinding(t, findings, "worktree_identity", SeverityInfo, "")
}

func TestDoctorDoesNotLabelPrimaryCheckoutSubdirectoryAsWorktree(t *testing.T) {
	repository := filepath.Join(t.TempDir(), "primary")
	if err := os.MkdirAll(filepath.Join(repository, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("git", "init", repository)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	findings, err := Doctor(context.Background(), DoctorOptions{
		Home: t.TempDir(), ProjectDir: filepath.Join(repository, "nested"),
	}, BuiltinBundle())
	if err != nil {
		t.Fatal(err)
	}
	if hasDoctorFinding(findings, "worktree_identity", "") {
		t.Fatalf("primary checkout subdirectory was labeled as a worktree: %#v", findings)
	}
}

func repositoryForDoctor(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func assertDoctorFinding(t *testing.T, findings []Finding, code string, severity Severity, harness string) {
	t.Helper()
	for _, finding := range findings {
		if finding.Code == code && finding.Severity == severity && finding.Harness == harness {
			return
		}
	}
	t.Fatalf("missing finding code=%q severity=%q harness=%q in %#v", code, severity, harness, findings)
}

func hasDoctorFinding(findings []Finding, code, harness string) bool {
	for _, finding := range findings {
		if finding.Code == code && finding.Harness == harness {
			return true
		}
	}
	return false
}
