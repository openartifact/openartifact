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
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/pkg/logger"
)

type Searcher interface {
	// SearchPage returns one page of results and the number of matches (capped
	// at MaxSearchResults+1) from a single scan.
	SearchPage(ctx context.Context, filter SearchFilter, limit, offset int, readableRepos map[uuid.UUID]struct{}) ([]SearchResult, int64, error)
	SearchRepositories(ctx context.Context, filter SearchFilter, limit, offset int, readableRepos map[uuid.UUID]struct{}) ([]RepositoryResult, error)
}

type Handler struct {
	searcher  Searcher
	db        *sql.DB
	evaluator *auth.Evaluator
	log       *logger.Logger
}

func NewHandler(db *sql.DB, evaluator *auth.Evaluator, log *logger.Logger) *Handler {
	return &Handler{
		searcher:  NewExecutor(db),
		db:        db,
		evaluator: evaluator,
		log:       log,
	}
}

const (
	maxFilterValues = 50
	searchTimeout   = 15 * time.Second
)

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), searchTimeout)
	defer cancel()

	q := r.URL.Query().Get("q")
	if q == "" {
		writeJSONError(w, http.StatusBadRequest, "missing_q", "query parameter 'q' is required")
		return
	}

	limit := 25

	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			writeJSONError(w, http.StatusBadRequest, "invalid_limit", "limit must be a positive integer")
			return
		}

		if n > 100 {
			writeJSONError(w, http.StatusBadRequest, "invalid_limit", "limit must not exceed 100")
			return
		}

		limit = n
	}

	offset := 0

	if v := r.URL.Query().Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			writeJSONError(w, http.StatusBadRequest, "invalid_offset", "offset must be a non-negative integer")
			return
		}

		offset = n
	}

	if offset > MaxSearchResults {
		writeJSONError(w, http.StatusBadRequest, "invalid_offset", "offset too large")
		return
	}

	filter := ParseQuery(q)
	filter.Formats = append(filter.Formats, r.URL.Query()["format"]...)
	filter.Repos = append(filter.Repos, r.URL.Query()["repo"]...)

	if len(filter.Formats) > maxFilterValues || len(filter.Repos) > maxFilterValues {
		writeJSONError(w, http.StatusBadRequest, "invalid_filter", "too many format or repo filters")
		return
	}

	principal := middleware.PrincipalFromContext(ctx)

	readableRepos, err := h.resolveReadableRepos(ctx, principal)
	if err != nil {
		h.log.Errorw("resolve readable repos failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "permission resolution failed")

		return
	}

	results, count, err := h.searcher.SearchPage(ctx, filter, limit, offset, readableRepos)
	if err != nil {
		h.log.Errorw("search failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "search failed")

		return
	}

	if count > MaxSearchResults {
		writeJSONError(w, http.StatusBadRequest, "query_too_broad", "query too broad; add more filters")
		return
	}

	repositories, err := h.searcher.SearchRepositories(ctx, filter, limit, offset, readableRepos)
	if err != nil {
		h.log.Errorw("repository search failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "search failed")

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
		"results":      results,
		"packages":     results,
		"repositories": repositories,
	}); encErr != nil {
		_ = encErr // response header already written
	}
}

func (h *Handler) resolveReadableRepos(ctx context.Context, principal *auth.Principal) (map[uuid.UUID]struct{}, error) {
	readable := make(map[uuid.UUID]struct{})

	if principal == nil {
		return readable, nil
	}

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

func writeJSONError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	if encErr := json.NewEncoder(w).Encode(map[string]string{"error": msg, "code": code}); encErr != nil {
		_ = encErr // response header already written
	}
}
