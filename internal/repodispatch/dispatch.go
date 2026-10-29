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
	"github.com/openartifact/openartifact/internal/database/models"
	formatapk "github.com/openartifact/openartifact/internal/format/apk"
	formatconan "github.com/openartifact/openartifact/internal/format/conan"
	formatnix "github.com/openartifact/openartifact/internal/format/nix"
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

// DispatcherStore is the part of the database Dispatcher needs.
type DispatcherStore interface {
	GetGroupMetadataState(ctx context.Context, groupID string) (*models.GroupMetadataState, error)
	GetGroupPackageOwner(ctx context.Context, groupID string, path string) (string, bool, error)
	GetRepoMetadataByPath(ctx context.Context, repositoryID string, path string) (*models.RepoMetadata, error)
	GetRepositoryByName(ctx context.Context, name string) (*models.Repository, error)
	HasCondaPackageName(ctx context.Context, repoID string, name string) (bool, error)
	IvyModuleExists(ctx context.Context, repoID string, organisation string, module string) (bool, error)
	ListCondaPackageNames(ctx context.Context, repoID string) ([]string, error)
	ListGroupMemberRepositories(ctx context.Context, groupRepoID string) ([]*models.Repository, error)
	SBTModuleExists(ctx context.Context, repoID string, groupID string, artifactID string) (bool, error)
}

// Dispatcher serves proxy and group repositories.
type Dispatcher struct {
	log   *logger.Logger
	db    DispatcherStore
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
func New(log *logger.Logger, db DispatcherStore, proxySvc *proxy.Service, authz Authorizer, hosted map[string]http.Handler) *Dispatcher {
	return &Dispatcher{
		log:    log,
		db:     db,
		proxy:  proxySvc,
		group:  group.NewHandler(log, db),
		authz:  authz,
		hosted: hosted,
	}
}

// WithBaseURL sets the explicit BASE_URL override (empty when the operator set
// none) for the origin of rewritten metadata such as npm tarball URLs; without
// it the origin follows the request (see internal/publicurl).
func (d *Dispatcher) WithBaseURL(baseURL string) *Dispatcher {
	d.proxy.SetBaseURL(baseURL)
	d.group.SetBaseURL(baseURL)

	return d
}

// WithGroupMetadata enables deb, pacman and rpm groups: gen generates their merged
// metadata, stored in blobs.
func (d *Dispatcher) WithGroupMetadata(blobs storage.BlobStore, gen group.MetadataGenerator) *Dispatcher {
	d.group.WithMetadata(blobs, gen)

	return d
}

// WithApkKeys enables the public key downloads of apk groups: keys resolves the
// key a group's merged indexes are signed with.
func (d *Dispatcher) WithApkKeys(keys formatapk.KeyProvider) *Dispatcher {
	d.group.WithApkKeys(keys)

	return d
}

// WithNixKeys enables the signing of nix groups: keys resolves the key of a
// group, the one its hosted members' narinfo are signed with and its
// public.key is served from.
func (d *Dispatcher) WithNixKeys(keys formatnix.KeyProvider) *Dispatcher {
	d.group.WithNixKeys(keys)

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
			if hint := wrongUploadHint(repo, r); hint != "" {
				http.Error(w, hint, http.StatusNotFound)
				return
			}

			// A hosted repository of a format mounted on the dispatcher (apk) serves a mirror tree of free-form paths
			// (<branch>/<repo>/<arch>/<file>) that no route of the shared
			// /repos/{name} router can express without shadowing the other
			// formats, so it is served here by the format's own router.
			if repo != nil && repotype.MountOf(repo.Format) == repotype.MountDispatcher {
				_, tail := middleware.RepoAndPath(r.URL.Path)
				d.serveMember(w, r, repo, strings.TrimPrefix(tail, "/"))

				return
			}

			next.ServeHTTP(w, r)

			return
		}

		// Docker proxies and groups speak the registry protocol under /v2/
		// (OCIMiddleware); they have no /repos/{name}/... files.
		if repotype.MountOf(repo.Format) == repotype.MountOCI {
			http.NotFound(w, r)
			return
		}

		_, tail := middleware.RepoAndPath(r.URL.Path)

		// A conan client pings and logs in to the repository it is configured
		// with before it asks for any package; those requests are about the
		// session with this server, whatever the repository serves.
		if repo.Format == "conan" && (r.Method == http.MethodGet || r.Method == http.MethodHead) &&
			formatconan.ServeReadOnlySession(w, r, strings.TrimPrefix(tail, "/")) {
			return
		}

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

// wrongUploadHint answers a POST or PUT to /repos/{name}/upload of a hosted
// deb or rpm repository with the real endpoint. No route exists there, so the
// shared catch-alls of other formats (conda upload, npm publish) would
// otherwise answer with an error that has nothing to do with the request. It
// returns "" for every other request.
func wrongUploadHint(repo *models.Repository, r *http.Request) string {
	if repo == nil || (r.Method != http.MethodPost && r.Method != http.MethodPut) {
		return ""
	}

	endpoint := repotype.RestUploadEndpoint(repo.Format)
	if endpoint == "" {
		return ""
	}

	if _, tail := middleware.RepoAndPath(r.URL.Path); strings.TrimSuffix(tail, "/") != "upload" {
		return ""
	}

	return "no upload endpoint at /repos/" + repo.Name + "/upload: upload packages with POST " +
		"/api/v1/repositories/" + repo.Name + "/" + endpoint
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

		if repo == nil || repotype.MountOf(repo.Format) != repotype.MountOCI {
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

	h, ok := d.hosted[member.Format]
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
	if repotype.MountOf(member.Format) == repotype.MountOCI {
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
