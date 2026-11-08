// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package pypi

import (
	"context"
	"crypto/md5" //nolint:gosec // required by protocol specification
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	"github.com/openartifact/openartifact/internal/importmeta"
	"github.com/openartifact/openartifact/internal/iocopy"
	"github.com/openartifact/openartifact/internal/licensemeta"
	"github.com/openartifact/openartifact/internal/licensepolicy"
	"github.com/openartifact/openartifact/internal/pathseg"
	"github.com/openartifact/openartifact/internal/presign"
	"github.com/openartifact/openartifact/internal/pypisimple"
	"github.com/openartifact/openartifact/internal/quarantine"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/trash"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/internal/webhook"
	"github.com/openartifact/openartifact/pkg/logger"
)

// HTTPStore is the part of the database HTTPHandler needs.
type HTTPStore interface {
	quarantine.Checker
	BlobReferenceCount(ctx context.Context, sha256 string) (int, error)
	CreatePyPIPackage(ctx context.Context, pkg *models.PyPIPackage) error
	DecrementStorageUsage(ctx context.Context, repoID string, sizeBytes int64, artifactCount int) error
	DeletePyPIPackage(ctx context.Context, repoID string, name string, version string, filename string) error
	GetPyPIPackageByFilename(ctx context.Context, repoID string, filename string) (*models.PyPIPackage, error)
	GetRawArtifact(ctx context.Context, repoID string, path string) (*models.RawArtifact, error)
	GetRepositoryByName(ctx context.Context, name string) (*models.Repository, error)
	ListLicensePolicies(ctx context.Context, repoID string) ([]*models.LicensePolicy, error)
	ListPyPIPackageNames(ctx context.Context, repoID string) ([]string, error)
	ListPyPIPackageVersions(ctx context.Context, repoID string, name string) ([]*models.PyPIPackage, error)
	PyPIIndexStamp(ctx context.Context, repoID string) (string, error)
	PyPIProjectStamp(ctx context.Context, repoID string, name string) (string, int64, error)
	ReserveStorageUsage(ctx context.Context, repoID string, sizeBytes int64, artifactCount int) (bool, error)
	UpsertRawArtifact(ctx context.Context, artifact *models.RawArtifact) error
}

// existingReleaseSpelling returns the spelling of the live release of project that
// version names under PEP 440 canonicalization, or version itself when there is none.
func (h *HTTPHandler) existingReleaseSpelling(ctx context.Context, repoID, project, version string) (string, error) {
	existing, err := h.DB.ListPyPIPackageVersions(ctx, repoID, project)
	if err != nil {
		return "", fmt.Errorf("list pypi versions: %w", err)
	}

	for _, e := range existing {
		if pypisimple.SameVersion(e.Version, version) {
			return e.Version, nil
		}
	}

	return version, nil
}

type HTTPHandler struct {
	*base.Handler

	// maxUpload bounds one uploaded distribution file.
	maxUpload int64
	DB        HTTPStore
}

func NewHTTPHandler(log *logger.Logger, db HTTPStore, blobStore storage.BlobStore, baseURL string) *HTTPHandler {
	return &HTTPHandler{Handler: base.New("pypi", log, db, blobStore, baseURL), maxUpload: maxUploadBytes, DB: db}
}

func (h *HTTPHandler) RegisterRepoRoutes(r chi.Router) {
	// The three metadata routes serve a render-cached document that carries its
	// own gzip form (base.Rendered), so none is wrapped in base.Compress.
	r.Get("/simple/", h.handleSimpleIndex)
	r.Get("/simple/{package}/", h.handlePackageIndex)
	r.Get("/files/{filename}", h.handleDownload)
	r.Post("/legacy/", h.handleUpload)
	r.Delete("/files/{filename}", h.handleDelete)
	r.Get("/json/{package}", h.handleJSON)
}

func (h *HTTPHandler) RegisterRoutes(r chi.Router, prefix string) {
	r.Route(prefix, func(r chi.Router) {
		h.RegisterRepoRoutes(r)
	})
	h.Log.Infow("pypi handler registered", "prefix", prefix)
}

