// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/config"
	"github.com/openartifact/openartifact/internal/metrics"
	"github.com/openartifact/openartifact/internal/repodispatch"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/logger"
	"github.com/prometheus/client_golang/prometheus"
)

// documentedUpload is how one format is uploaded to: the route the server
// registers for it and what the documentation says about it.
type documentedUpload struct {
	// method and pattern are the chi route that receives the upload.
	method, pattern string
	// readme must appear in the Upload column of the format matrix in
	// docs/formats/README.md.
	readme string
	// page must appear in the format's own page (the matrix's Page column).
	page string
}

// documentedUploads is the single table that ties the documented upload of
// every format to the route table. Changing a route, or the documentation of
// one, fails a test here until both agree.
var documentedUploads = map[string]documentedUpload{ //nolint:gochecknoglobals // read-only test table
	"maven":  {http.MethodPut, "/repos/maven/{name}/*", "mvn deploy", "PUT /repos/maven/REPO/<group/path>/<artifactId>/<version>/<file>"},
	"sbt":    {http.MethodPut, "/repos/sbt/{name}/*", "sbt publish", "HOST/repos/sbt/REPO"},
	"ivy":    {http.MethodPut, "/repos/ivy/{name}/*", "ivy:publish", "HOST/repos/ivy/REPO"},
	"raw":    {http.MethodPut, "/repos/raw/{name}/*", "PUT <path>", "-T file.bin HOST/repos/raw/REPO/path/to/file.bin"},
	"npm":    {http.MethodPut, "/repos/{name}/*", "PUT /<pkg>", "PUT /repos/REPO/<package>"},
	"pypi":   {http.MethodPost, "/repos/{name}/legacy/", "HOST/repos/REPO/legacy/", "HOST/repos/REPO/legacy/"},
	"docker": {http.MethodPost, "/v2/*", "docker push", "docker push HOSTPORT/REPO:latest"},
	"helm":   {http.MethodPost, "/repos/{name}/charts", "POST /charts", "HOST/repos/REPO/charts"},
	"deb": {
		http.MethodPost,
		"/api/v1/repositories/{name}/debs",
		"POST /api/v1/repositories/REPO/debs",
		"POST /api/v1/repositories/REPO/debs",
	},
	"rpm": {
		http.MethodPost,
		"/api/v1/repositories/{name}/packages",
		"POST /api/v1/repositories/REPO/packages",
		"POST /api/v1/repositories/REPO/packages",
	},
	"go":       {http.MethodPut, "/repos/{name}/*", "PUT <module>/@v/<version>", "HOST/repos/REPO/example.com/mymod/@v/v1.0.0"},
	"cargo":    {http.MethodPut, "/repos/{name}/api/v1/crates/new", "PUT /api/v1/crates/new", "| `PUT /api/v1/crates/new` | publish |"},
	"nuget":    {http.MethodPut, "/repos/{name}/v3/package", "PUT /v3/package", "`PackagePublish/2.0.0` | `/v3/package`"},
	"rubygems": {http.MethodPost, "/repos/{name}/api/v1/gems", "POST /api/v1/gems", "| `POST /api/v1/gems` | push"},
	"conda":    {http.MethodPost, "/repos/{name}/{platform}", "POST /<platform>", "| `POST /<platform>` | upload"},
	"pacman":   {http.MethodPut, "/repos/{name}/os/{arch}/{filename}", "PUT /os/<arch>/<pkg>.pkg.tar.zst", "PUT /repos/REPO/os/ARCH/FILE"},
	"conan": {
		http.MethodPut,
		"/repos/{name}/v2/conans/{pkg}/{version}/{user}/{channel}/revisions/{rrev}/files/*",
		"conan upload",
		"conan upload",
	},
}

// uploadRouteTable registers the real upload-capable routes the way the
// server does (repository protocol handlers, the REST upload handlers and the
// isolated /repos/<format>/{name} prefixes) on a chi router.
func uploadRouteTable(t *testing.T) chi.Router {
	t.Helper()

	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatal(err)
	}

	a := &app{cfg: &config.Config{}, log: log, reg: metrics.NewRegistryOn(prometheus.NewRegistry())}
	a.web.authed = chi.NewRouter()

	fsStore, err := storage.NewFilesystemStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	a.store.blobs = storage.NewMetricsBlobStore(fsStore, a.reg)

	a.newFormatHandlers()
	a.registerUploadRoutes()

	dispatcher := repodispatch.New(a.log, a.db, nil, nil, a.hostedRouters())
	a.mountRepoRoutes(dispatcher)
	a.mountIsolatedRepoRoutes(dispatcher)

	return a.web.authed
}

