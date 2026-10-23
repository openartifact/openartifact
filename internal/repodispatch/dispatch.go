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
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/group"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/internal/ocipath"
	"github.com/openartifact/openartifact/internal/proxy"
	"github.com/openartifact/openartifact/internal/repotype"
	"github.com/openartifact/openartifact/internal/storage"
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

// WithBaseURL sets the server base URL that rewritten metadata (npm tarball
// URLs) points at: the same value the hosted format handlers use.
func (d *Dispatcher) WithBaseURL(baseURL string) *Dispatcher {
	d.proxy.SetBaseURL(baseURL)
	d.group.SetBaseURL(baseURL)

	return d
}

// WithGroupMetadata enables deb and rpm groups: gen generates their merged
// metadata, stored in blobs.
func (d *Dispatcher) WithGroupMetadata(blobs storage.BlobStore, gen group.MetadataGenerator) *Dispatcher {
	d.group.WithMetadata(blobs, gen)

	return d
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

		// Docker proxies and groups speak the registry protocol under /v2/
		// (OCIMiddleware); they have no /repos/{name}/... files.
		if repo.Format == "docker" {
			http.NotFound(w, r)
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

// OCIMiddleware is Middleware for the registry routes (/v2/<name>/...): a
// docker proxy or group repository addressed by the first segment of the
// distribution name is served here; hosted repositories (and anything that is
// not a repository path, such as GET /v2/) fall through to the registry.
func (d *Dispatcher) OCIMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rest, ok := strings.CutPrefix(r.URL.Path, "/v2/")
		if !ok || rest == "" {
			next.ServeHTTP(w, r)
			return
		}

		req := ocipath.Parse(rest)
		name, _ := ocipath.SplitName(req.Name)

		repo, err := d.db.GetRepositoryByName(r.Context(), name)
		if err != nil {
			d.log.Errorw("repository lookup failed", "repo", name, "error", err)
			ocipath.WriteError(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

			return
		}

		if repo == nil || repo.Format != "docker" {
			next.ServeHTTP(w, r)
			return
		}

		switch repo.RepoType {
		case repotype.Proxy:
			d.proxy.ServeOCI(w, r, repo, req)
		case repotype.Group:
			d.group.ServeOCI(w, r, repo, req, ocipath.Resource(rest, req.Name), group.Backend{
				Authorize:   d.authorize,
				ServeMember: d.serveMember,
			})
		default:
			next.ServeHTTP(w, r)
		}
	})
}

// serveDockerMember serves relPath ("<image>/manifests/<ref>", …) of a docker
// group member: a proxy member through the pull-through service, a hosted one
// through the registry handler addressed as /v2/<member>/<relPath>.
func (d *Dispatcher) serveDockerMember(w http.ResponseWriter, r *http.Request, member *models.Repository, relPath string) {
	if member.RepoType == repotype.Proxy {
		d.proxy.ServeOCI(w, r, member, ocipath.Parse(member.Name+"/"+relPath))
		return
	}

	h, ok := d.hosted["docker"]
	if !ok {
		ocipath.WriteError(w, http.StatusNotFound, "NAME_UNKNOWN", "repository name not known to registry")
		return
	}

	p := "/v2/" + member.Name + "/" + relPath

	rctx := chi.NewRouteContext()
	rctx.RoutePath = p

	// Clone: the URL is rewritten, and the group's own request must keep its.
	mr := r.Clone(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
	mr.URL.Path, mr.URL.RawPath = p, ""

	h.ServeHTTP(w, mr)
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
	if member.Format == "docker" {
		d.serveDockerMember(w, r, member, relPath)
		return
	}

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
