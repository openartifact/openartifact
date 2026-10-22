// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package deb

// StreamingGenerator generates Debian repository metadata on-the-fly from
// database queries, writing directly to an io.Writer without intermediate
// blob storage. This eliminates the metadata staleness window: clients always
// receive up-to-date metadata even before the background worker has run.
//
// Usage: call StreamPackages or StreamRelease from HTTP handlers when no
// pre-generated blob exists in RepoMetadata.

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"strings"

	db "github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/xzutil"
)

// Compression selects the output encoding for StreamPackages.
type Compression int

const (
	CompressionNone Compression = iota
	CompressionGzip
	CompressionXZ
)

// StreamPackages writes a Packages file for the given distribution/component/arch
// directly to w, applying the requested compression. Returns the number of bytes
// written and any error.
func StreamPackages(ctx context.Context, database db.Database, repoID, dist, comp, arch string, compression Compression, w io.Writer) (int64, error) {
	packages, err := database.ListDebPackageEntries(ctx, repoID, dist, comp, arch)
	if err != nil {
		return 0, fmt.Errorf("stream packages: query: %w", err)
	}

	// Build the Packages content in memory (it must be complete before compression).
	var sb strings.Builder

	for _, pkg := range packages {
		sb.WriteString(FormatPackagesEntry(pkg))
	}

	content := sb.String()

	switch compression {
	case CompressionGzip:
		cw := newCountingWriter(w)

		gz := gzip.NewWriter(cw)
		if _, err := io.WriteString(gz, content); err != nil {
			return cw.n, fmt.Errorf("stream packages: gzip write: %w", err)
		}

		if err := gz.Close(); err != nil {
			return cw.n, fmt.Errorf("stream packages: gzip close: %w", err)
		}

		return cw.n, nil

	case CompressionXZ:
		cw := newCountingWriter(w)

		xzw, err := xzutil.NewWriter(cw, len(content))
		if err != nil {
			return cw.n, fmt.Errorf("stream packages: xz writer: %w", err)
		}

		if _, err := io.WriteString(xzw, content); err != nil {
			return cw.n, fmt.Errorf("stream packages: xz write: %w", err)
		}

		if err := xzw.Close(); err != nil {
			return cw.n, fmt.Errorf("stream packages: xz close: %w", err)
		}

		return cw.n, nil

	default: // CompressionNone
		n, err := io.WriteString(w, content)
		return int64(n), err
	}
}

// StreamRelease writes a Release file for the given distribution directly to w.
// It queries all components and architectures from the database and generates
// the Release header. Checksum entries are omitted (no pre-computed blobs);
// clients that require checksums should use the pre-generated blob path.
func StreamRelease(ctx context.Context, database db.Database, repoID, dist string, w io.Writer) error {
	distributions, err := database.GetDebianDistributionsByRepo(ctx, repoID)
	if err != nil {
		return fmt.Errorf("stream release: query distributions: %w", err)
	}

	var distID string

	for _, d := range distributions {
		if d.Name == dist {
			distID = d.ID
			break
		}
	}

	if distID == "" {
		return fmt.Errorf("stream release: distribution %q not found", dist) //nolint:err113 // dynamic error with runtime context
	}

	components, err := database.GetDebianComponentsByDist(ctx, distID)
	if err != nil {
		return fmt.Errorf("stream release: query components: %w", err)
	}

	architectures, err := database.GetDebianArchitecturesByDist(ctx, distID)
	if err != nil {
		return fmt.Errorf("stream release: query architectures: %w", err)
	}

	gen := NewReleaseFileGenerator("openartifact", "openartifact", dist, dist)
	for _, c := range components {
		gen.AddComponent(c.Name)
	}

	for _, a := range architectures {
		gen.AddArchitecture(a.Name)
	}

	// Generate without file checksums (streaming path; checksums require pre-computed blobs).
	content := gen.GenerateRelease(nil)
	_, err = io.WriteString(w, content)

	return err
}

// countingWriter wraps an io.Writer and counts bytes written.
type countingWriter struct {
	w io.Writer
	n int64
}

func newCountingWriter(w io.Writer) *countingWriter {
	return &countingWriter{w: w}
}

func (cw *countingWriter) Write(p []byte) (int, error) {
	n, err := cw.w.Write(p)
	cw.n += int64(n)

	return n, err
}
