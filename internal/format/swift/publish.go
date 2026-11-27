// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package swift

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"regexp"
	"strings"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	"github.com/openartifact/openartifact/internal/importmeta"
	"github.com/openartifact/openartifact/internal/quota"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/trash"
	"github.com/openartifact/openartifact/internal/webhook"
	"github.com/openartifact/openartifact/pkg/swift"
)

const (
	// maxArchiveBytes bounds one published source archive.
	maxArchiveBytes = 256 << 20
	// maxPartBytes bounds the metadata and each signature of a publish request.
	maxPartBytes = swift.MaxMetadataBytes
	// formOverhead is what the multipart envelope may add to the parts.
	formOverhead = 1 << 20

	// Names of the multipart sections of a publish request.
	partArchive           = swift.SourceArchive
	partArchiveSignature  = "source-archive-signature"
	partMetadata          = "metadata"
	partMetadataSignature = "metadata-signature"

	// signatureFormatHeader names the signature format of a signed release.
	signatureFormatHeader = "X-Swift-Package-Signature-Format"
	// maxSignatureFormatLen bounds the signature format a client names.
	maxSignatureFormatLen = 64
)

// signatureFormatRe matches a signature format such as "cms-1.0.0".
var signatureFormatRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// upload is a parsed publish request. The signatures are base64 encoded as the
// release document carries them.
type upload struct {
	archive           *base.Spool
	archiveSignature  []byte
	metadata          []byte
	metadataSignature []byte

	seen map[string]bool
}

func (u *upload) close() {
	if u.archive != nil {
		u.archive.Close()
	}
}

// errBadUpload is a malformed publish request.
var errBadUpload = errors.New("invalid publish request")

// partReader returns the content of a multipart section: decoded when the
// client sent it base64 encoded (Content-Transfer-Encoding: base64), which the
// multipart reader does not undo, and as is otherwise.
func partReader(part io.Reader, header map[string][]string) io.Reader {
	for _, v := range header["Content-Transfer-Encoding"] {
		if strings.EqualFold(strings.TrimSpace(v), "base64") {
			return base64.NewDecoder(base64.StdEncoding, part)
		}
	}

	return part
}

// readSmall reads a section that is at most maxPartBytes long.
func readSmall(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxPartBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read part: %w", err)
	}

	if len(data) > maxPartBytes {
		return nil, base.ErrContentTooLarge
	}

	return data, nil
}

// readUpload reads the multipart body of a publish request. The sections are
// found by name and need no file name; an unknown one is ignored.
func readUpload(r *http.Request) (*upload, error) {
	mr, err := r.MultipartReader()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errBadUpload, err)
	}

	up := &upload{seen: map[string]bool{}}

	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			return up, nil
		}

		if err != nil {
			up.close()
			return nil, fmt.Errorf("%w: %w", errBadUpload, err)
		}

		if err := up.readPart(part.FormName(), partReader(part, part.Header)); err != nil {
			up.close()
			return nil, err
		}
	}
}

// readPart stores the section name read from src.
func (u *upload) readPart(name string, src io.Reader) error {
	switch name {
	case partArchive, partArchiveSignature, partMetadata, partMetadataSignature:
	default:
		return nil
	}

	if u.seen[name] {
		return fmt.Errorf("%w: more than one %q section", errBadUpload, name)
	}

	u.seen[name] = true

	if name == partArchive {
		spool, err := base.SpoolLimited(src, maxArchiveBytes)
		if err != nil {
			return fmt.Errorf("read %s: %w", name, err)
		}

		u.archive = spool

		return nil
	}

	data, err := readSmall(src)
	if err != nil {
		return fmt.Errorf("read %s: %w", name, err)
	}

	switch name {
	case partArchiveSignature:
		u.archiveSignature = data
	case partMetadata:
		u.metadata = data
	default:
		u.metadataSignature = data
	}

	return nil
}

