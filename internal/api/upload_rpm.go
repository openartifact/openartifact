// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/go-chi/chi/v5"
	database "github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	formatrpm "github.com/openartifact/openartifact/internal/format/rpm"
	"github.com/openartifact/openartifact/internal/importmeta"
	"github.com/openartifact/openartifact/internal/iocopy"
	"github.com/openartifact/openartifact/internal/licensepolicy"
	"github.com/openartifact/openartifact/internal/metrics"
	"github.com/openartifact/openartifact/internal/quota"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/trash"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/internal/webhook"
	"github.com/openartifact/openartifact/internal/worker"
	"github.com/openartifact/openartifact/pkg/gpg"
	"github.com/openartifact/openartifact/pkg/logger"
	pkgrpm "github.com/openartifact/openartifact/pkg/rpm"
	"github.com/openartifact/openartifact/pkg/rpmsign"
)

const (
	maxRPMUploadSize = 500 * 1024 * 1024 // 500MB
	rpmFormMemory    = 1 << 20           // multipart bytes held in memory
	maxCompsSize     = 10 * 1024 * 1024
)

type RPMUploadHandler struct {
	log        *logger.Logger
	db         database.Database
	blobStore  storage.BlobStore
	handler    *formatrpm.RepositoryHandler
	reg        *metrics.Registry
	debHandler *DebUploadHandler
	rpmWorker  *worker.RPMetadataWorker
	publisher  webhook.Publisher

	packageDeleter PackageDeleter
	keys           gpg.Resolver
}

func NewRPMUploadHandler(log *logger.Logger, db database.Database, blobStore storage.BlobStore, reg *metrics.Registry) *RPMUploadHandler {
	return &RPMUploadHandler{
		log:        log,
		db:         db,
		blobStore:  blobStore,
		handler:    formatrpm.NewRepositoryHandler(),
		reg:        reg,
		debHandler: NewDebUploadHandler(log, db, blobStore, reg),
	}
}

func (h *RPMUploadHandler) SetDebWorker(w *worker.DebMetadataWorker) {
	h.debHandler.SetDebWorker(w)
}

func (h *RPMUploadHandler) SetRPMWorker(w *worker.RPMetadataWorker) {
	h.rpmWorker = w
}

// SetKeyResolver wires the signing keys used by repositories with sign_packages.
func (h *RPMUploadHandler) SetKeyResolver(r gpg.Resolver) {
	h.keys = r
}

// SetPublisher wires the webhook publisher used for artifact events. It also
// covers the embedded deb handler that serves deb repositories on the same routes.
func (h *RPMUploadHandler) SetPublisher(p webhook.Publisher) {
	h.publisher = p
	h.debHandler.SetPublisher(p)
}

func (h *RPMUploadHandler) Register(router chi.Router) {
	router.Post("/api/v1/repositories/{name}/packages", h.handleUpload)
	router.Delete("/api/v1/repositories/{name}/packages/{id}", h.handleDelete)
	router.Put("/api/v1/repositories/{name}/comps", h.handleCompsUpload)
	h.log.Infow("RPM upload handler registered")
}

func (h *RPMUploadHandler) handleUpload(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	repoName := vars["name"]

	// Dispatch to the correct format handler based on the repository's format.
	repo, err := h.db.GetRepositoryByName(r.Context(), repoName)
	if err != nil || repo == nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "repository not found: "+repoName)
		return
	}

	if rejectNonHosted(w, repo) {
		return
	}

	if repo.Format == "deb" {
		h.debHandler.handleUpload(w, r)
		return
	}

	uploadResult := "success"

	if h.reg != nil {
		defer func() { h.reg.CountUpload(metrics.FormatRPM, uploadResult) }()
	}

	if repo.Format != "rpm" {
		uploadResult = "error"

		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "repository format is not rpm")

		return
	}

	// Cap the whole request body (file plus multipart overhead); keep only a
	// small part of the form in memory, the rest spills to disk.
	r.Body = http.MaxBytesReader(w, r.Body, maxRPMUploadSize+1<<20)

	if err := r.ParseMultipartForm(rpmFormMemory); err != nil { //nolint:gosec // r.Body is bounded by http.MaxBytesReader above
		if _, tooBig := errors.AsType[*http.MaxBytesError](err); tooBig {
			uploadResult = "error"

			writeError(w, http.StatusRequestEntityTooLarge, "INVALID_INPUT", "file exceeds maximum size limit")

			return
		}

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

	if header.Size > maxRPMUploadSize {
		uploadResult = "error"

		writeError(w, http.StatusRequestEntityTooLarge, "INVALID_INPUT", "file exceeds maximum size limit")

		return
	}

	contentType := header.Header.Get("Content-Type")
	if contentType != "" && contentType != "application/x-rpm" && contentType != "application/octet-stream" {
		uploadResult = "error"

		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "invalid content type: "+contentType)

		return
	}

	result, err := h.uploadPipeline(w, r, file, repo)
	if err != nil {
		uploadResult = "error"

		h.writeUploadError(w, err, repoName, header.Filename)

		return
	}

	if result.Duplicate {
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

	h.log.Infow("RPM package uploaded", "repository", repoName, "package", result.Name, "version", result.Version)
	writeJSON(w, http.StatusOK, Response{
		Success: true,
		Data:    result,
	})
}

