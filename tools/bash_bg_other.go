//go:build !linux

package tools

import (
	"os"
	"syscall"
)

// groupAlive reports whether process group pgid still has a member. Pids are
// not enumerated off Linux.
func groupAlive(pgid int) (bool, []int) {
	return syscall.Kill(-pgid, 0) == nil, nil
}

// setPipeSize is a no-op off Linux.
func setPipeSize(*os.File) {}
