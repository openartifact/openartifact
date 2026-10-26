// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"cmp"
	"context"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/openartifact/openartifact/internal/api/responses"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/repotype"
)

const (
	repoSortName          = "name"
	repoSortUpdated       = "updated"
	repoSortLastPublished = "last_published"
	repoSortSize          = "size"

	// defaultQuotaThreshold is the used fraction of the quota from which a
	// repository is "near quota" when quota_threshold is not given.
	defaultQuotaThreshold = 0.8

	// maxRepoQueryLen bounds the q name filter of the repository list.
	maxRepoQueryLen = 200
)

// repoListParams are the query parameters of GET /api/v1/repositories.
type repoListParams struct {
	format string
	// repoType is hosted, proxy or group; "" for all.
	repoType string
	// project is a project name; "" for all.
	project string
	// name is the lowercase substring the repository name must contain.
	name      string
	flagged   bool
	nearQuota bool
	unscanned bool
	threshold float64
	sort      string
	page      int
	limit     int
}

// needsStats reports whether the filters or the order need the stats of every
// readable repository, not only of the requested page.
func (p repoListParams) needsStats() bool {
	return p.flagged || p.nearQuota || p.unscanned || p.sort == repoSortSize || p.sort == repoSortLastPublished
}

// parseRepoListParams reads and validates the query of the repository list. The
// second result is the error message of a bad parameter, "" when all are fine.
func parseRepoListParams(r *http.Request) (repoListParams, string) {
	q := r.URL.Query()

	params := repoListParams{
		format:    q.Get("format"),
		repoType:  q.Get("type"),
		project:   q.Get("project"),
		name:      strings.ToLower(strings.TrimSpace(q.Get("q"))),
		threshold: defaultQuotaThreshold,
		sort:      q.Get("sort"),
		page:      min(parseIntMin(q.Get("page"), 1, 1), maxListPage),
		limit:     parseIntClamp(q.Get("limit"), 25, 1, 100),
	}

	if len(params.name) > maxRepoQueryLen {
		return params, "q is too long"
	}

	switch params.repoType {
	case "", repotype.Hosted, repotype.Proxy, repotype.Group:
	default:
		return params, "type must be hosted, proxy or group"
	}

	switch params.sort {
	case "", repoSortName, repoSortUpdated, repoSortLastPublished, repoSortSize:
	default:
		return params, "sort must be name, updated, last_published or size"
	}

	for _, flag := range []struct {
		key string
		dst *bool
	}{{"flagged", &params.flagged}, {"near_quota", &params.nearQuota}, {"unscanned", &params.unscanned}} {
		v, ok := parseBoolParam(q.Get(flag.key))
		if !ok {
			return params, flag.key + " must be true or false"
		}

		*flag.dst = v
	}

	if raw := q.Get("quota_threshold"); raw != "" {
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil || v <= 0 || v > 1 {
			return params, "quota_threshold must be a number above 0 and at most 1"
		}

		params.threshold = v
	}

	return params, ""
}

// parseBoolParam reads an optional boolean query parameter: "" and "false" are
// false, "true" is true; ok is false for any other value.
func parseBoolParam(s string) (bool, bool) {
	switch s {
	case "", "false":
		return false, true
	case "true":
		return true, true
	}

	return false, false
}