// ensureRPMRepo makes sure the rpm_repos row the metadata worker needs exists.
func (h *RPMUploadHandler) ensureRPMRepo(ctx context.Context, repoID string) error {
	rpmRepo, err := h.db.GetRPMRepo(ctx, repoID)
	if err != nil {
		return fmt.Errorf("failed to check rpm_repos: %w", err)
	}

	if rpmRepo == nil {
		if err := h.db.CreateRPMRepo(ctx, &models.RPMRepo{RepositoryID: repoID, BaseArch: "x86_64"}); err != nil {
			return fmt.Errorf("failed to create rpm_repos entry: %w", err)
		}
	}

	return nil
}

// rpmSpool is an upload buffered on disk with its digest and size.
type rpmSpool struct {
	file   *os.File
	sha256 string
	size   int64
}

func (s *rpmSpool) close(log *logger.Logger) {
	base.CloseLog(s.file, log)

	_ = os.Remove(s.file.Name())
}

// spoolUpload streams the request file to a temp file while hashing it.
func (h *RPMUploadHandler) spoolUpload(file io.Reader) (*rpmSpool, error) {
	tmpFile, err := os.CreateTemp("", "rpm-upload-*.tmp")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp file: %w", err)
	}

	sp := &rpmSpool{file: tmpFile}
	hasher := sha256.New()

	sp.size, err = iocopy.Copy(tmpFile, io.TeeReader(file, hasher))
	if err != nil {
		sp.close(h.log)
		return nil, fmt.Errorf("failed to buffer upload: %w", err)
	}

	sp.sha256 = hex.EncodeToString(hasher.Sum(nil))

	return sp, nil
}

// storeBlobDeduped stores the temp file in the blob store unless a blob with
// the same sha256 already exists, and reports whether it pre-existed.
func (h *RPMUploadHandler) storeBlobDeduped(ctx context.Context, sp *rpmSpool, repoName string) (string, bool, error) {
	blobID, blobExists, err := h.blobStore.ExistsBySHA256(ctx, sp.sha256)
	if err != nil {
		return "", false, fmt.Errorf("failed to check blob existence: %w", err)
	}

	if blobExists {
		return blobID, true, nil
	}

	if _, err := sp.file.Seek(0, io.SeekStart); err != nil {
		return "", false, fmt.Errorf("failed to seek temp file: %w", err)
	}

	blobID, err = h.blobStore.Put(ctx, sp.file, map[string]string{
		"format":    "rpm",
		"repo_name": repoName,
		"sha256":    sp.sha256,
	})
	if err != nil {
		return "", false, fmt.Errorf("failed to store blob: %w", err)
	}

	return blobID, false, nil
}

// isInvalidRPMError reports whether err means the uploaded file is not a valid rpm.
func isInvalidRPMError(err error) bool {
	return errors.Is(err, pkgrpm.ErrInvalidHeader) || errors.Is(err, pkgrpm.ErrInvalidHeaderMagic) ||
		errors.Is(err, pkgrpm.ErrHeaderTooLarge) || errors.Is(err, pkgrpm.ErrInvalidLeadMagic) ||
		errors.Is(err, pkgrpm.ErrUnsupportedVersion)
}

