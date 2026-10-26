// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/openartifact/openartifact/internal/database/dbsqlc"
	"github.com/openartifact/openartifact/internal/database/models"
)

// ErrInvalidFindingFilter is returned by ListSecurityFindings for a type,
// severity or status it does not know, and for a page size below 1.
var ErrInvalidFindingFilter = errors.New("invalid security finding filter")

// noScanTime is what the security queries return for "no scan": the Unix epoch.
// No scan finished then, so it never collides with a real time.
//
//nolint:gochecknoglobals // immutable sentinel
var noScanTime = time.Unix(0, 0)

// scanTimePtr maps a security query time to nil when it is the noScanTime sentinel.
func scanTimePtr(t time.Time) *time.Time {
	if !t.After(noScanTime) {
		return nil
	}

	return &t
}

// severityRanks maps a severity label to the rank the security_results view uses.
//
//nolint:gochecknoglobals // immutable lookup table
var severityRanks = map[string]int32{
	models.FindingSeverityCritical: 4,
	models.FindingSeverityHigh:     3,
	models.FindingSeverityMedium:   2,
	models.FindingSeverityLow:      1,
}

// vexStatusByRank is the inverse of the vex_rank column of the view.
//
//nolint:gochecknoglobals // immutable lookup table
var vexStatusByRank = map[int32]string{
	1: models.VEXAffected,
	2: models.VEXUnderInvestigation,
	3: models.VEXFixed,
	4: models.VEXNotAffected,
}

func optionalText(s string) *string {
	if s == "" {
		return nil
	}

	return &s
}

// scopeProjects returns the project ids as a non-nil slice (a nil slice would
// bind as SQL NULL, which matches nothing in ANY()).
func scopeProjects(s models.FindingScope) []string {
	if s.ProjectIDs == nil {
		return []string{}
	}

	return s.ProjectIDs
}

// validFindingParams reports whether p only holds values the view knows.
func validFindingParams(p models.FindingListParams) (*int32, bool) {
	if p.Limit < 1 || len(p.Types) == 0 {
		return nil, false
	}

	for _, t := range p.Types {
		switch t {
		case models.FindingVuln, models.FindingLicense, models.FindingSecret, models.FindingAI:
		default:
			return nil, false
		}
	}

	switch p.Status {
	case "", models.FindingStatusOpen, models.FindingStatusQuarantined, models.FindingStatusSuppressed:
	default:
		return nil, false
	}

	if p.Severity == "" {
		return nil, true
	}

	rank, ok := severityRanks[p.Severity]
	if !ok {
		return nil, false
	}

	return &rank, true
}

// ListSecurityFindings returns one keyset page of the findings of every
// requested type, worst severity first, then newest scan, then id.
func (p *PostgresDB) ListSecurityFindings(ctx context.Context, params models.FindingListParams) (*models.FindingPage, error) {
	rank, ok := validFindingParams(params)
	if !ok {
		return nil, ErrInvalidFindingFilter
	}

	arg := dbsqlc.ListSecurityFindingsParams{
		Kinds:         params.Types,
		ScopeAll:      params.Scope.All,
		ScopeProjects: scopeProjects(params.Scope),
		Repo:          optionalText(params.Repo),
		Project:       optionalText(params.Project),
		SeverityRank:  rank,
		Status:        optionalText(params.Status),
		PageLimit:     int32(params.Limit + 1), //nolint:gosec // G115: the page size is bounded by the handler
		// The cursor parameters are cast even when unused; a placeholder keeps the uuid cast valid.
		AfterID: "00000000-0000-0000-0000-000000000000",
		AfterAt: noScanTime,
	}

	if params.Q != "" {
		like := "%" + escapeLike(params.Q) + "%"
		arg.Q = &like
	}

	if c := params.After; c != nil {
		arg.HasCursor = true
		arg.AfterRank = int32(c.SeverityRank) //nolint:gosec // G115: a rank is 0..4
		arg.AfterAt = c.ScannedAt
		arg.AfterID = c.ID
	}

	rows, err := p.q.ListSecurityFindings(ctx, arg)
	if err != nil {
		return nil, fmt.Errorf("list security findings: %w", err)
	}

	page := &models.FindingPage{Findings: make([]*models.Finding, 0, min(len(rows), params.Limit))}

	for i, r := range rows {
		if i == params.Limit {
			last := page.Findings[len(page.Findings)-1]
			page.Next = &models.FindingCursor{SeverityRank: last.SeverityRank, ScannedAt: last.ScannedAt, ID: last.ID}

			break
		}

		page.Findings = append(page.Findings, findingFromRow(r))
	}

	if err := p.resolveFindingPackages(ctx, page.Findings); err != nil {
		return nil, err
	}

	return page, nil
}

// resolveFindingPackages replaces the package name of findings recorded against
// a package row id (scans started from a package page use the row id as
// artifact_id, and the view then has nothing but that id to show) with the
// row's name and version. An id that no longer resolves (purged package) is
// blanked: the API never returns a bare id as a package name.
func (p *PostgresDB) resolveFindingPackages(ctx context.Context, findings []*models.Finding) error {
	var ids []string

	for _, f := range findings {
		if f.PackageName == f.ArtifactID && isUUID(f.ArtifactID) {
			ids = append(ids, f.ArtifactID)
		}
	}

	if len(ids) == 0 {
		return nil
	}

	rows, err := p.q.ResolveComponentNames(ctx, ids)
	if err != nil {
		return fmt.Errorf("resolve finding packages: %w", err)
	}

	type key struct{ repo, id string }

	names := make(map[key]dbsqlc.ResolveComponentNamesRow, len(rows))
	for _, r := range rows {
		names[key{r.RepositoryID, r.ID}] = r
	}

	for _, f := range findings {
		if f.PackageName != f.ArtifactID || !isUUID(f.ArtifactID) {
			continue
		}

		r, ok := names[key{f.RepositoryID, f.ArtifactID}]
		if !ok {
			f.PackageName, f.PackageVersion = "", ""

			continue
		}

		f.PackageName, f.PackageVersion = r.Name, r.Version
	}

	return nil
}

