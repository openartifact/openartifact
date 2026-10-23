// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/audit"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/crypto"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/gpgkeys"
	"github.com/openartifact/openartifact/pkg/gpg"
	"github.com/openartifact/openartifact/pkg/logger"
)

// AdminGPGHandler manages the GPG keys that sign deb/rpm metadata and sealed
// release bundles (docs/security/signing.md). Every route needs an admin.
type AdminGPGHandler struct {
	db        database.Database
	log       *logger.Logger
	evaluator *auth.Evaluator
	keys      *gpgkeys.Service
}

func NewAdminGPGHandler(db database.Database, log *logger.Logger, evaluator *auth.Evaluator, keys *gpgkeys.Service) *AdminGPGHandler {
	return &AdminGPGHandler{db: db, log: log, evaluator: evaluator, keys: keys}
}

func (h *AdminGPGHandler) Register(r chi.Router) {
	r.Get("/api/v1/admin/gpg/keys", h.handleList)
	r.Post("/api/v1/admin/gpg/keys", h.handleImport)
	r.Post("/api/v1/admin/gpg/keys/generate", h.handleGenerate)
	r.Delete("/api/v1/admin/gpg/keys/{id}", h.handleDelete)
	r.Put("/api/v1/admin/gpg/keys/{id}/activate", h.handleActivate)
	r.Delete("/api/v1/admin/gpg/keys/{id}/activate", h.handleDeactivate)
	r.Get("/api/v1/admin/gpg/keys/{id}/export", h.handleExport)
}

const (
	maxGPGNameLen      = 255
	maxGPGUserFieldLen = 128
)

// gpgKeyResponse is the API shape of a key. It never carries private key material.
type gpgKeyResponse struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	Fingerprint  string     `json:"fingerprint"`
	KeyID        string     `json:"key_id"`
	Algorithm    string     `json:"algorithm"`
	UserID       string     `json:"user_id"`
	Source       string     `json:"source"`
	ReadOnly     bool       `json:"read_only"`
	ActiveGlobal bool       `json:"active_global"`
	Repositories []string   `json:"repositories"`
	CreatedBy    string     `json:"created_by"`
	CreatedAt    *time.Time `json:"created_at"`
	ExpiresAt    *time.Time `json:"expires_at"`
}

func gpgEntryResponse(e gpgkeys.Entry) gpgKeyResponse {
	resp := gpgKeyResponse{
		ID: e.ID, Name: e.Name, Fingerprint: e.Fingerprint, KeyID: e.KeyID, Algorithm: e.Algorithm,
		UserID: e.UserID, Source: e.Source, ReadOnly: e.ReadOnly, ActiveGlobal: e.ActiveGlobal,
		Repositories: e.Repositories, CreatedBy: e.CreatedBy, ExpiresAt: e.ExpiresAt,
	}

	if !e.CreatedAt.IsZero() {
		t := e.CreatedAt
		resp.CreatedAt = &t
	}

	return resp
}

func gpgModelResponse(k *models.GPGKey) gpgKeyResponse {
	return gpgEntryResponse(gpgkeys.Entry{
		ID: k.ID, Name: k.Name, Fingerprint: k.Fingerprint, KeyID: k.KeyID, Algorithm: k.Algorithm,
		UserID: k.UserID, Source: k.Source, ActiveGlobal: k.IsGlobalActive, Repositories: []string{},
		CreatedBy: k.CreatedBy, CreatedAt: k.CreatedAt, ExpiresAt: k.ExpiresAt,
	})
}

func (h *AdminGPGHandler) handleList(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	entries, err := h.keys.List(r.Context())
	if err != nil {
		h.log.Errorw("list gpg keys failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to list GPG keys")

		return
	}

	out := make([]gpgKeyResponse, 0, len(entries))
	for _, e := range entries {
		out = append(out, gpgEntryResponse(e))
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"keys": out,
		// Keys can only be imported or generated when OPENARTIFACT_SECRET_KEY
		// is set: private keys are stored sealed with it, never in plaintext.
		"can_store_keys": crypto.Enabled(),
	})
}

