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

// CleanupStore owns cleanup policies.
type CleanupStore struct{ *core }

// cleanupPolicyToModel converts a dbsqlc.CleanupPolicy to *models.CleanupPolicy.
func cleanupPolicyToModel(row dbsqlc.CleanupPolicy) *models.CleanupPolicy {
	lastRunAt := row.LastRunAt

	var keepLastN *int
	if row.KeepLastN != nil {
		v := int(*row.KeepLastN)
		keepLastN = &v
	}

	var keepSinceDays *int
	if row.KeepSinceDays != nil {
		v := int(*row.KeepSinceDays)
		keepSinceDays = &v
	}

	var keepDownloadedSinceDays *int
	if row.KeepDownloadedSinceDays != nil {
		v := int(*row.KeepDownloadedSinceDays)
		keepDownloadedSinceDays = &v
	}

	var lastRunDeleted *int
	if row.LastRunDeleted != nil {
		v := int(*row.LastRunDeleted)
		lastRunDeleted = &v
	}

	return &models.CleanupPolicy{
		ID:                      row.ID,
		RepositoryID:            derefStr(row.RepositoryID),
		ProjectID:               derefStr(row.ProjectID),
		Name:                    row.Name,
		Enabled:                 row.Enabled,
		KeepLastN:               keepLastN,
		KeepSinceDays:           keepSinceDays,
		KeepDownloadedSinceDays: keepDownloadedSinceDays,
		NamePattern:             derefStr(row.NamePattern),
		ScheduleCron:            row.ScheduleCron,
		LastRunAt:               lastRunAt,
		LastRunDeleted:          lastRunDeleted,
		LastRunError:            derefStr(row.LastRunError),
		CreatedAt:               row.CreatedAt,
		UpdatedAt:               row.UpdatedAt,
	}
}

// createCleanupPolicyParams builds sqlc params from a models.CleanupPolicy.
func createCleanupPolicyParams(policy *models.CleanupPolicy) dbsqlc.CreateCleanupPolicyParams {
	var keepLastN *int32
	if policy.KeepLastN != nil {
		v := int32(*policy.KeepLastN) //nolint:gosec // G115: bounded by API
		keepLastN = &v
	}

	var keepSinceDays *int32
	if policy.KeepSinceDays != nil {
		v := int32(*policy.KeepSinceDays) //nolint:gosec // G115: bounded by API
		keepSinceDays = &v
	}

	var keepDownloadedSinceDays *int32
	if policy.KeepDownloadedSinceDays != nil {
		v := int32(*policy.KeepDownloadedSinceDays) //nolint:gosec // G115: bounded by API
		keepDownloadedSinceDays = &v
	}

	return dbsqlc.CreateCleanupPolicyParams{
		RepositoryID:            strPtr(policy.RepositoryID),
		ProjectID:               strPtr(policy.ProjectID),
		Name:                    policy.Name,
		Enabled:                 policy.Enabled,
		KeepLastN:               keepLastN,
		KeepSinceDays:           keepSinceDays,
		KeepDownloadedSinceDays: keepDownloadedSinceDays,
		NamePattern:             strPtr(policy.NamePattern),
		ScheduleCron:            policy.ScheduleCron,
	}
}

// CreateCleanupPolicy creates a new cleanup policy.
func (p *CleanupStore) CreateCleanupPolicy(ctx context.Context, policy *models.CleanupPolicy) error {
	row, err := p.q.CreateCleanupPolicy(ctx, createCleanupPolicyParams(policy))

	return finishCreate(insertedRow(row), err, "create cleanup policy", &policy.ID, &policy.CreatedAt, &policy.UpdatedAt)
}

// GetCleanupPolicy retrieves a cleanup policy by ID.
func (p *CleanupStore) GetCleanupPolicy(ctx context.Context, id string) (*models.CleanupPolicy, error) {
	row, err := p.q.GetCleanupPolicy(ctx, id)

	return mapRow(row, err, "get cleanup policy", cleanupPolicyToModel)
}

