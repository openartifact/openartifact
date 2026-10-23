// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"cmp"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func sum256(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func sum1(b []byte) string {
	h := sha1.Sum(b)
	return hex.EncodeToString(h[:])
}

// ─── fake OpenArtifact ────────────────────────────────────────────────────────

type fakeItem struct {
	id      string
	path    string // maven/raw path, deb/rpm file name
	name    string // listed package name (formats registering their own routes)
	version string // listed package version
	sha     string
	size    int
	created string
	dist    string
	comp    string
	props   map[string]string
}

type fakeRepo struct {
	format   string
	repoType string
	items    []*fakeItem
}

// fakeOARoutes lets a format's test file add the routes of its native upload
// protocol to every fakeOA, keyed by the repository format. The routes are
// relative to the repository: "/repos/<repo>/..." is registered as "/..." and
// docker's "/v2/<repo>/..." as "/v2/...". Register from an init function.
var fakeOARoutes = map[string]func(f *fakeOA, mux *http.ServeMux){} //nolint:gochecknoglobals // routes of each format register themselves from init functions

// formatMux picks the per-format mux for /repos/<repo>/... and /v2/<repo>/...
// requests and strips the repository from the path (stashing it in the
// "X-Fake-Repo" header). Everything else is nil.
func (f *fakeOA) formatMux(byFormat map[string]*http.ServeMux, r *http.Request) *http.ServeMux {
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 3)
	if len(parts) < 2 || (parts[0] != "repos" && parts[0] != "v2") {
		return nil
	}

	repo := f.repos[parts[1]]
	if repo == nil {
		return nil
	}

	m := byFormat[repo.format]
	if m == nil {
		return nil
	}

	rest := "/"
	if len(parts) == 3 {
		rest += parts[2]
	}

	if parts[0] == "v2" {
		rest = "/v2" + rest
	}

	r.Header.Set("X-Fake-Repo", parts[1])
	r.URL.Path = rest
	r.URL.RawPath = ""

	return m
}

// fakeOA mimics the slice of the OpenArtifact API the importer uses.
type fakeOA struct {
	mu      sync.Mutex
	token   string
	repos   map[string]*fakeRepo
	seq     int
	writes  int // mutating requests received
	deletes int
	lieSHA  string // when set, uploads report this digest instead of the real one
	srv     *httptest.Server
}

func newFakeOA(t *testing.T, repos map[string]*fakeRepo) *fakeOA {
	t.Helper()

	f := &fakeOA{token: "admin-token", repos: repos}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/repository/{name}", f.getRepo)
	mux.HandleFunc("POST /api/v1/repositories", f.createRepo)
	mux.HandleFunc("GET /api/v1/repositories/{name}/packages", f.listPackages)
	mux.HandleFunc("POST /api/v1/repositories/{name}/debs", f.postPackage)
	mux.HandleFunc("POST /api/v1/repositories/{name}/packages", f.postPackage)
	mux.HandleFunc("DELETE /api/v1/repositories/{name}/debs/{id}", f.deleteByID)
	mux.HandleFunc("DELETE /api/v1/repositories/{name}/packages/{id}", f.deleteByID)
	mux.HandleFunc("PATCH /api/v1/repositories/{name}/artifacts/{id}/properties", f.patchProps)
	mux.HandleFunc("PUT /repos/{format}/{name}/{path...}", f.putFile)
	mux.HandleFunc("GET /repos/maven/{name}/{path...}", f.getMavenChecksum)
	mux.HandleFunc("DELETE /repos/{format}/{name}/{path...}", f.deleteFile)

	// Formats with their own upload protocol add their routes from their
	// test files (see fakeOARoutes); the request is routed by the format of the
	// repository it names, as the real server does.
	byFormat := map[string]*http.ServeMux{}

	for format, register := range fakeOARoutes {
		m := http.NewServeMux()
		register(f, m)
		byFormat[format] = m
	}

	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+f.token {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}

		f.mu.Lock()
		defer f.mu.Unlock()

		if m := f.formatMux(byFormat, r); m != nil {
			m.ServeHTTP(w, r)
			return
		}

		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(f.srv.Close)

	return f
}

func (f *fakeOA) client() *Client {
	return &Client{baseURL: f.srv.URL, token: f.token, httpClient: f.srv.Client()}
}

func (f *fakeOA) nextID() string {
	f.seq++
	return "id-" + strconv.Itoa(f.seq)
}

func (f *fakeOA) find(repo, path string) *fakeItem {
	if r := f.repos[repo]; r != nil {
		for _, it := range r.items {
			if it.path == path {
				return it
			}
		}
	}

	return nil
}