// parseSpooled parses the spooled file as an rpm package.
func parseSpooled(sp *rpmSpool) (*pkgrpm.Package, error) {
	if _, err := sp.file.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("failed to seek temp file: %w", err)
	}

	pkg, err := pkgrpm.ParseRPM(sp.file)
	if err != nil {
		return nil, fmt.Errorf("failed to parse rpm: %w", err)
	}

	return pkg, nil
}

// newComponent builds the rpm_packages row for pkg referencing blob blobUUID.
func newComponent(repoID, blobUUID, filename string, size int64, pkg *pkgrpm.Package, extra []byte, createdAt time.Time) *models.Component {
	return &models.Component{
		CreatedAt:     createdAt,
		RepositoryID:  repoID,
		ArtifactID:    blobUUID,
		Name:          pkg.Name,
		Version:       pkg.Version,
		Release:       pkg.Release,
		Epoch:         int(pkg.Epoch),
		Arch:          pkg.Arch,
		Summary:       pkg.Summary,
		Description:   pkg.Description,
		License:       pkg.License,
		URL:           pkg.URL,
		Vendor:        pkg.Vendor,
		Group:         pkg.Group,
		Packager:      pkg.Packager,
		SourceRPM:     pkg.SourceRPM,
		BuildHost:     pkg.BuildHost,
		BuildTime:     pkg.BuildTime,
		PackageSize:   size,
		InstalledSize: pkg.InstalledSize,
		ArchiveSize:   pkg.ArchiveSize,
		Filename:      filename,
		HeaderStart:   pkg.HeaderStart,
		HeaderEnd:     pkg.HeaderEnd,
		RPMExtra:      extra,
	}
}

// rpmFilename is the canonical file name of a package.
func rpmFilename(pkg *pkgrpm.Package) string {
	return fmt.Sprintf("%s-%s-%s.%s.rpm", pkg.Name, pkg.Version, pkg.Release, pkg.Arch)
}

// rpmVersionRelease renders "version-release" (just version when no release).
func rpmVersionRelease(version, release string) string {
	if release == "" {
		return version
	}

	return version + "-" + release
}

// isDuplicateUpload reports whether the same NEVRA, or another epoch of the same
// N-V-R.A (same file name), is already stored in the repository.
func (h *RPMUploadHandler) isDuplicateUpload(ctx context.Context, repoID string, pkg *pkgrpm.Package) (bool, error) {
	nevraExists, err := h.db.RpmPackageExists(ctx, repoID, pkg.Name, pkg.Version, pkg.Release, int(pkg.Epoch), pkg.Arch)
	if err != nil {
		return false, fmt.Errorf("failed to check package existence: %w", err)
	}

	if nevraExists {
		return true, nil
	}

	// The file name carries no epoch, so another epoch of the same N-V-R.A
	// would share it and the download could not tell the two apart.
	sameFile, err := h.db.GetComponentByFilename(ctx, repoID, rpmFilename(pkg))
	if err != nil {
		return false, fmt.Errorf("failed to check file name: %w", err)
	}

	return sameFile != nil, nil
}

// releaseQuota returns a reservation made by quota.Enforce.
func (h *RPMUploadHandler) releaseQuota(ctx context.Context, repoName, repoID string, size int64) {
	if err := quota.Release(ctx, h.db, repoID, size); err != nil {
		h.log.Warnw("failed to release quota", "repository", repoName, "error", err)
	}
}

// dropOrphanBlob removes a blob this upload stored when no row references it.
func (h *RPMUploadHandler) dropOrphanBlob(ctx context.Context, sha256sum, reason string) {
	if err := storage.SafeDeleteBlob(context.WithoutCancel(ctx), h.db, h.blobStore, sha256sum); err != nil {
		h.log.Warnw("failed to clean up blob after "+reason, "sha256", sha256sum, "error", err)
	}
}

