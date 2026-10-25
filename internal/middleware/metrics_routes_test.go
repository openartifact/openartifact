// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package middleware_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/api"
	formatcargo "github.com/openartifact/openartifact/internal/format/cargo"
	formatconan "github.com/openartifact/openartifact/internal/format/conan"
	formatconda "github.com/openartifact/openartifact/internal/format/conda"
	formatdeb "github.com/openartifact/openartifact/internal/format/deb"
	formatdocker "github.com/openartifact/openartifact/internal/format/docker"
	formatgomod "github.com/openartifact/openartifact/internal/format/gomod"
	formathelm "github.com/openartifact/openartifact/internal/format/helm"
	formativy "github.com/openartifact/openartifact/internal/format/ivy"
	formatmaven "github.com/openartifact/openartifact/internal/format/maven"
	formatnpm "github.com/openartifact/openartifact/internal/format/npm"
	formatnuget "github.com/openartifact/openartifact/internal/format/nuget"
	formatpypi "github.com/openartifact/openartifact/internal/format/pypi"
	formatraw "github.com/openartifact/openartifact/internal/format/raw"
	formatrpm "github.com/openartifact/openartifact/internal/format/rpm"
	formatrubygems "github.com/openartifact/openartifact/internal/format/rubygems"
	formatsbt "github.com/openartifact/openartifact/internal/format/sbt"
	"github.com/openartifact/openartifact/internal/metrics"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/pkg/logger"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// serverRoutes registers every format and upload handler the way cmd/server
// wires them (same prefixes, same relative routes), with no dependencies: the
// handlers are only walked, never served.
func serverRoutes(t *testing.T) chi.Router {
	t.Helper()

	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatal(err)
	}

	r := chi.NewRouter()

	r.Route("/repos/{name}", func(r chi.Router) {
		formatrpm.NewHTTPHandler(nil, nil, nil).RegisterRepoRoutes(r)
		formatdeb.NewHTTPHandler(nil, nil, nil).RegisterRepoRoutes(r)
		formathelm.NewHTTPHandler(nil, nil, nil, "").RegisterRepoRoutes(r)
		formatpypi.NewHTTPHandler(nil, nil, nil, "").RegisterRepoRoutes(r)
		formatnuget.NewHTTPHandler(nil, nil, nil, "").RegisterRepoRoutes(r)
		formatnpm.NewHTTPHandler(nil, nil, nil, "").RegisterRepoRoutes(r)
		formatcargo.NewHTTPHandler(nil, nil, nil, "").RegisterRepoRoutes(r)
		formatrubygems.NewHTTPHandler(nil, nil, nil, "").RegisterRepoRoutes(r)
		formatconda.NewHTTPHandler(nil, nil, nil, "").RegisterRepoRoutes(r)
		formatconan.NewHTTPHandler(nil, nil, nil, "").RegisterRepoRoutes(r)
		formatgomod.NewHTTPHandler(nil, nil, nil, "").RegisterRepoRoutes(r)
		api.NewNPMUploadHandler(log, nil, nil, "", nil).RegisterRepoRoutes(r)
		// cmd/server dispatches PUT "/*" to npm publish or the GOPROXY upload.
		r.Put("/*", func(http.ResponseWriter, *http.Request) {})
	})
	r.Route("/repos/raw/{name}", func(r chi.Router) { formatraw.NewHTTPHandler(nil, nil, nil, "").RegisterRepoRoutes(r) })
	r.Route("/repos/maven/{name}", func(r chi.Router) { formatmaven.NewHTTPHandler(nil, nil, nil).RegisterRepoRoutes(r) })
	r.Route("/repos/sbt/{name}", func(r chi.Router) { formatsbt.NewHTTPHandler(nil, nil, nil, "").RegisterRepoRoutes(r) })
	r.Route("/repos/ivy/{name}", func(r chi.Router) { formativy.NewHTTPHandler(nil, nil, nil, "").RegisterRepoRoutes(r) })
	formatdocker.NewHTTPHandler(nil, nil, nil, "").RegisterRoutes(r)

	api.NewDebUploadHandler(log, nil, nil, nil).Register(r)
	api.NewRPMUploadHandler(log, nil, nil, nil).Register(r)
	api.NewMavenUploadHandler(log, nil, nil, nil).Register(r)
	api.NewPresignedUploadHandler(log, nil, nil, nil, 0, 0, nil).Register(r)

	return r
}

type walkedRoute struct{ method, pattern string }

