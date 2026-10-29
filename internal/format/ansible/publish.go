// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package ansible

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	"github.com/openartifact/openartifact/internal/importmeta"
	"github.com/openartifact/openartifact/internal/iocopy"
	"github.com/openartifact/openartifact/internal/licensepolicy"
	"github.com/openartifact/openartifact/internal/quota"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/trash"
	"github.com/openartifact/openartifact/internal/webhook"
	"github.com/openartifact/openartifact/pkg/ansible"
)

const (
	// maxPublishBytes bounds one published collection artifact.
	maxPublishBytes = 500 << 20
	// formOverhead is what the multipart envelope may add to the artifact.
	formOverhead = 1 << 20
	// formMemory is how much of the form the parser keeps in memory; the rest
	// goes to a temporary file.
	formMemory = 8 << 20
	// artifactMaxAge is how long shared caches may keep a public artifact: it
	// never changes under its file name.
	artifactMaxAge = 3600
)

// body is the artifact of an upload, read in place and hashed.
type body interface {
	Size() int64
	SHA256Hex() string
	Reader() *io.SectionReader
	Close()
}

// upload is a parsed publish request.
type upload struct {
	part     body
	filename string
	// sha256 is the checksum the client declared, "" when it sent none.
	sha256 string
}

// readUpload parses the multipart form of "ansible-galaxy collection publish":
// the artifact in "file", its checksum in "sha256". On failure it writes the
// response and returns nil; the caller closes the result.
func (h *HTTPHandler) readUpload(w http.ResponseWriter, r *http.Request) *upload {
	r.Body = http.MaxBytesReader(w, r.Body, maxPublishBytes+formOverhead)

	if err := r.ParseMultipartForm(formMemory); err != nil { //nolint:gosec // r.Body is bounded by http.MaxBytesReader above
		if _, tooBig := errors.AsType[*http.MaxBytesError](err); tooBig {
			writeError(w, http.StatusRequestEntityTooLarge, "too_large", "Collection too large.", "")
			return nil
		}

		writeError(w, http.StatusBadRequest, "invalid", "Invalid multipart form.", "")

		return nil
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid", "Missing the file part.", "")
		return nil
	}

	if header.Size > maxPublishBytes {
		_ = file.Close()

		writeError(w, http.StatusRequestEntityTooLarge, "too_large", "Collection too large.", "")

		return nil
	}

	part, err := readPart(file, header)
	if err != nil {
		if errors.Is(err, base.ErrContentTooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "too_large", "Collection too large.", "")
			return nil
		}

		h.Log.Errorw("failed to read uploaded collection", "error", err)
		writeError(w, http.StatusBadRequest, "invalid", "Cannot read the uploaded file.", "")

		return nil
	}

	return &upload{
		part:     part,
		filename: path.Base(strings.ReplaceAll(header.Filename, "\\", "/")),
		sha256:   strings.ToLower(strings.TrimSpace(r.FormValue("sha256"))),
	}
}

// readPart hashes the uploaded file. ansible-galaxy sends the artifact base64
// encoded (Content-Transfer-Encoding: base64), which the multipart reader does
// not undo; it is decoded here into a spool.
func readPart(file multipart.File, header *multipart.FileHeader) (body, error) {
	if !strings.EqualFold(strings.TrimSpace(header.Header.Get("Content-Transfer-Encoding")), "base64") {
		return base.HashPart(file, header.Size)
	}

	defer func() { _ = file.Close() }()

	spool, err := base.SpoolLimited(base64.NewDecoder(base64.StdEncoding, file), maxPublishBytes)
	if err != nil {
		return nil, fmt.Errorf("decode base64 file: %w", err)
	}

	return spool, nil
}

// check reads and validates the artifact and compares it with what the client
// declared. The returned message is empty when the upload is acceptable.
func (u *upload) check() (*ansible.Collection, string) {
	if u.sha256 != "" && u.sha256 != u.part.SHA256Hex() {
		return nil, "The sha256 checksum did not match."
	}

	col, err := ansible.Read(u.part.Reader())
	if err != nil {
		if errors.Is(err, ansible.ErrInvalid) {
			return nil, err.Error()
		}

		return nil, "Cannot read the collection: " + err.Error()
	}

	if want := ansible.FileName(col.Namespace, col.Name, col.Version); u.filename != want {
		return nil, fmt.Sprintf("The file name %q must be %q, after the MANIFEST.json.", u.filename, want)
	}

	return col, ""
}

