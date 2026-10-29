// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"errors"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/audit"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/repotype"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/pkg/logger"
)

// signingKeyService is a per-repository signing key service whose key info has
// the type I (internal/apkkeys, internal/nixkeys).
type signingKeyService[I any] interface {
	Info(ctx context.Context, repo *models.Repository) (*I, error)
	Generate(ctx context.Context, repo *models.Repository, by string) (*I, error)
	Import(ctx context.Context, repo *models.Repository, private, keyName, by string) (*I, error)
}

// signingKeyAdminStore is the part of the database signingKeyAdmin needs.
type signingKeyAdminStore interface {
	GetRepositoryByName(ctx context.Context, name string) (*models.Repository, error)
}

// signingKeyAdmin manages the signing key of the repositories of one format
// that sign with a key of their own (apk, nix), at
// /api/v1/repositories/{name}/<route>. Every route needs an admin.
type signingKeyAdmin[I any] struct {
	db        signingKeyAdminStore
	log       *logger.Logger
	evaluator *auth.Evaluator
	keys      signingKeyService[I]

	// format is the repository format, route the URL segment after the
	// repository name, and noun what the key is called in messages.
	format, route, noun string
	// groups is whether group repositories have a key too (proxy ones never do).
	groups bool
	// respond is the API shape of a key and describe the audit text of a change.
	respond  func(*I) any
	describe func(*I) string
	// mapError answers the errors of the key service the handler does not know.
	mapError func(http.ResponseWriter, error) bool
}

func (h *signingKeyAdmin[I]) register(r chi.Router) {
	r.Get("/api/v1/repositories/{name}/"+h.route, h.handleGet)
	r.Post("/api/v1/repositories/{name}/"+h.route, h.handleReplace)
}

func (h *signingKeyAdmin[I]) repo(w http.ResponseWriter, r *http.Request) *models.Repository {
	if !requireAdmin(w, r, h.evaluator) {
		return nil
	}

	name := urlparams.Vars(r)["name"]

	repo, err := h.db.GetRepositoryByName(r.Context(), name)
	if err != nil {
		h.log.Errorw(h.noun+": repository lookup failed", "repo", name, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to look up the repository")

		return nil
	}

	if repo == nil {
		writeError(w, http.StatusNotFound, "not_found", "repository not found")
		return nil
	}

	if repo.Format != h.format || repo.RepoType == repotype.Proxy || (repo.RepoType == repotype.Group && !h.groups) {
		writeError(w, http.StatusBadRequest, "invalid_input", "this repository has no "+h.noun)
		return nil
	}

	return repo
}

func (h *signingKeyAdmin[I]) handleGet(w http.ResponseWriter, r *http.Request) {
	repo := h.repo(w, r)
	if repo == nil {
		return
	}

	info, err := h.keys.Info(r.Context(), repo)
	if err != nil {
		h.writeError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, h.respond(info))
}

// handleReplace rotates the key: an empty body generates a new one, a body with
// the private key imports it (key_name optionally names it).
func (h *signingKeyAdmin[I]) handleReplace(w http.ResponseWriter, r *http.Request) {
	repo := h.repo(w, r)
	if repo == nil {
		return
	}

	var body struct {
		PrivateKey string `json:"private_key"`
		KeyName    string `json:"key_name"`
	}

	if err := decodeJSON(r, &body); err != nil && !errors.Is(err, io.EOF) {
		writeBadJSON(w, err)
		return
	}

	by := currentPrincipal(r).Username

	var (
		info *I
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

	auditAdmin(r, audit.ActionRepositoryUpdated, "repository", repo.ID, h.noun+" of "+repo.Name+" replaced, "+h.describe(info))

	writeJSON(w, http.StatusOK, h.respond(info))
}

func (h *signingKeyAdmin[I]) writeError(w http.ResponseWriter, err error) {
	if h.mapError(w, err) {
		return
	}

	h.log.Errorw(h.noun+" operation failed", "error", err)
	writeError(w, http.StatusInternalServerError, "internal_error", h.noun+" operation failed")
}

// keyErrorMapper answers the errors every signing key service shares: no
// OPENARTIFACT_SECRET_KEY to seal a key with (409) and an unusable imported key (400).
func keyErrorMapper(format string, required, invalid error) func(http.ResponseWriter, error) bool {
	return func(w http.ResponseWriter, err error) bool {
		switch {
		case errors.Is(err, required):
			writeError(w, http.StatusConflict, "secret_key_required",
				"OPENARTIFACT_SECRET_KEY must be set: "+format+" signing keys are only stored encrypted")
		case errors.Is(err, invalid):
			writeError(w, http.StatusBadRequest, "validation_failed", err.Error())
		default:
			return false
		}

		return true
	}
}
