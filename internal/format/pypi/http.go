// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package pypi

import (
	"bytes"
	"context"
	"crypto/md5" //nolint:gosec // required by protocol specification
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	htmlesc "html"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	"github.com/openartifact/openartifact/internal/importmeta"
	"github.com/openartifact/openartifact/internal/licensemeta"
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

type HTTPHandler struct {
	*base.Handler

	// maxUpload bounds one uploaded distribution file.
	maxUpload int64
}

func NewHTTPHandler(log *logger.Logger, db database.Database, blobStore storage.BlobStore, baseURL string) *HTTPHandler {
	return &HTTPHandler{Handler: base.New(log, db, blobStore, baseURL), maxUpload: maxUploadBytes}
}

func (h *HTTPHandler) RegisterRepoRoutes(r chi.Router) {
	r.Get("/simple/", base.Compress(h.handleSimpleIndex))
	r.Get("/simple/{package}/", base.Compress(h.handlePackageIndex))
	r.Get("/files/{filename}", h.handleDownload)
	r.Post("/legacy/", h.handleUpload)
	r.Delete("/files/{filename}", h.handleDelete)
	r.Get("/json/{package}", base.Compress(h.handleJSON))
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
	// uploadFormMemory is the in-memory threshold of the multipart parser;
	// larger parts spill to disk.
	uploadFormMemory = 32 << 20
)

var pypiNameSeparators = regexp.MustCompile(`[-_.]+`)

// normalizePyPIName is the PEP 503 normalization: runs of "-", "_" and "." become
// a single "-", lowercased.
func normalizePyPIName(name string) string {
	return strings.ToLower(pypiNameSeparators.ReplaceAllString(name, "-"))
}

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
	case filename == "" || strings.ContainsAny(filename, `/\`) || filename == "." || filename == "..":
		return errPyPIBadFilename
	}

	dist, ok := splitPyPIFilename(filename, version)
	if !ok || normalizePyPIName(dist) != normalizePyPIName(name) {
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

// simpleIndexMaxAge is how long shared caches may reuse a public repository's
// simple index pages before revalidating.
const simpleIndexMaxAge = 60

// serveSimplePage writes a simple-index HTML page. The page is a pure function
// of the repository's rows, so a hash of the body is its validator: a matching
// If-None-Match saves the transfer.
func serveSimplePage(w http.ResponseWriter, r *http.Request, repoPublic bool, page string) {
	sum := sha256.Sum256([]byte(page))

	if base.Revalidate(w, r, repoPublic, `"`+hex.EncodeToString(sum[:16])+`"`, simpleIndexMaxAge) {
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, page)
}

func (h *HTTPHandler) handleSimpleIndex(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	repoName := vars["name"]

	repo, err := h.ResolveRepo(r.Context(), repoName)
	if err != nil {
		h.Log.Errorw("failed to resolve repo", "name", repoName, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if repo == nil {
		http.NotFound(w, r)
		return
	}

	storedNames, err := h.DB.ListPyPIPackageNames(r.Context(), repo.ID)
	if err != nil {
		h.Log.Errorw("failed to list packages", "repo", repoName, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	// Stored names are distinct but not necessarily normalized (legacy rows),
	// so two of them can collapse to one project.
	seen := make(map[string]bool, len(storedNames))
	uniqueNames := make([]string, 0, len(storedNames))

	for _, name := range storedNames {
		normalized := normalizePyPIName(name)
		if !seen[normalized] {
			seen[normalized] = true
			uniqueNames = append(uniqueNames, normalized)
		}
	}

	// Generate HTML
	var html strings.Builder
	html.WriteString("<!DOCTYPE html>\n<html><body>\n")

	for _, name := range uniqueNames {
		fmt.Fprintf(&html, `<a href="/repos/%s/simple/%s/">%s</a>`+"\n",
			htmlesc.EscapeString(repoName), url.PathEscape(name), htmlesc.EscapeString(name))
	}

	html.WriteString("</body></html>\n")

	serveSimplePage(w, r, repo.Public, html.String())
}

func (h *HTTPHandler) handlePackageIndex(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	repoName := vars["name"]
	pkgName := vars["package"]

	repo, err := h.ResolveRepo(r.Context(), repoName)
	if err != nil {
		h.Log.Errorw("failed to resolve repo", "name", repoName, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if repo == nil {
		http.NotFound(w, r)
		return
	}

	// Normalize the package name for lookup
	normalizedName := normalizePyPIName(pkgName)

	versions, err := h.DB.ListPyPIPackageVersions(r.Context(), repo.ID, normalizedName)
	if err != nil {
		h.Log.Errorw("failed to list package versions", "pkg", pkgName, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if len(versions) == 0 {
		http.NotFound(w, r)
		return
	}

	// Generate HTML with links to files
	var html strings.Builder
	html.WriteString("<!DOCTYPE html>\n<html><body>\n")

	for _, pkg := range versions {
		fileURL := fmt.Sprintf("/repos/%s/files/%s", htmlesc.EscapeString(repoName), url.PathEscape(pkg.Filename))
		fmt.Fprintf(&html, `<a href="%s#sha256=%s"`, fileURL, htmlesc.EscapeString(pkg.SHA256Digest))

		if pkg.RequiresPython != "" {
			fmt.Fprintf(&html, ` data-requires-python="%s"`, htmlesc.EscapeString(pkg.RequiresPython))
		}

		fmt.Fprintf(&html, ">%s</a>\n", htmlesc.EscapeString(pkg.Filename))
	}

	html.WriteString("</body></html>\n")

	serveSimplePage(w, r, repo.Public, html.String())
}

func (h *HTTPHandler) handleDownload(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	repoName := vars["name"]
	filename := vars["filename"]

	repoID, err := h.ResolveRepoID(r.Context(), repoName)
	if err != nil {
		h.Log.Errorw("failed to resolve repo", "name", repoName, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if repoID == "" {
		http.NotFound(w, r)
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

	if h.DenyIfQuarantined(w, r, repoID, quarantine.PathKeys(pkg.Filename, r.URL.Path)...) {
		return
	}

	if h.TryPresign(w, r, presign.Blob{SHA256: pkg.SHA256Digest, Size: pkg.SizeBytes, Filename: pkg.Filename, ContentType: "application/octet-stream"}) {
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

	if _, err := io.Copy(w, rc); err != nil {
		h.Log.Errorw("failed to write file response", "filename", filename, "error", err)
	}
}

// readUploadedFile parses the multipart upload and returns the "content" part
// with its file name. On failure it writes the response and returns false.
func (h *HTTPHandler) readUploadedFile(w http.ResponseWriter, r *http.Request) ([]byte, string, bool) {
	// Cap the whole body: ParseMultipartForm only bounds the in-memory part and
	// spools the rest to disk.
	r.Body = http.MaxBytesReader(w, r.Body, h.maxUpload+uploadFormOverhead)

	if err := r.ParseMultipartForm(uploadFormMemory); err != nil { //nolint:gosec // r.Body is bounded by http.MaxBytesReader above
		if _, tooBig := errors.AsType[*http.MaxBytesError](err); tooBig {
			http.Error(w, "file too large", http.StatusRequestEntityTooLarge)
			return nil, "", false
		}

		h.Log.Errorw("failed to parse form", "error", err)
		http.Error(w, "invalid form", http.StatusBadRequest)

		return nil, "", false
	}

	file, fileHeader, err := r.FormFile("content")
	if err != nil {
		h.Log.Errorw("failed to get file from form", "error", err)
		http.Error(w, "missing file", http.StatusBadRequest)

		return nil, "", false
	}
	defer func() { _ = file.Close() }()

	data, err := base.ReadAllLimited(file, h.maxUpload)
	if errors.Is(err, base.ErrContentTooLarge) {
		http.Error(w, "file too large", http.StatusRequestEntityTooLarge)
		return nil, "", false
	}

	if err != nil {
		h.Log.Errorw("failed to read file", "error", err)
		http.Error(w, "failed to read file", http.StatusBadRequest)

		return nil, "", false
	}

	return data, fileHeader.Filename, true
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

	repoID, err := h.ResolveRepoID(r.Context(), repoName)
	if err != nil {
		h.Log.Errorw("failed to resolve repo", "name", repoName, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if repoID == "" {
		http.NotFound(w, r)
		return
	}

	fileData, filename, ok := h.readUploadedFile(w, r)
	if !ok {
		return
	}

	// Calculate hashes
	md5Hash := md5.Sum(fileData) //nolint:gosec // required by protocol specification
	sha256Hash := sha256.Sum256(fileData)
	md5Digest := hex.EncodeToString(md5Hash[:])
	sha256Digest := hex.EncodeToString(sha256Hash[:])

	// Get form fields
	name := strings.TrimSpace(r.FormValue("name"))
	version := strings.TrimSpace(r.FormValue("version"))
	filetype := strings.TrimSpace(r.FormValue("filetype"))
	pyversion := strings.TrimSpace(r.FormValue("pyversion"))
	requiresPython := strings.TrimSpace(r.FormValue("requires_python"))
	summary := strings.TrimSpace(r.FormValue("summary"))
	author := strings.TrimSpace(r.FormValue("author"))
	authorEmail := strings.TrimSpace(r.FormValue("author_email"))
	license := strings.TrimSpace(r.FormValue("license"))
	keywords := strings.TrimSpace(r.FormValue("keywords"))
	classifiersStr := r.FormValue("classifiers")

	// Parse classifiers
	var classifiers []string
	if classifiersStr != "" {
		classifiers = strings.Split(classifiersStr, "\n")
	}

	if err := validateUploadIdentity(name, version, filename); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	licenses, ok := h.uploadLicenses(w, r, repoID, filename, fileData)
	if !ok || licensepolicy.Enforce(w, r, h.DB, h.Log, repoID, licenses...) {
		return
	}

	if h.filenameTaken(w, r, repoID, filename) {
		return
	}

	// Normalize package name
	normalizedName := normalizePyPIName(name)

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
		SizeBytes:      int64(len(fileData)),
	}

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

	// Store the blob and insert the referencing row under the per-blob lock
	// so a concurrent unpublish cannot delete the blob in between.
	var (
		status int
		msg    string
	)

	lockErr := storage.WithBlobLock(r.Context(), h.DB, sha256Digest, func(ctx context.Context) error {
		if _, err := h.BlobStore.Put(ctx, bytes.NewReader(fileData), map[string]string{
			"sha256": sha256Digest,
			"format": "pypi",
		}); err != nil {
			h.Log.Errorw("failed to store blob", "error", err)

			status, msg = http.StatusInternalServerError, "failed to store file"

			return err
		}

		pkg.CreatedAt = importmeta.CreatedAt(ctx)

		if err := h.DB.CreatePyPIPackage(ctx, pkg); err != nil {
			if strings.Contains(err.Error(), "23505") || strings.Contains(err.Error(), "duplicate key") {
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
		if status == 0 {
			h.Log.Errorw("blob lock failed", "error", lockErr)

			status, msg = http.StatusInternalServerError, "failed to create package"
		}

		if err := quota.Release(r.Context(), h.DB, repoID, pkg.SizeBytes); err != nil {
			h.Log.Warnw("failed to release quota", "repo", repoName, "error", err)
		}

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
		Size:       int64(len(fileData)),
	}))

	// Return 200 OK (twine expects 200, not 201)
	w.WriteHeader(http.StatusOK)
}

func (h *HTTPHandler) handleDelete(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	repoName := vars["name"]
	filename := vars["filename"]

	repoID, err := h.ResolveRepoID(r.Context(), repoName)
	if err != nil {
		h.Log.Errorw("failed to resolve repo", "name", repoName, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if repoID == "" {
		http.NotFound(w, r)
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

	err = trash.Delete(r.Context(), h.DB, trash.Request{RepoID: repoID, Kind: models.TrashKindPyPI, IDs: []string{pkg.ID}, Resource: pkg.Filename}, func() error {
		if err := h.DB.DeletePyPIPackage(r.Context(), repoID, pkg.Name, pkg.Version, pkg.Filename); err != nil {
			return fmt.Errorf("delete pypi package: %w", err)
		}

		if relErr := quota.Release(r.Context(), h.DB, repoID, pkg.SizeBytes); relErr != nil {
			h.Log.Warnw("failed to release quota", "repo", repoName, "error", relErr)
		}

		// Blob is content-addressed and may be shared by other artifacts; only
		// remove it from storage once no other row references the same sha256.
		if delErr := storage.SafeDeleteBlob(r.Context(), h.DB, h.BlobStore, pkg.SHA256Digest); delErr != nil {
			h.Log.Warnw("failed to clean up blob after package delete", "sha256", pkg.SHA256Digest, "error", delErr)
		}

		return nil
	})
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

func (h *HTTPHandler) handleJSON(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	repoName := vars["name"]
	pkgName := vars["package"]

	repoID, err := h.ResolveRepoID(r.Context(), repoName)
	if err != nil {
		h.Log.Errorw("failed to resolve repo", "name", repoName, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if repoID == "" {
		http.NotFound(w, r)
		return
	}

	// Normalize the package name for lookup
	normalizedName := normalizePyPIName(pkgName)

	versions, err := h.DB.ListPyPIPackageVersions(r.Context(), repoID, normalizedName)
	if err != nil {
		h.Log.Errorw("failed to list package versions", "pkg", pkgName, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if len(versions) == 0 {
		http.NotFound(w, r)
		return
	}

	// Build JSON response
	releases := make(map[string][]map[string]any)

	for _, pkg := range versions {
		fileURL := fmt.Sprintf("%s/repos/%s/files/%s", h.BaseURL, repoName, url.PathEscape(pkg.Filename))

		fileInfo := map[string]any{
			"filename": pkg.Filename,
			"url":      fileURL,
			"digests": map[string]string{
				"sha256": pkg.SHA256Digest,
			},
		}
		if pkg.MD5Digest != "" {
			if digests, ok := fileInfo["digests"].(map[string]string); ok {
				digests["md5"] = pkg.MD5Digest
			}
		}

		releases[pkg.Version] = append(releases[pkg.Version], fileInfo)
	}

	// info describes the highest version by PEP 440 order; the query orders
	// versions as strings, where 9.0.0 sorts above 10.0.0.
	firstPkg := versions[0]

	for _, pkg := range versions[1:] {
		if comparePEP440(pkg.Version, firstPkg.Version) > 0 {
			firstPkg = pkg
		}
	}

	info := map[string]any{
		"name":    firstPkg.Name,
		"version": firstPkg.Version,
	}
	if firstPkg.Summary != "" {
		info["summary"] = firstPkg.Summary
	}

	if firstPkg.Author != "" {
		info["author"] = firstPkg.Author
	}

	if firstPkg.AuthorEmail != "" {
		info["author_email"] = firstPkg.AuthorEmail
	}

	if firstPkg.License != "" {
		info["license"] = firstPkg.License
	}

	response := map[string]any{
		"info":     info,
		"releases": releases,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	if encErr := json.NewEncoder(w).Encode(response); encErr != nil {
		_ = encErr // response header already written
	}
}

// uploadLicenses returns the licenses a policy is enforced on: the ones the
// uploaded file declares in its own METADATA / PKG-INFO. The form fields are
// only the fallback for a file that declares none, so a curl upload without
// form metadata cannot bypass a policy, nor can a form claim override the file.
// A file whose metadata cannot be read safely is refused, and so is a file that
// cannot be read at all (not a wheel or sdist, a damaged archive) when the
// repository has a blocking policy: only the client's claims would decide it
// (false, response written).
func (h *HTTPHandler) uploadLicenses(w http.ResponseWriter, r *http.Request, repoID, filename string, data []byte) ([]string, bool) {
	res, err := licensemeta.ReadBytes(r.Context(), "pypi", filename, data)

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

	return declaredLicenses(r), true
}

// maxLicenseTextLen bounds a free-text "license" field that is treated as a
// license identifier; longer values are usually a whole license text and
// cannot be matched against a policy.
const maxLicenseTextLen = 100

// declaredLicenses returns the licenses an upload form declares: the PEP 639
// license_expression if present, else a short single-line license field, plus
// every "License ::" trove classifier.
func declaredLicenses(r *http.Request) []string {
	var out []string

	if expr := strings.TrimSpace(r.FormValue("license_expression")); expr != "" {
		out = append(out, expr)
	} else if lic := strings.TrimSpace(r.FormValue("license")); lic != "" &&
		len(lic) <= maxLicenseTextLen && !strings.ContainsAny(lic, "\r\n") {
		out = append(out, lic)
	}

	// twine sends one "classifiers" value per classifier; tolerate
	// newline-joined values as well.
	for _, v := range r.Form["classifiers"] {
		for c := range strings.SplitSeq(v, "\n") {
			if c = strings.TrimSpace(c); strings.HasPrefix(c, "License ::") {
				out = append(out, c)
			}
		}
	}

	return out
}
