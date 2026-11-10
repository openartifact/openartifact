// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package deb

// Debian repository metadata generated on-the-fly from database queries,
// written directly to an io.Writer without intermediate blob storage. This
// eliminates the metadata staleness window: clients always receive up-to-date
// metadata even before the background worker has run.
//
// Usage: call StreamPackages or StreamRelease from HTTP handlers when no
// pre-generated blob exists in RepoMetadata.

import (
	"context"
	"fmt"
	"io"
	"time"

	pkgdeb "github.com/M0Rf30/pkgformat/deb"
	"github.com/openartifact/openartifact/internal/database/models"
)

// StreamPackagesStore is the part of the database StreamPackages needs.
type StreamPackagesStore interface {
	ListDebPackageEntries(ctx context.Context, repoID string, dist string, comp string, arch string) ([]models.DebPackageEntry, error)
}

// StreamPackages writes a Packages file for the given distribution/component/arch
// directly to w, applying the requested compression. Returns the number of bytes
// written and any error.
func StreamPackages(
	ctx context.Context,
	database StreamPackagesStore,
	repoID, dist, comp, arch string,
	compression pkgdeb.Compression,
	w io.Writer,
) (int64, error) {
	packages, err := database.ListDebPackageEntries(ctx, repoID, dist, comp, arch)
	if err != nil {
		return 0, fmt.Errorf("stream packages: query: %w", err)
	}

	var index pkgdeb.Index

	for _, pkg := range packages {
		index.Add(IndexEntry(pkg))
	}

	n, err := index.WriteTo(w, compression)
	if err != nil {
		return n, fmt.Errorf("stream packages: %w", err)
	}

	return n, nil
}

// StreamReleaseStore is the part of the database StreamRelease needs.
type StreamReleaseStore interface {
	GetDebianArchitecturesByDist(ctx context.Context, distID string) ([]models.DebianArchitecture, error)
	GetDebianComponentsByDist(ctx context.Context, distID string) ([]models.DebianComponent, error)
	GetDebianDistributionsByRepo(ctx context.Context, repoID string) ([]models.DebianDistribution, error)
}

// StreamRelease writes a Release file for the given distribution directly to w.
// It queries all components and architectures from the database and generates
// the Release header. Checksum entries are omitted (no pre-computed blobs);
// clients that require checksums should use the pre-generated blob path.
func StreamRelease(ctx context.Context, database StreamReleaseStore, repoID, dist string, w io.Writer) error {
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

	release := NewRelease(dist)
	for _, c := range components {
		release.Components = append(release.Components, c.Name)
	}

	for _, a := range architectures {
		release.Architectures = append(release.Architectures, a.Name)
	}

	_, err = w.Write(release.Marshal())

	return err
}

// NewRelease starts the Release of a distribution the repository publishes. No
// Valid-Until is set: metadata is only regenerated on upload, so an expiry
// would make apt reject an idle repository.
func NewRelease(dist string) *pkgdeb.Release {
	return &pkgdeb.Release{
		Origin:   "openartifact",
		Label:    "openartifact",
		Suite:    dist,
		Codename: dist,
		Date:     time.Now().UTC(),
	}
}
