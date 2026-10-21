// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"

	"github.com/openartifact/openartifact/internal/database/models"
)

// RepoReadFilter returns a predicate reporting whether the principal may read a
// repository by name. The principal's permission targets are loaded once, so
// filtering N repositories costs one query instead of N. Organization
// memberships apply as in Evaluate (decided from the repository's OrgID).
func (e *Evaluator) RepoReadFilter(ctx context.Context, principal Principal) (func(repo *models.Repository) (bool, error), error) {
	if principal.IsLegacy {
		return func(*models.Repository) (bool, error) { return true, nil }, nil
	}

	if principal.IsAnonymous {
		publicNames, err := e.db.ListPublicRepositoryNames(ctx)
		if err != nil {
			return nil, err
		}

		public := make(map[string]struct{}, len(publicNames))
		for _, n := range publicNames {
			public[n] = struct{}{}
		}

		var target *models.PermissionTarget

		if e.allowAnonymousRead {
			target, err = e.db.GetPermissionTargetByName(ctx, "anonymous-read")
			if err != nil {
				return nil, err
			}
		}

		return func(repo *models.Repository) (bool, error) {
			repoName := repo.Name

			if _, ok := public[repoName]; ok {
				return true, nil
			}

			if target == nil {
				return false, nil
			}

			return e.matchTarget(target, repoName, "", "read")
		}, nil
	}

	targets, err := e.principalTargets(ctx, principal)
	if err != nil {
		return nil, err
	}

	levels, err := e.orgLevels(ctx, principal)
	if err != nil {
		return nil, err
	}

	return func(repo *models.Repository) (bool, error) {
		if repo.OrgID != nil {
			if level, ok := levels[*repo.OrgID]; ok && orgLevelAllows(level, "read") {
				return true, nil
			}
		}

		for _, target := range targets {
			ok, err := e.matchTarget(target, repo.Name, "", "read")
			if err != nil {
				return false, err
			}

			if ok {
				return true, nil
			}
		}

		return false, nil
	}, nil
}
