package harness

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

type ResolveOptions struct {
	LegacyPrefix           string
	ConfiguredProject      string
	ConfiguredNamespace    string
	AllowDirectoryFallback bool
	GitCommonDir           func(context.Context, string) (string, error)
}

type ProjectIdentity struct {
	Root        string
	Name        string
	Namespace   string
	Projectless bool
}

func DefaultResolveOptions() ResolveOptions {
	return ResolveOptions{
		LegacyPrefix: "claude:",
		GitCommonDir: gitCommonDir,
	}
}

// ResolveProject establishes one harness-independent project identity. Git
// common-directory lookup makes a repository and all of its linked worktrees
// resolve to the same namespace.
func ResolveProject(ctx context.Context, event Event, opts ResolveOptions) (ProjectIdentity, error) {
	if event.Project.Namespace != "" {
		root := cleanNonEmptyPath(event.Project.Root)
		name := event.Project.Namespace
		if root != "" {
			name = filepath.Base(root)
		} else if opts.LegacyPrefix != "" {
			name = strings.TrimPrefix(name, opts.LegacyPrefix)
		}
		return ProjectIdentity{Root: root, Name: name, Namespace: event.Project.Namespace}, nil
	}

	lookup := opts.GitCommonDir
	if lookup == nil {
		lookup = gitCommonDir
	}
	directories := []string{event.Project.Root, event.CWD}
	previous := ""
	for _, directory := range directories {
		directory = cleanNonEmptyPath(directory)
		if directory == "" || directory == previous {
			continue
		}
		previous = directory
		commonDir, err := lookup(ctx, directory)
		if err != nil {
			continue
		}
		root := repositoryRoot(directory, commonDir)
		name := projectName(root)
		if name == "" {
			continue
		}
		return ProjectIdentity{
			Root:      root,
			Name:      name,
			Namespace: opts.LegacyPrefix + name,
		}, nil
	}

	if namespace := strings.TrimSpace(opts.ConfiguredNamespace); namespace != "" {
		return ProjectIdentity{
			Name:      strings.TrimPrefix(namespace, opts.LegacyPrefix),
			Namespace: namespace,
		}, nil
	}

	if configured := strings.TrimSpace(opts.ConfiguredProject); configured != "" {
		return ProjectIdentity{
			Name:      configured,
			Namespace: opts.LegacyPrefix + configured,
		}, nil
	}

	if opts.AllowDirectoryFallback {
		root := cleanNonEmptyPath(event.CWD)
		name := projectName(root)
		if name != "" {
			return ProjectIdentity{
				Root:        root,
				Name:        name,
				Namespace:   opts.LegacyPrefix + name,
				Projectless: true,
			}, nil
		}
	}

	return ProjectIdentity{}, fmt.Errorf("cannot resolve project identity for cwd %q", event.CWD)
}

func gitCommonDir(ctx context.Context, directory string) (string, error) {
	output, err := exec.CommandContext(
		ctx,
		"git",
		"-C",
		directory,
		"rev-parse",
		"--path-format=absolute",
		"--git-common-dir",
	).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

func repositoryRoot(directory, commonDir string) string {
	commonDir = strings.TrimSpace(commonDir)
	if commonDir == "" {
		return ""
	}
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(directory, commonDir)
	}
	commonDir = filepath.Clean(commonDir)
	if filepath.Base(commonDir) == ".git" {
		return filepath.Dir(commonDir)
	}
	return commonDir
}

func cleanNonEmptyPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	return filepath.Clean(path)
}

func projectName(root string) string {
	if root == "" {
		return ""
	}
	name := filepath.Base(root)
	if name == "." || name == string(filepath.Separator) {
		return ""
	}
	return name
}
