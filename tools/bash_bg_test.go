package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

var bgNoteRe = regexp.MustCompile(`\[background job (\d+): pgid (\d+) still running \(pids [\d, ]+\)\. Read its output with bash job=(\d+)\. Killed when the session ends\.\]`)

func bgMatch(t *testing.T, text string) (id, pgid int) {
	t.Helper()
	m := bgNoteRe.FindStringSubmatch(text)
	require.NotNil(t, m, "no background note in %q", text)
	require.Equal(t, m[1], m[3])
	id, _ = strconv.Atoi(m[1])
	pgid, _ = strconv.Atoi(m[2])
	return id, pgid
}

func eventually(t *testing.T, d time.Duration, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

// callJob drives the job forms through the real tool entry point.
func callJob(t *testing.T, params map[string]any) (string, error) {
	t.Helper()
	res, err := NewBashTool(t.TempDir()).Execute(context.Background(), "id", params, nil)
	if err != nil {
		return "", err
	}
	return res.Content[0].Text, nil
}

func fdCount(t *testing.T) int {
	t.Helper()
	es, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skip("no /proc/self/fd")
	}
	return len(es)
}

func TestBash_BackgroundJobLateOutput(t *testing.T) {
	t.Cleanup(KillBackgroundJobs)
	start := time.Now()
	res, err := executeBash(context.Background(), "echo early; (sleep 1; echo late) &", t.TempDir(), 5*time.Second, "")
	require.NoError(t, err)
	require.Less(t, time.Since(start), 500*time.Millisecond)
	require.Contains(t, res.Content[0].Text, "early")
	id, pgid := bgMatch(t, res.Content[0].Text)
	require.Contains(t, BackgroundJobs(), pgid)

	text, err := callJob(t, map[string]any{"job": float64(id)})
	require.NoError(t, err)
	require.Contains(t, text, "running")

	time.Sleep(1500 * time.Millisecond)
	text, err = callJob(t, map[string]any{"job": float64(id)})
	require.NoError(t, err)
	require.Contains(t, text, "late")
	require.Contains(t, text, "exited")
	require.NotContains(t, text, "early") // nor the command text

	// Exited and read once: forgotten.
	_, err = callJob(t, map[string]any{"job": float64(id)})
	require.ErrorContains(t, err, "no background job")
}

func TestBash_NohupOutputReachesBuffer(t *testing.T) {
	t.Cleanup(KillBackgroundJobs)
	res, err := executeBash(context.Background(), "nohup sh -c 'sleep 1; echo hi' &", t.TempDir(), 5*time.Second, "")
	require.NoError(t, err)
	id, _ := bgMatch(t, res.Content[0].Text)
	var got string
	eventually(t, 5*time.Second, func() bool {
		text, err := readJob(id)
		require.NoError(t, err)
		got += text
		return strings.Contains(got, "hi")
	})
}

func TestBash_PlainCommandNoJobNoFdLeak(t *testing.T) {
	before := fdCount(t)
	for i := 0; i < 5; i++ {
		res, err := executeBash(context.Background(), "echo hi", t.TempDir(), 5*time.Second, "")
		require.NoError(t, err)
		require.Equal(t, "hi\n", res.Content[0].Text)
	}
	require.LessOrEqual(t, fdCount(t), before)
}

// Foreground output bigger than any pipe buffer must not deadlock.
func TestBash_LargeForegroundOutput(t *testing.T) {
	res, err := executeBash(context.Background(), "head -c 3000000 /dev/zero | tr '\\0' a; echo; echo end", t.TempDir(), 20*time.Second, "")
	require.NoError(t, err)
	require.Contains(t, res.Content[0].Text, "end")
}

func TestBash_RingBufferKeepsLastMB(t *testing.T) {
	t.Cleanup(KillBackgroundJobs)
	res, err := executeBash(context.Background(), "(sleep 0.3; yes | head -c 5000000; echo; echo tail-marker; sleep 30) &", t.TempDir(), 5*time.Second, "")
	require.NoError(t, err)
	id, _ := bgMatch(t, res.Content[0].Text)
	bgMu.Lock()
	j := bgJobs[id]
	bgMu.Unlock()
	eventually(t, 10*time.Second, func() bool {
		j.out.mu.Lock()
		defer j.out.mu.Unlock()
		return strings.Contains(string(j.out.buf), "tail-marker")
	})
	j.out.mu.Lock()
	n, dropped := len(j.out.buf), j.out.dropped
	j.out.mu.Unlock()
	require.LessOrEqual(t, n, MaxJobOutputBytes)
	require.EqualValues(t, 5000000+len("\ntail-marker\n"), int64(n)+dropped)
	text, err := readJob(id)
	require.NoError(t, err)
	require.Contains(t, text, "earlier bytes dropped")
	require.Contains(t, text, "tail-marker")
}

