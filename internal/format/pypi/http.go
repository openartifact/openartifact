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
	"fmt"
	htmlesc "html"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	"github.com/openartifact/openartifact/internal/licensepolicy"
	"github.com/openartifact/openartifact/internal/presign"
	"github.com/openartifact/openartifact/internal/quarantine"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/internal/webhook"
	"github.com/openartifact/openartifact/pkg/logger"
)

type HTTPHandler struct {
	*base.Handler
}

func NewHTTPHandler(log *logger.Logger, db database.Database, blobStore storage.BlobStore, baseURL string) *HTTPHandler {
	return &HTTPHandler{Handler: base.New(log, db, blobStore, baseURL)}
}

func (h *HTTPHandler) RegisterRepoRoutes(r chi.Router) {
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

func normalizePyPIName(name string) string {
	// Normalize: lowercase, replace _ and . with -
	name = strings.ToLower(name)
	name = strings.ReplaceAll(name, "_", "-")
	name = strings.ReplaceAll(name, ".", "-")

	return name
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

	packages, err := h.DB.ListPyPIPackages(r.Context(), repo.ID)
	if err != nil {
		h.Log.Errorw("failed to list packages", "repo", repoName, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	// Build unique package names
	seen := make(map[string]bool)

	var uniqueNames []string

	for _, pkg := range packages {
		normalized := normalizePyPIName(pkg.Name)
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

	packages, err := h.DB.ListPyPIPackages(r.Context(), repoID)
	if err != nil {
		h.Log.Errorw("failed to list packages", "repo", repoName, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	var pkg *models.PyPIPackage

	for _, p := range packages {
		if p.Filename == filename {
			pkg = p
			break
		}
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
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, pkg.Filename))

	if _, err := io.Copy(w, rc); err != nil {
		h.Log.Errorw("failed to write file response", "filename", filename, "error", err)
	}
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

	// Parse multipart form
	//nolint:gosec // G120: form size is bounded by the 100MB limit passed to ParseMultipartForm
	if err := r.ParseMultipartForm(100 * 1024 * 1024); err != nil {
		h.Log.Errorw("failed to parse form", "error", err)
		http.Error(w, "invalid form", http.StatusBadRequest)

		return
	}

	// Get file from form
	file, fileHeader, err := r.FormFile("content")
	if err != nil {
		h.Log.Errorw("failed to get file from form", "error", err)
		http.Error(w, "missing file", http.StatusBadRequest)

		return
	}
	defer func() { _ = file.Close() }()

	// Read file content
	fileData, err := io.ReadAll(file)
	if err != nil {
		h.Log.Errorw("failed to read file", "error", err)
		http.Error(w, "failed to read file", http.StatusBadRequest)

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

	if licensepolicy.Enforce(w, r, h.DB, h.Log, repoID, declaredLicenses(r)...) {
		return
	}

	// Normalize package name
	normalizedName := normalizePyPIName(name)

	blobKey := fmt.Sprintf("pypi/%s/%s", repoID, fileHeader.Filename)

	pkg := &models.PyPIPackage{
		RepositoryID:   repoID,
		Name:           normalizedName,
		Version:        version,
		Filename:       fileHeader.Filename,
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

		http.Error(w, msg, status)

		return
	}

	h.Emit(r.Context(), repoID, webhook.EventArtifactUploaded, webhook.ArtifactPayload(webhook.EventArtifactUploaded, webhook.Artifact{
		Repository: repoName,
		Format:     "pypi",
		Name:       normalizedName,
		Version:    version,
		Path:       fileHeader.Filename,
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

	// Find package by filename
	packages, err := h.DB.ListPyPIPackages(r.Context(), repoID)
	if err != nil {
		h.Log.Errorw("failed to list packages", "repo", repoName, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	var pkg *models.PyPIPackage

	for _, p := range packages {
		if p.Filename == filename {
			pkg = p
			break
		}
	}

	if pkg == nil {
		http.NotFound(w, r)
		return
	}

	// Delete from database
	if err := h.DB.DeletePyPIPackage(r.Context(), repoID, pkg.Name, pkg.Version, pkg.Filename); err != nil {
		h.Log.Errorw("failed to delete package", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	// Blob is content-addressed and may be shared by other artifacts; only
	// remove it from storage once no other row references the same sha256.
	if delErr := storage.SafeDeleteBlob(r.Context(), h.DB, h.BlobStore, pkg.SHA256Digest); delErr != nil {
		h.Log.Warnw("failed to clean up blob after package delete", "sha256", pkg.SHA256Digest, "error", delErr)
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

	// Use first version as info
	firstPkg := versions[0]

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
