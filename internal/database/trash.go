// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/openartifact/openartifact/internal/database/models"
)

var (
	// ErrTrashNotFound means no trashed row matches the kind/id (it was never
	// deleted, was already restored, or was purged).
	ErrTrashNotFound = errors.New("trash item not found")
	// ErrAmbiguousPyPIFilename means several packages of one repository hold a
	// file with the same name.
	ErrAmbiguousPyPIFilename = errors.New("pypi filename is ambiguous")
	// ErrTrashConflict means a restore collided with a live row that took over
	// the item's unique key while it sat in the trash.
	ErrTrashConflict = errors.New("a live item with the same identity already exists")
	// ErrTrashRepoTrashed means the item's repository is itself in the trash.
	ErrTrashRepoTrashed = errors.New("the item's repository is in the trash")
	// ErrTrashManifestInTrash means a docker tag cannot be restored because the
	// manifest it points at is itself in the trash (restore the manifest, which
	// brings its tags back).
	ErrTrashManifestInTrash = errors.New("the tag's manifest is in the trash")
	// ErrTrashUnknownKind means the kind is not a registered trash kind.
	ErrTrashUnknownKind = errors.New("unknown trash kind")
)

const (
	defaultTrashPageLimit = 50
	maxTrashPageLimit     = 200
)

// TrashStore is the storage side of soft delete (internal/trash, the trash API
// and the purge worker). PostgresDB implements it; handlers treat a database
// that does not (test fakes) as "soft delete unavailable" and hard-delete.
//
// Retention is resolved per repository: repositories.trash_retention_days, or
// the server default (SetTrashRetentionDays) when NULL. 0 means no trash.
type TrashStore interface {
	// TrashRows soft-deletes the given rows of repoID (stamps deleted_at and
	// deleted_by) and returns true. It returns false without touching anything
	// when the repository's retention is 0: the caller then hard-deletes as
	// before. ErrTrashNotFound means none of the rows is live in repoID.
	TrashRows(ctx context.Context, repoID string, kind models.TrashKind, ids []string, deletedBy string) (bool, error)

	// TrashRepository soft-deletes a repository (contents stay in place, hidden
	// with it). It returns false without touching anything when retention is 0.
	TrashRepository(ctx context.Context, id, deletedBy string) (bool, error)

	// ListTrash lists trashed items (newest first) and the total count.
	ListTrash(ctx context.Context, f models.TrashFilter) ([]*models.TrashItem, int, error)

	// GetTrashItem returns one trashed item, ErrTrashNotFound when absent.
	GetTrashItem(ctx context.Context, kind models.TrashKind, id string) (*models.TrashItem, error)

	// RestoreTrash brings an item (or a repository) back. ErrTrashNotFound,
	// ErrTrashConflict (unique key taken meanwhile) and ErrTrashRepoTrashed
	// are the expected failures.
	RestoreTrash(ctx context.Context, kind models.TrashKind, id string) (*models.TrashItem, error)

	// PurgeTrash permanently removes a trashed item (or repository, cascading
	// its contents) and reports what the caller must clean up: blobs to free via
	// storage.SafeDeleteBlob and quota to release. A repository's blobs are not
	// listed (they can be numerous); the caller collects orphans afterwards.
	// A foreign-key violation (IsForeignKeyViolation) means the repository is
	// still referenced, e.g. by a release bundle.
	PurgeTrash(ctx context.Context, kind models.TrashKind, id string) (*models.TrashPurgeResult, error)

	// RegenKeys returns what the metadata workers key a refresh by for an item:
	// (group_id, artifact_id) for a Maven artifact, (package_id, "") for an npm
	// version and ("", "") for every other kind (deb/rpm refresh per repository).
	RegenKeys(ctx context.Context, kind models.TrashKind, id string) (string, string, error)

	// ListExpiredTrash returns up to limit items whose retention window has
	// passed (items of repositories that are themselves in the trash are
	// left to the repository's own purge).
	ListExpiredTrash(ctx context.Context, limit int) ([]models.TrashRef, error)
}

