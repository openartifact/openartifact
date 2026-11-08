// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"fmt"

	"github.com/openartifact/openartifact/internal/database/models"
)

// QuayStore owns the state of the Quay-compatible API (internal/quayapi): the
// image paths, robot accounts, teams and notifications a Quay client created
// inside a hosted docker repository.
type QuayStore struct{ *core }

// QuayRepositorySpec describes a Quay repository to create: the row and the two
// permission targets that scope a principal to its image.
type QuayRepositorySpec struct {
	ID           string
	RepositoryID string
	Name         string
	Description  string
	ReadTarget   models.PermissionTarget
	WriteTarget  models.PermissionTarget
}

const quayRepoColumns = `q.id, q.repository_id, r.name, q.name, q.description, q.read_target_id, q.write_target_id, q.created_at`

func scanQuayRepository(row rowScanner) (*models.QuayRepository, error) {
	q := &models.QuayRepository{}
	if err := row.Scan(&q.ID, &q.RepositoryID, &q.OrgName, &q.Name, &q.Description,
		&q.ReadTargetID, &q.WriteTargetID, &q.CreatedAt); err != nil {
		return nil, err //nolint:wrapcheck // callers wrap with their own context and test sql.ErrNoRows
	}

	return q, nil
}

// CreateQuayRepository stores a Quay repository with its permission targets in
// one transaction. A clash with an existing image path is IsUniqueViolation.
func (p *QuayStore) CreateQuayRepository(ctx context.Context, spec QuayRepositorySpec) (*models.QuayRepository, error) {
	defer p.authCache.invalidate()

	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin create quay repository tx: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	var readID, writeID string

	for _, t := range []struct {
		spec *models.PermissionTarget
		id   *string
	}{{&spec.ReadTarget, &readID}, {&spec.WriteTarget, &writeID}} {
		if err := tx.QueryRowContext(ctx,
			`INSERT INTO permission_targets (name, repository_filter, path_pattern, actions) VALUES ($1, $2, $3, $4::text[]) RETURNING id`,
			t.spec.Name, t.spec.RepositoryFilter, t.spec.PathPattern, t.spec.Actions,
		).Scan(t.id); err != nil {
			return nil, fmt.Errorf("create quay permission target: %w", err)
		}
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO quay_repositories (id, repository_id, name, description, read_target_id, write_target_id)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		spec.ID, spec.RepositoryID, spec.Name, spec.Description, readID, writeID,
	); err != nil {
		return nil, fmt.Errorf("create quay repository: %w", err)
	}

	q, err := scanQuayRepository(tx.QueryRowContext(ctx,
		`SELECT `+quayRepoColumns+` FROM quay_repositories q JOIN repositories r ON r.id = q.repository_id WHERE q.id = $1`, spec.ID))
	if err != nil {
		return nil, fmt.Errorf("reload quay repository: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit create quay repository: %w", err)
	}

	return q, nil
}

// GetQuayRepository returns the Quay repository of an organization, nil when
// there is none.
func (p *QuayStore) GetQuayRepository(ctx context.Context, repositoryID, name string) (*models.QuayRepository, error) {
	q, err := scanQuayRepository(p.db.QueryRowContext(ctx,
		`SELECT `+quayRepoColumns+` FROM quay_repositories q JOIN repositories r ON r.id = q.repository_id
		WHERE q.repository_id = $1 AND q.name = $2`, repositoryID, name))

	return orNotFound(q, err, "get quay repository")
}

// ListQuayRepositories returns the Quay repositories of an organization by name.
func (p *QuayStore) ListQuayRepositories(ctx context.Context, repositoryID string) ([]*models.QuayRepository, error) {
	rows, err := p.db.QueryContext(ctx,
		`SELECT `+quayRepoColumns+` FROM quay_repositories q JOIN repositories r ON r.id = q.repository_id
		WHERE q.repository_id = $1 ORDER BY q.name`, repositoryID)
	if err != nil {
		return nil, fmt.Errorf("failed to list quay repositories: %w", err)
	}

	return collectRows(rows, "failed to scan quay repository", "failed to iterate quay repositories", scanQuayRepository)
}

// DeleteQuayRepository removes a Quay repository: its tags (the manifests
// become untagged and the docker GC reclaims them), its permission targets
// with their assignments, and its notifications.
func (p *QuayStore) DeleteQuayRepository(ctx context.Context, q *models.QuayRepository) error {
	defer p.authCache.invalidate()

	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin delete quay repository tx: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(
		ctx,
		`DELETE FROM docker_tags WHERE repository_id = $1 AND image = $2`,
		q.RepositoryID,
		q.Name,
	); err != nil {
		return fmt.Errorf("delete quay repository tags: %w", err)
	}

	// The quay_repositories row goes with its targets (ON DELETE CASCADE), the
	// targets' assignments and the notifications with it.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM permission_targets WHERE id = ANY($1::uuid[])`, []string{q.ReadTargetID, q.WriteTargetID}); err != nil {
		return fmt.Errorf("delete quay permission targets: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit delete quay repository: %w", err)
	}

	return nil
}

