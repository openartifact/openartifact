// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package pypi

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
	"github.com/openartifact/openartifact/internal/publicurl"
)

// stampDB is an in-memory repository that answers the stamps the way the real
// store does (a hash of the live file set, moved by a release metadata
// rewrite) and counts the reads of the rows the pages are rendered from.
type stampDB struct {
	*formattest.DB

	files []*models.PyPIPackage
	// revision stands for the updated_at of the releases: it moves when a
	// release's metadata is rewritten.
	revision int

	stamps       atomic.Int32
	listNames    atomic.Int32
	listVersions atomic.Int32

	// failStamp and failList make the matching read fail.
	failStamp, failList bool
	// hideFilesFromList makes the row listing come back empty while the stamp
	// still counts the files (they were deleted in between).
	hideFilesFromList bool
}

func newStampDB() *stampDB { return &stampDB{DB: formattest.NewDB("pypi")} }

func (d *stampDB) publish(project, version, ext string) {
	f := benchPyPIFile(project, version, ext)
	f.ID = f.Filename
	d.files = append(d.files, f)
}

func (d *stampDB) remove(filename string) {
	d.files = slices.DeleteFunc(d.files, func(f *models.PyPIPackage) bool { return f.Filename == filename })
}

// setSummary rewrites the metadata of one release.
func (d *stampDB) setSummary(project, version, summary string) {
	for _, f := range d.files {
		if f.Name == project && f.Version == version {
			f.Summary = summary
		}
	}

	d.revision++
}

func (d *stampDB) PyPIIndexStamp(_ context.Context, _ string) (string, error) {
	d.stamps.Add(1)

	if d.failStamp {
		return "", errors.New("stamp failed")
	}

	return strings.Join(distinctPyPINames(d.files), ","), nil
}

func (d *stampDB) PyPIProjectStamp(_ context.Context, _, name string) (string, int64, error) {
	d.stamps.Add(1)

	if d.failStamp {
		return "", 0, errors.New("stamp failed")
	}

	ids := []string{strconv.Itoa(d.revision)}

	for _, f := range d.files {
		if strings.EqualFold(f.Name, name) {
			ids = append(ids, f.ID)
		}
	}

	slices.Sort(ids)

	return strings.Join(ids, ","), int64(len(ids) - 1), nil
}

func (d *stampDB) ListPyPIPackageNames(_ context.Context, _ string) ([]string, error) {
	d.listNames.Add(1)

	if d.failList {
		return nil, errors.New("list failed")
	}

	return distinctPyPINames(d.files), nil
}

func (d *stampDB) ListPyPIPackageVersions(_ context.Context, _, name string) ([]*models.PyPIPackage, error) {
	d.listVersions.Add(1)

	if d.failList {
		return nil, errors.New("list failed")
	}

	if d.hideFilesFromList {
		return nil, nil
	}

	var out []*models.PyPIPackage

	for _, f := range d.files {
		if strings.EqualFold(f.Name, name) {
			out = append(out, f)
		}
	}

	// The real query orders by version, newest first.
	slices.SortStableFunc(out, func(a, b *models.PyPIPackage) int { return strings.Compare(b.Version, a.Version) })

	return out, nil
}

// reads is the number of row listings (names and files) served so far.
func (d *stampDB) reads() int32 { return d.listNames.Load() + d.listVersions.Load() }

func stampRouter(t *testing.T, db *stampDB) http.Handler {
	t.Helper()

	h := NewHTTPHandler(formattest.NewLogger(t), db, formattest.NewBlobStore(), "")
	router := chi.NewRouter()
	router.Route("/repos/{name}", h.RegisterRepoRoutes)

	return publicurl.Middleware("", "http://fallback.test", trustNoPeers)(router)
}

func fetch(t *testing.T, h http.Handler, path string, header map[string]string) *httptest.ResponseRecorder {
	t.Helper()

	return urlGet(t, h, path, func(r *http.Request) {
		for k, v := range header {
			r.Header.Set(k, v)
		}
	})
}

func gunzip(t *testing.T, body []byte) []byte {
	t.Helper()

	zr, err := gzip.NewReader(bytes.NewReader(body))
	require.NoError(t, err)

	out, err := io.ReadAll(zr)
	require.NoError(t, err)

	return out
}

const (
	indexPath   = "/repos/myrepo/simple/"
	projectPath = "/repos/myrepo/simple/demo/"
	jsonPath    = "/repos/myrepo/json/demo"
)