// UpdateCleanupPolicy updates an existing cleanup policy.
func (p *CleanupStore) UpdateCleanupPolicy(ctx context.Context, policy *models.CleanupPolicy) error {
	var keepLastN *int32
	if policy.KeepLastN != nil {
		v := int32(*policy.KeepLastN) //nolint:gosec // G115: bounded by API
		keepLastN = &v
	}

	var keepSinceDays *int32
	if policy.KeepSinceDays != nil {
		v := int32(*policy.KeepSinceDays) //nolint:gosec // G115: bounded by API
		keepSinceDays = &v
	}

	var keepDownloadedSinceDays *int32
	if policy.KeepDownloadedSinceDays != nil {
		v := int32(*policy.KeepDownloadedSinceDays) //nolint:gosec // G115: bounded by API
		keepDownloadedSinceDays = &v
	}

	updatedAt, err := p.q.UpdateCleanupPolicy(ctx, dbsqlc.UpdateCleanupPolicyParams{
		ID:                      policy.ID,
		Name:                    policy.Name,
		Enabled:                 policy.Enabled,
		KeepLastN:               keepLastN,
		KeepSinceDays:           keepSinceDays,
		KeepDownloadedSinceDays: keepDownloadedSinceDays,
		NamePattern:             strPtr(policy.NamePattern),
		ScheduleCron:            policy.ScheduleCron,
	})
	if err != nil {
		return fmt.Errorf("failed to update cleanup policy: %w", err)
	}

	policy.UpdatedAt = updatedAt

	return nil
}

// DeleteCleanupPolicy deletes a cleanup policy.
func (p *CleanupStore) DeleteCleanupPolicy(ctx context.Context, id string) error {
	if err := p.q.DeleteCleanupPolicy(ctx, id); err != nil {
		return fmt.Errorf("failed to delete cleanup policy: %w", err)
	}

	return nil
}

// ListCleanupPolicies lists all cleanup policies for a repository.
func (p *CleanupStore) ListCleanupPolicies(ctx context.Context, repoID string) ([]*models.CleanupPolicy, error) {
	rows, err := p.q.ListCleanupPolicies(ctx, &repoID)

	return mapRows(rows, err, "list cleanup policies", cleanupPolicyToModel)
}

// ListProjectCleanupPolicies lists the default cleanup policies of a project.
func (p *CleanupStore) ListProjectCleanupPolicies(ctx context.Context, projectID string) ([]*models.CleanupPolicy, error) {
	rows, err := p.q.ListProjectCleanupPolicies(ctx, &projectID)

	return mapRows(rows, err, "list project cleanup policies", cleanupPolicyToModel)
}

// ListProjectCleanupRepositories returns the IDs of the live hosted
// repositories a project default policy applies to: those without an enabled
// repository-level policy.
func (p *CleanupStore) ListProjectCleanupRepositories(ctx context.Context, projectID string) ([]string, error) {
	ids, err := p.q.ListProjectCleanupRepositories(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("failed to list project cleanup repositories: %w", err)
	}

	return ids, nil
}

// ListEnabledCleanupPolicies lists all enabled cleanup policies.
func (p *CleanupStore) ListEnabledCleanupPolicies(ctx context.Context) ([]*models.CleanupPolicy, error) {
	rows, err := p.q.ListEnabledCleanupPolicies(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list enabled cleanup policies: %w", err)
	}

	policies := make([]*models.CleanupPolicy, len(rows))
	for i, row := range rows {
		policies[i] = cleanupPolicyToModel(row)
	}

	return policies, nil
}

// UpdateCleanupPolicyRunResult updates the run result of a cleanup policy.
func (p *CleanupStore) UpdateCleanupPolicyRunResult(ctx context.Context, id string, deletedCount int, runErr error) error {
	var errStr *string
	if runErr != nil {
		errMsg := runErr.Error()
		errStr = &errMsg
	}

	deletedCount32 := int32(deletedCount) //nolint:gosec // G115: bounded by policy execution

	if err := p.q.UpdateCleanupPolicyRunResult(ctx, dbsqlc.UpdateCleanupPolicyRunResultParams{
		ID:             id,
		LastRunDeleted: &deletedCount32,
		LastRunError:   errStr,
	}); err != nil {
		return fmt.Errorf("failed to update cleanup policy run result: %w", err)
	}

	return nil
}
