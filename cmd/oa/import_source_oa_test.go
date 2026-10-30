// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/openartifact/openartifact/pkg/nix"
)

// ─── fake destination: apk, nix and terraform upload protocols ───────────────

//nolint:gochecknoinits // adds the routes of these formats to every fakeOA (see fakeOARoutes)
func init() {
	fakeOARoutes["apk"] = func(f *fakeOA, mux *http.ServeMux) {
		mux.HandleFunc("PUT /{branch}/{repo}/{arch}/{file}", func(w http.ResponseWriter, r *http.Request) {
			f.writes++

			data, _ := io.ReadAll(r.Body)
			it := f.store(r.Header.Get("X-Fake-Repo"), r.URL.Path[1:], data, r.Header.Get(headerImportCreatedAt))
			it.name, it.version = r.PathValue("branch")+"/"+r.PathValue("repo")+"/"+r.PathValue("file"), r.PathValue("arch")
			f.reply(w, http.StatusCreated, map[string]any{"id": it.id, "sha256": it.sha})
		})
	}

	fakeOARoutes["terraform"] = func(f *fakeOA, mux *http.ServeMux) {
		put := func(path string) http.HandlerFunc {
			return func(w http.ResponseWriter, r *http.Request) {
				f.writes++

				data, _ := io.ReadAll(r.Body)
				it := f.store(r.Header.Get("X-Fake-Repo"), r.URL.Path, data, r.Header.Get(headerImportCreatedAt))
				it.name = path

				if p := r.URL.Query().Get("protocols"); p != "" {
					it.props = map[string]string{"protocols": p}
				}

				f.reply(w, http.StatusCreated, map[string]any{"id": it.id, "sha256": it.sha})
			}
		}

		mux.HandleFunc("PUT /_api/modules/{name}/{system}/{version}", put("module"))
		mux.HandleFunc("PUT /_api/providers/{type}/{version}/{os}/{arch}", put("provider"))
	}

	fakeOARoutes["nix"] = func(f *fakeOA, mux *http.ServeMux) {
		nars := map[string][]byte{}

		mux.HandleFunc("PUT /nar/{file}", func(w http.ResponseWriter, r *http.Request) {
			f.writes++

			nars[r.PathValue("file")], _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusCreated)
		})
		mux.HandleFunc("PUT /{narinfo}", func(w http.ResponseWriter, r *http.Request) {
			f.writes++

			text, _ := io.ReadAll(r.Body)

			info, err := nix.ParseNarInfo(text)
			if err != nil {
				f.reply(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}

			file, _, _ := info.NarFile()

			nar, ok := nars[file]
			if !ok {
				f.reply(w, http.StatusBadRequest, map[string]string{"error": "NAR not uploaded"})
				return
			}

			it := f.store(r.Header.Get("X-Fake-Repo"), r.PathValue("narinfo"), nar, r.Header.Get(headerImportCreatedAt))
			it.name, it.version = info.Name(), info.StoreHash()
			it.props = map[string]string{"narinfo": string(text)}
			f.reply(w, http.StatusCreated, map[string]any{})
		})
	}
}

// ─── fake source: an OpenArtifact 0.14 instance ──────────────────────────────

const (
	oaSrcToken = "src-token"
	oaSrcStamp = "2024-03-01T10:00:00Z"
)

type oaSrcRow struct {
	id, name, version, arch, filename string
	data                              []byte
	sha                               string // the listing's digest; the data's when empty
	created                           string
	props                             map[string]string
}

type oaSrcRepo struct {
	format, repoType string
	rows             []oaSrcRow
}

// fakeOA014 serves the public API of a 0.14 instance the importer reads: the
// repository list, the package listing with its downloads and properties, and
// the native endpoints a format's listing does not cover (static answers keyed
// by the path below /repos/<repo>/).
type fakeOA014 struct {
	srv    *httptest.Server
	repos  map[string]*oaSrcRepo
	order  []string
	native map[string][]byte
	mu     sync.Mutex
	hits   map[string]int
}

