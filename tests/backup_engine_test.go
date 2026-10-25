// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package integration_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/backup"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/databasetest"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/logger"
)

// backupMockDB wraps MockDatabase and provides a real BackupJob for testing.
type backupMockDB struct {
	*MockDatabase

	job *models.BackupJob
}

func (b *backupMockDB) GetBackupJob(ctx context.Context, id string) (*models.BackupJob, error) {
	if b.job != nil && b.job.ID == id {
		return b.job, nil
	}

	return nil, nil
}

func (b *backupMockDB) BeginBackupSnapshot(context.Context, time.Duration) (database.SnapshotReader, error) {
	return &databasetest.LiveSnapshot{Database: b}, nil
}

func (b *backupMockDB) UpdateBackupJob(ctx context.Context, job *models.BackupJob) error {
	b.job = job
	return nil
}

func newBackupLogger(t *testing.T) *logger.Logger {
	t.Helper()

	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatalf("failed to create logger: %v", err)
	}

	return log
}

func TestBackupEngine_RunBackup_MetadataOnly(t *testing.T) {
	dir := t.TempDir()
	log := newBackupLogger(t)

	blobStore, err := storage.NewFilesystemStore(filepath.Join(dir, "blobs"))
	if err != nil {
		t.Fatalf("failed to create blob store: %v", err)
	}

	job := &models.BackupJob{
		ID:        "job-001",
		Status:    "pending",
		Type:      "metadata-only",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	db := &backupMockDB{MockDatabase: newMockDatabase(), job: job}

	engine := backup.NewEngine(db, blobStore, log, filepath.Join(dir, "backups"))

	err = engine.RunBackup(context.Background(), "job-001", "metadata-only")
	if err != nil {
		t.Fatalf("RunBackup failed: %v", err)
	}

	if db.job.Status != "completed" {
		t.Errorf("job status = %q, want completed", db.job.Status)
	}

	finalPath := filepath.Join(dir, "backups", "backup_job-001.tar.gz")
	if _, err := os.Stat(finalPath); os.IsNotExist(err) {
		t.Errorf("backup file not created at %s", finalPath)
	}

	if db.job.SizeBytes <= 0 {
		t.Errorf("SizeBytes = %d, want > 0", db.job.SizeBytes)
	}
}

func TestBackupEngine_RunBackup_JobNotFound(t *testing.T) {
	dir := t.TempDir()
	log := newBackupLogger(t)

	blobStore, _ := storage.NewFilesystemStore(filepath.Join(dir, "blobs"))
	db := &backupMockDB{MockDatabase: newMockDatabase(), job: nil}
	engine := backup.NewEngine(db, blobStore, log, filepath.Join(dir, "backups"))

	err := engine.RunBackup(context.Background(), "nonexistent", "metadata-only")
	if err == nil {
		t.Fatal("expected error for missing job, got nil")
	}
}

// createBackupArchive creates a minimal valid backup tarball for testing RunRestore
func createBackupArchive(t *testing.T, dir string) string {
	t.Helper()

	manifest := map[string]any{
		"version":    "1.0",
		"created_at": time.Now().UTC(),
		"type":       "metadata-only",
		"table_counts": map[string]int{
			"repositories": 1,
			"users":        1,
			"groups":       1,
		},
	}
	manifestJSON, _ := json.MarshalIndent(manifest, "", "  ")

	repos := []*models.Repository{{
		ID: "r1", Name: "testrepo", Format: "npm",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}}
	reposJSON, _ := json.Marshal(repos)

	aliceEmail := "alice@example.com"
	users := []*models.User{{
		ID: "u1", Username: "alice", Email: &aliceEmail,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}}
	usersJSON, _ := json.Marshal(users)

	groups := []*models.Group{{ID: "g1", Name: "devs", CreatedAt: time.Now(), UpdatedAt: time.Now()}}
	groupsJSON, _ := json.Marshal(groups)

	archivePath := filepath.Join(dir, "test_backup.tar.gz")

	f, err := os.Create(archivePath)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}

	defer func() { _ = f.Close() }()

	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)

	for _, entry := range []struct {
		name string
		data []byte
	}{
		{"manifest.json", manifestJSON},
		{"repositories.jsonl", reposJSON},
		{"users.jsonl", usersJSON},
		{"groups.jsonl", groupsJSON},
	} {
		_ = tw.WriteHeader(&tar.Header{Name: entry.name, Mode: 0644, Size: int64(len(entry.data))})
		_, _ = tw.Write(entry.data)
	}

	_ = tw.Close()
	_ = gw.Close()

	return archivePath
}

func TestBackupEngine_RunRestore(t *testing.T) {
	dir := t.TempDir()
	log := newBackupLogger(t)

	blobStore, _ := storage.NewFilesystemStore(filepath.Join(dir, "blobs"))
	db := &backupMockDB{MockDatabase: newMockDatabase()}
	engine := backup.NewEngine(db, blobStore, log, dir)

	archivePath := createBackupArchive(t, dir)

	err := engine.RunRestore(context.Background(), archivePath)
	if err != nil {
		t.Fatalf("RunRestore failed: %v", err)
	}
}

func TestBackupEngine_RunRestore_InvalidVersion(t *testing.T) {
	dir := t.TempDir()
	log := newBackupLogger(t)

	blobStore, _ := storage.NewFilesystemStore(filepath.Join(dir, "blobs"))
	db := &backupMockDB{MockDatabase: newMockDatabase()}
	engine := backup.NewEngine(db, blobStore, log, dir)

	// Create archive with unsupported version
	manifest := map[string]any{
		"version": "99.0", "created_at": time.Now(), "type": "metadata-only",
	}
	manifestJSON, _ := json.MarshalIndent(manifest, "", "  ")

	archivePath := filepath.Join(dir, "bad_backup.tar.gz")
	f, _ := os.Create(archivePath)

	var buf bytes.Buffer

	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	_ = tw.WriteHeader(&tar.Header{Name: "manifest.json", Mode: 0644, Size: int64(len(manifestJSON))})
	_, _ = tw.Write(manifestJSON)
	_ = tw.Close()
	_ = gw.Close()
	_, _ = f.Write(buf.Bytes())
	_ = f.Close()

	err := engine.RunRestore(context.Background(), archivePath)
	if err == nil {
		t.Fatal("expected error for unsupported version, got nil")
	}
}

func (b *backupMockDB) ListAllProjects(context.Context) ([]*models.Project, error) { return nil, nil }

func (b *backupMockDB) ListPermissionAssignments(context.Context) ([]*models.PermissionAssignment, error) {
	return nil, nil
}

func (b *backupMockDB) ListUserGroupMemberships(context.Context) ([]*models.UserGroup, error) {
	return nil, nil
}

func (b *backupMockDB) ListStorageQuotas(context.Context) ([]*models.StorageQuota, error) {
	return nil, nil
}
