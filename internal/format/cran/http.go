// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package cran is the hosted R package repository: the CRAN repository layout
// (src/contrib, bin/windows/contrib/<R>, bin/macosx/.../contrib/<R> with their
// PACKAGES indexes and the source Archive) that install.packages() and
// available.packages() read, plus an upload and delete endpoint.
//
// The indexes are not stored: each PACKAGES, PACKAGES.gz and PACKAGES.rds is
// rendered from the live package rows, validated by a stamp of the row set, and
// kept in the shared render cache, like helm's index.yaml and pypi's simple
// index. There is no generation worker and no advisory lock: an upload is
// visible in the index as soon as its row is.
package cran

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"html"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	"github.com/openartifact/openartifact/internal/iocopy"
	"github.com/openartifact/openartifact/internal/licensepolicy"
	"github.com/openartifact/openartifact/internal/presign"
	"github.com/openartifact/openartifact/internal/quarantine"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/pkg/cran"
	"github.com/openartifact/openartifact/pkg/logger"
)

// HTTPStore is the part of the database HTTPHandler needs.
type HTTPStore interface {
	quarantine.Checker
	licensepolicy.PolicyLister
	BlobReferenceCount(ctx context.Context, sha256 string) (int, error)
	CRANIndexStamp(ctx context.Context, repoID string) (string, error)
	CreateCRANFile(ctx context.Context, f *models.CRANFile, replaceMeta bool) error
	DecrementStorageUsage(ctx context.Context, repoID string, sizeBytes int64, artifactCount int) error
	DeleteCRANFile(ctx context.Context, repoID, fileID string) error
	GetCRANFile(ctx context.Context, repoID, path string) (*models.CRANFile, error)
	GetOrCreateStorageQuota(ctx context.Context, repoID string) (*models.StorageQuota, error)
	GetRawArtifact(ctx context.Context, repoID string, path string) (*models.RawArtifact, error)
	GetRepositoryByName(ctx context.Context, name string) (*models.Repository, error)
	ListCRANPackageFiles(ctx context.Context, repoID, pkg string) ([]*models.CRANFile, error)
	ListCRANPackageNames(ctx context.Context, repoID string) ([]string, error)
	ListCRANTreeFiles(ctx context.Context, repoID, tree string) ([]*models.CRANFile, error)
	ReserveStorageUsage(ctx context.Context, repoID string, sizeBytes int64, artifactCount int) (bool, error)
	UpsertRawArtifact(ctx context.Context, artifact *models.RawArtifact) error
}

// HTTPHandler serves the hosted cran repositories.
type HTTPHandler struct {
	*base.Handler

	DB HTTPStore
}

// NewHTTPHandler returns the handler of the hosted cran repositories.
func NewHTTPHandler(log *logger.Logger, db HTTPStore, blobStore storage.BlobStore, baseURL string) *HTTPHandler {
	return &HTTPHandler{Handler: base.New("cran", log, db, blobStore, baseURL), DB: db}
}

// RegisterRepoRoutes registers the repository's routes on the sub-router of one
// repository (/repos/cran/{name}). Every path below it is parsed by
// cran.ParsePath: a CRAN client asks for a fixed set of paths, so one
// wildcard route per method is enough.
func (h *HTTPHandler) RegisterRepoRoutes(r chi.Router) {
	r.Get("/*", h.handleGet)
	r.Head("/*", h.handleGet)
	r.Put("/*", h.handlePut)
	r.Delete("/*", h.handleDelete)
}

// documentMaxAge is how long shared caches may reuse a public repository's
// index or listing before revalidating.
const documentMaxAge = 60

// docETag derives the validator of a generated document from the live-file
// stamp. Package files are immutable and trash/restore changes membership, so
// the stamp changes exactly when an index or listing would. The repository and
// the document are mixed in; no origin takes part (a listing links by relative
// name).
func docETag(repoID, repoName, doc, stamp string) string {
	sum := sha256.Sum256([]byte(repoID + "\x00" + repoName + "\x00" + doc + "\x00" + stamp))

	return `"` + hex.EncodeToString(sum[:16]) + `"`
}

