// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package raw

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
	"github.com/openartifact/openartifact/internal/webhook"
)

type rawDB struct {
	*formattest.DB

	art *models.RawArtifact
}

func (d *rawDB) UpsertRawArtifact(_ context.Context, a *models.RawArtifact) error {
	d.art = a
	return nil
}

func (d *rawDB) GetRawArtifact(_ context.Context, _, _ string) (*models.RawArtifact, error) {
	return d.art, nil
}

func (d *rawDB) DeleteRawArtifact(_ context.Context, _, _ string) error {
	d.art = nil
	return nil
}

func newTestHandler(t *testing.T) (*HTTPHandler, *rawDB, *formattest.BlobStore, *formattest.Publisher) {
	t.Helper()

	db := &rawDB{DB: formattest.NewDB()}
	bs := formattest.NewBlobStore()
	pub := &formattest.Publisher{}
	h := NewHTTPHandler(formattest.NewLogger(t), db, bs, "http://localhost")
	h.SetPublisher(pub)

	return h, db, bs, pub
}

func TestPutAndDeleteEmitEvents(t *testing.T) {
	h, db, bs, pub := newTestHandler(t)

	w := formattest.Do(h.RegisterRepoRoutes, formattest.Request(http.MethodPut, "/repos/myrepo/dir/sub/file.txt", strings.NewReader("hello")))
	if w.Code != http.StatusCreated {
		t.Fatalf("put status = %d: %s", w.Code, w.Body.String())
	}

	if db.art == nil || db.art.Path != "dir/sub/file.txt" || db.art.SizeBytes != 5 {
		t.Fatalf("artifact = %+v", db.art)
	}

	if got := string(bs.Blobs[db.art.SHA256]); got != "hello" {
		t.Fatalf("blob = %q", got)
	}

	evs := pub.Events()
	if len(evs) != 1 || evs[0].Type != webhook.EventArtifactUploaded || evs[0].RepoID != "repo-1" {
		t.Fatalf("events = %+v", evs)
	}

	if a := evs[0].Artifact(); a["path"] != "dir/sub/file.txt" || a["sha256"] != db.art.SHA256 {
		t.Errorf("artifact = %v", a)
	}

	if w = formattest.Do(h.RegisterRepoRoutes, formattest.Request(http.MethodDelete, "/repos/myrepo/dir/sub/file.txt", nil)); w.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d", w.Code)
	}

	evs = pub.Events()
	if len(evs) != 2 || evs[1].Type != webhook.EventArtifactDeleted {
		t.Fatalf("events after delete = %+v", evs)
	}

	if len(bs.Blobs) != 0 {
		t.Error("blob not removed after delete")
	}
}

func TestGetQuarantine(t *testing.T) {
	h, db, bs, _ := newTestHandler(t)
	db.art = &models.RawArtifact{Path: "a/b.txt", BlobKey: "abc", SHA256: "abc", SizeBytes: 3, ContentType: "text/plain"}
	bs.Blobs["abc"] = []byte("xyz")

	get := func() int {
		return formattest.Do(h.RegisterRepoRoutes, formattest.Request(http.MethodGet, "/repos/myrepo/a/b.txt", nil)).Code
	}

	if c := get(); c != http.StatusOK {
		t.Fatalf("status = %d, want 200", c)
	}

	db.Keys["a/b.txt"] = true

	if c := get(); c != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", c)
	}
}
