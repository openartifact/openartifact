// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"testing"
	"time"

	"github.com/openartifact/openartifact/pkg/logger"
)

// gcDB satisfies DockerGCDB; Stop never reaches the GC store.
type gcDB struct {
	DockerGCDB
}

// Stop must be idempotent and must not hang when Start was never called.
func TestWorkerStopWithoutStartAndTwice(t *testing.T) {
	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatal(err)
	}

	stoppers := map[string]func(){
		"cleanup":   NewCleanupWorker(nil, nil, log).Stop,
		"dockergc":  NewDockerGCWorker(&gcDB{}, nil, log, time.Hour, time.Hour).Stop,
		"presigned": NewPresignedUploadWorker(nil, nil, time.Hour, log).Stop,
		"dockerup":  NewDockerUploadSweeper(nil, t.TempDir(), time.Hour, log).Stop,
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
