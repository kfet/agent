package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/text/unicode/norm"
)

// countingCancelCtx reports cancellation only on the Nth (and later) call to
// Err(), letting tests reach the *second* defensive ctx.Err() check inside a
// tool's Execute (the first check passes, a later one trips). This covers those
// belt-and-suspenders cancellation guards without racy real-time cancellation.
type countingCancelCtx struct {
	context.Context
	n        *int
	cancelAt int
}

func (c *countingCancelCtx) Err() error {
	*c.n++
	if *c.n >= c.cancelAt {
		return context.Canceled
	}
	return c.Context.Err()
}

func cancelOnNthErr(parent context.Context, n int) context.Context {
	return &countingCancelCtx{Context: parent, n: new(int), cancelAt: n}
}

func TestWriteTool_ErrorPaths(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	tool := NewWriteTool(dir)
	ctx := context.Background()

	// Missing path.
	_, err := tool.Execute(ctx, "c", map[string]any{"content": "x"}, nil)
	require.Error(t, err)

	// First ctx check trips (pre-cancelled).
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = tool.Execute(cancelled, "c", map[string]any{"path": "a.txt", "content": "x"}, nil)
	require.Error(t, err)

	// Second ctx check trips (after the dir is created).
	_, err = tool.Execute(cancelOnNthErr(ctx, 2), "c", map[string]any{"path": "b.txt", "content": "x"}, nil)
	require.ErrorIs(t, err, context.Canceled)

	// MkdirAll error: a parent path element is a regular file.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "afile"), []byte("x"), 0644))
	_, err = tool.Execute(ctx, "c", map[string]any{"path": "afile/sub.txt", "content": "x"}, nil)
	require.Error(t, err)

	// WriteFile error: the target path is an existing directory.
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "adir"), 0755))
	_, err = tool.Execute(ctx, "c", map[string]any{"path": "adir", "content": "x"}, nil)
	require.Error(t, err)
}

func TestWriteToolWithWriter_ErrorPaths(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ctx := context.Background()

	// Missing path.
	tool := NewWriteToolWithWriter(dir, func(_ context.Context, _, _ string) error { return nil })
	_, err := tool.Execute(ctx, "c", map[string]any{"content": "x"}, nil)
	require.Error(t, err)

	// ctx cancelled.
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = tool.Execute(cancelled, "c", map[string]any{"path": "a.txt", "content": "x"}, nil)
	require.Error(t, err)

	// writeFn error.
	failing := NewWriteToolWithWriter(dir, func(_ context.Context, _, _ string) error {
		return context.DeadlineExceeded
	})
	_, err = failing.Execute(ctx, "c", map[string]any{"path": "a.txt", "content": "x"}, nil)
	require.Error(t, err)

	// Success.
	var got string
	ok := NewWriteToolWithWriter(dir, func(_ context.Context, _, content string) error {
		got = content
		return nil
	})
	_, err = ok.Execute(ctx, "c", map[string]any{"path": "a.txt", "content": "hello"}, nil)
	require.NoError(t, err)
	require.Equal(t, "hello", got)
}

func TestResolveReadPath_AllVariants(t *testing.T) {
	dir := t.TempDir()

	// Substitute an exact-byte-match fileExists so the normalization-sensitive
	// fallbacks are reachable regardless of the host filesystem's folding.
	orig := fileExists
	defer func() { fileExists = orig }()
	existing := map[string]bool{}
	fileExists = func(p string) bool { return existing[p] }

	// Literal missing; AM/PM narrow-no-break-space variant present.
	amLiteral := filepath.Join(dir, "Shot 3 AM.png")
	existing[filepath.Join(dir, "Shot 3"+narrowNoBreakSpace+"AM.png")] = true
	require.Equal(t, filepath.Join(dir, "Shot 3"+narrowNoBreakSpace+"AM.png"), ResolveReadPath(amLiteral, dir))

	// NFD variant present.
	existing = map[string]bool{}
	nfcLit := filepath.Join(dir, "caf\u00e9.txt")
	nfdVar := norm.NFD.String(nfcLit)
	existing[nfdVar] = true
	require.Equal(t, nfdVar, ResolveReadPath(nfcLit, dir))

	// Curly-quote variant present.
	existing = map[string]bool{}
	straight := filepath.Join(dir, "it's.txt")
	curly := filepath.Join(dir, "it\u2019s.txt")
	existing[curly] = true
	require.Equal(t, curly, ResolveReadPath(straight, dir))

	// Combined NFD + curly variant present (only the doubly-transformed name).
	existing = map[string]bool{}
	comboLiteral := filepath.Join(dir, "caf\u00e9's.txt")
	comboVariant := norm.NFD.String(filepath.Join(dir, "caf\u00e9\u2019s.txt"))
	existing[comboVariant] = true
	require.Equal(t, comboVariant, ResolveReadPath(comboLiteral, dir))

	// Nothing present anywhere -> returns the resolved literal unchanged.
	existing = map[string]bool{}
	require.Equal(t, nfcLit, ResolveReadPath(nfcLit, dir))
}

