// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func onHost(host string) func(*http.Request) { return func(r *http.Request) { r.Host = host } }

func withINM(mutate func(*http.Request), etag string) func(*http.Request) {
	return func(r *http.Request) {
		mutate(r)
		r.Header.Set("If-None-Match", etag)
	}
}

// config.json's dl and api are absolute (cargo concatenates the dl template and
// never resolves it against the index URL): they carry the derived origin, and
// the content ETag, a hash of that body, differs per origin.
func TestCargoProxy_ConfigOriginIsDerivedAndInTheETag(t *testing.T) {
	for _, tc := range originCases() {
		t.Run(tc.name, func(t *testing.T) {
			f, _ := cargoFixture(t)

			w := serveVia(f, tc.explicit, "config.json", tc.mutate)
			require.Equal(t, http.StatusOK, w.Code)

			var cfg map[string]any

			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &cfg))
			assert.Equal(t, tc.want+"/repos/crates/api/v1/crates", cfg["dl"])
			assert.Equal(t, tc.want+"/repos/crates", cfg["api"])

			if tc.explicit == "" {
				assert.Contains(t, w.Header().Get("Vary"), "X-Forwarded-Host")
			}
		})
	}

	f, _ := cargoFixture(t)
	a := serveVia(f, "", "config.json", onHost("a.test"))
	b := serveVia(f, "", "config.json", onHost("b.test"))

	require.NotEmpty(t, a.Header().Get("ETag"))
	assert.NotEqual(t, a.Header().Get("ETag"), b.Header().Get("ETag"))
	assert.Equal(t, http.StatusNotModified, serveVia(f, "", "config.json", withINM(onHost("a.test"), a.Header().Get("ETag"))).Code)
	assert.Equal(t, http.StatusOK, serveVia(f, "", "config.json", withINM(onHost("b.test"), a.Header().Get("ETag"))).Code)
	assert.Zero(t, f.hits.Load(), "config.json is generated, never fetched from the upstream")
}

// The service index @ids are full URLs built per request on the derived origin;
// the cached upstream copy keeps the upstream's own URLs.
func TestNuGetProxy_ServiceIndexOriginIsDerivedAndInTheETag(t *testing.T) {
	for _, tc := range originCases() {
		t.Run(tc.name, func(t *testing.T) {
			f, _ := nugetFixture(t)

			w := serveVia(f, tc.explicit, "v3/index.json", tc.mutate)
			doc := jsonOf(t, w)

			got := map[string]string{}

			for _, r := range doc["resources"].([]any) {
				res := r.(map[string]any)
				got[res["@type"].(string)] = res["@id"].(string)
			}

			root := tc.want + "/repos/nugetorg/v3"
			assert.Equal(t, root+"/registration/", got["RegistrationsBaseUrl/3.6.0"])
			assert.Equal(t, root+"/package/", got["PackageBaseAddress/3.0.0"])
			assert.Equal(t, root+"/query", got["SearchQueryService"])
			assert.Equal(t, root+"/autocomplete", got["SearchAutocompleteService"])
		})
	}

	f, feed := nugetFixture(t)
	a := serveVia(f, "", "v3/index.json", onHost("a.test"))
	b := serveVia(f, "", "v3/index.json", onHost("b.test"))

	require.NotEmpty(t, a.Header().Get("ETag"))
	assert.NotEqual(t, a.Header().Get("ETag"), b.Header().Get("ETag"))
	assert.Equal(t, http.StatusNotModified, serveVia(f, "", "v3/index.json", withINM(onHost("a.test"), a.Header().Get("ETag"))).Code)
	assert.Equal(t, http.StatusOK, serveVia(f, "", "v3/index.json", withINM(onHost("b.test"), a.Header().Get("ETag"))).Code)
	assert.NotContains(t, b.Body.String(), "a.test")
	assert.Equal(t, 1, feed.seen("/v3/index.json"), "one upstream fetch serves every origin")
}

