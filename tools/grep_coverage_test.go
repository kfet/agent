package tools

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// fakeRg installs a fake `rg` in a fresh temp dir that prints the given
// output, returning its absolute path. The output travels in a file next to
// the script rather than via env vars, so callers stay parallel-safe.
func fakeRg(t *testing.T, out string) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "out"), []byte(out), 0o644))
	p := filepath.Join(dir, "rg")
	require.NoError(t, os.WriteFile(p, []byte("#!/bin/bash\ncat \"$(dirname \"$0\")/out\"\n"), 0o755))
	return p
}

// rgMatch builds one ripgrep --json "match" event line.
func rgMatch(path string, line int) string {
	return fmt.Sprintf(`{"type":"match","data":{"path":{"text":%q},"line_number":%d}}`, path, line)
}

func TestGrepWithRipgrep_StdoutPipeError(t *testing.T) {
	orig := stdoutPipe
	t.Cleanup(func() { stdoutPipe = orig })
	stdoutPipe = func(cmd *exec.Cmd) (io.ReadCloser, error) { return nil, errors.New("boom") }
	_, err := grepWithRipgrep(context.Background(), "rg", "x", t.TempDir(), true, false, false, "", 0, 100)
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to start ripgrep")
}

func TestGrepWithRipgrep_StartError(t *testing.T) {
	t.Parallel()
	_, err := grepWithRipgrep(context.Background(), "/nonexistent/rg-binary", "x", t.TempDir(), true, false, false, "", 0, 100)
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to start ripgrep")
}

func TestGrepWithRipgrep_NoMatches(t *testing.T) {
	t.Parallel()
	rg := fakeRg(t, "")
	res, err := grepWithRipgrep(context.Background(), rg, "x", t.TempDir(), true, false, false, "", 0, 100)
	require.NoError(t, err)
	require.Contains(t, res.Content[0].Text, "No matches found")
}

func TestGrepWithRipgrep_BlankAndBadJSON(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	f := filepath.Join(dir, "a.txt")
	require.NoError(t, os.WriteFile(f, []byte("hello\nworld\n"), 0o644))
	out := strings.Join([]string{
		"",                // blank line -> skipped
		"not-json-at-all", // unmarshal error -> skipped
		`{"type":"begin","data":{"path":{"text":"x"}}}`, // non-match event
		rgMatch(f, 1),
	}, "\n")
	rg := fakeRg(t, out+"\n")
	res, err := grepWithRipgrep(context.Background(), rg, "hello", dir, true, false, false, "", 0, 100)
	require.NoError(t, err)
	require.Contains(t, res.Content[0].Text, "hello")
}

func TestGrepWithRipgrep_UnreadableFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	rg := fakeRg(t, rgMatch(filepath.Join(dir, "ghost.txt"), 3)+"\n")
	res, err := grepWithRipgrep(context.Background(), rg, "x", dir, true, false, false, "", 0, 100)
	require.NoError(t, err)
	require.Contains(t, res.Content[0].Text, "(unable to read file)")
}

func TestGrepWithRipgrep_ContextAndLongLine(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	f := filepath.Join(dir, "multi.txt")
	longLine := strings.Repeat("z", 600) // > GrepMaxLineLength (500)
	content := "line1\nline2\n" + longLine + "\nline4\nline5\n"
	require.NoError(t, os.WriteFile(f, []byte(content), 0o644))
	rg := fakeRg(t, rgMatch(f, 3)+"\n") // match the long line
	res, err := grepWithRipgrep(context.Background(), rg, "z", dir, true, false, false, "", 1, 100)
	require.NoError(t, err)
	txt := res.Content[0].Text
	require.Contains(t, txt, "line2") // context line (before)
	require.Contains(t, txt, "line4") // context line (after)
	require.Contains(t, txt, "Some lines truncated")
}

