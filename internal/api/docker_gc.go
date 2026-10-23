// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/internal/worker"
	"github.com/openartifact/openartifact/pkg/logger"
)

// maxDockerGCGraceSeconds caps a requested grace period (100 years) so the
// conversion to time.Duration cannot overflow.
const maxDockerGCGraceSeconds = 100 * 365 * 24 * 60 * 60

// DockerGCRunner runs Docker registry garbage collection. It is satisfied by
// *worker.DockerGCWorker.
type DockerGCRunner interface {
	RunRepo(ctx context.Context, repo *models.Repository, opts worker.DockerGCOptions) (*worker.DockerGCReport, error)
	DefaultGrace() time.Duration
}

// DockerGCHandler serves the admin endpoint that runs Docker registry garbage
// collection (untagged manifests and the blobs only they referenced) on demand.
type DockerGCHandler struct {
	db        database.Database
	log       *logger.Logger
	evaluator *auth.Evaluator
	runner    DockerGCRunner
}

func NewDockerGCHandler(db database.Database, log *logger.Logger, evaluator *auth.Evaluator, runner DockerGCRunner) *DockerGCHandler {
	return &DockerGCHandler{db: db, log: log, evaluator: evaluator, runner: runner}
}

func (h *DockerGCHandler) RegisterAuthed(r chi.Router) {
	r.Post("/api/v1/repositories/{name}/registry-gc", h.handleRun)
}

type dockerGCRequest struct {
	DryRun bool `json:"dry_run"`
	// GracePeriodSeconds overrides the configured grace period; 0 disables the
	// grace period. Omitted means the configured one.
	GracePeriodSeconds *int64 `json:"grace_period_seconds"`
}

type dockerGCManifestEntry struct {
	Image  string `json:"image"`
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
}

type dockerGCBlobEntry struct {
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
}

type dockerGCResponse struct {
	Repository         string `json:"repository"`
	DryRun             bool   `json:"dry_run"`
	GracePeriodSeconds int64  `json:"grace_period_seconds"`
	ManifestsDeleted   int    `json:"manifests_deleted"`
	BlobRefsDeleted    int    `json:"blob_refs_deleted"`
	BlobsFreed         int    `json:"blobs_freed"`
	BytesFreed         int64  `json:"bytes_freed"`
	Failures           int    `json:"failures"`
	// Manifests and Blobs list what was deleted (would be, in a dry run), capped
	// at worker.DockerGCReportLimit entries each; Truncated is set when capped.
	Manifests []dockerGCManifestEntry `json:"manifests"`
	Blobs     []dockerGCBlobEntry     `json:"blobs"`
	Truncated bool                    `json:"truncated"`
}

func (h *DockerGCHandler) handleRun(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	var body dockerGCRequest
	if err := decodeJSON(r, &body); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}

	grace := h.runner.DefaultGrace()

	if body.GracePeriodSeconds != nil {
		if *body.GracePeriodSeconds < 0 || *body.GracePeriodSeconds > maxDockerGCGraceSeconds {
			writeError(w, http.StatusBadRequest, "validation_failed", "grace_period_seconds must be between 0 and 3153600000")
			return
		}

		grace = time.Duration(*body.GracePeriodSeconds) * time.Second
	}

	repo, err := h.db.GetRepositoryByName(r.Context(), urlparams.Vars(r)["name"])
	if err != nil {
		h.log.Errorw("docker gc: db error getting repo", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if repo == nil {
		writeError(w, http.StatusNotFound, "not_found", "repository not found")
		return
	}

	// A large registry can take longer than the server write timeout.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})

	// Detached from the request: a client that gives up must not leave a run
	// half done; the run stops on shutdown or after its own timeout.
	report, err := h.runner.RunRepo(context.WithoutCancel(r.Context()), repo, worker.DockerGCOptions{DryRun: body.DryRun, Grace: grace})
	if err != nil {
		switch {
		case errors.Is(err, worker.ErrNotDockerRepo):
			writeError(w, http.StatusBadRequest, "validation_failed", "repository is not a docker repository")
		case errors.Is(err, worker.ErrDockerGCBusy):
			writeError(w, http.StatusConflict, "conflict", "garbage collection is already running for this repository")
		default:
			h.log.Errorw("docker gc failed", "repository", repo.Name, "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "garbage collection failed")
		}

		return
	}

	writeJSON(w, http.StatusOK, newDockerGCResponse(report))
}

func newDockerGCResponse(rep *worker.DockerGCReport) dockerGCResponse {
	resp := dockerGCResponse{
		Repository:         rep.Repository,
		DryRun:             rep.DryRun,
		GracePeriodSeconds: int64(rep.GracePeriod / time.Second),
		ManifestsDeleted:   rep.ManifestsDeleted,
		BlobRefsDeleted:    rep.BlobRefsDeleted,
		BlobsFreed:         rep.BlobsFreed,
		BytesFreed:         rep.BytesFreed,
		Failures:           rep.Failures,
		Manifests:          make([]dockerGCManifestEntry, len(rep.Manifests)),
		Blobs:              make([]dockerGCBlobEntry, len(rep.Blobs)),
		Truncated:          rep.Truncated,
	}

	for i, m := range rep.Manifests {
		resp.Manifests[i] = dockerGCManifestEntry{Image: m.Image, Digest: m.Digest, Size: m.Size}
	}

	for i, b := range rep.Blobs {
		resp.Blobs[i] = dockerGCBlobEntry{Digest: b.Digest, Size: b.Size}
	}

	return resp
}
