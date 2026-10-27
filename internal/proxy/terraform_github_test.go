// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
)

const tfSHA = "0123456789abcdef0123456789abcdef01234567"

// tfEnt is one entry of a test archive; a name ending in "/" is a directory,
// link makes it a symlink.
type tfEnt struct{ name, body, link string }

func tarGzBytes(t *testing.T, ents ...tfEnt) []byte {
	t.Helper()

	var buf bytes.Buffer

	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	// GitHub's tarballs start with a pax global header carrying the commit.
	if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeXGlobalHeader, PAXRecords: map[string]string{"comment": tfSHA}}); err != nil {
		t.Fatal(err)
	}

	for _, e := range ents {
		hdr := &tar.Header{Name: e.name, Mode: 0o644, Typeflag: tar.TypeReg, Size: int64(len(e.body))}

		switch {
		case e.link != "":
			hdr.Typeflag, hdr.Linkname, hdr.Size = tar.TypeSymlink, e.link, 0
		case strings.HasSuffix(e.name, "/"):
			hdr.Typeflag, hdr.Mode, hdr.Size = tar.TypeDir, 0o755, 0
		}

		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}

		if hdr.Typeflag == tar.TypeReg {
			if _, err := io.WriteString(tw, e.body); err != nil {
				t.Fatal(err)
			}
		}
	}

	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}

	return buf.Bytes()
}

func zipBytes(t *testing.T, ents ...tfEnt) []byte {
	t.Helper()

	var buf bytes.Buffer

	zw := zip.NewWriter(&buf)

	for _, e := range ents {
		w, err := zw.Create(e.name)
		if err != nil {
			t.Fatal(err)
		}

		if _, err := io.WriteString(w, e.body); err != nil {
			t.Fatal(err)
		}
	}

	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	return buf.Bytes()
}

// untarGz lists the entries of a tar.gz as name -> body ("" for directories,
// whose names keep the trailing slash).
func untarGz(t *testing.T, data []byte) map[string]string {
	t.Helper()

	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("not gzip: %v", err)
	}

	got := map[string]string{}
	tr := tar.NewReader(gz)

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return got
		}

		if err != nil {
			t.Fatalf("not tar: %v", err)
		}

		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}

		got[hdr.Name] = string(body)
	}
}

func unzipBytes(t *testing.T, data []byte) map[string]string {
	t.Helper()

	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("not zip: %v", err)
	}

	got := map[string]string{}

	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}

		body, err := io.ReadAll(rc)
		_ = rc.Close()

		if err != nil {
			t.Fatal(err)
		}

		got[f.Name] = string(body)
	}

	return got
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}

	slices.Sort(keys)

	return keys
}

// codeloadFake plays codeload.github.com: it serves the bodies registered per
// path and records the hits and the credentials it was sent.
type codeloadFake struct {
	srv *httptest.Server

	mu     sync.Mutex
	bodies map[string][]byte
	hits   map[string]int
	auth   map[string]string
}

func (c *codeloadFake) serve(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	body, ok := c.bodies[r.URL.Path]
	c.hits[r.URL.Path]++
	c.auth[r.URL.Path] = r.Header.Get("Authorization")
	c.mu.Unlock()

	if !ok {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", "application/x-gzip")
	_, _ = w.Write(body)
}

func (c *codeloadFake) put(path string, body []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.bodies[path] = body
}

func (c *codeloadFake) hitCount(path string) int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.hits[path]
}

// newTFCodeload starts the fake and points the proxy at it.
func newTFCodeload(t *testing.T, f *tfFake) *codeloadFake {
	t.Helper()

	c := &codeloadFake{bodies: map[string][]byte{}, hits: map[string]int{}, auth: map[string]string{}}
	c.srv = httptest.NewServer(http.HandlerFunc(c.serve))
	t.Cleanup(c.srv.Close)

	f.fixture.svc.tfCodeload = c.srv.URL

	return c
}

// githubModule is the usual repository layout: a wrapper directory with the
// module at the root and another one in modules/x.
func githubModule(t *testing.T, wrapper string) []byte {
	t.Helper()

	return tarGzBytes(t,
		tfEnt{name: wrapper + "/"},
		tfEnt{name: wrapper + "/main.tf", body: `resource "null_resource" "root" {}`},
		tfEnt{name: wrapper + "/README.md", body: "# repo"},
		tfEnt{name: wrapper + "/modules/"},
		tfEnt{name: wrapper + "/modules/x/"},
		tfEnt{name: wrapper + "/modules/x/main.tf", body: `resource "null_resource" "x" {}`},
	)
}

