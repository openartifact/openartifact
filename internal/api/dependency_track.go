// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/openartifact/openartifact/internal/api/responses"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/crypto"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/dtrack"
	"github.com/openartifact/openartifact/internal/netutil"
	"github.com/openartifact/openartifact/internal/sbom"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/pkg/logger"
)

const (
	maxDTrackPushes = 500
	dtrackTestTime  = 30 * time.Second
	maxDTrackURLLen = 2048
	maxDTrackKeyLen = 512
	// dtrackBOMSpec is the CycloneDX version of the BOM sent to Dependency-Track.
	dtrackBOMSpec = sbom.CycloneDX15
)

// DTrackPusher is what the handler needs from the background pusher.
type DTrackPusher interface {
	Enqueue(j dtrack.Job) bool
}

// DependencyTrackHandler manages the Dependency-Track integration: the global
// settings, per-repository overrides, connection tests and manual pushes.
type DependencyTrackHandler struct {
	db        database.Database
	log       *logger.Logger
	evaluator *auth.Evaluator
	pusher    DTrackPusher
}

// NewDependencyTrackHandler creates the handler; pusher may be nil (manual pushes then answer 501).
func NewDependencyTrackHandler(db database.Database, log *logger.Logger, evaluator *auth.Evaluator, pusher DTrackPusher) *DependencyTrackHandler {
	return &DependencyTrackHandler{db: db, log: log, evaluator: evaluator, pusher: pusher}
}

// RegisterAuthed registers the admin routes.
func (h *DependencyTrackHandler) RegisterAuthed(r chi.Router) {
	r.Get("/api/v1/admin/dependency-track", h.handleGetGlobal)
	r.Put("/api/v1/admin/dependency-track", h.handlePutGlobal)
	r.Post("/api/v1/admin/dependency-track/test", h.handleTestGlobal)

	const repo = "/api/v1/repositories/{name}/dependency-track"

	r.Get(repo, h.handleGetRepo)
	r.Put(repo, h.handlePutRepo)
	r.Delete(repo, h.handleDeleteRepo)
	r.Post(repo+"/test", h.handleTestRepo)
	r.Post(repo+"/push", h.handlePush)
	r.Get(repo+"/pushes", h.handleListPushes)
}

// ---- request validation ---------------------------------------------------

type dtrackSettingsRequest struct {
	Enabled        bool    `json:"enabled"`
	BaseURL        string  `json:"base_url"`
	APIKey         *string `json:"api_key"`
	ProjectName    string  `json:"project_name"`
	ProjectVersion string  `json:"project_version"`
	AutoPush       bool    `json:"auto_push"`
	PushVEX        bool    `json:"push_vex"`
}

type dtrackOverrideRequest struct {
	Enabled        bool    `json:"enabled"`
	BaseURL        string  `json:"base_url"`
	APIKey         *string `json:"api_key"`
	ProjectName    string  `json:"project_name"`
	ProjectVersion string  `json:"project_version"`
	AutoPush       *bool   `json:"auto_push"`
	PushVEX        *bool   `json:"push_vex"`
}

// validateDTrackFields checks the fields shared by the global and per-repo requests.
func validateDTrackFields(ctx context.Context, baseURL string, apiKey *string, name, version string) string {
	if baseURL != "" {
		if len(baseURL) > maxDTrackURLLen {
			return "base_url is too long"
		}

		if err := netutil.ValidateOutboundURLContext(ctx, baseURL); err != nil {
			return "base_url must be an http(s) URL of a reachable, non-internal host"
		}
	}

	if apiKey != nil {
		if len(*apiKey) > maxDTrackKeyLen {
			return "api_key is too long"
		}

		if err := crypto.CheckPlaintext(*apiKey); err != nil {
			return "api_key must be the plaintext key"
		}
	}

	if err := dtrack.ValidateTemplate(name); err != nil {
		return "project_name: " + err.Error()
	}

	if err := dtrack.ValidateTemplate(version); err != nil {
		return "project_version: " + err.Error()
	}

	return ""
}

// ---- global settings -------------------------------------------------------

func settingsResponse(s dtrack.Settings) responses.DependencyTrackSettingsResponse {
	return responses.DependencyTrackSettingsResponse{
		Enabled: s.Enabled, BaseURL: s.BaseURL, APIKeySet: s.APIKey != "",
		ProjectName: s.ProjectName, ProjectVersion: s.ProjectVersion,
		AutoPush: s.AutoPush, PushVEX: s.PushVEX,
	}
}

func (h *DependencyTrackHandler) handleGetGlobal(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	s, err := dtrack.LoadGlobal(r.Context(), h.db)
	if err != nil {
		h.log.Errorw("dependency-track: loading settings failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to load settings")

		return
	}

	writeJSON(w, http.StatusOK, settingsResponse(s))
}

