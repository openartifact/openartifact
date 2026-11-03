// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/api/responses"
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
// what the cleanup policies removed, the curation rules and the federation traffic - name
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
	"curation": {
		actions:   []string{audit.ActionCurationRuleCreated, audit.ActionCurationRuleUpdated, audit.ActionCurationRuleDeleted},
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

// ActivityCategoryNames lists the values of the `category` filter, sorted: the
// ones open to anyone who may read the repository, then the ones reserved to
// its admins.
func ActivityCategoryNames() ([]string, []string) {
	var open, adminOnly []string

	for name, c := range activityCategories {
		if c.adminOnly {
			adminOnly = append(adminOnly, name)
		} else {
			open = append(open, name)
		}
	}

	slices.Sort(open)
	slices.Sort(adminOnly)

	return open, adminOnly
}

// Refusals of a repository activity query.
var (
	// ErrActivityCategory: the category filter names no category.
	ErrActivityCategory = errors.New("unknown category")
	// ErrActivityAdminCategory: the category filter names a category reserved to
	// the repository's admins and the caller is not one.
	ErrActivityAdminCategory = errors.New("admin access required for this category")
	// ErrActivityActor: an anonymous caller asked to filter by actor.
	ErrActivityActor = errors.New("sign in to filter by actor")
	// ErrActivityCursor: the cursor is not one this feed issued.
	ErrActivityCursor = errors.New("invalid cursor")
)

// activityActions turns the `category` parameter (comma separated, repeatable)
// into the audit actions to keep. With no category an admin gets every event of
// the repository (nil), anyone else every category open to readers. An unknown
// category is ErrActivityCategory, an admin-only one asked by a non-admin
// ErrActivityAdminCategory.
func activityActions(values []string, isAdmin bool) ([]string, error) {
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
			return nil, ErrActivityCategory
		}

		if c.adminOnly && !isAdmin {
			return nil, ErrActivityAdminCategory
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

// ActivityCaller is who asks for the activity of a repository.
type ActivityCaller struct {
	// SignedIn is false for an anonymous caller (a public repository): no actor
	// names and no actor filter.
	SignedIn bool
	// RepoAdmin: a global admin, a maintainer of the repository's project or a
	// delegated repository admin (auth.Evaluator.EvaluateRepoAdmin).
	RepoAdmin bool
}

// ActivityQuery is the filter of a repository activity listing.
type ActivityQuery struct {
	// Categories are category names, each possibly comma separated.
	Categories  []string
	PackageName string
	Version     string
	Actor       string
	Action      string
	// Cursor is the next_cursor of the previous page.
	Cursor string
	// Limit is the page size, already bounded by the caller.
	Limit int
}

// ListRepositoryActivity is the page of audit events recorded against repo that
// caller may see, newest first, with actor and subject names resolved. It is the
// answer of GET /api/v1/repositories/{name}/activity and of the MCP
// get_repository_activity tool; the caller has already checked read access to
// the repository. The category restriction is applied here, whatever the client
// asks for. Refused filters are the ErrActivity* errors.
func ListRepositoryActivity(
	ctx context.Context, db RepositoryActivityStore, log *logger.Logger,
	repo *models.Repository, caller ActivityCaller, q ActivityQuery,
) (responses.ActivityPage, error) {
	if !caller.SignedIn && q.Actor != "" {
		return responses.ActivityPage{}, ErrActivityActor
	}

	actions, err := activityActions(q.Categories, caller.RepoAdmin)
	if err != nil {
		return responses.ActivityPage{}, err
	}

	params := models.AuditListParams{
		RepositoryID:   repo.ID,
		PackageName:    q.PackageName,
		PackageVersion: q.Version,
		Actor:          q.Actor,
		Action:         q.Action,
		Actions:        actions,
		Limit:          q.Limit,
		NoTotal:        true,
	}

	if q.Cursor != "" {
		cursor, err := decodeAuditCursor(q.Cursor)
		if err != nil {
			return responses.ActivityPage{}, ErrActivityCursor
		}

		params.After = cursor
	}

	page, err := db.ListAuditLogs(ctx, params)
	if err != nil {
		return responses.ActivityPage{}, fmt.Errorf("list repository activity: %w", err)
	}

	resp := activityPage(ctx, db, log, page, q.Limit)

	if !caller.SignedIn {
		for i := range resp.Data {
			resp.Data[i].Actor, resp.Data[i].ActorName = "", ""
		}
	}

	return resp, nil
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
	caller := ActivityCaller{SignedIn: principal != nil && !principal.IsAnonymous}

	if caller.SignedIn {
		caller.RepoAdmin, err = h.evaluator.EvaluateRepoAdmin(r.Context(), *principal, name)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "auth evaluation failed")
			return
		}
	}

	q := r.URL.Query()

	resp, err := ListRepositoryActivity(r.Context(), h.db, h.log, repo, caller, ActivityQuery{
		Categories:  q["category"],
		PackageName: q.Get("package_name"),
		Version:     q.Get("version"),
		Actor:       q.Get("actor"),
		Action:      q.Get("action"),
		Cursor:      q.Get("cursor"),
		Limit:       queryIntParam(r, "limit", activityDefaultPerPage, 1, activityMaxPerPage),
	})

	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, resp)
	case errors.Is(err, ErrActivityCategory), errors.Is(err, ErrActivityCursor):
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
	case errors.Is(err, ErrActivityAdminCategory), errors.Is(err, ErrActivityActor):
		writeError(w, http.StatusForbidden, "forbidden", err.Error())
	default:
		h.log.Errorw("repository activity: list failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")
	}
}
