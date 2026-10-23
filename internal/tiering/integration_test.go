// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package tiering

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/logger"
)

// pgRig is the real thing: PostgreSQL for placement and the journal, two
// filesystem directories for the hot and cold tiers.
type pgRig struct {
	t       *testing.T
	pg      *database.PostgresDB
	hot     storage.BlobStore
	hotCfg  storage.StorageConfig
	hotDir  string
	coldDir string
	coldID  string
	coldTID string
	reg     *Registry
	store   *Store
	mover   *Mover
	engine  *Engine
}

func newPGTestDB(t *testing.T) *database.PostgresDB {
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

	dbName := fmt.Sprintf("oa_tiering_%d", time.Now().UnixNano())
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

	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatalf("create logger: %v", err)
	}

	pg, err := database.NewPostgresDB(database.DatabaseConfig{
		Host: u.Hostname(), Port: port, User: u.User.Username(), Password: pw, Database: dbName,
		SSLMode: sslmode, MaxOpenConns: 8, MaxIdleConns: 8,
	}, log)
	if err != nil {
		t.Fatalf("NewPostgresDB: %v", err)
	}

	t.Cleanup(func() { _ = pg.Close() })

	if err := pg.Migrate(context.Background()); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}

	return pg
}

func newPGRig(t *testing.T) *pgRig {
	t.Helper()

	r := &pgRig{t: t, pg: newPGTestDB(t), hotDir: filepath.Join(t.TempDir(), "hot"), coldDir: filepath.Join(t.TempDir(), "cold")}
	ctx := context.Background()
	now := time.Now()

	r.hotCfg = storage.StorageConfig{Type: "filesystem", Path: r.hotDir}

	var err error
	if r.hot, err = storage.NewBlobStore(r.hotCfg); err != nil {
		t.Fatalf("hot store: %v", err)
	}

	cfg, _ := json.Marshal(map[string]string{"path": r.coldDir})
	r.coldID, r.coldTID = uuid.New().String(), uuid.New().String()

	if err := r.pg.CreateStorageBackend(ctx, &models.StorageBackend{
		ID: r.coldID, Name: "cold-fs", Kind: "filesystem", Config: cfg, Status: "unknown", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("create backend: %v", err)
	}

	if err := r.pg.CreateStorageTier(ctx, &models.StorageTier{ID: r.coldTID, Name: "cold", BackendID: r.coldID, Rank: 100}); err != nil {
		t.Fatalf("create tier: %v", err)
	}

	if err := r.pg.CreateStoragePolicy(ctx, &models.StoragePolicy{
		ID: uuid.New().String(), Name: "archive", Enabled: true, Position: 1, TargetTierID: r.coldTID, MinAgeSeconds: new(int64(86400)),
	}); err != nil {
		t.Fatalf("create policy: %v", err)
	}

	r.restart()

	return r
}

// restart rebuilds every in-process component over the same database and
// directories: what a crashed process loses.
func (r *pgRig) restart() {
	r.t.Helper()

	r.reg = NewRegistry(r.pg, r.hot, r.hotCfg, nil)
	if err := r.reg.Refresh(context.Background()); err != nil {
		r.t.Fatalf("registry refresh: %v", err)
	}

	r.store = NewStore(r.reg, r.pg, nil, nil, nil)
	r.mover = NewMover(r.pg, r.reg, r.store, nil, MoverConfig{VerifyReadback: true}, nil, nil)
	r.engine = NewEngine(r.pg, r.reg, r.mover, EngineConfig{Batch: 100, Concurrency: 2}, nil, nil)
}

// addBlob stores data through the tiered store and registers a blob row that
// is two days old.
func (r *pgRig) addBlob(data []byte) string {
	r.t.Helper()

	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])

	if _, err := r.store.Put(context.Background(), bytes.NewReader(data), map[string]string{"sha256": sha}); err != nil {
		r.t.Fatalf("put: %v", err)
	}

	if _, err := r.pg.GetDB().ExecContext(context.Background(),
		`INSERT INTO blobs (sha256, size_bytes, blob_key, created_at) VALUES ($1::text, $2, $1::text, now() - interval '2 days')`,
		sha, len(data)); err != nil {
		r.t.Fatalf("insert blob: %v", err)
	}

	return sha
}

// sweep carries out the queued deletions the way the blob deletion sweeper does
// (grace 0, no backup running): a copy that is authoritative again is kept.
func (r *pgRig) sweep() {
	r.t.Helper()

	ctx := context.Background()

	for {
		batch, err := r.pg.ClaimBlobDeletions(ctx, 100, time.Minute)
		if err != nil {
			r.t.Fatalf("ClaimBlobDeletions: %v", err)
		}

		if len(batch) == 0 {
			return
		}

		for _, d := range batch {
			backend, _, found, err := r.pg.GetBlobPlacement(ctx, d.SHA256)
			if err != nil {
				r.t.Fatalf("GetBlobPlacement: %v", err)
			}

			switch {
			case found && (d.Scope == database.BlobDeletionScopeAll || backend == d.Scope):
				// live again: keep the bytes
			case d.Scope == database.BlobDeletionScopeAll:
				err = r.store.Delete(ctx, d.SHA256)
			default:
				err = r.store.DeleteCopy(ctx, d.Scope, d.SHA256)
			}

			if err != nil && !errors.Is(err, storage.ErrBlobNotFound) {
				r.t.Fatalf("sweep %s/%s: %v", d.SHA256, d.Scope, err)
			}

			if err := r.pg.DeleteBlobDeletion(ctx, d.SHA256, d.Scope); err != nil {
				r.t.Fatalf("DeleteBlobDeletion: %v", err)
			}
		}
	}
}

