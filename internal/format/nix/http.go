// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package nix serves hosted Nix binary caches.
//
// A repository is a binary cache the way Nix's HTTP binary cache store expects
// it: /repos/{name}/nix-cache-info, /repos/{name}/<storehash>.narinfo and
// /repos/{name}/nar/<filehash>.nar[.<ext>]. "nix copy --to" uploads with PUT:
// the NAR file first, then its narinfo, which is what makes the path visible.
// A NAR is therefore staged per repository (nix_pending_nars) until a narinfo
// that names it commits it.
//
// Nothing is generated: the narinfo is rendered from the stored row and signed
// on the way out with the repository's ed25519 key (internal/nixkeys), so a key
// rotation re-signs every path at once.
package nix

import (
	"context"
	"encoding/hex"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	"github.com/openartifact/openartifact/internal/iocopy"
	"github.com/openartifact/openartifact/internal/metrics"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/internal/presign"
	"github.com/openartifact/openartifact/internal/quarantine"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/pkg/logger"
	"github.com/openartifact/openartifact/pkg/nix"
)

// FormatName is the repository format name.
const FormatName = "nix"

// How long shared caches may reuse a public repository's responses before
// revalidating. A narinfo changes when the key rotates; a NAR file never
// changes under its (content-hash) name.
const (
	narinfoMaxAge = 60
	narMaxAge     = 3600
)

// Content types Nix uses.
const (
	contentTypeNarInfo = "text/x-nix-narinfo"
	contentTypeNar     = "application/x-nix-nar"
	contentTypeText    = "text/plain; charset=utf-8"
)

// cachePriority is the Priority of nix-cache-info: lower is tried first, and
// cache.nixos.org has 40, so a private cache is asked before it.
const cachePriority = 30

// KeyProvider supplies the narinfo signing key of a repository.
type KeyProvider interface {
	// SecretKeyForRepo returns the key, or nil when none can be kept (no
	// OPENARTIFACT_SECRET_KEY): narinfo are then served without our signature.
	SecretKeyForRepo(ctx context.Context, repo *models.Repository) (*nix.SecretKey, error)
}

// HTTPStore is the part of the database HTTPHandler uses needs.
type HTTPStore interface {
	BlobReferenceCount(ctx context.Context, sha256 string) (int, error)
	CreateNixPath(ctx context.Context, p *models.NixPath) error
	DecrementStorageUsage(ctx context.Context, repoID string, sizeBytes int64, artifactCount int) error
	DeleteNixPath(ctx context.Context, repoID string, id string) error
	GetNixPathByHash(ctx context.Context, repoID string, storeHash string) (*models.NixPath, error)
	GetNixPathByNarFile(ctx context.Context, repoID string, narFile string) (*models.NixPath, error)
	GetNixPendingNar(ctx context.Context, repoID string, sha256 string) (*models.NixPendingNar, error)
	GetOrCreateStorageQuota(ctx context.Context, repoID string) (*models.StorageQuota, error)
	GetRawArtifact(ctx context.Context, repoID string, path string) (*models.RawArtifact, error)
	GetRepositoryByName(ctx context.Context, name string) (*models.Repository, error)
	IncrementNixDownloadCount(ctx context.Context, id string, repoID string) error
	PutNixPendingNar(ctx context.Context, n *models.NixPendingNar) error
	ReserveStorageUsage(ctx context.Context, repoID string, sizeBytes int64, artifactCount int) (bool, error)
	UpsertRawArtifact(ctx context.Context, artifact *models.RawArtifact) error
}

// HTTPHandler serves hosted Nix binary caches.
type HTTPHandler struct {
	*base.Handler

	keys    KeyProvider
	metrics *metrics.Registry
	DB      HTTPStore
}

// NewHTTPHandler creates the handler.
func NewHTTPHandler(log *logger.Logger, db HTTPStore, blobStore storage.BlobStore, baseURL string) *HTTPHandler {
	return &HTTPHandler{Handler: base.New(log, db, blobStore, baseURL), DB: db}
}

// WithMetrics counts a committed path (a narinfo PUT) as an upload.
func (h *HTTPHandler) WithMetrics(reg *metrics.Registry) *HTTPHandler {
	h.metrics = reg
	return h
}

// WithKeys sets the provider of the narinfo signing keys.
func (h *HTTPHandler) WithKeys(k KeyProvider) *HTTPHandler {
	h.keys = k
	return h
}

type routeKind int

const (
	routeCacheInfo routeKind = iota + 1
	routePublicKey
	routeNarInfo
	routeNar
	// routeAux are the listing and debug-info files "nix copy" writes with
	// write-nar-listing and index-debug-info.
	routeAux
)

// route is a request path the binary cache protocol defines. arg is the store
// hash of a narinfo and the file name of a NAR.
type route struct {
	kind routeKind
	arg  string
}

