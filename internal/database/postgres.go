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
	"math"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/openartifact/openartifact/internal/database/dbsqlc"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/pkg/logger"
)

var (
	ErrSBOMReportNotFound    = errors.New("SBOM report not found")
	ErrLicensePolicyNotFound = errors.New("license policy not found")
)

// toInt32 converts n to int32, saturating at the int32 bounds so out-of-range
// pagination/limit values cannot wrap around.
func toInt32(n int) int32 {
	if n > math.MaxInt32 {
		return math.MaxInt32
	}

	if n < math.MinInt32 {
		return math.MinInt32
	}

	return int32(n)
}

// core is the connection state every domain store shares: the pools, the
// sqlc queries, the logger and the cross-store caches and settings. A backup
// snapshot view (BeginBackupSnapshot) is a second core bound to the snapshot.
type core struct {
	db *sql.DB
	// pool is what the pgx-native queries run on: the shared pool, or the single
	// transaction of a backup snapshot (see BeginBackupSnapshot).
	pool pgxQuerier
	// rawPool is the shared pgx pool itself (closed by Close, source of the
	// connection settings of snapshot connections); nil on a snapshot view.
	rawPool *pgxpool.Pool
	q       *dbsqlc.Queries
	log     *logger.Logger

	// authCache serves the per-request auth lookups (see auth_cache.go).
	authCache authCache

	// trashDefaultDays is the retention, in days, of repositories without their
	// own trash_retention_days (see SetTrashRetentionDays).
	trashDefaultDays atomic.Int32

	// blobDeletionGrace is how long the bytes of a deleted blob are kept before
	// the sweeper may remove them, in nanoseconds (see SetBlobDeletionGrace).
	blobDeletionGrace atomic.Int64

	// lockSlots bounds how many blob-lock transactions (WithBlobLocks,
	// DeleteBlobIfUnreferenced) may hold a pooled connection at once. The
	// callback of a blob lock needs further connections of the same pool; if
	// every connection were pinned by a lock holder waiting for one, the pool
	// would deadlock. Keeping holders at half the pool leaves the rest for
	// their callbacks. nil means the pool is unbounded.
	lockSlots chan struct{}
}

func NewPostgresDB(cfg DatabaseConfig, log *logger.Logger) (*PostgresDB, error) {
	poolCfg, err := poolConfig(cfg)
	if err != nil {
		return nil, err
	}

	// sql.DB via pgx stdlib — for migrate.go, GetDB(), manual queries. It is
	// built from the parsed pgx config, so no DSN string is ever re-parsed.
	db := stdlib.OpenDB(*poolCfg.ConnConfig.Copy())

	configureSQLPool(db, cfg)

	if err := waitForDatabase(context.Background(), cfg.ConnectTimeout, db.PingContext, sleepContext, log); err != nil {
		_ = db.Close()

		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	// pgxpool for sqlc-generated queries (requires pgx-native connection),
	// bounded by the same configured limits.
	pool, err := pgxpool.NewWithConfig(context.Background(), poolCfg)
	if err != nil {
		_ = db.Close()

		return nil, fmt.Errorf("failed to create pgx pool: %w", err)
	}

	var lockSlots chan struct{}
	if cfg.MaxOpenConns > 0 {
		lockSlots = make(chan struct{}, max(1, cfg.MaxOpenConns/2))
	}

	return newPostgresDB(&core{
		db:        db,
		pool:      pool,
		rawPool:   pool,
		q:         dbsqlc.New(pool),
		log:       log,
		lockSlots: lockSlots,
		authCache: authCache{ttl: time.Duration(cfg.AuthCacheTTLMillis) * time.Millisecond},
	}), nil
}

// sqlIdleTimeout is how long a surplus database/sql connection may sit idle
// before it is closed.
const sqlIdleTimeout = 5 * time.Minute

// configureSQLPool applies the connection limits to the database/sql handle.
//
// database/sql closes a connection the moment it is returned to a full idle
// list. The per-request authentication queries (token, user, groups) run on
// this handle with as many concurrent callers as there are in-flight
// requests, so an idle cap far below MaxOpenConns turns every burst into a
// stream of connect + SCRAM handshake + close cycles (measured: ~170 new
// backends in 6 s at 32 concurrent clients). Idle connections are therefore
// kept up to MaxOpenConns and shrink back only after sqlIdleTimeout;
// MaxIdleConns keeps its meaning for the pgx pool's warm minimum.
func configureSQLPool(db *sql.DB, cfg DatabaseConfig) {
	db.SetMaxOpenConns(cfg.MaxOpenConns)
	db.SetMaxIdleConns(max(cfg.MaxIdleConns, cfg.MaxOpenConns))
	db.SetConnMaxIdleTime(sqlIdleTimeout)
	db.SetConnMaxLifetime(time.Duration(cfg.ConnMaxLifetimeMinutes) * time.Minute)
}

// GetDB returns the underlying sql.DB connection.
func (p *core) GetDB() *sql.DB {
	return p.db
}

// PoolStat reads the statistics of the shared pgx pool (for the
// openartifact_db_pool_* metrics). It must not be called on a backup snapshot
// view, which has no pool of its own.
func (p *core) PoolStat() *pgxpool.Stat {
	return p.rawPool.Stat()
}

// Close closes the database connection.
func (p *core) Close() error {
	if p.rawPool != nil {
		p.rawPool.Close()
		if p.log != nil {
			p.log.Debugw("pgx pool closed")
		}
	}
	return p.db.Close()
}

// Ping checks if the database is accessible.
func (p *core) Ping(ctx context.Context) error {
	return p.db.PingContext(ctx)
}

// BeginTx starts a new transaction.
func (p *core) BeginTx(ctx context.Context) (Transaction, error) {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}

	return &PostgresTx{tx: tx}, nil
}

// Migrate runs database migrations.
func (p *core) Migrate(ctx context.Context) error {
	return NewMigrator(p.db).Apply(ctx)
}

// repoColumns is the column list every repository read selects; repoScanDest
// returns the matching Scan destinations.
const repoColumns = `id, name, format, description, COALESCE(public, false), COALESCE(allow_full_unpublish, false),
	COALESCE(repo_type, 'hosted'), COALESCE(upstream_url, ''), COALESCE(upstream_username, ''), COALESCE(upstream_password_enc,
	''), COALESCE(cache_ttl_seconds, 3600), COALESCE(negative_cache_ttl_seconds, 60), COALESCE(max_stale_seconds, 604800),
	COALESCE(blocked, false), project_id, presign_downloads, trash_retention_days, immutable_releases, merge_proxy_versions,
	sign_packages, federated, deleted_at, COALESCE(deleted_by, ''), created_at, updated_at`

func repoScanDest(r *models.Repository) []any {
	return []any{
		&r.ID,
		&r.Name,
		&r.Format,
		&r.Description,
		&r.Public,
		&r.AllowFullUnpublish,
		&r.RepoType,
		&r.UpstreamURL,
		&r.UpstreamUsername,
		&r.UpstreamPasswordEnc,
		&r.CacheTTLSeconds,
		&r.NegativeCacheTTLSeconds,
		&r.MaxStaleSeconds,
		&r.Blocked,
		&r.ProjectID,
		&r.PresignDownloads,
		&r.TrashRetentionDays,
		&r.ImmutableReleases,
		&r.MergeProxyVersions,
		&r.SignPackages,
		&r.Federated,
		&r.DeletedAt,
		&r.DeletedBy,
		&r.CreatedAt,
		&r.UpdatedAt,
	}
}

func isUUID(s string) bool { return uuid.Validate(s) == nil }

// orNow returns t, or the current time when the caller left it unset, so an
// upsert never persists the zero time.
func orNow(t time.Time) time.Time {
	if t.IsZero() {
		return time.Now().UTC()
	}

	return t
}

// createdAtOrNil maps an unset (zero) timestamp to SQL NULL so the INSERT's
// COALESCE(..., now()) applies; callers that carry an origin timestamp (the
// admin-only import path) keep it.
func createdAtOrNil(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}

	return &t
}

