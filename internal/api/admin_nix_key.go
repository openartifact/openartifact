// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/nixkeys"
	"github.com/openartifact/openartifact/pkg/logger"
)

// NixKeyHandler manages the ed25519 key that signs the narinfo of a Nix
// repository (docs/formats/nix.md). Every route needs an admin.
type NixKeyHandler struct {
	signingKeyAdmin[nixkeys.Info]
}

// NewNixKeyHandler builds the handler.
func NewNixKeyHandler(db signingKeyAdminStore, log *logger.Logger, evaluator *auth.Evaluator, keys *nixkeys.Service) *NixKeyHandler {
	h := &NixKeyHandler{}
	h.db, h.log, h.evaluator, h.keys = db, log, evaluator, keys
	h.format, h.route, h.noun, h.groups = "nix", "nix-key", "nix signing key", true
	h.respond = func(i *nixkeys.Info) any { return nixKeyResponseOf(i) }
	h.describe = func(i *nixkeys.Info) string { return "key " + i.KeyName }
	h.mapError = keyErrorMapper("nix", nixkeys.ErrSecretKeyRequired, nixkeys.ErrInvalidKey)

	return h
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
