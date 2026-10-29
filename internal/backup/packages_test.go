// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/storage"
)

// TestMain registers the storage descriptors the unit tests need for the
// formats of their repositories: deb has a companion table, a side table that
// references it and deb regeneration; rpm and maven regenerate their own
// metadata; go, npm and raw have nothing.
func TestMain(m *testing.M) {
	storage.Register("deb", storage.Storage{
		SideTables: []storage.SideTable{{Name: "deb_files", Refs: []string{"distribution_id"}}},
		Backup: storage.Backup{
			Companions: []storage.Companion{{Name: "debian_distributions", Scope: "t.repository_id = $1", RepoColumn: "repository_id"}},
			Regen:      storage.RegenDeb,
		},
	})
	storage.Register("rpm", storage.Storage{Backup: storage.Backup{Regen: storage.RegenRPM}})
	storage.Register("maven", storage.Storage{Backup: storage.Backup{Regen: storage.RegenMaven}})
	storage.Register("npm", storage.Storage{})
	storage.Register("go", storage.Storage{})
	storage.Register("raw", storage.Storage{})

	os.Exit(m.Run())
}

func TestPackageRowSets_OrderCompanionsFirstTagsLast(t *testing.T) {
	d, _ := storage.Lookup("deb")

	var names []string
	for _, s := range packageRowSets(d) {
		names = append(names, s.Name())
	}

	want := "debian_distributions packages package_versions package_files deb_files package_tags"
	if got := strings.Join(names, " "); got != want {
		t.Fatalf("row sets = %q, want %q", got, want)
	}
}

func TestRunBackup_FailsForFormatWithoutDescriptor(t *testing.T) {
	ctx := context.Background()
	src := newFakeDB("src")

	if err := src.CreateRepository(ctx, &models.Repository{Name: "x", Format: "no-such-format", RepoType: "hosted"}); err != nil {
		t.Fatal(err)
	}

	engine, _ := newTestEngine(t, src)
	job := &models.BackupJob{ID: "job1", Status: "pending", Type: "full"}
	_ = src.CreateBackupJob(ctx, job)

	err := engine.RunBackup(ctx, job.ID, "full")
	if !errors.Is(err, ErrNoStorageDescriptor) {
		t.Fatalf("RunBackup error = %v, want ErrNoStorageDescriptor (a backup must not silently omit a repository's rows)", err)
	}

	if src.jobs[job.ID].Status != "failed" {
		t.Fatalf("job status = %q, want failed", src.jobs[job.ID].Status)
	}
}

// writeArchive writes a gzip tar with the given manifest version and entries.
func writeArchive(t *testing.T, version string, entries [][2]string) string {
	t.Helper()

	path := t.TempDir() + "/b.tar.gz"

	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}

	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)

	manifest, _ := json.Marshal(BackupManifest{Version: version, TableCounts: map[string]int{}})
	all := append([][2]string{{"manifest.json", string(manifest)}}, entries...)

	for _, e := range all {
		name, body := e[0], e[1]
		if err := tw.WriteHeader(&tar.Header{Name: name, Size: int64(len(body)), Mode: 0o644}); err != nil {
			t.Fatal(err)
		}

		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}

	if err := errors.Join(tw.Close(), gz.Close(), f.Close()); err != nil {
		t.Fatal(err)
	}

	return path
}

func TestRunRestore_RefusesArchivesOfOlderVersions(t *testing.T) {
	for _, version := range []string{"1.0", "", "3.0"} {
		dst := newFakeDB("dst")
		engine, _ := newTestEngine(t, dst)

		err := engine.RunRestore(context.Background(), writeArchive(t, version, nil))
		if !errors.Is(err, ErrUnsupportedArchive) {
			t.Fatalf("version %q: error = %v, want ErrUnsupportedArchive", version, err)
		}

		if !strings.Contains(err.Error(), "oa import") {
			t.Fatalf("version %q: error %q does not point at oa import", version, err)
		}
	}
}

// An entry can only target the tables of its repository's descriptor: a
// crafted entry naming another table must be skipped and counted, never run.
func TestRunRestore_IgnoresPackageRowsOutsideTheDescriptor(t *testing.T) {
	repos, _ := json.Marshal([]*models.Repository{{ID: "old1", Name: "plainrepo", Format: "raw", RepoType: "hosted"}})
	path := writeArchive(t, ArchiveVersion, [][2]string{
		{"repositories.jsonl", string(repos)},
		{"package_rows/old1/users.jsonl", `{"id":"x"}` + "\n"},
		{"package_rows/old1/packages.jsonl", `{"id":"p1"}` + "\n"},
		{"package_rows/unknown/packages.jsonl", `{"id":"p2"}` + "\n"},
		{"package_rows/old1/deb_files.jsonl", `{"id":"f"}` + "\n"},
		{"package_rows/old1/../../x/packages.jsonl", `{"id":"p3"}` + "\n"},
	})

	dst := newFakeDB("dst")
	engine, _ := newTestEngine(t, dst)

	err := engine.RunRestore(context.Background(), path)
	if !errors.Is(err, ErrRestoreIncomplete) {
		t.Fatalf("error = %v, want ErrRestoreIncomplete for the skipped entries", err)
	}

	if len(dst.restoredRows) != 1 || dst.restoredRows[0].table != "packages" || dst.restoredRows[0].rows[0] != `{"id":"p1"}` {
		t.Fatalf("restored = %+v, want only old1's packages row", dst.restoredRows)
	}
}
