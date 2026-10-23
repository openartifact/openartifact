// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/api/responses"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/internal/worker"
	"github.com/openartifact/openartifact/pkg/logger"
)

// CleanupRunner starts an immediate run of a cleanup policy. It is satisfied by
// *worker.CleanupWorker.
type CleanupRunner interface {
	TriggerPolicy(ctx context.Context, policyID string) error
	// PreviewPolicy reports what a run would remove, removing nothing.
	PreviewPolicy(ctx context.Context, policyID string) (*worker.CleanupPreview, error)
}

type CleanupHandler struct {
	db        database.Database
	log       *logger.Logger
	evaluator *auth.Evaluator
	runner    CleanupRunner
}

func NewCleanupHandler(db database.Database, log *logger.Logger, evaluator *auth.Evaluator, runner CleanupRunner) *CleanupHandler {
	return &CleanupHandler{db: db, log: log, evaluator: evaluator, runner: runner}
}

func (h *CleanupHandler) RegisterAuthed(r chi.Router) {
	r.Get("/api/v1/repositories/{name}/cleanup-policies", h.handleList)
	r.Post("/api/v1/repositories/{name}/cleanup-policies", h.handleCreate)
	r.Get("/api/v1/repositories/{name}/cleanup-policies/{id}", h.handleGet)
	r.Put("/api/v1/repositories/{name}/cleanup-policies/{id}", h.handleUpdate)
	r.Delete("/api/v1/repositories/{name}/cleanup-policies/{id}", h.handleDelete)
	r.Post("/api/v1/repositories/{name}/cleanup-policies/{id}/run", h.handleRun)
	r.Post("/api/v1/repositories/{name}/cleanup-policies/{id}/dry-run", h.handleDryRun)
}

