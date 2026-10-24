// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package helm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	"github.com/openartifact/openartifact/internal/importmeta"
	"github.com/openartifact/openartifact/internal/iocopy"
	"github.com/openartifact/openartifact/internal/licensepolicy"
	"github.com/openartifact/openartifact/internal/presign"
	"github.com/openartifact/openartifact/internal/quarantine"
	"github.com/openartifact/openartifact/internal/quota"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/trash"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/internal/webhook"
	pkghelm "github.com/openartifact/openartifact/pkg/helm"
	"github.com/openartifact/openartifact/pkg/logger"
	"go.yaml.in/yaml/v3"
	"golang.org/x/mod/semver"
)

const maxHelmUploadBytes = 100 << 20

type HTTPHandler struct {
	*base.Handler
}

func NewHTTPHandler(log *logger.Logger, db database.Database, blobStore storage.BlobStore, baseURL string) *HTTPHandler {
	return &HTTPHandler{Handler: base.New(log, db, blobStore, baseURL)}
}

func (h *HTTPHandler) RegisterRepoRoutes(r chi.Router) {
	r.Get("/index.yaml", h.handleGetIndex)
	r.Get("/charts/{filename}", h.handleDownloadChart)
	r.Post("/charts", h.handleUploadChart)
	r.Delete("/charts/{filename}", h.handleDeleteChart)
	r.Get("/charts", h.handleListCharts)
}

func (h *HTTPHandler) RegisterRoutes(r chi.Router, prefix string) {
	r.Route(prefix, func(r chi.Router) {
		h.RegisterRepoRoutes(r)
	})
	h.Log.Infow("helm handler registered", "prefix", prefix)
}

// indexMaxAge is how long shared caches may reuse a public repository's
// index.yaml before revalidating.
const indexMaxAge = 60

// indexETag derives the validator of index.yaml from the chart-set stamp
// (HelmIndexStamp) instead of the rendered body, which embeds a generation
// timestamp and so differs on every request. Chart rows are immutable, so the
// stamp changes exactly when a chart is added or removed; the repository and
// base URL shape the chart URLs.
func indexETag(repoID, repoName, baseURL, stamp string) string {
	sum := sha256.Sum256([]byte(repoID + "\x00" + repoName + "\x00" + baseURL + "\x00" + stamp))

	return `"` + hex.EncodeToString(sum[:16]) + `"`
}

