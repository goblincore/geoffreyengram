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
	atomicWriteFile = func(target rootedTarget, data []byte, mode fs.FileMode) error {
		if strings.Contains(target.path, ".dualmem-backup-") {
			return errors.New("injected backup failure")
		}
		return originalWriter(target, data, mode)
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

func TestApplyPublishesPrerequisitesBeforeDependentHookMigration(t *testing.T) {
	home := t.TempDir()
	hookPath := filepath.Join(home, ".claude", "settings.json")
	envPath := filepath.Join(home, ".config", "dualmem", "env")
	launcherPath := filepath.Join(home, ".config", "dualmem", "bin", "dualmem-run")
	if err := os.MkdirAll(filepath.Dir(hookPath), 0o700); err != nil {
		t.Fatal(err)
	}
	const legacyHook = "legacy hook with credential"
	if err := os.WriteFile(hookPath, []byte(legacyHook), 0o600); err != nil {
		t.Fatal(err)
	}

	originalWriter := atomicWriteFile
	t.Cleanup(func() { atomicWriteFile = originalWriter })
	var publications []string
	atomicWriteFile = func(target rootedTarget, data []byte, mode fs.FileMode) error {
		if !strings.Contains(target.path, ".dualmem-backup-") {
			publications = append(publications, target.path)
		}
		if target.path == hookPath {
			return errors.New("injected dependent hook failure")
		}
		return originalWriter(target, data, mode)
	}

	err := Apply(Result{Changes: []Change{
		{Path: hookPath, Action: ActionUpdate, Phase: PhaseIntegration, Mode: 0o600, Before: []byte(legacyHook), After: []byte("protected hook")},
		{Path: envPath, Action: ActionCreate, Phase: PhasePrerequisite, Mode: 0o600, After: []byte("migrated credential")},
		{Path: launcherPath, Action: ActionCreate, Phase: PhasePrerequisite, Mode: 0o700, After: []byte("launcher")},
	}})
	if err == nil {
		t.Fatal("Apply succeeded despite injected dependent hook failure")
	}
	if len(publications) != 3 || publications[0] != launcherPath || publications[1] != envPath || publications[2] != hookPath {
		t.Fatalf("publication order = %v, want prerequisites before hook", publications)
	}
	assertFile(t, launcherPath, "launcher", 0o700)
	assertFile(t, envPath, "migrated credential", 0o600)
	assertFile(t, hookPath, legacyHook, 0o600)
}

func TestApplyKeepsSharedAssetsWhenUninstallingHookFails(t *testing.T) {
	home := t.TempDir()
	hookPath := filepath.Join(home, ".codex", "hooks.json")
	envPath := filepath.Join(home, ".config", "dualmem", "env")
	launcherPath := filepath.Join(home, ".config", "dualmem", "bin", "dualmem-run")
	for path, content := range map[string]string{
		hookPath:     "managed hook",
		envPath:      "",
		launcherPath: "managed launcher",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	originalWriter := atomicWriteFile
	t.Cleanup(func() { atomicWriteFile = originalWriter })
	atomicWriteFile = func(target rootedTarget, data []byte, mode fs.FileMode) error {
		if target.path == hookPath {
			return errors.New("injected hook uninstall failure")
		}
		return originalWriter(target, data, mode)
	}

	err := Apply(Result{Changes: []Change{
		{Path: launcherPath, Action: ActionDelete, Phase: PhaseCleanup, Mode: 0o600, Before: []byte("managed launcher"), DeleteProof: ownedAssetDeleteProof([]byte("managed launcher"))},
		{Path: envPath, Action: ActionDelete, Phase: PhaseCleanup, Mode: 0o600, Before: []byte(""), DeleteProof: ownedAssetDeleteProof([]byte(""))},
		{Path: hookPath, Action: ActionUpdate, Phase: PhaseIntegration, Mode: 0o600, Before: []byte("managed hook"), After: []byte("unrelated hook")},
	}})
	if err == nil {
		t.Fatal("Apply succeeded despite injected hook uninstall failure")
	}
	assertFile(t, hookPath, "managed hook", 0o600)
	assertFile(t, envPath, "", 0o600)
	assertFile(t, launcherPath, "managed launcher", 0o600)
}

func TestApplyInjectedWriteFailureLeavesOriginalIntact(t *testing.T) {
	home := t.TempDir()
	target := filepath.Join(home, "config")
	if err := os.WriteFile(target, []byte("original"), 0o640); err != nil {
		t.Fatal(err)
	}
	originalWriter := atomicWriteFile
	t.Cleanup(func() { atomicWriteFile = originalWriter })
	atomicWriteFile = func(writeTarget rootedTarget, data []byte, mode fs.FileMode) error {
		if writeTarget.path == target {
			return errors.New("injected write failure")
		}
		return originalWriter(writeTarget, data, mode)
	}

	err := Apply(Result{Changes: []Change{{
		Path: target, Action: ActionUpdate, Mode: 0o600, Before: []byte("original"), After: []byte("replacement"),
	}}})
	if err == nil {
		t.Fatal("Apply succeeded despite injected write failure")
	}
	assertFile(t, target, "original", 0o640)
}

func TestApplyCreatePreservesConcurrentPathAppearance(t *testing.T) {
	home := t.TempDir()
	target := filepath.Join(home, "config")
	originalWriter := atomicWriteFile
	t.Cleanup(func() { atomicWriteFile = originalWriter })
	atomicWriteFile = func(writeTarget rootedTarget, data []byte, mode fs.FileMode) error {
		if writeTarget.path == target {
			if err := os.WriteFile(target, []byte("concurrent"), 0o640); err != nil {
				return err
			}
		}
		return originalWriter(writeTarget, data, mode)
	}
	err := Apply(Result{Changes: []Change{{Path: target, Action: ActionCreate, Mode: 0o600, After: []byte("planned")}}})
	if err == nil {
		t.Fatal("Apply overwrote a path that appeared after create preflight")
	}
	assertFile(t, target, "concurrent", 0o640)
}

func TestApplyUpdatePreservesConcurrentReplacementAndOriginalBackup(t *testing.T) {
	home := t.TempDir()
	target := filepath.Join(home, "config")
	if err := os.WriteFile(target, []byte("original"), 0o640); err != nil {
		t.Fatal(err)
	}
	originalWriter := atomicWriteFile
	t.Cleanup(func() { atomicWriteFile = originalWriter })
	atomicWriteFile = func(writeTarget rootedTarget, data []byte, mode fs.FileMode) error {
		if writeTarget.path == target {
			if err := os.WriteFile(target, []byte("concurrent"), 0o644); err != nil {
				return err
			}
		}
		return originalWriter(writeTarget, data, mode)
	}
	result := Result{Changes: []Change{{
		Path: target, Action: ActionUpdate, Mode: 0o600, Before: []byte("original"), After: []byte("planned"),
	}}}
	err := Apply(result)
	if err == nil {
		t.Fatal("Apply overwrote a concurrent update replacement")
	}
	assertFile(t, target, "concurrent", 0o644)
	if result.Changes[0].BackupPath == "" {
		t.Fatal("concurrent update abort did not retain the original backup")
	}
	assertFile(t, result.Changes[0].BackupPath, "original", 0o600)
}

func TestApplyDeletePreservesReplacementBeforeQuarantine(t *testing.T) {
	home := t.TempDir()
	target := filepath.Join(home, "owned")
	replacement := filepath.Join(home, "replacement")
	if err := os.WriteFile(target, []byte("canonical"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(replacement, []byte("concurrent"), 0o640); err != nil {
		t.Fatal(err)
	}
	originalHook := beforeQuarantineHook
	t.Cleanup(func() { beforeQuarantineHook = originalHook })
	beforeQuarantineHook = func(change Change) {
		if change.Path == target {
			if err := os.Rename(replacement, target); err != nil {
				t.Errorf("inject replacement: %v", err)
			}
		}
	}
	err := Apply(Result{Changes: []Change{{
		Path: target, Action: ActionDelete, Before: []byte("canonical"), DeleteProof: ownedAssetDeleteProof([]byte("canonical")),
	}}})
	if err == nil {
		t.Fatal("Apply deleted a replacement that arrived after preflight")
	}
	assertFile(t, target, "concurrent", 0o640)
}

func TestApplyDeletePreservesPathCreatedAfterQuarantine(t *testing.T) {
	home := t.TempDir()
	target := filepath.Join(home, "owned")
	if err := os.WriteFile(target, []byte("canonical"), 0o600); err != nil {
		t.Fatal(err)
	}
	originalHook := afterQuarantineHook
	t.Cleanup(func() { afterQuarantineHook = originalHook })
	afterQuarantineHook = func(change Change) {
		if change.Path == target {
			if err := os.WriteFile(target, []byte("concurrent"), 0o644); err != nil {
				t.Errorf("inject path appearance: %v", err)
			}
		}
	}
	if err := Apply(Result{Changes: []Change{{
		Path: target, Action: ActionDelete, Before: []byte("canonical"), DeleteProof: ownedAssetDeleteProof([]byte("canonical")),
	}}}); err != nil {
		t.Fatal(err)
	}
	assertFile(t, target, "concurrent", 0o644)
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

func TestApplyRejectsSymlinkedParentIntroducedAfterPlanning(t *testing.T) {
	home := t.TempDir()
	harnessDirectory := filepath.Join(home, ".codex")
	if err := os.Mkdir(harnessDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(harnessDirectory, "hooks.json")
	result, err := Plan(context.Background(), Options{Home: home, Harnesses: []string{"codex"}}, Bundle{Drivers: []Driver{
		&fileStateDriver{name: "codex", path: target, wanted: []byte("managed")},
	}})
	if err != nil {
		t.Fatal(err)
	}

	escape := t.TempDir()
	if err := os.Remove(harnessDirectory); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(escape, harnessDirectory); err != nil {
		t.Fatal(err)
	}
	if err := Apply(result); err == nil {
		t.Fatal("Apply followed a parent symlink introduced after planning")
	}
	if _, err := os.Lstat(filepath.Join(escape, "hooks.json")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Apply wrote through the symlinked parent: %v", err)
	}
}

func TestApplyParentSwapAtMutationBoundaryCannotEscapeHome(t *testing.T) {
	for _, test := range []struct {
		name        string
		action      Action
		stage       mutationStage
		phase       ChangePhase
		failPublish bool
	}{
		{name: "prerequisite create", action: ActionCreate, stage: mutationCreate, phase: PhasePrerequisite},
		{name: "update backup", action: ActionUpdate, stage: mutationBackup},
		{name: "update quarantine", action: ActionUpdate, stage: mutationQuarantine},
		{name: "update publication", action: ActionUpdate, stage: mutationPublish},
		{name: "update rollback", action: ActionUpdate, stage: mutationRestore, failPublish: true},
		{name: "cleanup delete quarantine", action: ActionDelete, stage: mutationQuarantine, phase: PhaseCleanup},
		{name: "cleanup delete discard", action: ActionDelete, stage: mutationDiscard, phase: PhaseCleanup},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			parent := filepath.Join(home, "harness")
			if err := os.Mkdir(parent, 0o700); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(parent, "managed")
			before := []byte(nil)
			if test.action != ActionCreate {
				before = []byte("original")
				if err := os.WriteFile(target, before, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			change := Change{Path: target, Action: test.action, Phase: test.phase, Mode: 0o600, Before: before, After: []byte("updated")}
			if test.action == ActionDelete {
				change.After = nil
				change.DeleteProof = ownedAssetDeleteProof(before)
			}
			result, err := Plan(context.Background(), Options{Home: home, Harnesses: []string{"codex"}}, Bundle{Drivers: []Driver{
				&fakeDriver{name: "codex", detection: Detection{Present: true, Managed: true}, changes: []Change{change}},
			}})
			if err != nil {
				t.Fatal(err)
			}

			escape := t.TempDir()
			outsideTarget := filepath.Join(escape, "managed")
			if test.action != ActionCreate {
				if err := os.WriteFile(outsideTarget, []byte("outside"), 0o640); err != nil {
					t.Fatal(err)
				}
			}
			movedParent := filepath.Join(home, "harness-original")
			swapped := false
			originalMutationHook := beforeMutationHook
			t.Cleanup(func() { beforeMutationHook = originalMutationHook })
			beforeMutationHook = func(_ Change, stage mutationStage) {
				if swapped || stage != test.stage {
					return
				}
				swapped = true
				if err := os.Rename(parent, movedParent); err != nil {
					t.Errorf("move parent: %v", err)
					return
				}
				if err := os.Symlink(escape, parent); err != nil {
					t.Errorf("replace parent with symlink: %v", err)
				}
			}
			if test.failPublish {
				originalWriter := atomicWriteFile
				t.Cleanup(func() { atomicWriteFile = originalWriter })
				atomicWriteFile = func(writeTarget rootedTarget, data []byte, mode fs.FileMode) error {
					if writeTarget.name == filepath.Base(target) {
						return errors.New("injected publication failure")
					}
					return originalWriter(writeTarget, data, mode)
				}
			}

			_ = Apply(result)
			if !swapped {
				t.Fatalf("mutation stage %q was not reached", test.stage)
			}
			if test.action == ActionCreate {
				if _, err := os.Lstat(outsideTarget); !errors.Is(err, fs.ErrNotExist) {
					t.Fatalf("create escaped home: %v", err)
				}
			} else {
				assertFile(t, outsideTarget, "outside", 0o640)
			}
			entries, err := os.ReadDir(escape)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) > 1 {
				t.Fatalf("mutation created files outside home: %v", entries)
			}
		})
	}
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
	if err := Apply(Result{Changes: []Change{{Path: owned, Action: ActionDelete, Before: []byte("owned bytes"), DeleteProof: ownedAssetDeleteProof([]byte("owned bytes"))}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(owned); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("owned file still exists: %v", err)
	}

	stale := filepath.Join(home, "stale")
	if err := os.WriteFile(stale, []byte("user changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Apply(Result{Changes: []Change{{Path: stale, Action: ActionDelete, Before: []byte("planned owned bytes"), DeleteProof: ownedAssetDeleteProof([]byte("planned owned bytes"))}}}); err == nil {
		t.Fatal("Apply deleted a file whose bytes changed")
	}
	assertFile(t, stale, "user changed", 0o600)
	if err := Apply(Result{Changes: []Change{{Path: stale, Action: ActionDelete, Before: []byte("user changed"), After: []byte("not empty"), DeleteProof: ownedAssetDeleteProof([]byte("user changed"))}}}); err == nil {
		t.Fatal("Apply accepted a delete with a non-empty post-state")
	}
	assertFile(t, stale, "user changed", 0o600)
}

func TestApplyRejectsExactCurrentDeleteWithoutProvenance(t *testing.T) {
	home := t.TempDir()
	target := filepath.Join(home, "user-owned")
	if err := os.WriteFile(target, []byte("exact user bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Apply(Result{Changes: []Change{{
		Path: target, Action: ActionDelete, Before: []byte("exact user bytes"),
	}}}); err == nil {
		t.Fatal("Apply deleted arbitrary exact-current user bytes without ownership provenance")
	}
	assertFile(t, target, "exact user bytes", 0o600)
}

func TestApplyRejectsManagedBlockDeleteWhenUnrelatedContentRemains(t *testing.T) {
	home := t.TempDir()
	target := filepath.Join(home, "instructions")
	content := "user content\n" + testBegin + "\nmanaged\n" + testEnd + "\n"
	if err := os.WriteFile(target, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Apply(Result{Changes: []Change{{
		Path: target, Action: ActionDelete, Before: []byte(content), DeleteProof: managedBlockDeleteProof(testBegin, testEnd),
	}}}); err == nil {
		t.Fatal("Apply deleted managed-block file with unrelated content")
	}
	assertFile(t, target, content, 0o600)
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
	if err := Apply(Result{Changes: []Change{{Path: onlyManaged, Action: ActionDelete, Before: []byte(onlyBefore), After: []byte(onlyAfter), DeleteProof: managedBlockDeleteProof(testBegin, testEnd)}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(onlyManaged); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("empty managed file still exists: %v", err)
	}
}

func TestApplyDryRunPlanDoesNotWriteAndSummaryOmitsBodies(t *testing.T) {
	home := t.TempDir()
	target := filepath.Join(home, "config")
	driver := &fakeDriver{name: "codex", detection: Detection{Present: true, Managed: true}, changes: []Change{{
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
	return Detection{Present: true, Managed: true}, nil
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
