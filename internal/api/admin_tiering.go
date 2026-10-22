// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/openartifact/openartifact/internal/api/responses"
	"github.com/openartifact/openartifact/internal/audit"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/tiering"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/pkg/logger"
)

const (
	tierRankStep       = 10
	maxTierRank        = 1_000_000
	maxPolicyPosition  = 1_000_000
	maxPolicyFormats   = 32
	pendingMovesListed = 20
	tieringOpTimeout   = 2 * time.Minute
)

const tierNameMsg = "name must be 1-64 characters: letters, digits, '.', '_' or '-'"

var (
	// storage_tiers.name is varchar(64).
	tierNameRe      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	storageFormatRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

	errTierValidation = errors.New("tier validation")
)

// TieringAdminDB is the persistence the tiering admin API needs.
type TieringAdminDB interface {
	ListStorageTiers(ctx context.Context) ([]*models.StorageTier, error)
	GetStorageTierByName(ctx context.Context, name string) (*models.StorageTier, error)
	CreateStorageTier(ctx context.Context, t *models.StorageTier) error
	UpdateStorageTier(ctx context.Context, t *models.StorageTier) error
	// DeleteStorageTierIfUnused checks and deletes atomically; it returns the
	// reason the tier is still in use, or "" once it is deleted.
	DeleteStorageTierIfUnused(ctx context.Context, id string) (string, error)
	GetStorageBackend(ctx context.Context, name string) (*models.StorageBackend, error)

	ListStoragePolicies(ctx context.Context) ([]*models.StoragePolicy, error)
	GetStoragePolicy(ctx context.Context, id string) (*models.StoragePolicy, error)
	CreateStoragePolicy(ctx context.Context, p *models.StoragePolicy) error
	UpdateStoragePolicy(ctx context.Context, p *models.StoragePolicy) error
	DeleteStoragePolicy(ctx context.Context, id string) (bool, error)

	StorageTierUsage(ctx context.Context) ([]models.TierUsage, error)
	ListStorageTierMoves(ctx context.Context, limit int) ([]*models.StorageTierMove, error)
	CountStorageTierMoves(ctx context.Context) (int64, error)
}

// TierPlanner simulates policies (the tiering engine).
type TierPlanner interface {
	Plan(ctx context.Context, draft *tiering.Policy, sampleLimit int) (*tiering.Plan, error)
}

// TierRunner is the background mover as the admin API sees it.
type TierRunner interface {
	// Trigger asks for an immediate pass; it reports false when one is already running.
	Trigger() bool
	Status() tiering.RunnerStatus
}

// TierRegistry is the tier registry as the admin API sees it.
type TierRegistry interface {
	Refresh(ctx context.Context) error
	Snapshot() *tiering.Snapshot
}

// AdminTieringHandler serves /api/v1/admin/tiering: tiers, lifecycle policies,
// dry runs and the mover status.
type AdminTieringHandler struct {
	db        TieringAdminDB
	reg       TierRegistry
	planner   TierPlanner
	runner    TierRunner
	log       *logger.Logger
	evaluator *auth.Evaluator
}

// NewAdminTieringHandler creates the handler.
func NewAdminTieringHandler(db TieringAdminDB, reg TierRegistry, planner TierPlanner, runner TierRunner, log *logger.Logger, evaluator *auth.Evaluator) *AdminTieringHandler {
	return &AdminTieringHandler{db: db, reg: reg, planner: planner, runner: runner, log: log, evaluator: evaluator}
}

// Register mounts the routes.
func (h *AdminTieringHandler) Register(r chi.Router) {
	r.Get("/api/v1/admin/tiering/tiers", h.handleListTiers)
	r.Post("/api/v1/admin/tiering/tiers", h.handleCreateTier)
	r.Patch("/api/v1/admin/tiering/tiers/{name}", h.handleUpdateTier)
	r.Delete("/api/v1/admin/tiering/tiers/{name}", h.handleDeleteTier)
	r.Get("/api/v1/admin/tiering/policies", h.handleListPolicies)
	r.Post("/api/v1/admin/tiering/policies", h.handleCreatePolicy)
	r.With(requireUUIDParams("id")).Get("/api/v1/admin/tiering/policies/{id}", h.handleGetPolicy)
	r.With(requireUUIDParams("id")).Put("/api/v1/admin/tiering/policies/{id}", h.handleReplacePolicy)
	r.With(requireUUIDParams("id")).Delete("/api/v1/admin/tiering/policies/{id}", h.handleDeletePolicy)
	r.Post("/api/v1/admin/tiering/dry-run", h.handleDryRun)
	r.Post("/api/v1/admin/tiering/run", h.handleRun)
	r.Get("/api/v1/admin/tiering/status", h.handleStatus)
}

