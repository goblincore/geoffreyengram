package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestConsultCLI_NoQuery(t *testing.T) {
	// Build the binary
	binary := t.TempDir() + "/dualmem"
	build := exec.Command("go", "build", "-o", binary, ".")
	build.Dir = "."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build failed: %s\n%s", err, out)
	}

	// Run without query — should exit with error
	cmd := exec.Command(binary, "consult")
	cmd.Env = append(os.Environ(), "HOME="+t.TempDir())
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("expected error for missing query")
	}
	if !strings.Contains(string(out), "usage:") {
		t.Errorf("expected usage message, got: %s", out)
	}
}
