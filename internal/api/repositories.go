// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/api/responses"
	"github.com/openartifact/openartifact/internal/audit"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/crypto"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/federation"
	"github.com/openartifact/openartifact/internal/iocopy"
	"github.com/openartifact/openartifact/internal/netutil"
	"github.com/openartifact/openartifact/internal/pathseg"
	"github.com/openartifact/openartifact/internal/quarantine"
	"github.com/openartifact/openartifact/internal/repotype"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/trash"
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
	groupMeta GroupMetadataNotifier
	// fed enables creating federated repositories (nil: federation is not
	// available on this server).
	fed *federation.Store

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

// WithFederation lets repositories be created federated (federation.enabled).
func (h *RepositoriesHandler) WithFederation(store *federation.Store) *RepositoriesHandler {
	h.fed = store

	return h
}

// WithPublisher enables repository.created / repository.deleted webhook events.
func (h *RepositoriesHandler) WithPublisher(p webhook.Publisher) *RepositoriesHandler {
	h.publisher = p

	return h
}

// GroupMetadataNotifier invalidates the generated metadata of deb and rpm group
// repositories (worker.GroupMetadataWorker).
type GroupMetadataNotifier interface {
	// NotifyStale marks a group's metadata stale and schedules its regeneration.
	NotifyStale(groupID string)
	// NotifyMemberChanged does so for every deb/rpm group listing the repository.
	NotifyMemberChanged(repoID string)
}

// WithGroupMetadata makes changes to deb and rpm repositories regenerate the
// metadata of the groups they belong to.
func (h *RepositoriesHandler) WithGroupMetadata(n GroupMetadataNotifier) *RepositoriesHandler {
	h.groupMeta = n

	return h
}

// metadataChanged invalidates the merged metadata a change to repo affects: a
// deb/rpm group's own, or its member's groups'.
func (h *RepositoriesHandler) metadataChanged(repo *models.Repository) {
	notifyGroupMetadata(h.groupMeta, repo)
}

func notifyGroupMetadata(n GroupMetadataNotifier, repo *models.Repository) {
	if n == nil || (repo.Format != "deb" && repo.Format != "rpm") {
		return
	}

	if repo.RepoType == repotype.Group {
		n.NotifyStale(repo.ID)
		return
	}

	n.NotifyMemberChanged(repo.ID)
}

// WaitBackground blocks until background orphan-blob collections finish.
func (h *RepositoriesHandler) WaitBackground() { h.gc.Wait() }

