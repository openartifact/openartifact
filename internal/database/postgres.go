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
	"net/http"
	"net/netip"
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
	// ErrCreateComponentDeprecated is returned by CreateComponent, which was
	// split into CreateDebPackage and CreateRpmPackage.
	ErrCreateComponentDeprecated = errors.New("CreateComponent is deprecated: use CreateDebPackage or CreateRpmPackage, passing distribution_id and component_id for DEB")
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

// idString returns v as a string; sqlc maps some uuid columns to any.
func idString(v any) string {
	s, _ := v.(string)

	return s
}

// PostgresDB implements the Database interface for PostgreSQL.
type PostgresDB struct {
	db   *sql.DB
	pool *pgxpool.Pool
	q    *dbsqlc.Queries
	log  *logger.Logger

	// authCache serves the per-request auth lookups (see auth_cache.go).
	authCache authCache

	// trashDefaultDays is the retention, in days, of repositories without their
	// own trash_retention_days (see SetTrashRetentionDays).
	trashDefaultDays atomic.Int32

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

	return &PostgresDB{
		db:        db,
		pool:      pool,
		q:         dbsqlc.New(pool),
		log:       log,
		lockSlots: lockSlots,
		authCache: authCache{ttl: time.Duration(cfg.AuthCacheTTLMillis) * time.Millisecond},
	}, nil
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
func (p *PostgresDB) GetDB() *sql.DB {
	return p.db
}

// Close closes the database connection.
func (p *PostgresDB) Close() error {
	if p.pool != nil {
		p.pool.Close()
		if p.log != nil {
			p.log.Debugw("pgx pool closed")
		}
	}
	return p.db.Close()
}

// Ping checks if the database is accessible.
func (p *PostgresDB) Ping(ctx context.Context) error {
	return p.db.PingContext(ctx)
}

// BeginTx starts a new transaction.
func (p *PostgresDB) BeginTx(ctx context.Context) (Transaction, error) {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}

	return &PostgresTx{tx: tx}, nil
}

// Migrate runs database migrations.
func (p *PostgresDB) Migrate(ctx context.Context) error {
	return NewMigrator(p.db).Apply(ctx)
}

// repoColumns is the column list every repository read selects; repoScanDest
// returns the matching Scan destinations.
const repoColumns = `id, name, format, description, COALESCE(public, false), COALESCE(allow_full_unpublish, false), COALESCE(repo_type, 'hosted'), COALESCE(upstream_url, ''), COALESCE(upstream_username, ''), COALESCE(upstream_password_enc, ''), COALESCE(cache_ttl_seconds, 3600), COALESCE(negative_cache_ttl_seconds, 60), COALESCE(max_stale_seconds, 604800), COALESCE(blocked, false), project_id, presign_downloads, trash_retention_days, immutable_releases, merge_proxy_versions, deleted_at, COALESCE(deleted_by, ''), created_at, updated_at`

func repoScanDest(r *models.Repository) []any {
	return []any{&r.ID, &r.Name, &r.Format, &r.Description, &r.Public, &r.AllowFullUnpublish, &r.RepoType, &r.UpstreamURL, &r.UpstreamUsername, &r.UpstreamPasswordEnc, &r.CacheTTLSeconds, &r.NegativeCacheTTLSeconds, &r.MaxStaleSeconds, &r.Blocked, &r.ProjectID, &r.PresignDownloads, &r.TrashRetentionDays, &r.ImmutableReleases, &r.MergeProxyVersions, &r.DeletedAt, &r.DeletedBy, &r.CreatedAt, &r.UpdatedAt}
}

func (p *PostgresDB) GetRepositoryByNameUncached(ctx context.Context, name string) (*models.Repository, error) {
	r := &models.Repository{}

	err := p.db.QueryRowContext(ctx,
		`SELECT `+repoColumns+` FROM repositories WHERE name = $1 AND deleted_at IS NULL`, name,
	).Scan(repoScanDest(r)...)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // nil, nil is the standard Go "not found" pattern
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get repository by name: %w", err)
	}

	return r, nil
}

// ListPublicRepositoryNames returns the names of all repositories flagged public.
func (p *PostgresDB) ListPublicRepositoryNames(ctx context.Context) ([]string, error) {
	rows, err := p.db.QueryContext(ctx, `SELECT name FROM repositories WHERE COALESCE(public, false) AND deleted_at IS NULL`)
	if err != nil {
		return nil, fmt.Errorf("failed to list public repositories: %w", err)
	}

	defer func() { _ = rows.Close() }()

	var names []string

	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, fmt.Errorf("failed to scan public repository: %w", err)
		}

		names = append(names, n)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate public repositories: %w", err)
	}

	return names, nil
}

// GetRepositoryByID retrieves a repository by its primary key.
func (p *PostgresDB) GetRepositoryByID(ctx context.Context, id string) (*models.Repository, error) {
	r := &models.Repository{}

	err := p.db.QueryRowContext(ctx,
		`SELECT `+repoColumns+` FROM repositories WHERE id = $1 AND deleted_at IS NULL`, id,
	).Scan(repoScanDest(r)...)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // nil, nil is the standard Go "not found" pattern
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get repository by id: %w", err)
	}

	return r, nil
}

func (p *PostgresDB) ListRepositories(ctx context.Context, page, limit int, formatFilter string) ([]*models.Repository, error) {
	offset := (page - 1) * limit

	var (
		rows *sql.Rows
		err  error
	)
	if formatFilter != "" {
		rows, err = p.db.QueryContext(ctx,
			`SELECT `+repoColumns+` FROM repositories WHERE format = $1 AND deleted_at IS NULL ORDER BY created_at DESC, id LIMIT $2 OFFSET $3`,
			formatFilter, limit, offset,
		)
	} else {
		rows, err = p.db.QueryContext(ctx,
			`SELECT `+repoColumns+` FROM repositories WHERE deleted_at IS NULL ORDER BY created_at DESC, id LIMIT $1 OFFSET $2`,
			limit, offset,
		)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to list repositories: %w", err)
	}

	defer func() { _ = rows.Close() }()

	var repos []*models.Repository

	for rows.Next() {
		r := &models.Repository{}
		if err := rows.Scan(repoScanDest(r)...); err != nil {
			return nil, fmt.Errorf("failed to scan repository: %w", err)
		}

		repos = append(repos, r)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate repositories: %w", err)
	}

	return repos, nil
}

func (p *PostgresDB) CountRepositories(ctx context.Context, formatFilter string) (int, error) {
	var (
		count int
		err   error
	)
	if formatFilter != "" {
		err = p.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM repositories WHERE format = $1 AND deleted_at IS NULL`, formatFilter,
		).Scan(&count)
	} else {
		err = p.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM repositories WHERE deleted_at IS NULL`,
		).Scan(&count)
	}

	if err != nil {
		return 0, fmt.Errorf("failed to count repositories: %w", err)
	}

	return count, nil
}

func (p *PostgresDB) UpdateRepository(ctx context.Context, repo *models.Repository) error {
	// Cached auth lookups must not outlive this write.
	defer p.authCache.invalidate()

	return updateRepositoryRow(ctx, p.db, repo)
}

// repoExecer is the part of *sql.DB and *sql.Tx the repository update needs.
type repoExecer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func updateRepositoryRow(ctx context.Context, db repoExecer, repo *models.Repository) error {
	_, err := db.ExecContext(ctx,
		`UPDATE repositories SET description = $1, public = $2, repo_type = $3, upstream_url = NULLIF($4,''), upstream_username = NULLIF($5,''), upstream_password_enc = NULLIF($6,''), cache_ttl_seconds = $7, negative_cache_ttl_seconds = $8, max_stale_seconds = $9, blocked = $10, project_id = $11, trash_retention_days = $12, immutable_releases = $13, presign_downloads = $15, merge_proxy_versions = $16, updated_at = now() WHERE id = $14 AND deleted_at IS NULL`,
		repo.Description, repo.Public, repo.RepoType, repo.UpstreamURL, repo.UpstreamUsername, repo.UpstreamPasswordEnc, repo.CacheTTLSeconds, repo.NegativeCacheTTLSeconds, repo.MaxStaleSeconds, repo.Blocked, repo.ProjectID, repo.TrashRetentionDays, repo.ImmutableReleases, repo.ID, repo.PresignDownloads, repo.MergeProxyVersions,
	)
	if err != nil {
		return fmt.Errorf("failed to update repository: %w", err)
	}

	return nil
}

