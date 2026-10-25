// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package search

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/httpx"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/pkg/logger"
)

type Searcher interface {
	// SearchPage returns one page of results and the number of matches (capped
	// at MaxSearchResults+1) from a single scan.
	SearchPage(ctx context.Context, filter SearchFilter, opts PageOptions, limit, offset int, readableRepos map[uuid.UUID]struct{}) ([]SearchResult, int64, error)
	SearchRepositories(ctx context.Context, filter SearchFilter, limit, offset int, readableRepos map[uuid.UUID]struct{}) ([]RepositoryResult, error)
}

type Handler struct {
	searcher  Searcher
	db        *sql.DB
	evaluator *auth.Evaluator
	log       *logger.Logger
	readable  readableCache
}

// Option customizes a Handler.
type Option func(*Handler)

// WithAuthCache caches each principal's readable-repository set for the auth
// cache's TTL, dropping it whenever the auth cache is invalidated (see
// readableCache). Without it every request resolves the set afresh.
func WithAuthCache(src AuthCache) Option {
	return func(h *Handler) { h.readable.src = src }
}

func NewHandler(db *sql.DB, evaluator *auth.Evaluator, log *logger.Logger, opts ...Option) *Handler {
	h := &Handler{
		searcher:  NewExecutor(db),
		db:        db,
		evaluator: evaluator,
		log:       log,
	}

	for _, opt := range opts {
		opt(h)
	}

	return h
}

const (
	maxFilterValues = 50
	searchTimeout   = 15 * time.Second

	// groupPackage is the group query value that returns one row per package.
	groupPackage = "package"
)

// parsePageOptions reads the group parameter; false means an unknown value.
func parsePageOptions(r *http.Request) (PageOptions, bool) {
	switch r.URL.Query().Get("group") {
	case "":
		return PageOptions{}, true
	case groupPackage:
		return PageOptions{GroupByPackage: true}, true
	default:
		return PageOptions{}, false
	}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), searchTimeout)
	defer cancel()

	q := r.URL.Query().Get("q")
	if q == "" {
		httpx.WriteError(w, http.StatusBadRequest, "missing_q", "query parameter 'q' is required")
		return
	}

	limit := 25

	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			httpx.WriteError(w, http.StatusBadRequest, "invalid_limit", "limit must be a positive integer")
			return
		}

		if n > 100 {
			httpx.WriteError(w, http.StatusBadRequest, "invalid_limit", "limit must not exceed 100")
			return
		}

		limit = n
	}

	offset := 0

	if v := r.URL.Query().Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			httpx.WriteError(w, http.StatusBadRequest, "invalid_offset", "offset must be a non-negative integer")
			return
		}

		offset = n
	}

	if offset > MaxSearchResults {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_offset", "offset too large")
		return
	}

	filter := ParseQuery(q)
	filter.Formats = append(filter.Formats, r.URL.Query()["format"]...)
	filter.Repos = append(filter.Repos, r.URL.Query()["repo"]...)

	if len(filter.Formats) > maxFilterValues || len(filter.Repos) > maxFilterValues {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_filter", "too many format or repo filters")
		return
	}

	opts, ok := parsePageOptions(r)
	if !ok {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_group", "group must be 'package'")
		return
	}

	principal := middleware.PrincipalFromContext(ctx)

	readableRepos, err := h.resolveReadableRepos(ctx, principal)
	if err != nil {
		h.log.Errorw("resolve readable repos failed", "error", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal_error", "permission resolution failed")

		return
	}

	results, count, err := h.searcher.SearchPage(ctx, filter, opts, limit, offset, readableRepos)
	if err != nil {
		h.log.Errorw("search failed", "error", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal_error", "search failed")

		return
	}

	if count > MaxSearchResults {
		httpx.WriteError(w, http.StatusBadRequest, "query_too_broad", "query too broad; add more filters")
		return
	}

	repositories, err := h.searcher.SearchRepositories(ctx, filter, limit, offset, readableRepos)
	if err != nil {
		h.log.Errorw("repository search failed", "error", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal_error", "search failed")

		return
	}

	if results == nil {
		results = []SearchResult{}
	}

	if repositories == nil {
		repositories = []RepositoryResult{}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	if encErr := json.NewEncoder(w).Encode(map[string]any{
		"total":        count,
		"packages":     results,
		"repositories": repositories,
	}); encErr != nil {
		_ = encErr // response header already written
	}
}

// resolveReadableRepos returns the repositories principal can read. The result
// may be shared with other requests of the same principal: callers must not
// modify it.
func (h *Handler) resolveReadableRepos(ctx context.Context, principal *auth.Principal) (map[uuid.UUID]struct{}, error) {
	if principal == nil {
		return map[uuid.UUID]struct{}{}, nil
	}

	gen, cached := h.readable.begin()
	if !cached {
		return h.loadReadableRepos(ctx, principal)
	}

	key := principalCacheKey(*principal)

	if repos, ok := h.readable.get(key, gen); ok {
		return repos, nil
	}

	repos, err := h.loadReadableRepos(ctx, principal)
	if err != nil {
		return nil, err
	}

	h.readable.store(key, gen, repos)

	return repos, nil
}

func (h *Handler) loadReadableRepos(ctx context.Context, principal *auth.Principal) (map[uuid.UUID]struct{}, error) {
	readable := make(map[uuid.UUID]struct{})

	// One batched permission lookup instead of one Evaluate per repository.
	canRead, err := h.evaluator.RepoReadFilter(ctx, *principal)
	if err != nil {
		return nil, err
	}

	rows, err := h.db.QueryContext(ctx, `SELECT id, name, project_id FROM repositories WHERE deleted_at IS NULL`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var (
			rawID string
			repo  models.Repository
		)

		if err := rows.Scan(&rawID, &repo.Name, &repo.ProjectID); err != nil {
			return nil, err
		}

		id, err := uuid.Parse(rawID)
		if err != nil {
			continue
		}

		allowed, err := canRead(&repo)
		if err != nil {
			return nil, err
		}

		if allowed {
			readable[id] = struct{}{}
		}
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return readable, nil
}
