// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package worker

import (
	"context"
	"io"
	"strings"
	"testing"
)

// metadataXML returns the maven-metadata.xml generated at path, or "" when no
// metadata row exists there.
func (s *cleanupSeed) metadataXML(repoID, path string) string {
	s.t.Helper()

	meta, err := s.pg.GetRepoMetadataByPath(context.Background(), repoID, path)
	if err != nil {
		s.t.Fatalf("GetRepoMetadataByPath(%s): %v", path, err)
	}

	if meta == nil {
		return ""
	}

	rc, err := s.blobs.Get(context.Background(), meta.BlobSHA256)
	if err != nil {
		s.t.Fatalf("metadata blob of %s is not stored: %v", path, err)
	}

	defer func() { _ = rc.Close() }()

	data, err := io.ReadAll(rc)
	if err != nil {
		s.t.Fatal(err)
	}

	return string(data)
}

// TestMavenRegenerateAdvertisesOnlyLiveVersions: the generated metadata lists
// the live versions with the right release/latest and the newest snapshot
// build, leaves trashed rows out and is pruned (with its blobs) when the last
// version of the artifact is gone.
func TestMavenRegenerateAdvertisesOnlyLiveVersions(t *testing.T) {
	s := newCleanupSeed(t)
	ctx := context.Background()

	repoID := s.repo("maven-meta")
	w := NewMavenMetadataWorker(s.pg, s.blobs, s.pg.GetDB(), testLogger(t), nil)

	defer w.Stop()

	rel10, _ := s.mvn(repoID, mvnRow{artifact: "a", version: "1.0", ext: "jar", ageDays: 5})
	s.mvn(repoID, mvnRow{artifact: "a", version: "1.0", ext: "pom", ageDays: 5})
	rel11, _ := s.mvn(repoID, mvnRow{artifact: "a", version: "1.10", ext: "jar", ageDays: 4})
	rel9, _ := s.mvn(repoID, mvnRow{artifact: "a", version: "1.9", ext: "jar", ageDays: 3})
	trashed, _ := s.mvn(repoID, mvnRow{artifact: "a", version: "3.0", ext: "jar", ageDays: 2})
	s.mvn(repoID, mvnRow{artifact: "a", version: "2.0-SNAPSHOT", ext: "jar", ageDays: 2, snapTS: "20240101.000000", snapBuild: 1})
	s.mvn(repoID, mvnRow{artifact: "a", version: "2.0-SNAPSHOT", ext: "jar", ageDays: 1, snapTS: "20240102.101010", snapBuild: 7})

	s.exec(`UPDATE maven_artifacts SET deleted_at = now() WHERE id = $1`, trashed)

	if err := w.regenerate(ctx, repoID, "g", "a"); err != nil {
		t.Fatalf("regenerate: %v", err)
	}

	group := s.metadataXML(repoID, "g/a/maven-metadata.xml")

	for _, want := range []string{"<version>1.0</version>", "<version>1.9</version>", "<version>1.10</version>", "<version>2.0-SNAPSHOT</version>",
		"<release>1.10</release>", "<latest>2.0-SNAPSHOT</latest>"} {
		if !strings.Contains(group, want) {
			t.Errorf("group metadata lacks %s:\n%s", want, group)
		}
	}

	if strings.Contains(group, "3.0") {
		t.Errorf("group metadata advertises a trashed version:\n%s", group)
	}

	snap := s.metadataXML(repoID, "g/a/2.0-SNAPSHOT/maven-metadata.xml")
	if !strings.Contains(snap, "2.0-20240102.101010-7") || strings.Contains(snap, "20240101.000000-1") {
		t.Errorf("snapshot metadata does not point at the newest build only:\n%s", snap)
	}

	// Removing a version re-points the group metadata and frees the old blob.
	oldGroup, _ := s.pg.GetRepoMetadataByPath(ctx, repoID, "g/a/maven-metadata.xml")
	s.exec(`DELETE FROM maven_artifacts WHERE id = $1`, rel11)

	if err := w.regenerate(ctx, repoID, "g", "a"); err != nil {
		t.Fatal(err)
	}

	group = s.metadataXML(repoID, "g/a/maven-metadata.xml")
	if strings.Contains(group, "1.10") || !strings.Contains(group, "<release>1.9</release>") {
		t.Errorf("metadata still advertises the removed version or the wrong release:\n%s", group)
	}

	if s.storedBlob(oldGroup.BlobSHA256) || s.blobRowExists(oldGroup.BlobSHA256) {
		t.Error("superseded group metadata blob was not reclaimed")
	}

	// The last live versions go: every metadata row of the artifact goes too.
	snapMeta, _ := s.pg.GetRepoMetadataByPath(ctx, repoID, "g/a/2.0-SNAPSHOT/maven-metadata.xml")
	curGroup, _ := s.pg.GetRepoMetadataByPath(ctx, repoID, "g/a/maven-metadata.xml")

	s.exec(`DELETE FROM maven_artifacts WHERE repository_id = $1 AND id <> $2`, repoID, rel10)
	s.exec(`DELETE FROM maven_artifacts WHERE id = $1`, rel9)
	s.exec(`DELETE FROM maven_artifacts WHERE id = $1`, rel10)

	if err := w.regenerate(ctx, repoID, "g", "a"); err != nil {
		t.Fatal(err)
	}

	if n := s.count(`SELECT COUNT(*) FROM repository_metadata WHERE repository_id = $1`, repoID); n != 0 {
		t.Errorf("%d metadata rows survive an artifact without versions", n)
	}

	for _, sha := range []string{snapMeta.BlobSHA256, curGroup.BlobSHA256} {
		if s.storedBlob(sha) || s.blobRowExists(sha) {
			t.Errorf("metadata blob %s of the deleted artifact was not reclaimed", sha[:8])
		}
	}
}

// Metadata of one artifact never touches another's: regenerating a.b while a.c
// has none leaves a.c's rows alone.
func TestMavenRegenerateIsScopedToItsArtifact(t *testing.T) {
	s := newCleanupSeed(t)
	ctx := context.Background()

	repoID := s.repo("maven-scope")
	w := NewMavenMetadataWorker(s.pg, s.blobs, s.pg.GetDB(), testLogger(t), nil)

	defer w.Stop()

	s.mvn(repoID, mvnRow{artifact: "one", version: "1.0", ext: "jar", ageDays: 1})
	s.mvn(repoID, mvnRow{artifact: "two", version: "2.0", ext: "jar", ageDays: 1})

	for _, a := range []string{"one", "two"} {
		if err := w.regenerate(ctx, repoID, "g", a); err != nil {
			t.Fatal(err)
		}
	}

	s.exec(`DELETE FROM maven_artifacts WHERE artifact_id_coord = 'one'`)

	if err := w.regenerate(ctx, repoID, "g", "one"); err != nil {
		t.Fatal(err)
	}

	if got := s.metadataXML(repoID, "g/one/maven-metadata.xml"); got != "" {
		t.Errorf("metadata of the emptied artifact survives:\n%s", got)
	}

	if got := s.metadataXML(repoID, "g/two/maven-metadata.xml"); !strings.Contains(got, "<release>2.0</release>") {
		t.Errorf("a neighbouring artifact's metadata was damaged:\n%s", got)
	}
}