func newFakeOA014(t *testing.T) *fakeOA014 {
	t.Helper()

	s := &fakeOA014{repos: map[string]*oaSrcRepo{}, native: map[string][]byte{}, hits: map[string]int{}}
	s.srv = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.srv.Close)

	return s
}

func (s *fakeOA014) add(name, format, repoType string, rows ...oaSrcRow) {
	for i := range rows {
		if rows[i].id == "" {
			rows[i].id = name + "-" + strconv.Itoa(i+1)
		}

		if rows[i].created == "" {
			rows[i].created = oaSrcStamp
		}
	}

	s.repos[name] = &oaSrcRepo{format: format, repoType: repoType, rows: rows}
	s.order = append(s.order, name)
}

func (s *fakeOA014) hit(what string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.hits[what]++
}

func (s *fakeOA014) json(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (s *fakeOA014) serve(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+oaSrcToken {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}

	p := r.URL.Path

	switch {
	case p == "/api/v1/repositories":
		s.listRepos(w, r)
	case strings.HasPrefix(p, "/api/v1/repository/"):
		name := strings.TrimPrefix(p, "/api/v1/repository/")

		repo := s.repos[name]
		if repo == nil {
			http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
			return
		}

		s.json(w, map[string]string{"name": name, "format": repo.format, "repo_type": repo.repoType})
	case strings.HasPrefix(p, "/api/v1/repositories/"):
		s.repoAPI(w, r, strings.Split(strings.TrimPrefix(p, "/api/v1/repositories/"), "/"))
	case strings.HasPrefix(p, "/repos/"):
		s.hit("native " + p)

		data, ok := s.native[strings.TrimPrefix(p, "/repos/")]
		if !ok {
			http.NotFound(w, r)
			return
		}

		_, _ = w.Write(data)
	default:
		http.NotFound(w, r)
	}
}

func (s *fakeOA014) listRepos(w http.ResponseWriter, _ *http.Request) {
	var out []map[string]string
	for _, n := range s.order {
		out = append(out, map[string]string{"name": n, "format": s.repos[n].format, "repo_type": s.repos[n].repoType})
	}

	s.json(w, map[string]any{"total": len(out), "repositories": out})
}

func (s *fakeOA014) repoAPI(w http.ResponseWriter, r *http.Request, parts []string) {
	repo := s.repos[parts[0]]
	if repo == nil || len(parts) < 2 {
		http.NotFound(w, r)
		return
	}

	switch {
	case parts[1] == "packages" && len(parts) == 2:
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))

		var out []map[string]any

		for i, row := range repo.rows {
			if i < (page-1)*limit || i >= page*limit {
				continue
			}

			sha := row.sha
			if sha == "" {
				sha = sum256(row.data)
			}

			out = append(out, map[string]any{
				"id": row.id, "name": row.name, "version": row.version, "arch": row.arch, "filename": row.filename,
				"size": len(row.data), "sha256": sha, "created_at": row.created, "format": repo.format,
			})
		}

		s.json(w, map[string]any{"total": len(repo.rows), "page": page, "limit": limit, "packages": out})
	case parts[1] == "packages" && len(parts) == 4 && parts[3] == "download":
		for _, row := range repo.rows {
			if row.id == parts[2] {
				s.hit("download " + row.id)
				_, _ = w.Write(row.data)

				return
			}
		}

		http.NotFound(w, r)
	case parts[1] == "artifacts" && len(parts) == 4 && parts[3] == "properties":
		for _, row := range repo.rows {
			if row.id == parts[2] && row.props != nil {
				s.json(w, row.props)
				return
			}
		}

		http.NotFound(w, r)
	default:
		http.NotFound(w, r)
	}
}

// ─── the test instance ───────────────────────────────────────────────────────

const (
	nixHashOf = 7 // byte value the fake store hash is made of
)

type oaFixture struct {
	src     *fakeOA014
	nixHash string
	nixNar  []byte
	debPath string
	nixInfo string
}

