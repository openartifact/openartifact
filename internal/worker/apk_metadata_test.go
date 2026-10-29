// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
	formatapk "github.com/openartifact/openartifact/internal/format/apk"
	"github.com/stretchr/testify/assert"
)

// apk fetches a package from <repo>/<arch of its index entry>/. Alpine's own
// indexes list a noarch package with the architecture of the directory
// (`apk index --rewrite-arch`); an A:noarch entry makes apk-tools request
// <repo>/noarch/<file>.apk, which does not exist (HTTP 404 on install).
func TestApkNoarchIsListedWithTheDirectoryArch(t *testing.T) {
	rows := []*models.ApkPackage{
		{Branch: "v3.20", Repo: "main", Arch: "x86_64", PkgArch: "x86_64", Name: "hello", Version: "1.0-r0"},
		{Branch: "v3.20", Repo: "main", Arch: "x86_64", PkgArch: "noarch", Name: "hello-doc", Version: "1.0-r0"},
		{Branch: "v3.20", Repo: "main", Arch: "aarch64", PkgArch: "noarch", Name: "hello-doc", Version: "1.0-r0"},
	}

	dirs := ApkPackageDirs(rows)
	x86 := formatapk.IndexPath("v3.20", "main", "x86_64")
	arm := formatapk.IndexPath("v3.20", "main", "aarch64")

	if assert.Len(t, dirs[x86], 2) {
		for _, p := range dirs[x86] {
			assert.Equal(t, "x86_64", p.Arch, p.Name)
		}
	}

	if assert.Len(t, dirs[arm], 1) {
		assert.Equal(t, "aarch64", dirs[arm][0].Arch)
	}
}
