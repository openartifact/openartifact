// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"errors"
	"strings"
	"testing"
)

// TestManifestTypeNotAnObject: only a JSON object is a manifest; null decodes
// into a struct without error and used to be cached under the Content-Type the
// upstream named.
func TestManifestTypeNotAnObject(t *testing.T) {
	t.Parallel()

	const header = "application/vnd.oci.image.manifest.v1+json"

	for _, body := range []string{"null", " null\n", "[]", "5", `"x"`, "true", ""} {
		if mt, err := manifestType(header, strings.NewReader(body)); !errors.Is(err, errBadManifest) {
			t.Errorf("manifestType(%q) = %q, %v, want errBadManifest", body, mt, err)
		}
	}

	if mt, err := manifestType(header, strings.NewReader("{}")); err != nil || mt != header {
		t.Errorf("manifestType({}) = %q, %v, want %q", mt, err, header)
	}
}
