// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
)

func TestDependencyTrackOverrideRoundTripAndCascade(t *testing.T) {
	pg := newQuotaTestDB(t, 4)
	ctx := context.Background()
	s := newQuotaSeeder(t, pg, "dt-override", "npm")

	if got, err := pg.GetRepositoryDependencyTrack(ctx, s.repo); err != nil || got != nil {
		t.Fatalf("no override: got %v, %v; want nil, nil", got, err)
	}

	yes := true
	row := &models.RepositoryDependencyTrack{
		RepositoryID: s.repo, Enabled: true, BaseURL: "https://dt.example", APIKey: "enc:ciphertext",
		ProjectName: "x/{name}", AutoPush: &yes,
	}
	if err := pg.UpsertRepositoryDependencyTrack(ctx, row); err != nil {
		t.Fatal(err)
	}

	got, err := pg.GetRepositoryDependencyTrack(ctx, s.repo)
	if err != nil || got == nil {
		t.Fatalf("get override: %v, %v", got, err)
	}

	if got.BaseURL != "https://dt.example" || got.APIKey != "enc:ciphertext" || got.ProjectName != "x/{name}" ||
		got.AutoPush == nil || !*got.AutoPush || got.PushVEX != nil {
		t.Fatalf("override = %+v; want stored values with push_vex NULL (inherit)", got)
	}

	// Upsert replaces, including turning a boolean back to inherit.
	row.AutoPush, row.Enabled = nil, false
	if err := pg.UpsertRepositoryDependencyTrack(ctx, row); err != nil {
		t.Fatal(err)
	}

	if got, _ = pg.GetRepositoryDependencyTrack(ctx, s.repo); got.Enabled || got.AutoPush != nil {
		t.Fatalf("after upsert = %+v; want disabled, auto_push NULL", got)
	}

	if existed, err := pg.DeleteRepositoryDependencyTrack(ctx, s.repo); err != nil || !existed {
		t.Fatalf("delete = %v, %v; want true", existed, err)
	}

	if existed, _ := pg.DeleteRepositoryDependencyTrack(ctx, s.repo); existed {
		t.Fatal("second delete reported an existing override")
	}
}

func TestDependencyTrackRowsCascadeWithRepository(t *testing.T) {
	pg := newQuotaTestDB(t, 4)
	ctx := context.Background()
	s := newQuotaSeeder(t, pg, "dt-cascade", "npm")
	row := &models.RepositoryDependencyTrack{RepositoryID: s.repo, Enabled: true, BaseURL: "https://dt.example"}

	if err := pg.UpsertRepositoryDependencyTrack(ctx, row); err != nil {
		t.Fatal(err)
	}

	if err := pg.UpsertDependencyTrackPush(ctx, &models.DependencyTrackPush{RepositoryID: s.repo, ArtifactID: "a@1", State: models.DTrackPushQueued}); err != nil {
		t.Fatal(err)
	}

	s.exec(`DELETE FROM repositories WHERE id = $1`, s.repo)

	if got, _ := pg.GetRepositoryDependencyTrack(ctx, s.repo); got != nil {
		t.Fatal("override survived its repository")
	}

	if got, _ := pg.GetDependencyTrackPush(ctx, s.repo, "a@1"); got != nil {
		t.Fatal("push state survived its repository")
	}
}

