// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"context"
	"fmt"

	"github.com/openartifact/openartifact/internal/audit"
	"github.com/openartifact/openartifact/internal/database/models"
)

// auditCleanupRun records what one policy run did to repo as a
// cleanup.run_completed event, so the repository's activity shows scheduled runs
// and their counts. A run that removed nothing and did not fail is not recorded:
// a policy that runs every hour on a repository with nothing to remove would
// fill the log. The error text is left out of the entry (it may carry
// infrastructure detail); the worker log has it.
func auditCleanupRun(
	ctx context.Context,
	policy *models.CleanupPolicy,
	repo *models.Repository,
	run *cleanupRun,
	removed int,
	runErr error,
) {
	if removed == 0 && runErr == nil {
		return
	}

	l := audit.Default()
	if l == nil {
		return
	}

	hard := 0
	if run != nil {
		hard = run.hard
	}

	details := fmt.Sprintf("cleanup policy %s removed %d versions (%d permanently, %d moved to the trash)",
		policy.Name, removed, hard, removed-hard)
	if runErr != nil {
		details = fmt.Sprintf("cleanup policy %s failed after removing %d versions (%d permanently, %d moved to the trash)",
			policy.Name, removed, hard, removed-hard)
	}

	repoID, resourceType, policyID := repo.ID, "cleanup_policy", policy.ID

	l.Log(context.WithoutCancel(ctx), audit.AuditEvent{
		Action:       audit.ActionCleanupRunCompleted,
		ActorType:    "system",
		Actor:        cleanupActor,
		RepositoryID: &repoID,
		ResourceType: &resourceType,
		ResourceID:   &policyID,
		Details:      details,
	})
}
