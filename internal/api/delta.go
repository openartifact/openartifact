// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

// DeltaHandler provides HTTP endpoints for binary diff (delta) distribution.
//
// Endpoints:
//   GET  /api/v1/repositories/{name}/packages/{id}/delta?from={sha256}
//        Returns a pre-computed bsdiff patch that transforms the package
//        identified by "from" SHA-256 into the package identified by "id".
//        If no pre-computed delta exists, one is generated on-the-fly,
//        stored for future requests, and streamed to the client.
//
//   GET  /api/v1/repositories/{name}/packages/{id}/deltas
//        Lists all available deltas targeting the package identified by "id".

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/go-chi/chi/v5"
	database "github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	"github.com/openartifact/openartifact/internal/httpcache"
	"github.com/openartifact/openartifact/internal/iocopy"
	"github.com/openartifact/openartifact/internal/quarantine"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/urlparams"
	pkgdelta "github.com/openartifact/openartifact/pkg/delta"
	"github.com/openartifact/openartifact/pkg/logger"
)

var sha256HexRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// errDeltaInputTooLarge is returned when a delta source blob exceeds the cap.
var errDeltaInputTooLarge = errors.New("delta input exceeds size limit")

const (
	// maxConcurrentDeltas bounds the bsdiff runs in flight server-wide: each
	// builds a suffix array several times the size of its (up to 64 MiB)
	// inputs, so unbounded distinct (from,to) pairs would exhaust memory.
	maxConcurrentDeltas = 2

	// maxRepoDeltas and maxRepoDeltaBytes bound the cached patches kept per
	// repository; the oldest are pruned (and their blobs released) beyond them.
	maxRepoDeltas     = 256
	maxRepoDeltaBytes = int64(1 << 30)
)

// deltaPruner is implemented by databases that can bound the stored deltas of
// a repository.
type deltaPruner interface {
	PruneRepoDeltas(ctx context.Context, repoID string, maxCount int, maxBytes int64) ([]string, error)
}

// deltaCall is a single in-flight delta generation shared by concurrent callers.
type deltaCall struct {
	wg    sync.WaitGroup
	delta *models.PackageDelta
	err   error
}

// DeltaHandler handles package delta requests.
type DeltaHandler struct {
	inflightMu sync.Mutex
	inflight   map[string]*deltaCall
	slots      chan struct{} // bounds concurrent generations

	log       *logger.Logger
	db        database.Database
	blobStore storage.BlobStore
}

// NewDeltaHandler creates a new DeltaHandler.
func NewDeltaHandler(log *logger.Logger, db database.Database, blobStore storage.BlobStore) *DeltaHandler {
	return &DeltaHandler{log: log, db: db, blobStore: blobStore, inflight: map[string]*deltaCall{}, slots: make(chan struct{}, maxConcurrentDeltas)}
}

// Register registers the delta routes on the given router.
func (h *DeltaHandler) Register(router chi.Router) {
	router.Get("/api/v1/repositories/{name}/packages/{id}/delta", h.handleGetDelta)
	router.Get("/api/v1/repositories/{name}/packages/{id}/deltas", h.handleListDeltas)
	h.log.Infow("Delta handler registered")
}

// handleGetDelta serves a binary diff patch.
// Query parameter: from=<sha256> — the SHA-256 of the package the client already has.
func (h *DeltaHandler) handleGetDelta(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	repoName := vars["name"]
	toID := vars["id"] // component ID of the target package

	fromSHA256 := strings.ToLower(r.URL.Query().Get("from"))
	if fromSHA256 == "" {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "missing 'from' query parameter (sha256 of source package)")
		return
	}

	if !sha256HexRe.MatchString(fromSHA256) {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "'from' must be a 64-character hex sha256")
		return
	}

	repo, err := h.db.GetRepositoryByName(r.Context(), repoName)
	if err != nil || repo == nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "repository not found: "+repoName)
		return
	}

	// Look up the target component.
	toComp, err := h.db.GetComponentByID(r.Context(), toID)
	if err != nil || toComp == nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "target package not found")
		return
	}

	if toComp.RepositoryID != repo.ID {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "target package not found")
		return
	}

	// The source package must also live in this repository.
	fromComp, found := h.findFromComponent(r, repo.ID, fromSHA256)
	if !found {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "source package not found in repository")
		return
	}

	// A patch reconstructs its target byte for byte from the source, so a
	// quarantined or blocked package on either side must not be derivable.
	if quarantine.Enforce(w, r, h.db, repo.ID, componentQuarantineKeys(toComp)...) ||
		quarantine.Enforce(w, r, h.db, repo.ID, componentQuarantineKeys(fromComp)...) {
		return
	}

	fromFilename := fromComp.Filename

	// Check for a pre-computed delta.
	existing, err := h.db.GetPackageDelta(r.Context(), repo.ID, fromSHA256, toComp.SHA256)
	if err != nil {
		h.log.Errorw("failed to query delta", "error", err)
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "failed to query delta")

		return
	}

	if existing != nil {
		// Serve the pre-computed delta blob.
		h.serveDeltaBlob(w, r, repo.Public, existing)
		return
	}

	// No pre-computed delta — generate on-the-fly.
	delta, err := h.generateDeltaOnce(r, repo.ID, fromSHA256, fromFilename, toComp)
	if errors.Is(err, errDeltaInputTooLarge) {
		writeError(w, http.StatusRequestEntityTooLarge, "DELTA_TOO_LARGE", "package too large for delta generation")
		return
	}

	if err != nil {
		h.log.Errorw("failed to generate delta", "from", fromSHA256, "to", toComp.SHA256, "error", err)
		writeError(w, http.StatusInternalServerError, "DELTA_ERROR", "failed to generate delta")

		return
	}

	h.serveDeltaBlob(w, r, repo.Public, delta)
}

