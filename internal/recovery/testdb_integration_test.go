// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package recovery

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver

	"github.com/openartifact/openartifact/internal/database"
	formatstorage "github.com/openartifact/openartifact/internal/format/storage"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/logger"
)

const systemProjectID = "00000000-0000-0000-0000-000000000001"

func testLog(t *testing.T) *logger.Logger {
	t.Helper()

	log, err := logger.New("error", "json", "stdout")
	if err != nil {
		t.Fatalf("logger: %v", err)
	}

	return log
}

// fixtureSideTables are the side-table descriptors the format plugins register
// in production (internal/formats): the journal registry is built from them.
var fixtureSideTables = map[string][]formatstorage.SideTable{ //nolint:gochecknoglobals // immutable test fixture
	"conan":  {{Name: "conan_files"}},
	"deb":    {{Name: "deb_files", Exclude: []string{"control_fields"}}},
	"maven":  {{Name: "maven_files"}},
	"pacman": {{Name: "pacman_files", Exclude: []string{"file_list"}}},
	"rpm":    {{Name: "rpm_files", Exclude: []string{"rpm_extra"}}},
}

// fixtureMu serialises registerFixtureFormats: the parallel tests of the
// package share one descriptor registry. Tests that Reset it are not parallel,
// and run before the parallel ones resume.
var fixtureMu sync.Mutex //nolint:gochecknoglobals // guards the process-wide descriptor registry

// registerFixtureFormats registers the side-table fixtures above for every
// format that has no descriptor yet.
func registerFixtureFormats() {
	fixtureMu.Lock()
	defer fixtureMu.Unlock()

	for format, side := range fixtureSideTables {
		if _, ok := formatstorage.Lookup(format); !ok {
			formatstorage.Register(format, formatstorage.Storage{SideTables: side})
		}
	}

	// npm's derived metadata is keyed by package: the rebuild hands those ids to the hook.
	if _, ok := formatstorage.Lookup("npm"); !ok {
		formatstorage.Register("npm", formatstorage.Storage{Backup: formatstorage.Backup{Regen: formatstorage.RegenNPM}})
	}
}

// newTestDB creates an empty, migrated database on the TEST_DATABASE_URL server
// and registers the fixture format descriptors.
func newTestDB(t *testing.T) *sql.DB {
	t.Helper()

	registerFixtureFormats()

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

	dbName := fmt.Sprintf("oa_recovery_%d", time.Now().UnixNano())

	if _, err := admin.ExecContext(context.Background(), `CREATE DATABASE `+dbName); err != nil {
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
		Database: dbName, SSLMode: sslmode, MaxOpenConns: 8, MaxIdleConns: 8,
	}, testLog(t))
	if err != nil {
		t.Fatalf("NewPostgresDB: %v", err)
	}

	t.Cleanup(func() { _ = pg.Close() })

	if err := pg.Migrate(context.Background()); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}

	return pg.GetDB()
}

func newFSStore(t *testing.T) storage.BlobStore {
	t.Helper()

	s, err := storage.NewFilesystemStore(t.TempDir())
	if err != nil {
		t.Fatalf("filesystem store: %v", err)
	}

	return s
}

func mustExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()

	if _, err := db.ExecContext(context.Background(), q, args...); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}

func mustCount(t *testing.T, db *sql.DB, q string, args ...any) int {
	t.Helper()

	var n int
	if err := db.QueryRowContext(context.Background(), q, args...).Scan(&n); err != nil {
		t.Fatalf("count %q: %v", q, err)
	}

	return n
}

func mustString(t *testing.T, db *sql.DB, q string, args ...any) string {
	t.Helper()

	var s sql.NullString
	if err := db.QueryRowContext(context.Background(), q, args...).Scan(&s); err != nil {
		t.Fatalf("query %q: %v", q, err)
	}

	return s.String
}

// seededRaw is a raw repository with one file whose bytes are in the store.
// artifactID is the package_files id of the file.
type seededRaw struct {
	repoID, blobID, packageID, versionID, artifactID, sha string
}

// rawAttrs is the attrs value of a raw file (see the raw mapping of the format).
const rawAttrs = `{"content_type": "application/octet-stream", "metadata": {}}`