func (h *DependencyTrackHandler) handlePutGlobal(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	var body dtrackSettingsRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}

	ctx := r.Context()

	if msg := validateDTrackFields(ctx, body.BaseURL, body.APIKey, body.ProjectName, body.ProjectVersion); msg != "" {
		writeError(w, http.StatusBadRequest, "validation_failed", msg)
		return
	}

	cur, err := dtrack.LoadGlobal(ctx, h.db)
	if err != nil {
		h.log.Errorw("dependency-track: loading settings failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to load settings")

		return
	}

	next := dtrack.Settings{
		Enabled: body.Enabled, BaseURL: body.BaseURL, APIKey: cur.APIKey,
		ProjectName: body.ProjectName, ProjectVersion: body.ProjectVersion,
		AutoPush: body.AutoPush, PushVEX: body.PushVEX,
	}

	// The API key is write-only: absent keeps the stored one, "" clears it.
	if body.APIKey != nil {
		next.APIKey = *body.APIKey
	}

	if next.Enabled && (next.BaseURL == "" || next.APIKey == "") {
		writeError(w, http.StatusBadRequest, "validation_failed", "base_url and api_key are required to enable the integration")
		return
	}

	if err := dtrack.SaveGlobal(ctx, h.db, next); err != nil {
		h.log.Errorw("dependency-track: saving settings failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to save settings")

		return
	}

	writeJSON(w, http.StatusOK, settingsResponse(next))
}

func (h *DependencyTrackHandler) handleTestGlobal(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	s, err := dtrack.LoadGlobal(r.Context(), h.db)
	if err != nil {
		h.log.Errorw("dependency-track: loading settings failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to load settings")

		return
	}

	h.testConnection(w, r, s.BaseURL, s.APIKey, s.PushVEX)
}

// testConnection runs a connection test; failures are answered 400 with a
// generic reason (the upstream's answer is logged, not echoed).
func (h *DependencyTrackHandler) testConnection(w http.ResponseWriter, r *http.Request, baseURL, apiKey string, needVEX bool) {
	if baseURL == "" || apiKey == "" {
		writeError(w, http.StatusBadRequest, "validation_failed", "base_url and api_key must be configured first")
		return
	}

	cl, err := dtrack.NewClient(baseURL, apiKey)
	if err != nil {
		writeError(w, http.StatusBadRequest, "validation_failed", err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), dtrackTestTime)
	defer cancel()

	info, err := cl.Check(ctx, needVEX)
	if err != nil {
		h.log.Warnw("dependency-track: connection test failed", "error", err)
		writeError(w, http.StatusBadRequest, "validation_failed",
			"could not reach Dependency-Track or the API key was rejected; check the URL and key (see the server log)")

		return
	}

	writeJSON(w, http.StatusOK, responses.DependencyTrackTestResponse{
		Version: info.Version, Permissions: info.Permissions, MissingPermissions: info.MissingPermissions,
	})
}

// ---- per-repository --------------------------------------------------------