func (h *AdminTieringHandler) internalError(w http.ResponseWriter, msg string, err error) {
	h.log.Errorw(msg, "error", err)
	writeError(w, http.StatusInternalServerError, "internal_error", msg)
}

// refreshRegistry makes a tier change effective on this replica at once (the
// others pick it up on their next periodic reload).
func (h *AdminTieringHandler) refreshRegistry(ctx context.Context) {
	if err := h.reg.Refresh(ctx); err != nil {
		h.log.Warnw("storage tier reload after change failed", "error", err)
	}
}

// ---- tiers ----

func (h *AdminTieringHandler) tierResponses(ctx context.Context) ([]responses.StorageTierResponse, error) {
	tiers, err := h.db.ListStorageTiers(ctx)
	if err != nil {
		return nil, err //nolint:wrapcheck // logged by the caller
	}

	usage, err := h.db.StorageTierUsage(ctx)
	if err != nil {
		return nil, err //nolint:wrapcheck // logged by the caller
	}

	byBackend := make(map[string]models.TierUsage, len(usage))
	for _, u := range usage {
		byBackend[u.BackendID] = u
	}

	snap := h.reg.Snapshot()
	out := make([]responses.StorageTierResponse, 0, len(tiers))

	for _, t := range tiers {
		resp := responses.StorageTierResponse{
			ID: t.ID, Name: t.Name, Primary: t.BackendID == "", BackendKind: t.BackendKind, BackendStatus: t.BackendStatus,
			Rank: t.Rank, PromoteOnRead: t.PromoteOnRead, CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt,
			BlobCount: byBackend[t.BackendID].Blobs, TotalBytes: byBackend[t.BackendID].Bytes,
		}

		if t.BackendID != "" {
			name := t.BackendName
			resp.Backend = &name
		}

		if rt, ok := snap.ByID(t.ID); ok {
			resp.Available = rt.Store != nil
		}

		out = append(out, resp)
	}

	return out, nil
}

func (h *AdminTieringHandler) handleListTiers(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	items, err := h.tierResponses(r.Context())
	if err != nil {
		h.internalError(w, "failed to list storage tiers", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"total": len(items), "items": items})
}

func (h *AdminTieringHandler) writeTierWriteError(w http.ResponseWriter, err error, action string) {
	if constraint := database.UniqueViolationConstraint(err); constraint != "" {
		msg := "a storage tier with this name already exists"
		if constraint == "idx_storage_tiers_backend" {
			msg = "this storage backend already is a storage tier"
		}

		writeError(w, http.StatusConflict, "conflict", msg)

		return
	}

	h.internalError(w, "failed to "+action+" storage tier", err)
}

type createTierBody struct {
	Name          string `json:"name"`
	Backend       string `json:"backend"`
	Rank          *int   `json:"rank"`
	PromoteOnRead bool   `json:"promote_on_read"`
}

