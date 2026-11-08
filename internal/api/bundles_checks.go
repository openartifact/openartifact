// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/openartifact/openartifact/internal/audit"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/bundlechecks"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/internal/webhook"
)

// Required external checks. A CI system reports the result of a named check
// against a release bundle; a pipeline stage that lists the name in
// required_checks refuses promotion until the check is a live success
// (validatePromotionStage). Reporting is authorized apart from the other bundle
// routes: an administrator, or a principal holding `write` on every source
// repository of the bundle (a deploy or OIDC-exchanged CI token), so a
// pipeline that publishes the artifacts can vouch for them without `manage` and
// without being the bundle's owner. Reading the checks needs the bundle to be
// visible, like the rest of the bundle, or the same write access.

// checkReportBody is the request body of POST /api/v1/bundles/{name}/{version}/checks.
type checkReportBody struct {
	Name       string     `json:"name"`
	Status     string     `json:"status"`
	Summary    string     `json:"summary"`
	DetailsURL string     `json:"details_url"`
	ExpiresAt  *time.Time `json:"expires_at"`
}

// validate returns a client-facing message for a body that cannot be stored.
func (b *checkReportBody) validate(now time.Time) string {
	if msg := bundlechecks.ValidateName(b.Name); msg != "" {
		return msg
	}

	switch {
	case !bundlechecks.ValidStatus(models.BundleCheckStatus(b.Status)):
		return "status must be one of pending, success, failure, error"
	case len(b.Summary) > bundlechecks.MaxSummaryLen:
		return "summary is too long"
	case b.ExpiresAt != nil && !b.ExpiresAt.After(now):
		return "expires_at must be in the future"
	}

	return validCheckURL(b.DetailsURL)
}

// validCheckURL returns a message unless raw is empty or an absolute http(s) URL.
func validCheckURL(raw string) string {
	if raw == "" {
		return ""
	}

	if len(raw) > bundlechecks.MaxURLLen {
		return "details_url is too long"
	}

	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "details_url must be an absolute http or https URL"
	}

	return ""
}

func (h *BundlesHandler) handleListChecks(w http.ResponseWriter, r *http.Request) {
	bundle, _, ok := h.loadBundleForChecks(w, r, false)
	if !ok {
		return
	}

	checks, err := h.db.ListBundleChecks(r.Context(), bundle.ID)
	if err != nil {
		h.dbError(w, "list bundle checks: db error", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"checks": mapList(checks, bundleCheckToMap)})
}

func (h *BundlesHandler) handleReportCheck(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	bundle, items, ok := h.loadBundleForChecks(w, r, true)
	if !ok {
		return
	}

	var body checkReportBody
	if err := decodeJSON(r, &body); err != nil {
		writeBadJSON(w, err)
		return
	}

	if msg := body.validate(time.Now()); msg != "" {
		writeError(w, http.StatusBadRequest, "validation_failed", msg)
		return
	}

	if bundle.State == models.BundleStateArchived {
		writeError(w, http.StatusConflict, "conflict", "an archived bundle takes no checks")
		return
	}

	check := &models.BundleCheck{
		BundleID:   bundle.ID,
		Name:       body.Name,
		Status:     models.BundleCheckStatus(body.Status),
		Summary:    body.Summary,
		DetailsURL: body.DetailsURL,
		ExpiresAt:  body.ExpiresAt,
	}
	if p := currentPrincipal(r); p != nil {
		check.ReportedBy = p.Username
	}

	if err := h.db.UpsertBundleCheck(ctx, check); err != nil {
		h.dbError(w, "report bundle check: db error", err)
		return
	}

	auditAdmin(r, audit.ActionBundleCheckReported, "bundle", bundle.ID,
		fmt.Sprintf("check %s of bundle %s %s reported %s", check.Name, bundle.Name, bundle.Version, check.Status))
	h.announceCheck(ctx, bundle, items, check)

	writeJSON(w, http.StatusOK, bundleCheckToMap(check))
}

// announceCheck emits bundle.check_updated to the webhooks of the bundle's first
// source repository (and the global ones).
func (h *BundlesHandler) announceCheck(
	ctx context.Context,
	bundle *models.ReleaseBundle,
	items []*models.ReleaseBundleItem,
	c *models.BundleCheck,
) {
	if h.publisher == nil {
		return
	}

	update := webhook.BundleCheckUpdate{
		Bundle: bundle.Name, Version: bundle.Version, Check: c.Name, Status: string(c.Status),
		Summary: c.Summary, DetailsURL: c.DetailsURL, ReportedBy: c.ReportedBy, ExpiresAt: c.ExpiresAt,
	}

	repoID := ""

	if len(items) > 0 {
		repo, err := h.db.GetRepositoryByID(ctx, items[0].SourceRepoID)
		if err != nil {
			h.log.Errorw("bundle check webhook: reading the source repository", "error", err)
		} else if repo != nil {
			repoID, update.Repository, update.Format = repo.ID, repo.Name, repo.Format
		}
	}

	webhook.Emit(ctx, h.publisher, repoID, webhook.EventBundleCheckUpdated, webhook.BundleCheckUpdatedPayload(update))
}