func (h *HTTPHandler) handleGet(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	repoName := vars["name"]

	p, ok := cran.ParsePath(vars["*"])
	if !ok {
		http.NotFound(w, r)
		return
	}

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

	switch p.Kind {
	case cran.KindIndex:
		h.serveIndex(w, r, repo, p)
	case cran.KindPackage, cran.KindArchivePackage:
		h.serveFile(w, r, repo, p)
	case cran.KindArchiveRoot:
		h.serveArchiveRoot(w, r, repo)
	case cran.KindArchiveDir:
		h.serveArchiveDir(w, r, repo, p.Package)
	case cran.KindOther:
		http.NotFound(w, r)
	}
}

// serveDocument serves a generated document validated by the live-file stamp.
func (h *HTTPHandler) serveDocument(
	w http.ResponseWriter, r *http.Request, repo *models.Repository, doc string,
	render func(ctx context.Context) (*base.Rendered, error),
) {
	stamp, err := h.DB.CRANIndexStamp(r.Context(), repo.ID)
	if err != nil {
		h.Log.Errorw("failed to stamp cran index", "repo", repo.Name, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	// Revalidation is answered from the stamp alone: no row is loaded and
	// nothing is rendered. The rendering is cached under the same stamp-derived
	// key, so a changed file set can never be served from an old one.
	etag := docETag(repo.ID, repo.Name, doc, stamp)

	h.ServeRendered(
		w,
		r,
		repo.Public,
		etag,
		etag,
		documentMaxAge,
		render,
		"failed to render cran document",
		"repo",
		repo.Name,
		"document",
		doc,
	)
}

func (h *HTTPHandler) serveIndex(w http.ResponseWriter, r *http.Request, repo *models.Repository, p cran.Path) {
	doc := p.Tree.Dir + "/" + p.Variant.File()

	h.serveDocument(w, r, repo, doc, func(ctx context.Context) (*base.Rendered, error) {
		files, err := h.DB.ListCRANTreeFiles(ctx, repo.ID, p.Tree.Dir)
		if err != nil {
			return nil, fmt.Errorf("list cran files: %w", err)
		}

		return renderIndex(files, p.Tree, p.Variant)
	})
}

// renderIndex renders one variant of the PACKAGES index of a tree.
func renderIndex(files []*models.CRANFile, tree cran.Tree, variant cran.Variant) (*base.Rendered, error) {
	recs := indexRecords(files, tree.Binary)

	out := &base.Rendered{ContentType: variant.ContentType()}

	var err error

	switch variant {
	case cran.VariantText:
		out.Body = cran.RenderText(recs)
	case cran.VariantGzip:
		out.Body, err = cran.Gzip(cran.RenderText(recs))
		out.Opaque = true
	case cran.VariantRDS:
		out.Body, err = cran.RenderRDS(recs, tree.Binary)
		out.Opaque = true
	}

	if err != nil {
		return nil, fmt.Errorf("render PACKAGES: %w", err)
	}

	return out, nil
}

// indexRecords is the PACKAGES content of a tree: the newest version of every
// package, in package name order. Older versions are reachable only through the
// Archive, like on CRAN.
func indexRecords(files []*models.CRANFile, binary bool) []cran.Record {
	newest := make(map[string]*models.CRANFile, len(files))

	for _, f := range files {
		if cur, ok := newest[f.Package]; !ok || cran.Less(cur.Version, f.Version) {
			newest[f.Package] = f
		}
	}

	names := make([]string, 0, len(newest))
	for name := range newest {
		names = append(names, name)
	}

	slices.Sort(names)

	recs := make([]cran.Record, 0, len(names))

	for _, name := range names {
		f := newest[name]
		recs = append(recs, cran.IndexRecord(f.Package, f.Version, f.MD5, f.Fields, binary))
	}

	return recs
}

func (h *HTTPHandler) serveArchiveRoot(w http.ResponseWriter, r *http.Request, repo *models.Repository) {
	h.serveDocument(w, r, repo, cran.SourceTree+"/"+cran.ArchiveDir+"/", func(ctx context.Context) (*base.Rendered, error) {
		names, err := h.DB.ListCRANPackageNames(ctx, repo.ID)
		if err != nil {
			return nil, fmt.Errorf("list cran packages: %w", err)
		}

		entries := make([]string, len(names))
		for i, n := range names {
			entries[i] = n + "/"
		}

		return listing("/"+cran.SourceTree+"/"+cran.ArchiveDir+"/", entries), nil
	})
}

// serveArchiveDir lists the source files of one package, answering 200 whenever
// the package has a live file in any tree (a package that only has binaries has
// an empty listing). A group asks this path of its hosted members to know
// whether one of them holds the package, so the answer must agree with what the
// repository would serve.
func (h *HTTPHandler) serveArchiveDir(w http.ResponseWriter, r *http.Request, repo *models.Repository, pkg string) {
	files, err := h.DB.ListCRANPackageFiles(r.Context(), repo.ID, pkg)
	if err != nil {
		h.Log.Errorw("failed to list cran package files", "package", pkg, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if len(files) == 0 {
		http.NotFound(w, r)
		return
	}

	dir := cran.SourceTree + "/" + cran.ArchiveDir + "/" + pkg + "/"

	h.serveDocument(w, r, repo, dir, func(context.Context) (*base.Rendered, error) {
		var entries []string

		for _, f := range files {
			if f.Tree == cran.SourceTree {
				entries = append(entries, f.Filename)
			}
		}

		slices.Sort(entries)

		return listing("/"+dir, entries), nil
	})
}

// listing renders an Apache-style directory index, the form remotes and other
// tools parse for the versions in the Archive.
func listing(dir string, entries []string) *base.Rendered {
	var b strings.Builder

	title := "Index of " + html.EscapeString(dir)

	b.WriteString(
		"<!DOCTYPE HTML PUBLIC \"-//W3C//DTD HTML 3.2 Final//EN\">\n<html>\n <head>\n  <title>" + title +
			"</title>\n </head>\n <body>\n<h1>" + title + "</h1>\n<ul>\n",
	)

	for _, e := range entries {
		esc := html.EscapeString(e)
		b.WriteString("<li><a href=\"" + esc + "\">" + esc + "</a></li>\n")
	}

	b.WriteString("</ul>\n</body></html>\n")

	return &base.Rendered{Body: []byte(b.String()), ContentType: "text/html; charset=utf-8"}
}

// serveFile streams one package file. Any version is served at its canonical
// path and at its Archive path, so a client that follows CRAN's rule (current
// version in src/contrib, older ones in Archive) and one that tries both finds
// it either way.
func (h *HTTPHandler) serveFile(w http.ResponseWriter, r *http.Request, repo *models.Repository, p cran.Path) {
	file, err := h.DB.GetCRANFile(r.Context(), repo.ID, p.Canonical())
	if err != nil {
		h.Log.Errorw("failed to get cran file", "path", p.Canonical(), "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if file == nil {
		http.NotFound(w, r)
		return
	}

	if h.DenyIfQuarantined(w, r, repo.ID, quarantine.PathKeys(file.ID, file.Path, file.Filename, r.URL.Path)...) {
		return
	}

	contentType := fileContentType(file.Filename)

	if r.Method != http.MethodHead && h.TryPresign(w, r, presign.Blob{
		SHA256: file.BlobKey, Size: file.SizeBytes, Filename: file.Filename, ContentType: contentType,
	}) {
		return
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.FormatInt(file.SizeBytes, 10))
	w.Header().Set("ETag", `"`+file.Digest+`"`)

	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}

	rc, err := h.BlobStore.Get(r.Context(), file.BlobKey)
	if err != nil {
		h.Log.Errorw("failed to get cran blob", "key", file.BlobKey, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	defer func() { _ = rc.Close() }()

	w.WriteHeader(http.StatusOK)

	if _, err := iocopy.Copy(w, rc); err != nil {
		h.Log.Errorw("failed to write cran file", "path", file.Path, "error", err)
	}
}

func fileContentType(filename string) string {
	if strings.HasSuffix(filename, ".zip") {
		return "application/zip"
	}

	return "application/gzip"
}
