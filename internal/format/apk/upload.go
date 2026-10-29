// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package apk

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	"github.com/openartifact/openartifact/internal/httpx"
	"github.com/openartifact/openartifact/internal/importmeta"
	"github.com/openartifact/openartifact/internal/licensepolicy"
	"github.com/openartifact/openartifact/internal/metrics"
	"github.com/openartifact/openartifact/internal/quota"
	"github.com/openartifact/openartifact/internal/repotype"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/trash"
	"github.com/openartifact/openartifact/internal/webhook"
	pkgapk "github.com/openartifact/openartifact/pkg/apk"
)

const (
	// multipartMemory is how much of a multipart upload is buffered in memory.
	multipartMemory = 8 << 20
	// multipartOverhead is the slack the form framing adds to maxUploadBytes.
	multipartOverhead = 1 << 20

	// noarch is the architecture of packages that run anywhere; they are stored in an explicit arch directory.
	noarch = "noarch"
)

// RegisterAPIRoutes registers the web UI upload endpoint on the authenticated REST router.
func (h *HTTPHandler) RegisterAPIRoutes(r chi.Router) {
	r.Post("/api/v1/repositories/{name}/apks", h.handleRESTUpload)
	h.Log.Infow("apk upload handler registered")
}

// invalidError is a client error of an upload (a 400 with its message).
type invalidError struct{ msg string }

func (e *invalidError) Error() string { return e.msg }

func invalidf(format string, args ...any) error {
	return &invalidError{msg: fmt.Sprintf(format, args...)}
}

var (
	// errWritten means the response was already written (license policy).
	errWritten = errors.New("response written")
	errExists  = errors.New("package file already exists")
)

// ingestRequest is one package upload.
type ingestRequest struct {
	repo *models.Repository
	// branch and repoDir address the target directory; arch is the architecture directory ("" derives it from the package).
	branch, repoDir, arch string
	// filename is the file name the client named; "" accepts the canonical one.
	filename string
	body     *base.Spool
}

// ingest validates and stores one package and schedules the index regeneration.
func (h *HTTPHandler) ingest(w http.ResponseWriter, r *http.Request, in ingestRequest) (*models.ApkPackage, error) {
	ctx := r.Context()

	pkg, err := pkgapk.ParsePackage(in.body.Reader(), in.body.Size())
	if err != nil {
		if errors.Is(err, pkgapk.ErrInvalidPackage) {
			return nil, invalidf("%v", err)
		}

		return nil, fmt.Errorf("parse apk: %w", err)
	}

	arch, err := in.directoryArch(pkg)
	if err != nil {
		return nil, err
	}

	if licensepolicy.Enforce(w, r, h.DB, h.Log, in.repo.ID, pkg.License) {
		return nil, errWritten
	}

	existing, err := h.DB.GetApkPackageFile(ctx, in.repo.ID, in.branch, in.repoDir, arch, pkg.Name, pkg.Version)
	if err != nil {
		return nil, fmt.Errorf("check existing apk package: %w", err)
	}

	if existing != nil {
		return nil, errExists
	}

	row := newRow(in, arch, pkg)

	if err := quota.Enforce(ctx, h.DB, in.repo.ID, row.SizeBytes); err != nil {
		return nil, err //nolint:wrapcheck // quota.ErrExceeded is matched by the callers
	}

	if err := h.store(r, row, in.body); err != nil {
		h.ReleaseQuota(ctx, in.repo.ID, in.repo.Name, row.SizeBytes)

		if errors.Is(err, database.ErrDuplicate) || database.IsUniqueViolation(err) {
			return nil, errExists
		}

		return nil, err
	}

	h.notify(in.repo.ID)

	path := in.branch + "/" + in.repoDir + "/" + arch + "/" + pkg.FileName()
	h.Emit(ctx, in.repo.ID, webhook.EventArtifactUploaded, webhook.ArtifactPayload(webhook.EventArtifactUploaded, webhook.Artifact{
		Repository: in.repo.Name,
		Format:     "apk",
		Name:       pkg.Name,
		Version:    pkg.Version,
		Path:       path,
		SHA256:     row.BlobSHA256,
		Size:       row.SizeBytes,
	}))

	return row, nil
}

