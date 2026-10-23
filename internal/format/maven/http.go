// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package maven

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	"github.com/openartifact/openartifact/internal/presign"
	"github.com/openartifact/openartifact/internal/quarantine"
	"github.com/openartifact/openartifact/internal/storage"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/pkg/logger"
	pkgmaven "github.com/openartifact/openartifact/pkg/maven"
)

// metadataMaxAge is how long shared caches may reuse a public repository's
// metadata and artifacts before revalidating.
// maxMetadataBlobBytes caps a maven-metadata.xml read back from the blob store.
const maxMetadataBlobBytes = 16 << 20

const metadataMaxAge = 300

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

func (h *HTTPHandler) serveArtifactBlob(w http.ResponseWriter, r *http.Request, repoPublic bool, art *models.MavenArtifact, contentType string) {
	if base.Revalidate(w, r, repoPublic, `"`+art.SHA256+`"`, metadataMaxAge) {
		return
	}

	if r.Method == http.MethodHead {
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Content-Length", strconv.FormatInt(art.BlobSizeBytes, 10))
		w.WriteHeader(http.StatusOK)

		return
	}

	if h.TryPresign(w, r, presign.Blob{SHA256: art.SHA256, Size: art.BlobSizeBytes, Filename: filepath.Base(art.RepoPath), ContentType: contentType}) {
		return
	}

	rc, err := h.BlobStore.Get(r.Context(), art.SHA256)
	if err != nil {
		h.Log.Errorw("blob fetch failed", "sha256", art.SHA256, "error", err)
		http.Error(w, "blob fetch failed", http.StatusInternalServerError)
		return
	}
	defer func() { _ = rc.Close() }()

	w.Header().Set("Content-Type", contentType)

	if _, err := io.Copy(w, rc); err != nil {
		h.Log.Errorw("failed to write response", "path", art.RepoPath, "error", err)
	}
}

