// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import "testing"

// The digest is the stored lookup key; it must never change or existing tokens stop working.
func TestLookupDigestStable(t *testing.T) {
	const want = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824" // sha256("hello")

	if got := lookupDigest("hello"); got != want {
		t.Fatalf("lookupDigest = %s, want %s", got, want)
	}
}
