// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package branding

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testPNG(t *testing.T, w, h int) []byte {
	t.Helper()

	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for x := range w {
		img.Set(x, 0, color.NRGBA{R: 200, A: 255})
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}

	return buf.Bytes()
}

const cleanSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><circle cx="5" cy="5" r="4"/></svg>`

func TestNewImage_Accepts(t *testing.T) {
	big := testPNG(t, 64, 32)

	img, err := NewImage("image/png", big)
	if err != nil || img.ContentType != ContentTypePNG || img.Width != 64 || img.Height != 32 || len(img.SHA256) != 64 {
		t.Fatalf("png: %+v, %v", img, err)
	}

	// Detection without a declared type, and a charset parameter.
	if _, err := NewImage("", big); err != nil {
		t.Errorf("png detect: %v", err)
	}

	img, err = NewImage("image/svg+xml; charset=utf-8", []byte(cleanSVG))
	if err != nil || img.ContentType != ContentTypeSVG || img.Width != 0 {
		t.Fatalf("svg: %+v, %v", img, err)
	}

	if _, err := NewImage("", []byte(cleanSVG)); err != nil {
		t.Errorf("svg detect: %v", err)
	}
}

func TestNewImage_PNGMetadataStripped(t *testing.T) {
	src := testPNG(t, 4, 4)
	// Splice a tEXt chunk after IHDR (offset 8 signature + 25 IHDR chunk).
	text := []byte{0, 0, 0, 9, 't', 'E', 'X', 't', 'k', 0, 's', 'e', 'c', 'r', 'e', 't', 0, 0, 0, 0}
	spliced := append(append(append([]byte{}, src[:33]...), text...), src[33:]...)

	img, err := NewImage("", spliced)
	if err != nil {
		// The hand-made chunk has an invalid CRC; the decoder may refuse it, which is also safe.
		return
	}

	if bytes.Contains(img.Data, []byte("tEXt")) {
		t.Error("ancillary chunk survived re-encoding")
	}
}

func TestNewImage_Rejects(t *testing.T) {
	tests := []struct {
		name     string
		declared string
		body     []byte
		tooLarge bool
	}{
		{"empty", "image/png", nil, false},
		{"gif", "image/gif", []byte("GIF89a"), false},
		{"jpeg declared", "image/jpeg", []byte("\xff\xd8\xff"), false},
		{"ico", "", []byte("\x00\x00\x01\x00\x01\x00"), false},
		{"html as svg", "image/svg+xml", []byte("<html><script>alert(1)</script></html>"), false},
		{"svg declared as png", "image/png", []byte(cleanSVG), false},
		{"png declared as svg", "image/svg+xml", testPNG(t, 2, 2), false},
		{"truncated png", "image/png", testPNG(t, 8, 8)[:40], false},
		{"png signature only", "", []byte("\x89PNG\r\n\x1a\n"), false},
		{"png too many pixels", "image/png", testPNG(t, maxPNGDimension+1, 1), false},
		{"oversize", "image/png", bytes.Repeat([]byte("a"), MaxImageBytes+1), true},
		{"script svg", "image/svg+xml", []byte(`<svg xmlns="http://www.w3.org/2000/svg"><style>@import 'x';</style></svg>`), false},
		{"garbage", "", []byte("not an image at all"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewImage(tt.declared, tt.body)
			if err == nil {
				t.Fatal("accepted")
			}

			if !errors.Is(err, ErrInvalid) {
				t.Errorf("error does not wrap ErrInvalid: %v", err)
			}

			if tt.tooLarge != errors.Is(err, ErrTooLarge) {
				t.Errorf("ErrTooLarge = %v, want %v", errors.Is(err, ErrTooLarge), tt.tooLarge)
			}
		})
	}
}

func TestLoadImageFile(t *testing.T) {
	dir := t.TempDir()

	// The extension is irrelevant: the content decides.
	p := filepath.Join(dir, "logo.dat")
	if err := os.WriteFile(p, []byte(strings.ReplaceAll(cleanSVG, "<circle", "<script>x</script><circle")), 0o600); err != nil {
		t.Fatal(err)
	}

	img, err := LoadImageFile(p)
	if err != nil || strings.Contains(string(img.Data), "script") {
		t.Fatalf("LoadImageFile = %v, %v", img, err)
	}

	bad := filepath.Join(dir, "bad.svg")
	if err := os.WriteFile(bad, []byte("<html/>"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadImageFile(bad); err == nil || !strings.Contains(err.Error(), "bad.svg") {
		t.Errorf("bad file error = %v", err)
	}

	huge := filepath.Join(dir, "huge.png")
	if err := os.WriteFile(huge, bytes.Repeat([]byte("a"), MaxImageBytes+10), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadImageFile(huge); !errors.Is(err, ErrTooLarge) {
		t.Errorf("huge file error = %v, want ErrTooLarge", err)
	}
}