// newOAFixture is a 0.14 instance with hosted repositories of several formats
// (one of them, conan, with a recipe and a package of several files each), a
// proxy and a group.
func newOAFixture(t *testing.T) *oaFixture {
	t.Helper()

	fx := &oaFixture{src: newFakeOA014(t)}
	s := fx.src

	// raw: single files, one with properties
	s.add(
		"files",
		"raw",
		"hosted",
		oaSrcRow{
			name:     "docs/a.txt",
			filename: "docs/a.txt",
			data:     []byte("alpha"),
			props:    map[string]string{"team": "docs", "build.number": "7"},
		},
		oaSrcRow{name: "b.bin", filename: "b.bin", data: []byte("bravo"), created: "2023-07-15T08:30:00Z"},
	)

	// conan: a recipe revision (the row) and one package revision, several files each
	manifest := []byte("1700000000\nconanfile.py: abc\n")
	s.add("libs", "conan", "hosted", oaSrcRow{
		name: "zlib", version: "1.3", filename: "zlib/1.3@_/_#rrev1", data: manifest, props: map[string]string{"owner": "core"},
	})

	const recipe = "libs/v2/conans/zlib/1.3/_/_/revisions/rrev1"

	s.native[recipe+"/files"] = []byte(
		`{"files":{"conanfile.py":{"size":11},"conanmanifest.txt":{"size":` + strconv.Itoa(len(manifest)) + `}}}`,
	)
	s.native[recipe+"/files/conanfile.py"] = []byte("# conanfile")
	s.native[recipe+"/files/conanmanifest.txt"] = manifest
	s.native[recipe+"/search"] = []byte(`{"pid1":{}}`)
	s.native[recipe+"/packages/pid1/revisions"] = []byte(`{"revisions":[{"revision":"prev1","time":"2024-03-02T11:00:00.000000+00:00"}]}`)
	s.native[recipe+"/packages/pid1/revisions/prev1/files"] = []byte(
		`{"files":{"conan_package.tgz":{"size":4},"conaninfo.txt":{"size":4},"conanmanifest.txt":{"size":4}}}`,
	)
	s.native[recipe+"/packages/pid1/revisions/prev1/files/conan_package.tgz"] = []byte("PKG!")
	s.native[recipe+"/packages/pid1/revisions/prev1/files/conaninfo.txt"] = []byte("INFO")
	s.native[recipe+"/packages/pid1/revisions/prev1/files/conanmanifest.txt"] = []byte("MANI")

	// apk
	s.add("alpine", "apk", "hosted", oaSrcRow{
		name: "v3.20/main/hello", version: "1.0-r0", arch: "x86_64", filename: "hello-1.0-r0.apk", data: []byte("apk-bytes"),
	})

	// terraform: a module and a provider platform
	s.add("tf", "terraform", "hosted",
		oaSrcRow{name: "modules/vpc/aws", version: "1.0.0", filename: "vpc-aws-1.0.0.tar.gz", data: []byte("module-archive")},
		oaSrcRow{
			name: "providers/random", version: "3.6.0", arch: "linux_amd64",
			filename: "terraform-provider-random_3.6.0_linux_amd64.zip", data: []byte("provider-zip"),
		},
	)
	s.native["tf/random/versions"] = []byte(
		`{"versions":[{"version":"3.6.0","protocols":["5.0","6.0"],"platforms":[{"os":"linux","arch":"amd64"}]}]}`,
	)

	// nix: the NAR is the row's file, the narinfo is read natively
	fx.nixNar = []byte("nar-bytes")
	fx.nixHash = nix.EncodeBase32(bytes.Repeat([]byte{nixHashOf}, 20))

	sum := sha256Of(fx.nixNar)
	fx.nixInfo = "StorePath: /nix/store/" + fx.nixHash + "-hello-2.12\n" +
		"URL: nar/" + sum.Base32() + ".nar\nCompression: none\n" +
		"FileHash: " + sum.String() + "\nFileSize: " + strconv.Itoa(len(fx.nixNar)) + "\n" +
		"NarHash: " + sum.String() + "\nNarSize: " + strconv.Itoa(len(fx.nixNar)) + "\n" +
		"References: \nSig: cache.example-1:c2ln\n"

	s.add("cache", "nix", "hosted", oaSrcRow{
		name: "hello-2.12", version: fx.nixHash, filename: fx.nixHash + ".narinfo", data: fx.nixNar,
	})
	s.native["cache/"+fx.nixHash+".narinfo"] = []byte(fx.nixInfo)

	// deb: the placement comes from the Packages index of the distribution
	fx.debPath = "pool/main/h/hello/hello_1.0_amd64.deb"
	s.add("apt", "deb", "hosted", oaSrcRow{
		name: "hello", version: "1.0", arch: "amd64", filename: "hello_1.0_amd64.deb", data: []byte("deb-bytes"),
	})
	s.native["apt/dists/jammy/Release"] = []byte("Origin: x\nArchitectures: amd64\nComponents: universe\n")
	s.native["apt/dists/jammy/universe/binary-amd64/Packages"] = []byte("Package: hello\nFilename: " + fx.debPath + "\n\n")

	// repositories without content of their own
	s.add("upstream", "raw", "proxy")
	s.add("all-raw", "raw", "group")

	return fx
}

