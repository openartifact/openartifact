// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"testing"
	"time"
)

// TestDockerBlobQuarantined: a blob of the repository is quarantined when a live
// manifest referencing it is, by the id of a tag pointing at it, its own id or
// its digest; a blob only a clean image references, a trashed manifest and the
// same digest in another repository do not count.
func TestDockerBlobQuarantined(t *testing.T) {
	pg := newQuotaTestDB(t, 8)
	ctx := context.Background()
	s := newQuotaSeeder(t, pg, "blob-quarantine", "docker")
	other := newQuotaSeeder(t, pg, "blob-quarantine-other", "docker")

	manifest := func(sd *quotaSeeder, image, digest, tag string, blobs ...string) (string, string) {
		manifestID := sd.queryID(`INSERT INTO docker_manifests (repository_id, image, digest, media_type, blob_id)
		        VALUES ($1, $2, $3, 'application/vnd.oci.image.manifest.v1+json', $4) RETURNING id`, sd.repo, image, digest, sd.blob(1))
		tagID := sd.queryID(`INSERT INTO docker_tags (repository_id, image, name, manifest_digest)
		        VALUES ($1, $2, $3, $4) RETURNING id`, sd.repo, image, tag, digest)

		for _, b := range blobs {
			sd.exec(`INSERT INTO docker_manifest_refs (manifest_id, repository_id, image, kind, digest) VALUES ($1, $2, $3, 'blob', $4)`,
				manifestID, sd.repo, image, b)
		}

		return manifestID, tagID
	}

	quarantine := func(sd *quotaSeeder, path string) {
		jobID := seedScanJob(t, pg.GetDB(), sd.repo, path, "vuln", "trivy", "completed", time.Now())
		seedVulnResult(t, pg.GetDB(), jobID, "[]", 1, 0, 0, 0, true)
	}

	check := func(sd *quotaSeeder, digest string) bool {
		got, err := pg.DockerBlobQuarantined(ctx, sd.repo, digest)
		if err != nil {
			t.Fatal(err)
		}

		return got
	}

	const shared, onlyA, onlyB, trashedOnly = "sha256:shared", "sha256:only-a", "sha256:only-b", "sha256:trashed"

	manifestA, tagA := manifest(s, "app", "sha256:ma", "latest", shared, onlyA)
	manifest(s, "lib", "sha256:mb", "v1", shared, onlyB)
	manifestC, _ := manifest(s, "old", "sha256:mc", "gone", trashedOnly)
	s.exec(`UPDATE docker_manifests SET deleted_at = now() WHERE id = $1`, manifestC)
	manifest(other, "app", "sha256:ma", "latest", shared)

	for _, d := range []string{shared, onlyA, onlyB, trashedOnly} {
		if check(s, d) {
			t.Fatalf("%s quarantined before any scan", d)
		}
	}

	for _, key := range []struct{ name, path string }{
		{"tag row id", tagA}, {"manifest row id", manifestA}, {"manifest digest", "sha256:ma"},
	} {
		t.Run(key.name, func(t *testing.T) {
			quarantine(s, key.path)

			want := map[string]bool{shared: true, onlyA: true, onlyB: false, trashedOnly: false}
			for d, w := range want {
				if got := check(s, d); got != w {
					t.Errorf("DockerBlobQuarantined(%s) = %v, want %v", d, got, w)
				}
			}

			if check(other, shared) {
				t.Error("the quarantine of another repository's image leaked")
			}

			s.exec(`DELETE FROM artifact_refs WHERE repository_id = $1 AND artifact_path = $2`, s.repo, key.path)
		})
	}

	// A quarantine on a trashed manifest holds nothing back.
	quarantine(s, manifestC)

	if check(s, trashedOnly) {
		t.Error("blob of a trashed quarantined manifest is quarantined")
	}
}
