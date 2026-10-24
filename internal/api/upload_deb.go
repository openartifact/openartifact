// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"
	database "github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	formatdeb "github.com/openartifact/openartifact/internal/format/deb"
	"github.com/openartifact/openartifact/internal/importmeta"
	"github.com/openartifact/openartifact/internal/iocopy"
	"github.com/openartifact/openartifact/internal/licensepolicy"
	"github.com/openartifact/openartifact/internal/metrics"
	"github.com/openartifact/openartifact/internal/quota"
	"github.com/openartifact/openartifact/internal/repotype"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/trash"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/internal/webhook"
	"github.com/openartifact/openartifact/internal/worker"
	pkgdeb "github.com/openartifact/openartifact/pkg/deb"
	"github.com/openartifact/openartifact/pkg/logger"
)

const (
	maxDebUploadSize = 500 * 1024 * 1024
	// multipartOverhead leaves room for form fields and boundaries on top of the file.
	multipartOverhead = 1 << 20
	// multipartMemory is the in-memory threshold; larger parts spill to disk.
	multipartMemory = 32 << 20
)

var (
	errDebInvalid      = errors.New("invalid deb package")
	errDebWrongFormat  = errors.New("repository is not a deb repository")
	errDebNotHosted    = errors.New("repository is not hosted")
	errDebRepoNotFound = errors.New("repository not found")
	debNameRe          = regexp.MustCompile(`^[a-z0-9][a-z0-9+.\-]+$`)
	debVersionRe       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.+~:\-]*$`)
	debArchRe          = regexp.MustCompile(`^[a-z0-9][a-z0-9\-]*$`)
	debSegmentRe       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._\-]*$`)
)

type DebUploadHandler struct {
	log       *logger.Logger
	db        database.Database
	blobStore storage.BlobStore
	reg       *metrics.Registry
	debWorker *worker.DebMetadataWorker
	publisher webhook.Publisher
}

// SetPublisher wires the webhook publisher used for artifact events.
func (h *DebUploadHandler) SetPublisher(p webhook.Publisher) {
	h.publisher = p
}

func NewDebUploadHandler(log *logger.Logger, db database.Database, blobStore storage.BlobStore, reg *metrics.Registry) *DebUploadHandler {
	return &DebUploadHandler{
		log:       log,
		db:        db,
		blobStore: blobStore,
		reg:       reg,
	}
}

func (h *DebUploadHandler) SetDebWorker(w *worker.DebMetadataWorker) {
	h.debWorker = w
}

func (h *DebUploadHandler) Register(router chi.Router) {
	router.Post("/api/v1/repositories/{name}/debs", h.handleUpload)
	router.Delete("/api/v1/repositories/{name}/debs/{id}", h.handleDelete)
	h.log.Infow("Deb upload handler registered")
}

func (h *DebUploadHandler) handleUpload(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	repoName := vars["name"]

	uploadResult := "success"

	if h.reg != nil {
		defer func() { h.reg.CountUpload(metrics.FormatDeb, uploadResult) }()
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxDebUploadSize+multipartOverhead)

	err := r.ParseMultipartForm(multipartMemory) //nolint:gosec // r.Body is bounded by http.MaxBytesReader above
	if err != nil {
		uploadResult = "error"

		writeError(w, http.StatusBadRequest, "INVALID_INPUT", fmt.Sprintf("failed to parse form: %v", err))

		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		uploadResult = "error"

		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "missing file field")

		return
	}
	defer func() { base.CloseLog(file, h.log) }()

	if header.Size > maxDebUploadSize {
		uploadResult = "error"

		writeError(w, http.StatusRequestEntityTooLarge, "INVALID_INPUT", "file exceeds maximum size limit")

		return
	}

	distribution := r.FormValue("distribution")
	if distribution == "" {
		distribution = "stable"
	}

	component := r.FormValue("component")
	if component == "" {
		component = "main"
	}

	if !debSegmentRe.MatchString(distribution) || !debSegmentRe.MatchString(component) {
		uploadResult = "error"

		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "invalid distribution or component name")

		return
	}

	result, err := h.uploadPipeline(w, r, file, repoName, distribution, component)
	if err != nil {
		uploadResult = "error"

		switch {
		case errors.Is(err, errResponseWritten):
			// licensepolicy.Enforce already answered.
		case errors.Is(err, errDebRepoNotFound):
			writeError(w, http.StatusNotFound, "NOT_FOUND", "repository not found")
		case errors.Is(err, errDebWrongFormat):
			writeError(w, http.StatusBadRequest, "INVALID_INPUT", "repository is not a deb repository")
		case errors.Is(err, errDebNotHosted):
			writeError(w, http.StatusMethodNotAllowed, "INVALID_INPUT", "proxy and group repositories are read-only")
		case errors.Is(err, errDebInvalid):
			writeError(w, http.StatusBadRequest, "INVALID_INPUT", err.Error())
		case errors.Is(err, quota.ErrExceeded):
			quota.WriteExceeded(w)
		default:
			h.log.Errorw("Deb upload failed", "repository", repoName, "filename", header.Filename, "error", err)
			writeError(w, http.StatusInternalServerError, "UPLOAD_ERROR", "upload processing failed")
		}

		return
	}

	if dup, _ := result["duplicate"].(bool); dup {
		uploadResult = "error"

		writeJSON(w, http.StatusConflict, Response{
			Success: false,
			Error:   "artifact already exists",
			Code:    "CONFLICT",
			Data: map[string]any{
				"duplicate": true,
			},
		})

		return
	}

	h.log.Infow("Deb package uploaded", "repository", repoName, "package", result["name"], "version", result["version"])
	writeJSON(w, http.StatusOK, Response{
		Success: true,
		Data:    result,
	})
}

