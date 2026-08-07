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

// ChangePhase expresses dependency ordering between independently planned
// filesystem changes. The zero value is a normal harness integration change;
// shared prerequisites publish first and shared cleanup runs last.
type ChangePhase uint8

const (
	PhaseIntegration ChangePhase = iota
	PhasePrerequisite
	PhaseCleanup
)

type deleteProofKind uint8

const (
	deleteProofNone deleteProofKind = iota
	deleteProofOwnedAsset
	deleteProofManagedBlock
)

// DeleteProof is immutable delete provenance produced only by trusted planners
// in this package. Its fields are intentionally private so callers cannot turn
// arbitrary current bytes into deletion authority.
type DeleteProof struct {
	kind       deleteProofKind
	ownedAsset string
	begin      string
	end        string
}

type Detection struct {
	Harness      string
	Present      bool
	Managed      bool
	Capabilities []Capability
}

type Change struct {
	Path        string
	Action      Action
	Phase       ChangePhase
	Mode        fs.FileMode
	Before      []byte
	After       []byte
	BackupPath  string
	DeleteProof DeleteProof
}

func ownedAssetDeleteProof(canonical []byte) DeleteProof {
	return DeleteProof{kind: deleteProofOwnedAsset, ownedAsset: string(canonical)}
}

func managedBlockDeleteProof(begin, end string) DeleteProof {
	return DeleteProof{kind: deleteProofManagedBlock, begin: begin, end: end}
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
	home       string
	pinnedHome string
}