// blobRefSpecs lists every (table, condition) that references blob alias b. It
// is the single source of truth for both the reference sum (blobRefSumExpr,
// point lookups by sha) and the anti-join (blobUnreferencedExpr, orphan scan);
// a new package table is added here once.
var blobRefSpecs = []struct{ table, cond string }{ //nolint:gochecknoglobals // built once, read-only
	{"rpm_packages", "blob_id = b.id"},
	{"deb_packages", "blob_id = b.id"},
	{"npm_versions", "blob_id = b.id"},
	{"maven_artifacts", "blob_id = b.id"},
	{"docker_blobs", "blob_id = b.id"},
	{"docker_manifests", "blob_id = b.id"},
	{"pypi_packages", "blob_id = b.id"},
	{"nuget_packages", "blob_id = b.id"},
	{"gomod_modules", "zip_blob_id = b.id OR mod_blob_id = b.id"},
	{"cargo_crates", "blob_id = b.id"},
	{"composer_packages", "blob_id = b.id"},
	{"rubygems_gems", "blob_id = b.id"},
	{"conda_packages", "blob_id = b.id"},
	{"ansible_collections", "blob_id = b.id"},
	{"pacman_packages", "blob_id = b.id"},
	{"conan_packages", "blob_id = b.id"},
	{"apk_packages", "blob_id = b.id"},
	{"nix_paths", "blob_id = b.id"},
	{"nix_pending_nars", "blob_id = b.id"},
	{"terraform_modules", "blob_id = b.id"},
	{"terraform_providers", "blob_id = b.id"},
	{"ivy_artifacts", "blob_id = b.id"},
	{"sbt_artifacts", "blob_id = b.id"},
	{"helm_charts", "blob_id = b.id"},
	{"raw_artifacts", "blob_id = b.id"},
	{"release_bundle_items", "sha256 = b.sha256"},
	{"proxy_cache_entries", "blob_key = b.sha256"},
	{"sbom_results", "blob_id = b.id"},
	// Generated metadata (maven-metadata.xml, Packages, ...) lives in the same
	// content-addressed key space but has no blobs row; a blobs row created for
	// byte-identical content must not let its object be deleted.
	{"repository_metadata", "blob_sha256 = b.sha256"},
	{"package_deltas", "delta_sha256 = b.sha256"},
}

//nolint:gochecknoglobals // built once, read-only

// blobUnreferencedExpr is true when no table references blob alias b. NOT
// EXISTS short-circuits at the first hit and never counts, unlike
// blobRefSumExpr = 0.
var blobUnreferencedExpr = buildBlobRefExpr( //nolint:gochecknoglobals // built once, read-only
	" AND\n",
	"NOT EXISTS (SELECT 1 FROM %s WHERE %s)",
)

func buildBlobRefExpr(sep, format string) string {
	parts := make([]string, len(blobRefSpecs))
	for i, r := range blobRefSpecs {
		parts[i] = fmt.Sprintf(format, r.table, r.cond)
	}

	return strings.Join(parts, sep)
}

// rowQuerier is satisfied by *sql.DB and *sql.Tx.
type rowQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Package delta methods

// Proxy/Group repository methods

// PostgresTx implements the Transaction interface for PostgreSQL.
type PostgresTx struct {
	tx *sql.Tx
}

// Commit commits the transaction.
func (t *PostgresTx) Commit() error {
	return t.tx.Commit()
}

// Rollback rolls back the transaction.
func (t *PostgresTx) Rollback() error {
	return t.tx.Rollback()
}

// CreateHelmChart creates a new Helm chart.
func (p *PostgresDB) CreateHelmChart(ctx context.Context, chart *models.HelmChart) error {
	// Marshal maintainers to JSON
	var maintainersJSON []byte

	if len(chart.Maintainers) > 0 {
		data, err := json.Marshal(chart.Maintainers)
		if err != nil {
			return fmt.Errorf("failed to marshal maintainers: %w", err)
		}

		maintainersJSON = data
	}

	blobID, err := p.upsertBlobWithMediaType(ctx, chart.Digest, chart.BlobKey, "application/gzip", chart.SizeBytes)
	if err != nil {
		return fmt.Errorf("failed to upsert blob for helm chart: %w", err)
	}

	row, err := p.q.CreateHelmChart(ctx, dbsqlc.CreateHelmChartParams{
		CreatedAt:    createdAtOrNil(chart.CreatedAt),
		RepositoryID: chart.RepositoryID,
		Name:         chart.Name,
		Version:      chart.Version,
		AppVersion:   strPtr(chart.AppVersion),
		Description:  strPtr(chart.Description),
		Keywords:     chart.Keywords,
		Home:         strPtr(chart.Home),
		Sources:      chart.Sources,
		Maintainers:  maintainersJSON,
		Icon:         strPtr(chart.Icon),
		ApiVersion:   chart.APIVersion,
		Type:         chart.Type,
		BlobID:       blobID,
	})

	return finishCreate(insertedRow(row), err, "create helm chart", &chart.ID, &chart.CreatedAt, &chart.UpdatedAt)
}

// helmChartFromRow maps a sqlc helm chart row; the list rows convert to it.
// helmChartRow lists the sqlc row types helmChartFromRow converts; they share the columns of GetHelmChartRow.
type helmChartRow interface {
	dbsqlc.GetHelmChartRow | dbsqlc.ListHelmChartsRow
}

func helmChartFromRow[R helmChartRow](in R) *models.HelmChart {
	row := dbsqlc.GetHelmChartRow(in)

	chart := &models.HelmChart{
		ID:           row.ID,
		RepositoryID: row.RepositoryID,
		Name:         row.Name,
		Version:      row.Version,
		AppVersion:   derefStr(row.AppVersion),
		Description:  derefStr(row.Description),
		Keywords:     row.Keywords,
		Home:         derefStr(row.Home),
		Sources:      row.Sources,
		Icon:         derefStr(row.Icon),
		APIVersion:   row.ApiVersion,
		Type:         row.Type,
		BlobKey:      row.BlobKey,
		Digest:       row.Digest,
		SizeBytes:    row.SizeBytes,
		CreatedAt:    row.CreatedAt,
		UpdatedAt:    row.UpdatedAt,
	}

	if len(row.Maintainers) > 0 {
		_ = json.Unmarshal(row.Maintainers, &chart.Maintainers)
	}

	return chart
}

func (p *PostgresDB) GetHelmChart(ctx context.Context, repoID, name, version string) (*models.HelmChart, error) {
	row, err := p.q.GetHelmChart(ctx, dbsqlc.GetHelmChartParams{
		RepositoryID: repoID,
		Name:         name,
		Version:      version,
	})

	return mapRow(row, err, "get helm chart", helmChartFromRow)
}

func (p *PostgresDB) DeleteHelmChart(ctx context.Context, repoID, name, version string) error {
	err := p.q.DeleteHelmChart(ctx, dbsqlc.DeleteHelmChartParams{
		RepositoryID: repoID,
		Name:         name,
		Version:      version,
	})
	if err != nil {
		return fmt.Errorf("failed to delete helm chart: %w", err)
	}

	return nil
}

func (p *PostgresDB) ListHelmCharts(ctx context.Context, repoID string) ([]*models.HelmChart, error) {
	rows, err := p.q.ListHelmCharts(ctx, repoID)

	return mapRowsOrNil(rows, err, "list helm charts", helmChartFromRow)
}

func (p *PostgresDB) CreatePyPIPackage(ctx context.Context, pkg *models.PyPIPackage) error {
	// pypi_packages has no md5_digest column and the blobs table only tracks
	// sha256, so pkg.MD5Digest does not round-trip.
	blobID, err := p.upsertBlobWithMediaType(ctx, pkg.SHA256Digest, pkg.BlobKey, "application/zip", pkg.SizeBytes)
	if err != nil {
		return fmt.Errorf("failed to upsert blob for pypi package: %w", err)
	}

	row, err := p.q.CreatePyPIPackage(ctx, dbsqlc.CreatePyPIPackageParams{
		CreatedAt:      createdAtOrNil(pkg.CreatedAt),
		RepositoryID:   pkg.RepositoryID,
		Name:           pkg.Name,
		Version:        pkg.Version,
		Filename:       pkg.Filename,
		PackageType:    pkg.PackageType,
		PythonVersion:  strPtr(pkg.PythonVersion),
		RequiresPython: strPtr(pkg.RequiresPython),
		Summary:        strPtr(pkg.Summary),
		Author:         strPtr(pkg.Author),
		AuthorEmail:    strPtr(pkg.AuthorEmail),
		License:        strPtr(pkg.License),
		Keywords:       strPtr(pkg.Keywords),
		Classifiers:    pkg.Classifiers,
		BlobID:         blobID,
	})

	return finishCreate(insertedRow(row), err, "create pypi package", &pkg.ID, &pkg.CreatedAt, &pkg.UpdatedAt)
}

// pypiPackageFromRow maps a sqlc PyPI package row; the list rows convert to it.
// pypiPackageRow lists the sqlc row types pypiPackageFromRow converts; they share the columns of GetPyPIPackagesByFilenameRow.
type pypiPackageRow interface {
	dbsqlc.GetPyPIPackagesByFilenameRow | dbsqlc.ListPyPIPackagesRow | dbsqlc.ListPyPIPackageVersionsRow
}