var _ TrashStore = (*PostgresDB)(nil)

// SetTrashRetentionDays sets the server-wide default retention, in days, for
// repositories whose trash_retention_days is NULL.
func (p *PostgresDB) SetTrashRetentionDays(days int) {
	if days < 0 {
		days = 0
	}

	p.trashDefaultDays.Store(int32(min(days, maxTrashRetentionDays)))
}

// maxTrashRetentionDays caps retention so interval arithmetic cannot overflow.
const maxTrashRetentionDays = 36500

func (p *PostgresDB) trashDefault() int {
	return int(p.trashDefaultDays.Load())
}

// effectiveRetention is the repository's retention in days (0 = no trash).
func (p *PostgresDB) effectiveRetention(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, repoID string,
) (int, error) {
	var days int

	err := q.QueryRowContext(ctx,
		`SELECT COALESCE(trash_retention_days, $2::int) FROM repositories WHERE id = $1::uuid`,
		repoID, p.trashDefault(),
	).Scan(&days)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrTrashNotFound
	}

	if err != nil {
		return 0, fmt.Errorf("resolve trash retention: %w", err)
	}

	return days, nil
}

// TrashRows implements TrashStore.
func (p *PostgresDB) TrashRows(ctx context.Context, repoID string, kind models.TrashKind, ids []string, deletedBy string) (bool, error) {
	spec, ok := trashSpecByKind(kind)
	if !ok {
		return false, fmt.Errorf("%w: %s", ErrTrashUnknownKind, kind)
	}

	if len(ids) == 0 {
		return false, ErrTrashNotFound
	}

	days, err := p.effectiveRetention(ctx, p.db, repoID)
	if err != nil {
		return false, err
	}

	if days == 0 {
		return false, nil
	}

	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin trash transaction: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	// One statement timestamp for the row and its cascade, so a cascaded child
	// is recognisably part of the same delete (and restored with it).
	//nolint:gosec // G202: spec.table and spec.repoID are constants of the kind registry; ids are bound arguments
	res, err := tx.ExecContext(ctx,
		`UPDATE `+spec.table+` t SET deleted_at = now(), deleted_by = $3
		 WHERE t.id = ANY($1::uuid[]) AND t.deleted_at IS NULL AND `+spec.repoID+` = $2::uuid`,
		uuidArray(ids), repoID, deletedBy,
	)
	if err != nil {
		return false, fmt.Errorf("trash %s: %w", kind, err)
	}

	if n, _ := res.RowsAffected(); n == 0 {
		return false, ErrTrashNotFound
	}

	for _, id := range ids {
		for _, stmt := range spec.cascade {
			if _, err := tx.ExecContext(ctx, stmt, id, deletedBy); err != nil {
				return false, fmt.Errorf("trash %s cascade: %w", kind, err)
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit trash transaction: %w", err)
	}

	return true, nil
}

// uuidArray renders ids as a Postgres array literal for ANY($1::uuid[]).
func uuidArray(ids []string) string {
	return "{" + strings.Join(ids, ",") + "}"
}

// TrashRepository implements TrashStore.
func (p *PostgresDB) TrashRepository(ctx context.Context, id, deletedBy string) (bool, error) {
	// Cached auth lookups must not outlive this write.
	defer p.authCache.invalidate()

	days, err := p.effectiveRetention(ctx, p.db, id)
	if err != nil {
		return false, err
	}

	if days == 0 {
		return false, nil
	}

	res, err := p.db.ExecContext(ctx,
		`UPDATE repositories SET deleted_at = now(), deleted_by = $2 WHERE id = $1::uuid AND deleted_at IS NULL`,
		id, deletedBy,
	)
	if err != nil {
		return false, fmt.Errorf("trash repository: %w", err)
	}

	if n, _ := res.RowsAffected(); n == 0 {
		return false, ErrTrashNotFound
	}

	return true, nil
}

// purgeAtExpr is the instant the purge worker removes a trashed row; $1 is the
// server default retention.
const purgeAtExpr = `%s.deleted_at + make_interval(days => COALESCE(r.trash_retention_days, $1::int))`

// itemSelect builds the listing/lookup SELECT of one spec. extra is appended to
// the WHERE clause (it starts with " AND ").
func (s *trashSpec) itemSelect(extra string) string {
	return fmt.Sprintf(
		`SELECT '%s' AS kind, t.id::text AS id, r.id::text AS repo_id, r.name AS repo_name, '%s' AS format,
		        (%s)::text AS name, (%s)::text AS version, %s AS size_bytes,
		        t.deleted_at AS deleted_at, COALESCE(t.deleted_by, '') AS deleted_by, `+purgeAtExpr+` AS purge_at
		 FROM %s t JOIN repositories r ON r.id = %s
		 WHERE t.deleted_at IS NOT NULL%s`,
		s.kind, s.format, s.name, s.version, s.blobSizeExpr(), "t", s.table, s.repoID, extra,
	)
}

// repoItemSelect is the repository branch of the listing.
func repoItemSelect(extra string) string {
	return fmt.Sprintf(
		`SELECT 'repository' AS kind, r.id::text AS id, r.id::text AS repo_id, r.name AS repo_name, r.format AS format,
		        r.name AS name, ''::text AS version, 0::bigint AS size_bytes,
		        r.deleted_at AS deleted_at, COALESCE(r.deleted_by, '') AS deleted_by, `+purgeAtExpr+` AS purge_at
		 FROM repositories r WHERE r.deleted_at IS NOT NULL%s`, "r", extra,
	)
}

type trashQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func scanTrashItem(sc interface{ Scan(...any) error }) (*models.TrashItem, error) {
	var (
		it   models.TrashItem
		kind string
	)

	if err := sc.Scan(&kind, &it.ID, &it.RepositoryID, &it.RepositoryName, &it.Format, &it.Name, &it.Version,
		&it.SizeBytes, &it.DeletedAt, &it.DeletedBy, &it.PurgeAt); err != nil {
		return nil, err //nolint:wrapcheck // wrapped by callers
	}

	it.Kind = models.TrashKind(kind)

	return &it, nil
}

// ListTrash implements TrashStore.
func (p *PostgresDB) ListTrash(ctx context.Context, f models.TrashFilter) ([]*models.TrashItem, int, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = defaultTrashPageLimit
	}

	limit = min(limit, maxTrashPageLimit)
	offset := max(f.Page-1, 0) * limit

	args := []any{p.trashDefault()}
	repoFilter := ""

	if f.RepositoryID != "" {
		args = append(args, f.RepositoryID)
		repoFilter = " AND r.id = $2::uuid"
	}

	var branches []string

	for _, s := range trashSpecs {
		if f.Kind != "" && f.Kind != s.kind {
			continue
		}

		extra := " AND r.deleted_at IS NULL" + repoFilter
		if s.listWhere != "" {
			extra += " AND " + s.listWhere
		}

		branches = append(branches, s.itemSelect(extra))
	}

	if f.IncludeRepositories && f.RepositoryID == "" && (f.Kind == "" || f.Kind == models.TrashKindRepository) {
		branches = append(branches, repoItemSelect(""))
	}

	if len(branches) == 0 {
		return nil, 0, nil
	}

	union := strings.Join(branches, "\nUNION ALL\n")

	var total int
	if err := p.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM (`+union+`) u`, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count trash: %w", err)
	}

	args = append(args, limit, offset)

	rows, err := p.db.QueryContext(ctx,
		fmt.Sprintf(`SELECT kind, id, repo_id, repo_name, format, name, version, size_bytes, deleted_at, deleted_by, purge_at FROM (%s) u ORDER BY deleted_at DESC, id LIMIT $%d OFFSET $%d`, union, len(args)-1, len(args)),
		args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list trash: %w", err)
	}

	defer func() { _ = rows.Close() }()

	items := []*models.TrashItem{}

	for rows.Next() {
		it, err := scanTrashItem(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("scan trash item: %w", err)
		}

		items = append(items, it)
	}

	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate trash: %w", err)
	}

	return items, total, nil
}

// getTrashItem loads one trashed row; lock adds FOR UPDATE.
func (p *PostgresDB) getTrashItem(ctx context.Context, q trashQuerier, kind models.TrashKind, id string, lock bool) (*models.TrashItem, error) {
	var query string

	suffix := ""
	if lock {
		suffix = " FOR UPDATE OF %s"
	}

	if kind == models.TrashKindRepository {
		query = repoItemSelect(" AND r.id = $2::uuid")
		if lock {
			query += fmt.Sprintf(suffix, "r")
		}
	} else {
		spec, ok := trashSpecByKind(kind)
		if !ok {
			return nil, fmt.Errorf("%w: %s", ErrTrashUnknownKind, kind)
		}

		query = spec.itemSelect(" AND t.id = $2::uuid")
		if lock {
			query += fmt.Sprintf(suffix, "t")
		}
	}

	it, err := scanTrashItem(q.QueryRowContext(ctx, query, p.trashDefault(), id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrTrashNotFound
	}

	if err != nil {
		return nil, fmt.Errorf("get trash item: %w", err)
	}

	return it, nil
}

// GetTrashItem implements TrashStore.
func (p *PostgresDB) GetTrashItem(ctx context.Context, kind models.TrashKind, id string) (*models.TrashItem, error) {
	return p.getTrashItem(ctx, p.db, kind, id, false)
}

// TrashedRepositoryByName returns the repository in the trash that holds name
// (its name stays reserved until it is purged or restored), ErrTrashNotFound
// when no trashed repository has it. It is not part of TrashStore: only the
// repository-create conflict message needs it.
func (p *PostgresDB) TrashedRepositoryByName(ctx context.Context, name string) (*models.TrashItem, error) {
	it, err := scanTrashItem(p.db.QueryRowContext(ctx, repoItemSelect(" AND r.name = $2"), p.trashDefault(), name))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrTrashNotFound
	}

	if err != nil {
		return nil, fmt.Errorf("look up trashed repository: %w", err)
	}

	return it, nil
}

// repoTrashed reports whether the repository is in the trash.
func repoTrashed(ctx context.Context, q trashQuerier, repoID string) (bool, error) {
	var trashed bool
	if err := q.QueryRowContext(ctx,
		`SELECT deleted_at IS NOT NULL FROM repositories WHERE id = $1::uuid`, repoID).Scan(&trashed); err != nil {
		return false, fmt.Errorf("check repository trash state: %w", err)
	}

	return trashed, nil
}

// RestoreTrash implements TrashStore.
func (p *PostgresDB) RestoreTrash(ctx context.Context, kind models.TrashKind, id string) (*models.TrashItem, error) {
	if kind == models.TrashKindRepository {
		return p.restoreRepository(ctx, id)
	}

	spec, ok := trashSpecByKind(kind)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrTrashUnknownKind, kind)
	}

	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin restore transaction: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	item, err := p.getTrashItem(ctx, tx, kind, id, true)
	if err != nil {
		return nil, err
	}

	if trashed, err := repoTrashed(ctx, tx, item.RepositoryID); err != nil {
		return nil, err
	} else if trashed {
		return nil, ErrTrashRepoTrashed
	}

	if kind == models.TrashKindDockerTag {
		if err := ensureTagManifestLive(ctx, tx, id); err != nil {
			return nil, err
		}
	}

	// Companions (docker tags deleted with their manifest) go first: they are
	// matched on the manifest's still-set deleted_at.
	for _, stmt := range spec.restoreCascade() {
		if _, err := tx.ExecContext(ctx, stmt, id); err != nil {
			return nil, fmt.Errorf("restore %s companions: %w", kind, err)
		}
	}

	//nolint:gosec // G202: spec.table is a constant of the kind registry; the id is a bound argument
	if _, err := tx.ExecContext(ctx,
		`UPDATE `+spec.table+` SET deleted_at = NULL, deleted_by = NULL WHERE id = $1::uuid AND deleted_at IS NOT NULL`, id); err != nil {
		if IsUniqueViolation(err) {
			return nil, ErrTrashConflict
		}

		return nil, fmt.Errorf("restore %s: %w", kind, err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit restore transaction: %w", err)
	}

	return item, nil
}

// ensureTagManifestLive returns ErrTrashManifestInTrash when the trashed tag
// id points at a manifest (same repository, image and digest) that is not live.
func ensureTagManifestLive(ctx context.Context, tx *sql.Tx, tagID string) error {
	var live bool

	err := tx.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM docker_manifests m
			WHERE m.repository_id = dt.repository_id AND m.image = dt.image
			  AND m.digest = dt.manifest_digest AND m.deleted_at IS NULL)
		 FROM docker_tags dt WHERE dt.id = $1::uuid`, tagID).Scan(&live)
	if err != nil {
		return fmt.Errorf("check docker tag manifest: %w", err)
	}

	if !live {
		return ErrTrashManifestInTrash
	}

	return nil
}