func (h *AdminTieringHandler) handleCreateTier(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	var body createTierBody
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}

	if !tierNameRe.MatchString(body.Name) {
		writeError(w, http.StatusBadRequest, "validation_failed", tierNameMsg)
		return
	}

	if body.Rank != nil && (*body.Rank < 0 || *body.Rank > maxTierRank) {
		writeError(w, http.StatusBadRequest, "validation_failed", "rank must be between 0 and 1000000")
		return
	}

	backend, err := h.db.GetStorageBackend(r.Context(), body.Backend)
	if err != nil {
		h.internalError(w, "failed to get storage backend", err)
		return
	}

	if backend == nil {
		writeError(w, http.StatusBadRequest, "validation_failed", "backend must name an existing storage backend")
		return
	}

	tiers, err := h.db.ListStorageTiers(r.Context())
	if err != nil {
		h.internalError(w, "failed to list storage tiers", err)
		return
	}

	rank := nextTierRank(tiers)
	if body.Rank != nil {
		rank = *body.Rank
	}

	t := &models.StorageTier{ID: uuid.New().String(), Name: body.Name, BackendID: backend.ID, Rank: rank, PromoteOnRead: body.PromoteOnRead}
	if err := h.db.CreateStorageTier(r.Context(), t); err != nil {
		h.writeTierWriteError(w, err, "create")
		return
	}

	auditAdmin(r, audit.ActionSystemStorageBackendChanged, "storage_tier", t.Name, "created")
	h.refreshRegistry(r.Context())

	h.respondTier(w, r, http.StatusCreated, t.Name)
}

func nextTierRank(tiers []*models.StorageTier) int {
	rank := 0
	for _, t := range tiers {
		rank = max(rank, t.Rank)
	}

	return min(rank+tierRankStep, maxTierRank)
}

// respondTier writes the named tier (with its placement stats) as the response.
func (h *AdminTieringHandler) respondTier(w http.ResponseWriter, r *http.Request, status int, name string) {
	items, err := h.tierResponses(r.Context())
	if err != nil {
		h.internalError(w, "failed to load storage tier", err)
		return
	}

	i := slices.IndexFunc(items, func(t responses.StorageTierResponse) bool { return t.Name == name })
	if i < 0 {
		writeError(w, http.StatusNotFound, "not_found", "storage tier not found")
		return
	}

	writeJSON(w, status, items[i])
}

func (h *AdminTieringHandler) handleUpdateTier(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	t, err := h.db.GetStorageTierByName(r.Context(), urlparams.Vars(r)["name"])
	if err != nil {
		h.internalError(w, "failed to get storage tier", err)
		return
	}

	if t == nil {
		writeError(w, http.StatusNotFound, "not_found", "storage tier not found")
		return
	}

	var patch struct {
		Name          *string `json:"name"`
		Rank          *int    `json:"rank"`
		PromoteOnRead *bool   `json:"promote_on_read"`
	}

	if err := decodeJSON(r, &patch); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}

	if msg := applyTierPatch(t, patch.Name, patch.Rank, patch.PromoteOnRead); msg != "" {
		writeError(w, http.StatusBadRequest, "validation_failed", msg)
		return
	}

	if err := h.db.UpdateStorageTier(r.Context(), t); err != nil {
		h.writeTierWriteError(w, err, "update")
		return
	}

	auditAdmin(r, audit.ActionSystemStorageBackendChanged, "storage_tier", t.Name, "updated")
	h.refreshRegistry(r.Context())

	h.respondTier(w, r, http.StatusOK, t.Name)
}

// applyTierPatch applies the set fields to t and returns a validation message
// for an invalid one.
func applyTierPatch(t *models.StorageTier, name *string, rank *int, promote *bool) string {
	if name != nil {
		if !tierNameRe.MatchString(*name) {
			return tierNameMsg
		}

		t.Name = *name
	}

	if rank != nil {
		if *rank < 0 || *rank > maxTierRank {
			return "rank must be between 0 and 1000000"
		}

		t.Rank = *rank
	}

	if promote != nil {
		if *promote && t.BackendID == "" {
			return "the primary tier cannot promote on read: it is where promoted blobs go"
		}

		t.PromoteOnRead = *promote
	}

	return ""
}

func (h *AdminTieringHandler) handleDeleteTier(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	t, err := h.db.GetStorageTierByName(r.Context(), urlparams.Vars(r)["name"])
	if err != nil {
		h.internalError(w, "failed to get storage tier", err)
		return
	}

	if t == nil {
		writeError(w, http.StatusNotFound, "not_found", "storage tier not found")
		return
	}

	if t.BackendID == "" {
		writeError(w, http.StatusBadRequest, "cannot_delete_primary", "the primary storage tier cannot be deleted")
		return
	}

	if msg, err := h.db.DeleteStorageTierIfUnused(r.Context(), t.ID); err != nil {
		if database.IsForeignKeyViolation(err) {
			writeError(w, http.StatusConflict, "conflict", "storage tier is still referenced")
			return
		}

		h.internalError(w, "failed to delete storage tier", err)

		return
	} else if msg != "" {
		writeError(w, http.StatusConflict, "conflict", msg)
		return
	}

	auditAdmin(r, audit.ActionSystemStorageBackendChanged, "storage_tier", t.Name, "deleted")
	h.refreshRegistry(r.Context())

	writeNoContent(w)
}

