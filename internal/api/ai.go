// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/internal/worker"
	"github.com/openartifact/openartifact/pkg/logger"
)

// AIHandler handles AI analysis REST endpoints.
type AIHandler struct {
	db        database.Database
	log       *logger.Logger
	evaluator *auth.Evaluator
	aiWorker  *worker.AIAnalysisWorker
}

const (
	// aiMaxManifestBytes caps manifest_text forwarded to the LLM.
	aiMaxManifestBytes = 256 * 1024
	aiMaxNameLen       = 512
	aiMaxListOffset    = 1_000_000
)

// NewAIHandler creates a new AI analysis handler.
func NewAIHandler(db database.Database, log *logger.Logger, evaluator *auth.Evaluator, aiWorker *worker.AIAnalysisWorker) *AIHandler {
	return &AIHandler{db: db, log: log, evaluator: evaluator, aiWorker: aiWorker}
}

// RegisterRoutes registers AI analysis routes.
func (h *AIHandler) RegisterRoutes(router chi.Router, prefix string) {
	// GET /api/v1/repositories/{name}/ai-analysis — list analyses for a repo
	// GET /api/v1/repositories/{name}/ai-analysis/{artifactID} — get one analysis
	// POST /api/v1/repositories/{name}/ai-analysis/{artifactID}/trigger — trigger re-analysis
	router.Route(prefix, func(r chi.Router) {
		r.Get("/ai-analysis", h.handleList)
		r.Get("/ai-analysis/{artifactID}", h.handleGet)
		r.Post("/ai-analysis/{artifactID}/trigger", h.handleTrigger)
	})
}

// loadRepo resolves the repository named in the URL, writing the error
// response (404 only for a genuinely missing repository) on failure.
func (h *AIHandler) loadRepo(w http.ResponseWriter, r *http.Request) (*models.Repository, bool) {
	repoName := urlparams.Vars(r)["name"]

	repo, err := h.db.GetRepositoryByName(r.Context(), repoName)
	if err != nil {
		h.log.Errorw("ai handler: get repository failed", "repo", repoName, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return nil, false
	}

	if repo == nil {
		writeError(w, http.StatusNotFound, "not_found", "repository not found")
		return nil, false
	}

	return repo, true
}

func (h *AIHandler) handleList(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.loadRepo(w, r)
	if !ok {
		return
	}

	status := r.URL.Query().Get("status")
	limit := 20
	offset := 0

	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 100 {
			limit = n
		}
	}

	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = min(n, aiMaxListOffset)
		}
	}

	analyses, total, err := h.db.ListAIAnalyses(r.Context(), repo.ID, status, limit, offset)
	if err != nil {
		h.log.Errorw("ai handler: list analyses failed", "repo", repo.Name, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"analyses": analyses,
		"total":    total,
		"limit":    limit,
		"offset":   offset,
	})
}

func (h *AIHandler) handleGet(w http.ResponseWriter, r *http.Request) {
	artifactID := urlparams.Vars(r)["artifactID"]

	repo, ok := h.loadRepo(w, r)
	if !ok {
		return
	}

	analysis, err := h.db.GetAIAnalysis(r.Context(), repo.ID, artifactID)
	if err != nil {
		h.log.Errorw("ai handler: get analysis failed", "artifact_id", artifactID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if analysis == nil {
		writeError(w, http.StatusNotFound, "not_found", "analysis not found")
		return
	}

	writeJSON(w, http.StatusOK, analysis)
}

func (h *AIHandler) handleTrigger(w http.ResponseWriter, r *http.Request) {
	artifactID := urlparams.Vars(r)["artifactID"]

	if h.aiWorker == nil {
		writeError(w, http.StatusServiceUnavailable, "ai_disabled", "AI analysis is not enabled")
		return
	}

	repo, ok := h.loadRepo(w, r)
	if !ok {
		return
	}

	var body struct {
		ManifestText    string `json:"manifest_text"`
		ArtifactFormat  string `json:"artifact_format"`
		ArtifactName    string `json:"artifact_name"`
		ArtifactVersion string `json:"artifact_version"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}

	if len(body.ManifestText) > aiMaxManifestBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "payload_too_large", "manifest_text exceeds "+strconv.Itoa(aiMaxManifestBytes)+" bytes")
		return
	}

	if body.ArtifactFormat != "" && !validFormats[body.ArtifactFormat] {
		writeError(w, http.StatusBadRequest, "validation_failed", "artifact_format must be one of: "+validFormatList())
		return
	}

	if len(body.ArtifactName) > aiMaxNameLen || len(body.ArtifactVersion) > aiMaxNameLen {
		writeError(w, http.StatusBadRequest, "validation_failed", "artifact_name/artifact_version too long")
		return
	}

	h.aiWorker.Notify(worker.AIAnalysisRequest{
		RepositoryID:    repo.ID,
		ArtifactID:      artifactID,
		ArtifactFormat:  body.ArtifactFormat,
		ArtifactName:    body.ArtifactName,
		ArtifactVersion: body.ArtifactVersion,
		ManifestText:    body.ManifestText,
	})

	writeJSON(w, http.StatusAccepted, map[string]string{
		"status":  "queued",
		"message": "AI analysis has been queued",
	})
}
