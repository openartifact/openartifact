// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/openartifact/openartifact/internal/database/dbsqlc"
	"github.com/openartifact/openartifact/internal/database/models"
)

// VEXFeedStore owns the vendor CSAF VEX feeds and the statements imported from
// their documents (vex_feed_statements). Materializing those statements into
// vex_statements is VEXStore.ReplaceFeedVEXStatements.
type VEXFeedStore struct{ *core }

var (
	// ErrVEXFeedNotFound is returned when a VEX feed does not exist.
	ErrVEXFeedNotFound = errors.New("VEX feed not found")
	// ErrVEXFeedConflict is returned when another VEX feed has the name.
	ErrVEXFeedConflict = errors.New("VEX feed name already exists")
)

func vexFeedFromRow(f dbsqlc.VexFeed, statements, applied int32) *models.VEXFeed {
	return &models.VEXFeed{
		ID:               f.ID,
		Name:             f.Name,
		URL:              f.Url,
		Enabled:          f.Enabled,
		RequireSignature: f.RequireSignature,
		PGPKeys:          f.PgpKeys,
		IntervalHours:    int(f.IntervalHours),
		CursorAt:         f.CursorAt,
		LastAttemptAt:    f.LastAttemptAt,
		LastSuccessAt:    f.LastSuccessAt,
		LastError:        f.LastError,
		DocumentsTotal:   int(f.DocumentsTotal),
		RejectedTotal:    int(f.RejectedTotal),
		StatementCount:   int(statements),
		AppliedCount:     int(applied),
		CreatedAt:        f.CreatedAt,
		UpdatedAt:        f.UpdatedAt,
	}
}

// ListVEXFeeds lists the feeds by name, with their statement counts.
func (p *VEXFeedStore) ListVEXFeeds(ctx context.Context) ([]*models.VEXFeed, error) {
	rows, err := p.q.ListVEXFeeds(ctx)

	return mapRows(rows, err, "list VEX feeds", func(r dbsqlc.ListVEXFeedsRow) *models.VEXFeed {
		return vexFeedFromRow(r.VexFeed, r.StatementCount, r.AppliedCount)
	})
}

// GetVEXFeed reads one feed; ErrVEXFeedNotFound when absent.
func (p *VEXFeedStore) GetVEXFeed(ctx context.Context, id string) (*models.VEXFeed, error) {
	if !isUUID(id) {
		return nil, ErrVEXFeedNotFound
	}

	r, err := p.q.GetVEXFeed(ctx, id)
	if isNoRows(err) {
		return nil, ErrVEXFeedNotFound
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get VEX feed: %w", err)
	}

	return vexFeedFromRow(r.VexFeed, r.StatementCount, r.AppliedCount), nil
}

// CreateVEXFeed stores a feed and fills f with the stored row (ID, defaults,
// timestamps); ErrVEXFeedConflict when the name is taken.
func (p *VEXFeedStore) CreateVEXFeed(ctx context.Context, f *models.VEXFeed) error {
	row, err := p.q.CreateVEXFeed(ctx, dbsqlc.CreateVEXFeedParams{
		Name:             f.Name,
		Url:              f.URL,
		Enabled:          f.Enabled,
		RequireSignature: f.RequireSignature,
		PgpKeys:          f.PGPKeys,
		IntervalHours:    toInt32(f.IntervalHours),
	})
	if IsUniqueViolation(err) {
		return ErrVEXFeedConflict
	}

	if err != nil {
		return fmt.Errorf("failed to create VEX feed: %w", err)
	}

	*f = *vexFeedFromRow(row, 0, 0)

	return nil
}

// UpdateVEXFeed replaces the editable fields of feed f.ID (name, url, enabled,
// require_signature, pgp_keys, interval_hours) and fills f with the stored row;
// a new URL, pgp_keys or require_signature resets the cursor. ErrVEXFeedNotFound when absent,
// ErrVEXFeedConflict when another feed has the name.
func (p *VEXFeedStore) UpdateVEXFeed(ctx context.Context, f *models.VEXFeed) error {
	if !isUUID(f.ID) {
		return ErrVEXFeedNotFound
	}

	row, err := p.q.UpdateVEXFeed(ctx, dbsqlc.UpdateVEXFeedParams{
		ID:               f.ID,
		Name:             f.Name,
		Url:              f.URL,
		Enabled:          f.Enabled,
		RequireSignature: f.RequireSignature,
		PgpKeys:          f.PGPKeys,
		IntervalHours:    toInt32(f.IntervalHours),
	})

	switch {
	case isNoRows(err):
		return ErrVEXFeedNotFound
	case IsUniqueViolation(err):
		return ErrVEXFeedConflict
	case err != nil:
		return fmt.Errorf("failed to update VEX feed: %w", err)
	}

	*f = *vexFeedFromRow(row, toInt32(f.StatementCount), toInt32(f.AppliedCount))

	return nil
}

