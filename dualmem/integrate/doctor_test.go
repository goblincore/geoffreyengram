package integrate

import (
	"context"
	"os"
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
	assertDoctorFinding(t, findings, "capabilities", SeverityOK, "claude")
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
