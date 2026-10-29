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
	"reflect"
	"regexp"
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
	"github.com/openartifact/openartifact/internal/pathseg"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/pkg/logger"
)

const storageTestTimeout = 10 * time.Second

var storageBackendNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)

// AdminStorageStore is the part of the database AdminStorageHandler needs.
type AdminStorageStore interface {
	CountBlobsOnStorageBackend(ctx context.Context, backendID string) (int64, error)
	CreateStorageBackend(ctx context.Context, backend *models.StorageBackend) error
	DeleteStorageBackend(ctx context.Context, id string) error
	GetStorageBackend(ctx context.Context, name string) (*models.StorageBackend, error)
	ListStorageBackends(ctx context.Context) ([]*models.StorageBackend, error)
	StorageBackendBusyReason(ctx context.Context, backendID string) (string, error)
	StorageTierUsage(ctx context.Context) ([]models.TierUsage, error)
	UpdateStorageBackend(ctx context.Context, backend *models.StorageBackend) error
	UpdateStorageBackendStatus(ctx context.Context, id string, status string, message *string) error
}

type AdminStorageHandler struct {
	db        AdminStorageStore
	log       *logger.Logger
	evaluator *auth.Evaluator

	// primary is the configured primary store; nil when none is wired (tests).
	primary *primaryStore
}

