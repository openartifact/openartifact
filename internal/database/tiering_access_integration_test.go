// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestTouchBlobAccessWritesFirstReadAfterFlip: a blob read, then demoted, then
// read again inside the access resolution must still record the second read,
// or promote-on-read would miss it.
func TestTouchBlobAccessWritesFirstReadAfterFlip(t *testing.T) {
	f := newTierFixture(t)
	ctx := context.Background()
	sha := f.addBlob(t, "flip-read", 10, 48*time.Hour)

	readBefore := time.Now().Add(-30 * time.Minute)

	if n, err := f.pg.TouchBlobAccess(ctx, map[string]time.Time{sha: readBefore}, time.Hour); err != nil || n != 1 {
		t.Fatalf("first touch = %d, %v", n, err)
	}

	// Demoted 10 minutes ago, i.e. after the recorded read.
	if _, err := f.pg.GetDB().ExecContext(ctx,
		`UPDATE blobs SET storage_backend_id = $1::uuid, tier_changed_at = now() - interval '10 minutes' WHERE sha256 = $2`, f.cold, sha); err != nil {
		t.Fatal(err)
	}

	// A read 5 minutes ago is within an hour of the recorded one, yet it is the
	// first read on the cold tier.
	if n, err := f.pg.TouchBlobAccess(ctx, map[string]time.Time{sha: time.Now().Add(-5 * time.Minute)}, time.Hour); err != nil || n != 1 {
		t.Fatalf("first read after the flip wrote %d rows (%v), want 1", n, err)
	}

	cand, err := f.pg.ListPromotionCandidates(ctx, []string{f.cold}, "", 10)
	if err != nil || len(cand) != 1 || cand[0].SHA256 != sha {
		t.Fatalf("promotion candidates = %+v, %v", cand, err)
	}

	// A second read inside the resolution is a no-op again.
	if n, _ := f.pg.TouchBlobAccess(ctx, map[string]time.Time{sha: time.Now()}, time.Hour); n != 0 {
		t.Errorf("repeat read rewrote %d rows", n)
	}

	// A read recorded from before the flip does not count as a read on the cold tier.
	other := f.addBlob(t, "stale-read", 10, 48*time.Hour)

	if _, err := f.pg.GetDB().ExecContext(ctx,
		`UPDATE blobs SET storage_backend_id = $1::uuid, tier_changed_at = now() - interval '10 minutes', last_accessed_at = now() - interval '20 minutes' WHERE sha256 = $2`, f.cold, other); err != nil {
		t.Fatal(err)
	}

	if n, _ := f.pg.TouchBlobAccess(ctx, map[string]time.Time{other: time.Now().Add(-15 * time.Minute)}, time.Hour); n != 0 {
		t.Errorf("a read from before the flip was written (%d rows)", n)
	}
}

// TestTouchBlobAccessConcurrentOverlapDoesNotDeadlock: flushes over the same
// blobs lock rows in one order, so concurrent flushes never abort with 40P01.
func TestTouchBlobAccessConcurrentOverlapDoesNotDeadlock(t *testing.T) {
	f := newTierFixture(t)
	ctx := context.Background()

	const blobs = 60

	shas := make([]string, blobs)
	for i := range shas {
		shas[i] = f.addBlob(t, fmt.Sprintf("overlap-%d", i), 1, time.Hour)
	}

	var wg sync.WaitGroup

	errs := make(chan error, 8)

	for w := range 4 {
		wg.Go(func() {
			for round := range 25 {
				at := time.Now().Add(time.Duration(w*1000+round) * time.Hour) // always past the resolution
				batch := make(map[string]time.Time, blobs)

				for _, s := range shas {
					batch[s] = at
				}

				if _, err := f.pg.TouchBlobAccess(ctx, batch, time.Minute); err != nil {
					errs <- err

					return
				}
			}
		})
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("concurrent flush failed: %v", err)
	}
}

// TestPromotionCandidatesUsePartialIndex: with sequential scans disabled the
// promotion query must be answered from idx_blobs_tier_promote, which only
// holds blobs read since they reached their tier, so the planner proves the
// index covers the query and never walks every cold blob.
func TestPromotionCandidatesUsePartialIndex(t *testing.T) {
	f := newTierFixture(t)
	ctx := context.Background()

	// A realistic shape: most blobs hot, many cold but never read since they
	// arrived, a handful read on the cold tier.
	if _, err := f.pg.GetDB().ExecContext(ctx,
		`INSERT INTO blobs (sha256, size_bytes, blob_key, storage_backend_id, last_accessed_at, tier_changed_at)
		 SELECT md5(i::text) || md5(i::text || 'x'), 1, md5(i::text), 
		        CASE WHEN i % 2 = 0 THEN $1::uuid END,
		        CASE WHEN i % 500 = 0 THEN now() END,
		        CASE WHEN i % 2 = 0 THEN now() - interval '1 hour' END
		 FROM generate_series(1, 20000) AS i`, f.cold); err != nil {
		t.Fatal(err)
	}

	if _, err := f.pg.GetDB().ExecContext(ctx, `ANALYZE blobs`); err != nil {
		t.Fatal(err)
	}

	// Pin one connection: SET is per session.
	conn, err := f.pg.GetDB().Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = conn.Close() }()

	if _, err := conn.ExecContext(ctx, `SET enable_seqscan = off`); err != nil {
		t.Fatal(err)
	}

	rows, err := conn.QueryContext(ctx, "EXPLAIN "+promotionCandidatesQuery, []string{f.cold}, "", 100)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = rows.Close() }()

	var plan strings.Builder

	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}

		plan.WriteString(line + "\n")
	}

	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	got := plan.String()
	if !strings.Contains(got, "idx_blobs_tier_promote") || strings.Contains(got, "idx_blobs_storage_backend") {
		t.Errorf("promotion query does not use idx_blobs_tier_promote:\n%s", got)
	}
}
