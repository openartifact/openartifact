// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package terraform serves hosted Terraform/OpenTofu registries: modules
// (module registry protocol) and providers (provider registry protocol), in a
// repository of format "terraform". The repository name is the registry
// namespace, so a module is addressed as
//
//	module "x" { source = "host/REPO/NAME/SYSTEM" }
//
// and a provider as host/REPO/TYPE. The service discovery document at
// /.well-known/terraform.json points the clients at /repos/.
//
// Publishing is OpenArtifact's own API (the registry protocols have none):
// PUT /repos/{repo}/_api/modules/... and /_api/providers/....
package terraform

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/pkg/gpg"
	"github.com/openartifact/openartifact/pkg/logger"
)

// formatName is the repository format and the webhook/metrics label.
const formatName = "terraform"

// versionsMaxAge is how long shared caches may reuse a public repository's
// version lists before revalidating.
const versionsMaxAge = 60

// ticketPath is the bare-router prefix of the credential-free download URLs.
const ticketPath = "/terraform/dl/"

// HTTPHandler serves the module and provider registry protocols and their
// publishing API.
type HTTPHandler struct {
	*base.Handler

	signers   gpg.Resolver
	proxy     ProxyServer
	ticketKey []byte
	now       func() time.Time
	DB        database.Database
}

// NewHTTPHandler creates the handler. baseURL is the configured server URL
// (empty when unset); only its path prefix is used, the download tickets and
// discovery values are path-absolute references. Providers need WithSigner and
// download tickets need WithTicketKey; without them the matching endpoints
// answer 503.
func NewHTTPHandler(log *logger.Logger, db database.Database, blobStore storage.BlobStore, baseURL string) *HTTPHandler {
	return &HTTPHandler{Handler: base.New(log, db, blobStore, baseURL), now: time.Now, DB: db}
}

// WithSigner sets where provider checksum signatures come from: the GPG key
// of the repository (see gpg.Resolver).
func (h *HTTPHandler) WithSigner(r gpg.Resolver) *HTTPHandler {
	h.signers = r

	return h
}

// WithTicketKey derives the download-ticket key from the server HMAC secret.
func (h *HTTPHandler) WithTicketKey(secret []byte) *HTTPHandler {
	h.ticketKey = deriveTicketKey(secret)

	return h
}

// RegisterRepoRoutes registers the routes of /repos/{name}. The module
// "latest" route GET /{mod}/{system} is not here, see LatestGuard.
func (h *HTTPHandler) RegisterRepoRoutes(r chi.Router) {
	// Registry protocol (read).
	r.Get("/{type}/versions", h.handleProviderVersions)
	r.Get("/{type}/{version}/download/{os}/{arch}", h.handleProviderDownload)
	r.Get("/{mod}/{system}/versions", h.handleModuleVersions)
	r.Get("/{mod}/{system}/download", h.handleModuleLatestDownload)
	r.Get("/{mod}/{system}/{version}/download", h.handleModuleDownload)
	r.Get("/{mod}/{system}/{version}", h.handleModuleVersion)

	// Publishing API.
	r.Put("/_api/modules/{mod}/{system}/{version}", h.handleUploadModule)
	r.Put("/_api/providers/{type}/{version}/{os}/{arch}", h.handleUploadProvider)
	r.Delete("/_api/modules/{mod}/{system}/{version}", h.handleDeleteModule)
	r.Delete("/_api/providers/{type}/{version}/{os}/{arch}", h.handleDeleteProviderPlatform)
	r.Delete("/_api/providers/{type}/{version}", h.handleDeleteProviderVersion)
}

// RegisterPublicRoutes registers the routes that live outside /repos on the
// unauthenticated router: the service discovery document and the ticketed
// downloads.
func (h *HTTPHandler) RegisterPublicRoutes(r chi.Router) {
	r.Get("/.well-known/terraform.json", h.handleDiscovery)
	r.Head("/.well-known/terraform.json", h.handleDiscovery)
	r.Get(ticketPath+"{ticket}/{filename}", h.handleTicketDownload)
	r.Head(ticketPath+"{ticket}/{filename}", h.handleTicketDownload)
}