func NewAdminStorageHandler(db AdminStorageStore, log *logger.Logger, evaluator *auth.Evaluator) *AdminStorageHandler {
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

// reservedStorageBackendName reports whether name is a path segment with a
// static route under /api/v1/admin/storage. Those win over /{name}, so a record
// with such a name would be unreachable by name: they cannot be given to a
// record, on create or on rename. Keep the set in step with those routes (a
// test walks the router and fails on a static segment missing here).
//
// A record that already has such a name (the check came later than the name)
// stays in the list and keeps working where the static routes do not take the
// method: PATCH and DELETE reach it, so it can be renamed or deleted by name
// (for "primary" through handlePrimaryUpdate and handlePrimaryDelete). GET, and the test action of "primary",
// answer with the static route's response; the test action of a record named
// "replication" still reaches it.
func reservedStorageBackendName(name string) bool {
	return name == primaryStorageName || name == replicationStorageSegment
}

// validStorageBackendName writes the 400 for a malformed or reserved name.
func validStorageBackendName(w http.ResponseWriter, name string) bool {
	switch {
	case !storageBackendNameRe.MatchString(name):
		writeError(w, http.StatusBadRequest, "validation_failed", "name must be 1-128 characters: letters, digits, '.', '_' or '-'")
	case reservedStorageBackendName(name):
		writeError(w, http.StatusBadRequest, "validation_failed", "name '"+name+"' is reserved")
	default:
		return true
	}

	return false
}

func storageKindList() string {
	return "filesystem, s3, gcs, azure, garage, seaweedfs, oci"
}

func (h *AdminStorageHandler) Register(r chi.Router) {
	r.Get("/api/v1/admin/storage", h.handleList)
	r.Post("/api/v1/admin/storage", h.handleCreate)
	// The primary store is the server configuration, not a row: these static
	// paths take precedence over /{name}, so the name is reserved.
	r.Get("/api/v1/admin/storage/"+primaryStorageName, h.handleGetPrimary)
	r.Patch("/api/v1/admin/storage/"+primaryStorageName, h.handlePrimaryUpdate)
	r.Delete("/api/v1/admin/storage/"+primaryStorageName, h.handlePrimaryDelete)
	r.Post("/api/v1/admin/storage/"+primaryStorageName+"/test", h.handleTestPrimary)
	r.Get("/api/v1/admin/storage/{name}", h.handleGet)
	r.Patch("/api/v1/admin/storage/{name}", h.handleUpdate)
	r.Delete("/api/v1/admin/storage/{name}", h.handleDelete)
	r.Post("/api/v1/admin/storage/{name}/test", h.handleTest)
}

// writeStorageWriteError maps persistence errors to 409 on unique violations.
func (h *AdminStorageHandler) writeStorageWriteError(w http.ResponseWriter, err error, action string) {
	if database.UniqueViolationConstraint(err) != "" {
		writeError(w, http.StatusConflict, "conflict", "a storage backend with this name already exists")

		return
	}

	h.log.Errorw("storage backend "+action+" failed", "error", err)
	writeError(w, http.StatusInternalServerError, "internal_error", "failed to "+action+" storage backend")
}

// prepareStorageConfig validates raw for kind, encrypts secret fields and
// carries over secrets from existing (the stored config) that raw omits. It
// returns the JSON to store, or a validation error message.
func prepareStorageConfig(ctx context.Context, kind string, raw, existing []byte) ([]byte, error) {
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

	if err := validateStorageConfig(ctx, kind, cfg); err != nil {
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

func validateStorageConfig(ctx context.Context, kind string, cfg map[string]any) error {
	switch kind {
	case "filesystem":
		path := cfgString(cfg, "path")
		if path == "" {
			return validationError("config.path is required for filesystem backends")
		}

		if pathseg.HasParent(path) {
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
			if err := netutil.ValidateOutboundURLContext(ctx, endpoint); err != nil {
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

		if err := netutil.ValidateOutboundURLContext(ctx, endpoint); err != nil {
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
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	backends, err := h.db.ListStorageBackends(r.Context())
	if err != nil {
		h.log.Errorw("failed to list storage backends", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to list storage backends")

		return
	}

	usage, err := h.backendUsage(r.Context())
	if err != nil {
		h.log.Errorw("failed to read storage backend usage", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to list storage backends")

		return
	}

	data := make([]responses.StorageBackendResponse, len(backends))
	for i, b := range backends {
		data[i] = storageBackendToResponse(b)
		data[i].Usage = usage.of(b.ID)
	}

	resp := map[string]any{
		"total": len(data),
		"items": data,
	}

	if h.primary != nil {
		resp["primary"] = h.primary.response(r.Context(), usage.of(""), false)
	}

	writeJSON(w, http.StatusOK, resp)
}

func (h *AdminStorageHandler) handleCreate(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	var body struct {
		Name   string          `json:"name"`
		Kind   string          `json:"kind"`
		Config json.RawMessage `json:"config"`
	}

	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}

	if !validStorageBackendName(w, body.Name) {
		return
	}

	if !validStorageKinds[body.Kind] {
		writeError(w, http.StatusBadRequest, "validation_failed", "kind must be one of: "+storageKindList())
		return
	}

	config, err := prepareStorageConfig(r.Context(), body.Kind, body.Config, nil)
	if err != nil {
		writeError(w, http.StatusBadRequest, "validation_failed", err.Error())
		return
	}

	now := time.Now()
	backend := &models.StorageBackend{
		ID:        uuid.New().String(),
		Name:      body.Name,
		Kind:      body.Kind,
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
	if !requireAdmin(w, r, h.evaluator) {
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

	usage, err := h.backendUsage(r.Context())
	if err != nil {
		h.log.Errorw("failed to read storage backend usage", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to get storage backend")

		return
	}

	resp := storageBackendToResponse(backend)
	resp.Usage = usage.of(backend.ID)

	writeJSON(w, http.StatusOK, resp)
}

// backendUsageByID maps a storage backend id to what it holds.
type backendUsageByID map[string]models.TierUsage

// of returns the usage of one backend; a backend holding nothing reports zeros.
func (u backendUsageByID) of(id string) *responses.StorageBackendUsage {
	t := u[id]
	return &responses.StorageBackendUsage{BlobCount: t.Blobs, SizeBytes: t.Bytes}
}

func (h *AdminStorageHandler) backendUsage(ctx context.Context) (backendUsageByID, error) {
	rows, err := h.db.StorageTierUsage(ctx)
	if err != nil {
		return nil, fmt.Errorf("storage backend usage: %w", err)
	}

	out := make(backendUsageByID, len(rows))
	for _, u := range rows {
		out[u.BackendID] = u
	}

	return out, nil
}

func (h *AdminStorageHandler) handleUpdate(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
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
		Name   *string          `json:"name"`
		Kind   *string          `json:"kind"`
		Config *json.RawMessage `json:"config"`
	}

	if err := decodeJSON(r, &patch); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}

	if patch.Name != nil {
		if !validStorageBackendName(w, *patch.Name) {
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

	// Always re-validate the effective config: the kind may have changed even
	// when no config was sent. Omitted secrets are preserved.
	var raw []byte
	if patch.Config != nil {
		raw = *patch.Config
	} else {
		raw = backend.Config
	}

	config, err := prepareStorageConfig(r.Context(), backend.Kind, raw, backend.Config)
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
	if !requireAdmin(w, r, h.evaluator) {
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
	if !requireAdmin(w, r, h.evaluator) {
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
	case "gcs":
		result = h.testGCSBackend(ctx, backend.Name, cfg)
	case "azure":
		result = h.testAzureBackend(ctx, backend.Name, cfg)
	default:
		result = storageTestResult{false, "invalid_config", "unknown backend kind: " + backend.Kind}
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
		if err := netutil.ValidateOutboundURLContext(ctx, endpoint); err != nil {
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

// testGCSBackend checks the bucket through the GCS XML API with the stored HMAC credentials.
func (h *AdminStorageHandler) testGCSBackend(ctx context.Context, name string, cfg map[string]any) storageTestResult {
	bucket := cfgString(cfg, "bucket")
	if bucket == "" {
		return storageTestResult{false, "invalid_config", "bucket is required in config"}
	}

	hmacKey, err := decryptedSecret(cfg, "hmac_key", "hmacKey")
	if err != nil {
		h.log.Errorw("storage test: decrypt failed", "backend", name, "error", err)
		return storageTestResult{false, "invalid_config", "stored credentials could not be decrypted"}
	}

	hmacSecret, err := decryptedSecret(cfg, "hmac_secret", "hmacSecret")
	if err != nil {
		h.log.Errorw("storage test: decrypt failed", "backend", name, "error", err)
		return storageTestResult{false, "invalid_config", "stored credentials could not be decrypted"}
	}

	store, err := storage.NewGCSBlobStore(storage.StorageConfig{GCSBucket: bucket, GCSHMACKey: hmacKey, GCSHMACSecret: hmacSecret})
	if err != nil {
		return storageTestResult{false, "invalid_config", err.Error()}
	}

	return h.healthCheckStore(ctx, name, "gcs", store)
}

// testAzureBackend checks the container with the stored shared-key credentials.
func (h *AdminStorageHandler) testAzureBackend(ctx context.Context, name string, cfg map[string]any) storageTestResult {
	accountKey, err := decryptedSecret(cfg, "account_key", "accountKey")
	if err != nil {
		h.log.Errorw("storage test: decrypt failed", "backend", name, "error", err)
		return storageTestResult{false, "invalid_config", "stored credentials could not be decrypted"}
	}

	store, err := storage.NewAzureBlobStore(storage.StorageConfig{
		AzureAccountName:   cfgString(cfg, "account", "account_name", "accountName"),
		AzureContainerName: cfgString(cfg, "container"),
		AzureAccountKey:    accountKey,
	})
	if err != nil {
		return storageTestResult{false, "invalid_config", err.Error()}
	}

	return h.healthCheckStore(ctx, name, "azure", store)
}

// healthCheckStore runs the backend's own connectivity check.
func (h *AdminStorageHandler) healthCheckStore(
	ctx context.Context,
	name, kind string,
	store interface{ HealthCheck(context.Context) error },
) storageTestResult {
	if err := store.HealthCheck(ctx); err != nil {
		h.log.Warnw("storage test: health check failed", "backend", name, "kind", kind, "error", err)
		return storageTestResult{
			false,
			"connection_failed",
			"could not access the " + kind + " bucket or container with the configured credentials",
		}
	}

	return storageTestResult{ok: true}
}

func (h *AdminStorageHandler) testOCIBackend(ctx context.Context, name string, cfg map[string]any) storageTestResult {
	endpoint := ociEndpoint(cfg)
	if endpoint == "" {
		return storageTestResult{false, "invalid_config", "endpoint is required in config"}
	}

	if err := netutil.ValidateOutboundURLContext(ctx, endpoint); err != nil {
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
		Config:        config,
		Status:        b.Status,
		StatusMessage: b.StatusMessage,
		LastCheckedAt: formatTimePtr(b.LastCheckedAt),
		CreatedAt:     formatTime(b.CreatedAt),
		UpdatedAt:     formatTime(b.UpdatedAt),
	}
}
