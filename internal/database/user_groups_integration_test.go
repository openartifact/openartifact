// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"sync"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
)

// Concurrent SSO logins mapping to a new group must converge on one row, and
// a second admin create of the same name must be a unique violation.
func TestGetOrCreateGroupConcurrentSingleRow(t *testing.T) {
	ctx := context.Background()
	pg := newQuotaTestDB(t, 8)
	name := "sso-race-" + t.Name()

	t.Cleanup(func() {
		if g, err := pg.GetGroupByName(ctx, name); err == nil && g != nil {
			_ = pg.DeleteGroup(ctx, g.ID)
		}
	})

	const workers = 8

	ids := make([]string, workers)
	errs := make([]error, workers)

	var wg sync.WaitGroup

	for i := range workers {
		wg.Go(func() {
			g, err := pg.GetOrCreateGroup(ctx, name)
			if err == nil && g != nil {
				ids[i] = g.ID
			}

			errs[i] = err
		})
	}

	wg.Wait()

	for i, err := range errs {
		if err != nil || ids[i] == "" {
			t.Fatalf("worker %d: id=%q err=%v", i, ids[i], err)
		}

		if ids[i] != ids[0] {
			t.Errorf("worker %d got group %s, want %s", i, ids[i], ids[0])
		}
	}

	var n int
	if err := pg.db.QueryRowContext(ctx, `SELECT count(*) FROM auth_groups WHERE name = $1 AND project_id IS NULL`, name).
		Scan(&n); err != nil ||
		n != 1 {
		t.Fatalf("global groups named %q = %d, err %v; want 1", name, n, err)
	}

	if _, err := pg.CreateGroup(ctx, &models.Group{Name: name}); !IsUniqueViolation(err) {
		t.Errorf("CreateGroup duplicate err = %v, want unique violation", err)
	}
}
