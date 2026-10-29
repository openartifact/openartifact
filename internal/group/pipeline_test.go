// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package group

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/pkg/logger"
)

// A member is asked for the document itself: a GET without the client's
// validators, ranges or Accept-Encoding (a hosted member would gzip what the
// merge has to read).
func TestMemberGetRequest_AsksForTheDocumentItself(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequestWithContext(context.Background(), http.MethodHead, "/x", nil)
	for _, h := range []string{
		"If-None-Match", "If-Modified-Since", "If-Match", "If-Unmodified-Since", "If-Range", "Range", "Accept-Encoding",
	} {
		req.Header.Set(h, "x")
	}

	req.Header.Set("Accept", "application/json")

	mr := memberGetRequest(req)

	if mr.Method != http.MethodGet {
		t.Errorf("method = %s, want GET", mr.Method)
	}

	for _, h := range []string{"If-None-Match", "If-Modified-Since", "If-Match", "If-Unmodified-Since", "If-Range", "Range", "Accept-Encoding"} {
		if v := mr.Header.Get(h); v != "" {
			t.Errorf("%s = %q reaches the member", h, v)
		}
	}

	if mr.Header.Get("Accept") != "application/json" {
		t.Error("Accept of the client was dropped")
	}

	if req.Header.Get("Accept-Encoding") == "" {
		t.Error("the client's own request was modified")
	}
}

// The cargo index is a serveMergedDoc document: the three answers of the
// pipeline are the merge (200), 404 when no member has it and 502 when the
// only members that could have it failed; a merge that skipped a failed member
// is not cacheable.
func TestServeMergedDoc_Ladder(t *testing.T) {
	t.Parallel()

	const path = "index/de/mo/demo"

	t.Run("none have it", func(t *testing.T) {
		t.Parallel()

		f := cargoGroup(t)
		if w := f.get(http.MethodGet, path); w.Code != http.StatusNotFound {
			t.Errorf("= %d, want 404", w.Code)
		}
	})

	t.Run("only failures", func(t *testing.T) {
		t.Parallel()

		f := cargoGroup(t)
		f.set("hosted", path, 500, "boom")

		if w := f.get(http.MethodGet, path); w.Code != http.StatusBadGateway {
			t.Errorf("= %d, want 502", w.Code)
		}
	})

	t.Run("partial merge is not cacheable", func(t *testing.T) {
		t.Parallel()

		f := cargoGroup(t)
		f.set("hosted", path, 500, "boom")
		f.set("mirror", path, 200, cargoLine("mirror", "1.0.0"))

		w := f.get(http.MethodGet, path)
		if w.Code != http.StatusOK {
			t.Fatalf("= %d, want 200", w.Code)
		}

		if w.Header().Get("ETag") != "" || w.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("partial merge carries validators: ETag %q, Cache-Control %q", w.Header().Get("ETag"), w.Header().Get("Cache-Control"))
		}
	})
}

// A member handler that writes nothing answers an empty 200, as net/http does:
// it is the (empty) document, not a failed member.
func TestAskMember_EmptyResponseIsAnEmptyOK(t *testing.T) {
	t.Parallel()

	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatal(err)
	}

	h := &Handler{log: log}
	sess := &mergeSession{ctx: context.Background()}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/x", nil)
	be := Backend{ServeMember: func(http.ResponseWriter, *http.Request, *models.Repository, string) {}}

	got := h.askMember(sess, req, &models.Repository{Name: "m"}, "doc", 1024, be)
	if got.failed || !got.found {
		t.Fatalf("answer = %+v, want found and not failed", got)
	}
}
