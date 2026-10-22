// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package gomod

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
	"github.com/openartifact/openartifact/internal/storage"
)

func newRepairTestDB(t *testing.T) *database.PostgresDB {
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

	dbName := fmt.Sprintf("oa_gomodrepair_%d", time.Now().UnixNano())
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
	}, formattest.NewLogger(t))
	if err != nil {
		t.Fatalf("NewPostgresDB: %v", err)
	}

	t.Cleanup(func() { _ = pg.Close() })

	if err := pg.Migrate(context.Background()); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}

	return pg
}

// repairStores returns the blob stores the scenario runs against: always the
// filesystem, plus S3 (Garage) when TEST_S3_ENDPOINT is set.
func repairStores(t *testing.T) map[string]storage.BlobStore {
	t.Helper()

	fs, err := storage.NewFilesystemStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFilesystemStore: %v", err)
	}

	stores := map[string]storage.BlobStore{"filesystem": fs}

	if endpoint := os.Getenv("TEST_S3_ENDPOINT"); endpoint != "" {
		s3, err := storage.NewS3BlobStore(storage.StorageConfig{
			Type: "s3", Endpoint: endpoint,
			Bucket:         envOr("TEST_S3_BUCKET", "openartifact-test"),
			Region:         envOr("TEST_S3_REGION", "us-east-1"),
			AccessKey:      envOr("TEST_S3_ACCESS_KEY", "minioadmin"),
			SecretKey:      envOr("TEST_S3_SECRET_KEY", "minioadmin"),
			ForcePathStyle: true,
		})
		if err != nil {
			t.Fatalf("NewS3BlobStore: %v", err)
		}

		stores["s3"] = s3
	}

	return stores
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}

	return def
}

func sha(b []byte) string {
	sum := sha256.Sum256(b)

	return hex.EncodeToString(sum[:])
}

// leakWorld is the database and blob store as OpenArtifact 0.4.1 left them.
type leakWorld struct {
	t     *testing.T
	pg    *database.PostgresDB
	store storage.BlobStore
	// emptyBlobID is the shared blobs row keyed by ''.
	emptyBlobID string
	zips        map[string][]byte // module name -> zip bytes
	router      http.Handler
}

func newLeakWorld(t *testing.T, store storage.BlobStore) *leakWorld {
	t.Helper()

	w := &leakWorld{t: t, pg: newRepairTestDB(t), store: store, zips: map[string][]byte{}}

	// A database upgraded from 0.4.1 holds the legacy '' row: migration 021's
	// CHECK is NOT VALID, so it does not reject rows that were already there.
	// Seeding one into a fresh database needs the constraint out of the way.
	w.exec(`ALTER TABLE blobs DROP CONSTRAINT blobs_sha256_hex`)

	h := NewHTTPHandler(formattest.NewLogger(t), w.pg, store, "http://localhost")
	r := chi.NewRouter()
	r.Route("/repos/{name}", func(sub chi.Router) { h.RegisterRepoRoutes(sub) })
	w.router = r

	return w
}

func (w *leakWorld) exec(q string, args ...any) {
	w.t.Helper()

	if _, err := w.pg.GetDB().ExecContext(context.Background(), q, args...); err != nil {
		w.t.Fatalf("exec %q: %v", q, err)
	}
}

func (w *leakWorld) repo(org, name string) string {
	w.t.Helper()

	var orgID string
	if err := w.pg.GetDB().QueryRowContext(context.Background(),
		`INSERT INTO organizations (name, slug) VALUES ($1, $1)
		 ON CONFLICT (name) DO UPDATE SET name = EXCLUDED.name RETURNING id`, org).Scan(&orgID); err != nil {
		w.t.Fatalf("insert org: %v", err)
	}

	repo := &models.Repository{Name: name, Format: "go", RepoType: "hosted", OrgID: &orgID}
	if err := w.pg.CreateRepository(context.Background(), repo); err != nil {
		w.t.Fatalf("CreateRepository: %v", err)
	}

	return repo.ID
}

// zipFor builds a module zip once per label: the bytes are stable afterwards.
func (w *leakWorld) zipFor(label, module, version, extra string) []byte {
	w.t.Helper()

	z := createGoModZip(w.t, map[string]string{
		module + "@" + version + "/go.mod":  "module " + module + "\n\ngo 1.21\n",
		module + "@" + version + "/main.go": "package x // " + extra + "\n",
	})
	w.zips[label] = z

	return z
}