// directoryArch validates where pkg may be stored and returns its architecture directory.
func (in ingestRequest) directoryArch(pkg *pkgapk.Package) (string, error) {
	arch := in.arch

	switch {
	case arch == "" && pkg.Arch == noarch:
		return "", invalidf("a noarch package needs the architecture directory to be stored in")
	case arch == "":
		arch = pkg.Arch
	case arch != pkg.Arch && pkg.Arch != noarch:
		return "", invalidf("package architecture %q does not match the %q directory", pkg.Arch, arch)
	}

	if !pkgapk.ValidSegment(arch) || !pkgapk.ValidSegment(in.branch) || !pkgapk.ValidSegment(in.repoDir) {
		return "", invalidf("invalid branch, repo or arch name")
	}

	if in.filename != "" && in.filename != pkg.FileName() {
		return "", invalidf("file name %q does not match the package, expected %q", in.filename, pkg.FileName())
	}

	return arch, nil
}

func newRow(in ingestRequest, arch string, pkg *pkgapk.Package) *models.ApkPackage {
	return &models.ApkPackage{
		RepositoryID:     in.repo.ID,
		Branch:           in.branch,
		Repo:             in.repoDir,
		Arch:             arch,
		PkgArch:          pkg.Arch,
		Name:             pkg.Name,
		Version:          pkg.Version,
		Checksum:         pkg.Checksum,
		DataHash:         pkg.DataHash,
		Description:      pkg.Description,
		URL:              pkg.URL,
		License:          pkg.License,
		Origin:           pkg.Origin,
		Maintainer:       pkg.Maintainer,
		Commit:           pkg.Commit,
		BuildTime:        pkg.BuildTime,
		InstalledSize:    pkg.InstalledSize,
		Depends:          pkg.Depends,
		Provides:         pkg.Provides,
		InstallIf:        pkg.InstallIf,
		ProviderPriority: pkg.ProviderPriority,
		BlobSHA256:       in.body.SHA256Hex(),
		SizeBytes:        in.body.Size(),
	}
}

// store writes the blob, then the referencing row under the per-blob lock so a
// concurrent delete cannot remove the blob in between. A blob nothing ends up
// referencing is dropped again.
func (h *HTTPHandler) store(r *http.Request, row *models.ApkPackage, body *base.Spool) error {
	ctx := r.Context()

	blob := storage.SeekerBlob(row.BlobSHA256, body.Reader(), map[string]string{
		"sha256":   row.BlobSHA256,
		"filename": row.Name + "-" + row.Version + ".apk",
		"arch":     row.Arch,
	})

	err := storage.CommitBlob(ctx, h.DB, h.BlobStore, blob, func(ctx context.Context, blobKey string) error {
		row.BlobKey = blobKey
		row.CreatedAt = importmeta.CreatedAt(ctx)

		if err := h.DB.CreateApkPackage(ctx, row); err != nil {
			return fmt.Errorf("create apk package: %w", err)
		}

		return nil
	})
	if err == nil {
		return nil
	}

	if cleanErr := storage.SafeDeleteBlob(ctx, h.DB, h.BlobStore, row.BlobSHA256); cleanErr != nil {
		h.Log.Warnw("failed to clean up apk blob", "sha256", row.BlobSHA256, "error", cleanErr)
	}

	return err
}

