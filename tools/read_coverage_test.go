package tools

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func writePNG(t *testing.T, path string, w, h int) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), uint8(x + y), 255})
		}
	}
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	require.NoError(t, os.WriteFile(path, buf.Bytes(), 0o644))
}

func TestExecuteRead_ReadFileError(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("root bypasses file permissions")
	}
	dir := t.TempDir()
	f := filepath.Join(dir, "noperm.txt")
	require.NoError(t, os.WriteFile(f, []byte("secret"), 0o644))
	require.NoError(t, os.Chmod(f, 0o000))
	t.Cleanup(func() { _ = os.Chmod(f, 0o644) })
	// Stat succeeds (dir traversable) but ReadFile fails (no read perm).
	_, err := executeRead(f, dir, nil, nil, "")
	require.Error(t, err)
}

func TestReadImage_ReadFileError(t *testing.T) {
	t.Parallel()
	_, err := readImage(filepath.Join(t.TempDir(), "ghost.png"), "ghost.png", "image/png")
	require.Error(t, err)
}

func TestReadImage_ResizeDimensionNote(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := filepath.Join(dir, "big.png")
	writePNG(t, p, 2100, 20) // wider than 2000 -> resized -> dimension note
	res, err := readImage(p, "big.png", "image/png")
	require.NoError(t, err)
	require.Contains(t, res.Content[0].Text, "Image: original")
}

func TestReadTextPartial_OpenError(t *testing.T) {
	t.Parallel()
	zero := 1
	_, err := readTextPartial(filepath.Join(t.TempDir(), "ghost.txt"), "ghost.txt", &zero, nil)
	require.Error(t, err)
}

func TestReadTextPartial_OffsetZeroClamps(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	f := filepath.Join(dir, "a.txt")
	require.NoError(t, os.WriteFile(f, []byte("one\ntwo\nthree\n"), 0o644))
	zero := 0
	one := 2
	res, err := readTextPartial(f, "a.txt", &zero, &one)
	require.NoError(t, err)
	require.Contains(t, res.Content[0].Text, "one")
}

func TestReadTextPartial_ScannerError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	f := filepath.Join(dir, "huge.txt")
	// A single line larger than the 1MB scanner buffer triggers scanner.Err.
	require.NoError(t, os.WriteFile(f, []byte(strings.Repeat("x", 2*1024*1024)), 0o644))
	off := 1
	lim := 1
	_, err := readTextPartial(f, "huge.txt", &off, &lim)
	require.Error(t, err)
}

func TestReadTool_PartialLineCapAndTruncation(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	f := filepath.Join(dir, "many.txt")
	var b strings.Builder
	for i := 0; i < 2100; i++ {
		b.WriteString("line\n")
	}
	require.NoError(t, os.WriteFile(f, []byte(b.String()), 0o644))
	tool := NewReadTool(dir)
	res, err := tool.Execute(context.Background(), "c", map[string]any{"path": "many.txt", "offset": float64(1)}, nil)
	require.NoError(t, err)
	require.Contains(t, res.Content[0].Text, "Use offset=")
}

func TestFormatPartialRead_FirstLineExceedsBytes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	f := filepath.Join(dir, "wide.txt")
	require.NoError(t, os.WriteFile(f, []byte(strings.Repeat("a", 60*1024)+"\n"), 0o644))
	tool := NewReadTool(dir)
	res, err := tool.Execute(context.Background(), "c", map[string]any{"path": "wide.txt", "offset": float64(1), "limit": float64(1)}, nil)
	require.NoError(t, err)
	require.Contains(t, res.Content[0].Text, "exceeds")
}

func TestFormatPartialRead_TruncatedByBytes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	f := filepath.Join(dir, "exact.txt")
	// First line == DefaultMaxBytes (not >), so byteCount = limit+1 > limit.
	require.NoError(t, os.WriteFile(f, []byte(strings.Repeat("a", DefaultMaxBytes)+"\nmore\n"), 0o644))
	tool := NewReadTool(dir)
	res, err := tool.Execute(context.Background(), "c", map[string]any{"path": "exact.txt", "offset": float64(1), "limit": float64(2)}, nil)
	require.NoError(t, err)
	require.Contains(t, res.Content[0].Text, "limit). Use offset=")
}

