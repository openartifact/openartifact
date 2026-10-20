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

func TestMavenWorkerStop(t *testing.T) {
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

	w := worker.NewMavenMetadataWorker(db, bs, nil, log, nil).WithDebounce(100 * time.Millisecond)
	w.NotifyStale("repo-1", "com.example", "myapp")
	w.Stop()

	time.Sleep(200 * time.Millisecond)

	calls := db.upsertCalls.Load()
	if calls != 0 {
		t.Errorf("expected 0 UpsertRepoMetadata calls after Stop(), got %d", calls)
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

	time.Sleep(200 * time.Millisecond)

	calls := db.upsertCalls.Load()
	if calls != 2 {
		t.Errorf("expected 2 UpsertRepoMetadata calls for different keys, got %d", calls)
	}
}
