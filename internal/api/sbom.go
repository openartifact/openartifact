// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/api/responses"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/sbom"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/pkg/logger"
)

type SBOMHandler struct {
	db        database.Database
	log       *logger.Logger
	evaluator *auth.Evaluator
	generator *sbom.Generator
	blobStore storage.BlobStore
}

func NewSBOMHandler(db database.Database, log *logger.Logger, evaluator *auth.Evaluator, generator *sbom.Generator, blobStore storage.BlobStore) *SBOMHandler {
	return &SBOMHandler{
		db:        db,
		log:       log,
		evaluator: evaluator,
		generator: generator,
		blobStore: blobStore,
	}
}

func (h *SBOMHandler) RegisterAuthed(r chi.Router) {
	r.Post("/api/v1/repositories/{name}/sbom/generate", h.handleGenerate)
	r.Get("/api/v1/repositories/{name}/sbom", h.handleList)
	r.Get("/api/v1/repositories/{name}/sbom/{id}", h.handleGet)
	r.Get("/api/v1/repositories/{name}/sbom/{id}/download", h.handleDownload)
	r.Delete("/api/v1/repositories/{name}/sbom/{id}", h.handleDelete)
}

// validateSBOMRequest returns a validation message for an invalid SBOM
// generation request, or "" when the request is valid. An empty specVersion is
// accepted and means the format's default (see defaultSpecVersion).
func validateSBOMRequest(artifactID, format, specVersion string) string {
	if msg := validateArtifactID(artifactID); msg != "" {
		return msg
	}

	switch {
	case format == "":
		return "format is required"
	case format != "cyclonedx" && format != "spdx" && format != "spdx-json":
		return "format must be 'cyclonedx', 'spdx' or 'spdx-json'"
	case format == "cyclonedx" && specVersion != "" && !sbom.CycloneDXVersionSupported(specVersion):
		return "CycloneDX spec_version must be '1.4', '1.5' or '1.6'"
	case format != "cyclonedx" && specVersion != "" && specVersion != sbom.SPDX23:
		return "SPDX spec_version must be '2.3'"
	}

	return ""
}

// sbomFileExtension is the download file extension of a stored SBOM format.
func sbomFileExtension(format string) string {
	switch format {
	case "cyclonedx":
		return "cdx.json"
	case "spdx-json":
		return "spdx.json"
	case "spdx":
		return "spdx"
	default:
		return format
	}
}

