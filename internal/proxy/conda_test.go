// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
)

// fakeConda is an upstream conda channel: a map of paths below the channel
// to bodies, with the request count of every path.
type fakeConda struct {
	mu    sync.Mutex
	files map[string][]byte
	hits  map[string]int
}

func newFakeConda() *fakeConda {
	return &fakeConda{files: map[string][]byte{}, hits: map[string]int{}}
}

func (c *fakeConda) handler(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	path := strings.TrimPrefix(r.URL.Path, "/main/")
	body, ok := c.files[path]
	c.hits[path]++
	c.mu.Unlock()

	if !ok {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	_, _ = w.Write(body)
}

func (c *fakeConda) hitsOf(path string) int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.hits[path]
}

func (c *fakeConda) set(path string, body []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.files[path] = body
}

func condaMD5Hex(b []byte) string {
	h := md5.Sum(b)
	return hex.EncodeToString(h[:])
}

func zstdOf(t *testing.T, b []byte) []byte {
	t.Helper()

	var buf bytes.Buffer

	enc, err := zstd.NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := enc.Write(b); err != nil {
		t.Fatal(err)
	}

	if err := enc.Close(); err != nil {
		t.Fatal(err)
	}

	return buf.Bytes()
}

// condaEntryJSON is a repodata entry; empty digests are left out.
func condaEntryJSON(name, sha, md5sum string, size int64) string {
	parts := []string{fmt.Sprintf(`"name":%q,"version":"1.0","build":"0"`, name)}
	if sha != "" {
		parts = append(parts, fmt.Sprintf(`"sha256":%q`, sha))
	}

	if md5sum != "" {
		parts = append(parts, fmt.Sprintf(`"md5":%q`, md5sum))
	}

	if size >= 0 {
		parts = append(parts, fmt.Sprintf(`"size":%d`, size))
	}

	return "{" + strings.Join(parts, ",") + "}"
}

func condaRepodata(info string, tarbz2, condas map[string]string) []byte {
	section := func(m map[string]string) string {
		parts := make([]string, 0, len(m))
		for file, entry := range m {
			parts = append(parts, fmt.Sprintf("%q:%s", file, entry))
		}

		return "{" + strings.Join(parts, ",") + "}"
	}

	return fmt.Appendf(nil, `{"info":%s,"packages":%s,"packages.conda":%s,"repodata_version":1}`, info, section(tarbz2), section(condas))
}

const (
	condaPkg    = "demo-1.0-0.conda"
	condaOldPkg = "old-1.0-0.tar.bz2"
)

var (
	condaPkgBody    = []byte("demo conda package")  //nolint:gochecknoglobals // read-only fixture
	condaOldPkgBody = []byte("old tar.bz2 package") //nolint:gochecknoglobals // read-only fixture
)

// condaFixture is a conda proxy repository over a fake channel listing demo
// (.conda, sha256) and old (.tar.bz2, md5 only) in linux-64.
func condaFixture(t *testing.T) (*fixture, *fakeConda) {
	t.Helper()

	up := newFakeConda()
	f := newFixture(t, up.handler)
	f.repo.Format = "conda"
	f.repo.UpstreamURL = f.upstream.URL + "/main"

	repodata := condaRepodata(`{"subdir":"linux-64"}`,
		map[string]string{condaOldPkg: condaEntryJSON("old", "", condaMD5Hex(condaOldPkgBody), int64(len(condaOldPkgBody)))},
		map[string]string{condaPkg: condaEntryJSON("demo", sum(string(condaPkgBody)), condaMD5Hex(condaPkgBody), int64(len(condaPkgBody)))})

	up.set("linux-64/repodata.json", repodata)
	up.set("linux-64/repodata.json.zst", zstdOf(t, repodata))
	up.set("linux-64/current_repodata.json", repodata)
	up.set("channeldata.json", []byte(`{"channeldata_version":1,"packages":{"demo":{}}}`))
	up.set("linux-64/"+condaPkg, condaPkgBody)
	up.set("linux-64/"+condaOldPkg, condaOldPkgBody)

	return f, up
}

