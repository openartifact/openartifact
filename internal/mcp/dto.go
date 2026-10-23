// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package mcp

import (
	"time"

	"github.com/openartifact/openartifact/internal/api"
	"github.com/openartifact/openartifact/internal/api/responses"
	"github.com/openartifact/openartifact/internal/clientsetup"
	"github.com/openartifact/openartifact/internal/database/models"
)

// Every tool result is built from the explicit snake_case DTOs below. Never
// marshal a database model directly: models carry secrets (upstream
// credentials) and have no JSON tags.

type repositoryListResult struct {
	provenance

	Total        int                            `json:"total"`
	Page         int                            `json:"page"`
	Limit        int                            `json:"limit"`
	Repositories []responses.RepositoryResponse `json:"repositories"`
}

// packageDTO mirrors the package item of GET /api/v1/repositories/{name}/packages.
type packageDTO struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Version   string `json:"version"`
	Format    string `json:"format"`
	Arch      string `json:"arch"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"`
	Filename  string `json:"filename"`
	CreatedAt string `json:"created_at"`
}

func toPackageDTO(c *models.Component) packageDTO {
	return packageDTO{
		ID:        c.ID,
		Name:      c.Name,
		Version:   c.Version,
		Format:    c.Format,
		Arch:      c.Arch,
		Size:      c.PackageSize,
		SHA256:    c.SHA256,
		Filename:  c.Filename,
		CreatedAt: formatTime(c.CreatedAt),
	}
}

type packageListResult struct {
	provenance

	Repository string       `json:"repository"`
	Total      int          `json:"total"`
	Page       int          `json:"page"`
	Limit      int          `json:"limit"`
	Packages   []packageDTO `json:"packages"`
}

type searchHit struct {
	Repository string     `json:"repository"`
	Package    packageDTO `json:"package"`
}

type searchResult struct {
	provenance

	Query   string      `json:"query"`
	Results []searchHit `json:"results"`
	Count   int         `json:"count"`
}

type packageVersionDTO struct {
	ID          string `json:"id"`
	Version     string `json:"version"`
	Arch        string `json:"arch"`
	Filename    string `json:"filename"`
	SHA256      string `json:"sha256"`
	Size        int64  `json:"size"`
	CreatedAt   string `json:"created_at"`
	DownloadURL string `json:"download_url"`
}

type packageDetailResult struct {
	provenance

	Repository string              `json:"repository"`
	Format     string              `json:"format"`
	Name       string              `json:"name"`
	Versions   []packageVersionDTO `json:"versions"`
	// Truncated is set when the version list or the repository scan hit its
	// cap, so the list may be incomplete.
	Truncated bool `json:"truncated"`
}

type severityCounts struct {
	Critical int `json:"critical"`
	High     int `json:"high"`
	Medium   int `json:"medium"`
	Low      int `json:"low"`
	Unknown  int `json:"unknown"`
}

type findingDTO struct {
	PackageName    string   `json:"package_name"`
	PackageVersion string   `json:"package_version"`
	ID             string   `json:"id"`
	Severity       string   `json:"severity"`
	CVSS           float64  `json:"cvss"`
	Summary        string   `json:"summary"`
	FixedIn        string   `json:"fixed_in"`
	References     []string `json:"references"`
}

type vulnerabilitiesResult struct {
	provenance

	Repository         string         `json:"repository"`
	PackageFilter      string         `json:"package_filter,omitempty"`
	VersionFilter      string         `json:"version_filter,omitempty"`
	ScannedPackages    int            `json:"scanned_packages"`
	VulnerablePackages int            `json:"vulnerable_packages"`
	PendingScans       int            `json:"pending_scans"`
	FailedScans        int            `json:"failed_scans"`
	Totals             severityCounts `json:"totals"`
	Findings           []findingDTO   `json:"findings"`
	FindingsTruncated  bool           `json:"findings_truncated"`
}

type clientSetupResult struct {
	Repository string                `json:"repository"`
	Format     string                `json:"format"`
	BaseURL    string                `json:"base_url"`
	Clients    []clientsetup.Snippet `json:"clients"`
}

// analysisDTO deliberately omits error_message (may echo provider errors) and
// token accounting.
type analysisDTO struct {
	ID                    string   `json:"id"`
	RepositoryID          string   `json:"repository_id"`
	ArtifactID            string   `json:"artifact_id"`
	ArtifactFormat        string   `json:"artifact_format"`
	ArtifactName          string   `json:"artifact_name"`
	ArtifactVersion       string   `json:"artifact_version"`
	Summary               string   `json:"summary"`
	Tags                  []string `json:"tags"`
	LicenseStatus         string   `json:"license_status"`
	LicenseName           string   `json:"license_name"`
	SecurityNotes         string   `json:"security_notes"`
	QuarantineRecommended bool     `json:"quarantine_recommended"`
	Status                string   `json:"status"`
	ModelUsed             string   `json:"model_used"`
	CreatedAt             string   `json:"created_at"`
	UpdatedAt             string   `json:"updated_at"`
}

func toAnalysisDTO(a *models.AIAnalysis) analysisDTO {
	tags := a.Tags
	if tags == nil {
		tags = []string{}
	}

	return analysisDTO{
		ID:                    a.ID,
		RepositoryID:          a.RepositoryID,
		ArtifactID:            a.ArtifactID,
		ArtifactFormat:        a.ArtifactFormat,
		ArtifactName:          a.ArtifactName,
		ArtifactVersion:       a.ArtifactVersion,
		Summary:               truncateText(a.Summary, maxUntrustedText),
		Tags:                  tags,
		LicenseStatus:         a.LicenseStatus,
		LicenseName:           a.LicenseName,
		SecurityNotes:         truncateText(a.SecurityNotes, maxUntrustedText),
		QuarantineRecommended: a.QuarantineRecommended,
		Status:                a.Status,
		ModelUsed:             a.ModelUsed,
		CreatedAt:             formatTime(a.CreatedAt),
		UpdatedAt:             formatTime(a.UpdatedAt),
	}
}

type analysisListResult struct {
	provenance

	Analyses []analysisDTO `json:"analyses"`
	Total    int           `json:"total"`
	Limit    int           `json:"limit"`
	Offset   int           `json:"offset"`
}

// analysisResult is one analysis with the untrusted-content marker.
type analysisResult struct {
	provenance
	analysisDTO
}

// repositoryResult is one repository with the untrusted-content marker.
type repositoryResult struct {
	provenance
	responses.RepositoryResponse
}

type queuedResult struct {
	Status     string `json:"status"`
	ArtifactID string `json:"artifact_id"`
	Message    string `json:"message"`
}

func formatTime(t time.Time) string { return t.Format(time.RFC3339Nano) }

// toRepositoryDTO is the REST repository shape minus the upstream location and
// account: an MCP client has no need for them and they can name internal hosts.
func toRepositoryDTO(r *models.Repository) responses.RepositoryResponse {
	resp := api.RepositoryToResponse(r)
	resp.UpstreamURL = ""
	resp.UpstreamUsername = ""

	return resp
}