func (w *leakWorld) putZip(z []byte) string {
	w.t.Helper()

	key, err := w.store.Put(context.Background(), bytes.NewReader(z), map[string]string{"sha256": sha(z)})
	if err != nil {
		w.t.Fatalf("put zip: %v", err)
	}

	return key
}

// leakRow inserts a gomod_modules row pointing at the shared "" blobs row.
// firstZip, when non-nil, is the zip the "" row's blob_key points at (the
// first upload); it is created together with the row.
func (w *leakWorld) leakRow(repoID, module, version string, firstZip []byte) {
	w.t.Helper()

	if w.emptyBlobID == "" {
		if firstZip == nil {
			w.t.Fatal("the first leak row must carry the first zip")
		}

		key := w.putZip(firstZip)
		if err := w.pg.GetDB().QueryRowContext(context.Background(),
			`INSERT INTO blobs (sha256, size_bytes, media_type, blob_key)
			 VALUES ('', $1, 'application/zip', $2) RETURNING id`, len(firstZip), key).Scan(&w.emptyBlobID); err != nil {
			w.t.Fatalf("insert '' blob row: %v", err)
		}
	}

	goMod := "module " + module + "\n\ngo 1.21\n"
	modSHA := sha([]byte(goMod))

	var modBlobID string
	if err := w.pg.GetDB().QueryRowContext(context.Background(),
		`INSERT INTO blobs (sha256, size_bytes, media_type, blob_key) VALUES ($1::text, $2, 'text/plain', $1::text)
		 ON CONFLICT (sha256) DO UPDATE SET media_type = EXCLUDED.media_type RETURNING id`, modSHA, len(goMod)).Scan(&modBlobID); err != nil {
		w.t.Fatalf("insert mod blob: %v", err)
	}

	w.exec(`INSERT INTO gomod_modules (repository_id, module_path, version, go_mod_content, zip_blob_id, mod_blob_id)
	        VALUES ($1, $2, $3, $4, $5, $6)`, repoID, module, version, goMod, w.emptyBlobID, modBlobID)
}

func (w *leakWorld) get(repo, module, version string) (int, []byte) {
	w.t.Helper()

	rec := serve(w.router, http.MethodGet, "/repos/"+repo+"/"+module+"/@v/"+version+".zip", nil)

	return rec.Code, rec.Body.Bytes()
}

func (w *leakWorld) repair() RepairResult {
	w.t.Helper()

	res, err := RepairZipBlobs(context.Background(), w.pg, w.store, formattest.NewLogger(w.t))
	if err != nil {
		w.t.Fatalf("RepairZipBlobs: %v", err)
	}

	return res
}

func (w *leakWorld) emptyBlobRows() int {
	w.t.Helper()

	var n int
	if err := w.pg.GetDB().QueryRowContext(context.Background(), `SELECT count(*) FROM blobs WHERE sha256 = ''`).Scan(&n); err != nil {
		w.t.Fatalf("count '' rows: %v", err)
	}

	return n
}

func (w *leakWorld) mustServe(repo, module, version string, want []byte) {
	w.t.Helper()

	code, body := w.get(repo, module, version)
	if code != http.StatusOK || !bytes.Equal(body, want) {
		w.t.Errorf("GET %s %s@%s = %d (%d bytes); want 200 with its own %d bytes", repo, module, version, code, len(body), len(want))
	}
}

func (w *leakWorld) mustRefuse(repo, module, version string, leaked ...[]byte) {
	w.t.Helper()

	code, body := w.get(repo, module, version)
	if code != http.StatusNotFound {
		w.t.Errorf("GET %s %s@%s = %d, want 404 (fail closed)", repo, module, version, code)
	}

	for _, l := range leaked {
		if len(body) > 0 && bytes.Contains(body, l[:min(len(l), 16)]) {
			w.t.Errorf("GET %s %s@%s leaked another module's bytes", repo, module, version)
		}
	}
}

