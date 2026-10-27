// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package integration_test

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/migrations"
)

// freshPostgres creates an empty database on the TEST_DATABASE_URL server, runs
// the embedded migrations (001..N) on it, and returns a PostgresDB over it.
func freshPostgres(t *testing.T, maxOpen int) *database.PostgresDB {
	t.Helper()

	return freshPostgresCfg(t, maxOpen, nil)
}

// freshPostgresCfg is freshPostgres with a hook to adjust the connection config.
func freshPostgresCfg(t *testing.T, maxOpen int, mutate func(*database.DatabaseConfig)) *database.PostgresDB {
	t.Helper()

	admin := setupTestDB(t)
	ctx := context.Background()

	dbName := fmt.Sprintf("oa_gaps_%d", time.Now().UnixNano())

	if _, err := admin.ExecContext(ctx, `CREATE DATABASE `+dbName); err != nil {
		t.Fatalf("create database: %v", err)
	}

	t.Cleanup(func() {
		_, _ = admin.ExecContext(context.Background(), `DROP DATABASE IF EXISTS `+dbName+` WITH (FORCE)`)
	})

	u, err := url.Parse(os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatalf("parse TEST_DATABASE_URL: %v", err)
	}

	port, _ := strconv.Atoi(u.Port())
	pw, _ := u.User.Password()

	sslmode := u.Query().Get("sslmode")
	if sslmode == "" {
		sslmode = "disable"
	}

	cfg := database.DatabaseConfig{
		Host:                   u.Hostname(),
		Port:                   port,
		User:                   u.User.Username(),
		Password:               pw,
		Database:               dbName,
		SSLMode:                sslmode,
		MaxOpenConns:           maxOpen,
		MaxIdleConns:           1,
		ConnMaxLifetimeMinutes: 5,
	}

	if mutate != nil {
		mutate(&cfg)
	}

	pg, err := database.NewPostgresDB(cfg, newTestLogger(t))
	if err != nil {
		t.Fatalf("NewPostgresDB: %v", err)
	}

	t.Cleanup(func() { _ = pg.Close() })

	if err := pg.Migrate(ctx); err != nil {
		t.Fatalf("apply embedded migrations: %v", err)
	}

	return pg
}

func TestEmbeddedMigrationsApplyCleanly(t *testing.T) {
	pg := freshPostgres(t, 5)
	ctx := context.Background()

	entries, err := migrations.FS.ReadDir(".")
	if err != nil {
		t.Fatalf("read embedded migrations: %v", err)
	}

	want := 0

	for _, e := range entries {
		if !e.IsDir() && len(e.Name()) > 4 && e.Name()[len(e.Name())-4:] == ".sql" {
			want++
		}
	}

	var got int
	if err := pg.GetDB().QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&got); err != nil {
		t.Fatalf("count schema_migrations: %v", err)
	}

	if got != want {
		t.Errorf("schema_migrations rows = %d, want %d (one per embedded migration)", got, want)
	}

	// Re-applying must be a no-op (checksums match, nothing pending).
	if err := pg.Migrate(ctx); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
}

func TestSQLPoolHonoursConfiguredLimit(t *testing.T) {
	pg := freshPostgres(t, 3)

	if got := pg.GetDB().Stats().MaxOpenConnections; got != 3 {
		t.Errorf("database/sql MaxOpenConnections = %d, want 3", got)
	}
}

func seedWebhook(t *testing.T, pg *database.PostgresDB) *models.Webhook {
	t.Helper()

	wh := &models.Webhook{
		Name:           "claim-test",
		URL:            "https://example.com/hook",
		Enabled:        true,
		Events:         []string{"artifact.uploaded"},
		ContentType:    "application/json",
		SSLVerify:      true,
		MaxRetries:     3,
		TimeoutSeconds: 10,
	}

	if err := pg.CreateWebhook(context.Background(), wh); err != nil {
		t.Fatalf("CreateWebhook: %v", err)
	}

	return wh
}

