// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package gitlfs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
	"github.com/openartifact/openartifact/internal/webhook"
	"github.com/openartifact/openartifact/pkg/gitlfs"
)

// lfsDB keeps objects in memory and counts quota like the real reservation:
// limit 0 is unlimited, a reservation that does not fit changes nothing.
type lfsDB struct {
	*formattest.DB

	objects   map[string]*models.GitLFSObject
	downloads map[string]int

	limit, used int64
	artifacts   int

	// raceOnPut makes PutGitLFSObject lose to a concurrent upload of the object.
	raceOnPut bool
}

func (d *lfsDB) GetGitLFSObjects(_ context.Context, _ string, oids []string) ([]*models.GitLFSObject, error) {
	var out []*models.GitLFSObject

	for _, oid := range oids {
		if o := d.objects[oid]; o != nil {
			out = append(out, o)
		}
	}

	return out, nil
}

func (d *lfsDB) PutGitLFSObject(_ context.Context, o *models.GitLFSObject) error {
	if d.raceOnPut {
		return fmt.Errorf("failed to put git lfs object: %w", database.ErrDuplicate)
	}

	o.ID = "file-" + o.OID[:8]
	d.objects[o.OID] = o

	return nil
}

func (d *lfsDB) CountFileDownload(id string) { d.downloads[id]++ }

func (d *lfsDB) ReserveStorageUsage(_ context.Context, _ string, size int64, count int) (bool, error) {
	if d.limit > 0 && d.used+size > d.limit {
		return false, nil
	}

	d.used += size
	d.artifacts += count

	return true, nil
}

func (d *lfsDB) DecrementStorageUsage(_ context.Context, _ string, size int64, count int) error {
	d.used -= size
	d.artifacts -= count

	return nil
}

func (d *lfsDB) GetOrCreateStorageQuota(context.Context, string) (*models.StorageQuota, error) {
	return &models.StorageQuota{MaxSizeBytes: d.limit, CurrentSizeBytes: d.used, CurrentArtifactCount: d.artifacts}, nil
}

// rig is a handler with the fakes behind it.
type rig struct {
	h   *HTTPHandler
	db  *lfsDB
	bs  *formattest.BlobStore
	pub *formattest.Publisher
}

func newRig(t *testing.T) rig {
	t.Helper()

	db := &lfsDB{DB: formattest.NewDB("gitlfs"), objects: map[string]*models.GitLFSObject{}, downloads: map[string]int{}}
	bs := formattest.NewBlobStore()
	pub := &formattest.Publisher{}
	h := NewHTTPHandler(formattest.NewLogger(t), db, bs, "http://localhost")
	h.SetPublisher(pub)

	return rig{h: h, db: db, bs: bs, pub: pub}
}

func newTestHandler(t *testing.T) (*HTTPHandler, *lfsDB, *formattest.BlobStore, *formattest.Publisher) {
	t.Helper()

	r := newRig(t)

	return r.h, r.db, r.bs, r.pub
}

func oidOf(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func do(h *HTTPHandler, method, target, body string) *httptest.ResponseRecorder {
	return formattest.Do(h.RegisterRepoRoutes, formattest.Request(method, target, strings.NewReader(body)))
}

func put(h *HTTPHandler, content string) *httptest.ResponseRecorder {
	return do(h, http.MethodPut, "/repos/myrepo/objects/"+oidOf(content), content)
}

// store puts an object into the repository through the handler.
func store(t *testing.T, h *HTTPHandler, content string) {
	t.Helper()

	if w := put(h, content); w.Code != http.StatusOK {
		t.Fatalf("put status = %d: %s", w.Code, w.Body.String())
	}
}

func batch(t *testing.T, h *HTTPHandler, body string) (*httptest.ResponseRecorder, gitlfs.BatchResponse) {
	t.Helper()

	w := do(h, http.MethodPost, "/repos/myrepo/objects/batch", body)

	var resp gitlfs.BatchResponse
	if w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("batch response is not JSON: %v: %s", err, w.Body.String())
		}
	}

	return w, resp
}

