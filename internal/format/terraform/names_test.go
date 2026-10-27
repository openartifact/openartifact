// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package terraform

import (
	"strings"
	"testing"
)

func TestValidName(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"vpc", true},
		{"aws", true},
		{"my-module_2", true},
		{"A", true},
		{strings.Repeat("a", 64), true},
		{strings.Repeat("a", 65), false},
		{"", false},
		{"-vpc", false},
		{"vpc-", false},
		{"_vpc", false},
		{"a.b", false},
		{"a b", false},
		{"a/b", false},
		{"..", false},
		{"é", false},
		{"a\x00b", false},
	} {
		t.Run(tc.in, func(t *testing.T) {
			if got := validName(tc.in); got != tc.want {
				t.Fatalf("validName(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestReservedNames(t *testing.T) {
	for _, tc := range []struct {
		name         string
		wantUpload   bool
		wantSystemOK bool
	}{
		{name: "packages", wantUpload: false, wantSystemOK: false},
		{name: "Packages", wantUpload: false, wantSystemOK: false},
		{name: "versions", wantUpload: true, wantSystemOK: false},
		{name: "vpc", wantUpload: true, wantSystemOK: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := validUploadName(tc.name); got != tc.wantUpload {
				t.Fatalf("validUploadName = %v, want %v", got, tc.wantUpload)
			}

			if got := validModuleSystem(tc.name); got != tc.wantSystemOK {
				t.Fatalf("validModuleSystem = %v, want %v", got, tc.wantSystemOK)
			}
		})
	}
}

func TestValidVersion(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"1.0.0", true},
		{"0.0.1", true},
		{"10.20.30", true},
		{"1.0.0-rc.1", true},
		{"1.0.0-alpha", true},
		{"1.0.0-0.3.7", true},
		{"v1.0.0", false},
		{"1.0", false},
		{"1", false},
		{"1.0.0+build.5", false},
		{"1.0.0-rc.1+build", false},
		{"01.0.0", false},
		{"1.0.0-", false},
		{"", false},
		{"latest", false},
		{"1.0.0/../x", false},
		{strings.Repeat("1", 101), false},
	} {
		t.Run(tc.in, func(t *testing.T) {
			if got := validVersion(tc.in); got != tc.want {
				t.Fatalf("validVersion(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestValidPlatformPart(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"linux", true}, {"amd64", true}, {"arm64", true}, {"386", true}, {"darwin", true},
		{"", false}, {"Linux", false}, {"linux_amd64", false}, {"a-b", false}, {strings.Repeat("a", 33), false},
	} {
		t.Run(tc.in, func(t *testing.T) {
			if got := validPlatformPart(tc.in); got != tc.want {
				t.Fatalf("validPlatformPart(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestLatestVersion(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   []string
		want string
	}{
		{"empty", nil, ""},
		{"single", []string{"1.0.0"}, "1.0.0"},
		{"numeric not lexical", []string{"1.9.0", "1.10.0", "1.2.0"}, "1.10.0"},
		{"stable beats newer prerelease", []string{"1.0.0", "2.0.0-rc.1"}, "1.0.0"},
		{"only prereleases", []string{"2.0.0-alpha", "2.0.0-beta", "1.0.0-rc.1"}, "2.0.0-beta"},
		{"prerelease below its release", []string{"1.0.0-rc.1", "1.0.0"}, "1.0.0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := latestVersion(tc.in); got != tc.want {
				t.Fatalf("latestVersion(%v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestFileNames(t *testing.T) {
	if got := providerZipName("aws", "1.2.3", "linux", "amd64"); got != "terraform-provider-aws_1.2.3_linux_amd64.zip" {
		t.Fatalf("providerZipName = %q", got)
	}

	if got := providerSumsName("aws", "1.2.3"); got != "terraform-provider-aws_1.2.3_SHA256SUMS" {
		t.Fatalf("providerSumsName = %q", got)
	}

	if got := moduleFileName("vpc", "aws", "1.0.0", "tar.gz"); got != "vpc-aws-1.0.0.tar.gz" {
		t.Fatalf("moduleFileName = %q", got)
	}
}