// downloadGet asks for the download of demo/aws/1.0.0 and returns the ticket
// file name.
func (f *tfFake) downloadGet(t *testing.T) string {
	t.Helper()

	w := f.get("demo/aws/1.0.0/download")
	if w.Code != http.StatusNoContent {
		t.Fatalf("download status %d: %s", w.Code, w.Body)
	}

	return ticketFile(t, w.Header().Get("X-Terraform-Get"))
}

func TestTerraformGitHubModuleIsRepackedWithoutItsWrapper(t *testing.T) {
	for _, tc := range []struct {
		name, get, path string
	}{
		{"sha", "git::https://github.com/org/repo.git?ref=" + tfSHA + "&depth=1", "/org/repo/tar.gz/" + tfSHA},
		{"tag", "git::https://github.com/org/repo?ref=v1.2.3", "/org/repo/tar.gz/v1.2.3"},
		{"slash branch", "https://github.com/org/repo?ref=feature/x", "/org/repo/tar.gz/feature/x"},
		{"bare detector", "github.com/org/repo?ref=v1.2.3", "/org/repo/tar.gz/v1.2.3"},
		{"no ref", "git::https://github.com/org/repo.git", "/org/repo/tar.gz/HEAD"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newTFFake(t)
			c := newTFCodeload(t, f)
			c.put(tc.path, githubModule(t, "repo-"+tfSHA))
			f.set(func(cfg *tfCfg) { cfg.moduleGet = tc.get })

			file := f.downloadGet(t)
			if file != "demo-aws-1.0.0.tar.gz" {
				t.Fatalf("ticket file %q", file)
			}

			var first []byte

			for i := range 2 {
				got := f.redeem(tfModuleRedeem(file))
				if got.Code != http.StatusOK || got.Header().Get("Content-Type") != "application/gzip" {
					t.Fatalf("redeem %d: status %d type %q: %s", i, got.Code, got.Header().Get("Content-Type"), got.Body)
				}

				if i == 0 {
					first = got.Body.Bytes()
				} else if !bytes.Equal(first, got.Body.Bytes()) {
					t.Fatal("the cached archive changed between two downloads")
				}
			}

			files := untarGz(t, first)
			want := []string{"README.md", "main.tf", "modules/", "modules/x/", "modules/x/main.tf"}

			if keys := sortedKeys(files); !slices.Equal(keys, want) {
				t.Fatalf("entries %v, want %v", keys, want)
			}

			if files["main.tf"] != `resource "null_resource" "root" {}` {
				t.Fatalf("main.tf = %q", files["main.tf"])
			}

			if c.hitCount(tc.path) != 1 {
				t.Fatalf("codeload fetched %d times, want once and then cached", c.hitCount(tc.path))
			}

			if got := c.auth[tc.path]; got != "" {
				t.Fatalf("codeload got credentials: %q", got)
			}
		})
	}
}

func TestTerraformGitHubSubdirIsTheModule(t *testing.T) {
	for _, get := range []string{
		"git::https://github.com/org/repo.git//modules/x?ref=v1",
		"github.com/org/repo//modules/x?ref=v1",
		"git::https://github.com/org/repo.git?ref=v1//modules/x/",
	} {
		f := newTFFake(t)
		c := newTFCodeload(t, f)
		c.put("/org/repo/tar.gz/v1", githubModule(t, "repo-"+tfSHA))
		f.set(func(cfg *tfCfg) { cfg.moduleGet = get })

		w := f.get("demo/aws/1.0.0/download")
		if strings.HasSuffix(get, "x/") {
			// A selector after the query is not a form registries use.
			if w.Code != http.StatusNotImplemented {
				t.Errorf("%q: status %d, want 501", get, w.Code)
			}

			continue
		}

		if w.Code != http.StatusNoContent {
			t.Fatalf("%q: status %d: %s", get, w.Code, w.Body)
		}

		got := f.redeem(tfModuleRedeem(ticketFile(t, w.Header().Get("X-Terraform-Get"))))
		if got.Code != http.StatusOK {
			t.Fatalf("%q: redeem %d: %s", get, got.Code, got.Body)
		}

		files := untarGz(t, got.Body.Bytes())
		if len(files) != 1 || files["main.tf"] != `resource "null_resource" "x" {}` {
			t.Fatalf("%q: entries %v", get, files)
		}
	}
}

