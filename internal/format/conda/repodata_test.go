// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package conda

import (
	"archive/tar"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
)

// demoIndex is the info/index.json of the demo-1.0-py_0 test packages.
const demoIndex = `{"name":"demo","version":"1.0","build":"py_0","build_number":3,` +
	`"depends":["python >=3.9","numpy"],"constrains":["scipy <2"],"license":"BSD-3-Clause",` +
	`"license_family":"BSD","noarch":"python","timestamp":1700000000123,"subdir":"noarch"}`

// demoTarBz2 is `tar -c info | bzip2` of a tree holding only info/index.json
// with the content of demoIndex (the standard library has no bzip2 writer).
const demoTarBz2 = "QlpoOTFBWSZTWey7pcUAAO9/hMSQBABQB/+3HAAoCv/332oACAAIMAE7ViwlVP0mmpiHpPSAaBo0BoDQaaAlRknqaNUZGT1MTNTaRgIyaYjGgkkgQGQyNAAaAAABmP3KvLJAQCApxCEi3NCDRPFWaGEYIxkIHLsgpxEOt1jbmxw41pOxR9b+S8yJPZW5Xu7PR79r3o6jIb7jOTmLTtDfZSfAE6KOLhHCTHhE+DDBG1DPFVlUA13i46Datmwk+mKRKjwvrdBiZHMK0hFiI4QXeu2LBFQihvQZzHCnzRPOs7hPtp+egQmKq62igYQlC2CEVSLIEbzaKZ9Y9vgHHsgHFYNHES6tH75JFHNXiMJICmwqSVHsgMffQfB4U8LwLdKQjaSGNqVSLpCwvqJ5UtcQx2tAhN4urEQwBkJiVo1HvO8Lr+JCD+LuSKcKEh2XdLig"

func decodeTarBz2(t *testing.T) []byte {
	t.Helper()

	b, err := base64.StdEncoding.DecodeString(demoTarBz2)
	if err != nil {
		t.Fatal(err)
	}

	return b
}

// upload posts a package and returns the response status.
func upload(h *HTTPHandler, platform, filename string, body []byte) int {
	w := formattest.Do(h.RegisterRepoRoutes,
		formattest.Request(http.MethodPost, "/repos/myrepo/"+platform+"?filename="+filename, bytes.NewReader(body)))

	return w.Code
}

// newHandlerOnly returns a handler for tests that need none of its fakes.
func newHandlerOnly(t *testing.T) *HTTPHandler {
	t.Helper()

	return NewHTTPHandler(formattest.NewLogger(t), &condaDB{DB: formattest.NewDB()}, formattest.NewBlobStore(), "http://localhost")
}

func TestParsePackageFilename(t *testing.T) {
	valid := []struct {
		filename                  string
		name, version, build, ext string
	}{
		{"demo-1.0-py_0.tar.bz2", "demo", "1.0", "py_0", ".tar.bz2"},
		{"demo-1.0-py_0.conda", "demo", "1.0", "py_0", ".conda"},
		{"scikit-learn-1.0-py39_0.tar.bz2", "scikit-learn", "1.0", "py39_0", ".tar.bz2"},
		{"python-dateutil-2.9.0.post0-pyhd8ed1ab_0.conda", "python-dateutil", "2.9.0.post0", "pyhd8ed1ab_0", ".conda"},
		{"ca-certificates-2024.2.2-hbcca054_0.conda", "ca-certificates", "2024.2.2", "hbcca054_0", ".conda"},
		{"a-b-c-d-e-1.2-3.tar.bz2", "a-b-c-d-e", "1.2", "3", ".tar.bz2"},
	}

	for _, c := range valid {
		got, ok := parsePackageFilename(c.filename)
		if !ok || got.name != c.name || got.version != c.version || got.build != c.build || got.ext != c.ext {
			t.Errorf("parsePackageFilename(%q) = %+v, %v; want %s / %s / %s / %s", c.filename, got, ok, c.name, c.version, c.build, c.ext)
		}
	}

	for _, filename := range []string{
		"", "demo.conda", "demo-1.0.conda", "demo-1.0-py_0.zip", "demo-1.0-py_0.tar.gz", "demo-1.0-py_0",
		"-1.0-py_0.conda", "demo--py_0.conda", "demo-1.0-.conda", "../demo-1.0-py_0.conda", `a\b-1.0-0.conda`,
		"repodata.json", "current_repodata.json",
		strings.Repeat("n", maxNameLen+1) + "-1.0-0.conda",
	} {
		if got, ok := parsePackageFilename(filename); ok {
			t.Errorf("parsePackageFilename(%q) = %+v, want rejection", filename, got)
		}
	}
}

