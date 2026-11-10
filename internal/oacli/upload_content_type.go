// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package oacli

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"strings"
)

// rawContentTypes is the content type of a raw file by extension: the types Go's mime
// package knows without the system tables, fixed here so the same file gets the same
// type on every machine (system tables differ, and some map .md to text/markdown,
// which a raw repository serves as a download rather than inline).
//
//nolint:gochecknoglobals // immutable lookup table
var rawContentTypes = map[string]string{
	".avif": "image/avif",
	".css":  "text/css; charset=utf-8",
	".gif":  "image/gif",
	".htm":  "text/html; charset=utf-8",
	".html": "text/html; charset=utf-8",
	".jpeg": "image/jpeg",
	".jpg":  "image/jpeg",
	".js":   "text/javascript; charset=utf-8",
	".json": "application/json",
	".mjs":  "text/javascript; charset=utf-8",
	".pdf":  "application/pdf",
	".png":  "image/png",
	".svg":  "image/svg+xml",
	".wasm": "application/wasm",
	".webp": "image/webp",
	".xml":  "text/xml; charset=utf-8",
}

// rawContentType is the Content-Type oa upload sends for a raw file: by the extension
// of its repository path for the types above, else detected from its first 512 bytes
// exactly as the server does for an upload without one. file is left at its start.
func rawContentType(file *os.File, remote string) (string, error) {
	if ct, ok := rawContentTypes[strings.ToLower(path.Ext(remote))]; ok {
		return ct, nil
	}

	buf := make([]byte, 512)

	n, err := io.ReadFull(file, buf)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return "", fmt.Errorf("reading file: %w", err)
	}

	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", fmt.Errorf("rewinding file: %w", err)
	}

	if n == 0 {
		return "application/octet-stream", nil
	}

	return http.DetectContentType(buf[:n]), nil
}
