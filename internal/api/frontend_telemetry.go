// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/metrics"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/internal/telemetry"
	"github.com/openartifact/openartifact/pkg/logger"
)

const (
	// frontendTelemetryMaxBody caps one relayed export request. The web UI
	// batches far below it.
	frontendTelemetryMaxBody = 1 << 20
	// frontendTelemetryMaxResponse caps the collector answer copied back (an
	// OTLP export response: at most a partial-success message).
	frontendTelemetryMaxResponse = 64 << 10
	// frontendTelemetryTimeout bounds one forward, so a stuck collector holds
	// no handler for long.
	frontendTelemetryTimeout = 10 * time.Second
	// frontendTelemetryMaxInflight caps the forwards in progress. Each holds up
	// to frontendTelemetryMaxBody in memory for up to frontendTelemetryTimeout,
	// so a hung collector cannot pile them up; the rate limit is per client.
	frontendTelemetryMaxInflight = 32
	// frontendTelemetryRate and frontendTelemetryBurst are the per-user token
	// bucket of the relay: requests per second, and how many may come at once.
	// The UI exports a batch of at most 64 records every few seconds per
	// signal, so a page that makes hundreds of API calls stays well inside.
	frontendTelemetryRate  = 2
	frontendTelemetryBurst = 20

	otlpProtobuf = "application/x-protobuf"
)

var errCollectorStatus = errors.New("collector answered")

// Results of the relay counter.
const (
	relayRelayed  = "relayed"
	relayRejected = "rejected"
	relayFailed   = "failed"
)

// FrontendTelemetryHandler serves the experimental browser telemetry: the
// config endpoint the web UI reads before loading the SDK, and the same-origin
// relay that validates its OTLP/HTTP export requests (protobuf that parses, ids
// of the right size, bounded record, attribute and string sizes, a fixed
// resource, a rate limit per user) and forwards the bounded re-encoding to the
// collector. Both answer 404 while the feature is off. Redaction of what the
// browser puts in the values stays the collector's job.
type FrontendTelemetryHandler struct {
	cfg      telemetry.Config
	client   *http.Client
	reg      *metrics.Registry
	log      *logger.Logger
	inflight chan struct{}
	limit    *userLimiter
}

// count increments the relay counter (a no-op without a metrics registry).
func (h *FrontendTelemetryHandler) count(signal, result string) {
	if h.reg != nil {
		h.reg.FrontendTelemetryRequestsTotal.WithLabelValues(signal, result).Inc()
	}
}

// NewFrontendTelemetryHandler creates the handler for cfg; reg may be nil. Its
// client refuses redirects: one would replay the body and the otlp.headers
// (collector credentials) to wherever it points. It is deliberately not the
// SSRF-safe client of internal/netutil: the collector is operator
// configuration and usually internal (localhost, a sidecar, a compose service).
func NewFrontendTelemetryHandler(cfg telemetry.Config, reg *metrics.Registry, log *logger.Logger) *FrontendTelemetryHandler {
	return &FrontendTelemetryHandler{
		cfg: cfg,
		client: &http.Client{
			Timeout:       frontendTelemetryTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		reg:      reg,
		log:      log,
		inflight: make(chan struct{}, frontendTelemetryMaxInflight),
		limit:    newUserLimiter(frontendTelemetryRate, frontendTelemetryBurst),
	}
}

// RegisterAuthed registers the routes on the authenticated router. RBAC lets
// sessions reach them (isSelfServiceRequest); the relay itself refuses every
// other credential.
func (h *FrontendTelemetryHandler) RegisterAuthed(r chi.Router) {
	r.Get("/api/v1/telemetry/frontend", h.handleConfig)
	r.With(h.countResult, middleware.MaxBodyMiddleware(frontendTelemetryMaxBody)).
		Post("/api/v1/telemetry/{signal}", h.handleRelay)
}

// frontendTelemetryConfig is what the web UI needs to start the SDK.
type frontendTelemetryConfig struct {
	Enabled bool `json:"enabled"`
	// SampleRatio is telemetry.otlp.sampleRatio: the UI samples its root
	// spans with it, so its traceparent cannot bypass server sampling.
	SampleRatio float64 `json:"sample_ratio"`
}

func (h *FrontendTelemetryHandler) handleConfig(w http.ResponseWriter, _ *http.Request) {
	if !h.cfg.FrontendActive() {
		writeError(w, http.StatusNotFound, "not_found", "not found")
		return
	}

	writeJSON(w, http.StatusOK, frontendTelemetryConfig{Enabled: true, SampleRatio: h.cfg.OTLP.SampleRatio})
}

// relaySignal reports whether signal is one the relay forwards.
func relaySignal(signal string) bool { return signal == "traces" || signal == "logs" }

func (h *FrontendTelemetryHandler) handleRelay(w http.ResponseWriter, r *http.Request) {
	signal := chi.URLParam(r, "signal")
	if !h.cfg.FrontendActive() || !relaySignal(signal) {
		writeError(w, http.StatusNotFound, "not_found", "not found")
		return
	}

	// Browser sessions only: API tokens (deploy tokens included), service
	// accounts and the legacy key have no business sending UI telemetry.
	p := currentPrincipal(r)
	if p == nil || p.IsAnonymous {
		writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}

	if p.IsToken() || p.Credential != auth.CredentialSession || p.UserID == nil {
		writeError(w, http.StatusForbidden, "forbidden", "telemetry is accepted from a browser session only")
		return
	}

	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != otlpProtobuf ||
		r.Header.Get("Content-Encoding") != "" {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "expected uncompressed "+otlpProtobuf)
		return
	}

	if !h.limit.allow(*p.UserID) {
		w.Header().Set("Retry-After", "1")
		writeError(w, http.StatusTooManyRequests, "rate_limited", "telemetry rate limit exceeded")

		return
	}

	select {
	case h.inflight <- struct{}{}:
		defer func() { <-h.inflight }()
	default:
		w.Header().Set("Retry-After", "5")
		writeError(w, http.StatusServiceUnavailable, "relay_busy", "telemetry relay busy")

		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		if _, tooLarge := errors.AsType[*http.MaxBytesError](err); tooLarge {
			writeError(w, http.StatusRequestEntityTooLarge, "payload_too_large", "request body too large")
			return
		}

		writeError(w, http.StatusBadRequest, "bad_request", "invalid request body")

		return
	}

	body, err = sanitizeOTLP(signal, body)
	if err != nil {
		if h.log != nil {
			h.log.Debugw("frontend telemetry payload refused", "signal", signal, "error", err)
		}

		writeError(w, http.StatusBadRequest, "bad_request", "invalid OTLP payload")

		return
	}

	h.forward(w, r, signal, body)
}

