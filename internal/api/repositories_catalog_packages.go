// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/openartifact/openartifact/internal/api/responses"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/urlparams"
)

const (
	// maxPackageQueryLen bounds the q search term of the package listing.
	maxPackageQueryLen = 200

	packageSortCreated = "created"
	packageSortName    = "name"

	// packageGroupByPackage is the group value that rolls the listing up to one
	// row per package.
	packageGroupByPackage = "package"
)

// packageListParams are the query parameters of
// GET /api/v1/repositories/{name}/packages.
type packageListParams struct {
	format  string
	name    string
	search  string
	sortKey string
	cursor  *database.ComponentCursor
	group   bool
	flagged bool
	page    int
	limit   int
}

// parsePackageListParams reads and validates the query of the package listing.
// The second result is the error message of a bad parameter, "" when all are fine.
func parsePackageListParams(r *http.Request) (packageListParams, string) {
	q := r.URL.Query()

	params := packageListParams{
		format:  q.Get("format"),
		name:    q.Get("package_name"),
		search:  strings.TrimSpace(q.Get("q")),
		sortKey: q.Get("sort"),
		page:    min(parseIntMin(q.Get("page"), 1, 1), maxListPage),
		limit:   parseIntClamp(q.Get("limit"), 25, 1, 100),
	}

	if len(params.search) > maxPackageQueryLen {
		return params, "q is too long"
	}

	if params.sortKey != "" && params.sortKey != packageSortCreated && params.sortKey != packageSortName {
		return params, "sort must be created or name"
	}

	switch q.Get("group") {
	case "":
	case packageGroupByPackage:
		params.group = true
	default:
		return params, "group must be package"
	}

	flagged, ok := parseBoolParam(q.Get("flagged"))
	if !ok {
		return params, "flagged must be true or false"
	}

	if flagged && !params.group {
		return params, "flagged requires group=package"
	}

	params.flagged = flagged

	return parseCursorParam(q.Get("cursor"), params)
}

// parseCursorParam reads the keyset cursor of the package listing. It only
// applies to the newest-first file listing, not to group=package or sort=name.
func parseCursorParam(raw string, params packageListParams) (packageListParams, string) {
	if raw == "" {
		return params, ""
	}

	cursor, err := decodeComponentCursor(raw)
	if err != nil {
		return params, "invalid cursor"
	}

	if params.group || params.sortKey == packageSortName {
		return params, "cursor requires the newest-first file listing (no group=package, no sort=name)"
	}

	params.cursor = cursor

	return params, ""
}

// encodeComponentCursor is the opaque cursor of the listing position after c.
func encodeComponentCursor(c *models.Component) string {
	return encodeAuditCursor(&models.AuditCursor{CreatedAt: c.CreatedAt, ID: c.ID})
}

// decodeComponentCursor parses a cursor produced by encodeComponentCursor.
func decodeComponentCursor(s string) (*database.ComponentCursor, error) {
	c, err := decodeAuditCursor(s)
	if err != nil {
		return nil, err
	}

	return &database.ComponentCursor{CreatedAt: c.CreatedAt, ID: c.ID}, nil
}