// handleListDeltas lists all available deltas targeting a package.
func (h *DeltaHandler) handleListDeltas(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	repoName := vars["name"]
	toID := vars["id"]

	repo, err := h.db.GetRepositoryByName(r.Context(), repoName)
	if err != nil || repo == nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "repository not found: "+repoName)
		return
	}

	toComp, err := h.db.GetComponentByID(r.Context(), toID)
	if err != nil || toComp == nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "target package not found")
		return
	}

	if toComp.RepositoryID != repo.ID {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "target package not found")
		return
	}

	deltas, err := h.db.ListPackageDeltas(r.Context(), repo.ID, toComp.SHA256)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "failed to list deltas")
		return
	}

	type deltaInfo struct {
		FromSHA256   string `json:"from_sha256"`
		FromFilename string `json:"from_filename"`
		ToSHA256     string `json:"to_sha256"`
		ToFilename   string `json:"to_filename"`
		DeltaSize    int64  `json:"delta_size"`
		Format       string `json:"format"`
	}

	result := make([]deltaInfo, 0, len(deltas))
	for _, d := range deltas {
		result = append(result, deltaInfo{
			FromSHA256:   d.FromSHA256,
			FromFilename: d.FromFilename,
			ToSHA256:     d.ToSHA256,
			ToFilename:   d.ToFilename,
			DeltaSize:    d.DeltaSize,
			Format:       d.Format,
		})
	}

	writeJSON(w, http.StatusOK, Response{Success: true, Data: result})
}

// findFromComponent looks up the source component by sha256 within the repository.
func (h *DeltaHandler) findFromComponent(r *http.Request, repoID, sha string) (*models.Component, bool) {
	c, err := h.db.GetComponentBySHA256(r.Context(), repoID, sha)
	if err != nil {
		h.log.Errorw("failed to look up source component", "error", err)
		return nil, false
	}

	return c, c != nil
}

// componentQuarantineKeys are the spellings a scan of the component may be
// recorded under (id, filename, and the per-format download paths).
func componentQuarantineKeys(c *models.Component) []string {
	return quarantine.PathKeys(c.ID, c.Filename, "Packages/"+c.Filename, "gems/"+c.Filename)
}

