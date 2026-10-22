// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package integration_test

import (
	"context"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
)

func (e *cleanupQuotaEnv) projectRepo(projectID, name, format string) string {
	e.t.Helper()

	repo := &models.Repository{Name: name, Format: format, RepoType: "hosted", ProjectID: &projectID}
	if err := e.pg.CreateRepository(context.Background(), repo); err != nil {
		e.t.Fatalf("CreateRepository: %v", err)
	}

	return repo.ID
}

func (e *cleanupQuotaEnv) versions(repoID string) int {
	e.t.Helper()

	var n int
	if err := e.pg.GetDB().QueryRowContext(context.Background(), `SELECT COUNT(*) FROM helm_charts WHERE repository_id = $1`, repoID).Scan(&n); err != nil {
		e.t.Fatalf("count versions: %v", err)
	}

	return n
}

// A project default policy cleans every hosted repository of the project that
// has no enabled policy of its own, releases the usage on both levels, and
// leaves repositories with their own enabled policy to that policy.
func TestProjectCleanupDefaultAppliesAndRepoPolicyOverrides(t *testing.T) {
	e := newCleanupQuotaEnv(t)
	ctx := context.Background()

	project := e.queryID(`INSERT INTO projects (id, name, slug) VALUES (gen_random_uuid(), 'cleanproj', 'cleanproj') RETURNING id`)
	plain := e.projectRepo(project, "plain", "helm")
	custom := e.projectRepo(project, "custom", "helm")
	other := e.projectRepo(models.SystemProjectID, "elsewhere", "helm")

	for _, repo := range []string{plain, custom, other} {
		e.pkg(repo, "helm", "pkg", "1.0.0", 100, 30)
		e.pkg(repo, "helm", "pkg", "2.0.0", 100, 20)
		e.pkg(repo, "helm", "pkg", "3.0.0", 100, 10)
	}

	if _, err := e.pg.SetProjectQuota(ctx, project, 10_000, 0, 80); err != nil {
		t.Fatalf("SetProjectQuota: %v", err)
	}

	own := 10
	if err := e.pg.CreateCleanupPolicy(ctx, &models.CleanupPolicy{RepositoryID: custom, Name: "own", Enabled: true, KeepLastN: &own}); err != nil {
		t.Fatalf("CreateCleanupPolicy: %v", err)
	}

	keep := 1
	def := &models.CleanupPolicy{ProjectID: project, Name: "default", Enabled: true, KeepLastN: &keep}

	if err := e.pg.CreateCleanupPolicy(ctx, def); err != nil {
		t.Fatalf("CreateCleanupPolicy(project): %v", err)
	}

	if err := e.worker.TriggerPolicy(ctx, def.ID); err != nil {
		t.Fatalf("TriggerPolicy: %v", err)
	}

	waitPolicyRun(t, e, def.ID)

	if n := e.versions(plain); n != 1 {
		t.Errorf("repository without its own policy keeps %d versions, want 1", n)
	}

	if n := e.versions(custom); n != 3 {
		t.Errorf("repository with its own enabled policy keeps %d versions, want all 3 (it overrides the default)", n)
	}

	if n := e.versions(other); n != 3 {
		t.Errorf("repository of another project keeps %d versions, want all 3", n)
	}

	plainSize, plainCount := e.usage(plain)
	customSize, customCount := e.usage(custom)

	after, afterCount := projectSize(t, e)
	if after != plainSize+customSize || afterCount != plainCount+customCount || plainCount != 1 {
		t.Errorf("project counters = %d/%d, want the repositories' sum %d/%d (plain keeps one version)", after, afterCount, plainSize+customSize, plainCount+customCount)
	}

	if _, err := e.pg.ReconcileAllStorageUsage(ctx); err != nil {
		t.Fatalf("ReconcileAllStorageUsage: %v", err)
	}

	if again, _ := projectSize(t, e); again != after {
		t.Errorf("reconcile changed the project counter %d -> %d: the incremental release drifted", after, again)
	}
}

func projectSize(t *testing.T, e *cleanupQuotaEnv) (int64, int64) {
	t.Helper()

	var size, count int64

	err := e.pg.GetDB().QueryRowContext(context.Background(),
		`SELECT current_size_bytes, current_artifact_count FROM project_quotas q JOIN projects p ON p.id = q.project_id WHERE p.slug = 'cleanproj'`,
	).Scan(&size, &count)
	if err != nil {
		t.Fatalf("read project counters: %v", err)
	}

	return size, count
}

func waitPolicyRun(t *testing.T, e *cleanupQuotaEnv, policyID string) {
	t.Helper()

	deadline := time.Now().Add(30 * time.Second)

	for time.Now().Before(deadline) {
		got, err := e.pg.GetCleanupPolicy(context.Background(), policyID)
		if err != nil || got == nil {
			t.Fatalf("GetCleanupPolicy = %v, %v", got, err)
		}

		if got.LastRunAt != nil {
			return
		}

		time.Sleep(50 * time.Millisecond)
	}

	t.Fatal("cleanup policy did not finish within 30s")
}
