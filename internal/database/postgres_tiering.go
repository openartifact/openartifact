// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/openartifact/openartifact/internal/database/dbsqlc"
	"github.com/openartifact/openartifact/internal/database/models"
)

// backendIDString converts a blobs.storage_backend_id (NULL = primary store)
// to the "" = primary convention used outside this file.
func backendIDString(id *string) string {
	if id == nil {
		return ""
	}

	return *id
}

// nullableBackendID is the inverse of backendIDString.
func nullableBackendID(id string) *string {
	if id == "" {
		return nil
	}

	return &id
}

func nullableString(s string) *string {
	if s == "" {
		return nil
	}

	return &s
}

func tierFromRow(id, name string, backendID *string, rank int32, promote bool, createdAt, updatedAt time.Time, backendName, backendKind, backendStatus string) *models.StorageTier {
	return &models.StorageTier{
		ID: id, Name: name, BackendID: backendIDString(backendID), Rank: int(rank), PromoteOnRead: promote,
		BackendName: backendName, BackendKind: backendKind, BackendStatus: backendStatus,
		CreatedAt: createdAt, UpdatedAt: updatedAt,
	}
}

// ListStorageTiers lists every tier, hottest rank first.
func (p *PostgresDB) ListStorageTiers(ctx context.Context) ([]*models.StorageTier, error) {
	rows, err := p.q.ListStorageTiers(ctx)
	if err != nil {
		return nil, fmt.Errorf("list storage tiers: %w", err)
	}

	out := make([]*models.StorageTier, 0, len(rows))
	for _, r := range rows {
		out = append(out, tierFromRow(r.ID, r.Name, r.BackendID, r.Rank, r.PromoteOnRead, r.CreatedAt, r.UpdatedAt, r.BackendName, r.BackendKind, r.BackendStatus))
	}

	return out, nil
}

// GetStorageTierByName returns the named tier, or nil when there is none.
func (p *PostgresDB) GetStorageTierByName(ctx context.Context, name string) (*models.StorageTier, error) {
	r, err := p.q.GetStorageTierByName(ctx, name)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("get storage tier: %w", err)
	}

	return tierFromRow(r.ID, r.Name, r.BackendID, r.Rank, r.PromoteOnRead, r.CreatedAt, r.UpdatedAt, r.BackendName, r.BackendKind, r.BackendStatus), nil
}

// CreateStorageTier inserts a tier (t.ID must be set).
func (p *PostgresDB) CreateStorageTier(ctx context.Context, t *models.StorageTier) error {
	return p.q.CreateStorageTier(ctx, dbsqlc.CreateStorageTierParams{
		ID: t.ID, Name: t.Name, BackendID: nullableBackendID(t.BackendID), Rank: int32(t.Rank), PromoteOnRead: t.PromoteOnRead, //nolint:gosec // rank is validated by the API
	})
}

// UpdateStorageTier updates a tier's name, rank and promote-on-read flag.
func (p *PostgresDB) UpdateStorageTier(ctx context.Context, t *models.StorageTier) error {
	return p.q.UpdateStorageTier(ctx, dbsqlc.UpdateStorageTierParams{
		ID: t.ID, Name: t.Name, Rank: int32(t.Rank), PromoteOnRead: t.PromoteOnRead, //nolint:gosec // rank is validated by the API
	})
}

// DeleteStorageTierIfUnused deletes a non-primary tier unless something still
// depends on it, atomically: the tier and its backend row are locked FOR UPDATE
// for the whole check-and-delete, so a concurrent move (whose journal and
// blobs.storage_backend_id writes take a key-share lock on the backend row)
// either commits before the check and is seen, or waits until the delete is
// done. It returns "" when the tier was deleted (or was already gone) and the
// human-readable reason otherwise.
func (p *PostgresDB) DeleteStorageTierIfUnused(ctx context.Context, tierID string) (string, error) {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("begin delete storage tier tx: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	var backendID sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT backend_id FROM storage_tiers WHERE id = $1 FOR UPDATE`, tierID).Scan(&backendID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}

		return "", fmt.Errorf("lock storage tier: %w", err)
	}

	if !backendID.Valid {
		return "the primary storage tier cannot be deleted", nil
	}

	if _, err := tx.ExecContext(ctx, `SELECT 1 FROM storage_backends WHERE id = $1 FOR UPDATE`, backendID.String); err != nil {
		return "", fmt.Errorf("lock storage backend: %w", err)
	}

	var policies bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM storage_policies WHERE target_tier_id = $1 OR from_tier_id = $1)`, tierID).Scan(&policies); err != nil {
		return "", fmt.Errorf("check storage tier policies: %w", err)
	}

	if policies {
		return "storage tier is still referenced by a policy", nil
	}

	if reason, err := storageBackendBusy(ctx, tx, backendID.String, false); err != nil || reason != "" {
		return reason, err
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM storage_tiers WHERE id = $1 AND backend_id IS NOT NULL`, tierID); err != nil {
		return "", fmt.Errorf("delete storage tier: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit delete storage tier: %w", err)
	}

	return "", nil
}

