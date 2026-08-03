package integrate

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyCreatesAndUpdatesWithExactModesAndRestrictiveBackup(t *testing.T) {
	home := t.TempDir()
	created := filepath.Join(home, "nested", "created")
	updated := filepath.Join(home, "updated")
	if err := os.WriteFile(updated, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	result := Result{Changes: []Change{
		{Path: created, Action: ActionCreate, Mode: 0o640, After: []byte("created")},
		{Path: updated, Action: ActionUpdate, Mode: 0o600, Before: []byte("old"), After: []byte("new")},
	}}

	if err := Apply(result); err != nil {
		t.Fatal(err)
	}
	assertFile(t, created, "created", 0o640)
	assertFile(t, updated, "new", 0o600)
	backup := result.Changes[1].BackupPath
	if !strings.HasPrefix(backup, updated+".dualmem-backup-") {
		t.Fatalf("backup path %q is not timestamped beside target", backup)
	}
	assertFile(t, backup, "old", 0o600)
}

func TestApplyPreflightFailureMakesZeroWrites(t *testing.T) {
	home := t.TempDir()
	created := filepath.Join(home, "would-create")
	updated := filepath.Join(home, "existing")
	if err := os.WriteFile(updated, []byte("actual"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := Result{Changes: []Change{
		{Path: created, Action: ActionCreate, Mode: 0o600, After: []byte("new")},
		{Path: updated, Action: ActionUpdate, Mode: 0o600, Before: []byte("stale"), After: []byte("replacement")},
	}}
	if err := Apply(result); err == nil {
		t.Fatal("Apply accepted stale planned bytes")
	}
	if _, err := os.Lstat(created); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("preflight failure created a file: %v", err)
	}
	assertFile(t, updated, "actual", 0o600)
}

func TestApplyBackupFailurePreventsAllTargetMutations(t *testing.T) {
	home := t.TempDir()
	created := filepath.Join(home, "a-create")
	updated := filepath.Join(home, "z-update")
	if err := os.WriteFile(updated, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	originalWriter := atomicWriteFile
	t.Cleanup(func() { atomicWriteFile = originalWriter })
	atomicWriteFile = func(path string, data []byte, mode fs.FileMode) error {
		if strings.Contains(path, ".dualmem-backup-") {
			return errors.New("injected backup failure")
		}
		return originalWriter(path, data, mode)
	}
	result := Result{Changes: []Change{
		{Path: created, Action: ActionCreate, Mode: 0o600, After: []byte("created")},
		{Path: updated, Action: ActionUpdate, Mode: 0o600, Before: []byte("original"), After: []byte("replacement")},
	}}
	if err := Apply(result); err == nil {
		t.Fatal("Apply succeeded despite backup failure")
	}
	if _, err := os.Lstat(created); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("backup failure did not prevent create: %v", err)
	}
	assertFile(t, updated, "original", 0o600)
}

func TestApplyInjectedWriteFailureLeavesOriginalIntact(t *testing.T) {
	home := t.TempDir()
	target := filepath.Join(home, "config")
	if err := os.WriteFile(target, []byte("original"), 0o640); err != nil {
		t.Fatal(err)
	}
	originalWriter := atomicWriteFile
	t.Cleanup(func() { atomicWriteFile = originalWriter })
	atomicWriteFile = func(path string, data []byte, mode fs.FileMode) error {
		if path == target {
			return errors.New("injected write failure")
		}
		return originalWriter(path, data, mode)
	}

	err := Apply(Result{Changes: []Change{{
		Path: target, Action: ActionUpdate, Mode: 0o600, Before: []byte("original"), After: []byte("replacement"),
	}}})
	if err == nil {
		t.Fatal("Apply succeeded despite injected write failure")
	}
	assertFile(t, target, "original", 0o640)
}

func TestApplyRejectsSymlinkTarget(t *testing.T) {
	home := t.TempDir()
	realPath := filepath.Join(home, "real")
	linkPath := filepath.Join(home, "link")
	if err := os.WriteFile(realPath, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realPath, linkPath); err != nil {
		t.Fatal(err)
	}
	err := Apply(Result{Changes: []Change{{
		Path: linkPath, Action: ActionUpdate, Mode: 0o600, Before: []byte("original"), After: []byte("replacement"),
	}}})
	if err == nil {
		t.Fatal("Apply followed a target symlink")
	}
	assertFile(t, realPath, "original", 0o600)
}

func TestApplySecondPlanIsUnchanged(t *testing.T) {
	home := t.TempDir()
	driver := &fileStateDriver{name: "codex", path: filepath.Join(home, "config"), wanted: []byte("managed")}
	bundle := Bundle{Drivers: []Driver{driver}}
	first, err := Plan(context.Background(), Options{Home: home, Harnesses: []string{"codex"}}, bundle)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Changes) != 1 || first.Changes[0].Action != ActionCreate {
		t.Fatalf("first plan = %#v, want create", first.Changes)
	}
	if err := Apply(first); err != nil {
		t.Fatal(err)
	}
	second, err := Plan(context.Background(), Options{Home: home, Harnesses: []string{"codex"}}, bundle)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Changes) != 1 || second.Changes[0].Action != ActionUnchanged {
		t.Fatalf("second plan = %#v, want unchanged", second.Changes)
	}
	if err := Apply(second); err != nil {
		t.Fatal(err)
	}
}

