//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd

package integrate

import (
	"errors"
	"os"
)

var errRootedMutationUnsupported = errors.New("safe rooted integration mutation is unsupported on this platform")

func rootedRename(_ *os.Root, _ string, _ *os.Root, _ string) error {
	return errRootedMutationUnsupported
}

func rootedLink(_ *os.Root, _ string, _ *os.Root, _ string) error {
	return errRootedMutationUnsupported
}
