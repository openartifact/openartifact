// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package group

import (
	"bytes"
	"net/http"
	"slices"
	"strings"
	"testing"

	pkgcran "github.com/openartifact/openartifact/pkg/cran"
)

// cranIndexOf is the PACKAGES text a member serves for packages given as
// name -> version; the MD5sum names the member so the winner is observable.
func cranIndexOf(member string, pkgs map[string]string) string {
	names := make([]string, 0, len(pkgs))
	for n := range pkgs {
		names = append(names, n)
	}

	slices.Sort(names)

	recs := make([]pkgcran.Record, 0, len(names))
	for _, n := range names {
		recs = append(recs, pkgcran.IndexRecord(n, pkgs[n], member+"-"+n+"-"+pkgs[n], nil, false))
	}

	return string(pkgcran.RenderText(recs))
}

// cranListed parses a merged PACKAGES into package -> "version/md5".
func cranListed(t *testing.T, body []byte) map[string]string {
	t.Helper()

	recs, err := pkgcran.ParsePackages(bytes.NewReader(body), pkgcran.MaxIndexBytes)
	if err != nil {
		t.Fatalf("merged index is not an index: %v\n%s", err, body)
	}

	out := map[string]string{}
	for _, r := range recs {
		out[r.Get("Package")] = r.Get("Version") + "/" + r.Get("MD5sum")
	}

	return out
}

const cranRoot = "src/contrib/Archive/"

func cranArchiveListing(names ...string) string {
	var b strings.Builder

	for _, n := range names {
		b.WriteString(`<li><a href="` + n + `/">` + n + "/</a></li>\n")
	}

	return b.String()
}

func cranGroup(t *testing.T) *fixture {
	t.Helper()

	f := newFixture(t, "cran", member("hosted", "cran", "hosted"), member("mirror", "cran", "proxy"))
	f.grp.MergeProxyVersions = false

	return f
}

func TestCRANGroup_IndexHighestVersionWinsAndHostedNamesHideTheProxy(t *testing.T) {
	f := cranGroup(t)
	f.set("hosted", "src/contrib/PACKAGES", 200, cranIndexOf("hosted", map[string]string{"mine": "1.0", "glue": "0.1"}))
	f.set("hosted", cranRoot, 200, cranArchiveListing("mine", "glue"))
	f.set(
		"mirror",
		"src/contrib/PACKAGES",
		200,
		cranIndexOf("mirror", map[string]string{"glue": "1.8.0", "mine": "9.9", "jsonlite": "1.8"}),
	)

	w := f.get(http.MethodGet, "src/contrib/PACKAGES")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}

	// A name a hosted member holds comes from hosted members only (dependency confusion).
	want := map[string]string{"mine": "1.0/hosted-mine-1.0", "glue": "0.1/hosted-glue-0.1", "jsonlite": "1.8/mirror-jsonlite-1.8"}
	if got := cranListed(t, w.Body.Bytes()); !equalMaps(got, want) {
		t.Errorf("merged index = %v, want %v", got, want)
	}

	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q", ct)
	}
}

func equalMaps(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}

	for k, v := range a {
		if b[k] != v {
			return false
		}
	}

	return true
}

func TestCRANGroup_MergeProxyVersionsKeepsTheHighestVersion(t *testing.T) {
	f := cranGroup(t)
	f.grp.MergeProxyVersions = true
	f.set("hosted", "src/contrib/PACKAGES", 200, cranIndexOf("hosted", map[string]string{"glue": "1.10.0", "tie": "1.0"}))
	f.set("mirror", "src/contrib/PACKAGES", 200, cranIndexOf("mirror", map[string]string{"glue": "1.9.0", "tie": "1.0", "other": "2.0"}))

	w := f.get(http.MethodGet, "src/contrib/PACKAGES")
	want := map[string]string{"glue": "1.10.0/hosted-glue-1.10.0", "tie": "1.0/hosted-tie-1.0", "other": "2.0/mirror-other-2.0"}

	if got := cranListed(t, w.Body.Bytes()); w.Code != http.StatusOK || !equalMaps(got, want) {
		t.Errorf("merged index = %d %v, want %v", w.Code, got, want)
	}
}

