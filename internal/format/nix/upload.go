// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package nix

import (
	"bufio"
	"compress/bzip2"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/klauspost/compress/zstd"

	"github.com/M0Rf30/pkgformat/xzlimit"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	"github.com/openartifact/openartifact/internal/importmeta"
	"github.com/openartifact/openartifact/internal/iocopy"
	"github.com/openartifact/openartifact/internal/quota"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/trash"
	"github.com/openartifact/openartifact/internal/webhook"
	"github.com/openartifact/openartifact/pkg/nix"
)

// maxNarBytes bounds one uploaded NAR file. It is spooled to a temporary file
// while its hash is computed, never held in memory.
const maxNarBytes = 16 << 30

// maxAuxBytes bounds the listing and debug-info files that are read and
// dropped.
const maxAuxBytes = 64 << 20

// maxUploadSigs bounds the signatures an uploader can attach to one path.
const maxUploadSigs = 32

// maxZstdWindow bounds the zstd window a narinfo body may declare; the decoder
// allocates it up front.
const maxZstdWindow = 64 << 20

// errUnsupportedEncoding is returned for a narinfo whose Content-Encoding the
// server cannot decode.
var errUnsupportedEncoding = errors.New("unsupported Content-Encoding")

// putNar stores a NAR file in staging after checking it against the hash in
// its name. The narinfo that names it commits it.
func (h *HTTPHandler) putNar(w http.ResponseWriter, r *http.Request, repo *models.Repository, file string) {
	want, err := nix.ParseNarFile(file)
	if err != nil {
		http.Error(w, "NAR files are named <nixbase32 sha256 of the file>.nar[.<compression>]", http.StatusBadRequest)
		return
	}

	known, err := h.narKnown(r.Context(), repo.ID, want, file)
	if err != nil {
		h.Log.Errorw("failed to look up nix nar", "file", file, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if known {
		w.WriteHeader(http.StatusOK)
		return
	}

	if !h.precheckNar(w, r, repo.ID) {
		return
	}

	spool, err := base.SpoolLimited(r.Body, maxNarBytes)
	if err != nil {
		if errors.Is(err, base.ErrContentTooLarge) {
			http.Error(w, "upload too large", http.StatusRequestEntityTooLarge)
			return
		}

		h.Log.Errorw("failed to read nix nar upload", "error", err)
		http.Error(w, "failed to read upload", http.StatusBadRequest)

		return
	}
	defer spool.Close()

	if spool.SHA256Hex() != want.Hex() {
		http.Error(w, "the NAR file does not match the hash in its name", http.StatusBadRequest)
		return
	}

	// A staged NAR holds its bytes in the quota until its narinfo commits it
	// (the reservation moves to the path) or the staging sweep drops it.
	if !h.reserveQuota(r.Context(), w, repo, spool.Size(), 0) {
		return
	}

	staged, err := h.stageNar(r.Context(), repo.ID, file, spool)
	if err != nil || !staged {
		h.releaseQuota(r.Context(), repo, spool.Size(), 0)
	}

	if err != nil {
		h.Log.Errorw("failed to stage nix nar", "file", file, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	w.WriteHeader(http.StatusCreated)
}

// precheckNar refuses an upload the repository's quota has no room for before
// the body is spooled. A body of unknown length cannot be checked against a
// limited quota, so it is refused with 411.
func (h *HTTPHandler) precheckNar(w http.ResponseWriter, r *http.Request, repoID string) bool {
	if r.ContentLength < 0 {
		if _, limited, err := quota.Room(r.Context(), h.DB, repoID); err == nil && limited {
			http.Error(w, "a NAR upload to a repository with a size quota needs a Content-Length", http.StatusLengthRequired)
			return false
		}

		return true
	}

	if err := quota.Precheck(r.Context(), h.DB, repoID, r.ContentLength); err != nil {
		quota.WriteExceeded(w)
		return false
	}

	return true
}

// reserveQuota reserves size bytes and count artifacts of the repository's
// quota, answering 507 (or 500) when it cannot.
func (h *HTTPHandler) reserveQuota(ctx context.Context, w http.ResponseWriter, repo *models.Repository, size int64, count int) bool {
	err := quota.EnforceN(ctx, h.DB, repo.ID, size, count)
	if err == nil {
		return true
	}

	if errors.Is(err, quota.ErrExceeded) {
		quota.WriteExceeded(w)

		return false
	}

	h.Log.Errorw("failed to reserve quota", "repo", repo.Name, "error", err)
	http.Error(w, "internal error", http.StatusInternalServerError)

	return false
}

// releaseQuota returns a reservation of reserveQuota; a failure is only logged.
func (h *HTTPHandler) releaseQuota(ctx context.Context, repo *models.Repository, size int64, count int) {
	if err := quota.ReleaseN(ctx, h.DB, repo.ID, size, count); err != nil {
		h.Log.Warnw("failed to release quota", "repo", repo.Name, "error", err)
	}
}

// narKnown reports whether the NAR file is already stored or staged (an
// uploader that skipped the HEAD): the content is addressed by the hash in the
// name, so there is nothing to read again.
func (h *HTTPHandler) narKnown(ctx context.Context, repoID string, sum nix.SHA256, file string) (bool, error) {
	p, err := h.DB.GetNixPathByNarFile(ctx, repoID, file)
	if err != nil || p != nil {
		return p != nil, err
	}

	pending, err := h.DB.GetNixPendingNar(ctx, repoID, sum.Hex())

	return pending != nil, err
}

// stageNar stores the blob and its staging row, which records the quota
// reservation of the NAR's size. It reports whether the row is new: false when
// a concurrent upload staged the same file first (its row holds the
// reservation, the caller's must be returned). The blob is written before the
// per-blob lock is taken, see storage.CommitBlobs.
func (h *HTTPHandler) stageNar(ctx context.Context, repoID, file string, spool *base.Spool) (bool, error) {
	sha := spool.SHA256Hex()
	blob := storage.SeekerBlob(sha, spool.Reader(), map[string]string{"sha256": sha, "filename": file})

	var inserted bool

	err := storage.CommitBlob(ctx, h.DB, h.BlobStore, blob, func(ctx context.Context, blobKey string) error {
		var err error

		inserted, err = h.DB.PutNixPendingNar(ctx, &models.NixPendingNar{
			RepositoryID: repoID, SHA256: sha, NarFile: file, BlobKey: blobKey, SizeBytes: spool.Size(), ReservedBytes: spool.Size(),
		})

		return err
	})
	if err == nil {
		return inserted, nil
	}

	if cleanErr := storage.SafeDeleteBlob(ctx, h.DB, h.BlobStore, sha); cleanErr != nil {
		h.Log.Warnw("failed to clean up nix nar blob", "sha256", sha, "error", cleanErr)
	}

	return false, fmt.Errorf("stage nix nar: %w", err)
}

// discardAux accepts the files "nix copy" writes next to a path with
// write-nar-listing and index-debug-info, and drops them: nothing serves them.
func (h *HTTPHandler) discardAux(w http.ResponseWriter, r *http.Request) {
	if _, err := iocopy.Copy(io.Discard, io.LimitReader(r.Body, maxAuxBytes)); err != nil {
		http.Error(w, "failed to read upload", http.StatusBadRequest)
		return
	}

	w.WriteHeader(http.StatusOK)
}

// uploadedNarInfo is a parsed narinfo together with what it names.
type uploadedNarInfo struct {
	info *nix.NarInfo
	// file is the NAR file name below nar/ and fileHash its hash.
	file     string
	fileHash nix.SHA256
}

// readNarInfo reads, decodes and validates the body of a narinfo PUT. A
// non-zero status is the answer to send, with msg as its text.
func readNarInfo(r *http.Request, hash string) (*uploadedNarInfo, int, string) {
	body, err := readEncoded(r)
	if err != nil {
		if errors.Is(err, errUnsupportedEncoding) {
			return nil, http.StatusUnsupportedMediaType, err.Error()
		}

		return nil, http.StatusBadRequest, "failed to read the narinfo (too large, or not valid " + r.Header.Get("Content-Encoding") + ")"
	}

	info, err := nix.ParseNarInfo(body)
	if err != nil {
		return nil, http.StatusBadRequest, err.Error()
	}

	if info.StoreHash() != hash {
		return nil, http.StatusBadRequest, "the narinfo is for another store path than the one it is uploaded to"
	}

	file, fileHash, err := info.NarFile()
	if err != nil {
		return nil, http.StatusBadRequest, "URL must be nar/<nixbase32 sha256 of the file>.nar[.<compression>]"
	}

	if len(info.Sigs) > maxUploadSigs {
		return nil, http.StatusBadRequest, "too many signatures"
	}

	if err := checkFileHash(info, fileHash); err != nil {
		return nil, http.StatusBadRequest, err.Error()
	}

	return &uploadedNarInfo{info: info, file: file, fileHash: fileHash}, 0, ""
}

var (
	errFileHash         = errors.New("FileHash does not match the hash in URL")
	errFileHashRequired = errors.New("FileHash is required for a compressed NAR")
)

// checkFileHash requires the narinfo's FileHash to be the hash in its URL. A
// narinfo of an uncompressed NAR may omit it: the file is then the NAR itself.
func checkFileHash(info *nix.NarInfo, fileHash nix.SHA256) error {
	if info.FileHash == "" {
		narHash, _ := nix.ParseSHA256(info.NarHash)
		if info.Compression != "none" || narHash != fileHash {
			return errFileHashRequired
		}

		return nil
	}

	if got, _ := nix.ParseSHA256(info.FileHash); got != fileHash {
		return errFileHash
	}

	return nil
}

// readEncoded reads the request body, undoing the Content-Encoding "nix copy"
// applies to the narinfo with narinfo-compression. The decoded size is bounded.
func readEncoded(r *http.Request) ([]byte, error) {
	var rd io.Reader

	rd = io.LimitReader(r.Body, nix.MaxNarInfoBytes+1)

	switch enc := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Encoding"))); enc {
	case "", "identity":
	case "gzip", "x-gzip":
		zr, err := gzip.NewReader(rd)
		if err != nil {
			return nil, fmt.Errorf("gzip: %w", err)
		}

		rd = zr
	case "xz":
		xr, err := xzlimit.NewReader(bufio.NewReaderSize(rd, 64<<10))
		if err != nil {
			return nil, fmt.Errorf("xz: %w", err)
		}

		rd = xr
	case "zstd":
		zr, err := zstd.NewReader(rd, zstd.WithDecoderMaxWindow(maxZstdWindow), zstd.WithDecoderConcurrency(1))
		if err != nil {
			return nil, fmt.Errorf("zstd: %w", err)
		}
		defer zr.Close()

		rd = zr
	case "bzip2":
		rd = bzip2.NewReader(rd)
	default:
		return nil, fmt.Errorf("%w %q (use none, gzip, xz, zstd or bzip2)", errUnsupportedEncoding, enc)
	}

	return base.ReadAllLimited(rd, nix.MaxNarInfoBytes)
}

// putNarInfo commits a path: it checks the narinfo against the NAR file that
// was uploaded before it and stores the row.
func (h *HTTPHandler) putNarInfo(w http.ResponseWriter, r *http.Request, repo *models.Repository, hash string) {
	up, status, msg := readNarInfo(r, hash)
	if up == nil {
		http.Error(w, msg, status)
		return
	}

	existing, err := h.DB.GetNixPathByHash(r.Context(), repo.ID, hash)
	if err != nil {
		h.Log.Errorw("failed to check existing nix path", "hash", hash, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if existing != nil {
		h.answerExisting(w, up.info, existing)
		return
	}

	row, staged, status, msg := h.rowFor(r.Context(), repo.ID, up)
	if row == nil {
		http.Error(w, msg, status)
		return
	}

	h.commitPath(w, r, repo, row, staged)
}

// answerExisting handles a narinfo for a path the repository already has:
// the same NAR is an idempotent repeat, anything else a conflict. Store paths
// do not change once published.
func (h *HTTPHandler) answerExisting(w http.ResponseWriter, info *nix.NarInfo, existing *models.NixPath) {
	have, _ := nix.ParseSHA256(existing.NarHash)
	got, _ := nix.ParseSHA256(info.NarHash)

	if have != got {
		http.Error(w, "the repository already has this store path with different contents", http.StatusConflict)
		return
	}

	w.WriteHeader(http.StatusOK)
}

// rowFor builds the row of an uploaded narinfo from the NAR file that is
// staged (or stored under another path) under its hash. status and msg say why
// no row results.
//
// staged is how many bytes of the NAR the staging row already holds in the
// repository's quota (0 for a NAR another path stored).
func (h *HTTPHandler) rowFor(
	ctx context.Context,
	repoID string,
	up *uploadedNarInfo,
) (*models.NixPath, int64, int, string) {
	nar, status, msg := h.findNar(ctx, repoID, up)
	if status != 0 {
		return nil, 0, status, msg
	}

	info := up.info
	if info.FileHash != "" && info.FileSize != nar.size {
		return nil, 0, http.StatusBadRequest, "FileSize does not match the uploaded NAR file"
	}

	return &models.NixPath{
		RepositoryID: repoID,
		StoreHash:    info.StoreHash(),
		Name:         info.Name(),
		NarHash:      canonical(info.NarHash),
		NarSize:      info.NarSize,
		NarFile:      up.file,
		Compression:  info.Compression,
		References:   info.References,
		Deriver:      info.Deriver,
		CA:           info.CA,
		Signatures:   info.Sigs,
		BlobKey:      nar.blobKey,
		SHA256:       nar.sha,
		SizeBytes:    nar.size,
		CreatedAt:    importmeta.CreatedAt(ctx),
	}, nar.reserved, 0, ""
}

// canonical returns the "sha256:<nixbase32>" spelling of a validated hash.
func canonical(s string) string {
	h, _ := nix.ParseSHA256(s)
	return h.String()
}

// narFile is the NAR file a narinfo names.
type narFile struct {
	sha     string
	size    int64
	blobKey string
	// reserved is the quota the staging row holds for it (0 when stored).
	reserved int64
}

// findNar locates the NAR file a narinfo names: staged by an earlier PUT, or
// already stored for another path of the repository. A non-zero status is the
// answer to send.
func (h *HTTPHandler) findNar(ctx context.Context, repoID string, up *uploadedNarInfo) (narFile, int, string) {
	sha := up.fileHash.Hex()

	pending, err := h.DB.GetNixPendingNar(ctx, repoID, sha)
	if err != nil {
		h.Log.Errorw("failed to get staged nix nar", "sha256", sha, "error", err)
		return narFile{}, http.StatusInternalServerError, "internal error"
	}

	if pending != nil {
		if pending.NarFile != up.file {
			return narFile{}, http.StatusBadRequest, "the NAR file was uploaded as nar/" + pending.NarFile
		}

		return narFile{sha: sha, size: pending.SizeBytes, blobKey: pending.BlobKey, reserved: pending.ReservedBytes}, 0, ""
	}

	other, err := h.DB.GetNixPathByNarFile(ctx, repoID, up.file)
	if err != nil {
		h.Log.Errorw("failed to get nix nar", "file", up.file, "error", err)
		return narFile{}, http.StatusInternalServerError, "internal error"
	}

	if other == nil {
		return narFile{}, http.StatusConflict, "the NAR file nar/" + up.file + " has not been uploaded; PUT it before its narinfo"
	}

	return narFile{sha: other.SHA256, size: other.SizeBytes, blobKey: other.BlobKey}, 0, ""
}

// commitPath reserves quota, inserts the row under the NAR blob's lock (a
// concurrent sweep of stale staging cannot delete the blob in between) and
// answers 201. The bytes the NAR's staging row already holds (staged) are not
// reserved again: the path takes them over when the row is created, and the
// staging row's reservation stays in place when the commit fails.
func (h *HTTPHandler) commitPath(w http.ResponseWriter, r *http.Request, repo *models.Repository, row *models.NixPath, staged int64) {
	ctx := r.Context()
	fresh := row.SizeBytes - min(staged, row.SizeBytes)

	if !h.reserveQuota(ctx, w, repo, fresh, 1) {
		return
	}

	err := storage.CommitBlob(ctx, h.DB, h.BlobStore, storage.Blob{SHA256: row.SHA256},
		func(ctx context.Context, blobKey string) error {
			row.BlobKey = blobKey

			if err := h.DB.CreateNixPath(ctx, row); err != nil {
				return fmt.Errorf("create nix path: %w", err)
			}

			return nil
		})
	if err != nil {
		h.releaseQuota(ctx, repo, fresh, 1)

		h.writeCommitError(w, row, err)

		return
	}

	h.Emit(ctx, repo.ID, webhook.EventArtifactUploaded, webhook.ArtifactPayload(webhook.EventArtifactUploaded, webhookArtifact(repo, row)))

	w.WriteHeader(http.StatusCreated)
}

func (h *HTTPHandler) writeCommitError(w http.ResponseWriter, row *models.NixPath, err error) {
	switch {
	case errors.Is(err, database.ErrDuplicate) || database.IsUniqueViolation(err):
		// A concurrent upload of the same path won.
		http.Error(w, "store path already exists", http.StatusConflict)
	case errors.Is(err, storage.ErrBlobGone):
		http.Error(w, "the NAR file is no longer staged; upload it again", http.StatusConflict)
	default:
		h.Log.Errorw("failed to store nix path", "hash", row.StoreHash, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

func webhookArtifact(repo *models.Repository, p *models.NixPath) webhook.Artifact {
	return webhook.Artifact{
		Repository: repo.Name,
		Format:     FormatName,
		Name:       p.Name,
		Version:    p.StoreHash,
		Path:       p.StoreHash + ".narinfo",
		SHA256:     p.SHA256,
		Size:       p.SizeBytes,
	}
}

// deletePath removes a store path from the repository (to the trash when the
// repository has one). Its NAR file goes with it unless another path shares it.
func (h *HTTPHandler) deletePath(w http.ResponseWriter, r *http.Request, repo *models.Repository, hash string) {
	p, err := h.DB.GetNixPathByHash(r.Context(), repo.ID, hash)
	if err != nil {
		h.Log.Errorw("failed to get nix path", "hash", hash, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if p == nil {
		http.NotFound(w, r)
		return
	}

	if gErr := trash.GuardReleaseByName(r, h.DB, repo.Name, FormatName, p.StoreHash); gErr != nil {
		if errors.Is(gErr, trash.ErrImmutableRelease) {
			http.Error(w, "repository has immutable releases", http.StatusConflict)
			return
		}

		h.Log.Errorw("failed to check release immutability", "name", p.Name, "error", gErr)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	err = trash.Delete(r.Context(), h.DB, trash.Request{
		RepoID:   repo.ID,
		Kind:     models.TrashKindNix,
		IDs:      []string{p.ID},
		Resource: hash + ".narinfo",
		Package:  p.Name,
		Version:  p.StoreHash,
	}, func() error {
		return h.hardDelete(r.Context(), repo, p)
	})
	if err != nil {
		if errors.Is(err, database.ErrTrashNotFound) {
			http.NotFound(w, r)
			return
		}

		h.Log.Errorw("failed to delete nix path", "id", p.ID, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	h.Emit(
		r.Context(),
		repo.ID,
		webhook.EventArtifactDeleted,
		webhook.ArtifactPayload(webhook.EventArtifactDeleted, webhookArtifact(repo, p)),
	)
	w.WriteHeader(http.StatusNoContent)
}

// hardDelete is the permanent delete: the row, the quota it held and, once no
// row references it, the content-addressed blob.
func (h *HTTPHandler) hardDelete(ctx context.Context, repo *models.Repository, p *models.NixPath) error {
	if err := h.DB.DeleteNixPath(ctx, repo.ID, p.ID); err != nil {
		return fmt.Errorf("delete nix path: %w", err)
	}

	h.warn(quota.Release(ctx, h.DB, repo.ID, p.SizeBytes), "failed to release quota")
	h.warn(storage.SafeDeleteBlob(ctx, h.DB, h.BlobStore, p.SHA256), "failed to clean up blob after nix delete")

	return nil
}

// warn logs a failed cleanup step that must not fail the request.
func (h *HTTPHandler) warn(err error, what string) {
	if err != nil {
		h.Log.Warnw(what, "error", err)
	}
}
