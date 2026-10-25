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
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
)

// PromotionPipeline methods

func (p *PostgresDB) CreatePromotionPipeline(ctx context.Context, pp *models.PromotionPipeline) error {
	stagesJSON, err := json.Marshal(pp.Stages)
	if err != nil {
		return fmt.Errorf("failed to marshal pipeline stages: %w", err)
	}

	now := time.Now()
	pp.CreatedAt = now
	pp.UpdatedAt = now

	return p.db.QueryRowContext(ctx,
		`INSERT INTO promotion_pipelines (name, description, stages, creator_id, created_at, updated_at)
		 VALUES ($1, $2, $3, (SELECT id FROM auth_users WHERE username = $4), $5, $6)
		 RETURNING id`,
		pp.Name, pp.Description, stagesJSON, pp.CreatedBy, pp.CreatedAt, pp.UpdatedAt,
	).Scan(&pp.ID)
}

func (p *PostgresDB) GetPromotionPipelineByName(ctx context.Context, name string) (*models.PromotionPipeline, error) {
	pp := &models.PromotionPipeline{}

	err := p.db.QueryRowContext(ctx,
		`SELECT pp.id, pp.name, COALESCE(pp.description,''), pp.stages, COALESCE(u.username,''), pp.created_at, pp.updated_at
		 FROM promotion_pipelines pp
		 LEFT JOIN auth_users u ON u.id = pp.creator_id
		 WHERE pp.name = $1`, name,
	).Scan(&pp.ID, &pp.Name, &pp.Description, &pp.StagesRaw, &pp.CreatedBy, &pp.CreatedAt, &pp.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get promotion pipeline by name: %w", err)
	}

	if err := json.Unmarshal(pp.StagesRaw, &pp.Stages); err != nil {
		return nil, fmt.Errorf("failed to unmarshal pipeline stages: %w", err)
	}

	return pp, nil
}

func (p *PostgresDB) GetPromotionPipelineByID(ctx context.Context, id string) (*models.PromotionPipeline, error) {
	pp := &models.PromotionPipeline{}

	err := p.db.QueryRowContext(ctx,
		`SELECT pp.id, pp.name, COALESCE(pp.description,''), pp.stages, COALESCE(u.username,''), pp.created_at, pp.updated_at
		 FROM promotion_pipelines pp
		 LEFT JOIN auth_users u ON u.id = pp.creator_id
		 WHERE pp.id = $1`, id,
	).Scan(&pp.ID, &pp.Name, &pp.Description, &pp.StagesRaw, &pp.CreatedBy, &pp.CreatedAt, &pp.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get promotion pipeline by id: %w", err)
	}

	if err := json.Unmarshal(pp.StagesRaw, &pp.Stages); err != nil {
		return nil, fmt.Errorf("failed to unmarshal pipeline stages: %w", err)
	}

	return pp, nil
}

func (p *PostgresDB) UpdatePromotionPipeline(ctx context.Context, pp *models.PromotionPipeline) error {
	stagesJSON, err := json.Marshal(pp.Stages)
	if err != nil {
		return fmt.Errorf("failed to marshal pipeline stages: %w", err)
	}

	_, err = p.db.ExecContext(ctx,
		`UPDATE promotion_pipelines SET name=$1, description=$2, stages=$3, updated_at=now()
		 WHERE id=$4`,
		pp.Name, pp.Description, stagesJSON, pp.ID,
	)

	return err
}

func (p *PostgresDB) DeletePromotionPipeline(ctx context.Context, id string) error {
	_, err := p.db.ExecContext(ctx, `DELETE FROM promotion_pipelines WHERE id=$1`, id)
	return err
}

func (p *PostgresDB) ListPromotionPipelines(ctx context.Context) ([]*models.PromotionPipeline, error) {
	rows, err := p.db.QueryContext(ctx,
		`SELECT pp.id, pp.name, COALESCE(pp.description,''), pp.stages, COALESCE(u.username,''), pp.created_at, pp.updated_at
		 FROM promotion_pipelines pp
		 LEFT JOIN auth_users u ON u.id = pp.creator_id
		 ORDER BY pp.name`,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list promotion pipelines: %w", err)
	}
	defer rows.Close()

	var result []*models.PromotionPipeline

	for rows.Next() {
		pp := &models.PromotionPipeline{}
		if err := rows.Scan(&pp.ID, &pp.Name, &pp.Description, &pp.StagesRaw, &pp.CreatedBy, &pp.CreatedAt, &pp.UpdatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan promotion pipeline: %w", err)
		}

		if err := json.Unmarshal(pp.StagesRaw, &pp.Stages); err != nil {
			return nil, fmt.Errorf("failed to unmarshal pipeline stages: %w", err)
		}

		result = append(result, pp)
	}

	return result, rows.Err()
}