func TestCRANGroup_VariantsAreRenderedFromTheMembersPlainIndex(t *testing.T) {
	f := cranGroup(t)
	f.set("hosted", "src/contrib/PACKAGES", 200, cranIndexOf("hosted", map[string]string{"mine": "1.0"}))
	f.set("hosted", cranRoot, 200, cranArchiveListing("mine"))

	text := f.get(http.MethodGet, "src/contrib/PACKAGES")

	gz := f.get(http.MethodGet, "src/contrib/PACKAGES.gz")
	if gz.Code != http.StatusOK {
		t.Fatalf("PACKAGES.gz = %d: %s", gz.Code, gz.Body)
	}

	plain, err := pkgcran.Gunzip(bytes.NewReader(gz.Body.Bytes()), pkgcran.MaxIndexBytes)
	if err != nil || !bytes.Equal(plain, text.Body.Bytes()) {
		t.Errorf("PACKAGES.gz does not hold PACKAGES: %v", err)
	}

	if rds := f.get(http.MethodGet, "src/contrib/PACKAGES.rds"); rds.Code != http.StatusOK || rds.Body.Len() == 0 {
		t.Errorf("PACKAGES.rds = %d with %d bytes", rds.Code, rds.Body.Len())
	}

	if etag := gz.Header().Get("ETag"); etag == "" || etag == text.Header().Get("ETag") {
		t.Errorf("ETags: gz %q, plain %q", etag, text.Header().Get("ETag"))
	}
}

func TestCRANGroup_PackageFilesAreFirstHit(t *testing.T) {
	f := cranGroup(t)
	f.set("mirror", "src/contrib/glue_1.8.0.tar.gz", 200, "glue-bytes")

	w := f.get(http.MethodGet, "src/contrib/glue_1.8.0.tar.gz")
	if w.Code != http.StatusOK || w.Body.String() != "glue-bytes" {
		t.Errorf("file = %d %q", w.Code, w.Body)
	}
}

func TestCRANGroup_HostedFileIsServedAndTheProxyIsNotAskedForItsName(t *testing.T) {
	f := cranGroup(t)
	f.set("hosted", cranRoot+"mine/", 200, cranArchiveListing())
	f.set("hosted", "src/contrib/mine_1.0.tar.gz", 200, "hosted-bytes")
	f.set("mirror", "src/contrib/mine_9.9.tar.gz", 200, "evil")

	if w := f.get(http.MethodGet, "src/contrib/mine_1.0.tar.gz"); w.Code != http.StatusOK || w.Body.String() != "hosted-bytes" {
		t.Errorf("hosted file = %d %q", w.Code, w.Body)
	}

	f.asked = nil

	if w := f.get(
		http.MethodGet,
		"src/contrib/mine_9.9.tar.gz",
	); w.Code != http.StatusNotFound ||
		strings.Contains(w.Body.String(), "evil") {
		t.Errorf("proxy file of a hosted name = %d %q, want 404", w.Code, w.Body)
	}

	if slices.Contains(f.asked, "mirror") {
		t.Errorf("the proxy was asked for a name a hosted member holds: %v", f.asked)
	}
}

func TestCRANGroup_FailingHostedMemberFailsClosed(t *testing.T) {
	f := cranGroup(t)
	f.set("hosted", "src/contrib/PACKAGES", 200, cranIndexOf("hosted", map[string]string{"mine": "1.0"}))
	f.set("hosted", cranRoot, 500, "boom")
	f.set("mirror", "src/contrib/PACKAGES", 200, cranIndexOf("mirror", map[string]string{"mine": "9.9"}))

	if w := f.get(http.MethodGet, "src/contrib/PACKAGES"); w.Code != http.StatusBadGateway {
		t.Errorf("index with an unreadable hosted name list = %d, want 502", w.Code)
	}

	g := cranGroup(t)
	g.set("hosted", cranRoot+"mine/", 500, "boom")
	g.set("mirror", "src/contrib/mine_9.9.tar.gz", 200, "evil")

	if w := g.get(http.MethodGet, "src/contrib/mine_9.9.tar.gz"); w.Code != http.StatusBadGateway {
		t.Errorf("file with a failing hosted probe = %d, want 502", w.Code)
	}
}

