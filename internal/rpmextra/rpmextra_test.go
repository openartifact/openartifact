// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package rpmextra_test

import (
	"reflect"
	"testing"

	"github.com/M0Rf30/pkgformat/rpm"
	"github.com/openartifact/openartifact/internal/rpmextra"
)

func TestRoundTrip(t *testing.T) {
	in := &rpm.Package{
		InstalledSize: 10, ArchiveSize: 7,
		Provides:   []rpm.Dependency{{Name: "a", Flags: 8, Version: "1", Epoch: 2}},
		Recommends: []rpm.Dependency{{Name: "r"}},
		Files:      []rpm.FileEntry{{Path: "/usr/bin/a", MTime: 5, Mode: 0o100755, Digest: "abc", LinkTo: "b"}},
		Changelogs: []rpm.ChangelogEntry{{Time: 3, Author: "x", Description: "y"}},
	}

	raw, err := rpmextra.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}

	var got rpmextra.Stored
	if err := got.Unmarshal(raw); err != nil {
		t.Fatal(err)
	}

	if !got.WeakDepsParsed || !got.SizesParsed || got.InstalledSize != 10 || got.ArchiveSize != 7 {
		t.Errorf("flags/sizes lost: %+v", got)
	}

	if !reflect.DeepEqual(got.Files, in.Files) || !reflect.DeepEqual(got.Provides, in.Provides) ||
		!reflect.DeepEqual(got.Changelogs, in.Changelogs) || !reflect.DeepEqual(got.Recommends, in.Recommends) {
		t.Errorf("round trip changed the data: %+v", got)
	}
}

// Rows written before the library took over carry capitalised keys and no
// weak-dependency or size markers; they must still decode and be flagged for
// the backfill.
func TestUnmarshalLegacyRow(t *testing.T) {
	const legacy = `{"provides":[{"Name":"p","Flags":8,"Version":"1.0","Epoch":1}],` +
		`"files":[{"Path":"/etc/x","MTime":9,"Digest":"d","LinkTo":"l"}],` +
		`"changelogs":[{"Time":4,"Author":"a","Description":"d"}]}`

	var got rpmextra.Stored
	if err := got.Unmarshal([]byte(legacy)); err != nil {
		t.Fatal(err)
	}

	if got.WeakDepsParsed || got.SizesParsed {
		t.Error("legacy row must be flagged for the blob backfill")
	}

	want := rpm.FileEntry{Path: "/etc/x", MTime: 9, Digest: "d", LinkTo: "l"}
	if len(got.Files) != 1 || !reflect.DeepEqual(got.Files[0], want) {
		t.Errorf("files = %+v, want %+v", got.Files, want)
	}

	if len(got.Provides) != 1 || got.Provides[0] != (rpm.Dependency{Name: "p", Flags: 8, Version: "1.0", Epoch: 1}) {
		t.Errorf("provides = %+v", got.Provides)
	}

	if len(got.Changelogs) != 1 || got.Changelogs[0].Author != "a" || got.Changelogs[0].Time != 4 {
		t.Errorf("changelogs = %+v", got.Changelogs)
	}
}

func TestUnmarshalEmpty(t *testing.T) {
	var got rpmextra.Stored
	if err := got.Unmarshal(nil); err != nil || got.WeakDepsParsed {
		t.Errorf("empty input: %v %+v", err, got)
	}
}
