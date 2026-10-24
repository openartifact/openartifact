// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package conda

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"math"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/klauspost/compress/zstd"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
)

const fuzzIndexJSON = `{"name":"demo","version":"1.0","build":"py_0","build_number":3,` +
	`"depends":["python >=3.8","numpy"],"constrains":["scipy <2"],"license":" MIT ","license_family":"MIT",` +
	`"noarch":"python","timestamp":1700000000123,"arch":null,"platform":"linux","features":"","track_features":["a","b"]}`

// fuzzTar builds an uncompressed tar whose entries hold the given name/content pairs.
func fuzzTar(entries ...[2]string) []byte {
	var buf bytes.Buffer

	tw := tar.NewWriter(&buf)

	for _, e := range entries {
		if err := tw.WriteHeader(&tar.Header{Name: e[0], Mode: 0o644, Size: int64(len(e[1]))}); err != nil {
			continue
		}

		_, _ = tw.Write([]byte(e[1]))
	}

	_ = tw.Close()

	return buf.Bytes()
}

// fuzzConda builds a .conda archive (a zip holding info-*.tar.zst) around the
// given info/index.json.
func fuzzConda(indexJSON string) []byte {
	var zst bytes.Buffer

	if enc, err := zstd.NewWriter(&zst); err == nil {
		_, _ = enc.Write(fuzzTar([2]string{"info/index.json", indexJSON}))
		_ = enc.Close()
	}

	var buf bytes.Buffer

	zw := zip.NewWriter(&buf)
	fw, _ := zw.Create("info-demo-1.0-py_0.tar.zst")
	_, _ = fw.Write(zst.Bytes())
	_ = zw.Close()

	return buf.Bytes()
}

// fuzzDBText reports whether v, a decoded JSON value, only holds strings (and
// object keys) PostgreSQL text and jsonb accept: no NUL.
func fuzzDBText(v any) bool {
	switch v := v.(type) {
	case string:
		return utf8.ValidString(v) && !strings.ContainsRune(v, 0)
	case []any:
		return !slices.ContainsFunc(v, func(e any) bool { return !fuzzDBText(e) })
	case map[string]any:
		for k, e := range v {
			if !fuzzDBText(k) || !fuzzDBText(e) {
				return false
			}
		}
	}

	return true
}

// fuzzCheckIndex checks the invariants of a parsed packageIndex: every field
// is within what the conda_packages columns hold and what repodata.json carries.
func fuzzCheckIndex(t *testing.T, idx packageIndex) {
	t.Helper()

	if idx.BuildNumber < 0 || idx.BuildNumber > math.MaxInt32 || idx.Timestamp < 0 {
		t.Fatalf("build number %d / timestamp %d out of range", idx.BuildNumber, idx.Timestamp)
	}

	if idx.Depends == nil {
		t.Fatal("nil depends, the column is NOT NULL")
	}

	if idx.License != strings.TrimSpace(idx.License) {
		t.Fatalf("license %q is not trimmed", idx.License)
	}

	for _, d := range idx.Depends {
		if !fuzzDBText(d) {
			t.Fatalf("dependency %q is not database text", d)
		}
	}

	var extra map[string]json.RawMessage
	if err := json.Unmarshal(idx.Extra, &extra); err != nil || extra == nil {
		t.Fatalf("extra %q is not a JSON object: %v", idx.Extra, err)
	}

	if !utf8.Valid(idx.Extra) {
		t.Fatalf("extra %q is not valid UTF-8, jsonb refuses it", idx.Extra)
	}

	var decoded any
	if err := json.Unmarshal(idx.Extra, &decoded); err != nil || !fuzzDBText(decoded) {
		t.Fatalf("extra %q holds text jsonb refuses (%v)", idx.Extra, err)
	}

	for k, v := range extra {
		if !slices.Contains(repodataExtraKeys(), k) || string(v) == "null" {
			t.Fatalf("extra key %q = %s is not a repodata field", k, v)
		}
	}
}