// acquire takes a generation slot, or reports false when ctx ends first.
func (h *DeltaHandler) acquire(ctx context.Context) bool {
	select {
	case h.slots <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

// generateDeltaOnce collapses concurrent requests for the same
// (repo, from, to) delta into a single generation.
func (h *DeltaHandler) generateDeltaOnce(r *http.Request, repoID, fromSHA256, fromFilename string, toComp *models.Component) (*models.PackageDelta, error) {
	key := repoID + "|" + fromSHA256 + "|" + toComp.SHA256

	h.inflightMu.Lock()

	if c, ok := h.inflight[key]; ok {
		h.inflightMu.Unlock()
		c.wg.Wait()

		return c.delta, c.err
	}

	c := &deltaCall{}
	c.wg.Add(1)
	h.inflight[key] = c
	h.inflightMu.Unlock()

	defer func() {
		h.inflightMu.Lock()
		delete(h.inflight, key)
		h.inflightMu.Unlock()
		c.wg.Done()
	}()

	c.delta, c.err = h.generateDelta(r, repoID, fromSHA256, fromFilename, toComp)

	return c.delta, c.err
}

// readCapped reads a blob after verifying its stored size is within the delta cap.
func (h *DeltaHandler) readCapped(r *http.Request, id string) ([]byte, error) {
	size, err := h.blobStore.GetSize(r.Context(), id)
	if err != nil {
		return nil, fmt.Errorf("stat blob %s: %w", id, err)
	}

	if size > pkgdelta.MaxInputSize {
		return nil, fmt.Errorf("%w: %s is %d bytes", errDeltaInputTooLarge, id, size)
	}

	rc, err := h.blobStore.Get(r.Context(), id)
	if err != nil {
		return nil, fmt.Errorf("fetch blob %s: %w", id, err)
	}
	defer func() { base.CloseLog(rc, h.log) }()

	data, err := io.ReadAll(io.LimitReader(rc, pkgdelta.MaxInputSize+1))
	if err != nil {
		return nil, fmt.Errorf("read blob %s: %w", id, err)
	}

	if len(data) > pkgdelta.MaxInputSize {
		return nil, fmt.Errorf("%w: %s", errDeltaInputTooLarge, id)
	}

	return data, nil
}

// generateDelta generates a bsdiff patch from fromSHA256 to toComp, stores it,
// and returns the PackageDelta record.
func (h *DeltaHandler) generateDelta(r *http.Request, repoID, fromSHA256, fromFilename string, toComp *models.Component) (*models.PackageDelta, error) {
	if !h.acquire(r.Context()) {
		return nil, r.Context().Err()
	}

	defer func() { <-h.slots }()

	fromData, err := h.readCapped(r, fromSHA256)
	if err != nil {
		return nil, err
	}

	toData, err := h.readCapped(r, toComp.SHA256)
	if err != nil {
		return nil, err
	}

	patch, patchSHA256, err := pkgdelta.Generate(fromData, toData)
	if err != nil {
		return nil, fmt.Errorf("generate diff: %w", err)
	}

	d := &models.PackageDelta{
		RepositoryID: repoID,
		FromSHA256:   fromSHA256,
		FromFilename: fromFilename,
		ToSHA256:     toComp.SHA256,
		ToFilename:   toComp.Filename,
		DeltaSHA256:  patchSHA256,
		DeltaSize:    int64(len(patch)),
		Format:       toComp.Format,
	}

	// Store the blob, register it in the blobs table (so the orphan sweep and
	// SafeDeleteBlob see it) and insert the referencing row. The bytes are
	// stored first; existence is re-checked under the blob lock, which covers
	// only the DB work.
	blob := storage.BytesBlob(patchSHA256, patch, map[string]string{
		"sha256": patchSHA256,
		"type":   "delta",
	})

	err = storage.CommitBlob(r.Context(), h.db, h.blobStore, blob, func(ctx context.Context, _ string) error {
		if _, err := h.db.UpsertBlob(ctx, patchSHA256, patchSHA256, d.DeltaSize); err != nil {
			return fmt.Errorf("register delta blob: %w", err)
		}

		return h.db.CreatePackageDelta(ctx, d)
	})
	if err != nil {
		return nil, err
	}

	h.pruneDeltas(r.Context(), repoID)

	return d, nil
}

// pruneDeltas keeps the stored patches of a repository within the caps,
// releasing the blobs of the pruned rows.
func (h *DeltaHandler) pruneDeltas(ctx context.Context, repoID string) {
	pr, ok := h.db.(deltaPruner)
	if !ok {
		return
	}

	shas, err := pr.PruneRepoDeltas(ctx, repoID, maxRepoDeltas, maxRepoDeltaBytes)
	if err != nil {
		h.log.Warnw("failed to prune package deltas", "repo", repoID, "error", err)
		return
	}

	for _, sha := range shas {
		if err := storage.SafeDeleteBlob(ctx, h.db, h.blobStore, sha); err != nil {
			h.log.Warnw("failed to release pruned delta blob", "sha256", sha, "error", err)
		}
	}
}

// serveDeltaBlob streams a delta blob to the client.
func (h *DeltaHandler) serveDeltaBlob(w http.ResponseWriter, r *http.Request, repoPublic bool, d *models.PackageDelta) {
	reader, err := h.blobStore.Get(r.Context(), d.DeltaSHA256)
	if err != nil {
		h.log.Errorw("failed to read delta blob", "sha256", d.DeltaSHA256, "error", err)
		writeError(w, http.StatusInternalServerError, "STORAGE_ERROR", "failed to read delta")

		return
	}
	defer func() { base.CloseLog(reader, h.log) }()

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.delta"`, strings.NewReplacer("\"", "", "\r", "", "\n", "").Replace(d.ToFilename)))
	w.Header().Set("ETag", `"`+d.DeltaSHA256+`"`)
	w.Header().Set("X-Delta-From", d.FromSHA256)
	w.Header().Set("X-Delta-To", d.ToSHA256)
	w.Header().Set("X-Delta-Size", strconv.FormatInt(d.DeltaSize, 10))
	// Shared caches may store the delta only for an anonymous read of a public
	// repository; the endpoint is RBAC-gated otherwise.
	w.Header().Set("Cache-Control", httpcache.CacheControl(base.SharedCacheable(r, repoPublic), 86400))
	w.Header().Add("Vary", "Authorization")

	if _, err := iocopy.Copy(w, reader); err != nil {
		h.log.Errorw("failed to write delta response", "error", err)
	}
}
