// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import "context"

// RepoReadFilter returns a predicate reporting whether the principal may read a
// repository by name. The principal's permission targets are loaded once, so
// filtering N repositories costs one query instead of N.
func (e *Evaluator) RepoReadFilter(ctx context.Context, principal Principal) (func(repoName string) (bool, error), error) {
	if principal.IsLegacy {
		return func(string) (bool, error) { return true, nil }, nil
	}

	if principal.IsAnonymous {
		if !e.allowAnonymousRead {
			return func(string) (bool, error) { return false, nil }, nil
		}

		target, err := e.db.GetPermissionTargetByName(ctx, "anonymous-read")
		if err != nil {
			return nil, err
		}

		return func(repoName string) (bool, error) {
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

	return func(repoName string) (bool, error) {
		for _, target := range targets {
			ok, err := e.matchTarget(target, repoName, "", "read")
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