// matrixRow is one row of the format matrix in docs/formats/README.md.
type matrixRow struct{ upload, page string }

var matrixPageLink = regexp.MustCompile(`\(([a-z-]+\.md)\)`)

// formatMatrix reads the "Format matrix" table of docs/formats/README.md,
// keyed by format id.
func formatMatrix(t *testing.T) map[string]matrixRow {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "formats", "README.md"))
	if err != nil {
		t.Fatal(err)
	}

	rows := map[string]matrixRow{}

	for line := range strings.SplitSeq(string(raw), "\n") {
		// | Format | Format id | Client URL | Types | Upload | Page |
		cells := strings.Split(line, "|")
		if len(cells) != 8 || !strings.HasPrefix(strings.TrimSpace(cells[2]), "`") {
			continue
		}

		id := strings.Trim(strings.TrimSpace(cells[2]), "`")

		link := matrixPageLink.FindStringSubmatch(cells[6])
		if link == nil {
			t.Fatalf("format %s: no page link in %q", id, cells[6])
		}

		rows[id] = matrixRow{upload: cells[5], page: link[1]}
	}

	if len(rows) == 0 {
		t.Fatal("no format rows found in docs/formats/README.md")
	}

	return rows
}

func TestFormatMatrixUploadColumnMatchesDocumentedUploads(t *testing.T) {
	rows := formatMatrix(t)

	for id := range rows {
		if _, ok := documentedUploads[id]; !ok {
			t.Errorf("format %q is in docs/formats/README.md but has no entry in documentedUploads", id)
		}
	}

	for id, want := range documentedUploads {
		row, ok := rows[id]
		if !ok {
			t.Errorf("format %q has an entry in documentedUploads but no row in docs/formats/README.md", id)
			continue
		}

		if !strings.Contains(row.upload, want.readme) {
			t.Errorf("%s: Upload column %q does not contain %q", id, strings.TrimSpace(row.upload), want.readme)
		}

		page, err := os.ReadFile(filepath.Join("..", "..", "docs", "formats", row.page))
		if err != nil {
			t.Errorf("%s: %v", id, err)
			continue
		}

		if !strings.Contains(string(page), want.page) {
			t.Errorf("%s: docs/formats/%s does not contain %q", id, row.page, want.page)
		}
	}
}

func TestDocumentedUploadsAreRegisteredRoutes(t *testing.T) {
	registered := map[string]bool{}

	err := chi.Walk(uploadRouteTable(t), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		registered[method+" "+route] = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	for id, want := range documentedUploads {
		if key := want.method + " " + want.pattern; !registered[key] {
			t.Errorf("%s: documented upload route %q is not registered", id, key)
		}
	}
}

// staleUploadPath matches the upload path that never existed: a generic
// /repos/<repo>/upload endpoint (issue #237).
var staleUploadPath = regexp.MustCompile(`/repos/[A-Za-z{}_<>$-]+/upload\b`)

func TestNoDocumentedGenericUploadEndpoint(t *testing.T) {
	root := filepath.Join("..", "..")

	for _, dir := range []string{"docs", filepath.Join("web", "src", "lib"), filepath.Join("web", "src", "i18n"), filepath.Join("internal", "clientsetup"), filepath.Join("internal", "mcp"), filepath.Join("cmd", "oa"), "README.md", "examples"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || strings.HasSuffix(path, "_test.go") || strings.Contains(path, "__tests__") {
				return err
			}

			switch filepath.Ext(path) {
			case ".md", ".go", ".ts", ".json", ".yaml", ".yml":
			default:
				return nil
			}

			raw, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}

			for line := range strings.SplitSeq(string(raw), "\n") {
				// The pages that say the path answers 404 may name it.
				if m := staleUploadPath.FindString(line); m != "" && !strings.Contains(line, "404") {
					t.Errorf(
						"%s mentions %q as an upload endpoint: there is none (deb: POST /api/v1/repositories/REPO/debs, rpm: .../packages)",
						path,
						m,
					)
				}
			}

			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
