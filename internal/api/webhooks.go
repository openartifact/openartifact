// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/api/responses"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/crypto"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/netutil"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/internal/webhook"
	"github.com/openartifact/openartifact/pkg/logger"
)

type WebhooksHandler struct {
	db         database.Database
	log        *logger.Logger
	evaluator  *auth.Evaluator
	dispatcher *webhook.Dispatcher
}

func NewWebhooksHandler(db database.Database, log *logger.Logger, evaluator *auth.Evaluator, dispatcher *webhook.Dispatcher) *WebhooksHandler {
	return &WebhooksHandler{
		db:         db,
		log:        log,
		evaluator:  evaluator,
		dispatcher: dispatcher,
	}
}

func (h *WebhooksHandler) RegisterAuthed(r chi.Router) {
	// Global webhooks
	r.Get("/api/v1/webhooks", h.handleListGlobal)
	r.Post("/api/v1/webhooks", h.handleCreateGlobal)
	r.Get("/api/v1/webhooks/{id}", h.handleGet)
	r.Put("/api/v1/webhooks/{id}", h.handleUpdate)
	r.Delete("/api/v1/webhooks/{id}", h.handleDelete)
	r.Get("/api/v1/webhooks/{id}/deliveries", h.handleListDeliveries)
	r.Post("/api/v1/webhooks/{id}/test", h.handleTest)

	// Repository webhooks
	r.Get("/api/v1/repositories/{name}/webhooks", h.handleListRepo)
	r.Post("/api/v1/repositories/{name}/webhooks", h.handleCreateRepo)
}

const (
	maxWebhookBodyBytes   = 64 << 10
	maxWebhookNameLen     = 255
	maxWebhookURLLen      = 2048
	maxWebhookSecretLen   = 512
	maxWebhookEvents      = 32
	defaultWebhookRetries = 3
	maxWebhookRetries     = 10
	webhookContentType    = "application/json"
)

func webhookToResponse(wh *models.Webhook) responses.WebhookResponse {
	var repoID *string
	if wh.RepositoryID != "" {
		repoID = &wh.RepositoryID
	}

	events := wh.Events
	if events == nil {
		events = []string{}
	}

	return responses.WebhookResponse{
		ID:             wh.ID,
		RepositoryID:   repoID,
		Name:           wh.Name,
		URL:            wh.URL,
		Enabled:        wh.Enabled,
		Events:         events,
		ContentType:    wh.ContentType,
		HasSecret:      wh.SecretEnc != "",
		SSLVerify:      wh.SSLVerify,
		TimeoutSeconds: wh.TimeoutSeconds,
		MaxRetries:     wh.MaxRetries,
		CreatedAt:      formatTime(wh.CreatedAt),
		UpdatedAt:      formatTime(wh.UpdatedAt),
	}
}

func webhooksToResponse(list []*models.Webhook) []responses.WebhookResponse {
	out := make([]responses.WebhookResponse, 0, len(list))
	for _, wh := range list {
		out = append(out, webhookToResponse(wh))
	}

	return out
}

func clampInt(v, lo, hi int) int {
	return min(max(v, lo), hi)
}

// requireAdmin writes a 403 and returns false unless the caller is an admin.
func (h *WebhooksHandler) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	isAdmin, err := principalIsAdmin(r.Context(), currentPrincipal(r), h.evaluator)
	if err != nil || !isAdmin {
		writeError(w, http.StatusForbidden, "forbidden", "admin access required")
		return false
	}

	return true
}

// decodeWebhookBody decodes a size-capped JSON body, writing 413/400 on failure.
func decodeWebhookBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxWebhookBodyBytes)

	if err := decodeJSON(r, dst); err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			writeError(w, http.StatusRequestEntityTooLarge, "payload_too_large", "request body too large")
			return false
		}

		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON")

		return false
	}

	return true
}

// validateWebhookURL requires an http(s) URL whose host is not an internal address.
func validateWebhookURL(raw string) error {
	if len(raw) > maxWebhookURLLen {
		return validationError("url is too long")
	}

	return netutil.ValidateOutboundURL(raw)
}