func walk(t *testing.T, r chi.Router) []walkedRoute {
	t.Helper()

	var routes []walkedRoute

	err := chi.Walk(r, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		// RoutePattern, which the metrics middleware reads, has no trailing
		// slash: register the same form.
		routes = append(routes, walkedRoute{method, strings.TrimSuffix(strings.ReplaceAll(route, "/*/", "/"), "/")})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	return routes
}

func isWrite(method string) bool {
	return method == http.MethodPut || method == http.MethodPost || method == http.MethodPatch
}

// A route that writes (PUT, POST, PATCH) must say whether it is a hosted upload
// and who counts it. A new format that adds one fails here until it is in the
// upload route table of internal/middleware (and so in
// openartifact_uploads_total).
func TestUploadRoutesClassified(t *testing.T) {
	seen := map[string]bool{}

	for _, rt := range walk(t, serverRoutes(t)) {
		seen[rt.pattern] = true

		if !isWrite(rt.method) {
			continue
		}

		if _, known := middleware.UploadRouteClass(rt.pattern); !known {
			t.Errorf("%s %s is not classified: add it to uploadRoutes in internal/middleware/metrics_upload.go "+
				"(countedByMiddleware with its format, countedByHandler, or notAnUpload)", rt.method, rt.pattern)
		}
	}

	for _, p := range middleware.UploadRoutePatterns() {
		if !seen[p] {
			t.Errorf("uploadRoutes lists %q, which no handler registers any more", p)
		}
	}
}

// Every format directory under internal/format must have an upload format
// label, and every repository format a counter path.
func TestEveryFormatHasUploadLabel(t *testing.T) {
	// directory under internal/format -> upload format label; "" is a support
	// package without hosted uploads.
	known := map[string]string{
		"base": "", "filerepo": "", "formattest": "",
		"cargo": metrics.FormatCargo, "conan": metrics.FormatConan, "conda": metrics.FormatConda,
		"deb": metrics.FormatDeb, "docker": metrics.FormatDocker, "gomod": metrics.FormatGo,
		"helm": metrics.FormatHelm, "ivy": metrics.FormatIvy, "maven": metrics.FormatMaven,
		"npm": metrics.FormatNPM, "nuget": metrics.FormatNuGet, "pypi": metrics.FormatPyPI,
		"raw": metrics.FormatRaw, "rpm": metrics.FormatRPM, "rubygems": metrics.FormatRubyGems,
		"sbt": metrics.FormatSBT,
	}

	entries, err := os.ReadDir("../format")
	if err != nil {
		t.Fatal(err)
	}

	labelled := map[string]bool{}

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}

		label, ok := known[e.Name()]
		if !ok {
			t.Errorf("internal/format/%s has no upload format label: count its uploads with "+
				"Registry.CountUpload and add the format to metrics.UploadFormats and this test", e.Name())

			continue
		}

		if label != "" {
			labelled[label] = true

			if !slices.Contains(metrics.UploadFormats, label) {
				t.Errorf("%s: %q is not in metrics.UploadFormats", e.Name(), label)
			}
		}
	}

	for _, f := range metrics.UploadFormats {
		if !labelled[f] {
			t.Errorf("metrics.UploadFormats has %q but no internal/format directory maps to it", f)
		}
	}
}

var paramRE = regexp.MustCompile(`\{[^}]*\}`)

// concretePath turns a route template into a request path carrying the given
// repository/package name in every parameter and wildcard.
func concretePath(pattern, name string) string {
	p := paramRE.ReplaceAllString(pattern, name)

	return strings.ReplaceAll(p, "*", name+"/"+name+"/file.bin")
}

// Behaviour of every counted route: a success status counts success, a failure
// status error, under the route's format, and the handler-counted formats are
// left to their handlers.
func TestUploadRoutesCounted(t *testing.T) {
	routes := walk(t, serverRoutes(t))

	for _, status := range []int{http.StatusCreated, http.StatusInternalServerError} {
		reg := metrics.NewRegistryOn(prometheus.NewRegistry())
		stubs := chi.NewRouter()

		for _, rt := range routes {
			stubs.MethodFunc(rt.method, rt.pattern, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) })
		}

		h := middleware.MetricsMiddleware(reg)(stubs)

		for _, rt := range routes {
			counted, known := middleware.UploadRouteClass(rt.pattern)
			if !isWrite(rt.method) || !known || !counted || rt.pattern == "/v2/*" {
				continue
			}

			h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), rt.method, concretePath(rt.pattern, "x"), nil))
		}

		result := metrics.UploadResult(status)

		for _, f := range []string{
			metrics.FormatCargo, metrics.FormatRubyGems, metrics.FormatHelm, metrics.FormatPyPI,
			metrics.FormatNuGet, metrics.FormatConda, metrics.FormatConan, metrics.FormatRaw,
			metrics.FormatIvy, metrics.FormatSBT,
		} {
			if got := testutil.ToFloat64(reg.UploadsTotal.WithLabelValues(f, result)); got < 1 {
				t.Errorf("status %d: format %s counted %v times, want >= 1", status, f, got)
			}
		}

		// Counted by their handlers (or the GOPROXY wrapper), never here.
		for _, f := range []string{metrics.FormatMaven, metrics.FormatNPM, metrics.FormatDeb, metrics.FormatRPM, metrics.FormatGo} {
			if got := testutil.ToFloat64(reg.UploadsTotal.WithLabelValues(f, result)); got != 0 {
				t.Errorf("status %d: format %s counted %v times by the middleware, want 0", status, f, got)
			}
		}
	}
}

