// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/license"
	"github.com/openartifact/openartifact/internal/licensepolicy"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/pkg/logger"
)

type PropertiesHandler struct {
	db       database.Database
	log      *logger.Logger
	licenses artifactLicenseSource
}

// NewPropertiesHandler returns the artifact properties handler. blobs is the
// store read to re-evaluate license policies on the artifact's own declared
// licenses when a license property changes.
func NewPropertiesHandler(log *logger.Logger, db database.Database, blobs storage.BlobStore) *PropertiesHandler {
	return &PropertiesHandler{
		db: db, log: log,
		licenses: artifactLicenseSource{db: db, blobs: blobs, log: log, detector: license.NewDetector(log)},
	}
}

// validatePropertyKey returns a message for an unusable property key, or "".
func validatePropertyKey(key string) string {
	switch {
	case key == "":
		return "property key must not be empty"
	case len(key) > maxPropertyKeyLen:
		return "property key is too long (max 255 bytes)"
	case !utf8.ValidString(key):
		return "property key must be valid UTF-8"
	}

	for _, r := range key {
		if r < 0x20 || r == 0x7f {
			return "property key must not contain control characters"
		}
	}

	return ""
}

// decodeProperties reads a properties request body and validates it. The body
// is a flat string map, or the same map wrapped as {"properties": {...}} (the
// shape the web UI sends). It returns a client-facing message on failure.
func decodeProperties(r *http.Request) (map[string]string, string) {
	var raw map[string]json.RawMessage
	if err := decodeJSON(r, &raw); err != nil {
		return nil, "invalid JSON body"
	}

	if inner, ok := raw["properties"]; ok && len(raw) == 1 {
		var wrapped map[string]json.RawMessage
		if json.Unmarshal(inner, &wrapped) == nil && wrapped != nil {
			raw = wrapped
		}
	}

	if len(raw) > maxPropertyCount {
		return nil, "too many properties (max " + strconv.Itoa(maxPropertyCount) + ")"
	}

	props := make(map[string]string, len(raw))

	for key, rawValue := range raw {
		if msg := validatePropertyKey(key); msg != "" {
			return nil, msg
		}

		var value string
		if err := json.Unmarshal(rawValue, &value); err != nil {
			return nil, "property values must be strings"
		}

		if len(value) > maxPropertyValueLen {
			return nil, "property value is too long (max 4096 bytes)"
		}

		if !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
			return nil, "property value must be valid UTF-8 without NUL bytes"
		}

		props[key] = value
	}

	return props, ""
}

func (h *PropertiesHandler) RegisterRoutes(r chi.Router, pathPrefix string) {
	r.Get(pathPrefix+"/artifacts/{artifact_id}/properties", h.handleGetProperties)
	r.Put(pathPrefix+"/artifacts/{artifact_id}/properties", h.handleSetProperties)
	r.Patch(pathPrefix+"/artifacts/{artifact_id}/properties", h.handlePatchProperties)
	r.Delete(pathPrefix+"/artifacts/{artifact_id}/properties/{key}", h.handleDeleteProperty)
	r.Get(pathPrefix+"/artifacts/search", h.handleSearchByProperty)
}