func (h *DependencyTrackHandler) adminRepo(w http.ResponseWriter, r *http.Request, op string) (*models.Repository, bool) {
	if !requireAdmin(w, r, h.evaluator) {
		return nil, false
	}

	repo, err := h.db.GetRepositoryByName(r.Context(), urlparams.Vars(r)["name"])
	if err != nil {
		h.log.Errorw("dependency-track "+op+": db error getting repo", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return nil, false
	}

	if repo == nil {
		writeError(w, http.StatusNotFound, "not_found", "repository not found")
		return nil, false
	}

	return repo, true
}

func overrideResponse(o *models.RepositoryDependencyTrack) *responses.DependencyTrackOverrideResponse {
	if o == nil {
		return nil
	}

	return &responses.DependencyTrackOverrideResponse{
		Enabled: o.Enabled, BaseURL: o.BaseURL, APIKeySet: o.APIKey != "",
		ProjectName: o.ProjectName, ProjectVersion: o.ProjectVersion, AutoPush: o.AutoPush, PushVEX: o.PushVEX,
	}
}

func effectiveResponse(e dtrack.Effective) responses.DependencyTrackEffectiveResponse {
	name, version := e.ProjectName, e.ProjectVersion
	if name == "" {
		name = dtrack.DefaultProjectName
	}

	if version == "" {
		version = dtrack.DefaultProjectVersion
	}

	return responses.DependencyTrackEffectiveResponse{
		Enabled: e.Enabled, Ready: e.Ready(), Source: e.Source, BaseURL: e.BaseURL, APIKeySet: e.APIKey != "",
		ProjectName: name, ProjectVersion: version, AutoPush: e.AutoPush, PushVEX: e.PushVEX,
	}
}

func (h *DependencyTrackHandler) repoResponse(ctx context.Context, repoID string) (*responses.DependencyTrackRepositoryResponse, error) {
	row, err := h.db.GetRepositoryDependencyTrack(ctx, repoID)
	if err != nil {
		return nil, err //nolint:wrapcheck // caller logs and maps to 500
	}

	global, err := dtrack.LoadGlobal(ctx, h.db)
	if err != nil {
		return nil, err //nolint:wrapcheck // caller logs and maps to 500
	}

	eff, err := dtrack.Resolve(global, row)
	if err != nil {
		return nil, err //nolint:wrapcheck // caller logs and maps to 500
	}

	return &responses.DependencyTrackRepositoryResponse{Override: overrideResponse(row), Effective: effectiveResponse(eff)}, nil
}

func (h *DependencyTrackHandler) handleGetRepo(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.adminRepo(w, r, "get")
	if !ok {
		return
	}

	resp, err := h.repoResponse(r.Context(), repo.ID)
	if err != nil {
		h.log.Errorw("dependency-track: loading repository settings failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to load settings")

		return
	}

	writeJSON(w, http.StatusOK, resp)
}

func (h *DependencyTrackHandler) handlePutRepo(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.adminRepo(w, r, "put")
	if !ok {
		return
	}

	var body dtrackOverrideRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}

	ctx := r.Context()

	if msg := validateDTrackFields(ctx, body.BaseURL, body.APIKey, body.ProjectName, body.ProjectVersion); msg != "" {
		writeError(w, http.StatusBadRequest, "validation_failed", msg)
		return
	}

	row, err := h.db.GetRepositoryDependencyTrack(ctx, repo.ID)
	if err != nil {
		h.log.Errorw("dependency-track: loading repository settings failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to load settings")

		return
	}

	next := &models.RepositoryDependencyTrack{
		RepositoryID: repo.ID, Enabled: body.Enabled, BaseURL: body.BaseURL,
		ProjectName: body.ProjectName, ProjectVersion: body.ProjectVersion,
		AutoPush: body.AutoPush, PushVEX: body.PushVEX,
	}
	if row != nil {
		next.APIKey = row.APIKey
	}

	if body.APIKey != nil {
		enc, encErr := crypto.Encrypt(*body.APIKey)
		if encErr != nil {
			h.log.Errorw("dependency-track: encrypting API key failed", "error", encErr)
			writeError(w, http.StatusInternalServerError, "internal_error", "failed to save settings")

			return
		}

		next.APIKey = enc
		if *body.APIKey == "" {
			next.APIKey = ""
		}
	}

	if err := h.db.UpsertRepositoryDependencyTrack(ctx, next); err != nil {
		h.log.Errorw("dependency-track: saving repository settings failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to save settings")

		return
	}

	resp, err := h.repoResponse(ctx, repo.ID)
	if err != nil {
		h.log.Errorw("dependency-track: loading repository settings failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to load settings")

		return
	}

	writeJSON(w, http.StatusOK, resp)
}

func (h *DependencyTrackHandler) handleDeleteRepo(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.adminRepo(w, r, "delete")
	if !ok {
		return
	}

	existed, err := h.db.DeleteRepositoryDependencyTrack(r.Context(), repo.ID)
	if err != nil {
		h.log.Errorw("dependency-track: deleting repository settings failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to delete settings")

		return
	}

	if !existed {
		writeError(w, http.StatusNotFound, "not_found", "repository has no Dependency-Track override")
		return
	}

	writeNoContent(w)
}

func (h *DependencyTrackHandler) handleTestRepo(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.adminRepo(w, r, "test")
	if !ok {
		return
	}

	eff, err := dtrack.EffectiveFor(r.Context(), h.db, repo.ID)
	if err != nil {
		h.log.Errorw("dependency-track: loading settings failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to load settings")

		return
	}

	h.testConnection(w, r, eff.BaseURL, eff.APIKey, eff.PushVEX)
}

