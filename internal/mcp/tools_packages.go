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

	"github.com/mark3labs/mcp-go/mcp"
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

func (s *Server) registerPackageTools() {
	s.mcpServer.AddTool(
		mcp.NewTool("list_packages",
			mcp.WithDescription("List the packages stored in a repository (any format), newest first. Returns name, version, format, architecture, size, sha256, filename and creation time."),
			mcp.WithString("repository", mcp.Required(), mcp.Description("Repository name")),
			mcp.WithString("format", mcp.Description("Only list packages of this format (e.g. npm, deb, docker). Leave empty for all.")),
			mcp.WithNumber("page", mcp.Description("Page number (1-based, default 1)"), mcp.DefaultNumber(1)),
			mcp.WithNumber("limit", mcp.Description("Results per page (default 20, max 100)"), mcp.DefaultNumber(defaultPageSize)),
		),
		s.handleListPackages,
	)

	s.mcpServer.AddTool(
		mcp.NewTool("get_package",
			mcp.WithDescription("Get every version of a package in a repository, with filename, sha256, size, creation time and a download URL."),
			mcp.WithString("repository", mcp.Required(), mcp.Description("Repository name")),
			mcp.WithString("name", mcp.Required(), mcp.Description("Exact package name as returned by list_packages")),
		),
		s.handleGetPackage,
	)

	s.mcpServer.AddTool(
		mcp.NewTool("get_vulnerabilities",
			mcp.WithDescription("Summarize known vulnerabilities from stored scan results for a repository: severity counts plus the most severe findings (capped at 100). Requires administrator (global manage) permission, like the REST vulnerability scan endpoints."),
			mcp.WithString("repository", mcp.Required(), mcp.Description("Repository name")),
			mcp.WithString("package", mcp.Description("Only include this package name")),
			mcp.WithString("version", mcp.Description("Only include this package version")),
		),
		s.handleGetVulnerabilities,
	)

	s.mcpServer.AddTool(
		mcp.NewTool("client_setup",
			mcp.WithDescription("Get the commands to configure and publish with the native client tools for a repository (npm, docker, pip, mvn, apt, ...). Credentials are placeholders: <USERNAME> and <TOKEN>."),
			mcp.WithString("repository", mcp.Required(), mcp.Description("Repository name")),
		),
		s.handleClientSetup,
	)
}

func (s *Server) handleListPackages(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	name, err := req.RequireString("repository")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	repo, denied := s.resolveRepo(ctx, name)
	if denied != nil {
		return denied, nil
	}

	format := req.GetString("format", "")
	page := intArg(req, "page", 1, 1, maxPage)
	limit := intArg(req, "limit", defaultPageSize, 1, maxPageSize)

	components, total, err := s.db.ListComponentsByRepo(ctx, repo.ID, format, page, limit)
	if err != nil {
		return s.internalError("list packages", err, "repository", repo.Name), nil
	}

	items := make([]packageDTO, 0, len(components))
	for _, c := range components {
		items = append(items, toPackageDTO(c))
	}

	return jsonResult(packageListResult{Repository: repo.Name, Total: total, Page: page, Limit: limit, Packages: items})
}

func (s *Server) handleGetPackage(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	repoName, err := req.RequireString("repository")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	pkgName, err := req.RequireString("name")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	repo, denied := s.resolveRepo(ctx, repoName)
	if denied != nil {
		return denied, nil
	}

	matches, truncated, err := s.findPackageVersions(ctx, repo, pkgName)
	if err != nil {
		return s.internalError("get package", err, "repository", repo.Name, "package", pkgName), nil
	}

	if len(matches) == 0 {
		return mcp.NewToolResultError("package not found"), nil
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

	return jsonResult(packageDetailResult{
		Repository: repo.Name,
		Format:     matches[0].Format,
		Name:       pkgName,
		Versions:   versions,
		Truncated:  truncated,
	})
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

func (s *Server) handleGetVulnerabilities(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	repoName, err := req.RequireString("repository")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	// Same gate as REST GET /api/v1/repositories/{name}/vuln/scans: the global
	// `manage` permission (evaluated with no repository, like principalIsAdmin).
	if !s.allowed(ctx, "", "manage") {
		return mcp.NewToolResultError(msgAdminRequired), nil
	}

	repo, denied := s.resolveRepo(ctx, repoName)
	if denied != nil {
		return denied, nil
	}

	pkg := req.GetString("package", "")
	version := req.GetString("version", "")

	scans, err := s.db.ListVulnScanResults(ctx, repo.ID, false)
	if err != nil {
		return s.internalError("list vuln scans", err, "repository", repo.Name), nil
	}

	return jsonResult(summarizeVulns(repo.Name, pkg, version, scans))
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

	summary := v.Summary
	if len(summary) > maxFindingSummary {
		summary = summary[:maxFindingSummary]
	}

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

func (s *Server) handleClientSetup(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	name, err := req.RequireString("repository")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	repo, denied := s.resolveRepo(ctx, name)
	if denied != nil {
		return denied, nil
	}

	return jsonResult(clientSetupResult{
		Repository: repo.Name,
		Format:     repo.Format,
		BaseURL:    clientsetup.RepoBaseURL(repo.Format, s.baseURL, repo.Name),
		Clients:    clientsetup.Snippets(repo.Format, s.baseURL, repo.Name),
	})
}
