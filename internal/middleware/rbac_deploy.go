// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package middleware

import (
	"context"
	"net/http"
	"strings"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/repotype"
)

// deployKey carries the group deploy decision RBAC made for a request.
type deployKey struct{}

// deployDecision is the group a write was authorized through and the hosted
// repository it was authorized against.
type deployDecision struct{ group, target string }

// WithGroupDeploy returns ctx recording that a write to group was authorized
// as a write to target, its deploy target. The dispatcher serves the write
// from exactly that repository, so what RBAC authorized is what is written.
func WithGroupDeploy(ctx context.Context, group, target string) context.Context {
	return context.WithValue(ctx, deployKey{}, deployDecision{group: group, target: target})
}

// GroupDeployFromContext returns the deploy target RBAC authorized a write to
// group against, "" when it authorized none (the write to a group is then
// refused).
func GroupDeployFromContext(ctx context.Context, group string) string {
	d, _ := ctx.Value(deployKey{}).(deployDecision)
	if d.group != group {
		return ""
	}

	return d.target
}

// viaGroup is the group a write was made through, "" for a direct one.
func viaGroup(ctx context.Context) string {
	d, _ := ctx.Value(deployKey{}).(deployDecision)

	return d.group
}

// groupDeployTarget is the name of the hosted repository a write request to
// repo is served by: the deploy target of a group whose format supports one.
// Only a request that needs "write" qualifies (a delete through a group stays
// refused, however it is spelled on the wire) and only below /repos/.
func groupDeployTarget(repo *models.Repository, r *http.Request, scope routeScope) string {
	if repo == nil || repo.RepoType != repotype.Group || repo.DeployTarget == "" || scope.action != "write" ||
		!strings.HasPrefix(r.URL.Path, "/repos/") || !repotype.GroupDeploySupported(repo.Format) {
		return ""
	}

	return repo.DeployTarget
}

// groupDeployScope turns the write scope of a group with a deploy target into
// the write scope of that target (decision: authorization is the target's
// only; a permission on the group neither grants nor is needed). Anything else
// is returned unchanged. The group's repository comes from the cached
// repository lookup, so a change of group, members or target is seen as soon as
// the auth cache is invalidated.
func groupDeployScope(
	ctx context.Context,
	db writeAccessDeniedStore,
	r *http.Request,
	scope routeScope,
) (routeScope, context.Context, error) {
	if scope.action != "write" || scope.repoName == "" || !strings.HasPrefix(r.URL.Path, "/repos/") {
		return scope, ctx, nil
	}

	repo, err := db.GetRepositoryByName(ctx, scope.repoName)
	if err != nil {
		return scope, ctx, err //nolint:wrapcheck // the caller answers 503 for any lookup failure
	}

	target := groupDeployTarget(repo, r, scope)
	if target == "" {
		return scope, ctx, nil
	}

	ctx = WithGroupDeploy(ctx, scope.repoName, target)
	scope.repoName = target

	return scope, ctx, nil
}
