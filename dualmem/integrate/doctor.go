package integrate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/goblincore/geoffreyengram/dualmem/harness"
)

type Severity string

const (
	SeverityOK      Severity = "ok"
	SeverityInfo    Severity = "info"
	SeverityWarning Severity = "warning"
	SeverityError   Severity = "error"
)

type Finding struct {
	Code     string   `json:"code"`
	Severity Severity `json:"severity"`
	Harness  string   `json:"harness,omitempty"`
	Message  string   `json:"message"`
	Fix      string   `json:"fix,omitempty"`
}

type DoctorOptions struct {
	Home       string
	ProjectDir string
}

var literalCredentialPattern = regexp.MustCompile(`(?i)\b(?:GEMINI_API_KEY|GOOGLE_API_KEY|OPENAI_API_KEY|ANTHROPIC_API_KEY|ZAI_API_KEY)\s*(?:=|:)\s*(?:['\"])?[^\s'\";,&}]+`)

// Doctor inspects only local integration state. It deliberately does not
// construct a DualMem engine, load a provider, or contact the network.
func Doctor(ctx context.Context, opts DoctorOptions, bundle Bundle) ([]Finding, error) {
	home := strings.TrimSpace(opts.Home)
	if home == "" {
		return nil, fmt.Errorf("doctor home directory is required")
	}
	if info, err := os.Stat(home); err != nil {
		return nil, fmt.Errorf("inspect doctor home %q: %w", home, err)
	} else if !info.IsDir() {
		return nil, fmt.Errorf("doctor home %q is not a directory", home)
	}

	projectDir := strings.TrimSpace(opts.ProjectDir)
	if projectDir == "" {
		return nil, fmt.Errorf("doctor project directory is required")
	}
	projectInfo, err := os.Stat(projectDir)
	if err != nil {
		return nil, fmt.Errorf("inspect doctor project %q: %w", projectDir, err)
	}
	if !projectInfo.IsDir() {
		return nil, fmt.Errorf("doctor project %q is not a directory", projectDir)
	}
	projectDir = filepath.Clean(projectDir)

	result, err := Plan(ctx, Options{Home: home, Harnesses: []string{"all"}}, bundle)
	if err != nil {
		return nil, fmt.Errorf("inspect integration state: %w", err)
	}

	identity, err := harness.ResolveProject(ctx, harness.Event{CWD: projectDir}, harness.ResolveOptions{
		LegacyPrefix:           "claude:",
		AllowDirectoryFallback: true,
	})
	if err != nil {
		return nil, fmt.Errorf("resolve doctor project identity: %w", err)
	}
	findings := []Finding{{
		Code:     "project_identity",
		Severity: SeverityOK,
		Message:  fmt.Sprintf("project %q resolves to shared namespace %q", identity.Name, identity.Namespace),
	}}
	if identity.Projectless {
		findings = append(findings, Finding{
			Code: "projectless_cwd", Severity: SeverityWarning,
			Message: fmt.Sprintf("%q is not a Git project; its directory name determines the namespace", projectDir),
			Fix:     "Run from a repository or supply an explicit project namespace in the event.",
		})
	} else if identity.Root != "" && filepath.Clean(identity.Root) != projectDir {
		findings = append(findings, Finding{
			Code: "worktree_identity", Severity: SeverityInfo,
			Message: fmt.Sprintf("worktree %q shares project identity rooted at %q", projectDir, identity.Root),
		})
	}

	for _, detection := range result.Detections {
		if !detection.Present {
			continue
		}
		findings = append(findings, Finding{
			Code:     "capabilities",
			Severity: SeverityOK,
			Harness:  detection.Harness,
			Message:  fmt.Sprintf("installed capabilities: %s", joinCapabilities(detection.Capabilities)),
		})
		missing, err := missingIntegrationParts(home, detection.Harness)
		if err != nil {
			return nil, err
		}
		if !detection.Managed || len(missing) > 0 {
			detail := integrationArtifact(detection.Harness)
			if len(missing) > 0 {
				detail = strings.Join(missing, " and ")
			}
			findings = append(findings, Finding{
				Code: "missing_integration", Severity: SeverityWarning, Harness: detection.Harness,
				Message: fmt.Sprintf("%s is present but does not contain the managed DualMem %s", detection.Harness, detail),
				Fix:     "Run dualmem integrate --harness " + detection.Harness + " --dry-run, review it, then apply it.",
			})
		}
		if detection.Harness == "codex" || detection.Harness == "pi" {
			findings = append(findings, Finding{
				Code: "transcript_reader_unavailable", Severity: SeverityInfo, Harness: detection.Harness,
				Message: "transcript distillation is planned for Phase 2; lifecycle integration remains available.",
			})
		}
	}

	findings, err = appendSecurityFindings(findings, home)
	if err != nil {
		return nil, err
	}
	findings, err = appendHarnessConfigurationFindings(findings, home)
	if err != nil {
		return nil, err
	}
	for _, change := range result.Changes {
		if change.Action == ActionUnchanged {
			continue
		}
		owner := integrateChangeOwner(home, change.Path)
		findings = append(findings, Finding{
			Code: "installer_drift", Severity: SeverityWarning, Harness: owner,
			Message: fmt.Sprintf("current integration plan would %s %s", change.Action, filepath.Base(change.Path)),
			Fix:     "Review dualmem integrate --harness " + owner + " --dry-run before applying changes.",
		})
	}
	sortFindings(findings)
	return findings, nil
}

