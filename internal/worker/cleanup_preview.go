// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/openartifact/openartifact/internal/database/models"
)

// CleanupPreviewLimit bounds the rows a CleanupPreview lists.
const CleanupPreviewLimit = 500

// CleanupPreviewItem is one row a run of the policy would remove.
type CleanupPreviewItem struct {
	ID      string
	Name    string
	Version string
}

// CleanupPreview is the outcome of a dry run: what a run of the policy would
// remove right now, without removing anything.
type CleanupPreview struct {
	// Repository is the repository the policy applies to.
	Repository string
	// Format is the repository's format.
	Format string
	// Rows is the number of package rows the run would remove (or move to the
	// trash). A version made of several files (jar and pom, sdist and wheel)
	// counts one row per file.
	Rows int
	// Items lists the rows, at most CleanupPreviewLimit.
	Items []CleanupPreviewItem
	// Truncated reports that Items is shorter than Rows.
	Truncated bool
}

// PreviewPolicy reports what a run of the repository policy would remove,
// applying every protection a run applies (immutable releases, release
// bundles, quarantine, dist-tagged and newest-snapshot rows), and removes
// nothing. It returns ErrPolicyNotFound for an unknown policy or a project
// default policy (see PreviewProjectPolicy).
func (w *CleanupWorker) PreviewPolicy(ctx context.Context, policyID string) (*CleanupPreview, error) {
	policy, err := w.loadPreviewPolicy(ctx, policyID)
	if err != nil {
		return nil, err
	}

	if policy.RepositoryID == "" {
		return nil, ErrPolicyNotFound
	}

	return w.previewRepository(ctx, policy)
}

// PreviewProjectPolicy reports what a run of a project default policy would
// remove, per repository: the policy expands over the live hosted repositories
// of the project that have no enabled policy of their own, exactly as a run
// does. Repositories of an unsupported format are left out.
func (w *CleanupWorker) PreviewProjectPolicy(ctx context.Context, policyID string) ([]*CleanupPreview, error) {
	policy, err := w.loadPreviewPolicy(ctx, policyID)
	if err != nil {
		return nil, err
	}

	if policy.RepositoryID != "" || policy.ProjectID == "" {
		return nil, ErrPolicyNotFound
	}

	repoIDs, err := w.db.ListProjectCleanupRepositories(ctx, policy.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("failed to list project repositories: %w", err)
	}

	out := make([]*CleanupPreview, 0, len(repoIDs))

	for _, repoID := range repoIDs {
		perRepo := *policy
		perRepo.RepositoryID = repoID

		prev, err := w.previewRepository(ctx, &perRepo)

		switch {
		case err == nil:
			out = append(out, prev)
		case errors.Is(err, errUnsupportedCleanupFormat), errors.Is(err, ErrRepositoryNotFound):
		default:
			return nil, fmt.Errorf("repository %s: %w", repoID, err)
		}
	}

	return out, nil
}

func (w *CleanupWorker) loadPreviewPolicy(ctx context.Context, policyID string) (*models.CleanupPolicy, error) {
	policy, err := w.db.GetCleanupPolicy(ctx, policyID)
	if err != nil {
		return nil, fmt.Errorf("failed to get cleanup policy: %w", err)
	}

	if policy == nil {
		return nil, ErrPolicyNotFound
	}

	if err := policy.Validate(); err != nil {
		return nil, err
	}

	return policy, nil
}

// previewRepository evaluates policy (with RepositoryID set) on one repository.
func (w *CleanupWorker) previewRepository(ctx context.Context, policy *models.CleanupPolicy) (*CleanupPreview, error) {
	repo, err := w.db.GetRepositoryByID(ctx, policy.RepositoryID)
	if err != nil {
		return nil, fmt.Errorf("failed to get repository: %w", err)
	}

	if repo == nil {
		return nil, ErrRepositoryNotFound
	}

	t, ok := cleanupFormatTable(repo.Format)
	if !ok {
		return nil, fmt.Errorf("%w: %s", errUnsupportedCleanupFormat, repo.Format)
	}

	eligible, err := w.planCandidates(ctx, policy, repo, t)
	if err != nil {
		return nil, err
	}

	prev := &CleanupPreview{Repository: repo.Name, Format: repo.Format, Rows: len(eligible)}

	for _, c := range eligible[:min(len(eligible), CleanupPreviewLimit)] {
		prev.Items = append(prev.Items, CleanupPreviewItem{ID: c.id, Name: c.name, Version: c.version})
	}

	prev.Truncated = len(eligible) > len(prev.Items)

	return prev, nil
}

// planCandidates is the selection of a run without its deletion: the rows the
// policy selects minus the ones a protection keeps.
func (w *CleanupWorker) planCandidates(ctx context.Context, policy *models.CleanupPolicy, repo *models.Repository, t formatTable) ([]cleanupCandidate, error) {
	selected, err := w.selectCandidates(ctx, policy, t)
	if err != nil {
		return nil, err
	}

	var eligible []cleanupCandidate

	for chunk := range slices.Chunk(selected, cleanupChunkSize) {
		facts, err := w.loadBlobFacts(ctx, chunk)
		if err != nil {
			return nil, fmt.Errorf("failed to look up blobs for cleanup: %w", err)
		}

		eligible = append(eligible, w.eligibleCandidates(ctx, repo, chunk, facts)...)
	}

	return eligible, nil
}