// writeGPGServiceError maps service and parser errors to API errors.
func (h *AdminGPGHandler) writeGPGServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, gpgkeys.ErrSecretKeyRequired):
		writeError(w, http.StatusConflict, "secret_key_required",
			"OPENARTIFACT_SECRET_KEY must be set: GPG private keys are only stored encrypted")
	case errors.Is(err, gpgkeys.ErrExists):
		writeError(w, http.StatusConflict, "conflict", "a GPG key with this name or fingerprint already exists")
	case errors.Is(err, gpgkeys.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "GPG key not found")
	case errors.Is(err, gpgkeys.ErrReadOnly):
		writeError(w, http.StatusConflict, "read_only", "the key configured through gpg.keyPath is read-only")
	case errors.Is(err, gpgkeys.ErrKeyInUse):
		writeError(w, http.StatusConflict, "key_in_use", "the key is active; deactivate it first or pass force=true")
	case errors.Is(err, gpgkeys.ErrNotActive):
		writeError(w, http.StatusConflict, "conflict", "the key is not active there")
	case errors.Is(err, gpg.ErrPassphraseRequired):
		writeError(w, http.StatusBadRequest, "passphrase_required", "the private key is passphrase-protected: pass its passphrase")
	case errors.Is(err, gpg.ErrBadPassphrase):
		writeError(w, http.StatusBadRequest, "validation_failed", "wrong passphrase for the private key")
	case errors.Is(err, crypto.ErrEncryptedInput):
		writeError(w, http.StatusBadRequest, "validation_failed", "private_key must be an armored private key, not an encrypted value")
	default:
		if reason, ok := gpgInputReason(err); ok {
			writeError(w, http.StatusBadRequest, "validation_failed", "invalid private key: "+reason)
			return
		}

		h.log.Errorw("gpg key operation failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "GPG key operation failed")
	}
}

// gpgInputErrors are the key-parsing failures that are the client's fault.
func gpgInputErrors() []error {
	return []error{
		gpg.ErrInvalidKey, gpg.ErrNoKeysFound, gpg.ErrNotPrivateKey, gpg.ErrKeyCannotSign, gpg.ErrKeyRevoked,
		gpg.ErrKeyExpired, gpg.ErrUnsupportedAlgorithm,
	}
}

// gpgInputReason names the matching client error. It is the sentinel text
// only, never the underlying library message.
func gpgInputReason(err error) (string, bool) {
	for _, target := range gpgInputErrors() {
		if errors.Is(err, target) {
			return target.Error(), true
		}
	}

	return "", false
}

// shouldActivate resolves the optional "activate" flag: unset means "make it
// the global key when nothing signs globally yet".
func (h *AdminGPGHandler) shouldActivate(r *http.Request, requested *bool) (bool, error) {
	if requested != nil {
		return *requested, nil
	}

	entries, err := h.keys.List(r.Context())
	if err != nil {
		return false, err //nolint:wrapcheck // caller logs
	}

	for _, e := range entries {
		if e.ActiveGlobal {
			return false, nil
		}
	}

	return true, nil
}

// finishCreate optionally activates a just-stored key and writes the 201.
func (h *AdminGPGHandler) finishCreate(w http.ResponseWriter, r *http.Request, key *models.GPGKey, activate *bool) {
	p := currentPrincipal(r)

	want, err := h.shouldActivate(r, activate)
	if err != nil {
		h.writeGPGServiceError(w, err)
		return
	}

	auditAdmin(r, audit.ActionGPGKeyCreated, "gpg_key", key.ID, "gpg key "+key.Name+" ("+key.Source+") created, fingerprint "+key.Fingerprint)

	if want {
		if err := h.keys.Activate(r.Context(), key.ID, nil, p.Username); err != nil {
			h.writeGPGServiceError(w, err)
			return
		}

		key.IsGlobalActive = true

		auditAdmin(r, audit.ActionGPGKeyActivated, "gpg_key", key.ID, "gpg key "+key.Name+" activated globally")
	}

	writeJSON(w, http.StatusCreated, gpgModelResponse(key))
}

