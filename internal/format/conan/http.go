// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package conan serves a hosted Conan 2 remote: the REST API (v2) the conan
// client speaks, with revisions.
//
// Every stored file is one conan_packages row, addressed by its recipe
// revision, package id, package revision and path; a revision "exists" on the
// remote once its conanmanifest.txt is stored, which the client uploads last.
package conan

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/pkg/logger"
)

const (
	maxConanUploadBytes = 200 << 20

	// manifestFile is the file whose presence publishes a revision.
	manifestFile = "conanmanifest.txt"
	// conanInfoFile describes a package's configuration.
	conanInfoFile = "conaninfo.txt"

	// maxRefPart bounds one recipe reference component (name, version, user,
	// channel); maxRevisionLen and maxFilenameLen bound the other URL segments
	// (conan_packages.recipe_revision/package_id/package_revision are varchar(64)).
	maxRefPart     = 100
	maxRevisionLen = 64
	maxFilenameLen = 512
)

type HTTPHandler struct {
	*base.Handler
}

func NewHTTPHandler(log *logger.Logger, db database.Database, blobStore storage.BlobStore, baseURL string) *HTTPHandler {
	return &HTTPHandler{Handler: base.New(log, db, blobStore, baseURL)}
}

// RegisterRepoRoutes registers Conan-specific routes on the provided sub-router.
// It does NOT wrap in r.Route() — callers are responsible for the path prefix.
//
// The conan client pings {remote}/v1/ping, then speaks {remote}/v2/... (Conan 2,
// or Conan 1 with revisions enabled) or {remote}/v1/... (Conan 1, see v1.go).
func (h *HTTPHandler) RegisterRepoRoutes(r chi.Router) {
	h.registerV1Routes(r)

	r.Get("/v1/ping", h.handlePing)
	r.Get("/v2/ping", h.handlePing)
	r.Get("/v2/users/authenticate", h.handleAuthenticate)
	r.Get("/v2/users/check_credentials", h.handleCheckCredentials)
	r.Get("/v2/conans/search", h.handleSearch)

	const recipe = "/v2/conans/{pkg}/{version}/{user}/{channel}"

	r.Get(recipe+"/latest", h.handleRecipeLatest)
	r.Get(recipe+"/revisions", h.handleRecipeRevisions)
	r.Get(recipe+"/search", h.handleSearchPackages)
	r.Delete(recipe, h.handleDeleteRecipe)

	const revision = recipe + "/revisions/{rrev}"

	r.Delete(revision, h.handleDeleteRecipeRevision)
	r.Get(revision+"/search", h.handleSearchPackages)
	r.Get(revision+"/files", h.handleListFiles)
	r.Get(revision+"/files/*", h.handleDownload)
	r.Head(revision+"/files/*", h.handleDownload)
	r.Put(revision+"/files/*", h.handleUpload)
	r.Delete(revision+"/packages", h.handleDeletePackages)

	const pkg = revision + "/packages/{pid}"

	r.Get(pkg+"/latest", h.handlePackageLatest)
	r.Get(pkg+"/revisions", h.handlePackageRevisions)

	const pkgRevision = pkg + "/revisions/{prev}"

	r.Delete(pkgRevision, h.handleDeletePackageRevision)
	r.Get(pkgRevision+"/files", h.handleListFiles)
	r.Get(pkgRevision+"/files/*", h.handleDownload)
	r.Head(pkgRevision+"/files/*", h.handleDownload)
	r.Put(pkgRevision+"/files/*", h.handleUpload)
}

func (h *HTTPHandler) RegisterRoutes(r chi.Router, prefix string) {
	r.Route(prefix, func(r chi.Router) {
		h.RegisterRepoRoutes(r)
	})
	h.Log.Infow("conan handler registered", "prefix", prefix)
}

// scope is what one request addresses: the repository, the recipe and, when the
// URL carries them, a recipe revision, package id, package revision and file.
type scope struct {
	repoName string
	repoID   string
	recipe   models.ConanRecipe
	rrev     string
	pid      string
	prev     string
	filename string
}

// joinUserChannel is the stored "user/channel"; "_/_" without either.
func joinUserChannel(user, channel string) string { return user + "/" + channel }

// key is the stored identity of the file filename of this scope's revision.
func (s *scope) key(filename string) models.ConanFileKey {
	return models.ConanFileKey{
		Name:            s.recipe.Name,
		Version:         s.recipe.Version,
		UserChannel:     s.recipe.UserChannel,
		RecipeRevision:  s.rrev,
		PackageID:       s.pid,
		PackageRevision: s.prev,
		Filename:        filename,
	}
}

// newFile is a record for the scope's file, ready for its content digests.
func (s *scope) newFile() *models.ConanPackage {
	return &models.ConanPackage{
		RepositoryID:    s.repoID,
		Name:            s.recipe.Name,
		Version:         s.recipe.Version,
		UserChannel:     s.recipe.UserChannel,
		RecipeRevision:  s.rrev,
		PackageID:       s.pid,
		PackageRevision: s.prev,
		Filename:        s.filename,
	}
}

