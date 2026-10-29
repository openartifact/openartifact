// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package repotype

import "testing"

func TestPURLEscape(t *testing.T) {
	t.Parallel()

	for in, want := range map[string]string{
		"openssl-1.0_a~b": "openssl-1.0_a~b",
		"1:2.0":           "1%3A2.0",
		"a b/c":           "a%20b%2Fc",
		"é":               "%C3%A9",
	} {
		if got := PURLEscape(in); got != want {
			t.Errorf("PURLEscape(%q) = %q, want %q", in, got, want)
		}
	}
}
