// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
)

func TestIsPacmanDatabase(t *testing.T) {
	for key, want := range map[string]bool{
		"extra/os/x86_64/extra.db":                         true,
		"extra/os/x86_64/extra.db.sig":                     true,
		"extra/os/x86_64/extra.files":                      true,
		"extra/os/x86_64/extra.db.tar.gz":                  true,
		"extra/os/x86_64/extra.files.tar.zst.sig":          true,
		"extra/os/x86_64/foo-1.0-1-x86_64.pkg.tar.zst":     false,
		"extra/os/x86_64/foo-1.0-1-x86_64.pkg.tar.zst.sig": false,
		"extra/os/x86_64/.db":                              false,
		"extra/os/x86_64/":                                 false,
		"lastsync":                                         false,
	} {
		if got := isPacmanDatabase(key); got != want {
			t.Errorf("isPacmanDatabase(%q) = %v, want %v", key, got, want)
		}
	}

	if !isMetadataKey("pacman", "core/os/x86_64/core.db") || isMetadataKey("pacman", "core/os/x86_64/a-1-1-any.pkg.tar.zst") {
		t.Error("isMetadataKey does not tell databases from packages")
	}

	// File names are not pinned: a third-party repository may republish one.
	if Immutable("pacman", "core/os/x86_64/a-1-1-any.pkg.tar.zst") {
		t.Error("a package file is pinned")
	}
}

func TestServe_PacmanMirrorPaths(t *testing.T) {
	var gotPath string

	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = io.WriteString(w, "db-bytes")
	})
	f.repo.Format = "pacman"
	f.repo.UpstreamURL = f.upstream.URL

	// A mirror root serves every section; the epoch colon passes through.
	if w := f.do(
		http.MethodGet,
		"extra/os/x86_64/oa-hello-1:1.0-1-x86_64.pkg.tar.zst",
	); w.Code != http.StatusOK ||
		w.Body.String() != "db-bytes" {
		t.Fatalf("package: %d %q", w.Code, w.Body.String())
	}

	if gotPath != "/extra/os/x86_64/oa-hello-1:1.0-1-x86_64.pkg.tar.zst" {
		t.Errorf("upstream path = %q", gotPath)
	}
}

func TestServe_PacmanDatabaseIfModifiedSince(t *testing.T) {
	f := newFixture(t, okBody("db"))
	f.repo.Format = "pacman"
	f.repo.UpstreamURL = f.upstream.URL

	const key = "extra/os/x86_64/extra.db"

	first := f.do(http.MethodGet, key)
	if first.Code != http.StatusOK {
		t.Fatalf("first = %d", first.Code)
	}

	modified := first.Header().Get("Last-Modified")
	if modified == "" {
		t.Fatal("no Last-Modified on a database")
	}

	get := func(since string) int {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
		if since != "" {
			req.Header.Set("If-Modified-Since", since)
		}

		w := httptest.NewRecorder()
		f.svc.Serve(w, req, f.repo, key)

		return w.Code
	}

	if code := get(modified); code != http.StatusNotModified {
		t.Errorf("If-Modified-Since = Last-Modified: %d, want 304", code)
	}

	older, err := http.ParseTime(modified)
	if err != nil {
		t.Fatal(err)
	}

	if code := get(older.Add(-time.Hour).Format(http.TimeFormat)); code != http.StatusOK {
		t.Errorf("If-Modified-Since before Last-Modified: %d, want 200", code)
	}

	if code := get("garbage"); code != http.StatusOK {
		t.Errorf("unparsable If-Modified-Since: %d, want 200", code)
	}

	// Package files carry no Last-Modified of ours.
	if w := f.do(http.MethodGet, "extra/os/x86_64/a-1-1-any.pkg.tar.zst"); w.Header().Get("Last-Modified") != "" {
		t.Error("Last-Modified on a package")
	}
}

func TestTTLForPacmanDatabasesIsCapped(t *testing.T) {
	repo := &models.Repository{Format: "pacman", CacheTTLSeconds: 3600}

	if got := ttlFor(repo, "core/os/x86_64/core.db", true); got != pacmanDBMaxTTL {
		t.Errorf("database TTL = %v, want %v", got, pacmanDBMaxTTL)
	}

	if got := ttlFor(repo, "core/os/x86_64/a-1-1-any.pkg.tar.zst", true); got != time.Hour {
		t.Errorf("package TTL = %v, want the repository TTL", got)
	}

	repo.CacheTTLSeconds = 60
	if got := ttlFor(repo, "core/os/x86_64/core.db", true); got != time.Minute {
		t.Errorf("short repository TTL = %v, want it kept", got)
	}
}