const (
	// maxUploadBytes bounds one uploaded distribution file.
	maxUploadBytes = 512 << 20
	// uploadFormOverhead leaves room for the form fields and multipart
	// boundaries on top of the file.
	uploadFormOverhead = 1 << 20
)

// pypiFileExtensions are the multi-part file endings stripped before a
// filename is split into distribution and version.
func pypiFileExtensions() []string {
	return []string{".tar.gz", ".tar.bz2", ".tar.xz", ".tar.Z", ".zip", ".egg", ".tar", ".exe", ".msi", ".rpm", ".dmg"}
}

// validateUploadIdentity checks that the form's name and version are present
// and that the file's name belongs to them, so a file can never be stored
// under (or later resolved as) another package.
func validateUploadIdentity(name, version, filename string) error {
	switch {
	case strings.Trim(name, "-_.") == "":
		return errPyPIMissingName
	case version == "":
		return errPyPIMissingVersion
	case !pathseg.Name(filename):
		return errPyPIBadFilename
	}

	dist, ok := splitPyPIFilename(filename, version)
	if !ok || pypisimple.NormalizeName(dist) != pypisimple.NormalizeName(name) {
		return errPyPIFilenameMismatch
	}

	return nil
}

var (
	errPyPIMissingName      = errors.New("missing package name")
	errPyPIMissingVersion   = errors.New("missing package version")
	errPyPIBadFilename      = errors.New("invalid filename")
	errPyPIFilenameMismatch = errors.New("filename does not match package name and version")
)

// splitPyPIFilename returns the distribution name of a filename that carries
// the given version. Wheels are {dist}-{version}-{tags}.whl with "-" in the
// version escaped as "_"; everything else is {dist}-{version}{ext}, read from
// the end of the stem because dist names may contain "-".
func splitPyPIFilename(filename, version string) (string, bool) {
	if stem, isWheel := strings.CutSuffix(filename, ".whl"); isWheel {
		parts := strings.Split(stem, "-")
		if len(parts) < 5 {
			return "", false
		}

		return parts[0], parts[1] == strings.ReplaceAll(version, "-", "_")
	}

	stem := filename

	for _, ext := range pypiFileExtensions() {
		if cut, found := strings.CutSuffix(filename, ext); found {
			stem = cut
			break
		}
	}

	if stem == filename {
		if i := strings.LastIndex(filename, "."); i > 0 {
			stem = filename[:i]
		}
	}

	dist, found := strings.CutSuffix(stem, "-"+version)

	return dist, found && dist != ""
}

