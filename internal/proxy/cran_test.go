// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/curation"
	pkgcran "github.com/openartifact/openartifact/pkg/cran"
)

// fakeCRAN is an upstream CRAN: a map of paths below the root to bodies, with
// the request count of every path.
type fakeCRAN struct {
	mu    sync.Mutex
	files map[string][]byte
	hits  map[string]int
}

func newFakeCRAN() *fakeCRAN { return &fakeCRAN{files: map[string][]byte{}, hits: map[string]int{}} }

func (c *fakeCRAN) handler(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/cran/")

	c.mu.Lock()
	body, ok := c.files[path]
	c.hits[path]++
	c.mu.Unlock()

	if !ok {
		http.NotFound(w, r)
		return
	}

	_, _ = w.Write(body)
}

func (c *fakeCRAN) hitsOf(path string) int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.hits[path]
}

func (c *fakeCRAN) set(path string, body []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.files[path] = body
}

// cranIndex builds a PACKAGES.gz listing the packages as name, version, md5.
func cranIndex(t *testing.T, pkgs ...[3]string) []byte {
	t.Helper()

	recs := make([]pkgcran.Record, 0, len(pkgs))
	for _, p := range pkgs {
		recs = append(recs, pkgcran.IndexRecord(p[0], p[1], p[2], map[string]string{"License": "MIT", "Depends": "R (>= 3.5)"}, false))
	}

	gz, err := pkgcran.Gzip(pkgcran.RenderText(recs))
	if err != nil {
		t.Fatal(err)
	}

	return gz
}

var (
	cranGlueBody    = []byte("glue 1.8.0 source tarball")  //nolint:gochecknoglobals // read-only fixture
	cranOldBody     = []byte("glue 1.6.0 source tarball")  //nolint:gochecknoglobals // read-only fixture
	cranWinBody     = []byte("glue 1.8.0 windows binary")  //nolint:gochecknoglobals // read-only fixture
	cranUnlistedPkg = []byte("ghost source, not in index") //nolint:gochecknoglobals // read-only fixture
)

// cranFixture is a cran proxy repository over a fake CRAN whose source tree
// lists glue 1.8.0 (current, in src/contrib) while 1.6.0 is only in the Archive.
func cranFixture(t *testing.T) (*fixture, *fakeCRAN) {
	t.Helper()

	up := newFakeCRAN()
	f := newFixture(t, up.handler)
	f.repo.Format = "cran"
	f.repo.UpstreamURL = f.upstream.URL + "/cran"

	up.set(
		"src/contrib/PACKAGES.gz",
		cranIndex(t, [3]string{"glue", "1.8.0", condaMD5Hex(cranGlueBody)}, [3]string{"zeta", "0.1", condaMD5Hex([]byte("z"))}),
	)
	up.set("src/contrib/glue_1.8.0.tar.gz", cranGlueBody)
	up.set("src/contrib/Archive/glue/glue_1.6.0.tar.gz", cranOldBody)
	up.set("src/contrib/Archive/ghost/ghost_1.0.tar.gz", cranUnlistedPkg)
	up.set("bin/windows/contrib/4.5/PACKAGES.gz", cranIndex(t, [3]string{"glue", "1.8.0", condaMD5Hex(cranWinBody)}))
	up.set("bin/windows/contrib/4.5/glue_1.8.0.zip", cranWinBody)

	return f, up
}

func TestCRAN_IndexVariantsAreRenderedFromOneUpstreamFetch(t *testing.T) {
	f, up := cranFixture(t)

	text := f.do(http.MethodGet, "src/contrib/PACKAGES")
	if text.Code != http.StatusOK {
		t.Fatalf("PACKAGES: %d %q", text.Code, text.Body)
	}

	recs, err := pkgcran.ParsePackages(bytes.NewReader(text.Body.Bytes()), pkgcran.MaxIndexBytes)
	if err != nil || len(recs) != 2 || recs[0].Get("Package") != "glue" || recs[0].Get("MD5sum") != condaMD5Hex(cranGlueBody) {
		t.Fatalf("PACKAGES records = %v, %v", recs, err)
	}

	gz := f.do(http.MethodGet, "src/contrib/PACKAGES.gz")
	if gz.Code != http.StatusOK {
		t.Fatalf("PACKAGES.gz: %d", gz.Code)
	}

	if plain, err := pkgcran.Gunzip(
		bytes.NewReader(gz.Body.Bytes()),
		pkgcran.MaxIndexBytes,
	); err != nil ||
		!bytes.Equal(plain, text.Body.Bytes()) {
		t.Errorf("PACKAGES.gz does not hold PACKAGES: %v", err)
	}

	if rds := f.do(http.MethodGet, "src/contrib/PACKAGES.rds"); rds.Code != http.StatusOK || rds.Body.Len() == 0 {
		t.Errorf("PACKAGES.rds: %d with %d bytes", rds.Code, rds.Body.Len())
	}

	if up.hitsOf("src/contrib/PACKAGES.gz") != 1 || up.hitsOf("src/contrib/PACKAGES") != 0 || up.hitsOf("src/contrib/PACKAGES.rds") != 0 {
		t.Errorf("upstream hits: gz %d, plain %d, rds %d; want 1/0/0",
			up.hitsOf("src/contrib/PACKAGES.gz"), up.hitsOf("src/contrib/PACKAGES"), up.hitsOf("src/contrib/PACKAGES.rds"))
	}
}