// writeIngestError answers a failed ingest. It returns the HTTP status for metrics.
func (h *HTTPHandler) writeIngestError(w http.ResponseWriter, err error, rest bool) int {
	var invalid *invalidError

	status, code, msg := http.StatusInternalServerError, "UPLOAD_ERROR", "upload processing failed"

	switch {
	case errors.Is(err, errWritten):
		return 0
	case errors.As(err, &invalid):
		status, code, msg = http.StatusBadRequest, "INVALID_INPUT", invalid.msg
	case errors.Is(err, errExists):
		status, code, msg = http.StatusConflict, "CONFLICT", errExists.Error()
	case errors.Is(err, quota.ErrExceeded):
		quota.WriteExceeded(w)
		return http.StatusInsufficientStorage
	case errors.Is(err, base.ErrContentTooLarge):
		status, code, msg = http.StatusRequestEntityTooLarge, "INVALID_INPUT", "upload too large"
	default:
		h.Log.Errorw("apk upload failed", "error", err)
	}

	if rest {
		httpx.WriteError(w, status, code, msg)
	} else {
		http.Error(w, msg, status)
	}

	return status
}

func (h *HTTPHandler) hostedAPKRepo(w http.ResponseWriter, r *http.Request, rest bool) *models.Repository {
	repo := h.resolveRepo(w, r)
	if repo == nil {
		return nil
	}

	if repo.Format != "apk" || (repo.RepoType != "" && repo.RepoType != repotype.Hosted) {
		msg := "not a hosted apk repository"
		if rest {
			httpx.WriteError(w, http.StatusBadRequest, "INVALID_INPUT", msg)
		} else {
			http.Error(w, msg, http.StatusMethodNotAllowed)
		}

		return nil
	}

	return repo
}

// handlePut stores the package PUT at <branch>/<repo>/<arch>/<name>-<version>.apk.
func (h *HTTPHandler) handlePut(w http.ResponseWriter, r *http.Request) {
	status := http.StatusCreated

	defer func() {
		if h.metrics != nil && status != 0 {
			h.metrics.CountUpload(metrics.FormatApk, metrics.UploadResult(status))
		}
	}()

	t, ok := parseTarget(relPath(r))
	if !ok || t.file == IndexName {
		status = http.StatusNotFound
		http.NotFound(w, r)

		return
	}

	repo := h.hostedAPKRepo(w, r, false)
	if repo == nil {
		status = 0
		return
	}

	if err := quota.Precheck(r.Context(), h.DB, repo.ID, r.ContentLength); err != nil {
		status = h.writeIngestError(w, err, false)
		return
	}

	spool, err := base.SpoolLimited(r.Body, maxUploadBytes)
	if err != nil {
		status = h.writeIngestError(w, err, false)
		return
	}
	defer spool.Close()

	row, err := h.ingest(w, r, ingestRequest{
		repo: repo, branch: t.dir.branch, repoDir: t.dir.repo, arch: t.dir.arch, filename: t.file, body: spool,
	})
	if err != nil {
		status = h.writeIngestError(w, err, false)
		return
	}

	httpx.WriteJSON(w, http.StatusCreated, uploadBody(row))
}

func uploadBody(row *models.ApkPackage) map[string]any {
	return map[string]any{
		"id":      row.ID,
		"name":    row.Name,
		"version": row.Version,
		"branch":  row.Branch,
		"repo":    row.Repo,
		"arch":    row.Arch,
		"sha256":  row.BlobSHA256,
		"size":    row.SizeBytes,
	}
}

// handleRESTUpload is the web UI upload: a multipart form with the file, the branch and repo
// directories and, for a noarch package, the arch.
func (h *HTTPHandler) handleRESTUpload(w http.ResponseWriter, r *http.Request) {
	status := http.StatusOK

	defer func() {
		if h.metrics != nil && status != 0 {
			h.metrics.CountUpload(metrics.FormatApk, metrics.UploadResult(status))
		}
	}()

	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes+multipartOverhead)

	if err := r.ParseMultipartForm(multipartMemory); err != nil { //nolint:gosec // r.Body is bounded by http.MaxBytesReader above
		status = http.StatusBadRequest
		httpx.WriteError(w, status, "INVALID_INPUT", fmt.Sprintf("failed to parse form: %v", err))

		return
	}

	file, _, err := r.FormFile("file")
	if err != nil {
		status = http.StatusBadRequest
		httpx.WriteError(w, status, "INVALID_INPUT", "missing file field")

		return
	}
	defer base.CloseLog(file, h.Log)

	repo := h.hostedAPKRepo(w, r, true)
	if repo == nil {
		status = 0
		return
	}

	if err := quota.Precheck(r.Context(), h.DB, repo.ID, r.ContentLength); err != nil {
		status = h.writeIngestError(w, err, true)
		return
	}

	spool, err := base.SpoolLimited(file, maxUploadBytes)
	if err != nil {
		status = h.writeIngestError(w, err, true)
		return
	}
	defer spool.Close()

	row, err := h.ingest(w, r, ingestRequest{
		repo:    repo,
		branch:  defaultTo(r.FormValue("branch"), "edge"),
		repoDir: defaultTo(r.FormValue("repo"), "main"),
		arch:    r.FormValue("arch"),
		body:    spool,
	})
	if err != nil {
		status = h.writeIngestError(w, err, true)
		return
	}

	httpx.WriteJSON(w, http.StatusOK, map[string]any{"success": true, "data": uploadBody(row)})
}

