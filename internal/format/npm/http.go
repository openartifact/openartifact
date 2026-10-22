// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package npm

import (
	"context"
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
	"github.com/openartifact/openartifact/internal/presign"
	"github.com/openartifact/openartifact/internal/quarantine"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/pkg/logger"
)

type HTTPHandler struct {
	*base.Handler
}

func NewHTTPHandler(log *logger.Logger, db database.Database, blobStore storage.BlobStore, baseURL string) *HTTPHandler {
	return &HTTPHandler{Handler: base.New(log, db, blobStore, baseURL)}
}

func (h *HTTPHandler) RegisterRepoRoutes(r chi.Router) {
	r.Get("/-/package/{pkg}/dist-tags", h.handleGetDistTags)
	r.Get("/{pkg}/-/{tarball}", h.handleTarball)
	r.Get("/{scope}/{pkg}/-/{tarball}", h.handleTarball)
	r.Get("/{pkg}/{version}", h.handleVersionManifest)
	r.Get("/{pkg}", h.handlePackument)
}

func (h *HTTPHandler) RegisterRoutes(r chi.Router, prefix string) {
	r.Route(prefix, func(r chi.Router) {
		h.RegisterRepoRoutes(r)
	})
	h.Log.Infow("npm handler registered", "prefix", prefix)
}

func resolvePackageName(raw string) string {
	s := strings.ReplaceAll(raw, "%2F", "/")
	s = strings.ReplaceAll(s, "%2f", "/")

	return s
}

// packumentMaxAge is how long shared caches may reuse a public repository's
// packument before revalidating.
const packumentMaxAge = 60

