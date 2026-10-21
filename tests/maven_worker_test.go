// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package integration_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/worker"
	"github.com/openartifact/openartifact/pkg/logger"
)

type countingMockDatabase struct {
	*MockDatabase

	upsertCalls atomic.Int64
}

func (c *countingMockDatabase) UpsertRepoMetadata(ctx context.Context, meta *models.RepoMetadata) error {
	c.upsertCalls.Add(1)
	return c.MockDatabase.UpsertRepoMetadata(ctx, meta)
}

func TestMavenWorkerNotifyStale_Debounce(t *testing.T) {
	log, _ := logger.New("info", "json", "stdout")
	db := &countingMockDatabase{MockDatabase: newMockDatabase()}
	bs := newMockBlobStore()

	db.mavenArtifacts["art-1"] = &models.MavenArtifact{
		ID:            "art-1",
		RepositoryID:  "repo-1",
		GroupID:       "com.example",
		ArtifactCoord: "myapp",
		Version:       "1.0.0",
		Extension:     "jar",
		IsSnapshot:    false,
	}

	w := worker.NewMavenMetadataWorker(db, bs, nil, log, nil).WithDebounce(50 * time.Millisecond)

	for range 5 {
		w.NotifyStale("repo-1", "com.example", "myapp")
	}

	waitUntil(t, 5*time.Second, "first UpsertRepoMetadata", func() bool { return db.upsertCalls.Load() >= 1 })
	time.Sleep(150 * time.Millisecond) // ensure no extra debounced runs fire

	calls := db.upsertCalls.Load()
	if calls != 1 {
		t.Errorf("expected 1 UpsertRepoMetadata call after debounce, got %d", calls)
	}
}

// Stop must drain: a regeneration still debouncing when the server shuts down
// runs immediately and completes before Stop returns, instead of being lost.
func TestMavenWorkerStopDrainsPendingRegeneration(t *testing.T) {
	log, _ := logger.New("info", "json", "stdout")
	db := &countingMockDatabase{MockDatabase: newMockDatabase()}
	bs := newMockBlobStore()

	db.mavenArtifacts["art-1"] = &models.MavenArtifact{
		ID:            "art-1",
		RepositoryID:  "repo-1",
		GroupID:       "com.example",
		ArtifactCoord: "myapp",
		Version:       "1.0.0",
	}

	w := worker.NewMavenMetadataWorker(db, bs, nil, log, nil).WithDebounce(time.Hour)
	w.NotifyStale("repo-1", "com.example", "myapp")
	w.Stop()

	if calls := db.upsertCalls.Load(); calls != 1 {
		t.Errorf("expected the pending regeneration to run during Stop(), got %d UpsertRepoMetadata calls", calls)
	}

	// Work offered after Stop is rejected.
	w.NotifyStale("repo-1", "com.example", "other")
	w.Stop()

	if calls := db.upsertCalls.Load(); calls != 1 {
		t.Errorf("expected no regeneration after Stop(), got %d UpsertRepoMetadata calls", calls)
	}
}

func TestMavenWorkerNotifyStale_DifferentKeys(t *testing.T) {
	log, _ := logger.New("info", "json", "stdout")
	db := &countingMockDatabase{MockDatabase: newMockDatabase()}
	bs := newMockBlobStore()

	db.mavenArtifacts["art-1"] = &models.MavenArtifact{
		ID: "art-1", RepositoryID: "repo-1", GroupID: "com.example", ArtifactCoord: "app1", Version: "1.0.0",
	}
	db.mavenArtifacts["art-2"] = &models.MavenArtifact{
		ID: "art-2", RepositoryID: "repo-1", GroupID: "com.example", ArtifactCoord: "app2", Version: "2.0.0",
	}

	w := worker.NewMavenMetadataWorker(db, bs, nil, log, nil).WithDebounce(50 * time.Millisecond)
	w.NotifyStale("repo-1", "com.example", "app1")
	w.NotifyStale("repo-1", "com.example", "app2")

	waitUntil(t, 5*time.Second, "both keys regenerated", func() bool { return db.upsertCalls.Load() >= 2 })
	w.Stop()

	calls := db.upsertCalls.Load()
	if calls != 2 {
		t.Errorf("expected 2 UpsertRepoMetadata calls for different keys, got %d", calls)
	}
}