// StorageBackendBusyReason reports why a backend's kind or config must not
// change (or the backend not be deleted): it holds blobs, is served by a tier
// or is named by a move journal row. "" means it is idle.
func (p *PostgresDB) StorageBackendBusyReason(ctx context.Context, backendID string) (string, error) {
	return storageBackendBusy(ctx, p.db, backendID, true)
}

func storageBackendBusy(ctx context.Context, q rowQuerier, backendID string, includeTier bool) (string, error) {
	var blobs, moves, tier bool

	err := q.QueryRowContext(ctx, `SELECT
		EXISTS(SELECT 1 FROM blobs WHERE storage_backend_id = $1),
		EXISTS(SELECT 1 FROM storage_tier_moves WHERE from_backend_id = $1 OR to_backend_id = $1),
		$2::boolean AND EXISTS(SELECT 1 FROM storage_tiers WHERE backend_id = $1)`, backendID, includeTier).Scan(&blobs, &moves, &tier)
	if err != nil {
		return "", fmt.Errorf("check storage backend usage: %w", err)
	}

	switch {
	case blobs:
		return "storage backend still holds blobs; move them to another tier first", nil
	case moves:
		return "storage backend is named by a tier move (in progress or journaled)", nil
	case tier:
		return "storage backend is used by a storage tier", nil
	}

	return "", nil
}