// ---- policies ----

// tierPolicyBody is the request body for creating, replacing and simulating a policy.
type tierPolicyBody struct {
	Name           string   `json:"name"`
	Enabled        *bool    `json:"enabled"`
	Position       *int     `json:"position"`
	TargetTier     string   `json:"target_tier"`
	FromTier       *string  `json:"from_tier"`
	MinAgeSeconds  *int64   `json:"min_age_seconds"`
	MinIdleSeconds *int64   `json:"min_idle_seconds"`
	MinSizeBytes   *int64   `json:"min_size_bytes"`
	MaxSizeBytes   *int64   `json:"max_size_bytes"`
	RepoGlob       *string  `json:"repo_glob"`
	Formats        []string `json:"formats"`
	PathGlob       *string  `json:"path_glob"`
}

func policyErr(msg string) error { return errors.Join(errTierValidation, validationError(msg)) }

// toModel validates the body against the known tiers and converts it. id,
// timestamps and (when unset) the position are left for the caller.
func (b *tierPolicyBody) toModel(tiers []*models.StorageTier) (*models.StoragePolicy, error) {
	if !storageBackendNameRe.MatchString(b.Name) {
		return nil, policyErr("name must be 1-128 characters: letters, digits, '.', '_' or '-'")
	}

	byName := make(map[string]*models.StorageTier, len(tiers))
	for _, t := range tiers {
		byName[t.Name] = t
	}

	target, ok := byName[b.TargetTier]
	if !ok {
		return nil, policyErr("target_tier must name an existing storage tier")
	}

	p := &models.StoragePolicy{
		Name: b.Name, Enabled: b.Enabled == nil || *b.Enabled, TargetTierID: target.ID,
		MinAgeSeconds: b.MinAgeSeconds, MinIdleSeconds: b.MinIdleSeconds, MinSizeBytes: b.MinSizeBytes, MaxSizeBytes: b.MaxSizeBytes,
		Formats: slices.Clone(b.Formats),
	}

	if b.Position != nil {
		p.Position = *b.Position
	}

	if b.FromTier != nil && *b.FromTier != "" {
		from, ok := byName[*b.FromTier]
		if !ok {
			return nil, policyErr("from_tier must name an existing storage tier")
		}

		if from.ID == target.ID {
			return nil, policyErr("from_tier must differ from target_tier")
		}

		p.FromTierID = from.ID
	}

	if b.RepoGlob != nil {
		p.RepoGlob = *b.RepoGlob
	}

	if b.PathGlob != nil {
		p.PathGlob = *b.PathGlob
	}

	if err := validatePolicyConditions(p); err != nil {
		return nil, err
	}

	return p, nil
}

func nonNegative(v *int64) bool { return v == nil || *v >= 0 }

func validatePolicyBounds(p *models.StoragePolicy) error {
	if p.Position < 0 || p.Position > maxPolicyPosition {
		return policyErr("position must be between 0 and 1000000")
	}

	if !nonNegative(p.MinAgeSeconds) || !nonNegative(p.MinIdleSeconds) || !nonNegative(p.MinSizeBytes) || !nonNegative(p.MaxSizeBytes) {
		return policyErr("age, idle and size conditions must not be negative")
	}

	if p.MinSizeBytes != nil && p.MaxSizeBytes != nil && *p.MinSizeBytes > *p.MaxSizeBytes {
		return policyErr("min_size_bytes must not exceed max_size_bytes")
	}

	return nil
}

