// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package osv

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/pkg/logger"
)

func testImporter(t *testing.T, srv *httptest.Server, maxBytes int64) *Importer {
	t.Helper()

	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatal(err)
	}

	imp, err := NewImporter(nil, Options{
		BaseURL: srv.URL + "/mirror/", Ecosystems: []string{"npm"}, TempDir: t.TempDir(), MaxArchiveBytes: maxBytes, Client: srv.Client(),
	}, log)
	if err != nil {
		t.Fatal(err)
	}

	return imp
}

func TestImporterArchiveURLAndConditionalRequest(t *testing.T) {
	t.Parallel()

	var gotPath, gotETag string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotETag = r.URL.Path, r.Header.Get("If-None-Match")

		w.WriteHeader(http.StatusNotModified)
	}))
	t.Cleanup(srv.Close)

	imp := testImporter(t, srv, 1<<20)
	st := &models.OSVMirrorState{Ecosystem: "npm", ETag: `"abc"`}

	res, err := imp.Import(context.Background(), "npm", st)
	if err != nil || !res.NotModified {
		t.Fatalf("Import = %+v, %v; want NotModified", res, err)
	}

	if gotPath != "/mirror/npm/all.zip" || gotETag != `"abc"` {
		t.Fatalf("request path %q, If-None-Match %q", gotPath, gotETag)
	}

	if st.LastCheckedAt == nil || st.SourceURL != srv.URL+"/mirror/npm/all.zip" || st.HighWater != nil {
		t.Fatalf("state after 304 = %+v", st)
	}
}

func TestImporterRejectsBadStatusAndOversizeAndLeavesState(t *testing.T) {
	t.Parallel()

	var status = http.StatusBadGateway

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if status != http.StatusOK {
			http.Error(w, "nope", status)

			return
		}

		_, _ = w.Write([]byte(strings.Repeat("x", 4096)))
	}))
	t.Cleanup(srv.Close)

	imp := testImporter(t, srv, 1024)
	st := &models.OSVMirrorState{Ecosystem: "npm"}

	if _, err := imp.Import(context.Background(), "npm", st); !errors.Is(err, ErrBadStatus) {
		t.Fatalf("bad status error = %v, want ErrBadStatus", err)
	}

	status = http.StatusOK

	if _, err := imp.Import(context.Background(), "npm", st); !errors.Is(err, ErrArchiveTooLarge) {
		t.Fatalf("oversize error = %v, want ErrArchiveTooLarge", err)
	}

	if st.HighWater != nil || st.ETag != "" {
		t.Fatalf("a failed import must not advance the state: %+v", st)
	}

	if left, _ := os.ReadDir(imp.opts.TempDir); len(left) != 0 {
		t.Fatalf("temp files left behind: %v", left)
	}
}

func TestNewImporterRejectsUnsupportedEcosystem(t *testing.T) {
	t.Parallel()

	if _, err := NewImporter(nil, Options{Ecosystems: []string{"Debian:11"}}, nil); err == nil {
		t.Fatal("NewImporter accepted a distro ecosystem")
	}
}