func (h *AdminGPGHandler) handleImport(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	var body struct {
		Name       string `json:"name"`
		PrivateKey string `json:"private_key"`
		Passphrase string `json:"passphrase"`
		Activate   *bool  `json:"activate"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}

	if msg := validateGPGName(body.Name); msg != "" {
		writeError(w, http.StatusBadRequest, "validation_failed", msg)
		return
	}

	if strings.TrimSpace(body.PrivateKey) == "" {
		writeError(w, http.StatusBadRequest, "validation_failed", "private_key is required (an ASCII-armored private key)")
		return
	}

	key, err := h.keys.Import(r.Context(), gpgkeys.ImportInput{
		Name: body.Name, PrivateKey: body.PrivateKey, Passphrase: body.Passphrase, CreatedBy: currentPrincipal(r).Username,
	})
	if err != nil {
		h.writeGPGServiceError(w, err)
		return
	}

	h.finishCreate(w, r, key, body.Activate)
}

func (h *AdminGPGHandler) handleGenerate(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	var body struct {
		Name      string `json:"name"`
		Algorithm string `json:"algorithm"`
		UserName  string `json:"user_name"`
		Comment   string `json:"comment"`
		Email     string `json:"email"`
		Activate  *bool  `json:"activate"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}

	if msg := validateGPGName(body.Name); msg != "" {
		writeError(w, http.StatusBadRequest, "validation_failed", msg)
		return
	}

	for field, v := range map[string]string{"user_name": body.UserName, "comment": body.Comment, "email": body.Email} {
		if msg := validateGPGUserField(field, v); msg != "" {
			writeError(w, http.StatusBadRequest, "validation_failed", msg)
			return
		}
	}

	alg, err := gpg.ParseAlgorithm(body.Algorithm)
	if err != nil {
		writeError(w, http.StatusBadRequest, "validation_failed", "algorithm must be ed25519 or rsa4096")
		return
	}

	key, err := h.keys.Generate(r.Context(), gpgkeys.GenerateInput{
		Name: body.Name, Algorithm: alg, UserName: body.UserName, Comment: body.Comment, Email: body.Email,
		CreatedBy: currentPrincipal(r).Username,
	})
	if err != nil {
		h.writeGPGServiceError(w, err)
		return
	}

	h.finishCreate(w, r, key, body.Activate)
}

func validateGPGName(name string) string {
	switch {
	case len(name) > maxGPGNameLen:
		return "name is too long (max 255 bytes)"
	case !utf8.ValidString(name):
		return "name must be valid UTF-8"
	}

	return ""
}

// validateGPGUserField rejects what OpenPGP user ids cannot carry.
func validateGPGUserField(field, v string) string {
	if len(v) > maxGPGUserFieldLen {
		return field + " is too long (max 128 bytes)"
	}

	if !utf8.ValidString(v) || strings.ContainsAny(v, "()<>") {
		return field + " must be valid UTF-8 without ( ) < >"
	}

	for _, c := range v {
		if c < 0x20 || c == 0x7f {
			return field + " must not contain control characters"
		}
	}

	return ""
}

// gpgKeyParam validates the {id} path parameter: a key UUID or "config".
func gpgKeyParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := chi.URLParam(r, "id")
	if id != gpgkeys.ConfigKeyID && !isUUIDString(id) {
		writeError(w, http.StatusNotFound, "not_found", "GPG key not found")
		return "", false
	}

	return id, true
}

func (h *AdminGPGHandler) handleDelete(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	id, ok := gpgKeyParam(w, r)
	if !ok {
		return
	}

	force := r.URL.Query().Get("force") == "true"

	if err := h.keys.Delete(r.Context(), id, force); err != nil {
		h.writeGPGServiceError(w, err)
		return
	}

	auditAdmin(r, audit.ActionGPGKeyDeleted, "gpg_key", id, "gpg key deleted (force="+boolString(force)+")")
	w.WriteHeader(http.StatusNoContent)
}

