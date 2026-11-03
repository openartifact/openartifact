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
)

var _ RepoStatsReconciler = (*PostgresDB)(nil)

var errRepoStatsBatch = errors.New("repo stats batch must be positive")

// RepoStatsReconciler recomputes the stored per-repository figures of the
// repository list. It is implemented by *PostgresDB and found by the repo stats
// worker with a type assertion on Database.
type RepoStatsReconciler interface {
	// ReconcileRepoStats recomputes the figures of up to batch repositories
	// (marked ones first, then the ones whose figures are missing or older than
	// maxAge) and returns how many; a result below batch means nothing more is
	// waiting.
	ReconcileRepoStats(ctx context.Context, batch int, maxAge time.Duration) (int, error)
}

// repoStatsFigures are the figures of one repository that repo_stats stores.
type repoStatsFigures struct {
	packages      int64
	lastPublished *time.Time
	security      dbsqlc.RepoSecurityCountsRow
}

// ReconcileRepoStats recomputes the repo_stats rows of up to batch repositories
// and returns how many it recomputed; a result below batch means nothing more
// is waiting. The repositories are the ones a write marked in repo_stats_dirty
// (the longest waiting first), then, with room left, the ones whose row is
// missing or was last computed more than maxAge ago, which repairs drift no mark
// announced. A repository deleted since is skipped.
//
// The marks are claimed in the transaction that stores the figures, so a failed
// pass leaves them for the next one, and the figures are read after the claim: a
// write that commits later leaves a new mark that the next pass takes. Callers
// must not run it concurrently for the same database (the worker holds a cluster
// lock); two passes would only recompute the same rows.
func (p *CatalogStore) ReconcileRepoStats(ctx context.Context, batch int, maxAge time.Duration) (int, error) {
	if batch < 1 {
		return 0, errRepoStatsBatch
	}

	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin repo stats pass: %w", err)
	}

	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	q := p.q.WithTx(tx)

	ids, err := q.ClaimRepoStatsMarks(ctx, int32(batch)) //nolint:gosec // batch is a small configured size
	if err != nil {
		return 0, fmt.Errorf("claim repo stats marks: %w", err)
	}

	if room := batch - len(ids); room > 0 {
		drifted, err := q.ListDriftedRepoStats(ctx, dbsqlc.ListDriftedRepoStatsParams{
			MaxAgeSeconds: maxAge.Seconds(),
			Batch:         int32(room), //nolint:gosec // room <= batch
		})
		if err != nil {
			return 0, fmt.Errorf("list drifted repo stats: %w", err)
		}

		ids = appendMissing(ids, drifted)
	}

	if len(ids) == 0 {
		return 0, nil
	}

	figures, err := p.computeRepoStats(ctx, ids)
	if err != nil {
		return 0, err
	}

	for _, id := range ids {
		f := figures[id]
		sec := f.security

		if err := q.UpsertRepoStats(ctx, dbsqlc.UpsertRepoStatsParams{
			RepositoryID:      id,
			PackageCount:      f.packages,
			LastPublishedAt:   f.lastPublished,
			VulnCritical:      sec.Critical,
			VulnHigh:          sec.High,
			VulnMedium:        sec.Medium,
			VulnLow:           sec.Low,
			Secrets:           sec.Secrets,
			LicenseViolations: sec.LicenseViolations,
			Quarantined:       sec.Quarantined,
			ScannedAt:         sec.ScannedAt,
		}); err != nil {
			return 0, fmt.Errorf("store repo stats: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit repo stats pass: %w", err)
	}

	return len(ids), nil
}

// computeRepoStats reads the figures of the repositories from the source tables,
// on the pool (not the claiming transaction), so every statement sees what was
// committed after the claim.
func (p *CatalogStore) computeRepoStats(ctx context.Context, ids []string) (map[string]repoStatsFigures, error) {
	figures := make(map[string]repoStatsFigures, len(ids))

	if err := p.queryPairs(ctx, packageCountQuery(), ids, func(rows pgx.Rows) error {
		var (
			id string
			n  int64
		)

		if err := rows.Scan(&id, &n); err != nil {
			return fmt.Errorf("scan package count: %w", err)
		}

		f := figures[id]
		f.packages = n
		figures[id] = f

		return nil
	}); err != nil {
		return nil, fmt.Errorf("count packages: %w", err)
	}

	if err := p.queryPairs(ctx, lastPublishedQuery(), ids, func(rows pgx.Rows) error {
		var (
			id string
			at *time.Time
		)

		if err := rows.Scan(&id, &at); err != nil {
			return fmt.Errorf("scan last published time: %w", err)
		}

		f := figures[id]
		f.lastPublished = at
		figures[id] = f

		return nil
	}); err != nil {
		return nil, fmt.Errorf("load last published times: %w", err)
	}

	security, err := p.q.RepoSecurityCounts(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("load repository security counts: %w", err)
	}

	for _, s := range security {
		f := figures[s.RepositoryID]
		f.security = s
		figures[s.RepositoryID] = f
	}

	return figures, nil
}

// queryPairs runs a statement taking the repository ids as $1 and hands every
// row to scan.
func (p *CatalogStore) queryPairs(ctx context.Context, query string, ids []string, scan func(pgx.Rows) error) error {
	rows, err := p.pool.Query(ctx, query, ids)
	if err != nil {
		return fmt.Errorf("query: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		if err := scan(rows); err != nil {
			return err
		}
	}

	return rows.Err()
}

// appendMissing appends the elements of extra that ids does not hold.
func appendMissing(ids, extra []string) []string {
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		seen[id] = struct{}{}
	}

	for _, id := range extra {
		if _, ok := seen[id]; !ok {
			ids = append(ids, id)
		}
	}

	return ids
}
