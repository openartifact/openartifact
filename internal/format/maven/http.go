// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package maven

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	"github.com/openartifact/openartifact/internal/quarantine"
	"github.com/openartifact/openartifact/internal/storage"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/pkg/logger"
	pkgmaven "github.com/openartifact/openartifact/pkg/maven"
)

type HTTPHandler struct {
	*base.Handler
}

func NewHTTPHandler(log *logger.Logger, db database.Database, blobStore storage.BlobStore) *HTTPHandler {
	return &HTTPHandler{Handler: base.New(log, db, blobStore, "")}
}

// RegisterRepoRoutes registers Maven-specific routes on the provided sub-router.
// Uses HandleFunc with internal method dispatch because chi v5.2.5 rejects
// non-standard HTTP methods (MKCOL) via Method()/MethodFunc().
//
// Uses a true chi wildcard ("/*") rather than a regexp param: chi's
// {name:regexp} params never span "/" (the tail delimiter is always '/'
// when nothing follows the closing brace, so the regexp is only ever
// tested against a single path segment) — a pattern like {path:.*/.+}
// can never match a multi-segment GAV path and silently 404s everything.
func (h *HTTPHandler) RegisterRepoRoutes(r chi.Router) {
	r.HandleFunc("/*", h.handlePath)
}

func (h *HTTPHandler) RegisterRoutes(r chi.Router, prefix string) {
	r.Route(prefix, func(r chi.Router) {
		h.RegisterRepoRoutes(r)
	})
}

