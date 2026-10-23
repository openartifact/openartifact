// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package middleware

import (
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestExtractRepoAndPath(t *testing.T) {
	tests := []struct {
		name         string
		path         string
		wantRepo     string
		wantArtifact string
	}{
		// Generic /repos/{name}/... format-handler endpoints.
		{"npm packument", "/repos/npm-repo/lodash", "npm-repo", "lodash"},
		{"npm publish unscoped", "/repos/npm-repo/lodash", "npm-repo", "lodash"},
		{"npm scoped publish", "/repos/npm-repo/@scope%2Fname", "npm-repo", "@scope%2Fname"},
		{"npm dist-tags", "/repos/npm-repo/-/package/lodash/dist-tags/latest", "npm-repo", "-/package/lodash/dist-tags/latest"},
		{"pypi upload", "/repos/pypi-repo/legacy/", "pypi-repo", "legacy/"},
		{"pypi simple index", "/repos/pypi-repo/simple/", "pypi-repo", "simple/"},
		{"pypi package index", "/repos/pypi-repo/simple/requests/", "pypi-repo", "simple/requests/"},
		{"helm chart upload", "/repos/helm-repo/charts", "helm-repo", "charts"},
		{"helm index", "/repos/helm-repo/index.yaml", "helm-repo", "index.yaml"},
		{"bare repo path", "/repos/some-repo", "some-repo", ""},

		// Isolated /repos/<marker>/{name}/... namespaces.
		{"raw namespace", "/repos/raw/my-raw-repo/some/nested/path.txt", "my-raw-repo", "some/nested/path.txt"},
		{"raw namespace bare", "/repos/raw/my-raw-repo", "my-raw-repo", ""},
		{"maven namespace", "/repos/maven/my-maven-repo/com/example/lib/1.0/lib-1.0.jar", "my-maven-repo", "com/example/lib/1.0/lib-1.0.jar"},
		{"sbt namespace", "/repos/sbt/my-sbt-repo/com/example/lib_2.13/1.0/lib.jar", "my-sbt-repo", "com/example/lib_2.13/1.0/lib.jar"},
		{"ivy namespace", "/repos/ivy/my-ivy-repo/org/module/ivys/ivy-1.0.xml", "my-ivy-repo", "org/module/ivys/ivy-1.0.xml"},

		// Docker/OCI /v2/... routes: name may itself contain "/".
		{"docker manifest simple", "/v2/myimage/manifests/latest", "myimage", "manifests/latest"},
		// The first name segment is the OpenArtifact repository; the rest is the image and leads the artifact path.
		{"docker manifest namespaced", "/v2/org/team/myimage/manifests/sha256:abcd", "org", "team/myimage/manifests/sha256:abcd"},
		{"docker blob namespaced", "/v2/reg/org/team/app/blobs/sha256:deadbeef", "reg", "org/team/app/blobs/sha256:deadbeef"},
		{"docker upload init namespaced", "/v2/reg/org/team/app/blobs/uploads/", "reg", "org/team/app/blobs/uploads/"},
		{"docker upload chunk namespaced", "/v2/reg/org/team/app/blobs/uploads/abc-123", "reg", "org/team/app/blobs/uploads/abc-123"},
		{"docker tags list namespaced", "/v2/reg/org/team/app/tags/list", "reg", "org/team/app/tags/list"},
		{"docker referrers namespaced", "/v2/reg/org/app/referrers/sha256:deadbeef", "reg", "org/app/referrers/sha256:deadbeef"},
		{"docker blob", "/v2/myimage/blobs/sha256:deadbeef", "myimage", "blobs/sha256:deadbeef"},
		{"docker blob upload init", "/v2/myimage/blobs/uploads/", "myimage", "blobs/uploads/"},
		{"docker blob upload chunk", "/v2/myimage/blobs/uploads/abc-123", "myimage", "blobs/uploads/abc-123"},
		{"docker tags list", "/v2/myimage/tags/list", "myimage", "tags/list"},
		{"docker referrers", "/v2/myimage/referrers/sha256:deadbeef", "myimage", "referrers/sha256:deadbeef"},
		{"docker manifest name contains literal marker segment", "/v2/myrepo/manifests/latest/manifests/sha256:abcd", "myrepo", "manifests/latest/manifests/sha256:abcd"},
		{"docker version check", "/v2/", "", ""},

		// Non-repo API paths.
		{"admin api path", "/api/v1/admin/users", "", "/api/v1/admin/users"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotRepo, gotArtifact := extractRepoAndPath(tc.path)
			if gotRepo != tc.wantRepo || gotArtifact != tc.wantArtifact {
				t.Errorf("extractRepoAndPath(%q) = (%q, %q), want (%q, %q)",
					tc.path, gotRepo, gotArtifact, tc.wantRepo, tc.wantArtifact)
			}
		})
	}
}