func (h *RepositoriesHandler) handleList(w http.ResponseWriter, r *http.Request) {
	params, msg := parseRepoListParams(r)
	if msg != "" {
		writeError(w, http.StatusBadRequest, "bad_request", msg)
		return
	}

	ctx := r.Context()

	repos, err := h.readableRepositories(ctx, currentPrincipal(r), params.format)
	if err != nil {
		h.log.Errorw("list repositories: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	repos, err = h.filterRepositoryScope(ctx, repos, params)
	if err != nil {
		h.log.Errorw("list repositories: project lookup failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	repos, err = h.filterAndSortRepositories(ctx, repos, params)
	if err != nil {
		h.log.Errorw("list repositories: stats error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	total := len(repos)
	start := min((params.page-1)*params.limit, total)
	pageRepos := repos[start:min(start+params.limit, total)]

	stats, counts, err := h.repositoryPageStats(ctx, pageRepos)
	if err != nil {
		h.log.Errorw("list repositories: stats error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	items := make([]responses.RepositoryResponse, 0, len(pageRepos))
	projectNames := map[string]string{}

	for _, repo := range pageRepos {
		resp := h.repositoryResponse(ctx, repo, projectNames)
		resp.Stats = repositoryStatsResponse(stats[repo.ID], counts[repo.ID])
		items = append(items, resp)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"total":        total,
		"page":         params.page,
		"limit":        params.limit,
		"repositories": items,
	})
}

// readableRepositories lists the repositories (of one format, or all when
// format is "") the principal may read, newest first. The principal's
// permission targets are loaded once and matched in memory: one query for the
// targets plus one per batch of repositories. An anonymous or missing principal
// is not filtered: the route table only lets authenticated callers in.
func (h *RepositoriesHandler) readableRepositories(ctx context.Context, p *auth.Principal, format string) ([]*models.Repository, error) {
	canRead := func(*models.Repository) (bool, error) { return true, nil }

	if p != nil && !p.IsAnonymous {
		filter, err := h.evaluator.RepoReadFilter(ctx, *p)
		if err != nil {
			return nil, err
		}

		canRead = filter
	}

	var repos []*models.Repository

	for dbPage := 1; ; dbPage++ {
		batch, err := h.db.ListRepositories(ctx, dbPage, repoListBatch, format)
		if err != nil {
			return nil, err
		}

		for _, repo := range batch {
			ok, err := canRead(repo)
			if err == nil && ok {
				repos = append(repos, repo)
			}
		}

		if len(batch) < repoListBatch {
			return repos, nil
		}
	}
}

// filterRepositoryScope applies the name, type and project filters. An unknown
// project matches nothing.
func (h *RepositoriesHandler) filterRepositoryScope(ctx context.Context, repos []*models.Repository, params repoListParams) ([]*models.Repository, error) {
	projectID := ""

	if params.project != "" {
		project, err := h.db.GetProjectByName(ctx, params.project)
		if err != nil {
			return nil, err
		}

		if project == nil {
			return nil, nil
		}

		projectID = project.ID
	}

	return slices.DeleteFunc(repos, func(repo *models.Repository) bool {
		switch {
		case params.name != "" && !strings.Contains(strings.ToLower(repo.Name), params.name):
			return true
		case params.repoType != "" && repo.RepoType != params.repoType:
			return true
		case projectID != "" && (repo.ProjectID == nil || *repo.ProjectID != projectID):
			return true
		}

		return false
	}), nil
}

// filterAndSortRepositories applies the stats filters and the sort order of
// params. The stats of every repository are only loaded when they are needed.
func (h *RepositoriesHandler) filterAndSortRepositories(ctx context.Context, repos []*models.Repository, params repoListParams) ([]*models.Repository, error) {
	var stats map[string]models.RepoStats

	if params.needsStats() && len(repos) > 0 {
		var err error

		stats, err = h.db.RepoCatalogStats(ctx, repoIDs(repos), params.sort == repoSortLastPublished)
		if err != nil {
			return nil, err
		}

		repos = slices.DeleteFunc(repos, func(repo *models.Repository) bool {
			return !matchesStatFilters(stats[repo.ID], params)
		})
	}

	sortRepositories(repos, stats, params.sort)

	return repos, nil
}

// matchesStatFilters reports whether a repository's stats pass every stats
// filter of params; the filters are ANDed.
func matchesStatFilters(s models.RepoStats, params repoListParams) bool {
	switch {
	case params.flagged && !s.Security.Flagged():
		return false
	case params.nearQuota && !s.NearQuota(params.threshold):
		return false
	case params.unscanned && s.Security.ScannedAt != nil:
		return false
	}

	return true
}

// sortRepositories orders repos by key; "" keeps the order of the list (newest
// first). Every order is stable and falls back to the name.
func sortRepositories(repos []*models.Repository, stats map[string]models.RepoStats, key string) {
	var cmpRepos func(a, b *models.Repository) int

	switch key {
	case repoSortName:
		cmpRepos = func(a, b *models.Repository) int { return strings.Compare(a.Name, b.Name) }
	case repoSortUpdated:
		cmpRepos = func(a, b *models.Repository) int { return b.UpdatedAt.Compare(a.UpdatedAt) }
	case repoSortSize:
		cmpRepos = func(a, b *models.Repository) int { return cmp.Compare(stats[b.ID].SizeBytes, stats[a.ID].SizeBytes) }
	case repoSortLastPublished:
		cmpRepos = func(a, b *models.Repository) int {
			return lastPublished(stats[b.ID]).Compare(lastPublished(stats[a.ID]))
		}
	default:
		return
	}

	slices.SortStableFunc(repos, func(a, b *models.Repository) int {
		return cmp.Or(cmpRepos(a, b), strings.Compare(a.Name, b.Name))
	})
}

// lastPublished is the time of the newest package of a repository, the zero
// time for an empty one (so it sorts last, newest first).
func lastPublished(s models.RepoStats) time.Time {
	if s.LastPublishedAt == nil {
		return time.Time{}
	}

	return *s.LastPublishedAt
}

func repoIDs(repos []*models.Repository) []string {
	ids := make([]string, len(repos))
	for i, repo := range repos {
		ids[i] = repo.ID
	}

	return ids
}

// repositoryPageStats loads the stats and package counts of one page of
// repositories, each in a single grouped query.
func (h *RepositoriesHandler) repositoryPageStats(ctx context.Context, repos []*models.Repository) (map[string]models.RepoStats, map[string]int64, error) {
	if len(repos) == 0 {
		return nil, nil, nil
	}

	ids := repoIDs(repos)

	stats, err := h.db.RepoCatalogStats(ctx, ids, true)
	if err != nil {
		return nil, nil, err
	}

	counts, err := h.db.CountRepoPackageNames(ctx, ids)
	if err != nil {
		return nil, nil, err
	}

	return stats, counts, nil
}

// repositoryStatsResponse renders a repository's stats.
func repositoryStatsResponse(s models.RepoStats, packageCount int64) *responses.RepositoryStats {
	quota := responses.RepositoryQuota{UsedBytes: s.Quota.UsedBytes}
	if s.Quota.LimitBytes > 0 {
		limit := s.Quota.LimitBytes
		quota.LimitBytes = &limit
	}

	return &responses.RepositoryStats{
		PackageCount:    packageCount,
		SizeBytes:       s.SizeBytes,
		LastPublishedAt: formatTimePtr(s.LastPublishedAt),
		Security: responses.RepositorySecurity{
			Critical:          s.Security.Critical,
			High:              s.Security.High,
			Medium:            s.Security.Medium,
			Low:               s.Security.Low,
			Secrets:           s.Security.Secrets,
			LicenseViolations: s.Security.LicenseViolations,
			ScannedAt:         formatTimePtr(s.Security.ScannedAt),
		},
		Quota: quota,
	}
}
