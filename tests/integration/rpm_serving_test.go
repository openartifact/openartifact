// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package integration_test

import (
	"compress/gzip"
	"context"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	rpmformat "github.com/openartifact/openartifact/internal/format/rpm"
)

func buildServingRouter(t *testing.T) chi.Router {
	t.Helper()
	httpHandler := rpmformat.NewHTTPHandler(harness.Log, harness.Database, harness.BlobStore)
	router := chi.NewRouter()
	router.Route("/repos/{name}", func(r chi.Router) {
		httpHandler.RegisterRepoRoutes(r)
	})
	return router
}

func setupServing(t *testing.T, repoName, pkgName string) (string, []byte) {
	t.Helper()
	ctx := createContext(t)
	r := createTestRepo(t, ctx, repoName)
	rpmData := CreateTestRPM(pkgName, "1.0.0", "1.el9", "x86_64")
	uploadRouter := buildUploadRouter(t)
	w := uploadRPM(t, uploadRouter, rpmData, repoName)
	if w.Code != 200 {
		t.Fatalf("upload failed: %d %s", w.Code, w.Body.String())
	}
	if err := harness.Database.MarkRepoStale(ctx, r.ID); err != nil {
		t.Fatalf("MarkRepoStale: %v", err)
	}
	runMetadataRegen(t, r.ID)
	return r.ID, rpmData
}

func createContext(t *testing.T) context.Context {
	t.Helper()
	return context.Background()
}

func TestRPMServing_RepomdXML(t *testing.T) {
	harness.Reset(t)

	setupServing(t, "serve-repomd-repo", "repomdpkg")

	router := buildServingRouter(t)
	req := httptest.NewRequestWithContext(context.Background(), "GET", "/repos/serve-repomd-repo/repodata/repomd.xml", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET repomd.xml: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	body := w.Body.Bytes()
	if !strings.Contains(string(body), "repomd") {
		t.Errorf("repomd.xml body does not contain 'repomd': %s", string(body[:min(len(body), 200)]))
	}

	var repomd struct {
		XMLName xml.Name `xml:"repomd"`
	}
	if err := xml.Unmarshal(body, &repomd); err != nil {
		t.Errorf("failed to parse repomd.xml as XML: %v", err)
	}
}

func TestRPMServing_PrimaryXMLGz(t *testing.T) {
	harness.Reset(t)
	ctx := createContext(t)

	repoID, _ := setupServing(t, "serve-primary-repo", "primarypkg")

	metas, err := harness.Database.GetRepoMetadataByRepo(ctx, repoID)
	if err != nil {
		t.Fatalf("GetRepoMetadataByRepo: %v", err)
	}

	var primaryPath string
	for _, m := range metas {
		if strings.Contains(m.Path, "primary.xml.gz") {
			primaryPath = m.Path
			break
		}
	}
	if primaryPath == "" {
		t.Fatal("no primary.xml.gz in repo_metadata")
	}

	urlPath := "/repos/serve-primary-repo/" + primaryPath
	router := buildServingRouter(t)
	req := httptest.NewRequestWithContext(context.Background(), "GET", urlPath, nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET primary.xml.gz: expected 200, got %d", w.Code)
	}

	body := w.Body.Bytes()
	if len(body) < 2 || body[0] != 0x1f || body[1] != 0x8b {
		t.Errorf("expected gzip magic header, got %x %x", body[0], body[1])
	}

	gr, err := gzip.NewReader(strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("gzip.NewReader: %v", err)
	}
	defer gr.Close()
	decompressed, err := io.ReadAll(gr)
	if err != nil {
		t.Fatalf("reading gzip: %v", err)
	}
	if !strings.Contains(string(decompressed), "primarypkg") {
		t.Errorf("primary.xml does not contain package name")
	}
}

func TestRPMServing_PackageDownload(t *testing.T) {
	harness.Reset(t)

	_, rpmData := setupServing(t, "serve-pkg-repo", "dlpkg")

	router := buildServingRouter(t)
	req := httptest.NewRequestWithContext(context.Background(), "GET", "/repos/serve-pkg-repo/Packages/dlpkg-1.0.0-1.el9.x86_64.rpm", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET package: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	if !strings.EqualFold(w.Header().Get("Content-Type"), "application/x-rpm") {
		t.Errorf("Content-Type = %q, want application/x-rpm", w.Header().Get("Content-Type"))
	}

	got := w.Body.Bytes()
	if len(got) != len(rpmData) {
		t.Errorf("body length = %d, want %d", len(got), len(rpmData))
	}
}

func TestRPMServing_RepomdXMLAsc_404(t *testing.T) {
	harness.Reset(t)

	setupServing(t, "serve-asc-repo", "ascpkg")

	router := buildServingRouter(t)
	req := httptest.NewRequestWithContext(context.Background(), "GET", "/repos/serve-asc-repo/repodata/repomd.xml.asc", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("GET repomd.xml.asc: expected 404, got %d", w.Code)
	}
}

func TestRPMServing_PublicKey_404(t *testing.T) {
	harness.Reset(t)

	setupServing(t, "serve-key-repo", "keypkg")

	router := buildServingRouter(t)
	req := httptest.NewRequestWithContext(context.Background(), "GET", "/repos/serve-key-repo/public.key", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("GET public.key: expected 404, got %d", w.Code)
	}
}