func sha256Of(b []byte) nix.SHA256 { return sha256.Sum256(b) }

func parseOACfg(t *testing.T, fx *oaFixture, extra ...string) importConfig {
	t.Helper()

	args := append([]string{"openartifact", "--source-url", fx.src.srv.URL, "--retries", "0"}, extra...)

	cfg, err := parseImportFlags(args, func(k string) string {
		if k == envSourceToken {
			return oaSrcToken
		}

		return ""
	}, strings.NewReader(""))
	if err != nil {
		t.Fatalf("parseImportFlags(%v): %v", args, err)
	}

	return cfg
}

func runOAImport(t *testing.T, cfg importConfig, fx *oaFixture, oa *fakeOA) *importReport {
	t.Helper()

	return runImportTest(t, cfg, fx.src.srv, oa)
}

func TestOpenArtifactSourceImportsEveryHostedRepository(t *testing.T) {
	fx := newOAFixture(t)
	oa := newFakeOA(t, map[string]*fakeRepo{})

	cfg := parseOACfg(t, fx, "--all", "--create-repos", "--deb-distribution", "jammy,noble")

	rep := runOAImport(t, cfg, fx, oa)
	if rep.failed() {
		t.Fatalf("import failed: %+v", rep.Failures)
	}

	// proxy and group repositories are not content: they are not created either
	for _, name := range []string{"upstream", "all-raw"} {
		if oa.repos[name] != nil {
			t.Errorf("repository %q was created by the content import", name)
		}
	}

	for name, format := range map[string]string{"files": "raw", "libs": "conan", "alpine": "apk", "tf": "terraform", "cache": "nix", "apt": "deb"} {
		if r := oa.repos[name]; r == nil || r.format != format {
			t.Errorf("repository %q = %+v, want a hosted %s repository", name, r, format)
		}
	}

	// raw: bytes, upload time and properties
	a := oa.find("files", "docs/a.txt")
	if a == nil || a.sha != sum256([]byte("alpha")) {
		t.Fatalf("docs/a.txt = %+v", a)
	}

	if a.created != oaSrcStamp {
		t.Errorf("docs/a.txt uploaded with created-at %q, want %q", a.created, oaSrcStamp)
	}

	if a.props["team"] != "docs" || a.props["build.number"] != "7" {
		t.Errorf("docs/a.txt properties = %v, want team and build.number copied", a.props)
	}

	if b := oa.find("files", "b.bin"); b == nil || b.created != "2023-07-15T08:30:00Z" {
		t.Errorf("b.bin = %+v, want its own created-at", b)
	}

	// conan: every file of the recipe and the package revision landed
	cf := conanOf(t, oa)
	base := "/v2/conans/zlib/1.3/_/_/revisions/rrev1"

	for _, want := range []string{
		base + "/files/conanfile.py", base + "/files/conanmanifest.txt",
		base + "/packages/pid1/revisions/prev1/files/conan_package.tgz",
		base + "/packages/pid1/revisions/prev1/files/conaninfo.txt",
		base + "/packages/pid1/revisions/prev1/files/conanmanifest.txt",
	} {
		st := cf.files[want]
		if st == nil {
			t.Errorf("conan file %s not uploaded; have %v", want, cf.order)
			continue
		}

		if want == base+"/files/conanmanifest.txt" && st.created != oaSrcStamp {
			t.Errorf("recipe manifest created-at = %q, want %q", st.created, oaSrcStamp)
		}

		if strings.Contains(want, "/packages/") && st.created != "2024-03-02T11:00:00Z" {
			t.Errorf("package file %s created-at = %q, want the package revision's time", want, st.created)
		}
	}

	if got := string(cf.files[base+"/packages/pid1/revisions/prev1/files/conan_package.tgz"].data); got != "PKG!" {
		t.Errorf("conan_package.tgz = %q", got)
	}

	// apk: at its own path
	if it := oa.find("alpine", "v3.20/main/x86_64/hello-1.0-r0.apk"); it == nil || it.sha != sum256([]byte("apk-bytes")) {
		t.Errorf("apk package = %+v", it)
	}

	// terraform: module and provider, with the provider's protocols
	if it := oa.find("tf", "/_api/modules/vpc/aws/1.0.0"); it == nil || it.sha != sum256([]byte("module-archive")) {
		t.Errorf("terraform module = %+v", it)
	}

	if it := oa.find("tf", "/_api/providers/random/3.6.0/linux/amd64"); it == nil || it.props["protocols"] != "5.0,6.0" {
		t.Errorf("terraform provider = %+v, want protocols 5.0,6.0", it)
	}

	// nix: the NAR first, then the narinfo as the source served it
	it := oa.find("cache", fx.nixHash+".narinfo")
	if it == nil || it.sha != sum256(fx.nixNar) || it.props["narinfo"] != fx.nixInfo {
		t.Errorf("nix path = %+v", it)
	}

	// deb: placed where the source's Packages index lists it
	d := oa.find("apt", "hello_1.0_amd64.deb")
	if d == nil || d.dist != "jammy" || d.comp != "universe" {
		t.Errorf("deb = %+v, want distribution jammy, component universe", d)
	}

	if len(rep.Repositories) != 6 {
		t.Errorf("report has %d repositories, want 6: %+v", len(rep.Repositories), rep.Repositories)
	}
}

