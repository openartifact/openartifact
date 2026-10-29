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
	"strings"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
)

// PromotionPipeline methods

// BundleStore owns promotion pipelines and release bundles.
type BundleStore struct{ *core }

func (p *BundleStore) CreatePromotionPipeline(ctx context.Context, pp *models.PromotionPipeline) error {
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

func (p *BundleStore) GetPromotionPipelineByName(ctx context.Context, name string) (*models.PromotionPipeline, error) {
	return p.getPromotionPipeline(ctx, "name", name)
}

func (p *BundleStore) GetPromotionPipelineByID(ctx context.Context, id string) (*models.PromotionPipeline, error) {
	return p.getPromotionPipeline(ctx, "id", id)
}

// getPromotionPipeline reads one promotion pipeline by its id or name column.
func (p *BundleStore) getPromotionPipeline(ctx context.Context, column, value string) (*models.PromotionPipeline, error) {
	pp := &models.PromotionPipeline{}

	err := p.db.QueryRowContext(ctx,
		`SELECT pp.id, pp.name, COALESCE(pp.description,''), pp.stages, COALESCE(u.username,''), pp.created_at, pp.updated_at
		 FROM promotion_pipelines pp
		 LEFT JOIN auth_users u ON u.id = pp.creator_id
		 WHERE pp.`+column+` = $1`, value,
	).Scan(&pp.ID, &pp.Name, &pp.Description, &pp.StagesRaw, &pp.CreatedBy, &pp.CreatedAt, &pp.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get promotion pipeline by %s: %w", column, err)
	}

	if err := json.Unmarshal(pp.StagesRaw, &pp.Stages); err != nil {
		return nil, fmt.Errorf("failed to unmarshal pipeline stages: %w", err)
	}

	return pp, nil
}

func (p *BundleStore) UpdatePromotionPipeline(ctx context.Context, pp *models.PromotionPipeline) error {
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

func (p *BundleStore) DeletePromotionPipeline(ctx context.Context, id string) error {
	_, err := p.db.ExecContext(ctx, `DELETE FROM promotion_pipelines WHERE id=$1`, id)
	return err
}

func (p *BundleStore) ListPromotionPipelines(ctx context.Context) ([]*models.PromotionPipeline, error) {
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

func (p *BundleStore) CreateReleaseBundle(ctx context.Context, b *models.ReleaseBundle) error {
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
	        COALESCE(uc.username,''), COALESCE(rb.creator_id::text,''), rb.sealed_at, us.username, rb.archived_at, ua.username,
	        rb.gpg_key_id, rb.gpg_signature, rb.created_at, rb.updated_at
	 FROM release_bundles rb
	 LEFT JOIN auth_users uc ON uc.id = rb.creator_id
	 LEFT JOIN auth_users us ON us.id = rb.sealer_id
	 LEFT JOIN auth_users ua ON ua.id = rb.archiver_id`

func (p *BundleStore) GetReleaseBundle(ctx context.Context, name, version string) (*models.ReleaseBundle, error) {
	b := &models.ReleaseBundle{}

	err := p.db.QueryRowContext(ctx,
		releaseBundleSelect+` WHERE rb.name=$1 AND rb.version=$2`, name, version,
	).Scan(&b.ID, &b.Name, &b.Version, &b.Description, &b.State, &b.PipelineID, &b.CurrentStage,
		&b.CreatedBy, &b.CreatorID, &b.SealedAt, &b.SealedBy, &b.ArchivedAt, &b.ArchivedBy,
		&b.GPGKeyID, &b.GPGSignature, &b.CreatedAt, &b.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get release bundle: %w", err)
	}

	return b, nil
}

func (p *BundleStore) GetReleaseBundleByID(ctx context.Context, id string) (*models.ReleaseBundle, error) {
	b := &models.ReleaseBundle{}

	err := p.db.QueryRowContext(ctx,
		releaseBundleSelect+` WHERE rb.id=$1`, id,
	).Scan(&b.ID, &b.Name, &b.Version, &b.Description, &b.State, &b.PipelineID, &b.CurrentStage,
		&b.CreatedBy, &b.CreatorID, &b.SealedAt, &b.SealedBy, &b.ArchivedAt, &b.ArchivedBy,
		&b.GPGKeyID, &b.GPGSignature, &b.CreatedAt, &b.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get release bundle by id: %w", err)
	}

	return b, nil
}

func (p *BundleStore) UpdateReleaseBundle(ctx context.Context, b *models.ReleaseBundle) error {
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

func (p *BundleStore) DeleteReleaseBundle(ctx context.Context, id string) error {
	_, err := p.db.ExecContext(ctx, `DELETE FROM release_bundles WHERE id=$1`, id)
	return err
}

func (p *BundleStore) ListReleaseBundles(
	ctx context.Context,
	nameFilter, stateFilter string,
	page, limit int,
) ([]*models.ReleaseBundle, int, error) {
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
			&b.CreatedBy, &b.CreatorID, &b.SealedAt, &b.SealedBy, &b.ArchivedAt, &b.ArchivedBy,
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
func (p *BundleStore) AddBundleItem(ctx context.Context, item *models.ReleaseBundleItem) error {
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
		item.SHA256, item.ArtifactPath, item.Format, item.SizeBytes, item.AddedBy, orNow(item.AddedAt),
	)

	return err
}

// bundleItemStoredFilesSQL is true when the live package file of source
// repository $1 stored as the blob with sha256 $2 is located by $3: its path,
// its filename or its id, or the last element of $3 (a path under a directory
// the client knows but the repository does not). $3 is empty for "any".
const bundleItemStoredFilesSQL = `
SELECT EXISTS (
  SELECT 1
    FROM package_files f
    JOIN blobs b ON b.id = f.blob_id
   WHERE f.repository_id = $1::uuid AND f.deleted_at IS NULL AND b.sha256 = $2
     AND ($3 = '' OR $3 IN (f.path, f.filename, f.id::text) OR regexp_replace($3, '^.*/', '') = f.filename))`

// bundleItemStoredDockerSQL is bundleItemStoredFilesSQL for the manifests of a
// docker repository ($4 is the repository name): $3 may be the manifest id, its
// digest, the image, the distribution name or any of those with a live tag.
const bundleItemStoredDockerSQL = `
SELECT EXISTS (
  SELECT 1
    FROM docker_manifests m
    JOIN blobs b ON b.id = m.blob_id
   WHERE m.repository_id = $1::uuid AND m.deleted_at IS NULL AND b.sha256 = $2
     AND ($3 = ''
          OR $3 IN (m.id::text, m.digest, b.sha256, m.image,
                    $4 || '/' || m.image, $4 || '/' || m.image || '@' || m.digest, $4 || '/' || m.image || '/manifests/' || m.digest)
          OR (m.image = '' AND $3 = $4)
          OR EXISTS (SELECT 1 FROM docker_tags t
                      WHERE t.repository_id = m.repository_id AND t.image = m.image
                        AND t.manifest_digest = m.digest AND t.deleted_at IS NULL
                        AND $3 IN (t.name, m.image || ':' || t.name, $4 || '/' || m.image || ':' || t.name,
                                   $4 || '/' || m.image || '/manifests/' || t.name))))`

// BundleItemStored reports whether repository repoID holds a live artifact of
// the given format whose content digest is sha256 and which path (leading "/"
// ignored) names. A bundle item that fails it would sign a claim about the
// repository that the repository does not back.
func (p *BundleStore) BundleItemStored(ctx context.Context, repoID, repoName, format, sha256, path string) (bool, error) {
	var (
		stored bool
		err    error
	)

	path = strings.TrimPrefix(path, "/")

	if format == "docker" {
		err = p.db.QueryRowContext(ctx, bundleItemStoredDockerSQL, repoID, sha256, path, repoName).Scan(&stored)
	} else {
		err = p.db.QueryRowContext(ctx, bundleItemStoredFilesSQL, repoID, sha256, path).Scan(&stored)
	}

	if err != nil {
		return false, fmt.Errorf("check bundle item %s in repository %s: %w", sha256, repoID, err)
	}

	return stored, nil
}

// RemoveBundleItem deletes an item only while the bundle is still a draft. The
// draft check and the delete are one statement that row-locks the bundle, so a
// concurrent seal cannot slip between them; a non-draft bundle yields
// ErrBundleNotDraft. Removing an item that is already gone is a no-op.
func (p *BundleStore) RemoveBundleItem(ctx context.Context, bundleID, sha256 string) error {
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

func (p *BundleStore) ListBundleItems(ctx context.Context, bundleID string) ([]*models.ReleaseBundleItem, error) {
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

func (p *BundleStore) GetBundleItem(ctx context.Context, bundleID, sha256 string) (*models.ReleaseBundleItem, error) {
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

func (p *BundleStore) CreateBundlePromotion(ctx context.Context, pr *models.BundlePromotion) error {
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

func (p *BundleStore) ListBundlePromotions(ctx context.Context, bundleID string) ([]*models.BundlePromotion, error) {
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

// AddBundleItems inserts all items atomically. The bundle row is locked and
// must be in draft state; otherwise ErrBundleNotDraft / ErrBundleNotFound is
// returned and nothing is inserted. Items whose (bundle_id, sha256) already
// exists are skipped. Each item's ID is filled in.
func (p *BundleStore) AddBundleItems(ctx context.Context, bundleID string, items []*models.ReleaseBundleItem) error {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	var state string

	err = tx.QueryRowContext(ctx, `SELECT state FROM release_bundles WHERE id = $1 FOR UPDATE`, bundleID).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrBundleNotFound
	}

	if err != nil {
		return fmt.Errorf("failed to lock release bundle: %w", err)
	}

	if state != string(models.BundleStateDraft) {
		return ErrBundleNotDraft
	}

	for _, item := range items {
		err := tx.QueryRowContext(ctx, `
			WITH ref AS (
				INSERT INTO artifact_refs (repository_id, artifact_path, format)
				VALUES ($2, $4, $5)
				ON CONFLICT (repository_id, artifact_path) DO UPDATE SET format = EXCLUDED.format
				RETURNING id
			), ins AS (
				INSERT INTO release_bundle_items
				(bundle_id, source_repo_id, artifact_ref_id, sha256, artifact_path, format, size_bytes, adder_id, added_at)
				SELECT $1, $2, ref.id, $3, $4, $5, $6, (SELECT id FROM auth_users WHERE username = $7), $8
				FROM ref
				ON CONFLICT (bundle_id, sha256) DO NOTHING
				RETURNING id
			)
			SELECT COALESCE(
				(SELECT id FROM ins),
				(SELECT id FROM release_bundle_items WHERE bundle_id = $1 AND sha256 = $3))`,
			bundleID, item.SourceRepoID, item.SHA256, item.ArtifactPath, item.Format, item.SizeBytes, item.AddedBy, orNow(item.AddedAt),
		).Scan(&item.ID)
		if err != nil {
			return fmt.Errorf("failed to add bundle item %q: %w", item.SHA256, err)
		}

		item.BundleID = bundleID
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit bundle items: %w", err)
	}

	return nil
}

// SealReleaseBundle moves a draft bundle to sealed with a conditional UPDATE
// (WHERE state = 'draft'), so concurrent item adds/seals cannot interleave.
// It reports whether the bundle was sealed; false means it was no longer a
// draft (or does not exist), or, when sig is given, that its items changed
// since the manifest was signed. A bundle with no items is never sealed.
func (p *BundleStore) SealReleaseBundle(
	ctx context.Context,
	bundleID, sealedBy string,
	sealedAt time.Time,
	sig *models.BundleSignature,
) (bool, error) {
	var keyID, signature, itemIDs *string

	if sig != nil {
		ids := strings.Join(sig.ItemIDs, ",")
		keyID, signature, itemIDs = &sig.KeyFingerprint, &sig.Signature, &ids
	}

	res, err := p.db.ExecContext(ctx, `
		UPDATE release_bundles
		SET state = 'sealed', sealed_at = $2, sealer_id = (SELECT id FROM auth_users WHERE username = $3),
		    gpg_key_id = $4, gpg_signature = $5, updated_at = now()
		WHERE id = $1 AND state = 'draft'
		  AND EXISTS (SELECT 1 FROM release_bundle_items WHERE bundle_id = $1)
		  AND ($6::text IS NULL OR $6::text = (
		        SELECT COALESCE(string_agg(id::text, ',' ORDER BY id::text COLLATE "C"), '')
		        FROM release_bundle_items WHERE bundle_id = $1))`,
		bundleID, sealedAt, sealedBy, keyID, signature, itemIDs)
	if err != nil {
		return false, fmt.Errorf("failed to seal release bundle: %w", err)
	}

	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("failed to seal release bundle: %w", err)
	}

	return n > 0, nil
}
