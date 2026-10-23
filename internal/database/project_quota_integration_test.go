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
	"sync/atomic"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
)

// quotaProjectRepo creates a hosted repository in project with an optional own
// size limit (0 = unlimited) and returns its ID.
func quotaProjectRepo(t *testing.T, pg *PostgresDB, name, projectID string, maxBytes int64) string {
	t.Helper()

	repo := &models.Repository{Name: name, Format: "raw", RepoType: "hosted", ProjectID: &projectID}
	if err := pg.CreateRepository(context.Background(), repo); err != nil {
		t.Fatalf("CreateRepository: %v", err)
	}

	if maxBytes > 0 {
		q, err := pg.GetOrCreateStorageQuota(context.Background(), repo.ID)
		if err != nil {
			t.Fatalf("GetOrCreateStorageQuota: %v", err)
		}

		q.MaxSizeBytes = maxBytes
		if err := pg.UpdateStorageQuota(context.Background(), q); err != nil {
			t.Fatalf("UpdateStorageQuota: %v", err)
		}
	}

	return repo.ID
}

func projectCounters(t *testing.T, pg *PostgresDB, projectID string) (int64, int64) {
	t.Helper()

	q, err := pg.GetProjectQuota(context.Background(), projectID)
	if err != nil {
		t.Fatalf("GetProjectQuota: %v", err)
	}

	return q.CurrentSizeBytes, q.CurrentArtifactCount
}

func mustReserve(t *testing.T, pg *PostgresDB, repoID string, size int64) bool {
	t.Helper()

	ok, err := pg.ReserveStorageUsage(context.Background(), repoID, size, 1)
	if err != nil {
		t.Fatalf("ReserveStorageUsage: %v", err)
	}

	return ok
}

func setProjectLimit(t *testing.T, pg *PostgresDB, projectID string, maxBytes, maxCount int64) {
	t.Helper()

	if _, err := pg.SetProjectQuota(context.Background(), projectID, maxBytes, maxCount, 80); err != nil {
		t.Fatalf("SetProjectQuota: %v", err)
	}
}

// Two repositories each under their own limit but over the project's: the
// second reservation fails and rolls the project counter back; a release gives
// both levels their usage back.
func TestReserveStorageUsage_ProjectCeiling(t *testing.T) {
	pg := newQuotaTestDB(t, 16)
	project := newCapProject(t, pg, "ceiling")
	repoA := quotaProjectRepo(t, pg, "ceil-a", project, 100)
	repoB := quotaProjectRepo(t, pg, "ceil-b", project, 100)

	setProjectLimit(t, pg, project, 150, 0)

	if !mustReserve(t, pg, repoA, 100) {
		t.Fatal("first reservation (100 of 150) refused")
	}

	if mustReserve(t, pg, repoB, 100) {
		t.Fatal("second reservation fits repo B's limit but passes the project's 150: must be refused")
	}

	if size, count, ok := quotaUsage(t, pg, repoB); ok && (size != 0 || count != 0) {
		t.Fatalf("refused reservation left repo B usage %d/%d", size, count)
	}

	if size, count := projectCounters(t, pg, project); size != 100 || count != 1 {
		t.Fatalf("project counters = %d/%d after a refused reservation, want 100/1", size, count)
	}

	if !mustReserve(t, pg, repoB, 50) {
		t.Fatal("50 more bytes (150 of 150) refused")
	}

	// The repository's own limit still applies under a roomy project.
	if mustReserve(t, pg, repoA, 1) {
		t.Fatal("repo A is at its 100 byte limit: reservation must be refused")
	}

	if size, count := projectCounters(t, pg, project); size != 150 || count != 2 {
		t.Fatalf("project counters = %d/%d, want 150/2 (a repository-level refusal rolls back the project)", size, count)
	}

	if err := pg.DecrementStorageUsage(context.Background(), repoA, 100, 1); err != nil {
		t.Fatalf("DecrementStorageUsage: %v", err)
	}

	if size, count := projectCounters(t, pg, project); size != 50 || count != 1 {
		t.Fatalf("project counters = %d/%d after release, want 50/1", size, count)
	}

	if size, _, _ := quotaUsage(t, pg, repoA); size != 0 {
		t.Fatalf("repo A usage = %d after release, want 0", size)
	}

	if !mustReserve(t, pg, repoA, 100) {
		t.Fatal("released room not reusable")
	}
}

