// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/api/responses"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/logger"
)

const (
	// primaryStorageName is the reserved name of the configured primary store
	// in the /api/v1/admin/storage/{name} paths.
	primaryStorageName = "primary"

	// primaryHealthTTL bounds how often the primary store is probed by reads of
	// the list; the test endpoint always probes.
	primaryHealthTTL = 10 * time.Second
	// primaryProbeTimeout bounds a probe made on behalf of a read.
	primaryProbeTimeout = 3 * time.Second

	statusHealthy     = "healthy"
	statusUnreachable = "unreachable"
	statusUnknown     = "unknown"
)

// primaryStore is the blob store the server is configured with (storage.*),
// as the storage admin API presents it: read-only, with a cached live health
// check and without any credential.
type primaryStore struct {
	cfg   storage.StorageConfig
	store storage.BlobStore
	log   *logger.Logger

	mu        sync.Mutex
	checkedAt time.Time
	result    storageTestResult
	status    string
}

// WithPrimary makes the configured primary store (cfg, opened as store) show up
// in the storage admin API. Only the non-secret location is ever exposed.
func (h *AdminStorageHandler) WithPrimary(cfg storage.StorageConfig, store storage.BlobStore) *AdminStorageHandler {
	h.primary = &primaryStore{cfg: cfg, store: store, log: h.log}

	return h
}

func (p *primaryStore) kind() string {
	if p.cfg.Type == "" {
		return "filesystem"
	}

	return p.cfg.Type
}

// location returns where the blobs live. Credentials (access and secret keys,
// HMAC and account keys, OCI password) are never part of it, and a URL's
// userinfo, query and fragment are dropped.
func (p *primaryStore) location() map[string]string {
	loc := map[string]string{}
	put := func(key, value string) {
		if value != "" {
			loc[key] = value
		}
	}

	switch p.kind() {
	case "filesystem":
		put("path", p.cfg.Path)
	case "s3", "garage", "seaweedfs":
		put("endpoint", publicEndpoint(p.cfg.Endpoint))
		put("bucket", p.cfg.Bucket)
		put("region", p.cfg.Region)
	case "gcs":
		put("bucket", p.cfg.GCSBucket)
	case "azure":
		put("account", p.cfg.AzureAccountName)
		put("container", p.cfg.AzureContainerName)
		put("endpoint", publicEndpoint(p.cfg.AzureEndpoint))
	case "oci":
		put("endpoint", publicEndpoint(p.cfg.Endpoint))
		put("repository", p.cfg.Bucket)
	}

	return loc
}

// publicEndpoint strips userinfo, query and fragment from an endpoint URL; a
// value that is not a URL is dropped rather than echoed.
func publicEndpoint(raw string) string {
	if raw == "" {
		return ""
	}

	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}

	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""

	return u.String()
}

// health returns the store's health: the cached probe while it is younger than
// primaryHealthTTL, else a fresh one. force always probes, with the longer
// test timeout. Probes are serialized, so concurrent readers share one.
func (p *primaryStore) health(ctx context.Context, force bool) (string, storageTestResult, time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !force && !p.checkedAt.IsZero() && time.Since(p.checkedAt) < primaryHealthTTL {
		return p.status, p.result, p.checkedAt
	}

	timeout := primaryProbeTimeout
	if force {
		timeout = storageTestTimeout
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	p.status, p.result = p.probe(ctx)
	p.checkedAt = time.Now()

	return p.status, p.result, p.checkedAt
}

func (p *primaryStore) probe(ctx context.Context) (string, storageTestResult) {
	hc, ok := p.store.(interface{ HealthCheck(context.Context) error })
	if !ok {
		return statusUnknown, storageTestResult{false, "not_supported", "the primary storage has no health check"}
	}

	if err := hc.HealthCheck(ctx); err != nil {
		p.log.Warnw("primary storage health check failed", "kind", p.kind(), "error", err)

		return statusUnreachable, storageTestResult{
			false, "connection_failed", "could not access the primary storage with the configured settings",
		}
	}

	return statusHealthy, storageTestResult{ok: true}
}

// response is the API form of the primary store. force re-probes it.
func (p *primaryStore) response(ctx context.Context, usage *responses.StorageBackendUsage, force bool) responses.StoragePrimaryResponse {
	status, result, checked := p.health(ctx, force)

	resp := responses.StoragePrimaryResponse{
		Name:          primaryStorageName,
		Kind:          p.kind(),
		Source:        "config",
		Location:      p.location(),
		Status:        status,
		LastCheckedAt: formatTimePtr(&checked),
		Usage:         usage,
	}

	if status == statusUnreachable {
		msg := result.message
		resp.StatusMessage = &msg
	}

	return resp
}

func (h *AdminStorageHandler) handleGetPrimary(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}

	if h.primary == nil {
		writeError(w, http.StatusNotFound, "not_found", "the primary storage is not available")
		return
	}

	usage, err := h.backendUsage(r.Context())
	if err != nil {
		h.log.Errorw("failed to read storage backend usage", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to get the primary storage")

		return
	}

	writeJSON(w, http.StatusOK, h.primary.response(r.Context(), usage.of(""), false))
}

// handleTestPrimary probes the primary store now, through the process's own
// store (the one that serves every request), and refreshes the cached status.
func (h *AdminStorageHandler) handleTestPrimary(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}

	if h.primary == nil {
		writeError(w, http.StatusNotFound, "not_found", "the primary storage is not available")
		return
	}

	_, result, _ := h.primary.health(r.Context(), true)

	response := map[string]any{"ok": result.ok}
	if !result.ok {
		response["error"] = result.code
		response["message"] = result.message
	}

	writeJSON(w, http.StatusOK, response)
}

// primaryWrite serves PATCH and DELETE of /primary. The store itself is read-only,
// but a record that already carries the name (created before it was reserved)
// must stay renamable and deletable: its writes go to record, which acts on it
// as it would on /{name}. Without such a record the answer is the 405.
func (h *AdminStorageHandler) primaryWrite(record http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !h.requireAdmin(w, r) {
			return
		}

		legacy, err := h.db.GetStorageBackend(r.Context(), primaryStorageName)
		if err != nil {
			h.log.Errorw("failed to get storage backend", "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "failed to get storage backend")

			return
		}

		rctx := chi.RouteContext(r.Context())
		if legacy == nil || rctx == nil {
			h.handlePrimaryReadOnly(w, r)
			return
		}

		rctx.URLParams.Add("name", primaryStorageName)
		record(w, r)
	}
}

// handlePrimaryReadOnly answers the writes the primary store does not accept.
func (h *AdminStorageHandler) handlePrimaryReadOnly(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}

	w.Header().Set("Allow", http.MethodGet)
	writeError(w, http.StatusMethodNotAllowed, "read_only",
		"the primary storage is set by the server configuration (storage.*) and cannot be changed here")
}