// tooLarge reports whether err is the request body or a section exceeding its
// limit.
func tooLarge(err error) bool {
	var maxBytes *http.MaxBytesError

	return base.IsTooLarge(err) || errors.Is(err, base.ErrContentTooLarge) || errors.As(err, &maxBytes)
}

// handlePublish serves PUT /{scope}/{name}/{version}: the publication of a
// release, synchronously. Everything that can be decided from the headers is
// decided before the body is read, so a client that sent Expect: 100-continue
// is refused without uploading the archive.
func (h *HTTPHandler) handlePublish(w http.ResponseWriter, r *http.Request) {
	repo := h.resolveRepo(w, r)
	if repo == nil {
		return
	}

	scope, name, version := param(r, "scope"), param(r, "pkg"), param(r, "version")
	if !swift.ValidScope(scope) || !swift.ValidName(name) || !swift.ValidPublishVersion(version) {
		swift.WriteProblem(w, http.StatusUnprocessableEntity, "invalid package scope, name or version")
		return
	}

	if !h.checkHeaders(w, r) {
		return
	}

	existing, err := h.DB.GetSwiftRelease(r.Context(), repo.ID, scope, name, version)
	if err != nil {
		h.internalError(w, "failed to look up swift release", err, "package", swift.ID(scope, name))
		return
	}

	if existing != nil {
		writeConflict(w, version)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxArchiveBytes+3*maxPartBytes+formOverhead)

	up, err := readUpload(r)
	if err != nil {
		h.uploadFailed(w, err)
		return
	}
	defer up.close()

	row, msg := h.release(repo, up, r.Header.Get(signatureFormatHeader))
	if msg != "" {
		swift.WriteProblem(w, http.StatusUnprocessableEntity, msg)
		return
	}

	row.Scope, row.Name, row.Version = scope, name, version
	row.Filename = name + "-" + version + ".zip"

	if !h.store(w, r, repo, row, up.archive) {
		return
	}

	h.Emit(r.Context(), repo.ID, webhook.EventArtifactUploaded, webhook.ArtifactPayload(webhook.EventArtifactUploaded, webhook.Artifact{
		Repository: repo.Name,
		Format:     FormatName,
		Name:       swift.ID(scope, name),
		Version:    version,
		Path:       row.Filename,
		SHA256:     row.SHA256,
		Size:       row.SizeBytes,
	}))

	w.Header().Set("Location", releaseURL(h.PublicBase(w, r), repo.Name, scope, name, version))
	w.WriteHeader(http.StatusCreated)
}

// checkHeaders refuses a publish request whose headers already show it cannot
// succeed: not a multipart body, a body that is too large or a malformed
// signature format.
func (h *HTTPHandler) checkHeaders(w http.ResponseWriter, r *http.Request) bool {
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "multipart/form-data" {
		swift.WriteProblem(w, http.StatusUnsupportedMediaType, "expected a multipart/form-data body")
		return false
	}

	if r.ContentLength > maxArchiveBytes+3*maxPartBytes+formOverhead {
		swift.WriteProblem(w, http.StatusRequestEntityTooLarge, "the release is too large")
		return false
	}

	if f := r.Header.Get(signatureFormatHeader); len(f) > maxSignatureFormatLen || (f != "" && !signatureFormatRe.MatchString(f)) {
		swift.WriteProblem(w, http.StatusBadRequest, "invalid "+signatureFormatHeader+" header")
		return false
	}

	return true
}

// uploadFailed answers a publish request whose body could not be read.
func (h *HTTPHandler) uploadFailed(w http.ResponseWriter, err error) {
	var corrupt base64.CorruptInputError

	switch {
	case tooLarge(err):
		swift.WriteProblem(w, http.StatusRequestEntityTooLarge, "the release is too large")
	case errors.Is(err, errBadUpload), errors.Is(err, io.ErrUnexpectedEOF), errors.As(err, &corrupt):
		swift.WriteProblem(w, http.StatusBadRequest, "invalid multipart body: "+err.Error())
	default:
		h.internalError(w, "failed to read swift publish request", err)
	}
}

