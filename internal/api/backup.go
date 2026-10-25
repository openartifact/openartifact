// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/api/responses"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/backup"
	"github.com/openartifact/openartifact/internal/bgtask"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/pkg/logger"
)

type BackupHandler struct {
	db           database.Database
	log          *logger.Logger
	evaluator    *auth.Evaluator
	backupEngine *backup.Engine
	tasks        *bgtask.Runner
}

func NewBackupHandler(db database.Database, log *logger.Logger, evaluator *auth.Evaluator, backupEngine *backup.Engine) *BackupHandler {
	return &BackupHandler{
		db:           db,
		log:          log,
		evaluator:    evaluator,
		backupEngine: backupEngine,
		tasks:        bgtask.New(context.Background()),
	}
}

// WithTasks runs the async backups and restores under the server lifecycle:
// they are cancelled and awaited when the runner shuts down.
func (h *BackupHandler) WithTasks(tasks *bgtask.Runner) *BackupHandler {
	h.tasks = tasks
	return h
}

func (h *BackupHandler) RegisterAuthed(r chi.Router) {
	r.Post("/api/v1/admin/backup", h.handleCreateBackup)
	r.Get("/api/v1/admin/backup", h.handleListBackups)
	r.Get("/api/v1/admin/backup/{id}", h.handleGetBackup)
	r.Get("/api/v1/admin/backup/{id}/download", h.handleDownloadBackup)
	r.Post("/api/v1/admin/restore", h.handleRestore)
}

