// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/vuln"
)

// scanPlan returns the package a scan looks up and the scanners that run for
// it. Only grype and trivy need more than the name and version of a deb or rpm: the
// distribution release (and an rpm's epoch and release), which are not part of
// the request. They are read from the stored package the artifact_id names, or
// from the version itself. When the release stays unknown, grype is replaced
// by osv (as is trivy), which needs no release, so the scan is neither refused nor reported
// clean on the strength of a database grype could not use.
func (h *VulnHandler) scanPlan(ctx context.Context, repo *models.Repository, artifactID string, pkg vuln.Package, scanners []string) (vuln.Package, []string, error) {
	if !vuln.NeedsDistro(pkg.Ecosystem) || !slices.ContainsFunc(scanners, func(n string) bool { return n == "grype" || n == "trivy" }) {
		return pkg, scanners, nil
	}

	pkg, err := h.withDistro(ctx, repo.ID, artifactID, pkg)
	if err != nil {
		return pkg, nil, err
	}

	routed := vuln.ScannersFor(scanners, pkg)
	if !slices.Equal(routed, scanners) {
		h.log.Infow("vuln scan: grype and trivy need the distribution of a deb or rpm and it is unknown or unsupported, scanning with osv instead",
			"repository", repo.Name,
			"ecosystem", pkg.Ecosystem,
			"package", pkg.Name,
			"version", pkg.Version,
			"scanners", strings.Join(routed, ","),
		)
	}

	return pkg, routed, nil
}

// withDistro adds the distribution release of a deb or rpm to pkg, and for an
// rpm its stored epoch and release.
func (h *VulnHandler) withDistro(ctx context.Context, repoID, artifactID string, pkg vuln.Package) (vuln.Package, error) {
	// Artifact ids are free-form; a stored package's id is a UUID.
	stored := isUUIDString(artifactID)

	switch strings.ToLower(pkg.Ecosystem) {
	case "deb":
		suite := ""

		if stored {
			var err error
			if suite, err = h.debSuite(ctx, repoID, artifactID, pkg); err != nil {
				return pkg, err
			}
		}

		pkg.Distro = vuln.DebDistro(pkg.Version, suite)
	case "rpm":
		if stored {
			c, err := h.db.GetComponentByID(ctx, artifactID)
			if err != nil {
				return pkg, fmt.Errorf("look up rpm package: %w", err)
			}

			// The listing shows an rpm's version without its release and epoch,
			// which the vulnerability match needs. Only the package the request
			// names is used.
			if c != nil && c.Format == "rpm" && c.RepositoryID == repoID && c.Name == pkg.Name && c.Version == pkg.Version {
				pkg.Release, pkg.Epoch = c.Release, c.Epoch
			}
		}

		pkg.Distro = vuln.RPMDistro(pkg.Version, pkg.Release)
	}

	return pkg, nil
}

// debSuite returns the name of the repository distribution ("jammy",
// "bookworm") the stored deb package is served from, or "" when the artifact is
// not a deb package of the repository with the requested name and version.
func (h *VulnHandler) debSuite(ctx context.Context, repoID, artifactID string, pkg vuln.Package) (string, error) {
	dp, err := h.db.GetDebPackageByID(ctx, artifactID)
	if err != nil {
		return "", fmt.Errorf("look up deb package: %w", err)
	}

	if dp == nil || dp.RepositoryID != repoID || dp.Name != pkg.Name || dp.Version != pkg.Version {
		return "", nil
	}

	dists, err := h.db.GetDebianDistributionsByRepo(ctx, repoID)
	if err != nil {
		return "", fmt.Errorf("list deb distributions: %w", err)
	}

	for _, d := range dists {
		if d.ID == dp.DistributionID {
			return d.Name, nil
		}
	}

	return "", nil
}
