package integrate

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
)

type fakeDriver struct {
	name      string
	detection Detection
	changes   []Change
	planErr   error
	requests  []DriverRequest
}

func (d *fakeDriver) Name() string { return d.name }

func (d *fakeDriver) Detect(context.Context, string) (Detection, error) {
	return d.detection, nil
}

func (d *fakeDriver) Plan(_ context.Context, request DriverRequest) ([]Change, error) {
	d.requests = append(d.requests, request)
	return append([]Change(nil), d.changes...), d.planErr
}

type fakeCommonPlanner struct {
	changes  []Change
	err      error
	requests []CommonRequest
}

func (p *fakeCommonPlanner) PlanCommon(_ context.Context, request CommonRequest) ([]Change, error) {
	p.requests = append(p.requests, request)
	return append([]Change(nil), p.changes...), p.err
}

func TestPlanRejectsDuplicateDriverNames(t *testing.T) {
	_, err := Plan(context.Background(), Options{Home: t.TempDir(), Harnesses: []string{"all"}}, Bundle{
		Drivers: []Driver{&fakeDriver{name: "codex"}, &fakeDriver{name: "codex"}},
	})
	if err == nil {
		t.Fatal("Plan accepted duplicate driver names")
	}
}

func TestPlanRejectsUnknownHarnessBeforeDetection(t *testing.T) {
	driver := &fakeDriver{name: "codex", detection: Detection{Installed: true}}
	_, err := Plan(context.Background(), Options{Home: t.TempDir(), Harnesses: []string{"unknown"}}, Bundle{Drivers: []Driver{driver}})
	if err == nil {
		t.Fatal("Plan accepted an unknown harness")
	}
	if len(driver.requests) != 0 {
		t.Fatal("unknown harness unexpectedly reached driver planning")
	}
}

func TestPlanAllSelectsOnlyDetectedDriversInDeterministicOrder(t *testing.T) {
	home := t.TempDir()
	claude := &fakeDriver{name: "claude", detection: Detection{Installed: true}, changes: []Change{{Path: filepath.Join(home, "z"), Action: ActionCreate, Mode: 0o600, After: []byte("z")}}}
	codex := &fakeDriver{name: "codex", detection: Detection{Installed: false}, changes: []Change{{Path: filepath.Join(home, "ignored"), Action: ActionCreate, Mode: 0o600, After: []byte("ignored")}}}
	pi := &fakeDriver{name: "pi", detection: Detection{Installed: true}, changes: []Change{{Path: filepath.Join(home, "a"), Action: ActionCreate, Mode: 0o600, After: []byte("a")}}}
	common := &fakeCommonPlanner{changes: []Change{{Path: filepath.Join(home, "m"), Action: ActionCreate, Mode: 0o600, After: []byte("m")}}}

	result, err := Plan(context.Background(), Options{Home: home, Harnesses: []string{"all"}}, Bundle{Common: common, Drivers: []Driver{pi, codex, claude}})
	if err != nil {
		t.Fatal(err)
	}
	if len(claude.requests) != 1 || len(pi.requests) != 1 || len(codex.requests) != 0 {
		t.Fatalf("unexpected plan calls: claude=%d codex=%d pi=%d", len(claude.requests), len(codex.requests), len(pi.requests))
	}
	if got, want := detectionNames(result.Detections), []string{"claude", "codex", "pi"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("detections = %v, want %v", got, want)
	}
	if got, want := changePaths(result.Changes), []string{filepath.Join(home, "a"), filepath.Join(home, "m"), filepath.Join(home, "z")}; !reflect.DeepEqual(got, want) {
		t.Fatalf("change order = %v, want %v", got, want)
	}
	if len(common.requests) != 1 {
		t.Fatalf("common planner called %d times, want 1", len(common.requests))
	}
	if got, want := common.requests[0].RemainingHarnesses, []string{"claude", "pi"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("remaining harnesses = %v, want %v", got, want)
	}
}

func TestPlanExplicitHarnessSelectsUndetectedDriver(t *testing.T) {
	driver := &fakeDriver{name: "codex", detection: Detection{Installed: false}}
	_, err := Plan(context.Background(), Options{Home: t.TempDir(), Harnesses: []string{"codex"}}, Bundle{Drivers: []Driver{driver}})
	if err != nil {
		t.Fatal(err)
	}
	if len(driver.requests) != 1 {
		t.Fatalf("driver planned %d times, want 1", len(driver.requests))
	}
}

func TestPlanTargetedUninstallRetainsCommonUntilLastHarness(t *testing.T) {
	for _, test := range []struct {
		name          string
		claudeManaged bool
		wantRemaining []string
	}{
		{name: "another harness remains", claudeManaged: true, wantRemaining: []string{"claude"}},
		{name: "last harness removed", claudeManaged: false, wantRemaining: nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			common := &fakeCommonPlanner{}
			codex := &fakeDriver{name: "codex", detection: Detection{Installed: true}}
			claude := &fakeDriver{name: "claude", detection: Detection{Installed: test.claudeManaged}}
			_, err := Plan(context.Background(), Options{Home: t.TempDir(), Harnesses: []string{"codex"}, Uninstall: true}, Bundle{Common: common, Drivers: []Driver{codex, claude}})
			if err != nil {
				t.Fatal(err)
			}
			if len(common.requests) != 1 {
				t.Fatalf("common planner called %d times, want 1", len(common.requests))
			}
			if got := common.requests[0]; got.Uninstall != true || !slices.Equal(got.RemainingHarnesses, test.wantRemaining) {
				t.Fatalf("common request = %#v, want uninstall with remaining %v", got, test.wantRemaining)
			}
		})
	}
}

func TestPlanReturnsNoChangesWhenAPlannerFails(t *testing.T) {
	home := t.TempDir()
	common := &fakeCommonPlanner{changes: []Change{{Path: filepath.Join(home, "common"), Action: ActionCreate}}}
	driver := &fakeDriver{name: "codex", detection: Detection{Installed: true}, planErr: errors.New("invalid JSON")}
	result, err := Plan(context.Background(), Options{Home: home, Harnesses: []string{"codex"}}, Bundle{Common: common, Drivers: []Driver{driver}})
	if err == nil {
		t.Fatal("Plan succeeded despite driver planning error")
	}
	if len(result.Changes) != 0 {
		t.Fatalf("Plan exposed %d partial changes", len(result.Changes))
	}
	if _, statErr := fs.Stat(osDirFS(home), "common"); !errors.Is(statErr, fs.ErrNotExist) {
		t.Fatalf("planning wrote common file: %v", statErr)
	}
}

func detectionNames(detections []Detection) []string {
	names := make([]string, len(detections))
	for i := range detections {
		names[i] = detections[i].Harness
	}
	return names
}

func changePaths(changes []Change) []string {
	paths := make([]string, len(changes))
	for i := range changes {
		paths[i] = changes[i].Path
	}
	return paths
}

func osDirFS(path string) fs.FS { return os.DirFS(path) }
