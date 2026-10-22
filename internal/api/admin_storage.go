// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/openartifact/openartifact/internal/api/responses"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/crypto"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/netutil"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/pkg/logger"
)

const storageTestTimeout = 10 * time.Second

var storageBackendNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)

type AdminStorageHandler struct {
	db        database.Database
	log       *logger.Logger
	evaluator *auth.Evaluator
}

func NewAdminStorageHandler(db database.Database, log *logger.Logger, evaluator *auth.Evaluator) *AdminStorageHandler {
	return &AdminStorageHandler{db: db, log: log, evaluator: evaluator}
}

//nolint:gochecknoglobals // immutable lookup table
var validStorageKinds = map[string]bool{
	"filesystem": true,
	"s3":         true,
	"gcs":        true,
	"azure":      true,
	"garage":     true,
	"seaweedfs":  true,
	"oci":        true,
}

// storageSecretFields are the config keys that hold credentials. They are
// encrypted at rest, never returned by the API (a has_<key> flag is returned
// instead) and preserved across updates that omit them.
//
//nolint:gochecknoglobals // immutable lookup table
var storageSecretFields = []string{
	"access_key", "accessKey", "secret_key", "secretKey", "password", "account_key", "accountKey",
	"connStr", "conn_str", "creds", "credentials_json", "sas_token", "hmac_key", "hmac_secret",
}

func storageKindList() string {
	return "filesystem, s3, gcs, azure, garage, seaweedfs, oci"
}

func (h *AdminStorageHandler) Register(r chi.Router) {
	r.Get("/api/v1/admin/storage", h.handleList)
	r.Post("/api/v1/admin/storage", h.handleCreate)
	r.Get("/api/v1/admin/storage/{name}", h.handleGet)
	r.Patch("/api/v1/admin/storage/{name}", h.handleUpdate)
	r.Delete("/api/v1/admin/storage/{name}", h.handleDelete)
	r.Post("/api/v1/admin/storage/{name}/test", h.handleTest)
}

func (h *AdminStorageHandler) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	p := currentPrincipal(r)

	isAdmin, err := principalIsAdmin(r.Context(), p, h.evaluator)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "auth evaluation failed")
		return false
	}

	if !isAdmin {
		writeError(w, http.StatusForbidden, "forbidden", "admin access required")
		return false
	}

	return true
}

// writeStorageWriteError maps persistence errors to 409 on unique violations.
func (h *AdminStorageHandler) writeStorageWriteError(w http.ResponseWriter, err error, action string) {
	if constraint := database.UniqueViolationConstraint(err); constraint != "" {
		msg := "a storage backend with this name already exists"
		if constraint == "idx_storage_backends_default" {
			msg = "another storage backend is already the default"
		}

		writeError(w, http.StatusConflict, "conflict", msg)

		return
	}

	h.log.Errorw("storage backend "+action+" failed", "error", err)
	writeError(w, http.StatusInternalServerError, "internal_error", "failed to "+action+" storage backend")
}

// prepareStorageConfig validates raw for kind, encrypts secret fields and
// carries over secrets from existing (the stored config) that raw omits. It
// returns the JSON to store, or a validation error message.
func prepareStorageConfig(kind string, raw, existing []byte) ([]byte, error) {
	cfg := map[string]any{}

	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return nil, validationError("config must be a JSON object")
		}
	}

	old := map[string]any{}
	if len(existing) > 0 {
		_ = json.Unmarshal(existing, &old)
	}

	for _, field := range storageSecretFields {
		v, present := cfg[field]
		if present && v != nil {
			s, ok := v.(string)
			if !ok {
				return nil, validationError("config." + field + " must be a string")
			}

			if s != "" {
				if err := crypto.CheckPlaintext(s); err != nil {
					return nil, validationError("config." + field + " must be the plaintext secret")
				}

				enc, err := crypto.Encrypt(s)
				if err != nil {
					return nil, validationError("failed to protect config." + field)
				}

				cfg[field] = enc

				continue
			}
		}

		// Omitted or empty: keep the stored secret, if any.
		if prev, ok := old[field].(string); ok && prev != "" {
			cfg[field] = prev
		} else {
			delete(cfg, field)
		}
	}

	if err := validateStorageConfig(kind, cfg); err != nil {
		return nil, err
	}

	out, err := json.Marshal(cfg)
	if err != nil {
		return nil, validationError("config could not be encoded")
	}

	return out, nil
}