func TestClaimPendingWebhookDeliveriesIsExclusive(t *testing.T) {
	pg := freshPostgres(t, 10)
	ctx := context.Background()
	wh := seedWebhook(t, pg)

	const total = 40

	for i := range total {
		d := &models.WebhookDelivery{
			WebhookID: wh.ID,
			EventType: "artifact.uploaded",
			Payload:   map[string]any{"i": i},
			Status:    "pending",
		}
		if err := pg.CreateWebhookDelivery(ctx, d); err != nil {
			t.Fatalf("CreateWebhookDelivery: %v", err)
		}
	}

	// A delivery created already leased (next_retry_at in the future) must not be claimable.
	lease := time.Now().Add(time.Hour)
	leased := &models.WebhookDelivery{
		WebhookID: wh.ID, EventType: "artifact.uploaded", Payload: map[string]any{"leased": true},
		Status: "pending", NextRetryAt: &lease,
	}

	if err := pg.CreateWebhookDelivery(ctx, leased); err != nil {
		t.Fatalf("CreateWebhookDelivery(leased): %v", err)
	}

	// Several "instances" poll concurrently; no delivery may be returned twice.
	var (
		mu   sync.Mutex
		seen = map[string]int{}
		wg   sync.WaitGroup
	)

	for range 8 {
		wg.Go(func() {
			got, err := pg.ClaimPendingWebhookDeliveries(ctx, 10, time.Minute)
			if err != nil {
				t.Errorf("claim: %v", err)
				return
			}

			mu.Lock()
			defer mu.Unlock()

			for _, d := range got {
				seen[d.ID]++

				if d.NextRetryAt == nil || !d.NextRetryAt.After(time.Now()) {
					t.Errorf("claimed delivery %s has no future lease: %v", d.ID, d.NextRetryAt)
				}
			}
		})
	}

	wg.Wait()

	if len(seen) != total {
		t.Errorf("claimed %d distinct deliveries, want %d", len(seen), total)
	}

	for id, n := range seen {
		if n != 1 {
			t.Errorf("delivery %s claimed %d times", id, n)
		}

		if id == leased.ID {
			t.Errorf("pre-leased delivery %s was claimed", id)
		}
	}

	// Everything is leased now: a further poll finds nothing.
	again, err := pg.ClaimPendingWebhookDeliveries(ctx, 100, time.Minute)
	if err != nil || len(again) != 0 {
		t.Fatalf("second claim = %d rows, err %v; want 0", len(again), err)
	}

	// An expired lease makes the delivery claimable again (crashed instance).
	if _, err := pg.GetDB().ExecContext(ctx,
		`UPDATE webhook_deliveries SET next_retry_at = now() - interval '1 second' WHERE id = $1`, leased.ID); err != nil {
		t.Fatalf("expire lease: %v", err)
	}

	reclaimed, err := pg.ClaimPendingWebhookDeliveries(ctx, 100, time.Minute)
	if err != nil || len(reclaimed) != 1 || reclaimed[0].ID != leased.ID {
		t.Fatalf("reclaim after lease expiry: %d rows, err %v", len(reclaimed), err)
	}
}

