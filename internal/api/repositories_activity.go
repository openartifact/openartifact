// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"net/http"
	"slices"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/audit"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/pkg/logger"
)

// RepositoryActivityStore is the part of the database RepositoryActivityHandler needs.
type RepositoryActivityStore interface {
	activityPageStore
	GetRepositoryByName(ctx context.Context, name string) (*models.Repository, error)
	ListAuditLogs(ctx context.Context, params models.AuditListParams) (*models.AuditPage, error)
}

// RepositoryActivityHandler serves the audit feed of one repository.
type RepositoryActivityHandler struct {
	db        RepositoryActivityStore
	log       *logger.Logger
	evaluator *auth.Evaluator
}

func NewRepositoryActivityHandler(db RepositoryActivityStore, log *logger.Logger, evaluator *auth.Evaluator) *RepositoryActivityHandler {
	return &RepositoryActivityHandler{db: db, log: log, evaluator: evaluator}
}

func (h *RepositoryActivityHandler) RegisterAuthed(r chi.Router) {
	r.Get("/api/v1/repositories/{name}/activity", h.handleList)
}

// activityCategory is one event type of the repository timeline: the audit
// actions it covers and whether it is reserved to the repository's admins.
type activityCategory struct {
	actions   []string
	adminOnly bool
}

// activityCategories is the closed set of values of the `category` filter.
// Whoever may read the repository sees the events of the artifacts it holds:
// uploads, deletions, trash restores and purges, promotions in and out,
// quarantine and release. The operational events - who changed which setting,
// what the cleanup policies removed and the federation traffic - name
// administrators' decisions and the infrastructure around the repository and
// stay with its admins.
//
//nolint:gochecknoglobals // immutable lookup table
var activityCategories = map[string]activityCategory{
	"uploads":    {actions: []string{audit.ActionArtifactUploaded}},
	"deletions":  {actions: []string{audit.ActionArtifactDeleted}},
	"trash":      {actions: []string{audit.ActionArtifactRestored, audit.ActionArtifactPurged, audit.ActionRepositoryRestored}},
	"promotions": {actions: []string{audit.ActionBundlePromoted, audit.ActionArtifactPromoted}},
	"quarantine": {actions: []string{audit.ActionArtifactQuarantined, audit.ActionArtifactReleased}},
	"cleanup": {
		actions:   []string{audit.ActionCleanupPolicyChanged, audit.ActionCleanupRunStarted, audit.ActionCleanupRunCompleted},
		adminOnly: true,
	},
	"settings": {
		actions: []string{
			audit.ActionRepositoryCreated, audit.ActionRepositoryUpdated,
			audit.ActionRepositoryBlocked, audit.ActionRepositoryUnblocked,
		},
		adminOnly: true,
	},
	"federation": {
		actions: []string{
			audit.ActionFederationEnabledChanged, audit.ActionFederationPeerCreated, audit.ActionFederationPeerUpdated,
			audit.ActionFederationPeerDeleted, audit.ActionFederationPeerResynced,
			audit.ActionFederationTokenCreated, audit.ActionFederationTokenRevoked,
		},
		adminOnly: true,
	},
}

// activityFilterError is a refused `category` filter: its HTTP status and the
// message for the client.
type activityFilterError struct {
	status  int
	message string
}

// activityActions turns the `category` parameter (comma separated, repeatable)
// into the audit actions to keep. With no category an admin gets every event of
// the repository (nil), anyone else every category open to readers. An unknown
// category is a 400, an admin-only one asked by a non-admin a 403.
func activityActions(values []string, isAdmin bool) ([]string, *activityFilterError) {
	var names []string

	for _, v := range values {
		for n := range strings.SplitSeq(v, ",") {
			if n = strings.TrimSpace(n); n != "" {
				names = append(names, n)
			}
		}
	}

	if len(names) == 0 {
		if isAdmin {
			return nil, nil
		}

		return readerActions(), nil
	}

	var actions []string

	for _, n := range names {
		c, ok := activityCategories[n]
		if !ok {
			return nil, &activityFilterError{http.StatusBadRequest, "unknown category"}
		}

		if c.adminOnly && !isAdmin {
			return nil, &activityFilterError{http.StatusForbidden, "admin access required for this category"}
		}

		actions = append(actions, c.actions...)
	}

	slices.Sort(actions)

	return slices.Compact(actions), nil
}

// readerActions lists the actions of every category open to readers.
func readerActions() []string {
	var out []string

	for _, c := range activityCategories {
		if !c.adminOnly {
			out = append(out, c.actions...)
		}
	}

	slices.Sort(out)

	return out
}

// handleList returns the audit events recorded against one repository, newest
// first, with actor and subject names resolved. Keyset paging only (`cursor`,
// `next_cursor`, `has_more`): no row is counted. package_name and version keep
// only the events that record that package version; `category` (comma
// separated: uploads, deletions, trash, promotions, quarantine, cleanup,
// settings, federation) keeps one or more event types.
//
// Visibility follows the repository: whoever may read it (the route's RBAC
// scope is a read of the repository, anonymous included on a public one) sees
// the artifact events (activityCategories); its admins - global admins, the
// project's maintainers and delegated repository admins - also see settings,
// cleanup and federation events. The server applies the restriction, whatever
// the client asks for. Client addresses are never returned, and an anonymous
// caller gets no actor names and cannot filter by actor.
//
//	GET /api/v1/repositories/{name}/activity?category=&package_name=&version=&actor=&action=&cursor=&limit=
func (h *RepositoryActivityHandler) handleList(w http.ResponseWriter, r *http.Request) {
	name := urlparams.Vars(r)["name"]

	repo, err := h.db.GetRepositoryByName(r.Context(), name)
	if err != nil {
		h.log.Errorw("repository activity: db error getting repo", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if repo == nil {
		writeError(w, http.StatusNotFound, "not_found", "repository not found")
		return
	}

	principal := currentPrincipal(r)
	signedIn := principal != nil && !principal.IsAnonymous

	isAdmin := false

	if signedIn {
		isAdmin, err = h.evaluator.EvaluateRepoAdmin(r.Context(), *principal, name)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "auth evaluation failed")
			return
		}
	}

	q := r.URL.Query()

	if !signedIn && q.Get("actor") != "" {
		writeError(w, http.StatusForbidden, "forbidden", "sign in to filter by actor")
		return
	}

	actions, ferr := activityActions(q["category"], isAdmin)
	if ferr != nil {
		code := "bad_request"
		if ferr.status == http.StatusForbidden {
			code = "forbidden"
		}

		writeError(w, ferr.status, code, ferr.message)

		return
	}

	limit := queryIntParam(r, "limit", activityDefaultPerPage, 1, activityMaxPerPage)

	params := models.AuditListParams{
		RepositoryID:   repo.ID,
		PackageName:    q.Get("package_name"),
		PackageVersion: q.Get("version"),
		Actor:          q.Get("actor"),
		Action:         q.Get("action"),
		Actions:        actions,
		Limit:          limit,
		NoTotal:        true,
	}

	if raw := q.Get("cursor"); raw != "" {
		cursor, err := decodeAuditCursor(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "invalid cursor")
			return
		}

		params.After = cursor
	}

	page, err := h.db.ListAuditLogs(r.Context(), params)
	if err != nil {
		h.log.Errorw("repository activity: list failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	resp := activityPage(r.Context(), h.db, h.log, page, limit)

	if !signedIn {
		for i := range resp.Data {
			resp.Data[i].Actor, resp.Data[i].ActorName = "", ""
		}
	}

	writeJSON(w, http.StatusOK, resp)
}