// CONDA-1: a hyphenated package name (scikit-learn) was split at the first
// hyphen, so the file was stored as name "scikit", version "learn" and could
// not be found again.
func TestHyphenatedNameRoundTrip(t *testing.T) {
	h, db, bs, _ := newTestHandler(t)

	const filename = "scikit-learn-1.0-py39_0.tar.bz2"

	if status := upload(h, "linux-64", filename, []byte("data")); status != http.StatusCreated {
		t.Fatalf("upload status = %d", status)
	}

	if p := db.pkg; p.Name != "scikit-learn" || p.Version != "1.0" || p.BuildString != "py39_0" {
		t.Fatalf("stored name/version/build = %q/%q/%q", p.Name, p.Version, p.BuildString)
	}

	w := formattest.Do(h.RegisterRepoRoutes, formattest.Request(http.MethodGet, "/repos/myrepo/linux-64/"+filename, nil))
	if w.Code != http.StatusOK || w.Body.String() != "data" {
		t.Fatalf("download = %d %q", w.Code, w.Body.String())
	}

	repodata := getRepodata(t, h, "linux-64")
	if _, ok := repodata.Packages[filename]; !ok {
		t.Fatalf("repodata packages = %v, want %s", repodata.Packages, filename)
	}

	w = formattest.Do(h.RegisterRepoRoutes, formattest.Request(http.MethodDelete, "/repos/myrepo/linux-64/"+filename, nil))
	if w.Code != http.StatusNoContent || len(bs.Blobs) != 0 {
		t.Fatalf("delete = %d, blobs = %d", w.Code, len(bs.Blobs))
	}
}

type repodataDoc struct {
	Info          map[string]any            `json:"info"`
	Packages      map[string]map[string]any `json:"packages"`
	PackagesConda map[string]map[string]any `json:"packages.conda"` //nolint:tagliatelle // conda repodata key is literally packages.conda
}

func getRepodata(t *testing.T, h *HTTPHandler, platform string) repodataDoc {
	t.Helper()

	w := formattest.Do(h.RegisterRepoRoutes, formattest.Request(http.MethodGet, "/repos/myrepo/"+platform+"/repodata.json", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("repodata status = %d", w.Code)
	}

	var doc repodataDoc
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatalf("repodata is not JSON: %v", err)
	}

	return doc
}

// CONDA-2 and CONDA-3: .conda files are listed under "packages.conda" with
// their real name, and entries carry the fields conda's solver and downloader
// need, read from info/index.json.
func TestRepodataFormatsAndFields(t *testing.T) {
	h := newHandlerOnly(t)

	tarBz2 := decodeTarBz2(t)
	conda := condaArchive(t, demoIndex)

	for filename, body := range map[string][]byte{
		"demo-1.0-py_0.tar.bz2": tarBz2,
		"demo-1.0-py_0.conda":   conda,
	} {
		if status := upload(h, "noarch", filename, body); status != http.StatusCreated {
			t.Fatalf("upload %s status = %d", filename, status)
		}
	}

	doc := getRepodata(t, h, "noarch")

	if len(doc.Packages) != 1 || len(doc.PackagesConda) != 1 {
		t.Fatalf("packages = %v, packages.conda = %v; want one entry each", doc.Packages, doc.PackagesConda)
	}

	for section, entry := range map[string]map[string]any{
		"packages":       doc.Packages["demo-1.0-py_0.tar.bz2"],
		"packages.conda": doc.PackagesConda["demo-1.0-py_0.conda"],
	} {
		if entry == nil {
			t.Fatalf("%s: entry missing", section)
		}

		want := map[string]any{
			"name": "demo", "version": "1.0", "build": "py_0", "build_number": float64(3),
			"subdir": "noarch", "timestamp": float64(1700000000123),
			"license": "BSD-3-Clause", "license_family": "BSD", "noarch": "python",
		}
		for k, v := range want {
			if entry[k] != v {
				t.Errorf("%s: %s = %v, want %v", section, k, entry[k], v)
			}
		}

		if deps, _ := entry["depends"].([]any); len(deps) != 2 || deps[0] != "python >=3.9" || deps[1] != "numpy" {
			t.Errorf("%s: depends = %v", section, entry["depends"])
		}

		if c, _ := entry["constrains"].([]any); len(c) != 1 || c[0] != "scipy <2" {
			t.Errorf("%s: constrains = %v", section, entry["constrains"])
		}

		if s, _ := entry["sha256"].(string); len(s) != 64 {
			t.Errorf("%s: sha256 = %v", section, entry["sha256"])
		}

		if s, _ := entry["md5"].(string); len(s) != 32 {
			t.Errorf("%s: md5 = %v", section, entry["md5"])
		}
	}

	// Both files of the same build are downloadable under their own name.
	for _, filename := range []string{"demo-1.0-py_0.tar.bz2", "demo-1.0-py_0.conda"} {
		w := formattest.Do(h.RegisterRepoRoutes, formattest.Request(http.MethodGet, "/repos/myrepo/noarch/"+filename, nil))
		if w.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", filename, w.Code)
		}
	}

	if got := doc.PackagesConda["demo-1.0-py_0.conda"]["size"]; got != float64(len(conda)) {
		t.Errorf(".conda size = %v, want %d", got, len(conda))
	}
}

