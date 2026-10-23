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

	"github.com/openartifact/openartifact/internal/api/responses"
	"github.com/openartifact/openartifact/internal/attest"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/pkg/logger"
)

// AttestationKeyPath is where the public key verifying the attestations is served, unauthenticated.
const AttestationKeyPath = "/.well-known/openartifact/attestation-key.pem"

// maxAttestBytes bounds a document read into memory to be signed.
const maxAttestBytes = 64 << 20

// attestMode is the `attest` query parameter of SBOM and VEX downloads.
type attestMode int

const (
	attestNone     attestMode = iota
	attestEnvelope            // attest=true: a DSSE envelope (in-toto attestation)
	attestBundle              // attest=bundle: a Sigstore bundle (cosign v3's input)
)

// parseAttest reads the `attest` query parameter: "" and "false" mean none,
// "true" a DSSE envelope, "bundle" a Sigstore bundle.
func parseAttest(r *http.Request) (attestMode, bool) {
	switch r.URL.Query().Get("attest") {
	case "", "false":
		return attestNone, true
	case "true":
		return attestEnvelope, true
	case "bundle":
		return attestBundle, true
	default:
		return attestNone, false
	}
}

// attestExtension is the download file extension of an attestation.
func (m attestMode) extension() string {
	if m == attestBundle {
		return "sigstore.json"
	}

	return "intoto.json"
}

// attestedResponse signs predicate (a JSON document) about subjects and writes
// the envelope or bundle as a download named filename. It answers the request
// itself on every outcome.
func attestedResponse(ctx context.Context, w http.ResponseWriter, log *logger.Logger, svc *attest.Service,
	mode attestMode, predicateType string, predicate []byte, subjects []attest.Subject, filename string,
) {
	if svc == nil {
		writeError(w, http.StatusNotImplemented, "not_implemented", "attestations are not enabled")
		return
	}

	signer, err := svc.Signer(ctx)
	if err != nil {
		log.Errorw("attestation: signing key unavailable", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "attestation signing key unavailable")

		return
	}

	env, err := signer.Attest(predicateType, predicate, subjects)
	if err != nil {
		log.Errorw("attestation: signing failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to sign attestation")

		return
	}

	body, contentType, err := signer.Encode(env, mode == attestBundle)
	if err != nil {
		log.Errorw("attestation: encoding failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to encode attestation")

		return
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", "attachment; filename="+filename)
	w.WriteHeader(http.StatusOK)

	//nolint:gosec // G705: a JSON document served as an attachment, not HTML
	if _, err := w.Write(body); err != nil {
		log.Warnw("attestation: failed to write response", "error", err)
	}
}

// readLimited reads at most maxAttestBytes from r.
func readLimited(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxAttestBytes+1))
	if err != nil {
		return nil, err //nolint:wrapcheck // caller logs and maps to 500
	}

	if len(data) > maxAttestBytes {
		return nil, errAttestTooLarge
	}

	return data, nil
}

var errAttestTooLarge = errors.New("document too large to attest")

// AttestationHandler exposes the public key that verifies SBOM and VEX attestations.
type AttestationHandler struct {
	svc       *attest.Service
	log       *logger.Logger
	evaluator *auth.Evaluator
}

// NewAttestationHandler creates the handler; svc may be nil when attestations are unavailable.
func NewAttestationHandler(svc *attest.Service, log *logger.Logger, evaluator *auth.Evaluator) *AttestationHandler {
	return &AttestationHandler{svc: svc, log: log, evaluator: evaluator}
}

// RegisterPublic serves the PEM public key without authentication: a verifier
// has to be able to fetch it before it trusts anything.
func (h *AttestationHandler) RegisterPublic(r chi.Router) {
	r.Get(AttestationKeyPath, h.handlePublicKey)
}

// RegisterAuthed registers the admin description of the signing key.
func (h *AttestationHandler) RegisterAuthed(r chi.Router) {
	r.Get("/api/v1/admin/attestation", h.handleInfo)
}

func (h *AttestationHandler) handlePublicKey(w http.ResponseWriter, r *http.Request) {
	info, ok := h.info(w, r)
	if !ok {
		return
	}

	w.Header().Set("Content-Type", "application/x-pem-file")
	w.Header().Set("Content-Disposition", "attachment; filename=openartifact-attestation.pub")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(info.PublicKeyPEM))
}

func (h *AttestationHandler) handleInfo(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	info, ok := h.info(w, r)
	if !ok {
		return
	}

	writeJSON(w, http.StatusOK, responses.AttestationKeyResponse{
		KeyID: info.KeyID, Algorithm: info.Algorithm, Source: info.Source,
		PublicKeyPEM: info.PublicKeyPEM, PublicKeyURL: AttestationKeyPath,
	})
}

func (h *AttestationHandler) info(w http.ResponseWriter, r *http.Request) (*attest.Info, bool) {
	if h.svc == nil {
		writeError(w, http.StatusNotImplemented, "not_implemented", "attestations are not enabled")
		return nil, false
	}

	info, err := h.svc.Info(r.Context())
	if err != nil {
		h.log.Errorw("attestation: signing key unavailable", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "attestation signing key unavailable")

		return nil, false
	}

	return info, true
}
