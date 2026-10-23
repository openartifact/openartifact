// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"fmt"

	"github.com/openartifact/openartifact/internal/database/dbsqlc"
	"github.com/openartifact/openartifact/internal/database/models"
)

// ListDeclarativeObjects returns every ownership marker, ordered by kind and name.
func (p *PostgresDB) ListDeclarativeObjects(ctx context.Context) ([]models.DeclarativeObject, error) {
	rows, err := p.q.ListDeclarativeObjects(ctx)
	if err != nil {
		return nil, fmt.Errorf("list declarative objects: %w", err)
	}

	out := make([]models.DeclarativeObject, 0, len(rows))
	for _, r := range rows {
		out = append(out, models.DeclarativeObject(r))
	}

	return out, nil
}

// UpsertDeclarativeObjects stores the markers by (kind, name) in one
// transaction and returns them as stored.
func (p *PostgresDB) UpsertDeclarativeObjects(ctx context.Context, items []models.DeclarativeObject) ([]models.DeclarativeObject, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin declarative upsert tx: %w", err)
	}

	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	q := p.q.WithTx(tx)
	out := make([]models.DeclarativeObject, 0, len(items))

	for _, it := range items {
		row, err := q.UpsertDeclarativeObject(ctx, dbsqlc.UpsertDeclarativeObjectParams{
			Kind: it.Kind, Name: it.Name, ObjectID: it.ObjectID, AppliedHash: it.AppliedHash,
		})
		if err != nil {
			return nil, fmt.Errorf("upsert declarative object %s/%s: %w", it.Kind, it.Name, err)
		}

		out = append(out, models.DeclarativeObject(row))
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit declarative upsert: %w", err)
	}

	return out, nil
}

// DeleteDeclarativeObject removes one marker and reports whether it existed.
func (p *PostgresDB) DeleteDeclarativeObject(ctx context.Context, kind, name string) (bool, error) {
	n, err := p.q.DeleteDeclarativeObject(ctx, dbsqlc.DeleteDeclarativeObjectParams{Kind: kind, Name: name})
	if err != nil {
		return false, fmt.Errorf("delete declarative object: %w", err)
	}

	return n > 0, nil
}

// ListPermissionAssignmentsForTarget returns the assignments of one permission
// target with principal names, ordered by type then name.
func (p *PostgresDB) ListPermissionAssignmentsForTarget(ctx context.Context, targetID string) ([]models.PermissionAssignmentView, error) {
	rows, err := p.q.ListPermissionAssignmentsForTarget(ctx, targetID)
	if err != nil {
		return nil, fmt.Errorf("list permission assignments for target: %w", err)
	}

	out := make([]models.PermissionAssignmentView, 0, len(rows))
	for _, r := range rows {
		out = append(out, models.PermissionAssignmentView(r))
	}

	return out, nil
}
