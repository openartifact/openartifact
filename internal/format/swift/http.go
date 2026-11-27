// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package swift serves hosted Swift package registries to SwiftPM (5.7 and
// later) through the Swift Package Registry Service API (SE-0292, SE-0391).
//
// A repository is a registry rooted at /repos/swift/{name}: the URL given to
// "swift package-registry set". The package implements the release list, the
// release metadata, the manifests (Package.swift and the version-specific
// ones), the source archive, the lookup of package identifiers by repository
// URL, "swift package-registry login" and "swift package-registry publish"
// (synchronous publication only). It also offers DELETE of a release.
package swift

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	"github.com/openartifact/openartifact/internal/quarantine"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/pkg/logger"
	"github.com/openartifact/openartifact/pkg/swift"
)

// FormatName is the repository format name.
const FormatName = "swift"

// metadataMaxAge is how long shared caches may reuse a public repository's
// metadata before revalidating.
const metadataMaxAge = 60

// HTTPStore is the part of the database HTTPHandler needs.
type HTTPStore interface {
	quarantine.Checker
	BlobReferenceCount(ctx context.Context, sha256 string) (int, error)
	CreateSwiftRelease(ctx context.Context, r *models.SwiftRelease) error
	DecrementStorageUsage(ctx context.Context, repoID string, sizeBytes int64, artifactCount int) error
	DeleteSwiftRelease(ctx context.Context, repoID, id string) error
	GetRawArtifact(ctx context.Context, repoID string, path string) (*models.RawArtifact, error)
	GetRepositoryByName(ctx context.Context, name string) (*models.Repository, error)
	GetSwiftRelease(ctx context.Context, repoID, scope, name, version string) (*models.SwiftRelease, error)
	ListSwiftIdentifiers(ctx context.Context, repoID, normalizedURL string) ([]string, error)
	ListSwiftReleaseVersions(ctx context.Context, repoID, scope, name string) ([]models.SwiftReleaseVersion, error)
	ReserveStorageUsage(ctx context.Context, repoID string, sizeBytes int64, artifactCount int) (bool, error)
	UpsertRawArtifact(ctx context.Context, artifact *models.RawArtifact) error
}

// HTTPHandler serves hosted Swift package registries.
type HTTPHandler struct {
	*base.Handler

	DB HTTPStore
}

// NewHTTPHandler creates the handler.
func NewHTTPHandler(log *logger.Logger, db HTTPStore, blobStore storage.BlobStore, baseURL string) *HTTPHandler {
	return &HTTPHandler{Handler: base.New(FormatName, log, db, blobStore, baseURL), DB: db}
}

// RegisterRepoRoutes registers the registry API on the /repos/swift/{name}
// subrouter. The publish route's parameters {scope}, {pkg} and {version} are the
// ones repotype's upload route names. Every route checks the Accept header and
// marks the response as version 1 of the API; GET routes answer HEAD too.
func (h *HTTPHandler) RegisterRepoRoutes(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(negotiate)

		get := func(pattern string, fn http.HandlerFunc) {
			r.Get(pattern, fn)
			r.Head(pattern, fn)
		}

		get("/identifiers", h.handleIdentifiers)
		r.Post("/login", h.handleLogin)
		get("/{scope}/{pkg}", h.handleList)
		get("/{scope}/{pkg}/{version}", h.handleRelease)
		r.Put("/{scope}/{pkg}/{version}", h.handlePublish)
		r.Delete("/{scope}/{pkg}/{version}", h.handleDelete)
		get("/{scope}/{pkg}/{version}/Package.swift", h.handleManifest)
	})
}

// negotiate marks the response as version 1 of the registry API and answers a
// request for another version.
func negotiate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if swift.CheckRequest(w, r) {
			next.ServeHTTP(w, r)
		}
	})
}

// resolveRepo returns the swift repository named in the URL. When there is none
// it writes the response and returns nil.
func (h *HTTPHandler) resolveRepo(w http.ResponseWriter, r *http.Request) *models.Repository {
	name := urlparams.Vars(r)["name"]

	repo, err := h.ResolveRepo(r.Context(), name)
	if err != nil {
		h.Log.Errorw("failed to resolve repo", "name", name, "error", err)
		swift.WriteProblem(w, http.StatusInternalServerError, "internal error")

		return nil
	}

	if repo == nil {
		swift.WriteProblem(w, http.StatusNotFound, "repository not found")
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

// packagePath is a package's registry path "{scope}/{name}": scope and name are
// letters, digits, '-', '.' and '_', so they need no escaping.
func packagePath(scope, name string) string { return scope + "/" + name }

// releaseURL is the absolute URL of a release below origin (see PublicBase).
func releaseURL(origin, repoName, scope, name, version string) string {
	return origin + "/repos/swift/" + url.PathEscape(repoName) + "/" + packagePath(scope, name) + "/" + version
}

// writeDoc answers with a JSON document, revalidated by the digest of its text
// and by origin, the origin its absolute links were built from: a client
// holding an ETag for one origin never gets a 304 for another. The Link
// headers must have been set by the caller.
func writeDoc(w http.ResponseWriter, r *http.Request, repo *models.Repository, origin, contentType string, body []byte) {
	sum := sha256.Sum256(body)

	etag := hex.EncodeToString(sum[:16]) + "-" + base.OriginTag(origin)

	if base.Revalidate(w, r, repo.Public, `"`+etag+`"`, metadataMaxAge) {
		return
	}

	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(http.StatusOK)

	if r.Method != http.MethodHead {
		//nolint:gosec // G705: a JSON or Package.swift document served with its own content type, not HTML
		_, _ = w.Write(body)
	}
}

// internalError logs err and answers 500.
func (h *HTTPHandler) internalError(w http.ResponseWriter, what string, err error, kv ...any) {
	h.Log.Errorw(what, append(kv, "error", err)...)
	swift.WriteProblem(w, http.StatusInternalServerError, "internal error")
}

// notFound answers 404 for a release or package that does not exist.
func notFound(w http.ResponseWriter, what string) {
	swift.WriteProblem(w, http.StatusNotFound, what+" not found")
}

// splitRelease parses the {scope}/{pkg}/{version} parameters of a request into
// a registry path of one of the given kinds. On failure it answers 404.
func splitRelease(w http.ResponseWriter, r *http.Request, kinds ...swift.Kind) (swift.Path, bool) {
	rel := param(r, "scope") + "/" + param(r, "pkg")
	if v := param(r, "version"); v != "" {
		rel += "/" + v
	}

	if strings.HasSuffix(r.URL.Path, "/"+swift.ManifestFile) {
		rel += "/" + swift.ManifestFile
	}

	p, ok := swift.ParsePath(rel)
	if ok {
		if slices.Contains(kinds, p.Kind) {
			return p, true
		}
	}

	notFound(w, "resource")

	return swift.Path{}, false
}