func pointer(content string) string {
	return fmt.Sprintf(`{"oid":%q,"size":%d}`, oidOf(content), len(content))
}

func TestPutStoresObjectOnceAndGetServesIt(t *testing.T) {
	check := formattest.IsolatedTemp(t)
	h, db, bs, pub := newTestHandler(t)

	const content = "hello lfs"

	store(t, h, content)

	oid := oidOf(content)
	if got := string(bs.Blobs[oid]); got != content {
		t.Fatalf("blob = %q", got)
	}

	obj := db.objects[oid]
	if obj == nil || obj.SizeBytes != int64(len(content)) || obj.BlobKey != oid || obj.RepositoryID != "repo-1" {
		t.Fatalf("object = %+v", obj)
	}

	if db.used != int64(len(content)) || db.artifacts != 1 {
		t.Errorf("quota used=%d artifacts=%d, want %d/1", db.used, db.artifacts, len(content))
	}

	evs := pub.Events()
	if len(evs) != 1 || evs[0].Type != webhook.EventArtifactUploaded {
		t.Fatalf("events = %+v", evs)
	}

	if a := evs[0].Artifact(); a["sha256"] != oid || a["path"] != "objects/"+oid[:2]+"/"+oid[2:4]+"/"+oid {
		t.Errorf("event artifact = %v", a)
	}

	// Uploading it again changes nothing, whatever the body.
	if w := put(h, content); w.Code != http.StatusOK {
		t.Fatalf("second put status = %d", w.Code)
	}

	if db.used != int64(len(content)) || db.artifacts != 1 || len(pub.Events()) != 1 {
		t.Errorf("second put changed state: used=%d artifacts=%d events=%d", db.used, db.artifacts, len(pub.Events()))
	}

	w := do(h, http.MethodGet, "/repos/myrepo/objects/"+oid, "")
	if w.Code != http.StatusOK || w.Body.String() != content {
		t.Fatalf("get = %d %q", w.Code, w.Body.String())
	}

	if etag := w.Header().Get("ETag"); etag != `"`+oid+`"` {
		t.Errorf("ETag = %q", etag)
	}

	if db.downloads[obj.ID] != 1 {
		t.Errorf("downloads counted = %d, want 1", db.downloads[obj.ID])
	}

	check()
}

func TestGetRangeConditionalAndHead(t *testing.T) {
	h, db, _, _ := newTestHandler(t)

	const content = "0123456789"

	store(t, h, content)

	oid := oidOf(content)
	target := "/repos/myrepo/objects/" + oid

	req := formattest.Request(http.MethodGet, target, nil)
	req.Header.Set("Range", "bytes=2-5")

	w := formattest.Do(h.RegisterRepoRoutes, req)
	if w.Code != http.StatusPartialContent || w.Body.String() != "2345" {
		t.Fatalf("range = %d %q", w.Code, w.Body.String())
	}

	req = formattest.Request(http.MethodGet, target, nil)
	req.Header.Set("If-None-Match", `"`+oid+`"`)

	if w := formattest.Do(h.RegisterRepoRoutes, req); w.Code != http.StatusNotModified {
		t.Errorf("conditional get = %d, want 304", w.Code)
	}

	w = do(h, http.MethodHead, target, "")
	if w.Code != http.StatusOK || w.Header().Get("Content-Length") != "10" || w.Body.Len() != 0 {
		t.Errorf("head = %d length %q body %d", w.Code, w.Header().Get("Content-Length"), w.Body.Len())
	}

	if n := db.downloads["file-"+oid[:8]]; n != 1 {
		t.Errorf("a download counts once: only the full GET, not the range, 304 or HEAD; got %d", n)
	}
}

