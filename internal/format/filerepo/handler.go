// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package filerepo is the HTTP handler shared by the hosted formats that store
// plain files addressed by a Maven-style group/module/version/filename path
// (ivy and sbt). A format supplies a Spec naming its table accessors and
// labels; routes, responses, the immutability guard, quota, trash and webhook
// behaviour are identical for all of them.
package filerepo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"strconv"
	"time"

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
)

const maxUploadBytes = 500 << 20

// Row is the format-independent view of a stored file row.
type Row struct {
	ID        string
	SHA256    string
	BlobKey   string
	SizeBytes int64
}

// Spec holds what differs between the formats served by Handler. A is the
// format's row model.
type Spec[A any] struct {
	// Format is the format name used in logs, webhook payloads and the
	// immutability guard.
	Format string
	// TrashKind is the trash kind of the format's table.
	TrashKind models.TrashKind
	// GroupKey is the blob metadata key that carries the group coordinate.
	GroupKey string

	// Get returns the live row at the coordinates, or nil.
	Get func(ctx context.Context, db database.Database, repoID string, c base.ArtifactPath) (*A, error)
	// New builds the row an upload of the file at c stores.
	New func(repoID string, c base.ArtifactPath, size int64, sha256Hex string) *A
	// Create stores the row (upserting an existing coordinate).
	Create func(ctx context.Context, db database.Database, a *A) error
	// Delete removes the row.
	Delete func(ctx context.Context, db database.Database, id string) error
	// View exposes the shared fields of a row.
	View func(a *A) Row
	// Stamp records the blob key and creation time on a row about to be stored.
	Stamp func(a *A, blobKey string, createdAt time.Time)
}

// Handler serves one file-repository format.
type Handler[A any] struct {
	*base.Handler

	spec Spec[A]
}

// New creates the handler of the format described by spec.
func New[A any](spec Spec[A], log *logger.Logger, db database.Database, blobStore storage.BlobStore, baseURL string) *Handler[A] {
	return &Handler[A]{Handler: base.New(log, db, blobStore, baseURL), spec: spec}
}

// RegisterRepoRoutes registers the format's routes on the provided sub-router.
// It does NOT wrap in r.Route() — callers are responsible for the path prefix.
// Routes use a true chi wildcard ("/*"): {name:regexp} params never span "/".
// chi does not answer HEAD from a GET route, so it is registered explicitly:
// resolvers probe for a file with HEAD before fetching it.
func (h *Handler[A]) RegisterRepoRoutes(r chi.Router) {
	r.Get("/*", h.handleRequest)
	r.Head("/*", h.handleRequest)
	r.Put("/*", h.handleRequest)
	r.Delete("/*", h.handleRequest)
}

// RegisterRoutes registers the routes under prefix.
func (h *Handler[A]) RegisterRoutes(r chi.Router, prefix string) {
	r.Route(prefix, func(r chi.Router) {
		h.RegisterRepoRoutes(r)
	})
	h.Log.Infow(h.spec.Format+" handler registered", "prefix", prefix)
}

func (h *Handler[A]) handleRequest(w http.ResponseWriter, r *http.Request) {
	vars := urlparams.Vars(r)
	repoName := vars["name"]
	filePath := vars["*"]

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

	switch r.Method {
	case http.MethodGet, http.MethodHead:
		h.handleDownload(w, r, repoID, filePath)
	case http.MethodPut:
		h.handleUpload(w, r, repoID, filePath)
	case http.MethodDelete:
		h.handleDelete(w, r, repoID, filePath)
	}
}

func repoNameOf(r *http.Request) string { return urlparams.Vars(r)["name"] }

func (h *Handler[A]) get(ctx context.Context, repoID string, c base.ArtifactPath) (*A, error) {
	return h.spec.Get(ctx, h.DB, repoID, c)
}

// lookup finds the artifact at cur, else the one an older release stored under
// the legacy reading of the same path.
func (h *Handler[A]) lookup(ctx context.Context, repoID string, cur, legacy base.ArtifactPath) (*A, error) {
	artifact, err := h.get(ctx, repoID, cur)
	if err != nil || artifact != nil || cur == legacy {
		return artifact, err
	}

	return h.get(ctx, repoID, legacy)
}