func policyFromRow(r dbsqlc.StoragePolicy) *models.StoragePolicy {
	return &models.StoragePolicy{
		ID: r.ID, Name: r.Name, Enabled: r.Enabled, Position: int(r.Position), TargetTierID: r.TargetTierID,
		FromTierID: backendIDString(r.FromTierID), MinAgeSeconds: r.MinAgeSeconds, MinIdleSeconds: r.MinIdleSeconds,
		MinSizeBytes: r.MinSizeBytes, MaxSizeBytes: r.MaxSizeBytes, RepoGlob: backendIDString(r.RepoGlob),
		Formats: slices.Clone(r.Formats), PathGlob: backendIDString(r.PathGlob), CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}

// ListStoragePolicies lists every policy in evaluation order.
func (p *PostgresDB) ListStoragePolicies(ctx context.Context) ([]*models.StoragePolicy, error) {
	rows, err := p.q.ListStoragePolicies(ctx)
	if err != nil {
		return nil, fmt.Errorf("list storage policies: %w", err)
	}

	out := make([]*models.StoragePolicy, 0, len(rows))
	for _, r := range rows {
		out = append(out, policyFromRow(r))
	}

	return out, nil
}

// GetStoragePolicy returns a policy by id, or nil when there is none.
func (p *PostgresDB) GetStoragePolicy(ctx context.Context, id string) (*models.StoragePolicy, error) {
	r, err := p.q.GetStoragePolicy(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("get storage policy: %w", err)
	}

	return policyFromRow(r), nil
}

// CreateStoragePolicy inserts a policy (pol.ID must be set).
func (p *PostgresDB) CreateStoragePolicy(ctx context.Context, pol *models.StoragePolicy) error {
	return p.q.CreateStoragePolicy(ctx, dbsqlc.CreateStoragePolicyParams{
		ID: pol.ID, Name: pol.Name, Enabled: pol.Enabled, Position: int32(pol.Position), //nolint:gosec // validated by the API
		TargetTierID: pol.TargetTierID, Formats: nonNilStrings(pol.Formats), FromTierID: nullableString(pol.FromTierID),
		MinAgeSeconds: pol.MinAgeSeconds, MinIdleSeconds: pol.MinIdleSeconds,
		MinSizeBytes: pol.MinSizeBytes, MaxSizeBytes: pol.MaxSizeBytes,
		RepoGlob: nullableString(pol.RepoGlob), PathGlob: nullableString(pol.PathGlob),
	})
}

// UpdateStoragePolicy overwrites every field of the policy.
func (p *PostgresDB) UpdateStoragePolicy(ctx context.Context, pol *models.StoragePolicy) error {
	return p.q.UpdateStoragePolicy(ctx, dbsqlc.UpdateStoragePolicyParams{
		ID: pol.ID, Name: pol.Name, Enabled: pol.Enabled, Position: int32(pol.Position), //nolint:gosec // validated by the API
		TargetTierID: pol.TargetTierID, Formats: nonNilStrings(pol.Formats), FromTierID: nullableString(pol.FromTierID),
		MinAgeSeconds: pol.MinAgeSeconds, MinIdleSeconds: pol.MinIdleSeconds,
		MinSizeBytes: pol.MinSizeBytes, MaxSizeBytes: pol.MaxSizeBytes,
		RepoGlob: nullableString(pol.RepoGlob), PathGlob: nullableString(pol.PathGlob),
	})
}

// DeleteStoragePolicy deletes a policy; it reports whether a row was removed.
func (p *PostgresDB) DeleteStoragePolicy(ctx context.Context, id string) (bool, error) {
	n, err := p.q.DeleteStoragePolicy(ctx, id)
	if err != nil {
		return false, fmt.Errorf("delete storage policy: %w", err)
	}

	return n > 0, nil
}

// StorageTierUsage returns blob counts and bytes per placement ("" = primary store).
func (p *PostgresDB) StorageTierUsage(ctx context.Context) ([]models.TierUsage, error) {
	rows, err := p.q.StorageTierUsage(ctx)
	if err != nil {
		return nil, fmt.Errorf("storage tier usage: %w", err)
	}

	out := make([]models.TierUsage, 0, len(rows))
	for _, r := range rows {
		out = append(out, models.TierUsage{BackendID: backendIDString(r.StorageBackendID), Blobs: r.BlobCount, Bytes: r.TotalBytes})
	}

	return out, nil
}

// GetBlobPlacement returns where the blob lives ("" = primary store) and its
// size. found is false when there is no blobs row.
func (p *PostgresDB) GetBlobPlacement(ctx context.Context, sha256 string) (string, int64, bool, error) {
	row, err := p.q.GetBlobPlacement(ctx, sha256)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, false, nil
	}

	if err != nil {
		return "", 0, false, fmt.Errorf("get blob placement: %w", err)
	}

	return backendIDString(row.StorageBackendID), row.SizeBytes, true, nil
}

