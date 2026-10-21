// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package repodispatch routes requests for proxy and group repositories to the
// proxy and group services. It is installed as chi middleware in front of the
// per-format routes of /repos/.../{name}: requests for hosted repositories pass
// straight through to the format handlers, everything else is served here.
package repodispatch

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/group"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/internal/proxy"
	"github.com/openartifact/openartifact/internal/repotype"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/pkg/logger"
)

// Authorizer decides whether the principal may perform action on artifactPath
// of repoName. *auth.Evaluator implements it.
type Authorizer interface {
	Evaluate(ctx context.Context, principal auth.Principal, repoName, artifactPath, action string) (bool, error)
}

// Dispatcher serves proxy and group repositories.
type Dispatcher struct {
	log   *logger.Logger
	db    database.Database
	proxy *proxy.Service
	group *group.Handler
	authz Authorizer

	// hosted serves a hosted repository of a format (by format name) from a
	// router that takes the repository name as the "name" URL parameter and
	// the repository-relative path as the trailing wildcard. Groups use it to
	// read their hosted members.
	hosted map[string]http.Handler
}

// New creates a Dispatcher.
func New(log *logger.Logger, db database.Database, proxySvc *proxy.Service, authz Authorizer, hosted map[string]http.Handler) *Dispatcher {
	return &Dispatcher{
		log:    log,
		db:     db,
		proxy:  proxySvc,
		group:  group.NewHandler(log, db),
		authz:  authz,
		hosted: hosted,
	}
}

// Middleware serves the request itself when the {name} repository is a proxy
// or group, and otherwise hands it to next.
func (d *Dispatcher) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := urlparams.Vars(r)["name"]

		repo, err := d.db.GetRepositoryByName(r.Context(), name)
		if err != nil {
			d.log.Errorw("repository lookup failed", "repo", name, "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)

			return
		}

		if repo == nil || repo.RepoType == repotype.Hosted || repo.RepoType == "" {
			next.ServeHTTP(w, r)
			return
		}

		_, tail := middleware.RepoAndPath(r.URL.Path)

		switch repo.RepoType {
		case repotype.Proxy:
			if !repotype.ProxySupported(repo.Format) {
				http.Error(w, "proxy repositories are not supported for this format", http.StatusNotImplemented)
				return
			}

			d.proxy.Serve(w, r, repo, tail)
		case repotype.Group:
			if !repotype.GroupSupported(repo.Format) {
				http.Error(w, "group repositories are not supported for this format", http.StatusNotImplemented)
				return
			}

			d.group.Serve(w, r, repo, tail, group.Backend{
				Authorize:   d.authorize,
				ServeMember: d.serveMember,
			})
		default:
			next.ServeHTTP(w, r)
		}
	})
}

// authorize reports whether the caller may read relPath of member. Without an
// authenticated-or-anonymous principal in the context it fails closed.
func (d *Dispatcher) authorize(r *http.Request, member *models.Repository, relPath string) bool {
	principal := middleware.PrincipalFromContext(r.Context())
	if principal == nil {
		return false
	}

	ok, err := d.authz.Evaluate(r.Context(), *principal, member.Name, relPath, "read")
	if err != nil {
		d.log.Errorw("group member authorization failed", "member", member.Name, "error", err)
		return false
	}

	return ok
}

// serveMember serves relPath from one group member.
func (d *Dispatcher) serveMember(w http.ResponseWriter, r *http.Request, member *models.Repository, relPath string) {
	if member.RepoType == repotype.Proxy {
		d.proxy.Serve(w, r, member, relPath)
		return
	}

	h, ok := d.hosted[member.Format]
	if !ok {
		http.NotFound(w, r)
		return
	}

	// The hosted router is driven with a fresh route context so it sees the
	// member as the {name} repository and relPath as its wildcard, not the
	// group's.
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("name", member.Name)
	rctx.RoutePath = "/" + relPath

	h.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx)))
}