// normalizeWebhookEvents validates event names against the dispatcher's event
// set and removes duplicates.
func normalizeWebhookEvents(events []string) ([]string, error) {
	if len(events) == 0 {
		return nil, validationError("at least one event is required")
	}

	if len(events) > maxWebhookEvents {
		return nil, validationError("too many events")
	}

	seen := make(map[string]struct{}, len(events))
	out := make([]string, 0, len(events))

	for _, e := range events {
		if !webhook.IsSubscribableEvent(e) {
			return nil, validationError("unknown event " + e + "; valid events: " + strings.Join(webhook.SubscribableEvents(), ", "))
		}

		if _, dup := seen[e]; dup {
			continue
		}

		seen[e] = struct{}{}

		out = append(out, e)
	}

	return out, nil
}

func normalizeWebhookName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", validationError("name is required")
	}

	if len(name) > maxWebhookNameLen {
		return "", validationError("name is too long")
	}

	return name, nil
}

func validateWebhookContentType(ct string) error {
	if ct != webhookContentType {
		return validationError("content_type must be " + webhookContentType)
	}

	return nil
}

// encryptWebhookSecret validates and encrypts a write-only signing secret.
func encryptWebhookSecret(secret string) (string, error) {
	if len(secret) > maxWebhookSecretLen {
		return "", validationError("secret is too long")
	}

	if err := crypto.CheckPlaintext(secret); err != nil {
		return "", validationError("secret must be the plaintext secret")
	}

	enc, err := crypto.Encrypt(secret)
	if err != nil {
		return "", err //nolint:wrapcheck // internal, logged by caller
	}

	return enc, nil
}

// handleListGlobal lists all global webhooks.
func (h *WebhooksHandler) handleListGlobal(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}

	webhooks, err := h.db.ListWebhooks(r.Context(), "")
	if err != nil {
		h.log.Errorw("list webhooks: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"webhooks": webhooksToResponse(webhooks),
	})
}

// handleCreateGlobal creates a new global webhook.
func (h *WebhooksHandler) handleCreateGlobal(w http.ResponseWriter, r *http.Request) {
	h.createWebhook(w, r, "")
}

// handleListRepo lists all webhooks for a repository.
func (h *WebhooksHandler) handleListRepo(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}

	ctx := r.Context()

	repo, err := h.db.GetRepositoryByName(ctx, urlparams.Vars(r)["name"])
	if err != nil {
		h.log.Errorw("get repository: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if repo == nil {
		writeError(w, http.StatusNotFound, "not_found", "repository not found")
		return
	}

	webhooks, err := h.db.ListWebhooks(ctx, repo.ID)
	if err != nil {
		h.log.Errorw("list webhooks: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"webhooks": webhooksToResponse(webhooks),
	})
}

// handleCreateRepo creates a new webhook for a repository.
func (h *WebhooksHandler) handleCreateRepo(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}

	repo, err := h.db.GetRepositoryByName(r.Context(), urlparams.Vars(r)["name"])
	if err != nil {
		h.log.Errorw("get repository: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if repo == nil {
		writeError(w, http.StatusNotFound, "not_found", "repository not found")
		return
	}

	h.createWebhook(w, r, repo.ID)
}

