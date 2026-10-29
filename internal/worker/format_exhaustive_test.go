// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"errors"
	"slices"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/group"
	"github.com/openartifact/openartifact/internal/repotype"
	"github.com/openartifact/openartifact/internal/repotype/repotypetest"
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
