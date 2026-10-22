package tools

import (
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Background job registry: process groups left running by a bash call
// (`cmd &`, `nohup cmd &`). The host kills them at session end via
// KillBackgroundJobs.
var (
	bgMu   sync.Mutex
	bgJobs = map[int]struct{}{}
)

// bgKillGrace is how long KillBackgroundJobs waits after SIGTERM before
// sending SIGKILL. A var so tests can shorten it.
var bgKillGrace = 2 * time.Second

func registerBackgroundJob(pgid int) {
	bgMu.Lock()
	bgJobs[pgid] = struct{}{}
	bgMu.Unlock()
}

// BackgroundJobs returns the process-group ids of tracked background jobs
// that are still alive.
func BackgroundJobs() []int {
	bgMu.Lock()
	defer bgMu.Unlock()
	var out []int
	for pgid := range bgJobs {
		if alive, _ := groupAlive(pgid); alive {
			out = append(out, pgid)
		} else {
			delete(bgJobs, pgid)
		}
	}
	return out
}

// KillBackgroundJobs terminates every background process group started by
// the bash tool: SIGTERM, a short grace period, then SIGKILL. Hosts call it
// when the session ends. Safe to call more than once.
func KillBackgroundJobs() {
	bgMu.Lock()
	pgids := make([]int, 0, len(bgJobs))
	for pgid := range bgJobs {
		pgids = append(pgids, pgid)
	}
	bgJobs = map[int]struct{}{}
	bgMu.Unlock()
	if len(pgids) == 0 {
		return
	}
	for _, pgid := range pgids {
		_ = syscall.Kill(-pgid, syscall.SIGTERM)
	}
	deadline := time.Now().Add(bgKillGrace)
	for {
		anyAlive := false
		for _, pgid := range pgids {
			if alive, _ := groupAlive(pgid); alive {
				anyAlive = true
				break
			}
		}
		if !anyAlive || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	for _, pgid := range pgids {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
	}
}

func joinInts(xs []int) string {
	s := make([]string, len(xs))
	for i, x := range xs {
		s[i] = strconv.Itoa(x)
	}
	return strings.Join(s, ", ")
}
