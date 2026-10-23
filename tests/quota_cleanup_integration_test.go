// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/worker"
)

// cleanupQuotaEnv is a real Postgres + filesystem blob store with a cleanup
// worker over them, and helpers to seed package rows the way an upload leaves
// them: blob bytes in the store, a blob row, a package row and the quota
// reservation.
type cleanupQuotaEnv struct {
	t      *testing.T
	pg     *database.PostgresDB
	store  storage.BlobStore
	worker *worker.CleanupWorker
	seq    int
}

func newCleanupQuotaEnv(t *testing.T) *cleanupQuotaEnv {
	t.Helper()

	pg := freshPostgres(t, 10)

	store, err := storage.NewFilesystemStore(filepath.Join(t.TempDir(), "blobs"))
	if err != nil {
		t.Fatalf("blob store: %v", err)
	}

	return &cleanupQuotaEnv{t: t, pg: pg, store: store, worker: worker.NewCleanupWorker(pg, store, newTestLogger(t))}
}

func (e *cleanupQuotaEnv) exec(query string, args ...any) {
	e.t.Helper()

	if _, err := e.pg.GetDB().ExecContext(context.Background(), query, args...); err != nil {
		e.t.Fatalf("exec %q: %v", query, err)
	}
}

func (e *cleanupQuotaEnv) queryID(query string, args ...any) string {
	e.t.Helper()

	var id string
	if err := e.pg.GetDB().QueryRowContext(context.Background(), query, args...).Scan(&id); err != nil {
		e.t.Fatalf("query %q: %v", query, err)
	}

	return id
}

func (e *cleanupQuotaEnv) repo(format string) string {
	e.t.Helper()

	org := models.SystemProjectID

	repo := &models.Repository{Name: format + "-repo", Format: format, RepoType: "hosted", ProjectID: &org}
	if err := e.pg.CreateRepository(context.Background(), repo); err != nil {
		e.t.Fatalf("CreateRepository: %v", err)
	}

	return repo.ID
}

// blob stores size bytes of unique content and returns the blob row id, its
// sha256 and the content size.
func (e *cleanupQuotaEnv) blob(size int) (string, string) {
	e.t.Helper()

	e.seq++
	data := bytes.Repeat([]byte{byte(e.seq)}, size)
	data = append(data, fmt.Appendf(nil, "#%d", e.seq)...)
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])

	key, err := e.store.Put(context.Background(), bytes.NewReader(data), map[string]string{"sha256": sha})
	if err != nil {
		e.t.Fatalf("store blob: %v", err)
	}

	id, err := e.pg.UpsertBlob(context.Background(), sha, key, int64(len(data)))
	if err != nil {
		e.t.Fatalf("UpsertBlob: %v", err)
	}

	return id, sha
}

// pkg inserts one version of a package of the repository's format, aged by
// ageDays, and reserves its quota like an upload would. It returns the blob
// sha256 and the number of bytes reserved.
func (e *cleanupQuotaEnv) pkg(repoID, format, name, version string, size, ageDays int) (string, int64) {
	e.t.Helper()

	blobID, sha := e.blob(size)
	created := time.Now().Add(-time.Duration(ageDays) * 24 * time.Hour)

	switch format {
	case "helm":
		e.exec(`INSERT INTO helm_charts (repository_id, name, version, blob_id, created_at) VALUES ($1, $2, $3, $4, $5)`,
			repoID, name, version, blobID, created)
	case "rpm":
		e.exec(`INSERT INTO rpm_packages (repository_id, blob_id, name, version, arch, created_at) VALUES ($1, $2, $3, $4, 'x86_64', $5)`,
			repoID, blobID, name, version, created)
	case "deb":
		dist := e.queryID(`INSERT INTO debian_distributions (repository_id, name) VALUES ($1, 'stable')
		                   ON CONFLICT (repository_id, name) DO UPDATE SET name = EXCLUDED.name RETURNING id`, repoID)
		comp := e.queryID(`INSERT INTO debian_components (distribution_id, name) VALUES ($1, 'main')
		                   ON CONFLICT (distribution_id, name) DO UPDATE SET name = EXCLUDED.name RETURNING id`, dist)
		e.exec(`INSERT INTO deb_packages (repository_id, distribution_id, component_id, blob_id, name, version, arch, created_at)
		        VALUES ($1, $2, $3, $4, $5, $6, 'amd64', $7)`, repoID, dist, comp, blobID, name, version, created)
	default:
		e.t.Fatalf("unsupported format %q", format)
	}

	var blobBytes int64
	if err := e.pg.GetDB().QueryRowContext(context.Background(), `SELECT size_bytes FROM blobs WHERE id = $1`, blobID).Scan(&blobBytes); err != nil {
		e.t.Fatalf("blob size: %v", err)
	}

	if ok, err := e.pg.ReserveStorageUsage(context.Background(), repoID, blobBytes, 1); err != nil || !ok {
		e.t.Fatalf("ReserveStorageUsage = %v, %v", ok, err)
	}

	return sha, blobBytes
}

