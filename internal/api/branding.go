// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/branding"
	"github.com/openartifact/openartifact/internal/httpcache"
	"github.com/openartifact/openartifact/pkg/logger"
)

const (
	// brandingMaxAge is how long a shared cache may serve branding to
	// anonymous callers without revalidating.
	brandingMaxAge = 60
	// maxBrandingBody caps the JSON body of PUT /api/v1/admin/branding.
	maxBrandingBody = 64 << 10
	// brandingImageCSP confines an SVG opened directly (not through <img>):
	// no script, no network, inline styles only.
	brandingImageCSP = "default-src 'none'; style-src 'unsafe-inline'"
)

// BrandingHandler serves the public white-label settings and lets global
// administrators change them at runtime.
type BrandingHandler struct {
	mgr       *branding.Manager
	log       *logger.Logger
	evaluator *auth.Evaluator
}

// NewBrandingHandler returns the branding handler.
func NewBrandingHandler(mgr *branding.Manager, log *logger.Logger, evaluator *auth.Evaluator) *BrandingHandler {
	return &BrandingHandler{mgr: mgr, log: log, evaluator: evaluator}
}

// RegisterPublic registers the unauthenticated endpoints on the bare router:
// the login page needs them before anyone is signed in.
func (h *BrandingHandler) RegisterPublic(r chi.Router) {
	r.Get("/api/v1/branding", h.handleGet)
	r.Get("/api/v1/branding/{asset}", h.handleImage)
}

// RegisterAuthed registers the admin endpoints behind the RBAC middleware.
func (h *BrandingHandler) RegisterAuthed(r chi.Router) {
	r.Get("/api/v1/admin/branding", h.handleAdminGet)
	r.Put("/api/v1/admin/branding", h.handleAdminPut)
	r.Delete("/api/v1/admin/branding", h.handleAdminReset)
	r.Put("/api/v1/admin/branding/{asset}", h.handleAdminPutImage)
	r.Delete("/api/v1/admin/branding/{asset}", h.handleAdminDeleteImage)
}

// anonymousRequest reports whether r carries no credentials at all. These
// routes sit outside RBAC, so there is no resolved principal: a request with an
// Authorization header or any cookie is treated as possibly authenticated and
// kept out of shared caches.
func anonymousRequest(r *http.Request) bool {
	return r.Header.Get("Authorization") == "" && r.Header.Get("Cookie") == ""
}

// revalidateBranding sets the validator and cache headers and answers 304 when
// the client already holds etag. It reports whether it wrote the response.
func revalidateBranding(w http.ResponseWriter, r *http.Request, etag string) bool {
	hd := w.Header()
	hd.Set("ETag", etag)
	hd.Set("Cache-Control", httpcache.CacheControl(anonymousRequest(r), brandingMaxAge))
	hd.Add("Vary", "Authorization")
	hd.Add("Vary", "Cookie")

	if !httpcache.ETagMatches(r.Header.Get("If-None-Match"), etag) {
		return false
	}

	w.WriteHeader(http.StatusNotModified)

	return true
}

func (h *BrandingHandler) handleGet(w http.ResponseWriter, r *http.Request) {
	snap := h.mgr.Snapshot(r.Context())

	if revalidateBranding(w, r, snap.ETag) {
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(snap.JSON)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(snap.JSON)
}

func (h *BrandingHandler) handleImage(w http.ResponseWriter, r *http.Request) {
	kind, ok := branding.ParseKind(chi.URLParam(r, "asset"))
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "unknown branding image")
		return
	}

	img, ok := h.mgr.Snapshot(r.Context()).Image(kind)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "no image configured")
		return
	}

	// The restrictive headers matter when the SVG is opened as a document
	// (or an attacker links to it): it cannot run script or reach the network.
	hd := w.Header()
	hd.Set("Content-Security-Policy", brandingImageCSP)
	hd.Set("X-Content-Type-Options", "nosniff")
	hd.Set("Content-Type", img.ContentType)

	if revalidateBranding(w, r, `"`+img.SHA256[:24]+`"`) {
		return
	}

	hd.Set("Content-Length", strconv.Itoa(len(img.Data)))
	w.WriteHeader(http.StatusOK)
	//nolint:gosec // G705: a sanitised image served with a locked-down CSP and nosniff, not HTML
	_, _ = w.Write(img.Data)
}

func (h *BrandingHandler) writeAdmin(w http.ResponseWriter, snap *branding.Snapshot) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, snap.Admin)
}

func (h *BrandingHandler) handleAdminGet(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	h.writeAdmin(w, h.mgr.Snapshot(r.Context()))
}

// brandingErr maps a manager error to a response: validation failures are the
// administrator's to fix (400, or 413 for an oversized image); anything else is
// logged and answered generically.
func (h *BrandingHandler) brandingErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, branding.ErrTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, "payload_too_large", userMessage(err))
	case errors.Is(err, branding.ErrInvalid):
		writeError(w, http.StatusBadRequest, "validation_failed", userMessage(err))
	default:
		h.log.Errorw("branding update failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "could not save branding")
	}
}

func userMessage(err error) string {
	return strings.ReplaceAll(err.Error(), branding.ErrInvalid.Error()+": ", "")
}

func (h *BrandingHandler) handleAdminPut(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	var u branding.Update

	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBrandingBody))
	dec.DisallowUnknownFields()

	if err := dec.Decode(&u); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid request body")
		return
	}

	snap, err := h.mgr.Update(r.Context(), u)
	if err != nil {
		h.brandingErr(w, err)
		return
	}

	h.log.Infow("branding updated", "by", currentPrincipal(r).Username)
	h.writeAdmin(w, snap)
}

func (h *BrandingHandler) handleAdminReset(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	snap, err := h.mgr.Reset(r.Context())
	if err != nil {
		h.brandingErr(w, err)
		return
	}

	h.log.Infow("branding reset to defaults", "by", currentPrincipal(r).Username)
	h.writeAdmin(w, snap)
}

func (h *BrandingHandler) handleAdminPutImage(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	kind, ok := branding.ParseKind(chi.URLParam(r, "asset"))
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "unknown branding image")
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, branding.MaxImageBytes))
	if err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			h.brandingErr(w, branding.ErrTooLarge)
			return
		}

		writeError(w, http.StatusBadRequest, "bad_request", "could not read the image")

		return
	}

	img, err := branding.NewImage(r.Header.Get("Content-Type"), body)
	if err != nil {
		h.brandingErr(w, err)
		return
	}

	snap, err := h.mgr.SetImage(r.Context(), kind, img)
	if err != nil {
		h.brandingErr(w, err)
		return
	}

	h.log.Infow("branding image replaced", "image", string(kind), "sha256", img.SHA256, "by", currentPrincipal(r).Username)
	h.writeAdmin(w, snap)
}

func (h *BrandingHandler) handleAdminDeleteImage(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	kind, ok := branding.ParseKind(chi.URLParam(r, "asset"))
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "unknown branding image")
		return
	}

	snap, err := h.mgr.ClearImage(r.Context(), kind)
	if err != nil {
		h.brandingErr(w, err)
		return
	}

	h.log.Infow("branding image removed", "image", string(kind), "by", currentPrincipal(r).Username)
	h.writeAdmin(w, snap)
}
