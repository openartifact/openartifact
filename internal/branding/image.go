// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package branding

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image/png"
	"io"
	"mime"
	"os"
	"strings"
)

const (
	// MaxImageBytes caps an uploaded or configured image (before sanitising).
	MaxImageBytes = 512 << 10
	// maxPNGDimension caps the pixel size of a PNG; the decoder allocates the
	// full bitmap, so the cap bounds memory.
	maxPNGDimension = 2048

	// ContentTypePNG and ContentTypeSVG are the only image types accepted.
	ContentTypePNG = "image/png"
	ContentTypeSVG = "image/svg+xml"
)

//nolint:gochecknoglobals // immutable magic number
var pngSignature = []byte("\x89PNG\r\n\x1a\n")

// Kind names one of the three brandable images.
type Kind string

// The brandable images.
const (
	LogoLight Kind = "logo-light"
	LogoDark  Kind = "logo-dark"
	Favicon   Kind = "favicon"
)

// Kinds lists every brandable image in a stable order.
func Kinds() []Kind { return []Kind{LogoLight, LogoDark, Favicon} }

// ParseKind maps a URL path segment to a Kind.
func ParseKind(s string) (Kind, bool) {
	for _, k := range Kinds() {
		if string(k) == s {
			return k, true
		}
	}

	return "", false
}

// Image is a validated, sanitised brand image.
type Image struct {
	ContentType string
	Data        []byte
	// SHA256 is the lower-case hex digest of Data.
	SHA256 string
	// Width and Height are the pixel size of a PNG; zero for SVG.
	Width, Height int
}

// ErrTooLarge reports an image over MaxImageBytes.
var ErrTooLarge = fmt.Errorf("%w: image is larger than %d KiB", ErrInvalid, MaxImageBytes>>10)

// NewImage validates and sanitises body. declared is the Content-Type the
// client sent; an empty value means "detect". The type is always decided by
// the bytes, and a declared type that disagrees is rejected. PNGs are decoded
// and re-encoded (dropping every ancillary chunk), SVGs are rewritten from an
// allowlist (see sanitizeSVG).
func NewImage(declared string, body []byte) (*Image, error) {
	if len(body) > MaxImageBytes {
		return nil, ErrTooLarge
	}

	if len(body) == 0 {
		return nil, invalidf("image is empty")
	}

	if declared != "" {
		mt, _, err := mime.ParseMediaType(declared)
		if err != nil || (mt != ContentTypePNG && mt != ContentTypeSVG) {
			return nil, invalidf("only PNG (image/png) and SVG (image/svg+xml) images are accepted")
		}

		declared = mt
	}

	if bytes.HasPrefix(body, pngSignature) {
		if declared != "" && declared != ContentTypePNG {
			return nil, invalidf("the image is a PNG but was sent as %s", declared)
		}

		return newPNG(body)
	}

	if declared == ContentTypePNG {
		return nil, invalidf("the image is not a valid PNG")
	}

	clean, err := sanitizeSVG(body)
	if err != nil {
		return nil, err
	}

	return finishImage(ContentTypeSVG, clean, 0, 0), nil
}

func newPNG(body []byte) (*Image, error) {
	cfg, err := png.DecodeConfig(bytes.NewReader(body))
	if err != nil {
		return nil, invalidf("the image is not a valid PNG")
	}

	if cfg.Width < 1 || cfg.Height < 1 || cfg.Width > maxPNGDimension || cfg.Height > maxPNGDimension {
		return nil, invalidf("PNG dimensions must be between 1 and %d pixels", maxPNGDimension)
	}

	img, err := png.Decode(bytes.NewReader(body))
	if err != nil {
		return nil, invalidf("the image is not a valid PNG")
	}

	var out bytes.Buffer

	enc := png.Encoder{CompressionLevel: png.BestCompression}
	if err := enc.Encode(&out, img); err != nil {
		return nil, invalidf("the image could not be re-encoded")
	}

	return finishImage(ContentTypePNG, out.Bytes(), cfg.Width, cfg.Height), nil
}

func finishImage(ctype string, data []byte, w, h int) *Image {
	sum := sha256.Sum256(data)

	return &Image{ContentType: ctype, Data: data, SHA256: hex.EncodeToString(sum[:]), Width: w, Height: h}
}

// LoadImageFile reads and sanitises an image from the filesystem (the
// branding.logoLight/logoDark/favicon configuration). The type is detected
// from the content, not the extension.
func LoadImageFile(path string) (*Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open branding image: %w", err)
	}
	defer func() { _ = f.Close() }()

	body, err := io.ReadAll(io.LimitReader(f, MaxImageBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read branding image: %w", err)
	}

	img, err := NewImage("", body)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}

	return img, nil
}

// restoreImage rebuilds an Image from stored, already sanitised bytes without
// sanitising them again; a stored blob that does not look like one of the two
// accepted types is dropped.
func restoreImage(ctype string, data []byte) (*Image, bool) {
	switch ctype {
	case ContentTypePNG:
		cfg, err := png.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			return nil, false
		}

		return finishImage(ctype, data, cfg.Width, cfg.Height), true
	case ContentTypeSVG:
		if !strings.HasPrefix(string(data), "<svg") {
			return nil, false
		}

		return finishImage(ctype, data, 0, 0), true
	}

	return nil, false
}