// Registration documents are rewritten at serve time from the one cached
// upstream copy: each origin sees only its own URLs, with its own validator.
func TestNuGetProxy_RegistrationOriginIsDerivedAndInTheETag(t *testing.T) {
	for _, tc := range originCases() {
		t.Run(tc.name, func(t *testing.T) {
			f, _ := nugetFixture(t)

			repo := tc.want + "/repos/nugetorg"
			content := repo + "/v3/package/lib.a/1.0.0/lib.a.1.0.0.nupkg"

			idx := serveVia(f, tc.explicit, "v3/registration/lib.a/index.json", tc.mutate)
			doc := jsonOf(t, idx)
			assert.Equal(t, repo+"/v3/registration/lib.a/index.json", doc["@id"])
			assert.Contains(t, idx.Body.String(), content)

			leaf := jsonOf(t, serveVia(f, tc.explicit, "v3/registration/lib.a/1.0.0.json", tc.mutate))
			assert.Equal(t, content, leaf["packageContent"])
			assert.Equal(t, repo+"/v3/registration/lib.a/index.json", leaf["registration"])
		})
	}

	f, feed := nugetFixture(t)
	path := "v3/registration/lib.a/index.json"
	a := serveVia(f, "", path, onHost("a.test"))
	b := serveVia(f, "", path, onHost("b.test"))

	require.NotEmpty(t, a.Header().Get("ETag"))
	assert.NotEqual(t, a.Header().Get("ETag"), b.Header().Get("ETag"))
	assert.Equal(t, http.StatusNotModified, serveVia(f, "", path, withINM(onHost("a.test"), a.Header().Get("ETag"))).Code)
	assert.Equal(t, http.StatusOK, serveVia(f, "", path, withINM(onHost("b.test"), a.Header().Get("ETag"))).Code)
	assert.NotContains(t, b.Body.String(), "a.test")
	assert.Equal(t, 1, feed.seen("/reg/lib.a/index.json"), "one upstream fetch serves every origin")

	// The explicit BASE_URL fixes the validator whatever the request says.
	e1 := serveVia(f, "https://oa.example", path, onHost("a.test"))
	e2 := serveVia(f, "https://oa.example", path, onHost("b.test"))
	assert.Equal(t, e1.Header().Get("ETag"), e2.Header().Get("ETag"))
	assert.Contains(t, e1.Body.String(), "https://oa.example/repos/nugetorg/v3/package/lib.a/1.0.0/lib.a.1.0.0.nupkg")
}

func TestNuGetProxy_SearchOriginIsDerived(t *testing.T) {
	for _, tc := range originCases() {
		t.Run(tc.name, func(t *testing.T) {
			f, _ := nugetFixture(t)

			hit := jsonOf(t, serveVia(f, tc.explicit, "v3/query", tc.mutate))["data"].([]any)[0].(map[string]any)
			assert.Equal(t, tc.want+"/repos/nugetorg/v3/registration/lib.a/index.json", hit["registration"])
		})
	}
}

// download_urls and digest name files at the proxy under the derived origin;
// the answer is private and unvalidated, and the cache holds none of our URLs.
func TestConanProxyV1_URLsOriginIsDerived(t *testing.T) {
	for _, tc := range originCases() {
		t.Run(tc.name, func(t *testing.T) {
			f, _ := conanV1Fixture(t)

			root := tc.want + "/repos/conan-proxy/" + conanV1Files + "/export/"

			w := serveVia(f, tc.explicit, conanV1Recipe+"/download_urls", tc.mutate)
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())

			var urls map[string]string

			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &urls))
			assert.Equal(t, root+"conanfile.py", urls["conanfile.py"])
			assert.Equal(t, root+"conanmanifest.txt", urls["conanmanifest.txt"])
			assert.Empty(t, w.Header().Get("ETag"))

			if tc.explicit == "" {
				assert.Contains(t, w.Header().Get("Vary"), "X-Forwarded-Host")
			}
		})
	}

	f, _ := conanV1Fixture(t)
	a := serveVia(f, "", conanV1Recipe+"/download_urls", onHost("a.test"))
	b := serveVia(f, "", conanV1Recipe+"/download_urls", onHost("b.test"))

	assert.Contains(t, a.Body.String(), "http://a.test/repos/conan-proxy/")
	assert.Contains(t, b.Body.String(), "http://b.test/repos/conan-proxy/")
	assert.NotContains(t, b.Body.String(), "a.test")
}
