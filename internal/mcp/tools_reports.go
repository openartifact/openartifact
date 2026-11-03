// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !nomcp

package mcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/openartifact/openartifact/internal/api"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/sbom"
)

const (
	defaultSBOMComponents = 100
	maxSBOMComponents     = 500

	// maxSBOMBytes bounds how much of one stored SBOM document is read.
	maxSBOMBytes = 32 << 20

	// maxArtifactIDBytes bounds an artifact id argument.
	maxArtifactIDBytes = 1024

	// maxLicenseEntries and maxLicenseText bound the license strings of one
	// result: they are read from the package file.
	maxLicenseEntries = 100
	maxLicenseText    = 500

	// maxSBOMText bounds a component name, version or purl of an SBOM, which
	// the package's own metadata chose.
	maxSBOMText = 300

	defaultActivityLimit = 25
	maxActivityLimit     = 100
	maxActivityMessage   = 1000
	maxCursorBytes       = 1024
)

var (
	errArtifactIDParam   = fmt.Errorf("artifact_id is required (at most %d bytes)", maxArtifactIDBytes)
	errNoLicenseScan     = errors.New("no license scan found for this artifact")
	errNoSBOM            = errors.New("no SBOM has been generated for this artifact")
	errSBOMFormat        = errors.New("format must be one of: cyclonedx, spdx-json, spdx3-json, spdx")
	errSBOMUnavailable   = errors.New("stored SBOM documents cannot be read on this server")
	errSBOMUnreadable    = errors.New("the stored SBOM could not be read")
	errActivityCategory  = errors.New("unknown category; see the tool description for the valid ones")
	errActivityAdminOnly = errors.New("admin access required for this category")
	errActivityCursor    = errors.New("invalid cursor")
)

func (s *Server) registerReportTools() {
	addTool(s, readTool(
		"get_licenses",
		"Get artifact licenses",
		"Get the license scan result of one stored artifact: the licenses found (as declared), the SPDX expressions they "+
			"resolve to, the unresolved ones that need a human review, where they came from (the package file or what a client "+
			"declared), the scan status, and whether the artifact is quarantined by a license policy (and whether an admin approved "+
			"it). Use it for license compliance questions about one version; get_artifact_analysis gives an AI opinion instead. "+
			"The artifact must have been scanned. Requires administrator (global manage) permission, like the REST license scan "+
			"endpoints.",
	),
		true, s.getLicenses)

	addTool(s, readTool(
		"get_sbom",
		"Get artifact SBOM",
		"Get the stored software bill of materials of one artifact: report id, format, spec version, generation time, and the "+
			"components it lists (name, version, purl, licenses), capped at limit (default 100, max 500) with truncated=true when "+
			"more exist. It reads an SBOM that was already generated; it never generates one. When the artifact has SBOMs in "+
			"several formats the most recent one is returned unless format asks for another; available_formats lists them. "+
			"Requires administrator (global manage) permission, like the REST SBOM endpoints.",
	),
		true, s.getSBOM)

	open, adminOnly := api.ActivityCategoryNames()

	addTool(s, readTool(
		"get_repository_activity",
		"Get repository activity",
		"List the recent events recorded against a repository, newest first: uploads, deletions, and so on, each with time, "+
			"actor, action and a message. Use it to find out who changed what and when, or why a package appeared or vanished. "+
			"Categories open to anyone who can read the repository: "+strings.Join(open, ", ")+". Categories reserved to the "+
			"repository's admins (global admin, or maintainer of its project): "+strings.Join(adminOnly, ", ")+"; without a "+
			"category you get every one you may see. Pass the returned next_cursor as cursor for older events. Requires read "+
			"permission on the repository.",
	),
		true, s.getRepositoryActivity)
}

func (s *Server) getLicenses(ctx context.Context, in artifactIn) (licenseResult, error) {
	repo, err := s.adminArtifact(ctx, in)
	if err != nil {
		return licenseResult{}, err
	}

	scan, err := s.db.GetLicenseScanResult(ctx, repo.ID, in.ArtifactID)
	if err != nil {
		return licenseResult{}, s.internalError("get license scan", err, "repository", repo.Name, "artifact_id", in.ArtifactID)
	}

	if scan == nil {
		return licenseResult{}, errNoLicenseScan
	}

	resp := api.LicenseScanResultResponse(scan)
	resp.ScanError = nil
	resp.Licenses = capLicenses(resp.Licenses)
	resp.SPDXExpressions = capLicenses(resp.SPDXExpressions)
	resp.UnresolvedLicenses = capLicenses(resp.UnresolvedLicenses)

	return licenseResult{provenance: newProvenance(), Repository: repo.Name, LicenseScanResultResponse: resp}, nil
}

// adminArtifact gates the tools that read what a scan stored for an artifact:
// the same global-admin permission as the REST scan endpoints, then read access
// to the repository.
func (s *Server) adminArtifact(ctx context.Context, in artifactIn) (*models.Repository, error) {
	if !s.allowed(ctx, "", "manage") {
		return nil, errAdminRequired
	}

	if in.ArtifactID == "" || len(in.ArtifactID) > maxArtifactIDBytes {
		return nil, errArtifactIDParam
	}

	return s.resolveRepo(ctx, in.Repository)
}

