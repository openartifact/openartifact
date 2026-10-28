// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/databasetest"
	"github.com/openartifact/openartifact/pkg/logger"
)

type nixPendingDB struct {
	databasetest.BaseMockDB

	batches [][]string
	calls   int
	before  time.Time
	limit   int
}

func (d *nixPendingDB) DeleteStaleNixPendingNars(_ context.Context, before time.Time, limit int) ([]string, error) {
	d.calls++
	d.before = before
	d.limit = limit

	if d.calls > len(d.batches) {
		return nil, nil
	}

	return d.batches[d.calls-1], nil
}

func (d *nixPendingDB) BlobReferenceCount(context.Context, string) (int, error) { return 0, nil }

func TestSweepNixPendingNarsIfDue(t *testing.T) {
	log, err := logger.New("error", "console", "stderr")
	if err != nil {
		t.Fatal(err)
	}

	full := make([]string, nixPendingBatch)
	for i := range full {
		full[i] = strings.Repeat("a", 63) + string(rune('a'+i%6))
	}

	db := &nixPendingDB{batches: [][]string{full, {strings.Repeat("b", 64)}}}
	store := &countingStore{}
	w := NewCleanupWorker(db, store, log)
	now := time.Now()

	w.sweepNixPendingNarsIfDue(context.Background(), now)

	if db.calls != 2 || store.deletes != nixPendingBatch+1 {
		t.Fatalf("first tick: db calls=%d blob deletes=%d; want 2/%d (a full batch is followed by another)",
			db.calls, store.deletes, nixPendingBatch+1)
	}

	if want := now.Add(-nixPendingMaxAge); !db.before.Equal(want) || db.limit != nixPendingBatch {
		t.Errorf("sweep asked for before=%v limit=%d; want %v/%d", db.before, db.limit, want, nixPendingBatch)
	}

	w.sweepNixPendingNarsIfDue(context.Background(), now.Add(nixPendingInterval-time.Second))

	if db.calls != 2 {
		t.Errorf("sweep ran again within %v (calls=%d)", nixPendingInterval, db.calls)
	}

	w.sweepNixPendingNarsIfDue(context.Background(), now.Add(nixPendingInterval))

	if db.calls != 3 {
		t.Errorf("sweep did not run after %v (calls=%d)", nixPendingInterval, db.calls)
	}
}
