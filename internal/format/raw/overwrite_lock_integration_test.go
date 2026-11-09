// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package raw

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver
)

// newOverwriteDB opens a fresh, migrated database.
func newOverwriteDB(t *testing.T) *database.PostgresDB {
	t.Helper()

	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("TEST_DATABASE_URL not set in CI; integration services are required")
		}

		t.Skip("TEST_DATABASE_URL not set")
	}

	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open admin connection: %v", err)
	}

	t.Cleanup(func() { _ = admin.Close() })

	dbName := fmt.Sprintf("oa_rawoverwrite_%d", time.Now().UnixNano())
	if _, err := admin.ExecContext(t.Context(), `CREATE DATABASE `+dbName); err != nil {
		t.Fatalf("create database: %v", err)
	}

	t.Cleanup(func() {
		_, _ = admin.ExecContext(context.Background(), `DROP DATABASE IF EXISTS `+dbName+` WITH (FORCE)`)
	})

	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse TEST_DATABASE_URL: %v", err)
	}

	port, _ := strconv.Atoi(u.Port())
	pw, _ := u.User.Password()

	sslmode := u.Query().Get("sslmode")
	if sslmode == "" {
		sslmode = "disable"
	}

	pg, err := database.NewPostgresDB(database.DatabaseConfig{
		Host: u.Hostname(), Port: port, User: u.User.Username(), Password: pw,
		Database: dbName, SSLMode: sslmode, MaxOpenConns: 20, MaxIdleConns: 20,
	}, formattest.NewLogger(t))
	if err != nil {
		t.Fatalf("NewPostgresDB: %v", err)
	}

	t.Cleanup(func() { _ = pg.Close() })

	if err := pg.Migrate(t.Context()); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}

	return pg
}

// Concurrent PUTs of different content to one path serialize on the path's
// lock: one file stays, the usage is that file's alone and the blobs the
// losers wrote are gone.
func TestConcurrentOverwritesKeepOneFileAndConsistentUsage(t *testing.T) {
	pg := newOverwriteDB(t)
	ctx := t.Context()

	var projectID string
	if err := pg.GetDB().QueryRowContext(ctx,
		`INSERT INTO projects (name, slug) VALUES ('raw-overwrite', 'raw-overwrite')
		 ON CONFLICT (name) DO UPDATE SET name = EXCLUDED.name RETURNING id`).Scan(&projectID); err != nil {
		t.Fatalf("insert project: %v", err)
	}

	repo := &models.Repository{Name: "myrepo", Format: "raw", RepoType: "hosted", ProjectID: &projectID}
	if err := pg.CreateRepository(ctx, repo); err != nil {
		t.Fatalf("CreateRepository: %v", err)
	}

	bs := formattest.NewBlobStore()
	h := NewHTTPHandler(formattest.NewLogger(t), pg, bs, "http://localhost")

	const writers = 8

	bodies := make([]string, writers)
	for i := range bodies {
		bodies[i] = strings.Repeat("x", i+1) + fmt.Sprintf("-%d", i)
	}

	var wg sync.WaitGroup

	codes := make([]int, writers)

	for i := range writers {
		wg.Go(func() {
			codes[i] = formattest.Do(h.RegisterRepoRoutes,
				formattest.Request(http.MethodPut, "/repos/myrepo/dir/file.txt", strings.NewReader(bodies[i]))).Code
		})
	}

	wg.Wait()

	for i, c := range codes {
		if c != http.StatusCreated {
			t.Fatalf("writer %d status = %d, want 201", i, c)
		}
	}

	stored, err := pg.GetRawArtifact(ctx, repo.ID, "dir/file.txt")
	if err != nil || stored == nil {
		t.Fatalf("GetRawArtifact = %v, %v", stored, err)
	}

	var files int
	if err := pg.GetDB().QueryRowContext(ctx,
		`SELECT count(*) FROM package_files WHERE repository_id = $1 AND deleted_at IS NULL`, repo.ID).Scan(&files); err != nil {
		t.Fatalf("count files: %v", err)
	}

	if files != 1 {
		t.Errorf("%d live files, want 1", files)
	}

	q, err := pg.GetOrCreateStorageQuota(ctx, repo.ID)
	if err != nil {
		t.Fatalf("GetOrCreateStorageQuota: %v", err)
	}

	if q.CurrentSizeBytes != stored.SizeBytes || q.CurrentArtifactCount != 1 {
		t.Errorf("usage = %d bytes / %d artifacts, want %d / 1", q.CurrentSizeBytes, q.CurrentArtifactCount, stored.SizeBytes)
	}

	var pending int
	if err := pg.GetDB().QueryRowContext(ctx,
		`SELECT count(*) FROM quota_pending WHERE repository_id = $1`, repo.ID).Scan(&pending); err != nil {
		t.Fatalf("count pending: %v", err)
	}

	if pending != 0 {
		t.Errorf("%d quota_pending entries left, want 0", pending)
	}

	if len(bs.Blobs) != 1 {
		t.Errorf("%d blobs in the store, want 1 (the stored file's)", len(bs.Blobs))
	}

	if _, ok := bs.Blobs[stored.SHA256]; !ok {
		t.Errorf("stored file's blob %s missing from the store", stored.SHA256)
	}
}