func TestOpenArtifactSourceRerunIsIdempotent(t *testing.T) {
	fx := newOAFixture(t)
	oa := newFakeOA(t, map[string]*fakeRepo{})

	state := filepath.Join(t.TempDir(), "state.json")
	args := []string{"--all", "--create-repos", "--formats", "raw,apk,terraform,nix,deb", "--deb-distribution", "jammy"}
	first := runOAImport(t, parseOACfg(t, fx, append(args, "--state-file", state)...), fx, oa)

	if first.failed() || first.Uploaded == 0 {
		t.Fatalf("first run: %+v", first)
	}

	// with the state file the assets are skipped without asking the destination
	resumed := runOAImport(t, parseOACfg(t, fx, append(args, "--state-file", state)...), fx, oa)
	if resumed.failed() || resumed.Uploaded != 0 || resumed.Resumed != first.Uploaded {
		t.Errorf(
			"resumed run: uploaded %d, resumed %d; want 0 and %d (%+v)",
			resumed.Uploaded,
			resumed.Resumed,
			first.Uploaded,
			resumed.Failures,
		)
	}

	// without it the destination has them by digest: nothing is uploaded again
	rerun := runOAImport(t, parseOACfg(t, fx, args...), fx, oa)
	if rerun.failed() || rerun.Uploaded != 0 || rerun.Present != first.Uploaded {
		t.Errorf(
			"rerun: uploaded %d, already present %d; want 0 and %d (%+v)",
			rerun.Uploaded,
			rerun.Present,
			first.Uploaded,
			rerun.Failures,
		)
	}
}

