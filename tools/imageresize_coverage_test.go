package tools

import (
	"bytes"
	"encoding/base64"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"math/rand"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWithDefaults_AllOverrides(t *testing.T) {
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
	b64 := encodePNGb64(t, 20, 2100, false) // taller than MaxHeight(2000)
	res := ResizeImage(b64, "image/png", nil)
	require.True(t, res.WasResized)
	require.LessOrEqual(t, res.Height, 2000)
}

func TestEncodeBest_PngEncodeError(t *testing.T) {
	orig := pngEncode
	t.Cleanup(func() { pngEncode = orig })
	pngEncode = func(_ io.Writer, _ image.Image) error { return errors.New("png boom") }
	_, _, err := encodeBest(image.NewRGBA(image.Rect(0, 0, 4, 4)), 80)
	require.Error(t, err)
	require.Contains(t, err.Error(), "png encode")
}

func TestEncodeBest_JpegEncodeError(t *testing.T) {
	orig := jpegEncode
	t.Cleanup(func() { jpegEncode = orig })
	jpegEncode = func(_ io.Writer, _ image.Image, _ *jpeg.Options) error { return errors.New("jpeg boom") }
	_, _, err := encodeBest(image.NewRGBA(image.Rect(0, 0, 4, 4)), 80)
	require.Error(t, err)
	require.Contains(t, err.Error(), "jpeg encode")
}

func TestResizeImage_EncodeErrorContinues(t *testing.T) {
	orig := pngEncode
	t.Cleanup(func() { pngEncode = orig })
	pngEncode = func(_ io.Writer, _ image.Image) error { return errors.New("png boom") }
	// Oversized square image enters the resize loop with both target dims
	// >= 100; encodeBest errors each iteration -> continue -> last-resort.
	// Small image + custom limits exercise the same path far faster than
	// a 2100x2100 image against the 2000px defaults.
	b64 := encodePNGb64(t, 450, 450, false)
	res := ResizeImage(b64, "image/png", &ResizeImageOptions{MaxWidth: 400, MaxHeight: 400})
	require.True(t, res.WasResized)
}

func TestEncodeBest_JpegSmallerThanPng(t *testing.T) {
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