func (h *BackupHandler) handleCreateBackup(w http.ResponseWriter, r *http.Request) {
	p := currentPrincipal(r)

	isAdmin, err := principalIsAdmin(r.Context(), p, h.evaluator)
	if err != nil || !isAdmin {
		writeError(w, http.StatusForbidden, "forbidden", "admin access required")
		return
	}

	var body struct {
		Type string `json:"type"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}

	// Validate backup type
	if body.Type != "full" && body.Type != "metadata-only" {
		writeError(w, http.StatusBadRequest, "validation_failed", "type must be 'full' or 'metadata-only'")
		return
	}

	// Create backup job
	now := time.Now()
	job := &models.BackupJob{
		Status:    "pending",
		Type:      body.Type,
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := h.db.CreateBackupJob(r.Context(), job); err != nil {
		h.log.Errorw("failed to create backup job", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	// Start backup asynchronously: it outlives the request but not the server.
	h.tasks.Go(func(ctx context.Context) {
		defer func() {
			if r := recover(); r != nil {
				h.log.Errorw("panic in goroutine", "panic", r)
			}
		}()

		ctx, cancel := context.WithTimeout(ctx, 24*time.Hour)
		defer cancel()

		if err := h.backupEngine.RunBackup(ctx, job.ID, body.Type); err != nil {
			h.log.Errorw("backup failed", "job_id", job.ID, "error", err)
		}
	})

	writeJSON(w, http.StatusAccepted, map[string]any{
		"id":         job.ID,
		"status":     job.Status,
		"type":       job.Type,
		"created_at": job.CreatedAt,
	})
}

func (h *BackupHandler) handleListBackups(w http.ResponseWriter, r *http.Request) {
	p := currentPrincipal(r)

	isAdmin, err := principalIsAdmin(r.Context(), p, h.evaluator)
	if err != nil || !isAdmin {
		writeError(w, http.StatusForbidden, "forbidden", "admin access required")
		return
	}

	jobs, err := h.db.ListBackupJobs(r.Context(), 100)
	if err != nil {
		h.log.Errorw("failed to list backup jobs", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if jobs == nil {
		jobs = []*models.BackupJob{}
	}

	var result []responses.BackupJobResponse
	for _, job := range jobs {
		result = append(result, backupJobToMap(job))
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"backups": result,
	})
}

func (h *BackupHandler) handleGetBackup(w http.ResponseWriter, r *http.Request) {
	p := currentPrincipal(r)

	isAdmin, err := principalIsAdmin(r.Context(), p, h.evaluator)
	if err != nil || !isAdmin {
		writeError(w, http.StatusForbidden, "forbidden", "admin access required")
		return
	}

	id := urlparams.Vars(r)["id"]

	job, err := h.db.GetBackupJob(r.Context(), id)
	if err != nil {
		h.log.Errorw("failed to get backup job", "id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if job == nil {
		writeError(w, http.StatusNotFound, "not_found", "backup job not found")
		return
	}

	writeJSON(w, http.StatusOK, backupJobToMap(job))
}

func (h *BackupHandler) handleDownloadBackup(w http.ResponseWriter, r *http.Request) {
	p := currentPrincipal(r)

	isAdmin, err := principalIsAdmin(r.Context(), p, h.evaluator)
	if err != nil || !isAdmin {
		writeError(w, http.StatusForbidden, "forbidden", "admin access required")
		return
	}

	id := urlparams.Vars(r)["id"]

	job, err := h.db.GetBackupJob(r.Context(), id)
	if err != nil {
		h.log.Errorw("failed to get backup job", "id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if job == nil {
		writeError(w, http.StatusNotFound, "not_found", "backup job not found")
		return
	}

	if job.Status != "completed" {
		writeError(w, http.StatusBadRequest, "bad_request", "backup is not completed")
		return
	}

	if job.StoragePath == "" {
		writeError(w, http.StatusInternalServerError, "internal_error", "backup file not found")
		return
	}

	// Open the backup file
	//nolint:gosec // G703: path is from validated database record, not user input
	file, err := os.Open(job.StoragePath)
	if err != nil {
		h.log.Errorw("failed to open backup file", "path", job.StoragePath, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to open backup file")

		return
	}
	defer func() { _ = file.Close() }()

	// Set response headers
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=backup_%s.tar.gz", id))
	w.Header().Set("Content-Length", strconv.FormatInt(job.SizeBytes, 10))

	// Stream the file
	if _, err := io.Copy(w, file); err != nil {
		h.log.Errorw("failed to stream backup file", "id", id, "error", err)
	}
}

// maxRestoreUploadBytes caps the restore archive upload size (10 GiB).
const maxRestoreUploadBytes = 10 << 30

func (h *BackupHandler) handleRestore(w http.ResponseWriter, r *http.Request) {
	p := currentPrincipal(r)

	isAdmin, err := principalIsAdmin(r.Context(), p, h.evaluator)
	if err != nil || !isAdmin {
		writeError(w, http.StatusForbidden, "forbidden", "admin access required")
		return
	}

	// Refuse before spooling the archive; RunRestore still guards the race
	// between this check and the start of the goroutine.
	if h.backupEngine.RestoreInProgress() {
		writeError(w, http.StatusConflict, "conflict", "a restore is already in progress")
		return
	}

	// Stream the multipart body straight to disk with a hard size cap
	// instead of buffering/parsing the whole form.
	r.Body = http.MaxBytesReader(w, r.Body, maxRestoreUploadBytes)

	mr, err := r.MultipartReader()
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "multipart body required")
		return
	}

	var part *multipart.Part

	for {
		p, perr := mr.NextPart()
		if perr != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "file field is required")
			return
		}

		if p.FormName() == "file" {
			part = p
			break
		}
	}

	filename := part.FileName()

	tempFile, err := os.CreateTemp("", "restore_*.tar.gz")
	if err != nil {
		h.log.Errorw("failed to create temp file", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to create temp file")

		return
	}

	tempPath := tempFile.Name()

	if _, err := io.Copy(tempFile, part); err != nil {
		_ = tempFile.Close()
		_ = os.Remove(tempPath)

		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			writeError(w, http.StatusRequestEntityTooLarge, "too_large", "backup archive too large")
			return
		}

		h.log.Errorw("failed to write temp file", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to write temp file")

		return
	}

	_ = tempFile.Close()

	// Start restore asynchronously; the task owns (and removes) the temp file
	// once the restore finishes, and ends with the server.
	h.tasks.Go(func(ctx context.Context) {
		defer func() { _ = os.Remove(tempPath) }()
		defer func() {
			if r := recover(); r != nil {
				h.log.Errorw("panic in goroutine", "panic", r)
			}
		}()

		ctx, cancel := context.WithTimeout(ctx, 24*time.Hour)
		defer cancel()

		if err := h.backupEngine.RunRestore(ctx, tempPath); err != nil {
			h.log.Errorw("restore failed", "filename", filename, "error", err)
		}
	})

	writeJSON(w, http.StatusAccepted, map[string]any{
		"message":  "restore started",
		"filename": filename,
	})
}

func backupJobToMap(job *models.BackupJob) responses.BackupJobResponse {
	var errorMessage *string
	if job.ErrorMessage != "" {
		errorMessage = &job.ErrorMessage
	}

	return responses.BackupJobResponse{
		ID:           job.ID,
		Status:       job.Status,
		Type:         job.Type,
		SizeBytes:    job.SizeBytes,
		CreatedAt:    formatTime(job.CreatedAt),
		UpdatedAt:    formatTime(job.UpdatedAt),
		ErrorMessage: errorMessage,
		StartedAt:    formatTimePtr(job.StartedAt),
		CompletedAt:  formatTimePtr(job.CompletedAt),
	}
}
