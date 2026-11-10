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

func TestNewReleaseOmitsValidUntil(t *testing.T) {
	out := NewRelease("stable").Marshal()
	if strings.Contains(string(out), "Valid-Until") {
		t.Errorf("unexpected Valid-Until in %q", out)
	}
}

// Stored control fields keep the deb822 wire form that rows have always had:
// pkgformat's logical values are encoded on the way in and decoded on the way
// out, so a stored multi-line description renders as it was uploaded.
func TestStoredControlFieldsKeepWireForm(t *testing.T) {
	logical := map[string]string{"Description": "short\nfirst\n\nsecond\n indented", "Section": "utils"}

	stored := ControlFieldsToStore(logical)

	const wire = "short\n first\n .\n second\n  indented"
	if stored["Description"] != wire {
		t.Fatalf("stored Description = %q, want %q", stored["Description"], wire)
	}

	got := FormatPackagesEntry(models.DebPackageEntry{
		Name: "p", Version: "1", Arch: "all", ComponentName: "main", Filename: "p_1_all.deb", Size: 1, SHA256: "abc",
		ControlFields: stored,
	})
	if !strings.HasSuffix(got, "Description: "+wire+"\n\n") {
		t.Errorf("stanza does not end with the stored description:\n%s", got)
	}
}