func TestBash_TimeoutKillsGroup(t *testing.T) {
	dir := t.TempDir()
	_, err := executeBash(context.Background(), "(sleep 1; echo x > F) & echo $$ > P; sleep 10", dir, 300*time.Millisecond, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "timed out")
	b, _ := os.ReadFile(filepath.Join(dir, "P"))
	pgid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	require.NotZero(t, pgid)
	eventually(t, 2*time.Second, func() bool { a, _ := groupAlive(pgid); return !a })
	time.Sleep(1200 * time.Millisecond)
	_, statErr := os.Stat(filepath.Join(dir, "F"))
	require.True(t, os.IsNotExist(statErr))
}

func TestBash_KillJob(t *testing.T) {
	t.Cleanup(KillBackgroundJobs)
	res, err := executeBash(context.Background(), "sleep 30 &", t.TempDir(), 5*time.Second, "")
	require.NoError(t, err)
	id, pgid := bgMatch(t, res.Content[0].Text)
	text, err := callJob(t, map[string]any{"job": float64(id), "kill": true})
	require.NoError(t, err)
	require.Contains(t, text, "exited")
	a, _ := groupAlive(pgid)
	require.False(t, a)
	_, err = callJob(t, map[string]any{"job": float64(id), "kill": true})
	require.ErrorContains(t, err, "no background job")
}

func TestBash_ListJobs(t *testing.T) {
	KillBackgroundJobs()
	t.Cleanup(KillBackgroundJobs)
	text, err := callJob(t, map[string]any{"jobs": true})
	require.NoError(t, err)
	require.Equal(t, "no background jobs", text)
	for i := 0; i < 2; i++ {
		_, err := executeBash(context.Background(), "sleep 30 &", t.TempDir(), 5*time.Second, "")
		require.NoError(t, err)
	}
	text, err = callJob(t, map[string]any{"job": float64(0)})
	require.NoError(t, err)
	require.Len(t, strings.Split(text, "\n"), 2)
	require.Contains(t, text, "running (pids")
	require.Contains(t, text, "sleep 30 &")
}

func TestBash_JobParamValidation(t *testing.T) {
	_, err := callJob(t, map[string]any{})
	require.ErrorContains(t, err, "command is required")
	_, err = callJob(t, map[string]any{"command": "echo", "job": float64(1)})
	require.ErrorContains(t, err, "not both")
	_, err = callJob(t, map[string]any{"job": float64(99999)})
	require.ErrorContains(t, err, "no background job")
	// The prefix wrapper leaves job-only calls alone.
	tool := NewBashToolWithPrefix(t.TempDir(), "true")
	_, err = tool.Execute(context.Background(), "id", map[string]any{"command": "", "job": float64(99999)}, nil)
	require.ErrorContains(t, err, "no background job")
}

func TestKillBackgroundJobs(t *testing.T) {
	var pgids []int
	for i := 0; i < 2; i++ {
		res, err := executeBash(context.Background(), "sleep 30 &", t.TempDir(), 5*time.Second, "")
		require.NoError(t, err)
		_, pgid := bgMatch(t, res.Content[0].Text)
		pgids = append(pgids, pgid)
	}
	KillBackgroundJobs()
	for _, pgid := range pgids {
		a, _ := groupAlive(pgid)
		require.False(t, a)
	}
	KillBackgroundJobs() // no-op when empty
}

// A job that ignores SIGTERM is SIGKILLed after the grace period.
func TestKillBackgroundJobs_SigtermIgnored(t *testing.T) {
	orig := bgKillGrace
	t.Cleanup(func() { bgKillGrace = orig })
	bgKillGrace = 100 * time.Millisecond
	res, err := executeBash(context.Background(), "sh -c 'trap \"\" TERM; sleep 30' &", t.TempDir(), 5*time.Second, "")
	require.NoError(t, err)
	_, pgid := bgMatch(t, res.Content[0].Text)
	time.Sleep(100 * time.Millisecond) // let trap install
	KillBackgroundJobs()
	eventually(t, 2*time.Second, func() bool { a, _ := groupAlive(pgid); return !a })
}

// Closing the read end kills a writer that escaped the group with SIGPIPE.
func TestBash_ClosedReaderSigpipesEscapedWriter(t *testing.T) {
	dir := t.TempDir()
	res, err := executeBash(context.Background(),
		"setsid sh -c 'echo $$ > P; sleep 0.5; while :; do echo x || exit 9; sleep 0.05; done' &", dir, 5*time.Second, "")
	require.NoError(t, err)
	require.NotContains(t, res.Content[0].Text, "background job") // setsid left the group
	var pid int
	eventually(t, 2*time.Second, func() bool {
		b, _ := os.ReadFile(filepath.Join(dir, "P"))
		pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		return pid > 0
	})
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	eventually(t, 5*time.Second, func() bool { return !pidAlive(pid) })
}