// cfgString returns the first non-empty string value among keys. Several keys
// are accepted because the admin UI uses camelCase names while API clients
// use snake_case.
func cfgString(cfg map[string]any, keys ...string) string {
	for _, key := range keys {
		if s, _ := cfg[key].(string); strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}

	return ""
}

func cfgBool(cfg map[string]any, keys ...string) bool {
	for _, key := range keys {
		if b, ok := cfg[key].(bool); ok && b {
			return true
		}
	}

	return false
}

// ociEndpoint returns an absolute URL for an OCI endpoint; the UI stores a
// bare host[:port], which is completed with https (http when insecure is set).
func ociEndpoint(cfg map[string]any) string {
	endpoint := strings.TrimRight(cfgString(cfg, "endpoint"), "/")
	if endpoint == "" || strings.Contains(endpoint, "://") {
		return endpoint
	}

	if cfgBool(cfg, "insecure", "oci_insecure") {
		return "http://" + endpoint
	}

	return "https://" + endpoint
}

func validateStorageConfig(kind string, cfg map[string]any) error {
	switch kind {
	case "filesystem":
		path := cfgString(cfg, "path")
		if path == "" {
			return validationError("config.path is required for filesystem backends")
		}

		if slices.Contains(strings.Split(filepath.ToSlash(path), "/"), "..") {
			return validationError("config.path must not contain '..'")
		}
	case "s3", "garage", "seaweedfs":
		if cfgString(cfg, "bucket") == "" {
			return validationError("config.bucket is required")
		}

		endpoint := cfgString(cfg, "endpoint")
		if endpoint == "" && kind != "s3" {
			return validationError("config.endpoint is required for " + kind + " backends")
		}

		if endpoint != "" {
			if err := netutil.ValidateOutboundURL(endpoint); err != nil {
				return fmt.Errorf("config.endpoint is not allowed: %w", err)
			}
		}

		hasAccess := cfgString(cfg, "access_key", "accessKey") != ""
		hasSecret := cfgString(cfg, "secret_key", "secretKey") != ""

		if hasAccess != hasSecret {
			return validationError("config.access_key and config.secret_key must be set together")
		}
	case "oci":
		endpoint := ociEndpoint(cfg)
		if endpoint == "" {
			return validationError("config.endpoint is required for oci backends")
		}

		if err := netutil.ValidateOutboundURL(endpoint); err != nil {
			return fmt.Errorf("config.endpoint is not allowed: %w", err)
		}
	case "gcs":
		if cfgString(cfg, "bucket") == "" {
			return validationError("config.bucket is required for gcs backends")
		}
	case "azure":
		if cfgString(cfg, "account", "account_name") == "" || cfgString(cfg, "container") == "" {
			return validationError("config.account and config.container are required for azure backends")
		}
	}

	return nil
}

func (h *AdminStorageHandler) handleList(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}

	backends, err := h.db.ListStorageBackends(r.Context())
	if err != nil {
		h.log.Errorw("failed to list storage backends", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to list storage backends")

		return
	}

	data := make([]responses.StorageBackendResponse, len(backends))
	for i, b := range backends {
		data[i] = storageBackendToResponse(b)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"total": len(data),
		"items": data,
	})
}

func (h *AdminStorageHandler) handleCreate(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}

	var body struct {
		Name      string          `json:"name"`
		Kind      string          `json:"kind"`
		IsDefault bool            `json:"is_default"`
		Config    json.RawMessage `json:"config"`
	}

	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}

	if !storageBackendNameRe.MatchString(body.Name) {
		writeError(w, http.StatusBadRequest, "validation_failed", "name must be 1-128 characters: letters, digits, '.', '_' or '-'")
		return
	}

	if !validStorageKinds[body.Kind] {
		writeError(w, http.StatusBadRequest, "validation_failed", "kind must be one of: "+storageKindList())
		return
	}

	config, err := prepareStorageConfig(body.Kind, body.Config, nil)
	if err != nil {
		writeError(w, http.StatusBadRequest, "validation_failed", err.Error())
		return
	}

	now := time.Now()
	backend := &models.StorageBackend{
		ID:        uuid.New().String(),
		Name:      body.Name,
		Kind:      body.Kind,
		IsDefault: body.IsDefault,
		Config:    config,
		Status:    "unknown",
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := h.db.CreateStorageBackend(r.Context(), backend); err != nil {
		h.writeStorageWriteError(w, err, "create")
		return
	}

	writeJSON(w, http.StatusCreated, storageBackendToResponse(backend))
}