func findingFromRow(r dbsqlc.ListSecurityFindingsRow) *models.Finding {
	return &models.Finding{
		ID:             r.ID,
		Type:           r.Kind,
		RepositoryID:   r.RepositoryID,
		Repository:     r.RepositoryName,
		Format:         r.RepositoryFormat,
		ArtifactID:     r.ArtifactID,
		PackageName:    r.PackageName,
		PackageVersion: r.PackageVersion,
		Scanner:        r.Scanner,
		ScanStatus:     r.ScanStatus,
		Critical:       int(r.Critical),
		High:           int(r.High),
		Medium:         int(r.Medium),
		Low:            int(r.Low),
		Quarantined:    r.Quarantined,
		Status:         r.Status,
		VEXStatus:      vexStatusByRank[r.VexRank],
		SeverityRank:   int(r.SeverityRank),
		Title:          r.Title,
		ScannedAt:      r.ScannedAt,
	}
}

// SecurityFindingsSummary counts the findings of the filter's scope per type and
// how many of its repositories were scanned.
func (p *PostgresDB) SecurityFindingsSummary(ctx context.Context, f models.FindingFilter) (*models.FindingsSummary, error) {
	rows, err := p.q.SecurityFindingsSummary(ctx, dbsqlc.SecurityFindingsSummaryParams{
		ScopeAll:      f.Scope.All,
		ScopeProjects: scopeProjects(f.Scope),
		Repo:          optionalText(f.Repo),
		Project:       optionalText(f.Project),
	})
	if err != nil {
		return nil, fmt.Errorf("security findings summary: %w", err)
	}

	out := &models.FindingsSummary{ByType: map[string]models.FindingTypeSummary{}}

	for _, t := range models.FindingTypes() {
		out.ByType[t] = models.FindingTypeSummary{}
	}

	for _, r := range rows {
		out.ByType[r.Kind] = models.FindingTypeSummary{
			Total: int(r.Total), Critical: int(r.Critical), High: int(r.High),
			Medium: int(r.Medium), Low: int(r.Low), Quarantined: int(r.Quarantined),
		}
	}

	cov, err := p.q.SecurityScanCoverage(ctx, dbsqlc.SecurityScanCoverageParams{
		ScopeAll:      f.Scope.All,
		ScopeProjects: scopeProjects(f.Scope),
		Repo:          optionalText(f.Repo),
		Project:       optionalText(f.Project),
	})
	if err != nil {
		return nil, fmt.Errorf("security scan coverage: %w", err)
	}

	out.TotalRepositories = int(cov.TotalRepositories)
	out.ScannedRepositories = int(cov.ScannedRepositories)
	out.LastScanAt = scanTimePtr(cov.LastScanAt)

	return out, nil
}

// SecurityHealthStats returns the instance-wide scan queue and coverage numbers.
func (p *PostgresDB) SecurityHealthStats(ctx context.Context) (*models.SecurityHealthStats, error) {
	r, err := p.q.SecurityHealthStats(ctx)
	if err != nil {
		return nil, fmt.Errorf("security health stats: %w", err)
	}

	return &models.SecurityHealthStats{
		ScansPending:          int(r.ScansPending),
		ScansFailed24h:        int(r.ScansFailed24h),
		LastScanAt:            scanTimePtr(r.LastScanAt),
		UnscannedRepositories: int(r.UnscannedRepositories),
	}, nil
}

// LatestFinishedDependencyTrackPush returns the outcome of the most recent
// finished push of any artifact, nil when none finished yet.
func (p *PostgresDB) LatestFinishedDependencyTrackPush(ctx context.Context) (*models.DependencyTrackLastPush, error) {
	r, err := p.q.LatestFinishedDependencyTrackPush(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil //nolint:nilnil // no push yet
	}

	if err != nil {
		return nil, fmt.Errorf("latest dependency-track push: %w", err)
	}

	return &models.DependencyTrackLastPush{OK: r.State == models.DTrackPushDone, At: r.UpdatedAt}, nil
}

// RepoSecuritySummary returns the counts-only security state of a repository,
// narrowed to a package (and version) when given.
func (p *PostgresDB) RepoSecuritySummary(ctx context.Context, repoID, packageName, version string) (*models.RepoSecuritySummary, error) {
	r, err := p.q.RepoSecuritySummary(ctx, dbsqlc.RepoSecuritySummaryParams{
		RepositoryID:   repoID,
		PackageName:    optionalText(packageName),
		PackageVersion: optionalText(version),
	})
	if err != nil {
		return nil, fmt.Errorf("repository security summary: %w", err)
	}

	return &models.RepoSecuritySummary{
		ScannedAt:               scanTimePtr(r.ScannedAt),
		VulnCritical:            int(r.VulnCritical),
		VulnHigh:                int(r.VulnHigh),
		VulnMedium:              int(r.VulnMedium),
		VulnLow:                 int(r.VulnLow),
		LicenseViolations:       int(r.LicenseViolations),
		SecretCount:             int(r.SecretCount),
		AIQuarantineRecommended: int(r.AiQuarantineRecommended),
		AIReviewRequired:        int(r.AiReviewRequired),
		Quarantined:             int(r.Quarantined),
	}, nil
}