func (h *HTTPHandler) handlePublish(w http.ResponseWriter, r *http.Request) {
	repo := h.resolveRepo(w, r)
	if repo == nil {
		return
	}

	up := h.readUpload(w, r)
	if up == nil {
		return
	}
	defer up.part.Close()

	col, msg := up.check()
	if msg != "" {
		writeError(w, http.StatusBadRequest, "invalid", "Invalid collection.", msg)
		return
	}

	if licensepolicy.Enforce(w, r, h.DB, h.Log, repo.ID, col.Licenses...) {
		return
	}

	existing, err := h.DB.GetAnsibleCollection(r.Context(), repo.ID, col.Namespace, col.Name, col.Version)
	if err != nil {
		h.Log.Errorw("failed to look up ansible collection", "collection", ansible.FQCN(col.Namespace, col.Name), "error", err)
		writeError(w, http.StatusInternalServerError, "error", "Internal error.", "")

		return
	}

	if existing != nil {
		writeConflict(w, col)
		return
	}

	row := &models.AnsibleCollection{
		RepositoryID:    repo.ID,
		Namespace:       col.Namespace,
		Name:            col.Name,
		Version:         col.Version,
		Filename:        up.filename,
		SizeBytes:       up.part.Size(),
		SHA256:          up.part.SHA256Hex(),
		Dependencies:    col.Dependencies,
		Metadata:        col.Info,
		RequiresAnsible: col.RequiresAnsible,
	}

	if !h.store(w, r, repo, row, up.part) {
		return
	}

	h.Emit(r.Context(), repo.ID, webhook.EventArtifactUploaded, webhook.ArtifactPayload(webhook.EventArtifactUploaded, webhook.Artifact{
		Repository: repo.Name,
		Format:     FormatName,
		Name:       ansible.FQCN(row.Namespace, row.Name),
		Version:    row.Version,
		Path:       row.Filename,
		SHA256:     row.SHA256,
		Size:       row.SizeBytes,
	}))

	base.WriteJSON(
		w,
		http.StatusAccepted,
		[]byte(`{"task":`+strconv.Quote(apiPath(h.PublicBase(w, r), repo.Name)+"/imports/collections/"+row.ID+"/")+`}`),
	)
}

func writeConflict(w http.ResponseWriter, col *ansible.Collection) {
	writeError(w, http.StatusConflict, "conflict", "Version already exists.",
		"Collection "+ansible.FQCN(col.Namespace, col.Name)+" "+col.Version+" is already published.")
}

// store reserves the quota and persists the artifact and its row. On failure it
// writes the response and returns false.
func (h *HTTPHandler) store(
	w http.ResponseWriter,
	r *http.Request,
	repo *models.Repository,
	row *models.AnsibleCollection,
	part body,
) bool {
	if err := quota.Enforce(r.Context(), h.DB, repo.ID, row.SizeBytes); err != nil {
		if errors.Is(err, quota.ErrExceeded) {
			quota.WriteExceeded(w)
			return false
		}

		h.Log.Errorw("failed to reserve quota", "repo", repo.Name, "error", err)
		writeError(w, http.StatusInternalServerError, "error", "Internal error.", "")

		return false
	}

	err := h.commit(r.Context(), row, part)
	if err == nil {
		return true
	}

	h.ReleaseQuota(r.Context(), repo.ID, repo.Name, row.SizeBytes)

	if errors.Is(err, database.ErrDuplicate) || database.IsUniqueViolation(err) {
		writeConflict(w, &ansible.Collection{Namespace: row.Namespace, Name: row.Name, Version: row.Version})
		return false
	}

	h.Log.Errorw("failed to store ansible collection", "filename", row.Filename, "error", err)
	writeError(w, http.StatusInternalServerError, "error", "Internal error.", "")

	return false
}

