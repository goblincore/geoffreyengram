//go:build unix

package integrate

import (
	"os"

	"golang.org/x/sys/unix"
)

func rootedRename(root *os.Root, oldName, newName string) error {
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()
	return unix.Renameat(int(directory.Fd()), oldName, int(directory.Fd()), newName)
}

func rootedLink(root *os.Root, oldName, newName string) error {
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()
	return unix.Linkat(int(directory.Fd()), oldName, int(directory.Fd()), newName, 0)
}
