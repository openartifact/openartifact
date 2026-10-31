// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/openartifact/openartifact/internal/api/responses"
	"github.com/openartifact/openartifact/internal/audit"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/curation"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/proxy"
	"github.com/openartifact/openartifact/internal/repotype"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/pkg/logger"
)

// Sources of an availability reason besides the gate's own (curation.SourceMinAge,
// curation.SourceCuration).
const (
	sourceQuarantine        = "quarantine"
	sourceLicensePolicy     = "license_policy"
	sourceRepositoryBlocked = "repository_blocked"
)

// Values of AvailabilityResponse.MinAge.
const (
	minAgeOff     = "off"
	minAgeChecked = "checked"
	minAgeUnknown = "unknown"
)

// maxAvailabilityParam bounds the package and version of an availability query.
const maxAvailabilityParam = 1024

// CurationStore is the part of the database CurationHandler needs.
type CurationStore interface {
	repoAccessStore
	ListCurationRules(ctx context.Context, repoID string) ([]curation.Rule, error)
	GetCurationRule(ctx context.Context, repoID, id string) (*curation.Rule, error)
	CreateCurationRule(ctx context.Context, rule *curation.Rule) (*curation.Rule, error)
	UpdateCurationRule(ctx context.Context, rule *curation.Rule) (*curation.Rule, error)
	DeleteCurationRule(ctx context.Context, repoID, id string) error
	PackageVersionHolds(ctx context.Context, repoID, pkg, version string) (models.VersionHolds, error)
}

// PublishTimeSource is the publish time an upstream states for a package
// version of a proxy repository, read from the metadata already cached. It is
// satisfied by *proxy.Service.
type PublishTimeSource interface {
	PublishTime(ctx context.Context, repo *models.Repository, pkg, version string) (time.Time, bool)
	HeldBackVersions(ctx context.Context, repo *models.Repository, pkg string) ([]proxy.HeldBack, bool, error)
}

// CurationHandler serves the curation rules of proxy repositories and the
// explanation of why a package version is, or is not, available.
type CurationHandler struct {
	repoAccess

	db        CurationStore
	published PublishTimeSource
}

// NewCurationHandler builds the handler. published may be nil: the minimum age
// is then reported as unknown.
func NewCurationHandler(
	db CurationStore, log *logger.Logger, evaluator *auth.Evaluator, published PublishTimeSource,
) *CurationHandler {
	return &CurationHandler{repoAccess: newRepoAccess(db, log, evaluator), db: db, published: published}
}

func (h *CurationHandler) RegisterAuthed(r chi.Router) {
	r.Get("/api/v1/repositories/{name}/curation-rules", h.handleList)
	r.Post("/api/v1/repositories/{name}/curation-rules", h.handleCreate)
	r.With(requireUUIDParams("id")).Patch("/api/v1/repositories/{name}/curation-rules/{id}", h.handleUpdate)
	r.With(requireUUIDParams("id")).Delete("/api/v1/repositories/{name}/curation-rules/{id}", h.handleDelete)
	r.Get("/api/v1/repositories/{name}/availability", h.handleAvailability)
	r.Get("/api/v1/repositories/{name}/held-back-versions", h.handleHeldBack)
}

func curationRuleToResponse(r *curation.Rule) responses.CurationRuleResponse {
	return responses.CurationRuleResponse{
		ID:             r.ID,
		Action:         r.Action,
		PackagePattern: r.Package,
		VersionPattern: r.Version,
		Reason:         r.Reason,
		CreatedBy:      r.CreatedBy,
		CreatedAt:      formatTime(r.CreatedAt),
	}
}

// requireProxy answers 400 unless repo is a proxy repository: only proxies
// have an upstream whose packages curation selects.
func requireProxy(w http.ResponseWriter, repo *models.Repository) bool {
	if repo.RepoType != repotype.Proxy {
		writeError(w, http.StatusBadRequest, "validation_failed", "curation rules are only valid for proxy repositories")
		return false
	}

	return true
}