func (r *pgRig) onDisk(sha string) (bool, bool) {
	r.t.Helper()

	_, errH := os.Stat(filepath.Join(r.hotDir, sha[:2], sha[2:]))
	_, errC := os.Stat(filepath.Join(r.coldDir, sha[:2], sha[2:]))

	return errH == nil, errC == nil
}

func (r *pgRig) placement(sha string) string {
	r.t.Helper()

	be, _, found, err := r.pg.GetBlobPlacement(context.Background(), sha)
	if err != nil || !found {
		r.t.Fatalf("GetBlobPlacement: found=%v err=%v", found, err)
	}

	return be
}

// readSum reads the blob through the tiered store and returns its sha256.
func (r *pgRig) readSum(sha string) string {
	r.t.Helper()

	rc, err := r.store.Get(context.Background(), sha)
	if err != nil {
		r.t.Fatalf("Get through the tiered store: %v", err)
	}
	defer rc.Close()

	h := sha256.New()
	if _, err := io.Copy(h, rc); err != nil {
		r.t.Fatalf("read: %v", err)
	}

	return hex.EncodeToString(h.Sum(nil))
}

func TestIntegrationDryRunChangesNothing(t *testing.T) {
	r := newPGRig(t)
	sha := r.addBlob([]byte("dry run data"))

	plan, err := r.engine.Plan(context.Background(), nil, 10)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	if plan.Count != 1 || len(plan.Sample) != 1 || plan.Sample[0].SHA256 != sha || plan.Sample[0].To != "cold" {
		t.Fatalf("plan = %+v, want the blob going to cold", plan)
	}

	if hot, cold := r.onDisk(sha); !hot || cold {
		t.Errorf("dry run touched storage: hot=%v cold=%v", hot, cold)
	}

	if be := r.placement(sha); be != "" {
		t.Errorf("dry run changed placement to %q", be)
	}

	if n, _ := r.pg.CountStorageTierMoves(context.Background()); n != 0 {
		t.Errorf("dry run left %d journal rows", n)
	}
}

func TestIntegrationPassMovesBlobBetweenDirectories(t *testing.T) {
	r := newPGRig(t)
	data := bytes.Repeat([]byte("artifact bytes "), 1000)
	sha := r.addBlob(data)
	young := func() string {
		sum := sha256.Sum256([]byte("young"))
		s := hex.EncodeToString(sum[:])

		if _, err := r.store.Put(context.Background(), bytes.NewReader([]byte("young")), map[string]string{"sha256": s}); err != nil {
			t.Fatal(err)
		}

		if _, err := r.pg.GetDB().ExecContext(context.Background(), `INSERT INTO blobs (sha256, size_bytes, blob_key) VALUES ($1::text, 5, $1::text)`, s); err != nil {
			t.Fatal(err)
		}

		return s
	}()

	st, err := r.engine.RunPass(context.Background())
	if err != nil || st.Moved != 1 || st.Failed != 0 {
		t.Fatalf("RunPass = %+v, %v; want exactly the old blob moved", st, err)
	}

	// The old copy is queued, not removed: a running backup may still be reading it.
	if hot, cold := r.onDisk(sha); !hot || !cold {
		t.Errorf("right after the move hot=%v cold=%v, want both copies until the sweep", hot, cold)
	}

	r.sweep()

	if hot, cold := r.onDisk(sha); hot || !cold {
		t.Errorf("after the sweep hot=%v cold=%v, want only the cold directory", hot, cold)
	}

	if be := r.placement(sha); be != r.coldID {
		t.Errorf("placement = %q, want the cold backend", be)
	}

	if got := r.readSum(sha); got != sha {
		t.Errorf("read back sha256 %s, want %s", got, sha)
	}

	if hot, cold := r.onDisk(young); !hot || cold {
		t.Errorf("a young blob moved: hot=%v cold=%v", hot, cold)
	}

	if n, _ := r.pg.CountStorageTierMoves(context.Background()); n != 0 {
		t.Errorf("%d journal rows left after a clean pass", n)
	}

	r.assertSafeDeleteQueuesThenSweeps(sha)
}

