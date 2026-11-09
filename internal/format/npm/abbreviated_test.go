// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package npm

import "testing"

func TestIsAbbreviatedFieldInstallMetadata(t *testing.T) {
	for _, key := range []string{"os", "cpu", "libc", "hasInstallScript", "funding", "acceptDependencies", "dist", "bin"} {
		if !isAbbreviatedField([]byte(key)) {
			t.Errorf("%q dropped from the abbreviated form", key)
		}
	}

	for _, key := range []string{"readme", "description", "scripts"} {
		if isAbbreviatedField([]byte(key)) {
			t.Errorf("%q kept in the abbreviated form", key)
		}
	}
}