// seedRaw inserts a raw repository, a blob row, its bytes and one raw file
// (package, version ” and file), the way an upload leaves them.
func seedRaw(t *testing.T, db *sql.DB, store storage.BlobStore, repo, path string, content []byte) seededRaw {
	t.Helper()

	sum := sha256.Sum256(content)
	sha := hex.EncodeToString(sum[:])

	if _, err := store.Put(context.Background(), bytes.NewReader(content), map[string]string{"sha256": sha}); err != nil {
		t.Fatalf("put blob: %v", err)
	}

	s := seededRaw{sha: sha}

	if err := db.QueryRowContext(
		context.Background(),
		`INSERT INTO repositories (name, format, project_id) VALUES ($1, 'raw', $2) RETURNING id`,
		repo,
		systemProjectID,
	).Scan(&s.repoID); err != nil {
		t.Fatalf("insert repository: %v", err)
	}

	if err := db.QueryRowContext(
		context.Background(),
		`INSERT INTO blobs (sha256, size_bytes, blob_key) VALUES ($1::varchar, $2, $3::text) RETURNING id`,
		sha,
		len(content),
		sha+"",
	).Scan(&s.blobID); err != nil {
		t.Fatalf("insert blob: %v", err)
	}

	ids := insertRawFile(t, db, s.repoID, s.blobID, path, false)
	s.packageID, s.versionID, s.artifactID = ids.pkg, ids.ver, ids.file

	return s
}

// rawIDs are the ids of the rows insertRawFile wrote.
type rawIDs struct{ pkg, ver, file string }

// insertRawFile inserts the package, version and file rows of one raw path;
// trashed stamps deleted_at/deleted_by on the version and the file.
func insertRawFile(t *testing.T, db *sql.DB, repoID, blobID, path string, trashed bool) rawIDs {
	t.Helper()

	var (
		ctx = context.Background()
		ids rawIDs
	)

	if err := db.QueryRowContext(ctx,
		`INSERT INTO packages (repository_id, format, name, name_key) VALUES ($1, 'raw', $2, $2) RETURNING id`,
		repoID, path).Scan(&ids.pkg); err != nil {
		t.Fatalf("insert package: %v", err)
	}

	if err := db.QueryRowContext(ctx,
		`INSERT INTO package_versions (package_id, repository_id, version, version_key, deleted_at, deleted_by)
		 VALUES ($1, $2, '', '', CASE WHEN $3::bool THEN now() END, CASE WHEN $3::bool THEN 'tester' END) RETURNING id`,
		ids.pkg, repoID, trashed).Scan(&ids.ver); err != nil {
		t.Fatalf("insert version: %v", err)
	}

	if err := db.QueryRowContext(ctx,
		`INSERT INTO package_files (version_id, repository_id, path, filename, blob_id, role, attrs, unit_key, deleted_at, deleted_by)
		 VALUES ($1, $2, $3, $3, $4, 'primary', $5::jsonb, $3, CASE WHEN $6::bool THEN now() END, CASE WHEN $6::bool THEN 'tester' END)
		 RETURNING id`,
		ids.ver, repoID, path, blobID, rawAttrs, trashed).Scan(&ids.file); err != nil {
		t.Fatalf("insert file: %v", err)
	}

	return ids
}

func newTestJournal(t *testing.T, db *sql.DB, store storage.BlobStore) *Journal {
	t.Helper()

	return NewJournal(db, store, testLog(t), nil, JournalConfig{Batch: 100, Concurrency: 4})
}

// drain drains the queue until it holds nothing claimable.
func drain(t *testing.T, j *Journal) {
	t.Helper()

	if _, failed, err := j.Drain(context.Background()); err != nil || failed != 0 {
		t.Fatalf("drain: failed=%d err=%v", failed, err)
	}
}

// reconcileAll runs the reconcile to completion, draining as it goes.
func reconcileAll(t *testing.T, j *Journal) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	for {
		res, err := j.ReconcileStep(ctx, 1000)
		if err != nil {
			t.Fatalf("reconcile: %v", err)
		}

		drain(t, j)

		if res.Idle {
			return
		}

		if res.Paused {
			t.Fatal("reconcile paused unexpectedly")
		}
	}
}