func pidAlive(pid int) bool {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return syscall.Kill(pid, 0) == nil
	}
	s := string(b)
	f := strings.Fields(s[strings.LastIndexByte(s, ')')+1:])
	return f[0] != "Z" && f[0] != "X"
}

// A job whose group is gone but whose pipe is still held elsewhere.
func TestJobStatus_PipeHeldOutsideGroup(t *testing.T) {
	t.Cleanup(KillBackgroundJobs)
	dir := t.TempDir()
	res, err := executeBash(context.Background(),
		"setsid sh -c 'echo $$ > S; exec sleep 30' & sleep 30 &", dir, 5*time.Second, "")
	require.NoError(t, err)
	id, pgid := bgMatch(t, res.Content[0].Text)
	var spid int
	eventually(t, 2*time.Second, func() bool {
		b, _ := os.ReadFile(filepath.Join(dir, "S"))
		spid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		return spid > 0
	})
	t.Cleanup(func() { _ = syscall.Kill(spid, syscall.SIGKILL) })
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
	eventually(t, 2*time.Second, func() bool { a, _ := groupAlive(pgid); return !a })
	text, err := readJob(id)
	require.NoError(t, err)
	require.Contains(t, text, "outside the group")
	require.Contains(t, text, "(no new output)")
}

func TestBash_BackgroundWithNonZeroExit(t *testing.T) {
	t.Cleanup(KillBackgroundJobs)
	_, err := executeBash(context.Background(), "sleep 5 & exit 3", t.TempDir(), 5*time.Second, "")
	require.Error(t, err)
	bgMatch(t, err.Error())
	require.Contains(t, err.Error(), "exited with code 3")
}

func TestBackgroundJobLimit(t *testing.T) {
	orig := MaxBackgroundJobs
	t.Cleanup(func() { MaxBackgroundJobs = orig; KillBackgroundJobs() })
	KillBackgroundJobs()
	MaxBackgroundJobs = 2
	dir := t.TempDir()
	for i := 0; i < 2; i++ {
		_, err := executeBash(context.Background(), "sleep 30 &", dir, 5*time.Second, "")
		require.NoError(t, err)
	}
	_, err := executeBash(context.Background(), "echo $$ > P; sleep 30 &", dir, 5*time.Second, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "background job limit reached: 2 jobs already running (job ")
	b, _ := os.ReadFile(filepath.Join(dir, "P"))
	pgid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	eventually(t, 5*time.Second, func() bool { a, _ := groupAlive(pgid); return !a })
	require.Len(t, BackgroundJobs(), 2)
}

// Exited, unread jobs beyond the cap are dropped oldest first.
func TestBackgroundJobs_ExitedEvicted(t *testing.T) {
	orig := MaxBackgroundJobs
	t.Cleanup(func() { MaxBackgroundJobs = orig; KillBackgroundJobs() })
	KillBackgroundJobs()
	MaxBackgroundJobs = 2
	var ids []int
	for i := 0; i < 3; i++ {
		res, err := executeBash(context.Background(), "sleep 0.3 &", t.TempDir(), 5*time.Second, "")
		require.NoError(t, err)
		id, pgid := bgMatch(t, res.Content[0].Text)
		ids = append(ids, id)
		eventually(t, 3*time.Second, func() bool { a, _ := groupAlive(pgid); return !a })
	}
	bgMu.Lock()
	_, first := bgJobs[ids[0]]
	n := len(bgJobs)
	bgMu.Unlock()
	require.False(t, first)
	require.Equal(t, 2, n)
	require.Empty(t, BackgroundJobs())
}

func TestExecuteBash_PipeError(t *testing.T) {
	orig := newOSPipe
	t.Cleanup(func() { newOSPipe = orig })
	newOSPipe = func() (*os.File, *os.File, error) { return nil, nil, errors.New("boom") }
	_, err := executeBash(context.Background(), "echo hi", t.TempDir(), time.Second, "")
	require.ErrorContains(t, err, "output pipe")
}

func TestBash_PrintModeNote(t *testing.T) {
	PrintMode = true
	t.Cleanup(func() { PrintMode = false; KillBackgroundJobs() })
	res, err := executeBash(context.Background(), "sleep 5 &", t.TempDir(), 5*time.Second, "")
	require.NoError(t, err)
	bgMatch(t, res.Content[0].Text)
	require.Contains(t, res.Content[0].Text, "This session ends after this turn, so the job will die.")
}
