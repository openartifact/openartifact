// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/apkkeys"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/pkg/logger"
)

// ApkKeyHandler manages the RSA key that signs the APKINDEX of an apk
// repository (docs/formats/apk.md). Every route needs an admin.
type ApkKeyHandler struct {
	signingKeyAdmin[apkkeys.Info]
}

// NewApkKeyHandler builds the handler.
func NewApkKeyHandler(db database.Database, log *logger.Logger, evaluator *auth.Evaluator, keys *apkkeys.Service) *ApkKeyHandler {
	return &ApkKeyHandler{signingKeyAdmin[apkkeys.Info]{
		db: db, log: log, evaluator: evaluator, keys: keys,
		format: "apk", route: "apk-key", noun: "apk signing key", groups: true,
		respond:  func(i *apkkeys.Info) any { return apkKeyResponseOf(i) },
		describe: func(i *apkkeys.Info) string { return "fingerprint " + i.Fingerprint },
		mapError: keyErrorMapper("apk", apkkeys.ErrSecretKeyRequired, apkkeys.ErrInvalidKey),
	}}
}

// Register registers the routes on the authenticated REST router.
func (h *ApkKeyHandler) Register(r chi.Router) { h.register(r) }

// apkKeyResponse is the API shape of a signing key. It never carries the private key.
type apkKeyResponse struct {
	KeyName     string    `json:"key_name"`
	Fingerprint string    `json:"fingerprint"`
	PublicKey   string    `json:"public_key"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func apkKeyResponseOf(info *apkkeys.Info) apkKeyResponse {
	return apkKeyResponse{
		KeyName: info.KeyName, Fingerprint: info.Fingerprint, PublicKey: info.PublicKey,
		CreatedAt: info.CreatedAt, UpdatedAt: info.UpdatedAt,
	}
}