// Concurrent reservations over several repositories never pass the project
// ceiling, and the project counter equals the sum of the repository counters.
func TestReserveStorageUsage_ProjectCeilingUnderContention(t *testing.T) {
	pg := newQuotaTestDB(t, 80)
	project := newCapProject(t, pg, "contended")

	const (
		repos      = 4
		goroutines = 64
		size       = int64(100)
		fit        = 10
	)

	ids := make([]string, repos)
	for i := range ids {
		ids[i] = quotaProjectRepo(t, pg, fmt.Sprintf("cont-%d", i), project, 0)
	}

	setProjectLimit(t, pg, project, fit*size, 0)

	var (
		ok    atomic.Int64
		wg    sync.WaitGroup
		start = make(chan struct{})
	)

	for i := range goroutines {
		wg.Go(func() {
			<-start

			got, err := pg.ReserveStorageUsage(context.Background(), ids[i%repos], size, 1)
			if err != nil {
				t.Errorf("ReserveStorageUsage: %v", err)
				return
			}

			if got {
				ok.Add(1)
			}
		})
	}

	close(start)
	wg.Wait()

	if ok.Load() != fit {
		t.Fatalf("%d reservations succeeded, want exactly %d", ok.Load(), fit)
	}

	var sum int64

	for _, id := range ids {
		s, _, _ := quotaUsage(t, pg, id)
		sum += s
	}

	if psize, _ := projectCounters(t, pg, project); psize != fit*size || sum != fit*size {
		t.Fatalf("project counter %d, repository sum %d, want both %d", psize, sum, fit*size)
	}
}

// A project without a limit keeps no counters: reservations and releases only
// touch the repository row.
func TestReserveStorageUsage_UnlimitedProjectIsNotTracked(t *testing.T) {
	pg := newQuotaTestDB(t, 8)
	project := newCapProject(t, pg, "unlimited")
	repo := quotaProjectRepo(t, pg, "unl-a", project, 0)

	if !mustReserve(t, pg, repo, 500) {
		t.Fatal("reservation in an unlimited project refused")
	}

	var rows int
	if err := pg.GetDB().QueryRowContext(context.Background(), `SELECT COUNT(*) FROM project_quotas WHERE project_id = $1`, project).Scan(&rows); err != nil {
		t.Fatal(err)
	}

	if rows != 0 {
		t.Fatalf("a project quota row exists for a project that never had a limit")
	}

	// A row with zero limits (limit removed again) is not maintained either.
	setProjectLimit(t, pg, project, 1000, 0)
	setProjectLimit(t, pg, project, 0, 0)

	if !mustReserve(t, pg, repo, 500) {
		t.Fatal("reservation refused after the limit was removed")
	}

	if size, count := projectCounters(t, pg, project); size != 0 || count != 0 {
		t.Fatalf("unlimited project counters = %d/%d, want 0/0", size, count)
	}
}

// Enabling a limit starts from what the repositories already hold.
func TestSetProjectQuota_InitialisesFromRepositories(t *testing.T) {
	pg := newQuotaTestDB(t, 8)
	project := newCapProject(t, pg, "enable")
	a := quotaProjectRepo(t, pg, "en-a", project, 0)
	b := quotaProjectRepo(t, pg, "en-b", project, 0)

	mustReserve(t, pg, a, 300)
	mustReserve(t, pg, b, 200)

	setProjectLimit(t, pg, project, 600, 0)

	if size, count := projectCounters(t, pg, project); size != 500 || count != 2 {
		t.Fatalf("counters after enabling = %d/%d, want 500/2", size, count)
	}

	if mustReserve(t, pg, a, 101) {
		t.Fatal("601 of 600 bytes accepted")
	}

	usage, err := pg.GetProjectUsage(context.Background(), project)
	if err != nil {
		t.Fatal(err)
	}

	if usage.SizeBytes != 500 || usage.ArtifactCount != 2 || len(usage.Repositories) != 2 || usage.Repositories[0].Name != "en-a" {
		t.Fatalf("usage = %+v, want 500 bytes / 2 artifacts, repositories largest first", usage)
	}

	if _, err := pg.SetProjectQuota(context.Background(), "00000000-0000-4000-8000-0000000000ff", 1, 0, 80); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("SetProjectQuota on an unknown project = %v, want ErrProjectNotFound", err)
	}
}

