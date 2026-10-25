// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package pypi

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/pathseg"
	"github.com/openartifact/openartifact/internal/pypisimple"
)

// FuzzValidateUploadIdentity feeds the name, version and file name of an
// upload to the check that stops a file being stored under another package.
func FuzzValidateUploadIdentity(f *testing.F) {
	f.Add("demo", "1.0", "demo-1.0.tar.gz")
	f.Add("My_Demo", "1.0-1", "my_demo-1.0_1-py3-none-any.whl")
	f.Add("a-b", "2", "a-b-2.zip")
	f.Add("demo", "1.0", "../demo-1.0.tar.gz")
	f.Add("..", "1", "..-1.tar.gz")
	f.Add("", "", "")

	f.Fuzz(func(t *testing.T, name, version, filename string) {
		err := validateUploadIdentity(name, version, filename)
		if err == nil {
			fuzzCheckAcceptedIdentity(t, name, version, filename)
		}

		if strings.ContainsAny(name+version, "/\\\x00") || strings.Trim(name, "-_.") == "" || version == "" {
			return
		}

		// A file named the way build tools name it must be accepted.
		sdist := name + "-" + version + ".tar.gz"
		if err := validateUploadIdentity(name, version, sdist); err != nil {
			t.Fatalf("validateUploadIdentity(%q, %q, %q) = %v", name, version, sdist, err)
		}

		wheel := strings.ReplaceAll(name, "-", "_") + "-" + strings.ReplaceAll(version, "-", "_") + "-py3-none-any.whl"
		if err := validateUploadIdentity(name, version, wheel); err != nil {
			t.Fatalf("validateUploadIdentity(%q, %q, %q) = %v", name, version, wheel, err)
		}
	})
}

func fuzzCheckAcceptedIdentity(t *testing.T, name, version, filename string) {
	t.Helper()

	if !pathseg.Name(filename) ||
		strings.Trim(name, "-_.") == "" || version == "" {
		t.Fatalf("validateUploadIdentity accepted (%q, %q, %q)", name, version, filename)
	}

	dist, ok := splitPyPIFilename(filename, version)
	if !ok || dist == "" || !strings.HasPrefix(filename, dist+"-") || normalizePyPIName(dist) != normalizePyPIName(name) {
		t.Fatalf("accepted (%q, %q, %q) but its distribution is %q (%v)", name, version, filename, dist, ok)
	}
}

// FuzzNormalizePyPIName checks the upload-side name normalization is
// idempotent and agrees with the one the simple API serves.
func FuzzNormalizePyPIName(f *testing.F) {
	f.Add("Foo_Bar.baz")
	f.Add("-._")
	f.Add("\u212a\xff")
	f.Add("")

	f.Fuzz(func(t *testing.T, name string) {
		norm := normalizePyPIName(name)
		if again := normalizePyPIName(norm); again != norm {
			t.Fatalf("normalizePyPIName is not idempotent: %q -> %q -> %q", name, norm, again)
		}

		if want := pypisimple.NormalizeName(name); norm != want {
			t.Fatalf("normalizePyPIName(%q) = %q, but the simple API normalizes it to %q", name, norm, want)
		}
	})
}

func fuzzSign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	}

	return 0
}

// FuzzComparePEP440 checks the version order is a total preorder whatever the
// strings: antisymmetric, transitive, reflexive.
func FuzzComparePEP440(f *testing.F) {
	f.Add("1.0.dev1", "1.0a1", "1.0")
	f.Add("1!0.1", "v1.0.post1", "1.0.0")
	f.Add("1.0+local", "not a version", "")
	f.Add("1.0.post1.dev99999999999999999999", "1.0.post1", "9999999999999999999999999")
	f.Add("1.0-1", "1.0.r2", "1.0RC.1")

	f.Fuzz(func(t *testing.T, a, b, c string) {
		if comparePEP440(a, a) != 0 {
			t.Fatalf("comparePEP440(%q, %q) != 0", a, a)
		}

		for _, pair := range [][2]string{{a, b}, {b, c}, {a, c}} {
			x, y := pair[0], pair[1]
			if fuzzSign(comparePEP440(x, y)) != -fuzzSign(comparePEP440(y, x)) {
				t.Fatalf("comparePEP440 is not antisymmetric for %q and %q", x, y)
			}
		}

		ab, bc, ac := comparePEP440(a, b), comparePEP440(b, c), comparePEP440(a, c)

		if ab <= 0 && bc <= 0 && ac > 0 || ab >= 0 && bc >= 0 && ac < 0 {
			t.Fatalf("comparePEP440 is not transitive for %q, %q, %q (%d, %d, %d)", a, b, c, ab, bc, ac)
		}

		if ab == 0 && fuzzSign(comparePEP440(a, c)) != fuzzSign(comparePEP440(b, c)) {
			t.Fatalf("comparePEP440 treats %q and %q as equal but orders them differently against %q", a, b, c)
		}

		if k, ok := parsePEP440(a); ok && (len(k.release) == 0 || k.preRank < 0 || k.preRank > 4 || k.epoch < 0) {
			t.Fatalf("parsePEP440(%q) = %+v", a, k)
		}
	})
}

// FuzzDeclaredLicenses feeds the license fields of an upload form to the
// declared-license reader.
func FuzzDeclaredLicenses(f *testing.F) {
	f.Add("MIT OR Apache-2.0", "", "License :: OSI Approved :: MIT License\nProgramming Language :: Python", "")
	f.Add("", " BSD ", "  License :: OSI Approved :: BSD License  ", "x")
	f.Add("", strings.Repeat("l", 101), "", "")
	f.Add("", "a\nb", "License ::", "License :: a :: b\r")

	f.Fuzz(func(t *testing.T, expr, lic, classifiers1, classifiers2 string) {
		form := url.Values{
			"license_expression": {expr},
			"license":            {lic},
			"classifiers":        {classifiers1, classifiers2},
		}

		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

		got := declaredLicenses(req)

		candidates := map[string]bool{strings.TrimSpace(expr): true, strings.TrimSpace(lic): true}

		for _, v := range []string{classifiers1, classifiers2} {
			for line := range strings.SplitSeq(v, "\n") {
				candidates[strings.TrimSpace(line)] = true
			}
		}

		for _, l := range got {
			if l == "" || l != strings.TrimSpace(l) || !candidates[l] {
				t.Fatalf("declaredLicenses returned %q, which no field holds", l)
			}
		}

		// A free-text license that is not short and single line is the license
		// text, not a name: it must not stand in for the missing expression.
		if l := strings.TrimSpace(lic); strings.TrimSpace(expr) == "" && l != "" &&
			(len(l) > maxLicenseTextLen || strings.ContainsAny(l, "\r\n")) && slices.Contains(got, l) &&
			!strings.HasPrefix(l, "License ::") {
			t.Fatalf("declaredLicenses took the license text %q as a license name", l)
		}
	})
}