// LatestGuard is chi middleware for the shared /repos/{name} subrouter that
// serves the module "latest" call GET /{mod}/{system}. A chi route for it
// would take the two-segment slot another format's route already owns
// (conda's /{platform}/{filename}), so it is answered here, and only when
// {name} is a terraform repository; everything else passes on untouched. The
// router must run it before the per-format routes.
func (h *HTTPHandler) LatestGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			next.ServeHTTP(w, r)
			return
		}

		_, tail := middleware.RepoAndPath(r.URL.Path)

		mod, system, ok := strings.Cut(strings.Trim(tail, "/"), "/")
		// /{type}/versions is the provider version list, and "versions" is
		// not an uploadable module system for that reason.
		if !ok || !validName(mod) || !validName(system) || system == "versions" {
			next.ServeHTTP(w, r)
			return
		}

		rctx := chi.RouteContext(r.Context())
		if rctx == nil {
			next.ServeHTTP(w, r)
			return
		}

		repo, err := h.ResolveRepo(r.Context(), rctx.URLParam("name"))
		if err != nil || repo == nil || repo.Format != formatName {
			next.ServeHTTP(w, r)
			return
		}

		rctx.URLParams.Add("mod", mod)
		rctx.URLParams.Add("system", system)
		h.handleModuleLatest(w, r)
	})
}

// resolveRepo loads the terraform repository named in the URL. When it writes
// the error response itself it returns nil.
func (h *HTTPHandler) resolveRepo(w http.ResponseWriter, r *http.Request) *models.Repository {
	name := urlparams.Vars(r)["name"]

	repo, err := h.ResolveRepo(r.Context(), name)
	if err != nil {
		h.Log.Errorw("failed to resolve repo", "name", name, "error", err)
		writeErrors(w, http.StatusInternalServerError, "internal error")

		return nil
	}

	// A repository of another format has no registry to serve; answering
	// would let a PUT create terraform rows in, say, a helm repository.
	if repo == nil || repo.Format != formatName {
		writeErrors(w, http.StatusNotFound, "repository not found")

		return nil
	}

	return repo
}

// writeErrors writes the error body the registry protocols use.
func writeErrors(w http.ResponseWriter, status int, msgs ...string) {
	writeJSON(w, status, map[string][]string{"errors": msgs})
}

// writeJSON writes v as a JSON response.
func writeJSON(w http.ResponseWriter, status int, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		// Not writeErrors: that marshals too.
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	base.WriteJSON(w, status, append(data, '\n'))
}

// versionsETag derives the validator of a version list from the stamp the
// database computes over the live rows (rows are immutable, so the stamp
// changes exactly when a version or platform is added, removed or restored).
func versionsETag(repoID, kind, name, stamp string) string {
	sum := sha256.Sum256([]byte(repoID + "\x00" + kind + "\x00" + name + "\x00" + stamp))

	return `"` + hex.EncodeToString(sum[:16]) + `"`
}

// emptyStamp is what the database reports for a module or provider with no
// live rows.
func emptyStamp(stamp string) bool { return strings.HasPrefix(stamp, "0:") }

// registryBasePath is the path a repository is served under.
func registryBasePath(repoName string) string {
	return "/repos/" + url.PathEscape(repoName)
}

// handleDiscovery serves the service discovery document: where the module and
// provider registries live. The URLs are relative to the document, so only
// the path of the server's base URL is needed.
func (h *HTTPHandler) handleDiscovery(w http.ResponseWriter, _ *http.Request) {
	prefix := h.pathPrefix()

	w.Header().Set("Cache-Control", "public, max-age=300")
	writeJSON(w, http.StatusOK, map[string]string{
		"modules.v1":   prefix + "/repos/",
		"providers.v1": prefix + "/repos/",
	})
}