// Reconcile rewrites the project counters from the repositories.
func TestReconcileStorageUsage_RepairsProjectDrift(t *testing.T) {
	pg := newQuotaTestDB(t, 8)
	ctx := context.Background()
	project := newCapProject(t, pg, "drift")
	repo := quotaProjectRepo(t, pg, "drift-a", project, 0)

	setProjectLimit(t, pg, project, 1000, 0)
	mustReserve(t, pg, repo, 100)

	if _, err := pg.GetDB().ExecContext(ctx, `UPDATE project_quotas SET current_size_bytes = 999, current_artifact_count = 42 WHERE project_id = $1`, project); err != nil {
		t.Fatal(err)
	}

	// No package rows exist and the reservation is abandoned (older than any
	// upload can take), so the repository recount is 0 and the project follows.
	if _, err := pg.GetDB().ExecContext(ctx, `UPDATE quota_pending SET created_at = now() - interval '2 hours' WHERE repository_id = $1`, repo); err != nil {
		t.Fatal(err)
	}

	if _, err := pg.ReconcileStorageUsage(ctx, repo); err != nil {
		t.Fatalf("ReconcileStorageUsage: %v", err)
	}

	if size, count := projectCounters(t, pg, project); size != 0 || count != 0 {
		t.Fatalf("project counters = %d/%d after a repository reconcile, want 0/0", size, count)
	}

	if _, err := pg.GetDB().ExecContext(ctx, `UPDATE project_quotas SET current_size_bytes = 7 WHERE project_id = $1`, project); err != nil {
		t.Fatal(err)
	}

	if n, err := pg.ReconcileAllStorageUsage(ctx); err != nil || n < 1 {
		t.Fatalf("ReconcileAllStorageUsage = %d, %v; want at least the project counter corrected", n, err)
	}

	if size, _ := projectCounters(t, pg, project); size != 0 {
		t.Fatalf("project size = %d after reconcile all, want 0", size)
	}
}

// A transfer moves the repository's usage from one ceiling to the other, and
// is refused (nothing moves) when the target cannot take it.
func TestMoveRepositoryToProject_MovesUsage(t *testing.T) {
	pg := newQuotaTestDB(t, 8)
	ctx := context.Background()
	from := newCapProject(t, pg, "xfer-from")
	to := newCapProject(t, pg, "xfer-to")
	toOther := newCapProject(t, pg, "xfer-open")

	moving := quotaProjectRepo(t, pg, "xfer-moving", from, 0)
	resident := quotaProjectRepo(t, pg, "xfer-resident", to, 0)

	setProjectLimit(t, pg, from, 1000, 0)
	setProjectLimit(t, pg, to, 120, 0)

	mustReserve(t, pg, moving, 100)
	mustReserve(t, pg, resident, 50)

	if err := pg.MoveRepositoryToProject(ctx, moving, to, 0); !errors.Is(err, ErrProjectQuotaExceeded) {
		t.Fatalf("move into a full project = %v, want ErrProjectQuotaExceeded", err)
	}

	if size, _ := projectCounters(t, pg, from); size != 100 {
		t.Fatalf("source counter = %d after a refused move, want 100", size)
	}

	var owner string
	if err := pg.GetDB().QueryRowContext(ctx, `SELECT project_id FROM repositories WHERE id = $1`, moving).Scan(&owner); err != nil || owner != from {
		t.Fatalf("repository project = %q, %v after a refused move, want %s", owner, err, from)
	}

	setProjectLimit(t, pg, to, 200, 0)

	if err := pg.MoveRepositoryToProject(ctx, moving, to, 0); err != nil {
		t.Fatalf("MoveRepositoryToProject: %v", err)
	}

	if fs, fc := projectCounters(t, pg, from); fs != 0 || fc != 0 {
		t.Fatalf("source counters = %d/%d, want 0/0", fs, fc)
	}

	if ts, tc := projectCounters(t, pg, to); ts != 150 || tc != 2 {
		t.Fatalf("target counters = %d/%d, want 150/2", ts, tc)
	}

	// Into a project with no ceiling: the source gives the usage up, nothing is tracked.
	if err := pg.MoveRepositoryToProject(ctx, moving, toOther, 0); err != nil {
		t.Fatalf("move to unlimited project: %v", err)
	}

	if ts, _ := projectCounters(t, pg, to); ts != 50 {
		t.Fatalf("former target counter = %d, want 50", ts)
	}
}

