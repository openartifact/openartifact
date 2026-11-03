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
	"time"

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

// FMTA-11, across replicas: a PATCH whose lease was taken over (another replica
// moved the session while this one was writing) does not join the upload: the
// answer is 409 and the session stays where the winner left it.
func TestHandleUploadChunk_LosingTheSessionAnswersConflict(t *testing.T) {
	uid := uuid.New().String()

	inner := &fakeDB{
		repo:   &models.Repository{ID: "repo-1", Format: "docker"},
		upload: &models.DockerUpload{RepositoryID: "repo-1", UploadUUID: uid},
	}

	// Another replica takes the lease over and appends between this request's
	// start and the moment it records its own position.
	db := &racingDB{fakeDB: inner, rival: func() {
		key := inner.uploadKey("repo-1", uid)
		inner.drop(key)
		inner.acquire(key, "rival", time.Minute)

		if !inner.advance(key, inner.upload, "rival", 0, 3, nil, "", time.Minute, false) {
			t.Error("the rival could not advance the session")
		}
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

	if inner.upload.Offset != 3 {
		t.Fatalf("offset = %d, want the winner's 3", inner.upload.Offset)
	}
}

// A request that finds the session in use by another one is told to query the
// status and resume, with the current Range; it writes nothing.
func TestHandleUploadChunk_SessionInUseAnswersConflict(t *testing.T) {
	uid := uuid.New().String()

	db := &fakeDB{
		repo:   &models.Repository{ID: "repo-1", Format: "docker"},
		upload: &models.DockerUpload{RepositoryID: "repo-1", UploadUUID: uid, Offset: 7},
	}
	db.hold(db.uploadKey("repo-1", uid))

	h, _ := newTestHandler(t, db)

	for _, method := range []string{http.MethodPatch, http.MethodPut} {
		req := withChiVars(
			httptest.NewRequestWithContext(
				context.Background(),
				method,
				"/v2/myrepo/blobs/uploads/"+uid+"?digest="+digestOf([]byte("x")),
				strings.NewReader("world"),
			),
			map[string]string{"name": "myrepo", "uuid": uid},
		)
		rec := httptest.NewRecorder()

		if method == http.MethodPut {
			h.handleCompleteBlobUpload(rec, req)
		} else {
			h.handleUploadChunk(rec, req)
		}

		if rec.Code != http.StatusConflict || rec.Header().Get("Range") != "0-6" {
			t.Fatalf("%s = %d Range %q, want 409 with Range 0-6", method, rec.Code, rec.Header().Get("Range"))
		}
	}

	if ids := uploadObjects(t, h); len(ids) != 0 || db.upload.Offset != 7 {
		t.Fatalf("a refused request changed the session: objects %v, offset %d", ids, db.upload.Offset)
	}
}

// racingDB runs rival right before the first position update of a request.
type racingDB struct {
	*fakeDB

	rival func()
}

func (d *racingDB) AdvanceDockerUpload(
	ctx context.Context, repositoryID, uploadUUID, token string,
	oldOffset, newOffset int64, hashState []byte, stageState string, ttl time.Duration, keepLease bool,
) (bool, error) {
	if d.rival != nil {
		d.rival()
		d.rival = nil
	}

	return d.fakeDB.AdvanceDockerUpload(ctx, repositoryID, uploadUUID, token, oldOffset, newOffset, hashState, stageState, ttl, keepLease)
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