func TestConda_RepodataIsServedByteForByteAndCached(t *testing.T) {
	f, up := condaFixture(t)

	for _, path := range []string{
		"linux-64/repodata.json", "linux-64/repodata.json.zst", "linux-64/current_repodata.json", "channeldata.json",
	} {
		first := f.do(http.MethodGet, path)
		if first.Code != http.StatusOK || !bytes.Equal(first.Body.Bytes(), up.files[path]) {
			t.Fatalf("%s: %d, %d bytes (want %d)", path, first.Code, first.Body.Len(), len(up.files[path]))
		}

		if second := f.do(http.MethodGet, path); !bytes.Equal(second.Body.Bytes(), up.files[path]) || up.hitsOf(path) != 1 {
			t.Errorf("%s: second request hit upstream %d times", path, up.hitsOf(path))
		}
	}
}

func TestConda_ConditionalRequests(t *testing.T) {
	f, _ := condaFixture(t)

	etag := f.do(http.MethodGet, "linux-64/repodata.json.zst").Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag")
	}

	for _, inm := range []string{etag, "*", `"other", ` + etag, "W/" + etag} {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
		req.Header.Set("If-None-Match", inm)

		w := httptest.NewRecorder()
		f.svc.Serve(w, req, f.repo, "linux-64/repodata.json.zst")

		if w.Code != http.StatusNotModified || w.Body.Len() != 0 {
			t.Errorf("If-None-Match %q: %d with %d bytes", inm, w.Code, w.Body.Len())
		}
	}

	if w := f.do(http.MethodHead, "linux-64/repodata.json.zst"); w.Code != http.StatusOK || w.Body.Len() != 0 {
		t.Errorf("HEAD: %d with %d bytes", w.Code, w.Body.Len())
	}
}

func TestConda_UnservedPathsAreNotFoundWithoutAnUpstreamRequest(t *testing.T) {
	f, up := condaFixture(t)

	up.set("linux-64/repodata.jlap", []byte("jlap"))

	for _, path := range []string{
		"linux-64/repodata.jlap", "linux-64/repodata_shards.msgpack.zst", "linux-64/shards/x.msgpack.zst", "index.html",
		"repodata.json", "linux-64/", "../x", "linux-64/..", "linux-64/sub/dir/x-1-0.conda",
	} {
		if w := f.do(http.MethodGet, path); w.Code != http.StatusNotFound {
			t.Errorf("%s: %d, want 404", path, w.Code)
		}
	}

	if up.hitsOf("linux-64/repodata.jlap") != 0 {
		t.Error("jlap was requested from the upstream")
	}
}

func TestConda_PackagesAreVerifiedAgainstRepodata(t *testing.T) {
	f, up := condaFixture(t)

	w := f.do(http.MethodGet, "linux-64/"+condaPkg)
	if w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), condaPkgBody) {
		t.Fatalf("sha256-listed package: %d %q", w.Code, w.Body.String())
	}

	f.do(http.MethodGet, "linux-64/"+condaPkg)

	if up.hitsOf("linux-64/"+condaPkg) != 1 {
		t.Errorf("package fetched %d times, want 1", up.hitsOf("linux-64/"+condaPkg))
	}

	// The index was read through its .zst copy only.
	if up.hitsOf("linux-64/repodata.json") != 0 || up.hitsOf("linux-64/repodata.json.zst") != 1 {
		t.Errorf("index hits: json %d, zst %d", up.hitsOf("linux-64/repodata.json"), up.hitsOf("linux-64/repodata.json.zst"))
	}

	// The sha256 digest pins the file; the md5-only one is held for the TTL.
	if e := f.db.entry(f.repo.ID, "linux-64/"+condaPkg); e == nil || time.Until(e.ExpiresAt) < 24*time.Hour {
		t.Errorf("sha256-verified package is not pinned: %+v", e)
	}

	w = f.do(http.MethodGet, "linux-64/"+condaOldPkg)
	if w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), condaOldPkgBody) {
		t.Fatalf("md5-listed package: %d %q", w.Code, w.Body.String())
	}

	if e := f.db.entry(f.repo.ID, "linux-64/"+condaOldPkg); e == nil || time.Until(e.ExpiresAt) < 24*time.Hour {
		t.Errorf("md5-verified package is not pinned: %+v", e)
	}
}

