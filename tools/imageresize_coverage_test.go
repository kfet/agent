package tools

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"math/rand"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWithDefaults_AllOverrides(t *testing.T) {
	t.Parallel()
	opts := (&ResizeImageOptions{MaxWidth: 100, MaxHeight: 200, MaxBytes: 300, JPEGQuality: 90}).withDefaults()
	require.Equal(t, 100, opts.MaxWidth)
	require.Equal(t, 200, opts.MaxHeight)
	require.Equal(t, 300, opts.MaxBytes)
	require.Equal(t, 90, opts.JPEGQuality)
}

func encodePNGb64(t *testing.T, w, h int, noise bool) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	rng := rand.New(rand.NewSource(1))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if noise {
				img.Set(x, y, color.RGBA{uint8(rng.Intn(256)), uint8(rng.Intn(256)), uint8(rng.Intn(256)), 255})
			} else {
				img.Set(x, y, color.RGBA{uint8(x), uint8(y), uint8(x + y), 255})
			}
		}
	}
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func TestResizeImage_TallImage(t *testing.T) {
	t.Parallel()
	b64 := encodePNGb64(t, 20, 2100, false) // taller than MaxHeight(2000)
	res := ResizeImage(b64, "image/png", nil)
	require.True(t, res.WasResized)
	require.LessOrEqual(t, res.Height, 2000)
}

func TestEncodeBest_PngEncodeError(t *testing.T) {
	t.Parallel()
	// png.Encode rejects zero-size images.
	_, _, err := encodeBest(image.NewRGBA(image.Rect(0, 0, 0, 0)), 80)
	require.Error(t, err)
	require.Contains(t, err.Error(), "png encode")
}

func TestEncodeBest_JpegEncodeError(t *testing.T) {
	t.Parallel()
	// jpeg.Encode rejects images with a dimension over 65535; png handles it.
	_, _, err := encodeBest(image.NewRGBA(image.Rect(0, 0, 65536, 1)), 80)
	require.Error(t, err)
	require.Contains(t, err.Error(), "jpeg encode")
}

func TestResizeImage_LastResortEncodeError_ReturnsOriginal(t *testing.T) {
	t.Parallel()
	// A 1x2100 image scales to targetW=1; the loop breaks immediately
	// (w < 100) and the last-resort resize collapses to width 0, which
	// png.Encode rejects. The original image must come back unchanged.
	b64 := encodePNGb64(t, 1, 2100, false)
	res := ResizeImage(b64, "image/png", nil)
	require.False(t, res.WasResized)
	require.Equal(t, b64, res.Data)
	require.Equal(t, 1, res.OriginalWidth)
	require.Equal(t, 2100, res.OriginalHeight)
}

func TestEncodeBest_JpegSmallerThanPng(t *testing.T) {
	t.Parallel()
	// A large noisy image compresses far better as JPEG than PNG, so encodeBest
	// returns the JPEG result.
	img := image.NewRGBA(image.Rect(0, 0, 200, 200))
	rng := rand.New(rand.NewSource(7))
	for y := 0; y < 200; y++ {
		for x := 0; x < 200; x++ {
			img.Set(x, y, color.RGBA{uint8(rng.Intn(256)), uint8(rng.Intn(256)), uint8(rng.Intn(256)), 255})
		}
	}
	_, mime, err := encodeBest(img, 40)
	require.NoError(t, err)
	require.Equal(t, "image/jpeg", mime)
}