func moveFromRow(r dbsqlc.StorageTierMove) *models.StorageTierMove {
	m := &models.StorageTierMove{
		SHA256: r.Sha256, FromBackendID: backendIDString(r.FromBackendID), ToBackendID: backendIDString(r.ToBackendID),
		State: r.State, Reason: r.Reason, SizeBytes: r.SizeBytes, Attempts: int(r.Attempts), CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
	if r.LastError != nil {
		m.LastError = *r.LastError
	}

	return m
}

// BeginStorageTierMove journals the start of a move. It returns the journal
// row that is now authoritative for the blob and whether this call created it;
// an existing row (a move in flight or one awaiting cleanup) is returned as is.
func (p *PostgresDB) BeginStorageTierMove(ctx context.Context, m *models.StorageTierMove) (*models.StorageTierMove, bool, error) {
	n, err := p.q.BeginStorageTierMove(ctx, dbsqlc.BeginStorageTierMoveParams{
		Sha256: m.SHA256, Reason: m.Reason, SizeBytes: m.SizeBytes,
		FromBackendID: nullableBackendID(m.FromBackendID), ToBackendID: nullableBackendID(m.ToBackendID),
	})
	if err != nil {
		return nil, false, fmt.Errorf("begin storage tier move: %w", err)
	}

	row, err := p.q.GetStorageTierMove(ctx, m.SHA256)
	if err != nil {
		return nil, false, fmt.Errorf("read storage tier move: %w", err)
	}

	return moveFromRow(row), n > 0, nil
}

// ListRecoverableStorageTierMoves returns journal rows that still have attempts
// left, or that last failed more than retryAfter ago.
func (p *PostgresDB) ListRecoverableStorageTierMoves(ctx context.Context, maxAttempts int, retryAfter time.Duration, limit int) ([]*models.StorageTierMove, error) {
	rows, err := p.q.ListRecoverableStorageTierMoves(ctx, dbsqlc.ListRecoverableStorageTierMovesParams{
		Attempts: int32(maxAttempts), Column2: retryAfter.Seconds(), Limit: int32(limit), //nolint:gosec // small bounded config values
	})
	if err != nil {
		return nil, fmt.Errorf("list recoverable storage tier moves: %w", err)
	}

	return movesFromRows(rows), nil
}

// ListStorageTierMoves returns the most recently touched journal rows.
func (p *PostgresDB) ListStorageTierMoves(ctx context.Context, limit int) ([]*models.StorageTierMove, error) {
	rows, err := p.q.ListStorageTierMoves(ctx, int32(limit)) //nolint:gosec // small bounded value
	if err != nil {
		return nil, fmt.Errorf("list storage tier moves: %w", err)
	}

	return movesFromRows(rows), nil
}

func movesFromRows(rows []dbsqlc.StorageTierMove) []*models.StorageTierMove {
	out := make([]*models.StorageTierMove, 0, len(rows))
	for _, r := range rows {
		out = append(out, moveFromRow(r))
	}

	return out
}

// CountStorageTierMoves counts the journal rows (moves in flight or awaiting cleanup).
func (p *PostgresDB) CountStorageTierMoves(ctx context.Context) (int64, error) {
	return p.q.CountStorageTierMoves(ctx)
}

// FailStorageTierMove records a failed attempt on the journal row.
func (p *PostgresDB) FailStorageTierMove(ctx context.Context, sha256, message string) error {
	if len(message) > 500 {
		message = message[:500]
	}

	return p.q.FailStorageTierMove(ctx, dbsqlc.FailStorageTierMoveParams{Sha256: sha256, LastError: &message})
}

// FinishStorageTierMove removes the journal row once the move is complete.
func (p *PostgresDB) FinishStorageTierMove(ctx context.Context, sha256 string) error {
	return p.q.FinishStorageTierMove(ctx, sha256)
}

// FlipBlobPlacement atomically repoints blobs.storage_backend_id from `from` to
// `to` and marks the journal row flipped, in ONE transaction under the per-blob
// advisory lock that SafeDeleteBlob and uploaders take (WithBlobLock). It is the
// only place a placement changes: either the whole flip commits or nothing did,
// so a crash leaves exactly one authoritative placement.
//
// verify runs once the blob lock is held and before the placement changes; a
// non-nil error aborts the flip. The mover uses it to prove the target copy
// still exists: every path that removes a copy (uploaders settling a race,
// SafeDeleteBlob) does so under the same lock, so a copy confirmed here cannot
// vanish before the flip commits.
func (p *PostgresDB) FlipBlobPlacement(ctx context.Context, sha256, from, to string, verify func(ctx context.Context) error) (models.FlipOutcome, error) {
	release, err := p.acquireLockSlot(ctx)
	if err != nil {
		return 0, err
	}
	defer release()

	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin placement flip tx: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('blob:' || $1, 0))`, sha256); err != nil {
		return 0, fmt.Errorf("acquire blob lock: %w", err)
	}

	if verify != nil {
		if err := verify(ctx); err != nil {
			return 0, err
		}
	}

	res, err := tx.ExecContext(ctx,
		`UPDATE blobs SET storage_backend_id = $2::uuid, tier_changed_at = now()
		 WHERE sha256 = $1 AND storage_backend_id IS NOT DISTINCT FROM $3::uuid`,
		sha256, nullableBackendID(to), nullableBackendID(from))
	if err != nil {
		return 0, fmt.Errorf("flip blob placement: %w", err)
	}

	if n, _ := res.RowsAffected(); n == 0 {
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM blobs WHERE sha256 = $1)`, sha256).Scan(&exists); err != nil {
			return 0, fmt.Errorf("check blob row: %w", err)
		}

		if !exists {
			return models.FlipBlobGone, nil
		}

		return models.FlipPlacementChanged, nil
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE storage_tier_moves SET state = $2, updated_at = now() WHERE sha256 = $1`,
		sha256, models.TierMoveFlipped); err != nil {
		return 0, fmt.Errorf("mark move flipped: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit placement flip: %w", err)
	}

	return models.FlipDone, nil
}

// TouchBlobAccess raises blobs.last_accessed_at for the given blobs in one
// statement. A blob whose recorded access is already within resolution of the
// new time is left alone, so repeated downloads of a hot blob cost no write. The
// exception is a blob whose recorded access is not newer than its last tier
// change: its first read on the new tier is always written, or promote-on-read
// would miss it for up to resolution after a demotion.
//
// Rows are locked in sha256 order (FOR UPDATE under ORDER BY) so two replicas
// flushing overlapping sets cannot deadlock each other. Every write here is a
// non-HOT update (last_accessed_at is in the promotion index predicate), which
// the access resolution bounds to one per blob per resolution window.
func (p *PostgresDB) TouchBlobAccess(ctx context.Context, accessed map[string]time.Time, resolution time.Duration) (int64, error) {
	if len(accessed) == 0 {
		return 0, nil
	}

	shas := make([]string, 0, len(accessed))
	for sha := range accessed {
		shas = append(shas, sha)
	}

	slices.Sort(shas)

	times := make([]time.Time, len(shas))
	for i, sha := range shas {
		times[i] = accessed[sha]
	}

	res, err := p.db.ExecContext(ctx,
		`WITH due AS (
		     SELECT b.sha256, v.at
		     FROM blobs b
		     JOIN unnest($1::text[], $2::timestamptz[]) AS v(sha, at) ON b.sha256 = v.sha
		     WHERE b.last_accessed_at IS NULL
		        OR b.last_accessed_at < v.at - make_interval(secs => $3::double precision)
		        OR (b.tier_changed_at IS NOT NULL AND b.last_accessed_at <= b.tier_changed_at AND v.at > b.tier_changed_at)
		     ORDER BY b.sha256
		     FOR UPDATE OF b
		 )
		 UPDATE blobs b SET last_accessed_at = due.at
		 FROM due WHERE b.sha256 = due.sha256`,
		shas, times, resolution.Seconds())
	if err != nil {
		return 0, fmt.Errorf("touch blob access: %w", err)
	}

	n, _ := res.RowsAffected()

	return n, nil
}

// tierScanWindowUpper returns the sha256 that closes a window of at most
// `window` blobs after the cursor, and valid=false when the table ends first.
func (p *PostgresDB) tierScanWindowUpper(ctx context.Context, after string, window int) (string, bool, error) {
	var s sql.NullString

	err := p.db.QueryRowContext(ctx,
		`SELECT sha256 FROM blobs WHERE sha256 > $1 ORDER BY sha256 OFFSET $2 LIMIT 1`, after, window-1).Scan(&s)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}

	if err != nil {
		return "", false, fmt.Errorf("find tier scan window end: %w", err)
	}

	return s.String, s.Valid, nil
}

// ScanTierCandidates examines one window of at most `window` blobs rows after
// the cursor and returns those matching any clause (and not already being
// moved). next is the cursor for the following call, "" once the end of the
// table was reached; scanned is how many rows the window covered.
func (p *PostgresDB) ScanTierCandidates(ctx context.Context, after string, window int, clauses []models.TierScanClause) ([]models.BlobFacts, string, int, error) {
	upper, bounded, err := p.tierScanWindowUpper(ctx, after, window)
	if err != nil {
		return nil, "", 0, err
	}

	var (
		next    string
		scanned int
	)

	if bounded {
		scanned = window
		next = upper
	} else if err := p.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM blobs WHERE sha256 > $1`, after).Scan(&scanned); err != nil {
		return nil, "", 0, fmt.Errorf("count tail blobs: %w", err)
	}

	if len(clauses) == 0 {
		return nil, next, scanned, nil
	}

	args := []any{after, nullableString(upper)}
	where := buildTierClauseSQL(clauses, &args)

	// where is assembled from fixed fragments with every value bound as a parameter.
	query := strings.Replace(tierScanQuery, "{clauses}", where, 1)

	rows, err := p.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, "", 0, fmt.Errorf("scan tier candidates: %w", err)
	}

	found, err := scanBlobFacts(rows)
	if err != nil {
		return nil, "", 0, err
	}

	return found, next, scanned, nil
}