// persistPackage stores the blob and inserts the package row under the
// per-blob lock so a concurrent unpublish/cleanup cannot delete the blob in
// between. The caller holds the quota reservation and releases it unless the
// package was created.
func (h *RPMUploadHandler) persistPackage(ctx context.Context, repo *models.Repository, sp *rpmSpool, pkg *pkgrpm.Package, extra []byte) (*formatrpm.UploadResult, error) {
	var (
		blobID   string
		storedBy bool // this upload put the blob (it did not pre-exist)
		created  bool
	)

	filename := rpmFilename(pkg)
	// Zero unless an administrator supplied an origin timestamp (importmeta).
	createdAt := importmeta.CreatedAt(ctx)
	comp := newComponent(repo.ID, "", filename, sp.size, pkg, extra, createdAt)

	err := storage.WithBlobLock(ctx, h.db, sp.sha256, func(ctx context.Context) error {
		id, existed, err := h.storeBlobDeduped(ctx, sp, repo.Name)
		if err != nil {
			return err
		}

		blobID, storedBy = id, !existed

		// Upsert into blobs table to get the UUID for blob_id FK reference.
		blobUUID, err := h.db.UpsertBlob(ctx, sp.sha256, sp.sha256, sp.size)
		if err != nil {
			return fmt.Errorf("failed to upsert blob record: %w", err)
		}

		comp.ArtifactID = blobUUID

		if err := h.db.CreateRpmPackage(ctx, comp); err != nil {
			return fmt.Errorf("failed to create component record: %w", err)
		}

		created = true

		return nil
	})
	if created {
		if err != nil {
			// Only the (empty) lock transaction failed to commit; the row exists.
			h.log.Warnw("blob lock commit failed after package was stored", "sha256", sp.sha256, "error", err)
		}

		return &formatrpm.UploadResult{
			ID:   comp.ID,
			Name: pkg.Name, Version: pkg.Version, Release: pkg.Release, Arch: pkg.Arch,
			SHA256: sp.sha256, BlobID: blobID, Size: sp.size,
		}, nil
	}

	if storedBy {
		reason := "DB error"
		if errors.Is(err, database.ErrDuplicate) {
			reason = "duplicate"
		}

		h.dropOrphanBlob(ctx, sp.sha256, reason)
	}

	// Lost the check-then-insert race: another concurrent upload of the same
	// package won (HTTP 409).
	if errors.Is(err, database.ErrDuplicate) {
		return &formatrpm.UploadResult{Duplicate: true}, nil
	}

	return nil, err
}

var (
	// errNoSigningKey means sign_packages is on but no key resolves for the repository.
	errNoSigningKey = errors.New("no signing key")
	// errSigningFailed means the package could not be signed (unsupported format, unreadable signature header).
	errSigningFailed = errors.New("signing failed")
)

// writeUploadError maps an uploadPipeline failure to its HTTP response.
func (h *RPMUploadHandler) writeUploadError(w http.ResponseWriter, err error, repoName, filename string) {
	switch {
	case errors.Is(err, errResponseWritten):
		// licensepolicy.Enforce already answered.
	case isInvalidRPMError(err):
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "invalid rpm package: "+err.Error())
	case errors.Is(err, quota.ErrExceeded):
		quota.WriteExceeded(w)
	case writeSigningError(w, err):
	default:
		h.log.Errorw("RPM upload failed", "repository", repoName, "filename", filename, "error", err)
		writeError(w, http.StatusInternalServerError, "UPLOAD_ERROR", "upload processing failed")
	}
}

// writeSigningError answers the sign_packages failures and reports whether err was one.
func writeSigningError(w http.ResponseWriter, err error) bool {
	switch {
	case errors.Is(err, errNoSigningKey):
		writeError(w, http.StatusConflict, "SIGNING_KEY_REQUIRED",
			"the repository signs packages (sign_packages) but no GPG key is active for it; activate a key or turn the setting off")
	case errors.Is(err, errSigningFailed):
		writeError(w, http.StatusUnprocessableEntity, "SIGNING_FAILED", "cannot sign the package: "+err.Error())
	default:
		return false
	}

	return true
}

// countingWriter counts the bytes written through it.
type countingWriter struct{ n int64 }

func (c *countingWriter) Write(p []byte) (int, error) {
	c.n += int64(len(p))
	return len(p), nil
}