func (p *PostgresDB) DeleteRepository(ctx context.Context, id string) error {
	// Cached auth lookups must not outlive this write.
	defer p.authCache.invalidate()

	_, err := p.db.ExecContext(ctx, `DELETE FROM repositories WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("failed to delete repository: %w", err)
	}

	return nil
}

// ListComponentsByRepo lists the packages of one repository across every
// format (see ListRepoComponents in queries/components.sql for the per-format
// mapping). formatFilter, when non-empty, restricts the result to one format.
func (p *PostgresDB) ListComponentsByRepo(ctx context.Context, repoID, formatFilter string, page, limit int) ([]*models.Component, int, error) {
	return p.SearchComponents(ctx, repoID, ComponentListOptions{Format: formatFilter, Page: page, Limit: limit})
}

// SearchComponents is ListComponentsByRepo with the extra filters and the
// ordering of ComponentListOptions.
func (p *PostgresDB) SearchComponents(ctx context.Context, repoID string, opts ComponentListOptions) ([]*models.Component, int, error) {
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
func (p *PostgresDB) componentTotal(ctx context.Context, repoID string, opts ComponentListOptions, pageRows int) (int, error) {
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
func (p *PostgresDB) listComponentRows(ctx context.Context, repoID string, opts ComponentListOptions) ([]componentRow, error) {
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

func isUUID(s string) bool { return uuid.Validate(s) == nil }

// GetRepoComponentBlob resolves a row id of the package listing
// (ListComponentsByRepo) to its blob, scoped to repoID. It returns nil, nil
// when no row of any format has that id in the repository, or when id is not
// a UUID.
func (p *PostgresDB) GetRepoComponentBlob(ctx context.Context, repoID, id string) (*models.ComponentBlob, error) {
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

func (p *PostgresDB) CreateRepository(ctx context.Context, repo *models.Repository) error {
	// A new repository joins the readable set of whoever's grants match it.
	defer p.authCache.invalidate()

	repoType := repo.RepoType
	if repoType == "" {
		repoType = "hosted"
	}

	return p.db.QueryRowContext(ctx,
		`INSERT INTO repositories (id, name, format, description, public, repo_type, upstream_url, upstream_username, upstream_password_enc, cache_ttl_seconds, negative_cache_ttl_seconds, max_stale_seconds, blocked, project_id, trash_retention_days, immutable_releases, presign_downloads, allow_full_unpublish, merge_proxy_versions) VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, NULLIF($6,''), NULLIF($7,''), NULLIF($8,''), $9, $10, $11, $12, $13, $14, $15, $16, $17, $18) RETURNING id, created_at, updated_at`,
		repo.Name, repo.Format, repo.Description, repo.Public, repoType, repo.UpstreamURL, repo.UpstreamUsername, repo.UpstreamPasswordEnc, repo.CacheTTLSeconds, repo.NegativeCacheTTLSeconds, repo.MaxStaleSeconds, repo.Blocked, repo.ProjectID, repo.TrashRetentionDays, repo.ImmutableReleases, repo.PresignDownloads, repo.AllowFullUnpublish, repo.MergeProxyVersions,
	).Scan(&repo.ID, &repo.CreatedAt, &repo.UpdatedAt)
}

func (p *PostgresDB) GetRPMRepo(ctx context.Context, repositoryID string) (*models.RPMRepo, error) {
	row, err := p.q.GetRPMRepo(ctx, repositoryID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // nil, nil is the standard Go "not found" pattern
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get rpm repo: %w", err)
	}

	return &models.RPMRepo{
		ID:                    row.ID,
		RepositoryID:          row.RepositoryID,
		BaseArch:              row.BaseArch,
		MetadataStale:         row.MetadataStale,
		SigningKeyID:          row.SigningKeyID,
		SigningKeyFingerprint: row.SigningKeyFingerprint,
		CreatedAt:             row.CreatedAt,
		UpdatedAt:             row.UpdatedAt,
	}, nil
}

func (p *PostgresDB) CreateRPMRepo(ctx context.Context, repo *models.RPMRepo) error {
	row, err := p.q.CreateRPMRepo(ctx, dbsqlc.CreateRPMRepoParams{
		RepositoryID: repo.RepositoryID,
		BaseArch:     repo.BaseArch,
	})
	if err != nil {
		return fmt.Errorf("failed to create rpm repo: %w", err)
	}

	repo.ID = row.ID
	repo.CreatedAt = row.CreatedAt
	repo.UpdatedAt = row.UpdatedAt

	return nil
}

// MarkRepoStale marks repository metadata as stale for regeneration.
// NOTE: Currently only tracks staleness for RPM repos via rpm_repos table.
// DEB staleness tracking needs a separate mechanism (e.g., a stale flag on repositories table).
func (p *PostgresDB) MarkRepoStale(ctx context.Context, repositoryID string) error {
	return p.q.MarkRepoStale(ctx, repositoryID)
}

func (p *PostgresDB) CreateComponent(ctx context.Context, component *models.Component) error {
	return ErrCreateComponentDeprecated
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

// CreateDebPackage inserts a DEB package with distribution and component references.
func (p *PostgresDB) CreateDebPackage(ctx context.Context, pkg *models.Component, distID, compID string) error {
	err := p.db.QueryRowContext(ctx,
		`INSERT INTO deb_packages (repository_id, distribution_id, component_id, blob_id, name, version, arch, description, summary, license, maintainer, homepage, filename, installed_size, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,COALESCE($15::timestamptz, now()))
		 ON CONFLICT DO NOTHING
		 RETURNING id, created_at, updated_at`,
		pkg.RepositoryID, distID, compID, pkg.ArtifactID, pkg.Name, pkg.Version,
		pkg.Arch, pkg.Description, pkg.Summary, pkg.License, "", "",
		pkg.Filename, pkg.InstalledSize, createdAtOrNil(pkg.CreatedAt),
	).Scan(&pkg.ID, &pkg.CreatedAt, &pkg.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrDuplicate
	}

	return err
}

// CreateRpmPackage inserts an RPM package.
func (p *PostgresDB) CreateRpmPackage(ctx context.Context, pkg *models.Component) error {
	err := p.db.QueryRowContext(ctx,
		`INSERT INTO rpm_packages (repository_id, blob_id, name, version, release, epoch, arch, description, summary, license, url, vendor, group_name, packager, source_rpm, build_host, build_time, package_size, archive_size, installed_size, filename, header_start, header_end, rpm_extra, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,to_timestamp(NULLIF($17::bigint,0)),$18,$19,$20,$21,$22,$23,COALESCE($24::jsonb,'{}'::jsonb),COALESCE($25::timestamptz, now()))
		 ON CONFLICT DO NOTHING
		 RETURNING id, created_at, updated_at`,
		pkg.RepositoryID, pkg.ArtifactID, pkg.Name, pkg.Version,
		pkg.Release, int64(pkg.Epoch), pkg.Arch,
		pkg.Description, pkg.Summary, pkg.License, pkg.URL,
		pkg.Vendor, pkg.Group, pkg.Packager, pkg.SourceRPM,
		pkg.BuildHost, pkg.BuildTime, pkg.PackageSize, pkg.ArchiveSize,
		pkg.InstalledSize, pkg.Filename, pkg.HeaderStart, pkg.HeaderEnd, nullableJSON(pkg.RPMExtra),
		createdAtOrNil(pkg.CreatedAt),
	).Scan(&pkg.ID, &pkg.CreatedAt, &pkg.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrDuplicate
	}

	return err
}

// scanRpmComponent runs a query against rpm_packages (LEFT JOIN blobs for
// sha256) and scans a single result into a models.Component.
func (p *PostgresDB) scanRpmComponent(ctx context.Context, query string, args ...any) (*models.Component, error) {
	c := &models.Component{Format: "rpm"}
	err := p.db.QueryRowContext(ctx, query, args...).Scan(
		&c.ID, &c.RepositoryID, &c.SHA256, &c.Name, &c.Version,
		&c.Release, &c.Epoch, &c.Arch, &c.Summary, &c.Description, &c.License,
		&c.URL, &c.Vendor, &c.Group, &c.Packager, &c.SourceRPM, &c.BuildHost, &c.BuildTime,
		&c.PackageSize, &c.InstalledSize, &c.ArchiveSize, &c.Filename, &c.CreatedAt, &c.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	return c, nil
}

// scanDebComponent runs a query against deb_packages (LEFT JOIN blobs for
// sha256/size) and scans a single result into a models.Component. deb_packages
// has no rpm-only columns (release, epoch, source_rpm, ...); those fields are
// left at their zero value.
func (p *PostgresDB) scanDebComponent(ctx context.Context, query string, args ...any) (*models.Component, error) {
	c := &models.Component{Format: "deb"}
	err := p.db.QueryRowContext(ctx, query, args...).Scan(
		&c.ID, &c.RepositoryID, &c.SHA256, &c.Name, &c.Version,
		&c.Arch, &c.Summary, &c.Description, &c.License,
		&c.PackageSize, &c.InstalledSize, &c.Filename, &c.CreatedAt, &c.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	return c, nil
}

const rpmComponentColumns = `rp.id, rp.repository_id, COALESCE(b.sha256,''), rp.name, rp.version,
		       COALESCE(rp.release,''), rp.epoch, COALESCE(rp.arch,''),
		       COALESCE(rp.summary,''), COALESCE(rp.description,''), COALESCE(rp.license,''),
		       COALESCE(rp.url,''), COALESCE(rp.vendor,''), COALESCE(rp.group_name,''),
		       COALESCE(rp.packager,''), COALESCE(rp.source_rpm,''), COALESCE(rp.build_host,''),
		       COALESCE(EXTRACT(EPOCH FROM rp.build_time)::bigint,0),
		       COALESCE(rp.package_size,0), COALESCE(rp.installed_size,0), COALESCE(rp.archive_size,0),
		       COALESCE(rp.filename,''), rp.created_at, rp.updated_at
		FROM rpm_packages rp LEFT JOIN blobs b ON b.id = rp.blob_id`

const debComponentColumns = `dp.id, dp.repository_id, COALESCE(b.sha256,''), dp.name, dp.version,
		       COALESCE(dp.arch,''), COALESCE(dp.summary,''), COALESCE(dp.description,''),
		       COALESCE(dp.license,''), COALESCE(b.size_bytes,0), COALESCE(dp.installed_size,0),
		       COALESCE(dp.filename,''), dp.created_at, dp.updated_at
		FROM deb_packages dp LEFT JOIN blobs b ON b.id = dp.blob_id`

// GetComponentByID looks up a package by its primary key, trying rpm_packages
// then deb_packages (row IDs are shared across the rpm_packages and
// deb_packages tables).
func (p *PostgresDB) GetComponentByID(ctx context.Context, id string) (*models.Component, error) {
	c, err := p.scanRpmComponent(ctx, "SELECT "+rpmComponentColumns+" WHERE rp.id = $1 AND rp.deleted_at IS NULL", id)
	if err == nil {
		return c, nil
	}

	if !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("failed to get component by id: %w", err)
	}

	c, err = p.scanDebComponent(ctx, "SELECT "+debComponentColumns+" WHERE dp.id = $1 AND dp.deleted_at IS NULL", id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // nil, nil is the standard Go "not found" pattern
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get component by id: %w", err)
	}

	return c, nil
}

func (p *PostgresDB) GetComponentByFilename(ctx context.Context, repoID string, filename string) (*models.Component, error) {
	c, err := p.scanRpmComponent(ctx, "SELECT "+rpmComponentColumns+" WHERE rp.repository_id = $1 AND rp.filename = $2 AND rp.deleted_at IS NULL", repoID, filename)
	if err == nil {
		return c, nil
	}

	if !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("failed to get component by filename: %w", err)
	}

	c, err = p.scanDebComponent(ctx, "SELECT "+debComponentColumns+" WHERE dp.repository_id = $1 AND dp.filename = $2 AND dp.deleted_at IS NULL", repoID, filename)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // nil, nil is the standard Go "not found" pattern
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get component by filename: %w", err)
	}

	return c, nil
}

// GetComponentBySHA256 finds a live rpm or deb package in a repository by the
// sha256 of its blob using a single-row lookup per table.
func (p *PostgresDB) GetComponentBySHA256(ctx context.Context, repoID, sha256 string) (*models.Component, error) {
	c, err := p.scanRpmComponent(ctx, "SELECT "+rpmComponentColumns+" WHERE rp.repository_id = $1 AND b.sha256 = $2 AND rp.deleted_at IS NULL LIMIT 1", repoID, sha256)
	if err == nil {
		return c, nil
	}

	if !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("failed to get component by sha256: %w", err)
	}

	c, err = p.scanDebComponent(ctx, "SELECT "+debComponentColumns+" WHERE dp.repository_id = $1 AND b.sha256 = $2 AND dp.deleted_at IS NULL LIMIT 1", repoID, sha256)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // nil, nil is the standard Go "not found" pattern
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get component by sha256: %w", err)
	}

	return c, nil
}

// GetComponentsByRepo lists RPM packages for a repository.
func (p *PostgresDB) GetComponentsByRepo(ctx context.Context, repoID string) ([]models.Component, error) {
	rows, err := p.db.QueryContext(ctx, "SELECT "+rpmComponentColumns+" WHERE rp.repository_id = $1 AND rp.deleted_at IS NULL", repoID)
	if err != nil {
		return nil, fmt.Errorf("failed to query components: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var components []models.Component

	for rows.Next() {
		c := models.Component{Format: "rpm"}
		if err := rows.Scan(&c.ID, &c.RepositoryID, &c.SHA256, &c.Name, &c.Version, &c.Release, &c.Epoch, &c.Arch,
			&c.Summary, &c.Description, &c.License, &c.URL, &c.Vendor, &c.Group, &c.Packager, &c.SourceRPM,
			&c.BuildHost, &c.BuildTime, &c.PackageSize, &c.InstalledSize, &c.ArchiveSize, &c.Filename,
			&c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan component: %w", err)
		}

		components = append(components, c)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate components: %w", err)
	}

	return components, nil
}

func (p *PostgresDB) DeleteComponent(ctx context.Context, componentID string) error {
	// Try deb_packages first, then rpm_packages. Only one will match.
	res, err := p.db.ExecContext(ctx, `DELETE FROM deb_packages WHERE id = $1 AND deleted_at IS NULL`, componentID)
	if err != nil {
		return err
	}
	rows, _ := res.RowsAffected()
	if rows > 0 {
		return nil
	}
	_, err = p.db.ExecContext(ctx, `DELETE FROM rpm_packages WHERE id = $1 AND deleted_at IS NULL`, componentID)
	return err
}

// GetDebPackageByID fetches a DEB package by its primary key.
func (p *PostgresDB) GetDebPackageByID(ctx context.Context, id string) (*models.DebPackage, error) {
	pkg := &models.DebPackage{}
	err := p.db.QueryRowContext(ctx,
		`SELECT id, repository_id, distribution_id, component_id, blob_id,
		        name, version, arch,
		        COALESCE(summary,''), COALESCE(description,''), COALESCE(license,''),
		        COALESCE(maintainer,''), COALESCE(homepage,''),
		        COALESCE(filename,''), COALESCE(installed_size,0),
		        created_at, updated_at
		 FROM deb_packages WHERE id = $1 AND deleted_at IS NULL`, id).
		Scan(&pkg.ID, &pkg.RepositoryID, &pkg.DistributionID, &pkg.ComponentID, &pkg.BlobID,
			&pkg.Name, &pkg.Version, &pkg.Arch,
			&pkg.Summary, &pkg.Description, &pkg.License,
			&pkg.Maintainer, &pkg.Homepage,
			&pkg.Filename, &pkg.InstalledSize,
			&pkg.CreatedAt, &pkg.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}
	if err != nil {
		return nil, fmt.Errorf("get deb package: %w", err)
	}
	return pkg, nil
}

// ListDebPackagesByRepo lists all DEB packages in a repository.
func (p *PostgresDB) ListDebPackagesByRepo(ctx context.Context, repoID string) ([]*models.DebPackage, error) {
	rows, err := p.db.QueryContext(ctx,
		`SELECT id, repository_id, distribution_id, component_id, blob_id,
		        name, version, arch,
		        COALESCE(summary,''), COALESCE(description,''), COALESCE(license,''),
		        COALESCE(maintainer,''), COALESCE(homepage,''),
		        COALESCE(filename,''), COALESCE(installed_size,0),
		        created_at, updated_at
		 FROM deb_packages WHERE repository_id = $1 AND deleted_at IS NULL`, repoID)
	if err != nil {
		return nil, fmt.Errorf("list deb packages: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var pkgs []*models.DebPackage
	for rows.Next() {
		pkg := &models.DebPackage{}
		if err := rows.Scan(&pkg.ID, &pkg.RepositoryID, &pkg.DistributionID, &pkg.ComponentID, &pkg.BlobID,
			&pkg.Name, &pkg.Version, &pkg.Arch,
			&pkg.Summary, &pkg.Description, &pkg.License,
			&pkg.Maintainer, &pkg.Homepage,
			&pkg.Filename, &pkg.InstalledSize,
			&pkg.CreatedAt, &pkg.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan deb package: %w", err)
		}
		pkgs = append(pkgs, pkg)
	}
	return pkgs, rows.Err()
}

// GetRpmPackageByID fetches an RPM package by its primary key.
func (p *PostgresDB) GetRpmPackageByID(ctx context.Context, id string) (*models.RpmPackage, error) {
	pkg := &models.RpmPackage{}
	err := p.db.QueryRowContext(ctx,
		`SELECT id, repository_id, blob_id,
		        name, version, COALESCE(release,''), epoch, COALESCE(arch,''),
		        COALESCE(summary,''), COALESCE(description,''), COALESCE(license,''),
		        COALESCE(url,''), COALESCE(vendor,''), COALESCE(group_name,''),
		        COALESCE(packager,''), COALESCE(source_rpm,''), COALESCE(build_host,''),
		        build_time,
		        COALESCE(filename,''), COALESCE(package_size,0), COALESCE(installed_size,0), COALESCE(archive_size,0),
		        created_at, updated_at
		 FROM rpm_packages WHERE id = $1 AND deleted_at IS NULL`, id).
		Scan(&pkg.ID, &pkg.RepositoryID, &pkg.BlobID,
			&pkg.Name, &pkg.Version, &pkg.Release, &pkg.Epoch, &pkg.Arch,
			&pkg.Summary, &pkg.Description, &pkg.License,
			&pkg.URL, &pkg.Vendor, &pkg.GroupName,
			&pkg.Packager, &pkg.SourceRPM, &pkg.BuildHost,
			&pkg.BuildTime,
			&pkg.Filename, &pkg.PackageSize, &pkg.InstalledSize, &pkg.ArchiveSize,
			&pkg.CreatedAt, &pkg.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}
	if err != nil {
		return nil, fmt.Errorf("get rpm package: %w", err)
	}
	return pkg, nil
}

// ListRpmPackagesByRepo lists all RPM packages in a repository.
func (p *PostgresDB) ListRpmPackagesByRepo(ctx context.Context, repoID string) ([]*models.RpmPackage, error) {
	rows, err := p.db.QueryContext(ctx,
		`SELECT id, repository_id, blob_id,
		        name, version, COALESCE(release,''), epoch, COALESCE(arch,''),
		        COALESCE(summary,''), COALESCE(description,''), COALESCE(license,''),
		        COALESCE(url,''), COALESCE(vendor,''), COALESCE(group_name,''),
		        COALESCE(packager,''), COALESCE(source_rpm,''), COALESCE(build_host,''),
		        build_time,
		        COALESCE(filename,''), COALESCE(package_size,0), COALESCE(installed_size,0), COALESCE(archive_size,0),
		        created_at, updated_at
		 FROM rpm_packages WHERE repository_id = $1 AND deleted_at IS NULL`, repoID)
	if err != nil {
		return nil, fmt.Errorf("list rpm packages: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var pkgs []*models.RpmPackage
	for rows.Next() {
		pkg := &models.RpmPackage{}
		if err := rows.Scan(&pkg.ID, &pkg.RepositoryID, &pkg.BlobID,
			&pkg.Name, &pkg.Version, &pkg.Release, &pkg.Epoch, &pkg.Arch,
			&pkg.Summary, &pkg.Description, &pkg.License,
			&pkg.URL, &pkg.Vendor, &pkg.GroupName,
			&pkg.Packager, &pkg.SourceRPM, &pkg.BuildHost,
			&pkg.BuildTime,
			&pkg.Filename, &pkg.PackageSize, &pkg.InstalledSize, &pkg.ArchiveSize,
			&pkg.CreatedAt, &pkg.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan rpm package: %w", err)
		}
		pkgs = append(pkgs, pkg)
	}
	return pkgs, rows.Err()
}

func (p *PostgresDB) GetRepoMetadataByPath(ctx context.Context, repositoryID string, path string) (*models.RepoMetadata, error) {
	m := &models.RepoMetadata{}

	err := p.db.QueryRowContext(ctx,
		`SELECT id, repository_id, path, blob_sha256, blob_size, last_generated FROM repository_metadata WHERE repository_id = $1 AND path = $2`,
		repositoryID, path,
	).Scan(&m.ID, &m.RepositoryID, &m.Path, &m.BlobSHA256, &m.BlobSize, &m.LastGenerated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // nil, nil is the standard Go "not found" pattern
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get repo metadata by path: %w", err)
	}

	return m, nil
}

func (p *PostgresDB) GetRepoMetadataByRepo(ctx context.Context, repositoryID string) ([]models.RepoMetadata, error) {
	rows, err := p.db.QueryContext(ctx,
		`SELECT id, repository_id, path, blob_sha256, blob_size, last_generated FROM repository_metadata WHERE repository_id = $1`,
		repositoryID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query repo metadata: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var metas []models.RepoMetadata

	for rows.Next() {
		var m models.RepoMetadata
		if err := rows.Scan(&m.ID, &m.RepositoryID, &m.Path, &m.BlobSHA256, &m.BlobSize, &m.LastGenerated); err != nil {
			return nil, fmt.Errorf("failed to scan repo metadata: %w", err)
		}

		metas = append(metas, m)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate repo metadata: %w", err)
	}

	return metas, nil
}

func (p *PostgresDB) UpsertRepoMetadata(ctx context.Context, meta *models.RepoMetadata) error {
	return p.db.QueryRowContext(ctx,
		`INSERT INTO repository_metadata (repository_id, path, blob_sha256, blob_size) VALUES ($1, $2, $3, $4) ON CONFLICT (repository_id, path) DO UPDATE SET blob_sha256 = $3, blob_size = $4, last_generated = now() RETURNING id, last_generated`,
		meta.RepositoryID, meta.Path, meta.BlobSHA256, meta.BlobSize,
	).Scan(&meta.ID, &meta.LastGenerated)
}

// PruneMavenMetadata deletes the generated maven-metadata.xml rows below the
// directory dir (e.g. "com/example/app/"): the artifact's own maven-metadata.xml
// and the one of each snapshot version directory, except the paths in keep. The
// regeneration calls it with the paths it just wrote, so metadata of an
// artifact (or snapshot version) that has no live file left disappears
// instead of advertising versions that were deleted or trashed. The blob shas of
// the deleted rows are returned so the caller can reclaim the objects.
func (p *PostgresDB) PruneMavenMetadata(ctx context.Context, repositoryID, dir string, keep []string) ([]string, error) {
	if keep == nil {
		keep = []string{}
	}

	rows, err := p.db.QueryContext(ctx,
		`DELETE FROM repository_metadata
		  WHERE repository_id = $1 AND starts_with(path, $2) AND NOT (path = ANY($3::text[]))
		    AND (substr(path, length($2) + 1) = 'maven-metadata.xml'
		         OR (substr(path, length($2) + 1) LIKE '%-SNAPSHOT/maven-metadata.xml'
		             AND position('/' IN substr(path, length($2) + 1)) = length(substr(path, length($2) + 1)) - length('maven-metadata.xml')))
		RETURNING blob_sha256`,
		repositoryID, dir, keep,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to prune maven metadata: %w", err)
	}

	defer func() { _ = rows.Close() }()

	var shas []string

	for rows.Next() {
		var sha string
		if err := rows.Scan(&sha); err != nil {
			return nil, fmt.Errorf("failed to scan pruned maven metadata: %w", err)
		}

		shas = append(shas, sha)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to prune maven metadata: %w", err)
	}

	return shas, nil
}

func (p *PostgresDB) CreateDebianDistribution(ctx context.Context, dist *models.DebianDistribution) error {
	row, err := p.q.CreateDebianDistribution(ctx, dbsqlc.CreateDebianDistributionParams{
		RepositoryID: dist.RepositoryID,
		Name:         dist.Name,
		Codename:     strPtr(dist.Codename),
		Description:  strPtr(dist.Description),
	})
	if err != nil {
		return fmt.Errorf("failed to create debian distribution: %w", err)
	}

	dist.ID = row.ID
	dist.CreatedAt = row.CreatedAt
	dist.UpdatedAt = row.UpdatedAt

	return nil
}

func (p *PostgresDB) GetDebianDistribution(ctx context.Context, repoID string, name string) (*models.DebianDistribution, error) {
	row, err := p.q.GetDebianDistribution(ctx, dbsqlc.GetDebianDistributionParams{
		RepositoryID: repoID,
		Name:         name,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // nil, nil is the standard Go "not found" pattern
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get debian distribution: %w", err)
	}

	return &models.DebianDistribution{
		ID:           row.ID,
		RepositoryID: row.RepositoryID,
		Name:         row.Name,
		Codename:     row.Codename,
		Description:  row.Description,
		CreatedAt:    row.CreatedAt,
		UpdatedAt:    row.UpdatedAt,
	}, nil
}

func (p *PostgresDB) GetDebianDistributionsByRepo(ctx context.Context, repoID string) ([]models.DebianDistribution, error) {
	rows, err := p.q.GetDebianDistributionsByRepo(ctx, repoID)
	if err != nil {
		return nil, fmt.Errorf("failed to query debian distributions: %w", err)
	}

	dists := make([]models.DebianDistribution, len(rows))
	for i, row := range rows {
		dists[i] = models.DebianDistribution{
			ID:           row.ID,
			RepositoryID: row.RepositoryID,
			Name:         row.Name,
			Codename:     row.Codename,
			Description:  row.Description,
			CreatedAt:    row.CreatedAt,
			UpdatedAt:    row.UpdatedAt,
		}
	}

	return dists, nil
}

func (p *PostgresDB) CreateDebianComponent(ctx context.Context, comp *models.DebianComponent) error {
	row, err := p.q.CreateDebianComponent(ctx, dbsqlc.CreateDebianComponentParams{
		DistributionID: comp.DistributionID,
		Name:           comp.Name,
		Description:    strPtr(comp.Description),
	})
	if err != nil {
		return fmt.Errorf("failed to create debian component: %w", err)
	}

	comp.ID = row.ID
	comp.CreatedAt = row.CreatedAt
	comp.UpdatedAt = row.UpdatedAt

	return nil
}

func (p *PostgresDB) GetDebianComponentsByDist(ctx context.Context, distID string) ([]models.DebianComponent, error) {
	rows, err := p.q.GetDebianComponentsByDist(ctx, distID)
	if err != nil {
		return nil, fmt.Errorf("failed to query debian components: %w", err)
	}

	comps := make([]models.DebianComponent, len(rows))
	for i, row := range rows {
		comps[i] = models.DebianComponent{
			ID:             row.ID,
			DistributionID: row.DistributionID,
			Name:           row.Name,
			Description:    row.Description,
			CreatedAt:      row.CreatedAt,
			UpdatedAt:      row.UpdatedAt,
		}
	}

	return comps, nil
}

func (p *PostgresDB) CreateDebianArchitecture(ctx context.Context, arch *models.DebianArchitecture) error {
	row, err := p.q.CreateDebianArchitecture(ctx, dbsqlc.CreateDebianArchitectureParams{
		DistributionID: arch.DistributionID,
		Name:           arch.Name,
	})
	if err != nil {
		return fmt.Errorf("failed to create debian architecture: %w", err)
	}

	arch.ID = row.ID
	arch.CreatedAt = row.CreatedAt
	arch.UpdatedAt = row.UpdatedAt

	return nil
}

func (p *PostgresDB) GetDebianArchitecturesByDist(ctx context.Context, distID string) ([]models.DebianArchitecture, error) {
	rows, err := p.q.GetDebianArchitecturesByDist(ctx, distID)
	if err != nil {
		return nil, fmt.Errorf("failed to query debian architectures: %w", err)
	}

	archs := make([]models.DebianArchitecture, len(rows))
	for i, row := range rows {
		archs[i] = models.DebianArchitecture{
			ID:             row.ID,
			DistributionID: row.DistributionID,
			Name:           row.Name,
			CreatedAt:      row.CreatedAt,
			UpdatedAt:      row.UpdatedAt,
		}
	}

	return archs, nil
}

func (p *PostgresDB) GetDebianPackageByFilename(ctx context.Context, repoID string, filename string) (*models.Component, error) {
	row, err := p.q.GetDebianPackageByFilename(ctx, dbsqlc.GetDebianPackageByFilenameParams{
		RepositoryID: repoID,
		Filename:     strPtr(filename),
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get debian package by filename: %w", err)
	}

	return &models.Component{
		ID:            row.ID,
		RepositoryID:  row.RepositoryID,
		SHA256:        row.Sha256,
		Format:        "deb",
		Name:          row.Name,
		Version:       row.Version,
		Arch:          row.Arch,
		Summary:       row.Summary,
		Description:   row.Description,
		License:       row.License,
		Filename:      row.Filename,
		PackageSize:   row.PackageSize,
		InstalledSize: row.InstalledSize,
		CreatedAt:     row.CreatedAt,
		UpdatedAt:     row.UpdatedAt,
	}, nil
}

func (p *PostgresDB) GetDebianPackagesByDistComp(ctx context.Context, repoID string, dist string, comp string, arch string) ([]models.Component, error) {
	rows, err := p.q.GetDebianPackagesByDistComp(ctx, dbsqlc.GetDebianPackagesByDistCompParams{
		RepositoryID: repoID,
		Dist:         dist,
		Comp:         comp,
		Arch:         arch,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to query debian packages: %w", err)
	}

	components := make([]models.Component, 0, len(rows))
	for _, row := range rows {
		components = append(components, models.Component{
			ID:            row.ID,
			RepositoryID:  row.RepositoryID,
			SHA256:        row.Sha256,
			Format:        "deb",
			Name:          row.Name,
			Version:       row.Version,
			Arch:          row.Arch,
			Summary:       row.Summary,
			Description:   row.Description,
			License:       row.License,
			Filename:      row.Filename,
			PackageSize:   row.PackageSize,
			InstalledSize: row.InstalledSize,
			CreatedAt:     row.CreatedAt,
			UpdatedAt:     row.UpdatedAt,
		})
	}

	return components, nil
}

func (p *PostgresDB) CreateMavenArtifact(ctx context.Context, a *models.MavenArtifact) error {
	id, err := p.q.CreateMavenArtifact(ctx, dbsqlc.CreateMavenArtifactParams{
		RepositoryID:        a.RepositoryID,
		BlobID:              a.BlobID,
		GroupID:             a.GroupID,
		ArtifactIDCoord:     a.ArtifactCoord,
		Version:             a.Version,
		Classifier:          a.Classifier,
		Extension:           a.Extension,
		SnapshotTimestamp:   strPtr(a.SnapshotTimestamp),
		SnapshotBuildNumber: toInt32(a.SnapshotBuildNumber),
		IsSnapshot:          a.IsSnapshot,
		RepoPath:            a.RepoPath,
		CreatedAt:           createdAtOrNil(a.CreatedAt),
	})
	if err != nil {
		return fmt.Errorf("failed to create maven artifact: %w", err)
	}

	// ON CONFLICT returns the existing row id; always bind the returned id.
	a.ID = id

	return p.storeMavenChecksums(ctx, a)
}

func (p *PostgresDB) GetMavenArtifactByPath(ctx context.Context, repoID, path string) (*models.MavenArtifact, error) {
	row, err := p.q.GetMavenArtifactByPath(ctx, dbsqlc.GetMavenArtifactByPathParams{
		RepositoryID: repoID,
		RepoPath:     path,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get maven artifact by path: %w", err)
	}

	art := &models.MavenArtifact{
		ID:                  row.ID,
		RepositoryID:        row.RepositoryID,
		BlobID:              row.BlobID,
		GroupID:             row.GroupID,
		ArtifactCoord:       row.ArtifactIDCoord,
		Version:             row.Version,
		Classifier:          row.Classifier,
		Extension:           row.Extension,
		SnapshotTimestamp:   derefStr(row.SnapshotTimestamp),
		SnapshotBuildNumber: int(row.SnapshotBuildNumber),
		IsSnapshot:          row.IsSnapshot,
		RepoPath:            row.RepoPath,
		CreatedAt:           row.CreatedAt,
		UpdatedAt:           row.UpdatedAt,
	}

	if err := p.fillMavenChecksums(ctx, art); err != nil {
		return nil, err
	}

	return art, nil
}

func (p *PostgresDB) GetMavenArtifactsByGAV(ctx context.Context, repoID, groupID, artifactID, version string) ([]*models.MavenArtifact, error) {
	rows, err := p.q.GetMavenArtifactsByGAV(ctx, dbsqlc.GetMavenArtifactsByGAVParams{
		RepositoryID:    repoID,
		GroupID:         groupID,
		ArtifactIDCoord: artifactID,
		Version:         version,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to query maven artifacts by gav: %w", err)
	}

	var artifacts []*models.MavenArtifact

	for _, row := range rows {
		artifacts = append(artifacts, &models.MavenArtifact{
			ID:                  row.ID,
			RepositoryID:        row.RepositoryID,
			BlobID:              row.BlobID,
			GroupID:             row.GroupID,
			ArtifactCoord:       row.ArtifactIDCoord,
			Version:             row.Version,
			Classifier:          row.Classifier,
			Extension:           row.Extension,
			SnapshotTimestamp:   derefStr(row.SnapshotTimestamp),
			SnapshotBuildNumber: int(row.SnapshotBuildNumber),
			IsSnapshot:          row.IsSnapshot,
			RepoPath:            row.RepoPath,
			CreatedAt:           row.CreatedAt,
			UpdatedAt:           row.UpdatedAt,
		})
	}

	if err := p.fillMavenChecksums(ctx, artifacts...); err != nil {
		return nil, err
	}

	return artifacts, nil
}

func (p *PostgresDB) GetMavenArtifactsByGA(ctx context.Context, repoID, groupID, artifactID string) ([]*models.MavenArtifact, error) {
	rows, err := p.q.GetMavenArtifactsByGA(ctx, dbsqlc.GetMavenArtifactsByGAParams{
		RepositoryID:    repoID,
		GroupID:         groupID,
		ArtifactIDCoord: artifactID,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to query maven artifacts by ga: %w", err)
	}

	var artifacts []*models.MavenArtifact

	for _, row := range rows {
		artifacts = append(artifacts, &models.MavenArtifact{
			ID:                  row.ID,
			RepositoryID:        row.RepositoryID,
			BlobID:              row.BlobID,
			GroupID:             row.GroupID,
			ArtifactCoord:       row.ArtifactIDCoord,
			Version:             row.Version,
			Classifier:          row.Classifier,
			Extension:           row.Extension,
			SnapshotTimestamp:   derefStr(row.SnapshotTimestamp),
			SnapshotBuildNumber: int(row.SnapshotBuildNumber),
			IsSnapshot:          row.IsSnapshot,
			RepoPath:            row.RepoPath,
			CreatedAt:           row.CreatedAt,
			UpdatedAt:           row.UpdatedAt,
		})
	}

	if err := p.fillMavenChecksums(ctx, artifacts...); err != nil {
		return nil, err
	}

	return artifacts, nil
}

func (p *PostgresDB) GetLatestSnapshot(ctx context.Context, repoID, groupID, artifactID, version string) (*models.MavenArtifact, error) {
	row, err := p.q.GetLatestSnapshot(ctx, dbsqlc.GetLatestSnapshotParams{
		RepositoryID:    repoID,
		GroupID:         groupID,
		ArtifactIDCoord: artifactID,
		Version:         version,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get latest snapshot: %w", err)
	}

	art := &models.MavenArtifact{
		ID:                  row.ID,
		RepositoryID:        row.RepositoryID,
		BlobID:              row.BlobID,
		GroupID:             row.GroupID,
		ArtifactCoord:       row.ArtifactIDCoord,
		Version:             row.Version,
		Classifier:          row.Classifier,
		Extension:           row.Extension,
		SnapshotTimestamp:   derefStr(row.SnapshotTimestamp),
		SnapshotBuildNumber: int(row.SnapshotBuildNumber),
		IsSnapshot:          row.IsSnapshot,
		RepoPath:            row.RepoPath,
		CreatedAt:           row.CreatedAt,
		UpdatedAt:           row.UpdatedAt,
	}

	if err := p.fillMavenChecksums(ctx, art); err != nil {
		return nil, err
	}

	return art, nil
}

func (p *PostgresDB) GetLatestSnapshotFile(ctx context.Context, repoID, groupID, artifactID, version, classifier, extension string) (*models.MavenArtifact, error) {
	row, err := p.q.GetLatestSnapshotFile(ctx, dbsqlc.GetLatestSnapshotFileParams{
		RepositoryID:    repoID,
		GroupID:         groupID,
		ArtifactIDCoord: artifactID,
		Version:         version,
		Classifier:      classifier,
		Extension:       extension,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get latest snapshot file: %w", err)
	}

	art := &models.MavenArtifact{
		ID:                  row.ID,
		RepositoryID:        row.RepositoryID,
		BlobID:              row.BlobID,
		GroupID:             row.GroupID,
		ArtifactCoord:       row.ArtifactIDCoord,
		Version:             row.Version,
		Classifier:          row.Classifier,
		Extension:           row.Extension,
		SnapshotTimestamp:   derefStr(row.SnapshotTimestamp),
		SnapshotBuildNumber: int(row.SnapshotBuildNumber),
		IsSnapshot:          row.IsSnapshot,
		RepoPath:            row.RepoPath,
		CreatedAt:           row.CreatedAt,
		UpdatedAt:           row.UpdatedAt,
	}

	if err := p.fillMavenChecksums(ctx, art); err != nil {
		return nil, err
	}

	return art, nil
}

func (p *PostgresDB) DeleteMavenArtifact(ctx context.Context, id string) error {
	if err := p.q.DeleteMavenArtifact(ctx, id); err != nil {
		return fmt.Errorf("failed to delete maven artifact: %w", err)
	}

	return nil
}

// ListMavenArtifactsByRepo lists every maven artifact for a repository,
// with its blob's sha256/size resolved via a JOIN (unlike BlobID on the
// other Get* methods here, which is the raw, restore-incompatible blobs.id
// UUID). Used only by backup export.
func (p *PostgresDB) ListMavenArtifactsByRepo(ctx context.Context, repoID string) ([]*models.MavenArtifact, error) {
	rows, err := p.q.ListMavenArtifactsByRepo(ctx, repoID)
	if err != nil {
		return nil, fmt.Errorf("failed to list maven artifacts by repo: %w", err)
	}

	var artifacts []*models.MavenArtifact

	for _, row := range rows {
		artifacts = append(artifacts, &models.MavenArtifact{
			ID:                  row.ID,
			RepositoryID:        row.RepositoryID,
			GroupID:             row.GroupID,
			ArtifactCoord:       row.ArtifactIDCoord,
			Version:             row.Version,
			Classifier:          row.Classifier,
			Extension:           row.Extension,
			SnapshotTimestamp:   derefStr(row.SnapshotTimestamp),
			SnapshotBuildNumber: int(row.SnapshotBuildNumber),
			IsSnapshot:          row.IsSnapshot,
			RepoPath:            row.RepoPath,
			BlobSHA256:          row.BlobSha256,
			BlobSizeBytes:       row.BlobSizeBytes,
			CreatedAt:           row.CreatedAt,
			UpdatedAt:           row.UpdatedAt,
		})
	}

	return artifacts, nil
}

func (p *PostgresDB) CreateNPMPackage(ctx context.Context, pkg *models.NPMPackage) (*models.NPMPackage, error) {
	row, err := p.q.CreateNPMPackage(ctx, dbsqlc.CreateNPMPackageParams{
		RepositoryID:   pkg.RepositoryID,
		Scope:          pkg.Scope,
		Name:           pkg.Name,
		FullName:       pkg.FullName,
		LatestVersion:  pkg.LatestVersion,
		PackumentStale: pkg.PackumentStale,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create npm package: %w", err)
	}

	pkg.ID = row.ID
	pkg.CreatedAt = row.CreatedAt
	pkg.UpdatedAt = row.UpdatedAt

	return pkg, nil
}

func (p *PostgresDB) GetNPMPackageByName(ctx context.Context, repoID, fullName string) (*models.NPMPackage, error) {
	row, err := p.q.GetNPMPackageByName(ctx, dbsqlc.GetNPMPackageByNameParams{
		RepositoryID: repoID,
		FullName:     fullName,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get npm package by name: %w", err)
	}

	return &models.NPMPackage{
		ID:             row.ID,
		RepositoryID:   row.RepositoryID,
		Scope:          row.Scope,
		Name:           row.Name,
		FullName:       row.FullName,
		LatestVersion:  row.LatestVersion,
		PackumentStale: row.PackumentStale,
		CreatedAt:      row.CreatedAt,
		UpdatedAt:      row.UpdatedAt,
	}, nil
}

func (p *PostgresDB) DeleteNPMPackage(ctx context.Context, packageID string) error {
	if err := p.q.DeleteNPMPackage(ctx, packageID); err != nil {
		return fmt.Errorf("failed to delete npm package: %w", err)
	}

	return nil
}

func (p *PostgresDB) MarkPackumentStale(ctx context.Context, packageID string) error {
	if err := p.q.MarkPackumentStale(ctx, packageID); err != nil {
		return fmt.Errorf("failed to mark packument stale: %w", err)
	}

	return nil
}

func (p *PostgresDB) CreateNPMVersion(ctx context.Context, v *models.NPMVersion) (*models.NPMVersion, error) {
	row, err := p.q.CreateNPMVersion(ctx, dbsqlc.CreateNPMVersionParams{
		PackageID:       v.PackageID,
		Version:         v.Version,
		Column3:         v.ManifestJSON,
		Integrity:       v.Integrity,
		Shasum:          v.Shasum,
		BlobID:          v.BlobID,
		TarballFilename: v.TarballFilename,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create npm version: %w", err)
	}

	v.ID = row.ID
	v.CreatedAt = row.CreatedAt

	return v, nil
}

func (p *PostgresDB) GetNPMVersion(ctx context.Context, packageID, version string) (*models.NPMVersion, error) {
	row, err := p.q.GetNPMVersion(ctx, dbsqlc.GetNPMVersionParams{
		PackageID: packageID,
		Version:   version,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get npm version: %w", err)
	}

	nv := &models.NPMVersion{
		ID:              row.ID,
		PackageID:       row.PackageID,
		Version:         row.Version,
		ManifestJSON:    row.ManifestJson,
		Integrity:       row.Integrity,
		Shasum:          row.Shasum,
		BlobID:          row.BlobID,
		TarballFilename: row.TarballFilename,
		CreatedAt:       row.CreatedAt,
	}

	if err := p.fillNPMBlobSHA256(ctx, packageID, nv); err != nil {
		return nil, err
	}

	return nv, nil
}

func (p *PostgresDB) GetNPMVersionsByPackage(ctx context.Context, packageID string) ([]*models.NPMVersion, error) {
	rows, err := p.q.GetNPMVersionsByPackage(ctx, packageID)
	if err != nil {
		return nil, fmt.Errorf("failed to query npm versions: %w", err)
	}

	var versions []*models.NPMVersion

	for _, row := range rows {
		versions = append(versions, &models.NPMVersion{
			ID:              row.ID,
			PackageID:       row.PackageID,
			Version:         row.Version,
			ManifestJSON:    row.ManifestJson,
			Integrity:       row.Integrity,
			Shasum:          row.Shasum,
			BlobID:          row.BlobID,
			TarballFilename: row.TarballFilename,
			CreatedAt:       row.CreatedAt,
		})
	}

	if err := p.fillNPMBlobSHA256(ctx, packageID, versions...); err != nil {
		return nil, err
	}

	return versions, nil
}

func (p *PostgresDB) GetNPMVersionStates(ctx context.Context, packageID string) ([]models.NPMVersionState, error) {
	rows, err := p.q.GetNPMVersionStates(ctx, packageID)
	if err != nil {
		return nil, fmt.Errorf("failed to query npm version states: %w", err)
	}

	states := make([]models.NPMVersionState, len(rows))
	for i, row := range rows {
		states[i] = models.NPMVersionState{Version: row.Version, Deprecated: row.Deprecated}
	}

	return states, nil
}

func (p *PostgresDB) UpdateNPMPackageLatestVersion(ctx context.Context, packageID, latestVersion string) error {
	if err := p.q.UpdateNPMPackageLatestVersion(ctx, dbsqlc.UpdateNPMPackageLatestVersionParams{
		ID:            packageID,
		LatestVersion: latestVersion,
	}); err != nil {
		return fmt.Errorf("failed to update npm package latest version: %w", err)
	}

	return nil
}

func (p *PostgresDB) UpdateNPMVersionManifest(ctx context.Context, packageID, version string, manifest []byte) error {
	if err := p.q.UpdateNPMVersionManifest(ctx, dbsqlc.UpdateNPMVersionManifestParams{
		PackageID: packageID,
		Version:   version,
		Column3:   manifest,
	}); err != nil {
		return fmt.Errorf("failed to update npm version manifest: %w", err)
	}

	return nil
}

func (p *PostgresDB) DeleteNPMVersion(ctx context.Context, packageID, version string) error {
	if err := p.q.DeleteNPMVersion(ctx, dbsqlc.DeleteNPMVersionParams{
		PackageID: packageID,
		Version:   version,
	}); err != nil {
		return fmt.Errorf("failed to delete npm version: %w", err)
	}

	return nil
}

func (p *PostgresDB) UpsertNPMDistTag(ctx context.Context, t *models.NPMDistTag) (*models.NPMDistTag, error) {
	row, err := p.q.UpsertNPMDistTag(ctx, dbsqlc.UpsertNPMDistTagParams{
		PackageID: t.PackageID,
		Tag:       t.Tag,
		Version:   t.Version,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to upsert npm dist tag: %w", err)
	}

	t.ID = row.ID
	t.UpdatedAt = row.UpdatedAt

	return t, nil
}

func (p *PostgresDB) GetNPMDistTags(ctx context.Context, packageID string) (map[string]string, error) {
	rows, err := p.q.GetNPMDistTags(ctx, packageID)
	if err != nil {
		return nil, fmt.Errorf("failed to query npm dist tags: %w", err)
	}

	tags := make(map[string]string)

	for _, row := range rows {
		tags[row.Tag] = row.Version
	}

	return tags, nil
}

func (p *PostgresDB) DeleteNPMDistTag(ctx context.Context, packageID, tag string) error {
	if err := p.q.DeleteNPMDistTag(ctx, dbsqlc.DeleteNPMDistTagParams{
		PackageID: packageID,
		Tag:       tag,
	}); err != nil {
		return fmt.Errorf("failed to delete npm dist tag: %w", err)
	}

	return nil
}

// ListAllNPMPackages lists every npm package for a repository. Used only
// by backup export.
func (p *PostgresDB) ListAllNPMPackages(ctx context.Context, repoID string) ([]*models.NPMPackage, error) {
	rows, err := p.q.ListAllNPMPackages(ctx, repoID)
	if err != nil {
		return nil, fmt.Errorf("failed to list all npm packages: %w", err)
	}

	var packages []*models.NPMPackage

	for _, row := range rows {
		packages = append(packages, &models.NPMPackage{
			ID:             row.ID,
			RepositoryID:   row.RepositoryID,
			Scope:          row.Scope,
			Name:           row.Name,
			FullName:       row.FullName,
			LatestVersion:  row.LatestVersion,
			PackumentStale: row.PackumentStale,
			CreatedAt:      row.CreatedAt,
			UpdatedAt:      row.UpdatedAt,
		})
	}

	return packages, nil
}

// ListNPMVersionsByRepo lists every npm version for a repository, with its
// tarball blob's sha256/size resolved via a JOIN (unlike BlobID on the
// other Get* methods here, which is the raw, restore-incompatible blobs.id
// UUID). Used only by backup export.
func (p *PostgresDB) ListNPMVersionsByRepo(ctx context.Context, repoID string) ([]*models.NPMVersion, error) {
	rows, err := p.q.ListNPMVersionsByRepo(ctx, repoID)
	if err != nil {
		return nil, fmt.Errorf("failed to list npm versions by repo: %w", err)
	}

	var versions []*models.NPMVersion

	for _, row := range rows {
		versions = append(versions, &models.NPMVersion{
			ID:              row.ID,
			PackageID:       row.PackageID,
			Version:         row.Version,
			ManifestJSON:    row.ManifestJson,
			Integrity:       row.Integrity,
			Shasum:          row.Shasum,
			TarballFilename: row.TarballFilename,
			BlobSHA256:      row.BlobSha256,
			BlobSizeBytes:   row.BlobSizeBytes,
			CreatedAt:       row.CreatedAt,
		})
	}

	return versions, nil
}

// UpsertBlob inserts a blob row keyed by sha256 and returns its id, using ON
// CONFLICT to handle concurrent uploads safely.
func (p *PostgresDB) UpsertBlob(ctx context.Context, sha256, blobKey string, sizeBytes int64) (string, error) {
	if err := validateBlobSHA256(sha256); err != nil {
		return "", err
	}

	var blobID string
	err := p.db.QueryRowContext(ctx,
		`INSERT INTO blobs (sha256, size_bytes, blob_key)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (sha256) DO UPDATE SET
		     size_bytes = CASE WHEN blobs.size_bytes = 0 THEN EXCLUDED.size_bytes ELSE blobs.size_bytes END
		 RETURNING id`,
		sha256, sizeBytes, blobKey,
	).Scan(&blobID)
	if err != nil {
		return "", fmt.Errorf("upsert blob: %w", err)
	}
	return blobID, nil
}

func (p *PostgresDB) ArtifactExists(ctx context.Context, sha256 string, repositoryID string) (bool, error) {
	var exists bool
	err := p.db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM deb_packages dp JOIN blobs b ON b.id = dp.blob_id WHERE b.sha256 = $1 AND dp.repository_id = $2 AND dp.deleted_at IS NULL UNION ALL SELECT 1 FROM rpm_packages rp JOIN blobs b ON b.id = rp.blob_id WHERE b.sha256 = $1 AND rp.repository_id = $2 AND rp.deleted_at IS NULL)`,
		sha256, repositoryID,
	).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("failed to check artifact existence: %w", err)
	}

	return exists, nil
}

