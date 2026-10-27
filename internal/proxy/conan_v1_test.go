// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

const (
	conanV1Recipe  = "v1/conans/zlib/1.3.1/_/_"
	conanV1Package = conanV1Recipe + "/packages/" + conanPID
	conanV1Files   = "v1/files/zlib/1.3.1/_/_/" + conanRRev
)

func conanV1Fixture(t *testing.T) (*fixture, *fakeConan) {
	t.Helper()

	f, c := conanFixture(t)
	f.svc.SetBaseURL(oaBase + "/")
	c.v1 = true

	return f, c
}

func conanURLs(t *testing.T, f *fixture, path string) map[string]string {
	t.Helper()

	w := f.do(http.MethodGet, path)
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s = %d %q", path, w.Code, w.Body.String())
	}

	var urls map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &urls); err != nil {
		t.Fatalf("GET %s: %v: %s", path, err, w.Body.String())
	}

	return urls
}

func TestConanProxyV1_DownloadURLsNameTheNewestRevisionsFilesAtTheProxy(t *testing.T) {
	f, _ := conanV1Fixture(t)

	got := conanURLs(t, f, conanV1Recipe+"/download_urls")
	root := oaBase + "/repos/conan-proxy/" + conanV1Files + "/export/"

	if len(got) != 2 || got["conanfile.py"] != root+"conanfile.py" || got["conanmanifest.txt"] != root+"conanmanifest.txt" {
		t.Errorf("download_urls = %v", got)
	}

	got = conanURLs(t, f, conanV1Recipe+"/digest")
	if len(got) != 1 || got["conanmanifest.txt"] != root+"conanmanifest.txt" {
		t.Errorf("digest = %v, want only the manifest", got)
	}
}

func TestConanProxyV1_PackageURLsUseTheNewestPackageRevision(t *testing.T) {
	f, _ := conanV1Fixture(t)

	got := conanURLs(t, f, conanV1Package+"/download_urls")
	want := oaBase + "/repos/conan-proxy/" + conanV1Files + "/package/" + conanPID + "/" + conanPRev + "/conanmanifest.txt"

	if got["conanmanifest.txt"] != want {
		t.Errorf("package download_urls = %v, want %s", got, want)
	}

	if got = conanURLs(t, f, conanV1Package+"/digest"); len(got) != 1 || got["conanmanifest.txt"] != want {
		t.Errorf("package digest = %v", got)
	}
}

func TestConanProxyV1_FileRequestsShareTheCacheWithConan2(t *testing.T) {
	f, c := conanV1Fixture(t)

	w := f.do(http.MethodGet, conanV1Files+"/package/"+conanPID+"/"+conanPRev+"/conan_package.tgz")
	if w.Code != http.StatusOK || w.Body.Len() == 0 {
		t.Fatalf("v1 file = %d", w.Code)
	}

	if w := f.do(http.MethodGet, conanPRoot+"/files/conan_package.tgz"); w.Code != http.StatusOK {
		t.Fatalf("v2 file = %d", w.Code)
	}

	if n := c.seen(conanPRoot + "/files/conan_package.tgz"); n != 1 {
		t.Errorf("upstream served the package file %d times, want 1: both clients share one cached copy", n)
	}

	if w := f.do(http.MethodGet, conanV1Files+"/export/../x"); w.Code != http.StatusNotFound {
		t.Errorf("traversal = %d, want 404", w.Code)
	}
}

func TestConanProxyV1_RecipeThatDoesNotExistIs404(t *testing.T) {
	f, _ := conanV1Fixture(t)

	for _, path := range []string{
		"v1/conans/nothere/1.0/_/_/download_urls", "v1/conans/nothere/1.0/_/_/digest",
		conanV1Recipe + "/packages/" + strings.Repeat("e", 40) + "/download_urls",
	} {
		if w := f.do(http.MethodGet, path); w.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, w.Code)
		}
	}
}

func TestConanProxyV1_SearchAndSnapshotArePassedThrough(t *testing.T) {
	f, c := conanV1Fixture(t)

	for path, want := range map[string]string{
		"v1/conans/search":        "zlib/1.3.1@_/_",
		conanV1Recipe + "/search": conanPID,
		conanV1Recipe:             "conanfile.py",
		conanV1Package:            "conaninfo.txt",
	} {
		w := f.doQuery(path, "q=zlib*")
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), want) {
			t.Errorf("GET %s = %d %q, want %q", path, w.Code, w.Body.String(), want)
		}

		if c.seen(path) != 1 {
			t.Errorf("upstream did not see %s", path)
		}
	}
}

func TestConanProxyV1_UpstreamFailureIsAGenericBadGateway(t *testing.T) {
	f, c := conanV1Fixture(t)
	c.down.Store(true)

	w := f.do(http.MethodGet, conanV1Recipe+"/download_urls")
	if w.Code != http.StatusBadGateway || strings.Contains(w.Body.String(), "down") {
		t.Errorf("= %d %q, want a generic 502", w.Code, w.Body.String())
	}
}
