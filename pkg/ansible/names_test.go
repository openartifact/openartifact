// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package ansible_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/pkg/ansible"
)

func TestValidName(t *testing.T) {
	for name, want := range map[string]bool{
		"community": true, "a_b2": true, "_x": true, "": false, "1abc": false, "Abc": false, "a-b": false, "a.b": false,
		"é": false, strings.Repeat("a", 64): true, strings.Repeat("a", 65): false,
	} {
		if got := ansible.ValidName(name); got != want {
			t.Errorf("ValidName(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestValidVersion(t *testing.T) {
	for v, want := range map[string]bool{
		"1.0.0": true, "0.0.1-alpha.1": true, "1.0.0+build.5": true, "1.0.0-rc.1+b": true,
		"1.0": false, "01.0.0": false, "v1.0.0": false, "1.0.0-": false, "1.0.0-01": false, "": false, "1.0.0\n": false,
	} {
		if got := ansible.ValidVersion(v); got != want {
			t.Errorf("ValidVersion(%q) = %v, want %v", v, got, want)
		}
	}
}

func TestCompareVersions(t *testing.T) {
	ordered := []string{"1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-beta", "1.0.0", "1.0.1", "1.10.0", "2.0.0"}
	shuffled := []string{"2.0.0", "1.0.0", "1.10.0", "1.0.0-beta", "1.0.1", "1.0.0-alpha.1", "1.0.0-alpha"}

	slices.SortFunc(shuffled, ansible.CompareVersions)

	if !slices.Equal(shuffled, ordered) {
		t.Errorf("sorted = %v, want %v", shuffled, ordered)
	}

	if ansible.CompareVersions("1.0.0+a", "1.0.0+b") >= 0 || ansible.CompareVersions("1.0.0+b", "1.0.0+b") != 0 {
		t.Error("versions equal by precedence must be ordered by their text")
	}

	if !ansible.IsPrerelease("1.0.0-rc.1") || ansible.IsPrerelease("1.0.0+b") {
		t.Error("IsPrerelease misreads the version")
	}
}

func TestFileName(t *testing.T) {
	file := ansible.FileName("acme", "tools", "1.0.0-rc.1+b")
	if file != "acme-tools-1.0.0-rc.1+b.tar.gz" {
		t.Fatalf("FileName = %q", file)
	}

	ns, name, version, ok := ansible.ParseFileName(file)
	if !ok || ns != "acme" || name != "tools" || version != "1.0.0-rc.1+b" {
		t.Errorf("ParseFileName = %q %q %q %v", ns, name, version, ok)
	}

	for _, bad := range []string{"acme-tools-1.0.0.zip", "acme-tools.tar.gz", "Acme-tools-1.0.0.tar.gz", "acme-tools-1.0.tar.gz", "../a-b-1.0.0.tar.gz"} {
		if _, _, _, ok := ansible.ParseFileName(bad); ok {
			t.Errorf("ParseFileName(%q) accepted", bad)
		}
	}
}

func TestSplitFQCN(t *testing.T) {
	if ns, name, ok := ansible.SplitFQCN("ansible.posix"); !ok || ns != "ansible" || name != "posix" {
		t.Errorf("SplitFQCN = %q %q %v", ns, name, ok)
	}

	for _, bad := range []string{"posix", "a.b.c", ".b", "a.", "A.b"} {
		if _, _, ok := ansible.SplitFQCN(bad); ok {
			t.Errorf("SplitFQCN(%q) accepted", bad)
		}
	}
}

func TestValidRequirement(t *testing.T) {
	for spec, want := range map[string]bool{
		"*": true, "1.0.0": true, ">=1.0.0": true, ">=1.0.0,<2.0.0": true, ">= 1.0.0, != 1.5.0": true,
		"": false, "@@": false, ">=": false, "1.0.0,,2": false, strings.Repeat("1", 300): false,
	} {
		if got := ansible.ValidRequirement(spec); got != want {
			t.Errorf("ValidRequirement(%q) = %v, want %v", spec, got, want)
		}
	}
}