// ArtifactExistsBySHA256 checks whether a blob with the given SHA-256 hash exists
// in any repository. Returns the repository ID of the first match.
func (p *PostgresDB) ArtifactExistsBySHA256(ctx context.Context, sha256 string) (string, bool, error) {
	var repoID string

	err := p.db.QueryRowContext(ctx,
		`SELECT dp.repository_id FROM deb_packages dp JOIN blobs b ON b.id = dp.blob_id WHERE b.sha256 = $1 AND dp.deleted_at IS NULL UNION ALL SELECT rp.repository_id FROM rpm_packages rp JOIN blobs b ON b.id = rp.blob_id WHERE b.sha256 = $1 AND rp.deleted_at IS NULL LIMIT 1`,
		sha256,
	).Scan(&repoID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, nil
		}

		return "", false, fmt.Errorf("failed to check artifact existence by sha256: %w", err)
	}

	return repoID, true, nil
}

// BlobReferenceCount returns the number of rows, across every package table
// that stores a blob_id FK into the global blobs table, referencing the blob
// identified by sha256. A count of 0 means the blob is safe to delete from
// storage (see SafeDeleteBlob in internal/storage/refcount.go). A blob that
// does not exist in the blobs table is still checked against the tables that
// reference storage objects by sha256 alone (generated metadata, deltas,
// release bundles, proxy cache).
func (p *PostgresDB) BlobReferenceCount(ctx context.Context, sha256 string) (int, error) {
	return blobRefCount(ctx, p.db, sha256)
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
	{"rubygems_gems", "blob_id = b.id"},
	{"conda_packages", "blob_id = b.id"},
	{"conan_packages", "blob_id = b.id"},
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

// blobRefSumExpr is the SQL sum of every reference to blob alias b.
var blobRefSumExpr = buildBlobRefExpr(" +\n", "(SELECT COUNT(*) FROM %s WHERE %s)") //nolint:gochecknoglobals // built once, read-only

// blobUnreferencedExpr is true when no table references blob alias b. NOT
// EXISTS short-circuits at the first hit and never counts, unlike
// blobRefSumExpr = 0.
var blobUnreferencedExpr = buildBlobRefExpr(" AND\n", "NOT EXISTS (SELECT 1 FROM %s WHERE %s)") //nolint:gochecknoglobals // built once, read-only

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

func blobRefCount(ctx context.Context, q rowQuerier, sha256 string) (int, error) {
	var count int

	err := q.QueryRowContext(ctx,
		`SELECT
			`+blobRefSumExpr+`
		 FROM (SELECT $1::text AS sha256, (SELECT id FROM blobs WHERE sha256 = $1::text) AS id) b`,
		sha256,
	).Scan(&count)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}

	if err != nil {
		return 0, fmt.Errorf("failed to count blob references: %w", err)
	}

	return count, nil
}

// Package delta methods

func (p *PostgresDB) CreatePackageDelta(ctx context.Context, d *models.PackageDelta) error {
	_, err := p.db.ExecContext(ctx, `
		INSERT INTO package_deltas
			(id, repository_id, from_sha256, from_filename, to_sha256, to_filename, delta_sha256, delta_size, format)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (repository_id, from_sha256, to_sha256) DO NOTHING`,
		d.RepositoryID, d.FromSHA256, d.FromFilename, d.ToSHA256, d.ToFilename,
		d.DeltaSHA256, d.DeltaSize, d.Format,
	)
	if err != nil {
		return fmt.Errorf("create package delta: %w", err)
	}

	return nil
}

func (p *PostgresDB) GetPackageDelta(ctx context.Context, repoID, fromSHA256, toSHA256 string) (*models.PackageDelta, error) {
	var d models.PackageDelta

	err := p.db.QueryRowContext(ctx, `
		SELECT id, repository_id, from_sha256, from_filename, to_sha256, to_filename,
		       delta_sha256, delta_size, format, created_at
		FROM package_deltas
		WHERE repository_id = $1 AND from_sha256 = $2 AND to_sha256 = $3`,
		repoID, fromSHA256, toSHA256,
	).Scan(&d.ID, &d.RepositoryID, &d.FromSHA256, &d.FromFilename, &d.ToSHA256, &d.ToFilename,
		&d.DeltaSHA256, &d.DeltaSize, &d.Format, &d.CreatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
		}

		return nil, fmt.Errorf("get package delta: %w", err)
	}

	return &d, nil
}