// signUpload returns a signed copy of the spooled package and its parsed
// header. The copy is a different file: its digest, size and header offsets
// differ from the upload's, and those are what the blob store, the database
// and the repodata checksums then use. The caller closes the returned spool.
func (h *RPMUploadHandler) signUpload(ctx context.Context, repo *models.Repository, sp *rpmSpool) (*rpmSpool, *pkgrpm.Package, error) {
	var signer gpg.Signer

	if h.keys != nil {
		var err error

		signer, err = h.keys.SignerForRepo(ctx, repo.ID)
		if err != nil {
			return nil, nil, fmt.Errorf("resolving the signing key: %w", err)
		}
	}

	if signer == nil {
		return nil, nil, errNoSigningKey
	}

	out, err := os.CreateTemp("", "rpm-signed-*.tmp")
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create temp file: %w", err)
	}

	signed := &rpmSpool{file: out}

	hasher, count := sha256.New(), &countingWriter{}
	if err := rpmsign.Sign(sp.file, io.MultiWriter(out, hasher, count), signer); err != nil {
		signed.close(h.log)
		return nil, nil, fmt.Errorf("%w: %w", errSigningFailed, err)
	}

	signed.sha256, signed.size = hex.EncodeToString(hasher.Sum(nil)), count.n

	pkg, err := parseSpooled(signed)
	if err != nil {
		signed.close(h.log)
		return nil, nil, fmt.Errorf("%w: the signed package does not parse: %w", errSigningFailed, err)
	}

	return signed, pkg, nil
}

// errResponseWritten means a policy check already wrote the HTTP response.
var errResponseWritten = errors.New("response already written")

func (h *RPMUploadHandler) uploadPipeline(w http.ResponseWriter, r *http.Request, file io.Reader, repo *models.Repository) (*formatrpm.UploadResult, error) {
	ctx := r.Context()

	if err := h.ensureRPMRepo(ctx, repo.ID); err != nil {
		return nil, err
	}

	// Refuse a body the quota cannot take before it is spooled to disk.
	if err := quota.Precheck(ctx, h.db, repo.ID, r.ContentLength); err != nil {
		return nil, fmt.Errorf("rpm upload: %w", err)
	}

	sp, err := h.spoolUpload(file)
	if err != nil {
		return nil, err
	}
	defer sp.close(h.log)

	// Per-repo duplicate check: same package already in this repo.
	exists, err := h.db.ArtifactExists(ctx, sp.sha256, repo.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to check artifact existence: %w", err)
	}

	if exists {
		return &formatrpm.UploadResult{Duplicate: true}, nil
	}

	pkg, err := parseSpooled(sp)
	if err != nil {
		return nil, err
	}

	// Same NEVRA already present (possibly with different content): immutable, 409.
	dup, err := h.isDuplicateUpload(ctx, repo.ID, pkg)
	if err != nil {
		return nil, err
	}

	if dup {
		return &formatrpm.UploadResult{Duplicate: true}, nil
	}

	if licensepolicy.Enforce(w, r, h.db, h.log, repo.ID, pkg.License) {
		return nil, errResponseWritten
	}

	if repo.SignPackages {
		signed, signedPkg, err := h.signUpload(ctx, repo, sp)
		if err != nil {
			return nil, err
		}

		defer signed.close(h.log)

		sp, pkg = signed, signedPkg
	}

	extra, err := pkg.MarshalExtra()
	if err != nil {
		return nil, fmt.Errorf("failed to encode package metadata: %w", err)
	}

	// Reserve quota before persisting anything; give it back unless a package row exists.
	if err := quota.Enforce(ctx, h.db, repo.ID, sp.size); err != nil {
		return nil, fmt.Errorf("rpm upload: %w", err)
	}

	result, err := h.persistPackage(ctx, repo, sp, pkg, extra)
	if err != nil || result.Duplicate {
		h.releaseQuota(ctx, repo.Name, repo.ID, sp.size)
		return result, err
	}

	// The worker regenerates only repositories flagged stale (the flag starts
	// true on the first upload, then is cleared by each regeneration).
	if err := h.db.MarkRepoStale(ctx, repo.ID); err != nil {
		h.log.Warnw("failed to mark repo stale after upload", "repo_id", repo.ID, "error", err)
	}

	if h.rpmWorker != nil {
		h.rpmWorker.NotifyStale(repo.ID)
	}

	webhook.Emit(ctx, h.publisher, repo.ID, webhook.EventArtifactUploaded, webhook.ArtifactPayload(webhook.EventArtifactUploaded, webhook.Artifact{
		Repository: repo.Name,
		Format:     "rpm",
		Name:       pkg.Name,
		Version:    rpmVersionRelease(pkg.Version, pkg.Release),
		Path:       rpmFilename(pkg),
		SHA256:     sp.sha256,
		Size:       sp.size,
	}))

	return result, nil
}

