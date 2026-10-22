package tools

import (
	"context"
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

var bgLogRe = regexp.MustCompile(`process group (\d+) still running \(pids [\d, ]+\)\. Output continues in (\S+)\.`)

func bgMatch(t *testing.T, text string) (int, string) {
	t.Helper()
	m := bgLogRe.FindStringSubmatch(text)
	require.NotNil(t, m, "no background note in %q", text)
	pgid, _ := strconv.Atoi(m[1])
	return pgid, m[2]
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

func TestBash_BackgroundJobSurvives(t *testing.T) {
	dir := t.TempDir()
	start := time.Now()
	res, err := executeBash(context.Background(), "sleep 2 && echo late > F &", dir, 5*time.Second, "")
	require.NoError(t, err)
	require.Less(t, time.Since(start), time.Second)
	pgid, logPath := bgMatch(t, res.Content[0].Text)
	t.Cleanup(func() { KillBackgroundJobs(); os.Remove(logPath) })
	require.Contains(t, BackgroundJobs(), pgid)
	eventually(t, 5*time.Second, func() bool {
		b, err := os.ReadFile(filepath.Join(dir, "F"))
		return err == nil && string(b) == "late\n"
	})
}

func TestBash_NohupOutputLandsInLog(t *testing.T) {
	res, err := executeBash(context.Background(), "nohup sh -c 'sleep 1; echo hi' &", t.TempDir(), 5*time.Second, "")
	require.NoError(t, err)
	_, logPath := bgMatch(t, res.Content[0].Text)
	t.Cleanup(func() { KillBackgroundJobs(); os.Remove(logPath) })
	eventually(t, 5*time.Second, func() bool {
		b, _ := os.ReadFile(logPath)
		return strings.Contains(string(b), "hi")
	})
}

func TestBash_PlainCommandNoNoteNoTempFile(t *testing.T) {
	var created string
	orig := createOutputFile
	t.Cleanup(func() { createOutputFile = orig })
	createOutputFile = func() (*os.File, error) {
		f, err := orig()
		if err == nil {
			created = f.Name()
		}
		return f, err
	}
	res, err := executeBash(context.Background(), "echo hi", t.TempDir(), 5*time.Second, "")
	require.NoError(t, err)
	require.NotContains(t, res.Content[0].Text, "background")
	require.NotEmpty(t, created)
	_, statErr := os.Stat(created)
	require.True(t, os.IsNotExist(statErr))
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

func TestKillBackgroundJobs(t *testing.T) {
	res, err := executeBash(context.Background(), "sleep 30 &", t.TempDir(), 5*time.Second, "")
	require.NoError(t, err)
	pgid, logPath := bgMatch(t, res.Content[0].Text)
	t.Cleanup(func() { os.Remove(logPath) })
	KillBackgroundJobs()
	a, _ := groupAlive(pgid)
	require.False(t, a)
	KillBackgroundJobs() // no-op when empty
}

// A job that ignores SIGTERM is SIGKILLed after the grace period.
func TestKillBackgroundJobs_SigtermIgnored(t *testing.T) {
	orig := bgKillGrace
	t.Cleanup(func() { bgKillGrace = orig })
	bgKillGrace = 100 * time.Millisecond
	res, err := executeBash(context.Background(), "sh -c 'trap \"\" TERM; sleep 30' &", t.TempDir(), 5*time.Second, "")
	require.NoError(t, err)
	pgid, logPath := bgMatch(t, res.Content[0].Text)
	t.Cleanup(func() { os.Remove(logPath) })
	time.Sleep(100 * time.Millisecond) // let trap install
	KillBackgroundJobs()
	eventually(t, 2*time.Second, func() bool { a, _ := groupAlive(pgid); return !a })
}

func TestBackgroundJobs_PrunesDead(t *testing.T) {
	cmd := []string{"sh", "-c", "exit 0"}
	p, err := os.StartProcess("/bin/sh", cmd, &os.ProcAttr{Sys: &syscall.SysProcAttr{Setpgid: true}})
	require.NoError(t, err)
	_, _ = p.Wait()
	registerBackgroundJob(p.Pid)
	require.NotContains(t, BackgroundJobs(), p.Pid)
}

func TestGroupAlive_ProcEdgeCases(t *testing.T) {
	orig := procDir
	t.Cleanup(func() { procDir = orig })
	d := t.TempDir()
	procDir = d
	mk := func(name, stat string) {
		require.NoError(t, os.MkdirAll(filepath.Join(d, name), 0o755))
		if stat != "" {
			require.NoError(t, os.WriteFile(filepath.Join(d, name, "stat"), []byte(stat), 0o644))
		}
	}
	mk("self", "")                      // non-numeric
	mk("10", "")                        // no stat file
	mk("11", "garbage")                 // no ')'
	mk("12", "12 (a) S")                // too few fields
	mk("13", "13 (z) Z 1 77 0")         // zombie
	mk("14", "14 (x (y)) S 1 77 0")     // match, tricky comm
	mk("15", "15 (q) S 1 notanumber 0") // bad pgrp
	alive, pids := groupAlive(77)
	require.True(t, alive)
	require.Equal(t, []int{14}, pids)
	require.Equal(t, "1, 2", joinInts([]int{1, 2}))
}

// Background job plus truncated output: the note and the full-output path
// share the same kept file.
func TestBash_BackgroundWithNonZeroExit(t *testing.T) {
	res, err := executeBash(context.Background(), "sleep 5 & exit 3", t.TempDir(), 5*time.Second, "")
	_ = res
	t.Cleanup(KillBackgroundJobs)
	require.Error(t, err)
	require.Contains(t, err.Error(), "background: process group")
	require.Contains(t, err.Error(), "exited with code 3")
	_, logPath := bgMatch(t, err.Error())
	os.Remove(logPath)
}
