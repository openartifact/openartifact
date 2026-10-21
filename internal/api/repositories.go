// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/api/responses"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/crypto"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/netutil"
	"github.com/openartifact/openartifact/internal/quarantine"
	"github.com/openartifact/openartifact/internal/repotype"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/internal/webhook"
	"github.com/openartifact/openartifact/pkg/logger"
)

var repoNameRe = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,100}$`)

// validFormats mirrors the chk_repo_format CHECK constraint in the schema.
//
//nolint:gochecknoglobals // immutable lookup table
var validFormats = map[string]bool{
	"deb":      true,
	"rpm":      true,
	"maven":    true,
	"npm":      true,
	"docker":   true,
	"helm":     true,
	"pypi":     true,
	"nuget":    true,
	"go":       true,
	"cargo":    true,
	"rubygems": true,
	"raw":      true,
	"conda":    true,
	"conan":    true,
	"ivy":      true,
	"sbt":      true,
}

const (
	// maxListPage bounds the page number so (page-1)*limit cannot overflow.
	maxListPage = 1_000_000
	// repoListBatch is the batch size used while scanning repositories to
	// filter them by the caller's permissions.
	repoListBatch = 500
	// maxGroupMembers bounds the size of a group repository.
	maxGroupMembers = 200

	defaultCacheTTLSeconds         = 3600
	defaultNegativeCacheTTLSeconds = 60
	defaultMaxStaleSeconds         = 7 * 24 * 3600
	maxCacheTTLSeconds             = 30 * 24 * 3600
	maxNegativeCacheTTLSeconds     = 24 * 3600
	maxMaxStaleSeconds             = 365 * 24 * 3600
)

func validFormatList() string {
	names := make([]string, 0, len(validFormats))
	for name := range validFormats {
		names = append(names, name)
	}

	sort.Strings(names)

	return strings.Join(names, ", ")
}

type RepositoriesHandler struct {
	db        database.Database
	log       *logger.Logger
	evaluator *auth.Evaluator
	blobStore storage.BlobStore
	publisher webhook.Publisher

	// gc tracks background orphan-blob collections started by repository deletes.
	gc sync.WaitGroup
}

func NewRepositoriesHandler(db database.Database, log *logger.Logger, evaluator *auth.Evaluator) *RepositoriesHandler {
	return &RepositoriesHandler{db: db, log: log, evaluator: evaluator}
}

// WithBlobStore enables the package download endpoint.
func (h *RepositoriesHandler) WithBlobStore(store storage.BlobStore) *RepositoriesHandler {
	h.blobStore = store

	return h
}

// WithPublisher enables repository.created / repository.deleted webhook events.
func (h *RepositoriesHandler) WithPublisher(p webhook.Publisher) *RepositoriesHandler {
	h.publisher = p

	return h
}

// WaitBackground blocks until background orphan-blob collections finish.
func (h *RepositoriesHandler) WaitBackground() { h.gc.Wait() }

func (h *RepositoriesHandler) RegisterAuthed(r chi.Router) {
	r.Get("/api/v1/repositories", h.handleList)
	r.Post("/api/v1/repositories", h.handleCreate)
	r.Get("/api/v1/repository/{name}", h.handleGet)
	r.Patch("/api/v1/repository/{name}", h.handleUpdate)
	r.Delete("/api/v1/repository/{name}", h.handleDelete)
	r.Get("/api/v1/repositories/{name}/packages", h.handlePackages)
	r.Get("/api/v1/repositories/{name}/packages/{id}/download", h.handleDownloadPackage)
	r.Get("/api/v1/repositories/{name}/members", h.handleGetMembers)
	r.Put("/api/v1/repositories/{name}/members", h.handleUpdateMembers)
}

func (h *RepositoriesHandler) handleList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	formatFilter := q.Get("format")
	page := min(parseIntMin(q.Get("page"), 1, 1), maxListPage)
	limit := parseIntClamp(q.Get("limit"), 25, 1, 100)

	ctx := r.Context()
	p := currentPrincipal(r)

	var (
		repos []*models.Repository
		total int
	)

	if p != nil && !p.IsAnonymous {
		// Load the principal's permission targets once and match in memory:
		// one query for the targets plus one per batch of repositories.
		canRead, err := h.evaluator.RepoReadFilter(ctx, *p)
		if err != nil {
			h.log.Errorw("list repositories: permission lookup failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "database error")

			return
		}

		for dbPage := 1; ; dbPage++ {
			batch, err := h.db.ListRepositories(ctx, dbPage, repoListBatch, formatFilter)
			if err != nil {
				h.log.Errorw("list repositories: db error", "error", err)
				writeError(w, http.StatusInternalServerError, "internal_error", "database error")

				return
			}

			for _, repo := range batch {
				ok, err := canRead(repo)
				if err == nil && ok {
					repos = append(repos, repo)
				}
			}

			if len(batch) < repoListBatch {
				break
			}
		}

		total = len(repos)
		start := (page - 1) * limit
		if start >= total {
			repos = nil
		} else {
			end := min(start+limit, total)
			repos = repos[start:end]
		}
	} else {
		var err error
		repos, err = h.db.ListRepositories(ctx, page, limit, formatFilter)
		if err != nil {
			h.log.Errorw("list repositories: db error", "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "database error")

			return
		}

		total, err = h.db.CountRepositories(ctx, formatFilter)
		if err != nil {
			h.log.Errorw("count repositories: db error", "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "database error")

			return
		}
	}

	filtered := make([]responses.RepositoryResponse, 0, len(repos))
	orgNames := map[string]string{}

	for _, repo := range repos {
		filtered = append(filtered, h.repositoryResponse(ctx, repo, orgNames))
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"total":        total,
		"page":         page,
		"limit":        limit,
		"repositories": filtered,
	})
}

func (h *RepositoriesHandler) handleGet(w http.ResponseWriter, r *http.Request) {
	name := urlparams.Vars(r)["name"]
	ctx := r.Context()

	repo, err := h.db.GetRepositoryByName(ctx, name)
	if err != nil {
		h.log.Errorw("get repository: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if repo == nil {
		writeError(w, http.StatusNotFound, "not_found", "repository not found")
		return
	}

	p := currentPrincipal(r)
	if p != nil && !p.IsAnonymous {
		ok, err := h.evaluator.Evaluate(ctx, *p, repo.Name, "", "read")
		if err != nil || !ok {
			writeError(w, http.StatusForbidden, "forbidden", "access denied")
			return
		}
	}

	writeJSON(w, http.StatusOK, h.repositoryResponse(ctx, repo, map[string]string{}))
}

// orgName resolves an organization ID to its name. memo caches lookups across
// the repositories of one response. An unresolvable organization yields "".
func (h *RepositoriesHandler) orgName(ctx context.Context, orgID *string, memo map[string]string) string {
	if orgID == nil {
		return ""
	}

	if name, ok := memo[*orgID]; ok {
		return name
	}

	org, err := h.db.GetOrganization(ctx, *orgID)
	if err != nil || org == nil {
		if err != nil {
			h.log.Errorw("resolve repository organization: db error", "org_id", *orgID, "error", err)
		}

		return ""
	}

	memo[*orgID] = org.Name

	return org.Name
}

// repositoryResponse is RepositoryToResponse plus the owning organization's name.
func (h *RepositoriesHandler) repositoryResponse(ctx context.Context, repo *models.Repository, orgNames map[string]string) responses.RepositoryResponse {
	resp := RepositoryToResponse(repo)
	resp.Org = h.orgName(ctx, repo.OrgID, orgNames)

	return resp
}

// resolveOrg looks an organization up by name for a create/move request. It
// writes the error response (404 for an unknown name) and returns nil on failure.
func (h *RepositoriesHandler) resolveOrg(ctx context.Context, w http.ResponseWriter, name string) *models.Organization {
	org, err := h.db.GetOrganizationByName(ctx, name)
	if err != nil {
		h.log.Errorw("resolve organization: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return nil
	}

	if org == nil {
		writeError(w, http.StatusNotFound, "not_found", "organization not found")
		return nil
	}

	return org
}

// validateUpstreamURL rejects non-http(s) schemes and private/metadata hosts.
func validateUpstreamURL(raw string) error {
	if err := netutil.ValidateOutboundURL(raw); err != nil {
		return fmt.Errorf("upstream_url is not allowed: %w", err)
	}

	// The proxy appends request paths to the URL, so it must be a plain base;
	// credentials belong in upstream_username/upstream_password where they are
	// encrypted and never returned.
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errUpstreamURLShape
	}

	return nil
}

// rejectNonHosted answers 405 and returns true when repo is a proxy or group:
// those are read-only views and take no uploads or package deletions.
func rejectNonHosted(w http.ResponseWriter, repo *models.Repository) bool {
	if repo.RepoType == "" || repo.RepoType == repotype.Hosted {
		return false
	}

	w.Header().Set("Allow", "GET, HEAD")
	writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "proxy and group repositories are read-only")

	return true
}

// unsupportedRepoTypeMessage explains why a proxy/group repository of format
// cannot be created, or returns "" when it can be served.
func unsupportedRepoTypeMessage(repoType, format string) string {
	switch {
	case repoType == repotype.Proxy && !repotype.ProxySupported(format):
		return fmt.Sprintf("proxy repositories are not supported for format %q (supported: %s)", format, repotype.ProxyFormats())
	case repoType == repotype.Group && (format == "deb" || format == "rpm"):
		return fmt.Sprintf("group repositories are not supported for format %q: repository metadata cannot be merged across members (supported: %s)", format, repotype.GroupFormats())
	case repoType == repotype.Group && !repotype.GroupSupported(format):
		return fmt.Sprintf("group repositories are not supported for format %q (supported: %s)", format, repotype.GroupFormats())
	}

	return ""
}

var errUpstreamURLShape = errors.New("upstream_url must be a plain base URL without credentials, query or fragment")

// normalizeTTL validates a cache TTL: negative values are rejected, values
// above max are clamped.
func normalizeTTL(field string, v, def, maxV int) (int, error) {
	if v < 0 {
		return 0, validationError(field + " must not be negative")
	}

	if v == 0 {
		return def, nil
	}

	return min(v, maxV), nil
}

// normalizeMaxStale validates max_stale_seconds. Unlike the TTLs, 0 is a real
// value (never serve a stale copy); only an absent field takes the default.
func normalizeMaxStale(v *int) (int, error) {
	switch {
	case v == nil:
		return defaultMaxStaleSeconds, nil
	case *v < 0:
		return 0, validationError("max_stale_seconds must not be negative")
	}

	return min(*v, maxMaxStaleSeconds), nil
}

// resolveGroupMembers validates a member list for a group repository and
// returns the member repository IDs in priority order.
func (h *RepositoriesHandler) resolveGroupMembers(ctx context.Context, groupName, groupFormat string, names []string) ([]string, int, string, string) {
	if len(names) > maxGroupMembers {
		return nil, http.StatusBadRequest, "validation_failed", fmt.Sprintf("a group may have at most %d members", maxGroupMembers)
	}

	seen := make(map[string]bool, len(names))
	ids := make([]string, 0, len(names))

	for _, memberName := range names {
		if memberName == groupName {
			return nil, http.StatusBadRequest, "validation_failed", "a group cannot contain itself"
		}

		if seen[memberName] {
			return nil, http.StatusBadRequest, "validation_failed", "duplicate member repository: " + memberName
		}

		seen[memberName] = true

		member, err := h.db.GetRepositoryByName(ctx, memberName)
		if err != nil {
			h.log.Errorw("failed to get member repo", "name", memberName, "error", err)
			return nil, http.StatusInternalServerError, "internal_error", "database error"
		}

		if member == nil {
			return nil, http.StatusBadRequest, "validation_failed", "member repository not found: " + memberName
		}

		if member.RepoType == repotype.Group {
			return nil, http.StatusBadRequest, "validation_failed",
				fmt.Sprintf("member repository %q is a group; groups cannot be nested", memberName)
		}

		if member.Format != groupFormat {
			return nil, http.StatusBadRequest, "validation_failed",
				fmt.Sprintf("member repository %q has format %q, group format is %q", memberName, member.Format, groupFormat)
		}

		ids = append(ids, member.ID)
	}

	return ids, 0, "", ""
}

// encryptUpstreamPassword encrypts a proxy upstream password for storage.
func encryptUpstreamPassword(plain string) (string, error) {
	enc, err := crypto.Encrypt(plain)
	if err != nil {
		return "", fmt.Errorf("encrypt upstream password: %w", err)
	}

	return enc, nil
}

//nolint:gocyclo,cyclop // validation-heavy request handler
func (h *RepositoriesHandler) handleCreate(w http.ResponseWriter, r *http.Request) {
	p := currentPrincipal(r)

	isAdmin, err := principalIsAdmin(r.Context(), p, h.evaluator)
	if err != nil || !isAdmin {
		writeError(w, http.StatusForbidden, "forbidden", "admin access required")
		return
	}

	var body struct {
		Name                    string   `json:"name"`
		Format                  string   `json:"format"`
		Description             string   `json:"description"`
		Public                  bool     `json:"public"`
		RepoType                string   `json:"repo_type"`
		UpstreamURL             string   `json:"upstream_url"`
		UpstreamUsername        string   `json:"upstream_username"`
		UpstreamPassword        string   `json:"upstream_password"`
		CacheTTLSeconds         int      `json:"cache_ttl_seconds"`
		NegativeCacheTTLSeconds int      `json:"negative_cache_ttl_seconds"`
		MaxStaleSeconds         *int     `json:"max_stale_seconds"`
		MemberRepos             []string `json:"member_repos"`
		// PresignDownloads overrides storage.presignDownloads (null/absent inherits).
		PresignDownloads *bool `json:"presign_downloads"`
		// Org is the owning organization's name; empty means the built-in
		// system organization.
		Org string `json:"org"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}

	if !repoNameRe.MatchString(body.Name) {
		writeError(w, http.StatusBadRequest, "validation_failed", "name must be 1-100 alphanumeric/hyphen/underscore characters")
		return
	}

	if !validFormats[body.Format] {
		writeError(w, http.StatusBadRequest, "validation_failed", "format must be one of: "+validFormatList())
		return
	}

	repoType := body.RepoType
	if repoType == "" {
		repoType = "hosted"
	}

	if repoType != "hosted" && repoType != "proxy" && repoType != "group" {
		writeError(w, http.StatusBadRequest, "validation_failed", "repo_type must be one of: hosted, proxy, group")
		return
	}

	if msg := unsupportedRepoTypeMessage(repoType, body.Format); msg != "" {
		writeError(w, http.StatusBadRequest, "validation_failed", msg)
		return
	}

	repo := &models.Repository{
		Name:        body.Name,
		Format:      body.Format,
		Description: body.Description,
		Public:      body.Public,
		RepoType:    repoType,
	}

	hasUpstream := body.UpstreamURL != "" || body.UpstreamUsername != "" || body.UpstreamPassword != ""

	switch repoType {
	case "proxy":
		if body.UpstreamURL == "" {
			writeError(w, http.StatusBadRequest, "validation_failed", "upstream_url is required for proxy repositories")
			return
		}

		if err := validateUpstreamURL(body.UpstreamURL); err != nil {
			writeError(w, http.StatusBadRequest, "validation_failed", err.Error())
			return
		}

		ttl, err := normalizeTTL("cache_ttl_seconds", body.CacheTTLSeconds, defaultCacheTTLSeconds, maxCacheTTLSeconds)
		if err != nil {
			writeError(w, http.StatusBadRequest, "validation_failed", err.Error())
			return
		}

		negTTL, err := normalizeTTL("negative_cache_ttl_seconds", body.NegativeCacheTTLSeconds, defaultNegativeCacheTTLSeconds, maxNegativeCacheTTLSeconds)
		if err != nil {
			writeError(w, http.StatusBadRequest, "validation_failed", err.Error())
			return
		}

		maxStale, err := normalizeMaxStale(body.MaxStaleSeconds)
		if err != nil {
			writeError(w, http.StatusBadRequest, "validation_failed", err.Error())
			return
		}

		encPassword, err := encryptUpstreamPassword(body.UpstreamPassword)
		if err != nil {
			h.log.Errorw("create repository: encrypt upstream password", "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "failed to protect upstream credentials")

			return
		}

		repo.UpstreamURL = body.UpstreamURL
		repo.UpstreamUsername = body.UpstreamUsername
		repo.UpstreamPasswordEnc = encPassword
		repo.CacheTTLSeconds = ttl
		repo.NegativeCacheTTLSeconds = negTTL
		repo.MaxStaleSeconds = maxStale
	default:
		if hasUpstream {
			writeError(w, http.StatusBadRequest, "validation_failed", "upstream_* fields are only valid for proxy repositories")
			return
		}

		repo.CacheTTLSeconds = defaultCacheTTLSeconds
		repo.NegativeCacheTTLSeconds = defaultNegativeCacheTTLSeconds
		repo.MaxStaleSeconds = defaultMaxStaleSeconds
	}

	var memberIDs []string

	switch {
	case repoType == "group":
		ids, status, code, msg := h.resolveGroupMembers(r.Context(), body.Name, body.Format, body.MemberRepos)
		if status != 0 {
			writeError(w, status, code, msg)
			return
		}

		memberIDs = ids
	case len(body.MemberRepos) > 0:
		writeError(w, http.StatusBadRequest, "validation_failed", "member_repos is only valid for group repositories")
		return
	}

	orgID := models.SystemOrgID

	if body.Org != "" {
		org := h.resolveOrg(r.Context(), w, body.Org)
		if org == nil {
			return
		}

		orgID = org.ID
	}

	now := time.Now()
	repo.OrgID = &orgID
	repo.PresignDownloads = body.PresignDownloads
	repo.CreatedAt = now
	repo.UpdatedAt = now

	if err := h.db.CreateRepositoryWithMembers(r.Context(), repo, memberIDs); err != nil {
		if database.IsUniqueViolation(err) {
			writeError(w, http.StatusConflict, "conflict", "a repository with this name already exists")
			return
		}

		h.log.Errorw("create repository: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	webhook.Emit(r.Context(), h.publisher, repo.ID, webhook.EventRepoCreated,
		webhook.RepositoryPayload(webhook.EventRepoCreated, repo.Name, repo.Format, repo.RepoType))

	writeJSON(w, http.StatusCreated, h.repositoryResponse(r.Context(), repo, map[string]string{}))
}

//nolint:gocyclo,cyclop // validation-heavy request handler
func (h *RepositoriesHandler) handleUpdate(w http.ResponseWriter, r *http.Request) {
	p := currentPrincipal(r)

	isAdmin, err := principalIsAdmin(r.Context(), p, h.evaluator)
	if err != nil || !isAdmin {
		writeError(w, http.StatusForbidden, "forbidden", "admin access required")
		return
	}

	name := urlparams.Vars(r)["name"]
	ctx := r.Context()

	repo, err := h.db.GetRepositoryByName(ctx, name)
	if err != nil {
		h.log.Errorw("update repository: db error getting repo", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if repo == nil {
		writeError(w, http.StatusNotFound, "not_found", "repository not found")
		return
	}

	// MemberRepos is a pointer so that an explicit empty list ([]) clears the
	// group while an absent/null field leaves it unchanged.
	var body struct {
		Description             *string   `json:"description"`
		Public                  *bool     `json:"public"`
		UpstreamURL             *string   `json:"upstream_url"`
		UpstreamUsername        *string   `json:"upstream_username"`
		UpstreamPassword        *string   `json:"upstream_password"`
		CacheTTLSeconds         *int      `json:"cache_ttl_seconds"`
		NegativeCacheTTLSeconds *int      `json:"negative_cache_ttl_seconds"`
		MaxStaleSeconds         *int      `json:"max_stale_seconds"`
		Blocked                 *bool     `json:"blocked"`
		MemberRepos             *[]string `json:"member_repos"`
		// PresignDownloads is tri-state: absent leaves the override unchanged,
		// null clears it (inherit storage.presignDownloads), true/false set it.
		PresignDownloads json.RawMessage `json:"presign_downloads"`
		// Org moves the repository to the named organization.
		Org *string `json:"org"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}

	touchesUpstream := body.UpstreamURL != nil || body.UpstreamUsername != nil || body.UpstreamPassword != nil ||
		body.CacheTTLSeconds != nil || body.NegativeCacheTTLSeconds != nil || body.MaxStaleSeconds != nil
	if touchesUpstream && repo.RepoType != "proxy" {
		writeError(w, http.StatusBadRequest, "validation_failed", "upstream and cache fields are only valid for proxy repositories")
		return
	}

	if body.MemberRepos != nil && repo.RepoType != "group" {
		writeError(w, http.StatusBadRequest, "validation_failed", "member_repos is only valid for group repositories")
		return
	}

	if body.Description != nil {
		repo.Description = *body.Description
	}

	if body.Public != nil {
		repo.Public = *body.Public
	}

	if body.UpstreamURL != nil {
		if *body.UpstreamURL == "" {
			writeError(w, http.StatusBadRequest, "validation_failed", "upstream_url cannot be empty for a proxy repository")
			return
		}

		if err := validateUpstreamURL(*body.UpstreamURL); err != nil {
			writeError(w, http.StatusBadRequest, "validation_failed", err.Error())
			return
		}

		repo.UpstreamURL = *body.UpstreamURL
	}

	if body.UpstreamUsername != nil {
		repo.UpstreamUsername = *body.UpstreamUsername
	}

	if body.UpstreamPassword != nil {
		enc, err := encryptUpstreamPassword(*body.UpstreamPassword)
		if err != nil {
			h.log.Errorw("update repository: encrypt upstream password", "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "failed to protect upstream credentials")

			return
		}

		repo.UpstreamPasswordEnc = enc
	}

	if body.CacheTTLSeconds != nil {
		ttl, err := normalizeTTL("cache_ttl_seconds", *body.CacheTTLSeconds, defaultCacheTTLSeconds, maxCacheTTLSeconds)
		if err != nil {
			writeError(w, http.StatusBadRequest, "validation_failed", err.Error())
			return
		}

		repo.CacheTTLSeconds = ttl
	}

	if body.NegativeCacheTTLSeconds != nil {
		ttl, err := normalizeTTL("negative_cache_ttl_seconds", *body.NegativeCacheTTLSeconds, defaultNegativeCacheTTLSeconds, maxNegativeCacheTTLSeconds)
		if err != nil {
			writeError(w, http.StatusBadRequest, "validation_failed", err.Error())
			return
		}

		repo.NegativeCacheTTLSeconds = ttl
	}

	if body.MaxStaleSeconds != nil {
		maxStale, err := normalizeMaxStale(body.MaxStaleSeconds)
		if err != nil {
			writeError(w, http.StatusBadRequest, "validation_failed", err.Error())
			return
		}

		repo.MaxStaleSeconds = maxStale
	}

	if body.Blocked != nil {
		repo.Blocked = *body.Blocked
	}

	if body.PresignDownloads != nil {
		var override *bool
		if err := json.Unmarshal(body.PresignDownloads, &override); err != nil {
			writeError(w, http.StatusBadRequest, "validation_failed", "presign_downloads must be true, false or null")
			return
		}

		repo.PresignDownloads = override
	}

	if body.Org != nil {
		org := h.resolveOrg(ctx, w, *body.Org)
		if org == nil {
			return
		}

		repo.OrgID = &org.ID
	}

	var memberIDs []string

	if body.MemberRepos != nil {
		ids, status, code, msg := h.resolveGroupMembers(ctx, repo.Name, repo.Format, *body.MemberRepos)
		if status != 0 {
			writeError(w, status, code, msg)
			return
		}

		memberIDs = ids
	}

	if err := h.db.UpdateRepository(ctx, repo); err != nil {
		if database.IsUniqueViolation(err) {
			writeError(w, http.StatusConflict, "conflict", "a repository with this name already exists")
			return
		}

		h.log.Errorw("update repository: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if body.MemberRepos != nil {
		if err := h.db.ReplaceGroupMembers(ctx, repo.ID, memberIDs); err != nil {
			h.log.Errorw("update repository: replace group members", "group_id", repo.ID, "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "database error")

			return
		}
	}

	writeJSON(w, http.StatusOK, h.repositoryResponse(ctx, repo, map[string]string{}))
}

func (h *RepositoriesHandler) handleDelete(w http.ResponseWriter, r *http.Request) {
	p := currentPrincipal(r)

	isAdmin, err := principalIsAdmin(r.Context(), p, h.evaluator)
	if err != nil || !isAdmin {
		writeError(w, http.StatusForbidden, "forbidden", "admin access required")
		return
	}

	name := urlparams.Vars(r)["name"]
	ctx := r.Context()

	repo, err := h.db.GetRepositoryByName(ctx, name)
	if err != nil {
		h.log.Errorw("delete repository: db error getting repo", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if repo == nil {
		writeError(w, http.StatusNotFound, "not_found", "repository not found")
		return
	}

	if err := h.db.DeleteRepository(ctx, repo.ID); err != nil {
		if database.IsForeignKeyViolation(err) {
			writeError(w, http.StatusConflict, "conflict", "repository is still referenced (for example by a release bundle or promotion)")
			return
		}

		h.log.Errorw("delete repository: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	webhook.Emit(ctx, h.publisher, repo.ID, webhook.EventRepoDeleted,
		webhook.RepositoryPayload(webhook.EventRepoDeleted, repo.Name, repo.Format, repo.RepoType))

	// The delete cascaded the repository's package rows away; reclaim the blobs
	// only it referenced.
	h.collectOrphansAsync()

	writeNoContent(w)
}

// maxOrphanPasses bounds how many batches one repository delete reclaims; the
// cleanup worker's periodic pass picks up whatever is left.
const maxOrphanPasses = 100

// collectOrphansAsync reclaims blobs left unreferenced by a repository delete.
// It runs detached from the request (the client must not wait for storage
// deletes) and is best-effort: failures are logged and retried by the cleanup
// worker's periodic pass.
func (h *RepositoriesHandler) collectOrphansAsync() {
	if h.blobStore == nil {
		return
	}

	ctx := context.WithoutCancel(context.Background())

	h.gc.Go(func() {
		for range maxOrphanPasses {
			removed, err := storage.CollectOrphanBlobs(ctx, h.db, h.blobStore, storage.OrphanMinAge, storage.OrphanBatch)
			if err != nil {
				h.log.Warnw("orphan blob collection after repository delete failed", "removed", removed, "error", err)
				return
			}

			if removed < storage.OrphanBatch {
				return
			}
		}
	})
}

func (h *RepositoriesHandler) handlePackages(w http.ResponseWriter, r *http.Request) {
	name := urlparams.Vars(r)["name"]
	q := r.URL.Query()
	formatFilter := q.Get("format")
	page := min(parseIntMin(q.Get("page"), 1, 1), maxListPage)
	limit := parseIntClamp(q.Get("limit"), 25, 1, 100)

	ctx := r.Context()

	repo, err := h.db.GetRepositoryByName(ctx, name)
	if err != nil {
		h.log.Errorw("packages: db error getting repo", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if repo == nil {
		writeError(w, http.StatusNotFound, "not_found", "repository not found")
		return
	}

	p := currentPrincipal(r)
	if p != nil && !p.IsAnonymous {
		ok, err := h.evaluator.Evaluate(ctx, *p, repo.Name, "", "read")
		if err != nil || !ok {
			writeError(w, http.StatusForbidden, "forbidden", "access denied")
			return
		}
	}

	components, total, err := h.db.ListComponentsByRepo(ctx, repo.ID, formatFilter, page, limit)
	if err != nil {
		h.log.Errorw("packages: db error listing components", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	packages := make([]map[string]any, 0, len(components))
	for _, c := range components {
		packages = append(packages, map[string]any{
			"id":         c.ID,
			"name":       c.Name,
			"version":    c.Version,
			"format":     c.Format,
			"arch":       c.Arch,
			"size":       c.PackageSize,
			"sha256":     c.SHA256,
			"filename":   c.Filename,
			"created_at": c.CreatedAt,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"total":    total,
		"page":     page,
		"limit":    limit,
		"packages": packages,
	})
}

// handleDownloadPackage streams the blob of a package (by component ID) that
// belongs to the repository named in the URL.
//
//	GET /api/v1/repositories/{name}/packages/{id}/download
func (h *RepositoriesHandler) handleDownloadPackage(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	ctx := r.Context()

	if h.blobStore == nil {
		writeError(w, http.StatusNotImplemented, "not_implemented", "package download is not available")
		return
	}

	repo, err := h.db.GetRepositoryByName(ctx, vars["name"])
	if err != nil {
		h.log.Errorw("download package: db error getting repo", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if repo == nil {
		writeError(w, http.StatusNotFound, "not_found", "repository not found")
		return
	}

	blob, err := h.db.GetRepoComponentBlob(ctx, repo.ID, vars["id"])
	if err != nil {
		h.log.Errorw("download package: db error getting component", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if blob == nil {
		writeError(w, http.StatusNotFound, "not_found", "package not found")
		return
	}

	if blob.SHA256 == "" {
		writeError(w, http.StatusNotFound, "not_found", "package has no downloadable file")
		return
	}

	p := currentPrincipal(r)
	if p == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}

	ok, err := h.evaluator.Evaluate(ctx, *p, repo.Name, blob.Filename, "read")
	if err != nil || !ok {
		writeError(w, http.StatusForbidden, "forbidden", "access denied")
		return
	}

	// The UI scans and quarantines packages by their row id.
	if quarantine.Enforce(w, r, h.db, repo.ID, quarantine.PathKeys(blob.ID, blob.Filename, r.URL.Path)...) {
		return
	}

	reader, err := h.blobStore.Get(ctx, blob.SHA256)
	if err != nil {
		h.log.Errorw("download package: blob read failed", "package_id", blob.ID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to read package")

		return
	}

	defer func() { _ = reader.Close() }()

	w.Header().Set("Content-Type", downloadContentType(blob.MediaType))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": downloadFilename(blob)}))

	if blob.Size > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(blob.Size, 10))
	}

	if _, err := io.Copy(w, reader); err != nil && !errors.Is(err, context.Canceled) {
		h.log.Errorw("download package: write failed", "package_id", blob.ID, "error", err)
	}
}

// downloadContentType returns the blob's stored media type, or
// application/octet-stream when it is empty or not a valid media type.
func downloadContentType(mediaType string) string {
	if _, _, err := mime.ParseMediaType(mediaType); err != nil {
		return "application/octet-stream"
	}

	return mediaType
}

// downloadFilename derives the attachment name of a listed package: the
// listing's filename reduced to a safe basename. Docker tags download their
// manifest, named after its digest; conan references (name/version@user#rev)
// keep every part with the separators flattened.
func downloadFilename(blob *models.ComponentBlob) string {
	name := blob.Filename

	switch blob.Format {
	case "docker":
		name = strings.ReplaceAll(name, ":", "_") + ".json"
	case "conan":
		name = strings.NewReplacer("/", "_", "\\", "_").Replace(name)
	default:
		name = path.Base(strings.ReplaceAll(name, "\\", "/"))
	}

	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}

		return r
	}, name)

	if name == "" || name == "." || name == ".." || name == "/" {
		return blob.ID
	}

	return name
}

// writeMemberList writes a group's members in resolution order with the
// member repository's name, format and type.
func (h *RepositoriesHandler) writeMemberList(w http.ResponseWriter, r *http.Request, members []*models.RepositoryGroupMember) {
	memberList := make([]map[string]any, 0, len(members))

	for _, member := range members {
		repo, err := h.db.GetRepositoryByID(r.Context(), member.MemberRepoID)
		if err != nil {
			h.log.Errorw("list members: db error getting member repo", "member_id", member.MemberRepoID, "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "database error")

			return
		}

		if repo == nil {
			continue
		}

		memberList = append(memberList, map[string]any{
			"id":        member.MemberRepoID,
			"name":      repo.Name,
			"format":    repo.Format,
			"repo_type": repo.RepoType,
			"priority":  member.Priority,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{"members": memberList})
}

func (h *RepositoriesHandler) handleGetMembers(w http.ResponseWriter, r *http.Request) {
	name := urlparams.Vars(r)["name"]
	ctx := r.Context()

	repo, err := h.db.GetRepositoryByName(ctx, name)
	if err != nil {
		h.log.Errorw("get members: db error getting repo", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if repo == nil {
		writeError(w, http.StatusNotFound, "not_found", "repository not found")
		return
	}

	if repo.RepoType != "group" {
		writeError(w, http.StatusBadRequest, "bad_request", "repository is not a group")
		return
	}

	members, err := h.db.ListGroupMembers(ctx, repo.ID)
	if err != nil {
		h.log.Errorw("get members: db error listing members", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	h.writeMemberList(w, r, members)
}

func (h *RepositoriesHandler) handleUpdateMembers(w http.ResponseWriter, r *http.Request) {
	p := currentPrincipal(r)

	isAdmin, err := principalIsAdmin(r.Context(), p, h.evaluator)
	if err != nil || !isAdmin {
		writeError(w, http.StatusForbidden, "forbidden", "admin access required")
		return
	}

	name := urlparams.Vars(r)["name"]
	ctx := r.Context()

	repo, err := h.db.GetRepositoryByName(ctx, name)
	if err != nil {
		h.log.Errorw("update members: db error getting repo", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if repo == nil {
		writeError(w, http.StatusNotFound, "not_found", "repository not found")
		return
	}

	if repo.RepoType != "group" {
		writeError(w, http.StatusBadRequest, "bad_request", "repository is not a group")
		return
	}

	var body struct {
		MemberRepos []string `json:"member_repos"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}

	memberIDs, status, code, msg := h.resolveGroupMembers(ctx, repo.Name, repo.Format, body.MemberRepos)
	if status != 0 {
		writeError(w, status, code, msg)
		return
	}

	if err := h.db.ReplaceGroupMembers(ctx, repo.ID, memberIDs); err != nil {
		h.log.Errorw("update members: replace group members", "group_id", repo.ID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	members, err := h.db.ListGroupMembers(ctx, repo.ID)
	if err != nil {
		h.log.Errorw("update members: db error listing members", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	h.writeMemberList(w, r, members)
}

// RepositoryToResponse converts a repository to its public API shape. It never
// includes upstream credentials; the MCP server reuses it.
func RepositoryToResponse(r *models.Repository) responses.RepositoryResponse {
	desc := r.Description // copy to allow taking address

	resp := responses.RepositoryResponse{
		ID:          r.ID,
		Name:        r.Name,
		Format:      r.Format,
		Description: &desc,
		Public:      r.Public,
		RepoType:    r.RepoType,
		CreatedAt:   formatTime(r.CreatedAt),
		UpdatedAt:   formatTime(r.UpdatedAt),

		PresignDownloads: r.PresignDownloads,
	}

	if r.RepoType == repotype.Proxy {
		ttl, negTTL, maxStale := r.CacheTTLSeconds, r.NegativeCacheTTLSeconds, r.MaxStaleSeconds
		resp.UpstreamURL = r.UpstreamURL
		resp.UpstreamUsername = r.UpstreamUsername
		resp.CacheTTLSeconds = &ttl
		resp.NegativeCacheTTLSeconds = &negTTL
		resp.MaxStaleSeconds = &maxStale
	}

	return resp
}

func parseIntMin(s string, defaultVal, minVal int) int {
	v, err := strconv.Atoi(s)
	if err != nil || v < minVal {
		if s == "" {
			return defaultVal
		}

		return minVal
	}

	return v
}

func parseIntClamp(s string, defaultVal, minVal, maxVal int) int { //nolint:unparam // minVal kept for API clarity and future flexibility
	v, err := strconv.Atoi(s)
	if err != nil {
		return defaultVal
	}

	if v < minVal {
		return minVal
	}

	if v > maxVal {
		return maxVal
	}

	return v
}