func TestConda_RefusesPackagesThatMismatchOrAreNotListed(t *testing.T) {
	f, up := condaFixture(t)

	up.set("linux-64/"+condaPkg, []byte("tampered conda package"))
	up.set("linux-64/"+condaOldPkg, []byte("tampered old package!!"))
	up.set("linux-64/ghost-1.0-0.conda", []byte("not in repodata"))

	for _, file := range []string{condaPkg, condaOldPkg} {
		if w := f.do(http.MethodGet, "linux-64/"+file); w.Code != http.StatusBadGateway {
			t.Errorf("%s with a wrong digest: %d, want 502", file, w.Code)
		}

		if f.db.entry(f.repo.ID, "linux-64/"+file) != nil {
			t.Errorf("%s was cached", file)
		}
	}

	if w := f.do(http.MethodGet, "linux-64/ghost-1.0-0.conda"); w.Code != http.StatusNotFound {
		t.Errorf("unlisted package: %d, want 404", w.Code)
	}

	if up.hitsOf("linux-64/ghost-1.0-0.conda") != 0 {
		t.Error("an unlisted package was requested from the upstream")
	}

	// The 404 is remembered: the index is not searched again.
	zst := up.hitsOf("linux-64/repodata.json.zst")
	f.do(http.MethodGet, "linux-64/ghost-1.0-0.conda")

	if up.hitsOf("linux-64/repodata.json.zst") != zst {
		t.Error("the index was fetched again for a known-missing package")
	}

	// A subdirectory the upstream does not have.
	if w := f.do(http.MethodGet, "win-64/"+condaPkg); w.Code != http.StatusNotFound {
		t.Errorf("unknown subdir: %d, want 404", w.Code)
	}
}

func TestConda_SizeMismatchIsRefusedWhenOnlyAMD5IsListed(t *testing.T) {
	f, up := condaFixture(t)

	body := []byte("package body")
	up.set("noarch/repodata.json", condaRepodata(`{}`, nil,
		map[string]string{"sized-1.0-0.conda": condaEntryJSON("sized", "", condaMD5Hex(body), int64(len(body))+1)}))
	up.set("noarch/sized-1.0-0.conda", body)

	if w := f.do(http.MethodGet, "noarch/sized-1.0-0.conda"); w.Code != http.StatusBadGateway {
		t.Errorf("size mismatch: %d, want 502", w.Code)
	}

	// The index had no .zst: the plain one was used.
	if up.hitsOf("noarch/repodata.json.zst") != 1 || up.hitsOf("noarch/repodata.json") != 1 {
		t.Errorf("index hits: zst %d, json %d", up.hitsOf("noarch/repodata.json.zst"), up.hitsOf("noarch/repodata.json"))
	}
}

func TestConda_PackageWithoutDigestsIsServedForTheTTLOnly(t *testing.T) {
	f, up := condaFixture(t)

	body := []byte("bare package")
	up.set("noarch/repodata.json", condaRepodata(`{}`, nil, map[string]string{"bare-1.0-0.conda": `{"name":"bare"}`}))
	up.set("noarch/bare-1.0-0.conda", body)

	w := f.do(http.MethodGet, "noarch/bare-1.0-0.conda")
	if w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), body) {
		t.Fatalf("%d %q", w.Code, w.Body.String())
	}

	e := f.db.entry(f.repo.ID, "noarch/bare-1.0-0.conda")
	if e == nil || time.Until(e.ExpiresAt) > time.Duration(f.repo.CacheTTLSeconds+1)*time.Second {
		t.Errorf("a package no digest vouches for is pinned: %+v", e)
	}
}

