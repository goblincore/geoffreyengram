package integrate

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

var atomicWriteFile = writeFileNoClobber

type mutationStage string

const (
	mutationBackup     mutationStage = "backup"
	mutationCreate     mutationStage = "create"
	mutationQuarantine mutationStage = "quarantine"
	mutationPublish    mutationStage = "publish"
	mutationDiscard    mutationStage = "discard"
	mutationRestore    mutationStage = "restore"
)

var beforeMutationHook = func(Change, mutationStage) {}

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
	home := result.home
	pinnedHome := result.pinnedHome
	if home == "" {
		var err error
		home, err = commonChangeHome(result.Changes)
		if err != nil {
			return fmt.Errorf("derive integration home: %w", err)
		}
		if home != "" {
			home, pinnedHome, err = integrationHome(home)
			if err != nil {
				return err
			}
		}
	}
	if home == "" {
		return nil
	}
	if err := validateExistingDirectoryPathNoSymlinks(pinnedHome); err != nil {
		return err
	}
	if err := validateChangesUnderHome(home, result.Changes); err != nil {
		return err
	}
	snapshots := make([]targetSnapshot, len(result.Changes))
	for i := range result.Changes {
		snapshot, err := inspectChangeState(result.Changes[i])
		if err != nil {
			return err
		}
		snapshots[i] = snapshot
	}
	filesystem, err := openAnchoredFilesystem(home, pinnedHome, true)
	if err != nil {
		return err
	}
	defer func() { _ = filesystem.close() }()
	for i := range result.Changes {
		change := &result.Changes[i]
		if change.Action != ActionUpdate {
			continue
		}
		backupPath, err := createBackup(filesystem, *change)
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
			beforeMutationHook(*change, mutationCreate)
			target, err := filesystem.target(change.Path, true)
			if err != nil {
				return err
			}
			err = atomicWriteFile(target, change.After, change.Mode)
			target.close()
			if err != nil {
				return fmt.Errorf("create integration file %q: %w", change.Path, err)
			}
		case ActionUpdate:
			quarantine, err := quarantineExpectedTarget(filesystem, *change, snapshots[i])
			if err != nil {
				return err
			}
			afterQuarantineHook(*change)
			beforeMutationHook(*change, mutationPublish)
			if err := atomicWriteFile(quarantine.target, change.After, change.Mode); err != nil {
				return quarantine.abort(*change, fmt.Errorf("publish updated integration file %q: %w", change.Path, err))
			}
			beforeMutationHook(*change, mutationDiscard)
			if err := quarantine.discard(); err != nil {
				return fmt.Errorf("discard replaced integration file %q: %w", quarantine.path, err)
			}
		case ActionDelete:
			quarantine, err := quarantineExpectedTarget(filesystem, *change, snapshots[i])
			if err != nil {
				return err
			}
			afterQuarantineHook(*change)
			beforeMutationHook(*change, mutationDiscard)
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
	target        rootedTarget
	directoryName string
	directory     *os.Root
	path          string
}

func quarantineExpectedTarget(filesystem *anchoredFilesystem, change Change, snapshot targetSnapshot) (*quarantinedTarget, error) {
	if !snapshot.exists || snapshot.info == nil {
		return nil, fmt.Errorf("integration target %q had no preflight identity", change.Path)
	}
	beforeMutationHook(change, mutationQuarantine)
	target, err := filesystem.target(change.Path, false)
	if err != nil {
		return nil, err
	}
	directoryName, directory, err := createPrivateDirectory(target.parent, ".dualmem-quarantine-")
	if err != nil {
		target.close()
		return nil, fmt.Errorf("create quarantine for %q: %w", change.Path, err)
	}
	quarantine := &quarantinedTarget{
		target:        target,
		directoryName: directoryName,
		directory:     directory,
		path:          filepath.Join(filepath.Dir(change.Path), directoryName, "target"),
	}
	beforeQuarantineHook(change)
	if err := rootedRename(target.parent, target.name, directory, "target"); err != nil {
		quarantine.cleanupDirectory()
		return nil, fmt.Errorf("quarantine integration target %q: %w", change.Path, err)
	}
	info, err := directory.Lstat("target")
	if err != nil {
		return nil, quarantine.abort(change, fmt.Errorf("inspect quarantined integration target %q: %w", change.Path, err))
	}
	if !info.Mode().IsRegular() || !os.SameFile(snapshot.info, info) {
		return nil, quarantine.abort(change, fmt.Errorf("integration target %q was replaced after preflight", change.Path))
	}
	current, err := directory.ReadFile("target")
	if err != nil {
		return nil, quarantine.abort(change, fmt.Errorf("read quarantined integration target %q: %w", change.Path, err))
	}
	if !bytes.Equal(current, change.Before) {
		return nil, quarantine.abort(change, fmt.Errorf("integration target %q changed after preflight", change.Path))
	}
	return quarantine, nil
}

