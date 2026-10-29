// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/openartifact/openartifact/internal/database/models"
)

// Docker is the one format with dedicated tables, so its trash is explicit: a
// manifest and its tags are two kinds (docker_manifest, docker_tag), deleting a
// manifest by digest cascades to the tags that pointed at it (same deleted_at),
// and restoring or purging the manifest takes those tags along.

// trashDockerRows stamps the docker rows and runs the manifest cascade on e.
func trashDockerRows(ctx context.Context, e interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}, spec *dockerSpec, repoID string, ids []string, deletedBy string,
) error {
	// One statement timestamp for the row and its cascade, so a cascaded child
	// is recognisably part of the same delete (and restored with it).
	//nolint:gosec // G202: the table is a constant of the docker spec; ids and repo are bound arguments
	res, err := e.ExecContext(ctx,
		`UPDATE `+spec.table+` t SET deleted_at = now(), deleted_by = $3
		 WHERE t.id = ANY($1::uuid[]) AND t.deleted_at IS NULL AND t.repository_id = $2::uuid`,
		uuidArray(ids), repoID, deletedBy,
	)
	if err != nil {
		return fmt.Errorf("trash %s: %w", spec.kind, err)
	}

	if n, _ := res.RowsAffected(); n == 0 {
		return ErrTrashNotFound
	}

	if spec.kind != models.TrashKindDockerManifest {
		return nil
	}

	for _, id := range ids {
		// Deleting a manifest by digest also removes the tags that pointed at it.
		if _, err := e.ExecContext(ctx,
			`UPDATE docker_tags dt SET deleted_at = m.deleted_at, deleted_by = $2
			 FROM docker_manifests m
			 WHERE m.id = $1 AND dt.repository_id = m.repository_id AND dt.image = m.image
			   AND dt.manifest_digest = m.digest AND dt.deleted_at IS NULL`, id, deletedBy); err != nil {
			return fmt.Errorf("trash %s cascade: %w", spec.kind, err)
		}
	}

	return nil
}

// restoreDockerRow un-stamps one docker row (and, for a manifest, the tags that
// were trashed with it).
func restoreDockerRow(ctx context.Context, tx *sql.Tx, spec *dockerSpec, id string) error {
	switch spec.kind { //nolint:exhaustive // only the two docker kinds reach here
	case models.TrashKindDockerTag:
		if err := ensureTagManifestLive(ctx, tx, id); err != nil {
			return err
		}
	case models.TrashKindDockerManifest:
		// Companions go first: they are matched on the manifest's still-set deleted_at.
		if _, err := tx.ExecContext(ctx,
			`UPDATE docker_tags dt SET deleted_at = NULL, deleted_by = NULL
			 FROM docker_manifests m
			 WHERE m.id = $1::uuid AND dt.repository_id = m.repository_id AND dt.image = m.image
			   AND dt.manifest_digest = m.digest AND dt.deleted_at = m.deleted_at
			   AND NOT EXISTS (SELECT 1 FROM docker_tags l
			       WHERE l.repository_id = dt.repository_id AND l.image = dt.image
			         AND l.name = dt.name AND l.deleted_at IS NULL)`, id); err != nil {
			return fmt.Errorf("restore %s companions: %w", spec.kind, err)
		}
	}

	//nolint:gosec // G202: the table is a constant of the docker spec; the id is a bound argument
	if _, err := tx.ExecContext(ctx,
		`UPDATE `+spec.table+` SET deleted_at = NULL, deleted_by = NULL WHERE id = $1::uuid AND deleted_at IS NOT NULL`, id); err != nil {
		if IsUniqueViolation(err) {
			return ErrTrashConflict
		}

		return fmt.Errorf("restore %s: %w", spec.kind, err)
	}

	return nil
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

// purgeDockerRow removes one trashed docker row (and the tags trashed with a
// manifest) and fills res with the blobs and quota to give back.
func purgeDockerRow(ctx context.Context, tx *sql.Tx, spec *dockerSpec, item *models.TrashItem, res *models.TrashPurgeResult) error {
	var shas string
	//nolint:gosec // G202: the SHA expression and table are constants of the docker spec; the id is a bound argument
	if err := tx.QueryRowContext(ctx,
		`SELECT `+spec.blobSHAExpr()+` FROM `+spec.table+` t WHERE t.id = $1::uuid`, item.ID).Scan(&shas); err != nil {
		return fmt.Errorf("collect blobs of %s: %w", spec.kind, err)
	}

	if spec.kind == models.TrashKindDockerManifest {
		// Tags trashed together with the manifest.
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM docker_tags dt USING docker_manifests m
			 WHERE m.id = $1::uuid AND dt.repository_id = m.repository_id AND dt.image = m.image
			   AND dt.manifest_digest = m.digest AND dt.deleted_at = m.deleted_at`, item.ID); err != nil {
			return fmt.Errorf("purge docker tags of manifest: %w", err)
		}
	}

	//nolint:gosec // G202: the table is a constant of the docker spec; the id is a bound argument
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM `+spec.table+` WHERE id = $1::uuid AND deleted_at IS NOT NULL`, item.ID); err != nil {
		return fmt.Errorf("purge %s: %w", spec.kind, err)
	}

	if shas != "" {
		res.BlobSHA256 = strings.Split(shas, ",")
	}

	if spec.quota {
		res.QuotaArtifacts = 1
		res.QuotaBytes = item.SizeBytes
	}

	return nil
}
