// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"net/http"

	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/pkg/logger"
)

// repoAccessStore is the part of the database repoAccess uses needs.
type repoAccessStore interface {
	GetRepositoryByName(ctx context.Context, name string) (*models.Repository, error)
}

// repoAccess is embedded by the handlers whose routes are scoped to the
// repository named {name} in the URL. It owns the repository resolution and
// the authorisation prologue those handlers share; each method writes the
// error response itself and reports false, so a handler only continues on
// success.
//
// The methods differ in the order of authorisation and lookup, and in the
// status an authorisation failure maps to. Those differences are observable
// (a caller probing an unknown repository sees 404 or 403 depending on the
// route) so they are kept as separate, named variants rather than unified.
type repoAccess struct {
	db        repoAccessStore
	log       *logger.Logger
	evaluator *auth.Evaluator
}

func newRepoAccess(db repoAccessStore, log *logger.Logger, evaluator *auth.Evaluator) repoAccess {
	return repoAccess{db: db, log: log, evaluator: evaluator}
}

// dbError logs msg with err and answers 500 "database error".
func (a repoAccess) dbError(w http.ResponseWriter, msg string, err error) {
	a.log.Errorw(msg, "error", err)
	writeError(w, http.StatusInternalServerError, "internal_error", "database error")
}

// repoNamed resolves the repository named in the URL: a lookup failure is
// logged as logMsg and answered 500, an unknown repository 404.
func (a repoAccess) repoNamed(w http.ResponseWriter, r *http.Request, logMsg string) (*models.Repository, bool) {
	repo, err := a.db.GetRepositoryByName(r.Context(), urlparams.Vars(r)["name"])
	if err != nil {
		a.dbError(w, logMsg, err)
		return nil, false
	}

	if repo == nil {
		writeError(w, http.StatusNotFound, "not_found", "repository not found")
		return nil, false
	}

	return repo, true
}

// loadRepo is repoNamed logging a failure as "<op>: db error getting repo".
func (a repoAccess) loadRepo(w http.ResponseWriter, r *http.Request, op string) (*models.Repository, bool) {
	return a.repoNamed(w, r, op+": db error getting repo")
}

// adminGate answers 403 unless the principal is a global admin. Unlike
// requireAdmin an evaluation error is also a 403, not a 500.
func (a repoAccess) adminGate(w http.ResponseWriter, r *http.Request) bool {
	isAdmin, err := principalIsAdmin(r.Context(), currentPrincipal(r), a.evaluator)
	if err != nil || !isAdmin {
		writeError(w, http.StatusForbidden, "forbidden", "admin access required")
		return false
	}

	return true
}

// adminRepo loads the repository (500/404), then requires a global admin
// (403): an unknown repository is 404 even for a caller that is no admin.
func (a repoAccess) adminRepo(w http.ResponseWriter, r *http.Request, op string) (*models.Repository, bool) {
	repo, ok := a.loadRepo(w, r, op)
	if !ok || !a.adminGate(w, r) {
		return nil, false
	}

	return repo, true
}

// adminFirstRepo is adminRepo with the authorisation first (requireAdmin: 500
// on an evaluation error), so a non-admin never learns whether the repository
// exists.
func (a repoAccess) adminFirstRepo(w http.ResponseWriter, r *http.Request, op string) (*models.Repository, bool) {
	if !requireAdmin(w, r, a.evaluator) {
		return nil, false
	}

	return a.loadRepo(w, r, op)
}

// repoThenRepoAdmin loads the repository, then requires a global admin or a
// maintainer/owner of its project (requireRepoAdmin).
func (a repoAccess) repoThenRepoAdmin(w http.ResponseWriter, r *http.Request, op string) (*models.Repository, bool) {
	repo, ok := a.loadRepo(w, r, op)
	if !ok || !requireRepoAdmin(w, r, a.evaluator, repo.Name) {
		return nil, false
	}

	return repo, true
}

