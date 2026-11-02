// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"context"
	"net/http"
	"slices"
	"testing"

	"github.com/openartifact/openartifact/internal/curation"
	"github.com/openartifact/openartifact/internal/pypisimple"
)

// versionSummary renders the listing as "version:status[:cached]", in order.
func versionSummary(list ProxyVersions) []string {
	out := make([]string, 0, len(list.Versions))

	for _, v := range list.Versions {
		s := v.Version + ":available"
		if v.Held {
			s = v.Version + ":held(" + v.Hold.Source + ")"
		}

		if v.Cached {
			s += ":cached"
		}

		out = append(out, s)
	}

	return out
}

func TestNPMProxyVersions_MarksHeldAndCachedFromTheCacheOnly(t *testing.T) {
	f, reg := minAgeFixture(t, npmMinAgeGate)
	f.useCuration(curation.ModeDenylist, blockRule("lib", "1.0.0"))

	ctx := context.Background()

	if list, err := f.svc.ListProxyVersions(ctx, f.repo, "lib"); err != nil || list.Supported || len(list.Versions) != 0 {
		t.Fatalf("nothing cached: %+v, %v; want an unsupported empty list", list, err)
	}

	if w := f.do(http.MethodGet, "lib"); w.Code != http.StatusOK {
		t.Fatalf("packument = %d", w.Code)
	}

	// Only 1.1.0 was downloaded; 2.0.0 and 0.9.0 were not.
	if w := f.do(http.MethodGet, "lib/-/lib-1.1.0.tgz"); w.Code != http.StatusOK {
		t.Fatalf("tarball = %d", w.Code)
	}

	abbr, gets, hits := reg.abbrServed.Load(), reg.tarballGets.Load(), f.hits.Load()

	list, err := f.svc.ListProxyVersions(ctx, f.repo, "lib")
	if err != nil || !list.Supported || !list.CacheKnown {
		t.Fatalf("list = %+v, err = %v; want supported with a known cache", list, err)
	}

	// Newest first: 2.0.0 and 2.1.0-rc.1 are too young, 1.0.0 is curated, 0.9.0 is undated.
	want := []string{
		"2.0.0:held(min_age)", "2.1.0-rc.1:held(min_age)", "1.1.0:available:cached", "1.0.0:held(curation)", "0.9.0:available",
	}
	if got := versionSummary(list); !slices.Equal(got, want) {
		t.Errorf("versions = %v, want %v", got, want)
	}

	if reg.abbrServed.Load() != abbr || reg.tarballGets.Load() != gets || f.hits.Load() != hits {
		t.Error("listing the proxy versions reached the upstream")
	}
}

func TestPyPIProxyVersions_CachedByDownloadedFile(t *testing.T) {
	f := curationPyPIFixture(t, 24)
	f.useCuration(curation.ModeDenylist, blockRule("requests", "2.31.0"))

	if w := f.doAccept(http.MethodGet, "simple/requests/", pypisimple.ContentTypeJSON); w.Code != http.StatusOK {
		t.Fatalf("project page = %d", w.Code)
	}

	// The wheel of 2.32.0 is downloaded; 2.31.0 is only listed.
	if w := f.do(http.MethodGet, "files/requests/"+wheelName); w.Code != http.StatusOK {
		t.Fatalf("%s = %d", wheelName, w.Code)
	}

	hits := f.hits.Load()

	list, err := f.svc.ListProxyVersions(context.Background(), f.repo, "Requests")
	if err != nil || !list.Supported || !list.CacheKnown {
		t.Fatalf("list = %+v, err = %v", list, err)
	}

	want := []string{"2.33.0:held(min_age)", "2.32.0:available:cached", "2.31.0:held(curation)", "2.34.0:available"}
	if got := versionSummary(list); !slices.Equal(got, want) {
		t.Errorf("versions = %v, want %v", got, want)
	}

	requireNoNewHits(t, f, hits)
}

func TestProxyVersions_FormatWithoutVersionedCacheKeysKnowsNoCache(t *testing.T) {
	f, _ := composerAgedFixture(t)

	if w := f.do(http.MethodGet, "p2/acme/lib.json"); w.Code != http.StatusOK {
		t.Fatalf("p2 = %d", w.Code)
	}

	list, err := f.svc.ListProxyVersions(context.Background(), f.repo, "acme/lib")
	if err != nil || !list.Supported || len(list.Versions) == 0 {
		t.Fatalf("list = %+v, err = %v; want the cached versions", list, err)
	}

	if list.CacheKnown {
		t.Error("composer cache keys name no version, yet the cache is claimed known")
	}

	for _, v := range list.Versions {
		if v.Cached {
			t.Errorf("%s is claimed cached", v.Version)
		}
	}
}

func TestCachedKeyVersion(t *testing.T) {
	tests := []struct {
		name, format, pkg, key, want string
	}{
		{"npm tarball", "npm", "lib", "lib/-/lib-1.1.0.tgz", "1.1.0"},
		{"npm scoped", "npm", "@s/lib", "@s/lib/-/lib-2.0.0-rc.1.tgz", "2.0.0-rc.1"},
		{"npm packument is no file", "npm", "lib", "lib", ""},
		{"pypi normalizes the project", "pypi", "My_Lib", "files/my-lib/my_lib-1.0.0-py3-none-any.whl", "1.0.0"},
		{"pypi sidecar is no file", "pypi", "lib", "files/lib/lib-1.0.0-py3-none-any.whl.metadata", ""},
		{"pypi sdist", "pypi", "lib", "files/lib/lib-2.0rc1.tar.gz", "2.0rc1"},
		{"rubygems keeps the platform out of the version", "rubygems", "nokogiri", "gems/nokogiri-1.16.0-x86_64-linux.gem", "1.16.0"},
		{"rubygems prefix of another gem", "rubygems", "rails", "gems/rails-html-sanitizer-1.6.0.gem", ""},
		{
			"nuget id is case insensitive",
			"nuget",
			"Newtonsoft.Json",
			"v3/package/newtonsoft.json/13.0.1/newtonsoft.json.13.0.1.nupkg",
			"13.0.1",
		},
		{"go module", "go", "github.com/Foo/bar", "github.com/!foo/bar/@v/v1.2.3.zip", "v1.2.3"},
		{"go info is no file", "go", "github.com/foo/bar", "github.com/foo/bar/@v/v1.2.3.info", ""},
		{"cargo crate", "cargo", "serde", "api/v1/crates/serde/1.0.200/download", "1.0.200"},
		{"helm chart", "helm", "nginx", "charts/nginx-1.2.3.tgz", "1.2.3"},
		{"helm other chart", "helm", "nginx", "charts/nginx-ingress-1.2.3.tgz", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := cachedKeyVersion(tt.format, tt.pkg, tt.key)
			if tt.want == "" {
				if ok {
					t.Fatalf("cachedKeyVersion(%q) = %q, want no version", tt.key, got)
				}

				return
			}

			if !ok || got != tt.want {
				t.Fatalf("cachedKeyVersion(%q) = %q, %v; want %q", tt.key, got, ok, tt.want)
			}
		})
	}
}