func missingIntegrationParts(home, name string) ([]string, error) {
	var hookPath, instructionsPath string
	var specs []hookSpec
	switch name {
	case "claude":
		hookPath = filepath.Join(home, ".claude", "settings.json")
		instructionsPath = filepath.Join(home, ".claude", "CLAUDE.md")
		specs = claudeHookSpecs
	case "codex":
		hookPath = filepath.Join(home, ".codex", "hooks.json")
		instructionsPath = filepath.Join(home, ".codex", "AGENTS.md")
		specs = codexHookSpecs
	case "pi":
		extension, err := readFileState(filepath.Join(home, ".pi", "agent", "extensions", "dualmem.ts"))
		if err != nil {
			return nil, err
		}
		instructions, err := readFileState(filepath.Join(home, ".pi", "agent", "AGENTS.md"))
		if err != nil {
			return nil, err
		}
		missing := make([]string, 0, 2)
		if !extension.exists || (!bytes.Equal(extension.bytes, renderedPiExtension(true)) && !bytes.Equal(extension.bytes, renderedPiExtension(false))) {
			missing = append(missing, "extension")
		}
		if !instructions.exists || !containsManagedInstructions(instructions.bytes) {
			missing = append(missing, "instruction block")
		}
		return missing, nil
	default:
		return nil, fmt.Errorf("unknown integration harness %q", name)
	}
	managedHooks, err := hookDocumentManaged(hookPath, specs)
	if err != nil {
		return nil, err
	}
	instructions, err := readFileState(instructionsPath)
	if err != nil {
		return nil, err
	}
	missing := make([]string, 0, 2)
	if !managedHooks {
		missing = append(missing, "hooks")
	}
	if !instructions.exists || !containsManagedInstructions(instructions.bytes) {
		missing = append(missing, "instruction block")
	}
	return missing, nil
}

func joinCapabilities(capabilities []Capability) string {
	values := make([]string, len(capabilities))
	for i, capability := range capabilities {
		values[i] = string(capability)
	}
	return strings.Join(values, ", ")
}

func integrationArtifact(harness string) string {
	switch harness {
	case "claude", "codex":
		return "hooks and instruction block"
	case "pi":
		return "extension and instruction block"
	default:
		return "integration"
	}
}

