//go:build !linux

package tools

import "syscall"

// groupAlive reports whether process group pgid still has a member. Pids are
// not enumerated off Linux.
func groupAlive(pgid int) (bool, []int) {
	return syscall.Kill(-pgid, 0) == nil, nil
}