// writeRuleError maps the store's rule errors to responses and reports whether
// err was one; any other error is the caller's to handle.
func (h *CurationHandler) writeRuleError(w http.ResponseWriter, err error) bool {
	switch {
	case errors.Is(err, curation.ErrInvalidRule):
		writeError(w, http.StatusBadRequest, "validation_failed", err.Error())
	case errors.Is(err, database.ErrCurationRuleLimit):
		writeError(w, http.StatusConflict, "conflict", fmt.Sprintf("a repository holds at most %d curation rules", curation.MaxRules))
	case errors.Is(err, database.ErrCurationRuleNotFound):
		writeError(w, http.StatusNotFound, "not_found", "curation rule not found")
	default:
		return false
	}

	return true
}

// auditRule records a change to a rule of repo, naming the rule's action,
// patterns and reason.
func auditRule(r *http.Request, repo *models.Repository, action, verb string, rule *curation.Rule) {
	details := fmt.Sprintf("curation rule %s of %s %s: action=%s package_pattern=%q version_pattern=%q reason=%q",
		rule.ID, repo.Name, verb, rule.Action, rule.Package, rule.Version, rule.Reason)

	auditRepository(r, repo, action, "curation_rule", rule.ID, details)
}

func (h *CurationHandler) handleList(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.repoThenRepoAdmin(w, r, "list curation rules")
	if !ok || !requireProxy(w, repo) {
		return
	}

	rules, err := h.db.ListCurationRules(r.Context(), repo.ID)
	if err != nil {
		h.dbError(w, "list curation rules: db error", err)
		return
	}

	items := make([]responses.CurationRuleResponse, len(rules))
	for i := range rules {
		items[i] = curationRuleToResponse(&rules[i])
	}

	writeJSON(w, http.StatusOK, responses.CurationRulesResponse{Rules: items})
}

func (h *CurationHandler) handleCreate(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.repoThenRepoAdmin(w, r, "create curation rule")
	if !ok || !requireProxy(w, repo) {
		return
	}

	var body struct {
		Action         string `json:"action"`
		PackagePattern string `json:"package_pattern"`
		VersionPattern string `json:"version_pattern"`
		Reason         string `json:"reason"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeBadJSON(w, err)
		return
	}

	rule := &curation.Rule{
		RepositoryID: repo.ID,
		Action:       body.Action,
		Package:      body.PackagePattern,
		Version:      body.VersionPattern,
		Reason:       body.Reason,
		CreatedBy:    currentUsername(r),
	}

	if err := curation.ValidateRule(rule); err != nil {
		h.writeRuleError(w, err)
		return
	}

	created, err := h.db.CreateCurationRule(r.Context(), rule)
	if err != nil {
		if !h.writeRuleError(w, err) {
			h.dbError(w, "create curation rule: db error", err)
		}

		return
	}

	auditRule(r, repo, audit.ActionCurationRuleCreated, "created", created)

	writeJSON(w, http.StatusCreated, curationRuleToResponse(created))
}

func (h *CurationHandler) handleUpdate(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.repoThenRepoAdmin(w, r, "update curation rule")
	if !ok || !requireProxy(w, repo) {
		return
	}

	rule, err := h.db.GetCurationRule(r.Context(), repo.ID, urlparams.Vars(r)["id"])
	if err != nil {
		if !h.writeRuleError(w, err) {
			h.dbError(w, "update curation rule: db error getting rule", err)
		}

		return
	}

	var body struct {
		Action         *string `json:"action"`
		PackagePattern *string `json:"package_pattern"`
		VersionPattern *string `json:"version_pattern"`
		Reason         *string `json:"reason"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeBadJSON(w, err)
		return
	}

	for field, value := range map[*string]*string{
		&rule.Action:  body.Action,
		&rule.Package: body.PackagePattern,
		&rule.Version: body.VersionPattern,
		&rule.Reason:  body.Reason,
	} {
		if value != nil {
			*field = *value
		}
	}

	if err := curation.ValidateRule(rule); err != nil {
		h.writeRuleError(w, err)
		return
	}

	updated, err := h.db.UpdateCurationRule(r.Context(), rule)
	if err != nil {
		if !h.writeRuleError(w, err) {
			h.dbError(w, "update curation rule: db error", err)
		}

		return
	}

	auditRule(r, repo, audit.ActionCurationRuleUpdated, "updated", updated)

	writeJSON(w, http.StatusOK, curationRuleToResponse(updated))
}

