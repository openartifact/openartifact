// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package gomod

import (
	"slices"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
)

func TestValidVersion(t *testing.T) {
	valid := []string{
		"v0.0.1", "v1.2.3", "v1.0.0-rc.1", "v1.0.0-RC1",
		"v0.0.0-20240102150405-0123456789ab", "v2.0.0+incompatible", "v2.0.0-beta.1+incompatible",
	}
	invalid := []string{
		"", "latest", "1.2.3", "v1", "v1.2", "v1.2.3+build", "v01.2.3", "v1.2.3-", "master", "v1.2.3/../x",
	}

	for _, v := range valid {
		if !validVersion(v) {
			t.Errorf("validVersion(%q) = false, want true", v)
		}
	}

	for _, v := range invalid {
		if validVersion(v) {
			t.Errorf("validVersion(%q) = true, want false", v)
		}
	}
}

func TestSortVersionsDescIsSemverNotLexical(t *testing.T) {
	var mods []*models.GoModule
	for _, v := range []string{"v1.9.0", "v1.10.0-rc1", "v1.10.0", "v0.1.0", "v1.10.0-rc.2"} {
		mods = append(mods, &models.GoModule{Version: v})
	}

	sortVersionsDesc(mods)

	got := make([]string, 0, len(mods))
	for _, m := range mods {
		got = append(got, m.Version)
	}

	want := []string{"v1.10.0", "v1.10.0-rc1", "v1.10.0-rc.2", "v1.9.0", "v0.1.0"}
	if !slices.Equal(got, want) {
		t.Errorf("sorted = %v, want %v", got, want)
	}
}

func TestLatestVersionEmpty(t *testing.T) {
	if latestVersion(nil) != nil {
		t.Error("latestVersion(nil) must be nil")
	}
}
