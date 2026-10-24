// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package licensemeta

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

const pypiFuzzMetadata = "Metadata-Version: 2.4\nName: demo\nVersion: 1.0\nLicense-Expression: MIT OR Apache-2.0\n" +
	"License: MIT\nClassifier: License :: OSI Approved :: MIT License\n\nLong description\n"

func pypiFuzzWheel(name, body string) []byte {
	var buf bytes.Buffer

	zw := zip.NewWriter(&buf)

	w, _ := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store})
	_, _ = w.Write([]byte(body))
	_ = zw.Close()

	return buf.Bytes()
}

func pypiFuzzSdist(name, body string) []byte {
	var buf bytes.Buffer

	gw, _ := gzip.NewWriterLevel(&buf, gzip.BestSpeed)
	tw := tar.NewWriter(gw)

	_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg})
	_, _ = tw.Write([]byte(body))
	_ = tw.Close()
	_ = gw.Close()

	return buf.Bytes()
}

// pypiFuzzCheckResult asserts what every license result guarantees.
func pypiFuzzCheckResult(t *testing.T, res Result) {
	t.Helper()

	if res.Empty() != (res.Source == "") {
		t.Fatalf("result %+v: Source must be set exactly when licenses were found", res)
	}

	for _, l := range res.Licenses {
		if l == "" || l != strings.TrimSpace(l) {
			t.Fatalf("result %+v holds an empty or untrimmed license %q", res, l)
		}
	}

	switch res.Source {
	case "":
	case "License-Expression":
		if len(res.Licenses) != 1 {
			t.Fatalf("License-Expression result holds %d licenses", len(res.Licenses))
		}
	case "License":
		l := res.Licenses[0]
		if len(res.Licenses) != 1 || len(l) > maxLicenseFieldLen || strings.ContainsAny(l, "\r\n") || strings.EqualFold(l, "UNKNOWN") {
			t.Fatalf("License result %q is not a short single-line license name", res.Licenses)
		}
	case "classifier":
		for _, l := range res.Licenses {
			if !licenseClassifier(l) {
				t.Fatalf("classifier result %q is not a license classifier", l)
			}
		}
	default:
		t.Fatalf("unknown license source %q", res.Source)
	}
}

// FuzzParsePyPILicenses feeds arbitrary METADATA / PKG-INFO text to the
// license reader.
func FuzzParsePyPILicenses(f *testing.F) {
	f.Add(pypiFuzzMetadata)
	f.Add("License: UNKNOWN\nClassifier: License :: OSI Approved\nClassifier: License :: OSI Approved :: BSD License\n")
	f.Add("License-Expression:\n MIT\nLicense: " + strings.Repeat("x", 101) + "\n")
	f.Add("license: a\n b\r\n\r\nLicense-Expression: late\n")
	f.Add(" continuation first\nno colon\nLicense : spaced\n")
	f.Add("")

	f.Fuzz(func(t *testing.T, text string) {
		res := parsePyPI(text)

		pypiFuzzCheckResult(t, res)

		// Nothing after the blank line that ends the header block, the long
		// description, can change what the package declares.
		withBody := parsePyPI(text + "\n\nLicense-Expression: injected\nLicense: injected\n")
		if withBody.Source != res.Source || strings.Join(withBody.Licenses, "\x00") != strings.Join(res.Licenses, "\x00") {
			t.Fatalf("a description changed the licenses: %+v -> %+v", res, withBody)
		}

		for key := range parseHeaders(text) {
			if key != strings.ToLower(key) || key != strings.TrimSpace(key) {
				t.Fatalf("parseHeaders returned the non-canonical key %q", key)
			}
		}
	})
}

// FuzzReadPyPI feeds an arbitrary file to the license reader under the file
// names of the PyPI distribution kinds.
func FuzzReadPyPI(f *testing.F) {
	f.Add(pypiFuzzWheel("demo-1.0.dist-info/METADATA", pypiFuzzMetadata), "demo-1.0-py3-none-any.whl")
	f.Add(pypiFuzzWheel("demo-1.0/PKG-INFO", pypiFuzzMetadata), "demo-1.0.zip")
	f.Add(pypiFuzzWheel("EGG-INFO/PKG-INFO", pypiFuzzMetadata), "demo-1.0.egg")
	f.Add(pypiFuzzSdist("demo-1.0/PKG-INFO", pypiFuzzMetadata), "demo-1.0.tar.gz")
	f.Add(pypiFuzzSdist("demo-1.0/PKG-INFO", pypiFuzzMetadata), "demo-1.0.tar.bz2")
	f.Add(pypiFuzzSdist("../PKG-INFO", pypiFuzzMetadata), "demo-1.0.tgz")
	f.Add([]byte("BZh9"), "demo-1.0.tar.bz2")
	f.Add([]byte("PK\x03\x04"), "demo.whl")
	f.Add([]byte{}, "")

	f.Fuzz(func(t *testing.T, data []byte, filename string) {
		res, err := ReadBytes(context.Background(), "pypi", filename, data)
		if err != nil {
			if Supported("pypi", filename) == errors.Is(err, ErrUnsupported) {
				t.Fatalf("ReadBytes(%q) = %v, but Supported = %v", filename, err, Supported("pypi", filename))
			}

			return
		}

		pypiFuzzCheckResult(t, res)
	})
}

// FuzzReadPyPIMetadata wraps arbitrary METADATA text in a wheel and in an
// sdist: both read the header block exactly as the text parser does.
func FuzzReadPyPIMetadata(f *testing.F) {
	f.Add(pypiFuzzMetadata)
	f.Add("License: MIT\r\n\r\nbody")
	f.Add("\n\nLicense: late\n")
	f.Add("Classifier: License :: a :: b\nClassifier: License :: c")
	f.Add("")

	f.Fuzz(func(t *testing.T, text string) {
		if len(text) > memberLimit/2 {
			return
		}

		want := parsePyPI(text)

		for filename, data := range map[string][]byte{
			"demo-1.0-py3-none-any.whl": pypiFuzzWheel("demo-1.0.dist-info/METADATA", text),
			"demo-1.0.tar.gz":           pypiFuzzSdist("demo-1.0/PKG-INFO", text),
		} {
			res, err := ReadBytes(context.Background(), "pypi", filename, data)

			// The extractor repairs invalid UTF-8 and drops NULs before the
			// header block is parsed.
			if !utf8.ValidString(text) || strings.Contains(text, "\x00") {
				continue
			}

			switch {
			case errors.Is(err, ErrNoMetadata):
				if !want.Empty() {
					t.Fatalf("%s: no metadata for a header block that declares %+v", filename, want)
				}
			case err != nil:
				t.Fatalf("%s: %v", filename, err)
			case res.Source != want.Source || strings.Join(res.Licenses, "\x00") != strings.Join(want.Licenses, "\x00"):
				t.Fatalf("%s: read %+v, text parser read %+v", filename, res, want)
			}
		}
	})
}
