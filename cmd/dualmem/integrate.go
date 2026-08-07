package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	harnessintegrate "github.com/goblincore/geoffreyengram/dualmem/integrate"
)

func cmdIntegrate() {
	status := runIntegrate(os.Args[2:], os.Stdout, os.Stderr)
	if status != 0 {
		os.Exit(status)
	}
}

func runIntegrate(args []string, out, errOut io.Writer) int {
	if len(args) > 0 && args[0] == "doctor" {
		return runIntegrateDoctor(args[1:], out, errOut)
	}
	defaultHome, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintln(errOut, "dualmem integrate: home directory unavailable")
		return 2
	}
	fs := flag.NewFlagSet("integrate", flag.ContinueOnError)
	fs.SetOutput(errOut)
	harness := fs.String("harness", "", "harness to install: claude, codex, pi, or all")
	home := fs.String("home", defaultHome, "home directory to configure")
	dryRun := fs.Bool("dry-run", false, "plan without writing")
	uninstall := fs.Bool("uninstall", false, "remove managed integration")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		fmt.Fprintln(errOut, "dualmem integrate: invalid arguments")
		return 2
	}
	if !validIntegrateHarness(*harness) || strings.TrimSpace(*home) == "" {
		fmt.Fprintln(errOut, "dualmem integrate: --harness must be claude, codex, pi, or all")
		return 2
	}

	result, err := harnessintegrate.Plan(context.Background(), harnessintegrate.Options{
		Home:      *home,
		Harnesses: []string{*harness},
		DryRun:    *dryRun,
		Uninstall: *uninstall,
	}, harnessintegrate.BuiltinBundle())
	if err != nil {
		fmt.Fprintf(errOut, "dualmem integrate: planning failed: %v\n", err)
		return 2
	}
	if !*dryRun {
		if err := harnessintegrate.Apply(result); err != nil {
			fmt.Fprintf(errOut, "dualmem integrate: apply failed: %v\n", err)
			return 1
		}
	}
	for _, change := range result.Changes {
		fmt.Fprintf(out, "harness=%s path=%s action=%s mode=%04o backup=%s\n",
			integrateChangeOwner(*home, change.Path), change.Path, change.Action, change.Mode.Perm(), change.BackupPath)
	}
	return 0
}

func runIntegrateDoctor(args []string, out, errOut io.Writer) int {
	defaultHome, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintln(errOut, "dualmem integrate doctor: home directory unavailable")
		return 2
	}
	defaultProject, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(errOut, "dualmem integrate doctor: project directory unavailable")
		return 2
	}
	fs := flag.NewFlagSet("integrate doctor", flag.ContinueOnError)
	fs.SetOutput(errOut)
	home := fs.String("home", defaultHome, "home directory to inspect")
	project := fs.String("project", defaultProject, "project directory to resolve")
	jsonOutput := fs.Bool("json", false, "write findings as JSON")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || strings.TrimSpace(*home) == "" || strings.TrimSpace(*project) == "" {
		fmt.Fprintln(errOut, "dualmem integrate doctor: invalid arguments")
		return 2
	}
	findings, err := harnessintegrate.Doctor(context.Background(), harnessintegrate.DoctorOptions{
		Home: *home, ProjectDir: *project,
	}, harnessintegrate.BuiltinBundle())
	if err != nil {
		fmt.Fprintf(errOut, "dualmem integrate doctor: inspection failed: %v\n", err)
		return 2
	}
	if *jsonOutput {
		if err := json.NewEncoder(out).Encode(findings); err != nil {
			fmt.Fprintf(errOut, "dualmem integrate doctor: encode findings: %v\n", err)
			return 2
		}
	} else {
		writeDoctorFindings(out, findings)
	}
	for _, finding := range findings {
		if finding.Severity == harnessintegrate.SeverityWarning || finding.Severity == harnessintegrate.SeverityError {
			return 1
		}
	}
	return 0
}

func writeDoctorFindings(out io.Writer, findings []harnessintegrate.Finding) {
	group := ""
	for _, finding := range findings {
		owner := finding.Harness
		if owner == "" {
			owner = "project"
		}
		if owner != group {
			if group != "" {
				fmt.Fprintln(out)
			}
			fmt.Fprintf(out, "%s:\n", owner)
			group = owner
		}
		fmt.Fprintf(out, "  %s %s: %s\n", finding.Severity, finding.Code, finding.Message)
		if finding.Fix != "" {
			fmt.Fprintf(out, "    fix: %s\n", finding.Fix)
		}
	}
}

func validIntegrateHarness(harness string) bool {
	switch harness {
	case "claude", "codex", "pi", "all":
		return true
	default:
		return false
	}
}

func integrateChangeOwner(home, path string) string {
	cleanPath := filepath.Clean(path)
	owners := []struct {
		name string
		root string
	}{
		{name: "shared", root: filepath.Join(home, ".config", "dualmem")},
		{name: "claude", root: filepath.Join(home, ".claude")},
		{name: "codex", root: filepath.Join(home, ".codex")},
		{name: "pi", root: filepath.Join(home, ".pi", "agent")},
	}
	for _, owner := range owners {
		relative, err := filepath.Rel(filepath.Clean(owner.root), cleanPath)
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return owner.name
		}
	}
	return "unknown"
}