func (h *SBOMHandler) handleGenerate(w http.ResponseWriter, r *http.Request) {
	name := urlparams.Vars(r)["name"]
	ctx := r.Context()

	repo, err := h.db.GetRepositoryByName(ctx, name)
	if err != nil {
		h.log.Errorw("generate SBOM: db error getting repo", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if repo == nil {
		writeError(w, http.StatusNotFound, "not_found", "repository not found")
		return
	}

	p := currentPrincipal(r)

	isAdmin, err := principalIsAdmin(ctx, p, h.evaluator)
	if err != nil || !isAdmin {
		writeError(w, http.StatusForbidden, "forbidden", "admin access required")
		return
	}

	var body struct {
		ArtifactID  string         `json:"artifact_id"`
		Format      string         `json:"format"`
		SpecVersion string         `json:"spec_version"`
		Metadata    map[string]any `json:"metadata"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}

	if msg := validateSBOMRequest(body.ArtifactID, body.Format, body.SpecVersion); msg != "" {
		writeError(w, http.StatusBadRequest, "validation_failed", msg)
		return
	}

	// Caller-supplied metadata would let a report claim arbitrary components;
	// the SBOM is built from the artifact's stored properties only.
	if len(body.Metadata) > 0 {
		writeError(w, http.StatusBadRequest, "validation_failed",
			"metadata is not accepted; set the artifact's dependencies property instead")

		return
	}

	metadata, err := storedArtifactMetadata(ctx, h.db, repo.ID, body.ArtifactID)
	if err != nil {
		h.log.Errorw("generate SBOM: db error reading artifact properties", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	// Generate SBOM
	var (
		result *sbom.SBOMResult
		genErr error
	)

	switch body.Format {
	case "cyclonedx":
		result, genErr = h.generator.GenerateCycloneDX(ctx, repo.ID, body.ArtifactID, repo.Format, metadata, body.SpecVersion)
	case "spdx-json":
		result, genErr = h.generator.GenerateSPDXJSON(ctx, repo.ID, body.ArtifactID, repo.Format, metadata)
	default:
		result, genErr = h.generator.GenerateSPDX(ctx, repo.ID, body.ArtifactID, repo.Format, metadata)
	}

	if errors.Is(genErr, sbom.ErrUnsafeArtifact) {
		writeError(w, http.StatusUnprocessableEntity, "unsafe_artifact",
			"the artifact's manifest is too large or ambiguous to be read safely")

		return
	}

	if genErr != nil {
		h.log.Errorw("generate SBOM: generation error", "error", genErr)
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to generate SBOM")

		return
	}

	// The generator returns the exact bytes handleDownload will later serve;
	// they are stored in the blob store content-addressed by sha256 —
	// sbom_results only carries a blob_id pointer (see CreateSBOMReport).
	data := result.Body

	sum := sha256.Sum256(data)
	sha256Hex := hex.EncodeToString(sum[:])

	report := &models.SBOMReport{
		RepositoryID:   repo.ID,
		ArtifactID:     body.ArtifactID,
		Format:         result.Format,
		SpecVersion:    result.SpecVersion,
		ContentType:    result.ContentType,
		SBOMData:       result.Data,
		SBOMText:       result.Text,
		BlobSHA256:     sha256Hex,
		BlobSizeBytes:  int64(len(data)),
		ComponentCount: result.Components,
		GeneratedAt:    time.Now(),
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}

	// Store the blob and insert the referencing row under the per-blob lock so
	// a concurrent SafeDeleteBlob (e.g. deleting an identical report) cannot
	// remove the blob between the two steps.
	var putFailed bool

	err = storage.WithBlobLock(ctx, h.db, sha256Hex, func(ctx context.Context) error {
		if _, err := h.blobStore.Put(ctx, bytes.NewReader(data), map[string]string{
			"sha256":       sha256Hex,
			"content-type": result.ContentType,
			"format":       "sbom",
		}); err != nil {
			putFailed = true
			return err //nolint:wrapcheck // logged by the caller below
		}

		return h.db.CreateSBOMReport(ctx, report)
	})
	if err != nil {
		if putFailed {
			h.log.Errorw("generate SBOM: failed to store blob", "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "failed to store SBOM content")

			return
		}

		h.log.Errorw("generate SBOM: db error creating report", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	writeJSON(w, http.StatusCreated, reportToMap(report))
}

//nolint:dupl // handler boilerplate: identical structure, different entities
func (h *SBOMHandler) handleList(w http.ResponseWriter, r *http.Request) {
	name := urlparams.Vars(r)["name"]
	ctx := r.Context()

	repo, err := h.db.GetRepositoryByName(ctx, name)
	if err != nil {
		h.log.Errorw("list SBOMs: db error getting repo", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if repo == nil {
		writeError(w, http.StatusNotFound, "not_found", "repository not found")
		return
	}

	p := currentPrincipal(r)

	isAdmin, err := principalIsAdmin(ctx, p, h.evaluator)
	if err != nil || !isAdmin {
		writeError(w, http.StatusForbidden, "forbidden", "admin access required")
		return
	}

	reports, err := h.db.ListSBOMReports(ctx, repo.ID)
	if err != nil {
		h.log.Errorw("list SBOMs: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if reports == nil {
		reports = []*models.SBOMReport{}
	}

	var result []responses.SBOMReportResponse
	for _, report := range reports {
		result = append(result, reportToMap(report))
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"reports": result,
	})
}

//nolint:dupl // handler boilerplate: identical structure, different entities
func (h *SBOMHandler) handleGet(w http.ResponseWriter, r *http.Request) {
	name := urlparams.Vars(r)["name"]
	id := urlparams.Vars(r)["id"]
	ctx := r.Context()

	repo, err := h.db.GetRepositoryByName(ctx, name)
	if err != nil {
		h.log.Errorw("get SBOM: db error getting repo", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if repo == nil {
		writeError(w, http.StatusNotFound, "not_found", "repository not found")
		return
	}

	p := currentPrincipal(r)

	isAdmin, err := principalIsAdmin(ctx, p, h.evaluator)
	if err != nil || !isAdmin {
		writeError(w, http.StatusForbidden, "forbidden", "admin access required")
		return
	}

	if !isUUIDString(id) {
		writeError(w, http.StatusNotFound, "not_found", "SBOM report not found")
		return
	}

	report, err := h.db.GetSBOMReportByID(ctx, repo.ID, id)
	if err != nil {
		h.log.Errorw("get SBOM: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if report == nil {
		writeError(w, http.StatusNotFound, "not_found", "SBOM report not found")
		return
	}

	writeJSON(w, http.StatusOK, reportToMap(report))
}

func (h *SBOMHandler) handleDownload(w http.ResponseWriter, r *http.Request) {
	name := urlparams.Vars(r)["name"]
	id := urlparams.Vars(r)["id"]
	ctx := r.Context()

	repo, err := h.db.GetRepositoryByName(ctx, name)
	if err != nil {
		h.log.Errorw("download SBOM: db error getting repo", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if repo == nil {
		writeError(w, http.StatusNotFound, "not_found", "repository not found")
		return
	}

	p := currentPrincipal(r)

	isAdmin, err := principalIsAdmin(ctx, p, h.evaluator)
	if err != nil || !isAdmin {
		writeError(w, http.StatusForbidden, "forbidden", "admin access required")
		return
	}

	if !isUUIDString(id) {
		writeError(w, http.StatusNotFound, "not_found", "SBOM report not found")
		return
	}

	report, err := h.db.GetSBOMReportByID(ctx, repo.ID, id)
	if err != nil {
		h.log.Errorw("download SBOM: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if report == nil {
		writeError(w, http.StatusNotFound, "not_found", "SBOM report not found")
		return
	}

	if report.BlobSHA256 == "" {
		writeError(w, http.StatusInternalServerError, "internal_error", "SBOM content not available")
		return
	}

	rc, err := h.blobStore.Get(ctx, report.BlobSHA256)
	if err != nil {
		h.log.Errorw("download SBOM: blob fetch failed", "sha256", report.BlobSHA256, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to read SBOM content")

		return
	}
	defer func() { _ = rc.Close() }()

	// The blob already holds the exact bytes generated at SBOM creation
	// time (JSON for cyclonedx, plain text for spdx), so stream it as-is
	// instead of re-decoding/re-encoding.
	w.Header().Set("Content-Type", report.ContentType)
	w.Header().Set("Content-Disposition", "attachment; filename=sbom."+sbomFileExtension(report.Format))
	w.WriteHeader(http.StatusOK)

	if _, err := io.Copy(w, rc); err != nil {
		h.log.Warnw("download SBOM: failed to stream content", "sha256", report.BlobSHA256, "error", err)
	}
}

func (h *SBOMHandler) handleDelete(w http.ResponseWriter, r *http.Request) {
	name := urlparams.Vars(r)["name"]
	id := urlparams.Vars(r)["id"]
	ctx := r.Context()

	repo, err := h.db.GetRepositoryByName(ctx, name)
	if err != nil {
		h.log.Errorw("delete SBOM: db error getting repo", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if repo == nil {
		writeError(w, http.StatusNotFound, "not_found", "repository not found")
		return
	}

	p := currentPrincipal(r)

	isAdmin, err := principalIsAdmin(ctx, p, h.evaluator)
	if err != nil || !isAdmin {
		writeError(w, http.StatusForbidden, "forbidden", "admin access required")
		return
	}

	if !isUUIDString(id) {
		writeError(w, http.StatusNotFound, "not_found", "SBOM report not found")
		return
	}

	// Delete scoped to this repository; returns the blob the report referenced.
	blobSHA, err := h.db.DeleteSBOMReportInRepo(ctx, repo.ID, id)
	if err != nil {
		if errors.Is(err, database.ErrSBOMReportNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "SBOM report not found")
			return
		}

		h.log.Errorw("delete SBOM: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	// Remove the blob unless another report, bundle item, or artifact still
	// references it. The report is already gone, so a failure here only leaks
	// storage; log it but still report success.
	if blobSHA != "" {
		if err := storage.SafeDeleteBlob(ctx, h.db, h.blobStore, blobSHA); err != nil {
			h.log.Warnw("delete SBOM: failed to remove unreferenced blob", "sha256", blobSHA, "error", err)
		}
	}

	writeNoContent(w)
}

func reportToMap(report *models.SBOMReport) responses.SBOMReportResponse {
	return responses.SBOMReportResponse{
		ID:             report.ID,
		RepositoryID:   report.RepositoryID,
		ArtifactID:     report.ArtifactID,
		Format:         report.Format,
		SpecVersion:    report.SpecVersion,
		ContentType:    report.ContentType,
		ComponentCount: report.ComponentCount,
		GeneratedAt:    formatTime(report.GeneratedAt),
		CreatedAt:      formatTime(report.CreatedAt),
		UpdatedAt:      formatTime(report.UpdatedAt),
		SBOMData:       report.SBOMData,
	}
}
