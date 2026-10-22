// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package cargo

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
)

const repoPrefix = "/repos/myrepo"

// newHandlerOnly is newCargoHandler for tests that inspect nothing but the
// HTTP responses.
func newHandlerOnly(t *testing.T) *HTTPHandler {
	t.Helper()

	h := NewHTTPHandler(formattest.NewLogger(t), &cargoDB{DB: formattest.NewDB()}, formattest.NewBlobStore(), "http://localhost")
	h.SetPublisher(&formattest.Publisher{})

	return h
}

func publishBody(meta string, crate []byte) *bytes.Buffer {
	var body bytes.Buffer

	_ = binary.Write(&body, binary.LittleEndian, uint32(len(meta)))
	body.WriteString(meta)
	_ = binary.Write(&body, binary.LittleEndian, uint32(len(crate)))
	body.Write(crate)

	return &body
}

func call(h *HTTPHandler, method, path string, body io.Reader) *httptest.ResponseRecorder {
	return formattest.Do(h.RegisterRepoRoutes, formattest.Request(method, repoPrefix+path, body))
}

func publish(t *testing.T, h *HTTPHandler, name, version string, crate []byte) *httptest.ResponseRecorder {
	t.Helper()

	meta := fmt.Sprintf(`{"name":%q,"vers":%q}`, name, version)

	return call(h, http.MethodPut, "/api/v1/crates/new", publishBody(meta, crate))
}

// indexEntries returns the JSON lines the sparse index serves for a crate.
func indexEntries(t *testing.T, h *HTTPHandler, path string) []map[string]any {
	t.Helper()

	w := call(h, http.MethodGet, path, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s = %d: %s", path, w.Code, w.Body.String())
	}

	var out []map[string]any

	for line := range strings.SplitSeq(strings.TrimSpace(w.Body.String()), "\n") {
		var e map[string]any
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("index line %q: %v", line, err)
		}

		out = append(out, e)
	}

	return out
}

// Yanking keeps a version out of new dependency resolution (the index says
// "yanked": true) but never removes it: every Cargo.lock that pins it must keep
// building, so the .crate stays downloadable.
func TestYankedCrateStaysDownloadable(t *testing.T) {
	h, _, bs, _ := newCargoHandler(t)
	crate := []byte("crate-bytes-of-demo-0.1.0")

	if w := publish(t, h, "demo", "0.1.0", crate); w.Code != http.StatusOK {
		t.Fatalf("publish = %d: %s", w.Code, w.Body.String())
	}

	if w := call(h, http.MethodDelete, "/api/v1/crates/demo/0.1.0/yank", nil); w.Code != http.StatusOK {
		t.Fatalf("yank = %d: %s", w.Code, w.Body.String())
	}

	entries := indexEntries(t, h, "/index/de/mo/demo")
	if len(entries) != 1 || entries[0]["yanked"] != true {
		t.Fatalf("index after yank = %v, want one entry with yanked=true", entries)
	}

	w := call(h, http.MethodGet, "/api/v1/crates/demo/0.1.0/download", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("download of a yanked crate = %d, want 200: %s", w.Code, w.Body.String())
	}

	if !bytes.Equal(w.Body.Bytes(), crate) {
		t.Errorf("downloaded %q, want the stored crate %q", w.Body.Bytes(), crate)
	}

	if len(bs.Blobs) != 1 {
		t.Errorf("blobs = %d: yanking must not delete the crate", len(bs.Blobs))
	}

	// Unyanking flips only the index flag back.
	if w := call(h, http.MethodPut, "/api/v1/crates/demo/0.1.0/unyank", nil); w.Code != http.StatusOK {
		t.Fatalf("unyank = %d", w.Code)
	}

	if entries := indexEntries(t, h, "/index/de/mo/demo"); entries[0]["yanked"] != false {
		t.Errorf("index after unyank = %v, want yanked=false", entries)
	}
}

// Cargo lowercases the crate name in index paths and download URLs, whatever
// case the crate was published in.
func TestCrateNameLookupIsCaseInsensitive(t *testing.T) {
	h := newHandlerOnly(t)

	if w := publish(t, h, "Demo-Crate", "1.0.0", []byte("x")); w.Code != http.StatusOK {
		t.Fatalf("publish = %d: %s", w.Code, w.Body.String())
	}

	entries := indexEntries(t, h, "/index/de/mo/demo-crate")
	if len(entries) != 1 || entries[0]["name"] != "Demo-Crate" {
		t.Fatalf("index via lower-case path = %v, want the Demo-Crate entry", entries)
	}

	if w := call(h, http.MethodGet, "/api/v1/crates/demo-crate/1.0.0/download", nil); w.Code != http.StatusOK {
		t.Errorf("download via lower-case name = %d, want 200", w.Code)
	}

	if w := call(h, http.MethodDelete, "/api/v1/crates/demo-crate/1.0.0/yank", nil); w.Code != http.StatusOK {
		t.Fatalf("yank via lower-case name = %d", w.Code)
	}

	if entries := indexEntries(t, h, "/index/de/mo/DEMO-CRATE"); entries[0]["yanked"] != true {
		t.Errorf("yank via the lower-case name did not apply: %v", entries)
	}
}