func TestDownloadRequiresMatchingExtension(t *testing.T) {
	h := newHandlerOnly(t)

	if status := upload(h, "linux-64", "demo-1.0-py_0.conda", condaArchive(t, demoIndex)); status != http.StatusCreated {
		t.Fatalf("upload status = %d", status)
	}

	for filename, want := range map[string]int{
		"demo-1.0-py_0.conda":   http.StatusOK,
		"demo-1.0-py_0.tar.bz2": http.StatusNotFound,
	} {
		w := formattest.Do(h.RegisterRepoRoutes, formattest.Request(http.MethodGet, "/repos/myrepo/linux-64/"+filename, nil))
		if w.Code != want {
			t.Errorf("GET %s = %d, want %d", filename, w.Code, want)
		}
	}
}

func TestRepodataEntryWithoutIndex(t *testing.T) {
	h := newHandlerOnly(t)

	if status := upload(h, "linux-64", "demo-1.0-py_0.tar.bz2", []byte("not an archive")); status != http.StatusCreated {
		t.Fatalf("upload status = %d", status)
	}

	entry := getRepodata(t, h, "linux-64").Packages["demo-1.0-py_0.tar.bz2"]
	if deps, ok := entry["depends"].([]any); !ok || len(deps) != 0 {
		t.Errorf("depends = %#v, want []", entry["depends"])
	}

	if _, ok := entry["timestamp"]; ok {
		t.Errorf("timestamp = %v, want it omitted when unknown", entry["timestamp"])
	}

	if entry["build_number"] != float64(0) || entry["subdir"] != "linux-64" {
		t.Errorf("build_number/subdir = %v/%v", entry["build_number"], entry["subdir"])
	}
}

// A second upload of the same file is rejected with 409 and neither replaces
// the stored row nor drops the blob the first upload stored.
func TestDuplicateUploadConflict(t *testing.T) {
	h, db, bs, pub := newTestHandler(t)

	if status := upload(h, "linux-64", "demo-1.0-py_0.tar.bz2", []byte("first")); status != http.StatusCreated {
		t.Fatalf("first upload status = %d", status)
	}

	first := db.pkg

	if status := upload(h, "linux-64", "demo-1.0-py_0.tar.bz2", []byte("second")); status != http.StatusConflict {
		t.Fatalf("second upload status = %d, want 409", status)
	}

	if db.pkg != first || len(db.pkgs) != 1 {
		t.Error("duplicate upload replaced the stored package")
	}

	if _, ok := bs.Blobs[first.SHA256]; !ok || len(bs.Blobs) != 1 {
		t.Errorf("blobs = %d, want only the first upload's", len(bs.Blobs))
	}

	if len(pub.Events()) != 1 {
		t.Errorf("events = %d, want 1", len(pub.Events()))
	}

	w := formattest.Do(h.RegisterRepoRoutes, formattest.Request(http.MethodGet, "/repos/myrepo/linux-64/demo-1.0-py_0.tar.bz2", nil))
	if w.Body.String() != "first" {
		t.Errorf("download = %q, want the first upload", w.Body.String())
	}
}

func TestUploadInvalidFilename(t *testing.T) {
	h, db, bs, _ := newTestHandler(t)

	for _, filename := range []string{"demo.tar.bz2", "demo-1.0-py_0.zip", "../demo-1.0-py_0.conda"} {
		if status := upload(h, "linux-64", filename, []byte("data")); status != http.StatusBadRequest {
			t.Errorf("upload %q status = %d, want 400", filename, status)
		}
	}

	if db.pkg != nil || len(bs.Blobs) != 0 {
		t.Error("rejected upload stored something")
	}
}

