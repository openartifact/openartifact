// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package helm

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/format/formattest"
	pkghelm "github.com/openartifact/openartifact/pkg/helm"
	"github.com/openartifact/openartifact/pkg/logger"
)

func fuzzChart(chartYAML string) []byte {
	var buf bytes.Buffer

	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	_ = tw.WriteHeader(&tar.Header{Name: "mychart/Chart.yaml", Mode: 0o644, Size: int64(len(chartYAML)), Typeflag: tar.TypeReg})
	_, _ = tw.Write([]byte(chartYAML))
	_ = tw.Close()
	_ = gw.Close()

	return buf.Bytes()
}

// FuzzUploadChart uploads a chart whose Chart.yaml is arbitrary text and, when
// the upload is accepted, follows what a helm client does next: read the
// repository's index.yaml, find the chart in it and download the URL listed
// there. Everything accepted must be listed in a readable index and be
// downloadable, byte for byte, from the URL the index gives.
func FuzzUploadChart(f *testing.F) {
	log, err := logger.New("error", "json", os.DevNull)
	if err != nil {
		f.Fatal(err)
	}

	f.Add("apiVersion: v2\nname: mychart\nversion: 1.0.0\ndescription: A test chart\n")
	f.Add("apiVersion: v2\nname: web\nversion: 2.3.4-rc.1+build.5\nappVersion: \"1.16.0\"\nkeywords: [a, b]\nhome: https://example.com\nsources: [https://example.com/src]\nmaintainers:\n- {name: Alice, email: a@example.com}\nicon: https://example.com/i.png\ntype: library\n")
	f.Add("apiVersion: v2\nname: \"a b\"\nversion: 1.0.0\n")
	f.Add("apiVersion: v2\nname: ..\nversion: 1.0.0\n")
	f.Add("apiVersion: v2\nname: web\nversion: 1.0\n")
	f.Add("apiVersion: v2\nname: web\nversion: v1.0.0\n")
	f.Add("name: [\n")
	f.Add("")

	f.Fuzz(func(t *testing.T, chartYAML string) {
		db := &stampDB{helmDB: newHelmDB()}
		bs := formattest.NewBlobStore()
		h := NewHTTPHandler(log, db, bs, "http://localhost")
		h.SetPublisher(&formattest.Publisher{})

		chart := fuzzChart(chartYAML)
		meta, parseErr := pkghelm.ParseChart(bytes.NewReader(chart))

		w := formattest.Do(h.RegisterRepoRoutes, formattest.Request(http.MethodPost, "/repos/myrepo/charts", bytes.NewReader(chart)))

		switch {
		case parseErr != nil && w.Code != http.StatusBadRequest:
			t.Fatalf("unparseable chart (%v) answered %d", parseErr, w.Code)
		case w.Code != http.StatusCreated && w.Code != http.StatusBadRequest:
			t.Fatalf("upload answered %d: %s", w.Code, w.Body.String())
		case w.Code != http.StatusCreated:
			if len(db.charts) != 0 || len(bs.Blobs) != 0 || db.used != 0 {
				t.Fatalf("rejected upload left charts=%d blobs=%d used=%d", len(db.charts), len(bs.Blobs), db.used)
			}

			return
		}

		name, _ := meta["name"].(string)
		version, _ := meta["version"].(string)

		var created struct{ Name, Version string }
		if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil || created.Name != name || created.Version != version {
			t.Fatalf("created %+v (%v), chart says %q %q", created, err, name, version)
		}

		sum := sha256.Sum256(chart)
		digest := hex.EncodeToString(sum[:])

		if len(db.charts) != 1 || db.used != int64(len(chart)) || string(bs.Blobs[digest]) != string(chart) {
			t.Fatalf("accepted chart stored as charts=%d used=%d blob=%v", len(db.charts), db.used, bs.Blobs[digest] != nil)
		}

		fuzzCheckListed(t, h, name, version, digest, chart)
	})
}