func (f *fakeOA) reply(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (f *fakeOA) getRepo(w http.ResponseWriter, r *http.Request) {
	repo := f.repos[r.PathValue("name")]
	if repo == nil {
		f.reply(w, http.StatusNotFound, map[string]string{"error": "repository not found"})
		return
	}

	f.reply(w, http.StatusOK, map[string]string{"name": r.PathValue("name"), "format": repo.format, "repo_type": repo.repoType})
}

func (f *fakeOA) createRepo(w http.ResponseWriter, r *http.Request) {
	f.writes++

	var body struct {
		Name     string `json:"name"`
		Format   string `json:"format"`
		RepoType string `json:"repo_type"`
	}

	_ = json.NewDecoder(r.Body).Decode(&body)
	f.repos[body.Name] = &fakeRepo{format: body.Format, repoType: body.RepoType}
	f.reply(w, http.StatusCreated, map[string]string{"name": body.Name})
}

func (f *fakeOA) listPackages(w http.ResponseWriter, r *http.Request) {
	repo := f.repos[r.PathValue("name")]
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	q := strings.ToLower(r.URL.Query().Get("q"))

	var matching []*fakeItem

	for _, it := range repo.items {
		if q == "" || strings.Contains(strings.ToLower(it.name), q) || strings.Contains(strings.ToLower(it.version), q) {
			matching = append(matching, it)
		}
	}

	var out []map[string]string

	for i, it := range matching {
		if i >= (page-1)*limit && i < page*limit {
			out = append(out, map[string]string{
				"id": it.id, "sha256": it.sha, "filename": it.path, "name": it.name, "version": it.version,
			})
		}
	}

	f.reply(w, http.StatusOK, map[string]any{"total": len(matching), "packages": out})
}

func (f *fakeOA) store(repoName, path string, data []byte, created string) *fakeItem {
	repo := f.repos[repoName]
	it := &fakeItem{id: f.nextID(), path: path, sha: sum256(data), size: len(data), created: created}

	for i, old := range repo.items {
		if old.path == path {
			it.id = old.id
			repo.items[i] = it

			return it
		}
	}

	repo.items = append(repo.items, it)

	return it
}

func (f *fakeOA) reported(it *fakeItem) string {
	if f.lieSHA != "" {
		return f.lieSHA
	}

	return it.sha
}

func (f *fakeOA) putFile(w http.ResponseWriter, r *http.Request) {
	f.writes++

	name := r.PathValue("name")
	if f.repos[name] == nil {
		f.reply(w, http.StatusNotFound, map[string]string{"error": "no such repository"})
		return
	}

	// An upload the client aborted (source cut off mid-stream) must not be
	// stored, as the real server rejects it; otherwise this late handler can
	// overwrite the retry's complete copy.
	data, err := io.ReadAll(r.Body)
	if err != nil {
		f.reply(w, http.StatusBadRequest, map[string]string{"error": "incomplete body"})
		return
	}

	it := f.store(name, r.PathValue("path"), data, r.Header.Get(headerImportCreatedAt))

	if r.PathValue("format") == "maven" {
		w.Header().Set(headerArtifactID, it.id)
		w.WriteHeader(http.StatusCreated)

		return
	}

	f.reply(w, http.StatusCreated, map[string]any{"id": it.id, "path": it.path, "sha256": f.reported(it), "size": it.size})
}

func (f *fakeOA) getMavenChecksum(w http.ResponseWriter, r *http.Request) {
	path, ok := strings.CutSuffix(r.PathValue("path"), ".sha256")
	it := f.find(r.PathValue("name"), path)

	if !ok || it == nil {
		http.NotFound(w, r)
		return
	}

	_, _ = fmt.Fprintf(w, "%s\n", f.reported(it))
}

func (f *fakeOA) postPackage(w http.ResponseWriter, r *http.Request) {
	f.writes++

	file, hdr, err := r.FormFile("file")
	if err != nil {
		f.reply(w, http.StatusBadRequest, map[string]string{"error": "file required"})
		return
	}

	data, _ := io.ReadAll(file)
	name := r.PathValue("name")

	for _, it := range f.repos[name].items {
		if it.sha == sum256(data) {
			f.reply(w, http.StatusConflict, map[string]any{"success": false, "error": "artifact already exists", "data": map[string]bool{"duplicate": true}})
			return
		}
	}

	it := f.store(name, hdr.Filename, data, r.Header.Get(headerImportCreatedAt))
	it.dist, it.comp = r.FormValue("distribution"), r.FormValue("component")
	f.reply(w, http.StatusOK, map[string]any{"success": true, "data": map[string]any{"id": it.id, "sha256": f.reported(it)}})
}

func (f *fakeOA) deleteByID(w http.ResponseWriter, r *http.Request) {
	f.writes++
	f.deletes++

	repo := f.repos[r.PathValue("name")]
	for i, it := range repo.items {
		if it.id == r.PathValue("id") {
			repo.items = append(repo.items[:i], repo.items[i+1:]...)
			break
		}
	}

	w.WriteHeader(http.StatusNoContent)
}

func (f *fakeOA) deleteFile(w http.ResponseWriter, r *http.Request) {
	f.writes++
	f.deletes++

	repo := f.repos[r.PathValue("name")]
	for i, it := range repo.items {
		if it.path == r.PathValue("path") {
			repo.items = append(repo.items[:i], repo.items[i+1:]...)
			break
		}
	}

	w.WriteHeader(http.StatusNoContent)
}

func (f *fakeOA) patchProps(w http.ResponseWriter, r *http.Request) {
	f.writes++

	var props map[string]string
	_ = json.NewDecoder(r.Body).Decode(&props)

	for _, it := range f.repos[r.PathValue("name")].items {
		if it.id == r.PathValue("id") {
			if it.props == nil {
				it.props = map[string]string{}
			}

			maps.Copy(it.props, props)
		}
	}

	f.reply(w, http.StatusOK, props)
}

// ─── fake sources ─────────────────────────────────────────────────────────────

type fakeFile struct {
	path    string
	data    []byte
	created string
	props   map[string][]string
	// sha256 overrides the advertised digest (to simulate corruption).
	sha256 string
	// compName/compVersion/compGroup make Nexus list the file as (part of) a
	// component through /service/rest/v1/components; empty: no component.
	compGroup, compName, compVersion string
	// contentType is the asset content type Nexus reports (default octet-stream).
	contentType string
}

func (f fakeFile) advertised() string {
	if f.sha256 != "" {
		return f.sha256
	}

	return sum256(f.data)
}

type fakeSourceRepo struct {
	name, format, kind string
	files              []fakeFile
}

// newFakeNexus serves a Nexus 3 REST API with 2-asset pages.
func newFakeNexus(t *testing.T, repos ...fakeSourceRepo) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()
	byName := map[string]fakeSourceRepo{}

	for _, r := range repos {
		byName[r.name] = r
	}

	mux.HandleFunc("GET /service/rest/v1/repositories", func(w http.ResponseWriter, _ *http.Request) {
		var out []map[string]string
		for _, r := range repos {
			out = append(out, map[string]string{"name": r.name, "format": r.format, "type": r.kind})
		}

		_ = json.NewEncoder(w).Encode(out)
	})

	mux.HandleFunc("GET /service/rest/v1/assets", func(w http.ResponseWriter, r *http.Request) {
		repo := byName[r.URL.Query().Get("repository")]
		start, _ := strconv.Atoi(r.URL.Query().Get("continuationToken"))
		end := min(start+2, len(repo.files))

		var items []map[string]any

		for _, f := range repo.files[start:end] {
			items = append(items, map[string]any{
				"downloadUrl": "http://" + r.Host + "/repository/" + repo.name + "/" + f.path,
				"path":        "/" + f.path,
				"checksum":    map[string]string{"sha1": sum1(f.data), "sha256": f.advertised()},
				"fileSize":    len(f.data),
				"blobCreated": f.created,
				"contentType": cmp.Or(f.contentType, "application/octet-stream"),
			})
		}

		page := map[string]any{"items": items, "continuationToken": nil}
		if end < len(repo.files) {
			page["continuationToken"] = strconv.Itoa(end)
		}

		_ = json.NewEncoder(w).Encode(page)
	})

	// Components: one component per file that names one, two per page.
	mux.HandleFunc("GET /service/rest/v1/components", func(w http.ResponseWriter, r *http.Request) {
		repo := byName[r.URL.Query().Get("repository")]

		var comps []fakeFile

		for _, f := range repo.files {
			if f.compName != "" {
				comps = append(comps, f)
			}
		}

		start, _ := strconv.Atoi(r.URL.Query().Get("continuationToken"))
		end := min(start+2, len(comps))

		var items []map[string]any

		for _, f := range comps[start:end] {
			items = append(items, map[string]any{
				"repository": repo.name, "format": repo.format,
				"group": f.compGroup, "name": f.compName, "version": f.compVersion,
				"assets": []map[string]any{{
					"downloadUrl": "http://" + r.Host + "/repository/" + repo.name + "/" + f.path,
					"path":        f.path,
					"checksum":    map[string]string{"sha1": sum1(f.data), "sha256": f.advertised()},
					"fileSize":    len(f.data),
				}},
			})
		}

		page := map[string]any{"items": items, "continuationToken": nil}
		if end < len(comps) {
			page["continuationToken"] = strconv.Itoa(end)
		}

		_ = json.NewEncoder(w).Encode(page)
	})

	mux.HandleFunc("GET /repository/{repo}/{path...}", func(w http.ResponseWriter, r *http.Request) {
		for _, f := range byName[r.PathValue("repo")].files {
			if f.path == r.PathValue("path") {
				_, _ = w.Write(f.data)
				return
			}
		}

		http.NotFound(w, r)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return srv
}

// newFakeArtifactory serves the Artifactory repositories, AQL, storage and
// download endpoints, requiring Basic auth user:secret.
func newFakeArtifactory(t *testing.T, repos ...fakeSourceRepo) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()
	byName := map[string]fakeSourceRepo{}

	for _, r := range repos {
		byName[r.name] = r
	}

	mux.HandleFunc("GET /api/repositories", func(w http.ResponseWriter, _ *http.Request) {
		var out []map[string]string
		for _, r := range repos {
			out = append(out, map[string]string{"key": r.name, "packageType": r.format, "type": r.kind})
		}

		_ = json.NewEncoder(w).Encode(out)
	})

	mux.HandleFunc("POST /api/search/aql", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		q := string(body)

		var results []map[string]any

		for _, repo := range repos {
			if !strings.Contains(q, `"repo":"`+repo.name+`"`) || strings.Contains(q, ".offset(1000)") {
				continue
			}

			for _, f := range repo.files {
				dir, name := splitPath(f.path)

				var props []map[string]string

				for k, vs := range f.props {
					for _, v := range vs {
						props = append(props, map[string]string{"key": k, "value": v})
					}
				}

				results = append(results, map[string]any{
					"repo": repo.name, "path": dir, "name": name, "size": len(f.data), "created": f.created,
					"sha256": f.advertised(), "actual_sha1": sum1(f.data), "properties": props,
				})
			}
		}

		_ = json.NewEncoder(w).Encode(map[string]any{"results": results})
	})

	mux.HandleFunc("GET /api/storage/{repo}/{path...}", func(w http.ResponseWriter, r *http.Request) {
		storageHandler(w, r, byName)
	})
	mux.HandleFunc("GET /api/storage/{repo}", func(w http.ResponseWriter, r *http.Request) {
		storageHandler(w, r, byName)
	})

	mux.HandleFunc("GET /{repo}/{path...}", func(w http.ResponseWriter, r *http.Request) {
		for _, f := range byName[r.PathValue("repo")].files {
			if f.path == r.PathValue("path") {
				_, _ = w.Write(f.data)
				return
			}
		}

		http.NotFound(w, r)
	})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != "svc" || p != "secret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	return srv
}