func TestGetUnknownOrInvalid(t *testing.T) {
	h := newRig(t).h

	for _, oid := range []string{oidOf("nothing"), "short", strings.ToUpper(oidOf("x")), "../x"} {
		if w := do(h, http.MethodGet, "/repos/myrepo/objects/"+oid, ""); w.Code != http.StatusNotFound {
			t.Errorf("GET %q = %d, want 404", oid, w.Code)
		}
	}
}

func TestGetQuarantine(t *testing.T) {
	h, db, _, _ := newTestHandler(t)

	const content = "quarantine me"

	store(t, h, content)

	oid := oidOf(content)
	db.Keys[oid] = true

	if w := do(h, http.MethodGet, "/repos/myrepo/objects/"+oid, ""); w.Code != http.StatusForbidden {
		t.Fatalf("get = %d, want 403", w.Code)
	}

	w, resp := batch(t, h, `{"operation":"download","objects":[`+pointer(content)+`]}`)
	if w.Code != http.StatusOK || len(resp.Objects) != 1 || resp.Objects[0].Error == nil ||
		resp.Objects[0].Error.Code != http.StatusForbidden || len(resp.Objects[0].Actions) != 0 {
		t.Fatalf("batch of a quarantined object = %d %s", w.Code, w.Body.String())
	}
}

func TestPutRefusesWhatIsNotTheObject(t *testing.T) {
	check := formattest.IsolatedTemp(t)
	h, db, bs, pub := newTestHandler(t)

	const content = "the real content"

	oid := oidOf(content)

	if w := do(h, http.MethodPut, "/repos/myrepo/objects/"+oid, "other content"); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("body that does not hash to the oid = %d, want 422", w.Code)
	}

	req := formattest.Request(http.MethodPut, "/repos/myrepo/objects/"+oid, strings.NewReader(content))
	req.ContentLength = int64(len(content)) + 4

	if w := formattest.Do(h.RegisterRepoRoutes, req); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("body shorter than declared = %d, want 422", w.Code)
	}

	if w := do(h, http.MethodPut, "/repos/myrepo/objects/not-an-oid", content); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("invalid oid = %d, want 422", w.Code)
	}

	if len(bs.Blobs) != 0 || len(db.objects) != 0 || db.used != 0 || db.artifacts != 0 || len(pub.Events()) != 0 {
		t.Errorf("a refused upload left state: blobs=%d objects=%d used=%d events=%d",
			len(bs.Blobs), len(db.objects), db.used, len(pub.Events()))
	}

	check()
}

func TestPutQuotaExceededStoresNothing(t *testing.T) {
	h, db, bs, _ := newTestHandler(t)
	db.limit = 4

	if w := put(h, "too big for the quota"); w.Code != http.StatusInsufficientStorage {
		t.Fatalf("status = %d, want 507", w.Code)
	}

	if len(bs.Blobs) != 0 || len(db.objects) != 0 || db.used != 0 {
		t.Errorf("state after refusal: blobs=%d objects=%d used=%d", len(bs.Blobs), len(db.objects), db.used)
	}
}

// A concurrent upload of the same object wins the row: this upload still
// succeeds, and the quota it reserved is given back.
func TestPutLosingTheRaceIsSuccess(t *testing.T) {
	h, db, _, pub := newTestHandler(t)
	db.raceOnPut = true

	if w := put(h, "raced"); w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	if db.used != 0 || db.artifacts != 0 {
		t.Errorf("quota not returned: used=%d artifacts=%d", db.used, db.artifacts)
	}

	if len(pub.Events()) != 0 {
		t.Errorf("the loser published %d events", len(pub.Events()))
	}
}

func TestUnknownRepositoryAndOtherFormats(t *testing.T) {
	h, db, _, _ := newTestHandler(t)

	for _, target := range []string{"/repos/nope/objects/batch", "/repos/nope/objects/" + oidOf("x"), "/repos/nope/verify"} {
		method := http.MethodPost
		if strings.Contains(target, "/objects/"+oidOf("x")) {
			method = http.MethodGet
		}

		if w := do(h, method, target, `{"operation":"download"}`); w.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404", method, target, w.Code)
		}
	}

	db.Repo.Format = "raw"

	if w := do(h, http.MethodPost, "/repos/myrepo/objects/batch", `{"operation":"download","objects":[]}`); w.Code != http.StatusNotFound {
		t.Errorf("repository of another format = %d, want 404", w.Code)
	}
}