func (h *RepositoriesHandler) RegisterAuthed(r chi.Router) {
	r.Get("/api/v1/repositories", h.handleList)
	r.Post("/api/v1/repositories", h.handleCreate)
	r.Get("/api/v1/repository/{name}", h.handleGet)
	r.Patch("/api/v1/repository/{name}", h.handleUpdate)
	r.Delete("/api/v1/repository/{name}", h.handleDelete)
	r.Post("/api/v1/projects/{project}/repositories", h.handleCreateInProject)
	r.Post("/api/v1/projects/{project}/repositories/{name}/transfer", h.handleTransfer)
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
	projectNames := map[string]string{}

	for _, repo := range repos {
		filtered = append(filtered, h.repositoryResponse(ctx, repo, projectNames))
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

	resp := h.repositoryResponse(ctx, repo, map[string]string{})

	if p != nil {
		perms, err := h.repositoryPermissions(ctx, *p, repo.Name)
		if err != nil {
			h.log.Errorw("get repository: permission evaluation failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "auth evaluation failed")

			return
		}

		resp.Permissions = perms
	}

	writeJSON(w, http.StatusOK, resp)
}

// repositoryPermissions evaluates the write, delete, manage and admin actions of p on repoName.
func (h *RepositoriesHandler) repositoryPermissions(ctx context.Context, p auth.Principal, repoName string) (*responses.RepositoryPermissions, error) {
	var perms responses.RepositoryPermissions

	for action, dst := range map[string]*bool{"write": &perms.Write, "delete": &perms.Delete, "manage": &perms.Manage} {
		ok, err := h.evaluator.Evaluate(ctx, p, repoName, "", action)
		if err != nil {
			return nil, err
		}

		*dst = ok
	}

	admin, err := h.evaluator.EvaluateRepoAdmin(ctx, p, repoName)
	if err != nil {
		return nil, err
	}

	perms.Admin = admin

	return &perms, nil
}

// projectName resolves a project ID to its name. memo caches lookups across
// the repositories of one response. An unresolvable project yields "".
func (h *RepositoriesHandler) projectName(ctx context.Context, projectID *string, memo map[string]string) string {
	if projectID == nil {
		return ""
	}

	if name, ok := memo[*projectID]; ok {
		return name
	}

	project, err := h.db.GetProject(ctx, *projectID)
	if err != nil || project == nil {
		if err != nil {
			h.log.Errorw("resolve repository project: db error", "project_id", *projectID, "error", err)
		}

		return ""
	}

	memo[*projectID] = project.Name

	return project.Name
}

// repositoryResponse is RepositoryToResponse plus the owning project's name.
func (h *RepositoriesHandler) repositoryResponse(ctx context.Context, repo *models.Repository, projectNames map[string]string) responses.RepositoryResponse {
	resp := RepositoryToResponse(repo)
	resp.Project = h.projectName(ctx, repo.ProjectID, projectNames)

	return resp
}

// resolveProject looks a project up by name for a create/move request. It
// writes the error response (404 for an unknown name) and returns nil on failure.
func (h *RepositoriesHandler) resolveProject(ctx context.Context, w http.ResponseWriter, name string) *models.Project {
	project, err := h.db.GetProjectByName(ctx, name)
	if err != nil {
		h.log.Errorw("resolve project: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return nil
	}

	if project == nil {
		writeError(w, http.StatusNotFound, "not_found", "project not found")
		return nil
	}

	return project
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

// sameUpstreamOrigin reports whether two upstream URLs share scheme, host and
// port, i.e. whether credentials valid for one are sent to the same server by
// the other. Unparsable input counts as a different origin.
func sameUpstreamOrigin(a, b string) bool {
	ua, errA := url.Parse(a)
	ub, errB := url.Parse(b)

	if errA != nil || errB != nil {
		return false
	}

	return strings.EqualFold(ua.Scheme, ub.Scheme) &&
		strings.EqualFold(ua.Hostname(), ub.Hostname()) &&
		upstreamPort(ua) == upstreamPort(ub)
}

func upstreamPort(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}

	if strings.EqualFold(u.Scheme, "https") {
		return "443"
	}

	return "80"
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
	case repoType == repotype.Group && !repotype.GroupSupported(format):
		return fmt.Sprintf("group repositories are not supported for format %q (supported: %s)", format, repotype.GroupFormats())
	}

	return ""
}

// errMergeProxyVersionsAdmin explains why a project maintainer may not touch
// merge_proxy_versions: it switches the dependency-confusion protection of a
// group off.
const errSignPackagesRPMOnly = "sign_packages is only valid for hosted rpm repositories"

const errMergeProxyVersionsAdmin = "only an administrator can change merge_proxy_versions"

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
// A non-nil reader must be able to read every member (403 otherwise): a group
// serves its members' content to whoever reads the group.
func (h *RepositoriesHandler) resolveGroupMembers(ctx context.Context, reader *auth.Principal, groupName, groupFormat string, names []string) ([]string, int, string, string) {
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

		if reader != nil {
			ok, err := h.evaluator.Evaluate(ctx, *reader, memberName, "", "read")
			if err != nil {
				h.log.Errorw("group members: read check failed", "name", memberName, "error", err)
				return nil, http.StatusInternalServerError, "internal_error", "auth evaluation failed"
			}

			if !ok {
				return nil, http.StatusForbidden, "forbidden", "no read access to member repository: " + memberName
			}
		}

		ids = append(ids, member.ID)
	}

	return ids, 0, "", ""
}

// encryptUpstreamPassword encrypts a proxy upstream password for storage.
func encryptUpstreamPassword(plain string) (string, error) {
	if err := crypto.CheckPlaintext(plain); err != nil {
		return "", validationError("upstream_password must be the plaintext password")
	}

	enc, err := crypto.Encrypt(plain)
	if err != nil {
		return "", fmt.Errorf("encrypt upstream password: %w", err)
	}

	return enc, nil
}

// createRepositoryBody is the body of POST /api/v1/repositories and of
// POST /api/v1/projects/{project}/repositories.
type createRepositoryBody struct {
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
	// Project is the owning project's name; empty means the built-in system
	// project. The project route takes it from the URL and ignores the field.
	Project string `json:"project"`
	// TrashRetentionDays: null/absent = server default, 0 = no trash.
	TrashRetentionDays *int `json:"trash_retention_days"`
	ImmutableReleases  bool `json:"immutable_releases"`
	// MergeProxyVersions (groups only, administrators only) turns the
	// hosted-members-win rule off: versions of every member are merged.
	MergeProxyVersions bool `json:"merge_proxy_versions"`
	// SignPackages (hosted rpm only) signs uploads with the active GPG key.
	SignPackages bool `json:"sign_packages"`
	// Federated (hosted repositories of a federation-capable format) starts the
	// repository's change log so it can replicate with peer instances.
	Federated bool `json:"federated"`
}

// handleCreate is the admin-only POST /api/v1/repositories.
func (h *RepositoriesHandler) handleCreate(w http.ResponseWriter, r *http.Request) {
	p := currentPrincipal(r)

	isAdmin, err := principalIsAdmin(r.Context(), p, h.evaluator)
	if err != nil || !isAdmin {
		writeError(w, http.StatusForbidden, "forbidden", "admin access required")
		return
	}

	var body createRepositoryBody
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}

	var project *models.Project

	if body.Project != "" {
		if project = h.resolveProject(r.Context(), w, body.Project); project == nil {
			return
		}
	}

	h.createRepository(w, r, &body, project, nil)
}

