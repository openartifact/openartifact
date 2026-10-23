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
// filtering N repositories costs one query instead of N. Project
// roles apply as in Evaluate (decided from the repository's ProjectID).
func (e *Evaluator) RepoReadFilter(ctx context.Context, principal Principal) (func(repo *models.Repository) (bool, error), error) {
	canRead, err := e.PathReadFilter(ctx, principal)
	if err != nil {
		return nil, err
	}

	return func(repo *models.Repository) (bool, error) { return canRead(repo, "") }, nil
}

// PathReadFilter is RepoReadFilter for an artifact path inside the repository:
// the returned predicate reports whether the principal may read artifactPath of
// a repository, so a permission target whose path_pattern limits it to part of
// the repository (one docker image, say) is matched against the path. The
// targets are loaded once, however many (repository, path) pairs are checked.
func (e *Evaluator) PathReadFilter(ctx context.Context, principal Principal) (func(repo *models.Repository, artifactPath string) (bool, error), error) {
	if principal.IsLegacy {
		return func(*models.Repository, string) (bool, error) { return true, nil }, nil
	}

	if principal.IsAnonymous {
		return e.anonymousReadFilter(ctx)
	}

	targets, err := e.principalTargets(ctx, principal)
	if err != nil {
		return nil, err
	}

	roles, err := e.projectRoles(ctx, principal)
	if err != nil {
		return nil, err
	}

	return func(repo *models.Repository, artifactPath string) (bool, error) {
		if repo.ProjectID != nil {
			if role, ok := roles[*repo.ProjectID]; ok && projectRoleAllows(role, "read") {
				return true, nil
			}
		}

		for _, target := range targets {
			ok, err := e.matchTarget(target, repo.Name, artifactPath, "read")
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

// anonymousReadFilter is PathReadFilter for an anonymous principal: public
// repositories, then the anonymous-read target when anonymous reads are on.
func (e *Evaluator) anonymousReadFilter(ctx context.Context) (func(repo *models.Repository, artifactPath string) (bool, error), error) {
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

	return func(repo *models.Repository, artifactPath string) (bool, error) {
		if _, ok := public[repo.Name]; ok {
			return true, nil
		}

		if target == nil {
			return false, nil
		}

		return e.matchTarget(target, repo.Name, artifactPath, "read")
	}, nil
}
