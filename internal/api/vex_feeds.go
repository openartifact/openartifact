// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/go-chi/chi/v5"

	"github.com/openartifact/openartifact/internal/api/responses"
	"github.com/openartifact/openartifact/internal/audit"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/netutil"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/pkg/logger"
)

const (
	maxVEXFeedURLLen         = 2048
	maxVEXFeedKeysBytes      = 64 << 10
	minVEXFeedIntervalHours  = 1
	maxVEXFeedIntervalHours  = 720
	defaultVEXFeedIntervalHr = 24
)

// VEXFeedStore is the part of the database VEXFeedHandler needs.
type VEXFeedStore interface {
	ListVEXFeeds(ctx context.Context) ([]*models.VEXFeed, error)
	GetVEXFeed(ctx context.Context, id string) (*models.VEXFeed, error)
	CreateVEXFeed(ctx context.Context, f *models.VEXFeed) error
	UpdateVEXFeed(ctx context.Context, f *models.VEXFeed) error
	DeleteVEXFeed(ctx context.Context, id string) error
}

// VEXFeedSyncer starts the sync of one feed in the background; false when a
// sync of that feed is already running.
type VEXFeedSyncer interface {
	StartSync(feedID string) bool
}

// VEXFeedHandler manages the vendor CSAF VEX feeds (global, admin-only).
type VEXFeedHandler struct {
	db        VEXFeedStore
	log       *logger.Logger
	evaluator *auth.Evaluator
	syncer    VEXFeedSyncer
}

// NewVEXFeedHandler creates the handler; syncer nil disables manual syncs.
func NewVEXFeedHandler(db VEXFeedStore, log *logger.Logger, evaluator *auth.Evaluator, syncer VEXFeedSyncer) *VEXFeedHandler {
	return &VEXFeedHandler{db: db, log: log, evaluator: evaluator, syncer: syncer}
}

// RegisterAuthed mounts the routes on the authenticated API router.
func (h *VEXFeedHandler) RegisterAuthed(r chi.Router) {
	r.Get("/api/v1/vex/feeds", h.handleList)
	r.Post("/api/v1/vex/feeds", h.handleCreate)
	r.Get("/api/v1/vex/feeds/{id}", h.handleGet)
	r.Put("/api/v1/vex/feeds/{id}", h.handleUpdate)
	r.Delete("/api/v1/vex/feeds/{id}", h.handleDelete)
	r.Post("/api/v1/vex/feeds/{id}/sync", h.handleSync)
}

func vexFeedResponse(f *models.VEXFeed) responses.VEXFeedResponse {
	return responses.VEXFeedResponse{
		ID:               f.ID,
		Name:             f.Name,
		URL:              f.URL,
		Enabled:          f.Enabled,
		RequireSignature: f.RequireSignature,
		PGPKeys:          f.PGPKeys,
		IntervalHours:    f.IntervalHours,
		CursorAt:         formatTimePtr(f.CursorAt),
		LastAttemptAt:    formatTimePtr(f.LastAttemptAt),
		LastSuccessAt:    formatTimePtr(f.LastSuccessAt),
		LastError:        f.LastError,
		DocumentsTotal:   f.DocumentsTotal,
		RejectedTotal:    f.RejectedTotal,
		Statements:       f.StatementCount,
		Applied:          f.AppliedCount,
		CreatedAt:        formatTime(f.CreatedAt),
		UpdatedAt:        formatTime(f.UpdatedAt),
	}
}

func (h *VEXFeedHandler) dbError(w http.ResponseWriter, op string, err error) {
	h.log.Errorw("vex feeds: "+op, "error", err)
	writeError(w, http.StatusInternalServerError, "internal_error", "database error")
}

func (h *VEXFeedHandler) handleList(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	feeds, err := h.db.ListVEXFeeds(r.Context())
	if err != nil {
		h.dbError(w, "list", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"feeds": mapList(feeds, vexFeedResponse)})
}

// loadFeed authorizes the caller and loads the {id} feed: 404 when unknown.
func (h *VEXFeedHandler) loadFeed(w http.ResponseWriter, r *http.Request) (*models.VEXFeed, bool) {
	if !requireAdmin(w, r, h.evaluator) {
		return nil, false
	}

	id := urlparams.Vars(r)["id"]
	if !isUUIDString(id) {
		writeError(w, http.StatusNotFound, "not_found", "VEX feed not found")
		return nil, false
	}

	feed, err := h.db.GetVEXFeed(r.Context(), id)
	if errors.Is(err, database.ErrVEXFeedNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "VEX feed not found")
		return nil, false
	}

	if err != nil {
		h.dbError(w, "get", err)
		return nil, false
	}

	return feed, true
}

func (h *VEXFeedHandler) handleGet(w http.ResponseWriter, r *http.Request) {
	if feed, ok := h.loadFeed(w, r); ok {
		writeJSON(w, http.StatusOK, vexFeedResponse(feed))
	}
}

// vexFeedBody is the editable part of a feed; PUT replaces all of it.
type vexFeedBody struct {
	Name             string `json:"name"`
	URL              string `json:"url"`
	Enabled          *bool  `json:"enabled"`
	RequireSignature *bool  `json:"require_signature"`
	PGPKeys          string `json:"pgp_keys"`
	IntervalHours    *int   `json:"interval_hours"`
}