func (h *HTTPHandler) handleDownload(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	repoName := vars["name"]
	filename := vars["filename"]

	repoID := h.ResolveRepoIDOrRespond(w, r, repoName)
	if repoID == "" {
		return
	}

	pkg, err := h.DB.GetPyPIPackageByFilename(r.Context(), repoID, filename)
	if errors.Is(err, database.ErrAmbiguousPyPIFilename) {
		http.Error(w, "filename is ambiguous", http.StatusConflict)
		return
	}

	if err != nil {
		h.Log.Errorw("failed to look up pypi file", "repo", repoName, "filename", filename, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if pkg == nil {
		http.NotFound(w, r)
		return
	}

	if h.DenyIfQuarantined(w, r, repoID, quarantine.PathKeys(pkg.ID, pkg.Filename, r.URL.Path)...) {
		return
	}

	if h.TryPresign(
		w,
		r,
		presign.Blob{SHA256: pkg.SHA256Digest, Size: pkg.SizeBytes, Filename: pkg.Filename, ContentType: "application/octet-stream"},
	) {
		return
	}

	// Get blob from storage
	rc, err := h.BlobStore.Get(r.Context(), pkg.SHA256Digest)
	if err != nil {
		h.Log.Errorw("failed to get blob", "sha256", pkg.SHA256Digest, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}
	defer func() { _ = rc.Close() }()

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(pkg.SizeBytes, 10))
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": pkg.Filename}))

	if _, err := iocopy.Copy(w, rc); err != nil {
		h.Log.Errorw("failed to write file response", "filename", filename, "error", err)
	}
}

// readUploadedForm reads the multipart upload: the "content" part spooled and
// hashed (its MD5 is returned), the other fields in the form. The caller closes
// the form. On failure it writes the response and returns false.
func (h *HTTPHandler) readUploadedForm(w http.ResponseWriter, r *http.Request) (*base.Form, string, bool) {
	// Cap the whole body: the file part is bounded by maxUpload, the fields by
	// the form overhead.
	r.Body = http.MaxBytesReader(w, r.Body, h.maxUpload+uploadFormOverhead)

	md5Sum := md5.New() //nolint:gosec // required by protocol specification

	form, err := base.ReadForm(r, base.FormSpec{
		FileField: "content",
		MaxFile:   h.maxUpload,
		MaxFields: uploadFormOverhead,
		Hashes:    []hash.Hash{md5Sum},
	})

	switch {
	case err == nil:
		return form, hex.EncodeToString(md5Sum.Sum(nil)), true
	case base.IsTooLarge(err):
		http.Error(w, "file too large", http.StatusRequestEntityTooLarge)
	case errors.Is(err, base.ErrNoFormFile):
		h.Log.Errorw("failed to get file from form", "error", err)
		http.Error(w, "missing file", http.StatusBadRequest)
	default:
		h.Log.Errorw("failed to read upload form", "error", err)
		http.Error(w, "invalid form", http.StatusBadRequest)
	}

	return nil, "", false
}

// filenameTaken reports, after writing the response, whether the repository
// already holds a file called filename. A filename resolves to exactly one
// row: refusing a file whose name another (name, version) already holds keeps
// its download route from becoming ambiguous.
func (h *HTTPHandler) filenameTaken(w http.ResponseWriter, r *http.Request, repoID, filename string) bool {
	existing, err := h.DB.GetPyPIPackageByFilename(r.Context(), repoID, filename)
	if err != nil && !errors.Is(err, database.ErrAmbiguousPyPIFilename) {
		h.Log.Errorw("failed to look up pypi file", "repo", repoID, "filename", filename, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return true
	}

	if existing != nil || err != nil {
		http.Error(w, "file already exists", http.StatusConflict)
		return true
	}

	return false
}

func (h *HTTPHandler) handleUpload(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	repoName := vars["name"]

	repoID := h.ResolveRepoIDOrRespond(w, r, repoName)
	if repoID == "" {
		return
	}

	form, md5Digest, ok := h.readUploadedForm(w, r)
	if !ok {
		return
	}
	defer form.Close()

	upload, filename := form.File, form.Filename

	sha256Digest := upload.SHA256Hex()

	// Get form fields
	name := strings.TrimSpace(form.Value("name"))
	version := strings.TrimSpace(form.Value("version"))
	filetype := strings.TrimSpace(form.Value("filetype"))
	pyversion := strings.TrimSpace(form.Value("pyversion"))
	requiresPython := strings.TrimSpace(form.Value("requires_python"))
	summary := strings.TrimSpace(form.Value("summary"))
	author := strings.TrimSpace(form.Value("author"))
	authorEmail := strings.TrimSpace(form.Value("author_email"))
	license := strings.TrimSpace(form.Value("license"))
	keywords := strings.TrimSpace(form.Value("keywords"))
	classifiersStr := form.Value("classifiers")

	// Parse classifiers
	var classifiers []string
	if classifiersStr != "" {
		classifiers = strings.Split(classifiersStr, "\n")
	}

	if err := validateUploadIdentity(name, version, filename); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	licenses, ok := h.uploadLicenses(w, r, repoID, form)
	if !ok || licensepolicy.Enforce(w, r, h.DB, h.Log, repoID, licenses...) {
		return
	}

	if h.filenameTaken(w, r, repoID, filename) {
		return
	}

	// Normalize package name
	normalizedName := pypisimple.NormalizeName(name)

	// Warehouse keeps one release per canonical version: a file uploaded as 1.0.0
	// joins the existing 1.0 release instead of opening a second one.
	version, err := h.existingReleaseSpelling(r.Context(), repoID, normalizedName, version)
	if err != nil {
		h.Log.Errorw("failed to list project versions", "error", err)
		http.Error(w, "failed to create package", http.StatusInternalServerError)

		return
	}

	blobKey := fmt.Sprintf("pypi/%s/%s", repoID, filename)

	pkg := &models.PyPIPackage{
		RepositoryID:   repoID,
		Name:           normalizedName,
		Version:        version,
		Filename:       filename,
		PackageType:    filetype,
		PythonVersion:  pyversion,
		RequiresPython: requiresPython,
		Summary:        summary,
		Author:         author,
		AuthorEmail:    authorEmail,
		License:        license,
		Keywords:       keywords,
		Classifiers:    classifiers,
		BlobKey:        blobKey,
		MD5Digest:      md5Digest,
		SHA256Digest:   sha256Digest,
		SizeBytes:      upload.Size(),
	}

	// Reserve quota before anything is persisted; every failure path below
	// returns the reservation.
	if !h.ReserveQuotaOrRespond(r.Context(), w, repoID, repoName, pkg.SizeBytes) {
		return
	}

	// The blob is stored first; its existence is re-checked and the
	// referencing row inserted under the per-blob lock (DB work only) so a
	// concurrent unpublish cannot delete the blob in between.
	var (
		status int
		msg    string
	)

	blob := storage.SeekerBlob(sha256Digest, upload.Reader(), map[string]string{
		"sha256": sha256Digest,
		"format": "pypi",
	})

	lockErr := storage.CommitBlob(r.Context(), h.DB, h.BlobStore, blob, func(ctx context.Context, _ string) error {
		pkg.CreatedAt = importmeta.CreatedAt(ctx)

		if err := h.DB.CreatePyPIPackage(ctx, pkg); err != nil {
			if errors.Is(err, database.ErrDuplicate) {
				status, msg = http.StatusConflict, "file already exists"

				return err
			}

			h.Log.Errorw("failed to create package record", "error", err)

			status, msg = http.StatusInternalServerError, "failed to create package"

			return err
		}

		return nil
	})
	if lockErr != nil {
		var storeErr *storage.StoreError

		switch {
		case status != 0:
		case errors.As(lockErr, &storeErr):
			h.Log.Errorw("failed to store blob", "error", lockErr)

			status, msg = http.StatusInternalServerError, "failed to store file"
		default:
			h.Log.Errorw("blob lock failed", "error", lockErr)

			status, msg = http.StatusInternalServerError, "failed to create package"
		}

		h.ReleaseQuota(r.Context(), repoID, repoName, pkg.SizeBytes)

		http.Error(w, msg, status)

		return
	}

	h.Emit(r.Context(), repoID, webhook.EventArtifactUploaded, webhook.ArtifactPayload(webhook.EventArtifactUploaded, webhook.Artifact{
		Repository: repoName,
		Format:     "pypi",
		Name:       normalizedName,
		Version:    version,
		Path:       filename,
		SHA256:     sha256Digest,
		Size:       upload.Size(),
	}))

	// Return 200 OK (twine expects 200, not 201)
	w.WriteHeader(http.StatusOK)
}

func (h *HTTPHandler) handleDelete(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	repoName := vars["name"]
	filename := vars["filename"]

	repoID := h.ResolveRepoIDOrRespond(w, r, repoName)
	if repoID == "" {
		return
	}

	pkg, err := h.DB.GetPyPIPackageByFilename(r.Context(), repoID, filename)
	if errors.Is(err, database.ErrAmbiguousPyPIFilename) {
		http.Error(w, "filename is ambiguous", http.StatusConflict)
		return
	}

	if err != nil {
		h.Log.Errorw("failed to look up pypi file", "repo", repoName, "filename", filename, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if pkg == nil {
		http.NotFound(w, r)
		return
	}

	if err := trash.GuardReleaseByName(r, h.DB, repoName, "pypi", pkg.Version); err != nil {
		if errors.Is(err, trash.ErrImmutableRelease) {
			http.Error(w, "release versions of this repository are immutable", http.StatusConflict)
			return
		}

		h.Log.Errorw("failed to check release immutability", "repo", repoName, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	err = trash.Delete(
		r.Context(),
		h.DB,
		trash.Request{
			RepoID:   repoID,
			Kind:     models.TrashKindPyPI,
			IDs:      []string{pkg.ID},
			Resource: pkg.Filename,
			Package:  pkg.Name,
			Version:  pkg.Version,
		},
		func() error {
			if err := h.DB.DeletePyPIPackage(r.Context(), repoID, pkg.Name, pkg.Version, pkg.Filename); err != nil {
				return fmt.Errorf("delete pypi package: %w", err)
			}

			h.ReleaseQuota(r.Context(), repoID, repoName, pkg.SizeBytes)

			// Blob is content-addressed and may be shared by other artifacts; only
			// remove it from storage once no other row references the same sha256.
			if delErr := storage.SafeDeleteBlob(r.Context(), h.DB, h.BlobStore, pkg.SHA256Digest); delErr != nil {
				h.Log.Warnw("failed to clean up blob after package delete", "sha256", pkg.SHA256Digest, "error", delErr)
			}

			return nil
		},
	)
	if err != nil {
		if errors.Is(err, database.ErrTrashNotFound) {
			http.NotFound(w, r)
			return
		}

		h.Log.Errorw("failed to delete package", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	h.Emit(r.Context(), repoID, webhook.EventArtifactDeleted, webhook.ArtifactPayload(webhook.EventArtifactDeleted, webhook.Artifact{
		Repository: repoName,
		Format:     "pypi",
		Name:       pkg.Name,
		Version:    pkg.Version,
		Path:       pkg.Filename,
		SHA256:     pkg.SHA256Digest,
		Size:       pkg.SizeBytes,
	}))

	w.WriteHeader(http.StatusOK)
}

// uploadLicenses returns the licenses a policy is enforced on: the ones the
// uploaded file declares in its own METADATA / PKG-INFO. The form fields are
// only the fallback for a file that declares none, so a curl upload without
// form metadata cannot bypass a policy, nor can a form claim override the file.
// A file whose metadata cannot be read safely is refused, and so is a file that
// cannot be read at all (not a wheel or sdist, a damaged archive) when the
// repository has a blocking policy: only the client's claims would decide it
// (false, response written).
func (h *HTTPHandler) uploadLicenses(w http.ResponseWriter, r *http.Request, repoID string, form *base.Form) ([]string, bool) {
	filename := form.Filename

	res, err := licensemeta.Read(r.Context(), "pypi", filename, func() (io.ReadCloser, error) {
		return form.File.Reader(), nil
	})

	switch {
	case licensemeta.Unsafe(err):
		h.Log.Warnw("pypi upload rejected: package metadata cannot be read safely", "filename", filename, "error", err)
		licensepolicy.RejectUnsafe(w, h.Log)

		return nil, false
	case err != nil:
		h.Log.Debugw("pypi upload: package metadata unreadable", "filename", filename, "error", err)

		if licensepolicy.RefuseUnderivable(w, r, h.DB, h.Log, repoID) {
			return nil, false
		}
	case !res.Empty():
		return res.Licenses, true
	}

	return declaredLicenses(form), true
}

// maxLicenseTextLen bounds a free-text "license" field that is treated as a
// license identifier; longer values are usually a whole license text and
// cannot be matched against a policy.
const maxLicenseTextLen = 100

// declaredLicenses returns the licenses an upload form declares: the PEP 639
// license_expression if present, else a short single-line license field, plus
// every "License ::" trove classifier.
func declaredLicenses(form *base.Form) []string {
	var out []string

	if expr := strings.TrimSpace(form.Value("license_expression")); expr != "" {
		out = append(out, expr)
	} else if lic := strings.TrimSpace(form.Value("license")); lic != "" &&
		len(lic) <= maxLicenseTextLen && !strings.ContainsAny(lic, "\r\n") {
		out = append(out, lic)
	}

	// twine sends one "classifiers" value per classifier; tolerate
	// newline-joined values as well.
	for _, v := range form.Values("classifiers") {
		for c := range strings.SplitSeq(v, "\n") {
			if c = strings.TrimSpace(c); strings.HasPrefix(c, "License ::") {
				out = append(out, c)
			}
		}
	}

	return out
}
