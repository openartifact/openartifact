// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package apk serves hosted Alpine apk repositories: the mirror-tree layout
// <branch>/<repo>/<arch>/ that apk reads as one repository line, the generated
// signed APKINDEX.tar.gz, and the .apk uploads and deletes.
package apk

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/apkkeys"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	"github.com/openartifact/openartifact/internal/iocopy"
	"github.com/openartifact/openartifact/internal/metrics"
	"github.com/openartifact/openartifact/internal/presign"
	"github.com/openartifact/openartifact/internal/quarantine"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/urlparams"
	pkgapk "github.com/openartifact/openartifact/pkg/apk"
	"github.com/openartifact/openartifact/pkg/logger"
)

// IndexName is the file name of the signed index of every branch/repo/arch
// directory, and the last element of its repository_metadata path.
const IndexName = "APKINDEX.tar.gz"

// IndexPath is the repository_metadata path of the index of one directory.
func IndexPath(branch, repo, arch string) string {
	return branch + "/" + repo + "/" + arch + "/" + IndexName
}

const (
	// maxUploadBytes bounds one .apk upload. Alpine's largest packages are a few hundred MiB.
	maxUploadBytes = 1 << 30

	// indexMaxAgeSeconds is how long a shared cache may reuse an index without asking.
	indexMaxAgeSeconds = 60
)

// KeyProvider is the signing key service (*apkkeys.Service).
type KeyProvider interface {
	Info(ctx context.Context, repo *models.Repository) (*apkkeys.Info, error)
}

// Regenerator schedules the regeneration of a repository's indexes.
type Regenerator interface{ NotifyStale(repoID string) }

// HTTPHandler serves hosted apk repositories.
type HTTPHandler struct {
	*base.Handler

	keys    KeyProvider
	regen   Regenerator
	metrics *metrics.Registry
}

// NewHTTPHandler builds the handler. Wire the key service and the regeneration
// worker with WithKeys and WithRegenerator before serving.
func NewHTTPHandler(log *logger.Logger, db database.Database, blobStore storage.BlobStore, baseURL string) *HTTPHandler {
	return &HTTPHandler{Handler: base.New(log, db, blobStore, baseURL)}
}

// WithKeys wires the signing key service that backs the public key downloads.
func (h *HTTPHandler) WithKeys(k KeyProvider) *HTTPHandler {
	h.keys = k
	return h
}

// WithRegenerator wires the worker that regenerates the indexes after a change.
func (h *HTTPHandler) WithRegenerator(r Regenerator) *HTTPHandler {
	h.regen = r
	return h
}

// WithMetrics counts uploads in reg.
func (h *HTTPHandler) WithMetrics(reg *metrics.Registry) *HTTPHandler {
	h.metrics = reg
	return h
}

func (h *HTTPHandler) notify(repoID string) {
	if h.regen != nil {
		h.regen.NotifyStale(repoID)
	}
}

// RegisterRepoRoutes registers the repository's file tree on r. The paths are
// free-form (<branch>/<repo>/<arch>/<file>, depth fixed by the layout), so the
// router is not mounted on the shared /repos/{name} sub-router: a four-segment
// parameter route there would shadow other formats' routes. The repository
// dispatcher drives it with the repository name and the path below it.
func (h *HTTPHandler) RegisterRepoRoutes(r chi.Router) {
	r.Get("/*", h.handleGet)
	r.Head("/*", h.handleGet)
	r.Put("/*", h.handlePut)
	r.Delete("/*", h.handleDelete)
}

// target is a parsed request path.
type target struct {
	dir  dirKey
	file string
}

// dirKey is one branch/repo/arch directory.
type dirKey struct{ branch, repo, arch string }

func (d dirKey) path() string { return d.branch + "/" + d.repo + "/" + d.arch }

// parseTarget splits <branch>/<repo>/<arch>/<file>.
func parseTarget(rel string) (target, bool) {
	parts := strings.Split(strings.Trim(rel, "/"), "/")
	if len(parts) != 4 || parts[3] == "" {
		return target{}, false
	}

	for _, seg := range parts[:3] {
		if !pkgapk.ValidSegment(seg) {
			return target{}, false
		}
	}

	return target{dir: dirKey{parts[0], parts[1], parts[2]}, file: parts[3]}, true
}

// IsKeyPath reports whether rel names the public key: /public.key or /<name>.rsa.pub.
func IsKeyPath(rel string) bool {
	rel = strings.Trim(rel, "/")

	return rel == "public.key" || (!strings.Contains(rel, "/") && strings.HasSuffix(rel, ".rsa.pub"))
}

func relPath(r *http.Request) string { return urlparams.Vars(r)["*"] }

