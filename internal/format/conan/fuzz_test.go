// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package conan

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"maps"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
)

// fuzzPath is the request path of segs, "/"-joined: with escaped every segment
// is escaped on its own with url.PathEscape, as fileURL writes the URLs it
// hands to conan 1 clients, otherwise the whole path gets the canonical
// encoding, as a client that builds the URL from the plain path writes it.
// The two differ for characters such as ';' and ',', which Go leaves alone in
// a path and PathEscape escapes; the server then sees a raw path.
func fuzzPath(escaped bool, segs ...string) string {
	if !escaped {
		return (&url.URL{Path: "/" + strings.Join(segs, "/")}).EscapedPath()
	}

	out := make([]string, len(segs))
	for i, s := range segs {
		out[i] = url.PathEscape(s)
	}

	return "/" + strings.Join(out, "/")
}

// fuzzRows is a snapshot of the stored rows, oldest first.
func fuzzRows(e *env) []models.ConanPackage {
	e.db.mu.Lock()
	defer e.db.mu.Unlock()

	rows := make([]models.ConanPackage, len(e.db.rows))
	for i, r := range e.db.rows {
		rows[i] = *r
	}

	return rows
}

// fuzzKeys is the set of identities of rows.
func fuzzKeys(rows []models.ConanPackage) map[models.ConanFileKey]bool {
	keys := make(map[models.ConanFileKey]bool, len(rows))
	for i := range rows {
		keys[keyOf(&rows[i])] = true
	}

	return keys
}

// FuzzValidators feeds arbitrary text to the validators every URL parameter
// goes through, and checks what each one admits: bounded plain text, no dot
// segments, no separators, and one consistent notion across the validators.
func FuzzValidators(f *testing.F) {
	for _, s := range [][4]string{
		{"zlib", "1.3", "_", "_"},
		{"conanfile.py", "r1", "a/b", `a\b`},
		{"metadata/sign/signature.json", "d4f0ab9e8c7b6a5948372615f0e1d2c3", "a b", "é"},
		{"a//b", "a/../b", "x\x00y", "\xff"},
		{"", "-", "..", strings.Repeat("a", 101)},
	} {
		f.Add(s[0], s[1], s[2], s[3])
	}

	f.Fuzz(func(t *testing.T, a, b, c, d string) {
		fuzzCheckFilename(t, a)
		fuzzCheckFilename(t, b)
		fuzzCheckRevision(t, a)
		fuzzCheckRevision(t, b)

		if validRef(a, b, c, d) {
			for _, p := range []string{a, b, c, d} {
				fuzzCheckRefPart(t, p)
			}
		}
	})
}

