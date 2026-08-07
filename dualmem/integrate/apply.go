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

var atomicWriteFile = writeFileNoClobber

var beforeQuarantineHook = func(Change) {}

var afterQuarantineHook = func(Change) {}

type targetSnapshot struct {
	exists bool
	info   fs.FileInfo
}

func Apply(result Result) error {
	if err := validateAndSortChanges(result.Changes); err != nil {
		return err
	}
	if result.home != "" {
		if err := validateChangesUnderHome(result.home, result.Changes); err != nil {
			return err
		}
	}
	snapshots := make([]targetSnapshot, len(result.Changes))
	for i := range result.Changes {
		snapshot, err := inspectChangeState(result.Changes[i])
		if err != nil {
			return err
		}
		snapshots[i] = snapshot
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
			if err := atomicWriteFile(change.Path, change.After, change.Mode); err != nil {
				return fmt.Errorf("create integration file %q: %w", change.Path, err)
			}
		case ActionUpdate:
			quarantine, err := quarantineExpectedTarget(*change, snapshots[i])
			if err != nil {
				return err
			}
			afterQuarantineHook(*change)
			if err := atomicWriteFile(change.Path, change.After, change.Mode); err != nil {
				return quarantine.abort(change.Path, fmt.Errorf("publish updated integration file %q: %w", change.Path, err))
			}
			if err := quarantine.discard(); err != nil {
				return fmt.Errorf("discard replaced integration file %q: %w", quarantine.path, err)
			}
		case ActionDelete:
			quarantine, err := quarantineExpectedTarget(*change, snapshots[i])
			if err != nil {
				return err
			}
			afterQuarantineHook(*change)
			if err := quarantine.discard(); err != nil {
				return fmt.Errorf("delete quarantined integration file %q: %w", quarantine.path, err)
			}
		}
	}
	return nil
}

func inspectChangeState(change Change) (targetSnapshot, error) {
	info, err := os.Lstat(change.Path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return targetSnapshot{}, fmt.Errorf("inspect integration target %q: %w", change.Path, err)
	}
	exists := err == nil
	if exists && info.Mode()&os.ModeSymlink != 0 {
		return targetSnapshot{}, fmt.Errorf("integration target %q is a symlink", change.Path)
	}
	if exists && !info.Mode().IsRegular() {
		return targetSnapshot{}, fmt.Errorf("integration target %q is not a regular file", change.Path)
	}

	switch change.Action {
	case ActionCreate:
		if exists {
			return targetSnapshot{}, fmt.Errorf("create target %q already exists", change.Path)
		}
		if len(change.Before) != 0 {
			return targetSnapshot{}, fmt.Errorf("create target %q has unexpected prior bytes", change.Path)
		}
	case ActionUpdate:
		if !exists {
			return targetSnapshot{}, fmt.Errorf("update target %q does not exist", change.Path)
		}
		if err := requireExactCurrent(change.Path, change.Before); err != nil {
			return targetSnapshot{}, err
		}
	case ActionDelete:
		if len(change.After) != 0 {
			return targetSnapshot{}, fmt.Errorf("delete target %q has a non-empty post-state", change.Path)
		}
		if err := validateDeleteProof(change); err != nil {
			return targetSnapshot{}, err
		}
		if !exists {
			return targetSnapshot{}, fmt.Errorf("delete target %q does not exist", change.Path)
		}
		if err := requireExactCurrent(change.Path, change.Before); err != nil {
			return targetSnapshot{}, err
		}
	case ActionUnchanged:
		if !bytes.Equal(change.Before, change.After) {
			return targetSnapshot{}, fmt.Errorf("unchanged target %q has different before and after bytes", change.Path)
		}
		if exists {
			if err := requireExactCurrent(change.Path, change.Before); err != nil {
				return targetSnapshot{}, err
			}
		} else if len(change.Before) != 0 {
			return targetSnapshot{}, fmt.Errorf("unchanged target %q does not exist", change.Path)
		}
	default:
		return targetSnapshot{}, fmt.Errorf("integration target %q has invalid action %q", change.Path, change.Action)
	}
	return targetSnapshot{exists: exists, info: info}, nil
}

