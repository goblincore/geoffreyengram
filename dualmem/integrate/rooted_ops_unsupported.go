//go:build !unix

package integrate

import (
	"errors"
	"os"
)

var errRootedMutationUnsupported = errors.New("safe rooted integration mutation is unsupported on this platform")

func rootedRename(_ *os.Root, _, _ string) error {
	return errRootedMutationUnsupported
}

func rootedLink(_ *os.Root, _, _ string) error {
	return errRootedMutationUnsupported
}