func (quarantine *quarantinedTarget) abort(change Change, cause error) error {
	beforeMutationHook(change, mutationRestore)
	restored, restoreErr := quarantine.restoreNoClobber()
	if restored {
		if restoreErr != nil {
			quarantine.release()
		}
		return cause
	}
	quarantine.release()
	if restoreErr != nil {
		return fmt.Errorf("%w; displaced target preserved at %q after restore error: %v", cause, quarantine.path, restoreErr)
	}
	return fmt.Errorf("%w; displaced target preserved at %q", cause, quarantine.path)
}

func (quarantine *quarantinedTarget) restoreNoClobber() (bool, error) {
	info, err := quarantine.directory.Lstat("target")
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, nil
	}
	if err := rootedLink(quarantine.directory, "target", quarantine.target.parent, quarantine.target.name); err != nil {
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
	if err := quarantine.directory.Remove("target"); err != nil {
		quarantine.release()
		return err
	}
	// Once the quarantined inode is removed, the requested update/delete is
	// complete. An empty private-directory cleanup failure must not turn that
	// successful material operation into an abort with no recoverable inode.
	quarantine.cleanupDirectory()
	return nil
}

func (quarantine *quarantinedTarget) cleanupDirectory() {
	_ = quarantine.directory.Close()
	_ = quarantine.target.parent.Remove(quarantine.directoryName)
	quarantine.target.close()
}

func (quarantine *quarantinedTarget) release() {
	_ = quarantine.directory.Close()
	quarantine.target.close()
}

func createBackup(filesystem *anchoredFilesystem, change Change) (string, error) {
	beforeMutationHook(change, mutationBackup)
	target, err := filesystem.target(change.Path, false)
	if err != nil {
		return "", err
	}
	defer target.close()
	stamp := time.Now().UTC().Format("20060102T150405.000000000Z")
	for sequence := 0; sequence < 100; sequence++ {
		backupPath := fmt.Sprintf("%s.dualmem-backup-%s", change.Path, stamp)
		if sequence > 0 {
			backupPath = fmt.Sprintf("%s-%d", backupPath, sequence)
		}
		backupName := filepath.Base(backupPath)
		if _, err := target.parent.Lstat(backupName); err == nil {
			continue
		} else if !errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("inspect backup target %q: %w", backupPath, err)
		}
		backupTarget := rootedTarget{parent: target.parent, name: backupName, path: backupPath}
		if err := atomicWriteFile(backupTarget, change.Before, 0o600); errors.Is(err, fs.ErrExist) {
			continue
		} else if err != nil {
			return "", fmt.Errorf("back up integration file %q: %w", change.Path, err)
		}
		return backupPath, nil
	}
	return "", fmt.Errorf("could not allocate backup path for %q", change.Path)
}

func writeFileNoClobber(target rootedTarget, content []byte, mode fs.FileMode) error {
	temporaryName, temporary, err := createPrivateFile(target.parent, ".dualmem-write-")
	if err != nil {
		return err
	}
	closed := false
	defer func() {
		if !closed {
			_ = temporary.Close()
		}
		_ = target.parent.Remove(temporaryName)
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
	if err := rootedLink(target.parent, temporaryName, target.parent, target.name); err != nil {
		return err
	}
	return nil
}

func createPrivateDirectory(parent *os.Root, prefix string) (string, *os.Root, error) {
	for attempt := 0; attempt < 100; attempt++ {
		name, err := privateName(prefix)
		if err != nil {
			return "", nil, err
		}
		if err := parent.Mkdir(name, 0o700); errors.Is(err, fs.ErrExist) {
			continue
		} else if err != nil {
			return "", nil, err
		}
		directory, err := openStableDirectory(parent, name, false)
		if err != nil {
			_ = parent.Remove(name)
			return "", nil, err
		}
		return name, directory, nil
	}
	return "", nil, fmt.Errorf("could not allocate private integration directory")
}

func createPrivateFile(parent *os.Root, prefix string) (string, *os.File, error) {
	for attempt := 0; attempt < 100; attempt++ {
		name, err := privateName(prefix)
		if err != nil {
			return "", nil, err
		}
		file, err := parent.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		return name, file, err
	}
	return "", nil, fmt.Errorf("could not allocate private integration file")
}

func privateName(prefix string) (string, error) {
	random := make([]byte, 12)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(random), nil
}