// handleDownload serves a file (GET) or just its headers (HEAD).
func (h *Handler[A]) handleDownload(w http.ResponseWriter, r *http.Request, repoID, filePath string) {
	cur, legacy, ok := base.SplitArtifactPath(filePath)
	if !ok {
		http.NotFound(w, r)
		return
	}

	artifact, err := h.lookup(r.Context(), repoID, cur, legacy)
	if err != nil {
		h.Log.Errorw("failed to get "+h.spec.Format+" artifact", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if artifact == nil {
		http.NotFound(w, r)
		return
	}

	row := h.spec.View(artifact)

	if h.DenyIfQuarantined(w, r, repoID, quarantine.PathKeys(filePath, cur.Filename, r.URL.Path)...) {
		return
	}

	if r.Method != http.MethodHead && h.TryPresign(w, r, presign.Blob{SHA256: row.BlobKey, Size: row.SizeBytes, Filename: path.Base(cur.Filename), ContentType: "application/octet-stream"}) {
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(row.SizeBytes, 10))

	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}

	blob, err := h.BlobStore.Get(r.Context(), row.BlobKey)
	if err != nil {
		h.Log.Errorw("failed to get blob", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}
	defer func() { _ = blob.Close() }()

	w.WriteHeader(http.StatusOK)
	_, _ = iocopy.Copy(w, blob)
}

// lookupForUpload returns the row an upload replaces (previous: current
// coordinates only) and the row it collides with (existing: previous, else one
// stored under the legacy reading of the path), so the immutability guard
// cannot be sidestepped by shadowing a legacy row.
func (h *Handler[A]) lookupForUpload(ctx context.Context, repoID string, cur, legacy base.ArtifactPath) (*A, *A, error) {
	previous, err := h.get(ctx, repoID, cur)
	if err != nil || previous != nil || cur == legacy {
		return previous, previous, err
	}

	existing, err := h.get(ctx, repoID, legacy)

	return nil, existing, err
}

// handleUpload stores a file. Publishing the same bytes again is a no-op;
// different bytes replace the file unless the repository has immutable releases
// and the version is a release.
func (h *Handler[A]) handleUpload(w http.ResponseWriter, r *http.Request, repoID, filePath string) {
	cur, legacy, ok := base.SplitArtifactPath(filePath)
	if !ok {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	spool, err := base.SpoolLimited(r.Body, maxUploadBytes)
	if err != nil {
		if errors.Is(err, base.ErrContentTooLarge) {
			http.Error(w, "upload too large", http.StatusRequestEntityTooLarge)
			return
		}

		http.Error(w, "failed to read upload", http.StatusBadRequest)

		return
	}
	defer func() { _ = r.Body.Close() }()
	defer spool.Close()

	if licensepolicy.Enforce(w, r, h.DB, h.Log, repoID, base.SpoolDescriptorLicenses(cur.Filename, spool)...) {
		return
	}

	sha256Hex := spool.SHA256Hex()
	size := spool.Size()

	artifact := h.spec.New(repoID, cur, size, sha256Hex)

	previous, existing, err := h.lookupForUpload(r.Context(), repoID, cur, legacy)
	if err != nil {
		h.Log.Errorw("failed to look up "+h.spec.Format+" artifact", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if existing != nil {
		if h.spec.View(existing).SHA256 == sha256Hex {
			h.writeStored(w, existing)
			return
		}

		guardVersion := cur.Version
		if previous == nil {
			guardVersion = legacy.Version
		}

		if guardErr := trash.GuardReleaseByName(r, h.DB, repoNameOf(r), h.spec.Format, guardVersion); guardErr != nil {
			h.rejectImmutable(w, filePath, guardErr)
			return
		}
	}

	// Reserve quota before anything is persisted; every failure path below
	// returns the reservation.
	if err := quota.Enforce(r.Context(), h.DB, repoID, size); err != nil {
		if errors.Is(err, quota.ErrExceeded) {
			quota.WriteExceeded(w)
			return
		}

		h.Log.Errorw("failed to reserve quota", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	// The blob is stored first; its existence is re-checked and the
	// referencing row inserted under the per-blob lock (DB work only) so a
	// concurrent unpublish cannot delete the blob in between.
	blob := storage.SeekerBlob(sha256Hex, spool.Reader(), map[string]string{
		"sha256":        sha256Hex,
		"filename":      cur.Filename,
		h.spec.GroupKey: cur.Group,
	})

	if err := storage.CommitBlob(r.Context(), h.DB, h.BlobStore, blob, func(ctx context.Context, blobKey string) error {
		h.spec.Stamp(artifact, blobKey, importmeta.CreatedAt(ctx))

		if err := h.spec.Create(ctx, h.DB, artifact); err != nil {
			return fmt.Errorf("create %s artifact: %w", h.spec.Format, err)
		}

		return nil
	}); err != nil {
		if relErr := quota.Release(r.Context(), h.DB, repoID, size); relErr != nil {
			h.Log.Warnw("failed to release quota", "error", relErr)
		}

		h.Log.Errorw("failed to store "+h.spec.Format+" artifact", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if previous != nil {
		prev := h.spec.View(previous)

		// The replaced row's usage is gone once the new content is in place.
		if relErr := quota.Release(r.Context(), h.DB, repoID, prev.SizeBytes); relErr != nil {
			h.Log.Warnw("failed to release quota", "error", relErr)
		}

		// The replaced bytes are content-addressed and may be shared; drop them only
		// once nothing references them.
		if delErr := storage.SafeDeleteBlob(r.Context(), h.DB, h.BlobStore, prev.SHA256); delErr != nil {
			h.Log.Warnw("failed to clean up replaced "+h.spec.Format+" blob", "sha256", prev.SHA256, "error", delErr)
		}
	}

	h.Emit(r.Context(), repoID, webhook.EventArtifactUploaded, webhook.ArtifactPayload(webhook.EventArtifactUploaded, webhook.Artifact{
		Repository: repoNameOf(r),
		Format:     h.spec.Format,
		Name:       cur.Group + "/" + cur.Module,
		Version:    cur.Version,
		Path:       filePath,
		SHA256:     sha256Hex,
		Size:       size,
	}))

	h.writeStored(w, artifact)
}

func (h *Handler[A]) writeStored(w http.ResponseWriter, a *A) {
	row := h.spec.View(a)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)

	if encErr := json.NewEncoder(w).Encode(map[string]any{
		"id":     row.ID,
		"sha256": row.SHA256,
	}); encErr != nil {
		_ = encErr // response header already written
	}
}

// rejectImmutable answers a refused overwrite or delete: 409 when the
// repository has immutable releases.
func (h *Handler[A]) rejectImmutable(w http.ResponseWriter, filePath string, err error) {
	if errors.Is(err, trash.ErrImmutableRelease) {
		http.Error(w, "released versions are immutable in this repository", http.StatusConflict)
		return
	}

	h.Log.Errorw("failed to check release immutability", "path", filePath, "error", err)
	http.Error(w, "internal error", http.StatusInternalServerError)
}

func (h *Handler[A]) handleDelete(w http.ResponseWriter, r *http.Request, repoID, filePath string) {
	cur, legacy, ok := base.SplitArtifactPath(filePath)
	if !ok {
		http.NotFound(w, r)
		return
	}

	artifact, err := h.lookup(r.Context(), repoID, cur, legacy)
	if err != nil {
		h.Log.Errorw("failed to get "+h.spec.Format+" artifact", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if artifact == nil {
		http.NotFound(w, r)
		return
	}

	row := h.spec.View(artifact)

	if guardErr := trash.GuardReleaseByName(r, h.DB, repoNameOf(r), h.spec.Format, cur.Version); guardErr != nil {
		h.rejectImmutable(w, filePath, guardErr)
		return
	}

	err = trash.Delete(r.Context(), h.DB, trash.Request{
		RepoID: repoID, Kind: h.spec.TrashKind, IDs: []string{row.ID}, Resource: filePath,
	}, func() error {
		if delErr := h.spec.Delete(r.Context(), h.DB, row.ID); delErr != nil {
			return delErr
		}

		if relErr := quota.Release(r.Context(), h.DB, repoID, row.SizeBytes); relErr != nil {
			h.Log.Warnw("failed to release quota", "error", relErr)
		}

		// The blob is content-addressed and may be shared; remove it only once no
		// row references it any more.
		if delErr := storage.SafeDeleteBlob(r.Context(), h.DB, h.BlobStore, row.SHA256); delErr != nil {
			h.Log.Warnw("failed to clean up blob after "+h.spec.Format+" delete", "sha256", row.SHA256, "error", delErr)
		}

		return nil
	})
	if err != nil {
		if errors.Is(err, database.ErrTrashNotFound) {
			http.NotFound(w, r)
			return
		}

		h.Log.Errorw("failed to delete "+h.spec.Format+" artifact", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	h.Emit(r.Context(), repoID, webhook.EventArtifactDeleted, webhook.ArtifactPayload(webhook.EventArtifactDeleted, webhook.Artifact{
		Repository: repoNameOf(r),
		Format:     h.spec.Format,
		Name:       cur.Group + "/" + cur.Module,
		Version:    cur.Version,
		Path:       filePath,
		SHA256:     row.SHA256,
		Size:       row.SizeBytes,
	}))

	w.WriteHeader(http.StatusNoContent)
}