func TestIsArtifactQuarantined(t *testing.T) {
	pg := freshPostgres(t, 5)
	ctx := context.Background()
	db := pg.GetDB()

	systemOrg := "00000000-0000-0000-0000-000000000001"

	repo := &models.Repository{Name: "q-repo", Format: "raw", RepoType: "hosted", ProjectID: &systemOrg}
	if err := pg.CreateRepository(ctx, repo); err != nil {
		t.Fatalf("CreateRepository: %v", err)
	}

	other := &models.Repository{Name: "q-other", Format: "raw", RepoType: "hosted", ProjectID: &systemOrg}
	if err := pg.CreateRepository(ctx, other); err != nil {
		t.Fatalf("CreateRepository(other): %v", err)
	}

	mustExec := func(q string, args ...any) {
		t.Helper()

		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("exec %q: %v", q, err)
		}
	}

	var refID, jobID string

	scan := func(dst *string, q string, args ...any) {
		t.Helper()

		if err := db.QueryRowContext(ctx, q, args...).Scan(dst); err != nil {
			t.Fatalf("query %q: %v", q, err)
		}
	}

	scan(
		&refID,
		`INSERT INTO artifact_refs (repository_id, artifact_path, format) VALUES ($1, 'pkg/bad-1.0.tgz', 'raw') RETURNING id`,
		repo.ID,
	)
	scan(
		&jobID,
		`INSERT INTO scan_jobs (artifact_ref_id, scan_type, scanner, status) VALUES ($1, 'vuln', 'osv', 'completed') RETURNING id`,
		refID,
	)
	mustExec(`INSERT INTO vuln_results (scan_job_id, quarantined) VALUES ($1, false)`, jobID)

	check := func(repoID string, want bool, keys ...string) {
		t.Helper()

		got, err := pg.IsArtifactQuarantined(ctx, repoID, keys)
		if err != nil {
			t.Fatalf("IsArtifactQuarantined: %v", err)
		}

		if got != want {
			t.Errorf("IsArtifactQuarantined(%s, %v) = %v, want %v", repoID, keys, got, want)
		}
	}

	check(repo.ID, false, "pkg/bad-1.0.tgz") // scanned but not quarantined

	mustExec(`UPDATE vuln_results SET quarantined = true WHERE scan_job_id = $1`, jobID)

	check(repo.ID, true, "pkg/bad-1.0.tgz")
	check(repo.ID, true, "nope", "pkg/bad-1.0.tgz") // any key matches
	check(repo.ID, false, "pkg/good-1.0.tgz")
	check(other.ID, false, "pkg/bad-1.0.tgz") // quarantine is per repository
	check(repo.ID, false)                     // no keys

	// A secret-scan quarantine blocks as well.
	var secretRef, secretJob string

	scan(
		&secretRef,
		`INSERT INTO artifact_refs (repository_id, artifact_path, format) VALUES ($1, 'pkg/leak.zip', 'raw') RETURNING id`,
		repo.ID,
	)
	scan(
		&secretJob,
		`INSERT INTO scan_jobs (artifact_ref_id, scan_type, status) VALUES ($1, 'secret', 'completed') RETURNING id`,
		secretRef,
	)
	mustExec(`INSERT INTO secret_results (scan_job_id, quarantined) VALUES ($1, true)`, secretJob)

	check(repo.ID, true, "pkg/leak.zip")
}

// Nullable timestamptz columns must scan as nil pointers, not fail with
// "cannot scan NULL into *time.Time" (sqlc used to map them to time.Time).
func TestNullableTimestampsWebhookDelivery(t *testing.T) {
	pg := freshPostgres(t, 5)
	ctx := context.Background()
	wh := seedWebhook(t, pg)

	d := &models.WebhookDelivery{WebhookID: wh.ID, EventType: "artifact.uploaded", Payload: map[string]any{"k": "v"}, Status: "pending"}
	if err := pg.CreateWebhookDelivery(ctx, d); err != nil {
		t.Fatalf("CreateWebhookDelivery: %v", err)
	}

	list, err := pg.ListWebhookDeliveries(ctx, wh.ID, 10)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListWebhookDeliveries: %d rows, err %v", len(list), err)
	}

	if list[0].NextRetryAt != nil || list[0].DeliveredAt != nil {
		t.Errorf("NULL timestamps must be nil, got next_retry_at=%v delivered_at=%v", list[0].NextRetryAt, list[0].DeliveredAt)
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	d.Status, d.DeliveredAt = "success", &now

	if err := pg.UpdateWebhookDelivery(ctx, d); err != nil {
		t.Fatalf("UpdateWebhookDelivery: %v", err)
	}

	list, _ = pg.ListWebhookDeliveries(ctx, wh.ID, 10)
	if len(list) != 1 || list[0].DeliveredAt == nil || !list[0].DeliveredAt.Equal(now) || list[0].NextRetryAt != nil {
		t.Errorf("delivered_at not persisted / next_retry_at not NULL: %+v", list)
	}
}