func (h *HTTPHandler) handleGetIndex(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	repoName := vars["name"]

	repo, err := h.ResolveRepo(r.Context(), repoName)
	if err != nil {
		h.Log.Errorw("failed to resolve repo", "name", repoName, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if repo == nil {
		http.NotFound(w, r)
		return
	}

	stamp, err := h.DB.HelmIndexStamp(r.Context(), repo.ID)
	if err != nil {
		h.Log.Errorw("failed to stamp helm index", "repo", repoName, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	// Revalidation is answered from the stamp alone: no chart is loaded and
	// nothing is rendered.
	etag := indexETag(repo.ID, repoName, h.BaseURL, stamp)
	if base.Revalidate(w, r, repo.Public, etag, indexMaxAge) {
		return
	}

	// The rendered index is cached under the stamp-derived ETag, so a changed
	// chart set can never be served from an old rendering, and the charts are
	// loaded and marshalled once per change instead of once per request.
	rendered, err := h.Rendered(r.Context(), etag, func(ctx context.Context) (*base.Rendered, error) {
		return h.renderIndex(ctx, repo.ID, repoName)
	})
	if err != nil {
		h.Log.Errorw("failed to render index.yaml", "repo", repoName, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	rendered.Serve(w, r)
}

// renderIndex loads the live charts of a repository and renders index.yaml.
func (h *HTTPHandler) renderIndex(ctx context.Context, repoID, repoName string) (*base.Rendered, error) {
	charts, err := h.DB.ListHelmCharts(ctx, repoID)
	if err != nil {
		return nil, fmt.Errorf("list helm charts: %w", err)
	}

	indexYAML, err := yaml.Marshal(buildHelmIndex(charts, repoName, h.BaseURL))
	if err != nil {
		return nil, fmt.Errorf("marshal index.yaml: %w", err)
	}

	return &base.Rendered{Body: indexYAML, ContentType: "application/yaml"}, nil
}

func (h *HTTPHandler) handleDownloadChart(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	repoName := vars["name"]

	filename, ok := chartFilename(r)
	if !ok {
		http.NotFound(w, r)
		return
	}

	repoID, err := h.ResolveRepoID(r.Context(), repoName)
	if err != nil {
		h.Log.Errorw("failed to resolve repo", "name", repoName, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if repoID == "" {
		http.NotFound(w, r)
		return
	}

	chart, err := h.findChart(r.Context(), repoID, filename)
	if err != nil {
		h.Log.Errorw("failed to get helm chart", "filename", filename, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if chart == nil {
		http.NotFound(w, r)
		return
	}

	if h.DenyIfQuarantined(w, r, repoID, quarantine.PathKeys(chart.ID, "charts/"+filename, filename, r.URL.Path)...) {
		return
	}

	if h.TryPresign(w, r, presign.Blob{SHA256: chart.BlobKey, Size: chart.SizeBytes, Filename: filename, ContentType: "application/gzip"}) {
		return
	}

	rc, err := h.BlobStore.Get(r.Context(), chart.BlobKey)
	if err != nil {
		h.Log.Errorw("failed to get chart blob", "key", chart.BlobKey, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}
	defer func() { _ = rc.Close() }()

	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Length", strconv.FormatInt(chart.SizeBytes, 10))
	w.Header().Set("ETag", `"`+chart.Digest+`"`)
	w.WriteHeader(http.StatusOK)

	if _, err := iocopy.Copy(w, rc); err != nil {
		h.Log.Errorw("failed to write chart response", "filename", filename, "error", err)
	}
}

//nolint:gocyclo,cyclop // protocol handler: branching mirrors spec state machine
func (h *HTTPHandler) handleUploadChart(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	repoName := vars["name"]

	repoID, err := h.ResolveRepoID(r.Context(), repoName)
	if err != nil {
		h.Log.Errorw("failed to resolve repo", "name", repoName, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if repoID == "" {
		http.NotFound(w, r)
		return
	}

	// Read the chart data
	spool, err := base.SpoolLimited(r.Body, maxHelmUploadBytes)
	if err != nil {
		if errors.Is(err, base.ErrContentTooLarge) {
			http.Error(w, "chart too large", http.StatusRequestEntityTooLarge)

			return
		}
		h.Log.Errorw("failed to read chart data", "error", err)
		http.Error(w, "failed to read chart", http.StatusBadRequest)

		return
	}
	defer func() { _ = r.Body.Close() }()
	defer spool.Close()

	// Parse the chart to extract metadata
	metadata, err := pkghelm.ParseChart(spool.Reader())
	if err != nil {
		h.Log.Errorw("failed to parse chart", "error", err)
		http.Error(w, "invalid chart", http.StatusBadRequest)

		return
	}

	// Extract metadata fields
	chartName, ok := metadata["name"].(string)
	if !ok || chartName == "" {
		http.Error(w, "chart name not found", http.StatusBadRequest)
		return
	}

	version, ok := metadata["version"].(string)
	if !ok || version == "" {
		http.Error(w, "chart version not found", http.StatusBadRequest)
		return
	}

	if !validChartIdent(chartName) || !validChartIdent(version) {
		http.Error(w, "invalid chart name or version", http.StatusBadRequest)
		return
	}

	if !strictSemver(version) {
		http.Error(w, "chart version must be a semantic version (MAJOR.MINOR.PATCH)", http.StatusBadRequest)
		return
	}

	collides, err := h.filenameCollides(r.Context(), repoID, chartName, version)
	if err != nil {
		h.Log.Errorw("failed to check chart filename collision", "chart", chartName, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if collides {
		http.Error(w, "chart filename conflicts with an existing chart", http.StatusConflict)
		return
	}

	existing, err := h.DB.GetHelmChart(r.Context(), repoID, chartName, version)
	if err != nil {
		h.Log.Errorw("failed to check existing chart", "chart", chartName, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if existing != nil {
		http.Error(w, "chart version already exists", http.StatusConflict)
		return
	}

	// Chart.yaml has no license field; the only standard declaration is the
	// artifacthub.io/license annotation.
	annotations, _ := metadata["annotations"].(map[string]string)
	if licensepolicy.Enforce(w, r, h.DB, h.Log, repoID, annotations["artifacthub.io/license"]) {
		return
	}

	digest := spool.SHA256Hex()
	size := spool.Size()

	// Extract optional fields
	appVersion, _ := metadata["appVersion"].(string)
	description, _ := metadata["description"].(string)
	home, _ := metadata["home"].(string)
	icon, _ := metadata["icon"].(string)

	apiVersion, _ := metadata["apiVersion"].(string)
	if apiVersion == "" {
		apiVersion = "v2"
	}

	chartType, _ := metadata["type"].(string)
	if chartType == "" {
		chartType = "application"
	}

	// Convert keywords
	var keywords []string

	switch kw := metadata["keywords"].(type) {
	case []string:
		keywords = kw
	case []any:
		for _, k := range kw {
			if s, ok := k.(string); ok {
				keywords = append(keywords, s)
			}
		}
	}

	// Convert sources
	var sources []string

	switch src := metadata["sources"].(type) {
	case []string:
		sources = src
	case []any:
		for _, s := range src {
			if str, ok := s.(string); ok {
				sources = append(sources, str)
			}
		}
	}

	// Convert maintainers
	var maintainers []map[string]string

	var maintRaw []map[string]any

	switch maint := metadata["maintainers"].(type) {
	case []map[string]any:
		maintRaw = maint
	case []any:
		for _, m := range maint {
			if mm, ok := m.(map[string]any); ok {
				maintRaw = append(maintRaw, mm)
			}
		}
	}

	for _, maintMap := range maintRaw {
		strMap := make(map[string]string)

		for k, v := range maintMap {
			if s, ok := v.(string); ok {
				strMap[k] = s
			}
		}

		if len(strMap) > 0 {
			maintainers = append(maintainers, strMap)
		}
	}

	// Create chart record
	chart := &models.HelmChart{
		RepositoryID: repoID,
		Name:         chartName,
		Version:      version,
		AppVersion:   appVersion,
		Description:  description,
		Keywords:     keywords,
		Home:         home,
		Sources:      sources,
		Maintainers:  maintainers,
		Icon:         icon,
		APIVersion:   apiVersion,
		Type:         chartType,
		Digest:       digest,
		SizeBytes:    size,
	}

	// Reserve quota before anything is persisted; every failure path below
	// returns the reservation.
	if err := quota.Enforce(r.Context(), h.DB, repoID, size); err != nil {
		if errors.Is(err, quota.ErrExceeded) {
			quota.WriteExceeded(w)
			return
		}

		h.Log.Errorw("failed to reserve quota", "repo", repoName, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if status, msg := h.storeChart(r.Context(), chart, spool); status != 0 {
		if err := quota.Release(r.Context(), h.DB, repoID, size); err != nil {
			h.Log.Warnw("failed to release quota", "repo", repoName, "error", err)
		}

		http.Error(w, msg, status)

		return
	}

	filename := fmt.Sprintf("%s-%s.tgz", chart.Name, chart.Version)

	h.Emit(r.Context(), repoID, webhook.EventArtifactUploaded, webhook.ArtifactPayload(webhook.EventArtifactUploaded, webhook.Artifact{
		Repository: repoName,
		Format:     "helm",
		Name:       chart.Name,
		Version:    chart.Version,
		Path:       "charts/" + filename,
		SHA256:     digest,
		Size:       size,
	}))

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)

	if encErr := json.NewEncoder(w).Encode(map[string]any{
		"id":      chart.ID,
		"name":    chart.Name,
		"version": chart.Version,
	}); encErr != nil {
		_ = encErr // response header already written
	}
}

func (h *HTTPHandler) handleDeleteChart(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	repoName := vars["name"]

	filename, ok := chartFilename(r)
	if !ok {
		http.NotFound(w, r)
		return
	}

	repoID, err := h.ResolveRepoID(r.Context(), repoName)
	if err != nil {
		h.Log.Errorw("failed to resolve repo", "name", repoName, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if repoID == "" {
		http.NotFound(w, r)
		return
	}

	chart, err := h.findChart(r.Context(), repoID, filename)
	if err != nil {
		h.Log.Errorw("failed to get helm chart", "filename", filename, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if chart == nil {
		http.NotFound(w, r)
		return
	}

	// Resolve the size to release before the blob can go away.
	size := chart.SizeBytes
	if size <= 0 {
		if n, sizeErr := h.BlobStore.GetSize(r.Context(), chart.BlobKey); sizeErr == nil {
			size = n
		}
	}

	if gErr := trash.GuardReleaseByName(r, h.DB, repoName, "helm", chart.Version); gErr != nil {
		if errors.Is(gErr, trash.ErrImmutableRelease) {
			http.Error(w, "repository has immutable releases", http.StatusConflict)
			return
		}

		h.Log.Errorw("failed to check release immutability", "chart", chart.Name, "error", gErr)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	err = trash.Delete(r.Context(), h.DB, trash.Request{
		RepoID:   repoID,
		Kind:     models.TrashKindHelm,
		IDs:      []string{chart.ID},
		Resource: "charts/" + filename,
	}, func() error {
		// Delete the record first, then the blob only if nothing else references it.
		if delErr := h.DB.DeleteHelmChart(r.Context(), repoID, chart.Name, chart.Version); delErr != nil {
			return fmt.Errorf("delete helm chart record: %w", delErr)
		}

		if relErr := quota.Release(r.Context(), h.DB, repoID, size); relErr != nil {
			h.Log.Warnw("failed to release quota", "repo", repoName, "error", relErr)
		}

		if blobErr := storage.SafeDeleteBlob(r.Context(), h.DB, h.BlobStore, chart.Digest); blobErr != nil {
			h.Log.Warnw("failed to delete chart blob", "sha256", chart.Digest, "error", blobErr)
		}

		return nil
	})
	if err != nil {
		if errors.Is(err, database.ErrTrashNotFound) {
			http.NotFound(w, r)
			return
		}

		h.Log.Errorw("failed to delete helm chart record", "chart", chart.Name, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	h.Emit(r.Context(), repoID, webhook.EventArtifactDeleted, webhook.ArtifactPayload(webhook.EventArtifactDeleted, webhook.Artifact{
		Repository: repoName,
		Format:     "helm",
		Name:       chart.Name,
		Version:    chart.Version,
		Path:       "charts/" + filename,
		SHA256:     chart.Digest,
		Size:       size,
	}))

	w.WriteHeader(http.StatusOK)
}

func (h *HTTPHandler) handleListCharts(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	repoName := vars["name"]

	repoID, err := h.ResolveRepoID(r.Context(), repoName)
	if err != nil {
		h.Log.Errorw("failed to resolve repo", "name", repoName, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if repoID == "" {
		http.NotFound(w, r)
		return
	}

	charts, err := h.DB.ListHelmCharts(r.Context(), repoID)
	if err != nil {
		h.Log.Errorw("failed to list helm charts", "repo", repoName, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	// Convert to JSON-friendly format
	var result []map[string]any
	for _, chart := range charts {
		result = append(result, map[string]any{
			"id":          chart.ID,
			"name":        chart.Name,
			"version":     chart.Version,
			"appVersion":  chart.AppVersion,
			"description": chart.Description,
			"digest":      chart.Digest,
			"size":        chart.SizeBytes,
			"createdAt":   chart.CreatedAt,
			"updatedAt":   chart.UpdatedAt,
		})
	}

	if result == nil {
		result = []map[string]any{}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	if encErr := json.NewEncoder(w).Encode(result); encErr != nil {
		_ = encErr // response header already written
	}
}

// Helper functions

// storeChart stores the chart blob first, then re-checks its existence and
// inserts the row under the per-blob lock (DB work only) so a concurrent delete
// cannot remove the blob before the row exists. It returns a zero status on
// success, otherwise the HTTP status and message to report; the caller releases
// the quota reservation.
func (h *HTTPHandler) storeChart(ctx context.Context, chart *models.HelmChart, body *base.Spool) (int, string) {
	status, msg := 0, ""

	blob := storage.SeekerBlob(chart.Digest, body.Reader(), map[string]string{
		"sha256":   chart.Digest,
		"filename": fmt.Sprintf("%s-%s.tgz", chart.Name, chart.Version),
		"chart":    chart.Name,
		"version":  chart.Version,
	})

	lockErr := storage.CommitBlob(ctx, h.DB, h.BlobStore, blob, func(ctx context.Context, blobKey string) error {
		chart.BlobKey = blobKey

		chart.CreatedAt = importmeta.CreatedAt(ctx)

		if err := h.DB.CreateHelmChart(ctx, chart); err != nil {
			if errors.Is(err, database.ErrDuplicate) || database.IsUniqueViolation(err) {
				status, msg = http.StatusConflict, "chart version already exists"

				return err
			}

			h.Log.Errorw("failed to create helm chart record", "chart", chart.Name, "error", err)

			status, msg = http.StatusInternalServerError, "internal error"

			return err
		}

		return nil
	})
	if lockErr == nil {
		return 0, ""
	}

	if status == 0 {
		storeErr, isStore := errors.AsType[*storage.StoreError](lockErr)

		switch {
		case isStore && storeErr.Op == "check":
			h.Log.Errorw("failed to check chart blob", "error", lockErr)
		case isStore:
			h.Log.Errorw("failed to store chart blob", "error", lockErr)
		default:
			h.Log.Errorw("chart blob lock failed", "chart", chart.Name, "error", lockErr)
		}

		status, msg = http.StatusInternalServerError, "internal error"
	}

	// The row was not created: drop the blob if nothing else references it.
	if err := storage.SafeDeleteBlob(ctx, h.DB, h.BlobStore, chart.Digest); err != nil {
		h.Log.Warnw("failed to clean up chart blob", "sha256", chart.Digest, "error", err)
	}

	return status, msg
}

// chartFilename returns the chart file name of a charts/{filename} request.
// The index lists the URL of the chart "a;b" as ".../a%3Bb-1.0.0.tgz", and chi
// hands out the {filename} parameter encoded or decoded depending on whether
// the request's escaping is the one Go would produce itself, so the parameter
// cannot be used as is: the last segment of the escaped path is decoded here.
func chartFilename(r *http.Request) (string, bool) {
	esc := r.URL.EscapedPath()

	filename, err := url.PathUnescape(esc[strings.LastIndexByte(esc, '/')+1:])

	return filename, err == nil && filename != "" && !strings.ContainsAny(filename, "/\\")
}

// findChart resolves "{name}-{version}.tgz". Both chart names and versions may
// contain hyphens (mychart-1.0.0-rc1.tgz), so every hyphen followed by a digit
// (or 'v' + digit) is tried as the name/version split point.
func (h *HTTPHandler) findChart(ctx context.Context, repoID, filename string) (*models.HelmChart, error) {
	var found *models.HelmChart

	err := h.eachSplit(ctx, repoID, filename, func(c *models.HelmChart) bool {
		found = c
		return true
	})

	return found, err
}

// filenameCollides reports whether "{name}-{version}.tgz" would also resolve to
// a different stored chart, which would make downloads of that file ambiguous.
func (h *HTTPHandler) filenameCollides(ctx context.Context, repoID, name, version string) (bool, error) {
	collides := false

	err := h.eachSplit(ctx, repoID, name+"-"+version+".tgz", func(c *models.HelmChart) bool {
		if c.Name == name && c.Version == version {
			return false
		}

		collides = true

		return true
	})

	return collides, err
}

// eachSplit calls fn for every stored chart matching a name/version split of
// filename until fn returns true.
func (h *HTTPHandler) eachSplit(ctx context.Context, repoID, filename string, fn func(*models.HelmChart) bool) error {
	base, ok := strings.CutSuffix(filename, ".tgz")
	if !ok {
		return nil
	}

	for i := 1; i < len(base)-1; i++ {
		if base[i] != '-' || !startsVersion(base[i+1:]) {
			continue
		}

		chart, err := h.DB.GetHelmChart(ctx, repoID, base[:i], base[i+1:])
		if err != nil {
			return err
		}

		if chart != nil && fn(chart) {
			return nil
		}
	}

	return nil
}

// strictSemver reports whether v is a full MAJOR.MINOR.PATCH[-pre][+build]
// semantic version with no "v" prefix or shortened forms.
func strictSemver(v string) bool {
	core, _, _ := strings.Cut(v, "+")
	sv := "v" + core

	return semver.IsValid(sv) && semver.Canonical(sv) == sv
}

// startsVersion reports whether s begins like a chart version: a digit, or 'v'
// followed by a digit.
func startsVersion(s string) bool {
	if s != "" && s[0] == 'v' {
		s = s[1:]
	}

	return s != "" && s[0] >= '0' && s[0] <= '9'
}

// validChartIdent rejects empty values and anything that could escape a
// filename or URL path segment.
func validChartIdent(s string) bool {
	if s == "" || len(s) > 255 {
		return false
	}

	for _, r := range s {
		if r == '/' || r == '\\' || r <= ' ' || r == 0x7f {
			return false
		}
	}

	return s != "." && s != ".."
}

// HelmIndex represents the Helm repository index.yaml structure.
type HelmIndex struct {
	APIVersion string                      `yaml:"apiVersion"`
	Generated  string                      `yaml:"generated"`
	Entries    map[string][]HelmChartEntry `yaml:"entries"`
}

type HelmChartEntry struct {
	Name        string              `yaml:"name"`
	Version     string              `yaml:"version"`
	AppVersion  string              `yaml:"appVersion,omitempty"`
	Description string              `yaml:"description,omitempty"`
	Digest      string              `yaml:"digest"`
	URLs        []string            `yaml:"urls"`
	Created     string              `yaml:"created"`
	Home        string              `yaml:"home,omitempty"`
	Sources     []string            `yaml:"sources,omitempty"`
	Maintainers []map[string]string `yaml:"maintainers,omitempty"`
	Icon        string              `yaml:"icon,omitempty"`
	APIVersion  string              `yaml:"apiVersion,omitempty"`
	Type        string              `yaml:"type,omitempty"`
	Keywords    []string            `yaml:"keywords,omitempty"`
}

func buildHelmIndex(charts []*models.HelmChart, repoName, baseURL string) *HelmIndex {
	entries := make(map[string][]HelmChartEntry)

	for _, chart := range charts {
		entry := HelmChartEntry{
			Name:        chart.Name,
			Version:     chart.Version,
			AppVersion:  chart.AppVersion,
			Description: chart.Description,
			Digest:      chart.Digest,
			URLs: []string{
				fmt.Sprintf("%s/repos/%s/charts/%s.tgz", baseURL, url.PathEscape(repoName), url.PathEscape(chart.Name+"-"+chart.Version)),
			},
			Created:     chart.CreatedAt.UTC().Format(time.RFC3339),
			Home:        chart.Home,
			Sources:     chart.Sources,
			Maintainers: chart.Maintainers,
			Icon:        chart.Icon,
			APIVersion:  chart.APIVersion,
			Type:        chart.Type,
			Keywords:    chart.Keywords,
		}

		entries[chart.Name] = append(entries[chart.Name], entry)
	}

	return &HelmIndex{
		APIVersion: "v1",
		Generated:  time.Now().UTC().Format(time.RFC3339),
		Entries:    entries,
	}
}