func (p *PostgresDB) restoreRepository(ctx context.Context, id string) (*models.TrashItem, error) {
	// Cached auth lookups must not outlive this write.
	defer p.authCache.invalidate()

	item, err := p.getTrashItem(ctx, p.db, models.TrashKindRepository, id, false)
	if err != nil {
		return nil, err
	}

	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin restore transaction: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	// One captured delta shifts every row identically, so docker manifest/tag
	// cascade pairs (matched on equal deleted_at) stay equal.
	var delta string

	err = tx.QueryRowContext(ctx,
		`SELECT (now() - deleted_at)::text FROM repositories
		 WHERE id = $1::uuid AND deleted_at IS NOT NULL FOR UPDATE`, id).Scan(&delta)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrTrashNotFound
	}

	if err != nil {
		return nil, fmt.Errorf("lock trashed repository: %w", err)
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE repositories SET deleted_at = NULL, deleted_by = NULL WHERE id = $1::uuid`, id); err != nil {
		return nil, fmt.Errorf("restore repository: %w", err)
	}

	// Items trashed before the repository keep the window they had left when it
	// was trashed.
	for _, s := range trashSpecs {
		//nolint:gosec // G202: table and repoID are constants of the kind registry; the id is a bound argument
		if _, err := tx.ExecContext(ctx,
			`UPDATE `+s.table+` t SET deleted_at = t.deleted_at + $2::interval
			 WHERE `+s.repoID+` = $1::uuid AND t.deleted_at IS NOT NULL`, id, delta); err != nil {
			return nil, fmt.Errorf("shift trashed %s: %w", s.kind, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit restore transaction: %w", err)
	}

	return item, nil
}

// restoreCascade returns the statements that revive the companions of a row
// (and move the validator of a derived document); $1 is the row id.
func (s *trashSpec) restoreCascade() []string {
	switch s.kind { //nolint:exhaustive // only two kinds have restore side effects
	case models.TrashKindNPM:
		return []string{npmTouchPackumentOnRestore}
	case models.TrashKindDockerManifest:
		return []string{`UPDATE docker_tags dt SET deleted_at = NULL, deleted_by = NULL
		FROM docker_manifests m
		WHERE m.id = $1::uuid AND dt.repository_id = m.repository_id AND dt.image = m.image
		  AND dt.manifest_digest = m.digest AND dt.deleted_at = m.deleted_at
		  AND NOT EXISTS (SELECT 1 FROM docker_tags l
		      WHERE l.repository_id = dt.repository_id AND l.image = dt.image
		        AND l.name = dt.name AND l.deleted_at IS NULL)`}
	default:
		return nil
	}
}

// PurgeTrash implements TrashStore.
func (p *PostgresDB) PurgeTrash(ctx context.Context, kind models.TrashKind, id string) (*models.TrashPurgeResult, error) {
	if kind == models.TrashKindRepository {
		return p.purgeRepository(ctx, id)
	}

	spec, ok := trashSpecByKind(kind)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrTrashUnknownKind, kind)
	}

	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin purge transaction: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	item, err := p.getTrashItem(ctx, tx, kind, id, true)
	if err != nil {
		return nil, err
	}

	var shas string
	if err := tx.QueryRowContext(ctx,
		`SELECT `+spec.blobSHAExpr()+` FROM `+spec.table+` t WHERE t.id = $1::uuid`, id).Scan(&shas); err != nil {
		return nil, fmt.Errorf("collect blobs of %s: %w", kind, err)
	}

	pkgID, err := purgeCascades(ctx, tx, spec, id)
	if err != nil {
		return nil, err
	}

	//nolint:gosec // G202: spec.table is a constant of the kind registry; the id is a bound argument
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM `+spec.table+` WHERE id = $1::uuid AND deleted_at IS NOT NULL`, id); err != nil {
		return nil, fmt.Errorf("purge %s: %w", kind, err)
	}

	if pkgID != "" {
		// The last version of an npm package is gone: drop the empty package.
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM npm_packages p WHERE p.id = $1::uuid AND NOT EXISTS (SELECT 1 FROM npm_versions v WHERE v.package_id = p.id)`,
			pkgID); err != nil {
			return nil, fmt.Errorf("purge empty npm package: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit purge transaction: %w", err)
	}

	res := &models.TrashPurgeResult{
		Kind: kind, ID: id, RepositoryID: item.RepositoryID, RepositoryName: item.RepositoryName,
		Format: item.Format, Name: item.Name, Version: item.Version, SizeBytes: item.SizeBytes,
	}

	if shas != "" {
		res.BlobSHA256 = strings.Split(shas, ",")
	}

	if spec.quota {
		res.QuotaArtifacts = 1
		res.QuotaBytes = item.SizeBytes
	}

	return res, nil
}

// purgeCascades removes the rows that only make sense with the purged row and
// returns the npm package id to prune when kind is an npm version.
func purgeCascades(ctx context.Context, tx *sql.Tx, spec *trashSpec, id string) (string, error) {
	switch spec.kind { //nolint:exhaustive // only two kinds have dependants
	case models.TrashKindDockerManifest:
		// Tags trashed together with the manifest.
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM docker_tags dt USING docker_manifests m
			 WHERE m.id = $1::uuid AND dt.repository_id = m.repository_id AND dt.image = m.image
			   AND dt.manifest_digest = m.digest AND dt.deleted_at = m.deleted_at`, id); err != nil {
			return "", fmt.Errorf("purge docker tags of manifest: %w", err)
		}
	case models.TrashKindNPM:
		var pkgID string
		if err := tx.QueryRowContext(ctx, `SELECT package_id::text FROM npm_versions WHERE id = $1::uuid`, id).Scan(&pkgID); err != nil {
			return "", fmt.Errorf("find npm package of version: %w", err)
		}

		// Dist-tags pointing at the purged version (unless a live re-publish
		// of the same version now owns the tag).
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM npm_dist_tags d USING npm_versions v
			 WHERE v.id = $1::uuid AND d.package_id = v.package_id AND d.version = v.version
			   AND NOT EXISTS (SELECT 1 FROM npm_versions o
			       WHERE o.package_id = v.package_id AND o.version = v.version
			         AND o.id <> v.id AND o.deleted_at IS NULL)`, id); err != nil {
			return "", fmt.Errorf("purge npm dist-tags: %w", err)
		}

		return pkgID, nil
	}

	return "", nil
}

func (p *PostgresDB) purgeRepository(ctx context.Context, id string) (*models.TrashPurgeResult, error) {
	// Cached auth lookups must not outlive this write.
	defer p.authCache.invalidate()

	item, err := p.getTrashItem(ctx, p.db, models.TrashKindRepository, id, false)
	if err != nil {
		return nil, err
	}

	// One statement: every sub-statement sees the same snapshot, so the
	// repository's generated-metadata blobs are read before the cascade removes
	// their repository_metadata rows. Those blobs have no blobs row and would
	// otherwise stay in the object store forever.
	var (
		deleted int
		shas    string
	)

	if err := p.db.QueryRowContext(ctx,
		`WITH del AS (DELETE FROM repositories WHERE id = $1::uuid AND deleted_at IS NOT NULL RETURNING id)
		 SELECT (SELECT COUNT(*) FROM del),
		        COALESCE((SELECT string_agg(DISTINCT blob_sha256, ',') FROM repository_metadata WHERE repository_id = $1::uuid), '')`,
		id).Scan(&deleted, &shas); err != nil {
		return nil, fmt.Errorf("purge repository: %w", err)
	}

	if deleted == 0 {
		return nil, ErrTrashNotFound
	}

	out := &models.TrashPurgeResult{
		Kind: models.TrashKindRepository, ID: id, RepositoryID: id, RepositoryName: item.RepositoryName,
		Format: item.Format, Name: item.Name,
	}

	if shas != "" {
		out.MetadataBlobSHA256 = strings.Split(shas, ",")
	}

	return out, nil
}

// RegenKeys implements TrashStore.
func (p *PostgresDB) RegenKeys(ctx context.Context, kind models.TrashKind, id string) (string, string, error) {
	var a, b string

	var err error

	switch kind { //nolint:exhaustive // only two kinds are keyed
	case models.TrashKindMaven:
		err = p.db.QueryRowContext(ctx, `SELECT group_id, artifact_id_coord FROM maven_artifacts WHERE id = $1::uuid`, id).Scan(&a, &b)
	case models.TrashKindNPM:
		err = p.db.QueryRowContext(ctx, `SELECT package_id::text FROM npm_versions WHERE id = $1::uuid`, id).Scan(&a)
	default:
		return "", "", nil
	}

	if errors.Is(err, sql.ErrNoRows) {
		return "", "", ErrTrashNotFound
	}

	if err != nil {
		return "", "", fmt.Errorf("resolve regen keys of %s: %w", kind, err)
	}

	return a, b, nil
}

// ListExpiredTrash implements TrashStore.
func (p *PostgresDB) ListExpiredTrash(ctx context.Context, limit int) ([]models.TrashRef, error) {
	refs := make([]models.TrashRef, 0, limit)

	collect := func(kind models.TrashKind, query string) error {
		rows, err := p.db.QueryContext(ctx, query, p.trashDefault(), limit-len(refs))
		if err != nil {
			return fmt.Errorf("list expired %s: %w", kind, err)
		}

		defer func() { _ = rows.Close() }()

		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return fmt.Errorf("scan expired %s: %w", kind, err)
			}

			refs = append(refs, models.TrashRef{Kind: kind, ID: id})
		}

		return rows.Err() //nolint:wrapcheck // wrapped by the caller's context
	}

	if err := collect(models.TrashKindRepository,
		`SELECT r.id::text FROM repositories r
		 WHERE r.deleted_at IS NOT NULL
		   AND r.deleted_at + make_interval(days => COALESCE(r.trash_retention_days, $1::int)) <= now()
		 ORDER BY r.deleted_at LIMIT $2`); err != nil {
		return nil, err
	}

	for _, s := range trashSpecs {
		if len(refs) >= limit {
			break
		}

		if err := collect(s.kind,
			`SELECT t.id::text FROM `+s.table+` t JOIN repositories r ON r.id = `+s.repoID+`
			 WHERE t.deleted_at IS NOT NULL AND r.deleted_at IS NULL
			   AND t.deleted_at + make_interval(days => COALESCE(r.trash_retention_days, $1::int)) <= now()
			 ORDER BY t.deleted_at LIMIT $2`); err != nil {
			return nil, err
		}
	}

	return refs, nil
}
