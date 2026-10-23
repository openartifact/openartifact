// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/middleware"
)

// catalogBatch is how many catalog names one database round trip fetches. A
// page whose names are mostly unreadable by the caller takes several.
const catalogBatch = 500

// CatalogAuthorizer is the Authorizer of a registry that serves /v2/_catalog:
// it also loads a principal's permissions once to filter many (repository,
// path) pairs, which is what listing every image of every repository needs.
type CatalogAuthorizer interface {
	Authorizer
	PathReadFilter(ctx context.Context, principal auth.Principal) (func(repo *models.Repository, artifactPath string) (bool, error), error)
}

// pageParams reads the distribution spec's pagination query (n and last) of a
// listing. It answers 400 PAGINATION_NUMBER_INVALID itself and reports false on
// an invalid n; an absent n is the default page size and n is capped at
// maxTagPage.
func (h *HTTPHandler) pageParams(w http.ResponseWriter, r *http.Request) (int, string, bool) {
	n := defaultTagPage

	if nStr := r.URL.Query().Get("n"); nStr != "" {
		parsed, err := strconv.Atoi(nStr)
		if err != nil || parsed < 0 {
			h.errorResponse(w, http.StatusBadRequest, "PAGINATION_NUMBER_INVALID", "invalid n")
			return 0, "", false
		}

		n = min(parsed, maxTagPage)
	}

	return n, r.URL.Query().Get("last"), true
}

// handleCatalog serves GET /v2/_catalog: the images of the docker repositories
// the caller may read, as "<repository>/<image>" names (the bare repository
// name for its default image), in bytewise order and paginated with n/last. An
// image is listed when the caller may read its tag list, so a permission whose
// path_pattern limits it to some images lists exactly those; a group lists the
// images of the members the caller may read too.
func (h *HTTPHandler) handleCatalog(w http.ResponseWriter, r *http.Request) {
	n, last, ok := h.pageParams(w, r)
	if !ok {
		return
	}

	principal := middleware.PrincipalFromContext(r.Context())
	if principal == nil {
		h.errorResponse(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
		return
	}

	catalog, hasCatalog := h.db.(database.DockerCatalog)

	authz, hasAuthz := h.authz.(CatalogAuthorizer)
	if !hasCatalog || !hasAuthz {
		h.errorResponse(w, http.StatusNotImplemented, "UNSUPPORTED", "catalog is not available")
		return
	}

	canRead, err := authz.PathReadFilter(r.Context(), *principal)
	if err != nil {
		h.log.Errorw("catalog: permission lookup failed", "error", err)
		h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

		return
	}

	names := []string{}
	more := false

	if n > 0 {
		names, more, err = collectCatalog(r.Context(), catalog, canRead, last, n)
		if err != nil {
			h.log.Errorw("catalog: listing failed", "error", err)
			h.errorResponse(w, http.StatusInternalServerError, "UNKNOWN", "internal error")

			return
		}
	}

	if more {
		next := url.Values{"n": {strconv.Itoa(n)}, "last": {names[n-1]}}
		w.Header().Set("Link", fmt.Sprintf("</v2/_catalog?%s>; rel=\"next\"", next.Encode()))
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	if encErr := json.NewEncoder(w).Encode(map[string][]string{"repositories": names}); encErr != nil {
		_ = encErr // response header already written
	}
}

// collectCatalog returns the first n readable names after last, and whether a
// further readable one exists.
func collectCatalog(ctx context.Context, catalog database.DockerCatalog, canRead func(*models.Repository, string) (bool, error), last string, n int) ([]string, bool, error) {
	names := make([]string, 0, n)
	cursor := last

	for {
		batch, err := catalog.ListDockerCatalog(ctx, cursor, catalogBatch)
		if err != nil {
			return nil, false, fmt.Errorf("list catalog: %w", err)
		}

		for _, entry := range batch {
			cursor = entry.Name

			readable, err := catalogReadable(entry, canRead)
			if err != nil {
				return nil, false, err
			}

			if !readable {
				continue
			}

			if len(names) == n {
				return names, true, nil
			}

			names = append(names, entry.Name)
		}

		if len(batch) < catalogBatch {
			return names, false, nil
		}
	}
}

// catalogReadable reports whether the caller may read the image: the tag list
// of the listed repository and, for a group, of at least one member holding it
// (the registry serves a group's image from its members under their own
// permissions).
func catalogReadable(entry *models.DockerCatalogEntry, canRead func(*models.Repository, string) (bool, error)) (bool, error) {
	path := "tags/list"
	if entry.Image != "" {
		path = entry.Image + "/" + path
	}

	ok, err := canRead(entry.Repository, path)
	if err != nil || !ok {
		return false, err //nolint:wrapcheck // the evaluator's error is logged by the caller
	}

	if len(entry.Sources) == 1 && entry.Sources[0].ID == entry.Repository.ID {
		return true, nil
	}

	for _, src := range entry.Sources {
		ok, err := canRead(src, path)
		if err != nil {
			return false, err //nolint:wrapcheck // the evaluator's error is logged by the caller
		}

		if ok {
			return true, nil
		}
	}

	return false, nil
}
