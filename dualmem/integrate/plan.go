package integrate

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

var beforeHomeLeafValidationHook = func(string) {}

func Plan(ctx context.Context, opts Options, bundle Bundle) (Result, error) {
	home, pinnedHome, err := integrationHome(opts.Home)
	if err != nil {
		return Result{}, err
	}
	drivers, byName, err := validatedDrivers(bundle.Drivers)
	if err != nil {
		return Result{}, err
	}
	selection, all, err := requestedHarnesses(opts.Harnesses, byName)
	if err != nil {
		return Result{}, err
	}

	detections := make([]Detection, 0, len(drivers))
	present := make(map[string]bool, len(drivers))
	managed := make(map[string]bool, len(drivers))
	for _, driver := range drivers {
		detection, detectErr := driver.Detect(ctx, home)
		if detectErr != nil {
			return Result{}, fmt.Errorf("detect harness %q: %w", driver.Name(), detectErr)
		}
		detection.Harness = driver.Name()
		detection.Capabilities = append([]Capability(nil), detection.Capabilities...)
		sort.Slice(detection.Capabilities, func(i, j int) bool {
			return detection.Capabilities[i] < detection.Capabilities[j]
		})
		detections = append(detections, detection)
		present[driver.Name()] = detection.Present
		managed[driver.Name()] = detection.Managed
	}

	selected := make(map[string]bool, len(drivers))
	for _, driver := range drivers {
		selected[driver.Name()] = selection[driver.Name()] || (all && present[driver.Name()])
	}

	remaining := projectedHarnesses(drivers, managed, selected, opts.Uninstall)
	var changes []Change
	if bundle.Common != nil {
		commonChanges, planErr := bundle.Common.PlanCommon(ctx, CommonRequest{
			Home:               home,
			Uninstall:          opts.Uninstall,
			RemainingHarnesses: remaining,
		})
		if planErr != nil {
			return Result{}, fmt.Errorf("plan common integration: %w", planErr)
		}
		changes = append(changes, commonChanges...)
	}

	for _, driver := range drivers {
		if !selected[driver.Name()] {
			continue
		}
		driverChanges, planErr := driver.Plan(ctx, DriverRequest{Home: home, Uninstall: opts.Uninstall})
		if planErr != nil {
			return Result{}, fmt.Errorf("plan harness %q: %w", driver.Name(), planErr)
		}
		changes = append(changes, driverChanges...)
	}
	if err := validateAndSortChanges(changes); err != nil {
		return Result{}, err
	}
	if err := validateChangesUnderHome(home, changes); err != nil {
		return Result{}, err
	}
	if err := validateExistingDirectoryPathNoSymlinks(pinnedHome); err != nil {
		return Result{}, err
	}
	return Result{Detections: detections, Changes: changes, home: home, pinnedHome: pinnedHome}, nil
}

func integrationHome(home string) (string, string, error) {
	root, err := filepath.Abs(home)
	if err != nil {
		return "", "", fmt.Errorf("resolve integration home %q: %w", home, err)
	}
	root = filepath.Clean(root)
	pinnedParent, err := canonicalizeExistingAncestors(filepath.Dir(root))
	if err != nil {
		return "", "", err
	}
	pinnedRoot := filepath.Join(pinnedParent, filepath.Base(root))
	beforeHomeLeafValidationHook(root)
	if err := validateExistingDirectoryPathNoSymlinks(pinnedRoot); err != nil {
		return "", "", err
	}
	return root, pinnedRoot, nil
}

func validateChangesUnderHome(root string, changes []Change) error {
	for _, change := range changes {
		if err := validatePathUnderHome(root, change.Path); err != nil {
			return err
		}
	}
	return nil
}

func validatePathUnderHome(root, target string) error {
	absoluteTarget, err := filepath.Abs(target)
	if err != nil {
		return fmt.Errorf("resolve integration target %q: %w", target, err)
	}
	absoluteTarget = filepath.Clean(absoluteTarget)
	relativeTarget, err := filepath.Rel(root, absoluteTarget)
	if err != nil || relativeTarget == ".." || strings.HasPrefix(relativeTarget, ".."+string(filepath.Separator)) || filepath.IsAbs(relativeTarget) {
		return fmt.Errorf("integration target %q is outside home %q", target, root)
	}

	path := root
	components := []string{relativeTarget}
	if relativeTarget != "." {
		components = strings.Split(relativeTarget, string(filepath.Separator))
	}
	for _, component := range components {
		if component != "." {
			path = filepath.Join(path, component)
		}
		info, err := os.Lstat(path)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("inspect integration target ancestor %q: %w", path, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("integration target %q traverses symlink %q", target, path)
		}
	}
	return nil
}