// createWebhook is the shared create path for global (repoID == "") and
// repository webhooks.
func (h *WebhooksHandler) createWebhook(w http.ResponseWriter, r *http.Request, repoID string) {
	if !h.requireAdmin(w, r) {
		return
	}

	var req struct {
		Name           string   `json:"name"`
		URL            string   `json:"url"`
		Secret         string   `json:"secret"`
		Enabled        *bool    `json:"enabled"`
		Events         []string `json:"events"`
		ContentType    string   `json:"content_type"`
		SSLVerify      *bool    `json:"ssl_verify"`
		MaxRetries     *int     `json:"max_retries"`
		TimeoutSeconds *int     `json:"timeout_seconds"`
	}

	if !decodeWebhookBody(w, r, &req) {
		return
	}

	name, err := normalizeWebhookName(req.Name)
	if err != nil {
		writeError(w, http.StatusBadRequest, "validation_failed", err.Error())
		return
	}

	if req.URL == "" {
		writeError(w, http.StatusBadRequest, "validation_failed", "url is required")
		return
	}

	if err := validateWebhookURL(req.URL); err != nil {
		writeError(w, http.StatusBadRequest, "validation_failed", "invalid url: "+err.Error())
		return
	}

	events, err := normalizeWebhookEvents(req.Events)
	if err != nil {
		writeError(w, http.StatusBadRequest, "validation_failed", err.Error())
		return
	}

	contentType := req.ContentType
	if contentType == "" {
		contentType = webhookContentType
	}

	if err := validateWebhookContentType(contentType); err != nil {
		writeError(w, http.StatusBadRequest, "validation_failed", err.Error())
		return
	}

	secretEnc, err := encryptWebhookSecret(req.Secret)
	if err != nil {
		h.log.Errorw("create webhook: secret encryption failed", "error", err)
		writeError(w, http.StatusBadRequest, "validation_failed", "invalid secret")

		return
	}

	wh := &models.Webhook{
		RepositoryID:   repoID,
		Name:           name,
		URL:            req.URL,
		SecretEnc:      secretEnc,
		Enabled:        req.Enabled == nil || *req.Enabled,
		Events:         events,
		ContentType:    contentType,
		SSLVerify:      req.SSLVerify == nil || *req.SSLVerify,
		MaxRetries:     defaultWebhookRetries,
		TimeoutSeconds: webhook.DefaultTimeoutSeconds,
	}

	if req.MaxRetries != nil {
		wh.MaxRetries = clampInt(*req.MaxRetries, 0, maxWebhookRetries)
	}

	if req.TimeoutSeconds != nil {
		wh.TimeoutSeconds = clampInt(*req.TimeoutSeconds, 1, webhook.MaxTimeoutSeconds)
	}

	if err := h.db.CreateWebhook(r.Context(), wh); err != nil {
		h.log.Errorw("create webhook: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	writeJSON(w, http.StatusCreated, webhookToResponse(wh))
}

// loadWebhook fetches the webhook named by the {id} path parameter, writing the
// error response itself and returning nil when it cannot be served.
func (h *WebhooksHandler) loadWebhook(w http.ResponseWriter, r *http.Request) *models.Webhook {
	if !h.requireAdmin(w, r) {
		return nil
	}

	wh, err := h.db.GetWebhook(r.Context(), urlparams.Vars(r)["id"])
	if err != nil {
		h.log.Errorw("get webhook: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return nil
	}

	if wh == nil {
		writeError(w, http.StatusNotFound, "not_found", "webhook not found")
		return nil
	}

	return wh
}

// handleGet retrieves a webhook by ID.
func (h *WebhooksHandler) handleGet(w http.ResponseWriter, r *http.Request) {
	wh := h.loadWebhook(w, r)
	if wh == nil {
		return
	}

	writeJSON(w, http.StatusOK, webhookToResponse(wh))
}

// handleUpdate updates a webhook. Omitted fields are left unchanged; an empty
// "secret" clears the signing secret.
func (h *WebhooksHandler) handleUpdate(w http.ResponseWriter, r *http.Request) {
	wh := h.loadWebhook(w, r)
	if wh == nil {
		return
	}

	var req struct {
		Name           *string   `json:"name"`
		URL            *string   `json:"url"`
		Secret         *string   `json:"secret"`
		Enabled        *bool     `json:"enabled"`
		Events         *[]string `json:"events"`
		ContentType    *string   `json:"content_type"`
		SSLVerify      *bool     `json:"ssl_verify"`
		MaxRetries     *int      `json:"max_retries"`
		TimeoutSeconds *int      `json:"timeout_seconds"`
	}

	if !decodeWebhookBody(w, r, &req) {
		return
	}

	if req.Name != nil {
		name, err := normalizeWebhookName(*req.Name)
		if err != nil {
			writeError(w, http.StatusBadRequest, "validation_failed", err.Error())
			return
		}

		wh.Name = name
	}

	if req.URL != nil {
		if err := validateWebhookURL(*req.URL); err != nil {
			writeError(w, http.StatusBadRequest, "validation_failed", "invalid url: "+err.Error())
			return
		}

		wh.URL = *req.URL
	}

	if req.Secret != nil {
		enc, err := encryptWebhookSecret(*req.Secret)
		if err != nil {
			h.log.Errorw("update webhook: secret encryption failed", "error", err)
			writeError(w, http.StatusBadRequest, "validation_failed", "invalid secret")

			return
		}

		wh.SecretEnc = enc
	}

	if req.Enabled != nil {
		wh.Enabled = *req.Enabled
	}

	if req.Events != nil {
		events, err := normalizeWebhookEvents(*req.Events)
		if err != nil {
			writeError(w, http.StatusBadRequest, "validation_failed", err.Error())
			return
		}

		wh.Events = events
	}

	if req.ContentType != nil {
		if err := validateWebhookContentType(*req.ContentType); err != nil {
			writeError(w, http.StatusBadRequest, "validation_failed", err.Error())
			return
		}

		wh.ContentType = *req.ContentType
	}

	if req.SSLVerify != nil {
		wh.SSLVerify = *req.SSLVerify
	}

	if req.MaxRetries != nil {
		wh.MaxRetries = clampInt(*req.MaxRetries, 0, maxWebhookRetries)
	}

	if req.TimeoutSeconds != nil {
		wh.TimeoutSeconds = clampInt(*req.TimeoutSeconds, 1, webhook.MaxTimeoutSeconds)
	}

	if err := h.db.UpdateWebhook(r.Context(), wh); err != nil {
		h.log.Errorw("update webhook: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	writeJSON(w, http.StatusOK, webhookToResponse(wh))
}

// handleDelete deletes a webhook.
func (h *WebhooksHandler) handleDelete(w http.ResponseWriter, r *http.Request) {
	wh := h.loadWebhook(w, r)
	if wh == nil {
		return
	}

	if err := h.db.DeleteWebhook(r.Context(), wh.ID); err != nil {
		h.log.Errorw("delete webhook: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleListDeliveries lists deliveries for a webhook.
func (h *WebhooksHandler) handleListDeliveries(w http.ResponseWriter, r *http.Request) {
	wh := h.loadWebhook(w, r)
	if wh == nil {
		return
	}

	limit := parseIntClamp(r.URL.Query().Get("limit"), 50, 1, 500)

	deliveries, err := h.db.ListWebhookDeliveries(r.Context(), wh.ID, limit)
	if err != nil {
		h.log.Errorw("list webhook deliveries: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if deliveries == nil {
		deliveries = []*models.WebhookDelivery{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"deliveries": deliveries,
	})
}

// handleTest queues a test delivery for exactly this webhook, independent of
// its event subscriptions.
func (h *WebhooksHandler) handleTest(w http.ResponseWriter, r *http.Request) {
	wh := h.loadWebhook(w, r)
	if wh == nil {
		return
	}

	if h.dispatcher == nil {
		writeError(w, http.StatusNotImplemented, "not_implemented", "webhook dispatcher is not available")
		return
	}

	testPayload := map[string]any{
		"event":     webhook.EventWebhookTest,
		"message":   "This is a test webhook delivery",
		"timestamp": time.Now().Unix(),
	}

	if err := h.dispatcher.DispatchTo(r.Context(), wh, webhook.EventWebhookTest, testPayload); err != nil {
		h.log.Errorw("test webhook: dispatch failed", "webhook_id", wh.ID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to queue test event")

		return
	}

	writeJSON(w, http.StatusAccepted, map[string]any{
		"message": "test event queued",
	})
}
