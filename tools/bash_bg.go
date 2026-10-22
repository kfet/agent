package tools

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Bash output plumbing and the background job registry.
//
// stdout+stderr of every bash call go to one anonymous pipe. A reader
// goroutine drains it into memory. When bash exits we take whatever the pipe
// holds (non-blocking reads until EAGAIN) as the call's result. If the
// process group still has live members, the call registers a job: the read
// end stays open and the reader keeps the last MaxJobOutputBytes in a ring.
//
// At session end the host calls KillBackgroundJobs, which kills every
// tracked group and closes the read ends. If the host crashes, the kernel
// closes the read ends for it, and any job that writes afterwards dies of
// SIGPIPE.

// MaxBackgroundJobs caps how many live background process groups the bash
// tool tracks at once. A call that would leave one more running fails, and
// its new group is killed. Exited jobs whose output has not been read are
// kept too, up to the same count; the oldest are dropped first.
var MaxBackgroundJobs = 10

// MaxJobOutputBytes is the size of a background job's output ring buffer.
var MaxJobOutputBytes = 1 << 20

// bgKillGrace is how long a kill waits after SIGTERM before SIGKILL. A var
// so tests can shorten it.
var bgKillGrace = 2 * time.Second

// newOSPipe isolates os.Pipe so its failure path (EMFILE) can be tested.
var newOSPipe = os.Pipe

// pipeReader drains a pipe read end into memory. limit 0 keeps everything;
// limit > 0 keeps only the last limit bytes.
type pipeReader struct {
	r *os.File

	mu       sync.Mutex
	buf      []byte
	limit    int
	dropped  int64
	eof      bool
	drainReq bool
	drained  chan struct{} // closed once a requested drain hit EAGAIN or EOF
	done     chan struct{} // closed when the goroutine exits
}

func newPipeReader(r *os.File) *pipeReader {
	setPipeSize(r)
	p := &pipeReader{r: r, drained: make(chan struct{}), done: make(chan struct{})}
	go p.loop()
	return p
}

func (p *pipeReader) loop() {
	defer close(p.done)
	rc, _ := p.r.SyscallConn() // cannot fail for an open *os.File
	chunk := make([]byte, 64<<10)
	for {
		stop := false
		err := rc.Read(func(fd uintptr) bool {
			for {
				n, rerr := syscall.Read(int(fd), chunk)
				if n > 0 {
					p.add(chunk[:n])
					continue
				}
				if rerr == syscall.EAGAIN {
					p.mu.Lock()
					if p.drainReq {
						p.signalDrained()
					}
					p.mu.Unlock()
					return false // wait for more data
				}
				stop = true // EOF or a hard error
				return true
			}
		})
		if stop {
			p.finish()
			return
		}
		if errors.Is(err, os.ErrDeadlineExceeded) {
			// Woken by requestDrain; clear the deadline and read on.
			_ = p.r.SetReadDeadline(time.Time{})
			continue
		}
		// The read end was closed.
		p.finish()
		return
	}
}

func (p *pipeReader) add(b []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.buf = append(p.buf, b...)
	if p.limit > 0 && len(p.buf) > p.limit {
		cut := len(p.buf) - p.limit
		p.dropped += int64(cut)
		p.buf = append(p.buf[:0], p.buf[cut:]...)
	}
}

func (p *pipeReader) finish() {
	p.mu.Lock()
	p.eof = true
	p.signalDrained()
	p.mu.Unlock()
}

// signalDrained closes drained once. Caller holds mu.
func (p *pipeReader) signalDrained() {
	select {
	case <-p.drained:
	default:
		close(p.drained)
	}
}

// drain waits until everything already in the pipe has been read, then
// returns the buffered output and empties the buffer.
func (p *pipeReader) drain() []byte {
	p.mu.Lock()
	p.drainReq = true
	p.mu.Unlock()
	_ = p.r.SetReadDeadline(time.Now()) // wake the reader if it is parked
	<-p.drained
	out, _ := p.take()
	return out
}

// take returns the buffered output and how many bytes were dropped before
// it, and empties the buffer.
func (p *pipeReader) take() ([]byte, int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	out, dropped := p.buf, p.dropped
	p.buf, p.dropped = nil, 0
	return out, dropped
}

func (p *pipeReader) atEOF() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.eof
}

func (p *pipeReader) setLimit(n int) {
	p.mu.Lock()
	p.limit = n
	p.mu.Unlock()
}

// close closes the read end and waits for the goroutine. Writers that are
// still alive get SIGPIPE on their next write.
func (p *pipeReader) close() {
	_ = p.r.Close()
	<-p.done
}

// bgJob is a process group left running by a bash call.
type bgJob struct {
	id      int
	pgid    int
	pids    []int
	start   time.Time
	command string
	out     *pipeReader
}

var (
	bgMu    sync.Mutex
	bgJobs  = map[int]*bgJob{}
	bgJobID int
)