// release validates what the client sent and builds the row, without the
// identity and the blob key. The returned message is empty when the release is
// acceptable.
func (h *HTTPHandler) release(repo *models.Repository, up *upload, signatureFormat string) (*models.SwiftRelease, string) {
	if up.archive == nil {
		return nil, "the request has no " + partArchive + " section"
	}

	if len(up.archiveSignature) > 0 && signatureFormat == "" {
		return nil, "a signed release needs the " + signatureFormatHeader + " header"
	}

	manifests, err := swift.ReadArchive(up.archive, up.archive.Size())
	if err != nil {
		if errors.Is(err, swift.ErrInvalidArchive) {
			return nil, err.Error()
		}

		h.Log.Warnw("cannot read swift source archive", "repo", repo.Name, "error", err)

		return nil, "cannot read the source archive"
	}

	data := up.metadata
	if len(data) == 0 {
		data = []byte("{}")
	}

	raw, meta, err := swift.ParseMetadata(data)
	if err != nil {
		return nil, err.Error()
	}

	row := &models.SwiftRelease{
		RepositoryID:   repo.ID,
		SizeBytes:      up.archive.Size(),
		SHA256:         up.archive.SHA256Hex(),
		Metadata:       raw,
		RepositoryURLs: normalizedURLs(meta.RepositoryURLs),
		Manifests:      make([]models.SwiftManifest, len(manifests)),
	}

	for i, m := range manifests {
		row.Manifests[i] = models.SwiftManifest{
			Filename: m.Filename, SwiftVersion: m.SwiftVersion, ToolsVersion: m.ToolsVersion, Content: m.Content,
		}
	}

	if len(up.archiveSignature) > 0 {
		row.SignatureFormat = signatureFormat
		row.Signature = base64.StdEncoding.EncodeToString(up.archiveSignature)
	}

	if len(up.metadataSignature) > 0 {
		row.MetadataSignature = base64.StdEncoding.EncodeToString(up.metadataSignature)
	}

	return row, ""
}

// normalizedURLs returns the distinct normalized forms of the repository URLs,
// dropping the ones that normalize to nothing.
func normalizedURLs(urls []string) []string {
	out := make([]string, 0, len(urls))

	for _, u := range urls {
		n := swift.NormalizeURL(u)
		if n == "" {
			continue
		}

		dup := false

		for _, o := range out {
			dup = dup || o == n
		}

		if !dup {
			out = append(out, n)
		}
	}

	return out
}

func writeConflict(w http.ResponseWriter, version string) {
	swift.WriteProblem(w, http.StatusConflict, "a release with version "+version+" already exists")
}

// store reserves the quota and persists the archive and its row. On failure it
// writes the response and returns false.
func (h *HTTPHandler) store(
	w http.ResponseWriter,
	r *http.Request,
	repo *models.Repository,
	row *models.SwiftRelease,
	archive *base.Spool,
) bool {
	if err := quota.Enforce(r.Context(), h.DB, repo.ID, row.SizeBytes); err != nil {
		if errors.Is(err, quota.ErrExceeded) {
			swift.WriteProblem(w, http.StatusInsufficientStorage, quota.Message)
			return false
		}

		h.internalError(w, "failed to reserve quota", err, "repo", repo.Name)

		return false
	}

	err := h.commit(r.Context(), row, archive)
	if err == nil {
		return true
	}

	h.ReleaseQuota(r.Context(), repo.ID, repo.Name, row.SizeBytes)

	if errors.Is(err, database.ErrDuplicate) || database.IsUniqueViolation(err) {
		writeConflict(w, row.Version)
		return false
	}

	h.internalError(w, "failed to store swift release", err, "filename", row.Filename)

	return false
}

