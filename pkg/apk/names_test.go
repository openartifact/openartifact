// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package apk

import (
	"strings"
	"testing"
)

func TestPackage_FileName(t *testing.T) {
	tests := []struct {
		name string
		pkg  Package
		want string
	}{
		{"plain", Package{Name: "busybox", Version: "1.36.1-r0"}, "busybox-1.36.1-r0.apk"},
		{"name with dashes", Package{Name: "py3-sphinx", Version: "3.2.1-r0"}, "py3-sphinx-3.2.1-r0.apk"},
		{"name ending in a number", Package{Name: "lib-2", Version: "1.0"}, "lib-2-1.0.apk"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.pkg.FileName(); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseFileName(t *testing.T) {
	tests := []struct {
		in      string
		name    string
		version string
		ok      bool
	}{
		{"busybox-1.36.1-r0.apk", "busybox", "1.36.1-r0", true},
		{"py3-sphinx-3.2.1-r0.apk", "py3-sphinx", "3.2.1-r0", true},
		{"lib-2-1.0.apk", "lib-2", "1.0", true},
		{"gtk+3.0-3.24.43-r1.apk", "gtk+3.0", "3.24.43-r1", true},
		{"a-1.apk", "a", "1", true},
		{"openssl-3.3.7-r0.apk", "openssl", "3.3.7-r0", true},
		{"foo-1.0_rc1-r0.apk", "foo", "1.0_rc1-r0", true},
		{"foo-1.0~beta-r0.apk", "foo", "1.0~beta-r0", true},
		{"libc++-1.0-r0.apk", "libc++", "1.0-r0", true},

		{"", "", "", false},
		{".apk", "", "", false},
		{"busybox-1.36.1-r0", "", "", false},
		{"busybox-1.36.1-r0.APK", "", "", false},
		{"busybox-1.36.1-r0.apk.sig", "", "", false},
		{"busybox.apk", "", "", false},
		{"busybox-.apk", "", "", false},
		{"-1.0.apk", "", "", false},
		{"busybox-v1.apk", "", "", false},
		{"busybox-1.0/../x.apk", "", "", false},
		{"../busybox-1.0.apk", "", "", false},
		{"a/b-1.0.apk", "", "", false},
		{"busybox-1.0 .apk", "", "", false},
		{"busybox-1.0\n.apk", "", "", false},
		{"busybox-" + strings.Repeat("1", 129) + ".apk", "", "", false},
		{strings.Repeat("n", 129) + "-1.0.apk", "", "", false},
		{"APKINDEX.tar.gz", "", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			name, version, ok := ParseFileName(tt.in)
			if name != tt.name || version != tt.version || ok != tt.ok {
				t.Fatalf("got (%q, %q, %v), want (%q, %q, %v)", name, version, ok, tt.name, tt.version, tt.ok)
			}
		})
	}
}

// TestParseFileName_roundTrip checks the documented property: every valid
// name and version come back out of their file name.
func TestParseFileName_roundTrip(t *testing.T) {
	names := []string{
		"a", "a-b", "a-1", "a-1-2", "a-1-b-2", "lib-2", "9", "9-9", "A", "a.b", "a_b", "a+b", "a-b-c-d",
		"gtk+3.0", "py3-foo", "x86-64-thing", strings.Repeat("n", 128), "x-" + strings.Repeat("9", 126),
	}
	versions := []string{
		"1", "1.0", "1.0-r0", "1.2.3-r45", "0", "1.0_rc1-r0", "1.0_p20240101-r3", "1.0~beta", "1.0+git20240101-r0",
		"1-r0", "1a-rc", "1.0-r0-r1", "1--r0", "1.0-", "1" + strings.Repeat("0", 127), "1.0-alpha",
	}

	for _, n := range names {
		for _, v := range versions {
			if !validName(n) || !validVersion(v) {
				t.Fatalf("test data %q %q is not valid", n, v)
			}

			p := Package{Name: n, Version: v}

			gotName, gotVersion, ok := ParseFileName(p.FileName())
			if !ok || gotName != n || gotVersion != v {
				t.Errorf("%q: got (%q, %q, %v), want (%q, %q)", p.FileName(), gotName, gotVersion, ok, n, v)
			}
		}
	}
}

func TestValidSegment(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"v3.20", true},
		{"edge", true},
		{"main", true},
		{"x86_64", true},
		{"aarch64", true},
		{"my-repo_1.2", true},
		{"A", true},
		{"9", true},
		{strings.Repeat("a", 64), true},

		{"", false},
		{".", false},
		{"..", false},
		{".hidden", false},
		{"-x", false},
		{"_x", false},
		{"a/b", false},
		{"a\\b", false},
		{"a b", false},
		{"a\x00", false},
		{"a\n", false},
		{"a%2e", false},
		{"é", false},
		{strings.Repeat("a", 65), false},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := ValidSegment(tt.in); got != tt.want {
				t.Fatalf("ValidSegment(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestValidNameVersionArch(t *testing.T) {
	t.Run("name", func(t *testing.T) {
		for in, want := range map[string]bool{"a": true, "Zz9._+-": true, "": false, ".a": false, "_a": false, "a b": false, "a/b": false, "é": false, "a~": false} {
			if got := validName(in); got != want {
				t.Errorf("validName(%q) = %v", in, got)
			}
		}
	})

	t.Run("version", func(t *testing.T) {
		for in, want := range map[string]bool{
			"1": true, "1.2.3-r4": true, "1_rc1": true, "1~x": true, "1+g": true, "": false, "a1": false, "1 2": false, "1/2": false,
			"1-2": false, "1-r0-2": false, "1é": false,
		} {
			if got := validVersion(in); got != want {
				t.Errorf("validVersion(%q) = %v", in, got)
			}
		}
	})

	t.Run("arch", func(t *testing.T) {
		for in, want := range map[string]bool{"x86_64": true, "noarch": true, "armv7": true, "": false, "X86": false, "x-86": false, "a.b": false, strings.Repeat("a", 33): false} {
			if got := validArch(in); got != want {
				t.Errorf("validArch(%q) = %v", in, got)
			}
		}
	})
}