// debControlLicense returns the control stanza's `License:` field, matched
// case-insensitively like all control field names. It is usually absent.
func debControlLicense(cf *pkgdeb.ControlFile) string {
	if cf == nil {
		return ""
	}

	for name, value := range cf.Fields {
		if strings.EqualFold(name, "License") {
			return strings.TrimSpace(value)
		}
	}

	return ""
}

func (h *DebUploadHandler) resolveRepoID(r *http.Request, repoName string) (string, error) {
	repo, err := h.db.GetRepositoryByName(r.Context(), repoName)
	if err != nil {
		return "", fmt.Errorf("failed to look up repository: %w", err)
	}

	if repo == nil {
		return "", fmt.Errorf("%w: %s", errDebRepoNotFound, repoName)
	}

	if repo.Format != "deb" {
		return "", errDebWrongFormat
	}

	if repo.RepoType != "" && repo.RepoType != repotype.Hosted {
		return "", errDebNotHosted
	}

	return repo.ID, nil
}

//nolint:gocyclo,cyclop // upload pipeline: sequential validation and cleanup branches
func (h *DebUploadHandler) uploadPipeline(w http.ResponseWriter, r *http.Request, file io.Reader, repoName string, distribution string, component string) (map[string]any, error) {
	repoID, err := h.resolveRepoID(r, repoName)
	if err != nil {
		return nil, err
	}

	// Refuse a body the quota cannot take before it is spooled to disk.
	if err := quota.Precheck(r.Context(), h.db, repoID, r.ContentLength); err != nil {
		return nil, err
	}

	// Stream to temp file while computing SHA256
	tmpFile, err := os.CreateTemp("", "deb-upload-*.tmp")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp file: %w", err)
	}
	defer func() { _ = os.Remove(tmpFile.Name()) }()
	defer func() { base.CloseLog(tmpFile, h.log) }()

	hasher := sha256.New()

	written, err := iocopy.Copy(tmpFile, io.TeeReader(file, hasher))
	if err != nil {
		return nil, fmt.Errorf("failed to buffer upload: %w", err)
	}

	sha256sum := hex.EncodeToString(hasher.Sum(nil))

	// Per-repo duplicate check: same package already in this repo.
	exists, err := h.db.ArtifactExists(r.Context(), sha256sum, repoID)
	if err != nil {
		return nil, fmt.Errorf("failed to check artifact existence: %w", err)
	}

	if exists {
		return map[string]any{"duplicate": true}, nil
	}

	// Parse from the temp file via ReadAt: only the control archive is loaded.
	debFile, err := pkgdeb.ParseDebFileAt(tmpFile, written)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errDebInvalid, err) //nolint:errorlint // keep parser detail for the client message
	}

	pkg := debFile.Package

	if !debNameRe.MatchString(pkg.Package) || !debVersionRe.MatchString(pkg.Version) || !debArchRe.MatchString(pkg.Architecture) {
		return nil, fmt.Errorf("%w: package name, version or architecture contains invalid characters", errDebInvalid)
	}

	// Debian file names never contain the epoch.
	fileVersion := pkg.Version
	if i := strings.Index(fileVersion, ":"); i >= 0 {
		fileVersion = fileVersion[i+1:]
	}

	filename := fmt.Sprintf("%s_%s_%s.deb", pkg.Package, fileVersion, pkg.Architecture)

	// Same name/version/arch in this repo with different content is a conflict (immutability).
	if prev, err := h.db.GetDebianPackageByFilename(r.Context(), repoID, filename); err != nil {
		return nil, fmt.Errorf("failed to check existing package: %w", err)
	} else if prev != nil {
		return map[string]any{"duplicate": true}, nil
	}

	debLicense := debControlLicense(debFile.ControlFile)
	if licensepolicy.Enforce(w, r, h.db, h.log, repoID, debLicense) {
		return nil, errResponseWritten
	}

	if err := quota.Enforce(r.Context(), h.db, repoID, written); err != nil {
		return nil, err
	}

	quotaReserved := true
	releaseQuota := func() {
		if quotaReserved {
			if err := quota.Release(r.Context(), h.db, repoID, written); err != nil {
				h.log.Warnw("failed to release quota", "repository", repoName, "error", err)
			}
		}
	}

	dist, err := h.db.GetOrCreateDebianDistribution(r.Context(), repoID, distribution)
	if err != nil {
		releaseQuota()
		return nil, fmt.Errorf("failed to get distribution: %w", err)
	}

	comp, err := h.db.GetOrCreateDebianComponent(r.Context(), dist.ID, component)
	if err != nil {
		releaseQuota()
		return nil, fmt.Errorf("failed to get component: %w", err)
	}

	if _, err := h.db.GetOrCreateDebianArchitecture(r.Context(), dist.ID, pkg.Architecture); err != nil {
		releaseQuota()
		return nil, fmt.Errorf("failed to get architecture: %w", err)
	}

	description := pkg.Description
	if idx := strings.Index(description, "\n"); idx != -1 {
		description = description[:idx]
	}

	var (
		blobID     string
		blobExists bool
		duplicate  bool
		record     *models.Component
	)

	// Dedup check, blob store, blob record and referencing row happen under the
	// blob lock so a concurrent unpublish cannot delete the blob in between.
	lockErr := storage.WithBlobLock(r.Context(), h.db, sha256sum, func(ctx context.Context) error {
		var err error

		blobID, blobExists, err = h.blobStore.ExistsBySHA256(ctx, sha256sum)
		if err != nil {
			return fmt.Errorf("failed to check blob existence: %w", err)
		}

		if !blobExists {
			if _, err := tmpFile.Seek(0, io.SeekStart); err != nil {
				return fmt.Errorf("failed to seek temp file: %w", err)
			}

			blobID, err = h.blobStore.Put(ctx, tmpFile, map[string]string{
				"sha256":    sha256sum,
				"format":    "deb",
				"repo_name": repoName,
			})
			if err != nil {
				return fmt.Errorf("failed to store blob: %w", err)
			}
		}

		// Upsert into blobs table to get the UUID for blob_id FK reference.
		blobUUID, err := h.db.UpsertBlob(ctx, sha256sum, sha256sum, written)
		if err != nil {
			return fmt.Errorf("failed to upsert blob record: %w", err)
		}

		record = &models.Component{
			RepositoryID:  repoID,
			ArtifactID:    blobUUID,
			Name:          pkg.Package,
			Version:       pkg.Version,
			Arch:          pkg.Architecture,
			Summary:       description,
			Description:   pkg.Description,
			License:       debLicense,
			InstalledSize: pkg.InstalledSize,
			Filename:      filename,
			// Zero unless an administrator supplied an origin timestamp.
			CreatedAt: importmeta.CreatedAt(ctx),
		}

		if err := h.db.CreateDebPackage(ctx, record, dist.ID, comp.ID); err != nil {
			if errors.Is(err, database.ErrDuplicate) {
				duplicate = true
				return nil
			}

			return fmt.Errorf("failed to create component record: %w", err)
		}

		// Persist the control stanza so Packages entries carry Depends, Maintainer, etc.
		if err := h.db.SetDebControlFields(ctx, record.ID, pkg.Maintainer, pkg.Homepage,
			formatdeb.ControlFieldsToStore(debFile.ControlFile.Fields)); err != nil {
			if delErr := h.db.DeleteComponent(ctx, record.ID); delErr != nil {
				h.log.Warnw("failed to roll back deb package after control persist error", "id", record.ID, "error", delErr)
			}

			return fmt.Errorf("failed to store control fields: %w", err)
		}

		return nil
	})

	// Failure/duplicate cleanup runs outside the lock (SafeDeleteBlob takes it).
	if lockErr != nil || duplicate {
		releaseQuota()

		if !blobExists {
			if delErr := storage.SafeDeleteBlob(r.Context(), h.db, h.blobStore, sha256sum); delErr != nil {
				h.log.Warnw("failed to clean up blob after failed upload", "sha256", sha256sum, "error", delErr)
			}
		}

		if lockErr != nil {
			return nil, lockErr
		}

		return map[string]any{"duplicate": true}, nil
	}

	quotaReserved = false

	if h.debWorker != nil {
		h.debWorker.NotifyStale(repoID)
	}

	webhook.Emit(r.Context(), h.publisher, repoID, webhook.EventArtifactUploaded, webhook.ArtifactPayload(webhook.EventArtifactUploaded, webhook.Artifact{
		Repository: repoName, Format: "deb", Name: pkg.Package, Version: pkg.Version,
		Path: filename, SHA256: sha256sum, Size: written,
	}))

	return map[string]any{
		"id":           record.ID,
		"name":         pkg.Package,
		"version":      pkg.Version,
		"architecture": pkg.Architecture,
		"sha256":       sha256sum,
		"blob_id":      blobID,
		"size":         written,
		"distribution": distribution,
		"component":    component,
	}, nil
}