func (h *CleanupHandler) handleList(w http.ResponseWriter, r *http.Request) {
	name := urlparams.Vars(r)["name"]
	ctx := r.Context()

	repo, err := h.db.GetRepositoryByName(ctx, name)
	if err != nil {
		h.log.Errorw("list cleanup policies: db error getting repo", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if repo == nil {
		writeError(w, http.StatusNotFound, "not_found", "repository not found")
		return
	}

	if !requireRepoAdmin(w, r, h.evaluator, repo.Name) {
		return
	}

	policies, err := h.db.ListCleanupPolicies(ctx, repo.ID)
	if err != nil {
		h.log.Errorw("list cleanup policies: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if policies == nil {
		policies = []*models.CleanupPolicy{}
	}

	var result []responses.CleanupPolicyResponse
	for _, policy := range policies {
		result = append(result, policyToMap(policy))
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"policies": result,
	})
}

func (h *CleanupHandler) handleCreate(w http.ResponseWriter, r *http.Request) {
	name := urlparams.Vars(r)["name"]
	ctx := r.Context()

	repo, err := h.db.GetRepositoryByName(ctx, name)
	if err != nil {
		h.log.Errorw("create cleanup policy: db error getting repo", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if repo == nil {
		writeError(w, http.StatusNotFound, "not_found", "repository not found")
		return
	}

	if !requireRepoAdmin(w, r, h.evaluator, repo.Name) {
		return
	}

	if !requireCleanupFormat(w, repo) {
		return
	}

	var body struct {
		Name                    string `json:"name"`
		Enabled                 bool   `json:"enabled"`
		KeepLastN               *int   `json:"keep_last_n"`
		KeepSinceDays           *int   `json:"keep_since_days"`
		KeepDownloadedSinceDays *int   `json:"keep_downloaded_since_days"`
		NamePattern             string `json:"name_pattern"`
		ScheduleCron            string `json:"schedule_cron"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}

	scheduleCron := strings.TrimSpace(body.ScheduleCron)
	if scheduleCron == "" {
		scheduleCron = models.DefaultCleanupCron
	}

	policy := &models.CleanupPolicy{
		RepositoryID:            repo.ID,
		Name:                    body.Name,
		Enabled:                 body.Enabled,
		KeepLastN:               body.KeepLastN,
		KeepSinceDays:           body.KeepSinceDays,
		KeepDownloadedSinceDays: body.KeepDownloadedSinceDays,
		NamePattern:             body.NamePattern,
		ScheduleCron:            scheduleCron,
		CreatedAt:               time.Now(),
		UpdatedAt:               time.Now(),
	}

	if err := policy.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "validation_failed", err.Error())
		return
	}

	if err := h.db.CreateCleanupPolicy(ctx, policy); err != nil {
		h.log.Errorw("create cleanup policy: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	writeJSON(w, http.StatusCreated, policyToMap(policy))
}

func (h *CleanupHandler) handleGet(w http.ResponseWriter, r *http.Request) {
	name := urlparams.Vars(r)["name"]
	id := urlparams.Vars(r)["id"]
	ctx := r.Context()

	repo, err := h.db.GetRepositoryByName(ctx, name)
	if err != nil {
		h.log.Errorw("get cleanup policy: db error getting repo", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if repo == nil {
		writeError(w, http.StatusNotFound, "not_found", "repository not found")
		return
	}

	if !requireRepoAdmin(w, r, h.evaluator, repo.Name) {
		return
	}

	policy, err := h.db.GetCleanupPolicy(ctx, id)
	if err != nil {
		h.log.Errorw("get cleanup policy: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if policy == nil || policy.RepositoryID != repo.ID {
		writeError(w, http.StatusNotFound, "not_found", "cleanup policy not found")
		return
	}

	writeJSON(w, http.StatusOK, policyToMap(policy))
}

// cleanupPolicyPatch is the partial-update body of a cleanup policy.
type cleanupPolicyPatch struct {
	Name                    *string `json:"name"`
	Enabled                 *bool   `json:"enabled"`
	KeepLastN               *int    `json:"keep_last_n"`
	KeepSinceDays           *int    `json:"keep_since_days"`
	KeepDownloadedSinceDays *int    `json:"keep_downloaded_since_days"`
	NamePattern             *string `json:"name_pattern"`
	ScheduleCron            *string `json:"schedule_cron"`
}

// apply copies the provided fields onto policy.
func (b *cleanupPolicyPatch) apply(policy *models.CleanupPolicy) {
	if b.Name != nil {
		policy.Name = *b.Name
	}

	if b.Enabled != nil {
		policy.Enabled = *b.Enabled
	}

	if b.KeepLastN != nil {
		policy.KeepLastN = b.KeepLastN
	}

	if b.KeepSinceDays != nil {
		policy.KeepSinceDays = b.KeepSinceDays
	}

	if b.KeepDownloadedSinceDays != nil {
		policy.KeepDownloadedSinceDays = b.KeepDownloadedSinceDays
	}

	if b.NamePattern != nil {
		policy.NamePattern = *b.NamePattern
	}

	if b.ScheduleCron != nil {
		policy.ScheduleCron = strings.TrimSpace(*b.ScheduleCron)
		if policy.ScheduleCron == "" {
			policy.ScheduleCron = models.DefaultCleanupCron
		}
	}
}

// onlyDisables reports whether the patch (already applied to policy) merely
// disabled the policy.
func (b *cleanupPolicyPatch) onlyDisables(policy *models.CleanupPolicy) bool {
	return !policy.Enabled && b.Name == nil && b.KeepLastN == nil && b.KeepSinceDays == nil &&
		b.KeepDownloadedSinceDays == nil && b.NamePattern == nil && b.ScheduleCron == nil
}

// codeUnsupportedFormat is the error code for operations the repository's format does not support.
const codeUnsupportedFormat = "UNSUPPORTED_FORMAT"

// requireCleanupFormat answers 400 when the cleanup worker has no support for
// the repository's format; such a policy would only ever fail at run time.
func requireCleanupFormat(w http.ResponseWriter, repo *models.Repository) bool {
	if worker.CleanupFormatSupported(repo.Format) {
		return true
	}

	writeError(w, http.StatusBadRequest, codeUnsupportedFormat,
		"cleanup policies are not supported for "+repo.Format+" repositories")

	return false
}

func (h *CleanupHandler) handleUpdate(w http.ResponseWriter, r *http.Request) {
	name := urlparams.Vars(r)["name"]
	id := urlparams.Vars(r)["id"]
	ctx := r.Context()

	repo, err := h.db.GetRepositoryByName(ctx, name)
	if err != nil {
		h.log.Errorw("update cleanup policy: db error getting repo", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if repo == nil {
		writeError(w, http.StatusNotFound, "not_found", "repository not found")
		return
	}

	if !requireRepoAdmin(w, r, h.evaluator, repo.Name) {
		return
	}

	if !requireCleanupFormat(w, repo) {
		return
	}

	policy, err := h.db.GetCleanupPolicy(ctx, id)
	if err != nil {
		h.log.Errorw("update cleanup policy: db error getting policy", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if policy == nil || policy.RepositoryID != repo.ID {
		writeError(w, http.StatusNotFound, "not_found", "cleanup policy not found")
		return
	}

	var body cleanupPolicyPatch
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}

	body.apply(policy)

	// Disabling a policy must always be possible, even one stored before
	// validation existed; any other change has to leave a valid policy.
	if !body.onlyDisables(policy) {
		if err := policy.Validate(); err != nil {
			writeError(w, http.StatusBadRequest, "validation_failed", err.Error())
			return
		}
	}

	if err := h.db.UpdateCleanupPolicy(ctx, policy); err != nil {
		h.log.Errorw("update cleanup policy: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	writeJSON(w, http.StatusOK, policyToMap(policy))
}

func (h *CleanupHandler) handleDelete(w http.ResponseWriter, r *http.Request) {
	name := urlparams.Vars(r)["name"]
	id := urlparams.Vars(r)["id"]
	ctx := r.Context()

	repo, err := h.db.GetRepositoryByName(ctx, name)
	if err != nil {
		h.log.Errorw("delete cleanup policy: db error getting repo", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if repo == nil {
		writeError(w, http.StatusNotFound, "not_found", "repository not found")
		return
	}

	if !requireRepoAdmin(w, r, h.evaluator, repo.Name) {
		return
	}

	policy, err := h.db.GetCleanupPolicy(ctx, id)
	if err != nil {
		h.log.Errorw("delete cleanup policy: db error getting policy", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if policy == nil || policy.RepositoryID != repo.ID {
		writeError(w, http.StatusNotFound, "not_found", "cleanup policy not found")
		return
	}

	if err := h.db.DeleteCleanupPolicy(ctx, id); err != nil {
		h.log.Errorw("delete cleanup policy: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	writeNoContent(w)
}

func (h *CleanupHandler) handleRun(w http.ResponseWriter, r *http.Request) {
	name := urlparams.Vars(r)["name"]
	id := urlparams.Vars(r)["id"]
	ctx := r.Context()

	repo, err := h.db.GetRepositoryByName(ctx, name)
	if err != nil {
		h.log.Errorw("run cleanup policy: db error getting repo", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if repo == nil {
		writeError(w, http.StatusNotFound, "not_found", "repository not found")
		return
	}

	if !requireRepoAdmin(w, r, h.evaluator, repo.Name) {
		return
	}

	policy, err := h.db.GetCleanupPolicy(ctx, id)
	if err != nil {
		h.log.Errorw("run cleanup policy: db error getting policy", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if policy == nil || policy.RepositoryID != repo.ID {
		writeError(w, http.StatusNotFound, "not_found", "cleanup policy not found")
		return
	}

	if h.runner == nil {
		writeError(w, http.StatusNotImplemented, "not_implemented", "cleanup worker is not available")
		return
	}

	// Refuse to run a policy that is invalid (e.g. created before validation existed).
	if err := policy.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "validation_failed", err.Error())
		return
	}

	if err := h.runner.TriggerPolicy(ctx, id); err != nil {
		switch {
		case errors.Is(err, worker.ErrPolicyNotFound):
			writeError(w, http.StatusNotFound, "not_found", "cleanup policy not found")
		case errors.Is(err, worker.ErrPolicyBusy):
			writeError(w, http.StatusConflict, "conflict", "cleanup policy is already running")
		default:
			h.log.Errorw("run cleanup policy: trigger failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "failed to start cleanup policy")
		}

		return
	}

	// The run executes asynchronously; its outcome is recorded in last_run_*.
	writeJSON(w, http.StatusAccepted, map[string]any{
		"message":   "cleanup policy run started",
		"policy_id": id,
	})
}

// cleanupPreviewResponse is what a dry run of a cleanup policy reports.
type cleanupPreviewResponse struct {
	Repository string                       `json:"repository"`
	Format     string                       `json:"format"`
	DryRun     bool                         `json:"dry_run"`
	Rows       int                          `json:"rows"`
	Items      []cleanupPreviewItemResponse `json:"items"`
	Truncated  bool                         `json:"truncated"`
}

type cleanupPreviewItemResponse struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Version string `json:"version"`
}

// handleDryRun reports, synchronously, what a run of the policy would remove
// right now: the rows outside the keep rules, minus the ones a protection
// keeps (immutable releases, release bundles, quarantine, dist-tagged npm
// versions, the newest build of a maven snapshot). Nothing is changed.
func (h *CleanupHandler) handleDryRun(w http.ResponseWriter, r *http.Request) {
	name := urlparams.Vars(r)["name"]
	id := urlparams.Vars(r)["id"]
	ctx := r.Context()

	repo, err := h.db.GetRepositoryByName(ctx, name)
	if err != nil {
		h.log.Errorw("dry-run cleanup policy: db error getting repo", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if repo == nil {
		writeError(w, http.StatusNotFound, "not_found", "repository not found")
		return
	}

	if !requireRepoAdmin(w, r, h.evaluator, repo.Name) {
		return
	}

	policy, err := h.db.GetCleanupPolicy(ctx, id)
	if err != nil {
		h.log.Errorw("dry-run cleanup policy: db error getting policy", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if policy == nil || policy.RepositoryID != repo.ID {
		writeError(w, http.StatusNotFound, "not_found", "cleanup policy not found")
		return
	}

	if h.runner == nil {
		writeError(w, http.StatusNotImplemented, "not_implemented", "cleanup worker is not available")
		return
	}

	prev, err := h.runner.PreviewPolicy(ctx, id)
	if err != nil {
		h.writePreviewError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, newCleanupPreviewResponse(prev))
}

func newCleanupPreviewResponse(prev *worker.CleanupPreview) cleanupPreviewResponse {
	items := make([]cleanupPreviewItemResponse, len(prev.Items))
	for i, it := range prev.Items {
		items[i] = cleanupPreviewItemResponse{ID: it.ID, Name: it.Name, Version: it.Version}
	}

	return cleanupPreviewResponse{
		Repository: prev.Repository,
		Format:     prev.Format,
		DryRun:     true,
		Rows:       prev.Rows,
		Items:      items,
		Truncated:  prev.Truncated,
	}
}

func (h *CleanupHandler) writePreviewError(w http.ResponseWriter, err error) {
	writePolicyPreviewError(w, h.log, err)
}

// writePolicyPreviewError maps a dry-run failure to its HTTP answer.
func writePolicyPreviewError(w http.ResponseWriter, log *logger.Logger, err error) {
	switch {
	case errors.Is(err, worker.ErrPolicyNotFound), errors.Is(err, worker.ErrRepositoryNotFound):
		writeError(w, http.StatusNotFound, "not_found", "cleanup policy not found")
	case errors.Is(err, models.ErrInvalidCleanupPolicy):
		writeError(w, http.StatusBadRequest, "validation_failed", err.Error())
	default:
		log.Errorw("dry-run cleanup policy failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to evaluate cleanup policy")
	}
}

func policyToMap(p *models.CleanupPolicy) responses.CleanupPolicyResponse {
	var lastRunError *string
	if p.LastRunError != "" {
		lastRunError = &p.LastRunError
	}

	namePattern := p.NamePattern
	scheduleCron := p.ScheduleCron

	return responses.CleanupPolicyResponse{
		ID:                      p.ID,
		RepositoryID:            p.RepositoryID,
		ProjectID:               p.ProjectID,
		Name:                    p.Name,
		Enabled:                 p.Enabled,
		KeepLastN:               p.KeepLastN,
		KeepSinceDays:           p.KeepSinceDays,
		KeepDownloadedSinceDays: p.KeepDownloadedSinceDays,
		NamePattern:             &namePattern,
		ScheduleCron:            &scheduleCron,
		CreatedAt:               formatTime(p.CreatedAt),
		UpdatedAt:               formatTime(p.UpdatedAt),
		LastRunAt:               formatTimePtr(p.LastRunAt),
		LastRunDeleted:          p.LastRunDeleted,
		LastRunError:            lastRunError,
	}
}
