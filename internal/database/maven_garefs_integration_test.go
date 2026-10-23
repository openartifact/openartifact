// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"reflect"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
)

func TestListMavenGARefs(t *testing.T) {
	pg := newQuotaTestDB(t, 10)
	ctx := context.Background()
	s := newQuotaSeeder(t, pg, "maven-refs", "maven")
	other := newQuotaSeeder(t, pg, "maven-refs-other", "maven")

	add := func(seed *quotaSeeder, artifact, version, classifier, ext, ts string, build int, deleted bool) {
		t.Helper()

		path := "com/example/" + artifact + "/" + version + "/" + artifact + "-" + version + "-" + classifier + "." + ext + "-" + ts
		blob := seed.blob(1)

		seed.exec(`INSERT INTO maven_artifacts (repository_id, group_id, artifact_id_coord, version, classifier, extension,
		              snapshot_timestamp, snapshot_build_number, is_snapshot, repo_path, blob_id, deleted_at)
		           VALUES ($1, 'com.example', $2, $3, $4, $5, NULLIF($6, ''), $7, $6 <> '', $8, $9, CASE WHEN $10 THEN now() END)`,
			seed.repo, artifact, version, classifier, ext, ts, build, path, blob, deleted)
	}

	add(s, "app", "1.0", "", "jar", "", 0, false)
	add(s, "app", "1.0", "", "pom", "", 0, false)
	add(s, "app", "1.0", "sources", "jar", "", 0, false)
	add(s, "app", "1.10", "", "jar", "", 0, false)
	add(s, "app", "2.0", "", "jar", "", 0, true) // trashed: invisible
	add(s, "app", "3.0-SNAPSHOT", "", "jar", "20260102.030405", 2, false)
	add(s, "app", "3.0-SNAPSHOT", "", "pom", "20260102.030405", 2, false)
	add(s, "app", "3.0-SNAPSHOT", "", "jar", "20260101.000000", 1, false)
	add(s, "app", "4.0-SNAPSHOT", "", "jar", "", 0, false) // NULL timestamp -> ""
	add(s, "other", "9.9", "", "jar", "", 0, false)        // other artifact
	add(other, "app", "7.0", "", "jar", "", 0, false)      // other repository

	got, err := pg.ListMavenGARefs(ctx, s.repo, "com.example", "app")
	if err != nil {
		t.Fatalf("ListMavenGARefs: %v", err)
	}

	wantVersions := []string{"1.0", "1.10", "3.0-SNAPSHOT", "4.0-SNAPSHOT"}
	if !reflect.DeepEqual(got.Versions, wantVersions) {
		t.Errorf("Versions = %q, want %q", got.Versions, wantVersions)
	}

	wantSnapshots := []models.MavenSnapshotRef{
		{Version: "3.0-SNAPSHOT", Extension: "jar", Timestamp: "20260101.000000", BuildNumber: 1},
		{Version: "3.0-SNAPSHOT", Extension: "jar", Timestamp: "20260102.030405", BuildNumber: 2},
		{Version: "3.0-SNAPSHOT", Extension: "pom", Timestamp: "20260102.030405", BuildNumber: 2},
		{Version: "4.0-SNAPSHOT", Extension: "jar"},
	}
	if !reflect.DeepEqual(got.Snapshots, wantSnapshots) {
		t.Errorf("Snapshots = %+v, want %+v", got.Snapshots, wantSnapshots)
	}

	empty, err := pg.ListMavenGARefs(ctx, s.repo, "com.example", "missing")
	if err != nil {
		t.Fatalf("ListMavenGARefs(missing): %v", err)
	}

	if len(empty.Versions) != 0 || len(empty.Snapshots) != 0 {
		t.Errorf("missing artifact returned %+v, want empty", empty)
	}
}