// createRepository validates body and creates the repository in project (nil =
// the built-in system project). It is shared by the admin route and the project
// route. A non-nil actor applies the project's self-service policy (formats,
// types, name prefix, public, repository cap, readable group members) as that
// non-admin caller; the admin route passes nil.
//
// checkFederated validates the federated flag of a create request: only an
// administrator (not a delegated project member) may start a federated
// repository, it must be hosted and of a capable format, and federation must be
// on for the server. It returns the message and status of the refusal.
func (h *RepositoriesHandler) checkFederated(body *createRepositoryBody, repoType string, actor *auth.Principal) (string, int) {
	switch {
	case !body.Federated:
		return "", 0
	case actor != nil:
		return "only an administrator can create a federated repository", http.StatusForbidden
	case repoType != repotype.Hosted || !repotype.FederationSupported(body.Format):
		return "federated is only valid for hosted repositories of a format with federation support: " + repotype.FederationFormats(), http.StatusBadRequest
	case h.fed == nil:
		return "federation is turned off on this server (federation.enabled / OPENARTIFACT_FEDERATION_ENABLED)", http.StatusBadRequest
	}

	return "", 0
}

//nolint:gocyclo,cyclop // validation-heavy request handler
func (h *RepositoriesHandler) createRepository(w http.ResponseWriter, r *http.Request, bodyPtr *createRepositoryBody, project *models.Project, actor *auth.Principal) {
	body := *bodyPtr

	if err := validateTrashRetention(body.TrashRetentionDays); err != nil {
		writeError(w, http.StatusBadRequest, "validation_failed", err.Error())
		return
	}

	if !repoNameRe.MatchString(body.Name) {
		writeError(w, http.StatusBadRequest, "validation_failed", "name must be 1-100 alphanumeric/hyphen/underscore characters")
		return
	}

	if models.IsReservedRepoName(body.Name) {
		writeError(w, http.StatusBadRequest, "validation_failed", "name is reserved; choose a different repository name")
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

	if body.MergeProxyVersions && repoType != "group" {
		writeError(w, http.StatusBadRequest, "validation_failed", "merge_proxy_versions is only valid for group repositories")
		return
	}

	if body.SignPackages && (repoType != "hosted" || body.Format != "rpm") {
		writeError(w, http.StatusBadRequest, "validation_failed", errSignPackagesRPMOnly)
		return
	}

	if msg, status := h.checkFederated(&body, repoType, actor); status != 0 {
		writeError(w, status, "validation_failed", msg)
		return
	}

	if actor != nil {
		if body.MergeProxyVersions {
			writeError(w, http.StatusForbidden, "forbidden", errMergeProxyVersionsAdmin)
			return
		}

		if status, code, msg := checkProjectRepoPolicy(project, body.Name, body.Format, repoType, body.Public); status != 0 {
			writeError(w, status, code, msg)
			return
		}
	}

	repo := &models.Repository{
		Name:        body.Name,
		Format:      body.Format,
		Description: body.Description,
		Public:      body.Public,
		RepoType:    repoType,

		TrashRetentionDays: body.TrashRetentionDays,
		ImmutableReleases:  body.ImmutableReleases,
		MergeProxyVersions: body.MergeProxyVersions,
		SignPackages:       body.SignPackages,
	}

	hasUpstream := body.UpstreamURL != "" || body.UpstreamUsername != "" || body.UpstreamPassword != ""

	switch repoType {
	case "proxy":
		if body.UpstreamURL == "" {
			body.UpstreamURL = repotype.DefaultUpstreamURL(body.Format)
		}

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
			if ve, ok := errors.AsType[validationError](err); ok {
				writeError(w, http.StatusBadRequest, "validation_failed", ve.Error())

				return
			}

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
		ids, status, code, msg := h.resolveGroupMembers(r.Context(), actor, body.Name, body.Format, body.MemberRepos)
		if status != 0 {
			writeError(w, status, code, msg)
			return
		}

		memberIDs = ids
	case len(body.MemberRepos) > 0:
		writeError(w, http.StatusBadRequest, "validation_failed", "member_repos is only valid for group repositories")
		return
	}

	projectID := models.SystemProjectID
	if project != nil {
		projectID = project.ID
	}

	now := time.Now()
	repo.ProjectID = &projectID
	repo.PresignDownloads = body.PresignDownloads
	repo.CreatedAt = now
	repo.UpdatedAt = now

	var err error
	if actor != nil && project != nil && project.MaxRepositories > 0 {
		err = h.db.CreateProjectRepository(r.Context(), repo, memberIDs, project.MaxRepositories)
	} else {
		err = h.db.CreateRepositoryWithMembers(r.Context(), repo, memberIDs)
	}

	if err != nil {
		if errors.Is(err, database.ErrProjectRepoLimit) {
			writeError(w, http.StatusConflict, "project_repository_limit", "the project has reached its repository limit")
			return
		}

		if database.IsUniqueViolation(err) {
			h.writeNameConflict(r.Context(), w, repo.Name)
			return
		}

		h.log.Errorw("create repository: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if body.Federated {
		if err := h.fed.Enable(r.Context(), repo.ID, repo.Format); err != nil {
			h.log.Errorw("create repository: enable federation", "repository", repo.Name, "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error",
				"the repository was created but federation could not be enabled; enable it from the repository's federation settings")

			return
		}

		repo.Federated = true
	}

	h.metadataChanged(repo)

	webhook.Emit(r.Context(), h.publisher, repo.ID, webhook.EventRepoCreated,
		webhook.RepositoryPayload(webhook.EventRepoCreated, repo.Name, repo.Format, repo.RepoType))

	if project != nil {
		h.emitProjectRepoEvent(r, audit.ActionProjectRepositoryCreated, project.ID, &repo.ID, "repository "+repo.Name+" created in project "+project.Name)
	}

	writeJSON(w, http.StatusCreated, h.repositoryResponse(r.Context(), repo, map[string]string{}))
}

//nolint:gocyclo,cyclop // validation-heavy request handler
func (h *RepositoriesHandler) handleUpdate(w http.ResponseWriter, r *http.Request) {
	name := urlparams.Vars(r)["name"]
	ctx := r.Context()

	if !requireRepoAdmin(w, r, h.evaluator, name) {
		return
	}

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

	// A project maintainer is a delegated, not a global, administrator.
	actor, err := h.delegatedActor(ctx, currentPrincipal(r))
	if err != nil {
		h.log.Errorw("update repository: auth evaluation failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "auth evaluation failed")

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
		// Project moves the repository to the named project.
		Project *string `json:"project"`
		// TrashRetentionDays: absent leaves it unchanged, null resets it to
		// the server default, a number sets it (0 = no trash).
		TrashRetentionDays nullableInt `json:"trash_retention_days"`
		ImmutableReleases  *bool       `json:"immutable_releases"`
		// MergeProxyVersions: groups only, administrators only.
		MergeProxyVersions *bool `json:"merge_proxy_versions"`
		SignPackages       *bool `json:"sign_packages"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}

	if body.SignPackages != nil {
		if *body.SignPackages && (repo.RepoType != "hosted" || repo.Format != "rpm") {
			writeError(w, http.StatusBadRequest, "validation_failed", errSignPackagesRPMOnly)
			return
		}

		repo.SignPackages = *body.SignPackages
	}

	if actor != nil && body.MergeProxyVersions != nil {
		writeError(w, http.StatusForbidden, "forbidden", errMergeProxyVersionsAdmin)
		return
	}

	if body.MergeProxyVersions != nil {
		if repo.RepoType != "group" {
			writeError(w, http.StatusBadRequest, "validation_failed", "merge_proxy_versions is only valid for group repositories")
			return
		}

		repo.MergeProxyVersions = *body.MergeProxyVersions
	}

	if actor != nil {
		if msg := h.delegatedUpdateViolation(ctx, repo, body.Blocked != nil, body.PresignDownloads != nil, body.Project != nil, body.Public); msg != "" {
			writeError(w, http.StatusForbidden, "forbidden", msg)
			return
		}
	}

	if body.TrashRetentionDays.Set {
		if err := validateTrashRetention(body.TrashRetentionDays.Value); err != nil {
			writeError(w, http.StatusBadRequest, "validation_failed", err.Error())
			return
		}

		repo.TrashRetentionDays = body.TrashRetentionDays.Value
	}

	if body.ImmutableReleases != nil {
		repo.ImmutableReleases = *body.ImmutableReleases
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

	credsRetarget := false

	if body.UpstreamURL != nil {
		if *body.UpstreamURL == "" {
			writeError(w, http.StatusBadRequest, "validation_failed", "upstream_url cannot be empty for a proxy repository")
			return
		}

		if err := validateUpstreamURL(*body.UpstreamURL); err != nil {
			writeError(w, http.StatusBadRequest, "validation_failed", err.Error())
			return
		}

		if !sameUpstreamOrigin(repo.UpstreamURL, *body.UpstreamURL) {
			credsRetarget = true
		}

		repo.UpstreamURL = *body.UpstreamURL
	}

	if body.UpstreamUsername != nil {
		if repo.UpstreamUsername != *body.UpstreamUsername {
			credsRetarget = true
		}

		repo.UpstreamUsername = *body.UpstreamUsername
	}

	// The stored password was set for the previous origin/username; sending it
	// to a new origin (or as another user) would hand it to whoever may edit
	// the upstream settings. It is dropped unless the request re-enters it.
	if credsRetarget && body.UpstreamPassword == nil {
		repo.UpstreamPasswordEnc = ""
	}

	if body.UpstreamPassword != nil {
		enc, err := encryptUpstreamPassword(*body.UpstreamPassword)
		if err != nil {
			if ve, ok := errors.AsType[validationError](err); ok {
				writeError(w, http.StatusBadRequest, "validation_failed", ve.Error())

				return
			}

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

	if body.Project != nil {
		project := h.resolveProject(ctx, w, *body.Project)
		if project == nil {
			return
		}

		if !h.moveToProject(ctx, w, repo, project.ID) {
			return
		}
	}

	var memberIDs []string

	if body.MemberRepos != nil {
		ids, status, code, msg := h.resolveGroupMembers(ctx, actor, repo.Name, repo.Format, *body.MemberRepos)
		if status != 0 {
			writeError(w, status, code, msg)
			return
		}

		memberIDs = ids
	}

	if body.MemberRepos != nil {
		err = h.db.UpdateRepositoryAndMembers(ctx, repo, memberIDs)
	} else {
		err = h.db.UpdateRepository(ctx, repo)
	}

	if err != nil {
		if database.IsUniqueViolation(err) {
			writeError(w, http.StatusConflict, "conflict", "a repository with this name already exists")
			return
		}

		h.log.Errorw("update repository: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	h.metadataChanged(repo)

	writeJSON(w, http.StatusOK, h.repositoryResponse(ctx, repo, map[string]string{}))
}

// moveToProject reassigns repo to the project (admin PATCH with "project").
// The move goes through MoveRepositoryToProject so the repository's usage
// follows it to the target project's quota; it writes the error response and
// returns false on failure. The caller's own update then re-writes the same
// project_id.
func (h *RepositoriesHandler) moveToProject(ctx context.Context, w http.ResponseWriter, repo *models.Repository, projectID string) bool {
	if repo.ProjectID != nil && *repo.ProjectID == projectID {
		return true
	}

	if err := h.db.MoveRepositoryToProject(ctx, repo.ID, projectID, 0); err != nil {
		switch {
		case errors.Is(err, database.ErrProjectQuotaExceeded):
			writeError(w, http.StatusConflict, "project_quota_exceeded", "the target project's storage quota cannot take this repository's usage")
		case errors.Is(err, database.ErrRepositoryNotFound):
			writeError(w, http.StatusNotFound, "not_found", "repository not found")
		default:
			h.log.Errorw("update repository: move to project failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "database error")
		}

		return false
	}

	repo.ProjectID = &projectID

	return true
}

func (h *RepositoriesHandler) handleDelete(w http.ResponseWriter, r *http.Request) {
	name := urlparams.Vars(r)["name"]
	ctx := r.Context()

	if !requireRepoAdmin(w, r, h.evaluator, name) {
		return
	}

	// Deleting a repository is the one irreversible-looking click: the caller
	// must type the name back (query parameter or JSON body).
	if deleteConfirmation(r) != name {
		writeError(w, http.StatusBadRequest, "confirmation_required",
			"repository deletion must be confirmed: pass confirm="+name+" (query parameter or JSON body)")

		return
	}

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

	// A hard delete cascades the membership rows away: note the groups first.
	groups := h.metadataGroupsOf(ctx, repo)

	soft, err := h.softDeleteRepository(ctx, repo)
	if err != nil {
		if errors.Is(err, database.ErrTrashNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "repository not found")
			return
		}

		h.log.Errorw("delete repository: move to trash", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if !soft && !h.hardDeleteRepository(ctx, w, repo) {
		return
	}

	for _, id := range groups {
		h.groupMeta.NotifyStale(id)
	}

	webhook.Emit(ctx, h.publisher, repo.ID, webhook.EventRepoDeleted,
		webhook.RepositoryPayload(webhook.EventRepoDeleted, repo.Name, repo.Format, repo.RepoType))

	if repo.ProjectID != nil && *repo.ProjectID != models.SystemProjectID {
		h.emitProjectRepoEvent(r, audit.ActionProjectRepositoryDeleted, *repo.ProjectID, nil, "repository "+repo.Name+" deleted")
	}

	if !soft {
		// The delete cascaded the repository's package rows away; reclaim the
		// blobs only it referenced. A trashed repository keeps its contents
		// (and blobs) until it is purged.
		h.collectOrphansAsync()
	}

	writeNoContent(w)
}

// deleteConfirmation returns the confirmation of a repository delete: the
// `confirm` query parameter, else the `confirm` field of a JSON body.
func deleteConfirmation(r *http.Request) string {
	if c := r.URL.Query().Get("confirm"); c != "" {
		return c
	}

	if r.Body == nil || r.Body == http.NoBody {
		return ""
	}

	var body struct {
		Confirm string `json:"confirm"`
	}

	if err := decodeJSON(r, &body); err != nil {
		return ""
	}

	return body.Confirm
}

// metadataGroupsOf returns the deb/rpm groups whose merged metadata lists repo.
func (h *RepositoriesHandler) metadataGroupsOf(ctx context.Context, repo *models.Repository) []string {
	if h.groupMeta == nil || repo.RepoType == repotype.Group || (repo.Format != "deb" && repo.Format != "rpm") {
		return nil
	}

	ids, err := h.db.ListMetadataGroupsOfMember(ctx, repo.ID)
	if err != nil {
		h.log.Warnw("delete repository: listing groups of member failed", "repository", repo.Name, "error", err)
		return nil
	}

	return ids
}

// softDeleteRepository moves the repository to the trash when its retention
// allows it. false means the caller must hard-delete: retention 0, or a
// database without trash support.
func (h *RepositoriesHandler) softDeleteRepository(ctx context.Context, repo *models.Repository) (bool, error) {
	store, ok := h.db.(database.TrashStore)
	if !ok {
		return false, nil
	}

	soft, err := store.TrashRepository(ctx, repo.ID, trash.Actor(ctx))
	if err != nil {
		return false, fmt.Errorf("trash repository: %w", err)
	}

	return soft, nil
}

// hardDeleteRepository permanently deletes the repository (cascading its
// contents). It writes the error response and returns false on failure.
func (h *RepositoriesHandler) hardDeleteRepository(ctx context.Context, w http.ResponseWriter, repo *models.Repository) bool {
	// The generated-metadata blobs are only reachable through the rows the
	// cascade is about to remove, so note them first.
	var metadataSHAs []string

	if metas, err := h.db.GetRepoMetadataByRepo(ctx, repo.ID); err != nil {
		h.log.Warnw("delete repository: listing metadata blobs failed; they will leak", "repository", repo.Name, "error", err)
	} else {
		for _, m := range metas {
			metadataSHAs = append(metadataSHAs, m.BlobSHA256)
		}
	}

	if err := h.db.DeleteRepository(ctx, repo.ID); err != nil {
		if database.IsForeignKeyViolation(err) {
			writeError(w, http.StatusConflict, "conflict", "repository is still referenced (for example by a release bundle or promotion)")
			return false
		}

		h.log.Errorw("delete repository: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return false
	}

	reclaimMetadataBlobs(context.WithoutCancel(ctx), h.db, h.blobStore, h.log, repo.Name, metadataSHAs)

	return true
}

// reclaimMetadataBlobs frees generated-metadata blobs (no blobs row, so the
// orphan collector cannot find them) of a repository that was just deleted.
// Best effort: a failure only leaves unreferenced space behind.
func reclaimMetadataBlobs(ctx context.Context, db database.Database, store storage.BlobStore, log *logger.Logger, repo string, shas []string) {
	if store == nil {
		return
	}

	for _, sha := range slices.Compact(slices.Sorted(slices.Values(shas))) {
		if err := storage.ReclaimMetadataBlob(ctx, db, store, sha); err != nil {
			log.Warnw("delete repository: reclaiming metadata blob failed", "repository", repo, "sha256", sha, "error", err)
		}
	}
}

// trashedNameLookup is implemented by *database.PostgresDB; fakes need not.
type trashedNameLookup interface {
	TrashedRepositoryByName(ctx context.Context, name string) (*models.TrashItem, error)
}

// writeNameConflict answers a unique violation on a repository name: 409, with
// a clearer message when the name is held by a repository in the trash.
func (h *RepositoriesHandler) writeNameConflict(ctx context.Context, w http.ResponseWriter, name string) {
	if lookup, ok := h.db.(trashedNameLookup); ok {
		item, err := lookup.TrashedRepositoryByName(ctx, name)

		switch {
		case err == nil && item != nil:
			writeError(w, http.StatusConflict, "name_reserved",
				"the name is reserved by a repository in the trash until it is purged or restored")

			return
		case err != nil && !errors.Is(err, database.ErrTrashNotFound):
			h.log.Warnw("create repository: look up trashed repository", "error", err)
		}
	}

	writeError(w, http.StatusConflict, "conflict", "a repository with this name already exists")
}

// maxOrphanPasses bounds how many batches one repository delete reclaims; the
// cleanup worker's periodic pass picks up whatever is left.
const maxOrphanPasses = 100

// collectOrphansAsync reclaims blobs left unreferenced by a repository delete.
// It runs detached from the request (the client must not wait for storage
// deletes) and is best-effort: failures are logged and retried by the cleanup
// worker's periodic pass.
func (h *RepositoriesHandler) collectOrphansAsync() {
	collectOrphansAsync(&h.gc, h.db, h.blobStore, h.log)
}

// collectOrphansAsync starts the detached orphan-blob sweep on wg (shared by
// repository delete and the trash purge). A nil store is a no-op.
func collectOrphansAsync(wg *sync.WaitGroup, db database.Database, store storage.BlobStore, log *logger.Logger) {
	if store == nil {
		return
	}

	ctx := context.WithoutCancel(context.Background())

	wg.Go(func() {
		for range maxOrphanPasses {
			removed, err := storage.CollectOrphanBlobs(ctx, db, store, storage.OrphanMinAge, storage.OrphanBatch)
			if err != nil {
				log.Warnw("orphan blob collection after repository delete failed", "removed", removed, "error", err)
				return
			}

			if removed < storage.OrphanBatch {
				return
			}
		}
	})
}

var errListFilterUnsupported = errors.New("package_name, q and sort are not supported by this store")

// listComponents lists a repository's packages with the filters and ordering
// of opts. The plain newest-first listing needs only the base store; any
// filter or other order needs a database.ComponentSearcher.
func (h *RepositoriesHandler) listComponents(ctx context.Context, repoID string, opts database.ComponentListOptions) ([]*models.Component, int, error) {
	if opts.Name == "" && opts.Query == "" && !opts.SortByName {
		return h.db.ListComponentsByRepo(ctx, repoID, opts.Format, opts.Page, opts.Limit)
	}

	searcher, ok := h.db.(database.ComponentSearcher)
	if !ok {
		return nil, 0, errListFilterUnsupported
	}

	return searcher.SearchComponents(ctx, repoID, opts)
}

// maxPackageQueryLen bounds the q search term of the package listing.
const maxPackageQueryLen = 200

func (h *RepositoriesHandler) handlePackages(w http.ResponseWriter, r *http.Request) {
	name := urlparams.Vars(r)["name"]
	q := r.URL.Query()
	formatFilter := q.Get("format")
	page := min(parseIntMin(q.Get("page"), 1, 1), maxListPage)
	limit := parseIntClamp(q.Get("limit"), 25, 1, 100)

	search := strings.TrimSpace(q.Get("q"))
	if len(search) > maxPackageQueryLen {
		writeError(w, http.StatusBadRequest, "bad_request", "q is too long")
		return
	}

	sortKey := q.Get("sort")
	if sortKey != "" && sortKey != "created" && sortKey != "name" {
		writeError(w, http.StatusBadRequest, "bad_request", "sort must be created or name")
		return
	}

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

	components, total, err := h.listComponents(ctx, repo.ID, database.ComponentListOptions{
		Format:     formatFilter,
		Name:       q.Get("package_name"),
		Query:      search,
		SortByName: sortKey == "name",
		Page:       page,
		Limit:      limit,
	})
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

	if _, err := iocopy.Copy(w, reader); err != nil && !errors.Is(err, context.Canceled) {
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

	if !pathseg.Name(name) {
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

	memberIDs, status, code, msg := h.resolveGroupMembers(ctx, nil, repo.Name, repo.Format, body.MemberRepos)
	if status != 0 {
		writeError(w, status, code, msg)
		return
	}

	if err := h.db.ReplaceGroupMembers(ctx, repo.ID, memberIDs); err != nil {
		h.log.Errorw("update members: replace group members", "group_id", repo.ID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	h.metadataChanged(repo)

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

		PresignDownloads:   r.PresignDownloads,
		TrashRetentionDays: r.TrashRetentionDays,
		ImmutableReleases:  r.ImmutableReleases,
		MergeProxyVersions: r.MergeProxyVersions,
		SignPackages:       r.SignPackages,
		Federated:          r.Federated,
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

var errTrashRetentionRange = errors.New("trash_retention_days must be between 0 and 36500 (or null for the server default)")

// maxTrashRetentionDays bounds trash_retention_days (100 years).
const maxTrashRetentionDays = 36500

// validateTrashRetention accepts nil (server default) or 0..maxTrashRetentionDays.
func validateTrashRetention(days *int) error {
	if days != nil && (*days < 0 || *days > maxTrashRetentionDays) {
		return errTrashRetentionRange
	}

	return nil
}

// nullableInt tells an absent JSON field (Set false) from an explicit null
// (Set true, Value nil) and a number.
type nullableInt struct {
	Set   bool
	Value *int
}

// UnmarshalJSON implements json.Unmarshaler. encoding/json calls it for an
// explicit null too, which is what makes the tri-state work.
func (n *nullableInt) UnmarshalJSON(b []byte) error {
	n.Set = true

	if string(bytes.TrimSpace(b)) == "null" {
		n.Value = nil
		return nil
	}

	var v int
	if err := json.Unmarshal(b, &v); err != nil {
		return fmt.Errorf("decode integer: %w", err)
	}

	n.Value = &v

	return nil
}

func parseIntMin(s string, defaultVal, minVal int) int { //nolint:unparam // every list endpoint defaults to page 1, kept general
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