// classify maps the path below the repository to a route.
func classify(rel string) (route, bool) {
	rel = strings.TrimPrefix(rel, "/")

	switch rel {
	case "nix-cache-info":
		return route{kind: routeCacheInfo}, true
	case "public.key":
		return route{kind: routePublicKey}, true
	}

	if file, ok := strings.CutPrefix(rel, "nar/"); ok && file != "" && !strings.Contains(file, "/") {
		return route{kind: routeNar, arg: file}, true
	}

	if id, ok := strings.CutPrefix(rel, "debuginfo/"); ok && id != "" && !strings.Contains(id, "/") {
		return route{kind: routeAux}, true
	}

	if hash, ok := strings.CutSuffix(rel, ".narinfo"); ok && nix.ValidStoreHash(hash) {
		return route{kind: routeNarInfo, arg: hash}, true
	}

	if hash, ok := strings.CutSuffix(rel, ".ls"); ok && nix.ValidStoreHash(hash) {
		return route{kind: routeAux}, true
	}

	return route{}, false
}

// RegisterRepoRoutes registers the protocol on a router of hosted nix
// repositories (a group reads its members through it). The whole path below
// the repository is classified by serve, so the router needs no route per
// file; an unknown path is a 404.
func (h *HTTPHandler) RegisterRepoRoutes(r chi.Router) {
	r.HandleFunc("/*", func(w http.ResponseWriter, req *http.Request) {
		rt, ok := classify(chi.URLParam(req, "*"))
		if !ok {
			http.NotFound(w, req)
			return
		}

		h.serve(w, req, urlparams.Vars(req)["name"], rt)
	})
}

// Guard is chi middleware for the shared /repos/{name} subrouter. The nix
// paths (/<hash>.narinfo, /nar/<file>) have no place among the routes of the
// other formats, which register wildcard parameters at the same depth, so a
// request is claimed here when it names a nix path and {name} is a nix
// repository, and passed on untouched otherwise. Run it before the per-format
// routes.
func (h *HTTPHandler) Guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rctx := chi.RouteContext(r.Context())
		if rctx == nil {
			next.ServeHTTP(w, r)
			return
		}

		rt, ok := classify(rctx.RoutePath)
		if !ok {
			next.ServeHTTP(w, r)
			return
		}

		name := rctx.URLParam("name")

		repo, err := h.ResolveRepo(r.Context(), name)
		if err != nil || repo == nil || repo.Format != FormatName {
			next.ServeHTTP(w, r)
			return
		}

		h.serve(w, r, name, rt)
	})
}

// allowed returns the methods a route answers.
func (k routeKind) allowed() []string {
	switch k {
	case routeNarInfo:
		return []string{http.MethodGet, http.MethodHead, http.MethodPut, http.MethodDelete}
	case routeNar:
		return []string{http.MethodGet, http.MethodHead, http.MethodPut}
	case routeAux:
		return []string{http.MethodGet, http.MethodHead, http.MethodPut}
	default:
		return []string{http.MethodGet, http.MethodHead}
	}
}

func (h *HTTPHandler) serve(w http.ResponseWriter, r *http.Request, name string, rt route) {
	if allowed := rt.kind.allowed(); !slices.Contains(allowed, r.Method) {
		w.Header().Set("Allow", strings.Join(allowed, ", "))
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)

		return
	}

	repo, err := h.ResolveRepo(r.Context(), name)
	if err != nil {
		h.Log.Errorw("failed to resolve repo", "name", name, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if repo == nil || repo.Format != FormatName {
		http.NotFound(w, r)
		return
	}

	switch {
	case rt.kind == routeCacheInfo:
		ServeCacheInfo(w, r)
	case rt.kind == routePublicKey:
		ServePublicKey(w, r, h.Log, h.keys, repo)
	case rt.kind == routeNarInfo && r.Method == http.MethodPut:
		middleware.CountUploads(h.metrics, metrics.FormatNix, func(w http.ResponseWriter, r *http.Request) {
			h.putNarInfo(w, r, repo, rt.arg)
		})(w, r)
	case rt.kind == routeNarInfo && r.Method == http.MethodDelete:
		h.deletePath(w, r, repo, rt.arg)
	case rt.kind == routeNarInfo:
		h.serveNarInfo(w, r, repo, rt.arg)
	case rt.kind == routeNar && r.Method == http.MethodPut:
		h.putNar(w, r, repo, rt.arg)
	case rt.kind == routeNar:
		h.serveNar(w, r, repo, rt.arg)
	case r.Method == http.MethodPut:
		h.discardAux(w, r)
	default:
		// Listings and debug info are never kept.
		http.NotFound(w, r)
	}
}

// serveText writes a small fixed text document.
func serveText(w http.ResponseWriter, r *http.Request, body string) {
	w.Header().Set("Content-Type", contentTypeText)
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)

	if r.Method != http.MethodHead {
		_, _ = w.Write([]byte(body))
	}
}