// tierScanQuery is the candidate scan; {clauses} is replaced by the
// parameterized policy conditions.
const tierScanQuery = `SELECT b.sha256, b.size_bytes, b.created_at, b.last_accessed_at, b.storage_backend_id, b.tier_changed_at
	 FROM blobs b
	 WHERE b.sha256 > $1 AND ($2::text IS NULL OR b.sha256 <= $2::text)
	   AND NOT EXISTS (SELECT 1 FROM storage_tier_moves m WHERE m.sha256 = b.sha256)
	   AND ({clauses})
	 ORDER BY b.sha256`

func buildTierClauseSQL(clauses []models.TierScanClause, args *[]any) string {
	bind := func(v any) string {
		*args = append(*args, v)

		return fmt.Sprintf("$%d", len(*args))
	}

	parts := make([]string, 0, len(clauses))

	for _, c := range clauses {
		conds := []string{"b.storage_backend_id IS DISTINCT FROM " + bind(nullableBackendID(c.TargetBackendID)) + "::uuid"}

		if c.HasFrom {
			conds = append(conds, "b.storage_backend_id IS NOT DISTINCT FROM "+bind(nullableBackendID(c.FromBackendID))+"::uuid")
		}

		if c.CreatedBefore != nil {
			conds = append(conds, "b.created_at <= "+bind(*c.CreatedBefore)+"::timestamptz")
		}

		if c.IdleBefore != nil {
			conds = append(conds, "COALESCE(b.last_accessed_at, b.created_at) <= "+bind(*c.IdleBefore)+"::timestamptz")
		}

		if c.MinSizeBytes != nil {
			conds = append(conds, "b.size_bytes >= "+bind(*c.MinSizeBytes)+"::bigint")
		}

		if c.MaxSizeBytes != nil {
			conds = append(conds, "b.size_bytes <= "+bind(*c.MaxSizeBytes)+"::bigint")
		}

		parts = append(parts, "("+strings.Join(conds, " AND ")+")")
	}

	return strings.Join(parts, " OR ")
}