func validatePolicyConditions(p *models.StoragePolicy) error {
	if err := validatePolicyBounds(p); err != nil {
		return err
	}

	if len(p.Formats) > maxPolicyFormats {
		return policyErr("too many formats")
	}

	for _, f := range p.Formats {
		if !storageFormatRe.MatchString(f) {
			return policyErr("formats must be lower-case format names such as maven or docker")
		}
	}

	if _, err := tiering.CompilePolicy(p); err != nil {
		return policyErr(err.Error())
	}

	if p.FromTierID == "" && p.MinAgeSeconds == nil && p.MinIdleSeconds == nil && p.MinSizeBytes == nil && p.MaxSizeBytes == nil &&
		p.RepoGlob == "" && len(p.Formats) == 0 && p.PathGlob == "" {
		return policyErr("a policy needs at least one condition; one without any would move every blob")
	}

	return nil
}

func (h *AdminTieringHandler) policyResponses(ctx context.Context, policies []*models.StoragePolicy) ([]responses.StoragePolicyResponse, error) {
	tiers, err := h.db.ListStorageTiers(ctx)
	if err != nil {
		return nil, err //nolint:wrapcheck // logged by the caller
	}

	names := make(map[string]string, len(tiers))
	for _, t := range tiers {
		names[t.ID] = t.Name
	}

	out := make([]responses.StoragePolicyResponse, 0, len(policies))
	for _, p := range policies {
		out = append(out, policyToResponse(p, names))
	}

	return out, nil
}

func policyToResponse(p *models.StoragePolicy, tierNames map[string]string) responses.StoragePolicyResponse {
	resp := responses.StoragePolicyResponse{
		ID: p.ID, Name: p.Name, Enabled: p.Enabled, Position: p.Position, TargetTier: tierNames[p.TargetTierID],
		MinAgeSeconds: p.MinAgeSeconds, MinIdleSeconds: p.MinIdleSeconds, MinSizeBytes: p.MinSizeBytes, MaxSizeBytes: p.MaxSizeBytes,
		Formats: p.Formats, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
	}

	if resp.Formats == nil {
		resp.Formats = []string{}
	}

	if p.FromTierID != "" {
		from := tierNames[p.FromTierID]
		resp.FromTier = &from
	}

	if p.RepoGlob != "" {
		resp.RepoGlob = &p.RepoGlob
	}

	if p.PathGlob != "" {
		resp.PathGlob = &p.PathGlob
	}

	return resp
}

func (h *AdminTieringHandler) handleListPolicies(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	policies, err := h.db.ListStoragePolicies(r.Context())
	if err != nil {
		h.internalError(w, "failed to list storage policies", err)
		return
	}

	items, err := h.policyResponses(r.Context(), policies)
	if err != nil {
		h.internalError(w, "failed to list storage policies", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"total": len(items), "items": items})
}

func (h *AdminTieringHandler) handleGetPolicy(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	p, err := h.db.GetStoragePolicy(r.Context(), urlparams.Vars(r)["id"])
	if err != nil {
		h.internalError(w, "failed to get storage policy", err)
		return
	}

	if p == nil {
		writeError(w, http.StatusNotFound, "not_found", "storage policy not found")
		return
	}

	h.respondPolicy(w, r, http.StatusOK, p)
}

func (h *AdminTieringHandler) respondPolicy(w http.ResponseWriter, r *http.Request, status int, p *models.StoragePolicy) {
	items, err := h.policyResponses(r.Context(), []*models.StoragePolicy{p})
	if err != nil {
		h.internalError(w, "failed to load storage policy", err)
		return
	}

	writeJSON(w, status, items[0])
}

func (h *AdminTieringHandler) writePolicyError(w http.ResponseWriter, err error) bool {
	if errors.Is(err, errTierValidation) {
		if ve, ok := errors.AsType[validationError](err); ok {
			writeError(w, http.StatusBadRequest, "validation_failed", ve.Error())
		} else {
			writeError(w, http.StatusBadRequest, "validation_failed", "invalid storage policy")
		}

		return true
	}

	return false
}

