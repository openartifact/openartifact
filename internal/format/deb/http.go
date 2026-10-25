// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package deb

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"
	db "github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	"github.com/openartifact/openartifact/internal/iocopy"
	"github.com/openartifact/openartifact/internal/pathseg"
	"github.com/openartifact/openartifact/internal/presign"
	"github.com/openartifact/openartifact/internal/quarantine"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/pkg/logger"
)

type HTTPHandler struct {
	log       *logger.Logger
	db        db.Database
	blobStore storage.BlobStore
}

func NewHTTPHandler(log *logger.Logger, database db.Database, blobStore storage.BlobStore) *HTTPHandler {
	return &HTTPHandler{
		log:       log,
		db:        database,
		blobStore: blobStore,
	}
}

// RegisterRepoRoutes registers Debian-specific routes on the provided sub-router.
// It does NOT wrap in r.Route() — callers are responsible for the path prefix.
func (h *HTTPHandler) RegisterRepoRoutes(r chi.Router) {
	r.Get("/dists/{dist}/Release", h.handleRelease)
	r.Get("/dists/{dist}/InRelease", h.handleInRelease)
	r.Get("/dists/{dist}/Release.gpg", h.handleReleaseGPG)
	r.Get("/dists/{dist}/{comp}/binary-{arch}/Packages", h.handlePackages)
	r.Get("/dists/{dist}/{comp}/binary-{arch}/Packages.gz", h.handlePackagesGz)
	r.Get("/dists/{dist}/{comp}/binary-{arch}/Packages.xz", h.handlePackagesXz)
	r.Get("/pool/{comp}/{prefix}/{pkg}/{filename}", h.handlePoolFile)
	r.Get("/public.key", h.handlePublicKey)
}

func (h *HTTPHandler) resolveRepo(ctx context.Context, name string) (*models.Repository, error) {
	repo, err := h.db.GetRepositoryByName(ctx, name)
	if err != nil {
		return nil, err
	}

	if repo == nil {
		return nil, fmt.Errorf("repository not found: %s", name) //nolint:err113 // dynamic error with runtime context
	}

	return repo, nil
}

func (h *HTTPHandler) resolveRepoID(ctx context.Context, name string) (string, error) {
	repo, err := h.resolveRepo(ctx, name)
	if err != nil {
		return "", err
	}

	return repo.ID, nil
}

// metadataMaxAge is how long shared caches may reuse a public repository's
// metadata before revalidating.
const metadataMaxAge = 300

