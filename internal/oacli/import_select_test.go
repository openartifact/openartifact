// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package oacli

import (
	"io"
	"testing"
)

// A source names the asset paths that end up in destination URLs; one that
// climbs out of its repository must be refused, not uploaded.
func TestSelectAssetsRejectsUnsafePaths(t *testing.T) {
	im := &importer{rep: &importReport{progress: io.Discard}}

	paths := []string{"a/b.jar", "../other/b.jar", "a/../../b.jar", "a//b.jar", `a\..\b.jar`, "a/./b.jar"}
	assets := make([]sourceAsset, len(paths))

	for i, p := range paths {
		assets[i] = sourceAsset{Repo: "src", Path: p}
	}

	got := im.selectAssets(sourceRepo{Name: "src", Format: formatRaw}, assets)
	if len(got) != 1 || got[0].Path != "a/b.jar" {
		t.Fatalf("selected %+v, want only a/b.jar", got)
	}

	if im.rep.Failed == 0 {
		t.Fatalf("no unsafe path was reported as failed")
	}
}