func TestIsSPARepoPathAndBypass(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		wantBypass bool
	}{
		// Legitimate Vue-router SPA pages: served as the SPA to browser navigations.
		{"repo detail", "/repos/myrepo", true},
		{"new repo wizard", "/repos/new", true},
		{"repo edit", "/repos/myrepo/edit", true},
		{"repo browse", "/repos/myrepo/browse", true},
		{"repo graph", "/repos/myrepo/graph", true},
		{"repo trash", "/repos/myrepo/trash", true},
		{"repo settings", "/repos/myrepo/settings", true},
		{"package detail", "/repos/myrepo/packages/npm/lodash", true},
		{"package detail scoped npm", "/repos/myrepo/packages/npm/@scope%2Fpkg", true},
		{"package detail docker", "/repos/myrepo/packages/docker/org%2Fapp", true},
		{"package detail raw path with extension", "/repos/myrepo/packages/raw/docs%2Fhello.txt", true},
		{"package detail dotted name", "/repos/myrepo/packages/npm/lodash.debounce", true},

		// Real format-handler endpoints that must NOT be treated as pages.
		{"npm packument", "/repos/myrepo/lodash", false},
		{"pypi upload", "/repos/myrepo/legacy/", false},
		{"pypi simple index", "/repos/myrepo/simple/", false},
		{"helm chart upload", "/repos/myrepo/charts", false},
		{"helm index", "/repos/myrepo/index.yaml", false},
		{"rpm repomd", "/repos/myrepo/repodata/repomd.xml", false},

		// Isolated namespaces are never SPA pages, regardless of shape.
		{"raw bare", "/repos/raw/myrepo", false},
		{"maven bare", "/repos/maven/myrepo", false},
		{"sbt bare", "/repos/sbt/myrepo", false},
		{"ivy bare", "/repos/ivy/myrepo", false},

		// Docker/OCI and /api/ paths are never SPA pages.
		{"docker manifest", "/v2/myimage/manifests/latest", false},
		{"admin api", "/api/v1/admin/users", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			browser := httptest.NewRequestWithContext(t.Context(), http.MethodGet, tc.path, nil)
			browser.Header.Set("Accept", "text/html,application/xhtml+xml,*/*;q=0.8")
			browser.Header.Set("Sec-Fetch-Mode", "navigate")

			if got := isSPANavigation(browser); got != tc.wantBypass {
				t.Errorf("isSPANavigation(browser GET %q) = %v, want %v", tc.path, got, tc.wantBypass)
			}

			// A non-navigation request (package manager / fetch) to the very
			// same URL is never a page load, whatever the path shape.
			pkg := httptest.NewRequestWithContext(t.Context(), http.MethodGet, tc.path, nil)
			pkg.Header.Set("Accept", "application/vnd.npm.install-v1+json")

			if isSPANavigation(pkg) {
				t.Errorf("isSPANavigation(non-HTML GET %q) = true, want false", tc.path)
			}

			// Credentials mean it is an API call, not a page load.
			authed := httptest.NewRequestWithContext(t.Context(), http.MethodGet, tc.path, nil)
			authed.Header.Set("Accept", "text/html")
			authed.Header.Set("Authorization", "Bearer x")

			if isSPANavigation(authed) {
				t.Errorf("isSPANavigation(authorized GET %q) = true, want false", tc.path)
			}

			// /repos/ paths are never plain static content.
			if strings.HasPrefix(tc.path, "/repos/") && isStaticPath(tc.path, RBACConfig{}) {
				t.Errorf("isStaticPath(%q) = true, want false", tc.path)
			}
		})
	}
}

func TestIsStaticPath(t *testing.T) {
	cfg := RBACConfig{MCPPath: "/mcp"}

	for path, want := range map[string]bool{
		"/":                     true,
		"/login":                true,
		"/assets/app.js":        true,
		"/sw.js":                true,
		"/manifest.webmanifest": true,
		"/offline.html":         true,
		"/icons/icon-192.png":   true,
		"/repos":                true,
		"/repos/x":              false,
		"/api/v1/auth/me":       false,
		"/v2/":                  false,
		"/mcp":                  false,
		"/mcp/sse":              false,
		"/mcpish":               true,
		"/repos/raw/x/a/b.txt":  false,
	} {
		if got := isStaticPath(path, cfg); got != want {
			t.Errorf("isStaticPath(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestMethodToActionDockerPaths(t *testing.T) {
	tests := []struct {
		method string
		path   string
		want   string
	}{
		{http.MethodGet, "/v2/myimage/manifests/latest", "read"},
		{http.MethodHead, "/v2/myimage/blobs/sha256:deadbeef", "read"},
		{http.MethodPut, "/v2/myimage/manifests/latest", "write"},
		{http.MethodPost, "/v2/myimage/blobs/uploads/", "write"},
		{http.MethodDelete, "/v2/myimage/manifests/latest", "delete"},
	}

	for _, tc := range tests {
		if got := methodToAction(tc.method, tc.path); got != tc.want {
			t.Errorf("methodToAction(%q, %q) = %q, want %q", tc.method, tc.path, got, tc.want)
		}
	}
}

// TestVueRepoRoutesAreServedAsSPA keeps the server's hand-written SPA matcher
// in sync with web/src/router/index.ts: every /repos/... vue-router path must
// deep-load as a page navigation.
func TestVueRepoRoutesAreServedAsSPA(t *testing.T) {
	src, err := os.ReadFile("../../web/src/router/index.ts")
	if err != nil {
		t.Fatalf("read router: %v", err)
	}

	paramRe := regexp.MustCompile(`:(\w+)`)
	routeRe := regexp.MustCompile(`path:\s*'(/repos[^']*)'`)

	matches := routeRe.FindAllStringSubmatch(string(src), -1)
	if len(matches) == 0 {
		t.Fatal("no /repos routes found in router/index.ts")
	}

	for _, m := range matches {
		path := paramRe.ReplaceAllString(m[1], "sample")
		if path == "/repos" {
			continue
		}

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
		req.Header.Set("Accept", "text/html")
		req.Header.Set("Sec-Fetch-Mode", "navigate")

		if !isSPANavigation(req) {
			t.Errorf("vue route %q (%q) is not recognised as an SPA navigation", m[1], path)
		}
	}
}