// Every cached document is rendered once per change of its rows, and every
// event that changes the document moves its validator and its content: a stale
// validator never gets a 304, and going back to an earlier state gives the
// earlier validator.
func TestPagesFollowTheRows(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		path string
		// change mutates the repository; present and absent are substrings of
		// the document before and after it.
		change  func(db *stampDB)
		present string
		absent  string
	}{
		{
			name: "index: first release of a new project", path: indexPath,
			change:  func(db *stampDB) { db.publish("other", "1.0", ".tar.gz") },
			present: `<a href="other/">other</a>`,
		},
		{
			name: "index: last file of a project deleted", path: indexPath,
			change: func(db *stampDB) {
				db.remove("demo-1.0.tar.gz")
				db.remove("demo-1.0-py3-none-any.whl")
			},
			absent: `<a href="demo/">demo</a>`,
		},
		{
			name: "project page: publish a version", path: projectPath,
			change:  func(db *stampDB) { db.publish("demo", "2.0", ".tar.gz") },
			present: "demo-2.0.tar.gz",
		},
		{
			name: "project page: delete a file", path: projectPath,
			change: func(db *stampDB) { db.remove("demo-1.0.tar.gz") },
			absent: "demo-1.0.tar.gz",
		},
		{
			name: "json: publish a version", path: jsonPath,
			change:  func(db *stampDB) { db.publish("demo", "2.0", ".tar.gz") },
			present: `"2.0":[`,
		},
		{
			name: "json: delete a file", path: jsonPath,
			change: func(db *stampDB) { db.remove("demo-1.0.tar.gz") },
			absent: "demo-1.0.tar.gz",
		},
		{
			name: "json: release metadata edit", path: jsonPath,
			change:  func(db *stampDB) { db.setSummary("demo", "1.0", "Rewritten summary") },
			present: `"summary":"Rewritten summary"`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			db := newStampDB()
			db.publish("demo", "1.0", ".tar.gz")
			db.publish("demo", "1.0", "-py3-none-any.whl")

			h := stampRouter(t, db)

			before := fetch(t, h, tc.path, nil)
			require.Equal(t, http.StatusOK, before.Code)

			etag := before.Header().Get("ETag")
			require.NotEmpty(t, etag)

			// Unchanged rows: the cached rendering, no further row read.
			reads := db.reads()
			again := fetch(t, h, tc.path, nil)
			assert.Equal(t, before.Body.String(), again.Body.String())
			assert.Equal(t, reads, db.reads(), "an unchanged document was rendered again")

			tc.change(db)

			stale := fetch(t, h, tc.path, map[string]string{"If-None-Match": etag})
			require.Equal(t, http.StatusOK, stale.Code, "a stale validator must not answer 304")
			assert.NotEqual(t, etag, stale.Header().Get("ETag"))
			assert.NotEqual(t, before.Body.String(), stale.Body.String())

			if tc.present != "" {
				assert.Contains(t, stale.Body.String(), tc.present)
			}

			if tc.absent != "" {
				assert.NotContains(t, stale.Body.String(), tc.absent)
			}

			// The new validator revalidates.
			fresh := fetch(t, h, tc.path, map[string]string{"If-None-Match": stale.Header().Get("ETag")})
			assert.Equal(t, http.StatusNotModified, fresh.Code)
		})
	}
}

// A publish into an existing project changes its pages but not the project
// list, whose stamp covers the set of projects only.
func TestIndexIgnoresNewVersionsOfKnownProjects(t *testing.T) {
	t.Parallel()

	db := newStampDB()
	db.publish("demo", "1.0", ".tar.gz")

	h := stampRouter(t, db)
	before := fetch(t, h, indexPath, nil)

	db.publish("demo", "2.0", ".tar.gz")

	after := fetch(t, h, indexPath, nil)
	assert.Equal(t, before.Header().Get("ETag"), after.Header().Get("ETag"))
	assert.Equal(t, int32(1), db.listNames.Load(), "the project list was rendered again for an unchanged project set")
}

// A matching If-None-Match is answered from the stamp alone: one stamp read,
// no row listing, no body.
func TestRevalidationReadsNoRows(t *testing.T) {
	t.Parallel()

	for _, path := range []string{indexPath, projectPath, jsonPath} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()

			db := newStampDB()
			db.publish("demo", "1.0", ".tar.gz")

			h := stampRouter(t, db)
			etag := fetch(t, h, path, nil).Header().Get("ETag")
			require.NotEmpty(t, etag)

			db.stamps.Store(0)
			db.listNames.Store(0)
			db.listVersions.Store(0)

			w := fetch(t, h, path, map[string]string{"If-None-Match": etag})
			assert.Equal(t, http.StatusNotModified, w.Code)
			assert.Empty(t, w.Body.String())
			assert.Equal(t, int32(1), db.stamps.Load(), "stamp reads")
			assert.Zero(t, db.reads(), "row listings on a 304")
		})
	}
}

