// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/promote"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/internal/webhook"
	"github.com/openartifact/openartifact/pkg/logger"
)

type BundlesHandler struct {
	db        database.Database
	log       *logger.Logger
	evaluator *auth.Evaluator
	keys      BundleKeyResolver
	promoter  *promote.Engine
	publisher webhook.Publisher
}

const (
	maxBundleItemsPerRequest = 500
	maxPipelineStages        = 50
	maxFormatLen             = 50 // release_bundle_items.format is varchar(50)
)

// validateBundleLabel validates a pipeline/bundle name or version: names end up
// in URL path segments, so they must be non-empty, bounded, and free of path
// separators and control characters.
func validateBundleLabel(field, value string) string {
	switch {
	case value == "":
		return field + " is required"
	case len(value) > maxNameLen:
		return field + " is too long (max 255 bytes)"
	case !utf8.ValidString(value):
		return field + " must be valid UTF-8"
	case strings.ContainsAny(value, "/\\"):
		return field + " must not contain path separators"
	}

	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return field + " must not contain control characters"
		}
	}

	return ""
}

func isHexSHA256(s string) bool {
	if len(s) != 64 {
		return false
	}

	for _, c := range []byte(s) {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}

	return true
}

// validateStages checks pipeline stages: bounded count, unique valid names, and
// any referenced repository id must be an existing repository.
// It returns a client-facing message for invalid input, or an error for a
// database failure.
func (h *BundlesHandler) validateStages(ctx context.Context, stages []models.PipelineStage) (string, error) {
	if len(stages) > maxPipelineStages {
		return "too many stages (max " + strconv.Itoa(maxPipelineStages) + ")", nil
	}

	seen := make(map[string]struct{}, len(stages))

	for i, stage := range stages {
		if msg := validateBundleLabel("stage name", stage.Name); msg != "" {
			return "stages[" + strconv.Itoa(i) + "]: " + msg, nil
		}

		if _, dup := seen[stage.Name]; dup {
			return "duplicate stage name: " + stage.Name, nil
		}

		seen[stage.Name] = struct{}{}

		if stage.RepositoryID == "" {
			continue
		}

		if !isUUIDString(stage.RepositoryID) {
			return "stages[" + strconv.Itoa(i) + "]: repo_id must be a valid UUID", nil
		}

		repo, err := h.db.GetRepositoryByID(ctx, stage.RepositoryID)
		if err != nil {
			return "", err //nolint:wrapcheck // caller logs and maps to 500
		}

		if repo == nil {
			return "stages[" + strconv.Itoa(i) + "]: repo_id does not reference an existing repository", nil
		}
	}

	return "", nil
}

func NewBundlesHandler(db database.Database, log *logger.Logger, evaluator *auth.Evaluator) *BundlesHandler {
	return &BundlesHandler{db: db, log: log, evaluator: evaluator, promoter: promote.NewEngine(db, log, nil)}
}

// WithPublisher enables the bundle.promoted webhook event.
func (h *BundlesHandler) WithPublisher(p webhook.Publisher) *BundlesHandler {
	h.publisher = p
	return h
}

// WithRegenerator enqueues the refresh of generated metadata (deb, rpm and npm
// indexes) in the target repository after a promotion.
func (h *BundlesHandler) WithRegenerator(r TrashRegenerator) *BundlesHandler {
	h.promoter = promote.NewEngine(h.db, h.log, r)
	return h
}

func (h *BundlesHandler) RegisterAuthed(r chi.Router) {
	// Pipelines
	r.Get("/api/v1/pipelines", h.handleListPipelines)
	r.Post("/api/v1/pipelines", h.handleCreatePipeline)
	r.Get("/api/v1/pipelines/{name}", h.handleGetPipeline)
	r.Patch("/api/v1/pipelines/{name}", h.handleUpdatePipeline)
	r.Delete("/api/v1/pipelines/{name}", h.handleDeletePipeline)

	// Bundles
	r.Get("/api/v1/bundles", h.handleListBundles)
	r.Post("/api/v1/bundles", h.handleCreateBundle)
	r.Get("/api/v1/bundles/{name}/{version}", h.handleGetBundle)
	r.Delete("/api/v1/bundles/{name}/{version}", h.handleDeleteBundle)

	// Bundle items
	r.Get("/api/v1/bundles/{name}/{version}/items", h.handleListItems)
	r.Post("/api/v1/bundles/{name}/{version}/items", h.handleAddItems)
	r.Delete("/api/v1/bundles/{name}/{version}/items/{sha256}", h.handleRemoveItem)

	// Bundle lifecycle
	r.Post("/api/v1/bundles/{name}/{version}/seal", h.handleSeal)
	r.Get("/api/v1/bundles/{name}/{version}/manifest", h.handleGetManifest)
	r.Get("/api/v1/bundles/{name}/{version}/manifest.asc", h.handleGetManifestSignature)
	r.Post("/api/v1/bundles/{name}/{version}/promote", h.handlePromote)
	r.Post("/api/v1/bundles/{name}/{version}/archive", h.handleArchive)

	// Promotion history
	r.Get("/api/v1/bundles/{name}/{version}/promotions", h.handleListPromotions)
}

