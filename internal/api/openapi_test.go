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
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
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
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}

	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}

	if doc.Info.Version != "1.2.3" {
		t.Errorf("info.version = %q", doc.Info.Version)
	}

	if doc.Info.License.Name != "AGPL-3.0-or-later" || doc.Info.License.URL != "https://www.gnu.org/licenses/agpl-3.0.html" {
		t.Errorf("info.license = %+v", doc.Info.License)
	}

	if strings.Contains(doc.Info.Description, "Enterprise") {
		t.Errorf("stale description: %q", doc.Info.Description)
	}

	if got := doc.Components.SecuritySchemes["cookieAuth"].Name; got != "oa_session" {
		t.Errorf("cookieAuth name = %q, want oa_session", got)
	}

	if len(doc.Servers) != 1 || doc.Servers[0].URL != "/" {
		t.Errorf("servers = %+v, want one relative server", doc.Servers)
	}

	yamlRec := serveOpenAPI(t, "1.2.3", true, "/api/v1/openapi.yaml")
	if yamlRec.Code != http.StatusOK {
		t.Fatalf("yaml status = %d", yamlRec.Code)
	}

	var y struct {
		Info struct {
			Version string `yaml:"version"`
		} `yaml:"info"`
	}
	if err := yaml.Unmarshal(yamlRec.Body.Bytes(), &y); err != nil {
		t.Fatal(err)
	}

	if y.Info.Version != "1.2.3" {
		t.Errorf("yaml info.version = %q", y.Info.Version)
	}
}

func TestOpenAPISpecServedWhenDocsDisabled(t *testing.T) {
	t.Parallel()

	for _, p := range []string{"/api/v1/openapi.json", "/api/v1/openapi.yaml"} {
		if code := serveOpenAPI(t, "dev", false, p).Code; code != http.StatusOK {
			t.Errorf("%s = %d, want 200", p, code)
		}
	}
}

func TestAPIReferencePage(t *testing.T) {
	t.Parallel()

	rec := serveOpenAPI(t, "dev", true, "/api/reference")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}

	if !strings.Contains(rec.Header().Get("Content-Type"), "text/html") {
		t.Errorf("content type = %q", rec.Header().Get("Content-Type"))
	}

	csp := rec.Header().Get("Content-Security-Policy")
	for _, d := range []string{"default-src 'self'", "script-src 'self'", "connect-src 'self'", "frame-ancestors 'none'"} {
		if !strings.Contains(csp, d) {
			t.Errorf("CSP %q lacks %q", csp, d)
		}
	}

	for _, bad := range []string{"unsafe-eval", "http"} {
		if strings.Contains(csp, bad) {
			t.Errorf("CSP %q must not contain %q", csp, bad)
		}
	}

	body := rec.Body.String()
	for _, want := range []string{`src="/api/reference/assets/scalar-1.76.0.js"`, `src="/api/reference/assets/init.js"`} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %s", want)
		}
	}

	// no inline script and nothing from another origin
	for _, bad := range []string{"<script>", "http://", "https://"} {
		if strings.Contains(body, bad) {
			t.Errorf("page must not contain %q", bad)
		}
	}
}

func TestAPIReferenceAssets(t *testing.T) {
	t.Parallel()

	initJS := serveOpenAPI(t, "dev", true, "/api/reference/assets/init.js")
	if initJS.Code != http.StatusOK {
		t.Fatalf("init.js status = %d", initJS.Code)
	}

	for _, want := range []string{"X-CSRF-Token", "oa_csrf"} {
		if !strings.Contains(initJS.Body.String(), want) {
			t.Errorf("init.js lacks %s", want)
		}
	}

	gz := serveOpenAPI(t, "dev", true, "/api/reference/assets/scalar-1.76.0.js", "Accept-Encoding", "gzip")
	if gz.Code != http.StatusOK || gz.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("gzip bundle: status %d, encoding %q", gz.Code, gz.Header().Get("Content-Encoding"))
	}

	if !strings.Contains(gz.Header().Get("Cache-Control"), "immutable") {
		t.Errorf("bundle Cache-Control = %q", gz.Header().Get("Cache-Control"))
	}

	zr, err := gzip.NewReader(gz.Body)
	if err != nil {
		t.Fatal(err)
	}

	fromGzip, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(fromGzip), "createApiReference") {
		t.Error("bundle does not expose createApiReference")
	}

	plain := serveOpenAPI(t, "dev", true, "/api/reference/assets/scalar-1.76.0.js")
	if plain.Code != http.StatusOK || plain.Header().Get("Content-Encoding") != "" {
		t.Fatalf("plain bundle: status %d, encoding %q", plain.Code, plain.Header().Get("Content-Encoding"))
	}

	if plain.Body.String() != string(fromGzip) {
		t.Error("identity response differs from the decompressed gzip response")
	}
}

func TestSwaggerRedirects(t *testing.T) {
	t.Parallel()

	for _, p := range []string{"/swagger", "/swagger/"} {
		rec := serveOpenAPI(t, "dev", true, p)
		if rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != "/api/reference" {
			t.Errorf("%s = %d -> %q", p, rec.Code, rec.Header().Get("Location"))
		}
	}
}

func TestAPIReferenceDisabled(t *testing.T) {
	t.Parallel()

	for _, p := range []string{
		"/api/reference", "/api/reference/", "/swagger", "/swagger/",
		"/api/reference/assets/init.js", "/api/reference/assets/scalar-1.76.0.js",
	} {
		rec := serveOpenAPI(t, "dev", false, p)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s = %d, want 404", p, rec.Code)
		}

		if strings.Contains(rec.Body.String(), "<html") {
			t.Errorf("%s served HTML while disabled", p)
		}
	}
}

func TestSystemInfoAdvertisesAPIReference(t *testing.T) {
	t.Parallel()

	for _, on := range []bool{true, false} {
		h := NewSystemInfoHandler("1.0.0", "", "", nil, nil, "").WithAPIReference(on)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/system/info", nil))

		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}

		if body["api_reference_enabled"] != on {
			t.Errorf("api_reference_enabled = %v, want %v", body["api_reference_enabled"], on)
		}
	}
}
