package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// installFakeBin writes an executable script named `name` into a fresh temp dir
// and prepends that dir to PATH so exec.LookPath finds it first.
func installFakeBin(t *testing.T, name, script string) {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(p, []byte(script), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// fakeFd installs a fake `fd` that echoes $FAKE_FD_OUT and exits $FAKE_FD_EXIT.
func fakeFd(t *testing.T) {
	installFakeBin(t, "fd", "#!/bin/bash\nprintf '%s' \"$FAKE_FD_OUT\"\nexit ${FAKE_FD_EXIT:-0}\n")
}

func TestRunFindCommand_FdSuccess(t *testing.T) {
	fakeFd(t)
	t.Setenv("FAKE_FD_OUT", "a.txt\nb.txt\n")
	t.Setenv("FAKE_FD_EXIT", "0")
	out, err := runFindCommand(context.Background(), "*.txt", t.TempDir(), 100)
	require.NoError(t, err)
	require.Contains(t, out, "a.txt")
}

func TestRunFindCommand_FdNoMatch(t *testing.T) {
	fakeFd(t)
	t.Setenv("FAKE_FD_OUT", "")
	t.Setenv("FAKE_FD_EXIT", "1") // fd exit 1 == no matches, not an error
	out, err := runFindCommand(context.Background(), "*.none", t.TempDir(), 100)
	require.NoError(t, err)
	require.Equal(t, "", out)
}

func TestRunFindCommand_FdFailsFallsBackToFind(t *testing.T) {
	fakeFd(t)
	t.Setenv("FAKE_FD_OUT", "")
	t.Setenv("FAKE_FD_EXIT", "2") // fd hard failure -> fall back to system find
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "real.txt"), []byte("x"), 0o644))
	out, err := runFindCommand(context.Background(), "real.txt", dir, 100)
	require.NoError(t, err)
	require.Contains(t, out, "real.txt")
}

func TestRunFindCommand_FindError(t *testing.T) {
	// No fd on PATH; point find at a nonexistent path so it errors.
	t.Setenv("PATH", "/usr/bin:/bin")
	_, err := runFindCommand(context.Background(), "*", filepath.Join(t.TempDir(), "nope"), 100)
	require.Error(t, err)
	require.Contains(t, err.Error(), "find failed")
}

func TestRunFindCommand_FindLimit(t *testing.T) {
	t.Setenv("PATH", "/usr/bin:/bin")
	dir := t.TempDir()
	for i := 0; i < 5; i++ {
		require.NoError(t, os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%d.txt", i)), []byte("x"), 0o644))
	}
	out, err := runFindCommand(context.Background(), "*.txt", dir, 2)
	require.NoError(t, err)
	require.Len(t, strings.Split(strings.TrimSpace(out), "\n"), 2)
}

func TestFindTool_RunCommandError(t *testing.T) {
	// Install fake fd (hard fail) and fake find (hard fail) so runFindCommand
	// returns an error and Execute surfaces it.
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "fd"), []byte("#!/bin/bash\nexit 2\n"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "find"), []byte("#!/bin/bash\nexit 2\n"), 0o755))
	t.Setenv("PATH", dir)
	tool := NewFindTool(t.TempDir())
	_, err := tool.Execute(context.Background(), "c", map[string]any{"pattern": "*"}, nil)
	require.Error(t, err)
}

func TestFindTool_NoFilesFound(t *testing.T) {
	fakeFd(t)
	t.Setenv("FAKE_FD_OUT", "   \n")
	tool := NewFindTool(t.TempDir())
	res, err := tool.Execute(context.Background(), "c", map[string]any{"pattern": "*.none"}, nil)
	require.NoError(t, err)
	require.Contains(t, res.Content[0].Text, "No files found")
}

func TestFindTool_LimitClamp(t *testing.T) {
	fakeFd(t)
	t.Setenv("FAKE_FD_OUT", "a.txt\n")
	dir := t.TempDir()
	tool := NewFindTool(dir)
	// limit < 1 clamps to 1.
	res, err := tool.Execute(context.Background(), "c", map[string]any{"pattern": "*.txt", "limit": float64(0)}, nil)
	require.NoError(t, err)
	require.Contains(t, res.Content[0].Text, "a.txt")
}

func TestFindTool_RelativizeBranches(t *testing.T) {
	fakeFd(t)
	dir := t.TempDir()
	// Mix of: prefixed path, blank line, unrelated-absolute (Rel succeeds),
	// relative path (Rel errors -> Base), and a trailing-slash relative path.
	out := strings.Join([]string{
		filepath.Join(dir, "under.txt"),
		"",
		"/some/other/abs.txt",
		"rel/inner.txt",
		"reldir/",
	}, "\n")
	t.Setenv("FAKE_FD_OUT", out+"\n")
	tool := NewFindTool(dir)
	res, err := tool.Execute(context.Background(), "c", map[string]any{"pattern": "*"}, nil)
	require.NoError(t, err)
	txt := res.Content[0].Text
	require.Contains(t, txt, "under.txt")
	require.Contains(t, txt, "reldir/")
}

func TestFindTool_ResultLimitReached(t *testing.T) {
	fakeFd(t)
	dir := t.TempDir()
	t.Setenv("FAKE_FD_OUT", "a.txt\nb.txt\nc.txt\nd.txt\ne.txt\n")
	tool := NewFindTool(dir)
	res, err := tool.Execute(context.Background(), "c", map[string]any{"pattern": "*", "limit": float64(3)}, nil)
	require.NoError(t, err)
	require.Contains(t, res.Content[0].Text, "results limit reached")
	det, ok := res.Details.(*FindToolDetails)
	require.True(t, ok)
	require.NotNil(t, det.ResultLimitReached)
}

func TestFindTool_ByteTruncation(t *testing.T) {
	fakeFd(t)
	dir := t.TempDir()
	// Produce >50KB of output across many lines but stay under the result
	// limit so only the byte-truncation branch trips.
	var b strings.Builder
	for i := 0; i < 800; i++ {
		fmt.Fprintf(&b, "file-with-a-fairly-long-name-%04d-%s.txt\n", i, strings.Repeat("x", 80))
	}
	t.Setenv("FAKE_FD_OUT", b.String())
	tool := NewFindTool(dir)
	res, err := tool.Execute(context.Background(), "c", map[string]any{"pattern": "*", "limit": float64(100000)}, nil)
	require.NoError(t, err)
	require.Contains(t, res.Content[0].Text, "limit reached")
	det, ok := res.Details.(*FindToolDetails)
	require.True(t, ok)
	require.NotNil(t, det.Truncation)
}