// ============================================================================
// Pipeline Handlers
// ============================================================================

func (h *BundlesHandler) handleListPipelines(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	p := currentPrincipal(r)

	isAdmin, err := principalIsAdmin(ctx, p, h.evaluator)
	if err != nil || !isAdmin {
		writeError(w, http.StatusForbidden, "forbidden", "admin access required")
		return
	}

	pipelines, err := h.db.ListPromotionPipelines(ctx)
	if err != nil {
		h.log.Errorw("list pipelines: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if pipelines == nil {
		pipelines = []*models.PromotionPipeline{}
	}

	var result []map[string]any
	for _, pipeline := range pipelines {
		result = append(result, pipelineToMap(pipeline))
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"pipelines": result,
	})
}

func (h *BundlesHandler) handleCreatePipeline(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	p := currentPrincipal(r)

	isAdmin, err := principalIsAdmin(ctx, p, h.evaluator)
	if err != nil || !isAdmin {
		writeError(w, http.StatusForbidden, "forbidden", "admin access required")
		return
	}

	var body struct {
		Name        string                 `json:"name"`
		Description string                 `json:"description"`
		Stages      []models.PipelineStage `json:"stages"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}

	if msg := validateBundleLabel("name", body.Name); msg != "" {
		writeError(w, http.StatusBadRequest, "validation_failed", msg)
		return
	}

	msg, err := h.validateStages(ctx, body.Stages)
	if err != nil {
		h.log.Errorw("create pipeline: db error validating stages", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if msg != "" {
		writeError(w, http.StatusBadRequest, "validation_failed", msg)
		return
	}

	stagesJSON, err := json.Marshal(body.Stages)
	if err != nil {
		h.log.Errorw("create pipeline: marshal stages error", "error", err)
		writeError(w, http.StatusBadRequest, "bad_request", "invalid stages")

		return
	}

	pipeline := &models.PromotionPipeline{
		Name:        body.Name,
		Description: body.Description,
		Stages:      body.Stages,
		StagesRaw:   stagesJSON,
		CreatedBy:   p.Username,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	if err := h.db.CreatePromotionPipeline(ctx, pipeline); err != nil {
		if database.IsUniqueViolation(err) {
			writeError(w, http.StatusConflict, "conflict", "pipeline already exists")
			return
		}

		h.log.Errorw("create pipeline: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	writeJSON(w, http.StatusCreated, pipelineToMap(pipeline))
}

func (h *BundlesHandler) handleGetPipeline(w http.ResponseWriter, r *http.Request) {
	name := urlparams.Vars(r)["name"]
	ctx := r.Context()

	p := currentPrincipal(r)

	isAdmin, err := principalIsAdmin(ctx, p, h.evaluator)
	if err != nil || !isAdmin {
		writeError(w, http.StatusForbidden, "forbidden", "admin access required")
		return
	}

	pipeline, err := h.db.GetPromotionPipelineByName(ctx, name)
	if err != nil {
		h.log.Errorw("get pipeline: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if pipeline == nil {
		writeError(w, http.StatusNotFound, "not_found", "pipeline not found")
		return
	}

	// Unmarshal stages
	if len(pipeline.StagesRaw) > 0 {
		if err := json.Unmarshal(pipeline.StagesRaw, &pipeline.Stages); err != nil {
			h.log.Errorw("get pipeline: unmarshal stages error", "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "database error")

			return
		}
	}

	writeJSON(w, http.StatusOK, pipelineToMap(pipeline))
}

func (h *BundlesHandler) handleUpdatePipeline(w http.ResponseWriter, r *http.Request) {
	name := urlparams.Vars(r)["name"]
	ctx := r.Context()

	p := currentPrincipal(r)

	isAdmin, err := principalIsAdmin(ctx, p, h.evaluator)
	if err != nil || !isAdmin {
		writeError(w, http.StatusForbidden, "forbidden", "admin access required")
		return
	}

	pipeline, err := h.db.GetPromotionPipelineByName(ctx, name)
	if err != nil {
		h.log.Errorw("update pipeline: db error getting pipeline", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if pipeline == nil {
		writeError(w, http.StatusNotFound, "not_found", "pipeline not found")
		return
	}

	var body struct {
		Description *string                 `json:"description"`
		Stages      *[]models.PipelineStage `json:"stages"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}

	if body.Description != nil {
		pipeline.Description = *body.Description
	}

	if body.Stages != nil {
		msg, err := h.validateStages(ctx, *body.Stages)
		if err != nil {
			h.log.Errorw("update pipeline: db error validating stages", "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "database error")

			return
		}

		if msg != "" {
			writeError(w, http.StatusBadRequest, "validation_failed", msg)
			return
		}

		pipeline.Stages = *body.Stages

		stagesJSON, err := json.Marshal(pipeline.Stages)
		if err != nil {
			h.log.Errorw("update pipeline: marshal stages error", "error", err)
			writeError(w, http.StatusBadRequest, "bad_request", "invalid stages")

			return
		}

		pipeline.StagesRaw = stagesJSON
	}

	pipeline.UpdatedAt = time.Now()

	if err := h.db.UpdatePromotionPipeline(ctx, pipeline); err != nil {
		h.log.Errorw("update pipeline: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	writeJSON(w, http.StatusOK, pipelineToMap(pipeline))
}

func (h *BundlesHandler) handleDeletePipeline(w http.ResponseWriter, r *http.Request) {
	name := urlparams.Vars(r)["name"]
	ctx := r.Context()

	p := currentPrincipal(r)

	isAdmin, err := principalIsAdmin(ctx, p, h.evaluator)
	if err != nil || !isAdmin {
		writeError(w, http.StatusForbidden, "forbidden", "admin access required")
		return
	}

	pipeline, err := h.db.GetPromotionPipelineByName(ctx, name)
	if err != nil {
		h.log.Errorw("delete pipeline: db error getting pipeline", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if pipeline == nil {
		writeError(w, http.StatusNotFound, "not_found", "pipeline not found")
		return
	}

	if err := h.db.DeletePromotionPipeline(ctx, pipeline.ID); err != nil {
		h.log.Errorw("delete pipeline: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	writeNoContent(w)
}

// ============================================================================
// Bundle Handlers
// ============================================================================

func (h *BundlesHandler) handleListBundles(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	nameFilter := q.Get("name")
	stateFilter := q.Get("state")
	page := parseIntMin(q.Get("page"), 1, 1)
	limit := parseIntClamp(q.Get("limit"), 25, 1, 100)

	ctx := r.Context()

	p := currentPrincipal(r)

	isAdmin, err := principalIsAdmin(ctx, p, h.evaluator)
	if err != nil || !isAdmin {
		writeError(w, http.StatusForbidden, "forbidden", "admin access required")
		return
	}

	bundles, total, err := h.db.ListReleaseBundles(ctx, nameFilter, stateFilter, page, limit)
	if err != nil {
		h.log.Errorw("list bundles: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if bundles == nil {
		bundles = []*models.ReleaseBundle{}
	}

	var result []map[string]any
	for _, bundle := range bundles {
		result = append(result, bundleToMap(bundle))
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"total":   total,
		"page":    page,
		"limit":   limit,
		"bundles": result,
	})
}

func (h *BundlesHandler) handleCreateBundle(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	p := currentPrincipal(r)

	if !h.requireBundleCreator(w, r) {
		return
	}

	var body struct {
		Name        string  `json:"name"`
		Version     string  `json:"version"`
		Description string  `json:"description"`
		PipelineID  *string `json:"pipeline_id"`
		// Pipeline names the pipeline instead of its UUID; the two are exclusive.
		Pipeline string `json:"pipeline"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}

	if body.Name == "" || body.Version == "" {
		writeError(w, http.StatusBadRequest, "validation_failed", "name and version are required")
		return
	}

	if msg := validateBundleLabel("name", body.Name); msg != "" {
		writeError(w, http.StatusBadRequest, "validation_failed", msg)
		return
	}

	if msg := validateBundleLabel("version", body.Version); msg != "" {
		writeError(w, http.StatusBadRequest, "validation_failed", msg)
		return
	}

	if body.Pipeline != "" {
		if body.PipelineID != nil {
			writeError(w, http.StatusBadRequest, "validation_failed", "pipeline and pipeline_id are exclusive")
			return
		}

		pipeline, err := h.db.GetPromotionPipelineByName(ctx, body.Pipeline)
		if err != nil {
			h.log.Errorw("create bundle: db error getting pipeline", "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "database error")

			return
		}

		if pipeline == nil {
			writeError(w, http.StatusBadRequest, "validation_failed", "pipeline does not reference an existing pipeline")
			return
		}

		body.PipelineID = &pipeline.ID
	} else if body.PipelineID != nil {
		if !isUUIDString(*body.PipelineID) {
			writeError(w, http.StatusBadRequest, "validation_failed", "pipeline_id must be a valid UUID")
			return
		}

		pipeline, err := h.db.GetPromotionPipelineByID(ctx, *body.PipelineID)
		if err != nil {
			h.log.Errorw("create bundle: db error getting pipeline", "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "database error")

			return
		}

		if pipeline == nil {
			writeError(w, http.StatusBadRequest, "validation_failed", "pipeline_id does not reference an existing pipeline")
			return
		}
	}

	bundle := &models.ReleaseBundle{
		Name:        body.Name,
		Version:     body.Version,
		Description: body.Description,
		State:       models.BundleStateDraft,
		PipelineID:  body.PipelineID,
		CreatedBy:   p.Username,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	if err := h.db.CreateReleaseBundle(ctx, bundle); err != nil {
		if database.IsUniqueViolation(err) {
			writeError(w, http.StatusConflict, "conflict", "bundle with this name and version already exists")
			return
		}

		if database.IsForeignKeyViolation(err) {
			writeError(w, http.StatusBadRequest, "validation_failed", "pipeline_id does not reference an existing pipeline")
			return
		}

		h.log.Errorw("create bundle: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	writeJSON(w, http.StatusCreated, bundleToMap(bundle))
}

func (h *BundlesHandler) handleGetBundle(w http.ResponseWriter, r *http.Request) {
	bundle, _, ok := h.loadBundle(w, r, bundleNeedView)
	if !ok {
		return
	}

	writeJSON(w, http.StatusOK, bundleToMap(bundle))
}

func (h *BundlesHandler) handleDeleteBundle(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	bundle, _, ok := h.loadBundle(w, r, bundleNeedOwn)
	if !ok {
		return
	}

	if bundle.State != models.BundleStateDraft {
		writeError(w, http.StatusConflict, "conflict", "can only delete draft bundles")
		return
	}

	if err := h.db.DeleteReleaseBundle(ctx, bundle.ID); err != nil {
		h.log.Errorw("delete bundle: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	writeNoContent(w)
}

// ============================================================================
// Bundle Item Handlers
// ============================================================================

func (h *BundlesHandler) handleListItems(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	bundle, _, ok := h.loadBundle(w, r, bundleNeedView)
	if !ok {
		return
	}

	items, err := h.db.ListBundleItems(ctx, bundle.ID)
	if err != nil {
		h.log.Errorw("list items: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if items == nil {
		items = []*models.ReleaseBundleItem{}
	}

	var result []map[string]any
	for _, item := range items {
		result = append(result, itemToMap(item))
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"items": result,
	})
}

// bundleItemInput is one requested entry of an add-items request.
type bundleItemInput struct {
	SourceRepoID string  `json:"source_repo_id"`
	ComponentID  *string `json:"component_id"`
	SHA256       string  `json:"sha256"`
	ArtifactPath string  `json:"artifact_path"`
	Format       string  `json:"format"`
	SizeBytes    int64   `json:"size_bytes"`
}

// apiFailure is an HTTP error to be written by the caller.
type apiFailure struct {
	status int
	code   string
	msg    string
}

func validationFailure(msg string) *apiFailure {
	return &apiFailure{http.StatusBadRequest, "validation_failed", msg}
}

// validateBundleItemFields checks the request-local fields of one item and
// returns its normalized (lowercase) sha256.
func validateBundleItemFields(itemData *bundleItemInput, seenSHA map[string]struct{}, prefix string) (string, *apiFailure) {
	if !isUUIDString(itemData.SourceRepoID) {
		return "", validationFailure(prefix + "source_repo_id must be a valid UUID")
	}

	sha := strings.ToLower(itemData.SHA256)
	if !isHexSHA256(sha) {
		return "", validationFailure(prefix + "sha256 must be 64 hex characters")
	}

	if _, dup := seenSHA[sha]; dup {
		return "", validationFailure(prefix + "duplicate sha256 in request")
	}

	seenSHA[sha] = struct{}{}

	if msg := validateArtifactID(itemData.ArtifactPath); msg != "" {
		return "", validationFailure(prefix + strings.Replace(msg, "artifact_id", "artifact_path", 1))
	}

	if itemData.SizeBytes < 0 {
		return "", validationFailure(prefix + "size_bytes must not be negative")
	}

	return sha, nil
}

// buildBundleItems validates every item before touching the database so the
// request is all-or-nothing, and verifies each source repository once. canRead,
// when set, limits the source repositories to the ones the caller may read: any
// other answers as an unknown repository.
func (h *BundlesHandler) buildBundleItems(ctx context.Context, bundleID, username string, in []bundleItemInput, canRead func(*models.Repository) (bool, error)) ([]*models.ReleaseBundleItem, *apiFailure) {
	knownRepos := make(map[string]*models.Repository, len(in))
	items := make([]*models.ReleaseBundleItem, 0, len(in))
	seenSHA := make(map[string]struct{}, len(in))

	for idx := range in {
		itemData := &in[idx]
		prefix := "items[" + strconv.Itoa(idx) + "]: "

		sha, fail := validateBundleItemFields(itemData, seenSHA, prefix)
		if fail != nil {
			return nil, fail
		}

		srcRepo, ok := knownRepos[itemData.SourceRepoID]
		if !ok {
			var err error

			srcRepo, err = h.db.GetRepositoryByID(ctx, itemData.SourceRepoID)
			if err != nil {
				h.log.Errorw("add items: db error getting source repo", "error", err)
				return nil, &apiFailure{http.StatusInternalServerError, "internal_error", "database error"}
			}

			if srcRepo != nil && canRead != nil {
				readable, err := canRead(srcRepo)
				if err != nil {
					h.log.Errorw("add items: auth evaluation error", "error", err)
					return nil, &apiFailure{http.StatusInternalServerError, "internal_error", "auth evaluation failed"}
				}

				if !readable {
					srcRepo = nil
				}
			}

			knownRepos[itemData.SourceRepoID] = srcRepo
		}

		if srcRepo == nil {
			return nil, validationFailure(prefix + "source_repo_id does not reference an existing repository")
		}

		format := itemData.Format
		if format == "" {
			format = srcRepo.Format
		}

		if len(format) > maxFormatLen {
			return nil, validationFailure(prefix + "format is too long")
		}

		items = append(items, &models.ReleaseBundleItem{
			BundleID:     bundleID,
			SourceRepoID: itemData.SourceRepoID,
			ComponentID:  itemData.ComponentID,
			SHA256:       sha,
			ArtifactPath: itemData.ArtifactPath,
			Format:       format,
			SizeBytes:    itemData.SizeBytes,
			AddedBy:      username,
			AddedAt:      time.Now(),
		})
	}

	return items, nil
}

func (h *BundlesHandler) handleAddItems(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	p := currentPrincipal(r)

	bundle, access, ok := h.loadBundle(w, r, bundleNeedOwn)
	if !ok {
		return
	}

	if bundle.State != models.BundleStateDraft {
		writeError(w, http.StatusConflict, "conflict", "can only add items to draft bundles")
		return
	}

	// An administrator may reference any repository; anyone else only the ones
	// they can read.
	var canRead func(*models.Repository) (bool, error)

	if !access.admin {
		var err error

		if canRead, err = h.evaluator.RepoReadFilter(ctx, *p); err != nil {
			h.log.Errorw("add items: auth evaluation error", "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "auth evaluation failed")

			return
		}
	}

	var body struct {
		Items []bundleItemInput `json:"items"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}

	if len(body.Items) == 0 || len(body.Items) > maxBundleItemsPerRequest {
		writeError(w, http.StatusBadRequest, "validation_failed",
			"items must contain between 1 and "+strconv.Itoa(maxBundleItemsPerRequest)+" entries")

		return
	}

	items, fail := h.buildBundleItems(ctx, bundle.ID, p.Username, body.Items, canRead)
	if fail != nil {
		writeError(w, fail.status, fail.code, fail.msg)
		return
	}

	// One transaction: the bundle row is locked and must still be a draft, so
	// a concurrent seal cannot interleave with the inserts.
	if err := h.db.AddBundleItems(ctx, bundle.ID, items); err != nil {
		switch {
		case errors.Is(err, database.ErrBundleNotDraft):
			writeError(w, http.StatusConflict, "conflict", "can only add items to draft bundles")
		case errors.Is(err, database.ErrBundleNotFound):
			writeError(w, http.StatusNotFound, "not_found", "bundle not found")
		case database.IsForeignKeyViolation(err):
			writeError(w, http.StatusBadRequest, "validation_failed", "source_repo_id does not reference an existing repository")
		default:
			h.log.Errorw("add items: db error adding items", "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "database error")
		}

		return
	}

	result := make([]map[string]any, 0, len(items))
	for _, item := range items {
		result = append(result, itemToMap(item))
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"items": result,
	})
}

func (h *BundlesHandler) handleRemoveItem(w http.ResponseWriter, r *http.Request) {
	sha256 := urlparams.Vars(r)["sha256"]
	ctx := r.Context()

	bundle, _, ok := h.loadBundle(w, r, bundleNeedOwn)
	if !ok {
		return
	}

	if bundle.State != models.BundleStateDraft {
		writeError(w, http.StatusConflict, "conflict", "can only remove items from draft bundles")
		return
	}

	item, err := h.db.GetBundleItem(ctx, bundle.ID, sha256)
	if err != nil {
		h.log.Errorw("remove item: db error getting item", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if item == nil {
		writeError(w, http.StatusNotFound, "not_found", "item not found")
		return
	}

	if err := h.db.RemoveBundleItem(ctx, bundle.ID, sha256); err != nil {
		if errors.Is(err, database.ErrBundleNotDraft) {
			writeError(w, http.StatusConflict, "conflict", "can only remove items from draft bundles")
			return
		}

		h.log.Errorw("remove item: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	writeNoContent(w)
}

// ============================================================================
// Bundle Lifecycle Handlers
// ============================================================================

func (h *BundlesHandler) handleSeal(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	p := currentPrincipal(r)

	bundle, access, ok := h.loadBundle(w, r, bundleNeedOwn)
	if !ok || !requireSourceReads(w, access) {
		return
	}

	if bundle.State != models.BundleStateDraft {
		writeError(w, http.StatusConflict, "conflict", "can only seal draft bundles")
		return
	}

	// Check if bundle has items
	items, err := h.db.ListBundleItems(ctx, bundle.ID)
	if err != nil {
		h.log.Errorw("seal bundle: db error listing items", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if len(items) == 0 {
		writeError(w, http.StatusBadRequest, "validation_failed", "bundle must have at least one item")
		return
	}

	var body struct {
		GPGKeyID *string `json:"gpg_key_id"`
	}
	// The body is optional, but a present, malformed body is a client error
	// rather than something to silently ignore.
	if err := decodeJSON(r, &body); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}

	var signature *models.BundleSignature

	if body.GPGKeyID != nil {
		if strings.TrimSpace(*body.GPGKeyID) == "" {
			writeError(w, http.StatusBadRequest, "validation_failed", "gpg_key_id must not be empty")
			return
		}

		var fail *apiFailure
		if signature, fail = h.signBundle(ctx, *body.GPGKeyID, bundle, items); fail != nil {
			writeError(w, fail.status, fail.code, fail.msg)
			return
		}
	}

	now := time.Now()

	// Conditional UPDATE ... WHERE state = 'draft': a concurrent seal/archive
	// (or item removal) between the checks above and here cannot be overwritten.
	sealed, err := h.db.SealReleaseBundle(ctx, bundle.ID, p.Username, now, signature)
	if err != nil {
		h.log.Errorw("seal bundle: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if !sealed {
		writeError(w, http.StatusConflict, "conflict", "bundle is no longer a sealable draft")
		return
	}

	bundle.State = models.BundleStateSealed
	bundle.SealedAt = &now
	bundle.SealedBy = &p.Username
	bundle.UpdatedAt = now

	if signature != nil {
		bundle.GPGKeyID = &signature.KeyFingerprint
		bundle.GPGSignature = &signature.Signature
	}

	writeJSON(w, http.StatusOK, bundleToMap(bundle))
}

// validatePromotionStage validates the requested stage against the bundle's
// pipeline and the target repository.
func (h *BundlesHandler) validatePromotionStage(ctx context.Context, bundle *models.ReleaseBundle, targetRepo *models.Repository, toStage string) *apiFailure {
	if bundle.PipelineID == nil {
		return validationFailure("bundle has no pipeline; cannot promote")
	}

	pipeline, err := h.db.GetPromotionPipelineByID(ctx, *bundle.PipelineID)
	if err != nil {
		h.log.Errorw("promote bundle: db error getting pipeline", "error", err)
		return &apiFailure{http.StatusInternalServerError, "internal_error", "database error"}
	}

	if pipeline == nil {
		return &apiFailure{http.StatusConflict, "conflict", "bundle's pipeline no longer exists"}
	}

	toIdx, curIdx := -1, -1

	for i, stage := range pipeline.Stages {
		if stage.Name == toStage {
			toIdx = i
		}

		if bundle.CurrentStage != nil && stage.Name == *bundle.CurrentStage {
			curIdx = i
		}
	}

	if toIdx < 0 {
		return validationFailure("to_stage is not a stage of the bundle's pipeline")
	}

	if toIdx <= curIdx {
		return &apiFailure{http.StatusConflict, "conflict", "to_stage must come after the bundle's current stage"}
	}

	if want := pipeline.Stages[toIdx].RepositoryID; want != "" && want != targetRepo.ID {
		return validationFailure("target_repo does not match the repository configured for to_stage")
	}

	return nil
}

// resolvePromotionTarget finds the repository a promotion writes to: the named
// one, or, when the name is empty, the repository the pipeline fixes for toStage.
func (h *BundlesHandler) resolvePromotionTarget(ctx context.Context, bundle *models.ReleaseBundle, toStage, name string) (*models.Repository, *apiFailure) {
	var (
		repo *models.Repository
		err  error
	)

	if name != "" {
		repo, err = h.db.GetRepositoryByName(ctx, name)
	} else {
		repoID, fail := h.stageRepositoryID(ctx, bundle, toStage)
		if fail != nil {
			return nil, fail
		}

		repo, err = h.db.GetRepositoryByID(ctx, repoID)
	}

	if err != nil {
		h.log.Errorw("promote bundle: db error getting target repo", "error", err)
		return nil, &apiFailure{http.StatusInternalServerError, "internal_error", "database error"}
	}

	if repo == nil {
		return nil, &apiFailure{http.StatusNotFound, "not_found", "target repository not found"}
	}

	return repo, nil
}

// stageRepositoryID returns the repository id the bundle's pipeline fixes for
// toStage, or a validation failure when target_repo has to be given instead.
func (h *BundlesHandler) stageRepositoryID(ctx context.Context, bundle *models.ReleaseBundle, toStage string) (string, *apiFailure) {
	if bundle.PipelineID == nil {
		return "", validationFailure("bundle has no pipeline; cannot promote")
	}

	pipeline, err := h.db.GetPromotionPipelineByID(ctx, *bundle.PipelineID)
	if err != nil {
		h.log.Errorw("promote bundle: db error getting pipeline", "error", err)
		return "", &apiFailure{http.StatusInternalServerError, "internal_error", "database error"}
	}

	if pipeline == nil {
		return "", &apiFailure{http.StatusConflict, "conflict", "bundle's pipeline no longer exists"}
	}

	for _, stage := range pipeline.Stages {
		if stage.Name != toStage {
			continue
		}

		if stage.RepositoryID == "" {
			return "", validationFailure("target_repo is required: stage " + toStage + " does not fix a repository")
		}

		return stage.RepositoryID, nil
	}

	return "", validationFailure("to_stage is not a stage of the bundle's pipeline")
}

func (h *BundlesHandler) handlePromote(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	bundle, access, ok := h.loadBundle(w, r, bundleNeedView)
	if !ok || !requireSourceReads(w, access) {
		return
	}

	if bundle.State != models.BundleStateSealed {
		writeError(w, http.StatusConflict, "conflict", "can only promote sealed bundles")
		return
	}

	var body struct {
		ToStage    string `json:"to_stage"`
		TargetRepo string `json:"target_repo"`
		Notes      string `json:"notes"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}

	if body.ToStage == "" {
		writeError(w, http.StatusBadRequest, "validation_failed", "to_stage is required")
		return
	}

	targetRepo, fail := h.resolvePromotionTarget(ctx, bundle, body.ToStage, body.TargetRepo)
	if fail != nil {
		writeError(w, fail.status, fail.code, fail.msg)
		return
	}

	if !h.requireTargetWrite(w, r, access, targetRepo) {
		return
	}

	if fail := h.validatePromotionStage(ctx, bundle, targetRepo, body.ToStage); fail != nil {
		writeError(w, fail.status, fail.code, fail.msg)
		return
	}

	h.promoteBundle(w, r, bundle, targetRepo, body.ToStage, body.Notes)
}

func (h *BundlesHandler) handleArchive(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	p := currentPrincipal(r)

	bundle, _, ok := h.loadBundle(w, r, bundleNeedOwn)
	if !ok {
		return
	}

	if bundle.State != models.BundleStateSealed {
		writeError(w, http.StatusConflict, "conflict", "can only archive sealed bundles")
		return
	}

	now := time.Now()
	bundle.State = models.BundleStateArchived
	bundle.ArchivedAt = &now
	bundle.ArchivedBy = &p.Username
	bundle.UpdatedAt = now

	if err := h.db.UpdateReleaseBundle(ctx, bundle); err != nil {
		h.log.Errorw("archive bundle: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	writeJSON(w, http.StatusOK, bundleToMap(bundle))
}

// ============================================================================
// Promotion History Handler
// ============================================================================

func (h *BundlesHandler) handleListPromotions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	bundle, _, ok := h.loadBundle(w, r, bundleNeedView)
	if !ok {
		return
	}

	promotions, err := h.db.ListBundlePromotions(ctx, bundle.ID)
	if err != nil {
		h.log.Errorw("list promotions: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if promotions == nil {
		promotions = []*models.BundlePromotion{}
	}

	var result []map[string]any
	for _, promotion := range promotions {
		result = append(result, promotionToMap(promotion))
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"promotions": result,
	})
}

// ============================================================================
// Response Mapping Helpers
// ============================================================================

func bundleToMap(b *models.ReleaseBundle) map[string]any {
	return map[string]any{
		"id":            b.ID,
		"name":          b.Name,
		"version":       b.Version,
		"description":   b.Description,
		"state":         b.State,
		"pipeline_id":   b.PipelineID,
		"current_stage": b.CurrentStage,
		"created_by":    b.CreatedBy,
		"sealed_at":     formatTimePtr(b.SealedAt),
		"sealed_by":     b.SealedBy,
		"archived_at":   formatTimePtr(b.ArchivedAt),
		"archived_by":   b.ArchivedBy,
		"gpg_key_id":    b.GPGKeyID,
		"gpg_signature": b.GPGSignature,
		"created_at":    formatTime(b.CreatedAt),
		"updated_at":    formatTime(b.UpdatedAt),
	}
}

func itemToMap(i *models.ReleaseBundleItem) map[string]any {
	return map[string]any{
		"id":             i.ID,
		"bundle_id":      i.BundleID,
		"source_repo_id": i.SourceRepoID,
		"component_id":   i.ComponentID,
		"sha256":         i.SHA256,
		"artifact_path":  i.ArtifactPath,
		"format":         i.Format,
		"size_bytes":     i.SizeBytes,
		"added_by":       i.AddedBy,
		"added_at":       formatTime(i.AddedAt),
	}
}

func promotionToMap(p *models.BundlePromotion) map[string]any {
	return map[string]any{
		"id":             p.ID,
		"bundle_id":      p.BundleID,
		"from_stage":     p.FromStage,
		"to_stage":       p.ToStage,
		"target_repo_id": p.TargetRepoID,
		"status":         p.Status,
		"promoted_by":    p.PromotedBy,
		"notes":          p.Notes,
		"error_message":  p.ErrorMessage,
		"item_count":     p.ItemCount,
		"started_at":     formatTime(p.StartedAt),
		"completed_at":   formatTimePtr(p.CompletedAt),
	}
}

func pipelineToMap(p *models.PromotionPipeline) map[string]any {
	// Unmarshal stages if needed
	if len(p.Stages) == 0 && len(p.StagesRaw) > 0 {
		_ = json.Unmarshal(p.StagesRaw, &p.Stages)
	}

	return map[string]any{
		"id":          p.ID,
		"name":        p.Name,
		"description": p.Description,
		"stages":      p.Stages,
		"created_by":  p.CreatedBy,
		"created_at":  formatTime(p.CreatedAt),
		"updated_at":  formatTime(p.UpdatedAt),
	}
}
