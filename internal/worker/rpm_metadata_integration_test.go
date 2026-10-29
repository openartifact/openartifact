// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package worker

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
)

type rpmFixture struct {
	*cleanupSeed

	repoID string
	w      *RPMetadataWorker
}

func newRPMFixture(t *testing.T, name string) *rpmFixture {
	t.Helper()

	f := &rpmFixture{cleanupSeed: newCleanupSeed(t)}

	repo := &models.Repository{Name: name, Format: "rpm", RepoType: "hosted", ProjectID: new(trashTestOrgID)}
	if err := f.pg.CreateRepository(context.Background(), repo); err != nil {
		t.Fatal(err)
	}

	f.repoID = repo.ID

	f.exec(`INSERT INTO rpm_repos (repository_id, base_arch) VALUES ($1, 'x86_64') ON CONFLICT DO NOTHING`, f.repoID)

	f.w = NewRPMetadataWorker(f.pg, f.blobs, nil, testLogger(t), nil)
	t.Cleanup(f.w.Stop)

	return f
}

func (f *rpmFixture) pkg(name string) string {
	f.t.Helper()

	f.seq++

	blobID, _ := f.blob(fmt.Sprintf("rpm %s %d", name, f.seq), time.Hour)

	comp := &models.Component{
		RepositoryID: f.repoID, ArtifactID: blobID, Name: name, Version: "1.0", Release: "1", Arch: "x86_64",
		Filename: name + "-1.0-1.x86_64.rpm",
	}
	if err := f.pg.CreateRpmPackage(context.Background(), comp); err != nil {
		f.t.Fatal(err)
	}

	id := comp.ID

	return id
}

func (f *rpmFixture) markStale() {
	f.t.Helper()

	f.exec(`UPDATE rpm_repos SET metadata_stale = true, updated_at = now() WHERE repository_id = $1`, f.repoID)
}

func (f *rpmFixture) stale() bool {
	f.t.Helper()

	return f.count(`SELECT COUNT(*) FROM rpm_repos WHERE repository_id = $1 AND metadata_stale`, f.repoID) == 1
}

func (f *rpmFixture) regenerate() {
	f.t.Helper()

	if err := f.w.Regenerate(context.Background(), f.repoID); err != nil {
		f.t.Fatalf("Regenerate: %v", err)
	}
}

// primary returns the uncompressed primary.xml the repository advertises and
// the sha256 of the blob holding it.
func (f *rpmFixture) primary() (string, string) {
	f.t.Helper()

	var path, sha string
	if err := f.pg.GetDB().QueryRowContext(context.Background(),
		`SELECT path, blob_sha256 FROM repository_metadata WHERE repository_id = $1 AND path LIKE 'repodata/%-primary.xml.gz'`, f.repoID).
		Scan(&path, &sha); err != nil {
		f.t.Fatalf("no primary.xml advertised: %v", err)
	}

	rc, err := f.blobs.Get(context.Background(), sha)
	if err != nil {
		f.t.Fatalf("primary.xml blob is not stored: %v", err)
	}

	defer func() { _ = rc.Close() }()

	raw, err := io.ReadAll(rc)
	if err != nil {
		f.t.Fatal(err)
	}

	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		f.t.Fatalf("primary.xml is not gzip: %v", err)
	}

	xml, err := io.ReadAll(zr)
	if err != nil {
		f.t.Fatal(err)
	}

	return string(xml), sha
}

