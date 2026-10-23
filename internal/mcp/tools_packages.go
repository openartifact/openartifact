// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package mcp

import (
	"cmp"
	"context"
	"net/url"
	"slices"
	"strings"

	"github.com/openartifact/openartifact/internal/clientsetup"
	"github.com/openartifact/openartifact/internal/database/models"
)

const (
	// getPackageScanPage and maxGetPackageScanPages bound the repository scan
	// get_package does to collect every version of one package name.
	getPackageScanPage     = 100
	maxGetPackageScanPages = 50
	maxPackageVersions     = 200

	maxFindings       = 100
	maxFindingRefs    = 5
	maxFindingSummary = 500
)

func (s *Server) listPackages(ctx context.Context, in listPackagesIn) (packageListResult, error) {
	repo, err := s.resolveRepo(ctx, in.Repository)
	if err != nil {
		return packageListResult{}, err
	}

	page := clampInt(in.Page, 1, 1, maxPage)
	limit := clampInt(in.Limit, defaultPageSize, 1, maxPageSize)

	components, total, err := s.db.ListComponentsByRepo(ctx, repo.ID, in.Format, page, limit)
	if err != nil {
		return packageListResult{}, s.internalError("list packages", err, "repository", repo.Name)
	}

	items := make([]packageDTO, 0, len(components))
	for _, c := range components {
		items = append(items, toPackageDTO(c))
	}

	return packageListResult{provenance: newProvenance(), Repository: repo.Name, Total: total, Page: page, Limit: limit, Packages: items}, nil
}

func (s *Server) getPackage(ctx context.Context, in getPackageIn) (packageDetailResult, error) {
	repo, err := s.resolveRepo(ctx, in.Repository)
	if err != nil {
		return packageDetailResult{}, err
	}

	matches, truncated, err := s.findPackageVersions(ctx, repo, in.Name)
	if err != nil {
		return packageDetailResult{}, s.internalError("get package", err, "repository", repo.Name, "package", in.Name)
	}

	if len(matches) == 0 {
		return packageDetailResult{}, errPackageNotFound
	}

	versions := make([]packageVersionDTO, 0, len(matches))
	for _, c := range matches {
		versions = append(versions, packageVersionDTO{
			ID:          c.ID,
			Version:     c.Version,
			Arch:        c.Arch,
			Filename:    c.Filename,
			SHA256:      c.SHA256,
			Size:        c.PackageSize,
			CreatedAt:   formatTime(c.CreatedAt),
			DownloadURL: s.downloadURL(repo.Name, c.ID),
		})
	}

	return packageDetailResult{
		provenance: newProvenance(),
		Repository: repo.Name,
		Format:     matches[0].Format,
		Name:       in.Name,
		Versions:   versions,
		Truncated:  truncated,
	}, nil
}

// findPackageVersions scans the repository listing for components named name.
// The listing has no name filter, so the scan is bounded; truncated reports
// that a bound was hit and results may be incomplete.
func (s *Server) findPackageVersions(ctx context.Context, repo *models.Repository, name string) ([]*models.Component, bool, error) {
	var matches []*models.Component

	for page := 1; page <= maxGetPackageScanPages; page++ {
		components, total, err := s.db.ListComponentsByRepo(ctx, repo.ID, "", page, getPackageScanPage)
		if err != nil {
			return nil, false, err //nolint:wrapcheck // logged by the caller with context
		}

		for _, c := range components {
			if c.Name != name {
				continue
			}

			if len(matches) >= maxPackageVersions {
				return matches, true, nil
			}

			matches = append(matches, c)
		}

		if len(components) == 0 || page*getPackageScanPage >= total {
			return matches, false, nil
		}
	}

	return matches, true, nil
}

// downloadURL builds the REST download URL of a package; absolute when a base
// URL is configured.
func (s *Server) downloadURL(repoName, id string) string {
	return s.baseURL + "/api/v1/repositories/" + url.PathEscape(repoName) + "/packages/" + url.PathEscape(id) + "/download"
}

