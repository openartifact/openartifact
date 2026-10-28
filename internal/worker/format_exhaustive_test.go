// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/databasetest"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/group"
	"github.com/openartifact/openartifact/internal/repotype"
	"github.com/openartifact/openartifact/internal/repotype/repotypetest"
	"github.com/openartifact/openartifact/pkg/logger"
)

// TestCleanupFormatTablesCoverEveryFormat: every format has cleanup tables, and
// they are the format's package tables from the registry. Docker's manifests
// are not ranked: they are reclaimed by registry garbage collection once their
// tags are gone.
func TestCleanupFormatTablesCoverEveryFormat(t *testing.T) {
	t.Parallel()

	repotypetest.Classified(t, "worker.cleanupFormatTables", repotype.Names(), nil)

	repotypetest.Each(t, func(t *testing.T, format string) {
		t.Helper()

		t.Parallel()

		if !CleanupFormatSupported(format) {
			t.Fatalf("CleanupFormatSupported(%s) = false: add cleanup tables for the format", format)
		}

		spec, _ := repotype.Lookup(format)

		var got []string

		for _, tbl := range cleanupFormatTables(format) {
			got = append(got, tbl.table)

			if tbl.kind == "" || len(tbl.partitionCols) == 0 || tbl.versionExpr == "" {
				t.Errorf("%s: table %s has no trash kind, partition or version expression", format, tbl.table)
			}
		}

		want := slices.Clone(spec.PackageTables)
		switch format {
		case "docker":
			want = []string{"docker_tags"}
		case "npm":
			// Versions are ranked; a package row goes with its last version (refreshNPM).
			want = []string{"npm_versions"}
		}

		slices.Sort(got)
		slices.Sort(want)

		if !slices.Equal(got, want) {
			t.Errorf("cleanup tables of %s = %v, want %v", format, got, want)
		}
	})

	if CleanupFormatSupported("no-such-format") {
		t.Error("CleanupFormatSupported accepted an unregistered format")
	}
}

type staleMarkDB struct {
	databasetest.BaseMockDB

	marked []string
}

func (d *staleMarkDB) MarkRepoStale(_ context.Context, id string) error {
	d.marked = append(d.marked, id)

	return nil
}

type notifyCounter struct{ calls int }

func (n *notifyCounter) NotifyStale(string) { n.calls++ }

// TestRefreshMetadataCoversEveryFormat: the formats whose metadata a cleanup run
// regenerates through a notifier are the registry's generated-metadata formats.
func TestRefreshMetadataCoversEveryFormat(t *testing.T) {
	t.Parallel()

	handled := repotypetest.Classified(t, "worker.CleanupWorker.refreshMetadata",
		[]string{"rpm", "deb", "pacman", "apk"},
		[]string{
			"docker", "npm", "pypi", "maven", "helm", "cargo", "rubygems", "nuget", "go", "raw", "conda", "conan",
			"ivy", "sbt", "terraform", "nix",
		},
	)

	for _, f := range repotype.Names() {
		if repotype.GeneratedGroupMetadata(f) != slices.Contains(handled, f) {
			t.Errorf(
				"refreshMetadata: %s: GeneratedGroupMetadata = %v disagrees with the notifier branches",
				f,
				!slices.Contains(handled, f),
			)
		}
	}

	log, err := logger.New("error", "console", "stderr")
	if err != nil {
		t.Fatal(err)
	}

	repotypetest.Each(t, func(t *testing.T, format string) {
		t.Helper()

		t.Parallel()

		db := &staleMarkDB{}
		rpm, deb, pac, apk := &notifyCounter{}, &notifyCounter{}, &notifyCounter{}, &notifyCounter{}
		w := NewCleanupWorker(db, nil, log)
		w.SetMetadataNotifiers(rpm, deb)
		w.SetPacmanNotifier(pac)
		w.SetApkNotifier(apk)

		w.refreshMetadata(t.Context(), &models.Repository{ID: "r", Format: format})

		notified := rpm.calls + deb.calls + pac.calls + apk.calls
		if want := slices.Contains(handled, format); (notified > 0) != want {
			t.Errorf("refreshMetadata(%s) notified %d workers, handled = %v", format, notified, want)
		}

		if marked := len(db.marked) > 0; marked != (format == "rpm") {
			t.Errorf("refreshMetadata(%s) marked stale = %v, want only rpm", format, marked)
		}
	})
}