func appendSecurityFindings(findings []Finding, home string) ([]Finding, error) {
	for _, target := range []struct {
		path string
		name string
		mode fs.FileMode
	}{
		{filepath.Join(home, ".config", "dualmem", "env"), "credential env file", 0o600},
		{filepath.Join(home, ".config", "dualmem", "bin", "dualmem-run"), "launcher", 0o700},
	} {
		info, err := os.Lstat(target.path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("inspect %s: %w", target.name, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("inspect %s: %s is not a regular file", target.name, target.path)
		}
		if info.Mode().Perm()&0o077 != 0 {
			findings = append(findings, Finding{
				Code: "insecure_mode", Severity: SeverityError, Harness: "shared",
				Message: fmt.Sprintf("%s has mode %04o; it must not be group- or world-readable", target.path, info.Mode().Perm()),
				Fix:     fmt.Sprintf("chmod %04o %s", target.mode.Perm(), target.path),
			})
		}
	}
	return findings, nil
}

func appendHarnessConfigurationFindings(findings []Finding, home string) ([]Finding, error) {
	paths := []struct {
		harness string
		path    string
	}{
		{"claude", filepath.Join(home, ".claude", "settings.json")},
		{"claude", filepath.Join(home, ".claude", "CLAUDE.md")},
		{"codex", filepath.Join(home, ".codex", "hooks.json")},
		{"codex", filepath.Join(home, ".codex", "AGENTS.md")},
		{"pi", filepath.Join(home, ".pi", "agent", "extensions", "dualmem.ts")},
		{"pi", filepath.Join(home, ".pi", "agent", "AGENTS.md")},
	}
	for _, target := range paths {
		state, err := readFileState(target.path)
		if err != nil {
			return nil, err
		}
		if !state.exists {
			continue
		}
		if literalCredentialPattern.Match(state.bytes) {
			findings = append(findings, Finding{
				Code: "literal_credential", Severity: SeverityError, Harness: target.harness,
				Message: fmt.Sprintf("credential-shaped literal found in %s", target.path),
				Fix:     "Move credentials to ~/.config/dualmem/env (mode 0600) and rotate any exposed provider key.",
			})
		}
		if target.harness == "codex" && strings.HasSuffix(target.path, "AGENTS.md") && containsSplitNamespaceGuidance(state.bytes) {
			findings = append(findings, Finding{
				Code: "split_namespace_guidance", Severity: SeverityWarning, Harness: "codex",
				Message: fmt.Sprintf("%s contains Codex-specific namespace guidance", target.path),
				Fix:     "Let the shared DualMem runtime resolve project identity; do not derive a Codex namespace in instructions.",
			})
		}
	}

	codexHooks := filepath.Join(home, ".codex", "hooks.json")
	state, err := readFileState(codexHooks)
	if err != nil || !state.exists {
		return findings, err
	}
	if containsClaudeMatcher(state.bytes) {
		findings = append(findings, Finding{
			Code: "incompatible_matcher", Severity: SeverityWarning, Harness: "codex",
			Message: fmt.Sprintf("%s contains Claude-only hook matcher guidance", codexHooks),
			Fix:     "Use Codex's canonical apply_patch post-tool hook rather than Claude Read, Glob, Grep, Edit, or Write matchers.",
		})
	}
	return findings, nil
}

func containsSplitNamespaceGuidance(raw []byte) bool {
	return strings.Contains(strings.ToLower(string(raw)), "codex:")
}

func containsClaudeMatcher(raw []byte) bool {
	_, hooks, err := decodeHookDocument(raw)
	if err != nil {
		return false
	}
	for _, event := range hooks {
		groups, err := decodeHookGroups(event)
		if err != nil {
			return false
		}
		for _, group := range groups {
			var object struct {
				Matcher string `json:"matcher"`
			}
			if json.Unmarshal(group, &object) != nil {
				continue
			}
			switch object.Matcher {
			case "Read", "Glob", "Grep", "Edit|Write", "Edit", "Write":
				return true
			}
		}
	}
	return false
}

func integrateChangeOwner(home, path string) string {
	cleanPath := filepath.Clean(path)
	owners := []struct {
		name string
		root string
	}{
		{"shared", filepath.Join(home, ".config", "dualmem")},
		{"claude", filepath.Join(home, ".claude")},
		{"codex", filepath.Join(home, ".codex")},
		{"pi", filepath.Join(home, ".pi", "agent")},
	}
	for _, owner := range owners {
		relative, err := filepath.Rel(filepath.Clean(owner.root), cleanPath)
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return owner.name
		}
	}
	return "unknown"
}

func sortFindings(findings []Finding) {
	sort.SliceStable(findings, func(i, j int) bool {
		if findings[i].Harness != findings[j].Harness {
			return findings[i].Harness < findings[j].Harness
		}
		if findings[i].Code != findings[j].Code {
			return findings[i].Code < findings[j].Code
		}
		return findings[i].Message < findings[j].Message
	})
}
