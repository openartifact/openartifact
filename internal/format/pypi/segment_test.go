// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package pypi

import "testing"

func TestUploadIdentityRejectsPathFilenames(t *testing.T) {
	for _, filename := range []string{"", ".", "..", "a/b-1.0.tar.gz", "a\\b-1.0.tar.gz", "pkg-1.0.tar.gz\x00"} {
		if err := validateUploadIdentity("pkg", "1.0", filename); err == nil {
			t.Errorf("validateUploadIdentity accepted filename %q", filename)
		}
	}

	if err := validateUploadIdentity("pkg", "1.0", "pkg-1.0.tar.gz"); err != nil {
		t.Errorf("validateUploadIdentity rejected a plain filename: %v", err)
	}
}