// repoAdminFirst is repoThenRepoAdmin with the authorisation first.
func (a repoAccess) repoAdminFirst(w http.ResponseWriter, r *http.Request, op string) (*models.Repository, bool) {
	if !requireRepoAdmin(w, r, a.evaluator, urlparams.Vars(r)["name"]) {
		return nil, false
	}

	return a.loadRepo(w, r, op)
}

// permittedRepo authorises a repository-scoped request, then loads the
// repository. The caller needs the repository-scoped action on {name}; with
// orRepoAdmin the maintainers and owners of the repository's project pass too.
// Failures are logged as "<op>: ..." and answered 403 "access denied", 500, or
// 404 for an unknown repository.
func (a repoAccess) permittedRepo(w http.ResponseWriter, r *http.Request, op, action string, orRepoAdmin bool) (*models.Repository, bool) {
	name := urlparams.Vars(r)["name"]
	ctx := r.Context()

	p := currentPrincipal(r)
	if p == nil || p.IsAnonymous {
		writeError(w, http.StatusForbidden, "forbidden", "access denied")
		return nil, false
	}

	allowed, err := a.evaluator.Evaluate(ctx, *p, name, "", action)
	if err == nil && !allowed && orRepoAdmin {
		allowed, err = a.evaluator.EvaluateRepoAdmin(ctx, *p, name)
	}

	if err != nil {
		a.log.Errorw(op+": auth evaluation failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "auth evaluation failed")

		return nil, false
	}

	if !allowed {
		writeError(w, http.StatusForbidden, "forbidden", "access denied")
		return nil, false
	}

	return a.loadRepo(w, r, op)
}

// requireUUID answers 404 "<noun> not found" for an id that is not a UUID: it
// cannot exist, and looking it up against a uuid column would be a database
// error (500) instead.
func requireUUID(w http.ResponseWriter, id, noun string) bool {
	if !isUUIDString(id) {
		writeError(w, http.StatusNotFound, "not_found", noun+" not found")
		return false
	}

	return true
}

// fetchByID resolves one entity by id for a repository-scoped route: a
// non-UUID id and a missing entity are 404 "<noun> not found", a lookup
// failure is logged as logMsg and answered 500.
func fetchByID[T any](w http.ResponseWriter, a repoAccess, logMsg, noun, id string, get func(id string) (*T, error)) (*T, bool) {
	if !requireUUID(w, id, noun) {
		return nil, false
	}

	entity, err := get(id)
	if err != nil {
		a.dbError(w, logMsg, err)
		return nil, false
	}

	if entity == nil {
		writeError(w, http.StatusNotFound, "not_found", noun+" not found")
		return nil, false
	}

	return entity, true
}

// fetchInRepo is fetchByID for the entity {id} of repo, looked up by a
// database getter taking (ctx, repoID, id).
func fetchInRepo[T any](
	w http.ResponseWriter, r *http.Request, a repoAccess, repo *models.Repository, logMsg, noun string,
	get func(ctx context.Context, repoID, id string) (*T, error),
) (*T, bool) {
	return fetchByID(w, a, logMsg, noun, urlparams.Vars(r)["id"], func(id string) (*T, error) {
		return get(r.Context(), repo.ID, id)
	})
}

// writeRepoList answers 200 {key: [...]} with the entities list returns for
// repo, converted with toResp; a failure is logged as "<op>: db error".
func writeRepoList[T, R any](
	w http.ResponseWriter, r *http.Request, a repoAccess, repo *models.Repository, op, key string,
	list func(ctx context.Context, repoID string) ([]T, error), toResp func(T) R,
) {
	items, err := list(r.Context(), repo.ID)
	if err != nil {
		a.dbError(w, op+": db error", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{key: mapList(items, toResp)})
}

// mapList converts items with f. The result is never nil, so an empty list
// encodes as [] rather than null.
func mapList[T, R any](items []T, f func(T) R) []R {
	out := make([]R, 0, len(items))
	for _, item := range items {
		out = append(out, f(item))
	}

	return out
}
