// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import "testing"

func TestPacmanPathAnyPackageGoesToAnArchitectureDirectory(t *testing.T) {
	for _, tc := range []struct {
		name, asset, want string
	}{
		{"x86_64 from the file name", "foo-1.0-1-x86_64.pkg.tar.zst", "/repos/r/os/x86_64/foo-1.0-1-x86_64.pkg.tar.zst"},
		{"any without a directory", "foo-1.0-1-any.pkg.tar.zst", "/repos/r/os/x86_64/foo-1.0-1-any.pkg.tar.zst"},
		{"any listed under its directory", "repo/os/aarch64/foo-1.0-1-any.pkg.tar.zst", "/repos/r/os/aarch64/foo-1.0-1-any.pkg.tar.zst"},
		{"any listed under os/any", "repo/os/any/foo-1.0-1-any.pkg.tar.zst", "/repos/r/os/x86_64/foo-1.0-1-any.pkg.tar.zst"},
		{"any in another directory", "dir/foo-1.0-1-any.pkg.tar.zst", "/repos/r/os/x86_64/foo-1.0-1-any.pkg.tar.zst"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := pacmanPath("r", tc.asset)
			if !ok || got != tc.want {
				t.Fatalf("pacmanPath(%q) = %q, %v; want %q", tc.asset, got, ok, tc.want)
			}
		})
	}
}
