// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package integration_test

import (
	"maps"
	"net/http"
	"testing"
)

// tnhDistTags reads the dist-tag rows of a package straight from the table, so
// rows hidden from every read path (tags of removed versions) are visible too.
func tnhDistTags(t *testing.T, repoID, pkgName string) map[string]string {
	t.Helper()

	rows, err := harness.DB.QueryContext(t.Context(),
		`SELECT d.tag, d.version FROM package_tags d JOIN packages p ON p.id = d.package_id
		 WHERE p.repository_id = $1 AND p.name = $2`, repoID, pkgName)
	if err != nil {
		t.Fatalf("query package_tags: %v", err)
	}
	defer rows.Close()

	tags := map[string]string{}

	for rows.Next() {
		var tag, version string
		if err := rows.Scan(&tag, &version); err != nil {
			t.Fatalf("scan package_tags: %v", err)
		}

		tags[tag] = version
	}

	if err := rows.Err(); err != nil {
		t.Fatalf("iterate package_tags: %v", err)
	}

	return tags
}

// With trash_retention_days = 0 an unpublish is permanent. The dist-tags of
// the removed version must go with it instead of lingering as orphan rows.
func TestTrashNPMHard_UnpublishRemovesDistTagsOfRemovedVersion(t *testing.T) {
	e := tprNewEnv(t, "tnh-npm", "npm")

	if _, err := harness.DB.ExecContext(
		t.Context(),
		`UPDATE repositories SET trash_retention_days = 0 WHERE id = $1`,
		e.repo.ID,
	); err != nil {
		t.Fatalf("disable the trash: %v", err)
	}

	v1 := tprNPMRelease(t, "tnh-lib", "1.0.0")
	v2 := tprNPMRelease(t, "tnh-lib", "2.0.0")

	e.publishNPM(v1)
	e.publishNPM(v2)

	// beta -> 2.0.0 (the version to be removed), stable -> 1.0.0.
	e.expect(e.do(http.MethodPut, e.npmURL("-", "package", "tnh-lib", "dist-tags", "beta"), tprDev, []byte(`"2.0.0"`)), http.StatusOK)
	e.expect(e.do(http.MethodPut, e.npmURL("-", "package", "tnh-lib", "dist-tags", "stable"), tprDev, []byte(`"1.0.0"`)), http.StatusOK)

	before := tnhDistTags(t, e.repo.ID, "tnh-lib")
	if before["beta"] != "2.0.0" || before["stable"] != "1.0.0" || before["latest"] != "2.0.0" {
		t.Fatalf("dist-tags before the unpublish = %v, want beta=2.0.0 stable=1.0.0 latest=2.0.0", before)
	}

	// Partial unpublish of 2.0.0; the client moves latest to 1.0.0.
	e.unpublishNPMVersion(v2, v1)

	e.assertNPMGone(v2)
	e.assertNPMServed(v1)

	if left := e.trashItems(); len(left) != 0 {
		t.Fatalf("a retention-0 unpublish left %+v in the trash", left)
	}

	got := tnhDistTags(t, e.repo.ID, "tnh-lib")
	if _, orphan := got["beta"]; orphan {
		t.Errorf("dist-tag beta -> %s is still in package_tags after the version was permanently removed: %v", got["beta"], got)
	}

	want := map[string]string{"stable": "1.0.0", "latest": "1.0.0"}
	if !maps.Equal(got, want) {
		t.Errorf("package_tags = %v, want %v", got, want)
	}

	if _, p := e.packument("tnh-lib"); !maps.Equal(p.tags, want) {
		t.Errorf("packument dist-tags = %v, want %v", p.tags, want)
	}
}