func validateDeleteProof(change Change) error {
	switch change.DeleteProof.kind {
	case deleteProofOwnedAsset:
		if !bytes.Equal(change.Before, []byte(change.DeleteProof.ownedAsset)) {
			return fmt.Errorf("delete target %q does not match its canonical owned asset", change.Path)
		}
		return nil
	case deleteProofManagedBlock:
		remaining, err := RemoveManagedBlock(string(change.Before), change.DeleteProof.begin, change.DeleteProof.end)
		if err != nil {
			return fmt.Errorf("verify managed delete target %q: %w", change.Path, err)
		}
		if remaining != "" {
			return fmt.Errorf("managed delete target %q contains unrelated content", change.Path)
		}
		return nil
	default:
		return fmt.Errorf("delete target %q lacks trusted ownership provenance", change.Path)
	}
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

type quarantinedTarget struct {
	directory string
	path      string
}

func quarantineExpectedTarget(change Change, snapshot targetSnapshot) (*quarantinedTarget, error) {
	if !snapshot.exists || snapshot.info == nil {
		return nil, fmt.Errorf("integration target %q had no preflight identity", change.Path)
	}
	directory, err := os.MkdirTemp(filepath.Dir(change.Path), ".dualmem-quarantine-*")
	if err != nil {
		return nil, fmt.Errorf("create quarantine for %q: %w", change.Path, err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		_ = os.Remove(directory)
		return nil, fmt.Errorf("protect quarantine for %q: %w", change.Path, err)
	}
	quarantine := &quarantinedTarget{directory: directory, path: filepath.Join(directory, "target")}
	beforeQuarantineHook(change)
	if err := os.Rename(change.Path, quarantine.path); err != nil {
		_ = os.Remove(directory)
		return nil, fmt.Errorf("quarantine integration target %q: %w", change.Path, err)
	}
	info, err := os.Lstat(quarantine.path)
	if err != nil {
		return nil, quarantine.abort(change.Path, fmt.Errorf("inspect quarantined integration target %q: %w", change.Path, err))
	}
	if !info.Mode().IsRegular() || !os.SameFile(snapshot.info, info) {
		return nil, quarantine.abort(change.Path, fmt.Errorf("integration target %q was replaced after preflight", change.Path))
	}
	current, err := os.ReadFile(quarantine.path)
	if err != nil {
		return nil, quarantine.abort(change.Path, fmt.Errorf("read quarantined integration target %q: %w", change.Path, err))
	}
	if !bytes.Equal(current, change.Before) {
		return nil, quarantine.abort(change.Path, fmt.Errorf("integration target %q changed after preflight", change.Path))
	}
	return quarantine, nil
}

func (quarantine *quarantinedTarget) abort(target string, cause error) error {
	restored, restoreErr := quarantine.restoreNoClobber(target)
	if restored {
		return cause
	}
	if restoreErr != nil {
		return fmt.Errorf("%w; displaced target preserved at %q after restore error: %v", cause, quarantine.path, restoreErr)
	}
	return fmt.Errorf("%w; displaced target preserved at %q", cause, quarantine.path)
}

func (quarantine *quarantinedTarget) restoreNoClobber(target string) (bool, error) {
	info, err := os.Lstat(quarantine.path)
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, nil
	}
	if err := os.Link(quarantine.path, target); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return false, nil
		}
		return false, err
	}
	if err := quarantine.discard(); err != nil {
		return true, err
	}
	return true, nil
}

func (quarantine *quarantinedTarget) discard() error {
	if err := os.Remove(quarantine.path); err != nil {
		return err
	}
	// Once the quarantined inode is removed, the requested update/delete is
	// complete. An empty private-directory cleanup failure must not turn that
	// successful material operation into an abort with no recoverable inode.
	_ = os.Remove(quarantine.directory)
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
		if err := atomicWriteFile(backupPath, content, 0o600); errors.Is(err, fs.ErrExist) {
			continue
		} else if err != nil {
			return "", fmt.Errorf("back up integration file %q: %w", path, err)
		}
		return backupPath, nil
	}
	return "", fmt.Errorf("could not allocate backup path for %q", path)
}

func writeFileNoClobber(path string, content []byte, mode fs.FileMode) error {
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
	if err := os.Link(temporaryPath, path); err != nil {
		return err
	}
	return nil
}