//nolint:gocyclo,cyclop // protocol handler: branching mirrors spec state machine
func (h *HTTPHandler) handleGet(w http.ResponseWriter, r *http.Request) {
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

	repoID := repo.ID
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

				h.serveMetadataChecksum(w, r, repo.Public, meta, suffix)

				return
			}

			art, err := h.DB.GetMavenArtifactByPath(r.Context(), repoID, underlyingPath)
			if err != nil {
				h.Log.Errorw("failed to query artifact", "path", underlyingPath, "error", err)
				http.Error(w, "internal error", http.StatusInternalServerError)

				return
			}

			if art == nil {
				art, err = h.latestSnapshotFile(r.Context(), repoID, underlyingPath)
				if err != nil {
					h.Log.Errorw("failed to resolve latest snapshot", "path", underlyingPath, "error", err)
					http.Error(w, "internal error", http.StatusInternalServerError)

					return
				}
			}

			if art == nil {
				http.NotFound(w, r)
				return
			}

			// A checksum discloses the artifact's digest: it is as quarantined
			// as the artifact itself.
			if h.DenyIfQuarantined(w, r, repoID, quarantine.PathKeys(art.RepoPath, underlyingPath, r.URL.Path, before)...) {
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

		h.serveMetadata(w, r, repo.Public, meta)

		return
	}

	art, err := h.DB.GetMavenArtifactByPath(r.Context(), repoID, artPath)
	if err != nil {
		h.Log.Errorw("failed to query artifact", "path", artPath, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	// A path with no row of its own may be the legacy non-unique snapshot name
	// ("foo-1.0-SNAPSHOT[-classifier].ext"): serve the newest build of that
	// very file.
	if art == nil {
		art, err = h.latestSnapshotFile(r.Context(), repoID, artPath)
		if err != nil {
			h.Log.Errorw("failed to resolve latest snapshot", "path", artPath, "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)

			return
		}

		if art != nil {
			if h.DenyIfQuarantined(w, r, repoID, quarantine.PathKeys(art.RepoPath, artPath, r.URL.Path)...) {
				return
			}

			h.serveArtifactBlob(w, r, repo.Public, art, ContentTypeForFile(art.RepoPath))

			return
		}
	}

	if art == nil {
		http.NotFound(w, r)
		return
	}

	if h.DenyIfQuarantined(w, r, repoID, quarantine.PathKeys(artPath, r.URL.Path)...) {
		return
	}

	h.serveArtifactBlob(w, r, repo.Public, art, ContentTypeForFile(filename))
}

// readMetadataBlob reads a maven-metadata.xml blob. The blob is content
// addressed, so what it returns for one digest never changes.
func (h *HTTPHandler) readMetadataBlob(ctx context.Context, sha256 string) ([]byte, error) {
	rc, err := h.BlobStore.Get(ctx, sha256)
	if err != nil {
		return nil, fmt.Errorf("get metadata blob: %w", err)
	}

	defer func() { _ = rc.Close() }()

	data, err := base.ReadAllLimited(rc, maxMetadataBlobBytes)
	if err != nil {
		return nil, fmt.Errorf("read metadata blob: %w", err)
	}

	return data, nil
}

// serveMetadata serves maven-metadata.xml. Every build tool polls it, so the
// rendering is cached by blob digest (no store round trip per poll) and sent
// compressed to clients that accept it.
func (h *HTTPHandler) serveMetadata(w http.ResponseWriter, r *http.Request, repoPublic bool, meta *models.RepoMetadata) {
	if base.Revalidate(w, r, repoPublic, `"`+meta.BlobSHA256+`"`, metadataMaxAge) {
		return
	}

	rendered, err := h.Rendered(r.Context(), "xml:"+meta.BlobSHA256, func(ctx context.Context) (*base.Rendered, error) {
		data, err := h.readMetadataBlob(ctx, meta.BlobSHA256)
		if err != nil {
			return nil, err
		}

		return &base.Rendered{Body: data, ContentType: "application/xml"}, nil
	})
	if err != nil {
		h.Log.Errorw("failed to read metadata blob", "sha256", meta.BlobSHA256, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	rendered.Serve(w, r)
}

// serveMetadataChecksum serves maven-metadata.xml.{md5,sha1,sha256,sha512}.
// The checksum is a pure function of the metadata blob, so the blob digest
// (plus the algorithm) is a complete validator: a revalidation never reads the
// blob, and a cache miss hashes it with the one algorithm asked for.
func (h *HTTPHandler) serveMetadataChecksum(w http.ResponseWriter, r *http.Request, repoPublic bool, meta *models.RepoMetadata, suffix string) {
	if base.Revalidate(w, r, repoPublic, `"`+meta.BlobSHA256+suffix+`"`, metadataMaxAge) {
		return
	}

	rendered, err := h.Rendered(r.Context(), "sum:"+meta.BlobSHA256+suffix, func(ctx context.Context) (*base.Rendered, error) {
		hashVal := meta.BlobSHA256

		if suffix != ".sha256" {
			data, err := h.readMetadataBlob(ctx, meta.BlobSHA256)
			if err != nil {
				return nil, err
			}

			hashVal = pkgmaven.Checksum(suffix, data)
		}

		return &base.Rendered{Body: []byte(hashVal + "  maven-metadata.xml\n"), ContentType: "text/plain"}, nil
	})
	if err != nil {
		h.Log.Errorw("failed to compute metadata checksum", "sha256", meta.BlobSHA256, "suffix", suffix, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	rendered.Serve(w, r)
}

// handleHead answers exactly what GET would (metadata, checksums, snapshots,
// quarantine, validators) without a body. Artifact blobs are never read: the
// size comes from the database.
func (h *HTTPHandler) handleHead(w http.ResponseWriter, r *http.Request) {
	hw := &headWriter{ResponseWriter: w}
	h.handleGet(hw, r)
	hw.finish()
}

// headWriter turns a GET response into a HEAD one. The small bodies GET
// builds itself (metadata, checksums) are buffered to learn their length; a
// handler that already declared Content-Length (artifact blobs) keeps it.
type headWriter struct {
	http.ResponseWriter

	status int
	body   bytes.Buffer
}

func (w *headWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}

func (w *headWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}

	return w.body.Write(p)
}

func (w *headWriter) finish() {
	if w.status == 0 {
		w.status = http.StatusOK
	}

	hdr := w.Header()
	if hdr.Get("Content-Length") == "" && w.body.Len() > 0 {
		hdr.Set("Content-Length", strconv.Itoa(w.body.Len()))
	}

	w.ResponseWriter.WriteHeader(w.status)
}

// latestSnapshotFile resolves "<artifact>-<X>-SNAPSHOT[-classifier].<ext>" in
// <group>/<artifact>/<X>-SNAPSHOT/ to the newest build of that classifier and
// extension. It returns nil when the path has another shape or no build.
func (h *HTTPHandler) latestSnapshotFile(ctx context.Context, repoID, artPath string) (*models.MavenArtifact, error) {
	groupPath, artifactID, version, filename, err := pkgmaven.ParseRepositoryPath(artPath)
	if err != nil {
		return nil, nil //nolint:nilnil,nilerr // an unparsable path is simply not a snapshot file
	}

	if !pkgmaven.IsSnapshot(version) {
		return nil, nil //nolint:nilnil // not a snapshot path
	}

	classifier, ext, ok := splitSnapshotFilename(filename, artifactID, version)
	if !ok {
		return nil, nil //nolint:nilnil // not a legacy snapshot name
	}

	art, err := h.DB.GetLatestSnapshotFile(ctx, repoID, strings.ReplaceAll(groupPath, "/", "."), artifactID, version, classifier, ext)
	if err != nil {
		return nil, fmt.Errorf("get latest snapshot file: %w", err)
	}

	return art, nil
}

// splitSnapshotFilename splits "<artifact>-<version>[-classifier].<ext>"
// (extensions may contain dots: tar.gz).
func splitSnapshotFilename(filename, artifactID, version string) (string, string, bool) {
	rest, found := strings.CutPrefix(filename, artifactID+"-"+version)
	if !found {
		return "", "", false
	}

	if after, hasDash := strings.CutPrefix(rest, "-"); hasDash {
		cls, e, hasExt := strings.Cut(after, ".")
		if !hasExt || cls == "" || e == "" {
			return "", "", false
		}

		return cls, e, true
	}

	if after, hasDot := strings.CutPrefix(rest, "."); hasDot && after != "" {
		return "", after, true
	}

	return "", "", false
}

func (h *HTTPHandler) handleMKCOL(w http.ResponseWriter) {
	w.WriteHeader(http.StatusOK)
}
