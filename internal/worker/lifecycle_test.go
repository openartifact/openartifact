// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/databasetest"
	"github.com/openartifact/openartifact/pkg/logger"
)

// gcDB satisfies DockerGCDB; Stop never reaches the GC store.
type gcDB struct {
	*databasetest.BaseMockDB
	database.DockerGCStore
}

// Stop must be idempotent and must not hang when Start was never called.
func TestWorkerStopWithoutStartAndTwice(t *testing.T) {
	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatal(err)
	}

	db := &databasetest.BaseMockDB{}

	stoppers := map[string]func(){
		"cleanup":   NewCleanupWorker(db, nil, log).Stop,
		"dockergc":  NewDockerGCWorker(&gcDB{BaseMockDB: db}, nil, log, time.Hour, time.Hour).Stop,
		"presigned": NewPresignedUploadWorker(db, nil, time.Hour, log).Stop,
		"dockerup":  NewDockerUploadSweeper(db, t.TempDir(), time.Hour, log).Stop,
	}

	for name, stop := range stoppers {
		done := make(chan struct{})

		go func() {
			stop()
			stop()
			close(done)
		}()

		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: Stop hung or panicked", name)
		}
	}
}