func pypiPackageFromRow[R pypiPackageRow](in R) *models.PyPIPackage {
	row := dbsqlc.GetPyPIPackagesByFilenameRow(in)

	return &models.PyPIPackage{
		ID:             row.ID,
		RepositoryID:   row.RepositoryID,
		Name:           row.Name,
		Version:        row.Version,
		Filename:       row.Filename,
		PackageType:    row.PackageType,
		PythonVersion:  derefStr(row.PythonVersion),
		RequiresPython: derefStr(row.RequiresPython),
		Summary:        derefStr(row.Summary),
		Author:         derefStr(row.Author),
		AuthorEmail:    derefStr(row.AuthorEmail),
		License:        derefStr(row.License),
		Keywords:       derefStr(row.Keywords),
		Classifiers:    row.Classifiers,
		BlobKey:        row.BlobKey,
		SHA256Digest:   row.Sha256Digest,
		SizeBytes:      row.SizeBytes,
		CreatedAt:      row.CreatedAt,
		UpdatedAt:      row.UpdatedAt,
	}
}

func (p *PostgresDB) GetPyPIPackageByFilename(ctx context.Context, repoID, filename string) (*models.PyPIPackage, error) {
	rows, err := p.q.GetPyPIPackagesByFilename(ctx, dbsqlc.GetPyPIPackagesByFilenameParams{RepositoryID: repoID, Filename: filename})
	if err != nil {
		return nil, fmt.Errorf("failed to get pypi package by filename: %w", err)
	}

	switch len(rows) {
	case 0:
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	case 1:
	default:
		return nil, ErrAmbiguousPyPIFilename
	}

	row := rows[0]

	return pypiPackageFromRow(row), nil
}

func (p *PostgresDB) DeletePyPIPackage(ctx context.Context, repoID, name, version, filename string) error {
	err := p.q.DeletePyPIPackage(ctx, dbsqlc.DeletePyPIPackageParams{
		RepositoryID: repoID,
		Name:         name,
		Version:      version,
		Filename:     filename,
	})
	if err != nil {
		return fmt.Errorf("failed to delete pypi package: %w", err)
	}

	return nil
}

func (p *PostgresDB) ListPyPIPackages(ctx context.Context, repoID string) ([]*models.PyPIPackage, error) {
	rows, err := p.q.ListPyPIPackages(ctx, repoID)

	return mapRowsOrNil(rows, err, "list pypi packages", pypiPackageFromRow)
}

func (p *PostgresDB) ListPyPIPackageNames(ctx context.Context, repoID string) ([]string, error) {
	names, err := p.q.ListPyPIPackageNames(ctx, repoID)
	if err != nil {
		return nil, fmt.Errorf("failed to list pypi package names: %w", err)
	}

	return names, nil
}

func (p *PostgresDB) ListPyPIPackageVersions(ctx context.Context, repoID, name string) ([]*models.PyPIPackage, error) {
	rows, err := p.q.ListPyPIPackageVersions(ctx, dbsqlc.ListPyPIPackageVersionsParams{
		RepositoryID: repoID,
		Name:         name,
	})

	return mapRowsOrNil(rows, err, "list pypi package versions", pypiPackageFromRow)
}

// NuGet methods

func (p *PostgresDB) CreateNuGetPackage(ctx context.Context, pkg *models.NuGetPackage) error {
	// PackageHash is SHA512-base64 (~88 chars): too long for blobs.sha256
	// VARCHAR(64) and not a real sha256, so BlobKey (the storage key) doubles
	// as the blob dedup key here, same as the docker_blobs precedent for
	// formats that don't compute a content hash before BlobStore.Put.
	// The schema has no package_hash_algorithm column, so
	// pkg.PackageHashAlgorithm does not round-trip.
	blobID, err := p.upsertBlobWithMediaType(ctx, pkg.BlobKey, pkg.BlobKey, "application/zip", pkg.PackageSize)
	if err != nil {
		return fmt.Errorf("failed to upsert blob for nuget package: %w", err)
	}

	row, err := p.q.CreateNuGetPackage(ctx, dbsqlc.CreateNuGetPackageParams{
		CreatedAt:                createdAtOrNil(pkg.CreatedAt),
		RepositoryID:             pkg.RepositoryID,
		PackageID:                pkg.PackageID,
		Version:                  pkg.Version,
		Title:                    strPtr(pkg.Title),
		Description:              strPtr(pkg.Description),
		Summary:                  strPtr(pkg.Summary),
		Authors:                  strPtr(pkg.Authors),
		Owners:                   strPtr(pkg.Owners),
		Tags:                     strPtr(pkg.Tags),
		ProjectUrl:               strPtr(pkg.ProjectURL),
		LicenseUrl:               strPtr(pkg.LicenseURL),
		IconUrl:                  strPtr(pkg.IconURL),
		RequireLicenseAcceptance: pkg.RequireLicenseAcceptance,
		IsPrerelease:             pkg.IsPrerelease,
		MinClientVersion:         strPtr(pkg.MinClientVersion),
		BlobID:                   blobID,
		DependencyGroups:         pkg.DependencyGroups,
	})
	if errors.Is(err, sql.ErrNoRows) {
		// ON CONFLICT DO NOTHING returned no row: the live (repo, id, version)
		// already exists. Its content is left untouched.
		return ErrDuplicate
	}

	return finishCreate(insertedRow(row), err, "create nuget package", &pkg.ID, &pkg.CreatedAt, &pkg.UpdatedAt)
}

func (p *PostgresDB) GetNuGetPackage(ctx context.Context, repoID, packageID, version string) (*models.NuGetPackage, error) {
	row, err := p.q.GetNuGetPackage(ctx, dbsqlc.GetNuGetPackageParams{
		RepositoryID: repoID,
		PackageID:    packageID,
		Version:      version,
	})

	return mapRow(row, err, "get nuget package", nugetRowToModel)
}

func (p *PostgresDB) DeleteNuGetPackage(ctx context.Context, repoID, packageID, version string) error {
	if err := p.q.DeleteNuGetPackage(ctx, dbsqlc.DeleteNuGetPackageParams{
		RepositoryID: repoID,
		PackageID:    packageID,
		Version:      version,
	}); err != nil {
		return fmt.Errorf("failed to delete nuget package: %w", err)
	}

	return nil
}

// SetNuGetDependencyGroups records the dependency groups of a package that has
// none stored yet. A stored value is never overwritten and updated_at is not
// touched.
func (p *PostgresDB) SetNuGetDependencyGroups(ctx context.Context, repoID, packageID, version string, groups []byte) error {
	if err := p.q.SetNuGetDependencyGroups(ctx, dbsqlc.SetNuGetDependencyGroupsParams{
		DependencyGroups: groups,
		RepositoryID:     repoID,
		PackageID:        packageID,
		Version:          version,
	}); err != nil {
		return fmt.Errorf("failed to set nuget dependency groups: %w", err)
	}

	return nil
}

func (p *PostgresDB) ListNuGetPackages(ctx context.Context, repoID string) ([]*models.NuGetPackage, error) {
	rows, err := p.q.ListNuGetPackages(ctx, repoID)

	return mapRowsOrNil(rows, err, "list nuget packages", nugetRowToModel)
}

func (p *PostgresDB) ListNuGetPackageVersions(ctx context.Context, repoID, packageID string) ([]*models.NuGetPackage, error) {
	rows, err := p.q.ListNuGetPackageVersions(ctx, dbsqlc.ListNuGetPackageVersionsParams{
		RepositoryID: repoID,
		PackageID:    packageID,
	})

	return mapRowsOrNil(rows, err, "list nuget package versions", nugetRowToModel)
}

// SearchNuGetPackages pages over matching packages: skip/take count packages,
// the total is the number of matching packages, and every live version of each
// package on the page is returned (grouped by package id, oldest first).
func (p *PostgresDB) SearchNuGetPackages(ctx context.Context, repoID, query string, skip, take int) ([]*models.NuGetPackage, int, error) {
	searchQuery := "%" + escapeLike(strings.ToLower(query)) + "%"

	total, err := p.q.SearchNuGetPackagesCount(ctx, dbsqlc.SearchNuGetPackagesCountParams{
		RepositoryID: repoID,
		PackageID:    searchQuery,
	})
	if err != nil {
		return nil, 0, fmt.Errorf("failed to count nuget packages: %w", err)
	}

	rows, err := p.q.SearchNuGetPackages(ctx, dbsqlc.SearchNuGetPackagesParams{
		RepositoryID: repoID,
		PackageID:    searchQuery,
		Offset:       toInt32(skip),
		Limit:        toInt32(take),
	})
	if err != nil {
		return nil, 0, fmt.Errorf("failed to search nuget packages: %w", err)
	}

	var packages []*models.NuGetPackage

	for _, row := range rows {
		packages = append(packages, nugetRowToModel(dbsqlc.GetNuGetPackageRow(row)))
	}

	return packages, int(total), nil
}