func TestDependencyTrackPushStateUpsertListAndConstraint(t *testing.T) {
	pg := newQuotaTestDB(t, 4)
	ctx := context.Background()
	s := newQuotaSeeder(t, pg, "dt-pushes", "npm")

	now := time.Now().UTC().Truncate(time.Second)
	p := &models.DependencyTrackPush{
		RepositoryID: s.repo, ArtifactID: "a@1", State: models.DTrackPushDone, ProjectUUID: "uuid-1",
		ProjectName: "r/a", ProjectVersion: "1", Components: 7, VEXStatements: 2, VEXApplied: 1, BOMPushedAt: &now,
	}
	if err := pg.UpsertDependencyTrackPush(ctx, p); err != nil {
		t.Fatal(err)
	}

	got, err := pg.GetDependencyTrackPush(ctx, s.repo, "a@1")
	if err != nil || got == nil {
		t.Fatalf("get push: %v, %v", got, err)
	}

	if got.Components != 7 || got.VEXApplied != 1 || got.ProjectUUID != "uuid-1" || got.BOMPushedAt == nil || got.VEXPushedAt != nil {
		t.Fatalf("push = %+v", got)
	}

	// A later push of the same artifact replaces the row; another artifact is listed first (newest update).
	p.State, p.Detail = models.DTrackPushFailed, "boom"
	if err := pg.UpsertDependencyTrackPush(ctx, p); err != nil {
		t.Fatal(err)
	}

	if err := pg.UpsertDependencyTrackPush(ctx, &models.DependencyTrackPush{RepositoryID: s.repo, ArtifactID: "b@2", State: models.DTrackPushRunning}); err != nil {
		t.Fatal(err)
	}

	list, err := pg.ListDependencyTrackPushes(ctx, s.repo, 10)
	if err != nil || len(list) != 2 {
		t.Fatalf("list = %d rows, %v; want 2", len(list), err)
	}

	if list[0].ArtifactID != "b@2" || list[1].State != models.DTrackPushFailed || list[1].Detail != "boom" {
		t.Fatalf("list = %+v / %+v; want newest first with the replaced state", list[0], list[1])
	}

	if one, _ := pg.ListDependencyTrackPushes(ctx, s.repo, 1); len(one) != 1 {
		t.Fatalf("limit 1 returned %d rows", len(one))
	}

	if err := pg.UpsertDependencyTrackPush(ctx, &models.DependencyTrackPush{RepositoryID: s.repo, ArtifactID: "c", State: "bogus"}); err == nil {
		t.Fatal("an unknown state was accepted")
	}
}

func TestCreateSystemSettingIfAbsentKeepsFirstWriter(t *testing.T) {
	pg := newQuotaTestDB(t, 4)
	ctx := context.Background()

	if ok, err := pg.CreateSystemSettingIfAbsent(ctx, "attestation.private_key", "first"); err != nil || !ok {
		t.Fatalf("first create = %v, %v; want true", ok, err)
	}

	if ok, err := pg.CreateSystemSettingIfAbsent(ctx, "attestation.private_key", "second"); err != nil || ok {
		t.Fatalf("second create = %v, %v; want false", ok, err)
	}

	if v, _ := pg.GetSystemSetting(ctx, "attestation.private_key"); v != "first" {
		t.Fatalf("stored value = %q; want first", v)
	}
}

func TestRepositoryAttestationAttachDefaultsOffAndCascades(t *testing.T) {
	pg := newQuotaTestDB(t, 4)
	ctx := context.Background()
	s := newQuotaSeeder(t, pg, "attach-switch", "docker")

	if on, err := pg.GetRepositoryAttestationAttach(ctx, s.repo); err != nil || on {
		t.Fatalf("default = %v, %v; want false, nil", on, err)
	}

	for _, want := range []bool{true, false, true} {
		if err := pg.SetRepositoryAttestationAttach(ctx, s.repo, want); err != nil {
			t.Fatal(err)
		}

		if on, err := pg.GetRepositoryAttestationAttach(ctx, s.repo); err != nil || on != want {
			t.Fatalf("after set(%v) = %v, %v", want, on, err)
		}
	}

	if _, err := pg.pool.Exec(ctx, `DELETE FROM repositories WHERE id = $1`, s.repo); err != nil {
		t.Fatal(err)
	}

	var n int
	if err := pg.pool.QueryRow(ctx, `SELECT count(*) FROM repository_attestation_attach WHERE repository_id = $1`, s.repo).Scan(&n); err != nil || n != 0 {
		t.Fatalf("setting rows after the repository was deleted = %d, %v; want 0", n, err)
	}
}
