// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package integration_test

import (
	"context"
	"encoding/json"
	"strconv"
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

// gatedNPMDB serves versions from a concurrency-safe store and blocks the first
// GetNPMVersionStates (after taking its snapshot) until released, so a
// regeneration is deterministically in flight while publishes keep arriving.
type gatedNPMDB struct {
	*npmCountingDB

	vmu      sync.Mutex
	versions []*models.NPMVersion

	release chan struct{}
	reads   atomic.Int64
}

func (g *gatedNPMDB) publish(pkgID, version string) {
	g.vmu.Lock()
	defer g.vmu.Unlock()

	g.versions = append(g.versions, &models.NPMVersion{
		ID: "v-" + version, PackageID: pkgID, Version: version, ManifestJSON: []byte(`{}`),
	})
}

func (g *gatedNPMDB) GetNPMVersionStates(_ context.Context, pkgID string) ([]models.NPMVersionState, error) {
	g.vmu.Lock()

	var snapshot []*models.NPMVersion

	for _, v := range g.versions {
		if v.PackageID == pkgID {
			snapshot = append(snapshot, v)
		}
	}

	g.vmu.Unlock()

	if g.reads.Add(1) == 1 {
		<-g.release
	}

	return npmVersionStates(snapshot), nil
}

// Publishing storms must collapse into at most one running plus one rerun, and
// the rerun must see the newest version.
func TestNPMWorker_ConcurrentNotifyCoalesces(t *testing.T) {
	const publishes = 200

	log, _ := logger.New("info", "json", "stdout")
	db := &gatedNPMDB{
		npmCountingDB: &npmCountingDB{MockDatabase: newMockDatabase()},
		release:       make(chan struct{}),
	}

	w := worker.NewNPMMetadataWorker(db, log, nil, nil).WithDebounce(5 * time.Millisecond)

	db.publish("pkg-1", "1.0.0")
	w.NotifyStale("pkg-1")

	waitUntil(t, 5*time.Second, "first regeneration to start", func() bool { return db.reads.Load() >= 1 })

	var wg sync.WaitGroup

	for i := 1; i <= publishes; i++ {
		wg.Go(func() {
			db.publish("pkg-1", "1.0."+strconv.Itoa(i))
			w.NotifyStale("pkg-1")
		})
	}

	wg.Wait()
	close(db.release)

	newest := "1.0." + strconv.Itoa(publishes)
	waitUntil(t, 5*time.Second, "latest version to include the newest publish", func() bool { return db.getLastVersion() == newest })

	w.Stop()

	if calls := db.updateCalls.Load(); calls > 2 {
		t.Errorf("UpdateNPMPackageLatestVersion called %d times for %d publishes, want <= 2", calls, publishes+1)
	}

	if got := db.getLastVersion(); got != newest {
		t.Errorf("final latest version = %q, want %q", got, newest)
	}
}