func TestNullableTimestampsBackupJob(t *testing.T) {
	pg := freshPostgres(t, 5)
	ctx := context.Background()
	now := time.Now().UTC()

	job := &models.BackupJob{Status: "pending", Type: "full", CreatedAt: now, UpdatedAt: now}
	if err := pg.CreateBackupJob(ctx, job); err != nil {
		t.Fatalf("CreateBackupJob: %v", err)
	}

	got, err := pg.GetBackupJob(ctx, job.ID)
	if err != nil || got == nil {
		t.Fatalf("GetBackupJob: %v %v", got, err)
	}

	if got.StartedAt != nil || got.CompletedAt != nil {
		t.Errorf("backup job NULL timestamps must be nil, got %v / %v", got.StartedAt, got.CompletedAt)
	}
}

func TestNullableTimestampsCleanupPolicy(t *testing.T) {
	pg := freshPostgres(t, 5)
	ctx := context.Background()
	systemOrg := "00000000-0000-0000-0000-000000000001"

	repo := &models.Repository{Name: "np-repo", Format: "raw", RepoType: "hosted", ProjectID: &systemOrg}
	if err := pg.CreateRepository(ctx, repo); err != nil {
		t.Fatalf("CreateRepository: %v", err)
	}

	keep := 3
	policy := &models.CleanupPolicy{
		RepositoryID: repo.ID,
		Name:         "p",
		Enabled:      true,
		KeepLastN:    &keep,
		ScheduleCron: models.DefaultCleanupCron,
	}

	if err := pg.CreateCleanupPolicy(ctx, policy); err != nil {
		t.Fatalf("CreateCleanupPolicy: %v", err)
	}

	got, err := pg.GetCleanupPolicy(ctx, policy.ID)
	if err != nil || got == nil {
		t.Fatalf("GetCleanupPolicy: %v %v", got, err)
	}

	if got.LastRunAt != nil {
		t.Errorf("never-run policy must have nil LastRunAt, got %v", got.LastRunAt)
	}

	enabled, err := pg.ListEnabledCleanupPolicies(ctx)
	if err != nil || len(enabled) != 1 {
		t.Fatalf("ListEnabledCleanupPolicies: %d rows, err %v", len(enabled), err)
	}
}

// Blob-lock holders pin a pooled connection while their callback needs more:
// with far more concurrent callers than connections this used to deadlock the
// pool. The lock-slot semaphore keeps half the pool free for callbacks.
func TestBlobLocksDoNotExhaustThePool(t *testing.T) {
	pg := freshPostgres(t, 4)

	const callers = 40

	var (
		wg       sync.WaitGroup
		inFlight atomic.Int32
		maxSeen  atomic.Int32
	)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	for i := range callers {
		wg.Go(func() {
			shaA := fmt.Sprintf("%064x", i%7)
			shaB := fmt.Sprintf("%064x", (i+3)%7)

			err := pg.WithBlobLocks(ctx, []string{shaB, shaA}, func(ctx context.Context) error {
				n := inFlight.Add(1)
				defer inFlight.Add(-1)

				for {
					old := maxSeen.Load()
					if n <= old || maxSeen.CompareAndSwap(old, n) {
						break
					}
				}

				var one int

				return pg.GetDB().QueryRowContext(ctx, `SELECT 1`).Scan(&one)
			})
			if err != nil {
				t.Errorf("caller %d: %v", i, err)
			}
		})
	}

	wg.Wait()

	if ctx.Err() != nil {
		t.Fatal("blob lock callers did not finish: pool exhausted")
	}

	if got := maxSeen.Load(); got > 2 {
		t.Errorf("%d callbacks ran inside blob locks at once, want at most MaxOpenConns/2 = 2", got)
	}
}
