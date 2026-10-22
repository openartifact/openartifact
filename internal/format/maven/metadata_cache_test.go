// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package maven

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha512"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
)

type countingBlobStore struct {
	*mavenMockBlobStore

	gets atomic.Int32
}

func (c *countingBlobStore) Get(ctx context.Context, id string) (io.ReadCloser, error) {
	c.gets.Add(1)

	return c.mavenMockBlobStore.Get(ctx, id)
}

func newMetadataRouter(tb testing.TB, xml []byte, sha string) (http.Handler, *mavenMockDB, *countingBlobStore) {
	tb.Helper()

	db := &mavenMockDB{
		repo: &models.Repository{ID: "repo-1", Name: "r"},
		meta: &models.RepoMetadata{ID: "m", RepositoryID: "repo-1", Path: "x", BlobSHA256: sha, BlobSize: int64(len(xml))},
	}
	bs := &countingBlobStore{mavenMockBlobStore: &mavenMockBlobStore{blobs: map[string][]byte{sha: xml}}}

	h := NewHTTPHandler(formattest.NewLogger(tb), db, bs)
	router := chi.NewRouter()
	router.Route("/repos/{name}", func(r chi.Router) { h.RegisterRepoRoutes(r) })

	return router, db, bs
}

const metadataPath = "/repos/r/com/ex/foo/maven-metadata.xml"

func TestMetadataChecksumsMatchAlgorithmAndAreCached(t *testing.T) {
	xml := []byte("<metadata><versioning><latest>1.0</latest></versioning></metadata>")
	router, _, bs := newMetadataRouter(t, xml, strings.Repeat("ab", 32))

	md5Sum := md5.Sum(xml)
	sha1Sum := sha1.Sum(xml)
	sha512Sum := sha512.Sum512(xml)

	for suffix, want := range map[string]string{
		".md5": hex.EncodeToString(md5Sum[:]), ".sha1": hex.EncodeToString(sha1Sum[:]), ".sha512": hex.EncodeToString(sha512Sum[:]),
	} {
		for range 3 {
			w := mvnReq(router, http.MethodGet, metadataPath+suffix)
			if got := strings.Fields(w.Body.String()); w.Code != http.StatusOK || len(got) != 2 || got[0] != want || got[1] != "maven-metadata.xml" {
				t.Fatalf("%s = %d %q, want digest %s", suffix, w.Code, w.Body.String(), want)
			}
		}
	}

	if n := bs.gets.Load(); n != 3 {
		t.Errorf("metadata blob read %d times for 9 checksum requests over 3 algorithms, want 3", n)
	}
}

func TestMetadataServedFromCacheAndFollowsNewDigest(t *testing.T) {
	xml := []byte("<metadata>one</metadata>")
	sha := strings.Repeat("cd", 32)
	router, db, bs := newMetadataRouter(t, xml, sha)

	for range 3 {
		if w := mvnReq(router, http.MethodGet, metadataPath); w.Body.String() != string(xml) || w.Header().Get("Content-Type") != "application/xml" {
			t.Fatalf("metadata = %q (%s)", w.Body.String(), w.Header().Get("Content-Type"))
		}
	}

	if n := bs.gets.Load(); n != 1 {
		t.Errorf("metadata blob read %d times for 3 polls, want 1", n)
	}

	// Regeneration produces a new blob digest: clients holding the old
	// validator get the new document.
	newXML := []byte("<metadata>two</metadata>")
	newSHA := strings.Repeat("ef", 32)
	bs.blobs[newSHA] = newXML
	db.meta = &models.RepoMetadata{ID: "m", RepositoryID: "repo-1", Path: "x", BlobSHA256: newSHA, BlobSize: int64(len(newXML))}

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, metadataPath, http.NoBody)
	req.Header.Set("If-None-Match", `"`+sha+`"`)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK || w.Body.String() != string(newXML) {
		t.Errorf("after regeneration: %d %q", w.Code, w.Body.String())
	}
}

func TestMetadataGzipOnlyWhenAccepted(t *testing.T) {
	xml := []byte("<metadata>" + strings.Repeat("<version>1.0.0</version>", 200) + "</metadata>")
	router, _, _ := newMetadataRouter(t, xml, strings.Repeat("12", 32))

	plain := mvnReq(router, http.MethodGet, metadataPath)
	if plain.Header().Get("Content-Encoding") != "" || !bytes.Equal(plain.Body.Bytes(), xml) {
		t.Fatalf("plain request: encoding %q", plain.Header().Get("Content-Encoding"))
	}

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, metadataPath, http.NoBody)
	req.Header.Set("Accept-Encoding", "gzip")

	zipped := httptest.NewRecorder()
	router.ServeHTTP(zipped, req)

	zr, err := gzip.NewReader(zipped.Body)
	if err != nil || zipped.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("gzip request: encoding %q err %v", zipped.Header().Get("Content-Encoding"), err)
	}

	if got, _ := io.ReadAll(zr); !bytes.Equal(got, xml) {
		t.Error("decompressed metadata differs")
	}

	if zipped.Body.Len() >= len(xml) {
		t.Errorf("gzip body %d bytes is not smaller than %d", zipped.Body.Len(), len(xml))
	}
}