// resource describes the addressed revision for audit and webhook payloads.
func (s *scope) resource() string {
	p := conanPath(s.recipe.Name, s.recipe.Version, s.recipe.UserChannel, "")
	if s.rrev != "" {
		p += "/" + s.rrev
	}

	if s.pid != "" {
		p += "/" + s.pid
	}

	if s.prev != "" {
		p += "/" + s.prev
	}

	return p
}

// conanPath is the repository-relative identifier of a recipe (or one of its
// files when filename is set).
func conanPath(pkg, version, userChannel, filename string) string {
	p := pkg + "/" + version + "/" + userChannel
	if filename != "" {
		p += "/" + filename
	}

	return p
}

// scopeOf resolves the repository and validates the URL parameters; when it
// returns false the response has been written. Nothing in the parameters ever
// reaches a file system, but they become database keys, so they are bounded
// and stripped of anything that is not a plain name.
func (h *HTTPHandler) scopeOf(w http.ResponseWriter, r *http.Request) (*scope, bool) {
	vars := urlparams.Vars(r)

	s := &scope{
		repoName: vars["name"],
		recipe: models.ConanRecipe{
			Name:        vars["pkg"],
			Version:     vars["version"],
			UserChannel: joinUserChannel(vars["user"], vars["channel"]),
		},
		rrev: vars["rrev"],
		pid:  vars["pid"],
		prev: vars["prev"],
	}

	if !validRef(vars["pkg"], vars["version"], vars["user"], vars["channel"]) ||
		!validRevision(s.rrev) || !validRevision(s.pid) || !validRevision(s.prev) {
		http.Error(w, "invalid reference", http.StatusBadRequest)
		return nil, false
	}

	if !h.resolveRepo(w, r, s) {
		return nil, false
	}

	return s, true
}

// fileScopeOf is scopeOf for the routes that end in a `files/*` wildcard: it
// also validates the file path (which may hold subdirectories, e.g. metadata/).
func (h *HTTPHandler) fileScopeOf(w http.ResponseWriter, r *http.Request) (*scope, bool) {
	name, err := url.PathUnescape(urlparams.Vars(r)["*"])
	if err != nil || !validFilename(name) {
		http.Error(w, "invalid file name", http.StatusBadRequest)
		return nil, false
	}

	s, ok := h.scopeOf(w, r)
	if !ok {
		return nil, false
	}

	s.filename = name

	return s, true
}

// repoScopeOf resolves only the repository, for the routes that address no
// recipe (search).
func (h *HTTPHandler) repoScopeOf(w http.ResponseWriter, r *http.Request) (*scope, bool) {
	s := &scope{repoName: urlparams.Vars(r)["name"]}

	if !h.resolveRepo(w, r, s) {
		return nil, false
	}

	return s, true
}

// resolveRepo fills s.repoID; when it returns false the response is written.
func (h *HTTPHandler) resolveRepo(w http.ResponseWriter, r *http.Request, s *scope) bool {
	repoID, err := h.ResolveRepoID(r.Context(), s.repoName)
	if err != nil {
		h.fail(w, "failed to resolve repo", err, "name", s.repoName)
		return false
	}

	if repoID == "" {
		http.NotFound(w, r)
		return false
	}

	s.repoID = repoID

	return true
}

// validRef reports whether name, version, user and channel are usable
// reference components ("_" stands for an absent user or channel).
func validRef(parts ...string) bool {
	for _, p := range parts {
		if p == "" || len(p) > maxRefPart || p == "." || p == ".." || !plainText(p) || strings.ContainsAny(p, `/\`) {
			return false
		}
	}

	return true
}

// validRevision reports whether s is a revision or package id; empty (absent
// from the route) is fine.
func validRevision(s string) bool {
	if len(s) > maxRevisionLen {
		return false
	}

	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '.', c == '_', c == '-':
		default:
			return false
		}
	}

	return true
}

// validFilename reports whether name is a relative path of plain segments, as
// conan names its files (conanfile.py, metadata/sign/signature.json).
func validFilename(name string) bool {
	if name == "" || len(name) > maxFilenameLen || !plainText(name) || strings.Contains(name, `\`) {
		return false
	}

	for seg := range strings.SplitSeq(name, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}

	return true
}

// plainText reports whether s is valid UTF-8 without control characters.
func plainText(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}

	for _, c := range s {
		if c < 0x20 || c == 0x7f {
			return false
		}
	}

	return true
}

// writeJSON writes v as an application/json response; the conan client rejects
// any other content type for JSON endpoints.
func (h *HTTPHandler) writeJSON(w http.ResponseWriter, v any) {
	h.writeJSONStatus(w, http.StatusOK, v)
}

func (h *HTTPHandler) writeJSONStatus(w http.ResponseWriter, status int, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		h.Log.Errorw("failed to encode conan response", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	base.WriteJSON(w, status, data)
}

// fail answers 500 for an internal error after logging it with msg.
func (h *HTTPHandler) fail(w http.ResponseWriter, msg string, err error, kv ...any) {
	h.Log.Errorw(msg, append([]any{"error", err}, kv...)...)
	http.Error(w, "internal error", http.StatusInternalServerError)
}
