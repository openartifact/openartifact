// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/api"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/backup"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/internal/storage"
)

// TestBackupJobsAcceptEveryTypeAndStatusTheCodeWrites pins the backup_jobs
// check constraints to the values the backup code really writes: the handler
// accepts 'full' and 'metadata-only', the engine finishes jobs as 'completed'.
// The schema once allowed ('full','incremental') and
// ('pending','running','success','failed'), so a metadata-only backup was
// refused with a 500 and no backup could ever be marked completed.
func TestBackupJobsAcceptEveryTypeAndStatusTheCodeWrites(t *testing.T) {
	pg := freshPostgres(t, 5)
	ctx := context.Background()

	tests := []struct {
		name      string
		jobType   string
		status    string
		wantAllow bool
	}{
		{"full pending", "full", "pending", true},
		{"metadata-only pending", "metadata-only", "pending", true},
		{"running", "full", "running", true},
		{"completed", "metadata-only", "completed", true},
		{"failed", "full", "failed", true},
		{"incremental was never implemented", "incremental", "pending", false},
		{"unknown type", "bogus", "pending", false},
		{"success is not a status the code writes", "full", "success", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Now()
			job := &models.BackupJob{Status: tc.status, Type: tc.jobType, CreatedAt: now, UpdatedAt: now}

			err := pg.CreateBackupJob(ctx, job)
			if tc.wantAllow && err != nil {
				t.Fatalf("CreateBackupJob(type=%q, status=%q): %v", tc.jobType, tc.status, err)
			}

			if !tc.wantAllow && err == nil {
				t.Fatalf("CreateBackupJob(type=%q, status=%q) succeeded; the schema must refuse it", tc.jobType, tc.status)
			}
		})
	}

	t.Run("a job moves through the statuses the engine uses", func(t *testing.T) {
		now := time.Now()
		job := &models.BackupJob{Status: "pending", Type: "metadata-only", CreatedAt: now, UpdatedAt: now}

		if err := pg.CreateBackupJob(ctx, job); err != nil {
			t.Fatalf("CreateBackupJob: %v", err)
		}

		for _, status := range []string{"running", "completed"} {
			job.Status = status
			if err := pg.UpdateBackupJob(ctx, job); err != nil {
				t.Fatalf("UpdateBackupJob(status=%q): %v", status, err)
			}
		}

		got, err := pg.GetBackupJob(ctx, job.ID)
		if err != nil || got == nil || got.Status != "completed" || got.Type != "metadata-only" {
			t.Fatalf("GetBackupJob = %+v, %v; want a completed metadata-only job", got, err)
		}
	})
}

func backupRouter(t *testing.T, pg *database.PostgresDB, backupDir string) chi.Router {
	t.Helper()

	log := newTestLogger(t)

	blobStore, err := storage.NewFilesystemStore(filepath.Join(t.TempDir(), "blobs"))
	if err != nil {
		t.Fatalf("blob store: %v", err)
	}

	h := api.NewBackupHandler(pg, log, auth.NewEvaluator(pg, log, false), backup.NewEngine(pg, blobStore, log, backupDir))

	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			p := &auth.Principal{Username: "admin", IsLegacy: true}
			next.ServeHTTP(w, req.WithContext(middleware.WithPrincipal(req.Context(), p)))
		})
	})
	h.RegisterAuthed(r)

	return r
}

func postBackup(t *testing.T, r http.Handler, backupType string) *httptest.ResponseRecorder {
	t.Helper()

	body, _ := json.Marshal(map[string]string{"type": backupType})
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/admin/backup", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	return w
}

// waitBackupJob polls the job until the engine leaves pending/running.
func waitBackupJob(t *testing.T, pg *database.PostgresDB, id string) *models.BackupJob {
	t.Helper()

	deadline := time.Now().Add(30 * time.Second)

	for time.Now().Before(deadline) {
		job, err := pg.GetBackupJob(context.Background(), id)
		if err != nil || job == nil {
			t.Fatalf("GetBackupJob(%s) = %v, %v", id, job, err)
		}

		if job.Status != "pending" && job.Status != "running" {
			return job
		}

		time.Sleep(50 * time.Millisecond)
	}

	t.Fatalf("backup job %s still unfinished after 30s", id)

	return nil
}

// TestBackupAPIRunsEveryAcceptedTypeEndToEnd drives POST /api/v1/admin/backup
// against the real schema: both accepted types must be created (202), run to
// 'completed' and leave a downloadable archive. 'incremental' (never
// implemented, only ever allowed by the old constraint) is rejected up front.
func TestBackupAPIRunsEveryAcceptedTypeEndToEnd(t *testing.T) {
	pg := freshPostgres(t, 5)
	backupDir := filepath.Join(t.TempDir(), "backups")
	r := backupRouter(t, pg, backupDir)

	for _, typ := range []string{"metadata-only", "full"} {
		t.Run(typ, func(t *testing.T) {
			w := postBackup(t, r, typ)
			if w.Code != http.StatusAccepted {
				t.Fatalf("POST type=%q: status = %d, want 202; body: %s", typ, w.Code, w.Body.String())
			}

			var created struct {
				ID   string `json:"id"`
				Type string `json:"type"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil || created.ID == "" || created.Type != typ {
				t.Fatalf("create response = %s (%v), want an id and type %q", w.Body.String(), err, typ)
			}

			job := waitBackupJob(t, pg, created.ID)
			if job.Status != "completed" {
				t.Fatalf("job status = %q (error: %q), want completed", job.Status, job.ErrorMessage)
			}

			if job.Type != typ || job.SizeBytes <= 0 {
				t.Errorf("job = type %q, %d bytes; want type %q and a non-empty archive", job.Type, job.SizeBytes, typ)
			}

			if _, err := os.Stat(job.StoragePath); err != nil {
				t.Errorf("archive %q missing: %v", job.StoragePath, err)
			}

			dl := httptest.NewRecorder()
			r.ServeHTTP(dl, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/admin/backup/"+created.ID+"/download", nil))

			if dl.Code != http.StatusOK || dl.Body.Len() == 0 {
				t.Errorf("download: status = %d, %d bytes; want 200 and the archive", dl.Code, dl.Body.Len())
			}
		})
	}

	for _, typ := range []string{"incremental", "metadata", ""} {
		t.Run("rejects "+typ, func(t *testing.T) {
			if w := postBackup(t, r, typ); w.Code != http.StatusBadRequest {
				t.Errorf("POST type=%q: status = %d, want 400; body: %s", typ, w.Code, w.Body.String())
			}
		})
	}
}