// pathNarInfo builds the narinfo of a stored path, with the repository's
// signature added to the ones the uploader supplied.
func (h *HTTPHandler) pathNarInfo(ctx context.Context, repo *models.Repository, p *models.NixPath) (*nix.NarInfo, error) {
	fileHash, _ := hex.DecodeString(p.SHA256)

	var sum nix.SHA256

	copy(sum[:], fileHash)

	n := &nix.NarInfo{
		StorePath:   nix.StoreDir + "/" + p.StoreHash + "-" + p.Name,
		URL:         "nar/" + p.NarFile,
		Compression: p.Compression,
		FileHash:    sum.String(),
		FileSize:    p.SizeBytes,
		NarHash:     p.NarHash,
		NarSize:     p.NarSize,
		References:  p.References,
		Deriver:     p.Deriver,
		CA:          p.CA,
	}

	key, err := SigningKey(ctx, h.keys, repo)
	if err != nil {
		return nil, err
	}

	n.Sigs = slices.Clone(p.Signatures)

	if key != nil {
		if err = SignNarInfo(n, key); err != nil {
			return nil, err
		}
	}

	return n, nil
}

func (h *HTTPHandler) serveNarInfo(w http.ResponseWriter, r *http.Request, repo *models.Repository, hash string) {
	p, err := h.DB.GetNixPathByHash(r.Context(), repo.ID, hash)
	if err != nil {
		h.Log.Errorw("failed to get nix path", "hash", hash, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if p == nil {
		http.NotFound(w, r)
		return
	}

	if h.DenyIfQuarantined(w, r, repo.ID, quarantine.PathKeys(p.ID, hash+".narinfo", p.Name, r.URL.Path)...) {
		return
	}

	n, err := h.pathNarInfo(r.Context(), repo, p)
	if err != nil {
		h.Log.Errorw("failed to render narinfo", "hash", hash, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	WriteNarInfo(w, r, repo.Public, n.String())
}

func (h *HTTPHandler) serveNar(w http.ResponseWriter, r *http.Request, repo *models.Repository, file string) {
	if _, err := nix.ParseNarFile(file); err != nil {
		http.NotFound(w, r)
		return
	}

	p, err := h.DB.GetNixPathByNarFile(r.Context(), repo.ID, file)
	if err != nil {
		h.Log.Errorw("failed to get nix path by nar file", "file", file, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if p == nil {
		h.serveStagedNarHead(w, r, repo, file)
		return
	}

	if h.DenyIfQuarantined(w, r, repo.ID, quarantine.PathKeys(p.ID, "nar/"+file, p.Name, r.URL.Path)...) {
		return
	}

	if base.Revalidate(w, r, repo.Public, `"`+p.SHA256+`"`, narMaxAge) {
		return
	}

	if r.Method == http.MethodGet && h.TryPresign(w, r, presign.Blob{
		SHA256: p.BlobKey, Size: p.SizeBytes, Filename: file, ContentType: contentTypeNar, Repository: repo,
	}) {
		return
	}

	w.Header().Set("Content-Type", contentTypeNar)
	w.Header().Set("Content-Length", strconv.FormatInt(p.SizeBytes, 10))

	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}

	blob, err := h.BlobStore.Get(r.Context(), p.BlobKey)
	if err != nil {
		h.Log.Errorw("failed to get nix nar blob", "key", p.BlobKey, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}
	defer base.CloseLog(blob, h.Log)

	w.WriteHeader(http.StatusOK)

	if _, err := iocopy.Copy(w, blob); err != nil {
		h.Log.Warnw("failed to write nix nar", "file", file, "error", err)
	}

	if err := h.DB.IncrementNixDownloadCount(r.Context(), p.ID, repo.ID); err != nil {
		h.Log.Warnw("failed to count nix download", "id", p.ID, "error", err)
	}
}

// serveStagedNarHead answers the HEAD "nix copy" sends before uploading a NAR:
// a NAR that is staged awaiting its narinfo exists as far as the uploader is
// concerned. Nothing else is served from staging.
func (h *HTTPHandler) serveStagedNarHead(w http.ResponseWriter, r *http.Request, repo *models.Repository, file string) {
	sum, _ := nix.ParseNarFile(file)

	if r.Method == http.MethodHead {
		pending, err := h.DB.GetNixPendingNar(r.Context(), repo.ID, sum.Hex())
		if err != nil {
			h.Log.Errorw("failed to get staged nar", "file", file, "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)

			return
		}

		if pending != nil && pending.NarFile == file {
			w.Header().Set("Content-Length", strconv.FormatInt(pending.SizeBytes, 10))
			w.WriteHeader(http.StatusOK)

			return
		}
	}

	http.NotFound(w, r)
}