// TestRPMRegenerateHonoursStaleFlagAndLivePackages: a clean repository is left
// alone, a stale one is rebuilt from its live packages (trashed ones are not
// advertised) and marked clean, and a superseded index blob is reclaimed.
func TestRPMRegenerateHonoursStaleFlagAndLivePackages(t *testing.T) {
	f := newRPMFixture(t, "rpm-regen")

	f.pkg("alpha")
	beta := f.pkg("beta")
	gone := f.pkg("gamma")
	f.exec(`UPDATE package_files SET deleted_at = now() WHERE id = $1`, gone)

	f.exec(`UPDATE rpm_repos SET metadata_stale = false WHERE repository_id = $1`, f.repoID)
	f.regenerate()

	if n := f.count(`SELECT COUNT(*) FROM repository_metadata WHERE repository_id = $1`, f.repoID); n != 0 {
		t.Fatalf("a repository that is not stale was regenerated (%d rows)", n)
	}

	f.markStale()
	f.regenerate()

	if f.stale() {
		t.Fatal("regeneration left the repository stale")
	}

	xml, firstSHA := f.primary()
	for _, want := range []string{"<name>alpha</name>", "<name>beta</name>"} {
		if !strings.Contains(xml, want) {
			t.Errorf("primary.xml lacks %s", want)
		}
	}

	if strings.Contains(xml, "gamma") {
		t.Error("primary.xml advertises a trashed package")
	}

	for _, path := range []string{"repodata/repomd.xml"} {
		if f.count(`SELECT COUNT(*) FROM repository_metadata WHERE repository_id = $1 AND path = $2`, f.repoID, path) != 1 {
			t.Errorf("%s was not generated", path)
		}
	}

	// A package leaves: the new index drops it and the old one is freed.
	f.exec(`DELETE FROM package_files WHERE id = $1`, beta)
	f.markStale()
	f.regenerate()

	xml, secondSHA := f.primary()
	if strings.Contains(xml, "beta") || !strings.Contains(xml, "<name>alpha</name>") {
		t.Errorf("primary.xml after the removal:\n%s", xml)
	}

	if secondSHA == firstSHA {
		t.Fatal("primary.xml did not change after a package left")
	}

	if f.storedBlob(firstSHA) || f.blobRowExists(firstSHA) {
		t.Error("the superseded primary.xml blob was not reclaimed")
	}
}

// A marking that arrives while a regeneration runs must survive it: the stale
// flag is cleared only when nothing re-marked the repository since it was read.
func TestRPMRegenerateKeepsRemarkedRepositoryStale(t *testing.T) {
	f := newRPMFixture(t, "rpm-remark")
	f.pkg("alpha")

	// Re-mark from inside the regeneration: the worker's blob store Put runs
	// between reading the stale marker and clearing it.
	f.w.blobStore = &putHookStore{
		BlobStore: f.blobs,
		match:     func(map[string]string) bool { return true },
		onPut: func() {
			f.exec(`UPDATE rpm_repos SET updated_at = now() + interval '1 second' WHERE repository_id = $1`, f.repoID)
		},
	}

	f.regenerate()

	if !f.stale() {
		t.Fatal("a notification that arrived during the run was swallowed")
	}
}

// RegenerateStale (startup recovery) rebuilds every stale repository and only those.
func TestRPMRegenerateStaleRebuildsOnlyStaleRepositories(t *testing.T) {
	f := newRPMFixture(t, "rpm-startup-a")
	f.pkg("alpha")

	other := &models.Repository{Name: "rpm-startup-b", Format: "rpm", RepoType: "hosted", ProjectID: new(trashTestOrgID)}
	if err := f.pg.CreateRepository(context.Background(), other); err != nil {
		t.Fatal(err)
	}

	f.exec(
		`INSERT INTO rpm_repos (repository_id, base_arch, metadata_stale) VALUES ($1, 'x86_64', false) ON CONFLICT (repository_id, base_arch) DO UPDATE SET metadata_stale = false`,
		other.ID,
	)

	f.markStale()
	f.w.RegenerateStale(context.Background())

	if f.stale() {
		t.Error("the stale repository was not rebuilt at startup")
	}

	if n := f.count(`SELECT COUNT(*) FROM repository_metadata WHERE repository_id = $1`, other.ID); n != 0 {
		t.Errorf("a clean repository was regenerated at startup (%d rows)", n)
	}
}