func (h *CurationHandler) handleDelete(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.repoThenRepoAdmin(w, r, "delete curation rule")
	if !ok || !requireProxy(w, repo) {
		return
	}

	id := urlparams.Vars(r)["id"]

	rule, err := h.db.GetCurationRule(r.Context(), repo.ID, id)
	if err != nil {
		if !h.writeRuleError(w, err) {
			h.dbError(w, "delete curation rule: db error getting rule", err)
		}

		return
	}

	if err := h.db.DeleteCurationRule(r.Context(), repo.ID, id); err != nil {
		if !h.writeRuleError(w, err) {
			h.dbError(w, "delete curation rule: db error", err)
		}

		return
	}

	auditRule(r, repo, audit.ActionCurationRuleDeleted, "deleted", rule)

	w.WriteHeader(http.StatusNoContent)
}

// currentUsername is the login name of the authenticated caller ("" for none).
func currentUsername(r *http.Request) string {
	if p := currentPrincipal(r); p != nil && !p.IsAnonymous {
		return p.Username
	}

	return ""
}

// handleAvailability explains whether one package version can be fetched from a
// repository and, when it cannot, why: the same gate the proxy applies to its
// clients, evaluated for the query.
//
//	GET /api/v1/repositories/{name}/availability?package=P&version=V
//
// A proxy repository answers with its blocked flag, its curation rules and its
// minimum age (the publish time comes from the metadata already cached, never
// from the upstream; min_age is "unknown" when the repository has a minimum age
// but the version cannot be dated). A hosted repository answers with its
// blocked flag and the quarantine and license-policy holds of the stored
// version. A group has no verdict of its own - it serves what its members
// serve - so it is a 400: ask a member.
func (h *CurationHandler) handleAvailability(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.loadRepo(w, r, "explain availability")
	if !ok {
		return
	}

	if p := currentPrincipal(r); p != nil && !p.IsAnonymous {
		allowed, err := h.evaluator.Evaluate(r.Context(), *p, repo.Name, "", "read")
		if err != nil || !allowed {
			writeError(w, http.StatusForbidden, "forbidden", "access denied")
			return
		}
	}

	pkg, version := r.URL.Query().Get("package"), r.URL.Query().Get("version")
	if pkg == "" || version == "" || len(pkg) > maxAvailabilityParam || len(version) > maxAvailabilityParam {
		writeError(w, http.StatusBadRequest, "validation_failed",
			fmt.Sprintf("package and version are required (at most %d bytes each)", maxAvailabilityParam))

		return
	}

	if repo.RepoType == repotype.Group {
		writeError(w, http.StatusBadRequest, "validation_failed",
			"a group repository has no availability of its own: ask one of its members")

		return
	}

	resp := responses.AvailabilityResponse{Reasons: []responses.AvailabilityReason{}, MinAge: minAgeOff}

	if repo.Blocked {
		resp.Reasons = append(resp.Reasons, responses.AvailabilityReason{
			Source: sourceRepositoryBlocked, Detail: "the repository is blocked",
		})
	}

	var err error

	if repo.RepoType == repotype.Proxy {
		err = h.explainProxy(r.Context(), repo, pkg, version, &resp)
	} else {
		err = h.explainHosted(r.Context(), repo, pkg, version, &resp)
	}

	if err != nil {
		h.dbError(w, "explain availability: db error", err)
		return
	}

	resp.Available = len(resp.Reasons) == 0

	writeJSON(w, http.StatusOK, resp)
}

