// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package rubygems

import (
	"bytes"
	"cmp"
	"compress/gzip"
	"compress/zlib"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

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
	"github.com/openartifact/openartifact/pkg/logger"
	"github.com/openartifact/openartifact/pkg/rubymarshal"
)

type HTTPHandler struct {
	*base.Handler
}

func NewHTTPHandler(log *logger.Logger, db database.Database, blobStore storage.BlobStore, baseURL string) *HTTPHandler {
	return &HTTPHandler{Handler: base.New(log, db, blobStore, baseURL)}
}

// RegisterRepoRoutes registers RubyGems-specific routes on the provided sub-router.
// It does NOT wrap in r.Route() — callers are responsible for the path prefix.
func (h *HTTPHandler) RegisterRepoRoutes(r chi.Router) {
	r.Get("/specs.4.8.gz", h.handleSpecsGz)
	r.Get("/latest_specs.4.8.gz", h.handleLatestSpecsGz)
	r.Get("/prerelease_specs.4.8.gz", h.handlePrereleaseSpecsGz)
	r.Get("/quick/Marshal.4.8/{filename}", h.handleGemspec)
	r.Get("/gems/{filename}", h.handleGemDownload)
	r.Post("/api/v1/gems", h.handleUploadGem)
	r.Delete("/api/v1/gems/yank", h.handleYankGem)
	r.Get("/api/v1/gems/{gem}.json", h.handleGemInfo)
	r.Get("/api/v1/search.json", h.handleSearch)
}

func (h *HTTPHandler) RegisterRoutes(r chi.Router, prefix string) {
	r.Route(prefix, func(r chi.Router) {
		h.RegisterRepoRoutes(r)
	})
	h.Log.Infow("rubygems handler registered", "prefix", prefix)
}

// specsMaxAge is how long shared caches may reuse a public repository's specs
// indexes before revalidating.
const specsMaxAge = 60

// specsKind selects which listed gems a specs index carries.
type specsKind int

const (
	specsReleased   specsKind = iota // specs.4.8.gz: every released version
	specsLatest                      // latest_specs.4.8.gz: the newest released version per name and platform
	specsPrerelease                  // prerelease_specs.4.8.gz: prereleases only
)

// handleSpecsGz returns all released gem specs as gzipped Marshal data.
func (h *HTTPHandler) handleSpecsGz(w http.ResponseWriter, r *http.Request) {
	h.serveSpecs(w, r, specsReleased)
}

// handleLatestSpecsGz returns only latest versions as gzipped Marshal data.
func (h *HTTPHandler) handleLatestSpecsGz(w http.ResponseWriter, r *http.Request) {
	h.serveSpecs(w, r, specsLatest)
}

// handlePrereleaseSpecsGz returns prerelease gems as gzipped Marshal data.
func (h *HTTPHandler) handlePrereleaseSpecsGz(w http.ResponseWriter, r *http.Request) {
	h.serveSpecs(w, r, specsPrerelease)
}