func splitPath(p string) (string, string) {
	dir, name, ok := strings.CutLast(p, "/")
	if !ok {
		return ".", p
	}

	return dir, name
}

func storageHandler(w http.ResponseWriter, r *http.Request, byName map[string]fakeSourceRepo) {
	repo := byName[r.PathValue("repo")]

	if _, ok := r.URL.Query()["list"]; ok {
		var files []map[string]any
		for _, f := range repo.files {
			files = append(files, map[string]any{
				"uri": "/" + f.path, "size": len(f.data), "folder": false,
				"lastModified": f.created, "sha1": sum1(f.data), "sha2": f.advertised(),
			})
		}

		_ = json.NewEncoder(w).Encode(map[string]any{"files": files})

		return
	}

	for _, f := range repo.files {
		if f.path != r.PathValue("path") {
			continue
		}

		if _, ok := r.URL.Query()["properties"]; ok {
			if len(f.props) == 0 {
				http.NotFound(w, r)
				return
			}

			_ = json.NewEncoder(w).Encode(map[string]any{"properties": f.props})

			return
		}

		_ = json.NewEncoder(w).Encode(map[string]any{
			"created":   f.created,
			"checksums": map[string]string{"sha1": sum1(f.data), "sha256": f.advertised()},
			"mimeType":  "application/octet-stream",
		})

		return
	}

	http.NotFound(w, r)
}
