// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package base

import (
	"net/http"

	"github.com/openartifact/openartifact/internal/httpcompress"
)

// Compress wraps a metadata handler so its text response (JSON, YAML, XML,
// HTML, plain text) is zstd- or gzip-encoded when the client's Accept-Encoding
// allows it; see httpcompress.Middleware for the exact rules (200 responses of
// a textual type that are not already encoded, ranged or downloads; bodies
// below httpcompress.MinSize are left alone). Only GET requests are encoded.
// Apply it to metadata routes only, never to artifact downloads.
func Compress(next http.HandlerFunc) http.HandlerFunc {
	return httpcompress.Func(next)
}
