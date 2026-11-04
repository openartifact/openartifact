// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package docker

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
	"github.com/openartifact/openartifact/internal/storage"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver
)

// newAttachLockDB opens a fresh, migrated database whose pool is capped at
// maxOpenConns, which also sizes the blob lock slots (maxOpenConns/2).
func newAttachLockDB(t *testing.T, maxOpenConns int) *database.PostgresDB {
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

	dbName := fmt.Sprintf("oa_attachlock_%d", time.Now().UnixNano())
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
		Database: dbName, SSLMode: sslmode, MaxOpenConns: maxOpenConns, MaxIdleConns: maxOpenConns,
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

// attachLockWorld is a docker repository holding one image manifest to attach
// referrers to.
type attachLockWorld struct {
	pg      *database.PostgresDB
	store   storage.BlobStore
	h       *HTTPHandler
	repo    *models.Repository
	subject string
}

func newAttachLockWorld(t *testing.T, maxOpenConns int) *attachLockWorld {
	t.Helper()

	pg := newAttachLockDB(t, maxOpenConns)

	store, err := storage.NewFilesystemStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFilesystemStore: %v", err)
	}

	var projectID string
	if err := pg.GetDB().QueryRowContext(t.Context(),
		`INSERT INTO projects (name, slug) VALUES ('attach-lock', 'attach-lock')
		 ON CONFLICT (name) DO UPDATE SET name = EXCLUDED.name RETURNING id`).Scan(&projectID); err != nil {
		t.Fatalf("insert project: %v", err)
	}

	repo := &models.Repository{Name: "attach-lock", Format: "docker", RepoType: "hosted", ProjectID: &projectID}
	if err := pg.CreateRepository(t.Context(), repo); err != nil {
		t.Fatalf("CreateRepository: %v", err)
	}

	w := &attachLockWorld{pg: pg, store: store, h: NewHTTPHandler(formattest.NewLogger(t), pg, store), repo: repo}

	body := []byte(`{"schemaVersion":2,"mediaType":"` + mediaTypeOCIManifest + `"}`)
	sum := sha256.Sum256(body)
	shaHex := hex.EncodeToString(sum[:])
	now := time.Now()

	subject := &models.DockerManifest{
		ID: uuid.New().String(), RepositoryID: repo.ID, Digest: "sha256:" + shaHex, MediaType: mediaTypeOCIManifest,
		Size: int64(len(body)), CreatedAt: now, UpdatedAt: now,
	}
	if err := w.h.storeManifest(t.Context(), subject, shaHex, mediaTypeOCIManifest, body); err != nil {
		t.Fatalf("store subject manifest: %v", err)
	}

	w.subject = subject.Digest

	return w
}

// attach attaches one referrer of kind to the subject; writer makes the content
// (and so the manifest) differ between concurrent attaches of one slot.
func (w *attachLockWorld) attach(ctx context.Context, kind string, writer int) (*ReferrerResult, error) {
	return w.h.AttachReferrer(ctx, w.repo, ReferrerRequest{
		SubjectDigest: w.subject, ArtifactType: testBundleType,
		Content:     fmt.Appendf(nil, `{"kind":%q,"writer":%d}`, kind, writer),
		Annotations: map[string]string{"kind": kind},
		SlotKeys:    []string{"kind"},
	})
}

// attachDeadline is how long the concurrent attaches may take. They finish in
// well under a second; a deadlock never does.
const attachDeadline = 30 * time.Second

// collectOrphansUntilDone runs the orphan blob collector back to back until ctx
// ends. minAge 0 makes it delete every blobs row nothing references at that
// instant: a blob an uploader has stored but not yet referenced is only safe
// behind its blob lock.
func (w *attachLockWorld) collectOrphansUntilDone(ctx context.Context) error {
	for ctx.Err() == nil {
		if _, err := storage.CollectOrphanBlobs(ctx, w.pg, w.store, 0, storage.OrphanBatch); err != nil && ctx.Err() == nil {
			return fmt.Errorf("collect orphan blobs: %w", err)
		}

		time.Sleep(time.Millisecond)
	}

	return nil
}

// Concurrent AttachReferrer calls must not exhaust the blob lock slots: the
// slot lock used to wrap commits and deletes that take a lock slot of their
// own, so once as many attaches held the outer lock as there are slots, every
// one of them waited for a slot that only another waiter could free. The pool
// here is 4 connections, so 2 lock slots, against 12 concurrent attaches.
//
// The attaches of one slot race each other, so exactly one referrer per slot
// must remain, while the orphan collector keeps deleting every blob nothing
// references yet: a manifest or layer blob must survive the window between its
// bytes being stored and its row being committed.
//
// The attaches run on goroutines that never touch t, so a deadlock fails the
// test after attachDeadline instead of hanging it.
func TestAttachReferrerConcurrentAttachesDoNotDeadlockTheLockSlots(t *testing.T) {
	const (
		maxOpenConns = 4
		kinds        = 4
		writers      = 3
	)

	w := newAttachLockWorld(t, maxOpenConns)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	gcDone := make(chan error, 1)

	go func() { gcDone <- w.collectOrphansUntilDone(ctx) }()

	results := make(chan error, kinds*writers)

	for k := range kinds {
		for writer := range writers {
			go func() {
				_, err := w.attach(ctx, strconv.Itoa(k), writer)
				if err != nil {
					err = fmt.Errorf("attach slot %d writer %d: %w", k, writer, err)
				}

				results <- err
			}()
		}
	}

	deadline := time.After(attachDeadline)

	for range kinds * writers {
		select {
		case err := <-results:
			if err != nil {
				t.Error(err)
			}
		case <-deadline:
			t.Fatalf("attaches still running after %v: the blob lock slots are deadlocked", attachDeadline)
		}
	}

	cancel()

	if err := <-gcDone; err != nil {
		t.Error(err)
	}

	if t.Failed() {
		return
	}

	w.assertOneReferrerPerSlot(t, kinds)
}

// assertOneReferrerPerSlot checks that the subject holds exactly one referrer
// for each of the kinds slots and that each one's manifest blob is intact.
func (w *attachLockWorld) assertOneReferrerPerSlot(t *testing.T, kinds int) {
	t.Helper()

	ctx := t.Context()

	list, err := w.pg.ListDockerManifestsBySubject(ctx, w.repo.ID, "", w.subject)
	if err != nil {
		t.Fatalf("list referrers: %v", err)
	}

	if len(list) != kinds {
		t.Fatalf("referrers = %d; want one per slot (%d)", len(list), kinds)
	}

	for k := range kinds {
		digest, err := w.h.FindReferrer(ctx, w.repo, "", w.subject, testBundleType, map[string]string{"kind": strconv.Itoa(k)})
		if err != nil || digest == "" {
			t.Errorf("slot %d: referrer = %q, %v; want one", k, digest, err)
		}
	}

	for _, m := range list {
		sha := manifestBlobKey(m.Digest)

		if refs, err := w.pg.BlobReferenceCount(ctx, sha); err != nil || refs == 0 {
			t.Errorf("manifest blob %s: %d references, %v; want its row to hold it", sha, refs, err)
		}

		if _, ok, err := w.store.ExistsBySHA256(ctx, sha); err != nil || !ok {
			t.Errorf("manifest blob %s: exists %v, %v; want it in the store", sha, ok, err)
		}
	}
}
