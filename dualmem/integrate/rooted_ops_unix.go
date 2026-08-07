//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package integrate

import (
	"os"

	"golang.org/x/sys/unix"
)

func rootedRename(sourceRoot *os.Root, sourceName string, destinationRoot *os.Root, destinationName string) error {
	sourceDirectory, err := sourceRoot.Open(".")
	if err != nil {
		return err
	}
	defer func() { _ = sourceDirectory.Close() }()
	destinationDirectory, err := destinationRoot.Open(".")
	if err != nil {
		return err
	}
	defer func() { _ = destinationDirectory.Close() }()
	return unix.Renameat(int(sourceDirectory.Fd()), sourceName, int(destinationDirectory.Fd()), destinationName)
}

func rootedLink(sourceRoot *os.Root, sourceName string, destinationRoot *os.Root, destinationName string) error {
	sourceDirectory, err := sourceRoot.Open(".")
	if err != nil {
		return err
	}
	defer func() { _ = sourceDirectory.Close() }()
	destinationDirectory, err := destinationRoot.Open(".")
	if err != nil {
		return err
	}
	defer func() { _ = destinationDirectory.Close() }()
	return unix.Linkat(int(sourceDirectory.Fd()), sourceName, int(destinationDirectory.Fd()), destinationName, 0)
}