func TestBatchDownload(t *testing.T) {
	h := newRig(t).h

	const have = "a stored object"

	store(t, h, have)

	missing := oidOf("never uploaded")
	body := `{"operation":"download","transfers":["basic"],"hash_algo":"sha256","objects":[` +
		pointer(have) + `,{"oid":"` + missing + `","size":5},{"oid":"zz","size":1},{"oid":"` + oidOf("x") + `","size":-1},` +
		`{"oid":"` + oidOf(have) + `","size":999}]}`

	w, resp := batch(t, h, body)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}

	if ct := w.Header().Get("Content-Type"); ct != gitlfs.MediaType {
		t.Errorf("Content-Type = %q", ct)
	}

	if resp.Transfer != "basic" || len(resp.Objects) != 5 {
		t.Fatalf("response = %+v", resp)
	}

	got := resp.Objects[0]

	href := "http://localhost/repos/gitlfs/myrepo/objects/" + oidOf(have)
	if got.OID != oidOf(have) || got.Size != int64(len(have)) || got.Error != nil || got.Actions[gitlfs.ActionDownload].Href != href {
		t.Errorf("stored object = %+v", got)
	}

	for i, want := range []int{0, http.StatusNotFound, http.StatusUnprocessableEntity, http.StatusUnprocessableEntity, http.StatusUnprocessableEntity} {
		o := resp.Objects[i]
		if want == 0 {
			continue
		}

		if o.Error == nil || o.Error.Code != want || len(o.Actions) != 0 {
			t.Errorf("object %d = %+v, want error %d without actions", i, o, want)
		}
	}

	// The actions the batch hands out are the routes the handler serves (the
	// test router mounts it at /repos/{name}, the server at /repos/gitlfs/{name}).
	followed := strings.Replace(got.Actions[gitlfs.ActionDownload].Href, "http://localhost/repos/gitlfs/", "/repos/", 1)
	if w := do(h, http.MethodGet, followed, ""); w.Code != http.StatusOK ||
		w.Body.String() != have {
		t.Errorf("following the download action = %d %q", w.Code, w.Body.String())
	}
}

func TestBatchUpload(t *testing.T) {
	h := newRig(t).h

	const have, fresh = "already stored", "brand new"

	store(t, h, have)

	w, resp := batch(t, h, `{"operation":"upload","transfers":["basic"],"objects":[`+pointer(have)+`,`+pointer(fresh)+
		`,{"oid":"`+oidOf(have)+`","size":1}]}`)
	if w.Code != http.StatusOK || len(resp.Objects) != 3 {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}

	if o := resp.Objects[0]; o.Error != nil || len(o.Actions) != 0 {
		t.Errorf("an object the repository holds needs no actions, got %+v", o)
	}

	o := resp.Objects[1]
	if o.Error != nil || o.Actions[gitlfs.ActionUpload].Href != "http://localhost/repos/gitlfs/myrepo/objects/"+oidOf(fresh) ||
		o.Actions[gitlfs.ActionVerify].Href != "http://localhost/repos/gitlfs/myrepo/verify" || len(o.Actions) != 2 {
		t.Errorf("new object = %+v", o)
	}

	if o := resp.Objects[2]; o.Error == nil || o.Error.Code != http.StatusUnprocessableEntity {
		t.Errorf("stored object with another size = %+v, want 422", o)
	}

	// Following the actions publishes the object.
	if w := do(h, http.MethodPut, "/repos/myrepo/objects/"+oidOf(fresh), fresh); w.Code != http.StatusOK {
		t.Fatalf("upload action = %d", w.Code)
	}

	if w := do(h, http.MethodPost, "/repos/myrepo/verify", pointer(fresh)); w.Code != http.StatusOK {
		t.Errorf("verify action = %d: %s", w.Code, w.Body.String())
	}
}

