// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/openartifact/openartifact/internal/database/models"
)

// FMTA-2: tags/list pages with n/last and a Link header; n is capped.
func TestRegistry_TagsListPaginates(t *testing.T) {
	g := newRegistry(t, newRegFakeDB("reg"))

	body := `{"schemaVersion":2,"mediaType":"` + ociManifestType + `"}`
	for i := range 5 {
		if rec := g.putManifest("reg/app", fmt.Sprintf("t%d", i), body); rec.Code != http.StatusCreated {
			t.Fatalf("PUT t%d = %d", i, rec.Code)
		}
	}

	list := func(query string) ([]string, string) {
		rec := g.do(http.MethodGet, "/v2/reg/app/tags/list"+query, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("tags/list%s = %d; %s", query, rec.Code, rec.Body.String())
		}

		var resp struct {
			Tags []string `json:"tags"`
		}

		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode: %v", err)
		}

		return resp.Tags, rec.Header().Get("Link")
	}

	tags, link := list("?n=2")
	if strings.Join(tags, ",") != "t0,t1" {
		t.Fatalf("page 1 = %v", tags)
	}

	if link == "" {
		t.Fatal("page 1 has no Link header")
	}

	target := link[strings.Index(link, "<")+1 : strings.Index(link, ">")]

	u, err := url.Parse(target)
	if err != nil || u.Path != "/v2/reg/app/tags/list" || u.Query().Get("last") != "t1" || u.Query().Get("n") != "2" {
		t.Fatalf("Link = %q", link)
	}

	tags, link = list("?n=2&last=t1")
	if strings.Join(tags, ",") != "t2,t3" || link == "" {
		t.Fatalf("page 2 = %v link=%q", tags, link)
	}

	tags, link = list("?n=2&last=t3")
	if strings.Join(tags, ",") != "t4" || link != "" {
		t.Fatalf("last page = %v link=%q", tags, link)
	}

	// Exactly n remaining: no spurious next page.
	if tags, link = list("?n=5"); len(tags) != 5 || link != "" {
		t.Fatalf("n=5 = %v link=%q", tags, link)
	}

	if tags, _ = list("?n=0"); len(tags) != 0 {
		t.Fatalf("n=0 = %v, want empty", tags)
	}

	if rec := g.do(http.MethodGet, "/v2/reg/app/tags/list?n=abc", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("n=abc = %d, want 400", rec.Code)
	}

	// An absurd n is capped, not honoured.
	if tags, _ = list("?n=999999"); len(tags) != 5 {
		t.Fatalf("n=999999 = %v", tags)
	}
}

// FMTA-13: manifest bodies are capped.
func TestRegistry_ManifestBodyIsCapped(t *testing.T) {
	g := newRegistry(t, newRegFakeDB("reg"))

	huge := `{"schemaVersion":2,"annotations":{"x":"` + strings.Repeat("a", maxManifestBytes) + `"}}`
	if rec := g.putManifest("reg/app", "big", huge); rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized manifest = %d, want 413", rec.Code)
	}
}

// FMTA-11: bytes a failed PATCH wrote past the accepted offset are discarded
// before the next chunk is appended.
func TestHandleUploadChunk_TruncatesStrayBytes(t *testing.T) {
	uid := uuid.New().String()

	// Accepted: "hello" (5 bytes). A dropped PATCH left "JUNK" behind.
	if err := os.WriteFile(tempUploadPath(t, uid), []byte("helloJUNK"), 0o600); err != nil {
		t.Fatal(err)
	}

	db := &fakeDB{repo: &models.Repository{ID: "repo-1", Format: "docker"}, upload: &models.DockerUpload{Offset: 5}}
	h, _ := newTestHandler(t, db)

	req := withChiVars(
		httptest.NewRequestWithContext(context.Background(), http.MethodPatch, "/v2/myrepo/blobs/uploads/"+uid, strings.NewReader("world")),
		map[string]string{"name": "myrepo", "uuid": uid},
	)
	rec := httptest.NewRecorder()
	h.handleUploadChunk(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d; %s", rec.Code, rec.Body.String())
	}

	got, _ := os.ReadFile(tempUploadPath(t, uid))
	if string(got) != "helloworld" {
		t.Fatalf("file = %q, want helloworld", got)
	}

	if rec.Header().Get("Range") != "0-9" {
		t.Fatalf("Range = %q", rec.Header().Get("Range"))
	}
}

// FMTA-16: an empty upload reports 0-0, never 0--1.
func TestUploadRange(t *testing.T) {
	if got := uploadRange(0); got != "0-0" {
		t.Fatalf("uploadRange(0) = %q", got)
	}

	if got := uploadRange(10); got != "0-9" {
		t.Fatalf("uploadRange(10) = %q", got)
	}
}
