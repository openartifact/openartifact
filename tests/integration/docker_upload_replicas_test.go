// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package integration_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/models"
	formatdocker "github.com/openartifact/openartifact/internal/format/docker"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/internal/storage"
)

// dockerReplica is a docker registry server of the deployment: its own handler
// (and blob store instance) over the shared database and storage directory.
func dockerReplica(t *testing.T) *httptest.Server {
	t.Helper()

	store, err := storage.NewFilesystemStore(harness.TempDir)
	if err != nil {
		t.Fatalf("store: %v", err)
	}

	h := formatdocker.NewHTTPHandler(harness.Log, harness.Database, store)
	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(middleware.WithPrincipal(r.Context(), &auth.Principal{Username: "tester"})))
		})
	})
	h.RegisterRoutes(router)

	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)

	return srv
}

// replicaDo sends a request to one replica and returns the status, headers and
// body.
func replicaDo(t *testing.T, srv *httptest.Server, method, target, body string) (int, http.Header, string) {
	t.Helper()

	req, err := http.NewRequestWithContext(context.Background(), method, srv.URL+target, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, target, err)
	}

	defer func() { _ = resp.Body.Close() }()

	data, _ := io.ReadAll(resp.Body)

	return resp.StatusCode, resp.Header, string(data)
}

// A chunked push whose POST, PATCHes and PUT land on alternating replicas
// completes against the real database, and the blob can be pulled from either.
func TestDockerChunkedUpload_AlternatesBetweenReplicas(t *testing.T) {
	harness.Reset(t)

	orgID := systemOrgID
	repo := &models.Repository{Name: "ha-docker", Format: "docker", ProjectID: &orgID}

	if err := harness.Database.CreateRepository(t.Context(), repo); err != nil {
		t.Fatalf("CreateRepository: %v", err)
	}

	replicas := []*httptest.Server{dockerReplica(t), dockerReplica(t)}
	next := 0
	pick := func() *httptest.Server {
		next++
		return replicas[next%len(replicas)]
	}

	chunks := []string{strings.Repeat("a", 1000), strings.Repeat("b", 2500), strings.Repeat("c", 700), "final body"}
	layer := strings.Join(chunks, "")
	sum := sha256.Sum256([]byte(layer))
	digest := "sha256:" + hex.EncodeToString(sum[:])

	code, hdr, body := replicaDo(t, pick(), http.MethodPost, "/v2/ha-docker/app/blobs/uploads/", "")
	if code != http.StatusAccepted {
		t.Fatalf("POST = %d %s", code, body)
	}

	loc := hdr.Get("Location")
	if i := strings.Index(loc, "/v2/"); i > 0 {
		loc = loc[i:]
	}

	offset := 0

	for i, chunk := range chunks[:3] {
		code, hdr, body = replicaDo(t, pick(), http.MethodPatch, loc, chunk)
		if code != http.StatusAccepted {
			t.Fatalf("PATCH %d = %d %s", i+1, code, body)
		}

		offset += len(chunk)

		if got, want := hdr.Get("Range"), "0-"+strconv.Itoa(offset-1); got != want {
			t.Fatalf("PATCH %d Range = %q, want %q", i+1, got, want)
		}
	}

	code, hdr, _ = replicaDo(t, pick(), http.MethodGet, loc, "")
	if code != http.StatusNoContent || hdr.Get("Range") != "0-"+strconv.Itoa(offset-1) {
		t.Fatalf("status = %d Range %q", code, hdr.Get("Range"))
	}

	code, _, body = replicaDo(t, pick(), http.MethodPut, loc+"?digest="+digest, chunks[3])
	if code != http.StatusCreated {
		t.Fatalf("PUT = %d %s", code, body)
	}

	for i, srv := range replicas {
		code, _, got := replicaDo(t, srv, http.MethodGet, "/v2/ha-docker/app/blobs/"+digest, "")
		if code != http.StatusOK || got != layer {
			t.Fatalf("replica %d pull = %d, %d bytes, want the %d pushed", i, code, len(got), len(layer))
		}
	}

	var rows int
	if err := harness.DB.QueryRowContext(t.Context(),
		`SELECT count(*) FROM docker_uploads`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("upload rows left = %d, %v, want none", rows, err)
	}
}
