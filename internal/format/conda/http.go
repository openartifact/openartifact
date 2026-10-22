// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package conda

import (
	"bytes"
	"context"
	"crypto/md5" //nolint:gosec // required by protocol specification
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	"github.com/openartifact/openartifact/internal/licensepolicy"
	"github.com/openartifact/openartifact/internal/presign"
	"github.com/openartifact/openartifact/internal/quarantine"
	"github.com/openartifact/openartifact/internal/quota"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/trash"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/internal/webhook"
	"github.com/openartifact/openartifact/pkg/logger"
)

const maxCondaUploadBytes = 500 << 20

type HTTPHandler struct {
	*base.Handler
}

func NewHTTPHandler(log *logger.Logger, db database.Database, blobStore storage.BlobStore, baseURL string) *HTTPHandler {
	return &HTTPHandler{Handler: base.New(log, db, blobStore, baseURL)}
}

// RegisterRepoRoutes registers Conda-specific routes on the provided sub-router.
// It does NOT wrap in r.Route() — callers are responsible for the path prefix.
func (h *HTTPHandler) RegisterRepoRoutes(r chi.Router) {
	r.Get("/{platform}/repodata.json", h.handleRepodata)
	r.Get("/{platform}/{filename}", h.handleDownload)
	r.Post("/{platform}", h.handleUpload)
	r.Delete("/{platform}/{filename}", h.handleDelete)
}

func (h *HTTPHandler) RegisterRoutes(r chi.Router, prefix string) {
	r.Route(prefix, func(r chi.Router) {
		h.RegisterRepoRoutes(r)
	})
	h.Log.Infow("conda handler registered", "prefix", prefix)
}

