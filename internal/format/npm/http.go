// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package npm

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/format/base"
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

func (h *HTTPHandler) handlePackument(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	if strings.HasPrefix(vars["pkg"], "-") {
		http.NotFound(w, r)
		return
	}

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

	abbreviated := strings.Contains(r.Header.Get("Accept"), "application/vnd.npm.install-v1+json")
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

	etag := fmt.Sprintf(`"%x"`, pkg.UpdatedAt.UnixNano())
	if abbreviated {
		etag = `"abbr-` + etag[1:]
	}

	w.Header().Set("Vary", "Accept")
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "public, max-age=60")

	if etagMatches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
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

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	//nolint:gosec // G705: npm package manifest JSON, not HTML output
	_, _ = w.Write(v.ManifestJSON)
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

	versions, err := h.DB.GetNPMVersionsByPackage(r.Context(), pkg.ID)
	if err != nil {
		h.Log.Errorw("failed to get npm versions", "pkg", pkgName, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	var (
		blobSHA256 string
		version    string
	)

	for _, v := range versions {
		if v.TarballFilename == tarball {
			blobSHA256 = v.BlobSHA256
			version = v.Version

			break
		}
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

// etagMatches reports whether an If-None-Match header value matches etag.
func etagMatches(header, etag string) bool {
	for c := range strings.SplitSeq(header, ",") {
		c = strings.TrimPrefix(strings.TrimSpace(c), "W/")
		if c == "*" || c == etag {
			return true
		}
	}

	return false
}
