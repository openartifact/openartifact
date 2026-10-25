// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/openartifact/openartifact/internal/api/responses"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/bgtask"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/trash"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/internal/webhook"
	"github.com/openartifact/openartifact/pkg/logger"
)

const (
	defaultTrashListLimit = 50
	maxTrashListLimit     = 200

	// maxEmptyTrashItems caps how many items one "empty trash" request purges;
	// the response reports what is left so the caller can repeat.
	maxEmptyTrashItems = 1000
	emptyTrashBatch    = 100
)

// TrashHandler serves the trash API: listing, restoring and purging soft-deleted
// packages and repositories.
//
// The global routes (/api/v1/trash...) are admin only. The repository routes
// (/api/v1/repositories/{name}/trash...) need the repository-scoped `delete`
// permission and only ever touch items of that repository; emptying a
// repository's trash needs the repository-scoped `manage` permission.
type TrashHandler struct {
	db        database.Database
	log       *logger.Logger
	evaluator *auth.Evaluator
	blobStore storage.BlobStore
	publisher webhook.Publisher
	regen     TrashRegenerator
	groupMeta GroupMetadataNotifier

	// tasks runs the detached orphan-blob collections started by repository
	// purges; they are cancelled at server shutdown.
	tasks *bgtask.Runner
}

func NewTrashHandler(db database.Database, log *logger.Logger, evaluator *auth.Evaluator) *TrashHandler {
	return &TrashHandler{db: db, log: log, evaluator: evaluator, tasks: bgtask.New(context.Background())}
}

// WithTasks runs the background orphan-blob collections under the server
// lifecycle: they are cancelled and awaited when the runner shuts down.
func (h *TrashHandler) WithTasks(tasks *bgtask.Runner) *TrashHandler {
	h.tasks = tasks

	return h
}

// WithBlobStore enables freeing blobs when an item is purged.
func (h *TrashHandler) WithBlobStore(store storage.BlobStore) *TrashHandler {
	h.blobStore = store

	return h
}

// WithPublisher enables the artifact.restored/purged and repository.restored/purged events.
func (h *TrashHandler) WithPublisher(p webhook.Publisher) *TrashHandler {
	h.publisher = p

	return h
}

// WithRegenerator enables the metadata refresh after a restore.
func (h *TrashHandler) WithRegenerator(r TrashRegenerator) *TrashHandler {
	h.regen = r

	return h
}

// WithGroupMetadata makes restoring a deb or rpm repository regenerate the
// merged metadata of the groups it belongs to (or its own, for a group).
func (h *TrashHandler) WithGroupMetadata(n GroupMetadataNotifier) *TrashHandler {
	h.groupMeta = n

	return h
}

func (h *TrashHandler) RegisterAuthed(r chi.Router) {
	r.Get("/api/v1/trash", h.handleListGlobal)
	r.Post("/api/v1/trash/{kind}/{id}/restore", h.handleRestoreGlobal)
	r.Delete("/api/v1/trash/{kind}/{id}", h.handlePurge)
	r.Get("/api/v1/repositories/{name}/trash", h.handleListRepo)
	r.Post("/api/v1/repositories/{name}/trash/{kind}/{id}/restore", h.handleRestoreRepo)
	r.Delete("/api/v1/repositories/{name}/trash", h.handleEmptyRepo)
}

func trashItemResponse(it *models.TrashItem) responses.TrashItemResponse {
	return responses.TrashItemResponse{
		ID:           it.ID,
		Kind:         string(it.Kind),
		RepositoryID: it.RepositoryID,
		Repository:   it.RepositoryName,
		Format:       it.Format,
		Name:         it.Name,
		Version:      it.Version,
		SizeBytes:    it.SizeBytes,
		DeletedAt:    it.DeletedAt.UTC().Format(time.RFC3339),
		DeletedBy:    it.DeletedBy,
		PurgeAt:      it.PurgeAt.UTC().Format(time.RFC3339),
	}
}

// store returns the trash storage, answering 501 when the database has none.
func (h *TrashHandler) store(w http.ResponseWriter) (database.TrashStore, bool) {
	store, ok := h.db.(database.TrashStore)
	if !ok {
		writeError(w, http.StatusNotImplemented, "not_implemented", "trash is not available")
		return nil, false
	}

	return store, true
}