func (h *DependencyTrackHandler) handlePush(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.adminRepo(w, r, "push")
	if !ok {
		return
	}

	var body struct {
		ArtifactID string `json:"artifact_id"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}

	if msg := validateArtifactID(body.ArtifactID); msg != "" {
		writeError(w, http.StatusBadRequest, "validation_failed", msg)
		return
	}

	if h.pusher == nil {
		writeError(w, http.StatusNotImplemented, "not_implemented", "Dependency-Track pushes are not enabled")
		return
	}

	ctx := r.Context()

	eff, err := dtrack.EffectiveFor(ctx, h.db, repo.ID)
	if err != nil {
		h.log.Errorw("dependency-track: loading settings failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to load settings")

		return
	}

	if !eff.Ready() {
		writeError(w, http.StatusConflict, "conflict", "Dependency-Track is not enabled or not fully configured for this repository")
		return
	}

	rec := &models.DependencyTrackPush{RepositoryID: repo.ID, ArtifactID: body.ArtifactID, State: models.DTrackPushQueued}

	if prev, err := h.db.GetDependencyTrackPush(ctx, repo.ID, body.ArtifactID); err == nil && prev != nil {
		*rec = *prev
		rec.State, rec.Detail = models.DTrackPushQueued, ""
	}

	if err := h.db.UpsertDependencyTrackPush(ctx, rec); err != nil {
		h.log.Errorw("dependency-track: recording queued push failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if !h.pusher.Enqueue(dtrack.Job{RepoID: repo.ID, ArtifactID: body.ArtifactID, Trigger: dtrack.TriggerManual}) {
		writeError(w, http.StatusConflict, "conflict", "the push queue is full; try again shortly")
		return
	}

	writeJSON(w, http.StatusAccepted, pushResponse(rec))
}

func pushResponse(p *models.DependencyTrackPush) responses.DependencyTrackPushResponse {
	opt := func(t *time.Time) *string {
		if t == nil {
			return nil
		}

		s := formatTime(*t)

		return &s
	}

	return responses.DependencyTrackPushResponse{
		ArtifactID: p.ArtifactID, State: p.State, Detail: p.Detail, ProjectUUID: p.ProjectUUID,
		ProjectName: p.ProjectName, ProjectVersion: p.ProjectVersion, Components: p.Components,
		VEXStatements: p.VEXStatements, VEXApplied: p.VEXApplied,
		BOMPushedAt: opt(p.BOMPushedAt), VEXPushedAt: opt(p.VEXPushedAt), UpdatedAt: formatTime(p.UpdatedAt),
	}
}

func (h *DependencyTrackHandler) handleListPushes(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.adminRepo(w, r, "pushes")
	if !ok {
		return
	}

	rows, err := h.db.ListDependencyTrackPushes(r.Context(), repo.ID, maxDTrackPushes)
	if err != nil {
		h.log.Errorw("dependency-track: listing pushes failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	out := make([]responses.DependencyTrackPushResponse, 0, len(rows))
	for _, p := range rows {
		out = append(out, pushResponse(p))
	}

	writeJSON(w, http.StatusOK, map[string]any{"pushes": out})
}

// ---- document sources --------------------------------------------------------

// DTrackSources builds the documents pushed to Dependency-Track from stored state.
type DTrackSources struct {
	db  database.Database
	gen *sbom.Generator
}

// NewDTrackSources creates the sources over db and the SBOM generator.
func NewDTrackSources(db database.Database, gen *sbom.Generator) *DTrackSources {
	return &DTrackSources{db: db, gen: gen}
}

// BOM implements dtrack.Sources: the CycloneDX SBOM without its vulnerabilities
// (Dependency-Track runs its own analysis and receives the decisions as VEX).
func (s *DTrackSources) BOM(ctx context.Context, repo *models.Repository, artifactID string) ([]byte, int, error) {
	metadata, err := storedArtifactMetadata(ctx, s.db, repo.ID, artifactID)
	if err != nil {
		return nil, 0, err
	}

	res, err := s.gen.GenerateCycloneDX(ctx, repo.ID, artifactID, repo.Format, metadata, dtrackBOMSpec)
	if err != nil {
		return nil, 0, err //nolint:wrapcheck // wrapped by the pusher
	}

	var doc map[string]json.RawMessage
	if err := json.Unmarshal(res.Body, &doc); err != nil {
		return nil, 0, err //nolint:wrapcheck // wrapped by the pusher
	}

	delete(doc, "vulnerabilities")

	out, err := json.Marshal(doc)
	if err != nil {
		return nil, 0, err //nolint:wrapcheck // wrapped by the pusher
	}

	return out, res.Components, nil
}

// VEX implements dtrack.Sources.
func (s *DTrackSources) VEX(ctx context.Context, repo *models.Repository, artifactID string, opts sbom.VEXOptions) ([]byte, int, error) {
	metadata, err := storedArtifactMetadata(ctx, s.db, repo.ID, artifactID)
	if err != nil {
		return nil, 0, err
	}

	in, err := s.gen.VEXInput(ctx, repo.ID, repo.Format, artifactID, metadata)
	if err != nil {
		return nil, 0, err //nolint:wrapcheck // wrapped by the pusher
	}

	res, err := s.gen.GenerateCycloneDXVEXWith([]*sbom.Input{in}, dtrackBOMSpec, opts)
	if err != nil {
		return nil, 0, err //nolint:wrapcheck // wrapped by the pusher
	}

	return res.Body, res.Components, nil
}