func boolString(b bool) string {
	if b {
		return "true"
	}

	return "false"
}

// signingRepo resolves the optional repository name of an activation to a
// hosted deb/rpm repository; nil, true for the global scope.
func (h *AdminGPGHandler) signingRepo(w http.ResponseWriter, r *http.Request, name string) (*models.GPGSigningRepo, bool) {
	if name == "" {
		return nil, true
	}

	repo, err := h.db.GetRepositoryByName(r.Context(), name)
	if err != nil {
		h.log.Errorw("gpg: loading repository failed", "repository", name, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return nil, false
	}

	if repo == nil {
		writeError(w, http.StatusNotFound, "not_found", "repository not found")
		return nil, false
	}

	if (repo.Format != "deb" && repo.Format != "rpm") || (repo.RepoType != "" && repo.RepoType != "hosted") {
		writeError(w, http.StatusBadRequest, "validation_failed", "only hosted deb and rpm repositories are signed")
		return nil, false
	}

	return &models.GPGSigningRepo{ID: repo.ID, Format: repo.Format}, true
}

func (h *AdminGPGHandler) handleActivate(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	id, ok := gpgKeyParam(w, r)
	if !ok {
		return
	}

	var body struct {
		Repository string `json:"repository"`
	}
	// The body is optional: an empty one means the global scope.
	if err := decodeJSON(r, &body); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}

	repo, ok := h.signingRepo(w, r, body.Repository)
	if !ok {
		return
	}

	if err := h.keys.Activate(r.Context(), id, repo, currentPrincipal(r).Username); err != nil {
		h.writeGPGServiceError(w, err)
		return
	}

	scope := "globally"
	if repo != nil {
		scope = "for repository " + body.Repository
	}

	auditAdmin(r, audit.ActionGPGKeyActivated, "gpg_key", id, "gpg key activated "+scope)
	h.writeKey(w, r, id)
}

func (h *AdminGPGHandler) handleDeactivate(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	id, ok := gpgKeyParam(w, r)
	if !ok {
		return
	}

	name := r.URL.Query().Get("repository")

	repo, ok := h.signingRepo(w, r, name)
	if !ok {
		return
	}

	if err := h.keys.Deactivate(r.Context(), id, repo); err != nil {
		h.writeGPGServiceError(w, err)
		return
	}

	scope := "globally"
	if repo != nil {
		scope = "for repository " + name
	}

	auditAdmin(r, audit.ActionGPGKeyDeactivated, "gpg_key", id, "gpg key deactivated "+scope)
	w.WriteHeader(http.StatusNoContent)
}

// writeKey answers with the current state of one key (as the list shows it).
func (h *AdminGPGHandler) writeKey(w http.ResponseWriter, r *http.Request, id string) {
	entries, err := h.keys.List(r.Context())
	if err != nil {
		h.log.Errorw("list gpg keys failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to load GPG key")

		return
	}

	for _, e := range entries {
		if e.ID == id {
			writeJSON(w, http.StatusOK, gpgEntryResponse(e))
			return
		}
	}

	writeError(w, http.StatusNotFound, "not_found", "GPG key not found")
}

func (h *AdminGPGHandler) handleExport(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	id, ok := gpgKeyParam(w, r)
	if !ok {
		return
	}

	pub, name, err := h.keys.PublicKey(r.Context(), id)
	if err != nil {
		h.writeGPGServiceError(w, err)
		return
	}

	auditAdmin(r, audit.ActionGPGKeyExported, "gpg_key", id, "public key of gpg key "+name+" exported")

	w.Header().Set("Content-Type", "application/pgp-keys")
	w.Header().Set("Content-Disposition", `attachment; filename="public.key"`)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)

	//nolint:gosec // G705: an armored public key served as an attachment, not HTML
	_, _ = w.Write(pub)
}
