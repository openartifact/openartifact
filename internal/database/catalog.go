// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"time"

	"github.com/openartifact/openartifact/internal/database/dbsqlc"
	"github.com/openartifact/openartifact/internal/database/models"
)

// CatalogStore is the format-independent package listing: the generic package
// tables (plus the docker tags and the proxy cache, see ListRepoComponents),
// repository size/security aggregates and the package rollups. A new format
// needs no branch: it lists once it writes package_files rows.
type CatalogStore struct{ *core }

// RepoCatalogStats returns the catalog figures of the repositories in repoIDs
// (a repository without any is present with zero values): stored size and quota
// and the security aggregates, plus the time of the newest package when
// withLastPublished. It runs one query of each kind for the whole set, however
// many repositories it holds. The package count is separate
// (CountRepoPackageNames), and the newest-package time is optional: both probe
// every package table, which the list filters do not need.
func (p *CatalogStore) RepoCatalogStats(
	ctx context.Context,
	repoIDs []string,
	withLastPublished bool,
) (map[string]models.RepoStats, error) {
	stats := make(map[string]models.RepoStats, len(repoIDs))
	if len(repoIDs) == 0 {
		return stats, nil
	}

	for _, id := range repoIDs {
		stats[id] = models.RepoStats{}
	}

	usage, err := p.q.RepoStorageUsage(ctx, repoIDs)
	if err != nil {
		return nil, fmt.Errorf("failed to load repository storage usage: %w", err)
	}

	for _, u := range usage {
		s := stats[u.RepositoryID]
		s.SizeBytes = u.CurrentSizeBytes
		s.Quota = models.RepoQuotaUsage{LimitBytes: u.MaxSizeBytes, UsedBytes: u.CurrentSizeBytes}
		stats[u.RepositoryID] = s
	}

	security, err := p.q.RepoSecurityCounts(ctx, repoIDs)
	if err != nil {
		return nil, fmt.Errorf("failed to load repository security counts: %w", err)
	}

	for _, sc := range security {
		s := stats[sc.RepositoryID]
		s.Security = models.RepoSecurity{
			Critical: sc.Critical, High: sc.High, Medium: sc.Medium, Low: sc.Low,
			Secrets:           sc.Secrets,
			LicenseViolations: sc.LicenseViolations,
			Quarantined:       sc.Quarantined,
			ScannedAt:         sc.ScannedAt,
		}
		stats[sc.RepositoryID] = s
	}

	if withLastPublished {
		if err := p.loadLastPublished(ctx, repoIDs, stats); err != nil {
			return nil, err
		}
	}

	return stats, nil
}

func (p *CatalogStore) loadLastPublished(ctx context.Context, repoIDs []string, stats map[string]models.RepoStats) error {
	rows, err := p.pool.Query(ctx, lastPublishedQuery(), repoIDs)
	if err != nil {
		return fmt.Errorf("failed to load last published times: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			id string
			at *time.Time
		)

		if err := rows.Scan(&id, &at); err != nil {
			return fmt.Errorf("failed to scan last published time: %w", err)
		}

		s := stats[id]
		s.LastPublishedAt = at
		stats[id] = s
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("failed to iterate last published times: %w", err)
	}

	return nil
}

// CountRepoPackageNames returns the number of distinct package names (as the
// package listing names them) of each repository in repoIDs; a repository with
// no packages maps to 0. It reads every package row of the repositories, so ask
// for one page of repositories at a time.
func (p *CatalogStore) CountRepoPackageNames(ctx context.Context, repoIDs []string) (map[string]int64, error) {
	counts := make(map[string]int64, len(repoIDs))
	if len(repoIDs) == 0 {
		return counts, nil
	}

	for _, id := range repoIDs {
		counts[id] = 0
	}

	rows, err := p.pool.Query(ctx, packageCountQuery(), repoIDs)
	if err != nil {
		return nil, fmt.Errorf("failed to count packages: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			id string
			n  int64
		)

		if err := rows.Scan(&id, &n); err != nil {
			return nil, fmt.Errorf("failed to scan package count: %w", err)
		}

		counts[id] = n
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate package counts: %w", err)
	}

	return counts, nil
}

