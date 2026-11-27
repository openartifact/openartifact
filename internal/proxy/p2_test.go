// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"archive/zip"
	"bytes"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ulikunitz/xz"

	"github.com/openartifact/openartifact/pkg/p2/p2test"
)

const p2Bundle = "plugins/org.example.core_1.2.3.v20240101.jar"

// p2Upstream is an update site below /releases/2024-12 of a host that also
// serves /technology/epp/ (what the composite's "../../technology/epp/" names).
type p2Upstream struct {
	*fixture

	mu   sync.Mutex
	hits map[string]int
}

func (u *p2Upstream) hit(path string) int {
	u.mu.Lock()
	defer u.mu.Unlock()

	return u.hits[path]
}

func newP2Fixture(t *testing.T) *p2Upstream {
	t.Helper()

	u := &p2Upstream{hits: map[string]int{}}
	// The composite names its children by the test server's own address, which
	// is only known once the server runs: serve it through a placeholder.
	var base string

	files := func() map[string][]byte {
		composite := strings.Replace(p2test.Composite, "https://updates.example.org/releases/2024-12", base+"/releases/2024-12", 1)

		return map[string][]byte{
			"/releases/2024-12/p2.index": []byte(
				"version=1\nartifact.repository.factory.order=compositeArtifacts.xml\n",
			),
			"/releases/2024-12/compositeArtifacts.jar":        p2test.Jar(t, "compositeArtifacts.xml", composite),
			"/releases/2024-12/202412041000/artifacts.xml.xz": p2test.XZ(t, p2test.Artifacts),
			"/releases/2024-12/202412041000/artifacts.jar":    p2test.Jar(t, "artifacts.xml", p2test.Artifacts),
			"/releases/2024-12/202412041000/" + p2Bundle:      []byte("bundle-bytes"),
			"/releases/2024-12/not-xml/artifacts.xml":         []byte("<html><body>captive portal</body></html>"),
			"/technology/epp/2024-12/p2.index":                []byte("version=1\n"),
			"/technology/epp/2024-12/" + p2Bundle:             []byte("epp-bundle"),
		}
	}

	u.fixture = newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		u.mu.Lock()
		u.hits[r.URL.Path]++
		u.mu.Unlock()

		body, ok := files()[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}

		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(body)
	})
	base = u.upstream.URL
	u.repo.Format = "p2"
	u.repo.UpstreamURL = base + "/releases/2024-12"

	return u
}

func jarEntry(t *testing.T, body []byte, name string) string {
	t.Helper()

	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("not a jar: %v", err)
	}

	for _, f := range zr.File {
		if f.Name == name {
			rc, err := f.Open()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = rc.Close() }()

			b, err := io.ReadAll(rc)
			if err != nil {
				t.Fatal(err)
			}

			return string(b)
		}
	}

	t.Fatalf("no %s in the jar", name)

	return ""
}

func TestP2Proxy_CompositeChildrenStayOnTheProxy(t *testing.T) {
	u := newP2Fixture(t)

	w := u.do(http.MethodGet, "compositeArtifacts.jar")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}

	doc := jarEntry(t, w.Body.Bytes(), "compositeArtifacts.xml")

	for _, want := range []string{
		"<child location='202412041000' />",                   // relative: untouched
		"<child location='_host/technology/epp/2024-12/'/>",   // leaves the repository
		"<child location='202412041000'/>",                    // absolute on the upstream: now relative
		"<child location='https://other.example.net/repo/'/>", // another host: as it was
		"<properties size='1'>",                               // mirrorsURL dropped
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("missing %q in\n%s", want, doc)
		}
	}

	if strings.Contains(doc, "mirrors") || strings.Contains(doc, u.upstream.URL) {
		t.Errorf("the rewritten document still names the mirror list or the upstream:\n%s", doc)
	}

	// Served from the cache, rewritten, with no second upstream request.
	again := u.do(http.MethodGet, "compositeArtifacts.jar")
	if !bytes.Equal(again.Body.Bytes(), w.Body.Bytes()) || u.hit("/releases/2024-12/compositeArtifacts.jar") != 1 {
		t.Errorf("second request: %d upstream hits, same body %v",
			u.hit("/releases/2024-12/compositeArtifacts.jar"), bytes.Equal(again.Body.Bytes(), w.Body.Bytes()))
	}
}