func scanBlobFacts(rows *sql.Rows) ([]models.BlobFacts, error) {
	defer func() { _ = rows.Close() }()

	var out []models.BlobFacts

	for rows.Next() {
		var (
			f       models.BlobFacts
			last    sql.NullTime
			changed sql.NullTime
			backend sql.NullString
		)

		if err := rows.Scan(&f.SHA256, &f.SizeBytes, &f.CreatedAt, &last, &backend, &changed); err != nil {
			return nil, fmt.Errorf("scan blob facts: %w", err)
		}

		if last.Valid {
			t := last.Time
			f.LastAccessedAt = &t
		}

		if changed.Valid {
			t := changed.Time
			f.TierChangedAt = &t
		}

		f.BackendID = backend.String
		out = append(out, f)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read blob facts: %w", err)
	}

	return out, nil
}

// promotionCandidatesQuery lists promote-on-read candidates. The first three
// conditions repeat the predicate of idx_blobs_tier_promote verbatim, so the
// planner can walk that small index in sha256 order.
const promotionCandidatesQuery = `SELECT b.sha256, b.size_bytes, b.created_at, b.last_accessed_at, b.storage_backend_id, b.tier_changed_at
	 FROM blobs b
	 WHERE b.storage_backend_id IS NOT NULL AND b.last_accessed_at IS NOT NULL
	   AND (b.tier_changed_at IS NULL OR b.last_accessed_at > b.tier_changed_at)
	   AND b.storage_backend_id = ANY($1::text[]::uuid[])
	   AND b.sha256 > $2
	   AND NOT EXISTS (SELECT 1 FROM storage_tier_moves m WHERE m.sha256 = b.sha256)
	 ORDER BY b.sha256
	 LIMIT $3`

// ListPromotionCandidates returns blobs placed on one of backendIDs that were
// read after they arrived there (last_accessed_at > tier_changed_at), ordered
// by sha256 after the cursor, skipping blobs with a move in flight.
func (p *PostgresDB) ListPromotionCandidates(ctx context.Context, backendIDs []string, after string, limit int) ([]models.BlobFacts, error) {
	if len(backendIDs) == 0 {
		return nil, nil
	}

	rows, err := p.db.QueryContext(ctx, promotionCandidatesQuery, backendIDs, after, limit)
	if err != nil {
		return nil, fmt.Errorf("list promotion candidates: %w", err)
	}

	return scanBlobFacts(rows)
}

// blobRefScopeSpec describes how one package table names the repository entry
// that references a blob: the SQL for the repository id, the blob join
// condition and the artifact path policies match with path_glob.
type blobRefScopeSpec struct {
	from     string // FROM/JOIN clause giving alias t (and aliases it needs)
	repoID   string
	blobCond string // condition on blobs alias b
	path     string
}

