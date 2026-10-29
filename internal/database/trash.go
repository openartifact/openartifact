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
	"github.com/openartifact/openartifact/internal/format/storage"
)

// TrashStore owns the trash (soft-deleted repositories and packages), its retention and purge.
//
// Every package format except docker keeps its files in package_files, so one
// generic engine (trash_files.go) trashes, restores and purges them; what a
// format does differently is data in its storage.Trash descriptor. Docker keeps
// dedicated tables and is the one explicit special case (trash_docker.go).
type TrashStore struct{ *core }

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

// TrashOps is the storage side of soft delete (internal/trash, the trash API
// and the purge worker). PostgresDB implements it; handlers treat a database
// that does not (test fakes) as "soft delete unavailable" and hard-delete.
//
// Retention is resolved per repository: repositories.trash_retention_days, or
// the server default (SetTrashRetentionDays) when NULL. 0 means no trash.
//
// The reference id of a package item is package_files.id of its listed file; a
// docker item is a manifest or tag id and a repository item is the repository
// id.
type TrashOps interface {
	// TrashRows soft-deletes the given rows of repoID (stamps deleted_at and
	// deleted_by) and returns true. For a package format the ids are listed
	// package_files ids and kind only has to be a package kind (the repository's
	// format decides what is stamped); docker kinds address docker rows. It
	// returns false without touching anything when the repository's retention is
	// 0: the caller then hard-deletes as before. ErrTrashNotFound means none of
	// the rows is live in repoID.
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

	// RegenKeys returns what the metadata workers key a refresh by for an item,
	// as the format's storage.Trash.RegenKeys fragment reads it: (group_id,
	// artifact_id) for a Maven artifact, (package id, "") for an npm version and
	// ("", "") for every other kind (deb/rpm refresh per repository).
	RegenKeys(ctx context.Context, kind models.TrashKind, id string) (string, string, error)

	// ListExpiredTrash returns up to limit items whose retention window has
	// passed (items of repositories that are themselves in the trash are
	// left to the repository's own purge).
	ListExpiredTrash(ctx context.Context, limit int) ([]models.TrashRef, error)
}

var _ TrashOps = (*PostgresDB)(nil)

// SetTrashRetentionDays sets the server-wide default retention, in days, for
// repositories whose trash_retention_days is NULL.
func (p *TrashStore) SetTrashRetentionDays(days int) {
	if days < 0 {
		days = 0
	}

	p.trashDefaultDays.Store(int32(min(days, maxTrashRetentionDays)))
}

// maxTrashRetentionDays caps retention so interval arithmetic cannot overflow.
const maxTrashRetentionDays = 36500

func (p *TrashStore) trashDefault() int {
	return int(p.trashDefaultDays.Load())
}

