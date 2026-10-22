// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/openartifact/openartifact/internal/database/models"
)

// Errors of the locked project member/group changes.
var (
	// ErrProjectNotFound: the project does not exist (any more).
	ErrProjectNotFound = errors.New("project not found")
	// ErrProjectOwnerLocked: the project's recorded owner can be neither
	// demoted nor removed.
	ErrProjectOwnerLocked = errors.New("the project owner cannot be demoted or removed")
	// ErrLastProjectOwner: the change would leave the project without an owner.
	ErrLastProjectOwner = errors.New("cannot remove or demote the last owner")
)

// ProjectRoleGuard decides a member or group change while the project row is
// locked. current is the target's role before the change ("" when the target
// has none); a non-nil error vetoes the change and is returned unchanged.
type ProjectRoleGuard func(current string) error

// ChangeProjectMember sets, adds or removes (newRole == "") a user's project
// role in one transaction that holds the project row lock, so the guard, the
// recorded-owner and last-owner checks and the write see one consistent state
// and concurrent changes serialize. The guard runs first; then the invariants
// ErrProjectOwnerLocked and ErrLastProjectOwner apply.
func (p *PostgresDB) ChangeProjectMember(ctx context.Context, projectID, userID, newRole string, guard ProjectRoleGuard) error {
	defer p.authCache.invalidate()

	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin project member change: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	recordedOwner, err := lockProject(ctx, tx, projectID)
	if err != nil {
		return err
	}

	current, err := currentRole(ctx, tx, `SELECT role FROM project_members WHERE project_id = $1 AND user_id = $2`, projectID, userID)
	if err != nil {
		return err
	}

	if guard != nil {
		if err := guard(current); err != nil {
			return err
		}
	}

	if current != "" && current != newRole {
		if recordedOwner != nil && *recordedOwner == userID {
			return ErrProjectOwnerLocked
		}

		if current == models.ProjectRoleOwner {
			if err := requireOtherOwner(ctx, tx, projectID); err != nil {
				return err
			}
		}
	}

	if err := writeMemberRole(ctx, tx, projectID, userID, current, newRole); err != nil {
		return err
	}

	// Project tokens die with their creator's membership, in the same transaction.
	if current != "" && newRole == "" {
		if err := revokeProjectTokensOf(ctx, tx, userID, projectID); err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit project member change: %w", err)
	}

	return nil
}

func lockProject(ctx context.Context, tx *sql.Tx, projectID string) (*string, error) {
	var owner *string

	err := tx.QueryRowContext(ctx, `SELECT owner_id FROM projects WHERE id = $1 FOR UPDATE`, projectID).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrProjectNotFound
	}

	if err != nil {
		return nil, fmt.Errorf("lock project: %w", err)
	}

	return owner, nil
}

func currentRole(ctx context.Context, tx *sql.Tx, query string, args ...any) (string, error) {
	var role string

	err := tx.QueryRowContext(ctx, query, args...).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}

	if err != nil {
		return "", fmt.Errorf("read current project role: %w", err)
	}

	return role, nil
}

func requireOtherOwner(ctx context.Context, tx *sql.Tx, projectID string) error {
	var owners int

	if err := tx.QueryRowContext(ctx,
		`SELECT count(*) FROM project_members WHERE project_id = $1 AND role = 'owner'`, projectID,
	).Scan(&owners); err != nil {
		return fmt.Errorf("count project owners: %w", err)
	}

	if owners <= 1 {
		return ErrLastProjectOwner
	}

	return nil
}

func writeMemberRole(ctx context.Context, tx *sql.Tx, projectID, userID, current, newRole string) error {
	var err error

	switch {
	case newRole == current:
		return nil
	case newRole == "":
		_, err = tx.ExecContext(ctx, `DELETE FROM project_members WHERE project_id = $1 AND user_id = $2`, projectID, userID)
	case current == "":
		_, err = tx.ExecContext(ctx, `INSERT INTO project_members (project_id, user_id, role) VALUES ($1, $2, $3)`, projectID, userID, newRole)
	default:
		_, err = tx.ExecContext(ctx, `UPDATE project_members SET role = $3 WHERE project_id = $1 AND user_id = $2`, projectID, userID, newRole)
	}

	if err != nil {
		return fmt.Errorf("write project member: %w", err)
	}

	return nil
}

// ChangeProjectGroup attaches a group with newRole, changes its role, or
// detaches it (newRole == ""), in one transaction that holds the project row
// lock; the guard sees the group's current role ("" when not attached).
func (p *PostgresDB) ChangeProjectGroup(ctx context.Context, projectID, groupID, newRole string, createdBy *string, guard ProjectRoleGuard) error {
	defer p.authCache.invalidate()

	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin project group change: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	if _, err := lockProject(ctx, tx, projectID); err != nil {
		return err
	}

	current, err := currentRole(ctx, tx, `SELECT role FROM project_groups WHERE project_id = $1 AND group_id = $2`, projectID, groupID)
	if err != nil {
		return err
	}

	if guard != nil {
		if err := guard(current); err != nil {
			return err
		}
	}

	switch newRole {
	case current:
	case "":
		_, err = tx.ExecContext(ctx, `DELETE FROM project_groups WHERE project_id = $1 AND group_id = $2`, projectID, groupID)
	default:
		_, err = tx.ExecContext(ctx,
			`INSERT INTO project_groups (project_id, group_id, role, created_by) VALUES ($1, $2, $3, $4)
			 ON CONFLICT (project_id, group_id) DO UPDATE SET role = EXCLUDED.role`,
			projectID, groupID, newRole, createdBy)
	}

	if err != nil {
		return fmt.Errorf("write project group: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit project group change: %w", err)
	}

	return nil
}

// ListProjectRepositories returns one page of the project's live repositories,
// newest first, and their total number.
func (p *PostgresDB) ListProjectRepositories(ctx context.Context, projectID string, page, limit int) ([]*models.Repository, int, error) {
	var total int

	if err := p.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM repositories WHERE project_id = $1 AND deleted_at IS NULL`, projectID,
	).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count project repositories: %w", err)
	}

	rows, err := p.db.QueryContext(ctx,
		`SELECT `+repoColumns+` FROM repositories WHERE project_id = $1 AND deleted_at IS NULL ORDER BY created_at DESC, id LIMIT $2 OFFSET $3`,
		projectID, limit, (page-1)*limit)
	if err != nil {
		return nil, 0, fmt.Errorf("list project repositories: %w", err)
	}

	defer func() { _ = rows.Close() }()

	repos := make([]*models.Repository, 0, limit)

	for rows.Next() {
		r := &models.Repository{}
		if err := rows.Scan(repoScanDest(r)...); err != nil {
			return nil, 0, fmt.Errorf("scan project repository: %w", err)
		}

		repos = append(repos, r)
	}

	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate project repositories: %w", err)
	}

	return repos, total, nil
}

// CountRepositoriesByProject returns the number of live repositories per
// project for the given project IDs (projects without any are absent).
func (p *PostgresDB) CountRepositoriesByProject(ctx context.Context, projectIDs []string) (map[string]int, error) {
	counts := make(map[string]int, len(projectIDs))
	if len(projectIDs) == 0 {
		return counts, nil
	}

	rows, err := p.q.CountRepositoriesByProjects(ctx, projectIDs)
	if err != nil {
		return nil, fmt.Errorf("count repositories by project: %w", err)
	}

	for _, row := range rows {
		counts[row.ProjectID] = int(row.Repositories)
	}

	return counts, nil
}