func TestRepairRelinksZipsOfThe041SharedBlobBug(t *testing.T) {
	for name, store := range repairStores(t) {
		t.Run(name, func(t *testing.T) {
			w := newLeakWorld(t, store)
			suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
			modA, modB, modC := "example.com/a"+suffix, "example.com/b"+suffix, "example.com/c"+suffix

			zipA := w.zipFor("a", modA, "v1.0.0", "A")
			zipB := w.zipFor("b", modB, "v1.0.0", "B")
			zipC := w.zipFor("c", modC, "v1.2.3", "C")

			repo1 := w.repo("org-one-"+suffix, "go-one-"+suffix)
			repo2 := w.repo("org-two-"+suffix, "go-two-"+suffix)

			// 0.4.1: every row shares the empty-sha256 blobs row whose object is A's zip (the first
			// upload); B's and C's real zips are in the store without a blobs row.
			w.leakRow(repo1, modA, "v1.0.0", zipA)
			w.leakRow(repo2, modB, "v1.0.0", nil)
			w.leakRow(repo2, modC, "v1.2.3", nil)
			w.putZip(zipB)
			w.putZip(zipC)

			// Before repair nothing is served; in particular B never gets A's zip.
			w.mustRefuse("go-two-"+suffix, modB, "v1.0.0", zipA)
			w.mustRefuse("go-two-"+suffix, modC, "v1.2.3", zipA)
			w.mustRefuse("go-one-"+suffix, modA, "v1.0.0")

			res := w.repair()
			if res.Affected != 3 || res.Repaired != 3 || len(res.Unrepairable) != 0 || res.BlobRowsDeleted != 1 {
				t.Fatalf("repair result = %+v, want 3 affected, 3 repaired, none unrepairable, 1 blob row deleted", res)
			}

			w.mustServe("go-one-"+suffix, modA, "v1.0.0", zipA)
			w.mustServe("go-two-"+suffix, modB, "v1.0.0", zipB)
			w.mustServe("go-two-"+suffix, modC, "v1.2.3", zipC)

			if n := w.emptyBlobRows(); n != 0 {
				t.Errorf("%d empty-sha256 blobs row(s) left after repair", n)
			}

			// A's object is a real module's zip: repair must not delete it.
			if ok, err := store.Exists(context.Background(), sha(zipA)); err != nil || !ok {
				t.Errorf("A's zip object exists = %v, %v; repair must never delete it", ok, err)
			}

			// Idempotent: a second run finds nothing to do.
			if again := w.repair(); again.Affected != 0 || again.Repaired != 0 || again.BlobRowsDeleted != 0 {
				t.Errorf("second repair = %+v, want a no-op", again)
			}

			w.mustServe("go-two-"+suffix, modB, "v1.0.0", zipB)
		})
	}
}

func TestRepairLeavesAmbiguousRowsUnservable(t *testing.T) {
	for name, store := range repairStores(t) {
		t.Run(name, func(t *testing.T) {
			w := newLeakWorld(t, store)
			suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
			modA, modB := "example.com/a"+suffix, "example.com/b"+suffix

			zipA := w.zipFor("a", modA, "v1.0.0", "A")
			// Two different zips of B v1.0.0 with the same go.mod: which one
			// belongs to the row cannot be told apart.
			zipB1 := w.zipFor("b1", modB, "v1.0.0", "fork one")
			zipB2 := w.zipFor("b2", modB, "v1.0.0", "fork two")

			repo := w.repo("org-amb-"+suffix, "go-amb-"+suffix)
			w.leakRow(repo, modA, "v1.0.0", zipA)
			w.leakRow(repo, modB, "v1.0.0", nil)
			w.putZip(zipB1)
			w.putZip(zipB2)

			res := w.repair()
			if res.Repaired != 1 || len(res.Unrepairable) != 1 || res.Unrepairable[0].Module != modB {
				t.Fatalf("repair result = %+v, want only A repaired and B unrepairable", res)
			}

			if !strings.Contains(res.Unrepairable[0].Reason, "ambiguous") {
				t.Errorf("reason = %q, want it to say ambiguous", res.Unrepairable[0].Reason)
			}

			w.mustServe("go-amb-"+suffix, modA, "v1.0.0", zipA)
			w.mustRefuse("go-amb-"+suffix, modB, "v1.0.0", zipA, zipB1, zipB2)

			// B still points at the '' row, so it must survive for now, and a
			// second run leaves B alone again.
			if n := w.emptyBlobRows(); n != 1 {
				t.Errorf("%d empty-sha256 blobs rows, want 1 while an unrepaired row references it", n)
			}

			if again := w.repair(); again.Repaired != 0 || len(again.Unrepairable) != 1 {
				t.Errorf("second repair = %+v, want B still unrepairable and nothing else changed", again)
			}
		})
	}
}

