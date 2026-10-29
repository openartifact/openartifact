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

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/storage"
	"github.com/openartifact/openartifact/internal/group"
	"github.com/openartifact/openartifact/internal/repotype"
	"github.com/openartifact/openartifact/internal/repotype/repotypetest"
	"github.com/openartifact/openartifact/pkg/logger"
)

// TestCleanupFormatSupported: a format is supported when it has a storage
// descriptor (what the plugin layer registers for every package format) or is
// docker, the one special case with dedicated tables.
func TestCleanupFormatSupported(t *testing.T) {
	storage.Reset()
	t.Cleanup(storage.Reset)

	storage.Register("fixture", storage.Storage{Trash: storage.Trash{Kind: "fixture_item"}})

	for format, want := range map[string]bool{"fixture": true, "docker": true, "no-such-format": false} {
		if got := CleanupFormatSupported(format); got != want {
			t.Errorf("CleanupFormatSupported(%s) = %v, want %v", format, got, want)
		}
	}

	tbl, ok := cleanupTableFor("fixture")
	if !ok || tbl.kind != "fixture_item" || tbl.docker {
		t.Errorf("cleanupTableFor(fixture) = %+v, %v", tbl, ok)
	}

	if tbl, _ := cleanupTableFor("docker"); !tbl.docker || tbl.kind != models.TrashKindDockerTag {
		t.Errorf("cleanupTableFor(docker) = %+v", tbl)
	}
}

type staleMarkDB struct {
	CleanupStore

	marked []string
}

func (d *staleMarkDB) MarkRepoStale(_ context.Context, id string) error {
	d.marked = append(d.marked, id)

	return nil
}

type notifyCounter struct{ calls int }

func (n *notifyCounter) NotifyStale(string) { n.calls++ }

type mavenNotifyCounter struct{ calls int }

func (n *mavenNotifyCounter) NotifyStale(_, _, _ string) { n.calls++ }

type dockerGCCounter struct{ runs int }

func (g *dockerGCCounter) RunRepo(context.Context, *models.Repository, DockerGCOptions) (*DockerGCReport, error) {
	g.runs++

	return &DockerGCReport{}, nil
}

func (*dockerGCCounter) DefaultGrace() time.Duration { return 0 }

// TestRefreshMetadataCoversEveryFormat: the formats with a follow-up after a run
// that removed rows are apk, deb, pacman and rpm (their metadata workers, rpm
// also marks the repository stale), maven (per artifact), npm (packument, needs
// a database: classified only) and docker (registry GC); every other format
// computes its indexes live from the tables.
func TestRefreshMetadataCoversEveryFormat(t *testing.T) {
	t.Parallel()

	handled := repotypetest.Classified(t, "worker.CleanupWorker.refreshMetadata",
		[]string{"apk", "deb", "docker", "maven", "npm", "pacman", "rpm"},
		[]string{
			"ansible", "cargo", "composer", "conan", "conda", "go", "helm", "ivy", "nix", "nuget", "pypi", "raw", "rubygems",
			"sbt", "terraform",
		},
	)

	for _, f := range repotype.Names() {
		if repotype.GeneratedGroupMetadata(f) != slices.Contains([]string{"apk", "deb", "pacman", "rpm"}, f) {
			t.Errorf("refreshMetadata: %s: GeneratedGroupMetadata disagrees with the notifier branches", f)
		}
	}

	log, err := logger.New("error", "console", "stderr")
	if err != nil {
		t.Fatal(err)
	}

	repotypetest.Each(t, func(t *testing.T, format string) {
		t.Helper()

		t.Parallel()

		if format == "npm" {
			return
		}

		db := &staleMarkDB{}
		rpm, deb, pac, apk := &notifyCounter{}, &notifyCounter{}, &notifyCounter{}, &notifyCounter{}
		mvn, gc := &mavenNotifyCounter{}, &dockerGCCounter{}
		w := NewCleanupWorker(db, nil, log)
		w.SetMetadataNotifiers(rpm, deb)
		w.SetPacmanNotifier(pac)
		w.SetApkNotifier(apk)
		w.SetRegenNotifiers(mvn, nil, gc)

		run := newCleanupRun()
		run.touch([]cleanupCandidate{{name: "org:lib"}})
		run.hard = 1

		w.refreshMetadata(t.Context(), &models.Repository{ID: "r", Format: format}, run)

		acted := rpm.calls+deb.calls+pac.calls+apk.calls+mvn.calls+gc.runs > 0
		if want := slices.Contains(handled, format); acted != want {
			t.Errorf("refreshMetadata(%s) acted = %v, handled = %v", format, acted, want)
		}

		if marked := len(db.marked) > 0; marked != (format == "rpm") {
			t.Errorf("refreshMetadata(%s) marked stale = %v, want only rpm", format, marked)
		}
	})
}

// TestGroupMetadataCoversEveryFormat: exactly the registry's generated-metadata
// formats have a group builder, an anchor and a hint check; every other group
// format is resolved member by member.
func TestGroupMetadataCoversEveryFormat(t *testing.T) {
	t.Parallel()

	generated := []string{"apk", "deb", "pacman", "rpm"}
	others := []string{
		"ansible", "cargo", "composer", "conan", "conda", "docker", "go", "helm", "ivy", "maven", "nix", "npm", "nuget",
		"pypi", "raw", "rubygems", "sbt", "terraform",
	}

	// run dispatches to buildDeb/buildRPM/buildPacman/buildApk: it needs a database and storage, so it is classified only.
	runHandled := repotypetest.Classified(t, "worker.GroupMetadataWorker.run", generated, others)
	hintHandled := repotypetest.Classified(t, "worker.GroupMetadataWorker.checkHint",
		[]string{"apk", "deb", "pacman"}, append([]string{"rpm"}, others...))
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