// commit stores the blob first, then inserts the referencing row under the
// per-blob lock so a concurrent delete cannot remove the blob in between. When
// no row results the blob is dropped again unless something else references it.
func (h *HTTPHandler) commit(ctx context.Context, row *models.SwiftRelease, archive *base.Spool) (err error) {
	defer func() {
		if err != nil {
			h.dropBlob(ctx, row.SHA256, "publish")
		}
	}()

	blob := storage.SeekerBlob(row.SHA256, archive.Reader(), map[string]string{"sha256": row.SHA256, "filename": row.Filename})

	return storage.CommitBlob(ctx, h.DB, h.BlobStore, blob, func(ctx context.Context, blobKey string) error {
		row.BlobKey = blobKey
		row.CreatedAt = importmeta.CreatedAt(ctx)

		return h.DB.CreateSwiftRelease(ctx, row) //nolint:wrapcheck // the caller reports the failure
	})
}

// dropBlob removes the content-addressed blob once no row references it.
func (h *HTTPHandler) dropBlob(ctx context.Context, sha256, after string) {
	if err := storage.SafeDeleteBlob(ctx, h.DB, h.BlobStore, sha256); err != nil {
		h.Log.Warnw("failed to clean up swift blob", "after", after, "sha256", sha256, "error", err)
	}
}

// handleDelete serves DELETE /{scope}/{name}/{version}: the removal of a
// release. The registry specification has no such operation; SwiftPM does not
// use it.
func (h *HTTPHandler) handleDelete(w http.ResponseWriter, r *http.Request) {
	repo := h.resolveRepo(w, r)
	if repo == nil {
		return
	}

	p, ok := splitRelease(w, r, swift.KindRelease)
	if !ok {
		return
	}

	rel := h.lookup(w, r, repo, p)
	if rel == nil {
		return
	}

	if gErr := trash.GuardReleaseByName(r, h.DB, repo.Name, FormatName, rel.Version); gErr != nil {
		if errors.Is(gErr, trash.ErrImmutableRelease) {
			swift.WriteProblem(w, http.StatusConflict, "the repository has immutable releases")
			return
		}

		h.internalError(w, "failed to check release immutability", gErr, "package", swift.ID(rel.Scope, rel.Name))

		return
	}

	err := trash.Delete(r.Context(), h.DB, trash.Request{
		RepoID:   repo.ID,
		Kind:     models.TrashKindSwift,
		IDs:      []string{rel.ID},
		Resource: rel.Filename,
		Package:  swift.ID(rel.Scope, rel.Name),
		Version:  rel.Version,
	}, func() error { return h.hardDelete(r.Context(), repo, rel) })
	if err != nil {
		if errors.Is(err, database.ErrTrashNotFound) {
			notFound(w, "release")
			return
		}

		h.internalError(w, "failed to delete swift release", err, "id", rel.ID)

		return
	}

	h.Emit(r.Context(), repo.ID, webhook.EventArtifactDeleted, webhook.ArtifactPayload(webhook.EventArtifactDeleted, webhook.Artifact{
		Repository: repo.Name,
		Format:     FormatName,
		Name:       swift.ID(rel.Scope, rel.Name),
		Version:    rel.Version,
		Path:       rel.Filename,
		SHA256:     rel.SHA256,
		Size:       rel.SizeBytes,
	}))

	w.WriteHeader(http.StatusNoContent)
}

// hardDelete is the permanent delete: the row, the quota it held and, once no
// row references it, the content-addressed blob.
func (h *HTTPHandler) hardDelete(ctx context.Context, repo *models.Repository, rel *models.SwiftRelease) error {
	return h.DeleteAccounted(ctx, repo.ID, rel.SHA256, rel.SizeBytes, func(ctx context.Context) error {
		return h.DB.DeleteSwiftRelease(ctx, repo.ID, rel.ID) //nolint:wrapcheck // wrapped by DeleteAccounted
	})
}