func TestOpenArtifactSourceDryRunCountsPerRepositoryAndWritesNothing(t *testing.T) {
	fx := newOAFixture(t)
	oa := newFakeOA(t, map[string]*fakeRepo{})

	rep := runOAImport(t, parseOACfg(t, fx, "--all", "--dry-run", "--create-repos", "--deb-distribution", "jammy"), fx, oa)
	if rep.failed() {
		t.Fatalf("dry run failed: %+v", rep.Failures)
	}

	if oa.writes != 0 || len(oa.repos) != 0 {
		t.Errorf("dry run wrote %d times and created %d repositories", oa.writes, len(oa.repos))
	}

	// nothing is downloaded either: the package contents are never read
	for k := range fx.src.hits {
		if strings.HasPrefix(k, "download ") {
			t.Errorf("dry run downloaded %s", k)
		}
	}

	got := map[string]repoReport{}
	for _, r := range rep.Repositories {
		got[r.Source] = r
	}

	for name, want := range map[string]struct {
		format   importFormat
		assets   int
		uploaded int
	}{
		"files": {formatRaw, 2, 2}, "libs": {formatConan, 5, 5}, "alpine": {formatAPK, 1, 1},
		"tf": {formatTerraform, 2, 2}, "cache": {formatNix, 1, 1}, "apt": {formatDeb, 1, 1},
	} {
		r, ok := got[name]
		if !ok || r.Format != want.format || r.Assets != want.assets || r.Uploaded != want.uploaded {
			t.Errorf("dry run %s = %+v, want %s with %d assets, %d to upload", name, r, want.format, want.assets, want.uploaded)
		}
	}

	if rep.Uploaded != 12 {
		t.Errorf("dry run would upload %d, want 12", rep.Uploaded)
	}

	var out bytes.Buffer

	rep.print(&out)

	for _, want := range []string{"would upload", "files", "libs", "alpine", "(dry run, nothing written)"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("summary lacks %q:\n%s", want, out.String())
		}
	}
}

func TestOpenArtifactSourceRepoMappingAndFormatFilter(t *testing.T) {
	fx := newOAFixture(t)
	oa := newFakeOA(t, map[string]*fakeRepo{})

	rep := runOAImport(t, parseOACfg(t, fx, "--repo", "files:docs", "--create-repos"), fx, oa)
	if rep.failed() || oa.find("docs", "docs/a.txt") == nil {
		t.Fatalf("--repo files:docs: %+v, repos %v", rep.Failures, oa.repos)
	}

	// --formats limits --all
	oa = newFakeOA(t, map[string]*fakeRepo{})

	rep = runOAImport(t, parseOACfg(t, fx, "--all", "--formats", "apk", "--create-repos"), fx, oa)
	if rep.failed() || len(oa.repos) != 1 || oa.repos["alpine"] == nil {
		t.Errorf("--formats apk created %v (failures %+v)", oa.repos, rep.Failures)
	}
}

func TestOpenArtifactSourceRefusesProxyAndGroup(t *testing.T) {
	fx := newOAFixture(t)
	oa := newFakeOA(t, map[string]*fakeRepo{})

	for _, name := range []string{"upstream", "all-raw"} {
		rep := runOAImport(t, parseOACfg(t, fx, "--repo", name, "--create-repos"), fx, oa)
		if !rep.failed() || !strings.Contains(rep.Failures[0].Error, "only hosted") {
			t.Errorf("%s: failures = %+v, want a refusal of the non-hosted repository", name, rep.Failures)
		}
	}

	rep := runOAImport(t, parseOACfg(t, fx, "--repo", "missing"), fx, oa)
	if !rep.failed() {
		t.Error("an unknown source repository was not reported")
	}
}

func TestOpenArtifactSourceRejectsAWrongToken(t *testing.T) {
	fx := newOAFixture(t)
	cfg := parseOACfg(t, fx, "--repo", "files")
	cfg.SourceToken = "nope"

	src := newImportSource(cfg, fx.src.srv.Client())
	if _, err := src.Repository(context.Background(), "files"); err == nil {
		t.Error("a wrong source token was accepted")
	}
}

