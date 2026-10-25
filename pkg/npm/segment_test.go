// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package npm

import "testing"

func TestParsePathVersionSegment(t *testing.T) {
	for _, rel := range []string{"lodash/..", "lodash/.", "lodash//"} {
		if p, err := ParsePath(rel); err == nil && p.Kind == KindVersion {
			t.Errorf("ParsePath(%q) read %q as a version", rel, p.Version)
		}
	}

	if p, err := ParsePath("lodash/4.17.21"); err != nil || p.Kind != KindVersion || p.Version != "4.17.21" {
		t.Errorf("ParsePath of a version = %+v, %v", p, err)
	}

	for _, file := range []string{"", ".", "..", "a/b.tgz", "a\\b.tgz"} {
		if validFilename(file) {
			t.Errorf("validFilename(%q) = true", file)
		}
	}
}
