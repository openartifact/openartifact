// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package group

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/publicurl"
)

func onHost(host string) func(*http.Request) { return func(r *http.Request) { r.Host = host } }

func withINM(mutate func(*http.Request), etag string) func(*http.Request) {
	return func(r *http.Request) {
		mutate(r)
		r.Header.Set("If-None-Match", etag)
	}
}

// originBackend is a backend whose members build their documents on the origin
// the request they are asked with resolves to, as a hosted or proxy member does:
// the group hands its members a clone of the client's request, context included.
func originBackend(explicit string, doc func(origin string, m *models.Repository, path string) (string, bool)) Backend {
	return Backend{
		Authorize: func(*http.Request, *models.Repository, string) bool { return true },
		ServeMember: func(w http.ResponseWriter, r *http.Request, m *models.Repository, p string) {
			body, ok := doc(publicurl.FromContext(r.Context(), explicit), m, p)
			if !ok {
				http.NotFound(w, r)
				return
			}

			_, _ = io.WriteString(w, body)
		},
	}
}

// config.json's dl and api are absolute (cargo concatenates the dl template and
// never resolves it against the index URL): they carry the derived origin, and
// the content ETag, a hash of that body, differs per origin.
func TestCargoGroup_ConfigOriginIsDerivedAndInTheETag(t *testing.T) {
	for _, tc := range originCases() {
		t.Run(tc.name, func(t *testing.T) {
			f := cargoGroup(t)

			w := serveVia(f.h, f, f.backend(), tc.explicit, "config.json", tc.mutate)
			require.Equal(t, http.StatusOK, w.Code)

			var cfg map[string]any

			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &cfg))
			assert.Equal(t, tc.want+"/repos/all/api/v1/crates", cfg["dl"])
			assert.Equal(t, tc.want+"/repos/all", cfg["api"])
		})
	}

	f := cargoGroup(t)
	a := serveVia(f.h, f, f.backend(), "", "config.json", onHost("a.test"))
	b := serveVia(f.h, f, f.backend(), "", "config.json", onHost("b.test"))

	require.NotEmpty(t, a.Header().Get("ETag"))
	assert.NotEqual(t, a.Header().Get("ETag"), b.Header().Get("ETag"))
	assert.Contains(t, a.Header().Get("Vary"), "X-Forwarded-Host")
	assert.Equal(t, http.StatusNotModified,
		serveVia(f.h, f, f.backend(), "", "config.json", withINM(onHost("a.test"), a.Header().Get("ETag"))).Code)
	assert.Equal(t, http.StatusOK,
		serveVia(f.h, f, f.backend(), "", "config.json", withINM(onHost("b.test"), a.Header().Get("ETag"))).Code)
}

func TestNuGetGroup_ServiceIndexOriginIsDerivedAndInTheETag(t *testing.T) {
	for _, tc := range originCases() {
		t.Run(tc.name, func(t *testing.T) {
			f := nugetGroup(t)

			w := serveVia(f.h, f, f.backend(), tc.explicit, "v3/index.json", tc.mutate)
			require.Equal(t, http.StatusOK, w.Code)

			var doc struct {
				Resources []struct {
					ID   string `json:"@id"`
					Type string `json:"@type"`
				} `json:"resources"`
			}

			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &doc))

			got := map[string]string{}
			for _, r := range doc.Resources {
				got[r.Type] = r.ID
			}

			assert.Equal(t, tc.want+"/repos/all/v3/registration/", got["RegistrationsBaseUrl/3.6.0"])
			assert.Equal(t, tc.want+"/repos/all/v3/package/", got["PackageBaseAddress/3.0.0"])
		})
	}

	f := nugetGroup(t)
	a := serveVia(f.h, f, f.backend(), "", "v3/index.json", onHost("a.test"))
	b := serveVia(f.h, f, f.backend(), "", "v3/index.json", onHost("b.test"))

	require.NotEmpty(t, a.Header().Get("ETag"))
	assert.NotEqual(t, a.Header().Get("ETag"), b.Header().Get("ETag"))
	assert.Equal(t, http.StatusOK,
		serveVia(f.h, f, f.backend(), "", "v3/index.json", withINM(onHost("b.test"), a.Header().Get("ETag"))).Code)
}