// hardDelete permanently removes a deb package row, releases its quota and
// drops the blob when nothing else references it.
func (h *DebUploadHandler) hardDelete(ctx context.Context, repoID, repoName, packageID, sha string, size int64) error {
	if err := h.db.DeleteComponent(ctx, packageID); err != nil {
		return err
	}

	if sha != "" {
		if err := quota.Release(ctx, h.db, repoID, size); err != nil {
			h.log.Warnw("failed to release quota", "repository", repoName, "error", err)
		}

		// Remove from storage only when no other row references the sha256.
		if err := storage.SafeDeleteBlob(ctx, h.db, h.blobStore, sha); err != nil {
			h.log.Warnw("failed to delete blob", "sha256", sha, "error", err)
		}
	}

	return nil
}

// writeDeleteError maps a delete failure to its HTTP answer.
func (h *DebUploadHandler) writeDeleteError(w http.ResponseWriter, err error, packageID string) {
	switch {
	case errors.Is(err, database.ErrTrashNotFound):
		writeError(w, http.StatusNotFound, "NOT_FOUND", "package not found")
	case errors.Is(err, trash.ErrImmutableRelease):
		writeError(w, http.StatusConflict, "IMMUTABLE_RELEASE", "release versions are immutable in this repository")
	default:
		h.log.Errorw("failed to delete component", "package_id", packageID, "error", err)
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "failed to delete package")
	}
}

