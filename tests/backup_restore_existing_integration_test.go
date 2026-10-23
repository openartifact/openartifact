// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package integration_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/backup"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/logger"
)

// A restore into a freshly started instance meets the records the server
// creates by itself: the bootstrap admin user (and its admin-all grant) and the
// migrations' default storage backend. Those exist on both sides, so they are
// skipped on purpose; the restore must not report them as failures, and
// whatever depends on the skipped admin (its grant, its tokens) is skipped with
// it. A genuine failure still fails the restore.
func TestRestoreIntoBootstrappedInstanceSkipsExistingRecordsWithoutFailing(t *testing.T) {
	ctx := context.Background()
	src, dst := freshPostgres(t, 10), freshPostgres(t, 10)

	for _, pg := range []*database.PostgresDB{src, dst} {
		seedBootstrapAdmin(t, pg)
	}

	srcAdmin, _ := src.GetUserByUsername(ctx, "admin")
	if _, err := src.CreateToken(ctx, &models.Token{UserID: srcAdmin.ID, Name: "ci", Kind: "access", TokenHash: "hash-of-ci", Scopes: []string{}}); err != nil {
		t.Fatal(err)
	}

	dev, err := src.CreateUser(ctx, &models.User{Username: "dev"})
	if err != nil {
		t.Fatal(err)
	}

	if err := src.GetOrCreatePermissionAssignment(ctx, adminAllTarget(t, src), "user", dev.ID); err != nil {
		t.Fatal(err)
	}

	job := &models.BackupJob{Status: "pending", Type: "full"}
	if err := src.CreateBackupJob(ctx, job); err != nil {
		t.Fatal(err)
	}

	if err := newBackupEngine(t, src).RunBackup(ctx, job.ID, "full"); err != nil {
		t.Fatalf("RunBackup: %v", err)
	}

	done, err := src.GetBackupJob(ctx, job.ID)
	if err != nil || done.Status != "completed" {
		t.Fatalf("backup job = %+v, %v", done, err)
	}

	logPath := filepath.Join(t.TempDir(), "restore.log")

	log, err := logger.New("info", "json", logPath)
	if err != nil {
		t.Fatal(err)
	}

	blobs, err := storage.NewFilesystemStore(filepath.Join(t.TempDir(), "blobs"))
	if err != nil {
		t.Fatal(err)
	}

	if err := backup.NewEngine(dst, blobs, log, t.TempDir()).RunRestore(ctx, done.StoragePath); err != nil {
		logged, _ := os.ReadFile(logPath)
		t.Fatalf("RunRestore into a bootstrapped instance: %v\n%s", err, logged)
	}

	logged, _ := os.ReadFile(logPath)
	if !strings.Contains(string(logged), "user already exists, keeping the existing one") {
		t.Errorf("the skipped admin was not logged:\n%s", logged)
	}

	if strings.Contains(string(logged), `"level":"warn"`) {
		t.Errorf("restore logged warnings for expected skips:\n%s", logged)
	}

	if got, err := dst.GetUserByUsername(ctx, "dev"); err != nil || got == nil {
		t.Errorf("the other user was not restored: %v", err)
	}

	if _, err := dst.GetUserByUsername(ctx, "admin"); err != nil {
		t.Errorf("the existing admin is gone: %v", err)
	}
}

func adminAllTarget(t *testing.T, pg *database.PostgresDB) string {
	t.Helper()

	target, err := pg.GetPermissionTargetByName(context.Background(), "admin-all")
	if err != nil || target == nil {
		t.Fatalf("admin-all target: %v", err)
	}

	return target.ID
}

// seedBootstrapAdmin creates what a server start creates: the admin user and
// its admin-all grant.
func seedBootstrapAdmin(t *testing.T, pg *database.PostgresDB) {
	t.Helper()

	ctx := context.Background()

	admin, err := pg.CreateUser(ctx, &models.User{Username: "admin"})
	if err != nil {
		t.Fatal(err)
	}

	if err := pg.GetOrCreatePermissionAssignment(ctx, adminAllTarget(t, pg), "user", admin.ID); err != nil {
		t.Fatal(err)
	}
}