// The first request of a document costs one stamp and one listing; a later one
// only the stamp.
func TestHitPathQueries(t *testing.T) {
	t.Parallel()

	db := newStampDB()
	db.publish("demo", "1.0", ".tar.gz")

	h := stampRouter(t, db)

	for range 3 {
		require.Equal(t, http.StatusOK, fetch(t, h, projectPath, nil).Code)
	}

	assert.Equal(t, int32(3), db.stamps.Load())
	assert.Equal(t, int32(1), db.listVersions.Load())
}

func TestUnknownProjectIsAnswered404FromTheStamp(t *testing.T) {
	t.Parallel()

	db := newStampDB()
	db.publish("demo", "1.0", ".tar.gz")

	h := stampRouter(t, db)

	for _, path := range []string{"/repos/myrepo/simple/nope/", "/repos/myrepo/json/nope", "/repos/other/simple/", "/repos/other/simple/demo/"} {
		w := fetch(t, h, path, nil)
		assert.Equal(t, http.StatusNotFound, w.Code, path)
		assert.Empty(t, w.Header().Get("ETag"), path)
	}

	assert.Zero(t, db.reads())
}

// The page and the JSON document of one project, and the same document of two
// origins, are different cache entries.
func TestVariantsAreNotMixed(t *testing.T) {
	t.Parallel()

	db := newStampDB()
	db.publish("demo", "1.0", ".tar.gz")

	h := stampRouter(t, db)

	page := fetch(t, h, projectPath, nil)
	doc := fetch(t, h, jsonPath, nil)

	assert.NotEqual(t, page.Header().Get("ETag"), doc.Header().Get("ETag"))
	assert.Contains(t, page.Header().Get("Content-Type"), "text/html")
	assert.Equal(t, "application/json", doc.Header().Get("Content-Type"))
	assert.True(t, json.Valid(doc.Body.Bytes()))
	assert.NotContains(t, page.Body.String(), "{")

	// The page validator does not revalidate the JSON document, nor the other way around.
	assert.Equal(t, http.StatusOK, fetch(t, h, jsonPath, map[string]string{"If-None-Match": page.Header().Get("ETag")}).Code)
	assert.Equal(t, http.StatusOK, fetch(t, h, projectPath, map[string]string{"If-None-Match": doc.Header().Get("ETag")}).Code)

	atA := fetch(t, h, jsonPath, nil)
	atB := urlGet(t, h, jsonPath, func(r *http.Request) { r.Host = "b.example" })

	assert.NotEqual(t, atA.Header().Get("ETag"), atB.Header().Get("ETag"))
	assert.Contains(t, atA.Body.String(), "http://a.example/repos/myrepo/files/")
	assert.Contains(t, atB.Body.String(), "http://b.example/repos/myrepo/files/")
	assert.Equal(t, http.StatusOK, urlGet(t, h, jsonPath, func(r *http.Request) {
		r.Host = "b.example"
		r.Header.Set("If-None-Match", atA.Header().Get("ETag"))
	}).Code, "a validator of one origin revalidated another")

	// The relative page is the same on every origin and shares one entry.
	pageB := urlGet(t, h, projectPath, func(r *http.Request) { r.Host = "b.example" })
	assert.Equal(t, page.Header().Get("ETag"), pageB.Header().Get("ETag"))
}

// A renamed repository (same ID) gets its own JSON URLs, not the cached ones.
func TestJSONFollowsTheRepositoryName(t *testing.T) {
	t.Parallel()

	db := newStampDB()
	db.publish("demo", "1.0", ".tar.gz")

	h := stampRouter(t, db)

	before := fetch(t, h, jsonPath, nil)
	require.Contains(t, before.Body.String(), "/repos/myrepo/files/")

	db.Repo.Name = "renamed"

	after := fetch(t, h, "/repos/renamed/json/demo", nil)
	assert.Contains(t, after.Body.String(), "/repos/renamed/files/")
	assert.NotContains(t, after.Body.String(), "/repos/myrepo/")
	assert.NotEqual(t, before.Header().Get("ETag"), after.Header().Get("ETag"))
}