func TestGrepWithRipgrep_LimitReached(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	f := filepath.Join(dir, "a.txt")
	require.NoError(t, os.WriteFile(f, []byte("m\nm\nm\nm\nm\n"), 0o644))
	var lines []string
	for i := 1; i <= 5; i++ {
		lines = append(lines, rgMatch(f, i))
	}
	rg := fakeRg(t, strings.Join(lines, "\n")+"\n")
	res, err := grepWithRipgrep(context.Background(), rg, "m", dir, true, false, false, "", 0, 2)
	require.NoError(t, err)
	require.Contains(t, res.Content[0].Text, "matches limit reached")
	det, ok := res.Details.(*GrepToolDetails)
	require.True(t, ok)
	require.NotNil(t, det.MatchLimitReached)
}

func TestGrepWithRipgrep_ByteTruncation(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	f := filepath.Join(dir, "big.txt")
	var b strings.Builder
	for i := 0; i < 800; i++ {
		fmt.Fprintf(&b, "match line %d %s\n", i, strings.Repeat("y", 80))
	}
	require.NoError(t, os.WriteFile(f, []byte(b.String()), 0o644))
	var lines []string
	for i := 1; i <= 800; i++ {
		lines = append(lines, rgMatch(f, i))
	}
	rg := fakeRg(t, strings.Join(lines, "\n")+"\n")
	res, err := grepWithRipgrep(context.Background(), rg, "match", dir, true, false, false, "", 0, 100000)
	require.NoError(t, err)
	require.Contains(t, res.Content[0].Text, "limit reached")
	det, ok := res.Details.(*GrepToolDetails)
	require.True(t, ok)
	require.NotNil(t, det.Truncation)
}

func TestGrepTool_ContextAndLimitClamp(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.txt"), []byte("alpha\nbeta\ngamma\n"), 0o644))
	tool := NewGrepTool(dir)
	res, err := tool.Execute(context.Background(), "c", map[string]any{
		"pattern": "beta", "context": float64(1), "limit": float64(0),
	}, nil)
	require.NoError(t, err)
	require.Contains(t, res.Content[0].Text, "beta")
}

func TestGrepTool_FallbackWhenNoRipgrep(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.txt"), []byte("needle\n"), 0o644))
	t.Setenv("PATH", "/usr/bin:/bin") // rg lives in /opt/homebrew, not here
	tool := NewGrepTool(dir)
	res, err := tool.Execute(context.Background(), "c", map[string]any{"pattern": "needle"}, nil)
	require.NoError(t, err)
	require.Contains(t, res.Content[0].Text, "needle")
}

// installFakeGrep puts a fake `grep` on PATH that prints $FAKE_GREP_OUT and
// exits $FAKE_GREP_EXIT.
func installFakeGrep(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "grep"),
		[]byte("#!/bin/bash\nprintf '%s' \"$FAKE_GREP_OUT\"\nexit ${FAKE_GREP_EXIT:-0}\n"), 0o755))
	t.Setenv("PATH", dir)
}

func TestGrepFallback_HardError(t *testing.T) {
	installFakeGrep(t)
	t.Setenv("FAKE_GREP_EXIT", "2") // not exit 1 (no-match) -> hard error
	_, err := grepFallback(context.Background(), "x", t.TempDir(), true, false, false, "", 100)
	require.Error(t, err)
	require.Contains(t, err.Error(), "grep failed")
}

func TestGrepFallback_EmptyOutput(t *testing.T) {
	installFakeGrep(t)
	t.Setenv("FAKE_GREP_OUT", "   \n")
	t.Setenv("FAKE_GREP_EXIT", "0")
	res, err := grepFallback(context.Background(), "x", t.TempDir(), true, false, false, "", 100)
	require.NoError(t, err)
	require.Contains(t, res.Content[0].Text, "No matches found")
}

func TestGrepFallback_ByteTruncation(t *testing.T) {
	installFakeGrep(t)
	var b strings.Builder
	for i := 0; i < 1000; i++ {
		fmt.Fprintf(&b, "/some/path/file%04d.txt:%d: matched %s\n", i, i, strings.Repeat("q", 60))
	}
	t.Setenv("FAKE_GREP_OUT", b.String())
	t.Setenv("FAKE_GREP_EXIT", "0")
	res, err := grepFallback(context.Background(), "x", t.TempDir(), true, false, false, "", 100000)
	require.NoError(t, err)
	require.Contains(t, res.Content[0].Text, "limit reached")
}