// handleHeldBack lists the versions of a proxy package the repository's gate
// (minimum age and curation rules) keeps from its clients, so that a client can
// tell them from versions that do not exist.
//
//	GET /api/v1/repositories/{name}/held-back-versions?package=P
//
// The versions come from the upstream metadata already cached (the upstream is
// never asked): "supported" is false when the format cannot list versions or
// nothing is cached yet. Only proxy repositories have such a list: anything
// else is a 400.
func (h *CurationHandler) handleHeldBack(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.loadRepo(w, r, "list held-back versions")
	if !ok {
		return
	}

	if p := currentPrincipal(r); p != nil && !p.IsAnonymous {
		allowed, err := h.evaluator.Evaluate(r.Context(), *p, repo.Name, "", "read")
		if err != nil || !allowed {
			writeError(w, http.StatusForbidden, "forbidden", "access denied")
			return
		}
	}

	pkg := r.URL.Query().Get("package")
	if pkg == "" || len(pkg) > maxAvailabilityParam {
		writeError(w, http.StatusBadRequest, "validation_failed",
			fmt.Sprintf("package is required (at most %d bytes)", maxAvailabilityParam))

		return
	}

	if repo.RepoType != repotype.Proxy || h.published == nil {
		writeError(w, http.StatusBadRequest, "validation_failed", "only a proxy repository holds versions back")

		return
	}

	held, supported, err := h.published.HeldBackVersions(r.Context(), repo, pkg)
	if err != nil {
		h.dbError(w, "list held-back versions: db error", err)
		return
	}

	resp := responses.HeldBackResponse{Supported: supported, Versions: make([]responses.HeldBackVersion, 0, len(held))}

	for _, v := range held {
		reason := responses.AvailabilityReason{Source: v.Hold.Source, Detail: v.Hold.Detail}
		if !v.Hold.Until.IsZero() {
			reason.Until = v.Hold.Until.UTC().Format(time.RFC3339)
		}

		if v.Hold.Rule != nil {
			reason.RuleID = v.Hold.Rule.ID
		}

		item := responses.HeldBackVersion{Version: v.Version, Reasons: []responses.AvailabilityReason{reason}}
		if !v.Published.IsZero() {
			item.PublishedAt = v.Published.UTC().Format(time.RFC3339)
		}

		resp.Versions = append(resp.Versions, item)
	}

	writeJSON(w, http.StatusOK, resp)
}

// explainProxy adds what the gate of a proxy repository says about the version.
func (h *CurationHandler) explainProxy(
	ctx context.Context, repo *models.Repository, pkg, version string, resp *responses.AvailabilityResponse,
) error {
	var rules []curation.Rule

	if repo.CurationMode != "" && repo.CurationMode != curation.ModeOff {
		var err error

		if rules, err = h.db.ListCurationRules(ctx, repo.ID); err != nil {
			return fmt.Errorf("list curation rules: %w", err)
		}
	}

	gate := curation.NewGate(repo.Format, repo.CurationMode, rules, repo.MinAgeHours, time.Now())

	var published time.Time

	if h.published != nil {
		if t, ok := h.published.PublishTime(ctx, repo, pkg, version); ok {
			published = t
			resp.PublishedAt = t.UTC().Format(time.RFC3339)
		}
	}

	switch {
	case !gate.AgeActive():
		resp.MinAge = minAgeOff
	case published.IsZero():
		resp.MinAge = minAgeUnknown
	default:
		resp.MinAge = minAgeChecked
	}

	if hidden, hold := gate.Hidden(pkg, version, published); hidden {
		reason := responses.AvailabilityReason{Source: hold.Source, Detail: hold.Detail}
		if !hold.Until.IsZero() {
			reason.Until = hold.Until.UTC().Format(time.RFC3339)
		}

		if hold.Rule != nil {
			reason.RuleID = hold.Rule.ID
		}

		resp.Reasons = append(resp.Reasons, reason)
	}

	return nil
}

// explainHosted adds the quarantine and license-policy holds of the stored
// version of a hosted repository.
func (h *CurationHandler) explainHosted(
	ctx context.Context, repo *models.Repository, pkg, version string, resp *responses.AvailabilityResponse,
) error {
	holds, err := h.db.PackageVersionHolds(ctx, repo.ID, pkg, version)
	if err != nil {
		return fmt.Errorf("package version holds: %w", err)
	}

	if holds.Quarantined {
		resp.Reasons = append(resp.Reasons, responses.AvailabilityReason{
			Source: sourceQuarantine, Detail: "a file of the version is quarantined by a vulnerability or secret scan",
		})
	}

	if holds.LicenseBlocked {
		resp.Reasons = append(resp.Reasons, responses.AvailabilityReason{
			Source: sourceLicensePolicy, Detail: "a file of the version is held by the repository's license policy",
		})
	}

	return nil
}