// runPolicy creates a keep-last-N policy, runs it through the worker and
// waits for the recorded outcome.
func (e *cleanupQuotaEnv) runPolicy(repoID string, keepLast int) *models.CleanupPolicy {
	e.t.Helper()

	ctx := context.Background()
	policy := &models.CleanupPolicy{RepositoryID: repoID, Name: "keep", KeepLastN: &keepLast}

	if err := e.pg.CreateCleanupPolicy(ctx, policy); err != nil {
		e.t.Fatalf("CreateCleanupPolicy: %v", err)
	}

	if err := e.worker.TriggerPolicy(ctx, policy.ID); err != nil {
		e.t.Fatalf("TriggerPolicy: %v", err)
	}

	deadline := time.Now().Add(30 * time.Second)

	for time.Now().Before(deadline) {
		got, err := e.pg.GetCleanupPolicy(ctx, policy.ID)
		if err != nil || got == nil {
			e.t.Fatalf("GetCleanupPolicy = %v, %v", got, err)
		}

		if got.LastRunAt != nil {
			return got
		}

		time.Sleep(50 * time.Millisecond)
	}

	e.t.Fatal("cleanup policy did not finish within 30s")

	return nil
}

func (e *cleanupQuotaEnv) usage(repoID string) (int64, int64) {
	e.t.Helper()

	var size, count int64

	err := e.pg.GetDB().QueryRowContext(context.Background(),
		`SELECT COALESCE(SUM(current_size_bytes), 0), COALESCE(SUM(current_artifact_count), 0) FROM storage_quotas WHERE repository_id = $1`, repoID,
	).Scan(&size, &count)
	if err != nil {
		e.t.Fatalf("read usage: %v", err)
	}

	return size, count
}