// fuzzCheckListed reads the repository's index.yaml the way a helm client
// does: it lists the chart once under its own name, and the URL it gives
// downloads the uploaded bytes.
func fuzzCheckListed(t *testing.T, h *HTTPHandler, name, version, digest string, chart []byte) {
	t.Helper()

	idx := formattest.Do(h.RegisterRepoRoutes, formattest.Request(http.MethodGet, "/repos/myrepo/index.yaml", nil))
	if idx.Code != http.StatusOK {
		t.Fatalf("index.yaml answered %d: %s", idx.Code, idx.Body.String())
	}

	ix, err := pkghelm.ParseIndex(idx.Body.Bytes())
	if err != nil {
		t.Fatalf("index.yaml of an accepted chart is not an index: %v\n%s", err, idx.Body.String())
	}

	var listed []pkghelm.ChartVersion
	for cv := range ix.Versions() {
		listed = append(listed, cv)
	}

	if len(listed) != 1 || listed[0].Name != name || listed[0].Version != version || listed[0].Digest != digest || len(listed[0].URLs) != 1 {
		t.Fatalf("index lists %+v for %q %q (digest %s)\n%s", listed, name, version, digest, idx.Body.String())
	}

	if file, ok := pkghelm.RelativeChartFile(listed[0].URLs[0]); !ok || file != name+"-"+version+".tgz" {
		t.Fatalf("index URL %q names %q (%v), want %q", listed[0].URLs[0], file, ok, name+"-"+version+".tgz")
	}

	u, err := url.Parse(listed[0].URLs[0])
	if err != nil {
		t.Fatalf("index URL %q: %v", listed[0].URLs[0], err)
	}

	dl := formattest.Do(h.RegisterRepoRoutes, httptest.NewRequestWithContext(t.Context(), http.MethodGet, u.RequestURI(), nil))
	if dl.Code != http.StatusOK || !bytes.Equal(dl.Body.Bytes(), chart) || dl.Header().Get("ETag") != `"`+digest+`"` {
		t.Fatalf("download of %s answered %d (%d bytes, ETag %q), want the %d uploaded bytes",
			u.RequestURI(), dl.Code, dl.Body.Len(), dl.Header().Get("ETag"), len(chart))
	}
}

// FuzzChartIdentity checks that a name and version the upload accepts always
// make a file name the download route can take apart again and a URL path
// segment the proxy and group read back unchanged.
func FuzzChartIdentity(f *testing.F) {
	for _, c := range [][2]string{
		{"mychart", "1.0.0"}, {"my-chart", "1.0.0-rc1+b.2"}, {"a-1", "1.0.0"}, {"a-v1", "1.0.0"}, {"é;,%?#!'()*", "0.0.1"},
		{"..", "1.0.0"}, {".", "1.0.0"}, {"a/b", "1.0.0"}, {"a", "v1.0.0"}, {"a", "1.0"}, {"a", "01.0.0"}, {"", ""},
	} {
		f.Add(c[0], c[1])
	}

	f.Fuzz(func(t *testing.T, name, version string) {
		if !validChartIdent(name) || !validChartIdent(version) || !strictSemver(version) {
			return
		}

		if !startsVersion(version) || version[0] == 'v' {
			t.Fatalf("strictSemver(%q) accepts a version that does not start with a digit", version)
		}

		file := name + "-" + version + ".tgz"

		// The split the download route tries for this file name.
		base := strings.TrimSuffix(file, ".tgz")
		i := len(name)

		if i < 1 || i >= len(base)-1 || base[i] != '-' || !startsVersion(base[i+1:]) || base[:i] != name || base[i+1:] != version {
			t.Fatalf("%q does not split back into %q and %q", file, name, version)
		}

		// The URL a client is given reads back as the same file.
		u, err := url.Parse("http://oa.test/repos/r/charts/" + url.PathEscape(name+"-"+version) + ".tgz")
		if err != nil {
			t.Fatalf("URL for %q: %v", file, err)
		}

		if got, ok := pkghelm.ChartFile(u); !ok || got != file {
			t.Fatalf("ChartFile of the URL for %q = %q, %v", file, got, ok)
		}
	})
}