// resolveRepo returns the ID of the repository named in the URL. When there is
// none (unknown repository or a lookup failure) it writes the response and
// returns "".
func (h *HTTPHandler) resolveRepo(w http.ResponseWriter, r *http.Request) string {
	repoName := urlparams.Vars(r)["name"]

	repoID, err := h.ResolveRepoID(r.Context(), repoName)
	if err != nil {
		h.Log.Errorw("failed to resolve repo", "name", repoName, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return ""
	}

	if repoID == "" {
		http.NotFound(w, r)
	}

	return repoID
}

// repodataEntry is the repodata.json record of one package file.
func repodataEntry(pkg *models.CondaPackage, platform string) map[string]any {
	depends := pkg.Depends
	if depends == nil {
		depends = []string{}
	}

	entry := map[string]any{
		"name":         pkg.Name,
		"version":      pkg.Version,
		"build":        pkg.BuildString,
		"build_number": pkg.BuildNumber,
		"depends":      depends,
		"sha256":       pkg.SHA256,
		"size":         pkg.SizeBytes,
		"subdir":       platform,
	}

	if pkg.MD5 != "" {
		entry["md5"] = pkg.MD5
	}

	if pkg.Timestamp != 0 {
		entry["timestamp"] = pkg.Timestamp
	}

	var extra map[string]json.RawMessage
	if json.Unmarshal(pkg.Extra, &extra) == nil {
		for k, v := range extra {
			if _, taken := entry[k]; !taken {
				entry[k] = v
			}
		}
	}

	return entry
}

func (h *HTTPHandler) handleRepodata(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	repoName := vars["name"]
	platform := vars["platform"]

	repoID := h.resolveRepo(w, r)
	if repoID == "" {
		return
	}

	// List all packages for this platform
	packages, err := h.DB.ListCondaPackages(r.Context(), repoID, platform)
	if err != nil {
		h.Log.Errorw("failed to list conda packages", "repo", repoName, "platform", platform, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	// .tar.bz2 files go under "packages", .conda files under "packages.conda".
	tarBz2 := make(map[string]any)
	conda := make(map[string]any)

	for _, pkg := range packages {
		if pkg.Extension == models.CondaExtConda {
			conda[pkg.Filename()] = repodataEntry(pkg, platform)
		} else {
			tarBz2[pkg.Filename()] = repodataEntry(pkg, platform)
		}
	}

	repodata := map[string]any{
		"info": map[string]any{
			"subdir": platform,
		},
		"packages":       tarBz2,
		"packages.conda": conda,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	if encErr := json.NewEncoder(w).Encode(repodata); encErr != nil {
		_ = encErr // response header already written
	}
}

func (h *HTTPHandler) handleDownload(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	platform := vars["platform"]
	filename := vars["filename"]

	file, ok := parsePackageFilename(filename)
	if !ok {
		http.NotFound(w, r)
		return
	}

	repoID := h.resolveRepo(w, r)
	if repoID == "" {
		return
	}

	pkg, err := h.DB.GetCondaPackageFile(r.Context(), repoID, file.name, file.version, file.build, platform, file.ext)
	if err != nil {
		h.Log.Errorw("failed to get conda package", "name", file.name, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if pkg == nil {
		http.NotFound(w, r)
		return
	}

	if h.DenyIfQuarantined(w, r, repoID, quarantine.PathKeys(filename, platform+"/"+filename, r.URL.Path)...) {
		return
	}

	if h.TryPresign(w, r, presign.Blob{SHA256: pkg.BlobKey, Size: pkg.SizeBytes, Filename: filename, ContentType: "application/octet-stream"}) {
		return
	}

	// Download blob
	blob, err := h.BlobStore.Get(r.Context(), pkg.BlobKey)
	if err != nil {
		h.Log.Errorw("failed to get blob", "key", pkg.BlobKey, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}
	defer func() { _ = blob.Close() }()

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(pkg.SizeBytes, 10))
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filename}))
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, blob)
}

// uploadFilename returns the file name an upload request names (query
// parameter or X-Filename header), or "".
func uploadFilename(r *http.Request) string {
	if filename := r.URL.Query().Get("filename"); filename != "" {
		return filename
	}

	return r.Header.Get("X-Filename")
}

func (h *HTTPHandler) handleUpload(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	repoName := vars["name"]
	platform := vars["platform"]

	repoID := h.resolveRepo(w, r)
	if repoID == "" {
		return
	}

	filename := uploadFilename(r)
	if filename == "" {
		http.Error(w, "filename required", http.StatusBadRequest)
		return
	}

	file, ok := parsePackageFilename(filename)
	if !ok {
		http.Error(w, "invalid filename format", http.StatusBadRequest)
		return
	}

	// Read and hash the file
	data, err := base.ReadAllLimited(r.Body, maxCondaUploadBytes)
	if err != nil {
		if errors.Is(err, base.ErrContentTooLarge) {
			http.Error(w, "upload too large", http.StatusRequestEntityTooLarge)

			return
		}
		h.Log.Errorw("failed to read upload", "error", err)
		http.Error(w, "failed to read upload", http.StatusBadRequest)

		return
	}
	defer func() { _ = r.Body.Close() }()

	idx := readPackageIndex(file.ext, data)
	if licensepolicy.Enforce(w, r, h.DB, h.Log, repoID, idx.License) {
		return
	}

	existing, err := h.DB.GetCondaPackageFile(r.Context(), repoID, file.name, file.version, file.build, platform, file.ext)
	if err != nil {
		h.Log.Errorw("failed to check existing conda package", "name", file.name, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if existing != nil {
		http.Error(w, "package file already exists", http.StatusConflict)
		return
	}

	pkg := newPackage(repoID, platform, file, idx, data)

	// Reserve quota before anything is persisted; every failure path below
	// returns the reservation.
	if err := quota.Enforce(r.Context(), h.DB, repoID, pkg.SizeBytes); err != nil {
		if errors.Is(err, quota.ErrExceeded) {
			quota.WriteExceeded(w)
			return
		}

		h.Log.Errorw("failed to reserve quota", "repo", repoName, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if err := h.storePackage(r.Context(), pkg, filename, data); err != nil {
		if relErr := quota.Release(r.Context(), h.DB, repoID, pkg.SizeBytes); relErr != nil {
			h.Log.Warnw("failed to release quota", "repo", repoName, "error", relErr)
		}

		if errors.Is(err, database.ErrDuplicate) || database.IsUniqueViolation(err) {
			http.Error(w, "package file already exists", http.StatusConflict)
			return
		}

		h.Log.Errorw("failed to store conda package", "name", file.name, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	h.Emit(r.Context(), repoID, webhook.EventArtifactUploaded, webhook.ArtifactPayload(webhook.EventArtifactUploaded, webhook.Artifact{
		Repository: repoName,
		Format:     "conda",
		Name:       file.name,
		Version:    file.version,
		Path:       platform + "/" + filename,
		SHA256:     pkg.SHA256,
		Size:       pkg.SizeBytes,
	}))

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)

	if encErr := json.NewEncoder(w).Encode(map[string]any{
		"id":      pkg.ID,
		"name":    pkg.Name,
		"version": pkg.Version,
		"sha256":  pkg.SHA256,
	}); encErr != nil {
		_ = encErr // response header already written
	}
}

// newPackage builds the database record for an uploaded package file.
func newPackage(repoID, platform string, file packageFile, idx packageIndex, data []byte) *models.CondaPackage {
	sha := sha256.Sum256(data)
	sum := md5.Sum(data) //nolint:gosec // required by protocol specification

	return &models.CondaPackage{
		RepositoryID: repoID,
		Name:         file.name,
		Version:      file.version,
		BuildString:  file.build,
		Platform:     platform,
		Extension:    file.ext,
		BuildNumber:  idx.BuildNumber,
		Timestamp:    idx.Timestamp,
		Depends:      idx.Depends,
		Extra:        idx.Extra,
		SizeBytes:    int64(len(data)),
		SHA256:       hex.EncodeToString(sha[:]),
		MD5:          hex.EncodeToString(sum[:]),
	}
}

// storePackage stores the blob and inserts the referencing row under the
// per-blob lock so a concurrent unpublish cannot delete the blob in between.
// When the row is not created (a concurrent upload of the same file won) the
// blob is dropped again unless something else references it.
func (h *HTTPHandler) storePackage(ctx context.Context, pkg *models.CondaPackage, filename string, data []byte) error {
	err := storage.WithBlobLock(ctx, h.DB, pkg.SHA256, func(ctx context.Context) error {
		blobKey, err := h.BlobStore.Put(ctx, bytes.NewReader(data), map[string]string{
			"sha256":   pkg.SHA256,
			"filename": filename,
			"platform": pkg.Platform,
		})
		if err != nil {
			return fmt.Errorf("store blob: %w", err)
		}

		pkg.BlobKey = blobKey

		if err := h.DB.CreateCondaPackage(ctx, pkg); err != nil {
			return fmt.Errorf("create conda package: %w", err)
		}

		return nil
	})
	if err == nil {
		return nil
	}

	if cleanErr := storage.SafeDeleteBlob(ctx, h.DB, h.BlobStore, pkg.SHA256); cleanErr != nil {
		h.Log.Warnw("failed to clean up conda blob", "sha256", pkg.SHA256, "error", cleanErr)
	}

	return err
}

func (h *HTTPHandler) handleDelete(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	repoName := vars["name"]
	platform := vars["platform"]
	filename := vars["filename"]

	file, ok := parsePackageFilename(filename)
	if !ok {
		http.NotFound(w, r)
		return
	}

	repoID := h.resolveRepo(w, r)
	if repoID == "" {
		return
	}

	pkg, err := h.DB.GetCondaPackageFile(r.Context(), repoID, file.name, file.version, file.build, platform, file.ext)
	if err != nil {
		h.Log.Errorw("failed to get conda package", "name", file.name, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if pkg == nil {
		http.NotFound(w, r)
		return
	}

	if gErr := trash.GuardReleaseByName(r, h.DB, repoName, "conda", pkg.Version); gErr != nil {
		if errors.Is(gErr, trash.ErrImmutableRelease) {
			http.Error(w, "repository has immutable releases", http.StatusConflict)
			return
		}

		h.Log.Errorw("failed to check release immutability", "name", file.name, "error", gErr)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	// Resolve the size to release before the blob can go away.
	size := pkg.SizeBytes
	if size <= 0 {
		if n, sizeErr := h.BlobStore.GetSize(r.Context(), pkg.BlobKey); sizeErr == nil {
			size = n
		}
	}

	err = trash.Delete(r.Context(), h.DB, trash.Request{
		RepoID:   repoID,
		Kind:     models.TrashKindConda,
		IDs:      []string{pkg.ID},
		Resource: platform + "/" + filename,
	}, func() error {
		if delErr := h.DB.DeleteCondaPackage(r.Context(), pkg.ID); delErr != nil {
			return fmt.Errorf("delete conda package: %w", delErr)
		}

		if relErr := quota.Release(r.Context(), h.DB, repoID, size); relErr != nil {
			h.Log.Warnw("failed to release quota", "repo", repoName, "error", relErr)
		}

		// The blob is content-addressed and may be shared; remove it only once
		// no row references it any more.
		if delErr := storage.SafeDeleteBlob(r.Context(), h.DB, h.BlobStore, pkg.SHA256); delErr != nil {
			h.Log.Warnw("failed to clean up blob after conda delete", "sha256", pkg.SHA256, "error", delErr)
		}

		return nil
	})
	if err != nil {
		if errors.Is(err, database.ErrTrashNotFound) {
			http.NotFound(w, r)
			return
		}

		h.Log.Errorw("failed to delete conda package", "id", pkg.ID, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	h.Emit(r.Context(), repoID, webhook.EventArtifactDeleted, webhook.ArtifactPayload(webhook.EventArtifactDeleted, webhook.Artifact{
		Repository: repoName,
		Format:     "conda",
		Name:       pkg.Name,
		Version:    pkg.Version,
		Path:       platform + "/" + filename,
		SHA256:     pkg.SHA256,
		Size:       pkg.SizeBytes,
	}))

	w.WriteHeader(http.StatusNoContent)
}
