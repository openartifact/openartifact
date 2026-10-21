// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package nuget

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
	r.Get("/v3/index.json", h.handleServiceIndex)
	r.Put("/v3/package", h.handlePushPackage)
	r.Delete("/v3/package/{id}/{version}", h.handleDeletePackage)
	r.Get("/v3/package/{id}/{version}/{filename}", h.handleDownloadPackage)
	r.Get("/v3/registration/{id}/index.json", h.handleRegistrationIndex)
	r.Get("/v3/query", h.handleSearch)
	r.Get("/v3/autocomplete", h.handleAutocomplete)
}

func (h *HTTPHandler) RegisterRoutes(r chi.Router, prefix string) {
	r.Route(prefix, func(r chi.Router) {
		h.RegisterRepoRoutes(r)
	})
	h.Log.Infow("nuget handler registered", "prefix", prefix)
}

func (h *HTTPHandler) handleServiceIndex(w http.ResponseWriter, r *http.Request) {
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

	baseURL := fmt.Sprintf("%s/repos/%s", h.BaseURL, repoName)

	index := map[string]any{
		"version": "3.0.0",
		"resources": []map[string]string{
			{
				"@id":   baseURL + "/v3/registration/",
				"@type": "RegistrationsBaseUrl",
			},
			{
				"@id":   baseURL + "/v3/package/",
				"@type": "PackageBaseAddress/3.0.0",
			},
			{
				"@id":   baseURL + "/v3/query",
				"@type": "SearchQueryService",
			},
			{
				"@id":   baseURL + "/v3/autocomplete",
				"@type": "SearchAutocompleteService",
			},
			{
				"@id":   baseURL + "/v3/package",
				"@type": "PackagePublish/2.0.0",
			},
		},
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	if encErr := json.NewEncoder(w).Encode(index); encErr != nil {
		_ = encErr // response header already written
	}
}

func (h *HTTPHandler) handlePushPackage(w http.ResponseWriter, r *http.Request) {
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
	if err := r.ParseMultipartForm(100 * 1024 * 1024); err != nil { //nolint:gosec // G120: form size bounded by ParseMultipartForm limit
		h.Log.Errorw("failed to parse multipart form", "error", err)
		http.Error(w, "invalid request", http.StatusBadRequest)

		return
	}

	file, _, err := r.FormFile("package")
	if err != nil {
		h.Log.Errorw("failed to get package file", "error", err)
		http.Error(w, "package file required", http.StatusBadRequest)

		return
	}
	defer func() { _ = file.Close() }()

	// Read and hash the package
	data, err := io.ReadAll(file)
	if err != nil {
		h.Log.Errorw("failed to read package data", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	// Parse metadata from .nupkg
	handler := NewNuGetHandler()

	metadata, err := handler.Parse(r.Context(), io.NopCloser(strings.NewReader(string(data))))
	if err != nil {
		h.Log.Errorw("failed to parse package metadata", "error", err)
		http.Error(w, "invalid package", http.StatusBadRequest)

		return
	}

	packageIDVal, ok := metadata["id"].(string)
	if !ok {
		h.Log.Errorw("failed to extract package id from metadata", "error", "id not found or not a string")
		http.Error(w, "invalid package", http.StatusBadRequest)

		return
	}

	packageID := strings.ToLower(packageIDVal)

	version, ok := metadata["version"].(string)
	if !ok {
		h.Log.Errorw("failed to extract version from metadata", "error", "version not found or not a string")
		http.Error(w, "invalid package", http.StatusBadRequest)

		return
	}

	// Only <license type="expression"> is enforceable; licenseUrl alone is not.
	licenseExpr, _ := metadata["licenseExpression"].(string)
	if licensepolicy.Enforce(w, r, h.DB, h.Log, repoID, licenseExpr) {
		return
	}

	// Compute SHA512 hash
	hash := sha512.Sum512(data)
	hashB64 := base64.StdEncoding.EncodeToString(hash[:])

	sum256 := sha256.Sum256(data)
	blobSHA := hex.EncodeToString(sum256[:])

	// Extract metadata fields with type checking
	title, _ := metadata["title"].(string)
	description, _ := metadata["description"].(string)
	summary, _ := metadata["summary"].(string)
	authors, _ := metadata["authors"].(string)
	owners, _ := metadata["owners"].(string)
	tags, _ := metadata["tags"].(string)
	projectURL, _ := metadata["projectUrl"].(string)
	licenseURL, _ := metadata["licenseUrl"].(string)
	iconURL, _ := metadata["iconUrl"].(string)
	requireLicenseAcceptance, _ := metadata["requireLicenseAcceptance"].(bool)
	minClientVersion, _ := metadata["minClientVersion"].(string)

	// Create database record
	pkg := &models.NuGetPackage{
		RepositoryID:             repoID,
		PackageID:                packageID,
		Version:                  version,
		Title:                    title,
		Description:              description,
		Summary:                  summary,
		Authors:                  authors,
		Owners:                   owners,
		Tags:                     tags,
		ProjectURL:               projectURL,
		LicenseURL:               licenseURL,
		IconURL:                  iconURL,
		RequireLicenseAcceptance: requireLicenseAcceptance,
		MinClientVersion:         minClientVersion,
		BlobKey:                  blobSHA,
		PackageHash:              hashB64,
		PackageHashAlgorithm:     "SHA512",
		PackageSize:              int64(len(data)),
	}

	// Store the blob and insert the referencing row under the per-blob lock so
	// a concurrent unpublish cannot delete the blob in between.
	if err := storage.WithBlobLock(r.Context(), h.DB, blobSHA, func(ctx context.Context) error {
		if _, err := h.BlobStore.Put(ctx, bytes.NewReader(data), map[string]string{
			"sha256":   blobSHA,
			"filename": fmt.Sprintf("%s.%s.nupkg", packageID, version),
			"package":  packageID,
			"version":  version,
		}); err != nil {
			return fmt.Errorf("store blob: %w", err)
		}

		if err := h.DB.CreateNuGetPackage(ctx, pkg); err != nil {
			return fmt.Errorf("create nuget package: %w", err)
		}

		return nil
	}); err != nil {
		h.Log.Errorw("failed to store nuget package", "id", packageID, "version", version, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	h.Emit(r.Context(), repoID, webhook.EventArtifactUploaded, webhook.ArtifactPayload(webhook.EventArtifactUploaded, webhook.Artifact{
		Repository: repoName,
		Format:     "nuget",
		Name:       packageID,
		Version:    version,
		Path:       fmt.Sprintf("%s.%s.nupkg", packageID, version),
		SHA256:     blobSHA,
		Size:       int64(len(data)),
	}))

	w.WriteHeader(http.StatusCreated)
}

func (h *HTTPHandler) handleDeletePackage(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	repoName := vars["name"]
	packageID := strings.ToLower(vars["id"])
	version := vars["version"]

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

	// Get package to find blob key
	pkg, err := h.DB.GetNuGetPackage(r.Context(), repoID, packageID, version)
	if err != nil {
		h.Log.Errorw("failed to get nuget package", "id", packageID, "version", version, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if pkg == nil {
		http.NotFound(w, r)
		return
	}

	// Delete from database
	if err := h.DB.DeleteNuGetPackage(r.Context(), repoID, packageID, version); err != nil {
		h.Log.Errorw("failed to delete nuget package", "id", packageID, "version", version, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	// The blob is content-addressed and may be shared; remove it only once no
	// row references it any more.
	if err := storage.SafeDeleteBlob(r.Context(), h.DB, h.BlobStore, pkg.BlobKey); err != nil {
		h.Log.Warnw("failed to clean up blob after nuget delete", "key", pkg.BlobKey, "error", err)
	}

	h.Emit(r.Context(), repoID, webhook.EventArtifactDeleted, webhook.ArtifactPayload(webhook.EventArtifactDeleted, webhook.Artifact{
		Repository: repoName,
		Format:     "nuget",
		Name:       packageID,
		Version:    version,
		Path:       fmt.Sprintf("%s.%s.nupkg", packageID, version),
		SHA256:     pkg.BlobKey,
		Size:       pkg.PackageSize,
	}))

	w.WriteHeader(http.StatusNoContent)
}

func (h *HTTPHandler) handleDownloadPackage(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	repoName := vars["name"]
	packageID := strings.ToLower(vars["id"])
	version := vars["version"]

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

	pkg, err := h.DB.GetNuGetPackage(r.Context(), repoID, packageID, version)
	if err != nil {
		h.Log.Errorw("failed to get nuget package", "id", packageID, "version", version, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if pkg == nil {
		http.NotFound(w, r)
		return
	}

	if h.DenyIfQuarantined(w, r, repoID, quarantine.PathKeys(
		fmt.Sprintf("%s.%s.nupkg", packageID, version), vars["filename"], r.URL.Path)...) {
		return
	}

	// Count the download before the blob is opened or redirected to: a
	// presigned response never reaches the streaming code below.
	if err := h.DB.IncrementNuGetDownloadCount(r.Context(), repoID, packageID, version); err != nil {
		h.Log.Errorw("failed to increment download count", "id", packageID, "version", version, "error", err)
		// Continue anyway
	}

	if h.TryPresign(w, r, presign.Blob{SHA256: pkg.BlobKey, Size: pkg.PackageSize, Filename: fmt.Sprintf("%s.%s.nupkg", packageID, version), ContentType: "application/zip"}) {
		return
	}

	// Get blob
	rc, err := h.BlobStore.Get(r.Context(), pkg.BlobKey)
	if err != nil {
		h.Log.Errorw("failed to get blob", "key", pkg.BlobKey, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}
	defer func() { _ = rc.Close() }()

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Length", strconv.FormatInt(pkg.PackageSize, 10))
	w.WriteHeader(http.StatusOK)

	if _, err := io.Copy(w, rc); err != nil {
		h.Log.Errorw("failed to write package response", "id", packageID, "version", version, "error", err)
	}
}

func (h *HTTPHandler) handleRegistrationIndex(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	repoName := vars["name"]
	packageID := strings.ToLower(vars["id"])

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

	versions, err := h.DB.ListNuGetPackageVersions(r.Context(), repoID, packageID)
	if err != nil {
		h.Log.Errorw("failed to list nuget versions", "id", packageID, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if len(versions) == 0 {
		http.NotFound(w, r)
		return
	}

	baseURL := fmt.Sprintf("%s/repos/%s", h.BaseURL, repoName)

	items := make([]map[string]any, len(versions))
	for i, v := range versions {
		items[i] = map[string]any{
			"catalogEntry": map[string]any{
				"id":               v.PackageID,
				"version":          v.Version,
				"description":      v.Description,
				"authors":          v.Authors,
				"title":            v.Title,
				"summary":          v.Summary,
				"tags":             v.Tags,
				"projectUrl":       v.ProjectURL,
				"licenseUrl":       v.LicenseURL,
				"iconUrl":          v.IconURL,
				"dependencyGroups": h.nuspecDependencyGroups(r.Context(), packageID, v),
			},
			"packageContent": fmt.Sprintf("%s/v3/package/%s/%s/%s.%s.nupkg", baseURL, packageID, v.Version, packageID, v.Version),
		}
	}

	index := map[string]any{
		"count": len(versions),
		"items": []map[string]any{
			{
				"count": len(versions),
				"items": items,
			},
		},
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	if encErr := json.NewEncoder(w).Encode(index); encErr != nil {
		_ = encErr // response header already written
	}
}

func (h *HTTPHandler) handleSearch(w http.ResponseWriter, r *http.Request) {
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

	q := r.URL.Query()
	query := q.Get("q")
	skip := parseIntMin(q.Get("skip"), 0, 0)
	take := parseIntClamp(q.Get("take"), 20, 1, 100)

	packages, total, err := h.DB.SearchNuGetPackages(r.Context(), repoID, query, skip, take)
	if err != nil {
		h.Log.Errorw("failed to search nuget packages", "query", query, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	baseURL := fmt.Sprintf("%s/repos/%s", h.BaseURL, repoName)

	data := make([]map[string]any, len(packages))
	for i, pkg := range packages {
		data[i] = map[string]any{
			"id":          pkg.PackageID,
			"version":     pkg.Version,
			"description": pkg.Description,
			"authors":     pkg.Authors,
			"title":       pkg.Title,
			"summary":     pkg.Summary,
			"tags":        pkg.Tags,
			"projectUrl":  pkg.ProjectURL,
			"iconUrl":     pkg.IconURL,
			"packageUrl":  fmt.Sprintf("%s/v3/package/%s/%s/%s.%s.nupkg", baseURL, pkg.PackageID, pkg.Version, pkg.PackageID, pkg.Version),
		}
	}

	result := map[string]any{
		"totalHits": total,
		"data":      data,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	if encErr := json.NewEncoder(w).Encode(result); encErr != nil {
		_ = encErr // response header already written
	}
}

func (h *HTTPHandler) handleAutocomplete(w http.ResponseWriter, r *http.Request) {
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

	q := r.URL.Query()
	query := q.Get("q")

	packages, total, err := h.DB.SearchNuGetPackages(r.Context(), repoID, query, 0, 100)
	if err != nil {
		h.Log.Errorw("failed to search nuget packages", "query", query, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	// Extract unique package IDs
	seen := make(map[string]bool)
	data := make([]string, 0)

	for _, pkg := range packages {
		if !seen[pkg.PackageID] {
			data = append(data, pkg.PackageID)
			seen[pkg.PackageID] = true
		}
	}

	result := map[string]any{
		"totalHits": total,
		"data":      data,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	if encErr := json.NewEncoder(w).Encode(result); encErr != nil {
		_ = encErr // response header already written
	}
}

func parseIntMin(s string, defaultVal, minVal int) int {
	if s == "" {
		return defaultVal
	}

	v, err := strconv.Atoi(s)
	if err != nil {
		return defaultVal
	}

	if v < minVal {
		return minVal
	}

	return v
}

func parseIntClamp(s string, defaultVal, minVal, maxVal int) int {
	if s == "" {
		return defaultVal
	}

	v, err := strconv.Atoi(s)
	if err != nil {
		return defaultVal
	}

	if v < minVal {
		return minVal
	}

	if v > maxVal {
		return maxVal
	}

	return v
}

// nuspecDependencyGroups fetches the .nupkg blob for one package version and
// extracts its <dependencies> element, converted to the registration index
// "dependencyGroups" shape. There is no dependency column on nuget_packages
// (the DB layer is out of scope here), so this re-parses the stored package
// on every registration index request; failures are logged and degrade to
// no dependency groups rather than failing the whole response.
func (h *HTTPHandler) nuspecDependencyGroups(ctx context.Context, packageID string, v *models.NuGetPackage) []map[string]any {
	rc, err := h.BlobStore.Get(ctx, v.BlobKey)
	if err != nil {
		h.Log.Warnw("failed to fetch package blob for dependency groups", "id", packageID, "version", v.Version, "error", err)

		return []map[string]any{}
	}
	defer func() { _ = rc.Close() }()

	nuspec, err := parseNuspec(rc)
	if err != nil {
		h.Log.Warnw("failed to parse nuspec for dependency groups", "id", packageID, "version", v.Version, "error", err)

		return []map[string]any{}
	}

	return nuspec.Metadata.Dependencies.DependencyGroups()
}