// ReleaseBundle methods

func (p *PostgresDB) CreateReleaseBundle(ctx context.Context, b *models.ReleaseBundle) error {
	now := time.Now()
	b.CreatedAt = now

	b.UpdatedAt = now
	if b.State == "" {
		b.State = models.BundleStateDraft
	}

	return p.db.QueryRowContext(ctx,
		`INSERT INTO release_bundles (name, version, description, state, pipeline_id, current_stage, creator_id, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, (SELECT id FROM auth_users WHERE username = $7), $8, $9)
		 RETURNING id`,
		b.Name, b.Version, b.Description, b.State, b.PipelineID, b.CurrentStage, b.CreatedBy, b.CreatedAt, b.UpdatedAt,
	).Scan(&b.ID)
}

// releaseBundleSelect is shared by GetReleaseBundle, GetReleaseBundleByID and
// ListReleaseBundles: created_by/sealed_by/archived_by are UUID FKs
// (creator_id/sealer_id/archiver_id) resolved back to usernames
// via a join against auth_users so callers keep seeing plain username strings.
const releaseBundleSelect = `SELECT rb.id, rb.name, rb.version, COALESCE(rb.description,''), rb.state, rb.pipeline_id, rb.current_stage,
	        COALESCE(uc.username,''), rb.sealed_at, us.username, rb.archived_at, ua.username,
	        rb.gpg_key_id, rb.gpg_signature, rb.created_at, rb.updated_at
	 FROM release_bundles rb
	 LEFT JOIN auth_users uc ON uc.id = rb.creator_id
	 LEFT JOIN auth_users us ON us.id = rb.sealer_id
	 LEFT JOIN auth_users ua ON ua.id = rb.archiver_id`

func (p *PostgresDB) GetReleaseBundle(ctx context.Context, name, version string) (*models.ReleaseBundle, error) {
	b := &models.ReleaseBundle{}

	err := p.db.QueryRowContext(ctx,
		releaseBundleSelect+` WHERE rb.name=$1 AND rb.version=$2`, name, version,
	).Scan(&b.ID, &b.Name, &b.Version, &b.Description, &b.State, &b.PipelineID, &b.CurrentStage,
		&b.CreatedBy, &b.SealedAt, &b.SealedBy, &b.ArchivedAt, &b.ArchivedBy,
		&b.GPGKeyID, &b.GPGSignature, &b.CreatedAt, &b.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get release bundle: %w", err)
	}

	return b, nil
}

func (p *PostgresDB) GetReleaseBundleByID(ctx context.Context, id string) (*models.ReleaseBundle, error) {
	b := &models.ReleaseBundle{}

	err := p.db.QueryRowContext(ctx,
		releaseBundleSelect+` WHERE rb.id=$1`, id,
	).Scan(&b.ID, &b.Name, &b.Version, &b.Description, &b.State, &b.PipelineID, &b.CurrentStage,
		&b.CreatedBy, &b.SealedAt, &b.SealedBy, &b.ArchivedAt, &b.ArchivedBy,
		&b.GPGKeyID, &b.GPGSignature, &b.CreatedAt, &b.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get release bundle by id: %w", err)
	}

	return b, nil
}

func (p *PostgresDB) UpdateReleaseBundle(ctx context.Context, b *models.ReleaseBundle) error {
	_, err := p.db.ExecContext(ctx,
		`UPDATE release_bundles
		 SET description=$1, state=$2, pipeline_id=$3, current_stage=$4,
		     sealed_at=$5, sealer_id=(SELECT id FROM auth_users WHERE username = $6),
		     archived_at=$7, archiver_id=(SELECT id FROM auth_users WHERE username = $8),
		     gpg_key_id=$9, gpg_signature=$10, updated_at=now()
		 WHERE id=$11`,
		b.Description, b.State, b.PipelineID, b.CurrentStage,
		b.SealedAt, b.SealedBy, b.ArchivedAt, b.ArchivedBy,
		b.GPGKeyID, b.GPGSignature, b.ID,
	)

	return err
}

