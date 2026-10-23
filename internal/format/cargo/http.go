// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package cargo

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	"github.com/openartifact/openartifact/internal/licensemeta"
	"github.com/openartifact/openartifact/internal/licensepolicy"
	"github.com/openartifact/openartifact/internal/presign"
	"github.com/openartifact/openartifact/internal/quarantine"
	"github.com/openartifact/openartifact/internal/quota"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/trash"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/internal/webhook"
	"github.com/openartifact/openartifact/pkg/logger"
)

type HTTPHandler struct {
	*base.Handler
}

func NewHTTPHandler(log *logger.Logger, db database.Database, blobStore storage.BlobStore, baseURL string) *HTTPHandler {
	return &HTTPHandler{Handler: base.New(log, db, blobStore, baseURL)}
}

// RegisterRepoRoutes registers Cargo-specific routes on the provided sub-router.
// It does NOT wrap in r.Route() — callers are responsible for the path prefix.
func (h *HTTPHandler) RegisterRepoRoutes(r chi.Router) {
	r.Get("/config.json", h.handleConfig)
	// Cargo reads config.json from the root of the index it was pointed at.
	r.Get("/index/config.json", h.handleConfig)
	r.Get("/index/{prefix1}/{prefix2}/{crate}", h.handleSparseIndex)
	r.Get("/index/{prefix1}/{crate}", h.handleSparseIndex)
	r.Get("/index/{crate}", h.handleSparseIndex)
	r.Get("/api/v1/crates/{crate}/{version}/download", h.handleDownload)
	r.Put("/api/v1/crates/new", h.handlePublish)
	r.Delete("/api/v1/crates/{crate}/{version}/yank", h.handleYank)
	r.Put("/api/v1/crates/{crate}/{version}/unyank", h.handleUnyank)
	r.Get("/api/v1/crates", base.Compress(h.handleSearch))
}

func (h *HTTPHandler) RegisterRoutes(r chi.Router, prefix string) {
	r.Route(prefix, func(r chi.Router) {
		h.RegisterRepoRoutes(r)
	})
	h.Log.Infow("cargo handler registered", "prefix", prefix)
}

