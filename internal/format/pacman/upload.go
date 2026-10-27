// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package pacman

import (
	"context"
	"crypto/md5" //nolint:gosec // pacman databases carry an MD5 of each package (%MD5SUM%)
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	"github.com/openartifact/openartifact/internal/importmeta"
	"github.com/openartifact/openartifact/internal/licensepolicy"
	"github.com/openartifact/openartifact/internal/quota"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/trash"
	"github.com/openartifact/openartifact/internal/webhook"
	"github.com/openartifact/openartifact/pkg/pacman"
)

// maxPackageBytes bounds one uploaded package file.
const maxPackageBytes = 8 << 30

func (h *HTTPHandler) handlePut(w http.ResponseWriter, r *http.Request) {
	filename := fileParam(r)

	kind, _ := classify(filename)
	if kind != kindPackage && kind != kindPackageSig {
		http.Error(
			w,
			"only package files (.pkg.tar.zst, .xz, .gz) and their .sig can be uploaded; the databases are generated",
			http.StatusBadRequest,
		)
		return
	}

	arch, ok := archOf(w, r)
	if !ok {
		return
	}

	if arch == pacman.ArchAny {
		http.Error(
			w,
			"upload packages to the directory of an architecture; an any package is served under all of them",
			http.StatusBadRequest,
		)
		return
	}

	repo := h.resolveRepo(w, r)
	if repo == nil {
		return
	}

	if kind == kindPackageSig {
		h.putSignature(w, r, repo, arch, strings.TrimSuffix(filename, ".sig"))
		return
	}

	h.putPackage(w, r, repo, arch, filename)
}

