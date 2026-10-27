// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package replication

import (
	"context"
	"os"
	"testing"

	"github.com/openartifact/openartifact/internal/metrics"
)

// The store reports the primary side: a lost replica degrades redundancy, not
// serving, and has its own status endpoint.
func TestStoreHealthCheckReportsThePrimarySide(t *testing.T) {
	clock := newFakeClock()
	primary, replica := newFS(t), newFS(t)

	svc, err := New(Config{
		Primary: primary.BlobStore, Replica: replica.BlobStore, Queue: newMemQueue(clock.Now),
		Options: defaultTestOptions(), Metrics: metrics.NewReplicationMetrics(),
		Info: ReplicaInfo{Type: "filesystem", Location: "replica"},
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(svc.Stop)

	ctx := context.Background()

	if err := svc.Store().HealthCheck(ctx); err != nil {
		t.Fatalf("healthy: %v", err)
	}

	if err := os.RemoveAll(replica.dir); err != nil {
		t.Fatal(err)
	}

	if err := svc.Store().HealthCheck(ctx); err != nil {
		t.Errorf("a lost replica fails the check: %v", err)
	}

	if err := os.RemoveAll(primary.dir); err != nil {
		t.Fatal(err)
	}

	if err := svc.Store().HealthCheck(ctx); err == nil {
		t.Error("a lost primary passed the check")
	}
}
