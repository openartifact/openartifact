// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package composer serves hosted Composer (PHP) repositories to the Composer 2
// client.
//
// A repository is a Composer repository rooted at /repos/{name}: the client's
// {"type": "composer", "url": ".../repos/{name}"}. The package implements the
// Composer 2 metadata protocol (packages.json with metadata-url and
// available-packages, the minified p2/vendor/name.json and
// p2/vendor/name~dev.json documents, the list and search endpoints), the dist
// zip download and the upload and delete of versions through
// PUT/DELETE /packages/upload/{vendor}/{name}/{version} (the Nexus-compatible
// path, "curl -T"). Composer 1 metadata (providers, p/ and includes) is not
// served.
package composer

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	"github.com/openartifact/openartifact/internal/metrics"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/internal/publicurl"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/pkg/composer"
	"github.com/openartifact/openartifact/pkg/logger"
)

// FormatName is the repository format name.
const FormatName = "composer"

// metadataMaxAge is how long shared caches may reuse a public repository's
// metadata before revalidating.
const metadataMaxAge = 60

// distMaxAge is how long shared caches may keep a public dist: it never changes
// under its content-addressed URL.
const distMaxAge = 3600

// HTTPHandler serves hosted Composer repositories.
type HTTPHandler struct {
	*base.Handler

	metrics *metrics.Registry
	DB      database.Database
}

// NewHTTPHandler creates the handler.
func NewHTTPHandler(log *logger.Logger, db database.Database, blobStore storage.BlobStore, baseURL string) *HTTPHandler {
	return &HTTPHandler{Handler: base.New(log, db, blobStore, baseURL), DB: db}
}

// WithMetrics counts a committed upload in openartifact_uploads_total.
func (h *HTTPHandler) WithMetrics(reg *metrics.Registry) *HTTPHandler {
	h.metrics = reg
	return h
}

// allowed returns the methods a route answers.
func allowed(k composer.Kind) []string {
	if k == composer.KindUpload {
		return []string{http.MethodPut, http.MethodDelete}
	}

	return []string{http.MethodGet, http.MethodHead}
}

// RegisterRepoRoutes registers the protocol on a router of hosted composer
// repositories (a group reads its members through it). The whole path below the
// repository is classified by serve, so the router needs no route per file; an
// unknown path is a 404.
func (h *HTTPHandler) RegisterRepoRoutes(r chi.Router) {
	r.HandleFunc("/*", func(w http.ResponseWriter, req *http.Request) {
		rt, ok := composer.ParsePath(chi.URLParam(req, "*"))
		if !ok {
			writeError(w, http.StatusNotFound, "Not found.")
			return
		}

		h.serve(w, req, urlparams.Vars(req)["name"], rt)
	})
}

// Guard is chi middleware for the shared /repos/{name} subrouter. The composer
// paths (/p2/..., /packages/upload/..., /dists/...) would clash with the
// parameter routes of the other formats registered at the same depth, so a
// request is claimed here when it names a composer path and {name} is a
// composer repository, and passed on untouched otherwise. Run it before the
// per-format routes.
func (h *HTTPHandler) Guard(next http.Handler) http.Handler {
	return base.Guard(h.Handler, FormatName, composer.ParsePath, h.serve)(next)
}

// serve answers a classified request for the repository called name.
func (h *HTTPHandler) serve(w http.ResponseWriter, r *http.Request, name string, rt composer.Path) {
	if allowed := allowed(rt.Kind); !slices.Contains(allowed, r.Method) {
		w.Header().Set("Allow", strings.Join(allowed, ", "))
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed.")

		return
	}

	repo, err := h.ResolveRepo(r.Context(), name)
	if err != nil {
		h.Log.Errorw("failed to resolve repo", "name", name, "error", err)
		writeError(w, http.StatusInternalServerError, "Internal error.")

		return
	}

	if repo == nil || repo.Format != FormatName {
		writeError(w, http.StatusNotFound, "Not found.")
		return
	}

	switch rt.Kind {
	case composer.KindRoot:
		h.handleRoot(w, r, repo)
	case composer.KindMetadata:
		h.handleMetadata(w, r, repo, rt)
	case composer.KindList:
		h.handleList(w, r, repo)
	case composer.KindSearch:
		h.handleSearch(w, r, repo)
	case composer.KindDist:
		h.handleDist(w, r, repo, rt)
	case composer.KindUpload:
		if r.Method == http.MethodDelete {
			h.handleDelete(w, r, repo, rt)
			return
		}

		middleware.CountUploads(h.metrics, metrics.FormatComposer, func(w http.ResponseWriter, r *http.Request) {
			h.handlePublish(w, r, repo, rt)
		})(w, r)
	}
}

// repoPath is the path of the repository root below an origin or prefix, without
// a trailing slash.
func repoPath(repoName string) string {
	return "/repos/" + url.PathEscape(repoName)
}

// rootPath is the path-absolute URL of the repository root ("/repos/<name>",
// behind BASE_URL's path prefix when the operator set one). The fields of
// packages.json use it: Composer resolves a value that starts with "/" against
// the scheme and host of the URL the repository was configured with
// (ComposerRepository::canonicalizeUrl), so the document names no origin and
// is the same whatever host it was fetched through.
func (h *HTTPHandler) rootPath(repoName string) string {
	return publicurl.PathPrefix(h.BaseURL) + repoPath(repoName)
}

// distURL is the absolute download URL of one version below origin (Composer
// downloads dist.url as-is): the coordinates and the content reference (the
// sha256 of the zip), so a URL never changes its bytes.
func distURL(origin, repoName string, p *models.ComposerPackage) string {
	return origin + repoPath(repoName) + "/" + composer.DistPath(p.Name, p.Version, p.SHA256)
}

// errorBody is the error document of the repository, the shape Packagist uses.
type errorBody struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

// writeError answers with a JSON error document.
func writeError(w http.ResponseWriter, status int, message string) {
	b, err := json.Marshal(errorBody{Status: "error", Message: message})
	if err != nil {
		http.Error(w, message, status)
		return
	}

	base.WriteJSON(w, status, b)
}

// writeDoc answers with a JSON document that names no origin, revalidated by
// the digest of its text.
func (h *HTTPHandler) writeDoc(w http.ResponseWriter, r *http.Request, repo *models.Repository, doc any) {
	body, err := json.Marshal(doc)
	if err != nil {
		h.Log.Errorw("failed to encode composer document", "error", err)
		writeError(w, http.StatusInternalServerError, "Internal error.")

		return
	}

	h.writeBody(w, r, repo, body, "")
}

// writeBody answers with an encoded document, revalidated by its digest and by
// the origin its absolute URLs were built from ("" when it has none), so a
// client holding an ETag for one origin never gets a 304 for another.
func (h *HTTPHandler) writeBody(w http.ResponseWriter, r *http.Request, repo *models.Repository, body []byte, origin string) {
	sum := sha256.Sum256(body)

	etag := hex.EncodeToString(sum[:16])
	if origin != "" {
		etag += "-" + base.OriginTag(origin)
	}

	if base.Revalidate(w, r, repo.Public, `"`+etag+`"`, metadataMaxAge) {
		return
	}

	base.WriteJSON(w, http.StatusOK, body)
}