// tryServeRepoBlob attempts to serve a pre-generated metadata blob. The blob
// digest is the ETag, so a matching If-None-Match is answered without reading
// the blob. Returns true if served, false if the blob does not exist yet.
func (h *HTTPHandler) tryServeRepoBlob(w http.ResponseWriter, r *http.Request, repo *models.Repository, path string, contentType string) bool {
	meta, err := h.db.GetRepoMetadataByPath(r.Context(), repo.ID, path)
	if err != nil {
		h.log.Errorw("failed to query repo metadata", "path", path, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return true // error already written
	}

	if meta == nil {
		return false // not found; caller should fall back to streaming
	}

	if base.Revalidate(w, r, repo.Public, `"`+meta.BlobSHA256+`"`, metadataMaxAge) {
		return true
	}

	reader, err := h.blobStore.Get(r.Context(), meta.BlobSHA256)
	if err != nil {
		h.log.Errorw("failed to read blob", "sha256", meta.BlobSHA256, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return true
	}
	defer func() { _ = reader.Close() }()

	w.Header().Set("Content-Type", contentType)

	if _, err := iocopy.Copy(w, reader); err != nil {
		h.log.Errorw("failed to write response", "path", path, "error", err)
	}

	return true
}

func (h *HTTPHandler) handleRelease(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	dist := vars["dist"]
	name := vars["name"]

	repo, err := h.resolveRepo(r.Context(), name)
	if err != nil {
		h.log.Warnw("failed to resolve repository", "name", name, "error", err)
		http.NotFound(w, r)

		return
	}

	// Fast path: serve pre-generated blob.
	path := GetReleasePath(dist)
	if h.tryServeRepoBlob(w, r, repo, path, "text/plain") {
		return
	}

	// Streaming fallback: generate on-the-fly from DB.
	w.Header().Set("Content-Type", "text/plain")
	w.Header().Set("Cache-Control", "no-cache")

	if err := StreamRelease(r.Context(), h.db, repo.ID, dist, w); err != nil {
		h.log.Errorw("failed to stream Release", "dist", dist, "error", err)
	}
}

func (h *HTTPHandler) handleInRelease(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	dist := vars["dist"]
	name := vars["name"]

	repo, err := h.resolveRepo(r.Context(), name)
	if err != nil {
		h.log.Warnw("failed to resolve repository", "name", name, "error", err)
		http.NotFound(w, r)

		return
	}

	path := GetInReleasePath(dist)
	if !h.tryServeRepoBlob(w, r, repo, path, "text/plain") {
		http.NotFound(w, r)
	}
}

func (h *HTTPHandler) handleReleaseGPG(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	dist := vars["dist"]
	name := vars["name"]

	repo, err := h.resolveRepo(r.Context(), name)
	if err != nil {
		h.log.Warnw("failed to resolve repository", "name", name, "error", err)
		http.NotFound(w, r)

		return
	}

	path := GetReleaseGPGPath(dist)
	if !h.tryServeRepoBlob(w, r, repo, path, "application/pgp-signature") {
		http.NotFound(w, r)
	}
}

func (h *HTTPHandler) handlePackages(w http.ResponseWriter, r *http.Request) {
	h.servePackages(w, r, "text/plain", "Packages", CompressionNone, GetPackagesPath)
}

func (h *HTTPHandler) handlePackagesGz(w http.ResponseWriter, r *http.Request) {
	h.servePackages(w, r, "application/gzip", "Packages.gz", CompressionGzip, GetPackagesGzPath)
}

func (h *HTTPHandler) handlePackagesXz(w http.ResponseWriter, r *http.Request) {
	h.servePackages(w, r, "application/x-xz", "Packages.xz", CompressionXZ, GetPackagesXzPath)
}

// servePackages serves a binary-<arch> Packages index in one compression: the
// stored blob when the repository has one, otherwise streamed from the database.
func (h *HTTPHandler) servePackages(w http.ResponseWriter, r *http.Request, contentType, label string, compression Compression, blobPath func(dist, comp, arch string) string) {
	vars := urlparams.Vars(r)
	dist := vars["dist"]
	comp := vars["comp"]
	arch := vars["arch"]
	name := vars["name"]

	repo, err := h.resolveRepo(r.Context(), name)
	if err != nil {
		h.log.Warnw("failed to resolve repository", "name", name, "error", err)
		http.NotFound(w, r)

		return
	}

	if h.tryServeRepoBlob(w, r, repo, blobPath(dist, comp, arch), contentType) {
		return
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-cache")

	if _, err := StreamPackages(r.Context(), h.db, repo.ID, dist, comp, arch, compression, w); err != nil {
		h.log.Errorw("failed to stream "+label, "dist", dist, "comp", comp, "arch", arch, "error", err)
	}
}

func (h *HTTPHandler) handlePoolFile(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	name := vars["name"]

	// chi hands out the raw path, so "libstdc%2b%2b6_1.deb" arrives encoded.
	filename, err := url.PathUnescape(vars["filename"])
	if err != nil || !pathseg.Name(filename) {
		http.NotFound(w, r)
		return
	}

	repoID, err := h.resolveRepoID(r.Context(), name)
	if err != nil {
		h.log.Warnw("failed to resolve repository", "name", name, "error", err)
		http.NotFound(w, r)

		return
	}

	comp, err := h.db.GetComponentByFilename(r.Context(), repoID, filename)
	if err != nil {
		h.log.Errorw("failed to look up component by filename", "filename", filename, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if comp == nil {
		http.NotFound(w, r)
		return
	}

	poolPath := "pool/" + vars["comp"] + "/" + vars["prefix"] + "/" + vars["pkg"] + "/" + filename
	if quarantine.Enforce(w, r, h.db, repoID, quarantine.PathKeys(comp.ID, poolPath, filename, r.URL.Path)...) {
		return
	}

	if presign.Try(h.blobStore, w, r, presign.Blob{SHA256: comp.SHA256, Size: comp.PackageSize, Filename: filename, ContentType: "application/vnd.debian.binary-package", Repo: name}) {
		return
	}

	// The blob is stored by SHA256 key (set via "sha256" metadata on Put)
	reader, err := h.blobStore.Get(r.Context(), comp.SHA256)
	if err != nil {
		h.log.Errorw("failed to read blob", "sha256", comp.SHA256, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}
	defer func() { _ = reader.Close() }()

	w.Header().Set("Content-Type", "application/vnd.debian.binary-package")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)

	if _, err := iocopy.Copy(w, reader); err != nil {
		h.log.Errorw("failed to write pool file response", "filename", filename, "error", err)
	}
}

func (h *HTTPHandler) handlePublicKey(w http.ResponseWriter, r *http.Request) {
	name := urlparams.Vars(r)["name"]

	repo, err := h.resolveRepo(r.Context(), name)
	if err != nil {
		h.log.Warnw("failed to resolve repository", "name", name, "error", err)
		http.NotFound(w, r)

		return
	}

	path := "public.key"
	if !h.tryServeRepoBlob(w, r, repo, path, "application/pgp-keys") {
		http.NotFound(w, r)
	}
}

func GetPoolPath(component, packageName, version, filename string) string {
	if packageName == "" {
		return ""
	}

	prefix := string(packageName[0])
	if strings.HasPrefix(packageName, "lib") && len(packageName) > 3 {
		prefix = "lib" + string(packageName[3])
	}

	return "pool/" + component + "/" + prefix + "/" + packageName + "/" + filename
}

func GetPackagesPath(distribution, component, architecture string) string {
	return "dists/" + distribution + "/" + component + "/binary-" + architecture + "/Packages"
}

func GetPackagesGzPath(distribution, component, architecture string) string {
	return "dists/" + distribution + "/" + component + "/binary-" + architecture + "/Packages.gz"
}

func GetPackagesXzPath(distribution, component, architecture string) string {
	return "dists/" + distribution + "/" + component + "/binary-" + architecture + "/Packages.xz"
}

func GetReleasePath(distribution string) string {
	return "dists/" + distribution + "/Release"
}

func GetInReleasePath(distribution string) string {
	return "dists/" + distribution + "/InRelease"
}

func GetReleaseGPGPath(distribution string) string {
	return "dists/" + distribution + "/Release.gpg"
}