func defaultTo(v, def string) string {
	if v == "" {
		return def
	}

	return v
}

// handleDelete removes the package at <branch>/<repo>/<arch>/<name>-<version>.apk.
func (h *HTTPHandler) handleDelete(w http.ResponseWriter, r *http.Request) {
	t, ok := parseTarget(relPath(r))
	if !ok {
		http.NotFound(w, r)
		return
	}

	name, version, ok := pkgapk.ParseFileName(t.file)
	if !ok {
		http.NotFound(w, r)
		return
	}

	repo := h.hostedAPKRepo(w, r, false)
	if repo == nil {
		return
	}

	ctx := r.Context()

	pkg, err := h.DB.GetApkPackageFile(ctx, repo.ID, t.dir.branch, t.dir.repo, t.dir.arch, name, version)
	if err != nil {
		h.Log.Errorw("failed to get apk package", "name", name, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if pkg == nil {
		http.NotFound(w, r)
		return
	}

	if gErr := trash.GuardRelease(r, repo, "apk", pkg.Version); gErr != nil {
		if errors.Is(gErr, trash.ErrImmutableRelease) {
			http.Error(w, "repository has immutable releases", http.StatusConflict)
			return
		}

		h.Log.Errorw("failed to check release immutability", "name", name, "error", gErr)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	err = trash.Delete(ctx, h.DB, trash.Request{
		RepoID:   repo.ID,
		Kind:     models.TrashKindApk,
		IDs:      []string{pkg.ID},
		Resource: t.dir.path() + "/" + t.file,
		Package:  pkg.Name,
		Version:  pkg.Version,
	}, func() error {
		if delErr := h.DB.DeleteApkPackage(ctx, pkg.ID, repo.ID); delErr != nil {
			return fmt.Errorf("delete apk package: %w", delErr)
		}

		h.ReleaseQuota(ctx, repo.ID, repo.Name, pkg.SizeBytes)

		// The blob is content-addressed and may be shared: remove it only once
		// no row references it any more.
		if delErr := storage.SafeDeleteBlob(ctx, h.DB, h.BlobStore, pkg.BlobSHA256); delErr != nil {
			h.Log.Warnw("failed to clean up blob after apk delete", "sha256", pkg.BlobSHA256, "error", delErr)
		}

		return nil
	})
	if err != nil {
		if errors.Is(err, database.ErrTrashNotFound) {
			http.NotFound(w, r)
			return
		}

		h.Log.Errorw("failed to delete apk package", "id", pkg.ID, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	h.notify(repo.ID)

	h.Emit(ctx, repo.ID, webhook.EventArtifactDeleted, webhook.ArtifactPayload(webhook.EventArtifactDeleted, webhook.Artifact{
		Repository: repo.Name,
		Format:     "apk",
		Name:       pkg.Name,
		Version:    pkg.Version,
		Path:       t.dir.path() + "/" + t.file,
		SHA256:     pkg.BlobSHA256,
		Size:       pkg.SizeBytes,
	}))

	w.WriteHeader(http.StatusNoContent)
}