func TestOpenArtifactAssetPaths(t *testing.T) {
	s := &openartifactSource{http: newSourceHTTP(&url.URL{Scheme: "http", Host: "src"}, "", "", nil)}

	cases := []struct {
		repo sourceRepo
		row  oaPackage
		want string
	}{
		{sourceRepo{Name: "r", Format: formatNPM}, oaPackage{Name: "@acme/lib", Filename: "lib-1.0.0.tgz"}, "@acme/lib/-/lib-1.0.0.tgz"},
		{
			sourceRepo{Name: "r", Format: formatGo},
			oaPackage{Name: "example.com/m", Version: "v1.2.3", Filename: "v1.2.3.zip"},
			"example.com/m/@v/v1.2.3.zip",
		},
		{
			sourceRepo{Name: "r", Format: formatCargo},
			oaPackage{Name: "serde", Filename: "serde-1.0.0.crate"},
			"crates/serde/serde-1.0.0.crate",
		},
		{
			sourceRepo{Name: "r", Format: formatConda},
			oaPackage{Arch: "linux-64", Filename: "numpy-1.0-0.conda"},
			"linux-64/numpy-1.0-0.conda",
		},
		{
			sourceRepo{Name: "r", Format: formatIvy},
			oaPackage{Name: "org.acme:lib", Version: "1.0", Filename: "lib-1.0.jar.jar"},
			"org.acme/lib/1.0/lib-1.0.jar",
		},
		{
			sourceRepo{Name: "r", Format: formatIvy},
			oaPackage{Name: "org.acme:lib", Version: "1.0", Filename: "lib-1.0.jar"},
			"org.acme/lib/1.0/lib-1.0.jar",
		},
		{
			sourceRepo{Name: "r", Format: formatIvy},
			oaPackage{Name: "org.acme:lib", Version: "1.0", Filename: "lib-1.0.pom"},
			"org.acme/lib/1.0/lib-1.0.pom",
		},
		{
			sourceRepo{Name: "r", Format: formatSBT},
			oaPackage{Name: "org.acme:lib_2.13", Version: "1.0", Filename: "lib_2.13-1.0.jar"},
			"org.acme/lib_2.13/1.0/lib_2.13-1.0.jar",
		},
		{
			sourceRepo{Name: "r", Format: formatAPK},
			oaPackage{Name: "v3.20/main/hello", Arch: "aarch64", Filename: "hello-1.0.apk"},
			"v3.20/main/aarch64/hello-1.0.apk",
		},
		{sourceRepo{Name: "r", Format: formatTerraform}, oaPackage{Name: "modules/vpc/aws", Version: "1.0.0"}, "modules/vpc/aws/1.0.0"},
		{
			sourceRepo{Name: "r", Format: formatTerraform}, oaPackage{Name: "providers/random", Version: "3.6.0", Arch: "darwin_arm64"},
			"providers/random/3.6.0/darwin/arm64",
		},
		{sourceRepo{Name: "r", Format: formatDocker}, oaPackage{Name: "r/app", Version: "1.0"}, "v2/app/manifests/1.0"},
		{sourceRepo{Name: "r", Format: formatDocker}, oaPackage{Name: "r", Version: "latest"}, "v2/manifests/latest"},
		{sourceRepo{Name: "r", Format: formatMaven}, oaPackage{Filename: "com/acme/lib/1.0/lib-1.0.jar"}, "com/acme/lib/1.0/lib-1.0.jar"},
		{sourceRepo{Name: "r", Format: formatHelm}, oaPackage{Filename: "chart-1.0.0.tgz"}, "chart-1.0.0.tgz"},
		{sourceRepo{Name: "r", Format: formatNuGet}, oaPackage{Filename: "Acme.Lib.1.0.0.nupkg"}, "Acme.Lib.1.0.0.nupkg"},
	}

	for _, c := range cases {
		a := s.assetOf(c.repo, c.row)
		if a.Path != c.want {
			t.Errorf("%s %+v: path = %q, want %q", c.repo.Format, c.row, a.Path, c.want)
		}

		if !importable(c.repo.Format, a.Path) {
			t.Errorf("%s: %q is not importable", c.repo.Format, a.Path)
		}
	}

	// a composer dist and a nix path are importable too
	if !importable(formatComposer, "acme-lib-1.0.0.zip") || !importable(formatNix, "abc.narinfo") {
		t.Error("composer or nix paths are not importable")
	}

	if got := s.assetOf(
		sourceRepo{Name: "r", Format: formatComposer},
		oaPackage{Name: "acme/lib", Version: "1.0.0"},
	).Component.Version; got != "1.0.0" {
		t.Errorf("composer component version = %q, want 1.0.0", got)
	}
}

