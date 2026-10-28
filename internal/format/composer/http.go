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
}

// NewHTTPHandler creates the handler.
func NewHTTPHandler(log *logger.Logger, db database.Database, blobStore storage.BlobStore, baseURL string) *HTTPHandler {
	return &HTTPHandler{Handler: base.New(log, db, blobStore, baseURL)}
}

// WithMetrics counts a committed upload in openartifact_uploads_total.
func (h *HTTPHandler) WithMetrics(reg *metrics.Registry) *HTTPHandler {
	h.metrics = reg
	return h
}

type routeKind int

const (
	routeRoot routeKind = iota + 1
	routeMetadata
	routeList
	routeSearch
	routeUpload
	routeDist
)

// route is a request path the protocol defines. name is "vendor/package"; the
// other fields are set per kind.
type route struct {
	kind routeKind
	name string
	// dev is set on a p2 document of the "~dev" variant.
	dev bool
	// version is the version of an upload or a dist, unescaped.
	version string
	// ref is the content reference of a dist (the sha256 of the zip).
	ref string
}

// unescapeAll unescapes every path segment, failing on invalid escapes.
func unescapeAll(parts []string) ([]string, bool) {
	out := make([]string, len(parts))

	for i, p := range parts {
		s, err := url.PathUnescape(p)
		if err != nil {
			return nil, false
		}

		out[i] = s
	}

	return out, true
}

// classify maps the path below the repository to a route.
func classify(rel string) (route, bool) {
	rel = strings.TrimPrefix(rel, "/")

	switch rel {
	case "packages.json":
		return route{kind: routeRoot}, true
	case "list.json":
		return route{kind: routeList}, true
	case "search.json":
		return route{kind: routeSearch}, true
	}

	parts, ok := unescapeAll(strings.Split(rel, "/"))
	if !ok {
		return route{}, false
	}

	switch {
	case len(parts) == 3 && parts[0] == "p2":
		pkg, dev, ok := composer.MetadataName(parts[2])
		name := parts[1] + "/" + pkg

		return route{kind: routeMetadata, name: name, dev: dev}, ok && composer.ValidName(name)
	case len(parts) == 5 && parts[0] == "packages" && parts[1] == "upload":
		name := parts[2] + "/" + parts[3]

		return route{kind: routeUpload, name: name, version: parts[4]}, composer.ValidName(name) && parts[4] != ""
	case len(parts) == 5 && parts[0] == "dists":
		name := parts[1] + "/" + parts[2]
		ref, isZip := strings.CutSuffix(parts[4], ".zip")

		return route{kind: routeDist, name: name, version: parts[3], ref: ref}, isZip && composer.ValidName(name) && parts[3] != ""
	}

	return route{}, false
}

// allowed returns the methods a route answers.
func (k routeKind) allowed() []string {
	if k == routeUpload {
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
		rt, ok := classify(chi.URLParam(req, "*"))
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
	return base.Guard(h.Handler, FormatName, classify, h.serve)(next)
}

// serve answers a classified request for the repository called name.
func (h *HTTPHandler) serve(w http.ResponseWriter, r *http.Request, name string, rt route) {
	if allowed := rt.kind.allowed(); !slices.Contains(allowed, r.Method) {
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

	switch rt.kind {
	case routeRoot:
		h.handleRoot(w, r, repo)
	case routeMetadata:
		h.handleMetadata(w, r, repo, rt)
	case routeList:
		h.handleList(w, r, repo)
	case routeSearch:
		h.handleSearch(w, r, repo)
	case routeDist:
		h.handleDist(w, r, repo, rt)
	case routeUpload:
		if r.Method == http.MethodDelete {
			h.handleDelete(w, r, repo, rt)
			return
		}

		middleware.CountUploads(h.metrics, metrics.FormatComposer, func(w http.ResponseWriter, r *http.Request) {
			h.handlePublish(w, r, repo, rt)
		})(w, r)
	}
}

// repoURL is the absolute URL of the repository root, without a trailing slash:
// every link the metadata hands out is absolute.
func (h *HTTPHandler) repoURL(repoName string) string {
	return strings.TrimSuffix(h.BaseURL, "/") + "/repos/" + url.PathEscape(repoName)
}

// distURL is the download URL of one version: the coordinates and the content
// reference (the sha256 of the zip), so a URL never changes its bytes.
func (h *HTTPHandler) distURL(repoName string, p *models.ComposerPackage) string {
	return h.repoURL(repoName) + "/dists/" + p.Name + "/" + url.PathEscape(p.Version) + "/" + p.SHA256 + ".zip"
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

// writeDoc answers with a JSON document, revalidated by the digest of its text.
func (h *HTTPHandler) writeDoc(w http.ResponseWriter, r *http.Request, repo *models.Repository, doc any) {
	body, err := json.Marshal(doc)
	if err != nil {
		h.Log.Errorw("failed to encode composer document", "error", err)
		writeError(w, http.StatusInternalServerError, "Internal error.")

		return
	}

	h.writeBody(w, r, repo, body)
}

// writeBody answers with an encoded document, revalidated by its digest.
func (h *HTTPHandler) writeBody(w http.ResponseWriter, r *http.Request, repo *models.Repository, body []byte) {
	sum := sha256.Sum256(body)
	if base.Revalidate(w, r, repo.Public, `"`+hex.EncodeToString(sum[:16])+`"`, metadataMaxAge) {
		return
	}

	base.WriteJSON(w, http.StatusOK, body)
}