// Rows in different repositories that hold the identical zip match the same
// single candidate and are all relinked; trashed rows are repaired too.
func TestRepairRelinksIdenticalZipsAcrossReposAndTrashedRows(t *testing.T) {
	store := repairStores(t)["filesystem"]
	w := newLeakWorld(t, store)
	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	modA, modB := "example.com/a"+suffix, "example.com/b"+suffix

	zipA := w.zipFor("a", modA, "v1.0.0", "A")
	zipB := w.zipFor("b", modB, "v1.0.0", "B")

	repo1 := w.repo("org-x-"+suffix, "go-x-"+suffix)
	repo2 := w.repo("org-y-"+suffix, "go-y-"+suffix)
	repo3 := w.repo("org-z-"+suffix, "go-z-"+suffix)

	w.leakRow(repo1, modA, "v1.0.0", zipA)
	w.leakRow(repo2, modB, "v1.0.0", nil)
	w.leakRow(repo3, modB, "v1.0.0", nil)
	w.putZip(zipB)
	w.exec(`UPDATE gomod_modules SET deleted_at = now() WHERE repository_id = $1`, repo3)

	res := w.repair()
	if res.Affected != 3 || res.Repaired != 3 || len(res.Unrepairable) != 0 {
		t.Fatalf("repair result = %+v, want all 3 rows (one trashed) repaired", res)
	}

	w.mustServe("go-y-"+suffix, modB, "v1.0.0", zipB)

	var trashedHash string
	if err := w.pg.GetDB().QueryRowContext(context.Background(),
		`SELECT b.sha256 FROM gomod_modules gm JOIN blobs b ON b.id = gm.zip_blob_id WHERE gm.repository_id = $1`, repo3).Scan(&trashedHash); err != nil {
		t.Fatalf("trashed row: %v", err)
	}

	if trashedHash != sha(zipB) {
		t.Errorf("trashed row zip sha256 = %q, want %q", trashedHash, sha(zipB))
	}
}

// The repair is a no-op, and takes no lock, on a healthy database.
func TestRepairIsNoOpWhenNothingIsAffected(t *testing.T) {
	store := repairStores(t)["filesystem"]
	w := newLeakWorld(t, store)

	if res := w.repair(); res.Affected != 0 || res.Repaired != 0 || res.LockBusy {
		t.Errorf("repair on an empty database = %+v, want a no-op", res)
	}
}

// Only one replica repairs at a time.
func TestRepairSkipsWhenAnotherReplicaHoldsTheLock(t *testing.T) {
	store := repairStores(t)["filesystem"]
	w := newLeakWorld(t, store)
	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	mod := "example.com/a" + suffix

	zip := w.zipFor("a", mod, "v1.0.0", "A")
	w.leakRow(w.repo("org-l-"+suffix, "go-l-"+suffix), mod, "v1.0.0", zip)

	release, ok, err := w.pg.TryGomodRepairLock(context.Background())
	if err != nil || !ok {
		t.Fatalf("TryGomodRepairLock = %v, %v", ok, err)
	}

	if res := w.repair(); !res.LockBusy || res.Repaired != 0 {
		t.Errorf("repair while the lock is held = %+v, want LockBusy and nothing repaired", res)
	}

	release()

	if res := w.repair(); res.Repaired != 1 {
		t.Errorf("repair after the lock is released = %+v, want 1 repaired", res)
	}
}

// Migration 021: the database itself refuses a blobs row without a real digest.
func TestBlobsTableRejectsInvalidSHA256(t *testing.T) {
	pg := newRepairTestDB(t)

	for _, sha := range []string{"", "sha256:" + strings.Repeat("a", 64), strings.Repeat("A", 64), "abc"} {
		_, err := pg.GetDB().ExecContext(context.Background(),
			`INSERT INTO blobs (sha256, size_bytes, blob_key) VALUES ($1, 1, 'k')`, sha)
		if err == nil {
			t.Errorf("INSERT blobs sha256=%q succeeded; want a CHECK violation", sha)
		}
	}

	if _, err := pg.GetDB().ExecContext(context.Background(),
		`INSERT INTO blobs (sha256, size_bytes, blob_key) VALUES ($1, 1, 'k')`, strings.Repeat("a", 64)); err != nil {
		t.Errorf("INSERT blobs with a valid sha256: %v", err)
	}
}
