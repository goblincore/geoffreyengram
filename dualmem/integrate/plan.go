package integrate

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

func Plan(ctx context.Context, opts Options, bundle Bundle) (Result, error) {
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
		detection, detectErr := driver.Detect(ctx, opts.Home)
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
			Home:               opts.Home,
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
		driverChanges, planErr := driver.Plan(ctx, DriverRequest{Home: opts.Home, Uninstall: opts.Uninstall})
		if planErr != nil {
			return Result{}, fmt.Errorf("plan harness %q: %w", driver.Name(), planErr)
		}
		changes = append(changes, driverChanges...)
	}
	if err := validateAndSortChanges(changes); err != nil {
		return Result{}, err
	}
	return Result{Detections: detections, Changes: changes}, nil
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
	sort.SliceStable(changes, func(i, j int) bool { return changes[i].Path < changes[j].Path })
	for i := range changes {
		if changes[i].Path == "" {
			return fmt.Errorf("integration change has an empty path")
		}
		switch changes[i].Action {
		case ActionCreate, ActionUpdate, ActionUnchanged, ActionDelete:
		default:
			return fmt.Errorf("integration change for %q has invalid action %q", changes[i].Path, changes[i].Action)
		}
		if i > 0 && changes[i-1].Path == changes[i].Path {
			return fmt.Errorf("multiple integration changes target %q", changes[i].Path)
		}
	}
	return nil
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