func (p *PostgresDB) DeleteReleaseBundle(ctx context.Context, id string) error {
	_, err := p.db.ExecContext(ctx, `DELETE FROM release_bundles WHERE id=$1`, id)
	return err
}

func (p *PostgresDB) ListReleaseBundles(ctx context.Context, nameFilter, stateFilter string, page, limit int) ([]*models.ReleaseBundle, int, error) {
	args := []any{}
	where := "WHERE 1=1"

	i := 1
	if nameFilter != "" {
		where += fmt.Sprintf(" AND name ILIKE $%d", i)

		args = append(args, "%"+escapeLike(nameFilter)+"%")
		i++
	}

	if stateFilter != "" {
		where += fmt.Sprintf(" AND state = $%d", i)

		args = append(args, stateFilter)
		i++
	}

	var total int
	if err := p.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM release_bundles "+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("failed to count release bundles: %w", err)
	}

	offset := (page - 1) * limit
	args = append(args, limit, offset)

	rows, err := p.db.QueryContext(ctx,
		releaseBundleSelect+` `+where+fmt.Sprintf(` ORDER BY rb.created_at DESC LIMIT $%d OFFSET $%d`, i, i+1),
		args...,
	)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to list release bundles: %w", err)
	}
	defer rows.Close()

	var result []*models.ReleaseBundle

	for rows.Next() {
		b := &models.ReleaseBundle{}
		if err := rows.Scan(&b.ID, &b.Name, &b.Version, &b.Description, &b.State, &b.PipelineID, &b.CurrentStage,
			&b.CreatedBy, &b.SealedAt, &b.SealedBy, &b.ArchivedAt, &b.ArchivedBy,
			&b.GPGKeyID, &b.GPGSignature, &b.CreatedAt, &b.UpdatedAt); err != nil {
			return nil, 0, fmt.Errorf("failed to scan release bundle: %w", err)
		}

		result = append(result, b)
	}

	return result, total, rows.Err()
}

// ReleaseBundleItem methods

// AddBundleItem inserts a bundle item. release_bundle_items has no
// component_id column, so item.ComponentID never round-trips through storage.
// The adder is stored as an adder_id UUID FK resolved from AddedBy (username) via a scalar subquery.
func (p *PostgresDB) AddBundleItem(ctx context.Context, item *models.ReleaseBundleItem) error {
	_, err := p.db.ExecContext(ctx,
		`WITH ref AS (
		     INSERT INTO artifact_refs (repository_id, artifact_path, format)
		     VALUES ($3, $5, $6)
		     ON CONFLICT (repository_id, artifact_path) DO UPDATE SET format = EXCLUDED.format
		     RETURNING id
		 )
		 INSERT INTO release_bundle_items
		 (id, bundle_id, source_repo_id, artifact_ref_id, sha256, artifact_path, format, size_bytes, adder_id, added_at)
		 SELECT $1, $2, $3, ref.id, $4, $5, $6, $7, (SELECT id FROM auth_users WHERE username = $8), $9
		 FROM ref
		 ON CONFLICT (bundle_id, sha256) DO NOTHING`,
		item.ID, item.BundleID, item.SourceRepoID,
		item.SHA256, item.ArtifactPath, item.Format, item.SizeBytes, item.AddedBy, item.AddedAt,
	)

	return err
}

// RemoveBundleItem deletes an item only while the bundle is still a draft. The
// draft check and the delete are one statement that row-locks the bundle, so a
// concurrent seal cannot slip between them; a non-draft bundle yields
// ErrBundleNotDraft. Removing an item that is already gone is a no-op.
func (p *PostgresDB) RemoveBundleItem(ctx context.Context, bundleID, sha256 string) error {
	res, err := p.db.ExecContext(ctx,
		`DELETE FROM release_bundle_items
		 WHERE bundle_id = $1 AND sha256 = $2
		   AND bundle_id IN (SELECT id FROM release_bundles WHERE id = $1 AND state = 'draft' FOR UPDATE)`,
		bundleID, sha256,
	)
	if err != nil {
		return err
	}

	if n, _ := res.RowsAffected(); n > 0 {
		return nil
	}

	var notDraft bool

	err = p.db.QueryRowContext(ctx, `SELECT state <> 'draft' FROM release_bundles WHERE id = $1`, bundleID).Scan(&notDraft)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrBundleNotFound
	}

	if err != nil {
		return err
	}

	if notDraft {
		return ErrBundleNotDraft
	}

	return nil
}

