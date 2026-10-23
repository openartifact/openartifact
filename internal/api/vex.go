// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/openartifact/openartifact/internal/api/responses"
	"github.com/openartifact/openartifact/internal/attest"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/sbom"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/pkg/logger"
)

const (
	maxVulnIDLen    = 255
	maxStatementLen = 4096
)

// VEXHandler serves VEX documents built from the scan results and manages the
// admin VEX decisions.
type VEXHandler struct {
	db        database.Database
	log       *logger.Logger
	evaluator *auth.Evaluator
	generator *sbom.Generator
	attest    *attest.Service
}

// WithAttestation enables signed documents (`?attest=true`).
func (h *VEXHandler) WithAttestation(svc *attest.Service) *VEXHandler {
	h.attest = svc
	return h
}

func NewVEXHandler(db database.Database, log *logger.Logger, evaluator *auth.Evaluator, generator *sbom.Generator) *VEXHandler {
	return &VEXHandler{db: db, log: log, evaluator: evaluator, generator: generator}
}

func (h *VEXHandler) RegisterAuthed(r chi.Router) {
	r.Get("/api/v1/repositories/{name}/vex", h.handleDocument)
	r.Get("/api/v1/repositories/{name}/vex/statements", h.handleListStatements)
	r.Put("/api/v1/repositories/{name}/vex/statements", h.handleUpsertStatement)
	r.Delete("/api/v1/repositories/{name}/vex/statements/{id}", h.handleDeleteStatement)
}