// handlePath dispatches based on HTTP method. MKCOL is WebDAV and not
// supported by chi v5.2.5's Method(), so we route it manually here.
func (h *HTTPHandler) handlePath(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.handleGet(w, r)
	case http.MethodHead:
		h.handleHead(w, r)
	case "MKCOL":
		h.handleMKCOL(w)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (h *HTTPHandler) serveArtifactBlob(w http.ResponseWriter, r *http.Request, art *models.MavenArtifact, contentType string) {
	rc, err := h.BlobStore.Get(r.Context(), art.SHA256)
	if err != nil {
		h.Log.Errorw("blob fetch failed", "sha256", art.SHA256, "error", err)
		http.Error(w, "blob fetch failed", http.StatusInternalServerError)
		return
	}
	defer func() { _ = rc.Close() }()

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("ETag", `"`+art.SHA256+`"`)
	w.Header().Set("Cache-Control", "public, max-age=300")

	if _, err := io.Copy(w, rc); err != nil {
		h.Log.Errorw("failed to write response", "path", art.RepoPath, "error", err)
	}
}

//nolint:gocyclo,cyclop // protocol handler: branching mirrors spec state machine
func (h *HTTPHandler) handleGet(w http.ResponseWriter, r *http.Request) {
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

	artPath := chi.URLParam(r, "*")

	checksumSuffixes := []string{".md5", ".sha1", ".sha256", ".sha512"}
	for _, suffix := range checksumSuffixes {
		if before, ok := strings.CutSuffix(artPath, suffix); ok { //nolint:nestif // Maven path suffix matching requires nested checks
			underlyingPath := before
			underlyingFilename := filepath.Base(underlyingPath)

			if filepath.Base(underlyingPath) == "maven-metadata.xml" {
				meta, err := h.DB.GetRepoMetadataByPath(r.Context(), repoID, underlyingPath)
				if err != nil {
					h.Log.Errorw("failed to query repo metadata", "path", underlyingPath, "error", err)
					http.Error(w, "internal error", http.StatusInternalServerError)

					return
				}

				if meta == nil {
					http.NotFound(w, r)
					return
				}

				var hashVal string

				switch suffix {
				case ".sha256":
					hashVal = meta.BlobSHA256
				default:
					rc, err := h.BlobStore.Get(r.Context(), meta.BlobSHA256)
					if err != nil {
						h.Log.Errorw("failed to read metadata blob for checksum", "sha256", meta.BlobSHA256, "error", err)
						http.Error(w, "internal error", http.StatusInternalServerError)

						return
					}

					data, readErr := io.ReadAll(rc)
					_ = rc.Close()

					if readErr != nil {
						h.Log.Errorw("failed to read metadata blob for checksum", "sha256", meta.BlobSHA256, "error", readErr)
						http.Error(w, "internal error", http.StatusInternalServerError)

						return
					}

					md5Hex, sha1Hex, _, sha512Hex, checksumErr := pkgmaven.ComputeChecksums(bytes.NewReader(data))
					if checksumErr != nil {
						h.Log.Errorw("failed to compute metadata checksums", "path", underlyingPath, "error", checksumErr)
						http.Error(w, "internal error", http.StatusInternalServerError)

						return
					}

					switch suffix {
					case ".md5":
						hashVal = md5Hex
					case ".sha1":
						hashVal = sha1Hex
					case ".sha512":
						hashVal = sha512Hex
					}
				}

				w.Header().Set("Content-Type", "text/plain")
				//nolint:gosec // G705: Maven artifact content, not HTML output
				_, _ = fmt.Fprintf(w, "%s  %s\n", hashVal, filepath.Base(underlyingPath))

				return
			}

			art, err := h.DB.GetMavenArtifactByPath(r.Context(), repoID, underlyingPath)
			if err != nil {
				h.Log.Errorw("failed to query artifact", "path", underlyingPath, "error", err)
				http.Error(w, "internal error", http.StatusInternalServerError)

				return
			}

			if art == nil {
				http.NotFound(w, r)
				return
			}

			var hashVal string

			switch suffix {
			case ".md5":
				hashVal = art.MD5
			case ".sha1":
				hashVal = art.SHA1
			case ".sha256":
				hashVal = art.SHA256
			case ".sha512":
				hashVal = art.SHA512
			}

			w.Header().Set("Content-Type", "text/plain")
			//nolint:gosec // G705: Maven artifact content, not HTML output
			_, _ = fmt.Fprintf(w, "%s  %s\n", hashVal, underlyingFilename)

			return
		}
	}

	filename := filepath.Base(artPath)
	if filename == "maven-metadata.xml" {
		meta, err := h.DB.GetRepoMetadataByPath(r.Context(), repoID, artPath)
		if err != nil {
			h.Log.Errorw("failed to query repo metadata", "path", artPath, "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)

			return
		}

		if meta == nil {
			http.NotFound(w, r)
			return
		}

		rc, err := h.BlobStore.Get(r.Context(), meta.BlobSHA256)
		if err != nil {
			h.Log.Errorw("failed to read metadata blob", "sha256", meta.BlobSHA256, "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)

			return
		}

		defer func() { _ = rc.Close() }()

		w.Header().Set("Content-Type", "application/xml")
		w.Header().Set("ETag", `"`+meta.BlobSHA256+`"`)
		w.Header().Set("Cache-Control", "public, max-age=300")

		if _, err := io.Copy(w, rc); err != nil {
			h.Log.Errorw("failed to write metadata response", "path", artPath, "error", err)
		}

		return
	}

	_, artifactID, version, _, err := pkgmaven.ParseRepositoryPath(artPath)
	if err == nil && pkgmaven.IsSnapshot(version) && strings.Contains(filename, "-SNAPSHOT.") {
		groupPath, _, _, _, _ := pkgmaven.ParseRepositoryPath(artPath)
		groupID := strings.ReplaceAll(groupPath, "/", ".")

		art, err := h.DB.GetLatestSnapshot(r.Context(), repoID, groupID, artifactID, version)
		if err != nil {
			h.Log.Errorw("failed to resolve latest snapshot", "path", artPath, "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)

			return
		}

		if art == nil {
			http.NotFound(w, r)
			return
		}

		if h.DenyIfQuarantined(w, r, repoID, quarantine.PathKeys(art.RepoPath, artPath, r.URL.Path)...) {
			return
		}

		h.serveArtifactBlob(w, r, art, ContentTypeForFile(art.RepoPath))

		return
	}

	art, err := h.DB.GetMavenArtifactByPath(r.Context(), repoID, artPath)
	if err != nil {
		h.Log.Errorw("failed to query artifact", "path", artPath, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if art == nil {
		http.NotFound(w, r)
		return
	}

	if h.DenyIfQuarantined(w, r, repoID, quarantine.PathKeys(artPath, r.URL.Path)...) {
		return
	}

	h.serveArtifactBlob(w, r, art, ContentTypeForFile(filename))
}

func (h *HTTPHandler) handleHead(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	repoName := vars["name"]

	repoID, err := h.ResolveRepoID(r.Context(), repoName)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if repoID == "" {
		http.NotFound(w, r)
		return
	}

	artPath := chi.URLParam(r, "*")

	art, err := h.DB.GetMavenArtifactByPath(r.Context(), repoID, artPath)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if art == nil {
		http.NotFound(w, r)
		return
	}

	filename := filepath.Base(artPath)
	w.Header().Set("Content-Type", ContentTypeForFile(filename))
	w.Header().Set("Content-Length", "0")
	w.Header().Set("ETag", `"`+art.SHA256+`"`)
	w.WriteHeader(http.StatusOK)
}

func (h *HTTPHandler) handleMKCOL(w http.ResponseWriter) {
	w.WriteHeader(http.StatusOK)
}
