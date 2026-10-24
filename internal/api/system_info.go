// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"net/http"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/storage"
)

var releaseVersionRe = regexp.MustCompile(`^v?(\d+)\.(\d+)\.\d+$`)

const (
	// healthCacheTTL bounds how often the DB and storage are probed, so
	// unauthenticated callers cannot amplify load.
	healthCacheTTL = 10 * time.Second
	// healthProbeTimeout bounds a single probe round.
	healthProbeTimeout = 3 * time.Second
)

type SystemInfoHandler struct {
	version     string
	commit      string
	buildTime   string
	startTime   time.Time
	db          database.Database
	blobStore   storage.BlobStore
	storageType string
	docsURL     string

	mu            sync.Mutex
	checkedAt     time.Time
	dbStatus      string
	storageStatus string
}

func NewSystemInfoHandler(version, commit, buildTime string, db database.Database, blobStore storage.BlobStore, storageType string) *SystemInfoHandler {
	return &SystemInfoHandler{
		version:     version,
		commit:      commit,
		buildTime:   buildTime,
		startTime:   time.Now(),
		db:          db,
		blobStore:   blobStore,
		storageType: storageType,
	}
}

// WithDocsBaseURL sets the documentation site root the public info endpoint
// advertises as docs_url, resolved against the server version.
func (h *SystemInfoHandler) WithDocsBaseURL(base string) *SystemInfoHandler {
	h.docsURL = docsURL(base, h.version)

	return h
}

// docsURL returns base (normalised to one trailing slash) followed by vX.Y/
// when version is a plain MAJOR.MINOR.PATCH release (optional leading "v"),
// else latest/. An empty base yields "".
func docsURL(base, version string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		return ""
	}

	base = strings.TrimRight(base, "/") + "/"

	if m := releaseVersionRe.FindStringSubmatch(version); m != nil {
		return base + "v" + m[1] + "." + m[2] + "/"
	}

	return base + "latest/"
}

// RegisterPublic registers the unauthenticated, trimmed endpoint: version and
// an overall status only.
func (h *SystemInfoHandler) RegisterPublic(r chi.Router) {
	r.Get("/api/v1/system/info", h.ServeHTTP)
}

// RegisterAuthed registers the detailed endpoint (behind the admin RBAC check).
func (h *SystemInfoHandler) RegisterAuthed(r chi.Router) {
	r.Get("/api/v1/admin/system/info", h.handleDetails)
}

// health returns the DB and storage status, probing at most once per
// healthCacheTTL.
func (h *SystemInfoHandler) health() (string, string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if !h.checkedAt.IsZero() && time.Since(h.checkedAt) < healthCacheTTL {
		return h.dbStatus, h.storageStatus
	}

	ctx, cancel := context.WithTimeout(context.Background(), healthProbeTimeout)
	defer cancel()

	h.dbStatus = "ok"

	if h.db != nil {
		if err := h.db.Ping(ctx); err != nil {
			h.dbStatus = "error"
		}
	}

	h.storageStatus = "ok"

	type healthChecker interface {
		HealthCheck(context.Context) error
	}

	if h.blobStore != nil {
		if hc, ok := h.blobStore.(healthChecker); ok {
			if err := hc.HealthCheck(ctx); err != nil {
				h.storageStatus = "error"
			}
		}
	}

	h.checkedAt = time.Now()

	return h.dbStatus, h.storageStatus
}

func (h *SystemInfoHandler) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	dbStatus, storageStatus := h.health()

	status := "ok"
	if dbStatus != "ok" || storageStatus != "ok" {
		status = "degraded"
	}

	body := map[string]any{
		"version": h.version,
		"status":  status,
	}
	if h.docsURL != "" {
		body["docs_url"] = h.docsURL
	}

	writeJSON(w, http.StatusOK, body)
}

func (h *SystemInfoHandler) handleDetails(w http.ResponseWriter, _ *http.Request) {
	dbStatus, storageStatus := h.health()

	writeJSON(w, http.StatusOK, map[string]any{
		"version":        h.version,
		"commit":         h.commit,
		"build_time":     h.buildTime,
		"go_version":     runtime.Version(),
		"os":             runtime.GOOS,
		"arch":           runtime.GOARCH,
		"uptime":         time.Since(h.startTime).Round(time.Second).String(),
		"db_status":      dbStatus,
		"storage_kind":   h.storageType,
		"storage_status": storageStatus,
	})
}