func (h *RepositoriesHandler) handlePackages(w http.ResponseWriter, r *http.Request) {
	params, msg := parsePackageListParams(r)
	if msg != "" {
		writeError(w, http.StatusBadRequest, "bad_request", msg)
		return
	}

	ctx := r.Context()

	repo := h.readableRepository(w, r, urlparams.Vars(r)["name"])
	if repo == nil {
		return
	}

	var (
		body any
		err  error
	)

	if params.group {
		body, err = h.packageGroups(ctx, repo, params)
	} else {
		body, err = h.packageVersions(ctx, repo, params)
	}

	if err != nil {
		h.log.Errorw("packages: db error listing packages", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	writeJSON(w, http.StatusOK, body)
}

// readableRepository resolves the repository named in the URL and checks the
// caller may read it. On failure it writes the error response and returns nil.
func (h *RepositoriesHandler) readableRepository(w http.ResponseWriter, r *http.Request, name string) *models.Repository {
	ctx := r.Context()

	repo, err := h.db.GetRepositoryByName(ctx, name)
	if err != nil {
		h.log.Errorw("packages: db error getting repo", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return nil
	}

	if repo == nil {
		writeError(w, http.StatusNotFound, "not_found", "repository not found")
		return nil
	}

	if p := currentPrincipal(r); p != nil && !p.IsAnonymous {
		ok, err := h.evaluator.Evaluate(ctx, *p, repo.Name, "", "read")
		if err != nil || !ok {
			writeError(w, http.StatusForbidden, "forbidden", "access denied")
			return nil
		}
	}

	return repo
}

// packageVersions is the listing of one row per file with the scan state of each.
func (h *RepositoriesHandler) packageVersions(
	ctx context.Context,
	repo *models.Repository,
	params packageListParams,
) (*responses.PackageListResponse, error) {
	opts := database.ComponentListOptions{
		Format:     params.format,
		Name:       params.name,
		Query:      params.search,
		SortByName: params.sortKey == packageSortName,
		Page:       params.page,
		Limit:      params.limit,
	}

	if params.cursor != nil {
		// One row more than the page says whether the listing goes on, so no
		// count is needed.
		opts.After, opts.NoTotal, opts.Page, opts.Limit = params.cursor, true, 1, params.limit+1
	}

	components, total, err := h.listComponents(ctx, repo.ID, opts)
	if err != nil {
		return nil, err
	}

	hasMore := false
	if params.cursor != nil && len(components) > params.limit {
		components, hasMore = components[:params.limit], true
	} else if params.cursor == nil {
		hasMore = params.page*params.limit < total
	}

	ids := make([]string, len(components))
	for i, c := range components {
		ids[i] = c.ID
	}

	security, err := h.db.ArtifactSecurity(ctx, repo.ID, ids)
	if err != nil {
		return nil, err
	}

	packages := make([]responses.PackageResponse, 0, len(components))

	for _, c := range components {
		sec := security[c.ID]

		packages = append(packages, responses.PackageResponse{
			ID:        c.ID,
			Name:      c.Name,
			Version:   c.Version,
			Format:    c.Format,
			Arch:      c.Arch,
			Size:      c.PackageSize,
			SHA256:    c.SHA256,
			Filename:  c.Filename,
			CreatedAt: formatTime(c.CreatedAt),
			Security:  packageSecurityResponse(sec.SecurityCounts),
			Flagged:   artifactFlagged(sec),
			ScannedAt: formatTimePtr(sec.ScannedAt),
		})
	}

	resp := &responses.PackageListResponse{Page: params.page, Limit: params.limit, HasMore: hasMore, Packages: packages}
	if params.cursor == nil {
		resp.Total = &total
	}

	if hasMore && len(components) > 0 && params.sortKey != packageSortName {
		resp.NextCursor = encodeComponentCursor(components[len(components)-1])
	}

	return resp, nil
}

// packageGroups is the listing of one row per package, with the scan state of
// the package's latest version.
func (h *RepositoriesHandler) packageGroups(
	ctx context.Context,
	repo *models.Repository,
	params packageListParams,
) (*responses.PackageGroupListResponse, error) {
	rollup, total, err := h.db.ListRepoPackageRollup(ctx, models.PackageRollupOptions{
		RepoID:     repo.ID,
		RepoFormat: repo.Format,
		Format:     params.format,
		Name:       params.name,
		Query:      params.search,
		Flagged:    params.flagged,
		SortByName: params.sortKey == packageSortName,
		Page:       params.page,
		Limit:      params.limit,
	})
	if err != nil {
		return nil, err
	}

	var latestIDs []string
	for _, g := range rollup {
		latestIDs = append(latestIDs, g.LatestIDs...)
	}

	security, err := h.db.ArtifactSecurity(ctx, repo.ID, latestIDs)
	if err != nil {
		return nil, err
	}

	packages := make([]responses.PackageGroupResponse, 0, len(rollup))

	for _, g := range rollup {
		sec := sumArtifactSecurity(g.LatestIDs, security)

		packages = append(packages, responses.PackageGroupResponse{
			Name:          g.Name,
			Format:        g.Format,
			LatestVersion: g.LatestVersion,
			VersionCount:  g.VersionCount,
			SizeBytes:     g.SizeBytes,
			UpdatedAt:     formatTime(g.UpdatedAt),
			Security:      packageSecurityResponse(sec.SecurityCounts),
			Flagged:       artifactFlagged(sec),
			ScannedAt:     formatTimePtr(sec.ScannedAt),
		})
	}

	return &responses.PackageGroupListResponse{Total: total, Page: params.page, Limit: params.limit, Packages: packages}, nil
}

// sumArtifactSecurity combines the scan state of the files ids: counts and
// secrets are summed, a quarantined file quarantines the whole, and the scan
// time is the latest.
func sumArtifactSecurity(ids []string, security map[string]models.ArtifactSecurity) models.ArtifactSecurity {
	var sum models.ArtifactSecurity

	for _, id := range ids {
		s, ok := security[id]
		if !ok {
			continue
		}

		sum.Critical += s.Critical
		sum.High += s.High
		sum.Medium += s.Medium
		sum.Low += s.Low
		sum.Secrets += s.Secrets
		sum.Quarantined = sum.Quarantined || s.Quarantined

		if s.ScannedAt != nil && (sum.ScannedAt == nil || s.ScannedAt.After(*sum.ScannedAt)) {
			t := *s.ScannedAt
			sum.ScannedAt = &t
		}
	}

	return sum
}

func packageSecurityResponse(c models.SecurityCounts) responses.PackageSecurity {
	return responses.PackageSecurity{Critical: c.Critical, High: c.High, Medium: c.Medium, Low: c.Low}
}

// artifactFlagged is the flagged rule of the catalog for one scan state.
func artifactFlagged(s models.ArtifactSecurity) bool {
	return s.Critical+s.High > 0 || s.Secrets > 0 || s.Quarantined
}

const (
	defaultRecentLimit = 10
	maxRecentLimit     = 50
)

// handleRecentPackages lists the newest package versions of the repositories the
// caller can read (mine=true: those they can also write to).
//
//	GET /api/v1/packages/recent?mine=true|false&limit=
func (h *RepositoriesHandler) handleRecentPackages(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	mine, ok := parseBoolParam(q.Get("mine"))
	if !ok {
		writeError(w, http.StatusBadRequest, "bad_request", "mine must be true or false")
		return
	}

	limit := parseIntClamp(q.Get("limit"), defaultRecentLimit, 1, maxRecentLimit)
	ctx := r.Context()
	p := currentPrincipal(r)

	repos, err := h.readableRepositories(ctx, p, "")
	if err == nil && mine {
		repos, err = h.writableRepositories(ctx, p, repos)
	}

	var recent []models.RecentPackage
	if err == nil {
		recent, err = h.db.ListRecentPackages(ctx, repoIDs(repos), limit)
	}

	if err != nil {
		h.log.Errorw("recent packages: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	out := make([]responses.RecentPackageResponse, 0, len(recent))
	for _, rp := range recent {
		out = append(out, responses.RecentPackageResponse{
			Repository: rp.Repository, Format: rp.Format, Name: rp.Name, Version: rp.Version,
			CreatedAt: formatTime(rp.CreatedAt),
		})
	}

	writeJSON(w, http.StatusOK, responses.RecentPackagesResponse{Packages: out})
}

// writableRepositories keeps the repositories the principal may write to. The
// server records no publisher, so this is what "my packages" means.
func (h *RepositoriesHandler) writableRepositories(
	ctx context.Context,
	p *auth.Principal,
	repos []*models.Repository,
) ([]*models.Repository, error) {
	if p == nil || p.IsAnonymous {
		return nil, nil
	}

	var out []*models.Repository

	for _, repo := range repos {
		ok, err := h.evaluator.Evaluate(ctx, *p, repo.Name, "", "write")
		if err != nil {
			return nil, err
		}

		if ok {
			out = append(out, repo)
		}
	}

	return out, nil
}