// ListPackageDeltas returns the deltas that target toSHA256. A delta whose
// source package is in the trash is hidden (it would reveal the trashed file's
// sha256 and name) and reappears when the source is restored; a source that
// still has a live row (same blob in another row) keeps its deltas visible.
func (p *PostgresDB) ListPackageDeltas(ctx context.Context, repoID, toSHA256 string) ([]*models.PackageDelta, error) {
	rows, err := p.db.QueryContext(ctx, `
		SELECT d.id, d.repository_id, d.from_sha256, d.from_filename, d.to_sha256, d.to_filename,
		       d.delta_sha256, d.delta_size, d.format, d.created_at
		FROM package_deltas d
		WHERE d.repository_id = $1 AND d.to_sha256 = $2
		  AND (
		    EXISTS (SELECT 1 FROM rpm_packages s JOIN blobs b ON b.id = s.blob_id
		            WHERE s.repository_id = d.repository_id AND b.sha256 = d.from_sha256 AND s.deleted_at IS NULL)
		    OR EXISTS (SELECT 1 FROM deb_packages s JOIN blobs b ON b.id = s.blob_id
		               WHERE s.repository_id = d.repository_id AND b.sha256 = d.from_sha256 AND s.deleted_at IS NULL)
		    OR NOT (
		      EXISTS (SELECT 1 FROM rpm_packages s JOIN blobs b ON b.id = s.blob_id
		              WHERE s.repository_id = d.repository_id AND b.sha256 = d.from_sha256 AND s.deleted_at IS NOT NULL)
		      OR EXISTS (SELECT 1 FROM deb_packages s JOIN blobs b ON b.id = s.blob_id
		                 WHERE s.repository_id = d.repository_id AND b.sha256 = d.from_sha256 AND s.deleted_at IS NOT NULL)
		    )
		  )
		ORDER BY d.created_at DESC`,
		repoID, toSHA256,
	)
	if err != nil {
		return nil, fmt.Errorf("list package deltas: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var deltas []*models.PackageDelta

	for rows.Next() {
		var d models.PackageDelta
		if err := rows.Scan(&d.ID, &d.RepositoryID, &d.FromSHA256, &d.FromFilename, &d.ToSHA256, &d.ToFilename,
			&d.DeltaSHA256, &d.DeltaSize, &d.Format, &d.CreatedAt); err != nil {
			return nil, fmt.Errorf("list package deltas: scan: %w", err)
		}

		deltas = append(deltas, &d)
	}

	return deltas, rows.Err()
}

// PruneRepoDeltas deletes the oldest package_deltas rows of a repository until
// at most maxCount rows totalling at most maxBytes remain, and returns the
// delta blob sha256s of the deleted rows (callers release them through
// storage.SafeDeleteBlob once the rows are gone).
func (p *PostgresDB) PruneRepoDeltas(ctx context.Context, repoID string, maxCount int, maxBytes int64) ([]string, error) {
	rows, err := p.db.QueryContext(ctx, `
		WITH ranked AS (
			SELECT id,
			       row_number() OVER w AS rn,
			       sum(delta_size) OVER w AS cum
			FROM package_deltas
			WHERE repository_id = $1
			WINDOW w AS (ORDER BY created_at DESC, id)
		)
		DELETE FROM package_deltas d
		USING ranked r
		WHERE d.id = r.id AND (r.rn > $2 OR r.cum > $3)
		RETURNING d.delta_sha256`,
		repoID, maxCount, maxBytes,
	)
	if err != nil {
		return nil, fmt.Errorf("prune package deltas: %w", err)
	}

	defer func() { _ = rows.Close() }()

	var shas []string

	for rows.Next() {
		var sha string
		if err := rows.Scan(&sha); err != nil {
			return nil, fmt.Errorf("prune package deltas: scan: %w", err)
		}

		shas = append(shas, sha)
	}

	return shas, rows.Err()
}

func (p *PostgresDB) DeletePackageDelta(ctx context.Context, id string) error {
	_, err := p.db.ExecContext(ctx, `DELETE FROM package_deltas WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete package delta: %w", err)
	}

	return nil
}

// Proxy/Group repository methods

func (p *PostgresDB) AddGroupMember(ctx context.Context, groupRepoID, memberRepoID string, priority int) error {
	if err := p.q.AddGroupMember(ctx, dbsqlc.AddGroupMemberParams{
		GroupRepoID:  groupRepoID,
		MemberRepoID: memberRepoID,
		Priority:     int32(priority), //nolint:gosec // G115: priority is bounded
	}); err != nil {
		return fmt.Errorf("failed to add group member: %w", err)
	}

	return nil
}

func (p *PostgresDB) RemoveGroupMember(ctx context.Context, groupRepoID, memberRepoID string) error {
	if err := p.q.RemoveGroupMember(ctx, dbsqlc.RemoveGroupMemberParams{
		GroupRepoID:  groupRepoID,
		MemberRepoID: memberRepoID,
	}); err != nil {
		return fmt.Errorf("failed to remove group member: %w", err)
	}

	return nil
}

func (p *PostgresDB) ListGroupMembers(ctx context.Context, groupRepoID string) ([]*models.RepositoryGroupMember, error) {
	rows, err := p.q.ListGroupMembers(ctx, groupRepoID)
	if err != nil {
		return nil, fmt.Errorf("failed to list group members: %w", err)
	}

	members := make([]*models.RepositoryGroupMember, len(rows))
	for i, row := range rows {
		members[i] = &models.RepositoryGroupMember{
			GroupRepoID:  row.GroupRepoID,
			MemberRepoID: row.MemberRepoID,
			Priority:     int(row.Priority),
			CreatedAt:    row.CreatedAt,
		}
	}

	return members, nil
}

func (p *PostgresDB) ListGroupMemberRepositories(ctx context.Context, groupRepoID string) ([]*models.Repository, error) {
	rows, err := p.db.QueryContext(ctx, `
		WITH links AS (
			SELECT member_repo_id, priority, created_at AS linked_at
			FROM repository_group_members WHERE group_repo_id = $1
		)
		SELECT `+repoColumns+`
		FROM repositories JOIN links ON links.member_repo_id = repositories.id
		WHERE repositories.deleted_at IS NULL
		ORDER BY links.priority, links.linked_at, links.member_repo_id`, groupRepoID)
	if err != nil {
		return nil, fmt.Errorf("failed to list group member repositories: %w", err)
	}

	defer func() { _ = rows.Close() }()

	var repos []*models.Repository

	for rows.Next() {
		r := &models.Repository{}
		if err := rows.Scan(repoScanDest(r)...); err != nil {
			return nil, fmt.Errorf("failed to scan group member repository: %w", err)
		}

		repos = append(repos, r)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to list group member repositories: %w", err)
	}

	return repos, nil
}

func (p *PostgresDB) GetProxyCacheEntry(ctx context.Context, repoID, cacheKey string) (*models.ProxyCacheEntry, error) {
	row, err := p.q.GetProxyCacheEntry(ctx, dbsqlc.GetProxyCacheEntryParams{
		RepositoryID: repoID,
		CacheKey:     cacheKey,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get proxy cache entry: %w", err)
	}

	return &models.ProxyCacheEntry{
		ID:           row.ID,
		RepositoryID: row.RepositoryID,
		CacheKey:     row.CacheKey,
		BlobKey:      row.BlobKey,
		ContentType:  row.ContentType,
		StatusCode:   int(row.StatusCode),
		SizeBytes:    row.SizeBytes,
		HitCount:     int(row.HitCount),
		LastHitAt:    row.LastHitAt,
		ExpiresAt:    row.ExpiresAt,
		CreatedAt:    row.CreatedAt,
		UpdatedAt:    row.UpdatedAt,
	}, nil
}

// maxNegativeProxyEntriesPerRepo bounds the live 404 rows one proxy repository
// may hold; beyond it new missing paths are simply not negative-cached.
const maxNegativeProxyEntriesPerRepo = 10000

func (p *PostgresDB) UpsertProxyCacheEntry(ctx context.Context, entry *models.ProxyCacheEntry) error {
	if entry.StatusCode == http.StatusNotFound && entry.BlobKey == "" {
		if err := p.q.UpsertNegativeProxyCacheEntry(ctx, dbsqlc.UpsertNegativeProxyCacheEntryParams{
			RepositoryID: entry.RepositoryID,
			CacheKey:     entry.CacheKey,
			ExpiresAt:    entry.ExpiresAt,
			Limit:        maxNegativeProxyEntriesPerRepo,
		}); err != nil {
			return fmt.Errorf("failed to upsert negative proxy cache entry: %w", err)
		}

		return nil
	}

	row, err := p.q.UpsertProxyCacheEntry(ctx, dbsqlc.UpsertProxyCacheEntryParams{
		RepositoryID: entry.RepositoryID,
		CacheKey:     entry.CacheKey,
		Column3:      entry.BlobKey,
		Column4:      entry.ContentType,
		StatusCode:   int32(entry.StatusCode), //nolint:gosec // G115: status code is bounded
		SizeBytes:    entry.SizeBytes,
		HitCount:     int64(entry.HitCount),
		LastHitAt:    entry.LastHitAt,
		ExpiresAt:    entry.ExpiresAt,
	})
	if err != nil {
		return fmt.Errorf("failed to upsert proxy cache entry: %w", err)
	}

	entry.ID = row.ID
	entry.CreatedAt = row.CreatedAt
	entry.UpdatedAt = row.UpdatedAt

	return nil
}

func (p *PostgresDB) DeleteProxyCacheEntry(ctx context.Context, repoID, cacheKey string) error {
	if err := p.q.DeleteProxyCacheEntry(ctx, dbsqlc.DeleteProxyCacheEntryParams{
		RepositoryID: repoID,
		CacheKey:     cacheKey,
	}); err != nil {
		return fmt.Errorf("failed to delete proxy cache entry: %w", err)
	}

	return nil
}

func (p *PostgresDB) DeleteExpiredProxyCacheEntries(ctx context.Context) (int64, error) {
	n, err := p.q.DeleteExpiredProxyCacheEntries(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to delete expired proxy cache entries: %w", err)
	}

	return n, nil
}

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
	if err != nil {
		return fmt.Errorf("failed to create helm chart: %w", err)
	}

	chart.ID = row.ID
	chart.CreatedAt = row.CreatedAt
	chart.UpdatedAt = row.UpdatedAt

	return nil
}

func (p *PostgresDB) GetHelmChart(ctx context.Context, repoID, name, version string) (*models.HelmChart, error) {
	row, err := p.q.GetHelmChart(ctx, dbsqlc.GetHelmChartParams{
		RepositoryID: repoID,
		Name:         name,
		Version:      version,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get helm chart: %w", err)
	}

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

	return chart, nil
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
	if err != nil {
		return nil, fmt.Errorf("failed to list helm charts: %w", err)
	}

	var charts []*models.HelmChart

	for _, row := range rows {
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

		charts = append(charts, chart)
	}

	return charts, nil
}

func (p *PostgresDB) ListHelmChartVersions(ctx context.Context, repoID, name string) ([]*models.HelmChart, error) {
	rows, err := p.q.ListHelmChartVersions(ctx, dbsqlc.ListHelmChartVersionsParams{
		RepositoryID: repoID,
		Name:         name,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list helm chart versions: %w", err)
	}

	var versions []*models.HelmChart

	for _, row := range rows {
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

		versions = append(versions, chart)
	}

	return versions, nil
}

func (p *PostgresDB) CreatePyPIPackage(ctx context.Context, pkg *models.PyPIPackage) error {
	// pypi_packages has no md5_digest column and the blobs table only tracks
	// sha256, so pkg.MD5Digest does not round-trip.
	blobID, err := p.upsertBlobWithMediaType(ctx, pkg.SHA256Digest, pkg.BlobKey, "application/zip", pkg.SizeBytes)
	if err != nil {
		return fmt.Errorf("failed to upsert blob for pypi package: %w", err)
	}

	row, err := p.q.CreatePyPIPackage(ctx, dbsqlc.CreatePyPIPackageParams{
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
	if err != nil {
		return fmt.Errorf("failed to create pypi package: %w", err)
	}

	pkg.ID = row.ID
	pkg.CreatedAt = row.CreatedAt
	pkg.UpdatedAt = row.UpdatedAt

	return nil
}

func (p *PostgresDB) GetPyPIPackage(ctx context.Context, repoID, name, version, filename string) (*models.PyPIPackage, error) {
	row, err := p.q.GetPyPIPackage(ctx, dbsqlc.GetPyPIPackageParams{
		RepositoryID: repoID,
		Name:         name,
		Version:      version,
		Filename:     filename,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get pypi package: %w", err)
	}

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
	}, nil
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
	}, nil
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
	if err != nil {
		return nil, fmt.Errorf("failed to list pypi packages: %w", err)
	}

	var packages []*models.PyPIPackage

	for _, row := range rows {
		pkg := &models.PyPIPackage{
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
		packages = append(packages, pkg)
	}

	return packages, nil
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
	if err != nil {
		return nil, fmt.Errorf("failed to list pypi package versions: %w", err)
	}

	var packages []*models.PyPIPackage

	for _, row := range rows {
		pkg := &models.PyPIPackage{
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
		packages = append(packages, pkg)
	}

	return packages, nil
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

	if err != nil {
		return fmt.Errorf("failed to create nuget package: %w", err)
	}

	pkg.ID = row.ID
	pkg.CreatedAt = row.CreatedAt
	pkg.UpdatedAt = row.UpdatedAt

	return nil
}

func (p *PostgresDB) GetNuGetPackage(ctx context.Context, repoID, packageID, version string) (*models.NuGetPackage, error) {
	row, err := p.q.GetNuGetPackage(ctx, dbsqlc.GetNuGetPackageParams{
		RepositoryID: repoID,
		PackageID:    packageID,
		Version:      version,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get nuget package: %w", err)
	}

	return nugetRowToModel(row), nil
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
	if err != nil {
		return nil, fmt.Errorf("failed to list nuget packages: %w", err)
	}

	var packages []*models.NuGetPackage

	for _, row := range rows {
		packages = append(packages, nugetRowToModel(dbsqlc.GetNuGetPackageRow(row)))
	}

	return packages, nil
}

func (p *PostgresDB) ListNuGetPackageVersions(ctx context.Context, repoID, packageID string) ([]*models.NuGetPackage, error) {
	rows, err := p.q.ListNuGetPackageVersions(ctx, dbsqlc.ListNuGetPackageVersionsParams{
		RepositoryID: repoID,
		PackageID:    packageID,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list nuget package versions: %w", err)
	}

	var packages []*models.NuGetPackage

	for _, row := range rows {
		packages = append(packages, nugetRowToModel(dbsqlc.GetNuGetPackageRow(row)))
	}

	return packages, nil
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

func (p *PostgresDB) InsertAuditLog(ctx context.Context, event *models.AuditLog) error {
	var ip *netip.Addr
	if event.IPAddress != nil {
		addr, err := netip.ParseAddr(*event.IPAddress)
		if err == nil {
			ip = &addr
		}
	}
	if err := p.q.InsertAuditLog(ctx, dbsqlc.InsertAuditLogParams{
		Action:       event.Action,
		ActorID:      event.ActorID,
		ActorType:    event.ActorType,
		DetailsJson:  event.DetailsJSON,
		ResourceType: event.ResourceType,
		ResourceID:   event.ResourceID,
		RepositoryID: event.RepositoryID,
		ProjectID:    event.ProjectID,
		IpAddress:    ip,
	}); err != nil {
		return fmt.Errorf("insert audit log: %w", err)
	}

	return nil
}

// auditExactCountMax bounds the work spent on an audit total: tables the
// planner estimates at or below it are counted exactly, larger ones are not
// counted at all (see auditTotal).
const auditExactCountMax = 10_000

// ErrInvalidAuditLimit is returned by ListAuditLogs for a page size below 1.
var ErrInvalidAuditLimit = errors.New("audit page limit must be positive")

// auditFilterWhere builds the project/actor/action predicate shared by the page
// and count queries. Placeholders start at $1.
func auditFilterWhere(params models.AuditListParams) (string, []any) {
	projectID, actor, action := params.ProjectID, params.Actor, params.Action

	where := "WHERE 1=1"
	args := []any{}

	// A project's activity is every entry naming the project plus every entry
	// of a repository it owns now (repository events carry no project_id).
	// The repository branch is written as = ANY(ARRAY(subquery)): the owned
	// repository ids are evaluated once into an array, which lets the planner
	// BitmapOr the project_id and repository_id indexes (an IN (subquery)
	// becomes a semi-join and forces a scan of the whole table).
	if projectID != "" {
		args = append(args, projectID)
		where += fmt.Sprintf(" AND (project_id = $%[1]d::uuid OR repository_id = ANY(ARRAY(SELECT id FROM repositories WHERE project_id = $%[1]d::uuid)))", len(args))
	}

	// Project token mint/revoke events are for maintainers and above only.
	if params.HideProjectTokenEvents {
		where += ` AND action NOT LIKE 'project.token\_%'`
	}

	if action != "" {
		args = append(args, "%"+escapeLike(action)+"%")
		where += fmt.Sprintf(" AND action ILIKE $%d", len(args))
	}

	// The human-readable actor (username / token name) is stored in
	// details_json; the user UUID is in actor_id. Accept either.
	if actor != "" {
		args = append(args, "%"+escapeLike(actor)+"%")
		where += fmt.Sprintf(" AND (details_json->>'actor' ILIKE $%[1]d OR actor_id::text ILIKE $%[1]d)", len(args))
	}

	return where, args
}

// ListAuditLogs returns one page of audit entries, newest first. It reads
// Limit+1 rows to learn whether more follow, so no COUNT(*) runs for keyset
// (After) paging, and offset paging only counts when the page itself cannot
// answer (see auditTotal).
func (p *PostgresDB) ListAuditLogs(ctx context.Context, params models.AuditListParams) (*models.AuditPage, error) {
	if params.Limit < 1 {
		return nil, fmt.Errorf("list audit logs: %w, got %d", ErrInvalidAuditLimit, params.Limit)
	}

	where, args := auditFilterWhere(params)
	filtered := len(args) > 0
	offset := params.Offset

	if c := params.After; c != nil {
		// created_at <= $c is the index condition; the OR only trims the
		// rows tied with the cursor.
		args = append(args, c.CreatedAt, c.ID)
		where += fmt.Sprintf(" AND created_at <= $%[1]d AND (created_at < $%[1]d OR id > $%[2]d::uuid)", len(args)-1, len(args))
		offset = 0
	}

	args = append(args, params.Limit+1, offset)
	// The formatted parts are the predicate built by auditFilterWhere from
	// constant clauses and $n placeholders (values are always bound
	// arguments) and placeholder numbers; no caller input reaches the text.
	query := fmt.Sprintf("SELECT id, action, project_id, repository_id, actor_id, actor_type, details_json::text, resource_type, resource_id, host(ip_address) AS ip_address, created_at FROM audit_logs %s ORDER BY created_at DESC, id LIMIT $%d OFFSET $%d", where, len(args)-1, len(args)) //nolint:gosec // G201: where is built from constant clauses with bound placeholders

	rows, err := p.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list audit logs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	logs := make([]*models.AuditLog, 0, params.Limit+1)

	for rows.Next() {
		l := &models.AuditLog{}

		var (
			detailsStr string
			ipStr      *string
		)

		if err := rows.Scan(&l.ID, &l.Action, &l.ProjectID, &l.RepositoryID, &l.ActorID, &l.ActorType, &detailsStr, &l.ResourceType, &l.ResourceID, &ipStr, &l.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan audit log: %w", err)
		}

		l.IPAddress = ipStr
		l.DetailsJSON = json.RawMessage(detailsStr)
		logs = append(logs, l)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list audit logs: %w", err)
	}

	page := &models.AuditPage{Logs: logs}

	hasMore := len(logs) > params.Limit
	if hasMore {
		page.Logs = logs[:params.Limit]
		last := page.Logs[params.Limit-1]
		page.Next = &models.AuditCursor{CreatedAt: last.CreatedAt, ID: last.ID}
	}

	if params.After != nil || params.NoTotal {
		return page, nil
	}

	// Count args are the filter args only (the cursor branch returned above).
	page.Total, page.TotalExact, err = p.auditTotal(ctx, where, args[:len(args)-2], filtered, offset, len(page.Logs), hasMore)
	if err != nil {
		return nil, err
	}

	return page, nil
}

// auditTotal reports the number of matching audit entries without a full
// COUNT(*) whenever the page already answers it. args are the filter
// arguments of where; offset and returned describe the page just read, and
// hasMore whether a further entry exists.
//
//   - Last page (or an empty first page): offset+returned is the exact total.
//   - Otherwise the table size decides. Small (planner estimate <= cap) or
//     never analysed: a count bounded to cap+1 rows (exact up to the cap, a
//     lower bound above it). Large: unfiltered reports the planner estimate,
//     filtered the lower bound offset+returned+1; neither touches the table.
//
// An empty page past the end of a large filtered result still needs the
// bounded count, because offset says nothing about the real total there.
func (p *PostgresDB) auditTotal(ctx context.Context, where string, args []any, filtered bool, offset, returned int, hasMore bool) (int, bool, error) {
	seen := offset + returned
	if !hasMore && (returned > 0 || offset == 0) {
		return seen, true, nil
	}

	var estimate int64

	err := p.db.QueryRowContext(ctx, `SELECT reltuples::bigint FROM pg_class WHERE oid = 'audit_logs'::regclass`).Scan(&estimate)
	if err != nil {
		return 0, false, fmt.Errorf("estimate audit logs: %w", err)
	}

	if estimate > auditExactCountMax {
		switch {
		case !filtered && hasMore:
			return max(int(estimate), seen+1), false, nil
		case !filtered:
			return int(estimate), false, nil
		case hasMore:
			return seen + 1, false, nil
		}
	}

	var counted int

	countQuery := fmt.Sprintf("SELECT count(*) FROM (SELECT 1 FROM audit_logs %s LIMIT $%d) s", where, len(args)+1) //nolint:gosec // G201: where is built from constant clauses with bound placeholders
	if err := p.db.QueryRowContext(ctx, countQuery, append(args, auditExactCountMax+1)...).Scan(&counted); err != nil {
		return 0, false, fmt.Errorf("count audit logs: %w", err)
	}

	return counted, counted <= auditExactCountMax, nil
}

func (p *PostgresDB) ListAllTokens(ctx context.Context, page, perPage int) ([]*models.Token, int, error) {
	var total int
	if err := p.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM auth_tokens WHERE revoked_at IS NULL").Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count tokens: %w", err)
	}

	rows, err := p.db.QueryContext(ctx,
		`SELECT `+tokenColumns+` FROM `+tokenFrom+` WHERE t.revoked_at IS NULL ORDER BY t.created_at DESC, t.id LIMIT $1 OFFSET $2`,
		perPage, (page-1)*perPage,
	)
	if err != nil {
		return nil, 0, fmt.Errorf("list all tokens: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var tokens []*models.Token

	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("scan token: %w", err)
		}

		tokens = append(tokens, t)
	}

	return tokens, total, rows.Err()
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
		RepositoryID: mod.RepositoryID,
		ModulePath:   mod.ModulePath,
		Version:      mod.Version,
		GoModContent: mod.GoModContent,
		ZipBlobID:    &zipBlobID,
		ModBlobID:    &modBlobID,
	})
	if err != nil {
		return fmt.Errorf("failed to create go module: %w", err)
	}

	mod.ID = row.ID
	mod.CreatedAt = row.CreatedAt
	mod.UpdatedAt = row.UpdatedAt

	return nil
}

// GetGoModule retrieves a Go module by repo, path, and version.
func (p *PostgresDB) GetGoModule(ctx context.Context, repoID, modulePath, version string) (*models.GoModule, error) {
	row, err := p.q.GetGoModule(ctx, dbsqlc.GetGoModuleParams{
		RepositoryID: repoID,
		ModulePath:   modulePath,
		Version:      version,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get go module: %w", err)
	}

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
	}, nil
}

// DeleteGoModule deletes a Go module.
func (p *PostgresDB) DeleteGoModule(ctx context.Context, repoID, modulePath, version string) error {
	if err := p.q.DeleteGoModule(ctx, dbsqlc.DeleteGoModuleParams{
		RepositoryID: repoID,
		ModulePath:   modulePath,
		Version:      version,
	}); err != nil {
		return fmt.Errorf("failed to delete go module: %w", err)
	}

	return nil
}

// ListGoModuleVersions lists all versions of a module.
func (p *PostgresDB) ListGoModuleVersions(ctx context.Context, repoID, modulePath string) ([]*models.GoModule, error) {
	rows, err := p.q.ListGoModuleVersions(ctx, dbsqlc.ListGoModuleVersionsParams{
		RepositoryID: repoID,
		ModulePath:   modulePath,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list go module versions: %w", err)
	}

	var modules []*models.GoModule

	for _, row := range rows {
		modules = append(modules, &models.GoModule{
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
		})
	}

	return modules, nil
}

// ListGoModulesByRepo lists every module version for a repository. Used
// only by backup export, where ListGoModuleVersions' mandatory module path
// filter would silently drop every other module.
func (p *PostgresDB) ListGoModulesByRepo(ctx context.Context, repoID string) ([]*models.GoModule, error) {
	rows, err := p.q.ListGoModulesByRepo(ctx, repoID)
	if err != nil {
		return nil, fmt.Errorf("failed to list go modules by repo: %w", err)
	}

	var modules []*models.GoModule

	for _, row := range rows {
		modules = append(modules, &models.GoModule{
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
		})
	}

	return modules, nil
}

// cleanupPolicyToModel converts a dbsqlc.CleanupPolicy to *models.CleanupPolicy.
func cleanupPolicyToModel(row dbsqlc.CleanupPolicy) *models.CleanupPolicy {
	lastRunAt := row.LastRunAt

	var keepLastN *int
	if row.KeepLastN != nil {
		v := int(*row.KeepLastN)
		keepLastN = &v
	}

	var keepSinceDays *int
	if row.KeepSinceDays != nil {
		v := int(*row.KeepSinceDays)
		keepSinceDays = &v
	}

	var keepDownloadedSinceDays *int
	if row.KeepDownloadedSinceDays != nil {
		v := int(*row.KeepDownloadedSinceDays)
		keepDownloadedSinceDays = &v
	}

	var lastRunDeleted *int
	if row.LastRunDeleted != nil {
		v := int(*row.LastRunDeleted)
		lastRunDeleted = &v
	}

	return &models.CleanupPolicy{
		ID:                      row.ID,
		RepositoryID:            derefStr(row.RepositoryID),
		ProjectID:               derefStr(row.ProjectID),
		Name:                    row.Name,
		Enabled:                 row.Enabled,
		KeepLastN:               keepLastN,
		KeepSinceDays:           keepSinceDays,
		KeepDownloadedSinceDays: keepDownloadedSinceDays,
		NamePattern:             derefStr(row.NamePattern),
		ScheduleCron:            row.ScheduleCron,
		LastRunAt:               lastRunAt,
		LastRunDeleted:          lastRunDeleted,
		LastRunError:            derefStr(row.LastRunError),
		CreatedAt:               row.CreatedAt,
		UpdatedAt:               row.UpdatedAt,
	}
}

// createCleanupPolicyParams builds sqlc params from a models.CleanupPolicy.
func createCleanupPolicyParams(policy *models.CleanupPolicy) dbsqlc.CreateCleanupPolicyParams {
	var keepLastN *int32
	if policy.KeepLastN != nil {
		v := int32(*policy.KeepLastN) //nolint:gosec // G115: bounded by API
		keepLastN = &v
	}

	var keepSinceDays *int32
	if policy.KeepSinceDays != nil {
		v := int32(*policy.KeepSinceDays) //nolint:gosec // G115: bounded by API
		keepSinceDays = &v
	}

	var keepDownloadedSinceDays *int32
	if policy.KeepDownloadedSinceDays != nil {
		v := int32(*policy.KeepDownloadedSinceDays) //nolint:gosec // G115: bounded by API
		keepDownloadedSinceDays = &v
	}

	return dbsqlc.CreateCleanupPolicyParams{
		RepositoryID:            strPtr(policy.RepositoryID),
		ProjectID:               strPtr(policy.ProjectID),
		Name:                    policy.Name,
		Enabled:                 policy.Enabled,
		KeepLastN:               keepLastN,
		KeepSinceDays:           keepSinceDays,
		KeepDownloadedSinceDays: keepDownloadedSinceDays,
		NamePattern:             strPtr(policy.NamePattern),
		ScheduleCron:            policy.ScheduleCron,
	}
}

// CreateCleanupPolicy creates a new cleanup policy.
func (p *PostgresDB) CreateCleanupPolicy(ctx context.Context, policy *models.CleanupPolicy) error {
	row, err := p.q.CreateCleanupPolicy(ctx, createCleanupPolicyParams(policy))
	if err != nil {
		return fmt.Errorf("failed to create cleanup policy: %w", err)
	}

	policy.ID = row.ID
	policy.CreatedAt = row.CreatedAt
	policy.UpdatedAt = row.UpdatedAt

	return nil
}

// GetCleanupPolicy retrieves a cleanup policy by ID.
func (p *PostgresDB) GetCleanupPolicy(ctx context.Context, id string) (*models.CleanupPolicy, error) {
	row, err := p.q.GetCleanupPolicy(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get cleanup policy: %w", err)
	}

	return cleanupPolicyToModel(row), nil
}

// UpdateCleanupPolicy updates an existing cleanup policy.
func (p *PostgresDB) UpdateCleanupPolicy(ctx context.Context, policy *models.CleanupPolicy) error {
	var keepLastN *int32
	if policy.KeepLastN != nil {
		v := int32(*policy.KeepLastN) //nolint:gosec // G115: bounded by API
		keepLastN = &v
	}

	var keepSinceDays *int32
	if policy.KeepSinceDays != nil {
		v := int32(*policy.KeepSinceDays) //nolint:gosec // G115: bounded by API
		keepSinceDays = &v
	}

	var keepDownloadedSinceDays *int32
	if policy.KeepDownloadedSinceDays != nil {
		v := int32(*policy.KeepDownloadedSinceDays) //nolint:gosec // G115: bounded by API
		keepDownloadedSinceDays = &v
	}

	updatedAt, err := p.q.UpdateCleanupPolicy(ctx, dbsqlc.UpdateCleanupPolicyParams{
		ID:                      policy.ID,
		Name:                    policy.Name,
		Enabled:                 policy.Enabled,
		KeepLastN:               keepLastN,
		KeepSinceDays:           keepSinceDays,
		KeepDownloadedSinceDays: keepDownloadedSinceDays,
		NamePattern:             strPtr(policy.NamePattern),
		ScheduleCron:            policy.ScheduleCron,
	})
	if err != nil {
		return fmt.Errorf("failed to update cleanup policy: %w", err)
	}

	policy.UpdatedAt = updatedAt

	return nil
}

// DeleteCleanupPolicy deletes a cleanup policy.
func (p *PostgresDB) DeleteCleanupPolicy(ctx context.Context, id string) error {
	if err := p.q.DeleteCleanupPolicy(ctx, id); err != nil {
		return fmt.Errorf("failed to delete cleanup policy: %w", err)
	}

	return nil
}

// ListCleanupPolicies lists all cleanup policies for a repository.
func (p *PostgresDB) ListCleanupPolicies(ctx context.Context, repoID string) ([]*models.CleanupPolicy, error) {
	rows, err := p.q.ListCleanupPolicies(ctx, &repoID)
	if err != nil {
		return nil, fmt.Errorf("failed to list cleanup policies: %w", err)
	}

	policies := make([]*models.CleanupPolicy, len(rows))
	for i, row := range rows {
		policies[i] = cleanupPolicyToModel(row)
	}

	return policies, nil
}

// ListProjectCleanupPolicies lists the default cleanup policies of a project.
func (p *PostgresDB) ListProjectCleanupPolicies(ctx context.Context, projectID string) ([]*models.CleanupPolicy, error) {
	rows, err := p.q.ListProjectCleanupPolicies(ctx, &projectID)
	if err != nil {
		return nil, fmt.Errorf("failed to list project cleanup policies: %w", err)
	}

	policies := make([]*models.CleanupPolicy, len(rows))
	for i, row := range rows {
		policies[i] = cleanupPolicyToModel(row)
	}

	return policies, nil
}

// ListProjectCleanupRepositories returns the IDs of the live hosted
// repositories a project default policy applies to: those without an enabled
// repository-level policy.
func (p *PostgresDB) ListProjectCleanupRepositories(ctx context.Context, projectID string) ([]string, error) {
	ids, err := p.q.ListProjectCleanupRepositories(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("failed to list project cleanup repositories: %w", err)
	}

	return ids, nil
}

// ListEnabledCleanupPolicies lists all enabled cleanup policies.
func (p *PostgresDB) ListEnabledCleanupPolicies(ctx context.Context) ([]*models.CleanupPolicy, error) {
	rows, err := p.q.ListEnabledCleanupPolicies(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list enabled cleanup policies: %w", err)
	}

	policies := make([]*models.CleanupPolicy, len(rows))
	for i, row := range rows {
		policies[i] = cleanupPolicyToModel(row)
	}

	return policies, nil
}

// UpdateCleanupPolicyRunResult updates the run result of a cleanup policy.
func (p *PostgresDB) UpdateCleanupPolicyRunResult(ctx context.Context, id string, deletedCount int, runErr error) error {
	var errStr *string
	if runErr != nil {
		errMsg := runErr.Error()
		errStr = &errMsg
	}

	deletedCount32 := int32(deletedCount) //nolint:gosec // G115: bounded by policy execution

	if err := p.q.UpdateCleanupPolicyRunResult(ctx, dbsqlc.UpdateCleanupPolicyRunResultParams{
		ID:             id,
		LastRunDeleted: &deletedCount32,
		LastRunError:   errStr,
	}); err != nil {
		return fmt.Errorf("failed to update cleanup policy run result: %w", err)
	}

	return nil
}

// CreateWebhook creates a new webhook.
func (p *PostgresDB) CreateWebhook(ctx context.Context, wh *models.Webhook) error {
	// Convert repoID to uuid.NullUUID
	var repoID *string

	if wh.RepositoryID != "" {
		repoID = &wh.RepositoryID
	}

	row, err := p.q.CreateWebhook(ctx, dbsqlc.CreateWebhookParams{
		RepositoryID:   repoID,
		Name:           wh.Name,
		Url:            wh.URL,
		SecretEnc:      strPtr(wh.SecretEnc),
		Enabled:        wh.Enabled,
		Events:         wh.Events,
		ContentType:    wh.ContentType,
		SslVerify:      wh.SSLVerify,
		MaxRetries:     int32(wh.MaxRetries),     //nolint:gosec // G115: value is bounded by API validation
		TimeoutSeconds: int32(wh.TimeoutSeconds), //nolint:gosec // G115: value is bounded by API validation
	})
	if err != nil {
		return fmt.Errorf("failed to create webhook: %w", err)
	}

	wh.ID = row.ID
	wh.CreatedAt = row.CreatedAt
	wh.UpdatedAt = row.UpdatedAt

	return nil
}

// GetWebhook retrieves a webhook by ID.
func (p *PostgresDB) GetWebhook(ctx context.Context, id string) (*models.Webhook, error) {
	row, err := p.q.GetWebhook(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get webhook: %w", err)
	}

	// Convert repoID from interface{} to string
	repoID := ""

	if row.RepositoryID != nil {
		if s, ok := row.RepositoryID.(string); ok {
			repoID = s
		}
	}

	return &models.Webhook{
		ID:             row.ID,
		RepositoryID:   repoID,
		Name:           row.Name,
		URL:            row.Url,
		SecretEnc:      row.SecretEnc,
		Enabled:        row.Enabled,
		Events:         row.Events,
		ContentType:    row.ContentType,
		SSLVerify:      row.SslVerify,
		MaxRetries:     int(row.MaxRetries),
		TimeoutSeconds: int(row.TimeoutSeconds),
		CreatedAt:      row.CreatedAt,
		UpdatedAt:      row.UpdatedAt,
	}, nil
}

// UpdateWebhook updates an existing webhook.
func (p *PostgresDB) UpdateWebhook(ctx context.Context, wh *models.Webhook) error {
	err := p.q.UpdateWebhook(ctx, dbsqlc.UpdateWebhookParams{
		ID:             wh.ID,
		Name:           wh.Name,
		Url:            wh.URL,
		SecretEnc:      strPtr(wh.SecretEnc),
		Enabled:        wh.Enabled,
		Events:         wh.Events,
		ContentType:    wh.ContentType,
		SslVerify:      wh.SSLVerify,
		MaxRetries:     int32(wh.MaxRetries),     //nolint:gosec // G115: value is bounded by API validation
		TimeoutSeconds: int32(wh.TimeoutSeconds), //nolint:gosec // G115: value is bounded by API validation
	})
	if err != nil {
		return fmt.Errorf("failed to update webhook: %w", err)
	}

	return nil
}

// DeleteWebhook deletes a webhook.
func (p *PostgresDB) DeleteWebhook(ctx context.Context, id string) error {
	err := p.q.DeleteWebhook(ctx, id)
	if err != nil {
		return fmt.Errorf("failed to delete webhook: %w", err)
	}

	return nil
}

// ListWebhooks lists all webhooks for a repository (or global if repoID is empty).
func (p *PostgresDB) ListWebhooks(ctx context.Context, repoID string) ([]*models.Webhook, error) {
	var webhooks []*models.Webhook

	if repoID == "" {
		rows, err := p.q.ListGlobalWebhooks(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to list webhooks: %w", err)
		}

		for _, r := range rows {
			repoIDStr := ""

			if r.RepositoryID != nil {
				if s, ok := r.RepositoryID.(string); ok {
					repoIDStr = s
				}
			}

			wh := &models.Webhook{
				ID:             r.ID,
				RepositoryID:   repoIDStr,
				Name:           r.Name,
				URL:            r.Url,
				SecretEnc:      r.SecretEnc,
				Enabled:        r.Enabled,
				Events:         r.Events,
				ContentType:    r.ContentType,
				SSLVerify:      r.SslVerify,
				MaxRetries:     int(r.MaxRetries),
				TimeoutSeconds: int(r.TimeoutSeconds),
				CreatedAt:      r.CreatedAt,
				UpdatedAt:      r.UpdatedAt,
			}
			webhooks = append(webhooks, wh)
		}
	} else {
		rows, err := p.q.ListWebhooksByRepo(ctx, &repoID)
		if err != nil {
			return nil, fmt.Errorf("failed to list webhooks: %w", err)
		}

		for _, r := range rows {
			repoIDStr := ""

			if r.RepositoryID != nil {
				if s, ok := r.RepositoryID.(string); ok {
					repoIDStr = s
				}
			}

			wh := &models.Webhook{
				ID:             r.ID,
				RepositoryID:   repoIDStr,
				Name:           r.Name,
				URL:            r.Url,
				SecretEnc:      r.SecretEnc,
				Enabled:        r.Enabled,
				Events:         r.Events,
				ContentType:    r.ContentType,
				SSLVerify:      r.SslVerify,
				MaxRetries:     int(r.MaxRetries),
				TimeoutSeconds: int(r.TimeoutSeconds),
				CreatedAt:      r.CreatedAt,
				UpdatedAt:      r.UpdatedAt,
			}
			webhooks = append(webhooks, wh)
		}
	}

	return webhooks, nil
}

// ListWebhooksForEvent lists all enabled webhooks that are subscribed to an event.
func (p *PostgresDB) ListWebhooksForEvent(ctx context.Context, repoID, eventType string) ([]*models.Webhook, error) {
	var repoIDPtr *string
	if repoID != "" {
		repoIDPtr = &repoID
	}

	rows, err := p.q.ListWebhooksForEvent(ctx, dbsqlc.ListWebhooksForEventParams{
		RepositoryID: repoIDPtr,
		EventType:    eventType,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list webhooks for event: %w", err)
	}

	var webhooks []*models.Webhook

	for _, r := range rows {
		repoIDStr := ""

		if r.RepositoryID != nil {
			if s, ok := r.RepositoryID.(string); ok {
				repoIDStr = s
			}
		}

		wh := &models.Webhook{
			ID:             r.ID,
			RepositoryID:   repoIDStr,
			Name:           r.Name,
			URL:            r.Url,
			SecretEnc:      r.SecretEnc,
			Enabled:        r.Enabled,
			Events:         r.Events,
			ContentType:    r.ContentType,
			SSLVerify:      r.SslVerify,
			MaxRetries:     int(r.MaxRetries),
			TimeoutSeconds: int(r.TimeoutSeconds),
			CreatedAt:      r.CreatedAt,
			UpdatedAt:      r.UpdatedAt,
		}
		webhooks = append(webhooks, wh)
	}

	return webhooks, nil
}

// webhookDeliveryToModel converts a dbsqlc.WebhookDelivery to *models.WebhookDelivery.
func webhookDeliveryToModel(row dbsqlc.WebhookDelivery) (*models.WebhookDelivery, error) {
	var payload map[string]any
	if err := json.Unmarshal(row.Payload, &payload); err != nil {
		return nil, fmt.Errorf("failed to unmarshal webhook payload: %w", err)
	}

	responseStatus := 0
	if row.ResponseStatus != nil {
		responseStatus = int(*row.ResponseStatus)
	}

	responseBody := ""
	if row.ResponseBody != nil {
		responseBody = *row.ResponseBody
	}

	return &models.WebhookDelivery{
		ID:             row.ID,
		WebhookID:      row.WebhookID,
		EventType:      row.EventType,
		Payload:        payload,
		Status:         row.Status,
		ResponseStatus: responseStatus,
		ResponseBody:   responseBody,
		AttemptCount:   int(row.AttemptCount),
		NextRetryAt:    row.NextRetryAt,
		DeliveredAt:    row.DeliveredAt,
		CreatedAt:      row.CreatedAt,
		UpdatedAt:      row.UpdatedAt,
	}, nil
}

// CreateWebhookDelivery creates a new webhook delivery record.
func (p *PostgresDB) CreateWebhookDelivery(ctx context.Context, delivery *models.WebhookDelivery) error {
	payloadJSON, err := json.Marshal(delivery.Payload)
	if err != nil {
		return fmt.Errorf("failed to marshal payload: %w", err)
	}

	row, err := p.q.CreateWebhookDelivery(ctx, dbsqlc.CreateWebhookDeliveryParams{
		WebhookID:    delivery.WebhookID,
		EventType:    delivery.EventType,
		Payload:      payloadJSON,
		Status:       delivery.Status,
		AttemptCount: int32(delivery.AttemptCount), //nolint:gosec // G115: bounded by retry logic
		NextRetryAt:  delivery.NextRetryAt,
	})
	if err != nil {
		return fmt.Errorf("failed to create webhook delivery: %w", err)
	}

	delivery.ID = row.ID
	delivery.CreatedAt = row.CreatedAt
	delivery.UpdatedAt = row.UpdatedAt

	return nil
}

// UpdateWebhookDelivery updates a webhook delivery record.
func (p *PostgresDB) UpdateWebhookDelivery(ctx context.Context, delivery *models.WebhookDelivery) error {
	payloadJSON, err := json.Marshal(delivery.Payload)
	if err != nil {
		return fmt.Errorf("failed to marshal payload: %w", err)
	}

	var responseStatus *int32
	if delivery.ResponseStatus != 0 {
		rs := int32(delivery.ResponseStatus) //nolint:gosec // G115: HTTP status code is bounded
		responseStatus = &rs
	}

	if err := p.q.UpdateWebhookDelivery(ctx, dbsqlc.UpdateWebhookDeliveryParams{
		ID:             delivery.ID,
		Status:         delivery.Status,
		ResponseStatus: responseStatus,
		ResponseBody:   strPtr(delivery.ResponseBody),
		AttemptCount:   int32(delivery.AttemptCount), //nolint:gosec // G115: bounded by retry logic
		NextRetryAt:    delivery.NextRetryAt,
		DeliveredAt:    delivery.DeliveredAt,
		Payload:        payloadJSON,
	}); err != nil {
		return fmt.Errorf("failed to update webhook delivery: %w", err)
	}

	return nil
}

// ListWebhookDeliveries lists deliveries for a webhook.
func (p *PostgresDB) ListWebhookDeliveries(ctx context.Context, webhookID string, limit int) ([]*models.WebhookDelivery, error) {
	rows, err := p.q.ListWebhookDeliveries(ctx, dbsqlc.ListWebhookDeliveriesParams{
		WebhookID: webhookID,
		Limit:     toInt32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list webhook deliveries: %w", err)
	}

	deliveries := make([]*models.WebhookDelivery, 0, len(rows))
	for _, row := range rows {
		d, err := webhookDeliveryToModel(row)
		if err != nil {
			return nil, err
		}

		deliveries = append(deliveries, d)
	}

	return deliveries, nil
}

// ClaimPendingWebhookDeliveries atomically claims up to limit due deliveries
// (pending, next_retry_at unset or elapsed) for lease: it pushes their
// next_retry_at to the end of the lease under FOR UPDATE SKIP LOCKED, so
// concurrent pollers on any number of server instances never receive the same
// row. A claimed delivery that is never completed becomes claimable again once
// the lease expires.
func (p *PostgresDB) ClaimPendingWebhookDeliveries(ctx context.Context, limit int, lease time.Duration) ([]*models.WebhookDelivery, error) {
	rows, err := p.q.ClaimPendingWebhookDeliveries(ctx, dbsqlc.ClaimPendingWebhookDeliveriesParams{
		LeaseSeconds: lease.Seconds(),
		BatchSize:    toInt32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to claim pending webhook deliveries: %w", err)
	}

	deliveries := make([]*models.WebhookDelivery, 0, len(rows))
	for _, row := range rows {
		d, err := webhookDeliveryToModel(row)
		if err != nil {
			return nil, err
		}

		deliveries = append(deliveries, d)
	}

	return deliveries, nil
}

// CreateBackupJob creates a new backup job.
func (p *PostgresDB) CreateBackupJob(ctx context.Context, job *models.BackupJob) error {
	sizeBytes := job.SizeBytes

	id, err := p.q.CreateBackupJob(ctx, dbsqlc.CreateBackupJobParams{
		Status:       job.Status,
		Type:         job.Type,
		StoragePath:  strPtr(job.StoragePath),
		SizeBytes:    &sizeBytes,
		ErrorMessage: strPtr(job.ErrorMessage),
		StartedAt:    job.StartedAt,
		CompletedAt:  job.CompletedAt,
		CreatedAt:    job.CreatedAt,
		UpdatedAt:    job.UpdatedAt,
	})
	if err != nil {
		return fmt.Errorf("failed to create backup job: %w", err)
	}

	job.ID = id

	return nil
}

// GetBackupJob retrieves a backup job by ID.
func (p *PostgresDB) GetBackupJob(ctx context.Context, id string) (*models.BackupJob, error) {
	row, err := p.q.GetBackupJob(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get backup job: %w", err)
	}

	return &models.BackupJob{
		ID:           row.ID,
		Status:       row.Status,
		Type:         row.Type,
		StoragePath:  row.StoragePath,
		SizeBytes:    row.SizeBytes,
		ErrorMessage: row.ErrorMessage,
		StartedAt:    row.StartedAt,
		CompletedAt:  row.CompletedAt,
		CreatedAt:    row.CreatedAt,
		UpdatedAt:    row.UpdatedAt,
	}, nil
}

// UpdateBackupJob updates an existing backup job.
func (p *PostgresDB) UpdateBackupJob(ctx context.Context, job *models.BackupJob) error {
	sizeBytes := job.SizeBytes

	err := p.q.UpdateBackupJob(ctx, dbsqlc.UpdateBackupJobParams{
		Status:       job.Status,
		Type:         job.Type,
		StoragePath:  strPtr(job.StoragePath),
		SizeBytes:    &sizeBytes,
		ErrorMessage: strPtr(job.ErrorMessage),
		StartedAt:    job.StartedAt,
		CompletedAt:  job.CompletedAt,
		UpdatedAt:    job.UpdatedAt,
		ID:           job.ID,
	})
	if err != nil {
		return fmt.Errorf("failed to update backup job: %w", err)
	}

	return nil
}

// ListBackupJobs lists backup jobs with a limit.
func (p *PostgresDB) ListBackupJobs(ctx context.Context, limit int) ([]*models.BackupJob, error) {
	rows, err := p.q.ListBackupJobs(ctx, toInt32(limit))
	if err != nil {
		return nil, fmt.Errorf("failed to list backup jobs: %w", err)
	}

	jobs := make([]*models.BackupJob, 0, len(rows))

	for _, row := range rows {
		jobs = append(jobs, &models.BackupJob{
			ID:           row.ID,
			Status:       row.Status,
			Type:         row.Type,
			StoragePath:  row.StoragePath,
			SizeBytes:    row.SizeBytes,
			ErrorMessage: row.ErrorMessage,
			StartedAt:    row.StartedAt,
			CompletedAt:  row.CompletedAt,
			CreatedAt:    row.CreatedAt,
			UpdatedAt:    row.UpdatedAt,
		})
	}

	return jobs, nil
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
	if err != nil {
		return fmt.Errorf("failed to create cargo crate: %w", err)
	}

	crate.ID = row.ID
	crate.CreatedAt = row.CreatedAt
	crate.UpdatedAt = row.UpdatedAt

	return nil
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

func (p *PostgresDB) DeleteCargoCrate(ctx context.Context, id string) error {
	err := p.q.DeleteCargoCrate(ctx, id)
	if err != nil {
		return fmt.Errorf("failed to delete cargo crate: %w", err)
	}

	return nil
}

func (p *PostgresDB) ListCargoCrateVersions(ctx context.Context, repoID, name string) ([]*models.CargoCrate, error) {
	rows, err := p.q.ListCargoCrateVersions(ctx, dbsqlc.ListCargoCrateVersionsParams{
		RepositoryID: repoID,
		Name:         name,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to query cargo crate versions: %w", err)
	}

	var crates []*models.CargoCrate

	for _, row := range rows {
		crate := &models.CargoCrate{
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
		crates = append(crates, crate)
	}

	return crates, nil
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
		crate := &models.CargoCrate{
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
		crates = append(crates, crate)
	}

	return crates, nil
}

func (p *PostgresDB) SearchCargoCrateSummaries(ctx context.Context, repoID, query string, limit int) ([]*models.CargoCrateSummary, int, error) {
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
func nugetRowToModel(row dbsqlc.GetNuGetPackageRow) *models.NuGetPackage {
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
	if err != nil {
		return fmt.Errorf("failed to create ruby gem: %w", err)
	}

	gem.ID = row.ID
	gem.CreatedAt = row.CreatedAt
	gem.UpdatedAt = row.UpdatedAt

	return nil
}

func (p *PostgresDB) GetRubyGem(ctx context.Context, repoID, name, version, platform string) (*models.RubyGem, error) {
	row, err := p.q.GetRubyGem(ctx, dbsqlc.GetRubyGemParams{
		RepositoryID: repoID,
		Name:         name,
		Version:      version,
		Platform:     platform,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get ruby gem: %w", err)
	}

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
	}, nil
}

func (p *PostgresDB) DeleteRubyGem(ctx context.Context, id string) error {
	err := p.q.DeleteRubyGem(ctx, id)
	if err != nil {
		return fmt.Errorf("failed to delete ruby gem: %w", err)
	}

	return nil
}

func (p *PostgresDB) ListRubyGems(ctx context.Context, repoID string) ([]*models.RubyGem, error) {
	rows, err := p.q.ListRubyGems(ctx, repoID)
	if err != nil {
		return nil, fmt.Errorf("failed to query ruby gems: %w", err)
	}

	var gems []*models.RubyGem

	for _, row := range rows {
		gem := &models.RubyGem{
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
		gems = append(gems, gem)
	}

	return gems, nil
}

func (p *PostgresDB) ListRubyGemVersions(ctx context.Context, repoID, name string) ([]*models.RubyGem, error) {
	rows, err := p.q.ListRubyGemVersions(ctx, dbsqlc.ListRubyGemVersionsParams{
		RepositoryID: repoID,
		Name:         name,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to query ruby gem versions: %w", err)
	}

	var gems []*models.RubyGem

	for _, row := range rows {
		gem := &models.RubyGem{
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
		gems = append(gems, gem)
	}

	return gems, nil
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
		gem := &models.RubyGem{
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
		gems = append(gems, gem)
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

// CreateSBOMReport creates or updates an SBOM report (upsert). The caller
// (internal/api/sbom.go) is expected to have already written the SBOM bytes
// into the blob store and set report.BlobSHA256/BlobSizeBytes; this upserts
// the corresponding blobs row and stores the resulting blob_id alongside
// the report metadata. A zero-value BlobSHA256 stores a NULL blob_id.
func (p *PostgresDB) CreateSBOMReport(ctx context.Context, report *models.SBOMReport) error {
	var blobID *string

	if report.BlobSHA256 != "" {
		id, err := p.upsertBlobWithMediaType(ctx, report.BlobSHA256, report.BlobSHA256, report.ContentType, report.BlobSizeBytes)
		if err != nil {
			return fmt.Errorf("failed to upsert blob for sbom report: %w", err)
		}

		blobID = &id
	}

	err := p.db.QueryRowContext(ctx, `
		WITH ref AS (
			INSERT INTO artifact_refs (repository_id, artifact_path, format)
			VALUES ($1, $2, $3)
			ON CONFLICT (repository_id, artifact_path) DO UPDATE SET format = EXCLUDED.format
			RETURNING id
		), job AS (
			INSERT INTO scan_jobs (artifact_ref_id, scan_type, scanner, status, completed_at)
			SELECT ref.id, 'sbom', '', 'completed', $4 FROM ref
			ON CONFLICT (artifact_ref_id, scan_type, scanner) DO UPDATE SET
				status = EXCLUDED.status, completed_at = EXCLUDED.completed_at, updated_at = now()
			RETURNING id, created_at, updated_at
		), ins AS (
			INSERT INTO sbom_results (scan_job_id, sbom_format, spec_version, blob_id, component_count)
			SELECT job.id, $3, $5, $7, $6 FROM job
			ON CONFLICT (scan_job_id, sbom_format, spec_version) DO UPDATE SET
				component_count = EXCLUDED.component_count, blob_id = EXCLUDED.blob_id
			RETURNING id, scan_job_id
		)
		SELECT ins.id, job.created_at, job.updated_at FROM ins JOIN job ON job.id = ins.scan_job_id`,
		report.RepositoryID, report.ArtifactID, report.Format, report.GeneratedAt, report.SpecVersion, report.ComponentCount, blobID,
	).Scan(&report.ID, &report.CreatedAt, &report.UpdatedAt)
	if err != nil {
		return fmt.Errorf("failed to create SBOM report: %w", err)
	}

	return nil
}

// GetSBOMReport retrieves an SBOM report by repo, artifact, format, and spec
// version. BlobSHA256 is resolved via a LEFT JOIN with blobs and is empty
// for reports that predate blob_id or were created with no content.
func (p *PostgresDB) GetSBOMReport(ctx context.Context, repoID, artifactID, format, specVersion string) (*models.SBOMReport, error) {
	report := &models.SBOMReport{}

	var blobSHA256 sql.NullString

	err := p.db.QueryRowContext(ctx, `
		SELECT sr.id, ref.repository_id, ref.artifact_path, sr.sbom_format, sr.spec_version,
		       sr.component_count, b.sha256, sj.completed_at, sj.created_at, sj.updated_at
		FROM sbom_results sr
		JOIN scan_jobs sj ON sj.id = sr.scan_job_id
		JOIN artifact_refs ref ON ref.id = sj.artifact_ref_id
		LEFT JOIN blobs b ON b.id = sr.blob_id
		WHERE sj.scan_type = 'sbom' AND ref.repository_id = $1 AND ref.artifact_path = $2
		  AND sr.sbom_format = $3 AND sr.spec_version = $4`,
		repoID, artifactID, format, specVersion,
	).Scan(&report.ID, &report.RepositoryID, &report.ArtifactID, &report.Format, &report.SpecVersion,
		&report.ComponentCount, &blobSHA256, &report.GeneratedAt, &report.CreatedAt, &report.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get SBOM report: %w", err)
	}

	report.BlobSHA256 = blobSHA256.String

	return report, nil
}

// ListSBOMReports lists all SBOM reports for a repository.
func (p *PostgresDB) ListSBOMReports(ctx context.Context, repoID string) ([]*models.SBOMReport, error) {
	rows, err := p.db.QueryContext(ctx, `
		SELECT sr.id, ref.repository_id, ref.artifact_path, sr.sbom_format, sr.spec_version,
		       sr.component_count, b.sha256, sj.completed_at, sj.created_at, sj.updated_at
		FROM sbom_results sr
		JOIN scan_jobs sj ON sj.id = sr.scan_job_id
		JOIN artifact_refs ref ON ref.id = sj.artifact_ref_id
		LEFT JOIN blobs b ON b.id = sr.blob_id
		WHERE sj.scan_type = 'sbom' AND ref.repository_id = $1
		ORDER BY sj.created_at DESC`, repoID)
	if err != nil {
		return nil, fmt.Errorf("failed to list SBOM reports: %w", err)
	}
	defer rows.Close()

	var reports []*models.SBOMReport

	for rows.Next() {
		report := &models.SBOMReport{}

		var blobSHA256 sql.NullString

		if err := rows.Scan(&report.ID, &report.RepositoryID, &report.ArtifactID, &report.Format, &report.SpecVersion,
			&report.ComponentCount, &blobSHA256, &report.GeneratedAt, &report.CreatedAt, &report.UpdatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan SBOM report: %w", err)
		}

		report.BlobSHA256 = blobSHA256.String

		reports = append(reports, report)
	}

	return reports, rows.Err()
}

// DeleteSBOMReport deletes an SBOM report by ID.
func (p *PostgresDB) DeleteSBOMReport(ctx context.Context, id string) error {
	res, err := p.db.ExecContext(ctx, `DELETE FROM sbom_results WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("failed to delete SBOM report: %w", err)
	}

	rowsAffected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to delete SBOM report: %w", err)
	}

	if rowsAffected == 0 {
		return ErrSBOMReportNotFound
	}

	return nil
}

// secretScanStatusToJob maps SecretScanResult.ScanStatus's "clean"/"flagged"
// values (set from finding_count at scan time) onto scan_jobs' CHECK-constrained
// status vocabulary (pending/running/completed/failed). The clean/flagged
// distinction is recovered on read from secret_results.finding_count instead
// of being stored redundantly (see secretScanStatusFromJob).
func secretScanStatusToJob(status string) string {
	if status == "clean" || status == "flagged" {
		return "completed"
	}

	return status
}

func secretScanStatusFromJob(jobStatus string, findingCount int) string {
	if jobStatus == "completed" {
		if findingCount > 0 {
			return "flagged"
		}

		return "clean"
	}

	return jobStatus
}

// vulnScanStatusToJob/FromJob map VulnScanResult.ScanStatus's "scanning" onto
// scan_jobs' "running" (the CHECK-constrained vocabulary), and back.
func vulnScanStatusToJob(status string) string {
	if status == "scanning" {
		return "running"
	}

	return status
}

func vulnScanStatusFromJob(status string) string {
	if status == "running" {
		return "scanning"
	}

	return status
}

// aiStatusToJob/FromJob map AIAnalysis.Status's "processing" onto scan_jobs'
// "running" (the CHECK-constrained vocabulary), and back.
func aiStatusToJob(status string) string {
	if status == "processing" {
		return "running"
	}

	return status
}

func aiStatusFromJob(status string) string {
	if status == "running" {
		return "processing"
	}

	return status
}

// CreateLicenseScanResult creates a new license scan result (UPSERT).
func (p *PostgresDB) CreateLicenseScanResult(ctx context.Context, result *models.LicenseScanResult) error {
	var scannedAt time.Time
	if result.ScannedAt != nil {
		scannedAt = *result.ScannedAt
	}

	err := p.db.QueryRowContext(ctx, `
		WITH ref AS (
			INSERT INTO artifact_refs (repository_id, artifact_path, format)
			VALUES ($1, $2, COALESCE((SELECT format FROM repositories WHERE id = $1), 'unknown'))
			ON CONFLICT (repository_id, artifact_path) DO UPDATE SET artifact_path = EXCLUDED.artifact_path
			RETURNING id
		), job AS (
			INSERT INTO scan_jobs (artifact_ref_id, scan_type, scanner, status, error_message, completed_at)
			SELECT ref.id, 'license', '', $3, NULLIF($4, ''), $5 FROM ref
			ON CONFLICT (artifact_ref_id, scan_type, scanner) DO UPDATE SET
				status = EXCLUDED.status, error_message = EXCLUDED.error_message,
				completed_at = EXCLUDED.completed_at, updated_at = now()
			RETURNING id
		)
		INSERT INTO license_results (scan_job_id, licenses, spdx_expressions, quarantined, quarantine_approved)
		SELECT job.id, $6, $7, $8, $9 FROM job
		ON CONFLICT (scan_job_id) DO UPDATE SET
			licenses = EXCLUDED.licenses, spdx_expressions = EXCLUDED.spdx_expressions,
			quarantined = EXCLUDED.quarantined, quarantine_approved = EXCLUDED.quarantine_approved
		RETURNING id`,
		result.RepositoryID, result.ArtifactID, result.ScanStatus, result.ScanError, scannedAt,
		nonNilStrings(result.Licenses), nonNilStrings(result.SPDXExpressions), result.Quarantined, result.QuarantineApproved,
	).Scan(&result.ID)
	if err != nil {
		return fmt.Errorf("failed to create license scan result: %w", err)
	}

	return nil
}

// GetLicenseScanResult gets a license scan result by repo and artifact ID.
func (p *PostgresDB) GetLicenseScanResult(ctx context.Context, repoID, artifactID string) (*models.LicenseScanResult, error) {
	result, err := scanLicenseResult(p.db.QueryRowContext(ctx, `
		SELECT `+licenseResultColumns+`
		FROM license_results lr
		JOIN scan_jobs sj ON sj.id = lr.scan_job_id
		JOIN artifact_refs ref ON ref.id = sj.artifact_ref_id
		WHERE sj.scan_type = 'license' AND ref.repository_id = $1 AND ref.artifact_path = $2`,
		repoID, artifactID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get license scan result: %w", err)
	}

	return result, nil
}

// UpdateLicenseScanResult updates a license scan result.
func (p *PostgresDB) UpdateLicenseScanResult(ctx context.Context, result *models.LicenseScanResult) error {
	var scannedAt time.Time
	if result.ScannedAt != nil {
		scannedAt = *result.ScannedAt
	}

	_, err := p.db.ExecContext(ctx, `
		WITH res AS (
			UPDATE license_results SET licenses = $1, spdx_expressions = $2, quarantined = $7, quarantine_approved = $8
			WHERE id = $6
			RETURNING scan_job_id
		)
		UPDATE scan_jobs sj SET status = $3, error_message = NULLIF($4, ''), completed_at = $5, updated_at = now()
		FROM res WHERE sj.id = res.scan_job_id`,
		result.Licenses, result.SPDXExpressions, result.ScanStatus, result.ScanError, scannedAt, result.ID,
		result.Quarantined, result.QuarantineApproved,
	)
	if err != nil {
		return fmt.Errorf("failed to update license scan result: %w", err)
	}

	return nil
}

// ListLicenseScanResults lists all license scan results for a repository.
func (p *PostgresDB) ListLicenseScanResults(ctx context.Context, repoID string) ([]*models.LicenseScanResult, error) {
	rows, err := p.db.QueryContext(ctx, `
		SELECT `+licenseResultColumns+`
		FROM license_results lr
		JOIN scan_jobs sj ON sj.id = lr.scan_job_id
		JOIN artifact_refs ref ON ref.id = sj.artifact_ref_id
		WHERE sj.scan_type = 'license' AND ref.repository_id = $1
		ORDER BY sj.created_at DESC`, repoID)
	if err != nil {
		return nil, fmt.Errorf("failed to list license scan results: %w", err)
	}
	defer rows.Close()

	var results []*models.LicenseScanResult

	for rows.Next() {
		result, err := scanLicenseResult(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan license scan result: %w", err)
		}

		results = append(results, result)
	}

	return results, rows.Err()
}

// CreateLicensePolicy creates a new license policy.
func (p *PostgresDB) CreateLicensePolicy(ctx context.Context, policy *models.LicensePolicy) error {
	id, err := p.q.CreateLicensePolicy(ctx, dbsqlc.CreateLicensePolicyParams{
		RepositoryID:    policy.RepositoryID,
		Name:            policy.Name,
		Description:     strPtr(policy.Description),
		Action:          policy.Action,
		AllowedLicenses: nonNilStrings(policy.AllowedLicenses),
		DeniedLicenses:  nonNilStrings(policy.DeniedLicenses),
		Enabled:         policy.Enabled,
		CreatedAt:       policy.CreatedAt,
		UpdatedAt:       policy.UpdatedAt,
	})
	if err != nil {
		return fmt.Errorf("failed to create license policy: %w", err)
	}

	policy.ID = id

	return nil
}

// GetLicensePolicy gets a license policy by ID.
func (p *PostgresDB) GetLicensePolicy(ctx context.Context, id string) (*models.LicensePolicy, error) {
	row, err := p.q.GetLicensePolicy(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get license policy: %w", err)
	}

	return &models.LicensePolicy{
		ID:              row.ID,
		RepositoryID:    row.RepositoryID,
		Name:            row.Name,
		Description:     derefStr(row.Description),
		Action:          row.Action,
		AllowedLicenses: row.AllowedLicenses,
		DeniedLicenses:  row.DeniedLicenses,
		Enabled:         row.Enabled,
		CreatedAt:       row.CreatedAt,
		UpdatedAt:       row.UpdatedAt,
	}, nil
}

// UpdateLicensePolicy updates a license policy.
func (p *PostgresDB) UpdateLicensePolicy(ctx context.Context, policy *models.LicensePolicy) error {
	err := p.q.UpdateLicensePolicy(ctx, dbsqlc.UpdateLicensePolicyParams{
		ID:              policy.ID,
		Name:            policy.Name,
		Description:     strPtr(policy.Description),
		Action:          policy.Action,
		AllowedLicenses: nonNilStrings(policy.AllowedLicenses),
		DeniedLicenses:  nonNilStrings(policy.DeniedLicenses),
		Enabled:         policy.Enabled,
		UpdatedAt:       policy.UpdatedAt,
	})
	if err != nil {
		return fmt.Errorf("failed to update license policy: %w", err)
	}

	return nil
}

// DeleteLicensePolicy deletes a license policy by ID.
func (p *PostgresDB) DeleteLicensePolicy(ctx context.Context, id string) error {
	rowsAffected, err := p.q.DeleteLicensePolicy(ctx, id)
	if err != nil {
		return fmt.Errorf("failed to delete license policy: %w", err)
	}

	if rowsAffected == 0 {
		return ErrLicensePolicyNotFound
	}

	return nil
}

// ListLicensePolicies lists all license policies for a repository (including global policies).
func (p *PostgresDB) ListLicensePolicies(ctx context.Context, repoID string) ([]*models.LicensePolicy, error) {
	rows, err := p.q.ListLicensePolicies(ctx, &repoID)
	if err != nil {
		return nil, fmt.Errorf("failed to list license policies: %w", err)
	}

	var policies []*models.LicensePolicy

	for _, row := range rows {
		policy := &models.LicensePolicy{
			ID:              row.ID,
			RepositoryID:    row.RepositoryID,
			Name:            row.Name,
			Description:     derefStr(row.Description),
			Action:          row.Action,
			AllowedLicenses: row.AllowedLicenses,
			DeniedLicenses:  row.DeniedLicenses,
			Enabled:         row.Enabled,
			CreatedAt:       row.CreatedAt,
			UpdatedAt:       row.UpdatedAt,
		}
		policies = append(policies, policy)
	}

	return policies, nil
}

// CreateSecretScanResult creates a new secret scan result (UPSERT).
func (p *PostgresDB) CreateSecretScanResult(ctx context.Context, result *models.SecretScanResult) error {
	findingsJSON, err := json.Marshal(result.Findings)
	if err != nil {
		return fmt.Errorf("failed to marshal findings: %w", err)
	}

	var scannedAt time.Time
	if result.ScannedAt != nil {
		scannedAt = *result.ScannedAt
	}

	err = p.db.QueryRowContext(ctx, `
		WITH ref AS (
			INSERT INTO artifact_refs (repository_id, artifact_path, format)
			VALUES ($1, $2, COALESCE((SELECT format FROM repositories WHERE id = $1), 'unknown'))
			ON CONFLICT (repository_id, artifact_path) DO UPDATE SET artifact_path = EXCLUDED.artifact_path
			RETURNING id
		), job AS (
			INSERT INTO scan_jobs (artifact_ref_id, scan_type, scanner, status, error_message, completed_at)
			SELECT ref.id, 'secret', '', $3, NULLIF($4, ''), $5 FROM ref
			ON CONFLICT (artifact_ref_id, scan_type, scanner) DO UPDATE SET
				status = EXCLUDED.status, error_message = EXCLUDED.error_message,
				completed_at = EXCLUDED.completed_at, updated_at = now()
			RETURNING id
		)
		INSERT INTO secret_results (scan_job_id, findings, finding_count, quarantined)
		SELECT job.id, $6, $7, $8 FROM job
		ON CONFLICT (scan_job_id) DO UPDATE SET
			findings = EXCLUDED.findings, finding_count = EXCLUDED.finding_count, quarantined = EXCLUDED.quarantined
		RETURNING id`,
		result.RepositoryID, result.ArtifactID, secretScanStatusToJob(result.ScanStatus), result.ScanError, scannedAt,
		findingsJSON, result.FindingCount, result.Quarantined,
	).Scan(&result.ID)
	if err != nil {
		return fmt.Errorf("failed to create secret scan result: %w", err)
	}

	return nil
}

// GetSecretScanResult gets a secret scan result by repo and artifact ID.
func (p *PostgresDB) GetSecretScanResult(ctx context.Context, repoID, artifactID string) (*models.SecretScanResult, error) {
	result := &models.SecretScanResult{}

	var (
		scannedAt   sql.NullTime
		errMsg      sql.NullString
		findingsRaw []byte
		jobStatus   string
	)

	err := p.db.QueryRowContext(ctx, `
		SELECT sr.id, ref.repository_id, ref.artifact_path, sj.status, sr.findings, sr.finding_count, sr.quarantined,
		       sj.error_message, sj.completed_at, sj.created_at, sj.updated_at
		FROM secret_results sr
		JOIN scan_jobs sj ON sj.id = sr.scan_job_id
		JOIN artifact_refs ref ON ref.id = sj.artifact_ref_id
		WHERE sj.scan_type = 'secret' AND ref.repository_id = $1 AND ref.artifact_path = $2`,
		repoID, artifactID,
	).Scan(&result.ID, &result.RepositoryID, &result.ArtifactID, &jobStatus, &findingsRaw, &result.FindingCount, &result.Quarantined,
		&errMsg, &scannedAt, &result.CreatedAt, &result.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get secret scan result: %w", err)
	}

	result.ScanStatus = secretScanStatusFromJob(jobStatus, result.FindingCount)
	result.ScanError = errMsg.String

	if scannedAt.Valid {
		result.ScannedAt = &scannedAt.Time
	}

	if len(findingsRaw) > 0 {
		if err := json.Unmarshal(findingsRaw, &result.Findings); err != nil {
			return nil, fmt.Errorf("failed to unmarshal findings: %w", err)
		}
	}

	return result, nil
}

// UpdateSecretScanResult updates a secret scan result.
func (p *PostgresDB) UpdateSecretScanResult(ctx context.Context, result *models.SecretScanResult) error {
	findingsJSON, err := json.Marshal(result.Findings)
	if err != nil {
		return fmt.Errorf("failed to marshal findings: %w", err)
	}

	var scannedAt time.Time
	if result.ScannedAt != nil {
		scannedAt = *result.ScannedAt
	}

	_, err = p.db.ExecContext(ctx, `
		WITH res AS (
			UPDATE secret_results SET findings = $1, finding_count = $2, quarantined = $3
			WHERE id = $7
			RETURNING scan_job_id
		)
		UPDATE scan_jobs sj SET status = $4, error_message = NULLIF($5, ''), completed_at = $6, updated_at = now()
		FROM res WHERE sj.id = res.scan_job_id`,
		findingsJSON, result.FindingCount, result.Quarantined,
		secretScanStatusToJob(result.ScanStatus), result.ScanError, scannedAt, result.ID,
	)
	if err != nil {
		return fmt.Errorf("failed to update secret scan result: %w", err)
	}

	return nil
}

// ListSecretScanResults lists all secret scan results for a repository.
func (p *PostgresDB) ListSecretScanResults(ctx context.Context, repoID string, flaggedOnly bool) ([]*models.SecretScanResult, error) {
	query := `
		SELECT sr.id, ref.repository_id, ref.artifact_path, sj.status, sr.findings, sr.finding_count, sr.quarantined,
		       sj.error_message, sj.completed_at, sj.created_at, sj.updated_at
		FROM secret_results sr
		JOIN scan_jobs sj ON sj.id = sr.scan_job_id
		JOIN artifact_refs ref ON ref.id = sj.artifact_ref_id
		WHERE sj.scan_type = 'secret' AND ref.repository_id = $1`
	if flaggedOnly {
		query += ` AND sr.quarantined = true`
	}

	query += ` ORDER BY sj.created_at DESC`

	rows, err := p.db.QueryContext(ctx, query, repoID)
	if err != nil {
		return nil, fmt.Errorf("failed to list secret scan results: %w", err)
	}
	defer rows.Close()

	results := make([]*models.SecretScanResult, 0)

	for rows.Next() {
		result := &models.SecretScanResult{}

		var (
			scannedAt   sql.NullTime
			errMsg      sql.NullString
			findingsRaw []byte
			jobStatus   string
		)

		if err := rows.Scan(&result.ID, &result.RepositoryID, &result.ArtifactID, &jobStatus, &findingsRaw, &result.FindingCount, &result.Quarantined,
			&errMsg, &scannedAt, &result.CreatedAt, &result.UpdatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan secret scan result: %w", err)
		}

		result.ScanStatus = secretScanStatusFromJob(jobStatus, result.FindingCount)
		result.ScanError = errMsg.String

		if scannedAt.Valid {
			result.ScannedAt = &scannedAt.Time
		}

		if len(findingsRaw) > 0 {
			if err := json.Unmarshal(findingsRaw, &result.Findings); err != nil {
				return nil, fmt.Errorf("failed to unmarshal findings: %w", err)
			}
		}

		results = append(results, result)
	}

	return results, rows.Err()
}

// QuarantineArtifact updates the quarantine status of an artifact.
func (p *PostgresDB) QuarantineArtifact(ctx context.Context, repoID, artifactID string, quarantined bool) error {
	_, err := p.db.ExecContext(ctx, `
		WITH target AS (
			SELECT sr.id AS result_id, sj.id AS job_id
			FROM secret_results sr
			JOIN scan_jobs sj ON sj.id = sr.scan_job_id
			JOIN artifact_refs ref ON ref.id = sj.artifact_ref_id
			WHERE sj.scan_type = 'secret' AND ref.repository_id = $2 AND ref.artifact_path = $3
		), upd1 AS (
			UPDATE secret_results SET quarantined = $1
			WHERE id = (SELECT result_id FROM target)
			RETURNING id
		)
		UPDATE scan_jobs SET updated_at = now() WHERE id = (SELECT job_id FROM target)`,
		quarantined, repoID, artifactID,
	)
	if err != nil {
		return fmt.Errorf("failed to quarantine artifact: %w", err)
	}

	return nil
}

// CreateVulnScanResult creates or updates a vulnerability scan result.
func (p *PostgresDB) CreateVulnScanResult(ctx context.Context, result *models.VulnScanResult) error {
	vulnsJSON, err := json.Marshal(result.Vulnerabilities)
	if err != nil {
		return fmt.Errorf("failed to marshal vulnerabilities: %w", err)
	}

	var scannedAt time.Time
	if result.ScannedAt != nil {
		scannedAt = *result.ScannedAt
	}

	err = p.db.QueryRowContext(ctx, `
		WITH ref AS (
			INSERT INTO artifact_refs (repository_id, artifact_path, format)
			VALUES ($1, $2, COALESCE((SELECT format FROM repositories WHERE id = $1), 'unknown'))
			ON CONFLICT (repository_id, artifact_path) DO UPDATE SET artifact_path = EXCLUDED.artifact_path
			RETURNING id
		), job AS (
			INSERT INTO scan_jobs (artifact_ref_id, scan_type, scanner, status, error_message, completed_at)
			SELECT ref.id, 'vuln', $3, $4, NULLIF($5, ''), $6 FROM ref
			ON CONFLICT (artifact_ref_id, scan_type, scanner) DO UPDATE SET
				status = EXCLUDED.status, error_message = EXCLUDED.error_message,
				completed_at = EXCLUDED.completed_at, updated_at = now()
			RETURNING id
		)
		INSERT INTO vuln_results (scan_job_id, vulnerabilities, vuln_count, critical_count, high_count, medium_count, low_count, quarantined, package_name, package_version, ecosystem, failed_scanners)
		SELECT job.id, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17 FROM job
		ON CONFLICT (scan_job_id) DO UPDATE SET
			vulnerabilities = EXCLUDED.vulnerabilities, vuln_count = EXCLUDED.vuln_count,
			critical_count = EXCLUDED.critical_count, high_count = EXCLUDED.high_count,
			medium_count = EXCLUDED.medium_count, low_count = EXCLUDED.low_count,
			quarantined = EXCLUDED.quarantined, package_name = EXCLUDED.package_name,
			package_version = EXCLUDED.package_version, ecosystem = EXCLUDED.ecosystem,
			failed_scanners = EXCLUDED.failed_scanners
		RETURNING id`,
		result.RepositoryID, result.ArtifactID, result.Scanner, vulnScanStatusToJob(result.ScanStatus), result.ScanError, scannedAt,
		vulnsJSON, result.VulnCount, result.CriticalCount, result.HighCount, result.MediumCount, result.LowCount,
		result.Quarantined, result.PackageName, result.PackageVersion, result.Ecosystem, nonNilStrings(result.FailedScanners),
	).Scan(&result.ID)
	if err != nil {
		return fmt.Errorf("failed to create vuln scan result: %w", err)
	}

	return nil
}

// GetVulnScanResult gets a vulnerability scan result by repo and artifact
// ID. scan_jobs' UNIQUE(artifact_ref_id, scan_type, scanner) allows more
// than one scanner (osv/trivy/grype/...) to have scanned the same artifact,
// so this orders by most-recently-created and takes the first row instead
// of letting QueryRowContext pick an arbitrary one nondeterministically.
func (p *PostgresDB) GetVulnScanResult(ctx context.Context, repoID, artifactID string) (*models.VulnScanResult, error) {
	result, err := scanVulnResult(p.db.QueryRowContext(ctx, `
		SELECT `+vulnResultColumns+`
		FROM vuln_results vr
		JOIN scan_jobs sj ON sj.id = vr.scan_job_id
		JOIN artifact_refs ref ON ref.id = sj.artifact_ref_id
		WHERE sj.scan_type = 'vuln' AND ref.repository_id = $1 AND ref.artifact_path = $2
		ORDER BY sj.created_at DESC
		LIMIT 1`,
		repoID, artifactID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get vuln scan result: %w", err)
	}

	return result, nil
}

// UpdateVulnScanResult updates a vulnerability scan result.
func (p *PostgresDB) UpdateVulnScanResult(ctx context.Context, result *models.VulnScanResult) error {
	vulnsJSON, err := json.Marshal(result.Vulnerabilities)
	if err != nil {
		return fmt.Errorf("failed to marshal vulnerabilities: %w", err)
	}

	var scannedAt time.Time
	if result.ScannedAt != nil {
		scannedAt = *result.ScannedAt
	}

	_, err = p.db.ExecContext(ctx, `
		WITH res AS (
			UPDATE vuln_results SET vulnerabilities = $1, vuln_count = $2, critical_count = $3,
			       high_count = $4, medium_count = $5, low_count = $6, quarantined = $7,
			       failed_scanners = $13
			WHERE id = $11
			RETURNING scan_job_id
		)
		UPDATE scan_jobs sj SET scanner = $8, status = $9, error_message = NULLIF($10, ''), completed_at = $12, updated_at = now()
		FROM res WHERE sj.id = res.scan_job_id`,
		vulnsJSON, result.VulnCount, result.CriticalCount, result.HighCount, result.MediumCount, result.LowCount, result.Quarantined,
		result.Scanner, vulnScanStatusToJob(result.ScanStatus), result.ScanError, result.ID, scannedAt,
		nonNilStrings(result.FailedScanners),
	)
	if err != nil {
		return fmt.Errorf("failed to update vuln scan result: %w", err)
	}

	return nil
}

// ListVulnScanResults lists all vulnerability scan results for a repository.
func (p *PostgresDB) ListVulnScanResults(ctx context.Context, repoID string, vulnerableOnly bool) ([]*models.VulnScanResult, error) {
	query := `
		SELECT ` + vulnResultColumns + `
		FROM vuln_results vr
		JOIN scan_jobs sj ON sj.id = vr.scan_job_id
		JOIN artifact_refs ref ON ref.id = sj.artifact_ref_id
		WHERE sj.scan_type = 'vuln' AND ref.repository_id = $1`
	if vulnerableOnly {
		query += ` AND vr.vuln_count > 0`
	}

	query += ` ORDER BY sj.created_at DESC`

	rows, err := p.db.QueryContext(ctx, query, repoID)
	if err != nil {
		return nil, fmt.Errorf("failed to list vuln scan results: %w", err)
	}
	defer rows.Close()

	results := make([]*models.VulnScanResult, 0)

	for rows.Next() {
		result, err := scanVulnResult(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan vuln scan result: %w", err)
		}

		results = append(results, result)
	}

	return results, rows.Err()
}

// QuarantineVulnArtifact updates the quarantine status of a vulnerable artifact.
func (p *PostgresDB) QuarantineVulnArtifact(ctx context.Context, repoID, artifactID string, quarantined bool) error {
	_, err := p.db.ExecContext(ctx, `
		WITH target AS (
			SELECT vr.id AS result_id, sj.id AS job_id
			FROM vuln_results vr
			JOIN scan_jobs sj ON sj.id = vr.scan_job_id
			JOIN artifact_refs ref ON ref.id = sj.artifact_ref_id
			WHERE sj.scan_type = 'vuln' AND ref.repository_id = $2 AND ref.artifact_path = $3
		), upd1 AS (
			UPDATE vuln_results SET quarantined = $1
			WHERE id = (SELECT result_id FROM target)
			RETURNING id
		)
		UPDATE scan_jobs SET updated_at = now() WHERE id = (SELECT job_id FROM target)`,
		quarantined, repoID, artifactID,
	)
	if err != nil {
		return fmt.Errorf("failed to quarantine vuln artifact: %w", err)
	}

	return nil
}

// GetOrCreateStorageQuota gets or creates a storage quota for a repository.
func (p *PostgresDB) GetOrCreateStorageQuota(ctx context.Context, repoID string) (*models.StorageQuota, error) {
	if err := p.q.InsertStorageQuotaIfNotExists(ctx, repoID); err != nil {
		return nil, fmt.Errorf("failed to create storage quota: %w", err)
	}

	quota, err := p.q.GetStorageQuota(ctx, repoID)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create storage quota: %w", err)
	}

	return &models.StorageQuota{
		ID:                   quota.ID,
		RepositoryID:         quota.RepositoryID,
		MaxSizeBytes:         quota.MaxSizeBytes,
		MaxArtifactCount:     int(quota.MaxArtifactCount),
		WarnThresholdPercent: int(quota.WarnThresholdPercent),
		CurrentSizeBytes:     quota.CurrentSizeBytes,
		CurrentArtifactCount: int(quota.CurrentArtifactCount),
		CreatedAt:            quota.CreatedAt,
		UpdatedAt:            quota.UpdatedAt,
	}, nil
}

// UpdateStorageQuota updates a storage quota.
func (p *PostgresDB) UpdateStorageQuota(ctx context.Context, quota *models.StorageQuota) error {
	err := p.q.UpdateStorageQuota(ctx, dbsqlc.UpdateStorageQuotaParams{
		MaxSizeBytes:         quota.MaxSizeBytes,
		MaxArtifactCount:     int64(quota.MaxArtifactCount),
		WarnThresholdPercent: toInt32(quota.WarnThresholdPercent),
		UpdatedAt:            time.Now(),
		RepositoryID:         quota.RepositoryID,
	})
	if err != nil {
		return fmt.Errorf("failed to update storage quota: %w", err)
	}

	return nil
}

// IncrementStorageUsage increments the storage usage for a repository.
func (p *PostgresDB) IncrementStorageUsage(ctx context.Context, repoID string, sizeBytes int64, artifactCount int) error {
	err := p.q.IncrementStorageUsage(ctx, dbsqlc.IncrementStorageUsageParams{
		CurrentSizeBytes:     sizeBytes,
		CurrentArtifactCount: int64(artifactCount),
		UpdatedAt:            time.Now(),
		RepositoryID:         repoID,
	})
	if err != nil {
		return fmt.Errorf("failed to increment storage usage: %w", err)
	}

	return nil
}

// CreateStorageBackend creates a new storage backend configuration.
func (p *PostgresDB) CreateStorageBackend(ctx context.Context, backend *models.StorageBackend) error {
	return p.q.CreateStorageBackend(ctx, dbsqlc.CreateStorageBackendParams{
		ID:            backend.ID,
		Name:          backend.Name,
		Kind:          backend.Kind,
		IsDefault:     backend.IsDefault,
		Config:        backend.Config,
		Status:        backend.Status,
		StatusMessage: backend.StatusMessage,
		CreatedAt:     backend.CreatedAt,
		UpdatedAt:     backend.UpdatedAt,
	})
}

// ListStorageBackends lists all storage backends.
func (p *PostgresDB) ListStorageBackends(ctx context.Context) ([]*models.StorageBackend, error) {
	rows, err := p.q.ListStorageBackends(ctx)
	if err != nil {
		return nil, err
	}

	backends := make([]*models.StorageBackend, 0, len(rows))

	for _, row := range rows {
		backends = append(backends, &models.StorageBackend{
			ID:            row.ID,
			Name:          row.Name,
			Kind:          row.Kind,
			IsDefault:     row.IsDefault,
			Config:        row.Config,
			Status:        row.Status,
			StatusMessage: row.StatusMessage,
			LastCheckedAt: row.LastCheckedAt,
			CreatedAt:     row.CreatedAt,
			UpdatedAt:     row.UpdatedAt,
		})
	}

	return backends, nil
}

// GetStorageBackend gets a storage backend by name.
func (p *PostgresDB) GetStorageBackend(ctx context.Context, name string) (*models.StorageBackend, error) {
	row, err := p.q.GetStorageBackend(ctx, name)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, err
	}

	return &models.StorageBackend{
		ID:            row.ID,
		Name:          row.Name,
		Kind:          row.Kind,
		IsDefault:     row.IsDefault,
		Config:        row.Config,
		Status:        row.Status,
		StatusMessage: row.StatusMessage,
		LastCheckedAt: row.LastCheckedAt,
		CreatedAt:     row.CreatedAt,
		UpdatedAt:     row.UpdatedAt,
	}, nil
}

// UpdateStorageBackend updates a storage backend configuration.
func (p *PostgresDB) UpdateStorageBackend(ctx context.Context, backend *models.StorageBackend) error {
	return p.q.UpdateStorageBackend(ctx, dbsqlc.UpdateStorageBackendParams{
		Name:      backend.Name,
		Kind:      backend.Kind,
		IsDefault: backend.IsDefault,
		Config:    backend.Config,
		UpdatedAt: backend.UpdatedAt,
		ID:        backend.ID,
	})
}

// DeleteStorageBackend deletes a storage backend.
func (p *PostgresDB) DeleteStorageBackend(ctx context.Context, id string) error {
	return p.q.DeleteStorageBackend(ctx, id)
}

// UpdateStorageBackendStatus updates the status of a storage backend.
func (p *PostgresDB) UpdateStorageBackendStatus(ctx context.Context, id, status string, message *string) error {
	return p.q.UpdateStorageBackendStatus(ctx, dbsqlc.UpdateStorageBackendStatusParams{
		Status:        status,
		StatusMessage: message,
		ID:            id,
	})
}

// SetArtifactProperty sets or updates an artifact property (UPSERT).
func (p *PostgresDB) SetArtifactProperty(ctx context.Context, repoID, artifactID, key, value string) error {
	err := p.q.SetArtifactProperty(ctx, dbsqlc.SetArtifactPropertyParams{
		RepositoryID: repoID,
		ArtifactPath: artifactID,
		Key:          key,
		Value:        value,
	})
	if err != nil {
		return fmt.Errorf("failed to set artifact property: %w", err)
	}

	return nil
}

// GetArtifactProperty gets a single artifact property.
func (p *PostgresDB) GetArtifactProperty(ctx context.Context, repoID, artifactID, key string) (string, error) {
	value, err := p.q.GetArtifactProperty(ctx, dbsqlc.GetArtifactPropertyParams{
		RepositoryID: repoID,
		ArtifactPath: artifactID,
		Key:          key,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}

	if err != nil {
		return "", fmt.Errorf("failed to get artifact property: %w", err)
	}

	return value, nil
}

// DeleteArtifactProperty deletes an artifact property.
func (p *PostgresDB) DeleteArtifactProperty(ctx context.Context, repoID, artifactID, key string) error {
	err := p.q.DeleteArtifactProperty(ctx, dbsqlc.DeleteArtifactPropertyParams{
		RepositoryID: repoID,
		ArtifactPath: artifactID,
		Key:          key,
	})
	if err != nil {
		return fmt.Errorf("failed to delete artifact property: %w", err)
	}

	return nil
}

// ListArtifactProperties lists all properties for an artifact.
func (p *PostgresDB) ListArtifactProperties(ctx context.Context, repoID, artifactID string) (map[string]string, error) {
	rows, err := p.q.ListArtifactProperties(ctx, dbsqlc.ListArtifactPropertiesParams{
		RepositoryID: repoID,
		ArtifactPath: artifactID,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list artifact properties: %w", err)
	}

	result := make(map[string]string, len(rows))

	for _, row := range rows {
		result[row.Key] = row.Value
	}

	return result, nil
}

// SearchByProperty searches for artifacts by property key and value.
func (p *PostgresDB) SearchByProperty(ctx context.Context, repoID, key, value string) ([]string, error) {
	result, err := p.q.SearchByProperty(ctx, dbsqlc.SearchByPropertyParams{
		RepositoryID: repoID,
		Key:          key,
		Value:        value,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to search by property: %w", err)
	}

	return result, nil
}

// Project methods

func projectFromRow(row *dbsqlc.Project) *models.Project {
	return &models.Project{
		ID:               row.ID,
		Name:             row.Name,
		DisplayName:      row.DisplayName,
		Description:      row.Description,
		OwnerID:          row.OwnerID,
		Slug:             row.Slug,
		AllowedFormats:   row.AllowedFormats,
		AllowedRepoTypes: row.AllowedRepoTypes,
		RepoNamePrefix:   row.RepoNamePrefix,
		AllowPublicRepos: row.AllowPublicRepos,
		MaxRepositories:  int(row.MaxRepositories),
		TokenMaxTTLDays:  int(row.TokenMaxTtlDays),
		CreatedAt:        row.CreatedAt,
		UpdatedAt:        row.UpdatedAt,
	}
}

func (p *PostgresDB) CreateProject(ctx context.Context, project *models.Project) error {
	// The recorded owner is an implicit member: cached grants must not outlive this write.
	defer p.authCache.invalidate()

	if project.Slug == "" {
		project.Slug = models.ProjectSlug(project.Name) // e.g. restored from a backup that predates slugs
	}

	// Unset policy fields take the column defaults.
	if project.AllowedRepoTypes == nil {
		project.AllowedRepoTypes = []string{models.ProjectRepoTypeHosted}
	}

	row, err := p.q.CreateProject(ctx, dbsqlc.CreateProjectParams{
		Name:             project.Name,
		Slug:             project.Slug,
		DisplayName:      project.DisplayName,
		Description:      project.Description,
		OwnerID:          project.OwnerID,
		AllowedFormats:   project.AllowedFormats,
		AllowedRepoTypes: project.AllowedRepoTypes,
		RepoNamePrefix:   project.RepoNamePrefix,
		AllowPublicRepos: project.AllowPublicRepos,
		MaxRepositories:  int32(project.MaxRepositories), //nolint:gosec // validated range by callers, column is integer
		TokenMaxTtlDays:  int32(project.TokenMaxTTLDays), //nolint:gosec // validated range by callers, column is integer
	})
	if err != nil {
		return fmt.Errorf("failed to create project: %w", err)
	}

	project.ID = row.ID
	project.CreatedAt = row.CreatedAt
	project.UpdatedAt = row.UpdatedAt

	return nil
}

func (p *PostgresDB) GetProject(ctx context.Context, id string) (*models.Project, error) {
	row, err := p.q.GetProject(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get project: %w", err)
	}

	return projectFromRow(&row), nil
}

func (p *PostgresDB) GetProjectByName(ctx context.Context, name string) (*models.Project, error) {
	row, err := p.q.GetProjectByName(ctx, name)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get project by name: %w", err)
	}

	return projectFromRow(&row), nil
}

func (p *PostgresDB) UpdateProject(ctx context.Context, project *models.Project) error {
	// owner_id changes what members may do.
	defer p.authCache.invalidate()

	err := p.q.UpdateProject(ctx, dbsqlc.UpdateProjectParams{
		DisplayName:      project.DisplayName,
		Description:      project.Description,
		OwnerID:          project.OwnerID,
		AllowedFormats:   project.AllowedFormats,
		AllowedRepoTypes: project.AllowedRepoTypes,
		RepoNamePrefix:   project.RepoNamePrefix,
		AllowPublicRepos: project.AllowPublicRepos,
		MaxRepositories:  int32(project.MaxRepositories), //nolint:gosec // validated range by callers, column is integer
		TokenMaxTtlDays:  int32(project.TokenMaxTTLDays), //nolint:gosec // validated range by callers, column is integer
		ID:               project.ID,
	})
	if err != nil {
		return fmt.Errorf("failed to update project: %w", err)
	}

	return nil
}

func (p *PostgresDB) DeleteProject(ctx context.Context, id string) error {
	defer p.authCache.invalidate()

	err := p.q.DeleteProject(ctx, id)
	if err != nil {
		return fmt.Errorf("failed to delete project: %w", err)
	}

	return nil
}

func projectsFromRows(rows []dbsqlc.Project) []*models.Project {
	projects := make([]*models.Project, 0, len(rows))

	for i := range rows {
		projects = append(projects, projectFromRow(&rows[i]))
	}

	return projects
}

func (p *PostgresDB) ListProjects(ctx context.Context, userID string) ([]*models.Project, error) {
	rows, err := p.q.ListProjectsByUser(ctx, &userID)
	if err != nil {
		return nil, fmt.Errorf("failed to list projects: %w", err)
	}

	return projectsFromRows(rows), nil
}

func (p *PostgresDB) ListAllProjects(ctx context.Context) ([]*models.Project, error) {
	rows, err := p.q.ListAllProjects(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list all projects: %w", err)
	}

	return projectsFromRows(rows), nil
}

func (p *PostgresDB) AddProjectMember(ctx context.Context, projectID, userID, role string) error {
	defer p.authCache.invalidate()

	_, err := p.q.AddProjectMember(ctx, dbsqlc.AddProjectMemberParams{
		ProjectID: projectID,
		UserID:    userID,
		Role:      role,
	})
	if err != nil {
		return fmt.Errorf("failed to add project member: %w", err)
	}

	return nil
}

func (p *PostgresDB) ListProjectMembers(ctx context.Context, projectID string) ([]*models.ProjectMember, error) {
	rows, err := p.q.ListProjectMembers(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("failed to list project members: %w", err)
	}

	members := make([]*models.ProjectMember, 0, len(rows))

	for _, row := range rows {
		members = append(members, &models.ProjectMember{
			ID:        idString(row.ID),
			ProjectID: row.ProjectID,
			UserID:    row.UserID,
			Username:  row.Username,
			Role:      row.Role,
			CreatedAt: row.CreatedAt,
		})
	}

	return members, nil
}

func (p *PostgresDB) GetProjectMember(ctx context.Context, projectID, userID string) (*models.ProjectMember, error) {
	row, err := p.q.GetProjectMember(ctx, dbsqlc.GetProjectMemberParams{
		ProjectID: projectID,
		UserID:    userID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get project member: %w", err)
	}

	return &models.ProjectMember{
		ID:        idString(row.ID),
		ProjectID: row.ProjectID,
		UserID:    row.UserID,
		Username:  row.Username,
		Role:      row.Role,
		CreatedAt: row.CreatedAt,
	}, nil
}

func (p *PostgresDB) ListProjectGroups(ctx context.Context, projectID string) ([]*models.ProjectGroup, error) {
	rows, err := p.q.ListProjectGroups(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("failed to list project groups: %w", err)
	}

	groups := make([]*models.ProjectGroup, 0, len(rows))

	for _, row := range rows {
		groups = append(groups, &models.ProjectGroup{
			ProjectID: row.ProjectID,
			GroupID:   row.GroupID,
			GroupName: row.GroupName,
			Role:      row.Role,
			CreatedBy: row.CreatedBy,
			CreatedAt: row.CreatedAt,
		})
	}

	return groups, nil
}

func (p *PostgresDB) GetProjectGroup(ctx context.Context, projectID, groupID string) (*models.ProjectGroup, error) {
	row, err := p.q.GetProjectGroup(ctx, dbsqlc.GetProjectGroupParams{ProjectID: projectID, GroupID: groupID})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get project group: %w", err)
	}

	return &models.ProjectGroup{
		ProjectID: row.ProjectID,
		GroupID:   row.GroupID,
		GroupName: row.GroupName,
		Role:      row.Role,
		CreatedBy: row.CreatedBy,
		CreatedAt: row.CreatedAt,
	}, nil
}

// SetProjectGroup attaches the group to the project with role, or changes the
// role of an existing attachment.
func (p *PostgresDB) SetProjectGroup(ctx context.Context, projectID, groupID, role string, createdBy *string) error {
	defer p.authCache.invalidate()

	err := p.q.SetProjectGroup(ctx, dbsqlc.SetProjectGroupParams{
		ProjectID: projectID,
		GroupID:   groupID,
		Role:      role,
		CreatedBy: createdBy,
	})
	if err != nil {
		return fmt.Errorf("failed to set project group: %w", err)
	}

	return nil
}

// GetSystemSetting retrieves a system setting by key.
func (p *PostgresDB) GetSystemSetting(ctx context.Context, key string) (string, error) {
	value, err := p.q.GetSystemSetting(ctx, key)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}

	if err != nil {
		return "", fmt.Errorf("failed to get system setting: %w", err)
	}

	return value, nil
}

// SetSystemSetting sets or updates a system setting.
func (p *PostgresDB) SetSystemSetting(ctx context.Context, key, value string) error {
	err := p.q.SetSystemSetting(ctx, dbsqlc.SetSystemSettingParams{
		Key:   key,
		Value: value,
	})
	if err != nil {
		return fmt.Errorf("failed to set system setting: %w", err)
	}

	return nil
}

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
func (p *PostgresDB) GetCondaPackageFile(ctx context.Context, repoID, name, version, buildString, platform, extension string) (*models.CondaPackage, error) {
	row, err := p.q.GetCondaPackageFile(ctx, dbsqlc.GetCondaPackageFileParams{
		RepositoryID: repoID,
		Name:         name,
		Version:      version,
		BuildString:  buildString,
		Platform:     platform,
		Extension:    extension,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get conda package: %w", err)
	}

	return condaRowToModel(row), nil
}

// condaRowToModel converts any of the three structurally identical conda row
// types (GetFile/List/ListAll share the same SELECT list).
func condaRowToModel(row dbsqlc.GetCondaPackageFileRow) *models.CondaPackage {
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
	if err != nil {
		return nil, fmt.Errorf("failed to query conda packages: %w", err)
	}

	var packages []*models.CondaPackage

	for _, row := range rows {
		packages = append(packages, condaRowToModel(dbsqlc.GetCondaPackageFileRow(row)))
	}

	return packages, nil
}

// ListAllCondaPackages lists all conda packages for a repository across
// every platform. Used only by backup export, where ListCondaPackages'
// mandatory platform filter would silently drop everything else.
func (p *PostgresDB) ListAllCondaPackages(ctx context.Context, repoID string) ([]*models.CondaPackage, error) {
	rows, err := p.q.ListAllCondaPackages(ctx, repoID)
	if err != nil {
		return nil, fmt.Errorf("failed to query all conda packages: %w", err)
	}

	var packages []*models.CondaPackage

	for _, row := range rows {
		packages = append(packages, condaRowToModel(dbsqlc.GetCondaPackageFileRow(row)))
	}

	return packages, nil
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
		RepositoryID:    pkg.RepositoryID,
		Name:            pkg.Name,
		Version:         pkg.Version,
		UserChannel:     pkg.UserChannel,
		RecipeRevision:  pkg.RecipeRevision,
		PackageID:       pkg.PackageID,
		PackageRevision: pkg.PackageRevision,
		Filename:        pkg.Filename,
		BlobID:          blobID,
	})
	if err != nil {
		return fmt.Errorf("failed to create conan package: %w", err)
	}

	pkg.ID = row.ID
	pkg.CreatedAt = row.CreatedAt
	pkg.UpdatedAt = row.UpdatedAt

	return nil
}

// conanPackageFromRow converts a conan_packages row joined with its blob.
func conanPackageFromRow(row dbsqlc.GetConanPackageRow) *models.ConanPackage {
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
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get conan package: %w", err)
	}

	return conanPackageFromRow(row), nil
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

func (p *PostgresDB) DeleteConanPackage(ctx context.Context, id string) error {
	if err := p.q.DeleteConanPackage(ctx, id); err != nil {
		return fmt.Errorf("failed to delete conan package: %w", err)
	}

	return nil
}

func (p *PostgresDB) ListConanPackages(ctx context.Context, repoID string) ([]*models.ConanPackage, error) {
	rows, err := p.q.ListConanPackages(ctx, repoID)
	if err != nil {
		return nil, fmt.Errorf("failed to query conan packages: %w", err)
	}

	var packages []*models.ConanPackage

	for _, row := range rows {
		packages = append(packages, conanPackageFromRow(dbsqlc.GetConanPackageRow(row)))
	}

	return packages, nil
}

// CreateIvyArtifact stores an Ivy artifact; a live artifact with the same
// coordinates is repointed at the new blob.
func (p *PostgresDB) CreateIvyArtifact(ctx context.Context, artifact *models.IvyArtifact) error {
	blobID, err := p.upsertBlobWithMediaType(ctx, artifact.SHA256, artifact.BlobKey, "application/java-archive", artifact.SizeBytes)
	if err != nil {
		return fmt.Errorf("failed to upsert blob for ivy artifact: %w", err)
	}

	row, err := p.q.CreateIvyArtifact(ctx, dbsqlc.CreateIvyArtifactParams{
		RepositoryID: artifact.RepositoryID,
		Organisation: artifact.Organisation,
		Module:       artifact.Module,
		Revision:     artifact.Revision,
		ArtifactName: artifact.ArtifactName,
		ArtifactType: artifact.ArtifactType,
		ArtifactExt:  artifact.ArtifactExt,
		BlobID:       blobID,
	})
	if err != nil {
		return fmt.Errorf("failed to create ivy artifact: %w", err)
	}

	artifact.ID = row.ID
	artifact.CreatedAt = row.CreatedAt
	artifact.UpdatedAt = row.UpdatedAt

	return nil
}

func (p *PostgresDB) GetIvyArtifact(ctx context.Context, repoID, organisation, module, revision, artifactName, artifactExt string) (*models.IvyArtifact, error) {
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

// CreateSBTArtifact stores an SBT artifact; a live artifact with the same
// coordinates is repointed at the new blob.
func (p *PostgresDB) CreateSBTArtifact(ctx context.Context, artifact *models.SBTArtifact) error {
	blobID, err := p.upsertBlobWithMediaType(ctx, artifact.SHA256, artifact.BlobKey, "application/java-archive", artifact.SizeBytes)
	if err != nil {
		return fmt.Errorf("failed to upsert blob for sbt artifact: %w", err)
	}

	row, err := p.q.CreateSBTArtifact(ctx, dbsqlc.CreateSBTArtifactParams{
		RepositoryID: artifact.RepositoryID,
		GroupID:      artifact.GroupID,
		ArtifactID:   artifact.ArtifactID,
		Version:      artifact.Version,
		Filename:     artifact.Filename,
		BlobID:       blobID,
	})
	if err != nil {
		return fmt.Errorf("failed to create sbt artifact: %w", err)
	}

	artifact.ID = row.ID
	artifact.CreatedAt = row.CreatedAt
	artifact.UpdatedAt = row.UpdatedAt

	return nil
}

func (p *PostgresDB) GetSBTArtifact(ctx context.Context, repoID, groupID, artifactID, version, filename string) (*models.SBTArtifact, error) {
	row, err := p.q.GetSBTArtifact(ctx, dbsqlc.GetSBTArtifactParams{
		RepositoryID: repoID,
		GroupID:      groupID,
		ArtifactID:   artifactID,
		Version:      version,
		Filename:     filename,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get sbt artifact: %w", err)
	}

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
	}, nil
}

func (p *PostgresDB) DeleteSBTArtifact(ctx context.Context, id string) error {
	if err := p.q.DeleteSBTArtifact(ctx, id); err != nil {
		return fmt.Errorf("failed to delete sbt artifact: %w", err)
	}

	return nil
}

func (p *PostgresDB) ListSBTArtifacts(ctx context.Context, repoID string) ([]*models.SBTArtifact, error) {
	rows, err := p.q.ListSBTArtifacts(ctx, repoID)
	if err != nil {
		return nil, fmt.Errorf("failed to query sbt artifacts: %w", err)
	}

	var artifacts []*models.SBTArtifact

	for _, row := range rows {
		artifacts = append(artifacts, &models.SBTArtifact{
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
		})
	}

	return artifacts, nil
}

// UpsertAIAnalysis inserts or updates an AI analysis record.
//
// AI analyses are stored across scan_jobs + artifact_refs + ai_results;
// ArtifactName/ArtifactVersion live on ai_results (artifact_refs only tracks
// repository_id/artifact_path/format). analysis.Status's "processing" value is
// mapped onto scan_jobs' CHECK-constrained "running" (see aiStatusToJob/FromJob).
//
// Only a "completed" analysis overwrites the result columns (summary, tags,
// license, security notes, model, token counts). A "processing", "pending" or
// "failed" upsert keeps what the last completed run stored, so a re-trigger or
// a failed re-analysis never wipes a good result; the status and error live on
// scan_jobs and always follow the latest upsert.
func (p *PostgresDB) UpsertAIAnalysis(ctx context.Context, analysis *models.AIAnalysis) error {
	tagsJSON, err := json.Marshal(analysis.Tags)
	if err != nil {
		return fmt.Errorf("marshal tags: %w", err)
	}

	_, err = p.db.ExecContext(ctx, `
		WITH ref AS (
			INSERT INTO artifact_refs (repository_id, artifact_path, format)
			VALUES ($1, $2, COALESCE($3, (SELECT format FROM repositories WHERE id = $1), 'unknown'))
			ON CONFLICT (repository_id, artifact_path) DO UPDATE SET artifact_path = EXCLUDED.artifact_path
			RETURNING id
		), job AS (
			INSERT INTO scan_jobs (artifact_ref_id, scan_type, scanner, status, error_message)
			SELECT ref.id, 'ai', '', $4, NULLIF($5, '') FROM ref
			ON CONFLICT (artifact_ref_id, scan_type, scanner) DO UPDATE SET
				status = EXCLUDED.status, error_message = EXCLUDED.error_message, updated_at = now()
			RETURNING id
		)
		INSERT INTO ai_results (scan_job_id, summary, tags, license_status, license_name, security_notes, quarantine_recommended, model_used, prompt_tokens, completion_tokens, artifact_name, artifact_version)
		SELECT job.id, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16 FROM job
		ON CONFLICT (scan_job_id) DO UPDATE SET
			summary = CASE WHEN $4::text = 'completed' THEN EXCLUDED.summary ELSE ai_results.summary END,
			tags = CASE WHEN $4::text = 'completed' THEN EXCLUDED.tags ELSE ai_results.tags END,
			license_status = CASE WHEN $4::text = 'completed' THEN EXCLUDED.license_status ELSE ai_results.license_status END,
			license_name = CASE WHEN $4::text = 'completed' THEN EXCLUDED.license_name ELSE ai_results.license_name END,
			security_notes = CASE WHEN $4::text = 'completed' THEN EXCLUDED.security_notes ELSE ai_results.security_notes END,
			quarantine_recommended = CASE WHEN $4::text = 'completed' THEN EXCLUDED.quarantine_recommended ELSE ai_results.quarantine_recommended END,
			model_used = CASE WHEN $4::text = 'completed' THEN EXCLUDED.model_used ELSE ai_results.model_used END,
			prompt_tokens = CASE WHEN $4::text = 'completed' THEN EXCLUDED.prompt_tokens ELSE ai_results.prompt_tokens END,
			completion_tokens = CASE WHEN $4::text = 'completed' THEN EXCLUDED.completion_tokens ELSE ai_results.completion_tokens END,
			artifact_name = EXCLUDED.artifact_name, artifact_version = EXCLUDED.artifact_version`,
		analysis.RepositoryID, analysis.ArtifactID, strPtr(analysis.ArtifactFormat), aiStatusToJob(analysis.Status), analysis.ErrorMessage,
		analysis.Summary, tagsJSON, analysis.LicenseStatus, analysis.LicenseName, analysis.SecurityNotes,
		analysis.QuarantineRecommended, analysis.ModelUsed, analysis.PromptTokens, analysis.CompletionTokens,
		analysis.ArtifactName, analysis.ArtifactVersion,
	)

	return err
}

const aiAnalysisSelect = `SELECT air.id, ref.repository_id, ref.artifact_path, ref.format,
	       air.summary, air.tags, air.license_status, air.license_name, air.security_notes,
	       air.quarantine_recommended, sj.status, sj.error_message, air.model_used,
	       air.prompt_tokens, air.completion_tokens, sj.created_at, sj.updated_at,
	       air.artifact_name, air.artifact_version
	FROM ai_results air
	JOIN scan_jobs sj ON sj.id = air.scan_job_id
	JOIN artifact_refs ref ON ref.id = sj.artifact_ref_id`

// GetAIAnalysis retrieves an AI analysis by repo and artifact ID.
func (p *PostgresDB) GetAIAnalysis(ctx context.Context, repoID, artifactID string) (*models.AIAnalysis, error) {
	row := p.db.QueryRowContext(ctx,
		aiAnalysisSelect+` WHERE sj.scan_type = 'ai' AND ref.repository_id = $1 AND ref.artifact_path = $2`,
		repoID, artifactID)

	return scanAIAnalysis(row)
}

// ListAIAnalyses lists AI analyses for a repository with optional status and
// REVIEW_REQUIRED license filters.
func (p *PostgresDB) ListAIAnalyses(ctx context.Context, repoID string, status string, licenseReviewOnly bool, limit, offset int) ([]*models.AIAnalysis, int, error) {
	args := []any{repoID}
	where := "WHERE sj.scan_type = 'ai' AND ref.repository_id = $1"

	if status != "" {
		args = append(args, aiStatusToJob(status))
		where += fmt.Sprintf(" AND sj.status = $%d", len(args))
	}

	if licenseReviewOnly {
		where += " AND air.license_status = 'REVIEW_REQUIRED'"
	}

	var total int

	countQuery := `SELECT COUNT(*) FROM ai_results air
		JOIN scan_jobs sj ON sj.id = air.scan_job_id
		JOIN artifact_refs ref ON ref.id = sj.artifact_ref_id ` + where
	if err := p.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	args = append(args, limit, offset)

	//nolint:gosec // G202: where clause is built from controlled inputs only ($1 placeholder + static AND status = $N)
	query := aiAnalysisSelect + ` ` + where + fmt.Sprintf(` ORDER BY sj.updated_at DESC LIMIT $%d OFFSET $%d`, len(args)-1, len(args))

	rows, err := p.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}

	defer rows.Close()

	var results []*models.AIAnalysis

	for rows.Next() {
		a, err := scanAIAnalysisRow(rows)
		if err != nil {
			return nil, 0, err
		}

		results = append(results, a)
	}

	return results, total, rows.Err()
}

// DeleteAIAnalysis removes an AI analysis record.
func (p *PostgresDB) DeleteAIAnalysis(ctx context.Context, repoID, artifactID string) error {
	_, err := p.db.ExecContext(ctx, `
		DELETE FROM ai_results air
		USING scan_jobs sj, artifact_refs ref
		WHERE air.scan_job_id = sj.id AND sj.artifact_ref_id = ref.id
		  AND sj.scan_type = 'ai' AND ref.repository_id = $1 AND ref.artifact_path = $2`,
		repoID, artifactID)

	return err
}

// ListPendingAIAnalyses returns analyses in "pending" status up to limit.
func (p *PostgresDB) ListPendingAIAnalyses(ctx context.Context, limit int) ([]*models.AIAnalysis, error) {
	rows, err := p.db.QueryContext(ctx,
		aiAnalysisSelect+` WHERE sj.scan_type = 'ai' AND sj.status = 'pending' ORDER BY sj.created_at ASC LIMIT $1`,
		limit)
	if err != nil {
		return nil, err
	}

	defer rows.Close()

	var results []*models.AIAnalysis

	for rows.Next() {
		a, err := scanAIAnalysisRow(rows)
		if err != nil {
			return nil, err
		}

		results = append(results, a)
	}

	return results, rows.Err()
}

type aiAnalysisScanner interface {
	Scan(dest ...any) error
}

func scanAIAnalysis(s aiAnalysisScanner) (*models.AIAnalysis, error) {
	return scanAIAnalysisRow(s)
}

// scanAIAnalysisRow scans an aiAnalysisSelect row.
func scanAIAnalysisRow(s aiAnalysisScanner) (*models.AIAnalysis, error) {
	var a models.AIAnalysis

	var tagsJSON []byte

	var errMsg sql.NullString

	var jobStatus string

	err := s.Scan(
		&a.ID, &a.RepositoryID, &a.ArtifactID, &a.ArtifactFormat,
		&a.Summary, &tagsJSON, &a.LicenseStatus, &a.LicenseName,
		&a.SecurityNotes, &a.QuarantineRecommended, &jobStatus,
		&errMsg, &a.ModelUsed,
		&a.PromptTokens, &a.CompletionTokens,
		&a.CreatedAt, &a.UpdatedAt,
		&a.ArtifactName, &a.ArtifactVersion,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
		}

		return nil, err
	}

	a.Status = aiStatusFromJob(jobStatus)
	a.ErrorMessage = errMsg.String

	if len(tagsJSON) > 0 {
		_ = json.Unmarshal(tagsJSON, &a.Tags)
	}

	return &a, nil
}