// commit stores the blob first, then inserts the referencing row under the
// per-blob lock so a concurrent delete cannot remove the blob in between. When
// no row results the blob is dropped again unless something else references it.
func (h *HTTPHandler) commit(ctx context.Context, row *models.AnsibleCollection, part body) (err error) {
	defer func() {
		if err != nil {
			h.dropBlob(ctx, row.SHA256, "publish")
		}
	}()

	blob := storage.SeekerBlob(row.SHA256, part.Reader(), map[string]string{"sha256": row.SHA256, "filename": row.Filename})

	return storage.CommitBlob(ctx, h.DB, h.BlobStore, blob, func(ctx context.Context, blobKey string) error {
		row.BlobKey = blobKey
		row.CreatedAt = importmeta.CreatedAt(ctx)

		return h.DB.CreateAnsibleCollection(ctx, row) //nolint:wrapcheck // the caller reports the failure
	})
}

// dropBlob removes the content-addressed blob once no row references it.
func (h *HTTPHandler) dropBlob(ctx context.Context, sha256, after string) {
	if err := storage.SafeDeleteBlob(ctx, h.DB, h.BlobStore, sha256); err != nil {
		h.Log.Warnw("failed to clean up ansible blob", "after", after, "sha256", sha256, "error", err)
	}
}

// serveBlob streams the artifact of c.
func (h *HTTPHandler) serveBlob(w http.ResponseWriter, r *http.Request, c *models.AnsibleCollection) {
	blob, err := h.BlobStore.Get(r.Context(), c.BlobKey)
	if err != nil {
		h.Log.Errorw("failed to get ansible blob", "key", c.BlobKey, "error", err)
		writeError(w, http.StatusInternalServerError, "error", "Internal error.", "")

		return
	}
	defer base.CloseLog(blob, h.Log)

	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Length", strconv.FormatInt(c.SizeBytes, 10))
	w.Header().Set("Content-Disposition", `attachment; filename="`+c.Filename+`"`)

	if _, err := iocopy.Copy(w, blob); err != nil {
		h.Log.Warnw("failed to stream ansible collection", "filename", c.Filename, "error", err)
	}
}

func (h *HTTPHandler) handleDelete(w http.ResponseWriter, r *http.Request) {
	repo := h.resolveRepo(w, r)
	if repo == nil {
		return
	}

	c := h.lookupVersion(w, r, repo)
	if c == nil {
		return
	}

	if gErr := trash.GuardReleaseByName(r, h.DB, repo.Name, FormatName, c.Version); gErr != nil {
		if errors.Is(gErr, trash.ErrImmutableRelease) {
			writeError(w, http.StatusConflict, "conflict", "Repository has immutable releases.", "")
			return
		}

		h.Log.Errorw("failed to check release immutability", "name", c.Name, "error", gErr)
		writeError(w, http.StatusInternalServerError, "error", "Internal error.", "")

		return
	}

	err := trash.Delete(r.Context(), h.DB, trash.Request{
		RepoID:   repo.ID,
		Kind:     models.TrashKindAnsible,
		IDs:      []string{c.ID},
		Resource: c.Filename,
		Package:  ansible.FQCN(c.Namespace, c.Name),
		Version:  c.Version,
	}, func() error { return h.hardDelete(r.Context(), repo, c) })
	if err != nil {
		if errors.Is(err, database.ErrTrashNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "Not found.", "")
			return
		}

		h.Log.Errorw("failed to delete ansible collection", "id", c.ID, "error", err)
		writeError(w, http.StatusInternalServerError, "error", "Internal error.", "")

		return
	}

	h.Emit(r.Context(), repo.ID, webhook.EventArtifactDeleted, webhook.ArtifactPayload(webhook.EventArtifactDeleted, webhook.Artifact{
		Repository: repo.Name,
		Format:     FormatName,
		Name:       ansible.FQCN(c.Namespace, c.Name),
		Version:    c.Version,
		Path:       c.Filename,
		SHA256:     c.SHA256,
		Size:       c.SizeBytes,
	}))

	w.WriteHeader(http.StatusNoContent)
}

// hardDelete is the permanent delete: the row, the quota it held and, once no
// row references it, the content-addressed blob.
func (h *HTTPHandler) hardDelete(ctx context.Context, repo *models.Repository, c *models.AnsibleCollection) error {
	return h.DeleteAccounted(ctx, repo.ID, c.SHA256, c.SizeBytes, func(ctx context.Context) error {
		return h.DB.DeleteAnsibleCollection(ctx, repo.ID, c.ID) //nolint:wrapcheck // wrapped by DeleteAccounted
	})
}