func fuzzCheckRefPart(t *testing.T, p string) {
	t.Helper()

	switch {
	case p == "" || len(p) > maxRefPart:
		t.Fatalf("validRef admits %q (%d bytes)", p, len(p))
	case p == "." || p == "..":
		t.Fatalf("validRef admits the dot segment %q", p)
	case !plainText(p) || strings.ContainsAny(p, `/\`):
		t.Fatalf("validRef admits %q, which is not a plain single segment", p)
	case !validFilename(p):
		t.Fatalf("validRef admits %q but validFilename does not", p)
	}
}

func fuzzCheckRevision(t *testing.T, s string) {
	t.Helper()

	if !validRevision(s) {
		return
	}

	if len(s) > maxRevisionLen {
		t.Fatalf("validRevision admits %q (%d bytes)", s, len(s))
	}

	// A revision is a URL segment of the files the clients address with it.
	if s == "." || s == ".." {
		t.Fatalf("validRevision admits the dot segment %q", s)
	}

	for i := range len(s) {
		c := s[i]
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '.' && c != '_' && c != '-' {
			t.Fatalf("validRevision admits %q, which has the byte %q", s, c)
		}
	}
}

func fuzzCheckFilename(t *testing.T, name string) {
	t.Helper()

	if !validFilename(name) {
		return
	}

	if len(name) > maxFilenameLen || !plainText(name) || strings.Contains(name, `\`) || path.IsAbs(name) {
		t.Fatalf("validFilename admits %q", name)
	}

	// Plain relative path: nothing for path.Clean to collapse, no dot segment.
	if path.Clean(name) != name {
		t.Fatalf("validFilename admits %q, which is not clean (%q)", name, path.Clean(name))
	}

	segs := strings.Split(name, "/")
	if slices.Contains(segs, "..") || slices.Contains(segs, ".") || slices.Contains(segs, "") {
		t.Fatalf("validFilename admits %q with a dot or empty segment", name)
	}

	// The escaped segments fileURL writes read back as the same name.
	for i, seg := range segs {
		segs[i] = url.PathEscape(seg)
	}

	if back, err := url.PathUnescape(strings.Join(segs, "/")); err != nil || back != name {
		t.Fatalf("name %q does not survive its URL form: %q, %v", name, back, err)
	}
}

// FuzzFileName uploads a file under an arbitrary name through the v2 route and
// reads it back: a name validFilename admits is stored under exactly that name,
// listed under it and served from it, whichever way the client escaped the URL;
// any other name is refused with nothing stored.
func FuzzFileName(f *testing.F) {
	for _, name := range []string{
		"conanfile.py", "conan_export.tgz", "metadata/sign/signature.json", "a b", "a;b,c", "é",
		"", "a//b", "a/../b", "..", `a\b`, "x\x00y", "a?b#c", "\xff",
	} {
		f.Add(name, false)
		f.Add(name, true)
	}

	f.Fuzz(func(t *testing.T, name string, escaped bool) {
		e := newEnv(t)
		files := []string{"repos", "myrepo", "v2", "conans", "zlib", "1.3", "_", "_", "revisions", rrev1, "files"}
		target := fuzzPath(escaped, append(slices.Clone(files), strings.Split(name, "/")...)...)

		w := e.do(http.MethodPut, target, "x")

		if !validFilename(name) {
			if w.Code < 400 || w.Code >= 500 || e.db.count() != 0 {
				t.Fatalf("PUT %q = %d with %d rows, want a 4xx refusal and nothing stored", name, w.Code, e.db.count())
			}

			return
		}

		if w.Code != http.StatusCreated {
			t.Fatalf("PUT %q = %d, want 201: %s", name, w.Code, w.Body.String())
		}

		fuzzCheckStoredFile(t, e, name)
		fuzzCheckListing(t, e, escaped, files, name, target)
	})
}

func fuzzCheckStoredFile(t *testing.T, e *env, name string) {
	t.Helper()

	rows := fuzzRows(e)
	if len(rows) != 1 {
		t.Fatalf("PUT %q stored %d rows, want 1", name, len(rows))
	}

	r := rows[0]
	if r.Filename != name || r.Name != "zlib" || r.Version != "1.3" || r.UserChannel != "_/_" ||
		r.RecipeRevision != rrev1 || r.PackageID != "" || r.PackageRevision != "" || r.SizeBytes != 1 {
		t.Fatalf("PUT %q stored %+v", name, r)
	}
}

// fuzzCheckListing publishes the revision and checks that its listing holds
// exactly the uploaded names and that the file downloads from its own URL.
func fuzzCheckListing(t *testing.T, e *env, escaped bool, files []string, name, target string) {
	t.Helper()

	if name != manifestFile {
		manifest := fuzzPath(escaped, append(slices.Clone(files), manifestFile)...)
		if w := e.do(http.MethodPut, manifest, "m"); w.Code != http.StatusCreated {
			t.Fatalf("PUT manifest = %d: %s", w.Code, w.Body.String())
		}
	}

	w := e.do(http.MethodGet, fuzzPath(escaped, files...), "")
	if w.Code != http.StatusOK {
		t.Fatalf("list files = %d: %s", w.Code, w.Body.String())
	}

	listing := decode[struct {
		Files map[string]struct {
			Size int64 `json:"size"`
		} `json:"files"`
	}](t, w)

	want := []string{manifestFile}
	if name != manifestFile {
		want = append(want, name)
	}

	slices.Sort(want)

	if got := slices.Sorted(maps.Keys(listing.Files)); !slices.Equal(got, want) {
		t.Fatalf("listing = %q, want %q", got, want)
	}

	if w := e.do(http.MethodGet, target, ""); w.Code != http.StatusOK || w.Body.String() != "x" {
		t.Fatalf("GET %q = %d %q, want 200 \"x\"", name, w.Code, w.Body.String())
	}
}

// fuzzV1Names decodes an upload_urls body the way the handler does and lists
// the status codes it may answer with: 200, and 400 or 413 for a name or size it
// refuses (the handler answers for whichever it meets first).
func fuzzV1Names(body []byte) (map[string]int64, []int) {
	var sizes map[string]int64
	if err := json.NewDecoder(bytes.NewReader(body)).Decode(&sizes); err != nil {
		return nil, []int{http.StatusBadRequest}
	}

	var want []int

	for name, size := range sizes {
		if !validFilename(name) || size < 0 {
			want = append(want, http.StatusBadRequest)
		} else if size > maxConanUploadBytes {
			want = append(want, http.StatusRequestEntityTooLarge)
		}
	}

	if len(want) == 0 {
		want = []int{http.StatusOK}
	}

	return sizes, want
}

// FuzzV1UploadURLs asks for upload URLs of arbitrary files of an arbitrary
// recipe reference, then uses the URLs it is given: each must store its file
// under exactly the recipe, revision and name that were asked for.
func FuzzV1UploadURLs(f *testing.F) {
	body := []byte(`{"conanfile.py":10,"conanmanifest.txt":3,"metadata/a b.json":0}`)

	f.Add("zlib", "1.3", "_", "_", false, false, body)
	f.Add("zlib", "1.3", "_", "_", true, true, body)
	f.Add("fmt", "10.2.1+1", "acme", "stable", true, false, body)
	f.Add("zlib", "1.3", "_", "_", false, false, []byte(`{"../x":1}`))
	f.Add("zlib", "1.3", "_", "_", false, false, []byte(`{"a":-1}`))
	f.Add("zlib", "1.3", "_", "_", false, false, []byte(`null`))
	f.Add("zlib", "1.3", "_", "_", false, false, []byte(`[`))

	f.Fuzz(func(t *testing.T, pkg, version, user, channel string, escaped, forPackage bool, body []byte) {
		e := newEnv(t)
		ref := []string{pkg, version, user, channel}
		segs := []string{"repos", "myrepo", "v1", "conans"}
		rrev, pkgID, prev := v1Revision, "", ""

		if forPackage {
			e.publishRecipe(rrev1)

			ref = []string{"zlib", "1.3", "_", "_"}
			rrev, pkgID, prev = rrev1, pid, v1Revision
		}

		segs = append(segs, ref...)
		if forPackage {
			segs = append(segs, "packages", pkgID)
		}

		w := e.do(http.MethodPost, fuzzPath(escaped, append(segs, "upload_urls")...), string(body))
		if w.Code >= http.StatusInternalServerError {
			t.Fatalf("upload_urls = %d: %s", w.Code, w.Body.String())
		}

		if !validRef(ref...) {
			return
		}

		sizes, want := fuzzV1Names(body)
		if !slices.Contains(want, w.Code) {
			t.Fatalf("upload_urls %q = %d, want one of %v", body, w.Code, want)
		}

		if w.Code == http.StatusOK {
			fuzzCheckIssuedURLs(t, e, w.Body.Bytes(), slices.Sorted(maps.Keys(sizes)), ref, rrev, pkgID, prev)
		}
	})
}

// fuzzIssuedTarget turns a URL the server handed out into the request target of
// the test router, keeping the escaping the server chose.
func fuzzIssuedTarget(t *testing.T, raw string) string {
	t.Helper()

	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("issued URL %q does not parse: %v", raw, err)
	}

	p := u.EscapedPath()

	i := strings.Index(p, "/v1/files/")
	if i < 0 {
		t.Fatalf("issued URL %q is not a v1 file URL", raw)
	}

	return "/repos/myrepo" + p[i:]
}

// fuzzCheckIssuedURLs uploads one byte to every issued URL and checks where it
// lands.
func fuzzCheckIssuedURLs(t *testing.T, e *env, resp []byte, names, ref []string, rrev, pkgID, prev string) {
	t.Helper()

	var urls map[string]string
	if err := json.Unmarshal(resp, &urls); err != nil {
		t.Fatalf("upload_urls answered %q: %v", resp, err)
	}

	if got := slices.Sorted(maps.Keys(urls)); !slices.Equal(got, names) {
		t.Fatalf("upload_urls names = %q, want %q", got, names)
	}

	if len(names) > 4 {
		return
	}

	before := e.db.count()

	for name, raw := range urls {
		target := fuzzIssuedTarget(t, raw)

		if w := e.do(http.MethodPut, target, "x"); w.Code != http.StatusCreated {
			t.Fatalf("PUT %s (the URL of %q) = %d: %s", raw, name, w.Code, w.Body.String())
		}

		if w := e.do(http.MethodGet, target, ""); w.Code != http.StatusOK || w.Body.String() != "x" {
			t.Fatalf("GET %s (the URL of %q) = %d %q", raw, name, w.Code, w.Body.String())
		}
	}

	stored := fuzzRows(e)[before:]
	if len(stored) != len(names) {
		t.Fatalf("%d uploads stored %d new rows", len(names), len(stored))
	}

	for _, r := range stored {
		if r.Name != ref[0] || r.Version != ref[1] || r.UserChannel != ref[2]+"/"+ref[3] ||
			r.RecipeRevision != rrev || r.PackageID != pkgID || r.PackageRevision != prev || !slices.Contains(names, r.Filename) {
			t.Fatalf("a URL for %q/%q@%q/%q (rev %q, package %q/%q) stored %+v", ref[0], ref[1], ref[2], ref[3], rrev, pkgID, prev, r)
		}
	}
}

// fuzzSeedV1 publishes revision "0" of the recipe with two packages: what the
// Conan 1 remove routes work on.
func fuzzSeedV1(e *env) {
	for _, f := range []string{"conanfile.py", "conan_export.tgz", "conanmanifest.txt"} {
		e.mustDo(http.MethodPut, recipeFileURL(v1Revision, f), f, http.StatusCreated)
	}

	for _, id := range []string{pid, "p2"} {
		for _, f := range []string{"conan_package.tgz", "conanmanifest.txt"} {
			e.mustDo(http.MethodPut, packageFileURL(v1Revision, id, v1Revision, f), f, http.StatusCreated)
		}
	}
}

// fuzzRemoved is what a remove request has to delete and the status it answers
// with; a body the handler rejects up front deletes nothing.
type fuzzRemoved struct {
	status int
	gone   map[models.ConanFileKey]bool
}

// fuzzWantRemoveFiles is the effect remove_files has with body.
func fuzzWantRemoveFiles(body []byte, rows []models.ConanPackage) fuzzRemoved {
	var req struct {
		Files []string `json:"files"`
	}

	if err := json.NewDecoder(bytes.NewReader(body)).Decode(&req); err != nil || slices.ContainsFunc(req.Files, func(n string) bool { return !validFilename(n) }) {
		return fuzzRemoved{status: http.StatusBadRequest}
	}

	gone := map[models.ConanFileKey]bool{}

	for i := range rows {
		if r := &rows[i]; r.RecipeRevision == v1Revision && r.PackageID == "" && slices.Contains(req.Files, r.Filename) {
			gone[keyOf(r)] = true
		}
	}

	return fuzzRemoved{status: http.StatusOK, gone: gone}
}

// fuzzWantRemovePackages is the effect packages/delete has with body.
func fuzzWantRemovePackages(body []byte, rows []models.ConanPackage) fuzzRemoved {
	var req struct {
		PackageIDs []string `json:"package_ids"`
	}

	if err := json.NewDecoder(bytes.NewReader(body)).Decode(&req); err != nil ||
		slices.ContainsFunc(req.PackageIDs, func(id string) bool { return id == "" || !validRevision(id) }) {
		return fuzzRemoved{status: http.StatusBadRequest}
	}

	gone := map[models.ConanFileKey]bool{}

	for i := range rows {
		if r := &rows[i]; r.PackageID != "" && (len(req.PackageIDs) == 0 || slices.Contains(req.PackageIDs, r.PackageID)) {
			gone[keyOf(r)] = true
		}
	}

	if len(gone) == 0 {
		return fuzzRemoved{status: http.StatusNotFound}
	}

	return fuzzRemoved{status: http.StatusOK, gone: gone}
}

// FuzzV1Remove sends arbitrary bodies to remove_files and packages/delete: a
// body the handler refuses deletes nothing, and one it accepts deletes exactly
// the files it names.
func FuzzV1Remove(f *testing.F) {
	for _, body := range []string{
		`{"files":["conanfile.py"]}`, `{"files":["conanmanifest.txt","nope"]}`, `{"files":["../x"]}`, `{"files":null}`,
		`{"package_ids":["` + pid + `"]}`, `{"package_ids":[]}`, `{"package_ids":[""]}`, `{"package_ids":["a/b"]}`, `{}`, `[`,
	} {
		f.Add(false, []byte(body))
		f.Add(true, []byte(body))
	}

	f.Fuzz(func(t *testing.T, packages bool, body []byte) {
		e := newEnv(t)
		fuzzSeedV1(e)

		before := fuzzRows(e)
		route, want := "remove_files", fuzzWantRemoveFiles(body, before)

		if packages {
			route, want = "packages/delete", fuzzWantRemovePackages(body, before)
		}

		w := e.do(http.MethodPost, "/repos/myrepo/v1/conans/zlib/1.3/_/_/"+route, string(body))
		if w.Code != want.status {
			t.Fatalf("%s %q = %d, want %d: %s", route, body, w.Code, want.status, w.Body.String())
		}

		left := fuzzKeys(fuzzRows(e))

		for i := range before {
			k := keyOf(&before[i])
			if left[k] == want.gone[k] {
				t.Fatalf("%s %q: %+v is left=%t, want gone=%t", route, body, k, left[k], want.gone[k])
			}
		}
	})
}

// FuzzChecksumDeploy asks for a checksum deploy with an arbitrary SHA-1: a
// malformed one is refused, one the repository holds stores the file with the
// content of the file it names, and any other finds nothing.
func FuzzChecksumDeploy(f *testing.F) {
	sum := sha1.Sum([]byte("x"))

	right := hex.EncodeToString(sum[:])

	f.Add(right)
	f.Add(strings.ToUpper(right))
	f.Add(strings.Repeat("0", 40))
	f.Add(right[:39])
	f.Add(right + "0")
	f.Add("zz" + right[2:])
	f.Add("")

	f.Fuzz(func(t *testing.T, header string) {
		e := newEnv(t)
		e.mustDo(http.MethodPut, recipeFileURL(rrev1, "a.txt"), "x", http.StatusCreated)

		req := formattest.Request(http.MethodPut, recipeFileURL(rrev1, "b.txt"), nil)
		req.Header.Set(checksumDeployHeader, "true")
		req.Header.Set(checksumSHA1Header, header)

		w := formattest.Do(e.h.RegisterRepoRoutes, req)

		lower := strings.ToLower(header)

		var want int

		switch {
		case !validSHA1(lower):
			want = http.StatusBadRequest
		case lower == hex.EncodeToString(sum[:]):
			want = http.StatusCreated
		default:
			want = http.StatusNotFound
		}

		if w.Code != want {
			t.Fatalf("deploy of %q = %d, want %d: %s", header, w.Code, want, w.Body.String())
		}

		rows := fuzzRows(e)
		if want != http.StatusCreated {
			if len(rows) != 1 {
				t.Fatalf("deploy of %q stored %d rows, want only the source", header, len(rows))
			}

			return
		}

		if len(rows) != 2 || rows[1].Filename != "b.txt" || rows[1].SHA256 != rows[0].SHA256 || rows[1].SizeBytes != 1 || rows[1].SHA1 != lower {
			t.Fatalf("deploy of %q stored %+v", header, rows)
		}
	})
}

// fuzzConanFiles builds the live and legacy rows of one recipe from ops, five
// bytes a row, with one row per identity as the table has.
func fuzzConanFiles(ops []byte) []*models.ConanPackage {
	rrevs := []string{"a", "b", "c"}
	pids := []string{"", "p1", "p2"}
	prevs := []string{"", "x", "y"}
	names := []string{manifestFile, manifestFile, "conanfile.py", ""}
	origin := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

	var files []*models.ConanPackage

	seen := map[models.ConanFileKey]bool{}

	for ; len(ops) >= 5; ops = ops[5:] {
		row := &models.ConanPackage{
			Name: "n", Version: "1", UserChannel: "_/_",
			RecipeRevision: rrevs[int(ops[0])%len(rrevs)], PackageID: pids[int(ops[1])%len(pids)],
			PackageRevision: prevs[int(ops[2])%len(prevs)], Filename: names[int(ops[3])%len(names)],
			UpdatedAt: origin.Add(time.Duration(ops[4]%4) * time.Second),
		}

		if k := keyOf(row); !seen[k] {
			seen[k] = true

			files = append(files, row)
		}
	}

	return files
}

// fuzzWantRevisions is the revisions the published manifests of files in the
// recipe revision rrev and package pid hold: the recipe's own when pid is empty.
func fuzzWantRevisions(files []*models.ConanPackage, rrev, pid string) []revision {
	var revs []revision

	for _, f := range files {
		if f.Filename != manifestFile || f.PackageID != pid {
			continue
		}

		switch {
		case pid == "":
			revs = append(revs, revision{ID: f.RecipeRevision, Time: f.UpdatedAt})
		case f.RecipeRevision == rrev:
			revs = append(revs, revision{ID: f.PackageRevision, Time: f.UpdatedAt})
		}
	}

	return revs
}

func fuzzCheckNewestFirst(t *testing.T, what string, got, want []revision) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("%s: %d revisions, want %d", what, len(got), len(want))
	}

	for i := 1; i < len(got); i++ {
		a, b := got[i-1], got[i]
		if a.Time.Before(b.Time) || (a.Time.Equal(b.Time) && a.ID < b.ID) {
			t.Fatalf("%s: %v before %v is not newest first", what, a, b)
		}
	}

	key := func(r revision) string { return r.ID + "@" + r.Time.String() }
	gotKeys := make([]string, len(got))
	wantKeys := make([]string, len(want))

	for i := range got {
		gotKeys[i], wantKeys[i] = key(got[i]), key(want[i])
	}

	slices.Sort(gotKeys)
	slices.Sort(wantKeys)

	if !slices.Equal(gotKeys, wantKeys) {
		t.Fatalf("%s: revisions %q, want %q", what, gotKeys, wantKeys)
	}
}

// FuzzRevisions builds the rows of a recipe from arbitrary bytes and checks the
// revision queries against a direct reading of the rows: a revision is published
// by its manifest, listed newest first, and its files are those of its identity.
func FuzzRevisions(f *testing.F) {
	f.Add([]byte{0, 0, 0, 0, 1, 1, 0, 0, 0, 2, 0, 1, 1, 0, 3})
	f.Add([]byte{0, 1, 1, 0, 0, 0, 2, 2, 1, 1, 1, 1, 1, 1, 1, 2, 2, 2, 3, 0})
	f.Add([]byte{0, 0, 0, 3, 0})
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, ops []byte) {
		files := fuzzConanFiles(ops)

		fuzzCheckNewestFirst(t, "recipe", recipeRevisions(files), fuzzWantRevisions(files, "", ""))

		for _, rrev := range []string{"a", "b", "c"} {
			var ids []string

			for _, id := range []string{"p1", "p2"} {
				want := fuzzWantRevisions(files, rrev, id)
				fuzzCheckNewestFirst(t, "package "+id, packageRevisions(files, rrev, id), want)

				if len(want) > 0 {
					ids = append(ids, id)
				}

				fuzzCheckRevisionFiles(t, files, rrev, id)
			}

			if got := packageIDs(files, rrev); !slices.Equal(got, ids) && (len(got) != 0 || len(ids) != 0) {
				t.Fatalf("packageIDs(%q) = %q, want %q", rrev, got, ids)
			}

			fuzzCheckRevisionFiles(t, files, rrev, "")
		}
	})
}

// fuzzCheckRevisionFiles checks revisionFiles of every package revision of
// (rrev, pid): the live rows with exactly that identity, in order.
func fuzzCheckRevisionFiles(t *testing.T, files []*models.ConanPackage, rrev, pid string) {
	t.Helper()

	for _, prev := range []string{"", "x", "y"} {
		var want []*models.ConanPackage

		for _, f := range files {
			if f.Filename != "" && f.RecipeRevision == rrev && f.PackageID == pid && f.PackageRevision == prev {
				want = append(want, f)
			}
		}

		got := revisionFiles(files, rrev, pid, prev)
		if len(got) != len(want) || (len(want) > 0 && !slices.Equal(got, want)) {
			t.Fatalf("revisionFiles(%q, %q, %q) = %d files, want %d", rrev, pid, prev, len(got), len(want))
		}
	}
}

// fuzzGlobTok is one element of a parsed glob: '*', '?', a character class '['
// or the literal 'l'.
type fuzzGlobTok struct {
	kind byte
	set  []rune
	neg  bool
	lit  rune
}

func (k fuzzGlobTok) accepts(r rune) bool {
	switch k.kind {
	case '?':
		return true
	case '[':
		return slices.Contains(k.set, r) != k.neg
	default:
		return r == k.lit
	}
}

// fuzzClassEnd is the index of the ']' that closes the class opened at g[open]
// under the fnmatch rules, or -1.
func fuzzClassEnd(g []rune, open int) int {
	j := open + 1

	if j < len(g) && g[j] == '!' {
		j++
	}

	if j < len(g) && g[j] == ']' {
		j++
	}

	for j < len(g) && g[j] != ']' {
		j++
	}

	if j >= len(g) {
		return -1
	}

	return j
}

// fuzzGlobParse reads a glob without ranges.
func fuzzGlobParse(g []rune) []fuzzGlobTok {
	var toks []fuzzGlobTok

	for i := 0; i < len(g); i++ {
		switch c := g[i]; c {
		case '*', '?':
			toks = append(toks, fuzzGlobTok{kind: byte(c)})
		case '[':
			end := fuzzClassEnd(g, i)
			if end < 0 {
				toks = append(toks, fuzzGlobTok{kind: 'l', lit: c})
				continue
			}

			tok := fuzzGlobTok{kind: '[', set: g[i+1 : end]}
			if len(tok.set) > 0 && tok.set[0] == '!' {
				tok.neg, tok.set = true, tok.set[1:]
			}

			toks = append(toks, tok)
			i = end
		default:
			toks = append(toks, fuzzGlobTok{kind: 'l', lit: c})
		}
	}

	return toks
}

// fuzzGlobMatch reports whether name matches the whole glob: a reference
// implementation of fnmatch, case sensitive, without ranges.
func fuzzGlobMatch(glob, name string) bool {
	rs := []rune(name)
	cur := make([]bool, len(rs)+1)
	cur[0] = true

	for _, tok := range fuzzGlobParse([]rune(glob)) {
		next := make([]bool, len(rs)+1)

		if tok.kind == '*' {
			open := false

			for j := range cur {
				open = open || cur[j]
				next[j] = open
			}
		} else {
			for j := 1; j <= len(rs); j++ {
				next[j] = cur[j-1] && tok.accepts(rs[j-1])
			}
		}

		cur = next
	}

	return cur[len(rs)]
}

// fuzzGlobLiteral is the glob that matches exactly s.
func fuzzGlobLiteral(s string) string {
	var b strings.Builder

	for _, r := range s {
		if r == '*' || r == '?' || r == '[' {
			b.WriteString("[" + string(r) + "]")
			continue
		}

		b.WriteRune(r)
	}

	return b.String()
}

// fuzzOracleable reports whether fuzzGlobMatch defines the meaning of glob:
// valid text and no ranges, whose fine print differs between implementations.
func fuzzOracleable(glob string) bool {
	return utf8.ValidString(glob) && !strings.Contains(glob, "-")
}

// FuzzGlobRegexp compiles arbitrary search globs: only a reversed range may
// fail to compile, `*` matches everything, a glob escaped for literal use
// matches exactly the text it was made from, and without case folding the
// regexp agrees with a reference fnmatch.
func FuzzGlobRegexp(f *testing.F) {
	for _, s := range [][2]string{
		{"zlib/*", "zlib/1.3"}, {"*", ""}, {"z?ib", "zlib"}, {"[a-z]lib", "zlib"}, {"[!z]lib", "zlib"}, {"[z-a]", "a"},
		{"[", "["}, {"[]", "]"}, {"[]a]", "]"}, {"[!]", "[!]"}, {"a[*]", "a*"}, {`\`, `\`}, {"^$.|(", "^$.|("}, {"é?", "éa"},
	} {
		f.Add(s[0], s[1], false)
		f.Add(s[0], s[1], true)
	}

	f.Fuzz(func(t *testing.T, glob, name string, ignoreCase bool) {
		star, err := globRegexp("*", ignoreCase)
		if err != nil || !star.MatchString(name) {
			t.Fatalf(`"*" does not match %q: %v`, name, err)
		}

		fuzzCheckLiteralGlob(t, name, ignoreCase)

		re, err := globRegexp(glob, ignoreCase)
		if err != nil {
			if !strings.Contains(glob, "-") {
				t.Fatalf("globRegexp(%q) = %v: only a reversed range can be refused", glob, err)
			}

			return
		}

		if !ignoreCase && fuzzOracleable(glob) && utf8.ValidString(name) {
			if got, want := re.MatchString(name), fuzzGlobMatch(glob, name); got != want {
				t.Fatalf("glob %q on %q: regexp %q says %t, fnmatch says %t", glob, name, re, got, want)
			}
		}
	})
}

func fuzzCheckLiteralGlob(t *testing.T, name string, ignoreCase bool) {
	t.Helper()

	if !utf8.ValidString(name) {
		return
	}

	lit := fuzzGlobLiteral(name)

	re, err := globRegexp(lit, ignoreCase)
	if err != nil || !re.MatchString(name) {
		t.Fatalf("the literal glob %q of %q = %v, does not match it", lit, name, err)
	}

	if re.MatchString(name + "x") {
		t.Fatalf("the literal glob %q of %q is not anchored at the end", lit, name)
	}

	if !ignoreCase {
		if upper := strings.ToUpper(name); upper != name && re.MatchString(upper) {
			t.Fatalf("the case sensitive literal glob %q of %q matches %q", lit, name, upper)
		}
	}
}

// FuzzRecipeRef checks recipe references against the partial matching of
// searches: a valid reference reads back as written, each token boundary prefix
// of it is found by exactly its own text, and a cut inside a token is not.
func FuzzRecipeRef(f *testing.F) {
	f.Add("zlib", "1.3", "_", "_", "zlib/*")
	f.Add("fmt", "10.2", "acme", "stable", "fmt/10.2@acme/stable")
	f.Add("a", "b", "c", "", "[a-")
	f.Add("é", "1", "ü", "\xff", "é")

	f.Fuzz(func(t *testing.T, name, version, user, channel, glob string) {
		ref := splitRecipe(models.ConanRecipe{Name: name, Version: version, UserChannel: joinUserChannel(user, channel)})
		if strings.Join(ref.tokens(), "") != ref.String() {
			t.Fatalf("String %q is not its tokens %q", ref.String(), ref.tokens())
		}

		if validRef(name, version, user, channel) {
			fuzzCheckRefRoundTrip(t, ref, name, version, user, channel)
		}

		full := ref.String()
		if !utf8.ValidString(full) {
			return
		}

		fuzzCheckRefPrefixes(t, ref)

		if re, err := globRegexp(glob, true); err == nil && re.MatchString(full) && !ref.partialMatch(re) {
			t.Fatalf("glob %q matches %q but not as a partial match", glob, full)
		}
	})
}

func fuzzCheckRefRoundTrip(t *testing.T, ref recipeRef, name, version, user, channel string) {
	t.Helper()

	if user == "_" {
		user = ""
	}

	if channel == "_" {
		channel = ""
	}

	if ref.name != name || ref.version != version || ref.user != user || ref.channel != channel {
		t.Fatalf("the reference %q/%q@%q/%q reads back as %+v", name, version, user, channel, ref)
	}

	if !strings.HasPrefix(ref.String(), name+"/"+version) {
		t.Fatalf("String %q does not start with name/version", ref.String())
	}
}

func fuzzCheckRefPrefixes(t *testing.T, ref recipeRef) {
	t.Helper()

	full := ref.String()
	boundaries := map[string]bool{}
	prefix := ""

	for _, tok := range ref.tokens() {
		prefix += tok
		boundaries[prefix] = true

		re, err := globRegexp(fuzzGlobLiteral(prefix), false)
		if err != nil || !ref.partialMatch(re) {
			t.Fatalf("the boundary prefix %q of %q is not found by its own text: %v", prefix, full, err)
		}
	}

	for i := 1; i < len(full); i++ {
		if !utf8.RuneStart(full[i]) || boundaries[full[:i]] {
			continue
		}

		re, err := globRegexp(fuzzGlobLiteral(full[:i]), false)
		if err != nil || ref.partialMatch(re) {
			t.Fatalf("%q, a cut of %q inside a token, matches: %v", full[:i], full, err)
		}
	}
}

// FuzzSearchQuery searches a repository with arbitrary globs through the real
// route: a glob that does not compile is a 400, any other lists the recipes
// the reference fnmatch finds by a token boundary prefix, and a case
// insensitive search never finds fewer.
func FuzzSearchQuery(f *testing.F) {
	for _, q := range []string{"", "*", "zlib", "zlib/1.3", "zlib/*@*", "fmt/1*", "[a-", "[z-a]", "?lib*", "Boost", "boost/*", "[!z]*", "\xff"} {
		f.Add(q, false)
		f.Add(q, true)
	}

	f.Fuzz(func(t *testing.T, q string, caseSensitive bool) {
		e := newEnv(t)

		refs := map[string]recipeRef{}

		for _, r := range [][4]string{{"zlib", "1.3", "_", "_"}, {"fmt", "10.2", "acme", "stable"}, {"Boost", "1.84", "_", "dev"}} {
			e.mustDo(http.MethodPut, "/repos/myrepo/v2/conans/"+r[0]+"/"+r[1]+"/"+r[2]+"/"+r[3]+"/revisions/"+rrev1+"/files/"+manifestFile, "m", http.StatusCreated)

			ref := splitRecipe(models.ConanRecipe{Name: r[0], Version: r[1], UserChannel: joinUserChannel(r[2], r[3])})
			refs[ref.String()] = ref
		}

		ignorecase := "True"
		if caseSensitive {
			ignorecase = "False"
		}

		w := e.do(http.MethodGet, "/repos/myrepo/v2/conans/search?ignorecase="+ignorecase+"&q="+url.QueryEscape(q), "")

		if _, err := globRegexp(q, !caseSensitive); q != "" && err != nil {
			if w.Code != http.StatusBadRequest {
				t.Fatalf("search %q = %d, want 400 for a glob that does not compile", q, w.Code)
			}

			return
		}

		if w.Code != http.StatusOK {
			t.Fatalf("search %q = %d: %s", q, w.Code, w.Body.String())
		}

		got := decode[struct {
			Results []string `json:"results"`
		}](t, w).Results

		fuzzCheckSearchResults(t, q, caseSensitive, refs, got)
	})
}

func fuzzCheckSearchResults(t *testing.T, q string, caseSensitive bool, refs map[string]recipeRef, got []string) {
	t.Helper()

	for _, r := range got {
		if _, ok := refs[r]; !ok {
			t.Fatalf("search %q found %q, which was never published", q, r)
		}
	}

	if q == "" {
		if len(got) != len(refs) {
			t.Fatalf("an empty search found %q of %d recipes", got, len(refs))
		}

		return
	}

	// An insensitive search can only widen what the reference finds, except for
	// a negated class, whose members it widens too.
	if !fuzzOracleable(q) || (!caseSensitive && strings.Contains(q, "[")) {
		return
	}

	for s, ref := range refs {
		found, has := fuzzFoundByBoundary(q, ref), slices.Contains(got, s)

		if (caseSensitive && has != found) || (!caseSensitive && found && !has) {
			t.Fatalf("search %q (case sensitive: %t) on %q: found=%t, want %t", q, caseSensitive, s, has, found)
		}
	}
}

// fuzzFoundByBoundary reports whether glob matches ref cut at a token boundary,
// by the reference fnmatch.
func fuzzFoundByBoundary(glob string, ref recipeRef) bool {
	prefix := ""

	for _, tok := range ref.tokens() {
		prefix += tok
		if fuzzGlobMatch(glob, prefix) {
			return true
		}
	}

	return false
}