func TestPackageIndexFields(t *testing.T) {
	idx := indexOf(models.CondaExtTarBz2, decodeTarBz2(t))

	if idx.BuildNumber != 3 || idx.Timestamp != 1700000000123 || idx.License != "BSD-3-Clause" {
		t.Errorf("index = %+v", idx)
	}

	if len(idx.Depends) != 2 {
		t.Errorf("depends = %v", idx.Depends)
	}

	var extra map[string]json.RawMessage
	if err := json.Unmarshal(idx.Extra, &extra); err != nil {
		t.Fatal(err)
	}

	for _, k := range []string{"constrains", "license", "license_family", "noarch"} {
		if _, ok := extra[k]; !ok {
			t.Errorf("extra lacks %q: %s", k, idx.Extra)
		}
	}

	// Fields with a column of their own, or the server owns, are not copied.
	for _, k := range []string{"name", "version", "build", "build_number", "depends", "subdir", "timestamp"} {
		if _, ok := extra[k]; ok {
			t.Errorf("extra has %q: %s", k, idx.Extra)
		}
	}

	// Malformed values degrade to defaults instead of failing the upload.
	bad := parsePackageIndex([]byte(`{"depends":"x","build_number":"y","timestamp":"z","license":5}`))
	if len(bad.Depends) != 0 || bad.BuildNumber != 0 || bad.Timestamp != 0 || bad.License != "" {
		t.Errorf("malformed index = %+v", bad)
	}
}

type zeros struct{}

func (zeros) Read(p []byte) (int, error) { clear(p); return len(p), nil }

// CONDA-4: looking for info/index.json must not decompress or walk without
// bound. A package whose index sits behind more data, or more entries, than the
// limits allow is read as having no index.
func TestIndexLookupIsBounded(t *testing.T) {
	const index = `{"license":"MIT"}`

	indexHeader := tar.Header{Name: "info/index.json", Mode: 0o644, Size: int64(len(index))}

	writeTar := func(entries []tar.Header) []byte {
		var buf bytes.Buffer

		tw := tar.NewWriter(&buf)

		for _, h := range entries {
			if err := tw.WriteHeader(&h); err != nil {
				t.Fatal(err)
			}

			var err error
			if h.Name == indexHeader.Name {
				_, err = tw.Write([]byte(index))
			} else if h.Size > 0 {
				_, err = io.CopyN(tw, zeros{}, h.Size)
			}

			if err != nil {
				t.Fatal(err)
			}
		}

		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}

		return buf.Bytes()
	}

	lim := tarLimits{bytes: 1 << 20, entries: 50}

	t.Run("found within limits", func(t *testing.T) {
		got := indexFromTar(bytes.NewReader(writeTar([]tar.Header{{Name: "info/about.json", Mode: 0o644, Size: 100}, indexHeader})), lim)
		if string(got) != index {
			t.Fatalf("index = %q", got)
		}
	})

	t.Run("bytes", func(t *testing.T) {
		// 4 MiB of zeros before the index: well over the 1 MiB budget.
		data := writeTar([]tar.Header{{Name: "data/blob", Mode: 0o644, Size: 4 << 20}, indexHeader})
		if got := indexFromTar(bytes.NewReader(data), lim); got != nil {
			t.Fatalf("index read past the byte limit: %q", got)
		}
	})

	t.Run("entries", func(t *testing.T) {
		var entries []tar.Header
		for i := range lim.entries + 10 {
			entries = append(entries, tar.Header{Name: "pkg/f" + string(rune('a'+i%26)) + string(rune('a'+i/26)), Mode: 0o644})
		}

		entries = append(entries, indexHeader)

		if got := indexFromTar(bytes.NewReader(writeTar(entries)), lim); got != nil {
			t.Fatalf("index read past the entry limit: %q", got)
		}
	})

	t.Run("reads through the decompressor limit", func(t *testing.T) {
		// A zstd stream expanding to far more than the budget: the reader must
		// stop at the limit and not decompress the rest.
		var zst bytes.Buffer

		enc, err := zstd.NewWriter(&zst)
		if err != nil {
			t.Fatal(err)
		}

		if _, err := enc.Write(writeTar([]tar.Header{{Name: "data/blob", Mode: 0o644, Size: 8 << 20}, indexHeader})); err != nil {
			t.Fatal(err)
		}

		if err := enc.Close(); err != nil {
			t.Fatal(err)
		}

		dec, err := zstd.NewReader(&zst)
		if err != nil {
			t.Fatal(err)
		}
		defer dec.Close()

		counted := &countingReader{r: dec}

		start := time.Now()
		if got := indexFromTar(counted, lim); got != nil {
			t.Fatalf("index read past the byte limit: %q", got)
		}

		if counted.n > lim.bytes+(64<<10) {
			t.Errorf("decompressed %d bytes, limit %d", counted.n, lim.bytes)
		}

		if d := time.Since(start); d > 5*time.Second {
			t.Errorf("took %v", d)
		}
	})
}

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)

	return n, err
}
