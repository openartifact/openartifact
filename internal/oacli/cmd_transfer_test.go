// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package oacli

import (
	"strings"
	"testing"
)

func TestParsePackageVersionSplitsAtTheLastAt(t *testing.T) {
	tests := []struct {
		in, pkg, ver string
		ok           bool
	}{
		{"com.acme:core@1.4.0", "com.acme:core", "1.4.0", true},
		{"@scope/pkg@2.0.0-rc.1", "@scope/pkg", "2.0.0-rc.1", true},
		{"nover@", "", "", false},
		{"@1.0", "", "", false},
		{"plain", "", "", false},
	}

	for _, tc := range tests {
		got, err := parsePackageVersion(tc.in)
		if (err == nil) != tc.ok || got.Package != tc.pkg || got.Version != tc.ver {
			t.Errorf("%q = %+v, %v; want %q %q ok=%v", tc.in, got, err, tc.pkg, tc.ver, tc.ok)
		}
	}
}

func TestTransferItemsCombinesVersionsAndDigest(t *testing.T) {
	items, err := transferItems([]string{"a@1", "b@2"}, strings.Repeat("a", 64), "x/y")
	if err != nil || len(items) != 3 || items[2].SHA256 == "" || items[2].Path != "x/y" {
		t.Fatalf("items = %+v, %v", items, err)
	}

	if _, err := transferItems(nil, "", ""); err == nil {
		t.Error("no items: want an error")
	}

	if _, err := transferItems([]string{"a@1"}, "", "x"); err == nil {
		t.Error("--path without --sha256: want an error")
	}
}

func TestTransferOutTextSaysWhenNothingWasWritten(t *testing.T) {
	out := transferOut{Operation: "move", Source: "a", Target: "b", DryRun: true, Copied: 1}
	if got := out.text(); !strings.Contains(got, "Dry run") || strings.Contains(got, "trash") {
		t.Errorf("text = %q", got)
	}

	out.DryRun, out.Moved = false, 1
	if got := out.text(); !strings.Contains(got, "1 moved to the source's trash") {
		t.Errorf("text = %q", got)
	}
}
