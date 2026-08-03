package integrate

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPiExtensionSurfacesResponseAndSuccessfulStderrDiagnosticsRedacted(t *testing.T) {
	piPath, err := exec.LookPath("pi")
	if err != nil {
		t.Skip("installed pi is required for the extension loader fixture")
	}
	resolvedPi, err := filepath.EvalSymlinks(piPath)
	if err != nil {
		t.Fatal(err)
	}
	loaderPath := filepath.Join(filepath.Dir(resolvedPi), "core", "extensions", "loader.js")
	if _, err := os.Stat(loaderPath); err != nil {
		t.Skipf("installed pi extension loader unavailable: %v", err)
	}
	nodePath, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required for the extension loader fixture")
	}

	temporary := t.TempDir()
	extensionPath := filepath.Join(temporary, "dualmem.ts")
	if err := os.WriteFile(extensionPath, renderedPiExtension(), 0o700); err != nil {
		t.Fatal(err)
	}
	launcherPath := filepath.Join(temporary, "dualmem-run")
	launcher := `#!/bin/sh
case "$DUALMEM_FIXTURE_MODE" in
  response)
    printf '%s\n' '{"schema_version":"1.0","action":"none","diagnostics":[{"code":"memory_unavailable","message":"redacted"}]}'
    ;;
  stderr)
    printf '%s\n' 'fixture-stderr-secret' >&2
    printf '%s\n' '{"schema_version":"1.0","action":"none"}'
    ;;
esac
`
	if err := os.WriteFile(launcherPath, []byte(launcher), 0o700); err != nil {
		t.Fatal(err)
	}
	fixturePath, err := filepath.Abs(filepath.Join("testdata", "pi-extension-loader.mjs"))
	if err != nil {
		t.Fatal(err)
	}

	for _, mode := range []string{"response", "stderr"} {
		t.Run(mode, func(t *testing.T) {
			command := exec.Command(nodePath, fixturePath, extensionPath, loaderPath)
			command.Env = append(os.Environ(), "DUALMEM_RUN="+launcherPath, "DUALMEM_FIXTURE_MODE="+mode)
			output, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("pi loader fixture: %v: %s", err, output)
			}
			if strings.Contains(string(output), "fixture-stderr-secret") {
				t.Fatalf("pi extension exposed successful-exit stderr: %s", output)
			}
			var notifications []struct {
				Message string `json:"message"`
				Level   string `json:"level"`
			}
			if err := json.Unmarshal(output, &notifications); err != nil {
				t.Fatalf("decode fixture notifications: %v: %s", err, output)
			}
			if len(notifications) != 1 || notifications[0].Message != "dualmem lifecycle unavailable" || notifications[0].Level != "warning" {
				t.Fatalf("notifications = %#v, want one redacted warning", notifications)
			}
		})
	}
}
