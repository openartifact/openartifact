// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/apkkeys"
	"github.com/openartifact/openartifact/internal/audit"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/repotype"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/pkg/logger"
)

// ApkKeyHandler manages the RSA key that signs the APKINDEX of an apk
// repository (docs/formats/apk.md). Every route needs an admin.
type ApkKeyHandler struct {
	db        database.Database
	log       *logger.Logger
	evaluator *auth.Evaluator
	keys      *apkkeys.Service
}

// NewApkKeyHandler builds the handler.
func NewApkKeyHandler(db database.Database, log *logger.Logger, evaluator *auth.Evaluator, keys *apkkeys.Service) *ApkKeyHandler {
	return &ApkKeyHandler{db: db, log: log, evaluator: evaluator, keys: keys}
}

// Register registers the routes on the authenticated REST router.
func (h *ApkKeyHandler) Register(r chi.Router) {
	r.Get("/api/v1/repositories/{name}/apk-key", h.handleGet)
	r.Post("/api/v1/repositories/{name}/apk-key", h.handleReplace)
}

// apkKeyResponse is the API shape of a signing key. It never carries the private key.
type apkKeyResponse struct {
	KeyName     string    `json:"key_name"`
	Fingerprint string    `json:"fingerprint"`
	PublicKey   string    `json:"public_key"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func (h *ApkKeyHandler) repo(w http.ResponseWriter, r *http.Request) *models.Repository {
	if !requireAdmin(w, r, h.evaluator) {
		return nil
	}

	name := urlparams.Vars(r)["name"]

	repo, err := h.db.GetRepositoryByName(r.Context(), name)
	if err != nil {
		h.log.Errorw("apk key: repository lookup failed", "repo", name, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to look up the repository")

		return nil
	}

	if repo == nil {
		writeError(w, http.StatusNotFound, "not_found", "repository not found")
		return nil
	}

	if repo.Format != "apk" || repo.RepoType == repotype.Proxy {
		writeError(w, http.StatusBadRequest, "invalid_input", "only hosted and group apk repositories have a signing key")
		return nil
	}

	return repo
}

func (h *ApkKeyHandler) handleGet(w http.ResponseWriter, r *http.Request) {
	repo := h.repo(w, r)
	if repo == nil {
		return
	}

	info, err := h.keys.Info(r.Context(), repo)
	if err != nil {
		h.writeError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, apkKeyResponseOf(info))
}

// handleReplace rotates the key: an empty body generates a new one, a body with
// private_key imports it (key_name optionally names it, default openartifact-<repo>.rsa.pub).
func (h *ApkKeyHandler) handleReplace(w http.ResponseWriter, r *http.Request) {
	repo := h.repo(w, r)
	if repo == nil {
		return
	}

	var body struct {
		PrivateKey string `json:"private_key"`
		KeyName    string `json:"key_name"`
	}

	if err := decodeJSON(r, &body); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}

	by := currentPrincipal(r).Username

	var (
		info *apkkeys.Info
		err  error
	)

	if body.PrivateKey == "" {
		if body.KeyName != "" {
			writeError(w, http.StatusBadRequest, "validation_failed", "key_name can only be set together with private_key")
			return
		}

		info, err = h.keys.Generate(r.Context(), repo, by)
	} else {
		info, err = h.keys.Import(r.Context(), repo, body.PrivateKey, body.KeyName, by)
	}

	if err != nil {
		h.writeError(w, err)
		return
	}

	auditAdmin(r, audit.ActionRepositoryUpdated, "repository", repo.ID,
		"apk signing key of "+repo.Name+" replaced, fingerprint "+info.Fingerprint)

	writeJSON(w, http.StatusOK, apkKeyResponseOf(info))
}

func (h *ApkKeyHandler) writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, apkkeys.ErrSecretKeyRequired):
		writeError(w, http.StatusConflict, "secret_key_required",
			"OPENARTIFACT_SECRET_KEY must be set: apk signing keys are only stored encrypted")
	case errors.Is(err, apkkeys.ErrInvalidKey):
		writeError(w, http.StatusBadRequest, "validation_failed", err.Error())
	default:
		h.log.Errorw("apk key operation failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "apk signing key operation failed")
	}
}

func apkKeyResponseOf(info *apkkeys.Info) apkKeyResponse {
	return apkKeyResponse{
		KeyName: info.KeyName, Fingerprint: info.Fingerprint, PublicKey: info.PublicKey,
		CreatedAt: info.CreatedAt, UpdatedAt: info.UpdatedAt,
	}
}