func TestTerraformPlainArchiveSubdir(t *testing.T) {
	zipBody := zipBytes(t,
		tfEnt{name: "wrap/other.tf", body: "other"},
		tfEnt{name: "wrap/sub/main.tf", body: "main"},
		tfEnt{name: "wrap/sub/lib/util.tf", body: "util"},
	)

	f := newTFFake(t)
	f.set(func(c *tfCfg) {
		c.moduleBody = string(zipBody)
		c.moduleGet = f.release.URL + "/modules/demo.zip//wrap/sub"
	})

	file := f.downloadGet(t)
	if file != "demo-aws-1.0.0.zip" {
		t.Fatalf("ticket file %q", file)
	}

	got := f.redeem(tfModuleRedeem(file))
	if got.Code != http.StatusOK || got.Header().Get("Content-Type") != "application/zip" {
		t.Fatalf("redeem %d type %q: %s", got.Code, got.Header().Get("Content-Type"), got.Body)
	}

	files := unzipBytes(t, got.Body.Bytes())
	if keys := sortedKeys(files); !slices.Equal(keys, []string{"lib/util.tf", "main.tf"}) || files["main.tf"] != "main" {
		t.Fatalf("zip entries %v", files)
	}

	// A tar.gz named by the archive hint, with the selector before the query.
	tgz := tarGzBytes(t, tfEnt{name: "sub/main.tf", body: "main"}, tfEnt{name: "elsewhere/x.tf", body: "x"})

	g := newTFFake(t)
	g.set(func(c *tfCfg) {
		c.moduleBody = string(tgz)
		c.moduleGet = g.release.URL + "/modules/demo//sub?archive=tar.gz"
	})

	got = g.redeem(tfModuleRedeem(g.downloadGet(t)))
	if got.Code != http.StatusOK {
		t.Fatalf("tar.gz redeem %d: %s", got.Code, got.Body)
	}

	if files := untarGz(t, got.Body.Bytes()); len(files) != 1 || files["main.tf"] != "main" {
		t.Fatalf("tar.gz entries %v", files)
	}
}

func TestTerraformRepackRefusesUnsafeOrMismatchedArchives(t *testing.T) {
	for _, tc := range []struct {
		name string
		get  string
		body []byte
	}{
		{"traversal", "git::https://github.com/org/repo?ref=v1", tarGzBytes(t, tfEnt{name: "repo/"}, tfEnt{name: "repo/../../evil.tf", body: "x"})},
		{"absolute", "git::https://github.com/org/repo?ref=v1", tarGzBytes(t, tfEnt{name: "repo/"}, tfEnt{name: "/etc/passwd", body: "x"})},
		{"symlink", "git::https://github.com/org/repo?ref=v1", tarGzBytes(t, tfEnt{name: "repo/main.tf", body: "x"}, tfEnt{name: "repo/link", link: "/etc/passwd"})},
		{"two top-level directories", "git::https://github.com/org/repo?ref=v1", tarGzBytes(t, tfEnt{name: "a/main.tf", body: "x"}, tfEnt{name: "b/main.tf", body: "x"})},
		{"file beside the wrapper", "git::https://github.com/org/repo?ref=v1", tarGzBytes(t, tfEnt{name: "repo/main.tf", body: "x"}, tfEnt{name: "README", body: "x"})},
		{"missing subdirectory", "git::https://github.com/org/repo//nope?ref=v1", githubModule(t, "repo")},
		{"subdirectory is a file", "git::https://github.com/org/repo//main.tf?ref=v1", githubModule(t, "repo")},
		{"empty archive", "git::https://github.com/org/repo?ref=v1", tarGzBytes(t, tfEnt{name: "repo/"})},
		{"not an archive", "git::https://github.com/org/repo?ref=v1", []byte("<html>rate limited</html>")},
		{"duplicate entries", "git::https://github.com/org/repo?ref=v1", tarGzBytes(t, tfEnt{name: "repo/a.tf", body: "1"}, tfEnt{name: "repo/a.tf", body: "2"})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newTFFake(t)
			c := newTFCodeload(t, f)
			c.put("/org/repo/tar.gz/v1", tc.body)
			f.set(func(cfg *tfCfg) { cfg.moduleGet = tc.get })

			file := f.downloadGet(t)

			if got := f.redeem(tfModuleRedeem(file)); got.Code != http.StatusBadGateway {
				t.Fatalf("status %d, want 502: %s", got.Code, got.Body)
			}

			if f.fixture.db.entry(f.fixture.repo.ID, tfModuleArchiveKey("demo", "aws", "1.0.0", file)) != nil {
				t.Fatal("a refused archive was cached")
			}
		})
	}
}

