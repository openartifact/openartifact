// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package deb

import (
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
)

// apt matches field names case-insensitively: a lowercase "filename:" or
// "sha256:" in an uploaded control stanza must not reach the Packages entry.
func TestControlFieldsCaseInsensitiveOwnedFields(t *testing.T) {
	in := map[string]string{
		"filename": "pool/main/e/evil/evil.deb", "SHA256": "dead", "sha256": "beef", "size": "1",
		"md5SUM": "x", "Section": "admin", "x-custom": "ok",
	}

	stored := ControlFieldsToStore(in)
	for _, k := range []string{"filename", "Filename", "sha256", "SHA256", "size", "Size", "md5SUM", "MD5sum"} {
		if _, ok := stored[k]; ok {
			t.Errorf("owned field %q was stored", k)
		}
	}

	if stored["Section"] != "admin" || stored["x-custom"] != "ok" {
		t.Errorf("legitimate fields lost: %v", stored)
	}

	// A row stored before the fix (lowercase owned fields kept) still renders once.
	entry := FormatPackagesEntry(models.DebPackageEntry{
		Name: "pkg", Version: "1", Arch: "amd64", ComponentName: "main", Filename: "pkg_1_amd64.deb",
		Size: 10, SHA256: "abc", ControlFields: in,
	})

	for _, k := range []string{"\nfilename:", "\nsha256:", "\nsize:", "\nmd5SUM:", "\nmd5sum:"} {
		if strings.Contains(entry, k) {
			t.Errorf("entry has non-canonical owned field %q:\n%s", k, entry)
		}
	}

	if strings.Count(entry, "Filename:") != 1 || strings.Count(entry, "SHA256: abc") != 1 {
		t.Errorf("entry should carry exactly the generated Filename/SHA256:\n%s", entry)
	}
}
