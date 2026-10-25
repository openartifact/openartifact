// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"os"
	"strings"

	"github.com/openartifact/openartifact/internal/storage"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type componentStatus struct {
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

type healthResponse struct {
	Status     string                     `json:"status"`
	Version    string                     `json:"version"`
	InstanceID string                     `json:"instance_id,omitempty"`
	Components map[string]componentStatus `json:"components"`
}

// registerOperationalRoutes mounts /health, /metrics and /ready on the bare
// router: they sit outside authentication.
func (a *app) registerOperationalRoutes() {
	router := a.web.router

	router.Get("/health", a.handleHealth)

	metricsToken := os.Getenv("METRICS_AUTH_TOKEN")
	metricsAnonymous := os.Getenv("METRICS_ALLOW_ANONYMOUS") == "true"

	if metricsToken == "" && !metricsAnonymous {
		a.log.Warnw("/metrics is disabled: set METRICS_AUTH_TOKEN (or METRICS_ALLOW_ANONYMOUS=true to expose it without authentication)")
	}

	router.Method("GET", "/metrics", metricsAuthMiddleware(metricsToken, metricsAnonymous)(promhttp.Handler()))

	router.Get("/ready", a.handleReady)
}

// storageHealth checks the object store when it is S3-compatible; other
// backends have nothing to probe.
func (a *app) storageHealth(r *http.Request) error {
	if s3store, ok := a.store.raw.(*storage.S3BlobStore); ok {
		return s3store.HealthCheck(r.Context())
	}

	return nil
}

// handleHealth reports the state of the components. Health is not readiness:
// it answers 200 even when a component is degraded, and 503 only while the
// storage tiers are not loaded.
func (a *app) handleHealth(w http.ResponseWriter, r *http.Request) {
	components := make(map[string]componentStatus)

	dbStatus := componentStatus{Status: "ok"}
	if err := a.db.Ping(r.Context()); err != nil {
		dbStatus.Status = "error"
		a.log.Errorw("health: database ping failed", "error", err)
		dbStatus.Message = "unavailable"
	}

	components["database"] = dbStatus

	if !a.store.tiers.Ready() {
		writeJSONBody(w, http.StatusServiceUnavailable, `{"status":"not ready","reason":"storage tiers not loaded"}`)
		return
	}

	storageStatus := componentStatus{Status: "ok"}

	if err := a.storageHealth(r); err != nil {
		storageStatus.Status = "error"
		a.log.Errorw("health: storage check failed", "error", err)
		storageStatus.Message = "unavailable"
	}

	components["storage"] = storageStatus

	overallStatus := "healthy"

	for _, comp := range components {
		if comp.Status != "ok" {
			overallStatus = "degraded"
			break
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	resp := healthResponse{
		Status:  overallStatus,
		Version: version,
		// Only an operator-chosen name: the hostname and uptime are not
		// disclosed on this unauthenticated endpoint.
		InstanceID: a.cfg.Server.InstanceID,
		Components: components,
	}
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		a.log.Warnw("health: writing the response failed", "error", err)
	}
}

// handleReady answers 200 once the database and the object store answer.
func (a *app) handleReady(w http.ResponseWriter, r *http.Request) {
	if err := a.db.Ping(r.Context()); err != nil {
		writeJSONBody(w, http.StatusServiceUnavailable, `{"status":"not ready","reason":"database unavailable"}`)
		return
	}

	if err := a.storageHealth(r); err != nil {
		writeJSONBody(w, http.StatusServiceUnavailable, `{"status":"not ready","reason":"storage unavailable"}`)
		return
	}

	writeJSONBody(w, http.StatusOK, `{"status":"ready"}`)
}

// writeJSONBody writes a fixed JSON document.
func writeJSONBody(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

// metricsAuthMiddleware fails closed: with no token configured the endpoint
// answers 401 unless anonymous access was explicitly enabled.
func metricsAuthMiddleware(token string, allowAnonymous bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if token == "" {
				if allowAnonymous {
					next.ServeHTTP(w, r)
					return
				}

				w.WriteHeader(http.StatusUnauthorized)

				return
			}

			authHeader := r.Header.Get("Authorization")
			if !strings.HasPrefix(authHeader, "Bearer ") ||
				subtle.ConstantTimeCompare([]byte(authHeader[len("Bearer "):]), []byte(token)) != 1 {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
