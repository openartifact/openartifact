// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/openartifact/openartifact/internal/crypto"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	"github.com/openartifact/openartifact/internal/netutil"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/logger"
)

// maxUpstreamBody caps any single upstream response copied through the proxy.
const maxUpstreamBody = 4 << 30 // 4 GiB

// maxUpstreamErrorBody caps upstream error bodies relayed to clients.
const maxUpstreamErrorBody = 1 << 20

type Handler struct {
	log       *logger.Logger
	db        database.Database
	blobStore storage.BlobStore
	client    *http.Client
}

func NewHandler(log *logger.Logger, db database.Database, blobStore storage.BlobStore) *Handler {
	return &Handler{
		log:       log,
		db:        db,
		blobStore: blobStore,
		client:    netutil.SafeHTTPClient(5 * time.Minute),
	}
}

// ServeProxy handles a proxied request for the given repo.
// cacheKey is a stable string identifying the resource (e.g. "blob:sha256:abc" or "manifest:nginx:latest")
// upstreamPath is the path to append to repo.UpstreamURL.
//
//nolint:gocyclo,cyclop // protocol handler: branching mirrors spec state machine
func (h *Handler) ServeProxy(w http.ResponseWriter, r *http.Request, repo *models.Repository, cacheKey, upstreamPath string) {
	ctx := r.Context()

	// Check if repo is blocked
	if repo.Blocked {
		http.Error(w, "repository is blocked", http.StatusForbidden)
		return
	}

	// Try to get from cache
	cacheEntry, err := h.db.GetProxyCacheEntry(ctx, repo.ID, cacheKey)
	if err != nil {
		h.log.Errorw("failed to get proxy cache entry", "repo_id", repo.ID, "cache_key", cacheKey, "error", err)
	}

	// Cache HIT: serve from blobStore
	if cacheEntry != nil && cacheEntry.ExpiresAt.After(time.Now()) {
		h.log.Debugw("proxy cache hit", "repo_id", repo.ID, "cache_key", cacheKey)

		// Negative cache entry: upstream said 404 and no blob exists.
		if cacheEntry.StatusCode == http.StatusNotFound || cacheEntry.BlobKey == "" {
			http.NotFound(w, r)
			return
		}

		// Serve from blob store
		blob, err := h.blobStore.Get(ctx, cacheEntry.BlobKey)
		if err != nil {
			h.log.Errorw("failed to get blob from store", "blob_key", cacheEntry.BlobKey, "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)

			return
		}
		defer func() { base.CloseLog(blob, h.log) }()

		// Update hit count and last_hit_at
		now := time.Now()
		cacheEntry.HitCount++

		cacheEntry.LastHitAt = &now
		if err := h.db.UpsertProxyCacheEntry(ctx, cacheEntry); err != nil {
			h.log.Errorw("failed to update proxy cache entry", "cache_key", cacheKey, "error", err)
		}

		// Write response headers
		if cacheEntry.ContentType != "" {
			w.Header().Set("Content-Type", cacheEntry.ContentType)
		}

		if cacheEntry.SizeBytes > 0 {
			w.Header().Set("Content-Length", strconv.FormatInt(cacheEntry.SizeBytes, 10))
		}

		w.WriteHeader(http.StatusOK)

		// Stream blob to client
		if _, err := io.Copy(w, blob); err != nil {
			h.log.Errorw("failed to write blob to response", "cache_key", cacheKey, "error", err)
		}

		return
	}

	// Cache MISS: fetch from upstream
	h.log.Debugw("proxy cache miss", "repo_id", repo.ID, "cache_key", cacheKey)

	upstreamURL := repo.UpstreamURL + upstreamPath

	// SSRF protection is enforced at dial time by the client (blocked
	// destination addresses, re-checked on every redirect hop).

	req, err := http.NewRequestWithContext(ctx, r.Method, upstreamURL, nil)
	if err != nil {
		h.log.Errorw("failed to create upstream request", "url", upstreamURL, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	// Copy relevant headers from original request
	for _, header := range []string{"Accept", "User-Agent"} {
		if val := r.Header.Get(header); val != "" {
			req.Header.Set(header, val)
		}
	}

	// Add basic auth if configured
	if repo.UpstreamUsername != "" {
		pw, derr := crypto.Decrypt(repo.UpstreamPasswordEnc)
		if derr != nil {
			h.log.Errorw("failed to decrypt upstream password", "repo_id", repo.ID, "error", derr)
			http.Error(w, "internal error", http.StatusInternalServerError)

			return
		}
		req.SetBasicAuth(repo.UpstreamUsername, pw)
	}

	// Fetch from upstream
	//nolint:gosec // G704: upstream URL is admin-configured at startup, not user-controlled
	resp, err := h.client.Do(req)
	if err != nil {
		h.log.Errorw("failed to fetch from upstream", "url", upstreamURL, "error", err)
		http.Error(w, "upstream error", http.StatusBadGateway)

		return
	}
	defer func() { base.CloseLog(resp.Body, h.log) }()

	// Handle upstream 404: store negative cache entry
	if resp.StatusCode == http.StatusNotFound {
		h.log.Debugw("upstream returned 404", "url", upstreamURL)

		expiresAt := time.Now().Add(time.Duration(repo.NegativeCacheTTLSeconds) * time.Second)

		negEntry := &models.ProxyCacheEntry{
			RepositoryID: repo.ID,
			CacheKey:     cacheKey,
			StatusCode:   http.StatusNotFound,
			ExpiresAt:    expiresAt,
			CreatedAt:    time.Now(),
			UpdatedAt:    time.Now(),
		}
		if err := h.db.UpsertProxyCacheEntry(ctx, negEntry); err != nil {
			h.log.Errorw("failed to store negative cache entry", "cache_key", cacheKey, "error", err)
		}

		http.NotFound(w, r)

		return
	}

	// Handle other upstream errors
	if resp.StatusCode >= 400 {
		h.log.Warnw("upstream returned error", "url", upstreamURL, "status", resp.StatusCode)
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, io.LimitReader(resp.Body, maxUpstreamErrorBody))

		return
	}

	contentType := resp.Header.Get("Content-Type")
	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}

	// Copy other relevant headers
	for _, header := range []string{"Content-Length", "ETag", "Last-Modified", "Docker-Content-Digest", "Cache-Control"} {
		if val := resp.Header.Get(header); val != "" {
			w.Header().Set(header, val)
		}
	}

	if resp.ContentLength > maxUpstreamBody {
		http.Error(w, "upstream response too large", http.StatusBadGateway)
		return
	}

	w.WriteHeader(http.StatusOK)

	body := io.LimitReader(resp.Body, maxUpstreamBody+1)

	spool, err := os.CreateTemp("", "openartifact-proxy-*")
	if err != nil {
		h.log.Errorw("failed to create spool file", "cache_key", cacheKey, "error", err)
		_, _ = io.Copy(w, body)
		return
	}
	defer func() {
		base.CloseLog(spool, h.log)
		if err := os.Remove(spool.Name()); err != nil {
			h.log.Errorw("failed to remove spool file", "cache_key", cacheKey, "error", err)
		}
	}()

	hasher := sha256.New()
	sizeBytes, err := io.Copy(io.MultiWriter(w, spool, hasher), body)
	if err != nil {
		h.log.Errorw("failed to stream response", "cache_key", cacheKey, "error", err)
		return
	}
	if sizeBytes > maxUpstreamBody || (resp.ContentLength >= 0 && sizeBytes != resp.ContentLength) {
		h.log.Warnw("upstream body oversized or truncated; not caching", "cache_key", cacheKey, "size", sizeBytes)
		return
	}

	sha256Hex := hex.EncodeToString(hasher.Sum(nil))
	if _, err := spool.Seek(0, io.SeekStart); err != nil {
		h.log.Errorw("failed to rewind spool file", "cache_key", cacheKey, "error", err)
		return
	}

	// Content-addressed Put: the blob store uses metadata["sha256"] as the
	// blob's identifier, so the id it returns matches what a later cache HIT
	// will Get() by (we store that same value as cacheEntry.BlobKey below,
	// decoupled from cacheKey — nothing derived from client-controlled input
	// is used to build storage keys or paths).
	blobID, err := h.blobStore.Put(ctx, spool, map[string]string{
		"sha256":       sha256Hex,
		"content-type": contentType,
	})
	if err != nil {
		h.log.Errorw("failed to store blob", "cache_key", cacheKey, "sha256", sha256Hex, "error", err)
		// Client already received the response; skip caching rather than
		// persisting a cache entry that points at a blob we never wrote.
		return
	}

	// Store cache entry
	expiresAt := time.Now().Add(time.Duration(repo.CacheTTLSeconds) * time.Second)

	cacheEntry = &models.ProxyCacheEntry{
		RepositoryID: repo.ID,
		CacheKey:     cacheKey,
		BlobKey:      blobID,
		ContentType:  contentType,
		StatusCode:   http.StatusOK,
		SizeBytes:    sizeBytes,
		HitCount:     0,
		ExpiresAt:    expiresAt,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	if err := h.db.UpsertProxyCacheEntry(ctx, cacheEntry); err != nil {
		h.log.Errorw("failed to store cache entry", "cache_key", cacheKey, "error", err)
	}
}
