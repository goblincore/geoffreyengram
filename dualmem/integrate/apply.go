package integrate

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

var atomicWriteFile = writeFileAtomically

func Apply(result Result) error {
	if err := validateAndSortChanges(result.Changes); err != nil {
		return err
	}
	for i := range result.Changes {
		if err := validateChangeState(result.Changes[i]); err != nil {
			return err
		}
	}
	for i := range result.Changes {
		change := &result.Changes[i]
		if change.Action != ActionUpdate {
			continue
		}
		backupPath, err := createBackup(change.Path, change.Before)
		if err != nil {
			return err
		}
		change.BackupPath = backupPath
	}
	for i := range result.Changes {
		change := &result.Changes[i]
		switch change.Action {
		case ActionUnchanged:
			continue
		case ActionCreate:
			if err := validateChangeState(*change); err != nil {
				return err
			}
			if err := atomicWriteFile(change.Path, change.After, change.Mode); err != nil {
				return fmt.Errorf("create integration file %q: %w", change.Path, err)
			}
		case ActionUpdate:
			if err := validateChangeState(*change); err != nil {
				return err
			}
			if err := atomicWriteFile(change.Path, change.After, change.Mode); err != nil {
				return fmt.Errorf("update integration file %q: %w", change.Path, err)
			}
		case ActionDelete:
			if err := validateChangeState(*change); err != nil {
				return err
			}
			if err := os.Remove(change.Path); err != nil {
				return fmt.Errorf("delete integration file %q: %w", change.Path, err)
			}
		}
	}
	return nil
}

func validateChangeState(change Change) error {
	info, err := os.Lstat(change.Path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("inspect integration target %q: %w", change.Path, err)
	}
	exists := err == nil
	if exists && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("integration target %q is a symlink", change.Path)
	}
	if exists && !info.Mode().IsRegular() {
		return fmt.Errorf("integration target %q is not a regular file", change.Path)
	}

	switch change.Action {
	case ActionCreate:
		if exists {
			return fmt.Errorf("create target %q already exists", change.Path)
		}
		if len(change.Before) != 0 {
			return fmt.Errorf("create target %q has unexpected prior bytes", change.Path)
		}
	case ActionUpdate:
		if !exists {
			return fmt.Errorf("update target %q does not exist", change.Path)
		}
		if err := requireExactCurrent(change.Path, change.Before); err != nil {
			return err
		}
	case ActionDelete:
		if len(change.After) != 0 {
			return fmt.Errorf("delete target %q has a non-empty post-state", change.Path)
		}
		if !exists {
			return fmt.Errorf("delete target %q does not exist", change.Path)
		}
		if err := requireExactCurrent(change.Path, change.Before); err != nil {
			return err
		}
	case ActionUnchanged:
		if !bytes.Equal(change.Before, change.After) {
			return fmt.Errorf("unchanged target %q has different before and after bytes", change.Path)
		}
		if exists {
			if err := requireExactCurrent(change.Path, change.Before); err != nil {
				return err
			}
		} else if len(change.Before) != 0 {
			return fmt.Errorf("unchanged target %q does not exist", change.Path)
		}
	default:
		return fmt.Errorf("integration target %q has invalid action %q", change.Path, change.Action)
	}
	return nil
}

func requireExactCurrent(path string, before []byte) error {
	current, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read integration target %q: %w", path, err)
	}
	if !bytes.Equal(current, before) {
		return fmt.Errorf("integration target %q changed after planning", path)
	}
	return nil
}

func createBackup(path string, content []byte) (string, error) {
	stamp := time.Now().UTC().Format("20060102T150405.000000000Z")
	for sequence := 0; sequence < 100; sequence++ {
		backupPath := fmt.Sprintf("%s.dualmem-backup-%s", path, stamp)
		if sequence > 0 {
			backupPath = fmt.Sprintf("%s-%d", backupPath, sequence)
		}
		if _, err := os.Lstat(backupPath); err == nil {
			continue
		} else if !errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("inspect backup target %q: %w", backupPath, err)
		}
		if err := atomicWriteFile(backupPath, content, 0o600); err != nil {
			return "", fmt.Errorf("back up integration file %q: %w", path, err)
		}
		return backupPath, nil
	}
	return "", fmt.Errorf("could not allocate backup path for %q", path)
}

func writeFileAtomically(path string, content []byte, mode fs.FileMode) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".dualmem-write-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	closed := false
	defer func() {
		if !closed {
			_ = temporary.Close()
		}
		_ = os.Remove(temporaryPath)
	}()
	if _, err := temporary.Write(content); err != nil {
		return err
	}
	if err := temporary.Chmod(mode.Perm()); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		closed = true
		return err
	}
	closed = true
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	return nil
}
