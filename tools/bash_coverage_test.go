package tools

import (
	"context"
	"errors"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestTruncateForLog(t *testing.T) {
	t.Parallel()
	require.Equal(t, "abc", truncateForLog("abc", 10))
	require.Equal(t, "ab...", truncateForLog("abcdef", 2))
}

// TestExecuteBash_PipeError exercises the os.Pipe failure path via the seam.
func TestExecuteBash_PipeError(t *testing.T) {
	orig := newOSPipe
	t.Cleanup(func() { newOSPipe = orig })
	newOSPipe = func() (*os.File, *os.File, error) { return nil, nil, errors.New("boom") }

	_, err := executeBash(context.Background(), "echo hi", t.TempDir(), time.Second, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "pipe")
}

// TestExecuteBash_StartError forces cmd.Start to fail by removing bash from PATH.
func TestExecuteBash_StartError(t *testing.T) {
	t.Setenv("PATH", "")
	_, err := executeBash(context.Background(), "echo hi", t.TempDir(), time.Second, "")
	require.Error(t, err)
}

// TestExecuteBash_DrainGraceFallback drives the drain-race fallback
// deterministically: the newOSPipe seam keeps an extra dup of the write end
// open, so io.Copy never sees EOF (mimicking a descendant that holds the pipe
// open past killpg). The drain therefore cannot finish within drainGracePeriod
// and the real force-close path runs.
func TestExecuteBash_DrainGraceFallback(t *testing.T) {
	origPipe := newOSPipe
	origGrace := drainGracePeriod
	t.Cleanup(func() { newOSPipe = origPipe; drainGracePeriod = origGrace })
	drainGracePeriod = 5 * time.Millisecond

	var extraWriteEnd *os.File
	t.Cleanup(func() {
		if extraWriteEnd != nil {
			_ = extraWriteEnd.Close()
		}
	})
	newOSPipe = func() (*os.File, *os.File, error) {
		pr, pw, err := os.Pipe()
		if err != nil {
			return nil, nil, err
		}
		// Dup the write end and hold it open: io.Copy on the read end won't
		// hit EOF until every write end closes, so the drain blocks past
		// drainGracePeriod and the fallback pr.Close() fires.
		fd, dupErr := syscall.Dup(int(pw.Fd()))
		if dupErr == nil {
			extraWriteEnd = os.NewFile(uintptr(fd), "extra-write-end")
		}
		return pr, pw, nil
	}

	res, err := executeBash(context.Background(), "echo foreground", t.TempDir(), 5*time.Second, "")
	require.NoError(t, err)
	require.Contains(t, res.Content[0].Text, "foreground")
}

// TestExecuteBash_GenericWaitError exercises the fall-through error path where
// process Wait returns a non-ExitError, non-cancellation error.
func TestExecuteBash_GenericWaitError(t *testing.T) {
	orig := processWait
	t.Cleanup(func() { processWait = orig })
	processWait = func(p *os.Process) (*os.ProcessState, error) {
		_, _ = p.Wait() // reap for real so we don't leak a zombie
		return nil, errors.New("synthetic wait failure")
	}
	_, err := executeBash(context.Background(), "echo hi", t.TempDir(), 5*time.Second, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "synthetic wait failure")
}

// and full-output temp-file path.
func TestExecuteBash_TruncationNotice(t *testing.T) {
	t.Parallel()
	// Produce more than DefaultMaxLines lines to trigger line truncation.
	res, err := executeBash(context.Background(),
		"for i in $(seq 1 2500); do echo line$i; done", t.TempDir(), 30*time.Second, "")
	require.NoError(t, err)
	require.Contains(t, res.Content[0].Text, "Full output:")
}

// TestExecuteBash_ByteTruncationNotice produces a handful of very long lines so
// truncation trips on the byte budget rather than the line budget.
func TestExecuteBash_ByteTruncationNotice(t *testing.T) {
	t.Parallel()
	// 10 lines of ~8KB each = ~80KB > 50KB byte budget, but only 10 lines.
	res, err := executeBash(context.Background(),
		"for i in $(seq 1 10); do head -c 8000 /dev/zero | tr '\\0' a; echo; done",
		t.TempDir(), 30*time.Second, "")
	require.NoError(t, err)
	require.Contains(t, res.Content[0].Text, "KB limit). Full output:")
}

// TestExecuteBash_TimeoutWithOutput hits the deadline path after some output
// has already been produced.
func TestExecuteBash_TimeoutWithOutput(t *testing.T) {
	t.Parallel()
	_, err := executeBash(context.Background(), "echo started; sleep 10", t.TempDir(), 200*time.Millisecond, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "started")
	require.Contains(t, err.Error(), "timed out")
}

// TestExecuteBash_CancelWithOutput hits the cancellation path after some output
// has already been produced.
func TestExecuteBash_CancelWithOutput(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	_, err := executeBash(ctx, "echo started; sleep 10", t.TempDir(), 5*time.Second, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "started")
	require.Contains(t, err.Error(), "aborted")
}
