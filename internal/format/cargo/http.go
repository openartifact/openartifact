// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package cargo

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	"github.com/openartifact/openartifact/internal/licensepolicy"
	"github.com/openartifact/openartifact/internal/presign"
	"github.com/openartifact/openartifact/internal/quarantine"
	"github.com/openartifact/openartifact/internal/storage"
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
	r.Get("/index/{prefix1}/{prefix2}/{crate}", h.handleSparseIndex)
	r.Get("/index/{prefix1}/{crate}", h.handleSparseIndex)
	r.Get("/index/{crate}", h.handleSparseIndex)
	r.Get("/api/v1/crates/{crate}/{version}/download", h.handleDownload)
	r.Put("/api/v1/crates/new", h.handlePublish)
	r.Delete("/api/v1/crates/{crate}/{version}/yank", h.handleYank)
	r.Put("/api/v1/crates/{crate}/{version}/unyank", h.handleUnyank)
	r.Get("/api/v1/crates", h.handleSearch)
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

	config := map[string]string{
		"dl":  fmt.Sprintf("%s/repos/%s/api/v1/crates", h.BaseURL, repoName),
		"api": fmt.Sprintf("%s/repos/%s", h.BaseURL, repoName),
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	if encErr := json.NewEncoder(w).Encode(config); encErr != nil {
		_ = encErr // response header already written
	}
}

func (h *HTTPHandler) handleSparseIndex(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	repoName := vars["name"]
	crateName := vars["crate"]

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

	// Get all versions of the crate
	versions, err := h.DB.ListCargoCrateVersions(r.Context(), repoID, crateName)
	if err != nil {
		h.Log.Errorw("failed to get cargo crate versions", "name", crateName, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if len(versions) == 0 {
		http.NotFound(w, r)
		return
	}

	// Build sparse index response (newline-delimited JSON)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	for _, v := range versions {
		// Parse metadata JSON to get deps and features
		var metadata map[string]any
		if err := json.Unmarshal(v.MetadataJSON, &metadata); err != nil {
			h.Log.Errorw("failed to parse metadata", "crate", crateName, "version", v.Version, "error", err)
			continue
		}

		// Build index entry
		entry := map[string]any{
			"name":     v.Name,
			"vers":     v.Version,
			"deps":     transformCargoIndexDeps(metadata["deps"]),
			"cksum":    v.Checksum,
			"features": metadata["features"],
			"yanked":   v.Yanked,
			"links":    metadata["links"],
		}

		if err := json.NewEncoder(w).Encode(entry); err != nil {
			h.Log.Errorw("failed to write index entry", "crate", crateName, "version", v.Version, "error", err)
			return
		}
	}
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

	if crate.Yanked {
		http.Error(w, "crate version yanked", http.StatusGone)
		return
	}

	if h.DenyIfQuarantined(w, r, repoID, quarantine.PathKeys(
		fmt.Sprintf("%s-%s.crate", crateName, version), crateName+"/"+version, r.URL.Path)...) {
		return
	}

	// Count the download before the blob is opened or redirected to: a
	// presigned response never reaches the streaming code below.
	if err := h.DB.IncrementCargoDownloadCount(r.Context(), repoID, crateName, version); err != nil {
		h.Log.Errorw("failed to increment download count", "name", crateName, "version", version, "error", err)
	}

	if h.TryPresign(w, r, presign.Blob{SHA256: crate.BlobKey, Filename: crateName + "-" + version + ".crate", ContentType: "application/octet-stream"}) {
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
	metadataBytes := make([]byte, metadataLen)
	if _, err := io.ReadFull(r.Body, metadataBytes); err != nil {
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
	if !ok {
		http.Error(w, "missing or invalid 'name' field", http.StatusBadRequest)
		return
	}

	version, ok := metadata["vers"].(string)
	if !ok {
		http.Error(w, "missing or invalid 'vers' field", http.StatusBadRequest)
		return
	}

	// A crate that only declares license_file has no enforceable license.
	declared, _ := metadata["license"].(string)
	if licensepolicy.Enforce(w, r, h.DB, h.Log, repoID, declared) {
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
	crateBytes := make([]byte, crateLen)
	if _, err := io.ReadFull(r.Body, crateBytes); err != nil {
		h.Log.Errorw("failed to read crate file", "error", err)
		http.Error(w, "invalid request body", http.StatusBadRequest)

		return
	}

	checksum := fmt.Sprintf("%x", sha256.Sum256(crateBytes))

	// Extract optional fields
	description, _ := metadata["description"].(string)
	homepage, _ := metadata["homepage"].(string)
	documentation, _ := metadata["documentation"].(string)
	repository, _ := metadata["repository"].(string)
	license, _ := metadata["license"].(string)
	readme, _ := metadata["readme"].(string)

	// Extract arrays
	var keywords, categories, authors []string

	if kw, ok := metadata["keywords"].([]any); ok {
		for _, k := range kw {
			if s, ok := k.(string); ok {
				keywords = append(keywords, s)
			}
		}
	}

	if cat, ok := metadata["categories"].([]any); ok {
		for _, c := range cat {
			if s, ok := c.(string); ok {
				categories = append(categories, s)
			}
		}
	}

	if auth, ok := metadata["authors"].([]any); ok {
		for _, a := range auth {
			if s, ok := a.(string); ok {
				authors = append(authors, s)
			}
		}
	}

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
		Yanked:        false,
		DownloadCount: 0,
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

	if err := h.DB.YankCargoCrate(r.Context(), repoID, crateName, version, true); err != nil {
		h.Log.Errorw("failed to yank cargo crate", "name", crateName, "version", version, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	response := map[string]any{
		"ok": true,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	if encErr := json.NewEncoder(w).Encode(response); encErr != nil {
		_ = encErr // response header already written
	}
}

func (h *HTTPHandler) handleUnyank(w http.ResponseWriter, r *http.Request) {
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

	if err := h.DB.YankCargoCrate(r.Context(), repoID, crateName, version, false); err != nil {
		h.Log.Errorw("failed to unyank cargo crate", "name", crateName, "version", version, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	response := map[string]any{
		"ok": true,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	if encErr := json.NewEncoder(w).Encode(response); encErr != nil {
		_ = encErr // response header already written
	}
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

	crates, err := h.DB.SearchCargoCrates(r.Context(), repoID, query, perPage)
	if err != nil {
		h.Log.Errorw("failed to search cargo crates", "query", query, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	// Build response
	var results []map[string]any
	for _, crate := range crates {
		results = append(results, map[string]any{
			"name":        crate.Name,
			"max_version": crate.Version,
			"description": crate.Description,
			"downloads":   crate.DownloadCount,
		})
	}

	if results == nil {
		results = []map[string]any{}
	}

	response := map[string]any{
		"crates": results,
		"meta": map[string]any{
			"total": len(results),
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