func TestCRAN_IndexConditionalRequestsAndHead(t *testing.T) {
	f, _ := cranFixture(t)

	etag := f.do(http.MethodGet, "src/contrib/PACKAGES").Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag")
	}

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	req.Header.Set("If-None-Match", etag)

	w := httptest.NewRecorder()
	f.svc.Serve(w, req, f.repo, "src/contrib/PACKAGES")

	if w.Code != http.StatusNotModified {
		t.Errorf("revalidation = %d, want 304", w.Code)
	}

	// Another variant is another representation.
	if other := f.do(http.MethodGet, "src/contrib/PACKAGES.gz").Header().Get("ETag"); other == etag {
		t.Error("PACKAGES and PACKAGES.gz share an ETag")
	}

	if h := f.do(http.MethodHead, "src/contrib/PACKAGES"); h.Code != http.StatusOK || h.Body.Len() != 0 {
		t.Errorf("HEAD = %d with %d bytes", h.Code, h.Body.Len())
	}
}

func TestCRAN_SourcePackageIsVerifiedPinnedAndCached(t *testing.T) {
	f, up := cranFixture(t)

	w := f.do(http.MethodGet, "src/contrib/glue_1.8.0.tar.gz")
	if w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), cranGlueBody) {
		t.Fatalf("download: %d %q", w.Code, w.Body)
	}

	f.do(http.MethodGet, "src/contrib/glue_1.8.0.tar.gz")

	if n := up.hitsOf("src/contrib/glue_1.8.0.tar.gz"); n != 1 {
		t.Errorf("package fetched %d times, want 1", n)
	}

	if e := f.db.entry(f.repo.ID, "src/contrib/glue_1.8.0.tar.gz"); e == nil || time.Until(e.ExpiresAt) < 24*time.Hour {
		t.Errorf("verified source package is not pinned: %+v", e)
	}

	// The Archive path of the current version is the same cached file.
	if a := f.do(
		http.MethodGet,
		"src/contrib/Archive/glue/glue_1.8.0.tar.gz",
	); a.Code != http.StatusOK ||
		!bytes.Equal(a.Body.Bytes(), cranGlueBody) {
		t.Errorf("archive path of the current version: %d", a.Code)
	}
}

func TestCRAN_RefusesAPackageThatMismatchesItsIndexDigest(t *testing.T) {
	f, up := cranFixture(t)
	up.set("src/contrib/glue_1.8.0.tar.gz", []byte("tampered"))

	if w := f.do(http.MethodGet, "src/contrib/glue_1.8.0.tar.gz"); w.Code != http.StatusBadGateway {
		t.Errorf("tampered package: %d, want 502", w.Code)
	}

	if f.db.entry(f.repo.ID, "src/contrib/glue_1.8.0.tar.gz") != nil {
		t.Error("a tampered package was cached")
	}
}

func TestCRAN_ArchivedVersionComesFromTheArchiveAndIsNotPinned(t *testing.T) {
	f, up := cranFixture(t)

	for _, path := range []string{"src/contrib/glue_1.6.0.tar.gz", "src/contrib/Archive/glue/glue_1.6.0.tar.gz"} {
		w := f.do(http.MethodGet, path)
		if w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), cranOldBody) {
			t.Fatalf("%s: %d %q", path, w.Code, w.Body)
		}
	}

	// Both request paths share one cache entry, fetched once.
	if n := up.hitsOf("src/contrib/Archive/glue/glue_1.6.0.tar.gz"); n != 1 {
		t.Errorf("archived file fetched %d times, want 1", n)
	}

	// No index record lists it, so there is no digest to pin it with.
	if e := f.db.entry(f.repo.ID, "src/contrib/glue_1.6.0.tar.gz"); e == nil || time.Until(e.ExpiresAt) > 24*time.Hour {
		t.Errorf("an unverified archived file is pinned: %+v", e)
	}

	// A package the upstream indexes nowhere.
	if w := f.do(
		http.MethodGet,
		"src/contrib/Archive/ghost/ghost_1.0.tar.gz",
	); w.Code != http.StatusOK ||
		!bytes.Equal(w.Body.Bytes(), cranUnlistedPkg) {
		t.Errorf("unlisted archived package: %d", w.Code)
	}
}