type mavenNotifyCounter struct{ calls int }

func (n *mavenNotifyCounter) NotifyStale(_, _, _ string) { n.calls++ }

type dockerGCCounter struct{ runs int }

func (g *dockerGCCounter) RunRepo(context.Context, *models.Repository, DockerGCOptions) (*DockerGCReport, error) {
	g.runs++

	return &DockerGCReport{}, nil
}

func (*dockerGCCounter) DefaultGrace() time.Duration { return 0 }

// TestRefreshFormatMetadataCoversEveryFormat: maven, npm and docker have a
// follow-up of a run that removed rows; every other format computes its
// indexes live from the tables.
func TestRefreshFormatMetadataCoversEveryFormat(t *testing.T) {
	t.Parallel()

	handled := repotypetest.Classified(t, "worker.CleanupWorker.refreshFormatMetadata",
		[]string{"maven", "npm", "docker"},
		[]string{
			"pypi", "helm", "cargo", "rubygems", "deb", "rpm", "nuget", "go", "raw", "conda", "conan", "ivy", "sbt",
			"terraform", "apk", "pacman", "nix",
		},
	)

	log, err := logger.New("error", "console", "stderr")
	if err != nil {
		t.Fatal(err)
	}

	repotypetest.Each(t, func(t *testing.T, format string) {
		t.Helper()

		t.Parallel()

		// npm needs a database connection to resolve its packages: classified only.
		if format == "npm" {
			return
		}

		mvn, gc := &mavenNotifyCounter{}, &dockerGCCounter{}
		w := NewCleanupWorker(&staleMarkDB{}, nil, log)
		w.SetRegenNotifiers(mvn, nil, gc)

		run := newCleanupRun()
		run.touch([]cleanupCandidate{{name: "org:lib"}})
		run.hard = 1

		w.refreshFormatMetadata(t.Context(), &models.Repository{ID: "r", Format: format}, run)

		acted := mvn.calls+gc.runs > 0
		if want := slices.Contains(handled, format); acted != want {
			t.Errorf("refreshFormatMetadata(%s) acted = %v, handled = %v", format, acted, want)
		}
	})
}

// TestGroupMetadataCoversEveryFormat: exactly the registry's generated-metadata
// formats have a group builder, an anchor and a hint check; every other group
// format is resolved member by member.
func TestGroupMetadataCoversEveryFormat(t *testing.T) {
	t.Parallel()

	generated := []string{"deb", "rpm", "pacman", "apk"}
	others := []string{
		"docker", "npm", "pypi", "maven", "helm", "cargo", "rubygems", "nuget", "go", "raw", "conda", "conan",
		"ivy", "sbt", "terraform", "nix",
	}

	// run dispatches to buildDeb/buildRPM/buildPacman/buildApk: it needs a database and storage, so it is classified only.
	runHandled := repotypetest.Classified(t, "worker.GroupMetadataWorker.run", generated, others)
	hintHandled := repotypetest.Classified(t, "worker.GroupMetadataWorker.checkHint",
		[]string{"deb", "apk", "pacman"}, append([]string{"rpm"}, others...))
	anchorHandled := repotypetest.Classified(t, "worker.groupAnchor", generated, others)

	for _, f := range repotype.Names() {
		if repotype.GeneratedGroupMetadata(f) != slices.Contains(runHandled, f) {
			t.Errorf("group worker run: %s: GeneratedGroupMetadata disagrees with the builders", f)
		}
	}

	w := &GroupMetadataWorker{}

	repotypetest.Each(t, func(t *testing.T, format string) {
		t.Helper()

		t.Parallel()

		grp := &models.Repository{Format: format, RepoType: repotype.Group}

		// A hint no distribution, directory or architecture can have: the checks that exist refuse it without any member.
		err := w.checkHint(t.Context(), grp, "../x", nil)
		if want := slices.Contains(hintHandled, format); errors.Is(err, group.ErrDistNotFound) != want {
			t.Errorf("checkHint(%s) = %v, handled = %v", format, err, want)
		}

		anchor := groupAnchor(format, "a/b/c")
		if slices.Contains(anchorHandled, format) && anchor == "" {
			t.Errorf("groupAnchor(%s) is empty for a generated-metadata format", format)
		}
	})
}