func validatedDrivers(input []Driver) ([]Driver, map[string]Driver, error) {
	drivers := append([]Driver(nil), input...)
	byName := make(map[string]Driver, len(drivers))
	for _, driver := range drivers {
		if driver == nil {
			return nil, nil, fmt.Errorf("integration driver is nil")
		}
		name := strings.TrimSpace(driver.Name())
		if name == "" || name != driver.Name() {
			return nil, nil, fmt.Errorf("invalid integration driver name %q", driver.Name())
		}
		if _, exists := byName[name]; exists {
			return nil, nil, fmt.Errorf("duplicate integration driver %q", name)
		}
		byName[name] = driver
	}
	sort.Slice(drivers, func(i, j int) bool { return drivers[i].Name() < drivers[j].Name() })
	return drivers, byName, nil
}

func requestedHarnesses(names []string, drivers map[string]Driver) (map[string]bool, bool, error) {
	if len(names) == 0 {
		return nil, false, fmt.Errorf("at least one harness is required")
	}
	selected := make(map[string]bool, len(names))
	all := false
	for _, name := range names {
		if name == "all" {
			all = true
			continue
		}
		if _, exists := drivers[name]; !exists {
			return nil, false, fmt.Errorf("unknown harness %q", name)
		}
		if selected[name] {
			return nil, false, fmt.Errorf("duplicate harness %q", name)
		}
		selected[name] = true
	}
	if all && len(names) != 1 {
		return nil, false, fmt.Errorf("harness %q cannot be combined with explicit harnesses", "all")
	}
	return selected, all, nil
}

func projectedHarnesses(drivers []Driver, currentlyManaged, selected map[string]bool, uninstall bool) []string {
	remaining := make([]string, 0, len(drivers))
	for _, driver := range drivers {
		name := driver.Name()
		managed := currentlyManaged[name]
		if selected[name] {
			managed = !uninstall
		}
		if managed {
			remaining = append(remaining, name)
		}
	}
	return remaining
}

func validateAndSortChanges(changes []Change) error {
	sort.SliceStable(changes, func(i, j int) bool {
		left, right := changePhaseRank(changes[i].Phase), changePhaseRank(changes[j].Phase)
		if left != right {
			return left < right
		}
		return changes[i].Path < changes[j].Path
	})
	seenPaths := make(map[string]struct{}, len(changes))
	for i := range changes {
		if changes[i].Path == "" {
			return fmt.Errorf("integration change has an empty path")
		}
		switch changes[i].Action {
		case ActionCreate, ActionUpdate, ActionUnchanged, ActionDelete:
		default:
			return fmt.Errorf("integration change for %q has invalid action %q", changes[i].Path, changes[i].Action)
		}
		if changePhaseRank(changes[i].Phase) < 0 {
			return fmt.Errorf("integration change for %q has invalid phase %d", changes[i].Path, changes[i].Phase)
		}
		if _, exists := seenPaths[changes[i].Path]; exists {
			return fmt.Errorf("multiple integration changes target %q", changes[i].Path)
		}
		seenPaths[changes[i].Path] = struct{}{}
	}
	return nil
}

func changePhaseRank(phase ChangePhase) int {
	switch phase {
	case PhasePrerequisite:
		return 0
	case PhaseIntegration:
		return 1
	case PhaseCleanup:
		return 2
	default:
		return -1
	}
}

func (result Result) String() string {
	var summary strings.Builder
	fmt.Fprintf(&summary, "integrations: %d detected", len(result.Detections))
	for _, change := range result.Changes {
		fmt.Fprintf(&summary, "; %s %s mode=%04o", change.Action, change.Path, change.Mode.Perm())
		if change.BackupPath != "" {
			fmt.Fprintf(&summary, " backup=%s", change.BackupPath)
		}
	}
	return summary.String()
}

func (change Change) String() string {
	return fmt.Sprintf("%s %s mode=%04o backup=%s", change.Action, change.Path, change.Mode.Perm(), change.BackupPath)
}
