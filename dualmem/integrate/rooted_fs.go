package integrate

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

func validateExistingDirectoryPathNoSymlinks(path string) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve integration directory %q: %w", path, err)
	}
	absolute = filepath.Clean(absolute)
	volumeRoot := filepath.VolumeName(absolute) + string(filepath.Separator)
	relative, err := filepath.Rel(volumeRoot, absolute)
	if err != nil {
		return fmt.Errorf("resolve integration directory %q below filesystem root: %w", path, err)
	}

	current, err := os.OpenRoot(volumeRoot)
	if err != nil {
		return fmt.Errorf("open filesystem root for integration directory %q: %w", path, err)
	}
	defer func() { _ = current.Close() }()
	if relative == "." {
		return nil
	}
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		before, err := current.Lstat(component)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("inspect integration directory component %q: %w", component, err)
		}
		if before.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("integration directory %q traverses symlink component %q", path, component)
		}
		if !before.IsDir() {
			return fmt.Errorf("integration directory component %q is not a directory", component)
		}

		child, err := current.OpenRoot(component)
		if err != nil {
			return fmt.Errorf("open integration directory component %q: %w", component, err)
		}
		opened, openedErr := child.Stat(".")
		after, afterErr := current.Lstat(component)
		if openedErr != nil || afterErr != nil || after.Mode()&os.ModeSymlink != 0 || !os.SameFile(before, opened) || !os.SameFile(after, opened) {
			_ = child.Close()
			return fmt.Errorf("integration directory component %q changed while opening", component)
		}
		_ = current.Close()
		current = child
	}
	return nil
}

func canonicalizeExistingAncestors(path string) (string, error) {
	missing := make([]string, 0, 4)
	current := filepath.Clean(path)
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			for i := len(missing) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, missing[i])
			}
			return filepath.Clean(resolved), nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("resolve integration home ancestors for %q: %w", path, err)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("resolve integration home ancestors for %q: %w", path, err)
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}

type anchoredFilesystem struct {
	home string
	root *os.Root
}

func openAnchoredFilesystem(home, pinnedHome string, create bool) (*anchoredFilesystem, error) {
	absolute, err := filepath.Abs(pinnedHome)
	if err != nil {
		return nil, fmt.Errorf("resolve integration home %q: %w", home, err)
	}
	absolute = filepath.Clean(absolute)
	volumeRoot := filepath.VolumeName(absolute) + string(filepath.Separator)
	relative, err := filepath.Rel(volumeRoot, absolute)
	if err != nil {
		return nil, fmt.Errorf("resolve integration home %q below filesystem root: %w", home, err)
	}
	current, err := os.OpenRoot(volumeRoot)
	if err != nil {
		return nil, fmt.Errorf("open filesystem root for integration home %q: %w", home, err)
	}
	if relative != "." {
		for _, component := range strings.Split(relative, string(filepath.Separator)) {
			child, err := openStableDirectory(current, component, create)
			if err != nil {
				_ = current.Close()
				return nil, fmt.Errorf("open integration home %q: %w", home, err)
			}
			_ = current.Close()
			current = child
		}
	}
	return &anchoredFilesystem{home: filepath.Clean(home), root: current}, nil
}

func (filesystem *anchoredFilesystem) close() error {
	return filesystem.root.Close()
}

type rootedTarget struct {
	parent      *os.Root
	closeParent bool
	name        string
	path        string
}

func (target rootedTarget) close() {
	if target.closeParent {
		_ = target.parent.Close()
	}
}

func (filesystem *anchoredFilesystem) target(path string, createParents bool) (rootedTarget, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return rootedTarget{}, fmt.Errorf("resolve integration target %q: %w", path, err)
	}
	absolute = filepath.Clean(absolute)
	relative, err := filepath.Rel(filesystem.home, absolute)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return rootedTarget{}, fmt.Errorf("integration target %q is outside home %q", path, filesystem.home)
	}
	components := strings.Split(relative, string(filepath.Separator))
	current := filesystem.root
	owned := false
	for _, component := range components[:len(components)-1] {
		child, err := openStableDirectory(current, component, createParents)
		if err != nil {
			if owned {
				_ = current.Close()
			}
			return rootedTarget{}, fmt.Errorf("open parent for integration target %q: %w", path, err)
		}
		if owned {
			_ = current.Close()
		}
		current = child
		owned = true
	}
	return rootedTarget{parent: current, closeParent: owned, name: components[len(components)-1], path: absolute}, nil
}

func openStableDirectory(parent *os.Root, name string, create bool) (*os.Root, error) {
	before, err := parent.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) && create {
		if err := parent.Mkdir(name, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return nil, err
		}
		before, err = parent.Lstat(name)
	}
	if err != nil {
		return nil, err
	}
	if before.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("directory component %q is a symlink", name)
	}
	if !before.IsDir() {
		return nil, fmt.Errorf("directory component %q is not a directory", name)
	}
	child, err := parent.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	opened, openedErr := child.Stat(".")
	after, afterErr := parent.Lstat(name)
	if openedErr != nil || afterErr != nil || after.Mode()&os.ModeSymlink != 0 || !os.SameFile(before, opened) || !os.SameFile(after, opened) {
		_ = child.Close()
		return nil, fmt.Errorf("directory component %q changed while opening", name)
	}
	return child, nil
}

func commonChangeHome(changes []Change) (string, error) {
	if len(changes) == 0 {
		return "", nil
	}
	common, err := filepath.Abs(filepath.Dir(changes[0].Path))
	if err != nil {
		return "", err
	}
	common = filepath.Clean(common)
	for _, change := range changes[1:] {
		candidate, err := filepath.Abs(filepath.Dir(change.Path))
		if err != nil {
			return "", err
		}
		candidate = filepath.Clean(candidate)
		for {
			relative, relErr := filepath.Rel(common, candidate)
			if relErr == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative) {
				break
			}
			parent := filepath.Dir(common)
			if parent == common {
				break
			}
			common = parent
		}
	}
	return common, nil
}