// loadBundleForChecks resolves the bundle {name}/{version} and authorizes the
// caller to read its checks, or with report to report one (see the comment at
// the top of this file). A principal that may report may also read, so a token
// holding only `write` can see what it reported. It writes the error response
// and returns ok=false on failure: 404 for an unknown bundle or one the caller
// can neither see nor vouch for, 403 for a visible bundle the caller may not
// report on.
func (h *BundlesHandler) loadBundleForChecks(
	w http.ResponseWriter,
	r *http.Request,
	report bool,
) (*models.ReleaseBundle, []*models.ReleaseBundleItem, bool) {
	ctx := r.Context()
	p := currentPrincipal(r)

	bundle, err := h.db.GetReleaseBundle(ctx, urlparams.Vars(r)["name"], urlparams.Vars(r)["version"])
	if err != nil {
		h.dbError(w, "report bundle check: db error getting bundle", err)
		return nil, nil, false
	}

	if bundle == nil {
		writeError(w, http.StatusNotFound, "not_found", "bundle not found")
		return nil, nil, false
	}

	access, err := h.bundleAccessFor(ctx, p, bundle)
	if err != nil {
		h.log.Errorw("bundle authorization: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "auth evaluation failed")

		return nil, nil, false
	}

	if access.admin {
		items, err := h.db.ListBundleItems(ctx, bundle.ID)
		if err != nil {
			h.dbError(w, "report bundle check: db error listing items", err)
			return nil, nil, false
		}

		return bundle, items, true
	}

	writes, err := h.writesAllSources(ctx, p, access.items)
	if err != nil {
		h.log.Errorw("bundle authorization: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "auth evaluation failed")

		return nil, nil, false
	}

	switch {
	case writes, access.visible() && !report:
		return bundle, access.items, true
	case access.visible():
		writeError(w, http.StatusForbidden, "forbidden", "write access to every source repository of the bundle is required")
	default:
		writeError(w, http.StatusNotFound, "not_found", "bundle not found")
	}

	return nil, nil, false
}

// writesAllSources reports whether p holds `write` on the source repository of
// every item. A bundle without items has no repository to vouch for: false.
func (h *BundlesHandler) writesAllSources(ctx context.Context, p *auth.Principal, items []*models.ReleaseBundleItem) (bool, error) {
	if len(items) == 0 || p == nil || p.IsAnonymous {
		return false, nil
	}

	checked := make(map[string]struct{}, len(items))

	for _, item := range items {
		if _, done := checked[item.SourceRepoID]; done {
			continue
		}

		checked[item.SourceRepoID] = struct{}{}

		repo, err := h.db.GetRepositoryByID(ctx, item.SourceRepoID)
		if err != nil || repo == nil {
			return false, err //nolint:wrapcheck // caller logs and maps to 500
		}

		ok, err := h.evaluator.Evaluate(ctx, *p, repo.Name, "", "write")
		if err != nil || !ok {
			return false, err //nolint:wrapcheck // caller logs and maps to 500
		}
	}

	return true, nil
}

// requiredChecksFailure is the 422 answer of a promotion into stage while a
// check the stage requires is not a live success; nil when it may proceed.
func (h *BundlesHandler) requiredChecksFailure(ctx context.Context, bundle *models.ReleaseBundle, stage models.PipelineStage) *apiFailure {
	if len(stage.RequiredChecks) == 0 {
		return nil
	}

	reported, err := h.db.ListBundleChecks(ctx, bundle.ID)
	if err != nil {
		h.log.Errorw("promote bundle: db error listing checks", "error", err)
		return &apiFailure{http.StatusInternalServerError, "internal_error", "database error"}
	}

	verdict := bundlechecks.Evaluate(stage.RequiredChecks, reported, time.Now())
	if verdict.Satisfied() {
		return nil
	}

	return &apiFailure{http.StatusUnprocessableEntity, "CHECKS_REQUIRED",
		"stage " + stage.Name + " requires checks that are not satisfied (" + verdict.String() + ")"}
}

func bundleCheckToMap(c *models.BundleCheck) map[string]any {
	return map[string]any{
		"name":        c.Name,
		"status":      c.Status,
		"summary":     c.Summary,
		"details_url": c.DetailsURL,
		"reported_by": c.ReportedBy,
		"updated_at":  formatTime(c.UpdatedAt),
		"expires_at":  formatTimePtr(c.ExpiresAt),
	}
}