// TestCleanupPolicyReleasesQuota: rows removed by a cleanup policy give their
// usage back. Before the fix the counters only ever grew in repositories that
// relied on cleanup, until uploads were refused with 507 although few
// artifacts were left.
func TestCleanupPolicyReleasesQuota(t *testing.T) {
	for _, format := range []string{"helm", "rpm", "deb"} {
		t.Run(format, func(t *testing.T) {
			e := newCleanupQuotaEnv(t)
			ctx := context.Background()
			repoID := e.repo(format)

			oldSHA, oldBytes := e.pkg(repoID, format, "pkg", "1.0.0", 100, 30)
			midSHA, midBytes := e.pkg(repoID, format, "pkg", "2.0.0", 200, 20)
			_, newBytes := e.pkg(repoID, format, "pkg", "3.0.0", 400, 10)
			_, otherBytes := e.pkg(repoID, format, "other", "1.0.0", 50, 40) // its own newest version: kept

			wantBytes := oldBytes + midBytes + newBytes + otherBytes
			if size, count := e.usage(repoID); size != wantBytes || count != 4 {
				t.Fatalf("usage before cleanup = %d / %d, want %d / 4", size, count, wantBytes)
			}

			got := e.runPolicy(repoID, 1)
			if got.LastRunError != "" || got.LastRunDeleted == nil || *got.LastRunDeleted != 2 {
				t.Fatalf("policy run: deleted = %v, error = %q; want 2 deleted (the two older pkg versions), no error", got.LastRunDeleted, got.LastRunError)
			}

			wantAfter := newBytes + otherBytes
			if size, count := e.usage(repoID); size != wantAfter || count != 2 {
				t.Errorf("usage after cleanup = %d / %d, want %d / 2 (only the kept versions)", size, count, wantAfter)
			}

			sweepQueuedBlobs(t, e.pg, e.store)

			for _, sha := range []string{oldSHA, midSHA} {
				if _, exists, err := e.store.ExistsBySHA256(ctx, sha); err != nil || exists {
					t.Errorf("blob %s of a deleted version: exists=%v err=%v; want removed from storage", sha[:12], exists, err)
				}
			}

			// The counters must now equal a recount of the surviving rows.
			recount, err := e.pg.ReconcileStorageUsage(ctx, repoID)
			if err != nil {
				t.Fatalf("ReconcileStorageUsage: %v", err)
			}

			if recount.CurrentSizeBytes != wantAfter || recount.CurrentArtifactCount != 2 {
				t.Errorf("recount = %d / %d; the incremental release drifted from the rows (want %d / 2)",
					recount.CurrentSizeBytes, recount.CurrentArtifactCount, wantAfter)
			}

			// Headroom is real: with the limit just above what is left, an upload
			// that fits only after the cleanup is accepted.
			q, err := e.pg.GetOrCreateStorageQuota(ctx, repoID)
			if err != nil {
				t.Fatalf("GetOrCreateStorageQuota: %v", err)
			}

			q.MaxSizeBytes = wantAfter + 300
			if err := e.pg.UpdateStorageQuota(ctx, q); err != nil {
				t.Fatalf("UpdateStorageQuota: %v", err)
			}

			if ok, err := e.pg.ReserveStorageUsage(ctx, repoID, 300, 1); err != nil || !ok {
				t.Errorf("upload of 300 bytes after cleanup = %v, %v; want accepted", ok, err)
			}
		})
	}
}

// TestCleanupPolicyRerunDoesNotReleaseTwice: a second run with nothing left to
// delete must not take usage out again.
func TestCleanupPolicyRerunDoesNotReleaseTwice(t *testing.T) {
	e := newCleanupQuotaEnv(t)
	repoID := e.repo("helm")

	_, b1 := e.pkg(repoID, "helm", "chart", "1.0.0", 100, 30)
	_, b2 := e.pkg(repoID, "helm", "chart", "2.0.0", 200, 10)

	for run := 1; run <= 2; run++ {
		e.runPolicy(repoID, 1)

		if size, count := e.usage(repoID); size != b2 || count != 1 {
			t.Fatalf("after run %d: usage = %d / %d, want %d / 1 (the %d-byte version is gone once)", run, size, count, b2, b1)
		}
	}
}

type recordingNotifier struct{ ids []string }

func (r *recordingNotifier) NotifyStale(repoID string) { r.ids = append(r.ids, repoID) }

// TestCleanupPolicyRefreshesMetadata (STOR-3): a cleanup run that deleted rpm
// or deb packages must schedule the regeneration of the repository's index;
// before the fix primary.xml / Packages kept listing files that were gone.
func TestCleanupPolicyRefreshesMetadata(t *testing.T) {
	for _, format := range []string{"rpm", "deb"} {
		t.Run(format, func(t *testing.T) {
			e := newCleanupQuotaEnv(t)
			repoID := e.repo(format)

			rpm, deb := &recordingNotifier{}, &recordingNotifier{}
			e.worker.SetMetadataNotifiers(rpm, deb)

			e.pkg(repoID, format, "pkg", "1.0.0", 100, 30)
			e.pkg(repoID, format, "pkg", "2.0.0", 100, 10)

			got := e.runPolicy(repoID, 1)
			if got.LastRunError != "" || got.LastRunDeleted == nil || *got.LastRunDeleted != 1 {
				t.Fatalf("policy run: deleted = %v error = %q, want 1 deleted", got.LastRunDeleted, got.LastRunError)
			}

			notified := rpm.ids
			if format == "deb" {
				notified = deb.ids
			}

			if len(notified) != 1 || notified[0] != repoID {
				t.Errorf("%s regeneration notifications = %v, want [%s]", format, notified, repoID)
			}
		})
	}
}
