// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package conan

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/clientsetup"
	"github.com/openartifact/openartifact/internal/database/models"
)

// Conan 1 protocol.
//
// A Conan 1 client (revisions disabled, its default) speaks `v1/...`: it asks
// for a map of file name to URL (`upload_urls`, `download_urls`, `digest`) and
// then transfers each file with a plain GET or PUT on that URL. The files are
// the same stored files as Conan 2 uses, so both clients share a repository:
//
//   - a v1 download serves the newest published revision, as conan_server does;
//   - a v1 upload is stored under revision "0" (v1Revision), like
//     conan_server's revision-less mode, and a package uploaded without a
//     revision goes to the newest published recipe revision;
//   - a Conan 1 client with revisions enabled uses the v2 routes.
const v1Revision = "0"

// maxV1RequestBytes bounds the JSON body of upload_urls, remove_files and
// packages/delete.
const maxV1RequestBytes = 1 << 20

// registerV1Routes adds the Conan 1 routes.
func (h *HTTPHandler) registerV1Routes(r chi.Router) {
	r.Get("/v1/users/authenticate", h.handleAuthenticate)
	r.Get("/v1/users/check_credentials", h.handleCheckCredentials)
	r.Get("/v1/conans/search", h.handleSearch)

	const recipe = "/v1/conans/{pkg}/{version}/{user}/{channel}"

	r.Get(recipe, h.handleV1Snapshot)
	r.Delete(recipe, h.handleDeleteRecipe)
	r.Get(recipe+"/search", h.handleV1SearchPackages)
	r.Get(recipe+"/digest", h.handleV1Digest)
	r.Get(recipe+"/download_urls", h.handleV1DownloadURLs)
	r.Post(recipe+"/upload_urls", h.handleV1UploadURLs)
	r.Post(recipe+"/remove_files", h.handleV1RemoveFiles)
	r.Post(recipe+"/packages/delete", h.handleV1RemovePackages)

	const pkg = recipe + "/packages/{pid}"

	r.Get(pkg, h.handleV1Snapshot)
	r.Get(pkg+"/digest", h.handleV1Digest)
	r.Get(pkg+"/download_urls", h.handleV1DownloadURLs)
	r.Post(pkg+"/upload_urls", h.handleV1UploadURLs)

	const files = "/v1/files/{pkg}/{version}/{user}/{channel}/{rrev}"

	r.Get(files+"/export/*", h.handleDownload)
	r.Head(files+"/export/*", h.handleDownload)
	r.Put(files+"/export/*", h.handleUpload)
	r.Get(files+"/package/{pid}/{prev}/*", h.handleDownload)
	r.Head(files+"/package/{pid}/{prev}/*", h.handleDownload)
	r.Put(files+"/package/{pid}/{prev}/*", h.handleUpload)
}

// v1Target is the published files a Conan 1 request addresses: the newest
// recipe revision, or, for a package, its newest package revision.
type v1Target struct {
	rrev  string
	prev  string
	files []*models.ConanPackage
}

// v1Resolve resolves the revision a Conan 1 request without revisions means and
// loads its files; it answers 404 and returns false when there is none.
func (h *HTTPHandler) v1Resolve(w http.ResponseWriter, r *http.Request, s *scope) (*v1Target, bool) {
	all, ok := h.recipeFiles(w, r, s)
	if !ok {
		return nil, false
	}

	revs := recipeRevisions(all)
	if len(revs) == 0 {
		http.NotFound(w, r)
		return nil, false
	}

	t := &v1Target{rrev: revs[0].ID}

	if s.pid == "" {
		t.files = revisionFiles(all, t.rrev, "", "")
		return t, true
	}

	prevs := packageRevisions(all, t.rrev, s.pid)
	if len(prevs) == 0 {
		http.NotFound(w, r)
		return nil, false
	}

	t.prev = prevs[0].ID
	t.files = revisionFiles(all, t.rrev, s.pid, t.prev)

	return t, true
}

// fileURL is the URL a Conan 1 client transfers one file of the revision from or
// to. It is absolute (the client resolves a relative one against the remote URL,
// which has no trailing slash).
func (h *HTTPHandler) fileURL(s *scope, rrev, pid, prev, filename string) string {
	user, channel, _ := strings.Cut(s.recipe.UserChannel, "/")
	origin := strings.TrimRight(h.BaseURL, "/")

	var b strings.Builder

	b.WriteString(clientsetup.RepoBaseURL("conan", origin, s.repoName))
	b.WriteString("/v1/files/")

	for _, seg := range []string{s.recipe.Name, s.recipe.Version, user, channel, rrev} {
		b.WriteString(url.PathEscape(seg))
		b.WriteByte('/')
	}

	if pid == "" {
		b.WriteString("export/")
	} else {
		b.WriteString("package/" + url.PathEscape(pid) + "/" + url.PathEscape(prev) + "/")
	}

	segs := strings.Split(filename, "/")
	for i, seg := range segs {
		segs[i] = url.PathEscape(seg)
	}

	b.WriteString(strings.Join(segs, "/"))

	return b.String()
}

