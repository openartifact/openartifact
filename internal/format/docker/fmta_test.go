// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package docker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
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

// An image without a manifest is unknown to the registry (404 NAME_UNKNOWN on
// its tag listing); an image that has manifests but no tag still lists as
// empty. Groups tell an empty hosted member from one holding the image by it.
func TestRegistry_TagsListUnknownImage(t *testing.T) {
	g := newRegistry(t, newRegFakeDB("reg"))

	rec := g.do(http.MethodGet, "/v2/reg/ghost/tags/list", "")
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "NAME_UNKNOWN") {
		t.Fatalf("unknown image tags/list = %d %s, want 404 NAME_UNKNOWN", rec.Code, rec.Body.String())
	}

	// Pushed by digest only: the image exists, it just has no tag.
	body := `{"schemaVersion":2,"mediaType":"` + ociManifestType + `"}`
	sum := sha256.Sum256([]byte(body))

	if rec := g.putManifest("reg/app", "sha256:"+hex.EncodeToString(sum[:]), body); rec.Code != http.StatusCreated {
		t.Fatalf("PUT by digest = %d; %s", rec.Code, rec.Body.String())
	}

	rec = g.do(http.MethodGet, "/v2/reg/app/tags/list", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"tags":[]`) {
		t.Fatalf("untagged image tags/list = %d %s, want 200 with empty tags", rec.Code, rec.Body.String())
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

// FMTA-11, across replicas: a PATCH that lost the race for the session's offset
// (another replica appended first) does not join the upload and leaves no chunk
// behind, so the blob is exactly the bytes the winning requests sent.
func TestHandleUploadChunk_LosingTheOffsetRaceDropsTheChunk(t *testing.T) {
	uid := uuid.New().String()

	inner := &fakeDB{
		repo:   &models.Repository{ID: "repo-1", Format: "docker"},
		upload: &models.DockerUpload{RepositoryID: "repo-1", UploadUUID: uid, Offset: 5},
	}
	inner.append("repo-1", uid, "winner-0", 5)

	// Another replica appends between this request's read of the offset and its
	// own append.
	db := &racingDB{fakeDB: inner, rival: func() {
		_, _ = inner.AppendDockerUploadChunk(context.Background(), "repo-1", uid, "winner-1", 5, 8)
	}}
	h, _ := newTestHandler(t, db)

	req := withChiVars(
		httptest.NewRequestWithContext(context.Background(), http.MethodPatch, "/v2/myrepo/blobs/uploads/"+uid, strings.NewReader("world")),
		map[string]string{"name": "myrepo", "uuid": uid},
	)
	rec := httptest.NewRecorder()
	h.handleUploadChunk(rec, req)

	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "BLOB_UPLOAD_INVALID") {
		t.Fatalf("status = %d %s, want 409 BLOB_UPLOAD_INVALID", rec.Code, rec.Body.String())
	}

	if got := len(inner.list("repo-1", uid)); got != 2 {
		t.Fatalf("chunk rows = %d, want the two winners only", got)
	}

	if ids := chunkObjects(t, h); len(ids) != 0 {
		t.Fatalf("chunk objects left behind: %v", ids)
	}
}

// racingDB runs rival right before the first append of a PATCH.
type racingDB struct {
	*fakeDB

	rival func()
}

func (d *racingDB) AppendDockerUploadChunk(
	ctx context.Context, repositoryID, uploadUUID, chunkID string, oldOffset, newOffset int64,
) (bool, error) {
	if d.rival != nil {
		d.rival()
		d.rival = nil
	}

	return d.fakeDB.AppendDockerUploadChunk(ctx, repositoryID, uploadUUID, chunkID, oldOffset, newOffset)
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