func TestCRANGroup_UnreadableHostedMemberDoesNotHideTheProxy(t *testing.T) {
	f := cranGroup(t)
	f.denied["hosted"] = true
	f.set("hosted", cranRoot, 200, cranArchiveListing("mine"))
	f.set("mirror", "src/contrib/PACKAGES", 200, cranIndexOf("mirror", map[string]string{"mine": "9.9"}))

	w := f.get(http.MethodGet, "src/contrib/PACKAGES")
	if got := cranListed(t, w.Body.Bytes()); w.Code != http.StatusOK || got["mine"] != "9.9/mirror-mine-9.9" {
		t.Errorf("index = %d %v, want the proxy's", w.Code, got)
	}
}

func TestCRANGroup_MemberFailureHandling(t *testing.T) {
	t.Run("a failing member is ignored while another contributes", func(t *testing.T) {
		f := cranGroup(t)
		f.grp.MergeProxyVersions = true
		f.set("hosted", "src/contrib/PACKAGES", 200, cranIndexOf("hosted", map[string]string{"mine": "1.0"}))
		f.set("mirror", "src/contrib/PACKAGES", 502, "down")

		if w := f.get(http.MethodGet, "src/contrib/PACKAGES"); w.Code != http.StatusOK {
			t.Errorf("status %d, want 200", w.Code)
		}
	})

	t.Run("an unreadable member index is ignored", func(t *testing.T) {
		f := cranGroup(t)
		f.grp.MergeProxyVersions = true
		f.set("hosted", "src/contrib/PACKAGES", 200, cranIndexOf("hosted", map[string]string{"mine": "1.0"}))
		f.set("mirror", "src/contrib/PACKAGES", 200, "<html>nope</html>")

		w := f.get(http.MethodGet, "src/contrib/PACKAGES")
		if got := cranListed(t, w.Body.Bytes()); w.Code != http.StatusOK || len(got) != 1 {
			t.Errorf("status %d, index %v", w.Code, got)
		}
	})

	t.Run("nobody has it", func(t *testing.T) {
		f := cranGroup(t)
		f.grp.MergeProxyVersions = true

		if w := f.get(http.MethodGet, "src/contrib/PACKAGES"); w.Code != http.StatusNotFound {
			t.Errorf("status %d, want 404", w.Code)
		}
	})

	t.Run("nobody has it and one member failed", func(t *testing.T) {
		f := cranGroup(t)
		f.grp.MergeProxyVersions = true
		f.set("mirror", "src/contrib/PACKAGES", 502, "down")

		if w := f.get(http.MethodGet, "src/contrib/PACKAGES"); w.Code != http.StatusBadGateway {
			t.Errorf("status %d, want 502", w.Code)
		}
	})
}

func TestCRANGroup_OnlyIndexesAndPackageFilesAreServed(t *testing.T) {
	f := cranGroup(t)
	// A hosted member answers its package name list; the group must not expose it.
	f.set("hosted", cranRoot, 200, cranArchiveListing("mine"))
	f.set("hosted", "other", 200, "x")

	for _, p := range []string{cranRoot, "src/contrib/", "other", "src/contrib/../x", ""} {
		if w := f.get(http.MethodGet, p); w.Code != http.StatusNotFound {
			t.Errorf("GET %q = %d %q, want 404", p, w.Code, w.Body)
		}
	}

	if w := f.get(http.MethodPut, "src/contrib/mine_1.0.tar.gz"); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("PUT = %d, want 405", w.Code)
	}
}

func TestCRANGroup_ArchiveListingIsFirstHit(t *testing.T) {
	f := cranGroup(t)
	f.set("hosted", cranRoot+"mine/", 200, `<a href="mine_1.0.tar.gz">mine_1.0.tar.gz</a>`)

	w := f.get(http.MethodGet, cranRoot+"mine/")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "mine_1.0.tar.gz") {
		t.Errorf("archive listing = %d %q", w.Code, w.Body)
	}
}