// putPackage stores a package file after checking that it is a pacman package
// whose .PKGINFO agrees with the directory and the file name it is uploaded to.
func (h *HTTPHandler) putPackage(w http.ResponseWriter, r *http.Request, repo *models.Repository, arch, filename string) {
	_, ext, _ := pacman.SplitPackageFileName(filename)

	md5Sum := md5.New() //nolint:gosec // see import

	spool, err := base.SpoolLimited(r.Body, maxPackageBytes, md5Sum)
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
	defer spool.Close()

	pkg, msg := h.readPackage(spool, filename, arch, ext)
	if msg != "" {
		http.Error(w, msg, http.StatusBadRequest)
		return
	}

	if licensepolicy.Enforce(w, r, h.DB, h.Log, repo.ID, pkg.Info.Licenses...) {
		return
	}

	existing, err := h.DB.GetPacmanPackageByFilename(r.Context(), repo.ID, filename)
	if err != nil {
		h.Log.Errorw("failed to check existing pacman package", "filename", filename, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if existing != nil {
		http.Error(w, "package file already exists", http.StatusConflict)
		return
	}

	row := newRow(repo.ID, arch, filename, pkg, spool, hex.EncodeToString(md5Sum.Sum(nil)))

	h.commitPackage(w, r, repo, row, spool)
}

// readPackage reads the package and checks it against the upload target. msg is
// the 400 text when the upload must be refused.
func (h *HTTPHandler) readPackage(spool *base.Spool, filename, arch, ext string) (*pacman.Package, string) {
	pkg, err := pacman.ReadPackage(spool.Reader())
	if err != nil {
		return nil, "not a valid pacman package: " + err.Error()
	}

	switch {
	case pkg.Info.Arch != arch && pkg.Info.Arch != pacman.ArchAny:
		return nil, fmt.Sprintf("package architecture %q does not match the os/%s directory", pkg.Info.Arch, arch)
	case pkg.Compression != ext:
		return nil, fmt.Sprintf("file name says .%s but the package is compressed with %s", ext, pkg.Compression)
	case !pacman.FileNameMatches(filename, pkg.Info, ext):
		return nil, "file name must be " + pacman.PackageFileName(pkg.Info.Name, pkg.Info.Version, pkg.Info.Arch, ext)
	}

	return pkg, ""
}

// newRow builds the database record for an uploaded package.
func newRow(repoID, arch, filename string, pkg *pacman.Package, body *base.Spool, md5Hex string) *models.PacmanPackage {
	info := pkg.Info

	fileList := strings.Join(pkg.Files, "\n")

	return &models.PacmanPackage{
		RepositoryID:  repoID,
		Name:          info.Name,
		Base:          info.Base,
		Version:       info.Version,
		Arch:          info.Arch,
		RepoArch:      arch,
		Filename:      filename,
		SizeBytes:     body.Size(),
		SHA256:        body.SHA256Hex(),
		MD5:           md5Hex,
		Description:   info.Description,
		URL:           info.URL,
		Packager:      info.Packager,
		BuildDate:     info.BuildDate,
		InstalledSize: info.InstalledSz,
		Licenses:      info.Licenses,
		Groups:        info.Groups,
		Replaces:      info.Replaces,
		Conflicts:     info.Conflicts,
		Provides:      info.Provides,
		Depends:       info.Depends,
		OptDepends:    info.OptDepends,
		MakeDepends:   info.MakeDepends,
		CheckDepends:  info.CheckDepends,
		FileList:      &fileList,
	}
}

// commitPackage reserves quota, stores the blob and the row, and answers 201.
func (h *HTTPHandler) commitPackage(
	w http.ResponseWriter,
	r *http.Request,
	repo *models.Repository,
	row *models.PacmanPackage,
	spool *base.Spool,
) {
	if err := quota.Enforce(r.Context(), h.DB, repo.ID, row.SizeBytes); err != nil {
		if errors.Is(err, quota.ErrExceeded) {
			quota.WriteExceeded(w)
			return
		}

		h.Log.Errorw("failed to reserve quota", "repo", repo.Name, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if err := h.storePackage(r.Context(), row, spool); err != nil {
		if relErr := quota.Release(r.Context(), h.DB, repo.ID, row.SizeBytes); relErr != nil {
			h.Log.Warnw("failed to release quota", "repo", repo.Name, "error", relErr)
		}

		if errors.Is(err, database.ErrDuplicate) || database.IsUniqueViolation(err) {
			http.Error(w, "package file or version already exists", http.StatusConflict)
			return
		}

		h.Log.Errorw("failed to store pacman package", "filename", row.Filename, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	h.notifyStale(repo.ID)

	h.Emit(r.Context(), repo.ID, webhook.EventArtifactUploaded, webhook.ArtifactPayload(webhook.EventArtifactUploaded, webhook.Artifact{
		Repository: repo.Name,
		Format:     FormatName,
		Name:       row.Name,
		Version:    row.Version,
		Path:       "os/" + row.RepoArch + "/" + row.Filename,
		SHA256:     row.SHA256,
		Size:       row.SizeBytes,
	}))

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)

	if encErr := json.NewEncoder(w).Encode(map[string]string{
		"id":       row.ID,
		"name":     row.Name,
		"version":  row.Version,
		"arch":     row.Arch,
		"filename": row.Filename,
		"sha256":   row.SHA256,
	}); encErr != nil {
		_ = encErr // response header already written
	}
}

// storePackage stores the blob first, then inserts the referencing row under
// the per-blob lock so a concurrent unpublish cannot delete the blob in
// between. When no row results the blob is dropped again unless something else
// references it.
func (h *HTTPHandler) storePackage(ctx context.Context, row *models.PacmanPackage, body *base.Spool) error {
	blob := storage.SeekerBlob(row.SHA256, body.Reader(), map[string]string{
		"sha256":   row.SHA256,
		"filename": row.Filename,
	})

	err := storage.CommitBlob(ctx, h.DB, h.BlobStore, blob, func(ctx context.Context, blobKey string) error {
		row.BlobKey = blobKey
		row.CreatedAt = importmeta.CreatedAt(ctx)

		if err := h.DB.CreatePacmanPackage(ctx, row); err != nil {
			return fmt.Errorf("create pacman package: %w", err)
		}

		return nil
	})
	if err == nil {
		return nil
	}

	if cleanErr := storage.SafeDeleteBlob(ctx, h.DB, h.BlobStore, row.SHA256); cleanErr != nil {
		h.Log.Warnw("failed to clean up pacman blob", "sha256", row.SHA256, "error", cleanErr)
	}

	return err
}

// putSignature attaches the uploader's detached signature to a package. It
// replaces a signature the server made, and is itself replaced by a later
// upload; the databases are regenerated to carry it.
func (h *HTTPHandler) putSignature(w http.ResponseWriter, r *http.Request, repo *models.Repository, arch, pkgFilename string) {
	pkg, ok := h.lookup(w, r, repo.ID, arch, pkgFilename)
	if !ok {
		return
	}

	data, err := base.ReadAllLimited(r.Body, pacman.MaxSignatureBytes)
	if err != nil {
		http.Error(w, "signature too large", http.StatusRequestEntityTooLarge)
		return
	}

	bin, err := pacman.NormalizeSignature(data)
	if err != nil {
		http.Error(w, "not a detached OpenPGP signature", http.StatusBadRequest)
		return
	}

	err = h.DB.SetPacmanSignature(r.Context(), repo.ID, pkg.ID, base64.StdEncoding.EncodeToString(bin), models.PacmanSigUploader, "")
	if err != nil {
		h.Log.Errorw("failed to store pacman signature", "filename", pkgFilename, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	h.notifyStale(repo.ID)
	w.WriteHeader(http.StatusCreated)
}

func (h *HTTPHandler) handleDelete(w http.ResponseWriter, r *http.Request) {
	filename := fileParam(r)

	kind, _ := classify(filename)
	if kind != kindPackage && kind != kindPackageSig {
		http.NotFound(w, r)
		return
	}

	arch, ok := archOf(w, r)
	if !ok {
		return
	}

	repo := h.resolveRepo(w, r)
	if repo == nil {
		return
	}

	if kind == kindPackageSig {
		h.deleteSignature(w, r, repo, arch, strings.TrimSuffix(filename, ".sig"))
		return
	}

	h.deletePackage(w, r, repo, arch, filename)
}

// deleteSignature removes an uploaded signature; the metadata worker then
// signs the package with the repository key, if it has one.
func (h *HTTPHandler) deleteSignature(w http.ResponseWriter, r *http.Request, repo *models.Repository, arch, pkgFilename string) {
	pkg, ok := h.lookup(w, r, repo.ID, arch, pkgFilename)
	if !ok {
		return
	}

	if pkg.SignatureSource != models.PacmanSigUploader {
		http.NotFound(w, r)
		return
	}

	if err := h.DB.SetPacmanSignature(r.Context(), repo.ID, pkg.ID, "", "", ""); err != nil {
		h.Log.Errorw("failed to clear pacman signature", "filename", pkgFilename, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	h.notifyStale(repo.ID)
	w.WriteHeader(http.StatusNoContent)
}

func (h *HTTPHandler) deletePackage(w http.ResponseWriter, r *http.Request, repo *models.Repository, arch, filename string) {
	pkg, ok := h.lookup(w, r, repo.ID, arch, filename)
	if !ok {
		return
	}

	if gErr := trash.GuardReleaseByName(r, h.DB, repo.Name, FormatName, pkg.Version); gErr != nil {
		if errors.Is(gErr, trash.ErrImmutableRelease) {
			http.Error(w, "repository has immutable releases", http.StatusConflict)
			return
		}

		h.Log.Errorw("failed to check release immutability", "name", pkg.Name, "error", gErr)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	err := trash.Delete(r.Context(), h.DB, trash.Request{
		RepoID:   repo.ID,
		Kind:     models.TrashKindPacman,
		IDs:      []string{pkg.ID},
		Resource: "os/" + arch + "/" + filename,
		Package:  pkg.Name,
		Version:  pkg.Version,
	}, func() error {
		return h.hardDelete(r.Context(), repo, pkg)
	})
	if err != nil {
		if errors.Is(err, database.ErrTrashNotFound) {
			http.NotFound(w, r)
			return
		}

		h.Log.Errorw("failed to delete pacman package", "id", pkg.ID, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	h.notifyStale(repo.ID)

	h.Emit(r.Context(), repo.ID, webhook.EventArtifactDeleted, webhook.ArtifactPayload(webhook.EventArtifactDeleted, webhook.Artifact{
		Repository: repo.Name,
		Format:     FormatName,
		Name:       pkg.Name,
		Version:    pkg.Version,
		Path:       "os/" + arch + "/" + filename,
		SHA256:     pkg.SHA256,
		Size:       pkg.SizeBytes,
	}))

	w.WriteHeader(http.StatusNoContent)
}

// hardDelete is the permanent delete: the row, the quota it held and, once no
// row references it, the content-addressed blob.
func (h *HTTPHandler) hardDelete(ctx context.Context, repo *models.Repository, pkg *models.PacmanPackage) error {
	if err := h.DB.DeletePacmanPackage(ctx, repo.ID, pkg.ID); err != nil {
		return fmt.Errorf("delete pacman package: %w", err)
	}

	size := pkg.SizeBytes
	if size <= 0 {
		if n, err := h.BlobStore.GetSize(ctx, pkg.BlobKey); err == nil {
			size = n
		}
	}

	if err := quota.Release(ctx, h.DB, repo.ID, size); err != nil {
		h.Log.Warnw("failed to release quota", "repo", repo.Name, "error", err)
	}

	if err := storage.SafeDeleteBlob(ctx, h.DB, h.BlobStore, pkg.SHA256); err != nil {
		h.Log.Warnw("failed to clean up blob after pacman delete", "sha256", pkg.SHA256, "error", err)
	}

	return nil
}
