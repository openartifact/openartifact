// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"database/sql"
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

// NuGet methods

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

// Cargo methods

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

// RubyGems methods

func vulnScanStatusFromJob(status string) string {
	if status == "running" {
		return "scanning"
	}

	return status
}