// FuzzParsePackageFilename feeds arbitrary file names to the conda file name
// parser behind every upload, download and delete.
func FuzzParsePackageFilename(f *testing.F) {
	f.Add("demo-1.0-py_0.tar.bz2")
	f.Add("scikit-learn-1.0-py39_0.conda")
	f.Add("a-b-c-d-e.conda")
	f.Add("-1.0-py_0.conda")
	f.Add("demo-1.0-.tar.bz2")
	f.Add("demo-1.0-py_0.tar.bz2.conda")
	f.Add("..-1-0.conda")
	f.Add("a\x00-1-0.conda")
	f.Add("repodata.json")
	f.Add("")

	f.Fuzz(func(t *testing.T, filename string) {
		file, ok := parsePackageFilename(filename)
		if !ok {
			return
		}

		if got := file.name + "-" + file.version + "-" + file.build + file.ext; got != filename {
			t.Fatalf("parsePackageFilename(%q) = %+v, which does not rebuild the name (%q)", filename, file, got)
		}

		pkg := models.CondaPackage{Name: file.name, Version: file.version, BuildString: file.build, Extension: file.ext}
		if pkg.Filename() != filename {
			t.Fatalf("stored %+v is listed as %q, uploaded as %q", file, pkg.Filename(), filename)
		}

		if again, ok := parsePackageFilename(pkg.Filename()); !ok || again != file {
			t.Fatalf("%q is parsed as %+v, then %+v (%v)", filename, file, again, ok)
		}

		if file.ext != models.CondaExtTarBz2 && file.ext != models.CondaExtConda {
			t.Fatalf("%q has extension %q", filename, file.ext)
		}

		if strings.Contains(file.version, "-") || strings.Contains(file.build, "-") {
			t.Fatalf("%q: a version or build with a hyphen: %+v", filename, file)
		}

		if len(file.name) > maxNameLen || len(file.version) > maxVersionLen || len(file.build) > maxBuildLen {
			t.Fatalf("%q does not fit the columns: %+v", filename, file)
		}

		if !fuzzDBText(filename) || strings.ContainsAny(filename, "/\\") || path.Base(filename) != filename {
			t.Fatalf("%q is not a plain database text file name", filename)
		}

		for _, r := range filename {
			if r < ' ' || r == 0x7f {
				t.Fatalf("%q holds the control character %q", filename, r)
			}
		}
	})
}

// FuzzParsePackageIndex feeds arbitrary info/index.json documents to the
// index reader, whose result is stored in conda_packages.
func FuzzParsePackageIndex(f *testing.F) {
	f.Add([]byte(fuzzIndexJSON))
	f.Add([]byte(`{"depends":null,"build_number":-1,"timestamp":1e300,"license":5}`))
	f.Add([]byte(`{"build_number":2147483648,"timestamp":9.3e18,"constrains":null}`))
	f.Add([]byte(`[]`))
	f.Add([]byte(``))

	f.Fuzz(func(t *testing.T, raw []byte) {
		idx := parsePackageIndex(raw)

		fuzzCheckIndex(t, idx)

		// A value is parsed the same whether it is the only field or one of many.
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) != nil && (idx.BuildNumber != 0 || idx.Timestamp != 0 || idx.License != "" || len(idx.Depends) != 0 || string(idx.Extra) != "{}") {
			t.Fatalf("%q is not an object but parsed as %+v", raw, idx)
		}
	})
}

// FuzzReadPackageIndex feeds arbitrary archive bytes, as .tar.bz2 and as
// .conda, to the package reader run on every upload.
func FuzzReadPackageIndex(f *testing.F) {
	if demo, err := base64.StdEncoding.DecodeString(demoTarBz2); err == nil {
		f.Add(false, demo)
	}

	f.Add(true, fuzzConda(fuzzIndexJSON))
	f.Add(true, fuzzConda(`{"depends":["x"]}`))
	f.Add(true, []byte("PK\x05\x06"))
	f.Add(false, []byte("BZh9"))
	f.Add(true, []byte{})

	f.Fuzz(func(t *testing.T, isConda bool, data []byte) {
		ext := models.CondaExtTarBz2
		if isConda {
			ext = models.CondaExtConda
		}

		idx := readPackageIndex(ext, bytes.NewReader(data), int64(len(data)))

		fuzzCheckIndex(t, idx)
	})
}

// FuzzIndexFromTar feeds arbitrary tar streams with arbitrary work limits to
// the info/index.json finder: limits only ever lose a result, never change it.
func FuzzIndexFromTar(f *testing.F) {
	f.Add(fuzzTar([2]string{"info/about.json", "{}"}, [2]string{"./info//index.json", fuzzIndexJSON}), uint16(4096), uint8(4))
	f.Add(fuzzTar([2]string{"info/index.json", fuzzIndexJSON}), uint16(600), uint8(1))
	f.Add(fuzzTar([2]string{"info/index.json", fuzzIndexJSON}), uint16(0), uint8(0))
	f.Add(fuzzTar([2]string{"info/index.json", fuzzIndexJSON}, [2]string{"info/index.json", "{}"}), uint16(65535), uint8(255))
	f.Add([]byte("not a tar"), uint16(10), uint8(10))

	f.Fuzz(func(t *testing.T, data []byte, limBytes uint16, limEntries uint8) {
		lim := tarLimits{bytes: int64(limBytes), entries: int(limEntries)}

		got := indexFromTar(bytes.NewReader(data), lim)
		if len(got) > maxIndexJSONBytes {
			t.Fatalf("returned %d bytes, over the %d cap", len(got), maxIndexJSONBytes)
		}

		want := indexFromTar(bytes.NewReader(data), tarLimits{bytes: math.MaxInt64, entries: math.MaxInt32})

		switch {
		case got != nil && !bytes.Equal(got, want):
			t.Fatalf("limits %+v changed the result: %q, unlimited %q", lim, got, want)
		case got == nil && want != nil && lim.bytes >= int64(len(data)) && lim.entries >= len(data)/512+1:
			t.Fatalf("limits %+v lost a result that fits them: %q", lim, want)
		}
	})
}