func TestCRAN_UnknownFilesAreNotFound(t *testing.T) {
	f, up := cranFixture(t)

	for _, path := range []string{
		"src/contrib/nothere_1.0.tar.gz", "src/contrib/Archive/nothere/nothere_1.0.tar.gz", "bin/windows/contrib/4.5/nothere_1.0.zip",
		"src/contrib/Archive/", "src/contrib/Archive/glue/", "elsewhere/glue_1.8.0.tar.gz", "src/contrib/../glue_1.8.0.tar.gz", "src/contrib/glue.tar.gz",
	} {
		if w := f.do(http.MethodGet, path); w.Code != http.StatusNotFound {
			t.Errorf("%s: %d, want 404", path, w.Code)
		}
	}

	// A tree the upstream has no index for.
	if w := f.do(http.MethodGet, "bin/windows/contrib/3.0/PACKAGES"); w.Code != http.StatusNotFound {
		t.Errorf("tree without an index: %d, want 404", w.Code)
	}

	// Only package files are fetched, never directory listings.
	if up.hitsOf("src/contrib/Archive/") != 0 || up.hitsOf("src/contrib/Archive/glue/") != 0 {
		t.Error("a directory listing was requested from the upstream")
	}
}

func TestCRAN_BinaryTreeHasItsOwnIndexAndFilesAreNotPinned(t *testing.T) {
	f, _ := cranFixture(t)

	idx := f.do(http.MethodGet, "bin/windows/contrib/4.5/PACKAGES")
	if idx.Code != http.StatusOK || !strings.Contains(idx.Body.String(), condaMD5Hex(cranWinBody)) {
		t.Fatalf("binary index: %d %q", idx.Code, idx.Body)
	}

	w := f.do(http.MethodGet, "bin/windows/contrib/4.5/glue_1.8.0.zip")
	if w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), cranWinBody) {
		t.Fatalf("binary download: %d", w.Code)
	}

	// CRAN rebuilds binaries in place: they are cached for the TTL only.
	if e := f.db.entry(f.repo.ID, "bin/windows/contrib/4.5/glue_1.8.0.zip"); e == nil || time.Until(e.ExpiresAt) > 24*time.Hour {
		t.Errorf("a binary package is pinned: %+v", e)
	}
}

func TestCRAN_RefusesAnIndexThatIsNotAnIndex(t *testing.T) {
	f, up := cranFixture(t)
	up.set("src/contrib/PACKAGES.gz", []byte("<html>maintenance</html>"))

	if w := f.do(http.MethodGet, "src/contrib/PACKAGES"); w.Code != http.StatusBadGateway {
		t.Errorf("HTML index: %d, want 502", w.Code)
	}

	if f.db.entry(f.repo.ID, "src/contrib/PACKAGES.gz") != nil {
		t.Error("an unusable index was cached")
	}
}

func TestCRAN_CurationHidesVersionsFromIndexAndDownloads(t *testing.T) {
	f, _ := cranFixture(t)
	f.useCuration(curation.ModeDenylist, blockRule("glue", "1.8.0"))

	idx := f.do(http.MethodGet, "src/contrib/PACKAGES")
	if idx.Code != http.StatusOK || strings.Contains(idx.Body.String(), "Package: glue") ||
		!strings.Contains(idx.Body.String(), "Package: zeta") {
		t.Errorf("index with glue 1.8.0 blocked:\n%s", idx.Body)
	}

	for _, path := range []string{"src/contrib/glue_1.8.0.tar.gz", "src/contrib/Archive/glue/glue_1.8.0.tar.gz"} {
		if w := f.do(http.MethodGet, path); w.Code != http.StatusForbidden {
			t.Errorf("%s: %d, want 403", path, w.Code)
		}
	}

	// The archived version is another version.
	if w := f.do(http.MethodGet, "src/contrib/Archive/glue/glue_1.6.0.tar.gz"); w.Code != http.StatusOK {
		t.Errorf("unblocked archived version: %d, want 200", w.Code)
	}
}