func TestTerraformRepackRefusesAnOversizedDownload(t *testing.T) {
	f := newTFFake(t)
	c := newTFCodeload(t, f)
	c.put("/org/repo/tar.gz/v1", githubModule(t, "repo"))
	f.set(func(cfg *tfCfg) { cfg.moduleGet = "git::https://github.com/org/repo?ref=v1" })
	f.fixture.svc.maxBody = 64

	file := f.downloadGet(t)

	if got := f.redeem(tfModuleRedeem(file)); got.Code != http.StatusBadGateway {
		t.Fatalf("status %d, want 502", got.Code)
	}

	if f.fixture.db.entry(f.fixture.repo.ID, tfModuleArchiveKey("demo", "aws", "1.0.0", file)) != nil {
		t.Fatal("an oversized archive was cached")
	}
}

func TestTerraformGitHubMissingRefIs404(t *testing.T) {
	f := newTFFake(t)
	newTFCodeload(t, f)
	f.set(func(cfg *tfCfg) { cfg.moduleGet = "git::https://github.com/org/repo?ref=gone" })

	file := f.downloadGet(t)

	if got := f.redeem(tfModuleRedeem(file)); got.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", got.Code)
	}
}

func TestTerraformGitHubPinAppliesToTheRepackedBytes(t *testing.T) {
	f := newTFFake(t)
	c := newTFCodeload(t, f)
	c.put("/org/repo/tar.gz/v1", githubModule(t, "repo-a"))
	f.set(func(cfg *tfCfg) { cfg.moduleGet = "git::https://github.com/org/repo?ref=v1" })

	repoID := f.fixture.repo.ID
	file := f.downloadGet(t)
	key := tfModuleArchiveKey("demo", "aws", "1.0.0", file)

	if got := f.redeem(tfModuleRedeem(file)); got.Code != http.StatusOK {
		t.Fatalf("first fetch %d: %s", got.Code, got.Body)
	}

	first := f.fixture.db.entry(repoID, key).BlobKey

	// Another wrapper directory name (a new commit) with the same content is
	// the same repacked archive, so the pin holds and a refetch is accepted.
	c.put("/org/repo/tar.gz/v1", githubModule(t, "repo-b"))
	f.fixture.db.expire(repoID, key)

	if got := f.redeem(tfModuleRedeem(file)); got.Code != http.StatusOK {
		t.Fatalf("refetch with the same content: %d: %s", got.Code, got.Body)
	}

	if cur := f.fixture.db.entry(repoID, key); cur == nil || cur.BlobKey != first {
		t.Fatalf("pin changed for identical content: %+v", cur)
	}

	// Changed content under the same ref is refused and the pin stays.
	c.put("/org/repo/tar.gz/v1", tarGzBytes(t, tfEnt{name: "repo-c/main.tf", body: "tampered"}))
	f.fixture.db.expire(repoID, key)
	f.redeem(tfModuleRedeem(file))

	if cur := f.fixture.db.entry(repoID, key); cur == nil || cur.BlobKey != first {
		t.Fatalf("the pinned archive changed: %+v", cur)
	}
}