func TestPublishRefusesNameDifferingOnlyInCase(t *testing.T) {
	h, db, _, _ := newCargoHandler(t)

	if w := publish(t, h, "Demo-Crate", "1.0.0", []byte("x")); w.Code != http.StatusOK {
		t.Fatalf("publish = %d: %s", w.Code, w.Body.String())
	}

	w := publish(t, h, "demo-crate", "1.1.0", []byte("y"))
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", w.Code, w.Body.String())
	}

	if len(db.crates) != 1 {
		t.Errorf("stored crates = %d, want 1", len(db.crates))
	}
}

func TestSearchReturnsOneEntryPerCrate(t *testing.T) {
	h, db, _, _ := newCargoHandler(t)
	db.total = 25
	db.summaries = []*models.CargoCrateSummary{
		{
			Name: "alpha", Description: "newest description", Downloads: 42,
			Versions:     []string{"0.9.0", "0.10.0", "1.0.0-rc.1"},
			LiveVersions: []string{"0.9.0", "0.10.0", "1.0.0-rc.1"},
		},
		{
			Name: "beta", Downloads: 7,
			Versions:     []string{"1.0.0", "2.0.0"},
			LiveVersions: []string{"1.0.0"},
		},
	}

	w := call(h, http.MethodGet, "/api/v1/crates?q=a&per_page=2", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Crates []struct {
			Name       string `json:"name"`
			MaxVersion string `json:"max_version"`
			Downloads  int64  `json:"downloads"`
		} `json:"crates"`
		Meta struct {
			Total int `json:"total"`
		} `json:"meta"`
	}

	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("body %q: %v", w.Body.String(), err)
	}

	if len(resp.Crates) != 2 || resp.Crates[0].Name != "alpha" || resp.Crates[1].Name != "beta" {
		t.Fatalf("crates = %+v, want alpha and beta once each", resp.Crates)
	}

	// Highest stable, not the lexically largest (0.9.0) nor the prerelease.
	if resp.Crates[0].MaxVersion != "0.10.0" || resp.Crates[0].Downloads != 42 {
		t.Errorf("alpha = %+v, want max_version 0.10.0 and the summed downloads", resp.Crates[0])
	}

	// A yanked version is not the crate's version while another is available.
	if resp.Crates[1].MaxVersion != "1.0.0" {
		t.Errorf("beta max_version = %q, want 1.0.0 (2.0.0 is yanked)", resp.Crates[1].MaxVersion)
	}

	// total counts crates matching the query, not the entries of this page.
	if resp.Meta.Total != 25 {
		t.Errorf("meta.total = %d, want 25", resp.Meta.Total)
	}
}