// The compressed and the plain form of a document are served to the clients
// that ask for them, encoded once, whatever was requested first.
func TestGzipAndIdentityAreNotMixed(t *testing.T) {
	t.Parallel()

	for _, path := range []string{indexPath, projectPath, jsonPath} {
		for _, order := range [][]string{{"gzip", ""}, {"", "gzip"}} {
			t.Run(path+" "+strings.Join(order, ","), func(t *testing.T) {
				t.Parallel()

				db := newStampDB()
				for i := range 40 {
					db.publish("demo"+strconv.Itoa(i), "1.0", ".tar.gz")
				}

				for i := range 40 {
					db.publish("demo", "1."+strconv.Itoa(i), "-py3-none-any.whl")
				}

				h := stampRouter(t, db)
				got := map[string]*httptest.ResponseRecorder{}

				for _, enc := range order {
					got[enc] = fetch(t, h, path, map[string]string{"Accept-Encoding": enc})
				}

				plain, zipped := got[""], got["gzip"]

				require.Equal(t, http.StatusOK, plain.Code)
				assert.Empty(t, plain.Header().Get("Content-Encoding"))
				assert.True(t, strings.HasPrefix(plain.Body.String(), "<!DOCTYPE") || strings.HasPrefix(plain.Body.String(), "{"))

				assert.Equal(t, "gzip", zipped.Header().Get("Content-Encoding"))
				assert.Contains(t, strings.Join(zipped.Header().Values("Vary"), ","), "Accept-Encoding")
				assert.Equal(t, plain.Body.Bytes(), gunzip(t, zipped.Body.Bytes()), "compressed more than once or a different document")
				assert.Equal(t, strconv.Itoa(zipped.Body.Len()), zipped.Header().Get("Content-Length"))
				assert.Equal(t, int32(1), db.reads(), "rendered once for both encodings")
			})
		}
	}
}

// The name of a request is normalized (PEP 503): every spelling of a project
// reaches the same document.
func TestProjectNameSpellings(t *testing.T) {
	t.Parallel()

	db := newStampDB()
	db.publish("my-demo", "1.0", ".tar.gz")

	h := stampRouter(t, db)
	want := fetch(t, h, "/repos/myrepo/simple/my-demo/", nil)
	require.Equal(t, http.StatusOK, want.Code)

	for _, spelling := range []string{"My_Demo", "my.demo", "MY--DEMO", "My-_-Demo"} {
		w := fetch(t, h, "/repos/myrepo/simple/"+spelling+"/", nil)
		require.Equal(t, http.StatusOK, w.Code, spelling)
		assert.Equal(t, want.Body.String(), w.Body.String(), spelling)
		assert.Equal(t, want.Header().Get("ETag"), w.Header().Get("ETag"), spelling)
	}
}

// Stored names that differ only before normalization are one project.
func TestIndexCollapsesLegacyNames(t *testing.T) {
	t.Parallel()

	db := newStampDB()
	db.publish("Foo_Bar", "1.0", ".tar.gz")
	db.publish("foo-bar", "1.0", ".zip")
	db.publish("Baz", "1.0", ".tar.gz")

	body := fetch(t, stampRouter(t, db), indexPath, nil).Body.String()

	assert.Equal(t, "<!DOCTYPE html>\n<html><body>\n<a href=\"baz/\">baz</a>\n<a href=\"foo-bar/\">foo-bar</a>\n</body></html>\n", body)
}

// A failed read answers 500 and is never cached: the next request retries.
func TestFailedReadsAreNotCached(t *testing.T) {
	t.Parallel()

	for _, path := range []string{indexPath, projectPath, jsonPath} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()

			db := newStampDB()
			db.publish("demo", "1.0", ".tar.gz")

			h := stampRouter(t, db)

			db.failStamp = true
			assert.Equal(t, http.StatusInternalServerError, fetch(t, h, path, nil).Code)

			db.failStamp, db.failList = false, true
			assert.Equal(t, http.StatusInternalServerError, fetch(t, h, path, nil).Code)

			db.failList = false

			w := fetch(t, h, path, nil)
			assert.Equal(t, http.StatusOK, w.Code)
			assert.Contains(t, w.Body.String(), "demo")
		})
	}
}

// A project emptied between its stamp and its listing answers this request
// only: the empty rendering carries no validator and is not cached.
func TestRaceWithDeleteIsNotCached(t *testing.T) {
	t.Parallel()

	for _, path := range []string{projectPath, jsonPath} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()

			db := newStampDB()
			db.publish("demo", "1.0", ".tar.gz")
			db.hideFilesFromList = true

			h := stampRouter(t, db)

			w := fetch(t, h, path, nil)
			assert.Equal(t, http.StatusOK, w.Code)
			assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
			assert.Empty(t, w.Header().Get("ETag"))

			db.hideFilesFromList = false

			again := fetch(t, h, path, nil)
			assert.Contains(t, again.Body.String(), "demo-1.0.tar.gz")
			assert.NotEmpty(t, again.Header().Get("ETag"))
		})
	}
}
