// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package gomod

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	"github.com/openartifact/openartifact/internal/presign"
	"github.com/openartifact/openartifact/internal/quarantine"
	"github.com/openartifact/openartifact/internal/quota"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/internal/webhook"
	"github.com/openartifact/openartifact/pkg/logger"
	xmod "golang.org/x/mod/module"
)

const maxGoModUploadBytes = 100 << 20

type HTTPHandler struct {
	*base.Handler
}

func NewHTTPHandler(log *logger.Logger, db database.Database, blobStore storage.BlobStore, baseURL string) *HTTPHandler {
	return &HTTPHandler{Handler: base.New(log, db, blobStore, baseURL)}
}

// RegisterRepoRoutes registers the GOPROXY GET endpoint relative to the shared
// /repos/{name} subrouter. Module paths contain slashes, which chi's
// {name:regexp} params never span, so a single catch-all is dispatched by
// suffix; the more specific routes of the other formats still win.
//
// PUT is not registered here: the subrouter's PUT "/*" is shared with the npm
// publish route, so the caller dispatches with IsUploadPath and HandlePut.
func (h *HTTPHandler) RegisterRepoRoutes(r chi.Router) {
	r.Get("/*", h.dispatchGet)
}

// IsUploadPath reports whether a /repos/{name}/ request tail is a GOPROXY
// upload target ("<module>/@v/<file>").
func IsUploadPath(tail string) bool {
	t, ok := splitModuleTail(tail)
	return ok && !t.latest
}

// HandlePut serves a GOPROXY upload. Callers route here only when
// IsUploadPath matched the request tail.
func (h *HTTPHandler) HandlePut(w http.ResponseWriter, r *http.Request) {
	h.dispatchPut(w, r)
}

// moduleTail is a parsed GOPROXY request tail.
type moduleTail struct {
	module string
	rest   string // part after "/@v/"
	latest bool
}

// splitModuleTail splits a request tail ("<module>/@v/<file>" or
// "<module>/@latest") into the module path and the part after "/@v/".
func splitModuleTail(tail string) (moduleTail, bool) {
	if m, found := strings.CutSuffix(tail, "/@latest"); found {
		return moduleTail{module: m, latest: true}, m != ""
	}

	module, rest, ok := strings.Cut(tail, "/@v/")

	return moduleTail{module: module, rest: rest}, ok && module != "" && rest != ""
}

// setModuleVars exposes the parsed module path (and version) to the handlers
// through the chi route context, where they read their URL parameters.
func setModuleVars(r *http.Request, module, version string) {
	rctx := chi.RouteContext(r.Context())
	if rctx == nil {
		return
	}

	rctx.URLParams.Add("module", module)

	if version != "" {
		rctx.URLParams.Add("version", version)
	}
}

func (h *HTTPHandler) dispatchGet(w http.ResponseWriter, r *http.Request) {
	tail, ok := splitModuleTail(urlparams.Vars(r)["*"])
	if !ok {
		http.NotFound(w, r)
		return
	}

	// The module path and version travel case-encoded in the URL ("!x" = "X");
	// every handler below works with the decoded values.
	modulePath, err := xmod.UnescapePath(tail.module)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	if tail.latest {
		setModuleVars(r, modulePath, "")
		h.handleLatest(w, r)

		return
	}

	if tail.rest == "list" {
		setModuleVars(r, modulePath, "")
		h.handleList(w, r)

		return
	}

	handlers := [...]struct {
		ext    string
		handle http.HandlerFunc
	}{{".info", h.handleInfo}, {".mod", h.handleMod}, {".zip", h.handleZip}}

	for _, e := range handlers {
		encodedVersion, found := strings.CutSuffix(tail.rest, e.ext)
		if !found || encodedVersion == "" {
			continue
		}

		version, err := xmod.UnescapeVersion(encodedVersion)
		if err != nil {
			http.NotFound(w, r)
			return
		}

		setModuleVars(r, modulePath, version)
		e.handle(w, r)

		return
	}

	http.NotFound(w, r)
}