func (h *RPMUploadHandler) handleDelete(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	repoName := vars["name"]
	packageID := vars["id"]

	// Dispatch to deb handler for deb repositories
	repo, err := h.db.GetRepositoryByName(r.Context(), repoName)
	if err != nil || repo == nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "repository not found: "+repoName)
		return
	}

	if rejectNonHosted(w, repo) {
		return
	}

	if repo.Format == "deb" {
		h.debHandler.handleDelete(w, r)
		return
	}

	// Every other format deletes through the generic package deleter.
	if repo.Format != "rpm" {
		h.deleteOtherFormat(w, r, repo)
		return
	}

	repoID := repo.ID

	// Look up component to get artifact ID for blob cleanup
	comp, err := h.db.GetRpmPackageByID(r.Context(), packageID)
	if err != nil {
		h.log.Errorw("failed to look up component", "package_id", packageID, "error", err)
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "failed to look up package")

		return
	}

	if comp == nil || comp.RepositoryID != repoID {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "package not found")
		return
	}

	// Resolve the sha256 blob key and size before the row disappears.
	blobSHA, size := "", comp.PackageSize
	if full, getErr := h.db.GetComponentByID(r.Context(), packageID); getErr == nil && full != nil {
		blobSHA = full.SHA256
		if full.PackageSize > 0 {
			size = full.PackageSize
		}
	}

	if err := trash.GuardRelease(r, repo, "rpm", rpmVersionRelease(comp.Version, comp.Release)); err != nil {
		h.writeDeleteError(w, err, packageID)
		return
	}

	err = trash.Delete(r.Context(), h.db, trash.Request{
		RepoID: repoID, Kind: models.TrashKindRPM, IDs: []string{packageID}, Resource: comp.Filename,
	}, func() error {
		if err := h.db.DeleteComponent(r.Context(), packageID); err != nil {
			return err
		}

		h.releaseQuota(r.Context(), repoName, repoID, size)

		// Blobs are keyed by sha256; remove only when no other row references it.
		if blobSHA != "" {
			if err := storage.SafeDeleteBlob(r.Context(), h.db, h.blobStore, blobSHA); err != nil {
				h.log.Warnw("failed to delete blob", "sha256", blobSHA, "error", err)
			}
		}

		return nil
	})
	if err != nil {
		h.writeDeleteError(w, err, packageID)
		return
	}

	if err := h.db.MarkRepoStale(r.Context(), repoID); err != nil {
		h.log.Warnw("failed to mark repo stale after delete", "repo_id", repoID, "error", err)
	}

	if h.rpmWorker != nil {
		h.rpmWorker.NotifyStale(repoID)
	}

	webhook.Emit(r.Context(), h.publisher, repoID, webhook.EventArtifactDeleted, webhook.ArtifactPayload(webhook.EventArtifactDeleted, webhook.Artifact{
		Repository: repoName,
		Format:     "rpm",
		Name:       comp.Name,
		Version:    rpmVersionRelease(comp.Version, comp.Release),
		Path:       comp.Filename,
		SHA256:     blobSHA,
		Size:       size,
	}))

	h.log.Infow("RPM package deleted", "repository", repoName, "package_id", packageID)

	writeJSON(w, http.StatusOK, Response{
		Success: true,
		Data: map[string]any{
			"repository": repoName,
			"package_id": packageID,
		},
	})
}