// DeleteVEXFeed deletes a feed with its imported and materialized statements;
// ErrVEXFeedNotFound when absent.
func (p *VEXFeedStore) DeleteVEXFeed(ctx context.Context, id string) error {
	if !isUUID(id) {
		return ErrVEXFeedNotFound
	}

	n, err := p.q.DeleteVEXFeed(ctx, id)
	if err != nil {
		return fmt.Errorf("failed to delete VEX feed: %w", err)
	}

	if n == 0 {
		return ErrVEXFeedNotFound
	}

	return nil
}

// ReplaceVEXFeedDocument replaces the stored statements of one document of a
// feed with stmts in one transaction (no stmts removes the document).
func (p *VEXFeedStore) ReplaceVEXFeedDocument(ctx context.Context, feedID, documentURL string, stmts []models.VEXFeedStatement) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin VEX document replace: %w", err)
	}

	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	q := p.q.WithTx(tx)

	if err := q.DeleteVEXFeedDocument(ctx, dbsqlc.DeleteVEXFeedDocumentParams{FeedID: feedID, DocumentUrl: documentURL}); err != nil {
		return fmt.Errorf("delete VEX document statements: %w", err)
	}

	if len(stmts) > 0 {
		if err := q.InsertVEXFeedStatements(ctx, feedStatementParams(feedID, documentURL, stmts)); err != nil {
			return fmt.Errorf("insert VEX document statements: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit VEX document replace: %w", err)
	}

	return nil
}

func feedStatementParams(feedID, documentURL string, stmts []models.VEXFeedStatement) dbsqlc.InsertVEXFeedStatementsParams {
	n := len(stmts)
	a := dbsqlc.InsertVEXFeedStatementsParams{
		FeedID:           feedID,
		DocumentUrl:      documentURL,
		TrackingIds:      make([]string, n),
		VulnIds:          make([]string, n),
		Purls:            make([]string, n),
		Cpes:             make([]string, n),
		Statuses:         make([]string, n),
		Justifications:   make([]string, n),
		ImpactStatements: make([]string, n),
		ActionStatements: make([]string, n),
		ReleasedAts:      make([]string, n),
	}

	for i := range stmts {
		s := &stmts[i]
		a.TrackingIds[i], a.VulnIds[i], a.Purls[i], a.Cpes[i] = s.TrackingID, s.VulnID, s.PURL, s.CPE
		a.Statuses[i], a.Justifications[i] = s.Status, s.Justification
		a.ImpactStatements[i], a.ActionStatements[i] = s.ImpactStatement, s.ActionStatement

		if s.ReleasedAt != nil {
			a.ReleasedAts[i] = s.ReleasedAt.UTC().Format(time.RFC3339Nano)
		}
	}

	return a
}

// ListVEXFeedStatements lists the imported statements of a feed in import order.
func (p *VEXFeedStore) ListVEXFeedStatements(ctx context.Context, feedID string) ([]models.VEXFeedStatement, error) {
	rows, err := p.q.ListVEXFeedStatements(ctx, feedID)

	return mapRows(rows, err, "list VEX feed statements", func(r dbsqlc.ListVEXFeedStatementsRow) models.VEXFeedStatement {
		return models.VEXFeedStatement{
			DocumentURL:     r.DocumentUrl,
			TrackingID:      r.TrackingID,
			VulnID:          r.VulnID,
			PURL:            r.Purl,
			CPE:             r.Cpe,
			Status:          r.Status,
			Justification:   r.Justification,
			ImpactStatement: r.ImpactStatement,
			ActionStatement: r.ActionStatement,
			ReleasedAt:      r.ReleasedAt,
		}
	})
}

// RecordVEXFeedSync records the outcome of a sync: the attempt time and error
// always; on success also the success time, the new cursor (res.CursorAt nil
// keeps the current one) and the document counts. ErrVEXFeedNotFound when the
// feed is gone.
func (p *VEXFeedStore) RecordVEXFeedSync(ctx context.Context, feedID string, res models.VEXFeedSyncResult) error {
	if !isUUID(feedID) {
		return ErrVEXFeedNotFound
	}

	n, err := p.q.RecordVEXFeedSync(ctx, dbsqlc.RecordVEXFeedSyncParams{
		ID:        feedID,
		LastError: res.Error,
		Success:   res.Success,
		CursorAt:  res.CursorAt,
		Documents: toInt32(res.Documents),
		Rejected:  toInt32(res.Rejected),
	})
	if err != nil {
		return fmt.Errorf("failed to record VEX feed sync: %w", err)
	}

	if n == 0 {
		return ErrVEXFeedNotFound
	}

	return nil
}