func TestFormatPartialRead_ByteBreakMidLoop(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	f := filepath.Join(dir, "chunks.txt")
	var b strings.Builder
	for i := 0; i < 6; i++ {
		b.WriteString(strings.Repeat("z", 20*1024))
		b.WriteString("\n")
	}
	require.NoError(t, os.WriteFile(f, []byte(b.String()), 0o644))
	tool := NewReadTool(dir)
	res, err := tool.Execute(context.Background(), "c", map[string]any{"path": "chunks.txt", "offset": float64(1), "limit": float64(10)}, nil)
	require.NoError(t, err)
	require.Contains(t, res.Content[0].Text, "Use offset=")
}

// applyReadFilters branches, reached via NewReadToolWithReader which calls it
// directly with caller-supplied offset/limit.
func TestApplyReadFilters_OffsetZeroClamps(t *testing.T) {
	t.Parallel()
	readFn := func(_ context.Context, _ string) (string, error) { return "a\nb\nc", nil }
	tool := NewReadToolWithReader(t.TempDir(), readFn)
	res, err := tool.Execute(context.Background(), "c", map[string]any{"path": "f.txt", "offset": float64(0), "limit": float64(2)}, nil)
	require.NoError(t, err)
	require.Contains(t, res.Content[0].Text, "a")
}

func TestApplyReadFilters_OffsetBeyondEOF(t *testing.T) {
	t.Parallel()
	readFn := func(_ context.Context, _ string) (string, error) { return "a\nb", nil }
	tool := NewReadToolWithReader(t.TempDir(), readFn)
	_, err := tool.Execute(context.Background(), "c", map[string]any{"path": "f.txt", "offset": float64(100)}, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "beyond end of file")
}

func TestApplyReadFilters_LimitClampsEndLine(t *testing.T) {
	t.Parallel()
	readFn := func(_ context.Context, _ string) (string, error) { return "a\nb\nc", nil }
	tool := NewReadToolWithReader(t.TempDir(), readFn)
	res, err := tool.Execute(context.Background(), "c", map[string]any{"path": "f.txt", "limit": float64(100)}, nil)
	require.NoError(t, err)
	require.Contains(t, res.Content[0].Text, "c")
}

func TestApplyReadFilters_FirstLineExceedsLimit(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("a", 60*1024)
	readFn := func(_ context.Context, _ string) (string, error) { return long + "\nmore", nil }
	tool := NewReadToolWithReader(t.TempDir(), readFn)
	res, err := tool.Execute(context.Background(), "c", map[string]any{"path": "f.txt"}, nil)
	require.NoError(t, err)
	require.Contains(t, res.Content[0].Text, "exceeds")
}

func TestApplyReadFilters_TruncatedByBytes(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	for i := 0; i < 6; i++ {
		b.WriteString(strings.Repeat("z", 20*1024))
		b.WriteString("\n")
	}
	content := b.String()
	readFn := func(_ context.Context, _ string) (string, error) { return content, nil }
	tool := NewReadToolWithReader(t.TempDir(), readFn)
	res, err := tool.Execute(context.Background(), "c", map[string]any{"path": "f.txt"}, nil)
	require.NoError(t, err)
	require.Contains(t, res.Content[0].Text, "limit). Use offset=")
}

func TestApplyReadFilters_UserLimitRemaining(t *testing.T) {
	t.Parallel()
	readFn := func(_ context.Context, _ string) (string, error) { return "a\nb\nc\nd\ne", nil }
	tool := NewReadToolWithReader(t.TempDir(), readFn)
	res, err := tool.Execute(context.Background(), "c", map[string]any{"path": "f.txt", "limit": float64(2)}, nil)
	require.NoError(t, err)
	require.Contains(t, res.Content[0].Text, "more lines in file")
}

func TestReadTool_CtxCancelled(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x"), 0o644))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	tool := NewReadTool(dir)
	_, err := tool.Execute(ctx, "c", map[string]any{"path": "a.txt"}, nil)
	require.Error(t, err)
}
