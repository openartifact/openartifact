// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import (
	"slices"
	"testing"
)

func TestRegisterLookup(t *testing.T) {
	Register("cargo", Storage{})

	if _, ok := Lookup("cargo"); !ok {
		t.Fatal("cargo is registered but not found")
	}

	if _, ok := Lookup("npm"); ok {
		t.Fatal("npm was never registered")
	}

	if got := Names(); !slices.Contains(got, "cargo") {
		t.Fatalf("Names = %v, want it to hold cargo", got)
	}
}