func (s *Server) getVulnerabilities(ctx context.Context, in vulnerabilitiesIn) (vulnerabilitiesResult, error) {
	// Same gate as REST GET /api/v1/repositories/{name}/vuln/scans: the global
	// `manage` permission (evaluated with no repository, like principalIsAdmin).
	if !s.allowed(ctx, "", "manage") {
		return vulnerabilitiesResult{}, errAdminRequired
	}

	repo, err := s.resolveRepo(ctx, in.Repository)
	if err != nil {
		return vulnerabilitiesResult{}, err
	}

	scans, err := s.db.ListVulnScanResults(ctx, repo.ID, false)
	if err != nil {
		return vulnerabilitiesResult{}, s.internalError("list vuln scans", err, "repository", repo.Name)
	}

	res := summarizeVulns(repo.Name, in.Package, in.Version, scans)
	res.provenance = newProvenance()

	return res, nil
}

// summarizeVulns aggregates scan results matching the optional package and
// version filters into severity counts and a capped, severity-ordered list of
// findings. Scanner error text is never included.
func summarizeVulns(repoName, pkg, version string, scans []*models.VulnScanResult) vulnerabilitiesResult {
	res := vulnerabilitiesResult{
		Repository:    repoName,
		PackageFilter: pkg,
		VersionFilter: version,
		Findings:      []findingDTO{},
	}

	var findings []findingDTO

	for _, scan := range scans {
		if scan == nil || (pkg != "" && scan.PackageName != pkg) || (version != "" && scan.PackageVersion != version) {
			continue
		}

		switch scan.ScanStatus {
		case "completed":
			res.ScannedPackages++
		case "pending", "scanning":
			res.PendingScans++
		case "failed", "partial":
			// A partial scan lacks the findings of the scanners that failed, so
			// it is not a clean "no vulnerabilities" result; its own findings
			// are still counted below.
			res.FailedScans++
		}

		if len(scan.Vulnerabilities) > 0 {
			res.VulnerablePackages++
		}

		for _, v := range scan.Vulnerabilities {
			res.Totals.add(v.Severity)
			findings = append(findings, toFindingDTO(scan, v))
		}
	}

	slices.SortStableFunc(findings, func(a, b findingDTO) int {
		if c := cmp.Compare(severityRank(b.Severity), severityRank(a.Severity)); c != 0 {
			return c
		}

		return cmp.Compare(b.CVSS, a.CVSS)
	})

	if len(findings) > maxFindings {
		findings = findings[:maxFindings]
		res.FindingsTruncated = true
	}

	if findings != nil {
		res.Findings = findings
	}

	return res
}

func (c *severityCounts) add(severity string) {
	switch strings.ToUpper(severity) {
	case "CRITICAL":
		c.Critical++
	case "HIGH":
		c.High++
	case "MEDIUM", "MODERATE":
		c.Medium++
	case "LOW":
		c.Low++
	default:
		c.Unknown++
	}
}

func severityRank(severity string) int {
	switch strings.ToUpper(severity) {
	case "CRITICAL":
		return 4
	case "HIGH":
		return 3
	case "MEDIUM", "MODERATE":
		return 2
	case "LOW":
		return 1
	default:
		return 0
	}
}

func toFindingDTO(scan *models.VulnScanResult, v models.Vulnerability) findingDTO {
	refs := v.References
	if len(refs) > maxFindingRefs {
		refs = refs[:maxFindingRefs]
	}

	if refs == nil {
		refs = []string{}
	}

	summary := truncateText(v.Summary, maxFindingSummary)

	return findingDTO{
		PackageName:    scan.PackageName,
		PackageVersion: scan.PackageVersion,
		ID:             v.ID,
		Severity:       strings.ToUpper(v.Severity),
		CVSS:           v.CVSS,
		Summary:        summary,
		FixedIn:        v.FixedIn,
		References:     refs,
	}
}

func (s *Server) clientSetup(ctx context.Context, in clientSetupIn) (clientSetupResult, error) {
	repo, err := s.resolveRepo(ctx, in.Repository)
	if err != nil {
		return clientSetupResult{}, err
	}

	return clientSetupResult{
		Repository: repo.Name,
		Format:     repo.Format,
		BaseURL:    clientsetup.RepoBaseURL(repo.Format, s.baseURL, repo.Name),
		Clients:    clientsetup.Snippets(repo.Format, s.baseURL, repo.Name),
	}, nil
}
