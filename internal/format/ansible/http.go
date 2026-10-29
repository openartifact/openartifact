// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package ansible serves hosted Ansible collection repositories to
// ansible-galaxy (ansible-core 2.15 and later) through the Galaxy API v3.
//
// A repository is a Galaxy server rooted at /repos/{name}/api/: the client's
// "url" in ansible.cfg or on the command line. The package implements the
// collection side of the API: discovery, the collection index, the version
// list (paginated), the version detail with its download URL and artifact
// checksum, the artifact download, and "ansible-galaxy collection publish" (a
// multipart upload whose import is validated synchronously, the import task the
// client polls reports the stored result). Roles (the Galaxy v1 API) are not
// served.
package ansible

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/pkg/logger"
)

// FormatName is the repository format name.
const FormatName = "ansible"

// metadataMaxAge is how long shared caches may reuse a public repository's
// metadata before revalidating.
const metadataMaxAge = 60

// HTTPHandler serves hosted Ansible collection repositories.
type HTTPHandler struct {
	*base.Handler
	DB database.Database
}

// NewHTTPHandler creates the handler.
func NewHTTPHandler(log *logger.Logger, db database.Database, blobStore storage.BlobStore, baseURL string) *HTTPHandler {
	return &HTTPHandler{Handler: base.New(log, db, blobStore, baseURL), DB: db}
}

// route registers h for pattern with and without the trailing slash: the
// client always sends it, but a hand-written URL need not.
func route(register func(string, http.HandlerFunc), pattern string, h http.HandlerFunc) {
	register(pattern, h)
	register(strings.TrimSuffix(pattern, "/"), h)
}

// RegisterRepoRoutes registers the Galaxy API on the /repos/{name} subrouter.
// A request for a repository of another format is answered 404.
func (h *HTTPHandler) RegisterRepoRoutes(r chi.Router) {
	const (
		collections = "/api/v3/collections/"
		one         = collections + "{namespace}/{collection}/"
		versions    = one + "versions/"
		artifacts   = "/api/v3/artifacts/collections/"
	)

	route(r.Get, "/api/", h.handleRoot)
	route(r.Get, "/api/v3/", h.handleV3)
	route(r.Get, collections, h.handleIndex)
	route(r.Get, one, h.handleCollection)
	route(r.Get, versions, h.handleVersions)
	route(r.Get, versions+"{version}/", h.handleVersion)
	route(r.Delete, versions+"{version}/", h.handleDelete)
	route(r.Get, "/api/v3/imports/collections/{id}/", h.handleImport)
	route(r.Post, artifacts, h.handlePublish)
	r.Get(artifacts+"{filename}", h.handleDownload)
}

// resolveRepo returns the ansible repository named in the URL. When there is
// none it writes the response and returns nil.
func (h *HTTPHandler) resolveRepo(w http.ResponseWriter, r *http.Request) *models.Repository {
	name := urlparams.Vars(r)["name"]

	repo, err := h.ResolveRepo(r.Context(), name)
	if err != nil {
		h.Log.Errorw("failed to resolve repo", "name", name, "error", err)
		writeError(w, http.StatusInternalServerError, "error", "Internal error.", "")

		return nil
	}

	if repo == nil || repo.Format != FormatName {
		writeError(w, http.StatusNotFound, "not_found", "Not found.", "")
		return nil
	}

	return repo
}

// param returns the unescaped URL parameter: the router hands out the raw path.
func param(r *http.Request, key string) string {
	raw := urlparams.Vars(r)[key]

	v, err := url.PathUnescape(raw)
	if err != nil {
		return raw
	}

	return v
}

// apiPath is the absolute URL of the repository's v3 API root, without a
// trailing slash. Every link the API hands out is absolute: ansible-core 2.15
// cannot resolve a root-relative download_url.
func (h *HTTPHandler) apiPath(repoName string) string {
	return strings.TrimSuffix(h.BaseURL, "/") + "/repos/" + url.PathEscape(repoName) + "/api/v3"
}

// errorBody is the error document of the Galaxy API.
type errorBody struct {
	Errors []errorItem `json:"errors"`
}

type errorItem struct {
	Status string `json:"status"`
	Code   string `json:"code"`
	Title  string `json:"title"`
	Detail string `json:"detail,omitempty"`
}

// writeError answers with the error document ansible-galaxy prints.
func writeError(w http.ResponseWriter, status int, code, title, detail string) {
	b, err := json.Marshal(errorBody{Errors: []errorItem{{
		Status: http.StatusText(status), Code: code, Title: title, Detail: detail,
	}}})
	if err != nil {
		http.Error(w, title, status)
		return
	}

	base.WriteJSON(w, status, b)
}

// writeDoc answers with a JSON document, revalidated by the digest of its text.
func (h *HTTPHandler) writeDoc(w http.ResponseWriter, r *http.Request, repo *models.Repository, doc any) {
	body, err := json.Marshal(doc)
	if err != nil {
		h.Log.Errorw("failed to encode ansible document", "error", err)
		writeError(w, http.StatusInternalServerError, "error", "Internal error.", "")

		return
	}

	sum := sha256.Sum256(body)
	if base.Revalidate(w, r, repo.Public, `"`+hex.EncodeToString(sum[:16])+`"`, metadataMaxAge) {
		return
	}

	base.WriteJSON(w, http.StatusOK, body)
}

func (h *HTTPHandler) handleRoot(w http.ResponseWriter, r *http.Request) {
	repo := h.resolveRepo(w, r)
	if repo == nil {
		return
	}

	h.writeDoc(w, r, repo, map[string]any{
		"description":        "Galaxy API of an OpenArtifact Ansible collection repository",
		"available_versions": map[string]string{"v3": "v3/"},
	})
}

func (h *HTTPHandler) handleV3(w http.ResponseWriter, r *http.Request) {
	repo := h.resolveRepo(w, r)
	if repo == nil {
		return
	}

	root := h.apiPath(repo.Name)

	h.writeDoc(w, r, repo, map[string]string{
		"collections": root + "/collections/",
		"artifacts":   root + "/artifacts/collections/",
	})
}