func (h *DebUploadHandler) handleDelete(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	repoName := vars["name"]
	packageID := vars["id"]

	repoID, err := h.resolveRepoID(r, repoName)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "repository not found: "+repoName)
		return
	}

	comp, err := h.db.GetDebPackageByID(r.Context(), packageID)
	if err != nil {
		h.log.Errorw("failed to look up component", "package_id", packageID, "error", err)
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "failed to look up package")

		return
	}

	// A package in another repository is reported as not found (no IDOR).
	if comp == nil || comp.RepositoryID != repoID {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "package not found")
		return
	}

	var (
		sha  string
		size int64
	)

	if comp.BlobID != "" {
		sha, size, err = h.db.GetDebBlobInfo(r.Context(), comp.BlobID)
		if err != nil {
			h.log.Errorw("failed to resolve blob sha256", "blob_id", comp.BlobID, "error", err)
			writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "failed to delete package")

			return
		}
	}

	if err := trash.GuardReleaseByName(r, h.db, repoName, "deb", comp.Version); err != nil {
		h.writeDeleteError(w, err, packageID)
		return
	}

	err = trash.Delete(r.Context(), h.db, trash.Request{
		RepoID: repoID, Kind: models.TrashKindDeb, IDs: []string{packageID}, Resource: comp.Filename,
	}, func() error {
		return h.hardDelete(r.Context(), repoID, repoName, packageID, sha, size)
	})
	if err != nil {
		h.writeDeleteError(w, err, packageID)
		return
	}

	if h.debWorker != nil {
		h.debWorker.NotifyStale(repoID)
	}

	webhook.Emit(r.Context(), h.publisher, repoID, webhook.EventArtifactDeleted, webhook.ArtifactPayload(webhook.EventArtifactDeleted, webhook.Artifact{
		Repository: repoName, Format: "deb", Name: comp.Name, Version: comp.Version,
		Path: comp.Filename, SHA256: sha, Size: size,
	}))

	h.log.Infow("Deb package deleted", "repository", repoName, "package_id", packageID)

	writeJSON(w, http.StatusOK, Response{
		Success: true,
		Data: map[string]any{
			"repository": repoName,
			"package_id": packageID,
		},
	})
}
