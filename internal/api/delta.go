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
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
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

	// maxQueuedDeltas bounds the distinct generations waiting for one of those
	// slots. A request that would be the next one is refused with 503 and a
	// Retry-After, so a flood of distinct (from,to) pairs cannot pile up parked
	// requests. Requests for a pair that is already being generated wait for
	// that run and take no queue place; they are bounded per run by
	// maxDeltaWaiters.
	maxQueuedDeltas = 8

	// maxDeltaWaiters bounds the requests that wait for one shared generation.
	maxDeltaWaiters = 32

	// deltaGenerationTimeout bounds one generation, the wait for a slot
	// included. The run belongs to every request waiting for it, so it does not
	// follow the context of the request that happened to start it.
	deltaGenerationTimeout = 2 * time.Minute

	// deltaRetryAfter is the Retry-After (seconds) of a refused generation.
	deltaRetryAfter = "10"

	// maxAnonymousDeltaInput caps each input of a delta generated for an
	// anonymous reader of a public repository (or for a request without a
	// resolved principal); signed-in readers get pkgdelta.MaxInputSize. A
	// stored delta is served to everyone whatever its size: the cap limits only
	// the CPU and memory an unauthenticated request can make the server spend.
	maxAnonymousDeltaInput = int64(8 << 20)

	// maxRepoDeltas and maxRepoDeltaBytes bound the cached patches kept per
	// repository; the oldest are pruned (and their blobs released) beyond them.
	maxRepoDeltas     = 256
	maxRepoDeltaBytes = int64(1 << 30)
)

// errDeltaBusy is returned when every generation slot is taken and the queue
// for them is full (or the wait outlasted deltaGenerationTimeout).
var errDeltaBusy = errors.New("delta generation is busy")

// deltaPruner is implemented by databases that can bound the stored deltas of
// a repository.
type deltaPruner interface {
	PruneRepoDeltas(ctx context.Context, repoID string, maxCount int, maxBytes int64) ([]string, error)
}

// deltaCall is a single in-flight delta generation shared by concurrent callers.
// delta and err are set before done is closed.
type deltaCall struct {
	done    chan struct{}
	waiters atomic.Int32 // requests parked on done, besides the one running it
	delta   *models.PackageDelta
	err     error
}

// DeltaStore is the part of the database DeltaHandler needs.
type DeltaStore interface {
	quarantine.Checker
	BlobReferenceCount(ctx context.Context, sha256 string) (int, error)
	CreatePackageDelta(ctx context.Context, delta *models.PackageDelta) error
	GetComponentByID(ctx context.Context, id string) (*models.Component, error)
	GetComponentBySHA256(ctx context.Context, repoID string, sha256 string) (*models.Component, error)
	GetPackageDelta(ctx context.Context, repoID string, fromSHA256 string, toSHA256 string) (*models.PackageDelta, error)
	GetRepositoryByName(ctx context.Context, name string) (*models.Repository, error)
	ListPackageDeltas(ctx context.Context, repoID string, toSHA256 string) ([]*models.PackageDelta, error)
	UpsertBlob(ctx context.Context, sha256 string, blobKey string, sizeBytes int64) (string, error)
}

// DeltaHandler handles package delta requests.
type DeltaHandler struct {
	inflightMu sync.Mutex
	inflight   map[string]*deltaCall
	slots      chan struct{} // bounds concurrent generations
	queued     atomic.Int32  // generations waiting for a slot

	log       *logger.Logger
	db        DeltaStore
	blobStore storage.BlobStore
}