// fuzzColumns checks a stored package against the conda_packages column
// definitions (migration 001): a longer or non-text value is a database error,
// answered with a 500 instead of a refusal.
func fuzzColumns(t *testing.T, pkg *models.CondaPackage) {
	t.Helper()

	for _, c := range []struct {
		name  string
		value string
		limit int
	}{
		{"name", pkg.Name, maxNameLen}, {"version", pkg.Version, maxVersionLen},
		{"build_string", pkg.BuildString, maxBuildLen}, {"platform", pkg.Platform, 50},
	} {
		if utf8.RuneCountInString(c.value) > c.limit || !fuzzDBText(c.value) {
			t.Fatalf("stored %s %q does not fit varchar(%d)", c.name, c.value, c.limit)
		}
	}
}

// FuzzUploadRoundTrip uploads a package under an arbitrary platform and file
// name through the real routes: a refusal is never a server error, and what is
// accepted can be downloaded, is listed in repodata.json and can be deleted.
func FuzzUploadRoundTrip(f *testing.F) {
	f.Add("linux-64", "demo-1.0-py_0.tar.bz2", []byte("data"))
	f.Add("noarch", "scikit-learn-1.0-py39_0.conda", fuzzConda(fuzzIndexJSON))
	f.Add("linux-64", "a;b-1+x-0.conda", []byte("x"))
	f.Add("a b", "a b-1-0.tar.bz2", []byte(""))
	f.Add("osx-64", "demo-1.0-"+strings.Repeat("b", 256)+".conda", []byte("x"))
	f.Add("noarch", "demo-1.0-0.conda\xff", []byte("x"))
	f.Add("repodata.json", "demo-1.0-0.conda", []byte("x"))

	f.Fuzz(func(t *testing.T, platform, filename string, body []byte) {
		if len(body) > 1<<16 {
			return
		}

		h, db, _, _ := newTestHandler(t)

		escaped := (&url.URL{Path: "/repos/myrepo/" + platform}).EscapedPath()
		upload := formattest.Request(http.MethodPost, escaped+"?filename="+url.QueryEscape(filename), bytes.NewReader(body))

		w := formattest.Do(h.RegisterRepoRoutes, upload)
		if w.Code >= http.StatusInternalServerError {
			t.Fatalf("upload of %q to %q answered %d: %s", filename, platform, w.Code, w.Body.String())
		}

		if w.Code != http.StatusCreated {
			return
		}

		if db.pkg == nil {
			t.Fatal("upload accepted but nothing stored")
		}

		fuzzColumns(t, db.pkg)
		fuzzCheckStored(t, h, db.pkg, filename, body)

		target := (&url.URL{Path: "/repos/myrepo/" + platform + "/" + filename}).EscapedPath()

		if del := formattest.Do(h.RegisterRepoRoutes, formattest.Request(http.MethodDelete, target, nil)); del.Code != http.StatusNoContent {
			t.Fatalf("delete of %q answered %d", target, del.Code)
		}

		if get := formattest.Do(h.RegisterRepoRoutes, formattest.Request(http.MethodGet, target, nil)); get.Code != http.StatusNotFound {
			t.Fatalf("deleted %q still answers %d", target, get.Code)
		}
	})
}

// fuzzCheckStored downloads the package stored by an upload and looks it up in
// repodata.json.
func fuzzCheckStored(t *testing.T, h *HTTPHandler, pkg *models.CondaPackage, filename string, body []byte) {
	t.Helper()

	target := (&url.URL{Path: "/repos/myrepo/" + pkg.Platform + "/" + filename}).EscapedPath()

	get := formattest.Do(h.RegisterRepoRoutes, formattest.Request(http.MethodGet, target, nil))
	if get.Code != http.StatusOK || !bytes.Equal(get.Body.Bytes(), body) {
		t.Fatalf("download of %q answered %d with %d bytes, uploaded %d", target, get.Code, get.Body.Len(), len(body))
	}

	list := formattest.Do(h.RegisterRepoRoutes, formattest.Request(http.MethodGet,
		(&url.URL{Path: "/repos/myrepo/" + pkg.Platform + "/repodata.json"}).EscapedPath(), nil))
	if list.Code != http.StatusOK {
		t.Fatalf("repodata of %q answered %d", pkg.Platform, list.Code)
	}

	var doc repodataDoc
	if err := json.Unmarshal(list.Body.Bytes(), &doc); err != nil {
		t.Fatalf("repodata is not JSON: %v", err)
	}

	section := doc.Packages
	if pkg.Extension == models.CondaExtConda {
		section = doc.PackagesConda
	}

	entry, ok := section[filename]
	if !ok {
		t.Fatalf("repodata lists %v, not %q", section, filename)
	}

	for key, want := range map[string]any{
		"name": pkg.Name, "version": pkg.Version, "build": pkg.BuildString, "subdir": pkg.Platform,
		"sha256": pkg.SHA256, "size": float64(len(body)), "build_number": float64(pkg.BuildNumber),
	} {
		if entry[key] != want {
			t.Fatalf("repodata entry of %q has %s = %v, want %v", filename, key, entry[key], want)
		}
	}
}
