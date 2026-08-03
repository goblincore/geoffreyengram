// Package integrate plans and safely applies harness integration changes.
package integrate

import (
	"context"
	"io/fs"
)

type Action string

const (
	ActionCreate    Action = "create"
	ActionUpdate    Action = "update"
	ActionUnchanged Action = "unchanged"
	ActionDelete    Action = "delete"
)

type Capability string

type Detection struct {
	Harness      string
	Installed    bool
	Capabilities []Capability
}

type Change struct {
	Path       string
	Action     Action
	Mode       fs.FileMode
	Before     []byte
	After      []byte
	BackupPath string
}

type Driver interface {
	Name() string
	Detect(context.Context, string) (Detection, error)
	Plan(context.Context, DriverRequest) ([]Change, error)
}

type CommonRequest struct {
	Home               string
	Uninstall          bool
	RemainingHarnesses []string
}

type CommonPlanner interface {
	PlanCommon(context.Context, CommonRequest) ([]Change, error)
}

type Bundle struct {
	Common  CommonPlanner
	Drivers []Driver
}

type DriverRequest struct {
	Home      string
	Uninstall bool
}

type Options struct {
	Home      string
	Harnesses []string
	DryRun    bool
	Uninstall bool
}

type Result struct {
	Detections []Detection
	Changes    []Change
}