func (h *AdminStorageHandler) handleGet(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}

	name := urlparams.Vars(r)["name"]

	backend, err := h.db.GetStorageBackend(r.Context(), name)
	if err != nil {
		h.log.Errorw("failed to get storage backend", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to get storage backend")

		return
	}

	if backend == nil {
		writeError(w, http.StatusNotFound, "not_found", "storage backend not found")
		return
	}

	writeJSON(w, http.StatusOK, storageBackendToResponse(backend))
}

func (h *AdminStorageHandler) handleUpdate(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}

	name := urlparams.Vars(r)["name"]

	backend, err := h.db.GetStorageBackend(r.Context(), name)
	if err != nil {
		h.log.Errorw("failed to get storage backend", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to get storage backend")

		return
	}

	if backend == nil {
		writeError(w, http.StatusNotFound, "not_found", "storage backend not found")
		return
	}

	origKind, origConfig := backend.Kind, backend.Config

	var patch struct {
		Name      *string          `json:"name"`
		Kind      *string          `json:"kind"`
		IsDefault *bool            `json:"is_default"`
		Config    *json.RawMessage `json:"config"`
	}

	if err := decodeJSON(r, &patch); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}

	if patch.Name != nil {
		if !storageBackendNameRe.MatchString(*patch.Name) {
			writeError(w, http.StatusBadRequest, "validation_failed", "name must be 1-128 characters: letters, digits, '.', '_' or '-'")
			return
		}

		backend.Name = *patch.Name
	}

	if patch.Kind != nil {
		if !validStorageKinds[*patch.Kind] {
			writeError(w, http.StatusBadRequest, "validation_failed", "kind must be one of: "+storageKindList())
			return
		}

		backend.Kind = *patch.Kind
	}

	if patch.IsDefault != nil {
		backend.IsDefault = *patch.IsDefault
	}

	// Always re-validate the effective config: the kind may have changed even
	// when no config was sent. Omitted secrets are preserved.
	var raw []byte
	if patch.Config != nil {
		raw = *patch.Config
	} else {
		raw = backend.Config
	}

	config, err := prepareStorageConfig(backend.Kind, raw, backend.Config)
	if err != nil {
		writeError(w, http.StatusBadRequest, "validation_failed", err.Error())
		return
	}

	// Blobs are located through the backend's kind and location config (credentials excluded, so they can rotate): re-pointing a
	// backend that holds blobs, or that a tier or move journal row names, would
	// orphan them.
	if backend.Kind != origKind || !storageLocationEqual(config, origConfig) {
		reason, err := h.db.StorageBackendBusyReason(r.Context(), backend.ID)
		if err != nil {
			h.log.Errorw("failed to check storage backend usage", "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "failed to update storage backend")

			return
		}

		if reason != "" {
			writeError(w, http.StatusConflict, "conflict", "cannot change kind or config: "+reason)
			return
		}
	}

	backend.Config = config
	backend.UpdatedAt = time.Now()

	if err := h.db.UpdateStorageBackend(r.Context(), backend); err != nil {
		h.writeStorageWriteError(w, err, "update")
		return
	}

	writeJSON(w, http.StatusOK, storageBackendToResponse(backend))
}

// storageLocationEqual compares two backend configs by where the blobs live
// (path, endpoint, bucket, region, prefix, account, container, OCI repo, ...):
// every key except the credentials (storageSecretFields, whose ciphertext
// changes on every write, and username), so rotating a secret is never a
// location change. Documents are compared structurally.
func storageLocationEqual(a, b []byte) bool {
	x, okA := storageLocation(a)
	y, okB := storageLocation(b)

	if !okA || !okB {
		return bytes.Equal(a, b)
	}

	return reflect.DeepEqual(x, y)
}

func storageLocation(raw []byte) (map[string]any, bool) {
	cfg := map[string]any{}

	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return nil, false
		}
	}

	for _, f := range storageSecretFields {
		delete(cfg, f)
	}

	delete(cfg, "username")

	return cfg, true
}

