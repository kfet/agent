package tools

import (
	"os"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

// procDir is the procfs root; a var so tests can point it elsewhere.
var procDir = "/proc"

// groupAlive reports whether any non-zombie process is in process group
// pgid, and lists their pids.
func groupAlive(pgid int) (bool, []int) {
	entries, _ := os.ReadDir(procDir)
	var pids []int
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		data, err := os.ReadFile(procDir + "/" + e.Name() + "/stat")
		if err != nil {
			continue
		}
		// Format: pid (comm) state ppid pgrp ...; comm may contain spaces/parens.
		s := string(data)
		i := strings.LastIndexByte(s, ')')
		if i < 0 {
			continue
		}
		f := strings.Fields(s[i+1:])
		if len(f) < 3 || f[0] == "Z" || f[0] == "X" {
			continue
		}
		if g, err := strconv.Atoi(f[2]); err == nil && g == pgid {
			pids = append(pids, pid)
		}
	}
	sort.Ints(pids)
	return len(pids) > 0, pids
}

// fSetPipeSz is F_SETPIPE_SZ (linux/fcntl.h); syscall does not export it.
const fSetPipeSz = 1031

// setPipeSize grows the pipe to 1MB so bursts do not block writers while the
// reader catches up. Errors (e.g. over /proc/sys/fs/pipe-max-size) are ignored.
func setPipeSize(f *os.File) {
	rc, _ := f.SyscallConn()
	_ = rc.Control(func(fd uintptr) {
		_, _, _ = syscall.Syscall(syscall.SYS_FCNTL, fd, fSetPipeSz, 1<<20)
	})
}