func TestParseModuleGetGitHub(t *testing.T) {
	base := mustURL(t, "https://registry.example/v1/modules/ns/demo/aws/1.0.0/download")
	const cl = tfCodeloadBase

	for _, tc := range []struct {
		in, url, subdir string
	}{
		{"git::https://github.com/o/r?ref=v1", cl + "/o/r/tar.gz/v1", ""},
		{"git::https://github.com/o/r.git?ref=" + tfSHA + "&depth=1", cl + "/o/r/tar.gz/" + tfSHA, ""},
		{"git::https://github.com/o/r.git", cl + "/o/r/tar.gz/HEAD", ""},
		{"https://github.com/o/r", cl + "/o/r/tar.gz/HEAD", ""},
		{"https://github.com/o/r.git?ref=refs/tags/v1.0.0", cl + "/o/r/tar.gz/refs/tags/v1.0.0", ""},
		{"github.com/o/r?ref=main", cl + "/o/r/tar.gz/main", ""},
		{"github.com/my-org/my.repo_x//modules/a-b?ref=v2", cl + "/my-org/my.repo_x/tar.gz/v2", "modules/a-b"},
		{"git::https://github.com/o/r.git//a/b/c?ref=v1&depth=1", cl + "/o/r/tar.gz/v1", "a/b/c"},
		{"git::https://github.com/o/r.git//sub/", cl + "/o/r/tar.gz/HEAD", "sub"},
		{"git::https://github.com/o/r//", cl + "/o/r/tar.gz/HEAD", ""},
		{"git::HTTP://GITHUB.com/o/r?ref=v1", cl + "/o/r/tar.gz/v1", ""},
	} {
		got, err := parseModuleGet(tc.in, base, cl)
		if err != nil || got.url != tc.url || got.subdir != tc.subdir || !got.strip || got.ext != "tar.gz" || !got.repack() {
			t.Errorf("%q = %+v, %v; want %s subdir %q", tc.in, got, err, tc.url, tc.subdir)
		}
	}

	for _, in := range []string{
		"git::https://github.com/o/r?ref=../x",
		"git::https://github.com/o/r?ref=a..b",
		"git::https://github.com/o/r?ref=-flag",
		"git::https://github.com/o/r?ref=a//b",
		"git::https://github.com/o/r?ref=a%20b",
		"git::https://github.com/o/r?ref=v1&ref=v2",
		"git::https://github.com/o/r?sshkey=abc",
		"git::https://github.com/o/r?checksum=md5:abc",
		"git::https://github.com/o/r?depth=x",
		"git::https://github.com/o/r?filename=x",
		"git::https://github.com/o/r?ref=v1;x=y",
		"git::https://github.com/o",
		"git::https://github.com/o/r/extra",
		"git::https://github.com/o/r/tree/main",
		"git::https://github.com/-o/r",
		"git::https://github.com/o/..",
		"git::https://github.com/o/.git",
		"git::https://u:p@github.com/o/r",
		"git::https://github.com:8443/o/r",
		"git::https://github.com/o/r#frag",
		"git::ssh://git@github.com/o/r.git",
		"git::git@github.com:o/r.git",
		"git::https://gitlab.com/o/r.git",
		"git::https://github.example.com/o/r.git",
		"git::https://github.com.evil.test/o/r.git",
		"git::https://github.com/o/r//../x",
		"git::https://github.com/o/r//a/../x",
		"git::https://github.com/o/r//a//b",
		"git::https://github.com/o/r//a%2Fb",
		"git::https://github.com/o/r//a\\b",
		"git::https://github.com/o/r//./a",
		"hg::https://github.com/o/r",
		"https://example.com/x.tar.bz2//sub",
		"https://example.com/x.zip//..",
	} {
		if _, err := parseModuleGet(in, base, cl); err == nil || !strings.Contains(err.Error(), "unsupported") {
			t.Errorf("%q: error %v, want an unsupported-source error", in, err)
		}
	}
}

func TestParseModuleGetPlainSubdir(t *testing.T) {
	base := mustURL(t, "https://registry.example/v1/modules/ns/demo/aws/1.0.0/download")

	for _, tc := range []struct {
		in, url, ext, subdir string
		strip                bool
	}{
		{"https://h.example/x.tar.gz//wrap/sub", "https://h.example/x.tar.gz", "tar.gz", "wrap/sub", false},
		{"https://h.example/x.zip//sub?token=a", "https://h.example/x.zip?token=a", "zip", "sub", false},
		{"https://h.example/x//sub?archive=tgz&t=1", "https://h.example/x?t=1", "tgz", "sub", false},
		{"/dl/x.zip//sub", "https://registry.example/dl/x.zip", "zip", "sub", false},
		// A "//" in a query value is not a subdirectory selector.
		{"https://h.example/x.zip?next=https://y.example/z", "https://h.example/x.zip?next=https://y.example/z", "zip", "", false},
		// A release archive on github.com is a plain URL, not a repository.
		{"https://github.com/o/r/archive/v1.zip", "https://github.com/o/r/archive/v1.zip", "zip", "", false},
		{"https://github.com/o/r/archive/refs/tags/v1.tar.gz//sub", "https://github.com/o/r/archive/refs/tags/v1.tar.gz", "tar.gz", "sub", false},
	} {
		got, err := parseModuleGet(tc.in, base, tfCodeloadBase)
		if err != nil || got.url != tc.url || got.ext != tc.ext || got.subdir != tc.subdir || got.strip != tc.strip {
			t.Errorf("%q = %+v, %v; want %s (%s) subdir %q", tc.in, got, err, tc.url, tc.ext, tc.subdir)
		}
	}
}
