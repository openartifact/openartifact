// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
)

func newCapProject(t *testing.T, db *PostgresDB, name string) string {
	t.Helper()

	var id string
	if err := db.db.QueryRowContext(context.Background(),
		`INSERT INTO projects (id, name, slug) VALUES (gen_random_uuid(), $1, $1) RETURNING id`, name).Scan(&id); err != nil {
		t.Fatalf("insert project: %v", err)
	}

	return id
}

// Concurrent creations under a cap never overshoot it: the project row lock
// serialises the count and the insert.
func TestCreateProjectRepository_CapIsAtomic(t *testing.T) {
	db := newQuotaTestDB(t, 16)
	ctx := context.Background()
	project := newCapProject(t, db, "capped")

	const attempts, limit = 12, 3

	var wg sync.WaitGroup

	errs := make(chan error, attempts)

	for i := range attempts {
		wg.Go(func() {
			repo := &models.Repository{Name: fmt.Sprintf("capped-%d", i), Format: "npm", RepoType: "hosted", ProjectID: &project}
			errs <- db.CreateProjectRepository(ctx, repo, nil, limit)
		})
	}

	wg.Wait()
	close(errs)

	created, refused := 0, 0

	for err := range errs {
		switch {
		case err == nil:
			created++
		case errors.Is(err, ErrProjectRepoLimit):
			refused++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}

	if created != limit || refused != attempts-limit {
		t.Fatalf("created %d, refused %d; want %d and %d", created, refused, limit, attempts-limit)
	}
}

func TestMoveRepositoryToProject(t *testing.T) {
	db := newQuotaTestDB(t, 4)
	ctx := context.Background()
	from, to := newCapProject(t, db, "from"), newCapProject(t, db, "to")

	repo := &models.Repository{Name: "mv-1", Format: "npm", RepoType: "hosted", ProjectID: &from}
	if err := db.CreateProjectRepository(ctx, repo, nil, 0); err != nil {
		t.Fatal(err)
	}

	other := &models.Repository{Name: "mv-2", Format: "npm", RepoType: "hosted", ProjectID: &to}
	if err := db.CreateProjectRepository(ctx, other, nil, 0); err != nil {
		t.Fatal(err)
	}

	if err := db.MoveRepositoryToProject(ctx, repo.ID, to, 1); !errors.Is(err, ErrProjectRepoLimit) {
		t.Fatalf("move into a full project: %v, want ErrProjectRepoLimit", err)
	}

	if err := db.MoveRepositoryToProject(ctx, repo.ID, to, 2); err != nil {
		t.Fatalf("move: %v", err)
	}

	got, err := db.GetRepositoryByName(ctx, "mv-1")
	if err != nil || got == nil || got.ProjectID == nil || *got.ProjectID != to {
		t.Fatalf("repository after move = %+v, %v", got, err)
	}

	if err := db.MoveRepositoryToProject(ctx, "00000000-0000-4000-8000-000000000999", to, 0); !errors.Is(err, ErrRepositoryNotFound) {
		t.Fatalf("move of a missing repository: %v", err)
	}
}