// handleListGlobal lists the trash of every repository, repositories included.
//
//	GET /api/v1/trash?kind=&repository=&page=&limit=
func (h *TrashHandler) handleListGlobal(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	store, ok := h.store(w)
	if !ok {
		return
	}

	filter := trashFilterFromQuery(r)
	filter.IncludeRepositories = true

	if name := r.URL.Query().Get("repository"); name != "" {
		repo, err := h.db.GetRepositoryByName(r.Context(), name)
		if err != nil {
			h.log.Errorw("list trash: db error getting repo", "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "database error")

			return
		}

		if repo == nil {
			// Nothing live carries that name: its trash is empty.
			writeJSON(w, http.StatusOK, responses.TrashListResponse{
				Items: []responses.TrashItemResponse{}, Page: filter.Page, Limit: filter.Limit,
			})

			return
		}

		filter.RepositoryID = repo.ID
	}

	h.writeList(r.Context(), w, store, filter)
}

// handleListRepo lists the trash of one repository.
//
//	GET /api/v1/repositories/{name}/trash?kind=&page=&limit=
func (h *TrashHandler) handleListRepo(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.repoForDelete(w, r)
	if !ok {
		return
	}

	store, ok := h.store(w)
	if !ok {
		return
	}

	filter := trashFilterFromQuery(r)
	filter.RepositoryID = repo.ID

	h.writeList(r.Context(), w, store, filter)
}

func trashFilterFromQuery(r *http.Request) models.TrashFilter {
	q := r.URL.Query()

	return models.TrashFilter{
		Kind:  models.TrashKind(q.Get("kind")),
		Page:  min(parseIntMin(q.Get("page"), 1, 1), maxListPage),
		Limit: parseIntClamp(q.Get("limit"), defaultTrashListLimit, 1, maxTrashListLimit),
	}
}

func (h *TrashHandler) writeList(ctx context.Context, w http.ResponseWriter, store database.TrashStore, filter models.TrashFilter) {
	items, total, err := store.ListTrash(ctx, filter)
	if err != nil {
		h.log.Errorw("list trash: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	out := make([]responses.TrashItemResponse, 0, len(items))
	for _, it := range items {
		out = append(out, trashItemResponse(it))
	}

	writeJSON(w, http.StatusOK, responses.TrashListResponse{Items: out, Total: total, Page: filter.Page, Limit: filter.Limit})
}

// repoForDelete authorizes a repository-scoped trash request: the caller needs
// the repository-scoped `delete` permission on {name} (RBAC already checked it
// at the gate; this keeps the handler safe on its own) and the repository must
// be live. It writes the error response and returns false otherwise.
func (h *TrashHandler) repoForDelete(w http.ResponseWriter, r *http.Request) (*models.Repository, bool) {
	return h.repoFor(w, r, "delete")
}

// repoFor is repoForDelete for an arbitrary repository-scoped action.
func (h *TrashHandler) repoFor(w http.ResponseWriter, r *http.Request, action string) (*models.Repository, bool) {
	name := urlparams.Vars(r)["name"]
	ctx := r.Context()

	p := currentPrincipal(r)
	if p == nil || p.IsAnonymous {
		writeError(w, http.StatusForbidden, "forbidden", "access denied")
		return nil, false
	}

	allowed, err := h.evaluator.Evaluate(ctx, *p, name, "", action)
	if err != nil {
		h.log.Errorw("trash: auth evaluation failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "auth evaluation failed")

		return nil, false
	}

	if !allowed && action == "manage" {
		// Emptying a repository's trash is also open to the maintainers and
		// owners of its project.
		allowed, err = h.evaluator.EvaluateRepoAdmin(ctx, *p, name)
		if err != nil {
			h.log.Errorw("trash: auth evaluation failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "auth evaluation failed")

			return nil, false
		}
	}

	if !allowed {
		writeError(w, http.StatusForbidden, "forbidden", "access denied")
		return nil, false
	}

	repo, err := h.db.GetRepositoryByName(ctx, name)
	if err != nil {
		h.log.Errorw("trash: db error getting repo", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return nil, false
	}

	if repo == nil {
		writeError(w, http.StatusNotFound, "not_found", "repository not found")
		return nil, false
	}

	return repo, true
}

// trashTarget parses {kind}/{id} of the URL. An id that is not a UUID cannot
// exist: it is answered like a missing item.
func trashTarget(w http.ResponseWriter, r *http.Request) (models.TrashKind, string, bool) {
	vars := urlparams.Vars(r)

	if _, err := uuid.Parse(vars["id"]); err != nil {
		writeError(w, http.StatusNotFound, "not_found", "trash item not found")
		return "", "", false
	}

	return models.TrashKind(vars["kind"]), vars["id"], true
}

// handleRestoreGlobal restores any item, repositories included (admin only).
//
//	POST /api/v1/trash/{kind}/{id}/restore
func (h *TrashHandler) handleRestoreGlobal(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	store, ok := h.store(w)
	if !ok {
		return
	}

	kind, id, ok := trashTarget(w, r)
	if !ok {
		return
	}

	h.restore(w, r, store, kind, id)
}

// handleRestoreRepo restores one item of the repository {name}. Repositories
// themselves can only be restored through the admin route.
//
//	POST /api/v1/repositories/{name}/trash/{kind}/{id}/restore
func (h *TrashHandler) handleRestoreRepo(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.repoForDelete(w, r)
	if !ok {
		return
	}

	store, ok := h.store(w)
	if !ok {
		return
	}

	kind, id, ok := trashTarget(w, r)
	if !ok {
		return
	}

	if kind == models.TrashKindRepository {
		writeError(w, http.StatusForbidden, "forbidden", "only an administrator can restore a repository")
		return
	}

	// The item must belong to the repository in the URL.
	item, err := store.GetTrashItem(r.Context(), kind, id)
	if err == nil && item.RepositoryID != repo.ID {
		err = database.ErrTrashNotFound
	}

	if err != nil {
		h.writeTrashError(w, "restore trash item", err)
		return
	}

	h.restore(w, r, store, kind, id)
}

func (h *TrashHandler) restore(w http.ResponseWriter, r *http.Request, store database.TrashStore, kind models.TrashKind, id string) {
	ctx := r.Context()

	item, err := store.RestoreTrash(ctx, kind, id)
	if err != nil {
		h.writeTrashError(w, "restore trash item", err)
		return
	}

	trash.AuditRestored(ctx, item)
	h.emitRestored(ctx, item)
	h.regenerateRestored(ctx, item)
	h.notifyRestoredRepository(ctx, item)

	writeJSON(w, http.StatusOK, trashItemResponse(item))
}

// notifyRestoredRepository invalidates the group metadata a restored repository
// takes part in: its members' groups reappear in them, a group's own metadata
// is regenerated.
func (h *TrashHandler) notifyRestoredRepository(ctx context.Context, item *models.TrashItem) {
	if item.Kind != models.TrashKindRepository || h.groupMeta == nil {
		return
	}

	repo, err := h.db.GetRepositoryByID(ctx, item.RepositoryID)
	if err != nil || repo == nil {
		return
	}

	notifyGroupMetadata(h.groupMeta, repo)
}

func (h *TrashHandler) emitRestored(ctx context.Context, item *models.TrashItem) {
	if item.Kind == models.TrashKindRepository {
		repoType := ""
		if repo, err := h.db.GetRepositoryByID(ctx, item.RepositoryID); err == nil && repo != nil {
			repoType = repo.RepoType
		}

		webhook.Emit(ctx, h.publisher, item.RepositoryID, webhook.EventRepoRestored,
			webhook.RepositoryPayload(webhook.EventRepoRestored, item.RepositoryName, item.Format, repoType))

		return
	}

	webhook.Emit(ctx, h.publisher, item.RepositoryID, webhook.EventArtifactRestored,
		webhook.ArtifactPayload(webhook.EventArtifactRestored, itemArtifact(item.RepositoryName, item.Format, item.Name, item.Version, item.SizeBytes)))
}

func itemArtifact(repo, format, name, version string, size int64) webhook.Artifact {
	return webhook.Artifact{Repository: repo, Format: format, Name: name, Version: version, Path: name, Size: size}
}

// writeTrashError maps the trash store's expected failures to responses.
func (h *TrashHandler) writeTrashError(w http.ResponseWriter, op string, err error) {
	switch {
	case errors.Is(err, database.ErrTrashNotFound):
		writeError(w, http.StatusNotFound, "not_found", "trash item not found")
	case errors.Is(err, database.ErrTrashUnknownKind):
		writeError(w, http.StatusBadRequest, "validation_failed", "unknown trash kind")
	case errors.Is(err, database.ErrTrashConflict):
		writeError(w, http.StatusConflict, "trash_conflict",
			"an item with the same name/version was published after the delete; delete or rename it first")
	case errors.Is(err, database.ErrTrashRepoTrashed):
		writeError(w, http.StatusConflict, "repository_in_trash",
			"the item's repository is in the trash; restore the repository first")
	case errors.Is(err, database.ErrTrashManifestInTrash):
		writeError(w, http.StatusConflict, "manifest_in_trash",
			"the tag's manifest is in the trash; restore the manifest first")
	case database.IsForeignKeyViolation(err):
		writeError(w, http.StatusConflict, "conflict",
			"the repository is still referenced (for example by a release bundle or promotion); remove the reference first")
	default:
		h.log.Errorw("trash: "+op+" failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")
	}
}

// handlePurge permanently removes a trashed item now (admin only).
//
//	DELETE /api/v1/trash/{kind}/{id}
func (h *TrashHandler) handlePurge(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	store, ok := h.store(w)
	if !ok {
		return
	}

	kind, id, ok := trashTarget(w, r)
	if !ok {
		return
	}

	ctx := r.Context()

	res, err := store.PurgeTrash(ctx, kind, id)
	if err != nil {
		h.writeTrashError(w, "purge trash item", err)
		return
	}

	h.afterPurge(ctx, res)
	trash.AuditPurged(ctx, res, "manual")
	h.emitPurged(ctx, res)

	writeNoContent(w)
}

// handleEmptyRepo purges every trashed item of the repository {name} now:
// items only, never the repository itself. It needs the repository-scoped
// `manage` permission (global administrators hold it everywhere) and purges at
// most maxEmptyTrashItems per request.
//
//	DELETE /api/v1/repositories/{name}/trash
func (h *TrashHandler) handleEmptyRepo(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.repoFor(w, r, "manage")
	if !ok {
		return
	}

	store, ok := h.store(w)
	if !ok {
		return
	}

	ctx := r.Context()
	filter := models.TrashFilter{RepositoryID: repo.ID, Page: 1, Limit: emptyTrashBatch}
	purged := 0

	for purged < maxEmptyTrashItems && ctx.Err() == nil {
		filter.Limit = min(emptyTrashBatch, maxEmptyTrashItems-purged)

		items, _, err := store.ListTrash(ctx, filter)
		if err != nil {
			h.log.Errorw("empty trash: list failed", "repository", repo.Name, "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "database error")

			return
		}

		if len(items) == 0 {
			break
		}

		n, err := h.purgeBatch(ctx, store, items)
		purged += n

		if err != nil {
			h.writeTrashError(w, "empty trash", err)
			return
		}
	}

	_, remaining, err := store.ListTrash(ctx, models.TrashFilter{RepositoryID: repo.ID, Page: 1, Limit: 1})
	if err != nil {
		h.log.Errorw("empty trash: count failed", "repository", repo.Name, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	writeJSON(w, http.StatusOK, responses.TrashEmptyResponse{Purged: purged, Remaining: remaining})
}

// purgeBatch purges items one by one (blobs, quota, audit, events like the
// single purge). An item that vanished meanwhile is skipped; any other failure
// stops the batch and is returned with the number purged so far.
func (h *TrashHandler) purgeBatch(ctx context.Context, store database.TrashStore, items []*models.TrashItem) (int, error) {
	purged := 0

	for _, it := range items {
		res, err := store.PurgeTrash(ctx, it.Kind, it.ID)
		if errors.Is(err, database.ErrTrashNotFound) {
			continue
		}

		if err != nil {
			return purged, err
		}

		h.afterPurge(ctx, res)
		trash.AuditPurged(ctx, res, "manual")
		h.emitPurged(ctx, res)

		purged++
	}

	return purged, nil
}

// afterPurge does the cleanup the database could not: free the blobs (a
// repository's are swept as orphans afterwards, there can be many) and give
// the quota back.
func (h *TrashHandler) afterPurge(ctx context.Context, res *models.TrashPurgeResult) {
	if res.Kind == models.TrashKindRepository {
		h.reclaimMetadataBlobs(ctx, res.RepositoryName, res.MetadataBlobSHA256)
		collectOrphansAsync(h.tasks, h.db, h.blobStore, h.log)

		return
	}

	ctx = context.WithoutCancel(ctx)

	if h.blobStore != nil {
		for _, sha := range res.BlobSHA256 {
			if err := storage.SafeDeleteBlob(ctx, h.db, h.blobStore, sha); err != nil {
				h.log.Warnw("trash purge: free blob failed", "sha256", sha, "repository", res.RepositoryName, "error", err)
			}
		}
	}

	if res.QuotaArtifacts > 0 {
		if err := h.db.DecrementStorageUsage(ctx, res.RepositoryID, res.QuotaBytes, res.QuotaArtifacts); err != nil {
			h.log.Warnw("trash purge: release quota failed", "repository", res.RepositoryName, "error", err)
		}
	}
}

// reclaimMetadataBlobs frees the generated-metadata blobs a purged repository
// owned; best effort, they are only space.
func (h *TrashHandler) reclaimMetadataBlobs(ctx context.Context, repo string, shas []string) {
	reclaimMetadataBlobs(ctx, h.db, h.blobStore, h.log, repo, shas)
}

func (h *TrashHandler) emitPurged(ctx context.Context, res *models.TrashPurgeResult) {
	if res.Kind == models.TrashKindRepository {
		// Same payload as the retention purge worker: the row is gone, so no type.
		payload := webhook.RepositoryPayload(webhook.EventRepoPurged, res.RepositoryName, res.Format, "")
		delete(payload, "type")
		webhook.Emit(ctx, h.publisher, res.RepositoryID, webhook.EventRepoPurged, payload)

		return
	}

	webhook.Emit(ctx, h.publisher, res.RepositoryID, webhook.EventArtifactPurged,
		webhook.ArtifactPayload(webhook.EventArtifactPurged, webhook.Artifact{
			Repository: res.RepositoryName, Format: res.Format, Name: res.Name, Version: res.Version, Size: res.SizeBytes,
		}))
}