// writeDeleteError maps a delete failure to its HTTP answer.
func (h *RPMUploadHandler) writeDeleteError(w http.ResponseWriter, err error, packageID string) {
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

func (h *RPMUploadHandler) handleCompsUpload(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	repoName := vars["name"]

	repo, err := h.db.GetRepositoryByName(r.Context(), repoName)
	if err != nil || repo == nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "repository not found: "+repoName)
		return
	}

	if rejectNonHosted(w, repo) {
		return
	}

	if repo.Format != "rpm" {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "repository format is not rpm")
		return
	}

	repoID := repo.ID

	if err := h.ensureRPMRepo(r.Context(), repoID); err != nil {
		h.log.Errorw("failed to ensure rpm repo", "repo_id", repoID, "error", err)
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "failed to prepare repository")

		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxCompsSize))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "INVALID_INPUT", "failed to read request body or body too large")
		return
	}

	if err := formatrpm.ValidateCompsXML(body); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", fmt.Sprintf("invalid comps XML: %v", err))
		return
	}

	compsSHA := fmt.Sprintf("%x", sha256.Sum256(body))

	// The upload replaces the repository's single comps.xml row: remember the
	// blob it supersedes so it is reclaimed instead of piling up per upload.
	previous, err := h.db.GetRepoMetadataByPath(r.Context(), repoID, "comps.xml")
	if err != nil {
		h.log.Errorw("failed to look up comps metadata", "error", err)
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "failed to store comps metadata")

		return
	}

	var blobID string

	// Store the blob and commit its row under the per-blob lock, so a concurrent
	// reclaim of identical content cannot delete it in between.
	err = storage.WithBlobLock(r.Context(), h.db, compsSHA, func(ctx context.Context) error {
		id, putErr := h.blobStore.Put(ctx, bytes.NewReader(body), map[string]string{
			"format":    "rpm",
			"type":      "comps",
			"repo_name": repoName,
			"sha256":    compsSHA,
		})
		if putErr != nil {
			return &compsStoreError{status: http.StatusInternalServerError, code: "STORAGE_ERROR", msg: "failed to store comps XML", err: putErr}
		}

		blobID = id

		meta := &models.RepoMetadata{
			RepositoryID: repoID,
			Path:         "comps.xml",
			BlobSHA256:   compsSHA,
			BlobSize:     int64(len(body)),
		}
		if upErr := h.db.UpsertRepoMetadata(ctx, meta); upErr != nil {
			return &compsStoreError{status: http.StatusInternalServerError, code: "DATABASE_ERROR", msg: "failed to store comps metadata", err: upErr}
		}

		return nil
	})
	if err != nil {
		if se, ok := errors.AsType[*compsStoreError](err); ok {
			h.log.Errorw("failed to store comps", "repo_id", repoID, "error", se.err)
			writeError(w, se.status, se.code, se.msg)

			return
		}

		h.log.Errorw("failed to store comps", "repo_id", repoID, "error", err)
		writeError(w, http.StatusInternalServerError, "STORAGE_ERROR", "failed to store comps XML")

		return
	}

	if previous != nil && previous.BlobSHA256 != compsSHA {
		if err := storage.ReclaimMetadataBlob(context.WithoutCancel(r.Context()), h.db, h.blobStore, previous.BlobSHA256); err != nil {
			h.log.Warnw("failed to reclaim superseded comps blob", "sha256", previous.BlobSHA256, "error", err)
		}
	}

	if err := h.db.MarkRepoStale(r.Context(), repoID); err != nil {
		h.log.Warnw("failed to mark repo stale after comps upload", "repo_id", repoID, "error", err)
	}

	if h.rpmWorker != nil {
		h.rpmWorker.NotifyStale(repoID)
	}

	h.log.Infow("RPM comps XML uploaded", "repository", repoName, "blob_id", blobID)

	writeJSON(w, http.StatusOK, Response{
		Success: true,
		Data: map[string]any{
			"repository": repoName,
			"blob_id":    blobID,
		},
	})
}

// compsStoreError carries the response of a failed comps store out of the blob lock.
type compsStoreError struct {
	status    int
	code, msg string
	err       error
}

func (e *compsStoreError) Error() string { return e.msg + ": " + e.err.Error() }
func (e *compsStoreError) Unwrap() error { return e.err }

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	if encErr := json.NewEncoder(w).Encode(v); encErr != nil {
		_ = encErr // response header already written
	}
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	if encErr := json.NewEncoder(w).Encode(ErrorResponse{
		Error: message,
		Code:  code,
	}); encErr != nil {
		_ = encErr // response header already written
	}
}
