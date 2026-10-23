// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package pypi

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/openartifact/openartifact/internal/format"
	"github.com/openartifact/openartifact/internal/format/base"
)

const (
	maxPyPIPackageBytes  = 100 << 20
	maxPyPIMetadataBytes = 4 << 20
	// maxPyPIUnpackedBytes bounds the decompressed bytes read while scanning an
	// sdist for PKG-INFO, so a gzip bomb cannot burn CPU in tar.Next.
	maxPyPIUnpackedBytes = 256 << 20
)

type PyPIHandler struct{}

func NewPyPIHandler() *PyPIHandler {
	return &PyPIHandler{}
}

func (h *PyPIHandler) Name() string {
	return "pypi"
}

func (h *PyPIHandler) GetContentType() string {
	return "application/octet-stream"
}

func (h *PyPIHandler) GetFileExtension() string {
	return ".whl"
}

func (h *PyPIHandler) Validate(ctx context.Context, r io.Reader) error {
	// Try to validate as wheel (zip) or sdist (tar.gz)
	data, err := base.ReadAllLimited(r, maxPyPIPackageBytes)
	if err != nil {
		return fmt.Errorf("failed to read package: %w", err)
	}

	// Try as wheel (zip)
	if isValidWheel(data) {
		return nil
	}

	// Try as sdist (tar.gz)
	if isValidSdist(data) {
		return nil
	}

	return errors.New("invalid pypi package: not a valid wheel or sdist") //nolint:err113 // static error
}

func (h *PyPIHandler) Parse(ctx context.Context, r io.Reader) (map[string]any, error) {
	data, err := base.ReadAllLimited(r, maxPyPIPackageBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to read package: %w", err)
	}

	// Try as wheel first
	if isValidWheel(data) {
		return parseWheel(data)
	}

	// Try as sdist
	if isValidSdist(data) {
		return parseSdist(data)
	}

	return nil, errors.New("invalid pypi package: not a valid wheel or sdist") //nolint:err113 // static error
}

func isValidWheel(data []byte) bool {
	if len(data) < 4 {
		return false
	}
	// ZIP magic number: 0x50 0x4B 0x03 0x04
	return data[0] == 0x50 && data[1] == 0x4B && data[2] == 0x03 && data[3] == 0x04
}

func isValidSdist(data []byte) bool {
	if len(data) < 2 {
		return false
	}
	// gzip magic number: 0x1f 0x8b
	return data[0] == 0x1f && data[1] == 0x8b
}

func parseWheel(data []byte) (map[string]any, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("failed to read wheel: %w", err)
	}

	// Find METADATA file in dist-info directory
	for _, f := range zr.File {
		if strings.HasSuffix(f.Name, "/METADATA") {
			rc, err := f.Open()
			if err != nil {
				return nil, fmt.Errorf("failed to open METADATA: %w", err)
			}
			defer func() { _ = rc.Close() }()

			return parseMetadata(rc)
		}
	}

	return map[string]any{}, nil
}

func parseSdist(data []byte) (map[string]any, error) {
	gr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("failed to read gzip: %w", err)
	}
	defer func() { _ = gr.Close() }()

	tr := tar.NewReader(io.LimitReader(gr, maxPyPIUnpackedBytes))

	// Find PKG-INFO at root
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}

		if err != nil {
			return nil, fmt.Errorf("failed to read tar: %w", err)
		}

		if strings.HasSuffix(hdr.Name, "/PKG-INFO") || hdr.Name == "PKG-INFO" {
			return parseMetadata(tr)
		}
	}

	return map[string]any{}, nil
}

func parseMetadata(r io.Reader) (map[string]any, error) {
	result := make(map[string]any)

	// Simple metadata parsing - just read the content
	data, err := base.ReadAllLimited(r, maxPyPIMetadataBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to read metadata: %w", err)
	}

	// Parse basic fields from metadata
	lines := strings.SplitSeq(string(data), "\n")
	for line := range lines {
		if after, ok := strings.CutPrefix(line, "Name: "); ok {
			result["name"] = after
		} else if after, ok := strings.CutPrefix(line, "Version: "); ok {
			result["version"] = after
		} else if after, ok := strings.CutPrefix(line, "Summary: "); ok {
			result["summary"] = after
		} else if after, ok := strings.CutPrefix(line, "Author: "); ok {
			result["author"] = after
		} else if after, ok := strings.CutPrefix(line, "Author-Email: "); ok {
			result["author_email"] = after
		} else if after, ok := strings.CutPrefix(line, "License: "); ok {
			result["license"] = after
		} else if after, ok := strings.CutPrefix(line, "Keywords: "); ok {
			result["keywords"] = after
		} else if after, ok := strings.CutPrefix(line, "Requires-Python: "); ok {
			result["requires_python"] = after
		}
	}

	return result, nil
}

var _ format.FormatHandler = (*PyPIHandler)(nil)