func (h *AdminStorageHandler) handleDelete(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}

	name := urlparams.Vars(r)["name"]

	backend, err := h.db.GetStorageBackend(r.Context(), name)
	if err != nil {
		h.log.Errorw("failed to get storage backend", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to get storage backend")

		return
	}

	if backend == nil {
		writeError(w, http.StatusNotFound, "not_found", "storage backend not found")
		return
	}

	if backend.IsDefault {
		writeError(w, http.StatusBadRequest, "cannot_delete_default", "cannot delete the default storage backend")
		return
	}

	inUse, err := h.db.CountBlobsOnStorageBackend(r.Context(), backend.ID)
	if err != nil {
		h.log.Errorw("failed to count blobs on storage backend", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to delete storage backend")

		return
	}

	if inUse > 0 {
		writeError(w, http.StatusConflict, "conflict", "storage backend still holds blobs")
		return
	}

	if err := h.db.DeleteStorageBackend(r.Context(), backend.ID); err != nil {
		if database.IsForeignKeyViolation(err) {
			writeError(w, http.StatusConflict, "conflict", "storage backend is used by a storage tier or an in-progress move")
			return
		}

		h.log.Errorw("failed to delete storage backend", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to delete storage backend")

		return
	}

	writeNoContent(w)
}

// storageTestResult is the outcome of a backend connectivity test. message is
// always a fixed, non-sensitive string; raw errors are logged only.
type storageTestResult struct {
	ok      bool
	code    string
	message string
}

func (h *AdminStorageHandler) handleTest(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}

	name := urlparams.Vars(r)["name"]

	backend, err := h.db.GetStorageBackend(r.Context(), name)
	if err != nil {
		h.log.Errorw("failed to get storage backend", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to get storage backend")

		return
	}

	if backend == nil {
		writeError(w, http.StatusNotFound, "not_found", "storage backend not found")
		return
	}

	cfg := map[string]any{}
	if len(backend.Config) > 0 {
		if err := json.Unmarshal(backend.Config, &cfg); err != nil {
			cfg = map[string]any{}
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), storageTestTimeout)
	defer cancel()

	var result storageTestResult

	switch backend.Kind {
	case "filesystem":
		result = h.testFilesystemBackend(cfg)
	case "s3", "garage", "seaweedfs":
		result = h.testS3Backend(ctx, backend.Name, cfg)
	case "oci":
		result = h.testOCIBackend(ctx, backend.Name, cfg)
	default:
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":      false,
			"error":   "not_implemented",
			"message": "testing for this backend type is not yet supported",
		})

		return
	}

	if result.ok {
		if err := h.db.UpdateStorageBackendStatus(r.Context(), backend.ID, "healthy", nil); err != nil {
			h.log.Errorw("failed to update storage backend status", "error", err)
		}
	} else {
		msg := result.message
		if err := h.db.UpdateStorageBackendStatus(r.Context(), backend.ID, "unreachable", &msg); err != nil {
			h.log.Errorw("failed to update storage backend status", "error", err)
		}
	}

	response := map[string]any{"ok": result.ok}
	if !result.ok {
		response["error"] = result.code
		response["message"] = result.message
	}

	writeJSON(w, http.StatusOK, response)
}

func (h *AdminStorageHandler) testFilesystemBackend(cfg map[string]any) storageTestResult {
	path := cfgString(cfg, "path")
	if path == "" {
		return storageTestResult{false, "invalid_config", "path is required in config"}
	}

	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return storageTestResult{false, "path_not_found", "storage path does not exist or is not a directory"}
	}

	tmpFile, err := os.CreateTemp(path, ".test-*")
	if err != nil {
		return storageTestResult{false, "not_writable", "storage path is not writable"}
	}

	_ = tmpFile.Close()
	_ = os.Remove(tmpFile.Name())

	return storageTestResult{ok: true}
}

// decryptedSecret returns the plaintext of a stored (possibly encrypted) secret.
func decryptedSecret(cfg map[string]any, keys ...string) (string, error) {
	stored := cfgString(cfg, keys...)
	if stored == "" {
		return "", nil
	}

	plain, err := crypto.Decrypt(stored)
	if err != nil {
		return "", fmt.Errorf("decrypt %s: %w", keys[0], err)
	}

	return plain, nil
}