func (h *HTTPHandler) handlePackument(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	if strings.HasPrefix(vars["pkg"], "-") {
		http.NotFound(w, r)
		return
	}

	repoName := vars["name"]
	pkgName := resolvePackageName(vars["pkg"])

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

	pkg, err := h.DB.GetNPMPackageByName(r.Context(), repo.ID, pkgName)
	if err != nil {
		h.Log.Errorw("failed to get npm package", "name", pkgName, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if pkg == nil {
		http.NotFound(w, r)
		return
	}

	abbreviated := strings.Contains(r.Header.Get("Accept"), "application/vnd.npm.install-v1+json")

	etag := fmt.Sprintf(`"%x"`, pkg.UpdatedAt.UnixNano())
	if abbreviated {
		etag = `"abbr-` + etag[1:]
	}

	w.Header().Set("Vary", "Accept")

	// The ETag derives from the package row alone, so a revalidation is
	// answered before loading and rendering every version.
	if base.Revalidate(w, r, repo.Public, etag, packumentMaxAge) {
		return
	}

	versions, err := h.DB.GetNPMVersionsByPackage(r.Context(), pkg.ID)
	if err != nil {
		h.Log.Errorw("failed to get npm versions", "pkg", pkgName, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	distTags, err := h.DB.GetNPMDistTags(r.Context(), pkg.ID)
	if err != nil {
		h.Log.Errorw("failed to get npm dist-tags", "pkg", pkgName, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	var body []byte

	if abbreviated {
		body, err = BuildAbbreviatedPackument(repoName, pkgName, versions, distTags, h.BaseURL)
	} else {
		body, err = BuildPackument(repoName, pkgName, versions, distTags, h.BaseURL)
	}

	if err != nil {
		h.Log.Errorw("failed to build packument", "pkg", pkgName, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body) //nolint:gosec // JSON document served as application/json, not HTML
}

func (h *HTTPHandler) handleVersionManifest(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	if strings.HasPrefix(vars["pkg"], "-") {
		http.NotFound(w, r)
		return
	}

	repoName := vars["name"]
	pkgName := resolvePackageName(vars["pkg"])
	versionOrTag := vars["version"]

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

	pkg, err := h.DB.GetNPMPackageByName(r.Context(), repoID, pkgName)
	if err != nil {
		h.Log.Errorw("failed to get npm package", "name", pkgName, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if pkg == nil {
		http.NotFound(w, r)
		return
	}

	resolver := NewVersionResolver(h.DB)

	resolvedVersion, err := resolver.Resolve(r.Context(), pkg.ID, versionOrTag)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	v, err := h.DB.GetNPMVersion(r.Context(), pkg.ID, resolvedVersion)
	if err != nil {
		h.Log.Errorw("failed to get npm version", "pkg", pkgName, "version", resolvedVersion, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if v == nil {
		http.NotFound(w, r)
		return
	}

	body, err := BuildVersionManifest(repoName, pkgName, v, h.BaseURL)
	if err != nil {
		h.Log.Errorw("failed to render npm version manifest", "pkg", pkgName, "version", resolvedVersion, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	//nolint:gosec // G705: npm package manifest JSON, not HTML output
	_, _ = w.Write(body)
}

func (h *HTTPHandler) handleTarball(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	repoName := vars["name"]
	pkgName := resolvePackageName(vars["pkg"])
	if scope := vars["scope"]; scope != "" {
		pkgName = resolvePackageName(scope) + "/" + pkgName
	}

	tarball := vars["tarball"]

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

	pkg, err := h.DB.GetNPMPackageByName(r.Context(), repoID, pkgName)
	if err != nil {
		h.Log.Errorw("failed to get npm package", "name", pkgName, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if pkg == nil {
		http.NotFound(w, r)
		return
	}

	blobSHA256, version, err := h.resolveTarball(r.Context(), pkg, pkgName, tarball)
	if err != nil {
		h.Log.Errorw("failed to get npm versions", "pkg", pkgName, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if blobSHA256 == "" {
		http.NotFound(w, r)
		return
	}

	if h.DenyIfQuarantined(w, r, repoID, quarantine.PathKeys(pkgName+"@"+version, pkgName+"/-/"+tarball, tarball, r.URL.Path)...) {
		return
	}

	size, err := h.BlobStore.GetSize(r.Context(), blobSHA256)
	if err != nil {
		h.Log.Errorw("failed to stat tarball blob", "sha256", blobSHA256, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if h.TryPresign(w, r, presign.Blob{SHA256: blobSHA256, Size: size, Filename: tarball, ContentType: "application/octet-stream"}) {
		return
	}

	rc, err := h.BlobStore.Get(r.Context(), blobSHA256)
	if err != nil {
		h.Log.Errorw("failed to get tarball blob", "sha256", blobSHA256, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}
	defer func() { _ = rc.Close() }()

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	w.Header().Set("ETag", `"`+blobSHA256+`"`)

	if _, err := io.Copy(w, rc); err != nil {
		h.Log.Errorw("failed to write tarball response", "filename", tarball, "error", err)
	}
}

// resolveTarball maps a tarball filename to its version and blob digest.
// Tarballs are named "<unscoped-name>-<version>.tgz", so the common case is one
// indexed lookup of that version; only an unconventional filename falls back to
// scanning every version of the package. It returns empty strings when no
// version owns the tarball.
func (h *HTTPHandler) resolveTarball(ctx context.Context, pkg *models.NPMPackage, pkgName, tarball string) (string, string, error) {
	base := pkgName
	if i := strings.LastIndexByte(base, '/'); i >= 0 {
		base = base[i+1:]
	}

	if cand, ok := strings.CutPrefix(strings.TrimSuffix(tarball, ".tgz"), base+"-"); ok && strings.HasSuffix(tarball, ".tgz") {
		v, err := h.DB.GetNPMVersion(ctx, pkg.ID, cand)
		if err != nil {
			return "", "", fmt.Errorf("get npm version: %w", err)
		}

		if v != nil && v.TarballFilename == tarball {
			return v.BlobSHA256, v.Version, nil
		}
	}

	versions, err := h.DB.GetNPMVersionsByPackage(ctx, pkg.ID)
	if err != nil {
		return "", "", fmt.Errorf("list npm versions: %w", err)
	}

	for _, v := range versions {
		if v.TarballFilename == tarball {
			return v.BlobSHA256, v.Version, nil
		}
	}

	return "", "", nil
}

func (h *HTTPHandler) handleGetDistTags(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	repoName := vars["name"]
	pkgName := resolvePackageName(vars["pkg"])

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

	pkg, err := h.DB.GetNPMPackageByName(r.Context(), repoID, pkgName)
	if err != nil {
		h.Log.Errorw("failed to get npm package", "name", pkgName, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if pkg == nil {
		http.NotFound(w, r)
		return
	}

	distTags, err := h.DB.GetNPMDistTags(r.Context(), pkg.ID)
	if err != nil {
		h.Log.Errorw("failed to get npm dist-tags", "pkg", pkgName, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	w.Header().Set("Content-Type", "application/json")

	if encErr := json.NewEncoder(w).Encode(distTags); encErr != nil {
		_ = encErr // response header already written
	}
}
