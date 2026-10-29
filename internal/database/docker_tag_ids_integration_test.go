// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"slices"
	"testing"
)

// TestListDockerTagIDsByDigest: the ids of the live tags of one manifest, the
// ids a scan of the tag is recorded under (the manifest pull checks them).
func TestListDockerTagIDsByDigest(t *testing.T) {
	pg := newQuotaTestDB(t, 8)
	ctx := context.Background()
	s := newQuotaSeeder(t, pg, "tag-ids", "docker")

	tag := func(image, name, digest string) string {
		return s.queryID(`INSERT INTO docker_tags (repository_id, image, name, manifest_digest)
		        VALUES ($1, $2, $3, $4) RETURNING id`, s.repo, image, name, digest)
	}

	latest := tag("app", "latest", "sha256:a")
	v1 := tag("app", "v1", "sha256:a")
	tag("app", "other", "sha256:b")
	tag("lib", "latest", "sha256:a") // same digest in another image
	gone := tag("app", "old", "sha256:a")
	s.exec(`UPDATE docker_tags SET deleted_at = now() WHERE id = $1`, gone)

	got, err := pg.ListDockerTagIDsByDigest(ctx, s.repo, "app", "sha256:a")
	if err != nil {
		t.Fatal(err)
	}

	slices.Sort(got)

	want := []string{latest, v1}
	slices.Sort(want)

	if !slices.Equal(got, want) {
		t.Errorf("ListDockerTagIDsByDigest = %v, want %v", got, want)
	}
}