// assertSafeDeleteQueuesThenSweeps deletes through the refcount-aware path: the
// row goes at once, every copy goes with the sweep.
func (r *pgRig) assertSafeDeleteQueuesThenSweeps(sha string) {
	r.t.Helper()
	t := r.t

	if err := storage.SafeDeleteBlob(context.Background(), r.pg, r.store, sha); err != nil {
		t.Fatalf("SafeDeleteBlob: %v", err)
	}

	// The row is gone at once; the bytes are removed by the sweep.
	if _, _, found, _ := r.pg.GetBlobPlacement(context.Background(), sha); found {
		t.Error("blob row survived SafeDeleteBlob")
	}

	if hot, cold := r.onDisk(sha); !hot && !cold {
		t.Error("SafeDeleteBlob removed bytes immediately instead of queueing them")
	}

	r.sweep()

	if hot, cold := r.onDisk(sha); hot || cold {
		t.Errorf("sweep left a copy: hot=%v cold=%v", hot, cold)
	}

	if _, _, found, _ := r.pg.GetBlobPlacement(context.Background(), sha); found {
		t.Error("blob row survived SafeDeleteBlob")
	}
}

// A crash after any journalled step leaves exactly one authoritative
// placement, readable meanwhile, and the next pass converges.
func TestIntegrationCrashAtEveryStep(t *testing.T) {
	for _, step := range []string{"claimed", "copied", "verified", "flipped", "cleaned"} {
		t.Run(step, func(t *testing.T) {
			r := newPGRig(t)
			data := []byte("crash data " + step)
			sha := r.addBlob(data)

			r.mover.fault = func(s string) error {
				if s == step {
					return errInjectedCrash
				}

				return nil
			}

			if _, err := r.engine.RunPass(context.Background()); err != nil && !errors.Is(err, errInjectedCrash) {
				t.Fatalf("RunPass: %v", err)
			}

			// "Process dies" here: the authoritative copy is whatever the database says.
			be := r.placement(sha)
			hot, cold := r.onDisk(sha)

			switch be {
			case "":
				if !hot {
					t.Fatalf("step %s: placement hot but no hot copy (hot=%v cold=%v)", step, hot, cold)
				}
			case r.coldID:
				if !cold {
					t.Fatalf("step %s: placement cold but no cold copy (hot=%v cold=%v)", step, hot, cold)
				}
			default:
				t.Fatalf("step %s: placement %q is not a known tier", step, be)
			}

			if got := r.readSum(sha); got != sha {
				t.Fatalf("step %s: unreadable or corrupt mid-move (%s)", step, got)
			}

			r.restart() // fresh process, no in-memory state

			if _, err := r.engine.RunPass(context.Background()); err != nil {
				t.Fatalf("recovery pass: %v", err)
			}

			r.sweep()

			if hot, cold := r.onDisk(sha); hot || !cold {
				t.Errorf("step %s: after recovery hot=%v cold=%v, want only cold", step, hot, cold)
			}

			if be := r.placement(sha); be != r.coldID {
				t.Errorf("step %s: placement after recovery = %q", step, be)
			}

			if got := r.readSum(sha); got != sha {
				t.Errorf("step %s: unreadable after recovery", step)
			}

			if n, _ := r.pg.CountStorageTierMoves(context.Background()); n != 0 {
				t.Errorf("step %s: %d journal rows left after recovery", step, n)
			}
		})
	}
}

// An upload that holds no blob lock, slow enough that a move flips the blob and
// the sweeper retires the hot copy before the bytes land, must not leave the
// recreated hot copy behind: the real queue accepts it without the lock and the
// sweep removes it.
func TestIntegrationWriteRacingAMoveLeavesNoStrayCopy(t *testing.T) {
	r := newPGRig(t)
	data := bytes.Repeat([]byte("racing upload "), 500)
	sha := r.addBlob(data)

	racing := &racingStore{BlobStore: r.hot}
	r.hot = racing
	r.restart()

	racing.before = func() {
		if st, err := r.engine.RunPass(context.Background()); err != nil || st.Moved != 1 {
			t.Errorf("racing pass = %+v, %v; want the blob moved", st, err)
		}

		r.sweep()
	}

	if id, err := r.store.Put(context.Background(), bytes.NewReader(data), map[string]string{"sha256": sha}); err != nil || id != sha {
		t.Fatalf("Put = %q, %v", id, err)
	}

	if hot, _ := r.onDisk(sha); !hot {
		t.Fatalf("setup: the racing write was meant to recreate the hot copy")
	}

	if n, err := r.pg.CountBlobDeletions(context.Background()); err != nil || n != 1 {
		t.Fatalf("queued deletions = %d, %v; want the stray hot copy", n, err)
	}

	r.sweep()

	if hot, cold := r.onDisk(sha); hot || !cold {
		t.Errorf("after the sweep hot=%v cold=%v, want only the cold directory", hot, cold)
	}

	if be := r.placement(sha); be != r.coldID {
		t.Errorf("placement = %q, want the cold backend", be)
	}

	if got := r.readSum(sha); got != sha {
		t.Errorf("blob unreadable after the race (%s)", got)
	}
}