// ListRepoPackageRollup lists the packages (format, name) of a repository, one
// row each: the latest version, the version count and the latest version's size.
// It returns the requested page and the total number of matching packages.
//
// The first stage groups the narrow rows of every table, the second reads the
// sizes of the page's packages only (see rollupQuery).
func (p *CatalogStore) ListRepoPackageRollup(ctx context.Context, opts models.PackageRollupOptions) ([]models.PackageRollup, int, error) {
	offset := (opts.Page - 1) * opts.Limit

	page, total, err := p.rollupPage(ctx, opts, opts.Limit, offset)
	if err != nil {
		return nil, 0, err
	}

	if len(page) == 0 {
		if offset == 0 {
			return page, 0, nil
		}

		// A page past the end carries no row to read the total from.
		_, total, err = p.rollupPage(ctx, opts, 1, 0)

		return page, total, err
	}

	if err := p.loadRollupSizes(ctx, opts, page); err != nil {
		return nil, 0, err
	}

	return page, total, nil
}

// rollupPage runs the first stage of the rollup for one page.
func (p *CatalogStore) rollupPage(
	ctx context.Context,
	opts models.PackageRollupOptions,
	limit, offset int,
) ([]models.PackageRollup, int, error) {
	query, args := rollupQuery(rollupFilter{
		repoID: opts.RepoID, repoFormat: opts.RepoFormat,
		format: opts.Format, name: opts.Name, query: opts.Query,
		flagged: opts.Flagged, sortByName: opts.SortByName,
		limit: limit, offset: offset,
	})

	rows, err := p.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to list package rollup: %w", err)
	}
	defer rows.Close()

	var (
		page  []models.PackageRollup
		total int64
	)

	for rows.Next() {
		var (
			r models.PackageRollup
			n int64
		)

		if err := rows.Scan(&r.Format, &r.Name, &r.LatestVersion, &n, &r.CreatedAt, &r.UpdatedAt, &total); err != nil {
			return nil, 0, fmt.Errorf("failed to scan package rollup: %w", err)
		}

		r.VersionCount = int(n)

		page = append(page, r)
	}

	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("failed to iterate package rollup: %w", err)
	}

	return page, int(total), nil
}