func (p *PostgresDB) IncrementNuGetDownloadCount(ctx context.Context, repoID, packageID, version string) error {
	if err := p.q.IncrementNuGetDownloadCount(ctx, dbsqlc.IncrementNuGetDownloadCountParams{
		RepositoryID: repoID,
		PackageID:    packageID,
		Version:      version,
	}); err != nil {
		return fmt.Errorf("failed to increment nuget download count: %w", err)
	}

	return nil
}

func (t *PostgresTx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return t.tx.ExecContext(ctx, query, args...)
}

// QueryContext executes a query that returns rows.
func (t *PostgresTx) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return t.tx.QueryContext(ctx, query, args...)
}

// QueryRowContext executes a query that returns a single row.
func (t *PostgresTx) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return t.tx.QueryRowContext(ctx, query, args...)
}

// CreateGoModule creates a new Go module record.
//
// gomod_modules stores two blobs (zip archive + go.mod file) via zip_blob_id/
// mod_blob_id FKs into the global blobs table. The mod
// blob has no dedicated hash field on models.GoModule, so its own blob key is
// used as the blobs.sha256 value (self-referential, mirrors the docker blob
// pattern) — it never collides since blob keys are unique per upload.
func (p *PostgresDB) CreateGoModule(ctx context.Context, mod *models.GoModule) error {
	zipBlobID, err := p.upsertBlobWithMediaType(ctx, mod.ZipHash, mod.ZipBlobKey, "application/zip", mod.ZipSizeBytes)
	if err != nil {
		return fmt.Errorf("failed to upsert zip blob for go module: %w", err)
	}

	modBlobID, err := p.upsertBlobWithMediaType(ctx, mod.ModBlobKey, mod.ModBlobKey, "text/plain", int64(len(mod.GoModContent)))
	if err != nil {
		return fmt.Errorf("failed to upsert mod blob for go module: %w", err)
	}

	row, err := p.q.CreateGoModule(ctx, dbsqlc.CreateGoModuleParams{
		CreatedAt:    createdAtOrNil(mod.CreatedAt),
		RepositoryID: mod.RepositoryID,
		ModulePath:   mod.ModulePath,
		Version:      mod.Version,
		GoModContent: mod.GoModContent,
		ZipBlobID:    &zipBlobID,
		ModBlobID:    &modBlobID,
	})

	return finishCreate(insertedRow(row), err, "create go module", &mod.ID, &mod.CreatedAt, &mod.UpdatedAt)
}

// GetGoModule retrieves a Go module by repo, path, and version.
func (p *PostgresDB) GetGoModule(ctx context.Context, repoID, modulePath, version string) (*models.GoModule, error) {
	row, err := p.q.GetGoModule(ctx, dbsqlc.GetGoModuleParams{
		RepositoryID: repoID,
		ModulePath:   modulePath,
		Version:      version,
	})

	return mapRow(row, err, "get go module", goModuleFromRow)
}

// goModuleRow lists the sqlc row types goModuleFromRow converts.
type goModuleRow interface {
	dbsqlc.GetGoModuleRow | dbsqlc.ListGoModuleVersionsRow | dbsqlc.ListGoModulesByRepoRow
}

func goModuleFromRow[R goModuleRow](in R) *models.GoModule {
	row := dbsqlc.GetGoModuleRow(in)

	return &models.GoModule{
		ID:           row.ID,
		RepositoryID: row.RepositoryID,
		ModulePath:   row.ModulePath,
		Version:      row.Version,
		GoModContent: row.GoModContent,
		ZipBlobKey:   row.ZipBlobKey,
		ModBlobKey:   row.ModBlobKey,
		ZipSizeBytes: row.ZipSizeBytes,
		ZipHash:      row.ZipHash,
		CreatedAt:    row.CreatedAt,
		UpdatedAt:    row.UpdatedAt,
	}
}

// ListGoModuleVersions lists all versions of a module.
func (p *PostgresDB) ListGoModuleVersions(ctx context.Context, repoID, modulePath string) ([]*models.GoModule, error) {
	rows, err := p.q.ListGoModuleVersions(ctx, dbsqlc.ListGoModuleVersionsParams{
		RepositoryID: repoID,
		ModulePath:   modulePath,
	})

	return mapRowsOrNil(rows, err, "list go module versions", goModuleFromRow)
}

// ListGoModulesByRepo lists every module version for a repository. Used
// only by backup export, where ListGoModuleVersions' mandatory module path
// filter would silently drop every other module.
func (p *PostgresDB) ListGoModulesByRepo(ctx context.Context, repoID string) ([]*models.GoModule, error) {
	rows, err := p.q.ListGoModulesByRepo(ctx, repoID)

	return mapRowsOrNil(rows, err, "list go modules by repo", goModuleFromRow)
}

// Cargo methods

// CreateCargoCrate creates a new Cargo crate.
//
// cargo_crates has no size_bytes column; the crate blob row takes its size
// from crate.SizeBytes.
func (p *PostgresDB) CreateCargoCrate(ctx context.Context, crate *models.CargoCrate) error {
	blobID, err := p.upsertBlobWithMediaType(ctx, crate.Checksum, crate.BlobKey, "application/gzip", crate.SizeBytes)
	if err != nil {
		return fmt.Errorf("failed to upsert blob for cargo crate: %w", err)
	}

	row, err := p.q.CreateCargoCrate(ctx, dbsqlc.CreateCargoCrateParams{
		CreatedAt:     createdAtOrNil(crate.CreatedAt),
		RepositoryID:  crate.RepositoryID,
		Name:          crate.Name,
		Version:       crate.Version,
		Description:   strPtr(crate.Description),
		Homepage:      strPtr(crate.Homepage),
		Documentation: strPtr(crate.Documentation),
		Repository:    strPtr(crate.Repository),
		License:       strPtr(crate.License),
		Keywords:      crate.Keywords,
		Categories:    crate.Categories,
		Authors:       crate.Authors,
		Readme:        strPtr(crate.Readme),
		MetadataJson:  crate.MetadataJSON,
		BlobID:        blobID,
		Yanked:        crate.Yanked,
		DownloadCount: int64(crate.DownloadCount),
	})

	return finishCreate(insertedRow(row), err, "create cargo crate", &crate.ID, &crate.CreatedAt, &crate.UpdatedAt)
}

