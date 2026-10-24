// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package rpm

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
	"github.com/openartifact/openartifact/internal/presign"
	"github.com/openartifact/openartifact/internal/quarantine"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/pkg/logger"
)

type HTTPHandler struct {
	name      string
	log       *logger.Logger
	db        db.Database
	blobStore storage.BlobStore
}

func NewHTTPHandler(log *logger.Logger, database db.Database, blobStore storage.BlobStore) *HTTPHandler {
	return &HTTPHandler{
		name:      "rpm-repository",
		log:       log,
		db:        database,
		blobStore: blobStore,
	}
}

func (h *HTTPHandler) Name() string {
	return h.name
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

// How long shared caches may reuse a public repository's metadata before
// revalidating. Content-addressed repodata files never change under their name.
const (
	metadataMaxAge  = 300
	repodataMaxAge  = 86400
	publicKeyMaxAge = 86400
	packageMaxAge   = 3600
)

// tryServeRepoBlob attempts to serve a pre-generated metadata blob. The blob
// digest is the ETag, so a matching If-None-Match is answered without reading
// the blob. Returns true if served (or an error was written), false if not
// found; nothing is written to w in the not-found case.
func (h *HTTPHandler) tryServeRepoBlob(w http.ResponseWriter, r *http.Request, repo *models.Repository, path string, contentType string, maxAge int) bool {
	meta, err := h.db.GetRepoMetadataByPath(r.Context(), repo.ID, path)
	if err != nil {
		h.log.Errorw("failed to query repo metadata", "path", path, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return true
	}

	if meta == nil {
		return false
	}

	if base.Revalidate(w, r, repo.Public, `"`+meta.BlobSHA256+`"`, maxAge) {
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

// RegisterRepoRoutes registers RPM-specific routes on the provided sub-router.
// It does NOT wrap in r.Route() — callers are responsible for the path prefix.
func (h *HTTPHandler) RegisterRepoRoutes(r chi.Router) {
	r.Get("/repodata/repomd.xml", h.handleRepomdXML)
	r.Get("/repodata/repomd.xml.asc", h.handleRepomdXMLAsc)
	r.Get("/repodata/comps.xml", h.handleComps)
	r.Get("/repodata/{checksum}-{type}.xml.gz", h.handleMetadataFile)
	r.Get("/repodata/{checksum}-{type}.xml.xz", h.handleMetadataFile)
	r.Get("/Packages/{filename}", h.handlePackageDownload)
	r.Get("/public.key", h.handlePublicKey)
}

// RegisterRoutes registers the RPM handler under a chi router prefix.
//
// Deprecated: prefer using RegisterRepoRoutes inside a caller-managed r.Route().
func (h *HTTPHandler) RegisterRoutes(r chi.Router, prefix string) {
	r.Route(prefix, func(r chi.Router) {
		h.RegisterRepoRoutes(r)
	})
	h.log.Infow("RPM repository HTTP handler registered", "prefix", prefix)
}

func (h *HTTPHandler) handleRepomdXML(w http.ResponseWriter, r *http.Request) {
	repo, err := h.resolveRepo(r.Context(), urlparams.Vars(r)["name"])
	if err != nil {
		http.NotFound(w, r)
		return
	}

	if h.tryServeRepoBlob(w, r, repo, "repodata/repomd.xml", "application/xml", metadataMaxAge) {
		return
	}
	// Metadata has not been generated yet; clients must retry rather than
	// receive a repomd.xml without valid checksums.
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Retry-After", "5")
	http.Error(w, "repository metadata is being generated", http.StatusServiceUnavailable)
}

func (h *HTTPHandler) handleRepomdXMLAsc(w http.ResponseWriter, r *http.Request) {
	repo, err := h.resolveRepo(r.Context(), urlparams.Vars(r)["name"])
	if err != nil {
		http.NotFound(w, r)
		return
	}

	if !h.tryServeRepoBlob(w, r, repo, "repodata/repomd.xml.asc", "application/pgp-signature", metadataMaxAge) {
		http.NotFound(w, r)
	}
}

func (h *HTTPHandler) handleMetadataFile(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)

	repo, err := h.resolveRepo(r.Context(), vars["name"])
	if err != nil {
		http.NotFound(w, r)
		return
	}

	checksum := vars["checksum"]
	metaType := vars["type"]

	isGZ := strings.HasSuffix(r.URL.Path, ".gz")

	contentType := "application/x-xz"
	if isGZ {
		contentType = "application/x-gzip"
	}

	ext := ".xz"
	if isGZ {
		ext = ".gz"
	}

	path := fmt.Sprintf("repodata/%s-%s.xml%s", checksum, metaType, ext)

	// The checksum is part of the file name: the content behind it never changes.
	if h.tryServeRepoBlob(w, r, repo, path, contentType, repodataMaxAge) {
		return
	}

	http.NotFound(w, r)
}

func (h *HTTPHandler) handlePackageDownload(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)

	repo, err := h.resolveRepo(r.Context(), vars["name"])
	if err != nil {
		http.NotFound(w, r)
		return
	}

	repoID := repo.ID
	// chi hands out the raw path; primary.xml hrefs are percent-encoded.
	filename, err := url.PathUnescape(vars["filename"])
	if err != nil || filename == "" || strings.ContainsAny(filename, "/\\") {
		http.NotFound(w, r)
		return
	}

	comp, err := h.db.GetComponentByFilename(r.Context(), repoID, filename)
	if err != nil {
		h.log.Errorw("failed to look up component", "filename", filename, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if comp == nil {
		http.NotFound(w, r)
		return
	}

	if quarantine.Enforce(w, r, h.db, repoID, quarantine.PathKeys(comp.ID, "Packages/"+filename, filename, r.URL.Path)...) {
		return
	}

	// Package files are content-addressed: the blob digest is a complete
	// validator, and a public repository's packages may be reused for an hour.
	if base.Revalidate(w, r, repo.Public, `"`+comp.SHA256+`"`, packageMaxAge) {
		return
	}

	if presign.Try(h.blobStore, w, r, presign.Blob{SHA256: comp.SHA256, Size: comp.PackageSize, Filename: filename, ContentType: "application/x-rpm", Repository: repo}) {
		return
	}

	reader, err := h.blobStore.Get(r.Context(), comp.SHA256)
	if err != nil {
		h.log.Errorw("failed to read package blob", "artifact_id", comp.ArtifactID, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}
	defer func() { _ = reader.Close() }()

	w.Header().Set("Content-Type", "application/x-rpm")

	if _, err := iocopy.Copy(w, reader); err != nil {
		h.log.Errorw("failed to write package response", "filename", filename, "error", err)
	}
}

func (h *HTTPHandler) handleComps(w http.ResponseWriter, r *http.Request) {
	repo, err := h.resolveRepo(r.Context(), urlparams.Vars(r)["name"])
	if err != nil {
		http.NotFound(w, r)
		return
	}

	if !h.tryServeRepoBlob(w, r, repo, "comps.xml", "application/xml", metadataMaxAge) {
		http.NotFound(w, r)
	}
}

func (h *HTTPHandler) handlePublicKey(w http.ResponseWriter, r *http.Request) {
	repo, err := h.resolveRepo(r.Context(), urlparams.Vars(r)["name"])
	if err != nil {
		http.NotFound(w, r)
		return
	}

	if !h.tryServeRepoBlob(w, r, repo, "public.key", "application/pgp-keys", publicKeyMaxAge) {
		http.NotFound(w, r)
	}
}