// loadRollupSizes fills the size, the files and the latest-version rows of page.
func (p *CatalogStore) loadRollupSizes(ctx context.Context, opts models.PackageRollupOptions, page []models.PackageRollup) error {
	names := make([]string, 0, len(page))
	byKey := make(map[[2]string]*models.PackageRollup, len(page))

	for i := range page {
		names = append(names, page[i].Name)
		byKey[[2]string{page[i].Format, page[i].Name}] = &page[i]
	}

	rows, err := p.pool.Query(ctx, rollupSizesQuery(opts.RepoFormat), opts.RepoID, names)
	if err != nil {
		return fmt.Errorf("failed to load package sizes: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var format, name, version, id string

		var size int64

		if err := rows.Scan(&format, &name, &version, &id, &size); err != nil {
			return fmt.Errorf("failed to scan package size: %w", err)
		}

		r, ok := byKey[[2]string{format, name}]
		if !ok || r.LatestVersion != version {
			continue
		}

		r.SizeBytes += size
		r.LatestIDs = append(r.LatestIDs, id)
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("failed to iterate package sizes: %w", err)
	}

	return nil
}

// ArtifactSecurity returns the scan state of the packages of repoID with the
// given listing row ids. A package that was never scanned has no entry.
func (p *CatalogStore) ArtifactSecurity(
	ctx context.Context,
	repoID string,
	artifactIDs []string,
) (map[string]models.ArtifactSecurity, error) {
	out := make(map[string]models.ArtifactSecurity, len(artifactIDs))
	if len(artifactIDs) == 0 {
		return out, nil
	}

	rows, err := p.q.ArtifactSecurityByPath(ctx, dbsqlc.ArtifactSecurityByPathParams{RepoID: repoID, ArtifactPaths: artifactIDs})
	if err != nil {
		return nil, fmt.Errorf("failed to load artifact security: %w", err)
	}

	for _, r := range rows {
		out[r.ArtifactPath] = models.ArtifactSecurity{
			Critical: int64(r.Critical), High: int64(r.High), Medium: int64(r.Medium), Low: int64(r.Low),
			Secrets:     int64(r.Secrets),
			Quarantined: r.Quarantined,
			ScannedAt:   r.ScannedAt,
		}
	}

	return out, nil
}

// ListRecentPackages asks each table for recentFetchFactor rows per requested
// version (at most recentFetchMax): a version of a multi-file format (maven, deb
// and rpm architectures, pypi distributions) is several rows.
const (
	recentFetchFactor = 6
	recentFetchMax    = 600
)

// ListRecentPackages returns the newest package versions across the
// repositories in repoIDs, newest first, at most limit of them. Multi-file
// versions appear once.
func (p *CatalogStore) ListRecentPackages(ctx context.Context, repoIDs []string, limit int) ([]models.RecentPackage, error) {
	if len(repoIDs) == 0 || limit < 1 {
		return nil, nil
	}

	perTable := min(limit*recentFetchFactor, recentFetchMax)

	rows, err := p.pool.Query(ctx, recentPackagesQuery(), repoIDs, perTable, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to list recent packages: %w", err)
	}
	defer rows.Close()

	var out []models.RecentPackage

	for rows.Next() {
		var r models.RecentPackage

		if err := rows.Scan(&r.Repository, &r.Format, &r.Name, &r.Version, &r.CreatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan recent package: %w", err)
		}

		out = append(out, r)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate recent packages: %w", err)
	}

	return out, nil
}

// GetRepoComponentDetail resolves a row id of the package listing (any format)
// to the row with the metadata its table stores, scoped to repoID. It returns
// nil, nil when the id is not in that repository or is not a UUID.
func (p *CatalogStore) GetRepoComponentDetail(ctx context.Context, repoID, id string) (*models.ComponentDetail, error) {
	if !isUUID(id) {
		return nil, nil //nolint:nilnil // nil, nil is the standard Go "not found" pattern
	}

	row, err := p.q.GetRepoComponentDetail(ctx, dbsqlc.GetRepoComponentDetailParams{RepoID: repoID, ID: id})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // nil, nil is the standard Go "not found" pattern
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get component detail: %w", err)
	}

	detail := &models.ComponentDetail{
		ID: row.ID, Format: row.Format, Name: row.Name, Version: row.Version, Arch: row.Arch,
		Filename: row.Filename, SHA256: row.Sha256, MD5: row.Md5, SHA1: row.Sha1, SizeBytes: row.SizeBytes,
		Summary: row.Summary, Description: row.Description, License: row.License, Homepage: row.Homepage,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, Deps: row.Deps,
		Info: map[string]string{},
	}

	if len(row.Info) > 0 {
		var info map[string]string
		if err := json.Unmarshal(row.Info, &info); err != nil {
			return nil, fmt.Errorf("failed to decode component info: %w", err)
		}

		maps.DeleteFunc(info, func(_, v string) bool { return v == "" })
		detail.Info = info
	}

	return detail, nil
}

// ListComponentsByRepo lists the packages of one repository across every
// format (see ListRepoComponents in queries/components.sql for the mapping). formatFilter, when non-empty, restricts the result to one format.
func (p *CatalogStore) ListComponentsByRepo(
	ctx context.Context,
	repoID, formatFilter string,
	page, limit int,
) ([]*models.Component, int, error) {
	return p.SearchComponents(ctx, repoID, ComponentListOptions{Format: formatFilter, Page: page, Limit: limit})
}