// adminRepo resolves the repository of the request and requires an admin
// principal; on failure it has already written the response.
func (h *VEXHandler) adminRepo(w http.ResponseWriter, r *http.Request, op string) (*models.Repository, bool) {
	ctx := r.Context()

	repo, err := h.db.GetRepositoryByName(ctx, urlparams.Vars(r)["name"])
	if err != nil {
		h.log.Errorw(op+": db error getting repo", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return nil, false
	}

	if repo == nil {
		writeError(w, http.StatusNotFound, "not_found", "repository not found")
		return nil, false
	}

	isAdmin, err := principalIsAdmin(ctx, currentPrincipal(r), h.evaluator)
	if err != nil || !isAdmin {
		writeError(w, http.StatusForbidden, "forbidden", "admin access required")
		return nil, false
	}

	return repo, true
}

// handleDocument returns the OpenVEX (default) or CycloneDX VEX document of
// one artifact (?artifact_id=) or of every scanned artifact of the repository.
func (h *VEXHandler) handleDocument(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.adminRepo(w, r, "vex document")
	if !ok {
		return
	}

	ctx := r.Context()
	q := r.URL.Query()

	format := q.Get("format")
	if format == "" {
		format = sbom.VEXOpenVEX
	}

	if format != sbom.VEXOpenVEX && format != sbom.VEXCycloneDX {
		writeError(w, http.StatusBadRequest, "validation_failed", "format must be 'openvex' or 'cyclonedx'")
		return
	}

	mode, ok := parseAttest(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "validation_failed", "attest must be 'true' or 'bundle'")
		return
	}

	specVersion := q.Get("spec_version")
	if specVersion != "" && (format != sbom.VEXCycloneDX || !sbom.CycloneDXVersionSupported(specVersion)) {
		writeError(w, http.StatusBadRequest, "validation_failed", "spec_version applies to cyclonedx only and must be '1.4', '1.5' or '1.6'")
		return
	}

	var (
		inputs []*sbom.Input
		err    error
	)

	if artifactID := q.Get("artifact_id"); artifactID != "" {
		if msg := validateArtifactID(artifactID); msg != "" {
			writeError(w, http.StatusBadRequest, "validation_failed", msg)
			return
		}

		var metadata map[string]any

		metadata, err = storedArtifactMetadata(ctx, h.db, repo.ID, artifactID)
		if err == nil {
			var in *sbom.Input

			in, err = h.generator.VEXInput(ctx, repo.ID, repo.Format, artifactID, metadata)
			inputs = []*sbom.Input{in}
		}
	} else {
		inputs, err = h.generator.VEXInputs(ctx, repo.ID, repo.Format)
	}

	if err != nil {
		h.log.Errorw("vex document: loading inputs failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	var result *sbom.SBOMResult
	if format == sbom.VEXOpenVEX {
		result, err = h.generator.GenerateOpenVEX(inputs)
	} else {
		result, err = h.generator.GenerateCycloneDXVEX(inputs, specVersion)
	}

	if err != nil {
		h.log.Errorw("vex document: generation failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to generate VEX document")

		return
	}

	if mode != attestNone {
		h.writeAttested(ctx, w, format, result, inputs, mode)
		return
	}

	w.Header().Set("Content-Type", result.ContentType)
	w.Header().Set("Content-Disposition", "attachment; filename=vex."+format+".json")
	w.WriteHeader(http.StatusOK)

	//nolint:gosec // G705: a JSON document served as an attachment, not HTML
	if _, err := w.Write(result.Body); err != nil {
		h.log.Warnw("vex document: failed to write response", "error", err)
	}
}

// writeAttested serves the VEX document as a signed in-toto attestation. Its
// subjects are the packages the document speaks about whose sha256 is known;
// a VEX document is regenerated on every request, so when none is known the
// subject is the digest of the signed document itself (the attestation's
// predicate).
func (h *VEXHandler) writeAttested(ctx context.Context, w http.ResponseWriter, format string, result *sbom.SBOMResult,
	inputs []*sbom.Input, mode attestMode,
) {
	predicateType := attest.PredicateOpenVEX
	if format == sbom.VEXCycloneDX {
		predicateType = attest.PredicateCycloneDX
	}

	var subjects []attest.Subject

	for _, in := range inputs {
		if in.SHA256 == "" {
			continue
		}

		name := in.PURL
		if name == "" {
			name = in.ArtifactID
		}

		subjects = append(subjects, attest.SHA256Subject(name, in.SHA256))
	}

	file := "vex." + format + ".json"
	if len(subjects) == 0 {
		sum := sha256.Sum256(result.Body)
		subjects = append(subjects, attest.SHA256Subject(file, hex.EncodeToString(sum[:])))
	}

	attestedResponse(ctx, w, h.log, h.attest, mode, predicateType, result.Body, subjects, file+"."+mode.extension())
}

func vexResponse(s *models.VEXStatement) responses.VEXStatementResponse {
	return responses.VEXStatementResponse{
		ID:              s.ID,
		ArtifactID:      s.ArtifactID,
		VulnID:          s.VulnID,
		Status:          s.Status,
		Justification:   s.Justification,
		ImpactStatement: s.ImpactStatement,
		ActionStatement: s.ActionStatement,
		CreatedBy:       s.CreatedBy,
		CreatedAt:       formatTime(s.CreatedAt),
		UpdatedAt:       formatTime(s.UpdatedAt),
	}
}

func (h *VEXHandler) handleListStatements(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.adminRepo(w, r, "list vex statements")
	if !ok {
		return
	}

	stmts, err := h.db.ListVEXStatements(r.Context(), repo.ID, r.URL.Query().Get("artifact_id"))
	if err != nil {
		h.log.Errorw("list vex statements: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	out := make([]responses.VEXStatementResponse, 0, len(stmts))
	for _, s := range stmts {
		out = append(out, vexResponse(s))
	}

	writeJSON(w, http.StatusOK, map[string]any{"statements": out})
}

// validateVEXStatement returns a validation message for an unusable decision,
// or "" when it can be recorded. "not_affected" needs a justification from the
// OpenVEX enum; any other status must not carry one.
func validateVEXStatement(s *models.VEXStatement) string {
	if msg := validateArtifactID(s.ArtifactID); msg != "" {
		return msg
	}

	switch {
	case s.VulnID == "" || len(s.VulnID) > maxVulnIDLen || strings.ContainsAny(s.VulnID, " \t\r\n") || !isPrintable(s.VulnID):
		return "vuln_id is required (e.g. CVE-2024-1234), without whitespace or control characters"
	case !sbom.ValidVEXStatus(s.Status):
		return "status must be one of: affected, not_affected, fixed, under_investigation"
	case s.Status == models.VEXNotAffected && !sbom.ValidVEXJustification(s.Justification):
		return "justification is required for not_affected and must be one of: component_not_present, " +
			"vulnerable_code_not_present, vulnerable_code_not_in_execute_path, " +
			"vulnerable_code_cannot_be_controlled_by_adversary, inline_mitigations_already_exist"
	case s.Status != models.VEXNotAffected && s.Justification != "":
		return "justification is only valid for not_affected"
	case len(s.ImpactStatement) > maxStatementLen || len(s.ActionStatement) > maxStatementLen:
		return "impact_statement and action_statement are too long"
	case !isPrintable(s.ImpactStatement + s.ActionStatement):
		return "statements must not contain control characters"
	}

	return ""
}

func isPrintable(s string) bool {
	for _, r := range s {
		if (r < 0x20 && r != '\n' && r != '\t' && r != '\r') || r == 0x7f {
			return false
		}
	}

	return true
}

// handleUpsertStatement records (or replaces) the decision for one
// (artifact_id, vuln_id) of the repository.
func (h *VEXHandler) handleUpsertStatement(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.adminRepo(w, r, "record vex statement")
	if !ok {
		return
	}

	var body struct {
		ArtifactID      string `json:"artifact_id"`
		VulnID          string `json:"vuln_id"`
		Status          string `json:"status"`
		Justification   string `json:"justification"`
		ImpactStatement string `json:"impact_statement"`
		ActionStatement string `json:"action_statement"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}

	stmt := &models.VEXStatement{
		RepositoryID:    repo.ID,
		ArtifactID:      body.ArtifactID,
		VulnID:          strings.TrimSpace(body.VulnID),
		Status:          body.Status,
		Justification:   body.Justification,
		ImpactStatement: strings.TrimSpace(body.ImpactStatement),
		ActionStatement: strings.TrimSpace(body.ActionStatement),
	}

	if p := currentPrincipal(r); p != nil {
		stmt.CreatedBy = p.Username
	}

	if msg := validateVEXStatement(stmt); msg != "" {
		writeError(w, http.StatusBadRequest, "validation_failed", msg)
		return
	}

	if err := h.db.UpsertVEXStatement(r.Context(), stmt); err != nil {
		h.log.Errorw("record vex statement: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	writeJSON(w, http.StatusOK, vexResponse(stmt))
}

func (h *VEXHandler) handleDeleteStatement(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.adminRepo(w, r, "delete vex statement")
	if !ok {
		return
	}

	id := urlparams.Vars(r)["id"]
	if !isUUIDString(id) {
		writeError(w, http.StatusNotFound, "not_found", "VEX statement not found")
		return
	}

	if err := h.db.DeleteVEXStatement(r.Context(), repo.ID, id); err != nil {
		if errors.Is(err, database.ErrVEXStatementNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "VEX statement not found")
			return
		}

		h.log.Errorw("delete vex statement: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	writeNoContent(w)
}