// serveSpecs answers one of the specs indexes. They are derived from the set of
// listed (not yanked) gems alone, so a stamp of that set is the validator and
// the cache key: an unchanged repository costs one cheap aggregate query, and
// the marshalled, gzipped index is built once per change instead of once per
// request.
func (h *HTTPHandler) serveSpecs(w http.ResponseWriter, r *http.Request, kind specsKind) {
	repoName := urlparams.Vars(r)["name"]

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

	stamp, err := h.DB.RubyGemsSpecsStamp(r.Context(), repo.ID)
	if err != nil {
		h.Log.Errorw("failed to stamp gem specs", "repo", repoName, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	sum := sha256.Sum256([]byte(repo.ID + "\x00" + strconv.Itoa(int(kind)) + "\x00" + stamp))

	etag := `"` + hex.EncodeToString(sum[:16]) + `"`
	if base.Revalidate(w, r, repo.Public, etag, specsMaxAge) {
		return
	}

	rendered, err := h.Rendered(r.Context(), etag, func(ctx context.Context) (*base.Rendered, error) {
		return h.renderSpecs(ctx, repo.ID, kind)
	})
	if err != nil {
		h.Log.Errorw("failed to render gem specs", "repo", repoName, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	rendered.Serve(w, r)
}

func (h *HTTPHandler) renderSpecs(ctx context.Context, repoID string, kind specsKind) (*base.Rendered, error) {
	gems, err := h.DB.ListRubyGemSpecs(ctx, repoID)
	if err != nil {
		return nil, fmt.Errorf("list gem specs: %w", err)
	}

	data, err := marshalSpecsList(selectSpecs(gems, kind))
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer

	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(data); err != nil {
		return nil, fmt.Errorf("gzip gem specs: %w", err)
	}

	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("gzip gem specs: %w", err)
	}

	return &base.Rendered{Body: buf.Bytes(), ContentType: "application/octet-stream", Opaque: true}, nil
}

// selectSpecs filters the listed gems for one specs index. Yanked gems are
// listed nowhere; specs.4.8 lists released versions only, prereleases live in
// prerelease_specs.4.8; latest_specs.4.8 keeps the highest released version per
// name and platform, ordered by name then platform so equal data always
// produces the same bytes.
func selectSpecs(gems []*models.RubyGem, kind specsKind) []*models.RubyGem {
	switch kind {
	case specsPrerelease:
		return slices.DeleteFunc(slices.Clone(gems), func(g *models.RubyGem) bool { return g.Yanked || !isPrerelease(g.Version) })
	case specsLatest:
		latest := make(map[string]*models.RubyGem)

		for _, g := range gems {
			if g.Yanked || isPrerelease(g.Version) {
				continue
			}

			key := g.Name + ":" + g.Platform
			if existing, ok := latest[key]; !ok || compareVersions(g.Version, existing.Version) > 0 {
				latest[key] = g
			}
		}

		out := slices.SortedFunc(maps.Values(latest), func(a, b *models.RubyGem) int {
			return cmp.Or(strings.Compare(a.Name, b.Name), strings.Compare(a.Platform, b.Platform))
		})

		return out
	default:
		return slices.DeleteFunc(slices.Clone(gems), func(g *models.RubyGem) bool { return g.Yanked || isPrerelease(g.Version) })
	}
}

// handleGemspec returns a zlib-compressed gemspec.
const maxGemUploadBytes = 200 << 20

func (h *HTTPHandler) handleGemspec(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	repoName := vars["name"]
	filename := vars["filename"]

	// Parse filename to extract gemname and version
	// Format: gemname-version.gemspec.rz
	// Strip the .gemspec.rz suffix
	const suffix = ".gemspec.rz"
	if !strings.HasSuffix(filename, suffix) {
		http.NotFound(w, r)
		return
	}

	nameVersion := strings.TrimSuffix(filename, suffix)

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

	gem, err := h.findGem(r.Context(), repoID, nameVersion)
	if errors.Is(err, errAmbiguousGemFile) {
		http.Error(w, "gem file name is ambiguous", http.StatusConflict)
		return
	}

	if err != nil {
		h.Log.Errorw("failed to get gem", "file", nameVersion, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if gem == nil || gem.Yanked {
		http.NotFound(w, r)
		return
	}

	spec := rubymarshal.Specification{
		Name:         gem.Name,
		Version:      gem.Version,
		Platform:     gem.Platform,
		Summary:      gem.Summary,
		Description:  gem.Description,
		Homepage:     gem.Homepage,
		Authors:      gem.Authors,
		Licenses:     gem.Licenses,
		Dependencies: decodeGemDependencies(gem.Dependencies),
	}

	data, err := rubymarshal.EncodeSpecification(spec)
	if err != nil {
		h.Log.Errorw("failed to encode gemspec", "name", gem.Name, "version", gem.Version, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")

	zw := zlib.NewWriter(w)
	defer func() { _ = zw.Close() }()

	_, _ = zw.Write(data)
}

// handleGemDownload serves the .gem file.
func (h *HTTPHandler) handleGemDownload(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	repoName := vars["name"]
	filename := vars["filename"]

	// Parse filename to extract gemname and version
	// Format: gemname-version.gem
	const suffix = ".gem"
	if !strings.HasSuffix(filename, suffix) {
		http.NotFound(w, r)
		return
	}

	nameVersion := strings.TrimSuffix(filename, suffix)

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

	gem, err := h.findGem(r.Context(), repoID, nameVersion)
	if errors.Is(err, errAmbiguousGemFile) {
		http.Error(w, "gem file name is ambiguous", http.StatusConflict)
		return
	}

	if err != nil {
		h.Log.Errorw("failed to get gem", "file", nameVersion, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if gem == nil || gem.Yanked {
		http.NotFound(w, r)
		return
	}

	if h.DenyIfQuarantined(w, r, repoID, quarantine.PathKeys(gem.ID, "gems/"+filename, filename, r.URL.Path)...) {
		return
	}

	if h.TryPresign(w, r, presign.Blob{SHA256: gem.BlobKey, Size: gem.Size, Filename: nameVersion + ".gem", ContentType: "application/octet-stream"}) {
		return
	}

	// Retrieve blob from storage
	blob, err := h.BlobStore.Get(r.Context(), gem.BlobKey)
	if err != nil {
		h.Log.Errorw("failed to get blob", "key", gem.BlobKey, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}
	defer func() { _ = blob.Close() }()

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": nameVersion + ".gem"}))
	_, _ = iocopy.Copy(w, blob)
}

// handleUploadGem handles gem uploads.
func (h *HTTPHandler) handleUploadGem(w http.ResponseWriter, r *http.Request) {
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

	// Read gem file from request body
	spool, err := base.SpoolLimited(r.Body, maxGemUploadBytes)
	if err != nil {
		if errors.Is(err, base.ErrContentTooLarge) {
			http.Error(w, "gem too large", http.StatusRequestEntityTooLarge)
			return
		}

		h.Log.Errorw("failed to read request body", "error", err)
		http.Error(w, "failed to read gem", http.StatusBadRequest)

		return
	}
	defer func() { _ = r.Body.Close() }()
	defer spool.Close()

	// Parse gem metadata
	handler := NewRubyGemsHandler()

	metadata, err := handler.Parse(r.Context(), spool.Reader())
	if err != nil {
		h.Log.Errorw("failed to parse gem", "error", err)
		http.Error(w, "invalid gem file", http.StatusBadRequest)

		return
	}

	name, _ := metadata["name"].(string)
	version, _ := metadata["version"].(string)

	if !validGemIdent(name) || !validGemVersion(version) {
		http.Error(w, "gem metadata is missing a valid name or version", http.StatusBadRequest)
		return
	}

	platform := getStringFromMetadata(metadata, "platform")
	if platform == "" {
		platform = "ruby"
	}

	if !validGemIdent(platform) {
		http.Error(w, "invalid gem platform", http.StatusBadRequest)
		return
	}

	if licensepolicy.Enforce(w, r, h.DB, h.Log, repoID, getStringArrayFromMetadata(metadata, "licenses")...) {
		return
	}

	existing, err := h.DB.GetRubyGem(r.Context(), repoID, name, version, platform)
	if err != nil {
		h.Log.Errorw("failed to check existing gem", "name", name, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if existing != nil {
		http.Error(w, "gem version already exists", http.StatusConflict)
		return
	}

	if h.gemFileNameTaken(w, r, repoID, name, version, platform) {
		return
	}

	digest := spool.SHA256Hex()
	size := spool.Size()

	gem := &models.RubyGem{
		RepositoryID:   repoID,
		Name:           name,
		Version:        version,
		Platform:       platform,
		Summary:        getStringFromMetadata(metadata, "summary"),
		Description:    getStringFromMetadata(metadata, "description"),
		Homepage:       getStringFromMetadata(metadata, "homepage"),
		Authors:        getStringArrayFromMetadata(metadata, "authors"),
		Licenses:       getStringArrayFromMetadata(metadata, "licenses"),
		Dependencies:   mustMarshalJSON(metadata["dependencies"]),
		ChecksumSHA256: digest,
		MetadataJSON:   mustMarshalJSON(metadata),
		DownloadCount:  0,
		Size:           size,
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

	if status, msg := h.storeGem(r.Context(), gem, spool); status != 0 {
		if err := quota.Release(r.Context(), h.DB, repoID, size); err != nil {
			h.Log.Warnw("failed to release quota", "repo", repoName, "error", err)
		}

		http.Error(w, msg, status)

		return
	}

	h.Emit(r.Context(), repoID, webhook.EventArtifactUploaded, webhook.ArtifactPayload(webhook.EventArtifactUploaded, gemArtifact(repoName, gem)))

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)

	if encErr := json.NewEncoder(w).Encode(map[string]any{
		"name":    gem.Name,
		"version": gem.Version,
		"message": "Gem uploaded successfully",
	}); encErr != nil {
		_ = encErr // response header already written
	}
}

// storeGem stores the gem blob first, then re-checks its existence and inserts
// the row under the per-blob lock (DB work only) so a concurrent cleanup cannot
// remove the blob before the row exists. It returns a zero status on success,
// otherwise the HTTP status and message to report; the caller releases the
// quota reservation.
func (h *HTTPHandler) storeGem(ctx context.Context, gem *models.RubyGem, body *base.Spool) (int, string) {
	status, msg := 0, ""

	blob := storage.SeekerBlob(gem.ChecksumSHA256, body.Reader(), map[string]string{
		"sha256":   gem.ChecksumSHA256,
		"gem_name": gem.Name,
		"version":  gem.Version,
	})

	lockErr := storage.CommitBlob(ctx, h.DB, h.BlobStore, blob, func(ctx context.Context, blobKey string) error {
		gem.BlobKey = blobKey

		gem.CreatedAt = importmeta.CreatedAt(ctx)

		if err := h.DB.CreateRubyGem(ctx, gem); err != nil {
			if database.IsUniqueViolation(err) || errors.Is(err, database.ErrDuplicate) {
				status, msg = http.StatusConflict, "gem version already exists"

				return err
			}

			h.Log.Errorw("failed to create gem record", "name", gem.Name, "version", gem.Version, "error", err)

			status, msg = http.StatusInternalServerError, "internal error"

			return err
		}

		return nil
	})
	if lockErr == nil {
		return 0, ""
	}

	if status == 0 {
		var storeErr *storage.StoreError

		switch {
		case errors.As(lockErr, &storeErr) && storeErr.Op == "check":
			h.Log.Errorw("failed to check gem blob", "error", lockErr)
		case storeErr != nil:
			h.Log.Errorw("failed to store blob", "error", lockErr)
		default:
			h.Log.Errorw("gem blob lock failed", "name", gem.Name, "error", lockErr)
		}

		status, msg = http.StatusInternalServerError, "internal error"
	}

	// The row was not created: drop the blob if nothing else references it.
	if err := storage.SafeDeleteBlob(ctx, h.DB, h.BlobStore, gem.ChecksumSHA256); err != nil {
		h.Log.Warnw("failed to clean up gem blob", "sha256", gem.ChecksumSHA256, "error", err)
	}

	return status, msg
}

// gemArtifact describes gem for webhook payloads.
func gemArtifact(repoName string, gem *models.RubyGem) webhook.Artifact {
	return webhook.Artifact{
		Repository: repoName,
		Format:     "rubygems",
		Name:       gem.Name,
		Version:    gem.Version,
		Path:       "gems/" + gemFilename(gem),
		SHA256:     gem.ChecksumSHA256,
		Size:       gem.Size,
	}
}

// gemFilename is "{name}-{version}[-{platform}].gem".
func gemFilename(gem *models.RubyGem) string {
	if gem.Platform == "" || gem.Platform == "ruby" {
		return gem.Name + "-" + gem.Version + ".gem"
	}

	return gem.Name + "-" + gem.Version + "-" + gem.Platform + ".gem"
}

// handleYankGem yanks a gem version.
func (h *HTTPHandler) handleYankGem(w http.ResponseWriter, r *http.Request) {
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

	// `gem yank` sends gem_name/version as a form-encoded DELETE body, which
	// net/http's ParseForm only reads for POST/PUT/PATCH.
	form, err := yankForm(r)
	if err != nil {
		http.Error(w, "invalid form data", http.StatusBadRequest)
		return
	}

	gemName := form.Get("gem_name")
	version := form.Get("version")
	platform := form.Get("platform")

	if gemName == "" || version == "" {
		http.Error(w, "gem_name and version are required", http.StatusBadRequest)
		return
	}

	versions, err := h.DB.ListRubyGemVersions(r.Context(), repoID, gemName)
	if err != nil {
		h.Log.Errorw("failed to list gem versions", "name", gemName, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	// Only gems that are currently listed are yanked (and announced) so a
	// repeated yank does not emit a second event.
	var yanked []*models.RubyGem

	for _, g := range versions {
		if g.Version == version && !g.Yanked && (platform == "" || g.Platform == platform) {
			yanked = append(yanked, g)
		}
	}

	if len(yanked) == 0 {
		http.NotFound(w, r)
		return
	}

	// Yanking hides a release from resolution, so it is as destructive as a
	// delete for immutable_releases.
	if err := trash.GuardReleaseByName(r, h.DB, repoName, "rubygems", version); err != nil {
		if errors.Is(err, trash.ErrImmutableRelease) {
			http.Error(w, "release versions of this repository are immutable", http.StatusConflict)
			return
		}

		h.Log.Errorw("failed to check release immutability", "repo", repoName, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if err := h.DB.YankRubyGem(r.Context(), repoID, gemName, version, platform, true); err != nil {
		h.Log.Errorw("failed to yank gem", "name", gemName, "version", version, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	for _, g := range yanked {
		h.Emit(r.Context(), repoID, webhook.EventArtifactDeleted, webhook.ArtifactPayload(webhook.EventArtifactDeleted, gemArtifact(repoName, g)))
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	if encErr := json.NewEncoder(w).Encode(map[string]any{
		"message": "Gem yanked successfully",
	}); encErr != nil {
		_ = encErr // response header already written
	}
}

const maxYankFormBytes = 64 << 10

// yankForm returns the query parameters merged with the form-encoded body.
func yankForm(r *http.Request) (url.Values, error) {
	form := r.URL.Query()

	if r.Body == nil {
		return form, nil
	}

	body, err := base.ReadAllLimited(r.Body, maxYankFormBytes)
	if err != nil {
		return nil, fmt.Errorf("read yank form: %w", err)
	}

	bodyValues, err := url.ParseQuery(string(body))
	if err != nil {
		return nil, fmt.Errorf("parse yank form: %w", err)
	}

	for k, vs := range bodyValues {
		form[k] = append(vs, form[k]...)
	}

	return form, nil
}

// handleGemInfo returns gem information as JSON.
func (h *HTTPHandler) handleGemInfo(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	repoName := vars["name"]
	gemName := chi.URLParam(r, "gem")

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

	versions, err := h.DB.ListRubyGemVersions(r.Context(), repoID, gemName)
	if err != nil {
		h.Log.Errorw("failed to list gem versions", "name", gemName, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	versions = liveGems(versions)
	if len(versions) == 0 {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	if encErr := json.NewEncoder(w).Encode(gemInfoResponse{Name: gemName, Versions: gemVersionResponses(versions)}); encErr != nil {
		_ = encErr // response header already written
	}
}

// handleSearch searches for gems.
func (h *HTTPHandler) handleSearch(w http.ResponseWriter, r *http.Request) {
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

	// An empty query or the "*" wildcard lists every gem.
	query := r.URL.Query().Get("query")
	if query == "*" {
		query = ""
	}

	gems, err := h.DB.SearchRubyGems(r.Context(), repoID, query)
	if err != nil {
		h.Log.Errorw("failed to search gems", "query", query, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	if encErr := json.NewEncoder(w).Encode(gemSearchResponse{Gems: gemVersionResponses(liveGems(gems))}); encErr != nil {
		_ = encErr // response header already written
	}
}

// gemVersionResponse is the public JSON view of one gem version: no internal
// ids, storage keys or raw metadata.
type gemVersionResponse struct {
	Name        string    `json:"name"`
	Version     string    `json:"version"`
	Platform    string    `json:"platform"`
	Summary     string    `json:"summary"`
	Description string    `json:"description"`
	Homepage    string    `json:"homepage"`
	Authors     []string  `json:"authors"`
	Licenses    []string  `json:"licenses"`
	SHA256      string    `json:"sha256"`
	Downloads   int       `json:"downloads"`
	Size        int64     `json:"size"`
	CreatedAt   time.Time `json:"created_at"`
}

type gemInfoResponse struct {
	Name     string               `json:"name"`
	Versions []gemVersionResponse `json:"versions"`
}

type gemSearchResponse struct {
	Gems []gemVersionResponse `json:"gems"`
}

// liveGems drops yanked versions: a yanked gem is no longer offered.
func liveGems(gems []*models.RubyGem) []*models.RubyGem {
	return slices.DeleteFunc(slices.Clone(gems), func(g *models.RubyGem) bool { return g.Yanked })
}

func gemVersionResponses(gems []*models.RubyGem) []gemVersionResponse {
	out := make([]gemVersionResponse, len(gems))

	for i, g := range gems {
		authors, licenses := g.Authors, g.Licenses
		if authors == nil {
			authors = []string{}
		}

		if licenses == nil {
			licenses = []string{}
		}

		out[i] = gemVersionResponse{
			Name:        g.Name,
			Version:     g.Version,
			Platform:    g.Platform,
			Summary:     g.Summary,
			Description: g.Description,
			Homepage:    g.Homepage,
			Authors:     authors,
			Licenses:    licenses,
			SHA256:      g.ChecksumSHA256,
			Downloads:   g.DownloadCount,
			Size:        g.Size,
			CreatedAt:   g.CreatedAt,
		}
	}

	return out
}

// Helper functions

func getStringFromMetadata(metadata map[string]any, key string) string {
	if v, ok := metadata[key].(string); ok {
		return v
	}

	return ""
}

func getStringArrayFromMetadata(metadata map[string]any, key string) []string {
	if v, ok := metadata[key].([]any); ok {
		var result []string

		for _, item := range v {
			if s, ok := item.(string); ok {
				result = append(result, s)
			}
		}

		return result
	}

	return nil
}

func mustMarshalJSON(v any) []byte {
	data, err := json.Marshal(v)
	if err != nil {
		// Return empty JSON object on error
		return []byte("{}")
	}

	return data
}

// isPrerelease follows Gem::Version#prerelease?: any letter in the version.
func isPrerelease(version string) bool {
	return strings.ContainsFunc(version, unicode.IsLetter)
}

// gemSegment is one run of a version: a number (ASCII digits, kept as text
// without leading zeros so it has no size limit) or a string.
type gemSegment struct {
	text string
	num  bool
}

// gemVersionSegments splits a version into numeric and string segments.
func gemVersionSegments(v string) []gemSegment {
	var (
		segs []gemSegment
		cur  strings.Builder
		num  bool
	)

	flush := func() {
		if cur.Len() == 0 {
			return
		}

		if num {
			segs = append(segs, gemSegment{text: strings.TrimLeft(cur.String(), "0"), num: true})
		} else {
			segs = append(segs, gemSegment{text: cur.String()})
		}

		cur.Reset()
	}

	for _, r := range v {
		switch {
		case r == '.' || r == '-':
			flush()
		case r >= '0' && r <= '9':
			if !num {
				flush()
			}

			num = true

			cur.WriteRune(r)
		default:
			if num {
				flush()
			}

			num = false

			cur.WriteRune(r)
		}
	}

	flush()

	return segs
}

// compareVersions implements Gem::Version#<=>: numeric segments compare
// numerically, string segments (prerelease) sort before numbers.
func compareVersions(v1, v2 string) int {
	a, b := gemVersionSegments(v1), gemVersionSegments(v2)

	for i := 0; i < len(a) || i < len(b); i++ {
		// A missing segment is the number 0.
		x, y := gemSegment{num: true}, gemSegment{num: true}
		if i < len(a) {
			x = a[i]
		}

		if i < len(b) {
			y = b[i]
		}

		if c := compareSegments(x, y); c != 0 {
			return c
		}
	}

	return 0
}

func compareSegments(x, y gemSegment) int {
	switch {
	case !x.num && !y.num:
		return strings.Compare(x.text, y.text)
	case !x.num:
		return -1
	case !y.num:
		return 1
	default:
		// Numbers without leading zeros: the longer one is larger.
		return cmp.Or(cmp.Compare(len(x.text), len(y.text)), strings.Compare(x.text, y.text))
	}
}

// errAmbiguousGemFile means several gems of one repository share a
// "{name}-{version}[-{platform}]" file name.
var errAmbiguousGemFile = errors.New("gem file name is ambiguous")

// findGem resolves "{name}-{version}[-{platform}]" to the one live (not
// yanked) gem it names, or nil. Names and platforms may contain hyphens and
// versions never do, so each hyphen followed by a digit is tried as the
// name/version boundary; every candidate is looked up and more than one hit is
// errAmbiguousGemFile instead of an arbitrary pick.
func (h *HTTPHandler) findGem(ctx context.Context, repoID, nameVersion string) (*models.RubyGem, error) {
	gems, err := h.gemsNamed(ctx, repoID, nameVersion)
	if err != nil {
		return nil, err
	}

	var live *models.RubyGem

	for _, g := range gems {
		if g.Yanked {
			continue
		}

		if live != nil {
			return nil, errAmbiguousGemFile
		}

		live = g
	}

	return live, nil
}

// gemsNamed returns every gem (yanked or not) whose file name, without the
// ".gem" suffix, is nameVersion.
func (h *HTTPHandler) gemsNamed(ctx context.Context, repoID, nameVersion string) ([]*models.RubyGem, error) {
	var gems []*models.RubyGem

	for i := 1; i < len(nameVersion)-1; i++ {
		if nameVersion[i] != '-' || nameVersion[i+1] < '0' || nameVersion[i+1] > '9' {
			continue
		}

		version, platform, hasPlatform := strings.Cut(nameVersion[i+1:], "-")
		if !hasPlatform {
			platform = "ruby"
		}

		gem, err := h.DB.GetRubyGem(ctx, repoID, nameVersion[:i], version, platform)
		if err != nil {
			return nil, err
		}

		if gem != nil {
			gems = append(gems, gem)
		}
	}

	return gems, nil
}

// gemFileNameTaken reports, after writing the response, whether the file name
// of the gem about to be stored already resolves to another gem.
// "{name}-{version}[-{platform}]" must name exactly one gem: otherwise one of
// the two could never be downloaded, or would shadow the other.
func (h *HTTPHandler) gemFileNameTaken(w http.ResponseWriter, r *http.Request, repoID, name, version, platform string) bool {
	file := strings.TrimSuffix(gemFilename(&models.RubyGem{Name: name, Version: version, Platform: platform}), ".gem")

	clashing, err := h.gemsNamed(r.Context(), repoID, file)
	if err != nil {
		h.Log.Errorw("failed to check gem file name", "name", name, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return true
	}

	if len(clashing) > 0 {
		http.Error(w, "gem file name is already used by another gem", http.StatusConflict)
		return true
	}

	return false
}

// validGemVersion accepts the canonical RubyGems version spelling: it starts
// with a digit, as findGem requires to locate the name/version boundary, and
// never holds a hyphen (Gem::Version rewrites "-" to ".pre.").
func validGemVersion(s string) bool {
	return validGemIdent(s) && s[0] >= '0' && s[0] <= '9' && !strings.Contains(s, "-")
}

func validGemIdent(s string) bool {
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

// marshalSpecsList creates a Ruby Marshal 4.8 encoded array of gem specs:
// [[name, Gem::Version, platform], ...], the format specs.4.8.gz et al.
// serve. Routed through pkg/rubymarshal instead of hand-written byte
// twiddling so it shares one codec with handleGemspec.
func marshalSpecsList(gems []*models.RubyGem) ([]byte, error) {
	entries := make([]any, len(gems))

	for i, gem := range gems {
		entries[i] = []any{gem.Name, rubymarshal.NewGemVersion(gem.Version), gem.Platform}
	}

	data, err := rubymarshal.Encode(entries)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal specs list: %w", err)
	}

	return data, nil
}

// gemDependencyJSON is the {"name","requirement","type"} shape
// parseGemspecMetadata/handleUploadGem persist into RubyGem.Dependencies.
type gemDependencyJSON struct {
	Name        string `json:"name"`
	Requirement string `json:"requirement"`
	Type        string `json:"type"`
}

// decodeGemDependencies parses a RubyGem.Dependencies JSON column back into
// rubymarshal.Dependency values for re-encoding into a served gemspec.
func decodeGemDependencies(raw []byte) []rubymarshal.Dependency {
	if len(raw) == 0 {
		return nil
	}

	var deps []gemDependencyJSON
	if err := json.Unmarshal(raw, &deps); err != nil {
		return nil
	}

	out := make([]rubymarshal.Dependency, len(deps))
	for i, d := range deps {
		out[i] = rubymarshal.Dependency{Name: d.Name, Requirement: d.Requirement, Type: d.Type}
	}

	return out
}