// NewDeltaHandler creates a new DeltaHandler.
func NewDeltaHandler(log *logger.Logger, db DeltaStore, blobStore storage.BlobStore) *DeltaHandler {
	return &DeltaHandler{
		log:       log,
		db:        db,
		blobStore: blobStore,
		inflight:  map[string]*deltaCall{},
		slots:     make(chan struct{}, maxConcurrentDeltas),
	}
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
	delta, err := h.generateDeltaOnce(r, repo.ID, fromSHA256, fromFilename, toComp, deltaInputLimit(r))
	if errors.Is(err, errDeltaInputTooLarge) {
		writeError(w, http.StatusRequestEntityTooLarge, "DELTA_TOO_LARGE", "package too large for delta generation")
		return
	}

	if errors.Is(err, errDeltaBusy) {
		w.Header().Set("Retry-After", deltaRetryAfter)
		writeError(w, http.StatusServiceUnavailable, "DELTA_BUSY", "delta generation is busy, retry later")

		return
	}

	if err != nil && r.Context().Err() != nil {
		// The client went away while waiting for the shared generation.
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

// deltaInputLimit is the largest input a delta generated for r may read: the
// full cap for a signed-in reader, the small one for an anonymous reader and for
// a request without a resolved principal (it fails closed).
func deltaInputLimit(r *http.Request) int64 {
	if p := currentPrincipal(r); p != nil && !p.IsAnonymous {
		return pkgdelta.MaxInputSize
	}

	return maxAnonymousDeltaInput
}

// acquire takes a generation slot. When every slot is busy it waits in a queue
// of at most maxQueuedDeltas places and returns errDeltaBusy beyond that, or
// when the wait outlasts the deadline of ctx; it returns ctx's error when ctx
// is canceled.
func (h *DeltaHandler) acquire(ctx context.Context) error {
	select {
	case h.slots <- struct{}{}:
		return nil
	default:
	}

	if h.queued.Add(1) > maxQueuedDeltas {
		h.queued.Add(-1)

		return errDeltaBusy
	}

	defer h.queued.Add(-1)

	select {
	case h.slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return errDeltaBusy
		}

		return fmt.Errorf("wait for a delta slot: %w", ctx.Err())
	}
}

// generateDeltaOnce collapses concurrent requests for the same
// (repo, from, to) delta into a single generation. The run belongs to all the
// requests waiting for it: it uses a context detached from the first request's
// (a client that gives up must not fail the others) and bounded by
// deltaGenerationTimeout, while every waiter also stops waiting when its own
// request ends. limit is the input cap of the callers; it is part of the key so
// a run capped for an anonymous reader never answers a signed-in one.
func (h *DeltaHandler) generateDeltaOnce(
	r *http.Request,
	repoID, fromSHA256, fromFilename string,
	toComp *models.Component,
	limit int64,
) (*models.PackageDelta, error) {
	key := repoID + "|" + fromSHA256 + "|" + toComp.SHA256 + "|" + strconv.FormatInt(limit, 10)

	h.inflightMu.Lock()

	if c, ok := h.inflight[key]; ok {
		h.inflightMu.Unlock()

		if c.waiters.Add(1) > maxDeltaWaiters {
			c.waiters.Add(-1)

			return nil, errDeltaBusy
		}

		defer c.waiters.Add(-1)

		select {
		case <-c.done:
			return c.delta, c.err
		case <-r.Context().Done():
			return nil, fmt.Errorf("wait for delta generation: %w", r.Context().Err())
		}
	}

	c := &deltaCall{done: make(chan struct{})}
	h.inflight[key] = c
	h.inflightMu.Unlock()

	defer func() {
		h.inflightMu.Lock()
		delete(h.inflight, key)
		h.inflightMu.Unlock()
		close(c.done)
	}()

	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), deltaGenerationTimeout)
	defer cancel()

	c.delta, c.err = h.generateDelta(ctx, repoID, fromSHA256, fromFilename, toComp, limit)

	return c.delta, c.err
}

// checkSize verifies the stored size of a blob is within limit.
func (h *DeltaHandler) checkSize(ctx context.Context, id string, limit int64) error {
	size, err := h.blobStore.GetSize(ctx, id)
	if err != nil {
		return fmt.Errorf("stat blob %s: %w", id, err)
	}

	if size > limit {
		return fmt.Errorf("%w: %s is %d bytes", errDeltaInputTooLarge, id, size)
	}

	return nil
}

// readCapped reads a blob of at most limit bytes.
func (h *DeltaHandler) readCapped(ctx context.Context, id string, limit int64) ([]byte, error) {
	rc, err := h.blobStore.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("fetch blob %s: %w", id, err)
	}
	defer func() { base.CloseLog(rc, h.log) }()

	data, err := io.ReadAll(io.LimitReader(rc, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read blob %s: %w", id, err)
	}

	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%w: %s", errDeltaInputTooLarge, id)
	}

	return data, nil
}

// generateDelta generates a bsdiff patch from fromSHA256 to toComp, stores it,
// and returns the PackageDelta record. Each input may be at most limit bytes;
// the sizes are checked before a generation slot is taken, so an oversized
// request never occupies the queue.
func (h *DeltaHandler) generateDelta(
	ctx context.Context,
	repoID, fromSHA256, fromFilename string,
	toComp *models.Component,
	limit int64,
) (*models.PackageDelta, error) {
	for _, id := range []string{fromSHA256, toComp.SHA256} {
		if err := h.checkSize(ctx, id, limit); err != nil {
			return nil, err
		}
	}

	if err := h.acquire(ctx); err != nil {
		return nil, err
	}

	defer func() { <-h.slots }()

	fromData, err := h.readCapped(ctx, fromSHA256, limit)
	if err != nil {
		return nil, err
	}

	toData, err := h.readCapped(ctx, toComp.SHA256, limit)
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

	err = storage.CommitBlob(ctx, h.db, h.blobStore, blob, func(ctx context.Context, _ string) error {
		if _, err := h.db.UpsertBlob(ctx, patchSHA256, patchSHA256, d.DeltaSize); err != nil {
			return fmt.Errorf("register delta blob: %w", err)
		}

		return h.db.CreatePackageDelta(ctx, d)
	})
	if err != nil {
		return nil, err
	}

	h.pruneDeltas(ctx, repoID)

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
	filename := strings.NewReplacer("\"", "", "\r", "", "\n", "").Replace(d.ToFilename)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.delta"`, filename))
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