// SearchComponents is ListComponentsByRepo with the extra filters and the
// ordering of ComponentListOptions.
func (p *CatalogStore) SearchComponents(ctx context.Context, repoID string, opts ComponentListOptions) ([]*models.Component, int, error) {
	rows, err := p.listComponentRows(ctx, repoID, opts)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to list packages: %w", err)
	}

	total, err := p.componentTotal(ctx, repoID, opts, len(rows))
	if err != nil {
		return nil, 0, err
	}

	components := make([]*models.Component, 0, len(rows))
	for _, r := range rows {
		components = append(components, &models.Component{
			ID:            r.ID,
			RepositoryID:  r.RepositoryID,
			Name:          r.Name,
			Version:       r.Version,
			Format:        r.Format,
			Arch:          r.Arch,
			SHA256:        r.Sha256,
			Release:       r.Release,
			Epoch:         int(r.Epoch),
			BuildTime:     r.BuildTime,
			ArchiveSize:   r.ArchiveSize,
			PackageSize:   r.PackageSize,
			InstalledSize: r.InstalledSize,
			Filename:      r.Filename,
			CreatedAt:     r.CreatedAt,
			UpdatedAt:     r.UpdatedAt,
		})
	}

	return components, total, nil
}

// componentTotal is the number of rows the listing matches in total. A page
// that came back neither full nor (past the first page) empty already ends the
// listing, so its size gives the total without scanning the repository again;
// only a full page, or an empty page past the end, needs CountRepoComponents.
func (p *CatalogStore) componentTotal(ctx context.Context, repoID string, opts ComponentListOptions, pageRows int) (int, error) {
	offset := (opts.Page - 1) * opts.Limit
	if opts.Limit > 0 && opts.Page > 0 && pageRows < opts.Limit && (pageRows > 0 || offset == 0) {
		return offset + pageRows, nil
	}

	total, err := p.q.CountRepoComponents(ctx, dbsqlc.CountRepoComponentsParams{
		RepoID:       repoID,
		FormatFilter: opts.Format,
		NameFilter:   opts.Name,
		Q:            opts.Query,
	})
	if err != nil {
		return 0, fmt.Errorf("failed to count packages: %w", err)
	}

	return int(total), nil
}

// componentRow is the row shape shared by ListRepoComponents and
// ListRepoComponentsByName.
type componentRow = dbsqlc.ListRepoComponentsRow

// listComponentRows runs the newest-first or the by-name variant of the
// package listing query.
func (p *CatalogStore) listComponentRows(ctx context.Context, repoID string, opts ComponentListOptions) ([]componentRow, error) {
	limit := toInt32(opts.Limit)
	offset := toInt32((opts.Page - 1) * opts.Limit)

	if !opts.SortByName {
		return p.q.ListRepoComponents(ctx, dbsqlc.ListRepoComponentsParams{
			RepoID: repoID, FormatFilter: opts.Format, NameFilter: opts.Name, Q: opts.Query,
			PageLimit: limit, PageOffset: offset,
		})
	}

	rows, err := p.q.ListRepoComponentsByName(ctx, dbsqlc.ListRepoComponentsByNameParams{
		RepoID: repoID, FormatFilter: opts.Format, NameFilter: opts.Name, Q: opts.Query,
		PageLimit: limit, PageOffset: offset,
	})
	if err != nil {
		return nil, fmt.Errorf("list components by name: %w", err)
	}

	out := make([]componentRow, len(rows))
	for i, r := range rows {
		out[i] = componentRow(r)
	}

	return out, nil
}

// GetRepoComponentBlob resolves a row id of the package listing
// (ListComponentsByRepo) to its blob, scoped to repoID. It returns nil, nil
// when no row of any format has that id in the repository, or when id is not
// a UUID.
func (p *CatalogStore) GetRepoComponentBlob(ctx context.Context, repoID, id string) (*models.ComponentBlob, error) {
	if !isUUID(id) {
		return nil, nil //nolint:nilnil // nil, nil is the standard Go "not found" pattern
	}

	row, err := p.q.GetRepoComponentBlob(ctx, dbsqlc.GetRepoComponentBlobParams{RepoID: repoID, ID: id})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // nil, nil is the standard Go "not found" pattern
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get component blob: %w", err)
	}

	return &models.ComponentBlob{
		ID:        row.ID,
		Format:    row.Format,
		Filename:  row.Filename,
		Name:      row.Name,
		Version:   row.Version,
		SHA256:    row.Sha256,
		Size:      row.SizeBytes,
		MediaType: row.MediaType,
	}, nil
}