func (h *AdminTieringHandler) handleCreatePolicy(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	var body tierPolicyBody
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}

	tiers, err := h.db.ListStorageTiers(r.Context())
	if err != nil {
		h.internalError(w, "failed to list storage tiers", err)
		return
	}

	p, err := body.toModel(tiers)
	if h.writePolicyError(w, err) {
		return
	}

	if body.Position == nil {
		existing, lerr := h.db.ListStoragePolicies(r.Context())
		if lerr != nil {
			h.internalError(w, "failed to list storage policies", lerr)
			return
		}

		p.Position = nextPolicyPosition(existing)
	}

	p.ID = uuid.New().String()
	if err := h.db.CreateStoragePolicy(r.Context(), p); err != nil {
		h.writePolicyWriteError(w, err, "create")
		return
	}

	auditAdmin(r, audit.ActionSystemStorageBackendChanged, "storage_policy", p.Name, "created")

	stored, err := h.db.GetStoragePolicy(r.Context(), p.ID)
	if err != nil || stored == nil {
		h.internalError(w, "failed to load storage policy", err)
		return
	}

	h.respondPolicy(w, r, http.StatusCreated, stored)
}

func nextPolicyPosition(policies []*models.StoragePolicy) int {
	pos := 0
	for _, p := range policies {
		pos = max(pos, p.Position)
	}

	return min(pos+tierRankStep, maxPolicyPosition)
}

func (h *AdminTieringHandler) writePolicyWriteError(w http.ResponseWriter, err error, action string) {
	if database.UniqueViolationConstraint(err) != "" {
		writeError(w, http.StatusConflict, "conflict", "a storage policy with this name already exists")
		return
	}

	h.internalError(w, "failed to "+action+" storage policy", err)
}

func (h *AdminTieringHandler) handleReplacePolicy(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	id := urlparams.Vars(r)["id"]

	existing, err := h.db.GetStoragePolicy(r.Context(), id)
	if err != nil {
		h.internalError(w, "failed to get storage policy", err)
		return
	}

	if existing == nil {
		writeError(w, http.StatusNotFound, "not_found", "storage policy not found")
		return
	}

	var body tierPolicyBody
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}

	tiers, err := h.db.ListStorageTiers(r.Context())
	if err != nil {
		h.internalError(w, "failed to list storage tiers", err)
		return
	}

	p, err := body.toModel(tiers)
	if h.writePolicyError(w, err) {
		return
	}

	if body.Position == nil {
		p.Position = existing.Position
	}

	p.ID = id
	if err := h.db.UpdateStoragePolicy(r.Context(), p); err != nil {
		h.writePolicyWriteError(w, err, "update")
		return
	}

	auditAdmin(r, audit.ActionSystemStorageBackendChanged, "storage_policy", p.Name, "updated")

	stored, err := h.db.GetStoragePolicy(r.Context(), id)
	if err != nil || stored == nil {
		h.internalError(w, "failed to load storage policy", err)
		return
	}

	h.respondPolicy(w, r, http.StatusOK, stored)
}

func (h *AdminTieringHandler) handleDeletePolicy(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	id := urlparams.Vars(r)["id"]

	existing, err := h.db.GetStoragePolicy(r.Context(), id)
	if err != nil {
		h.internalError(w, "failed to get storage policy", err)
		return
	}

	if existing == nil {
		writeError(w, http.StatusNotFound, "not_found", "storage policy not found")
		return
	}

	if _, err := h.db.DeleteStoragePolicy(r.Context(), id); err != nil {
		h.internalError(w, "failed to delete storage policy", err)
		return
	}

	auditAdmin(r, audit.ActionSystemStorageBackendChanged, "storage_policy", existing.Name, "deleted")

	writeNoContent(w)
}

// ---- dry-run, trigger and status ----

func (h *AdminTieringHandler) handleDryRun(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	var body struct {
		Policy      *tierPolicyBody `json:"policy"`
		SampleLimit int             `json:"sample_limit"`
	}

	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}

	if body.SampleLimit < 0 || body.SampleLimit > tiering.MaxPlanSample {
		writeError(w, http.StatusBadRequest, "validation_failed", "sample_limit must be between 0 and 200")
		return
	}

	var draft *tiering.Policy

	if body.Policy != nil {
		tiers, err := h.db.ListStorageTiers(r.Context())
		if err != nil {
			h.internalError(w, "failed to list storage tiers", err)
			return
		}

		if body.Policy.Name == "" {
			body.Policy.Name = "draft"
		}

		m, err := body.Policy.toModel(tiers)
		if h.writePolicyError(w, err) {
			return
		}

		if draft, err = tiering.CompilePolicy(m); err != nil {
			writeError(w, http.StatusBadRequest, "validation_failed", "invalid storage policy")
			return
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), tieringOpTimeout)
	defer cancel()

	plan, err := h.planner.Plan(ctx, draft, body.SampleLimit)
	if err != nil {
		h.internalError(w, "failed to simulate storage policies", err)
		return
	}

	writeJSON(w, http.StatusOK, dryRunResponse(plan))
}

