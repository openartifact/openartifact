// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import "testing"

func TestRegisterLookup(t *testing.T) {
	Reset()
	t.Cleanup(Reset)

	Register("cargo", Storage{})

	if _, ok := Lookup("cargo"); !ok {
		t.Fatal("cargo is registered but not found")
	}

	if _, ok := Lookup("npm"); ok {
		t.Fatal("npm was never registered")
	}

	if got := Names(); len(got) != 1 || got[0] != "cargo" {
		t.Fatalf("Names = %v, want [cargo]", got)
	}
}