func (h *HTTPHandler) dispatchPut(w http.ResponseWriter, r *http.Request) {
	tail, ok := splitModuleTail(urlparams.Vars(r)["*"])
	if !ok || tail.latest {
		http.NotFound(w, r)
		return
	}

	modulePath, err := xmod.UnescapePath(tail.module)
	if err != nil {
		http.Error(w, "invalid module path", http.StatusBadRequest)
		return
	}

	version, err := xmod.UnescapeVersion(tail.rest)
	if err != nil || !validVersion(version) {
		http.Error(w, "invalid module version", http.StatusBadRequest)
		return
	}

	setModuleVars(r, modulePath, version)
	h.handleUpload(w, r)
}

func (h *HTTPHandler) handleList(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	repoName := vars["name"]
	modulePath := vars["module"]

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

	versions, err := h.DB.ListGoModuleVersions(r.Context(), repoID, modulePath)
	if err != nil {
		h.Log.Errorw("failed to list go module versions", "module", modulePath, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	versions, err = h.dropQuarantined(r.Context(), repoID, modulePath, versions)
	if err != nil {
		h.Log.Errorw("failed to check quarantine", "module", modulePath, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if len(versions) == 0 {
		http.NotFound(w, r)
		return
	}

	// Newline-separated list, highest version first. The versions are plain
	// (never case-encoded): only URL paths use the "!x" encoding.
	sortVersionsDesc(versions)

	versionList := make([]string, 0, len(versions))
	for _, v := range versions {
		versionList = append(versionList, v.Version)
	}

	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(strings.Join(versionList, "\n")))
}

type versionInfo struct {
	Version string    `json:"Version"`
	Time    time.Time `json:"Time"`
}

func (h *HTTPHandler) handleInfo(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	repoName := vars["name"]
	modulePath := vars["module"]
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

	mod, err := h.DB.GetGoModule(r.Context(), repoID, modulePath, version)
	if err != nil {
		h.Log.Errorw("failed to get go module", "module", modulePath, "version", version, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if mod == nil {
		http.NotFound(w, r)
		return
	}

	if h.DenyIfQuarantined(w, r, repoID, versionQuarantineKeys(modulePath, version)...) {
		return
	}

	info := versionInfo{
		Version: mod.Version,
		Time:    mod.CreatedAt,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	if encErr := json.NewEncoder(w).Encode(info); encErr != nil {
		_ = encErr // response header already written
	}
}

// versionQuarantineKeys are the spellings under which a go module version may
// be quarantined: any of its files, or the module@version coordinate.
func versionQuarantineKeys(modulePath, version string) []string {
	base := modulePath + "/@v/" + version

	return quarantine.PathKeys(base+".zip", base+".mod", base+".info", modulePath+"@"+version)
}

// dropQuarantined removes quarantined versions so list, @latest and .info do
// not advertise them. It fails closed when the state cannot be determined.
func (h *HTTPHandler) dropQuarantined(ctx context.Context, repoID, modulePath string, versions []*models.GoModule) ([]*models.GoModule, error) {
	kept := make([]*models.GoModule, 0, len(versions))

	for _, v := range versions {
		blocked, err := quarantine.IsQuarantined(ctx, h.DB, repoID, versionQuarantineKeys(modulePath, v.Version)...)
		if err != nil {
			return nil, fmt.Errorf("check quarantine: %w", err)
		}

		if !blocked {
			kept = append(kept, v)
		}
	}

	return kept, nil
}

func (h *HTTPHandler) handleMod(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	repoName := vars["name"]
	modulePath := vars["module"]
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

	mod, err := h.DB.GetGoModule(r.Context(), repoID, modulePath, version)
	if err != nil {
		h.Log.Errorw("failed to get go module", "module", modulePath, "version", version, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if mod == nil {
		http.NotFound(w, r)
		return
	}

	if h.DenyIfQuarantined(w, r, repoID, quarantine.PathKeys(
		modulePath+"/@v/"+version+".mod", modulePath+"@"+version, r.URL.Path)...) {
		return
	}

	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(mod.GoModContent))
}

func (h *HTTPHandler) handleZip(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	repoName := vars["name"]
	modulePath := vars["module"]
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

	mod, err := h.DB.GetGoModule(r.Context(), repoID, modulePath, version)
	if err != nil {
		h.Log.Errorw("failed to get go module", "module", modulePath, "version", version, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if mod == nil {
		http.NotFound(w, r)
		return
	}

	if h.DenyIfQuarantined(w, r, repoID, quarantine.PathKeys(
		modulePath+"/@v/"+version+".zip", modulePath+"@"+version, r.URL.Path)...) {
		return
	}

	// Fail closed: versions published by <= 0.4.1 may reference a shared
	// blobs row with an empty sha256 whose object is some other module's zip.
	// Never serve (or presign) anything that is not addressed by a real digest.
	if !database.IsValidBlobSHA256(mod.ZipHash) {
		h.Log.Warnw("refusing to serve go module zip: its blob has no valid sha256; re-upload the version or run the gomod repair (openartifact-server -repair-gomod)",
			"repo", repoName, "module", modulePath, "version", version, "zip_hash", mod.ZipHash)
		http.NotFound(w, r)

		return
	}

	if h.TryPresign(w, r, presign.Blob{SHA256: mod.ZipBlobKey, Size: mod.ZipSizeBytes, ContentType: "application/zip"}) {
		return
	}

	rc, err := h.BlobStore.Get(r.Context(), mod.ZipBlobKey)
	if err != nil {
		h.Log.Errorw("failed to get zip blob", "key", mod.ZipBlobKey, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}
	defer func() { _ = rc.Close() }()

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Length", strconv.FormatInt(mod.ZipSizeBytes, 10))

	if mod.ZipHash != "" {
		w.Header().Set("ETag", `"`+mod.ZipHash+`"`)
	}

	w.WriteHeader(http.StatusOK)

	if _, err := io.Copy(w, rc); err != nil {
		h.Log.Errorw("failed to write zip response", "module", modulePath, "version", version, "error", err)
	}
}

func (h *HTTPHandler) handleLatest(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	repoName := vars["name"]
	modulePath := vars["module"]

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

	versions, err := h.DB.ListGoModuleVersions(r.Context(), repoID, modulePath)
	if err != nil {
		h.Log.Errorw("failed to get latest go module", "module", modulePath, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	versions, err = h.dropQuarantined(r.Context(), repoID, modulePath, versions)
	if err != nil {
		h.Log.Errorw("failed to check quarantine", "module", modulePath, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	mod := latestVersion(versions)
	if mod == nil {
		http.NotFound(w, r)
		return
	}

	info := versionInfo{
		Version: mod.Version,
		Time:    mod.CreatedAt,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	if encErr := json.NewEncoder(w).Encode(info); encErr != nil {
		_ = encErr // response header already written
	}
}

func (h *HTTPHandler) handleUpload(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	repoName := vars["name"]
	modulePath := vars["module"]
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

	// Read the zip file from request body
	zipData, err := base.ReadAllLimited(r.Body, maxGoModUploadBytes)
	if err != nil {
		if errors.Is(err, base.ErrContentTooLarge) {
			http.Error(w, "body too large", http.StatusRequestEntityTooLarge)

			return
		}
		h.Log.Errorw("failed to read upload body", "error", err)
		http.Error(w, "failed to read body", http.StatusBadRequest)

		return
	}
	defer func() { _ = r.Body.Close() }()

	// The module zip must be the one the URL names: its files live under
	// "<module>@<version>/" and its go.mod declares that same module path.
	mf, err := parseModuleZip(zipData)
	if err != nil {
		h.Log.Errorw("failed to parse go module", "error", err)
		http.Error(w, "invalid go module", http.StatusBadRequest)

		return
	}

	if mf.entry != modulePath+"@"+version+"/"+goModFileName {
		http.Error(w, "go.mod is not at <module>@<version>/go.mod in the zip", http.StatusBadRequest)
		return
	}

	if mf.modulePath != modulePath {
		http.Error(w, "go.mod module line does not match the module path in the URL", http.StatusBadRequest)
		return
	}

	goModContent := mf.content

	existing, err := h.DB.GetGoModule(r.Context(), repoID, modulePath, version)
	if err != nil {
		h.Log.Errorw("failed to check existing go module", "module", modulePath, "version", version, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if existing != nil {
		http.Error(w, "module version already exists", http.StatusConflict)
		return
	}

	zipSum := sha256.Sum256(zipData)
	zipSHA := hex.EncodeToString(zipSum[:])
	modSum := sha256.Sum256([]byte(goModContent))
	modSHA := hex.EncodeToString(modSum[:])

	mod := &models.GoModule{
		RepositoryID: repoID,
		ModulePath:   modulePath,
		Version:      version,
		GoModContent: goModContent,
		ZipSizeBytes: int64(len(zipData)),
		// The database keys the zip blob row by this digest; left empty, every
		// module version of the instance would share the first zip stored.
		ZipHash: zipSHA,
	}

	// One module is one artifact: the zip and go.mod bytes are reserved
	// together, matching the reconcile unit. Every failure path below returns
	// the reservation.
	usage := mod.ZipSizeBytes + int64(len(goModContent))

	if err := quota.EnforceN(r.Context(), h.DB, repoID, usage, 1); err != nil {
		if errors.Is(err, quota.ErrExceeded) {
			quota.WriteExceeded(w)
			return
		}

		h.Log.Errorw("failed to reserve quota", "repo", repoName, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	// Store both blobs and insert the referencing row under the per-blob locks
	// so a concurrent unpublish cannot delete either blob in between. Locks
	// are taken in a fixed (sorted) order to avoid deadlocks between uploads.
	first, second := zipSHA, modSHA
	if second < first {
		first, second = second, first
	}

	store := func(ctx context.Context) error {
		zipBlobID, err := h.BlobStore.Put(ctx, bytes.NewReader(zipData), map[string]string{
			"sha256":  zipSHA,
			"module":  modulePath,
			"version": version,
			"type":    "zip",
		})
		if err != nil {
			return fmt.Errorf("store zip blob: %w", err)
		}

		modBlobID, err := h.BlobStore.Put(ctx, strings.NewReader(goModContent), map[string]string{
			"sha256":  modSHA,
			"module":  modulePath,
			"version": version,
			"type":    "mod",
		})
		if err != nil {
			return fmt.Errorf("store mod blob: %w", err)
		}

		mod.ZipBlobKey = zipBlobID
		mod.ModBlobKey = modBlobID

		if err := h.DB.CreateGoModule(ctx, mod); err != nil {
			return fmt.Errorf("create go module record: %w", err)
		}

		return nil
	}

	lockErr := storage.WithBlobLocks(r.Context(), h.DB, []string{first, second}, store)
	if lockErr != nil {
		if err := quota.Release(r.Context(), h.DB, repoID, usage); err != nil {
			h.Log.Warnw("failed to release quota", "repo", repoName, "error", err)
		}

		if h.failStore(w, r, lockErr, zipSHA, modSHA) {
			return
		}

		h.Log.Errorw("failed to store go module", "module", modulePath, "version", version, "error", lockErr)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	h.Emit(r.Context(), repoID, webhook.EventArtifactUploaded, webhook.ArtifactPayload(webhook.EventArtifactUploaded, webhook.Artifact{
		Repository: repoName,
		Format:     "go",
		Name:       modulePath,
		Version:    version,
		Path:       modulePath + "/@v/" + version + ".zip",
		SHA256:     zipSHA,
		Size:       mod.ZipSizeBytes,
	}))

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)

	if encErr := json.NewEncoder(w).Encode(map[string]any{
		"version": version,
		"module":  modulePath,
	}); encErr != nil {
		_ = encErr // response header already written
	}
}

// failStore removes the blobs of a failed upload (refcounted: a blob another
// version references stays) and answers 409 when the failure was a lost
// duplicate race. It reports whether it wrote the response.
func (h *HTTPHandler) failStore(w http.ResponseWriter, r *http.Request, storeErr error, sums ...string) bool {
	for _, sum := range sums {
		if err := storage.SafeDeleteBlob(r.Context(), h.DB, h.BlobStore, sum); err != nil {
			h.Log.Warnw("failed to clean up go module blob", "sha256", sum, "error", err)
		}
	}

	if errors.Is(storeErr, database.ErrDuplicate) || database.IsUniqueViolation(storeErr) {
		http.Error(w, "module version already exists", http.StatusConflict)
		return true
	}

	return false
}