func (h *PropertiesHandler) handleGetProperties(w http.ResponseWriter, r *http.Request) {
	name := urlparams.Vars(r)["name"]
	artifactIDEncoded := urlparams.Vars(r)["artifact_id"]
	ctx := r.Context()

	// Decode the artifact ID from URL encoding
	artifactID, err := url.PathUnescape(artifactIDEncoded)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid artifact_id")
		return
	}

	if msg := validateArtifactID(artifactID); msg != "" {
		writeError(w, http.StatusBadRequest, "bad_request", msg)
		return
	}

	repo, err := h.db.GetRepositoryByName(ctx, name)
	if err != nil {
		h.log.Errorw("get properties: db error getting repo", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if repo == nil {
		writeError(w, http.StatusNotFound, "not_found", "repository not found")
		return
	}

	props, err := h.db.ListArtifactProperties(ctx, repo.ID, artifactID)
	if err != nil {
		h.log.Errorw("get properties: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if props == nil {
		props = make(map[string]string)
	}

	writeJSON(w, http.StatusOK, props)
}

func (h *PropertiesHandler) handleSetProperties(w http.ResponseWriter, r *http.Request) {
	name := urlparams.Vars(r)["name"]
	artifactIDEncoded := urlparams.Vars(r)["artifact_id"]
	ctx := r.Context()

	// Decode the artifact ID from URL encoding
	artifactID, err := url.PathUnescape(artifactIDEncoded)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid artifact_id")
		return
	}

	if msg := validateArtifactID(artifactID); msg != "" {
		writeError(w, http.StatusBadRequest, "bad_request", msg)
		return
	}

	repo, err := h.db.GetRepositoryByName(ctx, name)
	if err != nil {
		h.log.Errorw("set properties: db error getting repo", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if repo == nil {
		writeError(w, http.StatusNotFound, "not_found", "repository not found")
		return
	}

	body, msg := decodeProperties(r)
	if msg != "" {
		writeError(w, http.StatusBadRequest, "bad_request", msg)
		return
	}

	// Replace atomically: delete-all + insert in one transaction so a failure
	// midway can never leave the artifact with a partial property set.
	if err := h.db.SetArtifactProperties(ctx, repo.ID, artifactID, body, true); err != nil {
		h.log.Errorw("set properties: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if !h.applyLicensePolicy(w, r, repo.ID, artifactID, body) {
		return
	}

	writeJSON(w, http.StatusOK, body)
}

// applyLicensePolicy re-evaluates the license policies of an artifact whose
// license properties were just written, quarantining it when a blocking policy
// is violated (this is how a license that is only known after upload, e.g. for
// raw or imported artifacts, takes effect). It does nothing when props carry no
// license key. It returns false after answering 500 when the evaluation failed:
// the properties are stored, and repeating the request repeats the evaluation.
func (h *PropertiesHandler) applyLicensePolicy(w http.ResponseWriter, r *http.Request, repoID, artifactID string, props map[string]string) bool {
	touched := false

	for key := range props {
		if license.IsLicenseKey(key) {
			touched = true
			break
		}
	}

	if !touched {
		return true
	}

	ctx := r.Context()

	resolved, err := h.licenses.licenses(ctx, repoID, artifactID)
	if writeLicenseSourceError(w, err, "properties saved, but ") {
		return false
	}

	if err == nil {
		_, _, err = licensepolicy.Record(ctx, h.db, h.log, repoID, artifactID, resolved.Licenses, resolved.Source)
	}

	if err != nil {
		h.log.Errorw("properties: license policy evaluation failed", "repository_id", repoID, "artifact_id", artifactID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "properties saved, but the license policy could not be evaluated")

		return false
	}

	return true
}

func (h *PropertiesHandler) handlePatchProperties(w http.ResponseWriter, r *http.Request) {
	name := urlparams.Vars(r)["name"]
	artifactIDEncoded := urlparams.Vars(r)["artifact_id"]
	ctx := r.Context()

	// Decode the artifact ID from URL encoding
	artifactID, err := url.PathUnescape(artifactIDEncoded)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid artifact_id")
		return
	}

	if msg := validateArtifactID(artifactID); msg != "" {
		writeError(w, http.StatusBadRequest, "bad_request", msg)
		return
	}

	repo, err := h.db.GetRepositoryByName(ctx, name)
	if err != nil {
		h.log.Errorw("patch properties: db error getting repo", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if repo == nil {
		writeError(w, http.StatusNotFound, "not_found", "repository not found")
		return
	}

	body, msg := decodeProperties(r)
	if msg != "" {
		writeError(w, http.StatusBadRequest, "bad_request", msg)
		return
	}

	// Upsert all properties in one transaction.
	if err := h.db.SetArtifactProperties(ctx, repo.ID, artifactID, body, false); err != nil {
		h.log.Errorw("patch properties: db error setting properties", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if !h.applyLicensePolicy(w, r, repo.ID, artifactID, body) {
		return
	}

	// Return all properties
	props, err := h.db.ListArtifactProperties(ctx, repo.ID, artifactID)
	if err != nil {
		h.log.Errorw("patch properties: db error getting properties", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if props == nil {
		props = make(map[string]string)
	}

	writeJSON(w, http.StatusOK, props)
}

func (h *PropertiesHandler) handleDeleteProperty(w http.ResponseWriter, r *http.Request) {
	name := urlparams.Vars(r)["name"]
	artifactIDEncoded := urlparams.Vars(r)["artifact_id"]
	key := urlparams.Vars(r)["key"]
	ctx := r.Context()

	// Decode the artifact ID from URL encoding
	artifactID, err := url.PathUnescape(artifactIDEncoded)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid artifact_id")
		return
	}

	if msg := validateArtifactID(artifactID); msg != "" {
		writeError(w, http.StatusBadRequest, "bad_request", msg)
		return
	}

	repo, err := h.db.GetRepositoryByName(ctx, name)
	if err != nil {
		h.log.Errorw("delete property: db error getting repo", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if repo == nil {
		writeError(w, http.StatusNotFound, "not_found", "repository not found")
		return
	}

	if msg := validatePropertyKey(key); msg != "" {
		writeError(w, http.StatusBadRequest, "bad_request", msg)
		return
	}

	if err := h.db.DeleteArtifactProperty(ctx, repo.ID, artifactID, key); err != nil {
		h.log.Errorw("delete property: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	writeNoContent(w)
}

func (h *PropertiesHandler) handleSearchByProperty(w http.ResponseWriter, r *http.Request) {
	name := urlparams.Vars(r)["name"]
	ctx := r.Context()

	key := r.URL.Query().Get("key")
	value := r.URL.Query().Get("value")

	if key == "" || value == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "key and value query parameters are required")
		return
	}

	if len(key) > maxPropertyKeyLen || len(value) > maxPropertyValueLen {
		writeError(w, http.StatusBadRequest, "bad_request", "key or value is too long")
		return
	}

	repo, err := h.db.GetRepositoryByName(ctx, name)
	if err != nil {
		h.log.Errorw("search by property: db error getting repo", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if repo == nil {
		writeError(w, http.StatusNotFound, "not_found", "repository not found")
		return
	}

	artifactIDs, err := h.db.SearchByProperty(ctx, repo.ID, key, value)
	if err != nil {
		h.log.Errorw("search by property: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if artifactIDs == nil {
		artifactIDs = []string{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"artifact_ids": artifactIDs,
	})
}