func dryRunResponse(plan *tiering.Plan) responses.TierDryRunResponse {
	resp := responses.TierDryRunResponse{
		Scanned: plan.Scanned, Truncated: plan.Truncated,
		WouldMove: responses.TierMoveTotalsResponse{Count: plan.Count, Bytes: plan.Bytes},
		Routes:    make([]responses.TierRouteResponse, 0, len(plan.Routes)),
		Policies:  make([]responses.TierPolicyTotalResponse, 0, len(plan.Policies)),
		Sample:    make([]responses.TierMoveSampleResponse, 0, len(plan.Sample)),
	}

	for _, rt := range plan.Routes {
		resp.Routes = append(resp.Routes, responses.TierRouteResponse{From: rt.From, To: rt.To, Count: rt.Count, Bytes: rt.Bytes})
	}

	for _, p := range plan.Policies {
		resp.Policies = append(resp.Policies, responses.TierPolicyTotalResponse{Policy: p.Policy, Count: p.Count, Bytes: p.Bytes})
	}

	for _, s := range plan.Sample {
		resp.Sample = append(resp.Sample, responses.TierMoveSampleResponse{
			SHA256: s.SHA256, SizeBytes: s.SizeBytes, From: s.From, To: s.To, Policy: s.Policy,
			CreatedAt: s.CreatedAt, LastAccessedAt: s.LastAccessedAt,
		})
	}

	return resp
}

func (h *AdminTieringHandler) handleRun(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	if !h.runner.Status().Enabled {
		writeError(w, http.StatusConflict, "conflict", "the tier mover is disabled in the server configuration")
		return
	}

	auditAdmin(r, audit.ActionSystemStorageBackendChanged, "storage_tier", "", "mover run requested")

	started := h.runner.Trigger()
	writeJSON(w, http.StatusAccepted, map[string]any{"started": started})
}

func (h *AdminTieringHandler) handleStatus(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	st := h.runner.Status()

	pendingTotal, err := h.db.CountStorageTierMoves(r.Context())
	if err != nil {
		h.internalError(w, "failed to read tier mover status", err)
		return
	}

	moves, err := h.db.ListStorageTierMoves(r.Context(), pendingMovesListed)
	if err != nil {
		h.internalError(w, "failed to read tier mover status", err)
		return
	}

	snap := h.reg.Snapshot()
	tierName := func(backend string) string {
		if t, ok := snap.ByBackend(backend); ok {
			return t.Name
		}

		return "unknown"
	}

	resp := responses.TierMoverStatusResponse{
		Enabled: st.Enabled, Running: st.Running, IntervalSeconds: st.IntervalSeconds, PendingTotal: pendingTotal,
		Pending: make([]responses.TierPendingMoveResponse, 0, len(moves)),
	}

	if lp := st.LastPass; lp != nil {
		resp.LastPass = &responses.TierPassResponse{
			StartedAt: lp.StartedAt, FinishedAt: lp.FinishedAt, Recovered: lp.Recovered, Promoted: lp.Promoted, Moved: lp.Moved,
			Skipped: lp.Skipped, Failed: lp.Failed, Bytes: lp.Bytes, Scanned: lp.Scanned, Aborted: lp.Aborted,
		}
	}

	for _, m := range moves {
		resp.Pending = append(resp.Pending, responses.TierPendingMoveResponse{
			SHA256: m.SHA256, SizeBytes: m.SizeBytes, From: tierName(m.FromBackendID), To: tierName(m.ToBackendID),
			State: m.State, Reason: m.Reason, Attempts: m.Attempts, UpdatedAt: m.UpdatedAt,
		})
	}

	writeJSON(w, http.StatusOK, resp)
}
