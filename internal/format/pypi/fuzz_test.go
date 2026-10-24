// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package pypi

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/pypisimple"
)

const fuzzMetadata = "Metadata-Version: 2.1\nName: demo\nVersion: 1.0\nSummary: A demo\nAuthor: Jane\nAuthor-Email: j@example.org\n" +
	"License: MIT\nKeywords: a,b\nRequires-Python: >=3.8\nRequires-Dist: requests (>=2)\n\nLong description\n"

// fuzzMetadataHeader maps a key parseMetadata returns to the header it reads
// it from.
func fuzzMetadataHeader(key string) (string, bool) {
	switch key {
	case "name", "version", "summary", "author", "license", "keywords":
		return strings.ToUpper(key[:1]) + key[1:], true
	case "author_email":
		return "Author-Email", true
	case "requires_python":
		return "Requires-Python", true
	}

	return "", false
}

// fuzzWheel builds a stored (uncompressed) zip with one member.
func fuzzWheel(name, body string) []byte {
	var buf bytes.Buffer

	zw := zip.NewWriter(&buf)

	w, _ := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store})
	_, _ = w.Write([]byte(body))
	_ = zw.Close()

	return buf.Bytes()
}

// fuzzSdist builds a tar.gz with one regular member.
func fuzzSdist(name, body string) []byte {
	var buf bytes.Buffer

	gw, _ := gzip.NewWriterLevel(&buf, gzip.BestSpeed)
	tw := tar.NewWriter(gw)

	_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg})
	_, _ = tw.Write([]byte(body))
	_ = tw.Close()
	_ = gw.Close()

	return buf.Bytes()
}

// fuzzCheckMetadata asserts a parsed metadata map only holds the known string
// fields, and that writing it out and reading it back is the identity.
func fuzzCheckMetadata(t *testing.T, meta map[string]any) {
	t.Helper()

	var doc strings.Builder

	for _, key := range slices.Sorted(maps.Keys(meta)) {
		header, known := fuzzMetadataHeader(key)

		val, isString := meta[key].(string)
		if !known || !isString || strings.Contains(val, "\n") {
			t.Fatalf("metadata field %q = %#v is not a single-line known string", key, meta[key])
		}

		doc.WriteString(header + ": " + val + "\n")
	}

	back, err := parseMetadata(strings.NewReader(doc.String()))
	if err != nil {
		t.Fatalf("parseMetadata(rendered): %v", err)
	}

	if !reflect.DeepEqual(back, meta) {
		t.Fatalf("metadata round trip changed the fields:\n%#v\n%#v", meta, back)
	}
}

// FuzzHandlerParse feeds arbitrary bytes to the upload validator and parser,
// which dispatch on the zip and gzip magic to the wheel and sdist readers.
func FuzzHandlerParse(f *testing.F) {
	f.Add(fuzzWheel("demo-1.0.dist-info/METADATA", fuzzMetadata))
	f.Add(fuzzWheel("demo-1.0.dist-info/WHEEL", "Wheel-Version: 1.0\nRoot-Is-Purelib: true\n"))
	f.Add(fuzzWheel("METADATA", fuzzMetadata))
	f.Add(fuzzSdist("demo-1.0/PKG-INFO", fuzzMetadata))
	f.Add(fuzzSdist("PKG-INFO", "Name: x\nVersion: 2\n"))
	f.Add(fuzzSdist("demo-1.0/setup.py", "print(1)"))
	f.Add(fuzzWheel("demo-1.0.dist-info/METADATA", fuzzMetadata)[:40])
	f.Add([]byte{0x1f, 0x8b})
	f.Add([]byte("PK\x03\x04"))
	f.Add([]byte("garbage"))
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, data []byte) {
		h := NewPyPIHandler()
		ctx := context.Background()

		magic := isValidWheel(data) || isValidSdist(data)

		if err := h.Validate(ctx, bytes.NewReader(data)); (err == nil) != magic {
			t.Fatalf("Validate = %v for a package with magic=%v", err, magic)
		}

		meta, err := h.Parse(ctx, bytes.NewReader(data))
		if !magic {
			if err == nil {
				t.Fatal("Parse accepted data that is neither a wheel nor an sdist")
			}

			return
		}

		if err == nil {
			fuzzCheckMetadata(t, meta)
		}
	})
}

// FuzzParseMetadata feeds arbitrary METADATA / PKG-INFO text, directly and
// inside a wheel and an sdist, which must all read the same fields.
func FuzzParseMetadata(f *testing.F) {
	f.Add([]byte(fuzzMetadata))
	f.Add([]byte("Name: a\nName: b\nVersion: \nSummary:x\n\r\nAuthor: \r\n"))
	f.Add([]byte("License: MIT\n\nName: late\n"))
	f.Add([]byte("Author-email: lower\nAuthor-Email: upper\n"))
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > maxPyPIMetadataBytes {
			return
		}

		direct, err := parseMetadata(bytes.NewReader(data))
		if err != nil {
			t.Fatalf("parseMetadata: %v", err)
		}

		fuzzCheckMetadata(t, direct)

		wheel, err := parseWheel(fuzzWheel("demo-1.0.dist-info/METADATA", string(data)))
		if err != nil || !reflect.DeepEqual(wheel, direct) {
			t.Fatalf("wheel metadata = %#v (%v), want %#v", wheel, err, direct)
		}

		sdist, err := parseSdist(fuzzSdist("demo-1.0/PKG-INFO", string(data)))
		if err != nil || !reflect.DeepEqual(sdist, direct) {
			t.Fatalf("sdist metadata = %#v (%v), want %#v", sdist, err, direct)
		}
	})
}

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

		if strings.ContainsAny(name+version, `/\`) || strings.Trim(name, "-_.") == "" || version == "" {
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

	if strings.ContainsAny(filename, `/\`) || filename == "" || filename == "." || filename == ".." ||
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