// Reservations, releases and transfers in both directions run together without
// a lock cycle (the lock order is project, then repository) or an error.
func TestProjectQuota_TransferRacesReservations(t *testing.T) {
	pg := newQuotaTestDB(t, 40)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)

	defer cancel()

	p1 := newCapProject(t, pg, "race-1")
	p2 := newCapProject(t, pg, "race-2")
	a := quotaProjectRepo(t, pg, "race-a", p1, 0)
	b := quotaProjectRepo(t, pg, "race-b", p2, 0)

	setProjectLimit(t, pg, p1, 1<<40, 0)
	setProjectLimit(t, pg, p2, 1<<40, 0)

	var wg sync.WaitGroup

	for _, repo := range []string{a, b} {
		for range 4 {
			wg.Go(func() {
				for range 25 {
					if ok, err := pg.ReserveStorageUsage(ctx, repo, 10, 1); err != nil || !ok {
						t.Errorf("reserve: ok=%v err=%v", ok, err)
						return
					}

					if err := pg.DecrementStorageUsage(ctx, repo, 10, 1); err != nil {
						t.Errorf("release: %v", err)
						return
					}
				}
			})
		}
	}

	for _, mv := range []struct{ repo, first, second string }{{a, p2, p1}, {b, p1, p2}} {
		wg.Go(func() {
			for i := range 20 {
				target := mv.first
				if i%2 == 1 {
					target = mv.second
				}

				if err := pg.MoveRepositoryToProject(ctx, mv.repo, target, 0); err != nil {
					t.Errorf("move: %v", err)
					return
				}
			}
		})
	}

	wg.Wait()
}

// A cleanup policy belongs to exactly one scope, and the validated constraint
// and index from the migration are in place.
func TestMigration027_CleanupPolicyScope(t *testing.T) {
	pg := newQuotaTestDB(t, 4)
	ctx := context.Background()
	project := newCapProject(t, pg, "scope")
	repo := quotaProjectRepo(t, pg, "scope-a", project, 0)

	for name, args := range map[string][2]any{
		"no scope":    {nil, nil},
		"both scopes": {repo, project},
	} {
		if _, err := pg.GetDB().ExecContext(ctx,
			`INSERT INTO cleanup_policies (name, repository_id, project_id, keep_last_n) VALUES ('x', $1, $2, 1)`, args[0], args[1]); err == nil {
			t.Errorf("%s: insert accepted, want chk_cleanup_policy_scope to refuse it", name)
		}
	}

	var valid bool
	if err := pg.GetDB().QueryRowContext(ctx, `SELECT convalidated FROM pg_constraint WHERE conname = 'chk_cleanup_policy_scope'`).Scan(&valid); err != nil || !valid {
		t.Fatalf("chk_cleanup_policy_scope validated = %v, err = %v", valid, err)
	}

	var indexValid bool
	if err := pg.GetDB().QueryRowContext(ctx, `SELECT indisvalid FROM pg_index WHERE indexrelid = 'idx_cleanup_policies_project'::regclass`).Scan(&indexValid); err != nil || !indexValid {
		t.Fatalf("idx_cleanup_policies_project valid = %v, err = %v", indexValid, err)
	}
}