func TestApplyUninstallDeletesOnlyExactEmptyPostState(t *testing.T) {
	home := t.TempDir()
	owned := filepath.Join(home, "owned")
	if err := os.WriteFile(owned, []byte("owned bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Apply(Result{Changes: []Change{{Path: owned, Action: ActionDelete, Before: []byte("owned bytes")}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(owned); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("owned file still exists: %v", err)
	}

	stale := filepath.Join(home, "stale")
	if err := os.WriteFile(stale, []byte("user changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Apply(Result{Changes: []Change{{Path: stale, Action: ActionDelete, Before: []byte("planned owned bytes")}}}); err == nil {
		t.Fatal("Apply deleted a file whose bytes changed")
	}
	assertFile(t, stale, "user changed", 0o600)
	if err := Apply(Result{Changes: []Change{{Path: stale, Action: ActionDelete, Before: []byte("user changed"), After: []byte("not empty")}}}); err == nil {
		t.Fatal("Apply accepted a delete with a non-empty post-state")
	}
	assertFile(t, stale, "user changed", 0o600)
}

func TestApplyManagedBlockUninstallPreservesUnrelatedContentAndDeletesEmptyFile(t *testing.T) {
	home := t.TempDir()
	withUserText := filepath.Join(home, "instructions")
	before := "user\n" + testBegin + "\nmanaged\n" + testEnd + "\n"
	after, err := RemoveManagedBlock(before, testBegin, testEnd)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(withUserText, []byte(before), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Apply(Result{Changes: []Change{{Path: withUserText, Action: ActionUpdate, Mode: 0o644, Before: []byte(before), After: []byte(after)}}}); err != nil {
		t.Fatal(err)
	}
	assertFile(t, withUserText, "user\n", 0o644)

	onlyManaged := filepath.Join(home, "only-managed")
	onlyBefore := testBegin + "\nmanaged\n" + testEnd + "\n"
	onlyAfter, err := RemoveManagedBlock(onlyBefore, testBegin, testEnd)
	if err != nil {
		t.Fatal(err)
	}
	if onlyAfter != "" {
		t.Fatalf("managed block removal left %q", onlyAfter)
	}
	if err := os.WriteFile(onlyManaged, []byte(onlyBefore), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Apply(Result{Changes: []Change{{Path: onlyManaged, Action: ActionDelete, Before: []byte(onlyBefore), After: []byte(onlyAfter)}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(onlyManaged); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("empty managed file still exists: %v", err)
	}
}

func TestApplyDryRunPlanDoesNotWriteAndSummaryOmitsBodies(t *testing.T) {
	home := t.TempDir()
	target := filepath.Join(home, "config")
	driver := &fakeDriver{name: "codex", detection: Detection{Installed: true}, changes: []Change{{
		Path: target, Action: ActionCreate, Mode: 0o600, Before: []byte("credential-before"), After: []byte("credential-after"),
	}}}
	result, err := Plan(context.Background(), Options{Home: home, Harnesses: []string{"codex"}, DryRun: true}, Bundle{Drivers: []Driver{driver}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(target); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("dry-run plan wrote target: %v", err)
	}
	summary := fmt.Sprint(result)
	if strings.Contains(summary, "credential-before") || strings.Contains(summary, "credential-after") {
		t.Fatalf("summary exposed change bodies: %s", summary)
	}
}

type fileStateDriver struct {
	name   string
	path   string
	wanted []byte
}

func (d *fileStateDriver) Name() string { return d.name }

func (d *fileStateDriver) Detect(context.Context, string) (Detection, error) {
	return Detection{Installed: true}, nil
}

func (d *fileStateDriver) Plan(context.Context, DriverRequest) ([]Change, error) {
	current, err := os.ReadFile(d.path)
	if errors.Is(err, fs.ErrNotExist) {
		return []Change{{Path: d.path, Action: ActionCreate, Mode: 0o600, After: append([]byte(nil), d.wanted...)}}, nil
	}
	if err != nil {
		return nil, err
	}
	action := ActionUpdate
	if string(current) == string(d.wanted) {
		action = ActionUnchanged
	}
	return []Change{{Path: d.path, Action: action, Mode: 0o600, Before: current, After: append([]byte(nil), d.wanted...)}}, nil
}

func assertFile(t *testing.T, path, wantContent string, wantMode fs.FileMode) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != wantContent {
		t.Fatalf("%s content = %q, want %q", path, content, wantContent)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != wantMode.Perm() {
		t.Fatalf("%s mode = %04o, want %04o", path, got, wantMode.Perm())
	}
}