// testS3Backend performs a real HeadBucket against the configured bucket using
// the configured credentials.
func (h *AdminStorageHandler) testS3Backend(ctx context.Context, name string, cfg map[string]any) storageTestResult {
	endpoint := cfgString(cfg, "endpoint")
	bucket := cfgString(cfg, "bucket")

	if bucket == "" {
		return storageTestResult{false, "invalid_config", "bucket is required in config"}
	}

	if endpoint != "" {
		if err := netutil.ValidateOutboundURL(endpoint); err != nil {
			return storageTestResult{false, "invalid_config", "endpoint is not allowed"}
		}
	}

	accessKey, err := decryptedSecret(cfg, "access_key", "accessKey")
	if err != nil {
		h.log.Errorw("storage test: decrypt failed", "backend", name, "error", err)
		return storageTestResult{false, "invalid_config", "stored credentials could not be decrypted"}
	}

	secretKey, err := decryptedSecret(cfg, "secret_key", "secretKey")
	if err != nil {
		h.log.Errorw("storage test: decrypt failed", "backend", name, "error", err)
		return storageTestResult{false, "invalid_config", "stored credentials could not be decrypted"}
	}

	store, err := storage.NewS3BlobStore(storage.StorageConfig{
		Endpoint:       endpoint,
		Bucket:         bucket,
		Region:         cfgString(cfg, "region"),
		AccessKey:      accessKey,
		SecretKey:      secretKey,
		ForcePathStyle: endpoint != "" || cfgBool(cfg, "pathStyle", "force_path_style"),
	})
	if err != nil {
		h.log.Errorw("storage test: s3 client init failed", "backend", name, "error", err)
		return storageTestResult{false, "invalid_config", "could not initialise the S3 client"}
	}

	if err := store.HealthCheck(ctx); err != nil {
		h.log.Warnw("storage test: s3 bucket check failed", "backend", name, "error", err)
		return storageTestResult{false, "connection_failed", "could not access the bucket with the configured endpoint and credentials"}
	}

	return storageTestResult{ok: true}
}

func (h *AdminStorageHandler) testOCIBackend(ctx context.Context, name string, cfg map[string]any) storageTestResult {
	endpoint := ociEndpoint(cfg)
	if endpoint == "" {
		return storageTestResult{false, "invalid_config", "endpoint is required in config"}
	}

	if err := netutil.ValidateOutboundURL(endpoint); err != nil {
		return storageTestResult{false, "invalid_config", "endpoint is not allowed"}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/v2/", nil)
	if err != nil {
		return storageTestResult{false, "invalid_config", "endpoint is not a valid URL"}
	}

	if username := cfgString(cfg, "username"); username != "" {
		password, err := decryptedSecret(cfg, "password")
		if err != nil {
			h.log.Errorw("storage test: decrypt failed", "backend", name, "error", err)
			return storageTestResult{false, "invalid_config", "stored credentials could not be decrypted"}
		}

		req.SetBasicAuth(username, password)
	}

	resp, err := netutil.SafeHTTPClient(storageTestTimeout).Do(req)
	if err != nil {
		h.log.Warnw("storage test: oci request failed", "backend", name, "error", err)
		return storageTestResult{false, "connection_failed", "could not connect to the OCI endpoint"}
	}

	defer func() { _ = resp.Body.Close() }()

	// A registry answers /v2/ with 200, or 401 when it requires authentication.
	// With credentials supplied, 401 means they were rejected.
	switch {
	case resp.StatusCode == http.StatusOK:
		return storageTestResult{ok: true}
	case resp.StatusCode == http.StatusUnauthorized && req.Header.Get("Authorization") == "":
		return storageTestResult{ok: true}
	case resp.StatusCode == http.StatusUnauthorized:
		return storageTestResult{false, "auth_failed", "the registry rejected the configured credentials"}
	default:
		return storageTestResult{false, "invalid_endpoint", "endpoint does not appear to be a valid OCI registry"}
	}
}

// storageBackendToResponse converts a backend to its API form. Secret config
// fields are never returned; a has_<field> flag reports whether one is stored.
func storageBackendToResponse(b *models.StorageBackend) responses.StorageBackendResponse {
	config := map[string]any{}
	if len(b.Config) > 0 {
		_ = json.Unmarshal(b.Config, &config)
	}

	for _, field := range storageSecretFields {
		if v, ok := config[field]; ok {
			delete(config, field)

			if s, isStr := v.(string); isStr && s != "" {
				config["has_"+field] = true
			}
		}
	}

	return responses.StorageBackendResponse{
		ID:            b.ID,
		Name:          b.Name,
		Kind:          b.Kind,
		IsDefault:     b.IsDefault,
		Config:        config,
		Status:        b.Status,
		StatusMessage: b.StatusMessage,
		LastCheckedAt: formatTimePtr(b.LastCheckedAt),
		CreatedAt:     formatTime(b.CreatedAt),
		UpdatedAt:     formatTime(b.UpdatedAt),
	}
}