// The members answer with URLs on the origin of the request they were asked
// with; the group replaces their prefixes with its own at that same origin, so
// no member URL survives whatever the origin is, and the validator follows it.
func TestNuGetGroup_RegistrationOriginIsDerivedAndInTheETag(t *testing.T) {
	doc := func(origin string, m *models.Repository, path string) (string, bool) {
		switch path {
		case "v3/registration/lib/index.json":
			return nugetInlineIndexAt(origin, m.Name, "lib", "1.0.0"), true
		case "v3/registration/lib/1.0.0.json":
			return nugetItemAt(origin, m.Name, "lib", "1.0.0"), true
		}

		return "", false
	}

	for _, tc := range originCases() {
		t.Run(tc.name, func(t *testing.T) {
			f := nugetGroup(t)
			be := originBackend(tc.explicit, doc)

			w := serveVia(f.h, f, be, tc.explicit, "v3/registration/lib/index.json", tc.mutate)
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())

			group := tc.want + "/repos/all/v3/"
			assert.Contains(t, w.Body.String(), group+"registration/lib/1.0.0.json")
			assert.Contains(t, w.Body.String(), group+"package/lib/1.0.0/lib.1.0.0.nupkg")
			assert.NotContains(t, w.Body.String(), "/repos/hosted/")
			assert.NotContains(t, w.Body.String(), "/repos/mirror/")

			leaf := serveVia(f.h, f, be, tc.explicit, "v3/registration/lib/1.0.0.json", tc.mutate)
			require.Equal(t, http.StatusOK, leaf.Code, leaf.Body.String())
			assert.Contains(t, leaf.Body.String(), group+"package/lib/1.0.0/lib.1.0.0.nupkg")
			assert.NotContains(t, leaf.Body.String(), "/repos/hosted/")
		})
	}

	f := nugetGroup(t)
	be := originBackend("", doc)

	for _, path := range []string{"v3/registration/lib/index.json", "v3/registration/lib/1.0.0.json"} {
		a := serveVia(f.h, f, be, "", path, onHost("a.test"))
		b := serveVia(f.h, f, be, "", path, onHost("b.test"))

		require.NotEmpty(t, a.Header().Get("ETag"), path)
		assert.NotEqual(t, a.Header().Get("ETag"), b.Header().Get("ETag"), path)
		assert.NotContains(t, b.Body.String(), "a.test", path)
		assert.Equal(
			t,
			http.StatusNotModified,
			serveVia(f.h, f, be, "", path, withINM(onHost("a.test"), a.Header().Get("ETag"))).Code,
			path,
		)
		assert.Equal(t, http.StatusOK, serveVia(f.h, f, be, "", path, withINM(onHost("b.test"), a.Header().Get("ETag"))).Code, path)
	}
}

// The Conan 1 URL maps of a member name its files; the group points them at
// itself on the origin the client reached, and the validator follows it.
func TestConanGroup_V1URLsOriginIsDerivedAndInTheETag(t *testing.T) {
	doc := func(origin string, m *models.Repository, path string) (string, bool) {
		if path != conanV1Digest {
			return "", false
		}

		return `{"conanfile.py":"` + origin + "/repos/" + m.Name + `/v1/files/zlib/1.0/_/_/0/export/conanfile.py"}`, true
	}

	for _, tc := range originCases() {
		t.Run(tc.name, func(t *testing.T) {
			f := conanGroup(t, true)

			w := serveVia(f.h, f, originBackend(tc.explicit, doc), tc.explicit, conanV1Digest, tc.mutate)
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())

			var urls map[string]string

			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &urls))
			assert.Equal(t, tc.want+"/repos/all/v1/files/zlib/1.0/_/_/0/export/conanfile.py", urls["conanfile.py"])
		})
	}

	f := conanGroup(t, true)
	be := originBackend("", doc)
	a := serveVia(f.h, f, be, "", conanV1Digest, onHost("a.test"))
	b := serveVia(f.h, f, be, "", conanV1Digest, onHost("b.test"))

	require.NotEmpty(t, a.Header().Get("ETag"))
	assert.NotEqual(t, a.Header().Get("ETag"), b.Header().Get("ETag"))
	assert.NotContains(t, b.Body.String(), "a.test")
	assert.Equal(t, http.StatusOK, serveVia(f.h, f, be, "", conanV1Digest, withINM(onHost("b.test"), a.Header().Get("ETag"))).Code)
}