func TestConda_RefusesRepodataThatBypassesTheProxyOrIsBroken(t *testing.T) {
	f, up := condaFixture(t)

	for path, body := range map[string]string{
		"noarch/repodata.json":         string(condaRepodata(`{"subdir":"noarch","base_url":"https://elsewhere.example/pkgs"}`, nil, nil)),
		"noarch/current_repodata.json": "<html>captive portal</html>",
		"osx-64/repodata.json":         `{"packages":{"a-1-0.tar.bz2":"nope"}}`,
		"win-64/repodata.json":         `{"error":"not a channel"}`,
	} {
		up.set(path, []byte(body))

		if w := f.do(http.MethodGet, path); w.Code != http.StatusBadGateway {
			t.Errorf("%s: %d, want 502", path, w.Code)
		}

		if f.db.entry(f.repo.ID, path) != nil {
			t.Errorf("%s was cached", path)
		}
	}

	up.set("noarch/repodata.json.zst", []byte("plain text under a zst name"))

	if w := f.do(http.MethodGet, "noarch/repodata.json.zst"); w.Code != http.StatusBadGateway {
		t.Errorf("fake zst: %d, want 502", w.Code)
	}
}

func TestConda_IndexSizeLimit(t *testing.T) {
	f, _ := condaFixture(t)
	f.svc.maxCondaIndex = 64

	if w := f.do(http.MethodGet, "linux-64/repodata.json"); w.Code != http.StatusBadGateway {
		t.Errorf("index over the limit: %d, want 502", w.Code)
	}

	// A package is not an index: the index limit does not apply to it, and
	// the repodata lookup is bounded by it.
	if w := f.do(http.MethodGet, "linux-64/"+condaPkg); w.Code != http.StatusBadGateway {
		t.Errorf("package whose index is over the limit: %d, want 502", w.Code)
	}
}

func TestConda_DecompressionBombIsRefused(t *testing.T) {
	f, up := condaFixture(t)

	// 64 MiB of spaces inside a few KiB of zstd, then a valid document.
	big := append(bytes.Repeat([]byte(" "), 64<<20), condaRepodata(`{}`, nil, nil)...)
	up.set("noarch/repodata.json.zst", zstdOf(t, big))

	f.svc.maxCondaIndex = 1 << 20

	if w := f.do(http.MethodGet, "noarch/repodata.json.zst"); w.Code != http.StatusBadGateway {
		t.Errorf("bomb: %d, want 502", w.Code)
	}
}

func TestConda_StaleIndexIsServedWhenTheUpstreamIsDown(t *testing.T) {
	var down bool

	var mu sync.Mutex

	up := newFakeConda()
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		d := down
		mu.Unlock()

		if d {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}

		up.handler(w, r)
	})
	f.repo.Format = "conda"
	f.repo.UpstreamURL = f.upstream.URL + "/main"

	up.set("noarch/repodata.json", condaRepodata(`{}`, nil, nil))

	if w := f.do(http.MethodGet, "noarch/repodata.json"); w.Code != http.StatusOK {
		t.Fatalf("first fetch: %d", w.Code)
	}

	f.db.expire(f.repo.ID, "noarch/repodata.json")

	mu.Lock()
	down = true
	mu.Unlock()

	w := f.do(http.MethodGet, "noarch/repodata.json")
	if w.Code != http.StatusOK || w.Header().Get("Warning") == "" {
		t.Errorf("stale index: %d, warning %q", w.Code, w.Header().Get("Warning"))
	}
}

func TestConda_Policy(t *testing.T) {
	if !Immutable("conda", "linux-64/a-1-0.conda") || !Immutable("conda", "linux-64/a-1-0.tar.bz2") {
		t.Error("package files must be immutable")
	}

	for _, key := range []string{"linux-64/repodata.json", "linux-64/repodata.json.zst", "channeldata.json"} {
		if Immutable("conda", key) {
			t.Errorf("%s must not be immutable", key)
		}
	}

	if isMetadataKey("conda", "linux-64/a-1-0.conda") || !isMetadataKey("conda", "channeldata.json") {
		t.Error("only channeldata.json is common metadata")
	}

	if !isCondaIndexKey("linux-64/current_repodata.json.zst") || isCondaIndexKey("linux-64/a-1-0.conda") {
		t.Error("isCondaIndexKey")
	}
}