func TestOpenArtifactSourceReadsCargoYankedFromTheSparseIndex(t *testing.T) {
	fx := newOAFixture(t)
	base, _ := url.Parse(fx.src.srv.URL)
	src := &openartifactSource{http: newSourceHTTP(base, "", oaSrcToken, fx.src.srv.Client())}

	sample := sourceAsset{Repo: "crates", Path: "crates/serde/serde-1.0.0.crate"}
	index := `{"name":"serde","vers":"1.0.0","cksum":"aa","yanked":true}` + "\n"
	fx.src.native["crates/index/se/rd/serde"] = []byte(index)

	lines, name, err := cargoReadIndex(context.Background(), src, sample)
	if err != nil || name != "index/se/rd/serde" || len(lines) != 1 || !lines[0].Yanked {
		t.Errorf("cargoReadIndex = %+v, %q, %v; want the yanked line of the sparse index", lines, name, err)
	}

	sample.Path = "crates/missing/missing-1.0.0.crate"
	if lines, _, err := cargoReadIndex(context.Background(), src, sample); err != nil || len(lines) != 0 {
		t.Errorf("cargoReadIndex of an absent crate = %+v, %v", lines, err)
	}
}

func TestParseImportFlagsOpenArtifact(t *testing.T) {
	get := func(string) string { return "" }

	parse := func(args ...string) (importConfig, error) {
		return parseImportFlags(
			append([]string{"openartifact", "--source-url", "http://localhost:8080"}, args...),
			get,
			strings.NewReader(""),
		)
	}

	cfg, err := parse("--all", "--formats", "raw,apk")
	if err != nil {
		t.Fatal(err)
	}

	if !cfg.AllRepos || len(cfg.Formats) != 2 || cfg.Formats[1] != "apk" {
		t.Errorf("--all --formats: %+v", cfg)
	}

	// an OpenArtifact source copies all properties unless told otherwise
	if !propertyMatches(cfg.Properties, "anything.at.all") {
		t.Errorf("properties %v do not match every key", cfg.Properties)
	}

	cfg, err = parse("--repo", "x", "--properties", "team")
	if err != nil || propertyMatches(cfg.Properties, "other") || !propertyMatches(cfg.Properties, "team") {
		t.Errorf("--properties team: %+v, %v", cfg.Properties, err)
	}

	if cfg, err = parse(
		"--repo",
		"x",
		"--deb-distribution",
		"jammy,noble",
	); err != nil || cfg.DebDist != "jammy" ||
		len(cfg.DebDists) != 2 {
		t.Errorf("--deb-distribution jammy,noble: %q %v %v", cfg.DebDist, cfg.DebDists, err)
	}

	if cfg, _ = parse("--repo", "x"); len(cfg.DebDists) != 1 || cfg.DebDists[0] != "stable" {
		t.Errorf("default deb distributions = %v, want [stable]", cfg.DebDists)
	}

	for _, args := range [][]string{{"--all", "--repo", "x"}, {"--formats", "raw", "--repo", "x"}, {}} {
		if _, err := parse(args...); !errors.Is(err, errImportUsage) {
			t.Errorf("%v: err = %v, want a usage error", args, err)
		}
	}

	if _, err := parseImportFlags(
		[]string{"nexus", "--source-url", "http://localhost:8080", "--all"},
		get,
		strings.NewReader(""),
	); !errors.Is(
		err,
		errImportUsage,
	) {
		t.Errorf("--all with a nexus source: err = %v, want a usage error", err)
	}
}
