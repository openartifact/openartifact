// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/openartifact/openartifact/internal/attestation"
	"github.com/openartifact/openartifact/internal/audit"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/pkg/logger"
)

// sha256DigestRe is an OCI digest as the attestation index keys it.
var sha256DigestRe = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

// dockerRepoFormat is the format of the repositories attestations are attached to.
const dockerRepoFormat = "docker"

// OCIAttestationsHandler serves the attestations (signatures, SLSA provenance,
// SBOMs) attached to the images of a hosted docker repository, and the trust
// configuration they are verified against.
type OCIAttestationsHandler struct {
	repoAccess

	svc *attestation.Service
}

func NewOCIAttestationsHandler(
	db repoAccessStore,
	log *logger.Logger,
	evaluator *auth.Evaluator,
	svc *attestation.Service,
) *OCIAttestationsHandler {
	return &OCIAttestationsHandler{repoAccess: newRepoAccess(db, log, evaluator), svc: svc}
}

func (h *OCIAttestationsHandler) RegisterAuthed(r chi.Router) {
	r.Get("/api/v1/repositories/{name}/attestations", h.handleList)
	r.Get("/api/v1/repositories/{name}/attestations/{digest}", h.handleGet)
	r.Post("/api/v1/repositories/{name}/attestations/reverify", h.handleReverify)
	r.Get("/api/v1/repositories/{name}/attestation-trust", h.handleGetTrust)
	r.Put("/api/v1/repositories/{name}/attestation-trust", h.handleSetTrust)
}

func attestationToMap(a *models.OCIAttestation) map[string]any {
	return map[string]any{
		"digest":          a.AttestationDigest,
		"subject":         a.SubjectDigest,
		"image":           a.Image,
		"kind":            a.Kind,
		"predicate_type":  a.PredicateType,
		"signer_identity": a.SignerIdentity,
		"issuer":          a.Issuer,
		"verified":        a.Verified,
		"verify_error":    a.VerifyError,
		"created_at":      formatTime(a.CreatedAt),
	}
}

func (h *OCIAttestationsHandler) handleList(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.loadRepo(w, r, "list attestations")
	if !ok {
		return
	}

	q := r.URL.Query()

	subject := q.Get("subject")
	if !sha256DigestRe.MatchString(subject) {
		writeError(w, http.StatusBadRequest, "validation_failed", "subject must be an image digest (sha256:<64 hex>)")
		return
	}

	rows, err := h.svc.List(r.Context(), repo.ID, subject, q.Get("image"))
	if err != nil {
		h.dbError(w, "list attestations: db error", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"attestations": mapList(rows, attestationToMap)})
}

// handleGet answers the in-toto statement of an attachment, decoded.
func (h *OCIAttestationsHandler) handleGet(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.loadRepo(w, r, "get attestation")
	if !ok {
		return
	}

	digest := urlparams.Vars(r)["digest"]
	if !sha256DigestRe.MatchString(digest) {
		writeError(w, http.StatusBadRequest, "validation_failed", "digest must be an attestation digest (sha256:<64 hex>)")
		return
	}

	rows, err := h.svc.Lookup(r.Context(), repo.ID, digest)
	if err != nil {
		h.dbError(w, "get attestation: db error", err)
		return
	}

	image := r.URL.Query().Get("image")

	var row *models.OCIAttestation

	for _, c := range rows {
		if image == "" || c.Image == image {
			row = c
			break
		}
	}

	if row == nil {
		writeError(w, http.StatusNotFound, "not_found", "attestation not found")
		return
	}

	st, err := h.svc.Statement(r.Context(), row)
	if errors.Is(err, attestation.ErrNoStatement) {
		writeError(w, http.StatusNotFound, "not_found", "the attachment carries no readable in-toto statement")
		return
	}

	if err != nil {
		h.dbError(w, "get attestation: read statement", err)
		return
	}

	writeJSON(w, http.StatusOK, st)
}

// handleReverify judges the indexed attachments again against the current trust
// configuration. subject limits it to one image digest, image to one image.
func (h *OCIAttestationsHandler) handleReverify(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.repoThenRepoAdmin(w, r, "reverify attestations")
	if !ok {
		return
	}

	q := r.URL.Query()

	subject := q.Get("subject")
	if subject != "" && !sha256DigestRe.MatchString(subject) {
		writeError(w, http.StatusBadRequest, "validation_failed", "subject must be an image digest (sha256:<64 hex>)")
		return
	}

	if repo.Format != dockerRepoFormat {
		writeError(w, http.StatusBadRequest, "validation_failed", "attestations exist for docker repositories only")
		return
	}

	n, err := h.svc.Reverify(r.Context(), repo.ID, subject, q.Get("image"))
	if err != nil {
		h.dbError(w, "reverify attestations", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"reverified": n})
}

func (h *OCIAttestationsHandler) handleGetTrust(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.repoThenRepoAdmin(w, r, "get attestation trust")
	if !ok {
		return
	}

	cfg, err := h.svc.TrustConfig(r.Context(), repo.ID)
	if err != nil {
		h.dbError(w, "get attestation trust", err)
		return
	}

	writeJSON(w, http.StatusOK, cfg)
}

func (h *OCIAttestationsHandler) handleSetTrust(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.repoThenRepoAdmin(w, r, "set attestation trust")
	if !ok {
		return
	}

	if repo.Format != dockerRepoFormat {
		writeError(w, http.StatusBadRequest, "validation_failed", "attestation trust applies to docker repositories only")
		return
	}

	var cfg attestation.TrustConfig
	if err := decodeJSON(r, &cfg); err != nil {
		writeBadJSON(w, err)
		return
	}

	if err := h.svc.SetTrustConfig(r.Context(), repo.ID, &cfg); err != nil {
		if errors.Is(err, attestation.ErrInvalidTrust) {
			writeError(
				w,
				http.StatusBadRequest,
				"validation_failed",
				strings.TrimPrefix(err.Error(), attestation.ErrInvalidTrust.Error()+": "),
			)
			return
		}

		h.dbError(w, "set attestation trust", err)

		return
	}

	auditRepository(
		r,
		repo,
		audit.ActionRepositoryUpdated,
		"repository",
		repo.ID,
		"attestation trust configuration of "+repo.Name+" changed",
	)

	saved, err := h.svc.TrustConfig(r.Context(), repo.ID)
	if err != nil {
		h.dbError(w, "set attestation trust", err)
		return
	}

	writeJSON(w, http.StatusOK, saved)
}