// apply validates the body and copies it into f, returning a validation
// message for the first invalid field.
func (b *vexFeedBody) apply(ctx context.Context, f *models.VEXFeed) string {
	name, rawURL, keys := strings.TrimSpace(b.Name), strings.TrimSpace(b.URL), strings.TrimSpace(b.PGPKeys)

	if !peerNameRe().MatchString(name) {
		return "name must be 1-63 characters: letters, digits, '.', '_' or '-', starting with a letter or digit"
	}

	if msg := validateVEXFeedURL(ctx, rawURL); msg != "" {
		return msg
	}

	if msg := validateVEXFeedKeys(keys); msg != "" {
		return msg
	}

	interval := defaultVEXFeedIntervalHr
	if b.IntervalHours != nil {
		interval = *b.IntervalHours
	}

	if interval < minVEXFeedIntervalHours || interval > maxVEXFeedIntervalHours {
		return "interval_hours must be between " + strconv.Itoa(minVEXFeedIntervalHours) + " and " +
			strconv.Itoa(maxVEXFeedIntervalHours)
	}

	f.Name, f.URL, f.PGPKeys, f.IntervalHours = name, rawURL, keys, interval
	f.Enabled = b.Enabled == nil || *b.Enabled
	f.RequireSignature = b.RequireSignature == nil || *b.RequireSignature

	return ""
}

// validateVEXFeedURL requires an https URL (http only for a loopback host)
// that passes the outbound SSRF checks.
func validateVEXFeedURL(ctx context.Context, raw string) string {
	if raw == "" || len(raw) > maxVEXFeedURLLen || !netutil.IsHTTPSOrLoopbackURL(raw) {
		return "url must be an https URL of a CSAF provider-metadata.json, ROLIE feed or CSAF document"
	}

	if err := netutil.ValidateOutboundURLContext(ctx, raw); err != nil {
		return "url is not allowed: it must resolve to a public address"
	}

	return ""
}

// validateVEXFeedKeys accepts no keys or an armored OpenPGP public key ring.
func validateVEXFeedKeys(keys string) string {
	if keys == "" {
		return ""
	}

	if len(keys) > maxVEXFeedKeysBytes {
		return "pgp_keys is limited to 64 KiB"
	}

	if ring, err := openpgp.ReadArmoredKeyRing(strings.NewReader(keys)); err != nil || len(ring) == 0 {
		return "pgp_keys must be ASCII-armored OpenPGP public keys"
	}

	return ""
}

// decodeFeed reads and validates the body into f; false when a 400 was written.
func decodeFeed(w http.ResponseWriter, r *http.Request, f *models.VEXFeed) bool {
	var body vexFeedBody
	if err := decodeJSON(r, &body); err != nil {
		writeBadJSON(w, err)
		return false
	}

	if msg := body.apply(r.Context(), f); msg != "" {
		writeError(w, http.StatusBadRequest, "validation_failed", msg)
		return false
	}

	return true
}

func (h *VEXFeedHandler) handleCreate(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	feed := &models.VEXFeed{}
	if !decodeFeed(w, r, feed) {
		return
	}

	err := h.db.CreateVEXFeed(r.Context(), feed)
	if errors.Is(err, database.ErrVEXFeedConflict) {
		writeError(w, http.StatusConflict, "conflict", "a VEX feed with this name already exists")
		return
	}

	if err != nil {
		h.dbError(w, "create", err)
		return
	}

	auditAdmin(r, audit.ActionVEXFeedCreated, "vex_feed", feed.ID, "VEX feed "+feed.Name+" created")
	writeJSON(w, http.StatusCreated, vexFeedResponse(feed))
}

func (h *VEXFeedHandler) handleUpdate(w http.ResponseWriter, r *http.Request) {
	feed, ok := h.loadFeed(w, r)
	if !ok || !decodeFeed(w, r, feed) {
		return
	}

	err := h.db.UpdateVEXFeed(r.Context(), feed)

	switch {
	case errors.Is(err, database.ErrVEXFeedNotFound):
		writeError(w, http.StatusNotFound, "not_found", "VEX feed not found")
	case errors.Is(err, database.ErrVEXFeedConflict):
		writeError(w, http.StatusConflict, "conflict", "a VEX feed with this name already exists")
	case err != nil:
		h.dbError(w, "update", err)
	default:
		// Re-read: the URL change resets the cursor and the counters are derived.
		updated, err := h.db.GetVEXFeed(r.Context(), feed.ID)
		if err != nil {
			h.dbError(w, "get", err)
			return
		}

		auditAdmin(r, audit.ActionVEXFeedUpdated, "vex_feed", feed.ID, "VEX feed "+feed.Name+" updated")
		writeJSON(w, http.StatusOK, vexFeedResponse(updated))
	}
}

func (h *VEXFeedHandler) handleDelete(w http.ResponseWriter, r *http.Request) {
	feed, ok := h.loadFeed(w, r)
	if !ok {
		return
	}

	err := h.db.DeleteVEXFeed(r.Context(), feed.ID)
	if errors.Is(err, database.ErrVEXFeedNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "VEX feed not found")
		return
	}

	if err != nil {
		h.dbError(w, "delete", err)
		return
	}

	auditAdmin(r, audit.ActionVEXFeedDeleted, "vex_feed", feed.ID, "VEX feed "+feed.Name+" deleted")
	writeNoContent(w)
}

// handleSync starts a sync of the feed in the background: 202, or 409 while
// one is already running.
func (h *VEXFeedHandler) handleSync(w http.ResponseWriter, r *http.Request) {
	feed, ok := h.loadFeed(w, r)
	if !ok {
		return
	}

	if h.syncer == nil {
		writeError(w, http.StatusServiceUnavailable, "vex_sync_unavailable", "VEX feed sync is not available")
		return
	}

	if !h.syncer.StartSync(feed.ID) {
		writeError(w, http.StatusConflict, "conflict", "a sync of this VEX feed is already running")
		return
	}

	auditAdmin(r, audit.ActionVEXFeedSynced, "vex_feed", feed.ID, "sync of VEX feed "+feed.Name+" started")
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "started"})
}
