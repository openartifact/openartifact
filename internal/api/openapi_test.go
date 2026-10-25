// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

func serveOpenAPI(t *testing.T, version string, docs bool, path string, hdr ...string) *httptest.ResponseRecorder {
	t.Helper()

	r := chi.NewRouter()
	NewOpenAPIHandler(version, docs).RegisterRoutes(r)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	return rec
}

func TestOpenAPISpecMetadataPatched(t *testing.T) {
	t.Parallel()

	var doc struct {
		Info struct {
			Title       string `json:"title"`
			Version     string `json:"version"`
			Description string `json:"description"`
			License     struct {
				Name string `json:"name"`
				URL  string `json:"url"`
			} `json:"license"`
		} `json:"info"`
		Components struct {
			SecuritySchemes map[string]struct {
				Name string `json:"name"`
			} `json:"securitySchemes"` //nolint:tagliatelle // OpenAPI field name
		} `json:"components"`
		Servers []struct {
			URL string `json:"url"`
		} `json:"servers"`
	}

	rec := serveOpenAPI(t, "1.2.3", true, "/api/v1/openapi.json")
	require.Equal(t, http.StatusOK, rec.Code)
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &doc))

	assert.Equal(t, "1.2.3", doc.Info.Version)
	assert.Equal(t, "AGPL-3.0-or-later", doc.Info.License.Name)
	assert.Equal(t, "https://www.gnu.org/licenses/agpl-3.0.html", doc.Info.License.URL)
	assert.NotContains(t, doc.Info.Description, "Enterprise")
	assert.Equal(t, "oa_session", doc.Components.SecuritySchemes["cookieAuth"].Name)
	require.Len(t, doc.Servers, 1)
	assert.Equal(t, "/", doc.Servers[0].URL)

	yamlRec := serveOpenAPI(t, "1.2.3", true, "/api/v1/openapi.yaml")
	require.Equal(t, http.StatusOK, yamlRec.Code)

	var y map[string]any
	require.NoError(t, yaml.Unmarshal(yamlRec.Body.Bytes(), &y))
	assert.Equal(t, "1.2.3", y["info"].(map[string]any)["version"])
}

func TestOpenAPISpecServedWhenDocsDisabled(t *testing.T) {
	t.Parallel()

	for _, p := range []string{"/api/v1/openapi.json", "/api/v1/openapi.yaml"} {
		assert.Equal(t, http.StatusOK, serveOpenAPI(t, "dev", false, p).Code, p)
	}
}

func TestAPIReferencePage(t *testing.T) {
	t.Parallel()

	rec := serveOpenAPI(t, "dev", true, "/api/reference")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Header().Get("Content-Type"), "text/html")

	csp := rec.Header().Get("Content-Security-Policy")
	for _, d := range []string{"default-src 'self'", "script-src 'self'", "connect-src 'self'", "frame-ancestors 'none'"} {
		assert.Contains(t, csp, d)
	}

	assert.NotContains(t, csp, "unsafe-eval")
	assert.NotContains(t, csp, "http")

	body := rec.Body.String()
	assert.Contains(t, body, `src="/api/reference/assets/scalar-1.76.0.js"`)
	assert.Contains(t, body, `src="/api/reference/assets/init.js"`)
	// no inline script and nothing from another origin
	assert.NotContains(t, body, "<script>")
	assert.NotContains(t, body, "http://")
	assert.NotContains(t, body, "https://")
}

func TestAPIReferenceAssets(t *testing.T) {
	t.Parallel()

	init := serveOpenAPI(t, "dev", true, "/api/reference/assets/init.js")
	require.Equal(t, http.StatusOK, init.Code)
	assert.Contains(t, init.Body.String(), "X-CSRF-Token")
	assert.Contains(t, init.Body.String(), "oa_csrf")

	gz := serveOpenAPI(t, "dev", true, "/api/reference/assets/scalar-1.76.0.js", "Accept-Encoding", "gzip")
	require.Equal(t, http.StatusOK, gz.Code)
	assert.Equal(t, "gzip", gz.Header().Get("Content-Encoding"))
	assert.Contains(t, gz.Header().Get("Cache-Control"), "immutable")

	zr, err := gzip.NewReader(gz.Body)
	require.NoError(t, err)

	plainFromGzip, err := io.ReadAll(zr)
	require.NoError(t, err)
	assert.Contains(t, string(plainFromGzip), "createApiReference")

	plain := serveOpenAPI(t, "dev", true, "/api/reference/assets/scalar-1.76.0.js")
	require.Equal(t, http.StatusOK, plain.Code)
	assert.Empty(t, plain.Header().Get("Content-Encoding"))
	assert.Equal(t, string(plainFromGzip), plain.Body.String())
}

func TestSwaggerRedirects(t *testing.T) {
	t.Parallel()

	for _, p := range []string{"/swagger", "/swagger/"} {
		rec := serveOpenAPI(t, "dev", true, p)
		assert.Equal(t, http.StatusMovedPermanently, rec.Code, p)
		assert.Equal(t, "/api/reference", rec.Header().Get("Location"), p)
	}
}

func TestAPIReferenceDisabled(t *testing.T) {
	t.Parallel()

	for _, p := range []string{
		"/api/reference", "/api/reference/", "/swagger", "/swagger/",
		"/api/reference/assets/init.js", "/api/reference/assets/scalar-1.76.0.js",
	} {
		rec := serveOpenAPI(t, "dev", false, p)
		assert.Equal(t, http.StatusNotFound, rec.Code, p)
		assert.NotContains(t, rec.Body.String(), "<html", p)
	}
}

func TestSystemInfoAdvertisesAPIReference(t *testing.T) {
	t.Parallel()

	for _, on := range []bool{true, false} {
		h := NewSystemInfoHandler("1.0.0", "", "", nil, nil, "").WithAPIReference(on)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/system/info", nil))

		var body map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		assert.Equal(t, on, body["api_reference_enabled"])
	}
}