func TestBatchRefusals(t *testing.T) {
	h := newRig(t).h

	tests := []struct {
		name string
		body string
		want int
	}{
		{"not JSON", `nope`, http.StatusBadRequest},
		{"no operation", `{"objects":[]}`, http.StatusUnprocessableEntity},
		{"unknown operation", `{"operation":"delete","objects":[]}`, http.StatusUnprocessableEntity},
		{"no basic transfer", `{"operation":"download","transfers":["tus"],"objects":[]}`, http.StatusUnprocessableEntity},
		{"other hash", `{"operation":"download","hash_algo":"sha512","objects":[]}`, http.StatusConflict},
		{
			"too large",
			`{"operation":"download","objects":[]}` + strings.Repeat(" ", gitlfs.MaxBatchBytes),
			http.StatusRequestEntityTooLarge,
		},
		{"no objects", `{"operation":"download","objects":[]}`, http.StatusOK},
		{"transfers omitted means basic", `{"operation":"upload","objects":[` + pointer("y") + `]}`, http.StatusOK},
	}

	for _, tc := range tests {
		w, _ := batch(t, h, tc.body)
		if w.Code != tc.want {
			t.Errorf("%s: status = %d, want %d: %s", tc.name, w.Code, tc.want, w.Body.String())
		}

		if tc.want != http.StatusOK {
			var e gitlfs.Error
			if err := json.Unmarshal(w.Body.Bytes(), &e); err != nil || e.Message == "" {
				t.Errorf("%s: error body %q is not a protocol error document", tc.name, w.Body.String())
			}
		}
	}
}

func TestVerify(t *testing.T) {
	h := newRig(t).h

	const content = "verify me"

	store(t, h, content)

	tests := []struct {
		name string
		body string
		want int
	}{
		{"stored", pointer(content), http.StatusOK},
		{"wrong size", fmt.Sprintf(`{"oid":%q,"size":1}`, oidOf(content)), http.StatusUnprocessableEntity},
		{"missing", pointer("absent"), http.StatusNotFound},
		{"bad oid", `{"oid":"x","size":1}`, http.StatusUnprocessableEntity},
		{"not JSON", `x`, http.StatusUnprocessableEntity},
		{"too large", pointer(content) + strings.Repeat(" ", maxVerifyBytes), http.StatusRequestEntityTooLarge},
	}

	for _, tc := range tests {
		if w := do(h, http.MethodPost, "/repos/myrepo/verify", tc.body); w.Code != tc.want {
			t.Errorf("%s: status = %d, want %d: %s", tc.name, w.Code, tc.want, w.Body.String())
		}
	}
}

func TestLocksAreNotImplemented(t *testing.T) {
	h := newRig(t).h

	for _, rt := range []struct{ method, path string }{
		{http.MethodPost, "/repos/myrepo/locks"},
		{http.MethodGet, "/repos/myrepo/locks"},
		{http.MethodPost, "/repos/myrepo/locks/verify"},
		{http.MethodPost, "/repos/myrepo/locks/abc/unlock"},
	} {
		w := do(h, rt.method, rt.path, `{}`)
		if w.Code != http.StatusNotImplemented {
			t.Errorf("%s %s = %d, want 501", rt.method, rt.path, w.Code)
		}

		var e gitlfs.Error
		if err := json.Unmarshal(w.Body.Bytes(), &e); err != nil || e.Message == "" || w.Header().Get("Content-Type") != gitlfs.MediaType {
			t.Errorf(
				"%s %s answered %q (%s), want a protocol error document",
				rt.method,
				rt.path,
				w.Body.String(),
				w.Header().Get("Content-Type"),
			)
		}
	}
}