func TestP2Proxy_HostNamespaceReachesTheUpstreamHost(t *testing.T) {
	u := newP2Fixture(t)

	w := u.do(http.MethodGet, "_host/technology/epp/2024-12/"+p2Bundle)
	if w.Code != http.StatusOK || w.Body.String() != "epp-bundle" {
		t.Fatalf("status %d body %q", w.Code, w.Body.String())
	}

	if u.hit("/technology/epp/2024-12/"+p2Bundle) != 1 {
		t.Error("the upstream host path was not requested")
	}

	// A bundle is immutable: cached for good.
	e := u.db.entry("repo-1", "_host/technology/epp/2024-12/"+p2Bundle)
	if e == nil || e.ExpiresAt.Before(time.Now().Add(365*24*time.Hour)) {
		t.Errorf("bundle entry = %+v, want it pinned", e)
	}

	if w := u.do(http.MethodGet, "_host/technology/epp/2024-12/missing.jar"); w.Code != http.StatusNotFound {
		t.Errorf("missing file: status %d", w.Code)
	}

	if w := u.do(http.MethodGet, "_host/"); w.Code != http.StatusNotFound {
		t.Errorf("empty host path: status %d", w.Code)
	}
}

func TestP2Proxy_Encodings(t *testing.T) {
	u := newP2Fixture(t)

	for _, key := range []string{"202412041000/artifacts.jar", "202412041000/artifacts.xml.xz"} {
		w := u.do(http.MethodGet, key)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: status %d: %s", key, w.Code, w.Body.String())
		}

		var doc string
		if strings.HasSuffix(key, ".jar") {
			doc = jarEntry(t, w.Body.Bytes(), "artifacts.xml")
		} else {
			doc = decodeXZ(t, w.Body.Bytes())
		}

		if strings.Contains(doc, "download.php") || strings.Contains(doc, "statsURI") {
			t.Errorf("%s: mirror or statistics property survived:\n%s", key, doc)
		}

		if !strings.Contains(doc, "org.example.core") || !strings.Contains(doc, "<properties size='2'>") {
			t.Errorf("%s: artifact or property sizes lost:\n%s", key, doc)
		}
	}
}

func TestP2Proxy_BundlesAndIndexAreServedVerbatim(t *testing.T) {
	u := newP2Fixture(t)

	for key, want := range map[string]string{
		"p2.index":                 "version=1\nartifact.repository.factory.order=compositeArtifacts.xml\n",
		"202412041000/" + p2Bundle: "bundle-bytes",
	} {
		if w := u.do(http.MethodGet, key); w.Code != http.StatusOK || w.Body.String() != want {
			t.Errorf("%s: status %d body %q", key, w.Code, w.Body.String())
		}
	}

	e := u.db.entry("repo-1", "202412041000/"+p2Bundle)
	if e == nil || e.ExpiresAt.Before(time.Now().Add(365*24*time.Hour)) {
		t.Errorf("bundle entry = %+v, want it pinned", e)
	}

	if e := u.db.entry("repo-1", "p2.index"); e == nil || e.ExpiresAt.After(time.Now().Add(24*time.Hour)) {
		t.Errorf("p2.index entry = %+v, want the repository TTL", e)
	}
}

func TestP2Proxy_NotARepositoryDocumentIsRefused(t *testing.T) {
	u := newP2Fixture(t)

	w := u.do(http.MethodGet, "not-xml/artifacts.xml")
	if w.Code != http.StatusBadGateway {
		t.Errorf("status %d, want 502 for an HTML answer in place of artifacts.xml", w.Code)
	}

	if u.db.entry("repo-1", "not-xml/artifacts.xml") != nil {
		t.Error("the refused document was cached")
	}
}

func TestP2Proxy_MissingFilesAre404(t *testing.T) {
	u := newP2Fixture(t)

	for _, key := range []string{"compositeContent.xml", "content.jar", "plugins/none_1.0.0.jar"} {
		if w := u.do(http.MethodGet, key); w.Code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", key, w.Code)
		}
	}
}

func decodeXZ(t *testing.T, b []byte) string {
	t.Helper()

	r, err := xz.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}

	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}

	return string(out)
}
