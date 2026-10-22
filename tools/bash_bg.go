package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// Background job registry: process groups left running by a bash call
// (`cmd &`, `nohup cmd &`). The host kills them at session end via
// KillBackgroundJobs.
var (
	bgMu   sync.Mutex
	bgJobs = map[int]*bgJob{}
	bgWG   sync.WaitGroup // live monitor goroutines
)

type bgJob struct {
	logPath string
	stop    chan struct{}
}

// MaxBackgroundJobs caps how many live background process groups the bash
// tool tracks at once. A call that would leave one more running fails, and
// its new group is killed.
var MaxBackgroundJobs = 10

// MaxBackgroundLogBytes caps a background job's log file. When it grows past
// the cap the file is emptied and restarted with a notice line.
var MaxBackgroundLogBytes int64 = 50 << 20

// bgKillGrace is how long KillBackgroundJobs waits after SIGTERM before
// sending SIGKILL; bgPollInterval is how often a job's log size and liveness
// are checked. Vars so tests can shorten them.
var (
	bgKillGrace    = 2 * time.Second
	bgPollInterval = 500 * time.Millisecond
)

// registerBackgroundJob tracks pgid and starts its log monitor. It fails when
// MaxBackgroundJobs live jobs are already tracked.
func registerBackgroundJob(pgid int, logPath string) error {
	live := BackgroundJobs()
	bgMu.Lock()
	defer bgMu.Unlock()
	if len(live) >= MaxBackgroundJobs {
		return fmt.Errorf("background job limit reached: %d jobs already running (process groups %s). "+
			"The new background job was killed. Stop one with `kill -- -<pgid>` or wait for it to finish, then retry",
			len(live), joinInts(live))
	}
	j := &bgJob{logPath: logPath, stop: make(chan struct{})}
	bgJobs[pgid] = j
	bgWG.Add(1)
	go monitorJob(pgid, j)
	return nil
}

// monitorJob enforces the log cap until the group exits or is killed.
func monitorJob(pgid int, j *bgJob) {
	defer bgWG.Done()
	t := time.NewTicker(bgPollInterval)
	defer t.Stop()
	for {
		select {
		case <-j.stop:
			return
		case <-t.C:
		}
		capLog(j.logPath)
		if alive, _ := groupAlive(pgid); !alive {
			return
		}
	}
}

// capLog empties the log when it exceeds MaxBackgroundLogBytes. The job's
// fd is O_APPEND, so its next writes land after the notice.
func capLog(path string) {
	fi, err := os.Stat(path)
	if err != nil || fi.Size() <= MaxBackgroundLogBytes {
		return
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_TRUNC, 0)
	if err != nil {
		return
	}
	fmt.Fprintf(f, "[log exceeded %dMB; earlier output dropped]\n", MaxBackgroundLogBytes>>20)
	f.Close()
}

// BackgroundJobs returns the process-group ids of tracked background jobs
// that are still alive.
func BackgroundJobs() []int {
	bgMu.Lock()
	defer bgMu.Unlock()
	var out []int
	for pgid, j := range bgJobs {
		if alive, _ := groupAlive(pgid); alive {
			out = append(out, pgid)
		} else {
			close(j.stop)
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
	for pgid, j := range bgJobs {
		close(j.stop)
		pgids = append(pgids, pgid)
	}
	bgJobs = map[int]*bgJob{}
	bgMu.Unlock()
	defer bgWG.Wait() // monitors exit on their closed stop channels
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

// Log directory: one per process (session), named agent-bash-<pid>-*.
// Creating it first removes dirs left by processes that no longer exist.
var (
	logDirOnce sync.Once
	logDir     string
	logDirErr  error
	tempRoot   = os.TempDir
)

const logDirPrefix = "agent-bash-"

// BashLogDir returns this process's bash log directory, creating it (and
// cleaning stale ones) on first use.
func BashLogDir() (string, error) {
	logDirOnce.Do(func() {
		root := tempRoot()
		cleanStaleLogDirs(root)
		logDir, logDirErr = os.MkdirTemp(root, logDirPrefix+strconv.Itoa(os.Getpid())+"-")
	})
	return logDir, logDirErr
}

// cleanStaleLogDirs removes agent-bash-<pid>-* dirs whose pid is gone.
func cleanStaleLogDirs(root string) {
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || !strings.HasPrefix(name, logDirPrefix) {
			continue
		}
		rest := strings.TrimPrefix(name, logDirPrefix)
		i := strings.IndexByte(rest, '-')
		if i < 0 {
			continue
		}
		pid, err := strconv.Atoi(rest[:i])
		if err != nil || pid <= 0 || pidAlive(pid) {
			continue
		}
		_ = os.RemoveAll(filepath.Join(root, name))
	}
}

func pidAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

var logSeq atomic.Int64

// newLogFile creates an append-mode output file in the session log dir.
func newLogFile() (*os.File, error) {
	dir, err := BashLogDir()
	if err != nil {
		return nil, err
	}
	name := filepath.Join(dir, fmt.Sprintf("%d.log", logSeq.Add(1)))
	return os.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL|os.O_APPEND, 0o600)
}

func joinInts(xs []int) string {
	s := make([]string, len(xs))
	for i, x := range xs {
		s[i] = strconv.Itoa(x)
	}
	return strings.Join(s, ", ")
}
