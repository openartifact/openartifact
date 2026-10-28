// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package repotype

import "testing"

func TestGeneratedGroupMetadata(t *testing.T) {
	t.Parallel()

	for _, f := range []string{"apk", "deb", "pacman", "rpm"} {
		if !GeneratedGroupMetadata(f) || !GroupSupported(f) {
			t.Errorf("%s groups serve generated metadata and must be supported", f)
		}
	}

	for _, f := range []string{"docker", "maven", "npm", "raw", ""} {
		if GeneratedGroupMetadata(f) {
			t.Errorf("%s groups do not generate metadata", f)
		}
	}
}
