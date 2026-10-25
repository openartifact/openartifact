// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package rpm

import "testing"

func TestValidateRequiredSegments(t *testing.T) {
	ok := Package{Name: "foo", Version: "1.0", Release: "1.el9", Arch: "x86_64"}
	if err := validateRequired(&ok); err != nil {
		t.Fatalf("plain identity rejected: %v", err)
	}

	for _, bad := range []string{"..", ".", "a/b", "a\\b", "a\x00b", "a b", "a\tb"} {
		for _, field := range []string{"name", "version", "release", "arch"} {
			p := ok

			switch field {
			case "name":
				p.Name = bad
			case "version":
				p.Version = bad
			case "release":
				p.Release = bad
			case "arch":
				p.Arch = bad
			}

			if err := validateRequired(&p); err == nil {
				t.Errorf("validateRequired accepted %s %q", field, bad)
			}
		}
	}
}