func TestEditTool_ExtraErrorPaths(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	tool := NewEditTool(dir)
	ctx := context.Background()

	// ReadFile error: the path is a directory (Stat ok, ReadFile fails).
	sub := filepath.Join(dir, "adir")
	require.NoError(t, os.MkdirAll(sub, 0755))
	_, err := tool.Execute(ctx, "c", map[string]any{"path": "adir", "oldText": "a", "newText": "b"}, nil)
	require.Error(t, err)

	// Valid file for the ctx-check and write-error cases.
	f := filepath.Join(dir, "f.txt")
	require.NoError(t, os.WriteFile(f, []byte("hello"), 0644))

	// Second ctx check trips (after read).
	_, err = tool.Execute(cancelOnNthErr(ctx, 2), "c", map[string]any{"path": "f.txt", "oldText": "hello", "newText": "bye"}, nil)
	require.ErrorIs(t, err, context.Canceled)

	// Third ctx check trips (after applyEditLogic).
	_, err = tool.Execute(cancelOnNthErr(ctx, 3), "c", map[string]any{"path": "f.txt", "oldText": "hello", "newText": "bye"}, nil)
	require.ErrorIs(t, err, context.Canceled)

	// WriteFile error: target file is read-only.
	ro := filepath.Join(dir, "ro.txt")
	require.NoError(t, os.WriteFile(ro, []byte("hello"), 0444))
	_, err = tool.Execute(ctx, "c", map[string]any{"path": "ro.txt", "oldText": "hello", "newText": "bye"}, nil)
	require.Error(t, err)
}

func TestEditToolWithReadWriter_WriteFnError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	readFn := func(_ context.Context, _ string) (string, error) { return "hello", nil }
	writeFn := func(_ context.Context, _, _ string) error { return context.DeadlineExceeded }
	tool := NewEditToolWithReadWriter(dir, readFn, writeFn)
	_, err := tool.Execute(context.Background(), "c", map[string]any{"path": "f.txt", "oldText": "hello", "newText": "bye"}, nil)
	require.Error(t, err)
}

func TestDetectLineEnding_LFBeforeCRLF(t *testing.T) {
	t.Parallel()
	// \n appears before \r\n -> dominant ending is "\n".
	require.Equal(t, "\n", detectLineEnding("a\nb\r\nc"))
}

func TestEditDiff_ExtraBranches(t *testing.T) {
	t.Parallel()
	// contextLines <= 0 defaults internally.
	_ = GenerateDiffString("a\nb\n", "a\nc\n", 0)

	// Large input forces the simpleDiffParts fallback (m*n > 10_000_000).
	var oldB, newB strings.Builder
	for i := 0; i < 3200; i++ {
		fmt.Fprintf(&oldB, "old line %d\n", i)
		fmt.Fprintf(&newB, "new line %d\n", i)
	}
	_ = computeDiffParts(strings.Split(oldB.String(), "\n"), strings.Split(newB.String(), "\n"))

	// simpleDiffParts with newLines shorter than oldLines.
	_ = simpleDiffParts([]string{"a", "b", "c"}, []string{"a"})
}