func (h *HTTPHandler) handleConfig(w http.ResponseWriter, r *http.Request) {
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

	config := map[string]any{
		"dl":  fmt.Sprintf("%s/repos/%s/api/v1/crates", h.BaseURL, repoName),
		"api": fmt.Sprintf("%s/repos/%s", h.BaseURL, repoName),
	}

	// A private repository needs credentials for the index and the downloads
	// too; this tells cargo to send its token with every request instead of
	// trying anonymously first.
	if !repo.Public {
		config["auth-required"] = true
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	if encErr := json.NewEncoder(w).Encode(config); encErr != nil {
		_ = encErr // response header already written
	}
}

// indexMaxAge is how long shared caches may reuse a public repository's sparse
// index entry before revalidating.
const indexMaxAge = 60

// noIndexVersions is the CargoIndexStamp of a crate with no live version.
const noIndexVersions = "0:0"

func (h *HTTPHandler) handleSparseIndex(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	repoName := vars["name"]
	crateName := vars["crate"]

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

	// The stamp covers the live versions and their yanked flags, the only
	// mutable state of an entry, so cargo's per-dependency revalidation is a
	// narrow aggregate query instead of loading every version with its publish
	// metadata.
	stamp, err := h.DB.CargoIndexStamp(r.Context(), repo.ID, crateName)
	if err != nil {
		h.Log.Errorw("failed to stamp cargo index", "name", crateName, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if stamp == noIndexVersions {
		http.NotFound(w, r)
		return
	}

	sum := sha256.Sum256([]byte(repo.ID + "\x00" + strings.ToLower(crateName) + "\x00" + stamp))

	etag := `"` + hex.EncodeToString(sum[:16]) + `"`
	if base.Revalidate(w, r, repo.Public, etag, indexMaxAge) {
		return
	}

	rendered, err := h.Rendered(r.Context(), etag, func(ctx context.Context) (*base.Rendered, error) {
		return h.renderSparseIndex(ctx, repo.ID, crateName)
	})
	if err != nil {
		h.Log.Errorw("failed to render cargo index", "name", crateName, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	rendered.Serve(w, r)
}

// indexMetadata is the part of the publish metadata a sparse index entry
// shows. Decoding into it skips the rest (readme, description, ...) without
// building maps for it.
type indexMetadata struct {
	Deps     any `json:"deps"`
	Features any `json:"features"`
	Links    any `json:"links"`
}

// renderSparseIndex builds the newline-delimited JSON index of one crate.
func (h *HTTPHandler) renderSparseIndex(ctx context.Context, repoID, crateName string) (*base.Rendered, error) {
	versions, err := h.DB.ListCargoIndexVersions(ctx, repoID, crateName)
	if err != nil {
		return nil, fmt.Errorf("list cargo index versions: %w", err)
	}

	var buf bytes.Buffer

	enc := json.NewEncoder(&buf)

	for _, v := range versions {
		var metadata indexMetadata
		if err := json.Unmarshal(v.MetadataJSON, &metadata); err != nil {
			h.Log.Errorw("failed to parse metadata", "crate", crateName, "version", v.Version, "error", err)
			continue
		}

		if err := enc.Encode(map[string]any{
			"name":     v.Name,
			"vers":     v.Version,
			"deps":     transformCargoIndexDeps(metadata.Deps),
			"cksum":    v.Checksum,
			"features": metadata.Features,
			"yanked":   v.Yanked,
			"links":    metadata.Links,
		}); err != nil {
			return nil, fmt.Errorf("encode index entry %s: %w", v.Version, err)
		}
	}

	return &base.Rendered{Body: buf.Bytes(), ContentType: "application/json"}, nil
}

func (h *HTTPHandler) handleDownload(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	repoName := vars["name"]
	crateName := vars["crate"]
	version := vars["version"]

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

	crate, err := h.DB.GetCargoCrate(r.Context(), repoID, crateName, version)
	if err != nil {
		h.Log.Errorw("failed to get cargo crate", "name", crateName, "version", version, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if crate == nil {
		http.NotFound(w, r)
		return
	}

	// A yanked version stays downloadable: yanking only keeps new dependency
	// resolution away from it (index "yanked": true), while every existing
	// Cargo.lock that pins it must keep building.
	if h.DenyIfQuarantined(w, r, repoID, quarantine.PathKeys(
		fmt.Sprintf("%s-%s.crate", crateName, version), crateName+"/"+version, r.URL.Path)...) {
		return
	}

	// Count the download before the blob is opened or redirected to: a
	// presigned response never reaches the streaming code below.
	if err := h.DB.IncrementCargoDownloadCount(r.Context(), repoID, crateName, version); err != nil {
		h.Log.Errorw("failed to increment download count", "name", crateName, "version", version, "error", err)
	}

	if h.TryPresign(w, r, presign.Blob{SHA256: crate.BlobKey, Size: crate.SizeBytes, Filename: crateName + "-" + version + ".crate", ContentType: "application/octet-stream"}) {
		return
	}

	// Get blob from storage
	rc, err := h.BlobStore.Get(r.Context(), crate.BlobKey)
	if err != nil {
		h.Log.Errorw("failed to get crate blob", "key", crate.BlobKey, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}
	defer func() { _ = rc.Close() }()

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("ETag", `"`+crate.Checksum+`"`)
	w.WriteHeader(http.StatusOK)

	if _, err := io.Copy(w, rc); err != nil {
		h.Log.Errorw("failed to write crate response", "name", crateName, "version", version, "error", err)
	}
}

// Size caps for the client-declared lengths in a publish body; they bound the
// buffers allocated before any data is read.
const (
	maxPublishMetadataBytes = 1 << 20   // 1 MiB of JSON metadata
	maxPublishCrateBytes    = 128 << 20 // 128 MiB .crate file (crates.io allows 10 MiB)
)

// publishLicenses returns the licenses a policy is enforced on: the license of
// the Cargo.toml inside the crate. The publish metadata's "license" is only the
// fallback for a crate that declares none there, so a client cannot publish a
// crate under a different license than it ships. A crate whose archive cannot
// be read safely is refused, and so is one that cannot be read at all while the
// repository has a blocking policy (false, response written). A crate that
// only declares license_file has no enforceable license.
func (h *HTTPHandler) publishLicenses(w http.ResponseWriter, r *http.Request, repoID, name, version string, crate []byte, metadata map[string]any) ([]string, bool) {
	res, err := licensemeta.ReadBytes(r.Context(), "cargo", name+"-"+version+".crate", crate)

	switch {
	case licensemeta.Unsafe(err):
		h.Log.Warnw("cargo publish rejected: crate metadata cannot be read safely", "name", name, "version", version, "error", err)
		licensepolicy.RejectUnsafe(w, h.Log)

		return nil, false
	case err != nil:
		h.Log.Debugw("cargo publish: crate metadata unreadable", "name", name, "version", version, "error", err)

		if licensepolicy.RefuseUnderivable(w, r, h.DB, h.Log, repoID) {
			return nil, false
		}
	case !res.Empty():
		return res.Licenses, true
	}

	if declared, _ := metadata["license"].(string); declared != "" {
		return []string{declared}, true
	}

	return nil, true
}

//nolint:gocyclo,cyclop // protocol handler: branching mirrors spec state machine
func (h *HTTPHandler) handlePublish(w http.ResponseWriter, r *http.Request) {
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

	// Read metadata length
	var metadataLen uint32
	if err := binary.Read(r.Body, binary.LittleEndian, &metadataLen); err != nil {
		h.Log.Errorw("failed to read metadata length", "error", err)
		http.Error(w, "invalid request body", http.StatusBadRequest)

		return
	}

	if metadataLen > maxPublishMetadataBytes {
		http.Error(w, "metadata too large", http.StatusRequestEntityTooLarge)
		return
	}

	// Read metadata JSON
	metadataBytes, err := readDeclared(r.Body, metadataLen)
	if err != nil {
		h.Log.Errorw("failed to read metadata", "error", err)
		http.Error(w, "invalid request body", http.StatusBadRequest)

		return
	}

	// Parse metadata
	var metadata map[string]any
	if err := json.Unmarshal(metadataBytes, &metadata); err != nil {
		h.Log.Errorw("failed to parse metadata JSON", "error", err)
		http.Error(w, "invalid metadata JSON", http.StatusBadRequest)

		return
	}

	// Extract required fields
	crateName, ok := metadata["name"].(string)
	if !ok || !validCrateName(crateName) {
		writePublishError(w, http.StatusBadRequest, "missing or invalid 'name' field: a crate name starts with a letter or '_' and holds only ASCII letters, digits, '-' and '_' (at most 64)")
		return
	}

	version, ok := metadata["vers"].(string)
	if !ok || !validCrateVersion(version) {
		writePublishError(w, http.StatusBadRequest, "missing or invalid 'vers' field: expected a semantic version such as 1.2.3 or 1.2.3-rc.1")
		return
	}

	// Refuse a duplicate before the (up to 128 MiB) crate is read: a version
	// is published once, and a crate name is unique regardless of case.
	conflict, err := h.publishConflict(r.Context(), repoID, crateName, version)
	if err != nil {
		h.Log.Errorw("failed to check existing crate versions", "name", crateName, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if conflict != "" {
		writePublishError(w, http.StatusConflict, conflict)
		return
	}

	// Read crate file length
	var crateLen uint32
	if err := binary.Read(r.Body, binary.LittleEndian, &crateLen); err != nil {
		h.Log.Errorw("failed to read crate length", "error", err)
		http.Error(w, "invalid request body", http.StatusBadRequest)

		return
	}

	if crateLen > maxPublishCrateBytes {
		http.Error(w, "crate file too large", http.StatusRequestEntityTooLarge)
		return
	}

	// Read crate file and compute checksum
	crateBytes, err := readDeclared(r.Body, crateLen)
	if err != nil {
		h.Log.Errorw("failed to read crate file", "error", err)
		http.Error(w, "invalid request body", http.StatusBadRequest)

		return
	}

	checksum := fmt.Sprintf("%x", sha256.Sum256(crateBytes))

	licenses, ok := h.publishLicenses(w, r, repoID, crateName, version, crateBytes, metadata)
	if !ok || licensepolicy.Enforce(w, r, h.DB, h.Log, repoID, licenses...) {
		return
	}

	// Extract optional fields
	description, _ := metadata["description"].(string)
	homepage, _ := metadata["homepage"].(string)
	documentation, _ := metadata["documentation"].(string)
	repository, _ := metadata["repository"].(string)
	license, _ := metadata["license"].(string)
	readme, _ := metadata["readme"].(string)

	keywords := stringList(metadata["keywords"])
	categories := stringList(metadata["categories"])
	authors := stringList(metadata["authors"])

	// Create crate record
	crate := &models.CargoCrate{
		RepositoryID:  repoID,
		Name:          crateName,
		Version:       version,
		Description:   description,
		Homepage:      homepage,
		Documentation: documentation,
		Repository:    repository,
		License:       license,
		Keywords:      keywords,
		Categories:    categories,
		Authors:       authors,
		Readme:        readme,
		MetadataJSON:  metadataBytes,
		Checksum:      checksum,
		SizeBytes:     int64(len(crateBytes)),
		Yanked:        false,
		DownloadCount: 0,
	}

	// Reserve quota before anything is persisted; every failure path below
	// returns the reservation.
	if err := quota.Enforce(r.Context(), h.DB, repoID, crate.SizeBytes); err != nil {
		if errors.Is(err, quota.ErrExceeded) {
			quota.WriteExceeded(w)
			return
		}

		h.Log.Errorw("failed to reserve quota", "repo", repoName, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	// Store the blob and insert the referencing row under the per-blob lock so
	// a concurrent unpublish cannot delete the blob in between.
	if err := storage.WithBlobLock(r.Context(), h.DB, checksum, func(ctx context.Context) error {
		blobKey, err := h.BlobStore.Put(ctx, bytes.NewReader(crateBytes), map[string]string{
			"sha256":        checksum,
			"cargo-name":    crateName,
			"cargo-version": version,
		})
		if err != nil {
			return fmt.Errorf("store crate blob: %w", err)
		}

		crate.BlobKey = blobKey

		if err := h.DB.CreateCargoCrate(ctx, crate); err != nil {
			return fmt.Errorf("create cargo crate: %w", err)
		}

		return nil
	}); err != nil {
		if relErr := quota.Release(r.Context(), h.DB, repoID, crate.SizeBytes); relErr != nil {
			h.Log.Warnw("failed to release quota", "repo", repoName, "error", relErr)
		}

		// A concurrent publish of the same version won the race.
		if database.IsUniqueViolation(err) {
			writePublishError(w, http.StatusConflict, fmt.Sprintf("crate version `%s@%s` is already uploaded", crateName, version))
			return
		}

		h.Log.Errorw("failed to store cargo crate", "name", crateName, "version", version, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	h.Emit(r.Context(), repoID, webhook.EventArtifactUploaded, webhook.ArtifactPayload(webhook.EventArtifactUploaded, webhook.Artifact{
		Repository: repoName,
		Format:     "cargo",
		Name:       crateName,
		Version:    version,
		Path:       fmt.Sprintf("%s-%s.crate", crateName, version),
		SHA256:     checksum,
		Size:       int64(len(crateBytes)),
	}))

	// Return success response
	response := map[string]any{
		"warnings": []string{},
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	if encErr := json.NewEncoder(w).Encode(response); encErr != nil {
		_ = encErr // response header already written
	}
}

func (h *HTTPHandler) handleYank(w http.ResponseWriter, r *http.Request) {
	h.setYanked(w, r, true)
}

func (h *HTTPHandler) handleUnyank(w http.ResponseWriter, r *http.Request) {
	h.setYanked(w, r, false)
}

func (h *HTTPHandler) setYanked(w http.ResponseWriter, r *http.Request, yanked bool) {
	vars := urlparams.Vars(r)
	repoName := vars["name"]
	crateName := vars["crate"]
	version := vars["version"]

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

	if !h.guardYank(w, r, repoID, repoName, crateName, version) {
		return
	}

	if err := h.DB.YankCargoCrate(r.Context(), repoID, crateName, version, yanked); err != nil {
		h.Log.Errorw("failed to update cargo crate yank state", "name", crateName, "version", version, "yanked", yanked, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	if encErr := json.NewEncoder(w).Encode(map[string]any{"ok": true}); encErr != nil {
		_ = encErr // response header already written
	}
}

// guardYank answers 404 for an unknown crate version and 409 when the
// repository's immutable releases forbid hiding it. It reports whether the
// yank/unyank may proceed.
func (h *HTTPHandler) guardYank(w http.ResponseWriter, r *http.Request, repoID, repoName, crateName, version string) bool {
	crate, err := h.DB.GetCargoCrate(r.Context(), repoID, crateName, version)
	if err != nil {
		h.Log.Errorw("failed to get cargo crate", "name", crateName, "version", version, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return false
	}

	if crate == nil {
		http.NotFound(w, r)
		return false
	}

	if gErr := trash.GuardReleaseByName(r, h.DB, repoName, "cargo", crate.Version); gErr != nil {
		if errors.Is(gErr, trash.ErrImmutableRelease) {
			http.Error(w, "repository has immutable releases", http.StatusConflict)
			return false
		}

		h.Log.Errorw("failed to check release immutability", "name", crateName, "error", gErr)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return false
	}

	return true
}

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

	q := r.URL.Query()
	query := q.Get("q")
	perPage := 10

	if pp := q.Get("per_page"); pp != "" {
		if p, err := strconv.Atoi(pp); err == nil && p > 0 && p <= 100 {
			perPage = p
		}
	}

	crates, total, err := h.DB.SearchCargoCrateSummaries(r.Context(), repoID, query, perPage)
	if err != nil {
		h.Log.Errorw("failed to search cargo crates", "query", query, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	// One entry per crate; "total" counts every matching crate, not just the
	// page, so clients can tell there is more.
	results := make([]map[string]any, 0, len(crates))
	for _, crate := range crates {
		results = append(results, map[string]any{
			"name":        crate.Name,
			"max_version": maxCrateVersion(crate),
			"description": crate.Description,
			"downloads":   crate.Downloads,
		})
	}

	response := map[string]any{
		"crates": results,
		"meta": map[string]any{
			"total": total,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	if encErr := json.NewEncoder(w).Encode(response); encErr != nil {
		_ = encErr // response header already written
	}
}

// transformCargoIndexDeps converts the Cargo publish-API dependency shape
// (as stored verbatim in CargoCrate.MetadataJSON) into the registry index
// schema handleSparseIndex must serve. The two differ in exactly the way
// https://doc.rust-lang.org/cargo/reference/registry-index.html#json-schema
// documents: "version_req" -> "req", and a rename's original/alias names
// swap between the "name"/"explicit_name_in_toml" and "name"/"package"
// fields.
func transformCargoIndexDeps(rawDeps any) []map[string]any {
	publishDeps, ok := rawDeps.([]any)
	if !ok {
		return []map[string]any{}
	}

	index := make([]map[string]any, 0, len(publishDeps))

	for _, rd := range publishDeps {
		d, ok := rd.(map[string]any)
		if !ok {
			continue
		}

		name, _ := d["name"].(string)

		var pkg any
		if explicit, ok := d["explicit_name_in_toml"].(string); ok && explicit != "" {
			pkg = name
			name = explicit
		}

		index = append(index, map[string]any{
			"name":             name,
			"req":              d["version_req"],
			"features":         cargoDepOrDefault(d["features"], []any{}),
			"optional":         cargoDepOrDefault(d["optional"], false),
			"default_features": cargoDepOrDefault(d["default_features"], true),
			"target":           d["target"],
			"kind":             cargoDepOrDefault(d["kind"], "normal"),
			"registry":         d["registry"],
			"package":          pkg,
		})
	}

	return index
}

// cargoDepOrDefault returns v unless it is nil/absent, in which case it
// returns def — the index schema documents defaults for several optional
// dependency fields that the publish payload may omit entirely.
func cargoDepOrDefault(v, def any) any {
	if v == nil {
		return def
	}

	return v
}
