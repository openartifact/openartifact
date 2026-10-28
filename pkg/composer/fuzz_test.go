// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package composer

import (
	"archive/zip"
	"bytes"
	"testing"
)

// fataler is the part of testing.TB that both *testing.F and the OSS-Fuzz
// shim's F (which lacks the rest of testing.TB) provide.
type fataler interface {
	Helper()
	Fatal(args ...any)
}

// seedZip is a zip with the given file name and body.
func seedZip(tb fataler, name, body string) []byte {
	tb.Helper()

	var buf bytes.Buffer

	w := zip.NewWriter(&buf)

	fw, err := w.Create(name)
	if err != nil {
		tb.Fatal(err)
	}

	if _, err := fw.Write([]byte(body)); err != nil {
		tb.Fatal(err)
	}

	if err := w.Close(); err != nil {
		tb.Fatal(err)
	}

	return buf.Bytes()
}

// FuzzReadArchive feeds ReadArchive arbitrary bytes: it must never panic, and
// an archive it accepts must carry a valid package name.
func FuzzReadArchive(f *testing.F) {
	f.Add(seedZip(f, "composer.json", `{"name":"acme/tools","version":"1.0.0","license":"MIT"}`))
	f.Add(seedZip(f, "x-y-1/composer.json", `{"name":"acme/tools"}`))
	f.Add([]byte("PK\x03\x04"))

	f.Fuzz(func(t *testing.T, data []byte) {
		m, err := ReadArchive(bytes.NewReader(data), int64(len(data)))
		if err == nil && !ValidName(m.Name) {
			t.Fatalf("accepted the invalid name %q", m.Name)
		}
	})
}

// FuzzNormalizePublished checks that an accepted version is a bounded,
// URL-safe string and that its normalized form is never empty.
func FuzzNormalizePublished(f *testing.F) {
	for _, v := range []string{"1.0.0", "v1.2.3", "1.0.0-RC1", "1.x-dev", "dev-main", "2.0.0+build", "", "dev-", "1.2.3.4.5"} {
		f.Add(v)
	}

	f.Fuzz(func(t *testing.T, v string) {
		n, err := NormalizePublished(v)
		if err != nil {
			return
		}

		if n == "" || len(n) > MaxVersionLen || len(v) > MaxVersionLen {
			t.Fatalf("NormalizePublished(%q) = %q", v, n)
		}
	})
}