// ListQuayTags returns one page of the live tags of a Quay repository by name
// (page counts from 1) and whether more follow.
func (p *QuayStore) ListQuayTags(ctx context.Context, repositoryID, image string, page, limit int) ([]*models.QuayTag, bool, error) {
	rows, err := p.db.QueryContext(ctx,
		`SELECT t.name, t.manifest_digest, t.updated_at FROM docker_tags t
		WHERE t.repository_id = $1 AND t.image = $2 AND t.deleted_at IS NULL
		  AND EXISTS (SELECT 1 FROM docker_manifests m
		              WHERE m.repository_id = t.repository_id AND m.image = t.image
		                AND m.digest = t.manifest_digest AND m.deleted_at IS NULL)
		ORDER BY t.name LIMIT $3 OFFSET $4`,
		repositoryID, image, limit+1, (page-1)*limit)
	if err != nil {
		return nil, false, fmt.Errorf("failed to list quay tags: %w", err)
	}

	tags, err := collectRows(rows, "failed to scan quay tag", "failed to iterate quay tags", func(row rowScanner) (*models.QuayTag, error) {
		t := &models.QuayTag{}

		return t, row.Scan(&t.Name, &t.ManifestDigest, &t.UpdatedAt) //nolint:wrapcheck // wrapped by collectRows
	})
	if err != nil {
		return nil, false, err
	}

	more := len(tags) > limit
	if more {
		tags = tags[:limit]
	}

	return tags, more, nil
}

// SetQuayPermission gives a principal ("user" or "group") read or read+write
// access to a Quay repository, replacing any earlier role.
func (p *QuayStore) SetQuayPermission(ctx context.Context, q *models.QuayRepository, principalType, principalID string, write bool) error {
	defer p.authCache.invalidate()

	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin set quay permission tx: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	if _, err := deleteQuayAssignments(ctx, tx, q, principalType, principalID); err != nil {
		return err
	}

	target := q.ReadTargetID
	if write {
		target = q.WriteTargetID
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO permission_assignments (permission_target_id, principal_type, principal_id) VALUES ($1, $2, $3)`,
		target, principalType, principalID); err != nil {
		return fmt.Errorf("create quay permission assignment: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit set quay permission: %w", err)
	}

	return nil
}

// RemoveQuayPermission withdraws a principal's access to a Quay repository and
// reports whether it had any.
func (p *QuayStore) RemoveQuayPermission(ctx context.Context, q *models.QuayRepository, principalType, principalID string) (bool, error) {
	defer p.authCache.invalidate()

	return deleteQuayAssignments(ctx, p.db, q, principalType, principalID)
}

func deleteQuayAssignments(ctx context.Context, db execQuerier, q *models.QuayRepository, principalType, principalID string) (bool, error) {
	res, err := db.ExecContext(ctx,
		`DELETE FROM permission_assignments
		WHERE permission_target_id = ANY($1::uuid[]) AND principal_type = $2 AND principal_id = $3`,
		[]string{q.ReadTargetID, q.WriteTargetID}, principalType, principalID)
	if err != nil {
		return false, fmt.Errorf("delete quay permission assignment: %w", err)
	}

	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("count quay permission assignments: %w", err)
	}

	return n > 0, nil
}

// ListQuayUserPermissions returns the users and robots with access to a Quay
// repository by name.
func (p *QuayStore) ListQuayUserPermissions(ctx context.Context, q *models.QuayRepository) ([]*models.QuayUserPermission, error) {
	rows, err := p.db.QueryContext(ctx,
		`SELECT u.id, u.username, COALESCE(qr.name, ''), bool_or(a.permission_target_id = $2)
		FROM permission_assignments a
		JOIN auth_users u ON u.id = a.principal_id
		LEFT JOIN quay_robots qr ON qr.user_id = u.id
		WHERE a.principal_type = 'user' AND a.permission_target_id = ANY($1::uuid[])
		GROUP BY u.id, u.username, qr.name ORDER BY COALESCE(qr.name, u.username)`,
		[]string{q.ReadTargetID, q.WriteTargetID}, q.WriteTargetID)
	if err != nil {
		return nil, fmt.Errorf("failed to list quay permissions: %w", err)
	}

	return collectRows(rows, "failed to scan quay permission", "failed to iterate quay permissions",
		func(row rowScanner) (*models.QuayUserPermission, error) {
			perm := &models.QuayUserPermission{}

			return perm, row.Scan(&perm.UserID, &perm.Username, &perm.RobotName, &perm.Write) //nolint:wrapcheck // wrapped by collectRows
		})
}

// ListQuayTeamPermissions returns the Quay repositories a team's group has
// access to, by name.
func (p *QuayStore) ListQuayTeamPermissions(ctx context.Context, groupID string) ([]*models.QuayTeamPermission, error) {
	rows, err := p.db.QueryContext(ctx,
		`SELECT q.name, COALESCE(r.public, false), bool_or(a.permission_target_id = q.write_target_id)
		FROM permission_assignments a
		JOIN quay_repositories q ON a.permission_target_id IN (q.read_target_id, q.write_target_id)
		JOIN repositories r ON r.id = q.repository_id
		WHERE a.principal_type = 'group' AND a.principal_id = $1
		GROUP BY q.name, r.public ORDER BY q.name`, groupID)
	if err != nil {
		return nil, fmt.Errorf("failed to list quay team permissions: %w", err)
	}

	return collectRows(rows, "failed to scan quay team permission", "failed to iterate quay team permissions",
		func(row rowScanner) (*models.QuayTeamPermission, error) {
			perm := &models.QuayTeamPermission{}

			return perm, row.Scan(&perm.Repository, &perm.IsPublic, &perm.Write) //nolint:wrapcheck // wrapped by collectRows
		})
}