func TestSearchEmptyResultIsArray(t *testing.T) {
	h := newHandlerOnly(t)

	w := call(h, http.MethodGet, "/api/v1/crates?q=nothing", nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"crates":[]`) || !strings.Contains(w.Body.String(), `"total":0`) {
		t.Fatalf("empty search = %d %q", w.Code, w.Body.String())
	}
}

func TestMaxCrateVersion(t *testing.T) {
	cases := []struct {
		name string
		c    models.CargoCrateSummary
		want string
	}{
		{"highest stable wins", models.CargoCrateSummary{Versions: []string{"1.0.0", "2.0.0-beta.1", "1.10.0", "1.9.0"}, LiveVersions: []string{"1.0.0", "2.0.0-beta.1", "1.10.0", "1.9.0"}}, "1.10.0"},
		{"only prereleases", models.CargoCrateSummary{Versions: []string{"1.0.0-rc.2", "1.0.0-rc.10"}, LiveVersions: []string{"1.0.0-rc.2", "1.0.0-rc.10"}}, "1.0.0-rc.10"},
		{"yanked skipped", models.CargoCrateSummary{Versions: []string{"1.0.0", "1.1.0"}, LiveVersions: []string{"1.0.0"}}, "1.0.0"},
		{"stable yanked, prerelease live", models.CargoCrateSummary{Versions: []string{"1.0.0", "1.1.0-rc.1"}, LiveVersions: []string{"1.1.0-rc.1"}}, "1.1.0-rc.1"},
		{"everything yanked", models.CargoCrateSummary{Versions: []string{"0.2.0", "0.10.0"}}, "0.10.0"},
		{"unparsable rows", models.CargoCrateSummary{Versions: []string{"old", "newer"}, LiveVersions: []string{"old", "newer"}}, "newer"},
	}

	for _, c := range cases {
		if got := maxCrateVersion(&c.c); got != c.want {
			t.Errorf("%s: maxCrateVersion = %q, want %q", c.name, got, c.want)
		}
	}
}

// A private repository tells cargo to authenticate every request; a public one
// must stay anonymous. Cargo fetches config.json from the index root.
func TestConfigJSONAdvertisesAuthRequiredForPrivateRepos(t *testing.T) {
	for _, path := range []string{"/config.json", "/index/config.json"} {
		for _, public := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s public=%v", path, public), func(t *testing.T) {
				h, db, _, _ := newCargoHandler(t)
				db.Repo.Public = public

				w := call(h, http.MethodGet, path, nil)
				if w.Code != http.StatusOK {
					t.Fatalf("status = %d: %s", w.Code, w.Body.String())
				}

				var cfg map[string]any
				if err := json.Unmarshal(w.Body.Bytes(), &cfg); err != nil {
					t.Fatalf("body %q: %v", w.Body.String(), err)
				}

				if cfg["dl"] != "http://localhost/repos/myrepo/api/v1/crates" || cfg["api"] != "http://localhost/repos/myrepo" {
					t.Errorf("config = %v, want dl and api under the repo", cfg)
				}

				got, present := cfg["auth-required"]
				if public && present {
					t.Errorf("public repo advertises auth-required: %v", cfg)
				}

				if !public && got != true {
					t.Errorf("private repo config = %v, want \"auth-required\": true", cfg)
				}
			})
		}
	}
}

func TestPublishValidatesNameAndVersion(t *testing.T) {
	long := strings.Repeat("a", maxCrateNameLen+1)

	bad := []struct{ name, version string }{
		{"", "1.0.0"},
		{"../evil", "1.0.0"},
		{"1abc", "1.0.0"},
		{"-abc", "1.0.0"},
		{"a b", "1.0.0"},
		{"démo", "1.0.0"},
		{"a/b", "1.0.0"},
		{long, "1.0.0"},
		{"demo", ""},
		{"demo", "1.0"},
		{"demo", "v1.0.0"},
		{"demo", "1.0.0.0"},
		{"demo", "01.0.0"},
		{"demo", "latest"},
		{"demo", "1.0.0/../x"},
		{"demo", "1.0.0-" + strings.Repeat("a", maxCrateVersionLen)},
	}

	for _, c := range bad {
		t.Run(fmt.Sprintf("reject %.20q@%.20q", c.name, c.version), func(t *testing.T) {
			h, db, bs, pub := newCargoHandler(t)

			w := publish(t, h, c.name, c.version, []byte("x"))
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", w.Code, w.Body.String())
			}

			if len(db.crates) != 0 || len(bs.Blobs) != 0 || len(pub.Events()) != 0 {
				t.Error("rejected publish stored or emitted something")
			}
		})
	}

	good := []struct{ name, version string }{
		{"a", "0.0.1"},
		{"_x", "1.2.3"},
		{"foo-bar_9", "10.20.30-rc.1"},
		{"Serde", "1.0.0-alpha.1+build.5"},
		{strings.Repeat("a", maxCrateNameLen), "1.0.0"},
	}

	for _, c := range good {
		t.Run(fmt.Sprintf("accept %.20q@%.20q", c.name, c.version), func(t *testing.T) {
			h := newHandlerOnly(t)

			if w := publish(t, h, c.name, c.version, []byte("x")); w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
			}
		})
	}
}

// Publishing a version that exists is a 409 with cargo's JSON error shape, not
// a 500, and it is decided before the crate body is read.
func TestPublishDuplicateVersionIsConflict(t *testing.T) {
	h, db, bs, pub := newCargoHandler(t)

	if w := publish(t, h, "demo", "0.1.0", []byte("first")); w.Code != http.StatusOK {
		t.Fatalf("first publish = %d: %s", w.Code, w.Body.String())
	}

	for _, version := range []string{"0.1.0", "0.1.0+other.build"} {
		w := publish(t, h, "demo", version, []byte("second"))
		if w.Code != http.StatusConflict {
			t.Fatalf("republish of %s = %d, want 409: %s", version, w.Code, w.Body.String())
		}

		var resp struct {
			Errors []struct {
				Detail string `json:"detail"`
			} `json:"errors"`
		}

		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || len(resp.Errors) != 1 || !strings.Contains(resp.Errors[0].Detail, "already uploaded") {
			t.Errorf("409 body = %q, want cargo's {\"errors\":[{\"detail\":...}]}", w.Body.String())
		}
	}

	if len(db.crates) != 1 || len(bs.Blobs) != 1 || len(pub.Events()) != 1 {
		t.Errorf("duplicates changed state: crates=%d blobs=%d events=%d", len(db.crates), len(bs.Blobs), len(pub.Events()))
	}

	// Decided from the metadata alone: the crate part need not even arrive.
	truncated := bytes.NewBuffer(nil)
	meta := `{"name":"demo","vers":"0.1.0"}`
	_ = binary.Write(truncated, binary.LittleEndian, uint32(len(meta)))
	truncated.WriteString(meta)

	if w := call(h, http.MethodPut, "/api/v1/crates/new", truncated); w.Code != http.StatusConflict {
		t.Errorf("duplicate without a crate body = %d, want 409 before reading it", w.Code)
	}

	// A new version of the same crate still goes through.
	if w := publish(t, h, "demo", "0.2.0", []byte("third")); w.Code != http.StatusOK {
		t.Errorf("next version = %d: %s", w.Code, w.Body.String())
	}
}

// Two publishers of one version can both pass the pre-check; the loser hits the
// unique index and must still get a 409.
func TestPublishLostRaceIsConflict(t *testing.T) {
	h, db, _, pub := newCargoHandler(t)
	db.createErr = fmt.Errorf("create cargo crate: %w", &pgconn.PgError{Code: "23505"})

	if w := publish(t, h, "demo", "0.1.0", []byte("x")); w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", w.Code)
	}

	if len(pub.Events()) != 0 {
		t.Error("a lost race must not emit an upload event")
	}
}

// The declared length of the crate part is client-controlled. A body that
// declares 100 MiB and delivers a few bytes must be refused without the server
// having allocated the declared size first.
func TestPublishDoesNotPreallocateDeclaredLength(t *testing.T) {
	h := newHandlerOnly(t)

	meta := `{"name":"demo","vers":"0.1.0"}`

	var body bytes.Buffer

	_ = binary.Write(&body, binary.LittleEndian, uint32(len(meta)))
	body.WriteString(meta)
	_ = binary.Write(&body, binary.LittleEndian, uint32(100<<20))
	body.WriteString("only a few bytes")

	var before, after runtime.MemStats

	runtime.ReadMemStats(&before)

	w := call(h, http.MethodPut, "/api/v1/crates/new", &body)

	runtime.ReadMemStats(&after)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a short body", w.Code)
	}

	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 32<<20 {
		t.Errorf("a 16-byte body made the server allocate %d MiB; the declared 100 MiB must not be preallocated", allocated>>20)
	}
}

func TestYankUnknownCrateIs404(t *testing.T) {
	h, db, _, _ := newCargoHandler(t)

	for _, c := range []struct{ method, path string }{
		{http.MethodDelete, "/api/v1/crates/ghost/0.1.0/yank"},
		{http.MethodPut, "/api/v1/crates/ghost/0.1.0/unyank"},
	} {
		if w := call(h, c.method, c.path, nil); w.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404", c.method, c.path, w.Code)
		}
	}

	if len(db.crates) != 0 {
		t.Errorf("crates = %d, want none", len(db.crates))
	}
}

func TestYankHonoursImmutableReleases(t *testing.T) {
	h, db, _, _ := newCargoHandler(t)

	if w := publish(t, h, "demo", "0.1.0", []byte("x")); w.Code != http.StatusOK {
		t.Fatalf("publish = %d: %s", w.Code, w.Body.String())
	}

	db.Repo.ImmutableReleases = true

	if w := call(h, http.MethodDelete, "/api/v1/crates/demo/0.1.0/yank", nil); w.Code != http.StatusConflict {
		t.Fatalf("yank of immutable release = %d, want 409", w.Code)
	}

	if db.crates[0].Yanked {
		t.Error("release was yanked despite immutable_releases")
	}

	if w := publish(t, h, "demo", "0.2.0-rc1", []byte("y")); w.Code != http.StatusOK {
		t.Fatalf("publish prerelease = %d", w.Code)
	}

	if w := call(h, http.MethodDelete, "/api/v1/crates/demo/0.2.0-rc1/yank", nil); w.Code != http.StatusOK {
		t.Fatalf("yank of prerelease = %d, want 200", w.Code)
	}
}