// forward sends body (already sanitized) to the collector and answers with its verdict: 200 and
// the export response when accepted, 400 when the collector refused the
// payload, 502 for everything else (unreachable, overloaded, misconfigured).
func (h *FrontendTelemetryHandler) forward(w http.ResponseWriter, r *http.Request, signal string, body []byte) {
	// The target is operator configuration (telemetry.frontend.endpoint or
	// telemetry.otlp.endpoint), never request input: only the signal, checked
	// against relaySignal, is appended.
	//nolint:gosec // G704: see above
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, h.cfg.FrontendURL(signal), bytes.NewReader(body))
	if err != nil {
		h.badGateway(w, signal, err)
		return
	}

	req.Header.Set("Content-Type", otlpProtobuf)

	for k, v := range h.cfg.OTLP.Headers {
		req.Header.Set(k, v)
	}

	resp, err := h.client.Do(req) //nolint:gosec // G704: operator-configured collector URL, see above
	if err != nil {
		h.badGateway(w, signal, err)
		return
	}

	defer func() { _ = resp.Body.Close() }()

	switch {
	case resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices:
		out, err := io.ReadAll(io.LimitReader(resp.Body, frontendTelemetryMaxResponse))
		if err != nil {
			h.badGateway(w, signal, err)
			return
		}

		w.Header().Set("Content-Type", otlpProtobuf)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(out)
	case resp.StatusCode == http.StatusBadRequest:
		writeError(w, http.StatusBadRequest, "bad_request", "the collector rejected the payload")
	default:
		h.badGateway(w, signal, fmt.Errorf("%w: %s", errCollectorStatus, resp.Status))
	}
}

func (h *FrontendTelemetryHandler) badGateway(w http.ResponseWriter, signal string, err error) {
	if h.log != nil {
		h.log.Debugw("frontend telemetry relay failed", "signal", signal, "error", err)
	}

	writeError(w, http.StatusBadGateway, "bad_gateway", "telemetry collector unavailable")
}

// countResult counts every relay answer for a known signal by its status:
// 2xx relayed, 502 failed, any other refusal (401, 403, 413, 415, 400, 429,
// 503) rejected. 404s (feature off, unknown signal) are not counted, so the signal
// label stays bounded. It sits outside MaxBodyMiddleware to see its 413s too.
func (h *FrontendTelemetryHandler) countResult(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ww := chimw.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)

		signal, status := chi.URLParam(r, "signal"), ww.Status()
		if !relaySignal(signal) || status == http.StatusNotFound || !h.cfg.FrontendActive() {
			return
		}

		switch {
		case status >= http.StatusOK && status < http.StatusMultipleChoices:
			h.count(signal, relayRelayed)
		case status == http.StatusBadGateway:
			h.count(signal, relayFailed)
		default:
			h.count(signal, relayRejected)
		}
	})
}
