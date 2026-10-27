// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package maven

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/pkg/logger"
)

type snapshotMockDB struct {
	mavenMockDB

	arts []*models.MavenArtifact
}

func (m *snapshotMockDB) GetMavenArtifactByPath(_ context.Context, _, path string) (*models.MavenArtifact, error) {
	for _, a := range m.arts {
		if a.RepoPath == path {
			return a, nil
		}
	}

	return nil, nil
}

// GetLatestSnapshotFile mimics the SQL: filter on classifier+extension, newest build first.
func (m *snapshotMockDB) GetLatestSnapshotFile(
	_ context.Context,
	_, _, artifactID, version, classifier, ext string,
) (*models.MavenArtifact, error) {
	var best *models.MavenArtifact

	for _, a := range m.arts {
		if a.ArtifactCoord != artifactID || a.Version != version || a.Classifier != classifier || a.Extension != ext {
			continue
		}

		if best == nil || a.SnapshotTimestamp > best.SnapshotTimestamp {
			best = a
		}
	}

	return best, nil
}

func newSnapshotRouter(t *testing.T) (http.Handler, *mavenMockBlobStore) {
	t.Helper()

	files := map[string][]byte{
		"aa": []byte("POM-CONTENT"),
		"bb": []byte("JAR-CONTENT-LONGER"),
		"cc": []byte("SOURCES"),
		"dd": []byte("OLD-JAR"),
	}

	bs := &mavenMockBlobStore{blobs: files}
	mk := func(sha, ts, cls, ext string) *models.MavenArtifact {
		name := "foo-1.0-" + ts
		if cls != "" {
			name += "-" + cls
		}

		return &models.MavenArtifact{
			ArtifactCoord: "foo", Version: "1.0-SNAPSHOT", Classifier: cls, Extension: ext, IsSnapshot: true,
			SnapshotTimestamp: ts, SHA256: sha, BlobSizeBytes: int64(len(files[sha])),
			RepoPath: "com/ex/foo/1.0-SNAPSHOT/" + name + "." + ext,
		}
	}

	// All files of the newest build share timestamp+build; one older jar.
	db := &snapshotMockDB{
		repo: &models.Repository{ID: "repo-1", Name: "r"},
		arts: []*models.MavenArtifact{
			mk("aa", "20240102.000000-2", "", "pom"),
			mk("bb", "20240102.000000-2", "", "jar"),
			mk("cc", "20240102.000000-2", "sources", "jar"),
			mk("dd", "20240101.000000-1", "", "jar"),
		},
	}

	log, err := logger.New("error", "json", "stdout")
	if err != nil {
		t.Fatal(err)
	}

	h := NewHTTPHandler(log, db, bs)
	router := chi.NewRouter()
	router.Route("/repos/{name}", func(r chi.Router) { h.RegisterRepoRoutes(r) })

	return router, bs
}

func mvnReq(router http.Handler, method, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequestWithContext(context.Background(), method, path, nil))

	return w
}

// FMTA-5: -SNAPSHOT.<ext> and -SNAPSHOT-<classifier>.<ext> resolve to the file
// of that extension/classifier, not whichever row of the newest build comes first.
func TestSnapshotLegacyNameResolvesByClassifierAndExtension(t *testing.T) {
	router, _ := newSnapshotRouter(t)
	base := "/repos/r/com/ex/foo/1.0-SNAPSHOT/foo-1.0-SNAPSHOT"

	for suffix, want := range map[string]string{
		".pom":         "POM-CONTENT",
		".jar":         "JAR-CONTENT-LONGER",
		"-sources.jar": "SOURCES",
	} {
		w := mvnReq(router, http.MethodGet, base+suffix)
		if w.Code != http.StatusOK || w.Body.String() != want {
			t.Errorf("GET %s = %d %q, want %q", suffix, w.Code, w.Body.String(), want)
		}
	}

	if w := mvnReq(router, http.MethodGet, base+"-javadoc.jar"); w.Code != http.StatusNotFound {
		t.Errorf("unknown classifier = %d, want 404", w.Code)
	}
}

// FMTA-6: HEAD reports what GET would: status, length, and legacy-name resolution.
func TestHeadMatchesGet(t *testing.T) {
	router, _ := newSnapshotRouter(t)

	for _, path := range []string{
		"/repos/r/com/ex/foo/1.0-SNAPSHOT/foo-1.0-SNAPSHOT.jar",
		"/repos/r/com/ex/foo/1.0-SNAPSHOT/foo-1.0-SNAPSHOT-sources.jar",
		"/repos/r/com/ex/foo/1.0-SNAPSHOT/foo-1.0-20240102.000000-2.jar",
	} {
		get := mvnReq(router, http.MethodGet, path)
		head := mvnReq(router, http.MethodHead, path)

		if head.Code != get.Code {
			t.Errorf("%s: HEAD %d vs GET %d", path, head.Code, get.Code)
			continue
		}

		if got, want := head.Header().Get("Content-Length"), strconv.Itoa(get.Body.Len()); got != want {
			t.Errorf("%s: HEAD Content-Length = %q, want %q", path, got, want)
		}

		if head.Header().Get("ETag") != get.Header().Get("ETag") {
			t.Errorf("%s: ETag differs", path)
		}

		if head.Body.Len() != 0 {
			t.Errorf("%s: HEAD has a body", path)
		}
	}

	if w := mvnReq(router, http.MethodHead, "/repos/r/com/ex/foo/1.0-SNAPSHOT/nothing.jar"); w.Code != http.StatusNotFound {
		t.Errorf("HEAD missing = %d, want 404", w.Code)
	}
}

// FMTA-6: HEAD on maven-metadata.xml and checksum paths is answered like GET.
func TestHeadMetadataAndChecksums(t *testing.T) {
	xml := []byte("<metadata/>")
	sha := "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
	db := &mavenMockDB{
		repo: &models.Repository{ID: "repo-1", Name: "r"},
		meta: &models.RepoMetadata{ID: "m", RepositoryID: "repo-1", Path: "x", BlobSHA256: sha, BlobSize: int64(len(xml))},
	}

	log, _ := logger.New("error", "json", "stdout")
	h := NewHTTPHandler(log, db, &mavenMockBlobStore{blobs: map[string][]byte{sha: xml}})
	router := chi.NewRouter()
	router.Route("/repos/{name}", func(r chi.Router) { h.RegisterRepoRoutes(r) })

	for _, p := range []string{"/repos/r/com/ex/foo/maven-metadata.xml", "/repos/r/com/ex/foo/maven-metadata.xml.sha1", "/repos/r/com/ex/foo/maven-metadata.xml.sha256"} {
		get := mvnReq(router, http.MethodGet, p)
		head := mvnReq(router, http.MethodHead, p)

		if get.Code != http.StatusOK || head.Code != http.StatusOK {
			t.Errorf("%s: GET %d HEAD %d, want 200", p, get.Code, head.Code)
			continue
		}

		if head.Header().Get("Content-Length") != strconv.Itoa(get.Body.Len()) {
			t.Errorf("%s: HEAD Content-Length = %q, want %d", p, head.Header().Get("Content-Length"), get.Body.Len())
		}
	}
}
