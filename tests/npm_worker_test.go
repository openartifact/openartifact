// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package integration_test

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/worker"
	"github.com/openartifact/openartifact/pkg/logger"
)

type npmCountingDB struct {
	*MockDatabase

	updateCalls atomic.Int64
	mu          sync.Mutex
	lastVersion string
}

func (c *npmCountingDB) UpdateNPMPackageLatestVersion(ctx context.Context, packageID, latestVersion string) error {
	c.updateCalls.Add(1)
	c.mu.Lock()
	c.lastVersion = latestVersion
	c.mu.Unlock()

	return c.MockDatabase.UpdateNPMPackageLatestVersion(ctx, packageID, latestVersion)
}

func (c *npmCountingDB) getLastVersion() string {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.lastVersion
}

func TestNPMWorker_NotifyStale_Debounce(t *testing.T) {
	log, _ := logger.New("info", "json", "stdout")
	db := &npmCountingDB{MockDatabase: newMockDatabase()}

	db.npmVersions["pkg-1:1.0.0"] = &models.NPMVersion{
		ID: "v1", PackageID: "pkg-1", Version: "1.0.0", ManifestJSON: []byte(`{}`),
	}

	w := worker.NewNPMMetadataWorker(db, log, nil, nil).WithDebounce(50 * time.Millisecond)

	for range 5 {
		w.NotifyStale("pkg-1")
	}

	waitUntil(t, 5*time.Second, "first update", func() bool { return db.updateCalls.Load() >= 1 })
	time.Sleep(150 * time.Millisecond) // ensure no extra debounced runs fire

	calls := db.updateCalls.Load()
	if calls != 1 {
		t.Errorf("expected 1 UpdateNPMPackageLatestVersion call after debounce, got %d", calls)
	}
}

func TestNPMWorker_LatestVersionRecomputation(t *testing.T) {
	log, _ := logger.New("info", "json", "stdout")
	db := &npmCountingDB{MockDatabase: newMockDatabase()}

	for _, v := range []string{"1.0.0", "1.5.0", "2.0.0"} {
		raw, _ := json.Marshal(map[string]any{"name": "lodash", "version": v})
		db.npmVersions["pkg-1:"+v] = &models.NPMVersion{
			ID:           "v-" + v,
			PackageID:    "pkg-1",
			Version:      v,
			ManifestJSON: raw,
		}
	}

	w := worker.NewNPMMetadataWorker(db, log, nil, nil).WithDebounce(50 * time.Millisecond)
	w.NotifyStale("pkg-1")

	waitUntil(t, 5*time.Second, "latest version update", func() bool { return db.updateCalls.Load() >= 1 })

	if v := db.getLastVersion(); v != "2.0.0" {
		t.Errorf("latest version = %q, want 2.0.0", v)
	}

	if db.updateCalls.Load() != 1 {
		t.Errorf("expected 1 update call, got %d", db.updateCalls.Load())
	}
}