// capLicenses keeps at most maxLicenseEntries entries of in, each cut to
// maxLicenseText bytes; never nil.
func capLicenses(in []string) []string {
	out := make([]string, 0, min(len(in), maxLicenseEntries))

	for _, v := range in[:min(len(in), maxLicenseEntries)] {
		out = append(out, truncateText(v, maxLicenseText))
	}

	return out
}

func validSBOMFormat(format string) bool {
	switch format {
	case "", "cyclonedx", "spdx-json", "spdx3-json", "spdx":
		return true
	default:
		return false
	}
}

func (s *Server) getSBOM(ctx context.Context, in sbomIn) (sbomResult, error) {
	repo, err := s.adminArtifact(ctx, artifactIn{Repository: in.Repository, ArtifactID: in.ArtifactID})
	if err != nil {
		return sbomResult{}, err
	}

	if !validSBOMFormat(in.Format) {
		return sbomResult{}, errSBOMFormat
	}

	limit := clampInt(in.Limit, defaultSBOMComponents, 1, maxSBOMComponents)

	reports, err := s.db.ListSBOMReports(ctx, repo.ID)
	if err != nil {
		return sbomResult{}, s.internalError("list sbom reports", err, "repository", repo.Name)
	}

	report, formats := pickSBOMReport(reports, in.ArtifactID, in.Format)
	if report == nil {
		return sbomResult{}, errNoSBOM
	}

	comps, truncated, err := s.readSBOMComponents(ctx, report, limit)
	if err != nil {
		return sbomResult{}, err
	}

	for i := range comps {
		comps[i].Name = truncateText(comps[i].Name, maxSBOMText)
		comps[i].Version = truncateText(comps[i].Version, maxSBOMText)
		comps[i].PURL = truncateText(comps[i].PURL, maxSBOMText)
		comps[i].Licenses = capLicenses(comps[i].Licenses)
	}

	return sbomResult{
		provenance:       newProvenance(),
		Repository:       repo.Name,
		ArtifactID:       report.ArtifactID,
		ReportID:         report.ID,
		Format:           report.Format,
		SpecVersion:      report.SpecVersion,
		GeneratedAt:      formatTime(report.GeneratedAt),
		ComponentCount:   report.ComponentCount,
		Components:       comps,
		Truncated:        truncated,
		AvailableFormats: formats,
	}, nil
}

// pickSBOMReport selects the report of artifactID in the wanted format ("" for
// the most recent of any) and lists the formats the artifact has reports in.
// reports come newest first.
func pickSBOMReport(reports []*models.SBOMReport, artifactID, format string) (*models.SBOMReport, []string) {
	var picked *models.SBOMReport

	formats := []string{}

	for _, r := range reports {
		if r == nil || r.ArtifactID != artifactID {
			continue
		}

		if !slices.Contains(formats, r.Format) {
			formats = append(formats, r.Format)
		}

		if picked == nil && (format == "" || r.Format == format) {
			picked = r
		}
	}

	return picked, formats
}

func (s *Server) readSBOMComponents(ctx context.Context, report *models.SBOMReport, limit int) ([]sbom.Component, bool, error) {
	if s.blobs == nil {
		return nil, false, errSBOMUnavailable
	}

	if report.BlobSHA256 == "" {
		return nil, false, errSBOMUnreadable
	}

	rc, err := s.blobs.Get(ctx, report.BlobSHA256)
	if err != nil {
		return nil, false, s.internalError("read sbom blob", err, "report_id", report.ID, "sha256", report.BlobSHA256)
	}

	defer func() { _ = rc.Close() }()

	comps, truncated, err := sbom.ReadComponents(io.LimitReader(rc, maxSBOMBytes), report.Format, limit)
	if err != nil {
		s.log.Warnw("mcp: parse sbom", "report_id", report.ID, "format", report.Format, "error", err)

		return nil, false, errSBOMUnreadable
	}

	return comps, truncated, nil
}

func (s *Server) getRepositoryActivity(ctx context.Context, in activityIn) (activityResult, error) {
	if len(in.Cursor) > maxCursorBytes {
		return activityResult{}, errActivityCursor
	}

	repo, err := s.resolveRepo(ctx, in.Repository)
	if err != nil {
		return activityResult{}, err
	}

	caller := api.ActivityCaller{SignedIn: true, RepoAdmin: s.repoAdmin != nil && s.repoAdmin(ctx, repo.Name)}

	var categories []string
	if in.Category != "" {
		categories = []string{in.Category}
	}

	page, err := api.ListRepositoryActivity(ctx, s.db, s.log, repo, caller, api.ActivityQuery{
		Categories: categories,
		Cursor:     in.Cursor,
		Limit:      clampInt(in.Limit, defaultActivityLimit, 1, maxActivityLimit),
	})

	switch {
	case err == nil:
	case errors.Is(err, api.ErrActivityCategory):
		return activityResult{}, errActivityCategory
	case errors.Is(err, api.ErrActivityAdminCategory):
		return activityResult{}, errActivityAdminOnly
	case errors.Is(err, api.ErrActivityCursor):
		return activityResult{}, errActivityCursor
	default:
		return activityResult{}, s.internalError("list repository activity", err, "repository", repo.Name)
	}

	for i := range page.Data {
		page.Data[i].Message = truncateText(page.Data[i].Message, maxActivityMessage)
	}

	return activityResult{provenance: newProvenance(), Repository: repo.Name, ActivityPage: page}, nil
}