// registerBackgroundJob tracks a still-running group and keeps reading its
// output into a ring buffer. It fails when MaxBackgroundJobs live jobs exist.
func registerBackgroundJob(pgid int, pids []int, command string, out *pipeReader) (*bgJob, error) {
	bgMu.Lock()
	defer bgMu.Unlock()
	var live, exited []*bgJob
	for _, j := range bgJobs {
		if alive, _ := groupAlive(j.pgid); alive {
			live = append(live, j)
		} else {
			exited = append(exited, j)
		}
	}
	if len(live) >= MaxBackgroundJobs {
		return nil, fmt.Errorf("background job limit reached: %d jobs already running (%s). "+
			"The new background job was killed. Stop one with `job=<id>, kill=true` or wait for it to finish, then retry",
			len(live), jobList(live))
	}
	// Drop the oldest exited jobs beyond the cap.
	sort.Slice(exited, func(a, b int) bool { return exited[a].id < exited[b].id })
	for len(exited) >= MaxBackgroundJobs {
		dropJobLocked(exited[0])
		exited = exited[1:]
	}
	out.setLimit(MaxJobOutputBytes)
	bgJobID++
	j := &bgJob{id: bgJobID, pgid: pgid, pids: pids, start: time.Now(), command: command, out: out}
	bgJobs[j.id] = j
	return j, nil
}

func dropJobLocked(j *bgJob) {
	delete(bgJobs, j.id)
	j.out.close()
}

func jobList(js []*bgJob) string {
	sort.Slice(js, func(a, b int) bool { return js[a].id < js[b].id })
	s := make([]string, len(js))
	for i, j := range js {
		s[i] = fmt.Sprintf("job %d pgid %d", j.id, j.pgid)
	}
	return strings.Join(s, ", ")
}

// BackgroundJobs returns the process-group ids of tracked background jobs
// that are still alive.
func BackgroundJobs() []int {
	bgMu.Lock()
	defer bgMu.Unlock()
	var out []int
	for _, j := range bgJobs {
		if alive, _ := groupAlive(j.pgid); alive {
			out = append(out, j.pgid)
		}
	}
	sort.Ints(out)
	return out
}

// jobStatus reports a job's state. A job is exited once its group has no
// live members.
func jobStatus(j *bgJob) (string, bool) {
	alive, pids := groupAlive(j.pgid)
	if alive {
		s := "running"
		if len(pids) > 0 {
			s += " (pids " + joinInts(pids) + ")"
		}
		return s, true
	}
	if j.out.atEOF() {
		return "exited", false
	}
	return "exited (a process outside the group still holds its output pipe)", false
}

// readJob returns a job's status and the output produced since the last
// read. A job that has exited is forgotten once read.
func readJob(id int) (string, error) {
	bgMu.Lock()
	defer bgMu.Unlock()
	j, ok := bgJobs[id]
	if !ok {
		return "", fmt.Errorf("no background job %d (list jobs with `jobs=true`)", id)
	}
	status, alive := jobStatus(j)
	data, dropped := j.out.take()
	// The command is left out on purpose: agents grep this result for
	// expected output and would match the command text (see bash-job-ab).
	text := fmt.Sprintf("[job %d: %s, pgid %d, started %s ago]",
		j.id, status, j.pgid, time.Since(j.start).Round(time.Second))
	if dropped > 0 {
		text += fmt.Sprintf("\n[%d earlier bytes dropped; the buffer keeps the last %dKB]", dropped, MaxJobOutputBytes/1024)
	}
	out := TruncateTail(StripAnsi(string(data)), TruncationOptions{}).Content
	if out == "" {
		out = "(no new output)"
	}
	text += "\n" + out
	if !alive {
		dropJobLocked(j)
	}
	return text, nil
}

// killJob kills one job's group, then returns its final status and output.
func killJob(id int) (string, error) {
	bgMu.Lock()
	j, ok := bgJobs[id]
	bgMu.Unlock()
	if !ok {
		return "", fmt.Errorf("no background job %d (list jobs with `jobs=true`)", id)
	}
	killGroups([]int{j.pgid})
	return readJob(id)
}

// listJobs describes every tracked job.
func listJobs() string {
	bgMu.Lock()
	defer bgMu.Unlock()
	if len(bgJobs) == 0 {
		return "no background jobs"
	}
	ids := make([]int, 0, len(bgJobs))
	for id := range bgJobs {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	lines := make([]string, len(ids))
	for i, id := range ids {
		j := bgJobs[id]
		status, _ := jobStatus(j)
		lines[i] = fmt.Sprintf("job %d: %s, pgid %d, started %s ago: %s",
			j.id, status, j.pgid, time.Since(j.start).Round(time.Second), truncateForLog(j.command, 100))
	}
	return strings.Join(lines, "\n")
}

// killGroups sends SIGTERM, waits up to bgKillGrace, then SIGKILLs.
func killGroups(pgids []int) {
	for _, pgid := range pgids {
		_ = syscall.Kill(-pgid, syscall.SIGTERM)
	}
	deadline := time.Now().Add(bgKillGrace)
	for time.Now().Before(deadline) && anyAlive(pgids) {
		time.Sleep(20 * time.Millisecond)
	}
	for _, pgid := range pgids {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
	}
}

func anyAlive(pgids []int) bool {
	for _, pgid := range pgids {
		if alive, _ := groupAlive(pgid); alive {
			return true
		}
	}
	return false
}

// KillBackgroundJobs kills every background process group started by the
// bash tool, then closes their output pipes. Hosts call it when the session
// ends. Safe to call more than once.
func KillBackgroundJobs() {
	bgMu.Lock()
	jobs := bgJobs
	bgJobs = map[int]*bgJob{}
	bgMu.Unlock()
	pgids := make([]int, 0, len(jobs))
	for _, j := range jobs {
		pgids = append(pgids, j.pgid)
	}
	if len(pgids) > 0 {
		killGroups(pgids)
	}
	for _, j := range jobs {
		j.out.close()
	}
}

func joinInts(xs []int) string {
	s := make([]string, len(xs))
	for i, x := range xs {
		s[i] = strconv.Itoa(x)
	}
	return strings.Join(s, ", ")
}
