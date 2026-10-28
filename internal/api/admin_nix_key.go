// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/nixkeys"
	"github.com/openartifact/openartifact/pkg/logger"
)

// NixKeyHandler manages the ed25519 key that signs the narinfo of a Nix
// repository (docs/formats/nix.md). Every route needs an admin.
type NixKeyHandler struct {
	signingKeyAdmin[nixkeys.Info]
}

// NewNixKeyHandler builds the handler.
func NewNixKeyHandler(db database.Database, log *logger.Logger, evaluator *auth.Evaluator, keys *nixkeys.Service) *NixKeyHandler {
	return &NixKeyHandler{signingKeyAdmin[nixkeys.Info]{
		db: db, log: log, evaluator: evaluator, keys: keys,
		format: "nix", route: "nix-key", noun: "nix signing key",
		respond:  func(i *nixkeys.Info) any { return nixKeyResponseOf(i) },
		describe: func(i *nixkeys.Info) string { return "key " + i.KeyName },
		mapError: func(w http.ResponseWriter, err error) bool {
			switch {
			case errors.Is(err, nixkeys.ErrSecretKeyRequired):
				writeError(w, http.StatusConflict, "secret_key_required",
					"OPENARTIFACT_SECRET_KEY must be set: nix signing keys are only stored encrypted")
			case errors.Is(err, nixkeys.ErrInvalidKey):
				writeError(w, http.StatusBadRequest, "validation_failed", err.Error())
			default:
				return false
			}

			return true
		},
	}}
}

// Register registers the routes on the authenticated REST router.
func (h *NixKeyHandler) Register(r chi.Router) { h.register(r) }

// nixKeyResponse is the API shape of a signing key. It never carries the private key.
type nixKeyResponse struct {
	KeyName   string    `json:"key_name"`
	PublicKey string    `json:"public_key"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func nixKeyResponseOf(info *nixkeys.Info) nixKeyResponse {
	return nixKeyResponse{KeyName: info.KeyName, PublicKey: info.PublicKey, CreatedAt: info.CreatedAt, UpdatedAt: info.UpdatedAt}
}