// effectiveRetention is the repository's retention in days (0 = no trash).
func (p *TrashStore) effectiveRetention(ctx context.Context, q interface {
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

// TrashRows implements TrashOps.
func (p *TrashStore) TrashRows(ctx context.Context, repoID string, kind models.TrashKind, ids []string, deletedBy string) (bool, error) {
	if kind == "" || kind == models.TrashKindRepository {
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

	if spec, ok := dockerSpecByKind(kind); ok {
		err = trashDockerRows(ctx, tx, spec, repoID, ids, deletedBy)
	} else {
		err = trashFilesExec(ctx, tx, repoID, ids, deletedBy)
	}

	if err != nil {
		return false, err
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

// TrashRepository implements TrashOps.
func (p *TrashStore) TrashRepository(ctx context.Context, id, deletedBy string) (bool, error) {
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

// itemSelect builds the listing/lookup SELECT of one docker spec. extra is
// appended to the WHERE clause (it starts with " AND ").
func (s *dockerSpec) itemSelect(extra string) string {
	return fmt.Sprintf(
		`SELECT '%s' AS kind, t.id::text AS id, r.id::text AS repo_id, r.name AS repo_name, '%s' AS format,
		        (%s)::text AS name, (%s)::text AS version, %s AS size_bytes,
		        t.deleted_at AS deleted_at, COALESCE(t.deleted_by, '') AS deleted_by, `+purgeAtExpr+` AS purge_at
		 FROM %s t JOIN repositories r ON r.id = t.repository_id
		 WHERE t.deleted_at IS NOT NULL%s`,
		s.kind, dockerFormat, s.name, s.version, s.blobSizeExpr(), "t", s.table, extra,
	)
}

// filesItemSelect builds the listing/lookup SELECT of the package_files
// branch for the given (non-empty) formats. The kind column is empty: the
// scanner derives it from the format's descriptor. extra is appended to the
// WHERE clause (it starts with " AND ").
func filesItemSelect(formats []string, extra string) string {
	name := itemCase(formats, "p.name", func(t storage.Trash) string { return t.NameSQL() })
	version := itemCase(formats, "v.version", func(t storage.Trash) string { return t.VersionSQL() })

	own := `COALESCE((SELECT b.size_bytes FROM blobs b WHERE b.id = f.blob_id), 0)::bigint`
	size := own

	if vs := versionScopeFormats(); len(vs) > 0 {
		// A version-scoped item is the whole version: its size is that of every
		// file trashed with it.
		size = `CASE WHEN r.format IN (` + trashFormatsSQL(vs) + `)
			THEN (SELECT COALESCE(SUM(b.size_bytes), 0)::bigint FROM package_files sf
			      JOIN blobs b ON b.id = sf.blob_id
			      WHERE sf.version_id = f.version_id AND sf.deleted_at IS NOT DISTINCT FROM f.deleted_at)
			ELSE ` + own + ` END`
	}

	return fmt.Sprintf(
		`SELECT '' AS kind, f.id::text AS id, r.id::text AS repo_id, r.name AS repo_name, r.format AS format,
		        (%s)::text AS name, (%s)::text AS version, %s AS size_bytes,
		        f.deleted_at AS deleted_at, COALESCE(f.deleted_by, '') AS deleted_by, `+purgeAtExpr+` AS purge_at
		 FROM package_files f
		 JOIN package_versions v ON v.id = f.version_id
		 JOIN packages p ON p.id = v.package_id
		 JOIN repositories r ON r.id = f.repository_id
		 WHERE f.deleted_at IS NOT NULL AND f.listed AND r.format IN (%s)%s`,
		name, version, size, "f", trashFormatsSQL(formats), extra,
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
	if it.Kind == "" {
		it.Kind = trashKindOf(it.Format)
	}

	return &it, nil
}

// ListTrash implements TrashOps.
func (p *TrashStore) ListTrash(ctx context.Context, f models.TrashFilter) ([]*models.TrashItem, int, error) {
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

	extra := " AND r.deleted_at IS NULL" + repoFilter

	if formats := genericFormats(f.Kind); len(formats) > 0 && f.Kind != models.TrashKindRepository {
		branches = append(branches, filesItemSelect(formats, extra))
	}

	for _, s := range dockerSpecs {
		if f.Kind != "" && f.Kind != s.kind {
			continue
		}

		e := extra
		if s.listWhere != "" {
			e += " AND " + s.listWhere
		}

		branches = append(branches, s.itemSelect(e))
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

	rows, err := p.db.QueryContext(
		ctx,
		fmt.Sprintf(
			`SELECT kind, id, repo_id, repo_name, format, name, version, size_bytes, deleted_at, deleted_by, purge_at FROM (%s) u
				ORDER BY deleted_at DESC, id LIMIT $%d OFFSET $%d`,
			union,
			len(args)-1,
			len(args),
		),
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
func (p *TrashStore) getTrashItem(
	ctx context.Context,
	q trashQuerier,
	kind models.TrashKind,
	id string,
	lock bool,
) (*models.TrashItem, error) {
	var query string

	if kind == models.TrashKindRepository {
		query = repoItemSelect(" AND r.id = $2::uuid")
		if lock {
			query += " FOR UPDATE OF r"
		}
	} else if spec, ok := dockerSpecByKind(kind); ok {
		query = spec.itemSelect(" AND t.id = $2::uuid")
		if lock {
			query += " FOR UPDATE OF t"
		}
	} else {
		formats := genericFormats(kind)
		if kind == "" || len(formats) == 0 {
			return nil, fmt.Errorf("%w: %s", ErrTrashUnknownKind, kind)
		}

		query = filesItemSelect(formats, " AND f.id = $2::uuid")
		if lock {
			query += " FOR UPDATE OF f"
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

// GetTrashItem implements TrashOps.
func (p *TrashStore) GetTrashItem(ctx context.Context, kind models.TrashKind, id string) (*models.TrashItem, error) {
	return p.getTrashItem(ctx, p.db, kind, id, false)
}

// TrashedRepositoryByName returns the repository in the trash that holds name
// (its name stays reserved until it is purged or restored), ErrTrashNotFound
// when no trashed repository has it. It is not part of TrashOps: only the
// repository-create conflict message needs it.
func (p *TrashStore) TrashedRepositoryByName(ctx context.Context, name string) (*models.TrashItem, error) {
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

// RestoreTrash implements TrashOps.
func (p *TrashStore) RestoreTrash(ctx context.Context, kind models.TrashKind, id string) (*models.TrashItem, error) {
	if kind == models.TrashKindRepository {
		return p.restoreRepository(ctx, id)
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

	if spec, ok := dockerSpecByKind(kind); ok {
		err = restoreDockerRow(ctx, tx, spec, id)
	} else {
		err = restoreFilesExec(ctx, tx, item)
	}

	if err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit restore transaction: %w", err)
	}

	return item, nil
}

func (p *TrashStore) restoreRepository(ctx context.Context, id string) (*models.TrashItem, error) {
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

	// One captured delta shifts every row identically, so the pairs that are
	// matched on an equal deleted_at (a version and its files, a docker manifest
	// and its tags) stay equal.
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
	shift := []string{
		`UPDATE package_files SET deleted_at = deleted_at + $2::interval
		 WHERE repository_id = $1::uuid AND deleted_at IS NOT NULL`,
		`UPDATE package_versions SET deleted_at = deleted_at + $2::interval
		 WHERE repository_id = $1::uuid AND deleted_at IS NOT NULL`,
	}
	for _, s := range dockerSpecs {
		//nolint:gosec // G202: the table is a constant of the docker spec; the id is a bound argument
		shift = append(shift, `UPDATE `+s.table+` SET deleted_at = deleted_at + $2::interval
			 WHERE repository_id = $1::uuid AND deleted_at IS NOT NULL`)
	}

	for _, stmt := range shift {
		if _, err := tx.ExecContext(ctx, stmt, id, delta); err != nil {
			return nil, fmt.Errorf("shift trashed items: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit restore transaction: %w", err)
	}

	return item, nil
}

// PurgeTrash implements TrashOps.
func (p *TrashStore) PurgeTrash(ctx context.Context, kind models.TrashKind, id string) (*models.TrashPurgeResult, error) {
	if kind == models.TrashKindRepository {
		return p.purgeRepository(ctx, id)
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

	res := &models.TrashPurgeResult{
		Kind: kind, ID: id, RepositoryID: item.RepositoryID, RepositoryName: item.RepositoryName,
		Format: item.Format, Name: item.Name, Version: item.Version, SizeBytes: item.SizeBytes,
	}

	if spec, ok := dockerSpecByKind(kind); ok {
		err = purgeDockerRow(ctx, tx, spec, item, res)
	} else {
		err = purgeFilesExec(ctx, tx, item, res)
	}

	if err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit purge transaction: %w", err)
	}

	return res, nil
}

func (p *TrashStore) purgeRepository(ctx context.Context, id string) (*models.TrashPurgeResult, error) {
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

// RegenKeys implements TrashOps.
func (p *TrashStore) RegenKeys(ctx context.Context, kind models.TrashKind, id string) (string, string, error) {
	formats := genericFormats(kind)
	if kind == "" || len(formats) == 0 {
		// Docker and unknown kinds have no keyed regeneration.
		return "", "", nil
	}

	desc, err := trashDescriptor(formats[0])
	if err != nil {
		return "", "", err
	}

	var a, b sql.NullString

	//nolint:gosec // G202: RegenKeysSQL is a constant of the format descriptor; the id is a bound argument
	err = p.db.QueryRowContext(ctx,
		`SELECT `+desc.RegenKeysSQL()+` FROM package_files f
		 JOIN package_versions v ON v.id = f.version_id
		 JOIN packages p ON p.id = v.package_id
		 WHERE f.id = $1::uuid`, id).Scan(&a, &b)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", ErrTrashNotFound
	}

	if err != nil {
		return "", "", fmt.Errorf("resolve regen keys of %s: %w", kind, err)
	}

	return a.String, b.String, nil
}

// ListExpiredTrash implements TrashOps.
func (p *TrashStore) ListExpiredTrash(ctx context.Context, limit int) ([]models.TrashRef, error) {
	refs := make([]models.TrashRef, 0, limit)

	collect := func(kindOf func(format string) models.TrashKind, query string) error {
		rows, err := p.db.QueryContext(ctx, query, p.trashDefault(), limit-len(refs))
		if err != nil {
			return fmt.Errorf("list expired trash: %w", err)
		}

		defer func() { _ = rows.Close() }()

		for rows.Next() {
			var id, format string
			if err := rows.Scan(&id, &format); err != nil {
				return fmt.Errorf("scan expired trash: %w", err)
			}

			refs = append(refs, models.TrashRef{Kind: kindOf(format), ID: id})
		}

		return rows.Err() //nolint:wrapcheck // wrapped by the caller's context
	}

	constKind := func(k models.TrashKind) func(string) models.TrashKind {
		return func(string) models.TrashKind { return k }
	}

	if err := collect(constKind(models.TrashKindRepository),
		`SELECT r.id::text, r.format FROM repositories r
		 WHERE r.deleted_at IS NOT NULL
		   AND r.deleted_at + make_interval(days => COALESCE(r.trash_retention_days, $1::int)) <= now()
		 ORDER BY r.deleted_at LIMIT $2`); err != nil {
		return nil, err
	}

	if len(refs) < limit {
		if formats := genericFormats(""); len(formats) > 0 {
			if err := collect(trashKindOf,
				`SELECT f.id::text, r.format FROM package_files f JOIN repositories r ON r.id = f.repository_id
				 WHERE f.deleted_at IS NOT NULL AND f.listed AND r.deleted_at IS NULL
				   AND r.format IN (`+trashFormatsSQL(formats)+`)
				   AND f.deleted_at + make_interval(days => COALESCE(r.trash_retention_days, $1::int)) <= now()
				 ORDER BY f.deleted_at LIMIT $2`); err != nil {
				return nil, err
			}
		}
	}

	for _, s := range dockerSpecs {
		if len(refs) >= limit {
			break
		}

		//nolint:gosec // G202: the table is a constant of the docker spec
		if err := collect(constKind(s.kind),
			`SELECT t.id::text, 'docker' FROM `+s.table+` t JOIN repositories r ON r.id = t.repository_id
			 WHERE t.deleted_at IS NOT NULL AND r.deleted_at IS NULL
			   AND t.deleted_at + make_interval(days => COALESCE(r.trash_retention_days, $1::int)) <= now()
			 ORDER BY t.deleted_at LIMIT $2`); err != nil {
			return nil, err
		}
	}

	return refs, nil
}
