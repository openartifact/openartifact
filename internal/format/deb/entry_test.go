// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package deb

import (
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
)

func TestFormatPackagesEntry(t *testing.T) {
	got := FormatPackagesEntry(models.DebPackageEntry{
		Name: "libfoo", Version: "1.0", Arch: "amd64", Filename: "libfoo_1.0_amd64.deb",
		ComponentName: "main", SHA256: "abc", Size: 2048, InstalledSize: 9,
		ControlFields: map[string]string{"Depends": "libc6", "Maintainer": "A <a@b>", "Description": "short\n long"},
	})

	for _, want := range []string{
		"Package: libfoo\n", "Depends: libc6\n", "Maintainer: A <a@b>\n", "Installed-Size: 9\n",
		"Filename: pool/main/libf/libfoo/libfoo_1.0_amd64.deb\n", "Size: 2048\n", "SHA256: abc\n",
		"Description: short\n long\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}

	if !strings.HasSuffix(got, "\n\n") {
		t.Error("stanza must end with blank line")
	}
}

func TestReleaseOmitsValidUntilByDefault(t *testing.T) {
	out := NewReleaseFileGenerator("o", "l", "stable", "stable").GenerateRelease(nil)
	if strings.Contains(out, "Valid-Until") {
		t.Errorf("unexpected Valid-Until in %q", out)
	}
}