func (p *PostgresDB) ListBundleItems(ctx context.Context, bundleID string) ([]*models.ReleaseBundleItem, error) {
	rows, err := p.db.QueryContext(ctx,
		`SELECT rbi.id, rbi.bundle_id, rbi.source_repo_id, rbi.sha256, rbi.artifact_path, rbi.format, rbi.size_bytes,
		        COALESCE(u.username,''), rbi.added_at
		 FROM release_bundle_items rbi
		 LEFT JOIN auth_users u ON u.id = rbi.adder_id
		 WHERE rbi.bundle_id=$1 ORDER BY rbi.added_at`, bundleID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list bundle items: %w", err)
	}
	defer rows.Close()

	var result []*models.ReleaseBundleItem

	for rows.Next() {
		item := &models.ReleaseBundleItem{}
		if err := rows.Scan(&item.ID, &item.BundleID, &item.SourceRepoID,
			&item.SHA256, &item.ArtifactPath, &item.Format, &item.SizeBytes, &item.AddedBy, &item.AddedAt); err != nil {
			return nil, fmt.Errorf("failed to scan bundle item: %w", err)
		}

		result = append(result, item)
	}

	return result, rows.Err()
}

func (p *PostgresDB) GetBundleItem(ctx context.Context, bundleID, sha256 string) (*models.ReleaseBundleItem, error) {
	item := &models.ReleaseBundleItem{}

	err := p.db.QueryRowContext(ctx,
		`SELECT rbi.id, rbi.bundle_id, rbi.source_repo_id, rbi.sha256, rbi.artifact_path, rbi.format, rbi.size_bytes,
		        COALESCE(u.username,''), rbi.added_at
		 FROM release_bundle_items rbi
		 LEFT JOIN auth_users u ON u.id = rbi.adder_id
		 WHERE rbi.bundle_id=$1 AND rbi.sha256=$2`, bundleID, sha256,
	).Scan(&item.ID, &item.BundleID, &item.SourceRepoID,
		&item.SHA256, &item.ArtifactPath, &item.Format, &item.SizeBytes, &item.AddedBy, &item.AddedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get bundle item: %w", err)
	}

	return item, nil
}

// BundlePromotion methods

func (p *PostgresDB) CreateBundlePromotion(ctx context.Context, pr *models.BundlePromotion) error {
	pr.StartedAt = time.Now()
	if pr.Status == "" {
		pr.Status = models.PromotionStatusInProgress
	}

	return p.db.QueryRowContext(ctx,
		`INSERT INTO bundle_promotions (bundle_id, from_stage, to_stage, target_repo_id, status, promoter_id, notes, item_count, started_at)
		 VALUES ($1, $2, $3, $4, $5, (SELECT id FROM auth_users WHERE username = $6), $7, $8, $9)
		 RETURNING id`,
		pr.BundleID, pr.FromStage, pr.ToStage, pr.TargetRepoID, pr.Status,
		pr.PromotedBy, pr.Notes, pr.ItemCount, pr.StartedAt,
	).Scan(&pr.ID)
}

func (p *PostgresDB) ListBundlePromotions(ctx context.Context, bundleID string) ([]*models.BundlePromotion, error) {
	rows, err := p.db.QueryContext(ctx,
		`SELECT bp.id, bp.bundle_id, bp.from_stage, bp.to_stage, bp.target_repo_id, bp.status, COALESCE(u.username,''),
		        COALESCE(bp.notes,''), bp.error_message, bp.item_count, bp.started_at, bp.completed_at
		 FROM bundle_promotions bp
		 LEFT JOIN auth_users u ON u.id = bp.promoter_id
		 WHERE bp.bundle_id=$1 ORDER BY bp.started_at`, bundleID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list bundle promotions: %w", err)
	}
	defer rows.Close()

	var result []*models.BundlePromotion

	for rows.Next() {
		pr := &models.BundlePromotion{}
		if err := rows.Scan(&pr.ID, &pr.BundleID, &pr.FromStage, &pr.ToStage, &pr.TargetRepoID,
			&pr.Status, &pr.PromotedBy, &pr.Notes, &pr.ErrorMessage, &pr.ItemCount,
			&pr.StartedAt, &pr.CompletedAt); err != nil {
			return nil, fmt.Errorf("failed to scan bundle promotion: %w", err)
		}

		result = append(result, pr)
	}

	return result, rows.Err()
}
