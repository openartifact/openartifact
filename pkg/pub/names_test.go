// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package pub

import (
	"slices"
	"testing"
)

func TestValidName(t *testing.T) {
	t.Parallel()

	for name, want := range map[string]bool{
		"http": true, "flutter_bloc": true, "_private": true, "a1": true,
		"": false, "1abc": false, "Http": false, "my-pkg": false, "my.pkg": false, "a b": false,
	} {
		if got := ValidName(name); got != want {
			t.Errorf("ValidName(%q) = %v, want %v", name, got, want)
		}
	}

	if ValidName(string(make([]byte, MaxNameLen+1))) {
		t.Error("an over-long name is valid")
	}

	if !IsReserved("class") || IsReserved("http") {
		t.Error("IsReserved is wrong")
	}
}

func TestValidVersion(t *testing.T) {
	t.Parallel()

	for v, want := range map[string]bool{
		"1.0.0": true, "0.0.1-dev.3": true, "1.2.3+4": true, "1.2.3-rc.1+build.5": true, "0.0.01": true,
		"1.0": false, "v1.0.0": false, "1.0.0-": false, "1.0.0/../x": false, "": false, "1.0.0\t": false,
	} {
		if got := ValidVersion(v); got != want {
			t.Errorf("ValidVersion(%q) = %v, want %v", v, got, want)
		}
	}
}

func TestCompareVersionsOrder(t *testing.T) {
	t.Parallel()

	ordered := []string{
		"0.9.0", "1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta", "1.0.0-beta.2", "1.0.0-beta.11",
		"1.0.0-rc.1", "1.0.0", "1.0.0+1", "1.0.0+2", "1.0.1", "1.10.0", "2.0.0", "10.0.0",
	}

	for i, a := range ordered {
		for j, b := range ordered {
			want := 0
			if i < j {
				want = -1
			} else if i > j {
				want = 1
			}

			if got := CompareVersions(a, b); got != want {
				t.Errorf("CompareVersions(%q, %q) = %d, want %d", a, b, got, want)
			}
		}
	}

	shuffled := []string{"2.0.0", "1.0.0-rc.1", "1.0.0", "0.9.0"}
	slices.SortFunc(shuffled, CompareVersions)

	if want := []string{"0.9.0", "1.0.0-rc.1", "1.0.0", "2.0.0"}; !slices.Equal(shuffled, want) {
		t.Errorf("sorted = %v, want %v", shuffled, want)
	}

	if !IsPrerelease("1.0.0-rc.1") || IsPrerelease("1.0.0+1") {
		t.Error("IsPrerelease is wrong")
	}
}

func TestArchiveName(t *testing.T) {
	t.Parallel()

	file := ArchiveName("flutter_bloc", "8.1.0-dev.1")
	if file != "flutter_bloc-8.1.0-dev.1.tar.gz" {
		t.Fatalf("ArchiveName = %q", file)
	}

	name, version, ok := ParseArchiveName(file)
	if !ok || name != "flutter_bloc" || version != "8.1.0-dev.1" {
		t.Fatalf("ParseArchiveName = %q %q %v", name, version, ok)
	}

	for _, bad := range []string{"x.tar.gz", "x-1.0.tar.gz", "X-1.0.0.tar.gz", "x-1.0.0.zip", "-1.0.0.tar.gz", "../x-1.0.0.tar.gz"} {
		if _, _, ok := ParseArchiveName(bad); ok {
			t.Errorf("ParseArchiveName(%q) accepted", bad)
		}
	}
}