// The registry counts manifest and blob completions, in both outcomes.
func TestUploadRoutesCountedOCI(t *testing.T) {
	for status, result := range map[int]string{http.StatusCreated: "success", http.StatusInternalServerError: "error"} {
		reg := metrics.NewRegistryOn(prometheus.NewRegistry())
		stubs := chi.NewRouter()
		stubs.HandleFunc("/v2/*", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) })
		h := middleware.MetricsMiddleware(reg)(stubs)

		for _, req := range []struct{ method, url string }{
			{http.MethodPost, "/v2/r/img/blobs/uploads/"},
			{http.MethodPatch, "/v2/r/img/blobs/uploads/u1"},
			{http.MethodPut, "/v2/r/img/blobs/uploads/u1?digest=sha256:1"},
			{http.MethodPut, "/v2/r/img/manifests/latest"},
			{http.MethodGet, "/v2/r/img/manifests/latest"},
		} {
			h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), req.method, req.url, nil))
		}

		if got := testutil.ToFloat64(reg.UploadsTotal.WithLabelValues("docker", result)); got != 2 {
			t.Errorf("status %d: docker %s = %v, want 2 (blob completion + manifest)", status, result, got)
		}
	}
}

// Cardinality guard: whatever repository, package, version or digest names
// clients use, the path label set depends on the route table only.
func TestPathLabelCardinalityBounded(t *testing.T) {
	routes := walk(t, serverRoutes(t))
	gatherer := prometheus.NewRegistry()
	reg := metrics.NewRegistryOn(gatherer)
	stubs := chi.NewRouter()

	for _, rt := range routes {
		stubs.MethodFunc(rt.method, rt.pattern, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	}

	h := middleware.MetricsMiddleware(reg)(stubs)

	hit := func(i int) {
		name := fmt.Sprintf("name-%d-%x", i, i*2654435761)

		for _, rt := range routes {
			path := concretePath(rt.pattern, name)
			if rt.pattern == "/v2/*" {
				continue
			}

			h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), rt.method, path, nil))
		}

		// The registry and unrouted paths carry names too.
		for _, p := range []string{
			"/v2/" + name + "/" + name + "/manifests/" + name,
			"/v2/" + name + "/blobs/sha256:" + name,
			"/v2/" + name + "/blobs/uploads/",
			"/v2/" + name + "/blobs/uploads/" + name,
			"/v2/" + name + "/tags/list",
			"/v2/" + name + "/referrers/sha256:" + name,
			"/v2/" + name + "/nonsense",
			"/no/such/route/" + name,
		} {
			h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodGet, p, nil))
		}
	}

	labels := func() map[string]bool {
		mfs, err := gatherer.Gather()
		if err != nil {
			t.Fatal(err)
		}

		set := map[string]bool{}

		for _, mf := range mfs {
			if mf.GetName() != "openartifact_http_requests_total" {
				continue
			}

			for _, m := range mf.GetMetric() {
				for _, l := range m.GetLabel() {
					if l.GetName() == "path" {
						set[l.GetValue()] = true
					}
				}
			}
		}

		return set
	}

	for i := range 5 {
		hit(i)
	}

	before := labels()

	for i := 5; i < 400; i++ {
		hit(i)
	}

	after := labels()

	if len(before) != len(after) {
		t.Fatalf("path label set grew from %d to %d with new names", len(before), len(after))
	}

	for p := range after {
		if strings.Contains(p, "name-") {
			t.Errorf("path label %q carries a request name", p)
		}
	}

	if !after["unmatched"] {
		t.Error("unrouted requests must be labelled unmatched")
	}

	for _, want := range []string{
		"/v2/{name}/manifests/{reference}", "/v2/{name}/blobs/{digest}", "/v2/{name}/blobs/uploads",
		"/v2/{name}/blobs/uploads/{uuid}", "/v2/{name}/tags/list", "/v2/{name}/referrers/{digest}",
		"/repos/raw/{name}/*", "/repos/maven/{name}/*",
	} {
		if !after[want] {
			t.Errorf("missing path label %q", want)
		}
	}
}
