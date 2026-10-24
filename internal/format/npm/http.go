// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package npm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	"github.com/openartifact/openartifact/internal/iocopy"
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
	r.Get("/{pkg}/{version}", base.Compress(h.handleVersionManifest))
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

	// The rendering is cached under the validator, qualified by what else shapes
	// the document (package, repository name and base URL for the tarball URLs).
	// Everything that changes a packument marks the package row stale, which
	// changes the ETag, so a cached document is never served past a change; the
	// rows are read after the package row, so a rendering is never older than
	// its key.
	key := pkg.ID + "\x00" + etag + "\x00" + repoName + "\x00" + h.BaseURL

	rendered, err := h.Rendered(r.Context(), key, func(ctx context.Context) (*base.Rendered, error) {
		return h.renderPackument(ctx, repoName, pkgName, pkg.ID, abbreviated)
	})
	if err != nil {
		h.Log.Errorw("failed to render packument", "pkg", pkgName, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	rendered.Serve(w, r)
}

// renderPackument loads a package's versions and dist-tags and builds its
// packument (the abbreviated install form or the full document).
func (h *HTTPHandler) renderPackument(ctx context.Context, repoName, pkgName, pkgID string, abbreviated bool) (*base.Rendered, error) {
	versions, err := h.DB.GetNPMVersionsByPackage(ctx, pkgID)
	if err != nil {
		return nil, fmt.Errorf("get npm versions: %w", err)
	}

	distTags, err := h.DB.GetNPMDistTags(ctx, pkgID)
	if err != nil {
		return nil, fmt.Errorf("get npm dist-tags: %w", err)
	}

	build := BuildPackument
	if abbreviated {
		build = BuildAbbreviatedPackument
	}

	body, err := build(repoName, pkgName, versions, distTags, h.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("build packument: %w", err)
	}

	return &base.Rendered{Body: body, ContentType: "application/json"}, nil
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

	blob, err := h.resolveTarball(r.Context(), pkg, pkgName, tarball)
	if err != nil {
		h.Log.Errorw("failed to get npm versions", "pkg", pkgName, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if blob.sha256 == "" {
		http.NotFound(w, r)
		return
	}

	if h.DenyIfQuarantined(w, r, repoID, quarantine.PathKeys(pkgName+"@"+blob.version, pkgName+"/-/"+tarball, tarball, r.URL.Path)...) {
		return
	}

	// The size is recorded with the blob row; only a row without one (zero) has
	// to ask the store.
	size := blob.size
	if size <= 0 {
		size, err = h.BlobStore.GetSize(r.Context(), blob.sha256)
		if err != nil {
			h.Log.Errorw("failed to stat tarball blob", "sha256", blob.sha256, "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)

			return
		}
	}

	if h.TryPresign(w, r, presign.Blob{SHA256: blob.sha256, Size: size, Filename: tarball, ContentType: "application/octet-stream"}) {
		return
	}

	rc, err := h.BlobStore.Get(r.Context(), blob.sha256)
	if err != nil {
		h.Log.Errorw("failed to get tarball blob", "sha256", blob.sha256, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}
	defer func() { _ = rc.Close() }()

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	w.Header().Set("ETag", `"`+blob.sha256+`"`)

	if _, err := iocopy.Copy(w, rc); err != nil {
		h.Log.Errorw("failed to write tarball response", "filename", tarball, "error", err)
	}
}

// tarballBlob is the stored blob behind one tarball filename.
type tarballBlob struct {
	sha256  string // blob store key
	size    int64  // from the blob row; 0 when unknown
	version string
}

func tarballOf(v *models.NPMVersion) tarballBlob {
	return tarballBlob{sha256: v.BlobSHA256, size: v.BlobSizeBytes, version: v.Version}
}

// resolveTarball maps a tarball filename to its version and blob. Tarballs are
// named "<unscoped-name>-<version>.tgz", so the common case is one indexed
// lookup of that version; only an unconventional filename falls back to
// scanning every version of the package. The zero tarballBlob means no version
// owns the tarball.
func (h *HTTPHandler) resolveTarball(ctx context.Context, pkg *models.NPMPackage, pkgName, tarball string) (tarballBlob, error) {
	base := pkgName
	if i := strings.LastIndexByte(base, '/'); i >= 0 {
		base = base[i+1:]
	}

	if cand, ok := strings.CutPrefix(strings.TrimSuffix(tarball, ".tgz"), base+"-"); ok && strings.HasSuffix(tarball, ".tgz") {
		v, err := h.DB.GetNPMVersion(ctx, pkg.ID, cand)
		if err != nil {
			return tarballBlob{}, fmt.Errorf("get npm version: %w", err)
		}

		if v != nil && v.TarballFilename == tarball {
			return tarballOf(v), nil
		}
	}

	versions, err := h.DB.GetNPMVersionsByPackage(ctx, pkg.ID)
	if err != nil {
		return tarballBlob{}, fmt.Errorf("list npm versions: %w", err)
	}

	for _, v := range versions {
		if v.TarballFilename == tarball {
			return tarballOf(v), nil
		}
	}

	return tarballBlob{}, nil
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
