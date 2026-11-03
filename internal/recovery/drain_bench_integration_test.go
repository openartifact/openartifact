// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package recovery

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/storage"
)

// seedBulkRaw inserts n raw files (blob, package, version and file each) into a
// new repository with set-based statements, so the triggers queue 4n dirty rows.
// It returns how many marks the queue holds afterwards.
func seedBulkRaw(tb testing.TB, db *sql.DB, repo string, n int) int {
	tb.Helper()

	ctx := context.Background()

	var repoID string
	if err := db.QueryRowContext(ctx,
		`INSERT INTO repositories (name, format, project_id) VALUES ($1, 'raw', $2) RETURNING id`,
		repo, systemProjectID).Scan(&repoID); err != nil {
		tb.Fatalf("insert repository: %v", err)
	}

	steps := []struct {
		q    string
		args []any
	}{
		{`INSERT INTO blobs (sha256, size_bytes, blob_key)
		  SELECT encode(sha256(($1 || i)::bytea), 'hex'), 10, encode(sha256(($1 || i)::bytea), 'hex')
		  FROM generate_series(1, $2::int) i`, []any{repo, n}},
		{`INSERT INTO packages (repository_id, format, name, name_key)
		  SELECT $1::uuid, 'raw', $2 || i, $2 || i FROM generate_series(1, $3::int) i`, []any{repoID, repo, n}},
		{`INSERT INTO package_versions (package_id, repository_id, version, version_key)
		  SELECT id, repository_id, '', '' FROM packages WHERE repository_id = $1::uuid`, []any{repoID}},
		{`INSERT INTO package_files (version_id, repository_id, path, filename, blob_id, role, attrs, unit_key)
		  SELECT v.id, v.repository_id, p.name AS path, p.name AS filename, b.id, 'primary', '{}'::jsonb, p.name
		  FROM package_versions v
		  JOIN packages p ON p.id = v.package_id
		  JOIN blobs b ON b.sha256 = encode(sha256(($2 || substr(p.name, length($2) + 1))::bytea), 'hex')
		  WHERE v.repository_id = $1::uuid`, []any{repoID, repo}},
	}

	for _, s := range steps {
		if _, err := db.ExecContext(ctx, s.q, s.args...); err != nil {
			tb.Fatalf("seed: %v", err)
		}
	}

	return mustCount(tb, db, `SELECT count(*) FROM recovery_dirty`)
}

// benchStores returns the stores the drain benchmark runs against: a filesystem
// store, plus S3 when TEST_S3_ENDPOINT is set.
func benchStores(tb testing.TB) map[string]func() storage.BlobStore {
	tb.Helper()

	stores := map[string]func() storage.BlobStore{
		"fs": func() storage.BlobStore { return newFSStore(tb) },
		// null discards sidecars: the ceiling the database side of the drain allows.
		"null": func() storage.BlobStore { return discardStore{newFSStore(tb)} },
	}

	if endpoint := os.Getenv("TEST_S3_ENDPOINT"); endpoint != "" {
		stores["s3"] = func() storage.BlobStore {
			s, err := storage.NewS3BlobStore(storage.StorageConfig{
				Type: "s3", Endpoint: endpoint,
				Bucket:         envDefault("TEST_S3_BUCKET", "openartifact-test"),
				Region:         envDefault("TEST_S3_REGION", "us-east-1"),
				AccessKey:      envDefault("TEST_S3_ACCESS_KEY", "minioadmin"),
				SecretKey:      envDefault("TEST_S3_SECRET_KEY", "minioadmin"),
				ForcePathStyle: true,
			})
			if err != nil {
				tb.Fatalf("NewS3BlobStore: %v", err)
			}

			return s
		}
	}

	return stores
}

// discardStore accepts every Put without storing it.
type discardStore struct{ storage.BlobStore }

func (discardStore) Put(_ context.Context, r io.Reader, _ map[string]string) (string, error) {
	_, _ = io.Copy(io.Discard, r)

	return "", nil
}

func (discardStore) Delete(context.Context, string) error { return nil }

func envDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}

	return def
}

// BenchmarkDrain measures how fast the journal turns a backlog of dirty rows
// into sidecar blobs: dirty rows drained per second, on each store, for the
// default and a raised Concurrency. Run with
//
//	go test -tags integration -run '^$' -bench Drain -benchtime 1x ./internal/recovery
//
// and TEST_S3_ENDPOINT set to include S3. DRAIN_BENCH_ROWS sizes the backlog
// (raw files; four dirty rows each).
func BenchmarkDrain(b *testing.B) {
	files := 500

	if v := os.Getenv("DRAIN_BENCH_ROWS"); v != "" {
		if _, err := fmt.Sscan(v, &files); err != nil {
			b.Fatalf("DRAIN_BENCH_ROWS: %v", err)
		}
	}

	for name, newStore := range benchStores(b) {
		for _, conc := range []int{4, 32, 64} {
			b.Run(fmt.Sprintf("%s/concurrency=%d", name, conc), func(b *testing.B) {
				db := newTestDB(b)
				store := newStore()
				j := NewJournal(db, store, testLog(b), nil, JournalConfig{Batch: 200, Concurrency: conc})

				b.ResetTimer()

				for i := range b.N {
					b.StopTimer()

					marks := seedBulkRaw(b, db, fmt.Sprintf("bench%d-%d", time.Now().UnixNano(), i), files)

					b.StartTimer()

					if _, failed, err := j.Drain(context.Background()); err != nil || failed != 0 {
						b.Fatalf("drain: failed=%d err=%v", failed, err)
					}

					b.StopTimer()
					b.ReportMetric(float64(marks)/b.Elapsed().Seconds()*float64(b.N), "rows/s")
					b.StartTimer()
				}
			})
		}
	}
}