func (h *HTTPHandler) resolveRepo(w http.ResponseWriter, r *http.Request) *models.Repository {
	repo, err := h.ResolveRepo(r.Context(), urlparams.Vars(r)["name"])
	if err != nil {
		h.Log.Errorw("failed to resolve apk repository", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return nil
	}

	if repo == nil {
		http.NotFound(w, r)
		return nil
	}

	return repo
}

func (h *HTTPHandler) handleGet(w http.ResponseWriter, r *http.Request) {
	rel := relPath(r)

	repo := h.resolveRepo(w, r)
	if repo == nil {
		return
	}

	if IsKeyPath(rel) {
		ServeKey(w, r, h.Log, h.keys, repo, strings.Trim(rel, "/"))
		return
	}

	t, ok := parseTarget(rel)
	if !ok {
		http.NotFound(w, r)
		return
	}

	if t.file == IndexName {
		h.serveIndex(w, r, repo, t.dir)
		return
	}

	h.servePackage(w, r, repo, t)
}

func (h *HTTPHandler) serveIndex(w http.ResponseWriter, r *http.Request, repo *models.Repository, dir dirKey) {
	meta, err := h.DB.GetRepoMetadataByPath(r.Context(), repo.ID, dir.path()+"/"+IndexName)
	if err != nil {
		h.Log.Errorw("failed to load apk index metadata", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if meta == nil {
		http.NotFound(w, r)
		return
	}

	if base.Revalidate(w, r, repo.Public, `"`+meta.BlobSHA256+`"`, indexMaxAgeSeconds) {
		return
	}

	blob, err := h.BlobStore.Get(r.Context(), meta.BlobSHA256)
	if err != nil {
		h.Log.Errorw("failed to read apk index blob", "sha256", meta.BlobSHA256, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}
	defer base.CloseLog(blob, h.Log)

	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Length", strconv.FormatInt(meta.BlobSize, 10))
	w.WriteHeader(http.StatusOK)

	if r.Method != http.MethodHead {
		_, _ = iocopy.Copy(w, blob)
	}
}

func (h *HTTPHandler) servePackage(w http.ResponseWriter, r *http.Request, repo *models.Repository, t target) {
	name, version, ok := pkgapk.ParseFileName(t.file)
	if !ok {
		http.NotFound(w, r)
		return
	}

	pkg, err := h.DB.GetApkPackageFile(r.Context(), repo.ID, t.dir.branch, t.dir.repo, t.dir.arch, name, version)
	if err != nil {
		h.Log.Errorw("failed to get apk package", "name", name, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if pkg == nil {
		http.NotFound(w, r)
		return
	}

	if h.DenyIfQuarantined(w, r, repo.ID, quarantine.PathKeys(t.file, t.dir.path()+"/"+t.file, r.URL.Path)...) {
		return
	}

	if r.Method == http.MethodHead {
		writePackageHeaders(w, pkg, t.file)
		w.WriteHeader(http.StatusOK)

		return
	}

	if err := h.DB.IncrementApkDownloadCount(r.Context(), pkg.ID, repo.ID); err != nil {
		h.Log.Warnw("failed to count apk download", "id", pkg.ID, "error", err)
	}

	if h.TryPresign(w, r, presign.Blob{
		SHA256: pkg.BlobKey, Size: pkg.SizeBytes, Filename: t.file, ContentType: "application/vnd.alpine.apk", Repository: repo,
	}) {
		return
	}

	blob, err := h.BlobStore.Get(r.Context(), pkg.BlobKey)
	if err != nil {
		h.Log.Errorw("failed to get apk blob", "key", pkg.BlobKey, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}
	defer base.CloseLog(blob, h.Log)

	writePackageHeaders(w, pkg, t.file)
	w.WriteHeader(http.StatusOK)
	_, _ = iocopy.Copy(w, blob)
}

func writePackageHeaders(w http.ResponseWriter, pkg *models.ApkPackage, filename string) {
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(pkg.SizeBytes, 10))
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filename}))
}

// ServeKey answers /public.key and /<keyname>.rsa.pub (file) with the
// repository's public key; keys == nil means the server has no signing keys.
// The key is generated on first use. A group repository is served by the group
// handler through it with the group's own key.
func ServeKey(w http.ResponseWriter, r *http.Request, log *logger.Logger, keys KeyProvider, repo *models.Repository, file string) {
	if keys == nil {
		http.NotFound(w, r)
		return
	}

	info, err := keys.Info(r.Context(), repo)
	if err != nil {
		if errors.Is(err, apkkeys.ErrSecretKeyRequired) {
			http.Error(w, "this server does not sign apk indexes: OPENARTIFACT_SECRET_KEY is not set", http.StatusNotFound)
			return
		}

		log.Errorw("failed to load apk public key", "repo", repo.Name, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if file != "public.key" && file != info.KeyName {
		http.NotFound(w, r)
		return
	}

	if base.Revalidate(w, r, repo.Public, `"`+info.Fingerprint+`"`, indexMaxAgeSeconds) {
		return
	}

	w.Header().Set("Content-Type", "application/x-pem-file")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": info.KeyName}))
	w.Header().Set("Content-Length", strconv.Itoa(len(info.PublicKey)))
	w.WriteHeader(http.StatusOK)

	if r.Method != http.MethodHead {
		//nolint:gosec // G705: a PEM public key served as an attachment, not HTML
		_, _ = io.WriteString(w, info.PublicKey)
	}
}