// blobRefScopeSpecs lists, per package table, where a blob's repository, format
// and path come from. It parallels blobRefSpecs (which only counts references);
// a table missing here still blocks deletion but is invisible to repo/format/path
// conditions of tiering policies.
var blobRefScopeSpecs = []blobRefScopeSpec{ //nolint:gochecknoglobals // built once, read-only
	{"rpm_packages t", "t.repository_id", "t.blob_id = b.id", "t.filename"},
	{"deb_packages t", "t.repository_id", "t.blob_id = b.id", "t.filename"},
	{"npm_versions t JOIN npm_packages np ON np.id = t.package_id", "np.repository_id", "t.blob_id = b.id", "np.full_name || '/' || t.tarball_filename"},
	{"maven_artifacts t", "t.repository_id", "t.blob_id = b.id", "t.repo_path"},
	{"docker_blobs t", "t.repository_id", "t.blob_id = b.id", "t.digest"},
	{"docker_manifests t", "t.repository_id", "t.blob_id = b.id", "t.digest"},
	{"pypi_packages t", "t.repository_id", "t.blob_id = b.id", "t.filename"},
	{"nuget_packages t", "t.repository_id", "t.blob_id = b.id", "t.package_id || '/' || t.version"},
	{"gomod_modules t", "t.repository_id", "t.zip_blob_id = b.id", "t.module_path || '/@v/' || t.version || '.zip'"},
	{"gomod_modules t", "t.repository_id", "t.mod_blob_id = b.id", "t.module_path || '/@v/' || t.version || '.mod'"},
	{"cargo_crates t", "t.repository_id", "t.blob_id = b.id", "t.name || '/' || t.version"},
	{"rubygems_gems t", "t.repository_id", "t.blob_id = b.id", "t.name || '-' || t.version"},
	{"conda_packages t", "t.repository_id", "t.blob_id = b.id", "t.platform || '/' || t.name || '-' || t.version || '-' || t.build_string"},
	{"conan_packages t", "t.repository_id", "t.blob_id = b.id", "t.name || '/' || t.version"},
	{"ivy_artifacts t", "t.repository_id", "t.blob_id = b.id", "t.organisation || '/' || t.module || '/' || t.revision || '/' || t.artifact_name"},
	{"sbt_artifacts t", "t.repository_id", "t.blob_id = b.id", "t.group_id || '/' || t.artifact_id || '/' || t.version || '/' || t.filename"},
	{"helm_charts t", "t.repository_id", "t.blob_id = b.id", "t.name || '-' || t.version"},
	{"raw_artifacts t", "t.repository_id", "t.blob_id = b.id", "t.path"},
	{"proxy_cache_entries t", "t.repository_id", "t.blob_key = b.sha256", "t.cache_key"},
}

var blobRefsSQL = buildBlobRefsSQL() //nolint:gochecknoglobals // built once, read-only

func buildBlobRefsSQL() string {
	parts := make([]string, len(blobRefScopeSpecs))
	for i, s := range blobRefScopeSpecs {
		parts[i] = fmt.Sprintf(
			`SELECT b.sha256, r.name, r.format, %s::text FROM blobs b JOIN %s ON %s JOIN repositories r ON r.id = %s WHERE b.sha256 = ANY($1::text[])`,
			s.path, s.from, s.blobCond, s.repoID)
	}

	return strings.Join(parts, "\nUNION ALL\n")
}

// ListBlobRefs returns, per blob, the repository entries referencing it
// (repository name, format and artifact path).
func (p *PostgresDB) ListBlobRefs(ctx context.Context, shas []string) (map[string][]models.BlobRef, error) {
	out := make(map[string][]models.BlobRef, len(shas))
	if len(shas) == 0 {
		return out, nil
	}

	rows, err := p.db.QueryContext(ctx, blobRefsSQL, shas)
	if err != nil {
		return nil, fmt.Errorf("list blob refs: %w", err)
	}

	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var (
			sha string
			ref models.BlobRef
		)

		if err := rows.Scan(&sha, &ref.Repo, &ref.Format, &ref.Path); err != nil {
			return nil, fmt.Errorf("scan blob ref: %w", err)
		}

		out[sha] = append(out[sha], ref)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read blob refs: %w", err)
	}

	return out, nil
}