func (p *PostgresDB) GetCargoCrate(ctx context.Context, repoID, name, version string) (*models.CargoCrate, error) {
	row, err := p.q.GetCargoCrate(ctx, dbsqlc.GetCargoCrateParams{
		RepositoryID: repoID,
		Name:         name,
		Version:      version,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get cargo crate: %w", err)
	}

	return &models.CargoCrate{
		ID:            row.ID,
		RepositoryID:  row.RepositoryID,
		Name:          row.Name,
		Version:       row.Version,
		Description:   derefStr(row.Description),
		Homepage:      derefStr(row.Homepage),
		Documentation: derefStr(row.Documentation),
		Repository:    derefStr(row.Repository),
		License:       derefStr(row.License),
		Keywords:      row.Keywords,
		Categories:    row.Categories,
		Authors:       row.Authors,
		Readme:        derefStr(row.Readme),
		MetadataJSON:  row.MetadataJson,
		BlobKey:       row.BlobKey,
		Checksum:      row.Checksum,
		SizeBytes:     row.SizeBytes,
		Yanked:        row.Yanked,
		DownloadCount: int(row.DownloadCount),
		CreatedAt:     row.CreatedAt,
		UpdatedAt:     row.UpdatedAt,
	}, nil
}

// cargoCrateFromRow maps a sqlc cargo crate row; SearchCargoCratesRow converts to it.
func cargoCrateFromRow(row dbsqlc.ListCargoCrateVersionsRow) *models.CargoCrate {
	return &models.CargoCrate{
		ID:            row.ID,
		RepositoryID:  row.RepositoryID,
		Name:          row.Name,
		Version:       row.Version,
		Description:   derefStr(row.Description),
		Homepage:      derefStr(row.Homepage),
		Documentation: derefStr(row.Documentation),
		Repository:    derefStr(row.Repository),
		License:       derefStr(row.License),
		Keywords:      row.Keywords,
		Categories:    row.Categories,
		Authors:       row.Authors,
		Readme:        derefStr(row.Readme),
		MetadataJSON:  row.MetadataJson,
		BlobKey:       row.BlobKey,
		Checksum:      row.Checksum,
		Yanked:        row.Yanked,
		DownloadCount: int(row.DownloadCount),
		CreatedAt:     row.CreatedAt,
		UpdatedAt:     row.UpdatedAt,
	}
}

func (p *PostgresDB) ListCargoCrateVersions(ctx context.Context, repoID, name string) ([]*models.CargoCrate, error) {
	rows, err := p.q.ListCargoCrateVersions(ctx, dbsqlc.ListCargoCrateVersionsParams{
		RepositoryID: repoID,
		Name:         name,
	})

	return mapRowsOrNil(rows, err, "query cargo crate versions", cargoCrateFromRow)
}

func (p *PostgresDB) SearchCargoCrates(ctx context.Context, repoID, query string, limit int) ([]*models.CargoCrate, error) {
	rows, err := p.q.SearchCargoCrates(ctx, dbsqlc.SearchCargoCratesParams{
		RepositoryID: repoID,
		Name:         "%" + escapeLike(query) + "%",
		//nolint:gosec // G115: value is bounded by API validation
		Limit: int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to search cargo crates: %w", err)
	}

	var crates []*models.CargoCrate

	for _, row := range rows {
		crates = append(crates, cargoCrateFromRow(dbsqlc.ListCargoCrateVersionsRow(row)))
	}

	return crates, nil
}

func (p *PostgresDB) SearchCargoCrateSummaries(
	ctx context.Context,
	repoID, query string,
	limit int,
) ([]*models.CargoCrateSummary, int, error) {
	rows, err := p.q.SearchCargoCrateSummaries(ctx, dbsqlc.SearchCargoCrateSummariesParams{
		RepositoryID: repoID,
		Name:         "%" + escapeLike(query) + "%",
		//nolint:gosec // G115: value is bounded by API validation
		Limit: int32(limit),
	})
	if err != nil {
		return nil, 0, fmt.Errorf("failed to search cargo crates: %w", err)
	}

	var total int

	summaries := make([]*models.CargoCrateSummary, 0, len(rows))

	for _, row := range rows {
		total = int(row.Total)

		summaries = append(summaries, &models.CargoCrateSummary{
			Name:         row.Name,
			Description:  row.Description,
			Downloads:    row.Downloads,
			Versions:     row.Versions,
			LiveVersions: row.LiveVersions,
		})
	}

	return summaries, total, nil
}

func (p *PostgresDB) YankCargoCrate(ctx context.Context, repoID, name, version string, yanked bool) error {
	err := p.q.YankCargoCrate(ctx, dbsqlc.YankCargoCrateParams{
		RepositoryID: repoID,
		Name:         name,
		Version:      version,
		Yanked:       yanked,
	})
	if err != nil {
		return fmt.Errorf("failed to yank cargo crate: %w", err)
	}

	return nil
}

func (p *PostgresDB) IncrementCargoDownloadCount(ctx context.Context, repoID, name, version string) error {
	err := p.q.IncrementCargoDownloadCount(ctx, dbsqlc.IncrementCargoDownloadCountParams{
		RepositoryID: repoID,
		Name:         name,
		Version:      version,
	})
	if err != nil {
		return fmt.Errorf("failed to increment cargo download count: %w", err)
	}

	return nil
}

// Helper functions for nullable types with pgx/v5.
func strPtr(s string) *string {
	if s == "" {
		return nil
	}

	return &s
}

func derefStr(s *string) string {
	if s == nil {
		return ""
	}

	return *s
}

// nugetRowToModel converts any of the four structurally-identical nuget row
// types (Get/List/ListVersions/Search all share the same SELECT list) via
// Go's struct conversion rule (identical field sequence, tags ignored).
// The schema has no package_hash_algorithm column, so PackageHashAlgorithm
// never round-trips.
// nugetPackageRow lists the sqlc row types nugetRowToModel converts; they share the columns of GetNuGetPackageRow.
type nugetPackageRow interface {
	dbsqlc.GetNuGetPackageRow | dbsqlc.ListNuGetPackagesRow | dbsqlc.ListNuGetPackageVersionsRow
}

func nugetRowToModel[R nugetPackageRow](in R) *models.NuGetPackage {
	row := dbsqlc.GetNuGetPackageRow(in)

	return &models.NuGetPackage{
		ID:                       row.ID,
		RepositoryID:             row.RepositoryID,
		PackageID:                row.PackageID,
		Version:                  row.Version,
		Title:                    derefStr(row.Title),
		Description:              derefStr(row.Description),
		Summary:                  derefStr(row.Summary),
		Authors:                  derefStr(row.Authors),
		Owners:                   derefStr(row.Owners),
		Tags:                     derefStr(row.Tags),
		ProjectURL:               derefStr(row.ProjectUrl),
		LicenseURL:               derefStr(row.LicenseUrl),
		IconURL:                  derefStr(row.IconUrl),
		RequireLicenseAcceptance: row.RequireLicenseAcceptance,
		IsPrerelease:             row.IsPrerelease,
		MinClientVersion:         derefStr(row.MinClientVersion),
		DependencyGroups:         row.DependencyGroups,
		BlobKey:                  row.BlobKey,
		PackageHash:              row.PackageHash,
		PackageSize:              row.PackageSize,
		DownloadCount:            int(row.DownloadCount),
		CreatedAt:                row.CreatedAt,
		UpdatedAt:                row.UpdatedAt,
	}
}

// RubyGems methods

// CreateRubyGem creates a new RubyGem.
//
// gem.Size is persisted on the row (for quota release) and recorded as the
// blob's size_bytes.
func (p *PostgresDB) CreateRubyGem(ctx context.Context, gem *models.RubyGem) error {
	blobID, err := p.upsertBlobWithMediaType(ctx, gem.ChecksumSHA256, gem.BlobKey, "application/gzip", gem.Size)
	if err != nil {
		return fmt.Errorf("failed to upsert blob for ruby gem: %w", err)
	}

	row, err := p.q.CreateRubyGem(ctx, dbsqlc.CreateRubyGemParams{
		CreatedAt:     createdAtOrNil(gem.CreatedAt),
		RepositoryID:  gem.RepositoryID,
		Name:          gem.Name,
		Version:       gem.Version,
		Platform:      gem.Platform,
		Description:   strPtr(gem.Description),
		Summary:       strPtr(gem.Summary),
		Homepage:      strPtr(gem.Homepage),
		Authors:       gem.Authors,
		Licenses:      gem.Licenses,
		Dependencies:  gem.Dependencies,
		MetadataJson:  gem.MetadataJSON,
		BlobID:        blobID,
		Yanked:        gem.Yanked,
		DownloadCount: int64(gem.DownloadCount),
		Size:          gem.Size,
	})

	return finishCreate(insertedRow(row), err, "create ruby gem", &gem.ID, &gem.CreatedAt, &gem.UpdatedAt)
}

// rubyGemFromRow maps a sqlc ruby gem row to the model. The List/Search row
// types share GetRubyGemRow's layout and are converted to it by the callers.
// rubyGemRow lists the sqlc row types rubyGemFromRow converts; they share the columns of GetRubyGemRow.
type rubyGemRow interface {
	dbsqlc.GetRubyGemRow | dbsqlc.ListRubyGemsRow | dbsqlc.ListRubyGemVersionsRow
}

func rubyGemFromRow[R rubyGemRow](in R) *models.RubyGem {
	row := dbsqlc.GetRubyGemRow(in)

	return &models.RubyGem{
		ID:             row.ID,
		RepositoryID:   row.RepositoryID,
		Name:           row.Name,
		Version:        row.Version,
		Platform:       row.Platform,
		Description:    derefStr(row.Description),
		Summary:        derefStr(row.Summary),
		Homepage:       derefStr(row.Homepage),
		Authors:        row.Authors,
		Licenses:       row.Licenses,
		Dependencies:   row.Dependencies,
		MetadataJSON:   row.MetadataJson,
		BlobKey:        row.BlobKey,
		ChecksumSHA256: row.ChecksumSha256,
		Yanked:         row.Yanked,
		DownloadCount:  int(row.DownloadCount),
		Size:           row.Size,
		CreatedAt:      row.CreatedAt,
		UpdatedAt:      row.UpdatedAt,
	}
}

func (p *PostgresDB) GetRubyGem(ctx context.Context, repoID, name, version, platform string) (*models.RubyGem, error) {
	row, err := p.q.GetRubyGem(ctx, dbsqlc.GetRubyGemParams{
		RepositoryID: repoID,
		Name:         name,
		Version:      version,
		Platform:     platform,
	})

	return mapRow(row, err, "get ruby gem", rubyGemFromRow)
}

func (p *PostgresDB) ListRubyGems(ctx context.Context, repoID string) ([]*models.RubyGem, error) {
	rows, err := p.q.ListRubyGems(ctx, repoID)

	return mapRowsOrNil(rows, err, "query ruby gems", rubyGemFromRow)
}

func (p *PostgresDB) ListRubyGemVersions(ctx context.Context, repoID, name string) ([]*models.RubyGem, error) {
	rows, err := p.q.ListRubyGemVersions(ctx, dbsqlc.ListRubyGemVersionsParams{
		RepositoryID: repoID,
		Name:         name,
	})

	return mapRowsOrNil(rows, err, "query ruby gem versions", rubyGemFromRow)
}

func (p *PostgresDB) SearchRubyGems(ctx context.Context, repoID, query string) ([]*models.RubyGem, error) {
	rows, err := p.q.SearchRubyGems(ctx, dbsqlc.SearchRubyGemsParams{
		RepositoryID: repoID,
		Name:         "%" + escapeLike(query) + "%",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to search ruby gems: %w", err)
	}

	var gems []*models.RubyGem

	for _, row := range rows {
		gems = append(gems, rubyGemFromRow(dbsqlc.GetRubyGemRow(row)))
	}

	return gems, nil
}

func (p *PostgresDB) YankRubyGem(ctx context.Context, repoID, name, version, platform string, yanked bool) error {
	err := p.q.YankRubyGem(ctx, dbsqlc.YankRubyGemParams{
		RepositoryID: repoID,
		Name:         name,
		Version:      version,
		Column5:      platform,
		Yanked:       yanked,
	})
	if err != nil {
		return fmt.Errorf("failed to yank ruby gem: %w", err)
	}

	return nil
}

// UpsertRawArtifact inserts or updates a raw artifact.
func (p *PostgresDB) UpsertRawArtifact(ctx context.Context, artifact *models.RawArtifact) error {
	_, err := p.writeRawArtifact(ctx, artifact, false)

	return err
}

// InsertRawArtifactIfAbsent inserts a raw artifact only when no live row
// exists at its path, in one atomic statement. It reports whether the row was
// inserted; false means the path is taken and nothing was changed.
func (p *PostgresDB) InsertRawArtifactIfAbsent(ctx context.Context, artifact *models.RawArtifact) (bool, error) {
	return p.writeRawArtifact(ctx, artifact, true)
}

func (p *PostgresDB) writeRawArtifact(ctx context.Context, artifact *models.RawArtifact, ifAbsent bool) (bool, error) {
	metadataJSON, err := json.Marshal(artifact.Metadata)
	if err != nil {
		return false, fmt.Errorf("failed to marshal metadata: %w", err)
	}

	blobID, err := p.upsertBlobWithMediaType(ctx, artifact.SHA256, artifact.BlobKey, artifact.ContentType, artifact.SizeBytes)
	if err != nil {
		return false, fmt.Errorf("failed to upsert blob for raw artifact: %w", err)
	}

	// Callers (the raw PUT handler) leave the timestamps unset; storing the
	// zero time made every raw artifact show as uploaded on 0001-01-01.
	now := time.Now().UTC()
	if artifact.CreatedAt.IsZero() {
		artifact.CreatedAt = now
	}

	if artifact.UpdatedAt.IsZero() {
		artifact.UpdatedAt = now
	}

	inserted := true

	if ifAbsent {
		n, err := p.q.InsertRawArtifactIfAbsent(ctx, dbsqlc.InsertRawArtifactIfAbsentParams{
			ID:           artifact.ID,
			RepositoryID: artifact.RepositoryID,
			Path:         artifact.Path,
			ContentType:  artifact.ContentType,
			BlobID:       blobID,
			Metadata:     json.RawMessage(metadataJSON),
			CreatedAt:    artifact.CreatedAt,
			UpdatedAt:    artifact.UpdatedAt,
		})
		if err != nil {
			return false, fmt.Errorf("failed to insert raw artifact: %w", err)
		}

		inserted = n > 0
	} else if err := p.q.UpsertRawArtifact(ctx, dbsqlc.UpsertRawArtifactParams{
		ID:           artifact.ID,
		RepositoryID: artifact.RepositoryID,
		Path:         artifact.Path,
		ContentType:  artifact.ContentType,
		BlobID:       blobID,
		Metadata:     json.RawMessage(metadataJSON),
		CreatedAt:    artifact.CreatedAt,
		UpdatedAt:    artifact.UpdatedAt,
	}); err != nil {
		return false, fmt.Errorf("failed to upsert raw artifact: %w", err)
	}

	artifact.BlobID = blobID

	return inserted, nil
}

// GetRawArtifact retrieves a raw artifact by repo ID and path.
func (p *PostgresDB) GetRawArtifact(ctx context.Context, repoID, path string) (*models.RawArtifact, error) {
	row, err := p.q.GetRawArtifact(ctx, dbsqlc.GetRawArtifactParams{
		RepositoryID: repoID,
		Path:         path,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get raw artifact: %w", err)
	}

	var metadata map[string]any
	if err := json.Unmarshal(row.Metadata, &metadata); err != nil {
		return nil, fmt.Errorf("failed to unmarshal metadata: %w", err)
	}

	return &models.RawArtifact{
		ID:           row.ID,
		RepositoryID: row.RepositoryID,
		Path:         row.Path,
		ContentType:  row.ContentType,
		BlobID:       row.BlobID,
		SizeBytes:    row.SizeBytes,
		SHA256:       row.Sha256,
		BlobKey:      row.BlobKey,
		Metadata:     metadata,
		CreatedAt:    row.CreatedAt,
		UpdatedAt:    row.UpdatedAt,
	}, nil
}

// DeleteRawArtifact deletes a raw artifact by repo ID and path.
func (p *PostgresDB) DeleteRawArtifact(ctx context.Context, repoID, path string) error {
	if err := p.q.DeleteRawArtifact(ctx, dbsqlc.DeleteRawArtifactParams{
		RepositoryID: repoID,
		Path:         path,
	}); err != nil {
		return fmt.Errorf("failed to delete raw artifact: %w", err)
	}

	return nil
}

// ListRawArtifacts lists raw artifacts in a repository, optionally filtered by prefix.
func (p *PostgresDB) ListRawArtifacts(ctx context.Context, repoID, prefix string) ([]*models.RawArtifact, error) {
	var dbRows []dbsqlc.ListRawArtifactsRow

	var err error

	if prefix == "" {
		dbRows, err = p.q.ListRawArtifacts(ctx, repoID)
	} else {
		prefixRows, prefixErr := p.q.ListRawArtifactsByPrefix(ctx, dbsqlc.ListRawArtifactsByPrefixParams{
			RepositoryID: repoID,
			Path:         escapeLike(prefix) + "%",
		})

		err = prefixErr
		dbRows = make([]dbsqlc.ListRawArtifactsRow, len(prefixRows))

		for i, r := range prefixRows {
			dbRows[i] = dbsqlc.ListRawArtifactsRow(r)
		}
	}

	if err != nil {
		return nil, fmt.Errorf("failed to list raw artifacts: %w", err)
	}

	artifacts := make([]*models.RawArtifact, 0, len(dbRows))

	for _, row := range dbRows {
		var metadata map[string]any
		if err := json.Unmarshal(row.Metadata, &metadata); err != nil {
			return nil, fmt.Errorf("failed to unmarshal metadata: %w", err)
		}

		artifacts = append(artifacts, &models.RawArtifact{
			ID:           row.ID,
			RepositoryID: row.RepositoryID,
			Path:         row.Path,
			ContentType:  row.ContentType,
			BlobID:       row.BlobID,
			SizeBytes:    row.SizeBytes,
			SHA256:       row.Sha256,
			BlobKey:      row.BlobKey,
			Metadata:     metadata,
			CreatedAt:    row.CreatedAt,
			UpdatedAt:    row.UpdatedAt,
		})
	}

	return artifacts, nil
}

func vulnScanStatusFromJob(status string) string {
	if status == "running" {
		return "scanning"
	}

	return status
}

// Project methods

// CreateCondaPackage creates a new Conda package file. An empty
// pkg.Extension is stored as ".tar.bz2" and an empty pkg.Extra as "{}".
// A live row for the same (name, version, build, platform, extension) yields
// ErrDuplicate and is left untouched.
func (p *PostgresDB) CreateCondaPackage(ctx context.Context, pkg *models.CondaPackage) error {
	blobID, err := p.upsertBlobWithMediaType(ctx, pkg.SHA256, pkg.BlobKey, "application/x-conda", pkg.SizeBytes)
	if err != nil {
		return fmt.Errorf("failed to upsert blob for conda package: %w", err)
	}

	ext := pkg.Extension
	if ext == "" {
		ext = models.CondaExtTarBz2
	}

	extra := pkg.Extra
	if len(extra) == 0 || string(extra) == "null" {
		extra = json.RawMessage(`{}`)
	}

	depends := pkg.Depends
	if depends == nil {
		depends = []string{}
	}

	row, err := p.q.CreateCondaPackage(ctx, dbsqlc.CreateCondaPackageParams{
		CreatedAt:    createdAtOrNil(pkg.CreatedAt),
		RepositoryID: pkg.RepositoryID,
		Name:         pkg.Name,
		Version:      pkg.Version,
		BuildString:  pkg.BuildString,
		Platform:     pkg.Platform,
		Depends:      depends,
		BlobID:       blobID,
		Extension:    ext,
		BuildNumber:  toInt32(pkg.BuildNumber),
		Md5:          pkg.MD5,
		TimestampMs:  pkg.Timestamp,
		IndexExtra:   extra,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return ErrDuplicate
	}

	if err != nil {
		return fmt.Errorf("failed to create conda package: %w", err)
	}

	pkg.ID = row.ID
	pkg.Extension = ext
	pkg.CreatedAt = row.CreatedAt
	pkg.UpdatedAt = row.UpdatedAt

	return nil
}

// GetCondaPackageFile returns the live package file with this exact
// name, version, build, platform and extension, or nil.
func (p *PostgresDB) GetCondaPackageFile(
	ctx context.Context,
	repoID, name, version, buildString, platform, extension string,
) (*models.CondaPackage, error) {
	row, err := p.q.GetCondaPackageFile(ctx, dbsqlc.GetCondaPackageFileParams{
		RepositoryID: repoID,
		Name:         name,
		Version:      version,
		BuildString:  buildString,
		Platform:     platform,
		Extension:    extension,
	})

	return mapRow(row, err, "get conda package", condaRowToModel)
}

// condaRowToModel converts any of the three structurally identical conda row
// types (GetFile/List/ListAll share the same SELECT list).
// condaPackageRow lists the sqlc row types condaRowToModel converts; they share the columns of GetCondaPackageFileRow.
type condaPackageRow interface {
	dbsqlc.GetCondaPackageFileRow | dbsqlc.ListCondaPackagesRow | dbsqlc.ListAllCondaPackagesRow
}

func condaRowToModel[R condaPackageRow](in R) *models.CondaPackage {
	row := dbsqlc.GetCondaPackageFileRow(in)

	return &models.CondaPackage{
		ID:           row.ID,
		RepositoryID: row.RepositoryID,
		Name:         row.Name,
		Version:      row.Version,
		BuildString:  row.BuildString,
		Platform:     row.Platform,
		Extension:    row.Extension,
		BuildNumber:  int(row.BuildNumber),
		Timestamp:    row.TimestampMs,
		Depends:      row.Depends,
		Extra:        row.IndexExtra,
		BlobKey:      row.BlobKey,
		SizeBytes:    row.SizeBytes,
		SHA256:       row.Sha256,
		MD5:          row.Md5,
		CreatedAt:    row.CreatedAt,
		UpdatedAt:    row.UpdatedAt,
	}
}

func (p *PostgresDB) DeleteCondaPackage(ctx context.Context, id string) error {
	err := p.q.DeleteCondaPackage(ctx, id)
	if err != nil {
		return fmt.Errorf("failed to delete conda package: %w", err)
	}

	return nil
}

func (p *PostgresDB) ListCondaPackages(ctx context.Context, repoID, platform string) ([]*models.CondaPackage, error) {
	rows, err := p.q.ListCondaPackages(ctx, dbsqlc.ListCondaPackagesParams{
		RepositoryID: repoID,
		Platform:     platform,
	})

	return mapRowsOrNil(rows, err, "query conda packages", condaRowToModel)
}

// ListAllCondaPackages lists all conda packages for a repository across
// every platform. Used only by backup export, where ListCondaPackages'
// mandatory platform filter would silently drop everything else.
func (p *PostgresDB) ListAllCondaPackages(ctx context.Context, repoID string) ([]*models.CondaPackage, error) {
	rows, err := p.q.ListAllCondaPackages(ctx, repoID)

	return mapRowsOrNil(rows, err, "query all conda packages", condaRowToModel)
}

// CreateConanPackage stores one Conan file, replacing the blob of the live row
// with the same (recipe revision, package id, package revision, filename).
//
// conan_packages.blob_id is nullable; rows restored from a backup that predates
// the sha256 field carry only the blob key, which then stands in for the digest.
func (p *PostgresDB) CreateConanPackage(ctx context.Context, pkg *models.ConanPackage) error {
	var blobID *string

	if pkg.BlobKey != "" {
		sha := pkg.SHA256
		if sha == "" {
			sha = pkg.BlobKey
		}

		id, err := p.upsertBlobWithMediaType(ctx, sha, pkg.BlobKey, "application/octet-stream", pkg.SizeBytes)
		if err != nil {
			return fmt.Errorf("failed to upsert blob for conan package: %w", err)
		}

		blobID = &id
	}

	row, err := p.q.CreateConanPackage(ctx, dbsqlc.CreateConanPackageParams{
		CreatedAt:       createdAtOrNil(pkg.CreatedAt),
		RepositoryID:    pkg.RepositoryID,
		Name:            pkg.Name,
		Version:         pkg.Version,
		UserChannel:     pkg.UserChannel,
		RecipeRevision:  pkg.RecipeRevision,
		PackageID:       pkg.PackageID,
		PackageRevision: pkg.PackageRevision,
		Filename:        pkg.Filename,
		BlobID:          blobID,
		Md5:             pkg.MD5,
		Sha1:            pkg.SHA1,
	})

	return finishCreate(insertedRow(row), err, "create conan package", &pkg.ID, &pkg.CreatedAt, &pkg.UpdatedAt)
}

// conanPackageFromRow converts a conan_packages row joined with its blob.
// conanPackageRow lists the sqlc row types conanPackageFromRow converts; they share the columns of GetConanPackageRow.
type conanPackageRow interface {
	dbsqlc.GetConanPackageRow | dbsqlc.ListConanPackagesRow | dbsqlc.FindConanPackageBySHA1Row
}

func conanPackageFromRow[R conanPackageRow](in R) *models.ConanPackage {
	row := dbsqlc.GetConanPackageRow(in)

	return &models.ConanPackage{
		ID:              row.ID,
		RepositoryID:    row.RepositoryID,
		Name:            row.Name,
		Version:         row.Version,
		UserChannel:     row.UserChannel,
		RecipeRevision:  row.RecipeRevision,
		PackageID:       row.PackageID,
		PackageRevision: row.PackageRevision,
		Filename:        row.Filename,
		BlobKey:         row.BlobKey,
		SHA256:          row.Sha256,
		SizeBytes:       row.SizeBytes,
		MD5:             row.Md5,
		SHA1:            row.Sha1,
		CreatedAt:       row.CreatedAt,
		UpdatedAt:       row.UpdatedAt,
	}
}

// GetConanPackage returns the live file stored under key, or (nil, nil).
func (p *PostgresDB) GetConanPackage(ctx context.Context, repoID string, key models.ConanFileKey) (*models.ConanPackage, error) {
	row, err := p.q.GetConanPackage(ctx, dbsqlc.GetConanPackageParams{
		RepositoryID:    repoID,
		Name:            key.Name,
		Version:         key.Version,
		UserChannel:     key.UserChannel,
		RecipeRevision:  key.RecipeRevision,
		PackageID:       key.PackageID,
		PackageRevision: key.PackageRevision,
		Filename:        key.Filename,
	})

	return mapRow(row, err, "get conan package", conanPackageFromRow)
}

// ListConanRecipeFiles returns every live file of one recipe (all its recipe
// and package revisions), oldest first.
func (p *PostgresDB) ListConanRecipeFiles(ctx context.Context, repoID, name, version, userChannel string) ([]*models.ConanPackage, error) {
	rows, err := p.q.ListConanRecipeFiles(ctx, dbsqlc.ListConanRecipeFilesParams{
		RepositoryID: repoID,
		Name:         name,
		Version:      version,
		UserChannel:  userChannel,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to query conan recipe files: %w", err)
	}

	files := make([]*models.ConanPackage, 0, len(rows))

	for _, row := range rows {
		files = append(files, conanPackageFromRow(dbsqlc.GetConanPackageRow(row)))
	}

	return files, nil
}

// ListConanRecipes returns the distinct recipes that have at least one file.
func (p *PostgresDB) ListConanRecipes(ctx context.Context, repoID string) ([]models.ConanRecipe, error) {
	rows, err := p.q.ListConanRecipes(ctx, repoID)
	if err != nil {
		return nil, fmt.Errorf("failed to query conan recipes: %w", err)
	}

	recipes := make([]models.ConanRecipe, 0, len(rows))

	for _, row := range rows {
		recipes = append(recipes, models.ConanRecipe{Name: row.Name, Version: row.Version, UserChannel: row.UserChannel})
	}

	return recipes, nil
}

// FindConanPackageBySHA1 returns a live file of the repository whose content
// has the given SHA-1, or (nil, nil).
func (p *PostgresDB) FindConanPackageBySHA1(ctx context.Context, repoID, sha1 string) (*models.ConanPackage, error) {
	row, err := p.q.FindConanPackageBySHA1(ctx, dbsqlc.FindConanPackageBySHA1Params{RepositoryID: repoID, Sha1: sha1})

	return mapRow(row, err, "find conan package by sha1", conanPackageFromRow)
}

// SetConanPackageDigests records the MD5 and SHA-1 of a file stored without
// them.
func (p *PostgresDB) SetConanPackageDigests(ctx context.Context, id, md5, sha1 string) error {
	if err := p.q.SetConanPackageDigests(ctx, dbsqlc.SetConanPackageDigestsParams{ID: id, Md5: md5, Sha1: sha1}); err != nil {
		return fmt.Errorf("failed to set conan package digests: %w", err)
	}

	return nil
}

func (p *PostgresDB) DeleteConanPackage(ctx context.Context, id string) error {
	if err := p.q.DeleteConanPackage(ctx, id); err != nil {
		return fmt.Errorf("failed to delete conan package: %w", err)
	}

	return nil
}

func (p *PostgresDB) ListConanPackages(ctx context.Context, repoID string) ([]*models.ConanPackage, error) {
	rows, err := p.q.ListConanPackages(ctx, repoID)

	return mapRowsOrNil(rows, err, "query conan packages", conanPackageFromRow)
}

// CreateIvyArtifact stores an Ivy artifact; a live artifact with the same
// coordinates is repointed at the new blob.
func (p *PostgresDB) CreateIvyArtifact(ctx context.Context, artifact *models.IvyArtifact) error {
	blobID, err := p.upsertBlobWithMediaType(ctx, artifact.SHA256, artifact.BlobKey, "application/java-archive", artifact.SizeBytes)
	if err != nil {
		return fmt.Errorf("failed to upsert blob for ivy artifact: %w", err)
	}

	row, err := p.q.CreateIvyArtifact(ctx, dbsqlc.CreateIvyArtifactParams{
		CreatedAt:    createdAtOrNil(artifact.CreatedAt),
		RepositoryID: artifact.RepositoryID,
		Organisation: artifact.Organisation,
		Module:       artifact.Module,
		Revision:     artifact.Revision,
		ArtifactName: artifact.ArtifactName,
		ArtifactType: artifact.ArtifactType,
		ArtifactExt:  artifact.ArtifactExt,
		BlobID:       blobID,
	})

	return finishCreate(insertedRow(row), err, "create ivy artifact", &artifact.ID, &artifact.CreatedAt, &artifact.UpdatedAt)
}

func (p *PostgresDB) GetIvyArtifact(
	ctx context.Context,
	repoID, organisation, module, revision, artifactName, artifactExt string,
) (*models.IvyArtifact, error) {
	row, err := p.q.GetIvyArtifact(ctx, dbsqlc.GetIvyArtifactParams{
		RepositoryID: repoID,
		Organisation: organisation,
		Module:       module,
		Revision:     revision,
		ArtifactName: artifactName,
		ArtifactExt:  artifactExt,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get ivy artifact: %w", err)
	}

	return &models.IvyArtifact{
		ID:           row.ID,
		RepositoryID: row.RepositoryID,
		Organisation: row.Organisation,
		Module:       row.Module,
		Revision:     row.Revision,
		ArtifactName: row.ArtifactName,
		ArtifactType: row.ArtifactType,
		ArtifactExt:  row.ArtifactExt,
		BlobKey:      row.BlobKey,
		SizeBytes:    row.SizeBytes,
		SHA256:       row.Sha256,
		CreatedAt:    row.CreatedAt,
		UpdatedAt:    row.UpdatedAt,
	}, nil
}

func (p *PostgresDB) DeleteIvyArtifact(ctx context.Context, id string) error {
	if err := p.q.DeleteIvyArtifact(ctx, id); err != nil {
		return fmt.Errorf("failed to delete ivy artifact: %w", err)
	}

	return nil
}

func (p *PostgresDB) ListIvyArtifacts(ctx context.Context, repoID string) ([]*models.IvyArtifact, error) {
	rows, err := p.q.ListIvyArtifacts(ctx, repoID)
	if err != nil {
		return nil, fmt.Errorf("failed to query ivy artifacts: %w", err)
	}

	var artifacts []*models.IvyArtifact

	for _, row := range rows {
		artifacts = append(artifacts, &models.IvyArtifact{
			ID:           row.ID,
			RepositoryID: row.RepositoryID,
			Organisation: row.Organisation,
			Module:       row.Module,
			Revision:     row.Revision,
			ArtifactName: row.ArtifactName,
			ArtifactType: row.ArtifactType,
			ArtifactExt:  row.ArtifactExt,
			BlobKey:      row.BlobKey,
			SizeBytes:    row.SizeBytes,
			SHA256:       row.Sha256,
			CreatedAt:    row.CreatedAt,
			UpdatedAt:    row.UpdatedAt,
		})
	}

	return artifacts, nil
}

func (p *PostgresDB) IvyModuleExists(ctx context.Context, repoID, organisation, module string) (bool, error) {
	exists, err := p.q.IvyModuleExists(ctx, dbsqlc.IvyModuleExistsParams{RepositoryID: repoID, Organisation: organisation, Module: module})
	if err != nil {
		return false, fmt.Errorf("failed to check ivy module: %w", err)
	}

	return exists, nil
}

func (p *PostgresDB) SBTModuleExists(ctx context.Context, repoID, groupID, artifactID string) (bool, error) {
	exists, err := p.q.SBTModuleExists(ctx, dbsqlc.SBTModuleExistsParams{RepositoryID: repoID, GroupID: groupID, ArtifactID: artifactID})
	if err != nil {
		return false, fmt.Errorf("failed to check sbt artifact: %w", err)
	}

	return exists, nil
}

// CreateSBTArtifact stores an SBT artifact; a live artifact with the same
// coordinates is repointed at the new blob.
func (p *PostgresDB) CreateSBTArtifact(ctx context.Context, artifact *models.SBTArtifact) error {
	blobID, err := p.upsertBlobWithMediaType(ctx, artifact.SHA256, artifact.BlobKey, "application/java-archive", artifact.SizeBytes)
	if err != nil {
		return fmt.Errorf("failed to upsert blob for sbt artifact: %w", err)
	}

	row, err := p.q.CreateSBTArtifact(ctx, dbsqlc.CreateSBTArtifactParams{
		CreatedAt:    createdAtOrNil(artifact.CreatedAt),
		RepositoryID: artifact.RepositoryID,
		GroupID:      artifact.GroupID,
		ArtifactID:   artifact.ArtifactID,
		Version:      artifact.Version,
		Filename:     artifact.Filename,
		BlobID:       blobID,
	})

	return finishCreate(insertedRow(row), err, "create sbt artifact", &artifact.ID, &artifact.CreatedAt, &artifact.UpdatedAt)
}

func (p *PostgresDB) GetSBTArtifact(
	ctx context.Context,
	repoID, groupID, artifactID, version, filename string,
) (*models.SBTArtifact, error) {
	row, err := p.q.GetSBTArtifact(ctx, dbsqlc.GetSBTArtifactParams{
		RepositoryID: repoID,
		GroupID:      groupID,
		ArtifactID:   artifactID,
		Version:      version,
		Filename:     filename,
	})

	return mapRow(row, err, "get sbt artifact", sbtArtifactFromRow)
}

func sbtArtifactFromRow(row dbsqlc.GetSBTArtifactRow) *models.SBTArtifact {
	return &models.SBTArtifact{
		ID:           row.ID,
		RepositoryID: row.RepositoryID,
		GroupID:      row.GroupID,
		ArtifactID:   row.ArtifactID,
		Version:      row.Version,
		Filename:     row.Filename,
		BlobKey:      row.BlobKey,
		SizeBytes:    row.SizeBytes,
		SHA256:       row.Sha256,
		CreatedAt:    row.CreatedAt,
		UpdatedAt:    row.UpdatedAt,
	}
}

func (p *PostgresDB) DeleteSBTArtifact(ctx context.Context, id string) error {
	if err := p.q.DeleteSBTArtifact(ctx, id); err != nil {
		return fmt.Errorf("failed to delete sbt artifact: %w", err)
	}

	return nil
}

func (p *PostgresDB) ListSBTArtifacts(ctx context.Context, repoID string) ([]*models.SBTArtifact, error) {
	rows, err := p.q.ListSBTArtifacts(ctx, repoID)

	return mapRowsOrNil(rows, err, "query sbt artifacts", func(r dbsqlc.ListSBTArtifactsRow) *models.SBTArtifact {
		return sbtArtifactFromRow(dbsqlc.GetSBTArtifactRow(r))
	})
}