// handleV1Snapshot answers {file: md5} of the newest revision, which is how a
// Conan 1 client learns what the remote holds (and checks a download cache).
func (h *HTTPHandler) handleV1Snapshot(w http.ResponseWriter, r *http.Request) {
	s, ok := h.scopeOf(w, r)
	if !ok {
		return
	}

	t, ok := h.v1Resolve(w, r, s)
	if !ok {
		return
	}

	if err := h.ensureDigests(r.Context(), t.files); err != nil {
		h.fail(w, "failed to compute conan file digests", err, "path", s.resource())
		return
	}

	snapshot := make(map[string]string, len(t.files))
	for _, f := range t.files {
		snapshot[f.Filename] = f.MD5
	}

	h.writeJSON(w, snapshot)
}

// handleV1DownloadURLs answers {file: url} for every file of the newest
// revision.
func (h *HTTPHandler) handleV1DownloadURLs(w http.ResponseWriter, r *http.Request) {
	h.writeV1URLs(w, r, nil)
}

// handleV1Digest answers {conanmanifest.txt: url}.
func (h *HTTPHandler) handleV1Digest(w http.ResponseWriter, r *http.Request) {
	h.writeV1URLs(w, r, []string{manifestFile})
}

// writeV1URLs answers the URLs of the files of the newest revision, only the
// named ones when names is set; no such file is 404.
func (h *HTTPHandler) writeV1URLs(w http.ResponseWriter, r *http.Request, names []string) {
	s, ok := h.scopeOf(w, r)
	if !ok {
		return
	}

	t, ok := h.v1Resolve(w, r, s)
	if !ok {
		return
	}

	urls := make(map[string]string, len(t.files))

	for _, f := range t.files {
		if names == nil || slices.Contains(names, f.Filename) {
			urls[f.Filename] = h.fileURL(s, t.rrev, s.pid, t.prev, f.Filename)
		}
	}

	if len(urls) == 0 {
		http.NotFound(w, r)
		return
	}

	h.writeJSON(w, urls)
}

// handleV1UploadURLs answers {file: url} for the files the client is about to
// upload, given as {file: size}. A recipe goes to revision "0"; a package goes
// to the newest published recipe revision (404 when the recipe has none).
func (h *HTTPHandler) handleV1UploadURLs(w http.ResponseWriter, r *http.Request) {
	s, ok := h.scopeOf(w, r)
	if !ok {
		return
	}

	var sizes map[string]int64
	if !h.readV1Body(w, r, &sizes) {
		return
	}

	for name, size := range sizes {
		if !validFilename(name) || size < 0 {
			http.Error(w, "invalid file list", http.StatusBadRequest)
			return
		}

		if size > maxConanUploadBytes {
			http.Error(w, "upload too large", http.StatusRequestEntityTooLarge)
			return
		}
	}

	rrev, prev := v1Revision, ""

	if s.pid != "" {
		all, ok := h.recipeFiles(w, r, s)
		if !ok {
			return
		}

		revs := recipeRevisions(all)
		if len(revs) == 0 {
			http.Error(w, "recipe not found", http.StatusNotFound)
			return
		}

		rrev, prev = revs[0].ID, v1Revision
	}

	urls := make(map[string]string, len(sizes))
	for name := range sizes {
		urls[name] = h.fileURL(s, rrev, s.pid, prev, name)
	}

	h.writeJSON(w, urls)
}

// handleV1RemoveFiles deletes the named files of the recipe's revision "0": the
// client sends it after an upload for the files the new recipe no longer has.
// Naming a file that is not there is not an error.
func (h *HTTPHandler) handleV1RemoveFiles(w http.ResponseWriter, r *http.Request) {
	s, ok := h.scopeOf(w, r)
	if !ok {
		return
	}

	var body struct {
		Files []string `json:"files"`
	}

	if !h.readV1Body(w, r, &body) {
		return
	}

	for _, name := range body.Files {
		if !validFilename(name) {
			http.Error(w, "invalid file list", http.StatusBadRequest)
			return
		}
	}

	h.deleteScope(w, r, s, true, func(f *models.ConanPackage) bool {
		return f.RecipeRevision == v1Revision && f.PackageID == "" && slices.Contains(body.Files, f.Filename)
	})
}

// handleV1RemovePackages deletes the named packages of every recipe revision,
// or all of them when the list is empty.
func (h *HTTPHandler) handleV1RemovePackages(w http.ResponseWriter, r *http.Request) {
	s, ok := h.scopeOf(w, r)
	if !ok {
		return
	}

	var body struct {
		PackageIDs []string `json:"package_ids"`
	}

	if !h.readV1Body(w, r, &body) {
		return
	}

	for _, id := range body.PackageIDs {
		if id == "" || !validRevision(id) {
			http.Error(w, "invalid package id", http.StatusBadRequest)
			return
		}
	}

	h.deleteScope(w, r, s, false, func(f *models.ConanPackage) bool {
		return f.PackageID != "" && (len(body.PackageIDs) == 0 || slices.Contains(body.PackageIDs, f.PackageID))
	})
}

// readV1Body decodes the bounded JSON request body into v; it answers 400 and
// returns false when that fails.
func (h *HTTPHandler) readV1Body(w http.ResponseWriter, r *http.Request, v any) bool {
	defer func() { _ = r.Body.Close() }()

	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxV1RequestBytes))
	if err := dec.Decode(v); err != nil {
		if _, tooBig := errors.AsType[*http.MaxBytesError](err); tooBig {
			http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
			return false
		}

		http.Error(w, fmt.Sprintf("invalid JSON body: %v", err), http.StatusBadRequest)

		return false
	}

	return true
}
