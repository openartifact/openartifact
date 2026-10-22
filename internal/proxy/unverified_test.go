// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// A file whose upstream publishes no digest was not checked against anything,
// so it must not be pinned as immutable: it keeps the repository TTL and is
// fetched and checked again once it expires.

func assertRepoTTL(t *testing.T, f *fixture, key string) {
	t.Helper()

	e := f.db.entry("repo-1", key)
	if e == nil {
		t.Fatalf("%s: no cache entry", key)
	}

	if ttl := time.Until(e.ExpiresAt); ttl > time.Duration(f.repo.CacheTTLSeconds)*time.Second {
		t.Errorf("%s: unverified file expires in %v, want within the repository TTL", key, ttl)
	}
}

func assertPinned(t *testing.T, f *fixture, key string) {
	t.Helper()

	e := f.db.entry("repo-1", key)
	if e == nil {
		t.Fatalf("%s: no cache entry", key)
	}

	if ttl := time.Until(e.ExpiresAt); ttl < 50*365*24*time.Hour {
		t.Errorf("%s: verified file expires in %v, want immutable", key, ttl)
	}
}

func TestUnverified_PyPIFileWithoutHashUsesRepoTTL(t *testing.T) {
	up := &pypiUpstream{page: `<html><body><a href="%FILES%/files/` + wheelName + `">` + wheelName + `</a></body></html>`}
	f := newPyPIFixture(t, up)

	if w := f.do(http.MethodGet, "files/requests/"+wheelName); w.Code != http.StatusOK || w.Body.String() != wheelBody {
		t.Fatalf("file: %d %q", w.Code, w.Body.String())
	}

	assertRepoTTL(t, f, "files/requests/"+wheelName)
}

func TestUnverified_NPMTarballWithoutDigestUsesRepoTTL(t *testing.T) {
	f, reg := npmFixture(t)
	reg.noDigest = true

	if w := f.do(http.MethodGet, "lib/-/lib-1.0.0.tgz"); w.Code != http.StatusOK {
		t.Fatalf("tarball: %d", w.Code)
	}

	assertRepoTTL(t, f, "lib/-/lib-1.0.0.tgz")
}

func TestUnverified_NPMTarballWithDigestIsPinned(t *testing.T) {
	f, _ := npmFixture(t)

	if w := f.do(http.MethodGet, "lib/-/lib-1.0.0.tgz"); w.Code != http.StatusOK {
		t.Fatalf("tarball: %d", w.Code)
	}

	assertPinned(t, f, "lib/-/lib-1.0.0.tgz")
}

func TestUnverified_HelmChartWithoutDigestUsesRepoTTL(t *testing.T) {
	f, _ := helmFixture(t)

	for _, file := range []string{"nodigest-1.0.0.tgz", "app-1.0.0.tgz"} {
		if w := f.do(http.MethodGet, "charts/"+file); w.Code != http.StatusOK {
			t.Fatalf("%s: %d", file, w.Code)
		}
	}

	assertRepoTTL(t, f, "charts/nodigest-1.0.0.tgz")
	assertPinned(t, f, "charts/app-1.0.0.tgz")
}

func TestUnverified_NuGetPackageWithoutHashUsesRepoTTL(t *testing.T) {
	const key = "v3/package/lib.a/1.0.0/lib.a.1.0.0.nupkg"

	for name, setup := range map[string]func(*nugetFeed){
		"no hash or size":      func(g *nugetFeed) { g.noHash = true },
		"catalog entry gone":   func(g *nugetFeed) { g.noCatalog = true },
		"no registration leaf": func(g *nugetFeed) { g.noLeaf = true },
	} {
		t.Run(name, func(t *testing.T) {
			f, feed := nugetFixture(t)
			setup(feed)

			if w := f.do(http.MethodGet, key); w.Code != http.StatusOK {
				t.Fatalf("nupkg: %d", w.Code)
			}

			assertRepoTTL(t, f, key)
		})
	}

	t.Run("published hash", func(t *testing.T) {
		f, _ := nugetFixture(t)

		if w := f.do(http.MethodGet, key); w.Code != http.StatusOK {
			t.Fatalf("nupkg: %d", w.Code)
		}

		assertPinned(t, f, key)
	})
}

func TestUnverified_GoModuleUnknownToSumdbUsesRepoTTL(t *testing.T) {
	e := newGoEnv(t)
	delete(e.known, goMod1+"@"+goVersion1) // the checksum database has no record

	base := goEscMod1 + "/@v/" + goVersion1

	for _, ext := range []string{".zip", ".mod"} {
		if w := e.get(base + ext); w.Code != http.StatusOK {
			t.Fatalf("%s: %d", ext, w.Code)
		}

		assertRepoTTL(t, e.fixture, base+ext)
	}

	// Once the database has the record, an expired entry is fetched again and
	// pinned.
	e.known[goMod1+"@"+goVersion1] = goSumLines(t, goMod1, goVersion1, e.genuine.mod, e.genuine.zip)

	for _, ext := range []string{".zip", ".mod"} {
		e.db.expire("repo-1", base+ext)

		if w := e.get(base + ext); w.Code != http.StatusOK {
			t.Fatalf("%s after record: %d", ext, w.Code)
		}

		assertPinned(t, e.fixture, base+ext)
	}
}

func TestUnverified_GoModuleKnownToSumdbIsPinned(t *testing.T) {
	e := newGoEnv(t)
	base := goEscMod1 + "/@v/" + goVersion1

	for _, ext := range []string{".zip", ".mod"} {
		if w := e.get(base + ext); w.Code != http.StatusOK {
			t.Fatalf("%s: %d", ext, w.Code)
		}

		assertPinned(t, e.fixture, base+ext)
	}
}

// With the lookup switched off the operator accepts structure checks alone, so
// nothing is held back from the immutable cache.
func TestUnverified_GoSumdbOffStaysPinned(t *testing.T) {
	e := newGoEnv(t)
	e.svc.sumdb = nil

	if w := e.get(goEscMod1 + "/@v/" + goVersion1 + ".mod"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "module") {
		t.Fatalf("mod: %d", w.Code)
	}

	assertPinned(t, e.fixture, goEscMod1+"/@v/"+goVersion1+".mod")
}
