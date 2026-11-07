// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package integration_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/audit"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/metrics"
	"github.com/openartifact/openartifact/pkg/logger"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func newAuditTestRegistry() *metrics.Registry {
	dropped := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "openartifact_audit_dropped_total_test",
		Help: "Test dropped counter.",
	})

	return &metrics.Registry{AuditDroppedTotal: dropped}
}

type captureDB struct {
	MockDatabase

	ch chan *models.AuditLog
}

func newCaptureDB() *captureDB {
	return &captureDB{
		MockDatabase: *newMockDatabase(),
		ch:           make(chan *models.AuditLog, 100),
	}
}

func (c *captureDB) InsertAuditLog(_ context.Context, e *models.AuditLog) error {
	c.ch <- e
	return nil
}

func TestAuditLogger_LogEnqueues(t *testing.T) {
	db := newCaptureDB()
	log, _ := logger.New("error", "json", "stderr")
	reg := newAuditTestRegistry()

	al := audit.NewLogger(db, log, reg, 10)
	al.Log(context.Background(), audit.AuditEvent{Action: "test.action", Actor: "alice"})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	select {
	case ev := <-db.ch:
		if ev.Action != "test.action" {
			t.Errorf("expected action test.action, got %s", ev.Action)
		}

		var details map[string]string
		if err := json.Unmarshal(ev.DetailsJSON, &details); err != nil {
			t.Fatalf("unmarshal details_json: %v", err)
		}

		if details["actor"] != "alice" {
			t.Errorf("expected actor alice, got %s", details["actor"])
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for audit event to be inserted")
	}

	_ = al.Shutdown(context.Background())
}

func TestAuditLogger_BackpressureDropsIncrementsCounter(t *testing.T) {
	reg := newAuditTestRegistry()
	log, _ := logger.New("error", "json", "stderr")

	blockDB := &blockingDB{ready: make(chan struct{})}
	al := audit.NewLogger(blockDB, log, reg, 1)

	al.Log(context.Background(), audit.AuditEvent{Action: "first"})
	al.Log(context.Background(), audit.AuditEvent{Action: "drop1"})
	al.Log(context.Background(), audit.AuditEvent{Action: "drop2"})

	deadline := time.Now().Add(5 * time.Second)

	for testutil.ToFloat64(reg.AuditDroppedTotal) < 1 {
		if time.Now().After(deadline) {
			t.Fatalf("expected >=1 dropped events, got %v", testutil.ToFloat64(reg.AuditDroppedTotal))
		}

		time.Sleep(time.Millisecond)
	}

	close(blockDB.ready)

	_ = al.Shutdown(context.Background())
}

type blockingDB struct {
	MockDatabase

	ready chan struct{}
}

func (b *blockingDB) InsertAuditLog(_ context.Context, _ *models.AuditLog) error {
	<-b.ready
	return nil
}

func TestAuditLogger_ShutdownDrainsChannel(t *testing.T) {
	db := newCaptureDB()
	log, _ := logger.New("error", "json", "stderr")
	reg := newAuditTestRegistry()

	al := audit.NewLogger(db, log, reg, 100)
	for range 5 {
		al.Log(context.Background(), audit.AuditEvent{Action: "drain"})
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if err := al.Shutdown(ctx); err != nil {
		t.Errorf("Shutdown returned error: %v", err)
	}

	count := len(db.ch)
	if count != 5 {
		t.Errorf("expected 5 inserted events after shutdown, got %d", count)
	}
}

func TestAuditLogger_ShutdownRespectsDeadline(t *testing.T) {
	log, _ := logger.New("error", "json", "stderr")
	reg := newAuditTestRegistry()

	neverDB := &neverInsertDB{release: make(chan struct{})}
	t.Cleanup(func() { close(neverDB.release) })
	al := audit.NewLogger(neverDB, log, reg, 10)
	al.Log(context.Background(), audit.AuditEvent{Action: "stuck"})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := al.Shutdown(ctx)
	if err == nil {
		t.Error("expected deadline error from Shutdown with blocked worker")
	}
}

type neverInsertDB struct {
	MockDatabase

	release chan struct{}
}

func (n *neverInsertDB) InsertAuditLog(_ context.Context, _ *models.AuditLog) error {
	<-n.release

	return nil
}
